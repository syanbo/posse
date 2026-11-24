package app

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/rpc"
	"posse/internal/tslink"
)

func waitUntil(t *testing.T, a *app, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		a.runPending()
	}
	if !cond() {
		t.Fatal(msg)
	}
}

func TestTailscaleCardRetriesAndOldDaemon(t *testing.T) {
	var calls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if calls.Add(1) == 1 {
				return nil, rpc.Errorf(rpc.CodeRestarting, "restarting")
			}
			return tslink.Status{Enabled: false, State: tslink.StateDisabled}, nil
		},
	})
	a := &app{c: c}
	tt := newTester(func(c *ui.Context) { a.mobileTailscaleCard(c) }, 720, 480)
	a.loadTailscale()
	ts := &a.mobile.tailscale
	waitUntil(t, a, func() bool { return !ts.loading }, "initial tailscale.status did not finish")
	if ts.loaded || ts.old {
		t.Fatalf("after transient error: loaded=%v old=%v", ts.loaded, ts.old)
	}

	ts.loadedAt = time.Now().Add(-tailscaleRetry)
	tt.Frame()
	waitUntil(t, a, func() bool { return ts.loaded }, "tailscale.status not retried")

	// 旧版常驻进程：返回 CodeUnknown 后不再重试
	_, old := startFakeDaemon(t)
	b := &app{c: old}
	b.loadTailscale()
	waitUntil(t, b, func() bool { return !b.mobile.tailscale.loading }, "old daemon probe did not finish")
	if !b.mobile.tailscale.old {
		t.Fatal("unknown method not marked as old daemon")
	}
}

func TestTailscaleToggleAndPairRefresh(t *testing.T) {
	var setCalled atomic.Int32
	var pairCalled atomic.Int32
	var gotEnabled atomic.Bool
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Enabled *bool `json:"enabled"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			if p.Enabled != nil {
				gotEnabled.Store(*p.Enabled)
			}
			setCalled.Add(1)
			return tslink.Status{
				Enabled:  true,
				State:    tslink.StateRunning,
				IP:       "100.64.0.9",
				Hostname: "posse-mac",
				DNSName:  "posse-mac.tail.ts.net",
			}, nil
		},
		"mobile.pair": func(ctx context.Context, raw json.RawMessage) (any, error) {
			pairCalled.Add(1)
			return map[string]any{
				"url":      "orca://pair?code=abc",
				"code":     "abc",
				"endpoint": "ws://100.64.0.9:6789",
			}, nil
		},
		"mobile.devices": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return []mobileDevice{}, nil
		},
	})

	a := &app{c: c}
	now := time.Now()
	a.mobile = mobileView{
		shown:    true,
		loadedAt: now,
		tailscale: tailscaleView{
			loaded:   true,
			loadedAt: now,
			st:       tslink.Status{Enabled: false, State: tslink.StateDisabled},
		},
	}

	m := appL()
	a.toggleTailscale(m, true)
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.busy }, "tailscale.set did not finish")
	if setCalled.Load() != 1 || !gotEnabled.Load() {
		t.Fatalf("setCalled=%d enabled=%v", setCalled.Load(), gotEnabled.Load())
	}
	if a.mobile.tailscale.runningIP() != "100.64.0.9" {
		t.Fatalf("runningIP = %q", a.mobile.tailscale.runningIP())
	}
	waitUntil(t, a, func() bool { return pairCalled.Load() >= 1 && !a.mobile.loading }, "mobile.pair not refreshed on TS online")

	// setMobileAddress 不把内置 Tailscale IP 存进自定义地址列表，且记住来源为内置节点
	a.setMobileAddress("100.64.0.9", true)
	if slices.Contains(a.state.MobileAddresses, "100.64.0.9") || !a.state.MobileAddressBuiltIn {
		t.Fatalf("built-in TS IP state mismatch: custom=%v builtIn=%v", a.state.MobileAddresses, a.state.MobileAddressBuiltIn)
	}
}

func TestTailscaleNeedsLoginOpenAndRefresh(t *testing.T) {
	var mu sync.Mutex
	var opened []string
	origOpen := openExternalURL
	openExternalURL = func(raw string) error {
		mu.Lock()
		opened = append(opened, raw)
		mu.Unlock()
		return nil
	}
	defer func() { openExternalURL = origOpen }()

	var loginCalls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.login": func(ctx context.Context, raw json.RawMessage) (any, error) {
			loginCalls.Add(1)
			return tslink.Status{
				Enabled: true,
				State:   tslink.StateNeedsLogin,
				AuthURL: "https://login.tailscale.com/a/fresh",
			}, nil
		},
	})

	a := &app{c: c}
	m := appL()

	// 非 http/https 链接必须被拒绝，不能调系统 open
	a.openTailscaleAuthURL(m, "file:///etc/passwd")
	if a.mobile.tailscale.err == "" {
		t.Fatal("expected error for file:// URL")
	}
	a.openTailscaleAuthURL(m, "-a /Applications/Calculator.app")
	if a.mobile.tailscale.err == "" {
		t.Fatal("expected error for flag-like URL")
	}
	a.openTailscaleAuthURL(m, "https://:443/a")
	if a.mobile.tailscale.err == "" {
		t.Fatal("expected error for empty hostname URL")
	}
	a.openTailscaleAuthURL(m, "https://user:pass@login.tailscale.com/a/test")
	if a.mobile.tailscale.err == "" {
		t.Fatal("expected error for URL with userinfo")
	}
	mu.Lock()
	if len(opened) != 0 {
		t.Fatalf("openExternalURL should not have been called, got %v", opened)
	}
	mu.Unlock()

	// 合法 https 链接正常打开
	a.openTailscaleAuthURL(m, "https://login.tailscale.com/a/test123")
	waitUntil(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(opened) == 1
	}, "openExternalURL was not called")
	mu.Lock()
	if opened[0] != "https://login.tailscale.com/a/test123" {
		t.Fatalf("opened = %q", opened[0])
	}
	mu.Unlock()

	// 刷新登录链接
	a.loginTailscale(m)
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.busy }, "tailscale.login did not finish")
	if loginCalls.Load() != 1 || a.mobile.tailscale.st.AuthURL != "https://login.tailscale.com/a/fresh" {
		t.Fatalf("loginCalls=%d authURL=%q", loginCalls.Load(), a.mobile.tailscale.st.AuthURL)
	}
}

func TestTailscaleAdvancedSaveAndDraftProtection(t *testing.T) {
	var mu sync.Mutex
	var lastParams map[string]any
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p map[string]any
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			mu.Lock()
			lastParams = p
			mu.Unlock()
			return tslink.Status{
				Enabled:              true,
				State:                tslink.StateStarting,
				Hostname:             "my-mac",
				ControlURL:           "https://hs.example.com",
				DisableBindInterface: true,
			}, nil
		},
	})

	a := &app{c: c}
	a.applyTailscaleStatus(tslink.Status{
		Enabled:  false,
		State:    tslink.StateDisabled,
		Hostname: "old-name",
	}, false)

	// 用户正在编辑草稿时，后台轮询不能覆盖未保存草稿
	ts := &a.mobile.tailscale
	ts.hostname = "my-mac"
	ts.controlURL = "https://hs.example.com"
	ts.disableBind = true
	ts.draftDirty = true
	ts.authKey = "tskey-auth-secret"

	a.applyTailscaleStatus(tslink.Status{
		Enabled:  false,
		State:    tslink.StateDisabled,
		Hostname: "old-name",
	}, false)
	if ts.hostname != "my-mac" || ts.controlURL != "https://hs.example.com" || !ts.disableBind {
		t.Fatalf("dirty draft was overwritten: %+v", ts)
	}

	// 保存：带 AuthKey 且原未开启时应一并传 enabled=true，成功后清空 authKey 和 draftDirty
	a.saveTailscaleAdvanced(appL())
	waitUntil(t, a, func() bool { return !ts.busy }, "saveTailscaleAdvanced did not finish")
	mu.Lock()
	got := lastParams
	mu.Unlock()
	if got["hostname"] != "my-mac" || got["controlUrl"] != "https://hs.example.com" ||
		got["authKey"] != "tskey-auth-secret" || got["disableBindInterface"] != true || got["enabled"] != true {
		t.Fatalf("unexpected set params: %+v", got)
	}
	if ts.authKey != "" || ts.draftDirty {
		t.Fatalf("expected authKey cleared and draftDirty=false, got authKey=%q dirty=%v", ts.authKey, ts.draftDirty)
	}
}

func TestTailscaleResetConfirmAndLocalOnlyWarning(t *testing.T) {
	var resetCalls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.reset": func(ctx context.Context, raw json.RawMessage) (any, error) {
			resetCalls.Add(1)
			return map[string]any{
				"status":       tslink.Status{Enabled: false, State: tslink.StateDisabled},
				"loggedOut":    false,
				"logoutError":  "control unreachable",
				"localCleared": true,
			}, nil
		},
	})

	a := &app{c: c}
	a.mobile.tailscale = tailscaleView{
		loaded:   true,
		loadedAt: time.Now(),
		st:       tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.2"},
		authKey:  "leftover",
	}

	a.resetTailscale(appL())
	if !a.confirming || a.confirmDo == nil {
		t.Fatal("resetTailscale did not open confirmation dialog")
	}
	go a.confirmDo()
	waitUntil(t, a, func() bool {
		ts := &a.mobile.tailscale
		return resetCalls.Load() == 1 && !ts.busy && !ts.st.Enabled && ts.st.State == tslink.StateDisabled && ts.err != ""
	}, "tailscale.reset did not finish")
	ts := &a.mobile.tailscale
	if ts.st.Enabled || ts.st.State != tslink.StateDisabled || ts.authKey != "" {
		t.Fatalf("unexpected state after reset: %+v", ts)
	}
	if ts.err == "" {
		t.Fatal("expected warning when logoutError is non-empty")
	}
}

func TestTailscaleStalePollIgnored(t *testing.T) {
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
			close(pollStarted)
			<-releasePoll
			return tslink.Status{Enabled: false, State: tslink.StateDisabled}, nil
		},
		"tailscale.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.8"}, nil
		},
	})

	a := &app{c: c}
	a.loadTailscale()
	<-pollStarted

	a.toggleTailscale(appL(), true)
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.busy }, "toggle did not finish")

	close(releasePoll)
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.loading }, "stale poll did not finish")
	if !a.mobile.tailscale.st.Enabled || a.mobile.tailscale.st.State != tslink.StateRunning {
		t.Fatalf("stale poll overwrote newer state: %+v", a.mobile.tailscale.st)
	}
}

func TestTailscaleSelectedAddressTracksIPAndResetsOnOffline(t *testing.T) {
	var mu sync.Mutex
	var pairAddrs []string
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"mobile.pair": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Address string `json:"address"`
			}
			_ = json.Unmarshal(raw, &p)
			mu.Lock()
			pairAddrs = append(pairAddrs, p.Address)
			mu.Unlock()
			return map[string]any{"url": "orca://pair?code=x", "code": "x", "endpoint": "ws://127.0.0.1:6789"}, nil
		},
		"mobile.devices": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return []mobileDevice{}, nil
		},
	})

	a := &app{c: c}
	a.state.MobileAddress = "100.64.0.9"
	a.state.MobileAddressBuiltIn = true
	a.mobile = mobileView{
		shown:  true,
		locals: []string{"192.168.1.5"},
		tailscale: tailscaleView{
			loaded: true,
			st:     tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.9"},
		},
	}

	// 内置 IP 变化时，已选中的内置地址同步更新并触发刷新
	a.applyTailscaleStatus(tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.22"}, false)
	if a.state.MobileAddress != "100.64.0.22" {
		t.Fatalf("MobileAddress after IP change = %q, want 100.64.0.22", a.state.MobileAddress)
	}
	waitUntil(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pairAddrs) == 1 && !a.mobile.loading
	}, "mobile.pair not called on IP change")
	mu.Lock()
	if pairAddrs[0] != "100.64.0.22" {
		mu.Unlock()
		t.Fatalf("pair addr on IP change = %q, want 100.64.0.22", pairAddrs[0])
	}
	mu.Unlock()

	// 内置节点下线时，已选中的内置地址回退到自动（""），而不是卡在失效 IP
	a.applyTailscaleStatus(tslink.Status{Enabled: false, State: tslink.StateDisabled}, false)
	if a.state.MobileAddress != "" {
		t.Fatalf("MobileAddress after offline = %q, want empty (auto)", a.state.MobileAddress)
	}
	waitUntil(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pairAddrs) == 2 && !a.mobile.loading
	}, "mobile.pair not called on offline")
	mu.Lock()
	if pairAddrs[1] != "" {
		mu.Unlock()
		t.Fatalf("pair addr on offline = %q, want empty", pairAddrs[1])
	}
	mu.Unlock()

	// 若用户手工输入与内置 IP 相同的自定义地址（MobileAddressBuiltIn=false），轮询和下线绝不改写它
	a.state.MobileAddress = "100.64.0.33"
	a.state.MobileAddressBuiltIn = false
	a.mobile.tailscale.st = tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.33"}
	a.applyTailscaleStatus(tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.33"}, false)
	if a.state.MobileAddressBuiltIn || a.state.MobileAddress != "100.64.0.33" {
		t.Fatalf("manual address matching built-in IP was reclassified: addr=%q builtIn=%v", a.state.MobileAddress, a.state.MobileAddressBuiltIn)
	}
	a.applyTailscaleStatus(tslink.Status{Enabled: false, State: tslink.StateDisabled}, false)
	if a.state.MobileAddress != "100.64.0.33" {
		t.Fatalf("manual address was cleared on built-in offline: %q", a.state.MobileAddress)
	}
}

func TestTailscaleDefersPairRefreshWhilePairInFlight(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	var pairAddrs []string
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"mobile.pair": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Address string `json:"address"`
			}
			_ = json.Unmarshal(raw, &p)
			mu.Lock()
			pairAddrs = append(pairAddrs, p.Address)
			n := len(pairAddrs)
			mu.Unlock()
			if n == 1 {
				close(firstStarted)
				<-releaseFirst
			}
			return map[string]any{"url": "orca://pair?code=x", "code": "x", "endpoint": "ws://127.0.0.1:6789"}, nil
		},
		"mobile.devices": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return []mobileDevice{}, nil
		},
	})

	a := &app{c: c}
	a.state.MobileAddress = "100.64.0.9"
	a.state.MobileAddressBuiltIn = true
	a.mobile = mobileView{
		shown: true,
		tailscale: tailscaleView{
			loaded: true,
			st:     tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.9"},
		},
	}

	// 手动取码（poll=false）在途时内置节点下线：旧回包不得写回 100.64.0.9 或存进自定义列表，
	// 补刷必须使用回退后的自动地址（""）。
	a.startMobile(true, "100.64.0.9", true, false)
	<-firstStarted

	a.applyTailscaleStatus(tslink.Status{Enabled: false, State: tslink.StateDisabled}, false)
	if a.state.MobileAddress != "" || a.state.MobileAddressBuiltIn || !a.mobile.pairRefreshPending {
		t.Fatalf("expected auto fallback & pending refresh: addr=%q builtIn=%v pending=%v",
			a.state.MobileAddress, a.state.MobileAddressBuiltIn, a.mobile.pairRefreshPending)
	}

	close(releaseFirst)
	waitUntil(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pairAddrs) == 2 && !a.mobile.loading && !a.mobile.pairRefreshPending
	}, "deferred mobile.pair refresh did not run")

	mu.Lock()
	secondAddr := pairAddrs[1]
	mu.Unlock()
	if secondAddr != "" {
		t.Fatalf("second pair addr = %q, want empty (auto)", secondAddr)
	}
	if a.state.MobileAddress != "" || a.state.MobileAddressBuiltIn || slices.Contains(a.state.MobileAddresses, "100.64.0.9") {
		t.Fatalf("stale built-in IP leaked into state: addr=%q builtIn=%v custom=%v",
			a.state.MobileAddress, a.state.MobileAddressBuiltIn, a.state.MobileAddresses)
	}
}

func TestTailscaleColdRestartBuiltInAddressFallbackAndUpgradeRetry(t *testing.T) {
	var mu sync.Mutex
	var pairAddrs []string
	var statusCalls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"mobile.pair": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Address string `json:"address"`
			}
			_ = json.Unmarshal(raw, &p)
			mu.Lock()
			pairAddrs = append(pairAddrs, p.Address)
			mu.Unlock()
			return map[string]any{"url": "orca://pair?code=x", "code": "x", "endpoint": "ws://127.0.0.1:6789"}, nil
		},
		"mobile.devices": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return []mobileDevice{}, nil
		},
		"tailscale.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
			if statusCalls.Add(1) == 1 {
				return nil, rpc.Errorf(rpc.CodeRestarting, "restarting")
			}
			return tslink.Status{Enabled: false, State: tslink.StateDisabled}, nil
		},
		"tailscale.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method")
		},
	})

	// 场景 1：窗口冷启动，uiState 持久化了上次选中的内置 Tailscale 地址，但 tailscaleView 尚未 loaded；
	// 首个状态回报已关闭时，必须回退到自动并刷新配对码。
	a := &app{c: c}
	a.state.MobileAddress = "100.64.0.77"
	a.state.MobileAddressBuiltIn = true
	a.mobile = mobileView{shown: true}
	a.applyTailscaleStatus(tslink.Status{Enabled: false, State: tslink.StateDisabled}, false)
	if a.state.MobileAddress != "" || a.state.MobileAddressBuiltIn {
		t.Fatalf("cold restart did not clear dead built-in IP: addr=%q builtIn=%v", a.state.MobileAddress, a.state.MobileAddressBuiltIn)
	}
	waitUntil(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pairAddrs) == 1 && !a.mobile.loading
	}, "mobile.pair not refreshed after cold-restart fallback")
	mu.Lock()
	if pairAddrs[0] != "" {
		mu.Unlock()
		t.Fatalf("cold-restart pair addr = %q, want empty", pairAddrs[0])
	}
	mu.Unlock()

	// 场景 2：操作 RPC 先收到 CodeUnknown 时立刻隐藏卡片并置 old=true；
	// 后续重新探测遇到瞬态错误（正在升级）时解除 old=true，允许继续重试。
	a.mobile.tailscale.loaded = true
	a.mobile.tailscale.old = false
	a.toggleTailscale(appL(), true)
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.busy }, "toggle did not finish")
	if !a.mobile.tailscale.old || a.mobile.tailscale.loaded {
		t.Fatalf("CodeUnknown on action did not hide card: old=%v loaded=%v", a.mobile.tailscale.old, a.mobile.tailscale.loaded)
	}

	a.loadTailscale()
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.loading }, "transient re-probe did not finish")
	if a.mobile.tailscale.old {
		t.Fatal("transient error during re-probe should clear old flag to allow retry")
	}
}

func TestTailscaleOldDowngradeInvalidatesInFlightPairAndShowsFallbackError(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var mu sync.Mutex
	var pairAddrs []string
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"mobile.pair": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Address string `json:"address"`
			}
			_ = json.Unmarshal(raw, &p)
			mu.Lock()
			pairAddrs = append(pairAddrs, p.Address)
			n := len(pairAddrs)
			mu.Unlock()
			if n == 1 {
				close(firstStarted)
				<-releaseFirst
				return map[string]any{"url": "orca://pair?code=stale", "code": "stale", "endpoint": "ws://100.64.0.9:6789"}, nil
			}
			// 模拟回退到自动后无可用局域网/Tailscale 地址
			return nil, rpc.Errorf(rpc.CodeInternal, "no LAN or Tailscale address found; connect to a network first")
		},
		"mobile.devices": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return []mobileDevice{}, nil
		},
		"tailscale.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method")
		},
	})

	a := &app{c: c}
	a.state.MobileAddress = "100.64.0.9"
	a.state.MobileAddressBuiltIn = true
	a.mobile = mobileView{
		shown:    true,
		url:      "orca://pair?code=old",
		code:     "old",
		endpoint: "ws://100.64.0.9:6789",
		tailscale: tailscaleView{
			loaded: true,
			st:     tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.9"},
		},
	}

	// 手动取码在途时收到旧版 daemon 降级（CodeUnknown）：
	// 1) runningIP() 必须立刻清空（下拉不再出现失效 IP）；
	// 2) 在途回包不得把 100.64.0.9 写回配置；
	// 3) 补刷以自动地址（""）请求，若无可用地址必须清空旧码并展示错误信息。
	a.startMobile(true, "100.64.0.9", true, false)
	<-firstStarted

	a.loadTailscale()
	waitUntil(t, a, func() bool { return a.mobile.tailscale.old }, "markTailscaleOld did not run")
	if got := a.mobile.tailscale.runningIP(); got != "" {
		t.Fatalf("runningIP() after downgrade = %q, want empty", got)
	}
	if a.state.MobileAddress != "" || a.state.MobileAddressBuiltIn {
		t.Fatalf("selected built-in address not cleared on downgrade: addr=%q builtIn=%v", a.state.MobileAddress, a.state.MobileAddressBuiltIn)
	}

	close(releaseFirst)
	waitUntil(t, a, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(pairAddrs) == 2 && !a.mobile.loading && !a.mobile.pairRefreshPending
	}, "fallback pair refresh after downgrade did not finish")

	mu.Lock()
	secondAddr := pairAddrs[1]
	mu.Unlock()
	if secondAddr != "" {
		t.Fatalf("fallback pair addr = %q, want empty", secondAddr)
	}
	if a.state.MobileAddress != "" || a.state.MobileAddressBuiltIn || slices.Contains(a.state.MobileAddresses, "100.64.0.9") {
		t.Fatalf("stale built-in IP restored after downgrade: addr=%q builtIn=%v custom=%v",
			a.state.MobileAddress, a.state.MobileAddressBuiltIn, a.state.MobileAddresses)
	}
	if a.mobile.code != "" || a.mobile.endpoint != "" || a.mobile.url != "" {
		t.Fatalf("stale pair code not cleared when fallback fails: code=%q endpoint=%q", a.mobile.code, a.mobile.endpoint)
	}
	if a.mobile.err == "" {
		t.Fatal("expected fallback auto-pair error to be surfaced to user")
	}
}

func TestTailscaleDisabledSaveAndConcurrentDraftEdit(t *testing.T) {
	saveStarted := make(chan struct{})
	releaseSave := make(chan struct{})
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.set": func(ctx context.Context, raw json.RawMessage) (any, error) {
			close(saveStarted)
			<-releaseSave
			return tslink.Status{
				Enabled:              false,
				State:                tslink.StateDisabled,
				Hostname:             "saved-host",
				ControlURL:           "https://hs1.example.test",
				DisableBindInterface: true,
			}, nil
		},
	})

	a := &app{c: c}
	a.applyTailscaleStatus(tslink.Status{Enabled: false, State: tslink.StateDisabled}, false)
	ts := &a.mobile.tailscale
	ts.hostname = "saved-host"
	ts.controlURL = "https://hs1.example.test"
	ts.disableBind = true
	ts.draftDirty = true
	ts.draftSeq = 1

	a.saveTailscaleAdvanced(appL())
	<-saveStarted

	// 保存期间若又有新的编辑（draftSeq 递增），保存回包不得覆盖新编辑或清空新输入的 AuthKey
	ts.hostname = "newer-edit"
	ts.authKey = "tskey-new"
	ts.draftDirty = true
	ts.draftSeq++

	close(releaseSave)
	waitUntil(t, a, func() bool { return !ts.busy }, "saveTailscaleAdvanced did not finish")
	if ts.hostname != "newer-edit" || ts.authKey != "tskey-new" || !ts.draftDirty {
		t.Fatalf("in-flight edit was clobbered: host=%q auth=%q dirty=%v", ts.hostname, ts.authKey, ts.draftDirty)
	}
}

func TestTailscaleLoadBlockedDuringBusyAndOldDowngradeAfterLoaded(t *testing.T) {
	var statusCalls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{
		"tailscale.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
			statusCalls.Add(1)
			return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method")
		},
	})

	a := &app{c: c}
	a.mobile.tailscale = tailscaleView{
		loaded: true,
		busy:   true,
		st:     tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.9"},
	}

	// busy 期间重新进入页面调用 loadTailscale 不得发出并发查询
	a.loadTailscale()
	if a.mobile.tailscale.loading || statusCalls.Load() != 0 {
		t.Fatalf("loadTailscale should be skipped while busy: loading=%v calls=%d", a.mobile.tailscale.loading, statusCalls.Load())
	}

	// 已 loaded 后若常驻进程降级返回 CodeUnknown，应标记 old 并隐藏卡片、停止轮询
	a.mobile.tailscale.busy = false
	a.loadTailscale()
	waitUntil(t, a, func() bool { return !a.mobile.tailscale.loading }, "downgrade probe did not finish")
	if !a.mobile.tailscale.old || a.mobile.tailscale.loaded {
		t.Fatalf("expected old=true loaded=false after CodeUnknown, got old=%v loaded=%v", a.mobile.tailscale.old, a.mobile.tailscale.loaded)
	}

	tt := newTester(func(c *ui.Context) { a.mobileTailscaleCard(c) }, 720, 480)
	a.mobile.tailscale.loadedAt = time.Now().Add(-time.Hour)
	tt.Frame()
	if statusCalls.Load() != 1 {
		t.Fatalf("old daemon should not be polled again, calls=%d", statusCalls.Load())
	}
}
