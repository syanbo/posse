package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/remote"
	"posse/internal/rpc"
	"posse/locales"
)

func TestHostDetailsOverviewTabsAndLegacyFallback(t *testing.T) {
	_, a, b := hostViewsFixture(t)
	h := remoteTestHost("remote")
	h.Name = "bwh"
	h.Target = "surgeadmin@bwh.example"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	// 先测试非活动主机：展示内存快照和「切换到此主机」按钮，不发起实时拉取。
	b.remoteInfo = &rpc.RemoteInfo{
		Version:  "0.1.54",
		Build:    "6f4d47b 2026-11-27T12:00:00Z",
		Edition:  "headless",
		Managed:  true,
		Platform: "linux",
		Arch:     "amd64",
		Home:     "/home/surgeadmin/.posse",
	}
	a.openHostDetails(h)
	m := locales.In("zh-Hans")
	tt := newTester(func(c *ui.Context) { a.hostDialog(c) }, 760, 900)
	settle(tt)
	if !tt.HasText(m.HostDialogDetailsTitle("bwh")) || !tt.HasText(m.HostSwitchHere()) || !tt.HasText("0.1.54 (6f4d47b)") {
		t.Fatalf("inactive host overview missing expected content: %v", tt.Texts())
	}

	// 切到活动且已连接状态：展示资源占用明细、无千分位 PID 和 systemd linger 提示。
	a.views.activate(h.ID)
	b.host.current = hostSnapshot{HostID: h.ID, Connected: true, Controlled: true, Client: &client.Client{Remote: true, Platform: "linux"}}
	b.daemon = daemonStatus{
		Version:     "0.1.54",
		Build:       "6f4d47b 2026-11-27T12:00:00Z",
		Edition:     "headless",
		Pid:         4242,
		Home:        "/home/surgeadmin/.posse",
		StartedAt:   time.Now().Add(-3 * time.Hour),
		Terminals:   2,
		Running:     1,
		FootprintKB: 43 * 1024,
	}
	a.hostDlg.detail.liveKey = detailLiveKey(h.ID, b)
	a.hostDlg.detail.info = &rpc.RemoteInfo{
		Version:  "0.1.54",
		Build:    "6f4d47b 2026-11-27T12:00:00Z",
		Edition:  "headless",
		Managed:  true,
		Platform: "linux",
		Arch:     "amd64",
		Home:     "/home/surgeadmin/.posse",
		Features: []string{"autostart.v1", "memory.v1"},
	}
	a.hostDlg.detail.mem = &rpc.DaemonMemoryResult{
		TotalKB: 120 * 1024,
		Workspaces: []rpc.DaemonMemoryWorkspace{{
			ID: "w1", Label: "ai/main", KB: 120 * 1024,
			Terminals: []rpc.DaemonMemoryTerminal{{
				ID: "t1", Title: "bash", KB: 120 * 1024,
				Procs: []rpc.DaemonMemoryProc{{Name: "bash", KB: 4 * 1024, Count: 1}},
			}},
		}},
	}
	a.hostDlg.detail.autostart = &rpc.AutostartStatus{
		Supported:          true,
		Kind:               "systemd",
		Managed:            true,
		Writable:           true,
		Installed:          true,
		Enabled:            true,
		Active:             true,
		Path:               "/home/surgeadmin/.config/systemd/user/posse-server.service",
		ExpectedExecutable: "/home/surgeadmin/.posse/server/current/posse",
		Linger:             "no",
		NoteCode:           rpc.AutostartNoteSystemdLingerDisabled,
	}
	settle(tt)
	for _, want := range []string{
		m.HostEditionHeadlessManaged(),
		m.HostProcessPid("4242"),
		"43 MB",
		"120 MB",
		"ai/main",
		m.HostAutostartTimerActive(),
		m.HostAutostartNoteLinger(),
	} {
		if !tt.HasText(want) {
			t.Fatalf("connected overview missing %q in %v", want, tt.Texts())
		}
	}

	// 切换到连接设置页签并修改名称，切回概览页时应保留未保存修改并在页签上显示标记。
	a.hostDlg.tab = hostTabSettings
	a.hostDlg.name = "bwh-renamed"
	if !a.hostDialogDirty(&a.hostDlg) {
		t.Fatal("expected modified connection settings to be marked dirty")
	}
	a.hostDlg.tab = hostTabOverview
	a.hostDlg.detail.liveKey = detailLiveKey(h.ID, b)
	settle(tt)
	if !tt.HasText(m.HostTabSettings() + " *") {
		t.Fatalf("dirty settings tab marker missing: %v", tt.Texts())
	}
}

func startHostDetailsMockRPC(t *testing.T, handlers map[string]rpc.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hd-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	if h, ok := handlers["desktop.operation"]; ok {
		srv.Handle("desktop.operation", h)
	}
	srv.Handle("desktop.call", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var call rpc.DesktopCall
		if err := rpc.Decode(raw, &call); err != nil {
			return nil, err
		}
		h, ok := handlers[call.Method]
		if !ok {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method %q", call.Method)
		}
		return h(ctx, call.Params)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx, ln)
	t.Cleanup(func() {
		cancel()
		ln.Close()
	})
	return sock
}

func TestHostDetailsRealRPCRaceOrderAutoRefreshAndLegacy(t *testing.T) {
	_, a, b := hostViewsFixture(t)
	h := remoteTestHost("remote")
	h.Name = "bwh"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	// 1. 阻塞第一次展开请求，再发出第二次展开并核实旧请求不会覆盖新结果。
	blockFirst := make(chan struct{})
	firstEntered := make(chan struct{})
	var memCalls atomic.Int32
	var mu sync.Mutex
	legacy := false

	sock := startHostDetailsMockRPC(t, map[string]rpc.Handler{
		"daemon.info": func(context.Context, json.RawMessage) (any, error) {
			return rpc.RemoteInfo{
				Protocol: 1, InstanceID: "inst-1", Platform: "linux", Arch: "amd64",
				Home: "/home/dev/.posse", Version: "0.1.54", Build: "abc1234 2026-11-27T12:00:00Z",
				Edition: "headless", Managed: true,
			}, nil
		},
		"server.autostart.status": func(context.Context, json.RawMessage) (any, error) {
			mu.Lock()
			isLegacy := legacy
			mu.Unlock()
			if isLegacy {
				return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method")
			}
			return rpc.AutostartStatus{Supported: true, Kind: "systemd", Managed: true, Installed: true, Enabled: true, Active: true}, nil
		},
		"daemon.memory": func(ctx context.Context, raw json.RawMessage) (any, error) {
			mu.Lock()
			isLegacy := legacy
			mu.Unlock()
			if isLegacy {
				return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method")
			}
			n := memCalls.Add(1)
			if n == 2 {
				close(firstEntered)
				select {
				case <-blockFirst:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return rpc.DaemonMemoryResult{TotalKB: 1024, Workspaces: []rpc.DaemonMemoryWorkspace{{ID: "stale", Label: "stale-ws", KB: 1024}}}, nil
			}
			return rpc.DaemonMemoryResult{TotalKB: 2048, Workspaces: []rpc.DaemonMemoryWorkspace{{ID: "fresh", Label: "fresh-ws", KB: 2048}}}, nil
		},
		"daemon.status": func(context.Context, json.RawMessage) (any, error) {
			return map[string]any{
				"version": "0.1.54", "build": "abc1234", "edition": "headless",
				"pid": 9001, "home": "/home/dev/.posse", "terminals": 5, "running": 2, "footprintKB": 4096,
			}, nil
		},
		"project.list":   func(context.Context, json.RawMessage) (any, error) { return []project{}, nil },
		"workspace.list": func(context.Context, json.RawMessage) (any, error) { return []workspace{}, nil },
		"task.list":      func(context.Context, json.RawMessage) (any, error) { return []taskInfo{}, nil },
	})

	id := rpc.DesktopIdentity{ClientID: "win", InstanceID: "inst-1", Epoch: 1}
	c := (&client.Client{Socket: sock, Remote: true, Platform: "linux"}).WithDesktop(id)

	// 先在未连接时打开概览，随后模拟连接成功：概览帧渲染应自动触发刷新。
	a.views.activate(h.ID)
	a.openHostDetails(h)
	b.host.generation = 1
	b.host.current = hostSnapshot{HostID: h.ID, Connected: true, Controlled: true, Generation: 1, Client: c, Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 1}}
	m := locales.In("zh-Hans")
	tt := newTester(func(c *ui.Context) { a.hostDialog(c) }, 760, 900)
	settle(tt)
	for range 20 {
		a.runPending()
		if a.hostDlg.detail.info != nil && a.hostDlg.detail.mem != nil && a.hostDlg.detail.autostart != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if a.hostDlg.detail.info == nil || a.hostDlg.detail.mem == nil || a.hostDlg.detail.autostart == nil {
		t.Fatalf("overview did not auto-refresh after host connected: info=%v mem=%v autostart=%v", a.hostDlg.detail.info, a.hostDlg.detail.mem, a.hostDlg.detail.autostart)
	}

	// 验证展开乱序：先发起被阻塞的请求(2)，再发起新请求(3)完成，最后放行(2)，结果仍须为 fresh-ws。
	a.reloadHostDetailMemory(a, &a.hostDlg)
	select {
	case <-firstEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked expand request did not enter handler")
	}
	a.reloadHostDetailMemory(a, &a.hostDlg)
	for range 20 {
		a.runPending()
		if !a.hostDlg.detail.loadingMem && a.hostDlg.detail.mem != nil && len(a.hostDlg.detail.mem.Workspaces) == 1 && a.hostDlg.detail.mem.Workspaces[0].ID == "fresh" {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	close(blockFirst)
	time.Sleep(40 * time.Millisecond)
	a.runPending()
	if a.hostDlg.detail.mem == nil || len(a.hostDlg.detail.mem.Workspaces) != 1 || a.hostDlg.detail.mem.Workspaces[0].ID != "fresh" {
		t.Fatalf("stale expand response overwrote fresh result: %+v", a.hostDlg.detail.mem)
	}

	// 验证真实轮询传播 Terminals / Home / Edition 字段。
	b.refresh()
	b.runPending()
	if b.daemon.Terminals != 5 || b.daemon.Running != 2 || b.daemon.Home != "/home/dev/.posse" || b.daemon.Edition != "headless" {
		t.Fatalf("real poll dropped daemon fields: %+v", b.daemon)
	}

	// 验证真实旧服务端返回 CodeUnknown 后，界面自动降级为升级提示。
	mu.Lock()
	legacy = true
	mu.Unlock()
	a.refreshHostDetails(a, &a.hostDlg)
	for range 20 {
		a.runPending()
		if a.hostDlg.detail.memUnsupported && a.hostDlg.detail.autostartUnsupported {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	settle(tt)
	if !tt.HasText(m.HostResLegacyNote()) || !tt.HasText(m.HostAutostartLegacyNote()) {
		t.Fatalf("real CodeUnknown legacy fallback missing: %v", tt.Texts())
	}
}

func TestHostDetailsAutostartToggleAndOutcomeUnknownReconcile(t *testing.T) {
	_, a, b := hostViewsFixture(t)
	h := remoteTestHost("remote")
	h.Name = "bwh"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	var setCalls atomic.Int32
	var statusCalls atomic.Int32
	var opPolls atomic.Int32
	var mu sync.Mutex
	currentInstalled := false
	mode := "ok" // "ok" | "unknown"

	sock := startHostDetailsMockRPC(t, map[string]rpc.Handler{
		"daemon.info": func(context.Context, json.RawMessage) (any, error) {
			return rpc.RemoteInfo{Protocol: 1, InstanceID: "inst-1", Platform: "linux", Arch: "amd64", Home: "/home/dev/.posse", Version: "0.1.54", Edition: "headless", Managed: true, Features: []string{"autostart.v1", "autostart.write.v1", "memory.v1"}}, nil
		},
		"daemon.memory": func(context.Context, json.RawMessage) (any, error) {
			return rpc.DaemonMemoryResult{}, nil
		},
		"server.autostart.status": func(context.Context, json.RawMessage) (any, error) {
			statusCalls.Add(1)
			mu.Lock()
			on := currentInstalled
			mu.Unlock()
			return rpc.AutostartStatus{Supported: true, Kind: "systemd", Managed: true, Writable: true, Installed: on, Enabled: on, Active: on}, nil
		},
		"server.autostart.set": func(_ context.Context, raw json.RawMessage) (any, error) {
			setCalls.Add(1)
			var p rpc.AutostartSetParams
			_ = rpc.Decode(raw, &p)
			en := p.Enabled != nil && *p.Enabled
			mu.Lock()
			m := mode
			currentInstalled = en
			mu.Unlock()
			if m == "unknown" {
				return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
			}
			return rpc.AutostartStatus{Supported: true, Kind: "systemd", Managed: true, Writable: true, Installed: en, Enabled: en, Active: en}, nil
		},
		"desktop.operation": func(_ context.Context, raw json.RawMessage) (any, error) {
			var ref rpc.DesktopOperationRef
			_ = rpc.Decode(raw, &ref)
			n := opPolls.Add(1)
			// 第一次模拟临时查询错误，验证客户端按原 ref 重试而不提前退出；第二次返回协议规范的 unknown（Method 为空）走最终 status 对账。
			if n == 1 {
				return nil, errors.New("transient read failure")
			}
			return rpc.DesktopOperation{DesktopOperationRef: ref, State: "unknown"}, nil
		},
	})

	id := rpc.DesktopIdentity{ClientID: "win", InstanceID: "inst-1", Epoch: 1}
	c := (&client.Client{Socket: sock, Remote: true, Platform: "linux"}).WithDesktop(id)
	a.views.activate(h.ID)
	b.host.generation = 1
	b.host.current = hostSnapshot{HostID: h.ID, Connected: true, Controlled: true, Generation: 1, Client: c, Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 1}}

	a.openHostDetails(h)
	for range 20 {
		a.runPending()
		if a.hostDlg.detail.autostart != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if a.hostDlg.detail.autostart == nil || a.hostDlg.detail.autostart.Installed {
		t.Fatalf("initial autostart status unexpected: %+v", a.hostDlg.detail.autostart)
	}

	// 1. 控制端切换开启，走 PrepareSetAutostart -> server.autostart.set 成功。
	a.toggleHostAutostart(a, &a.hostDlg, b, true)
	for range 30 {
		a.runPending()
		if b.autostartOp != nil && !b.autostartOp.busy {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if setCalls.Load() != 1 || !a.hostDlg.detail.autostart.Installed || a.hostDlg.detail.autostartErr != "" {
		t.Fatalf("toggle enable failed: calls=%d st=%+v err=%q", setCalls.Load(), a.hostDlg.detail.autostart, a.hostDlg.detail.autostartErr)
	}

	// 2. 遇到 outcome_unknown 时即使关闭弹窗也保留主机级操作状态，重试临时读错后按原 ref 查终态再经 status 对账，不重发 set。
	mu.Lock()
	mode = "unknown"
	mu.Unlock()
	beforeStatus := statusCalls.Load()
	a.toggleHostAutostart(a, &a.hostDlg, b, false)
	a.hostDlg.open = false
	for range 60 {
		a.runPending()
		if b.autostartOp != nil && !b.autostartOp.busy {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if setCalls.Load() != 2 {
		t.Fatalf("outcome_unknown unexpectedly retried set: calls=%d", setCalls.Load())
	}
	if opPolls.Load() < 2 {
		t.Fatalf("outcome_unknown did not retry transient operation status query: polls=%d", opPolls.Load())
	}
	if statusCalls.Load() <= beforeStatus || b.autostartOp.status == nil || b.autostartOp.status.Installed || b.autostartOp.notice == "" {
		t.Fatalf("outcome_unknown did not reconcile via status: statusCalls=%d->%d op=%+v", beforeStatus, statusCalls.Load(), b.autostartOp)
	}
}

func TestRemoteMemoryPanelBreakdownAndFallback(t *testing.T) {
	_, a, b := hostViewsFixture(t)
	h := remoteTestHost("remote")
	h.Name = "bwh"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()
	b.daemon.FootprintKB = 40 * 1024
	b.mem.rows = remoteMemoryRows(rpc.DaemonMemoryResult{
		TotalKB: 80 * 1024,
		Workspaces: []rpc.DaemonMemoryWorkspace{{
			ID: "w1", Label: "ai/main", KB: 80 * 1024,
			Terminals: []rpc.DaemonMemoryTerminal{{ID: "t1", Title: "claude", Agent: "claude", KB: 80 * 1024}},
		}},
	})
	b.mem.terms = 80 * 1024
	b.mem.remoteTruncated = true

	m := locales.In("zh-Hans")
	tt := newTester(func(c *ui.Context) { b.memoryPanel(c) }, 360, 420)
	settle(tt)
	if !tt.HasText("120 MB") || !tt.HasText("ai/main") || !tt.HasText(m.HostResTruncated()) {
		t.Fatalf("remote memory panel missing breakdown: %v", tt.Texts())
	}

	b.mem.remoteUnsupported = true
	b.mem.remoteTruncated = false
	settle(tt)
	if !tt.HasText(m.MemoryRemoteNote("bwh")) {
		t.Fatalf("remote memory panel missing legacy fallback note: %v", tt.Texts())
	}
}

func TestPreviewHostDetails(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprint(dark), func(t *testing.T) {
			_, a, b := hostViewsFixture(t)
			h := remoteTestHost("remote")
			h.Name = "bwh"
			h.Target = "surgeadmin@bwh.example"
			writeHostMenuConfig(t, a, h)
			a.loadHostMenu()
			a.views.activate(h.ID)
			b.host.current = hostSnapshot{HostID: h.ID, Connected: true, Controlled: true, Client: &client.Client{Remote: true, Platform: "linux"}}
			b.daemon = daemonStatus{
				Version:     "0.1.54",
				Build:       "6f4d47b 2026-11-27T12:00:00Z",
				Edition:     "headless",
				Pid:         4242,
				Home:        "/home/surgeadmin/.posse",
				StartedAt:   time.Now().Add(-27 * time.Hour),
				Terminals:   3,
				Running:     2,
				FootprintKB: 43 * 1024,
			}
			a.openHostDetails(h)
			a.cancelHostDetails()
			a.hostDlg.detail.liveKey = detailLiveKey(h.ID, b)
			a.hostDlg.detail.loadingInfo = false
			a.hostDlg.detail.loadingAutostart = false
			a.hostDlg.detail.loadingMem = false
			a.hostDlg.detail.info = &rpc.RemoteInfo{
				Version:  "0.1.54",
				Build:    "6f4d47b 2026-11-27T12:00:00Z",
				Edition:  "headless",
				Managed:  true,
				Platform: "linux",
				Arch:     "amd64",
				Home:     "/home/surgeadmin/.posse",
				Features: []string{"autostart.v1", "autostart.write.v1", "memory.v1"},
			}
			b.upgradeCheck = &remote.ServerUpgradeCheck{
				Available:      true,
				Source:         "remote",
				CurrentVersion: "0.1.54",
				TargetVersion:  "0.1.55",
				TargetCommit:   "a1b2c3d",
				DesktopVersion: "0.1.55",
			}
			a.hostDlg.detail.memExpanded = map[string]bool{"t1": true}
			a.hostDlg.detail.mem = &rpc.DaemonMemoryResult{
				TotalKB: 185 * 1024,
				Workspaces: []rpc.DaemonMemoryWorkspace{{
					ID: "w1", Label: "ai/main", KB: 185 * 1024,
					Terminals: []rpc.DaemonMemoryTerminal{{
						ID: "t1", Title: "claude", Agent: "claude", KB: 165 * 1024,
						Procs: []rpc.DaemonMemoryProc{
							{Name: "claude", KB: 150 * 1024, Count: 1},
							{Name: "mcp-server", KB: 15 * 1024, Count: 2},
						},
					}, {
						ID: "t2", Title: "bash", KB: 20 * 1024,
					}},
				}},
			}
			a.hostDlg.detail.autostart = &rpc.AutostartStatus{
				Supported:          true,
				Kind:               "systemd",
				Managed:            true,
				Writable:           true,
				Installed:          true,
				Enabled:            true,
				Active:             true,
				Path:               "/home/surgeadmin/.config/systemd/user/posse-server.service",
				ExpectedExecutable: "/home/surgeadmin/.posse/server/current/posse",
				Linger:             "no",
				NoteCode:           rpc.AutostartNoteSystemdLingerDisabled,
			}
			tt := newTester(func(c *ui.Context) { a.hostDialog(c) }, 760, 1060)
			tt.SetDark(dark)
			settle(tt)
			savePNG(t, tt, fmt.Sprintf("host-details-%t.png", dark))
		})
	}
}

func TestHostUpgradeStateMachineHeartbeatBusyViewerAndSeamlessReattach(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	k := newHostConnections(v)
	a.hosts = k
	t.Cleanup(k.close)
	h := remoteTestHost("remote")
	h.Name = "bwh"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	var mu sync.Mutex
	curInst := "inst-1"
	curOwner := ""
	curEpoch := uint64(0)
	var takeCalls atomic.Int32

	// 扩展 mock server 直接处理 desktop.status / desktop.take / desktop.release 供 connectRemoteAt 使用。
	dir, err := os.MkdirTemp("/tmp", "hup-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock2 := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock2)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	srv.Handle("desktop.status", func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return rpc.DesktopState{InstanceID: curInst, Owner: curOwner, Epoch: curEpoch}, nil
	})
	srv.Handle("desktop.heartbeat", func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		return rpc.DesktopState{InstanceID: curInst, Owner: curOwner, Epoch: curEpoch}, nil
	})
	srv.Handle("desktop.take", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			ClientID string `json:"clientId"`
			Label    string `json:"label"`
		}
		_ = rpc.Decode(raw, &p)
		takeCalls.Add(1)
		mu.Lock()
		defer mu.Unlock()
		curOwner = p.ClientID
		curEpoch++
		return rpc.DesktopState{InstanceID: curInst, Owner: curOwner, Label: p.Label, Epoch: curEpoch}, nil
	})
	srv.Handle("desktop.release", func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"ok": true}, nil
	})
	srv.Handle("desktop.call", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var call rpc.DesktopCall
		_ = rpc.Decode(raw, &call)
		switch call.Method {
		case "daemon.info":
			mu.Lock()
			id := curInst
			mu.Unlock()
			return rpc.RemoteInfo{
				Protocol: 1, InstanceID: id, Platform: "linux", Arch: "amd64",
				Home: "/home/dev/.posse", Version: "0.1.55", Edition: "headless", Managed: true,
				Features: []string{"autostart.v1", "autostart.write.v1", "memory.v1"},
			}, nil
		case "daemon.memory":
			return rpc.DaemonMemoryResult{}, nil
		case "server.autostart.status":
			return rpc.AutostartStatus{Supported: true, Kind: "systemd", Managed: true, Writable: true}, nil
		default:
			return nil, rpc.Errorf(rpc.CodeUnknown, "unknown %s", call.Method)
		}
	})
	sctx, scancel := context.WithCancel(context.Background())
	go srv.Serve(sctx, ln)
	t.Cleanup(func() { scancel(); ln.Close() })

	baseClient := &client.Client{Socket: sock2, Remote: true, Platform: "linux", Home: "/home/dev/.posse"}
	a.views.activate(h.ID)
	// 先模拟其他端持控（查看态），通过真实 connectRemoteAt 建立连接。
	mu.Lock()
	curOwner = "other-window"
	curEpoch = 1
	mu.Unlock()
	if err := b.host.connectRemoteAt(context.Background(), baseClient, b.host.snapshot().Generation); err != nil {
		t.Fatalf("connectRemoteAt viewer failed: %v", err)
	}
	if b.host.snapshot().Controlled {
		t.Fatal("expected viewer mode when another owner holds desktop control")
	}

	// 1. 查看态（Controlled=false）不允许触发热升级。
	var upCalls atomic.Int32
	k.upgrade = func(context.Context, string, remote.Options) (rpc.RemoteInfo, bool, error) {
		upCalls.Add(1)
		return rpc.RemoteInfo{}, false, nil
	}
	k.upgradeRemote(h.ID)
	if upCalls.Load() != 0 || b.upgrading.Load() {
		t.Fatal("viewer was allowed to trigger upgradeRemote")
	}

	// 2. 切回无人控制并走真实 connectRemoteAt 自动接管控制权，同时保留现有分栏（验证升级不清空 panes/tabs）。
	mu.Lock()
	curOwner = ""
	curEpoch = 0
	mu.Unlock()
	if err := b.host.connectRemoteAt(context.Background(), baseClient, b.host.snapshot().Generation); err != nil {
		t.Fatalf("connectRemoteAt controller failed: %v", err)
	}
	if !b.host.snapshot().Controlled {
		t.Fatal("expected controller mode")
	}
	b.panes[0] = &termView{id: "term-keep"}
	k.active = &hostAttempt{app: b, cancel: func() {}}

	// 2a. 遇到 rpc.CodeBusy：保留原连接与分栏，设置报错文本。
	k.upgrade = func(context.Context, string, remote.Options) (rpc.RemoteInfo, bool, error) {
		return rpc.RemoteInfo{}, false, rpc.Errorf(rpc.CodeBusy, "handoff already in progress")
	}
	k.upgradeRemote(h.ID)
	for range 40 {
		a.runPending()
		b.runPending()
		if !b.upgrading.Load() {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !b.host.snapshot().Connected || !b.host.snapshot().Controlled || b.panes[0] == nil || b.panes[0].id != "term-keep" {
		t.Fatalf("CodeBusy disrupted live session or panes: snap=%+v pane=%+v", b.host.snapshot(), b.panes[0])
	}
	if b.upgradeErr == "" {
		t.Fatal("expected localized upgradeErr after CodeBusy")
	}

	// 2b. 正常热升级：验证 Stage 1（上传阶段）保持心跳续租不丢控制权；进入 Stage 2（BeforeHandoff）后才抑制心跳；
	// disconnect/修改连接/移除主机均被锁；不占 dialMu；handoff 后无感重接管不丢 pane。
	entered := make(chan struct{})
	enterHandoff := make(chan struct{})
	handoffStarted := make(chan struct{})
	release := make(chan struct{})
	k.upgrade = func(ctx context.Context, _ string, opts remote.Options) (rpc.RemoteInfo, bool, error) {
		if opts.Progress != nil {
			opts.Progress(remote.StageUpload, 0.5)
		}
		close(entered)
		<-enterHandoff
		if opts.BeforeHandoff != nil {
			if err := opts.BeforeHandoff(ctx); err != nil {
				return rpc.RemoteInfo{}, false, err
			}
		}
		if opts.Progress != nil {
			opts.Progress(remote.StageUpgrade, 0)
		}
		close(handoffStarted)
		<-release
		mu.Lock()
		curInst = "inst-2"
		curOwner = ""
		curEpoch = 0
		mu.Unlock()
		return rpc.RemoteInfo{
			InstanceID: "inst-2", Platform: "linux", Arch: "amd64", Home: "/home/dev/.posse",
			Version: "0.1.55", Edition: "headless", Managed: true,
			Features: []string{"autostart.v1", "autostart.write.v1", "memory.v1"},
		}, true, nil
	}

	beforeTakes := takeCalls.Load()
	k.upgradeRemote(h.ID)
	<-entered
	a.runPending()
	b.runPending()
	if !b.upgrading.Load() || b.upgradeHandingOff.Load() || a.remoteHostConnState(h.ID) != hostConnUpgrading {
		t.Fatalf("expected stage 1 upgrading state, got upgrading=%v handingOff=%v state=%v", b.upgrading.Load(), b.upgradeHandingOff.Load(), a.remoteHostConnState(h.ID))
	}
	// Stage 1 上传期间 heartbeat 保持运行续租（返回 true），不丢 15s 桌面租约。
	if !k.heartbeat(b) {
		t.Fatal("heartbeat should stay active during Stage 1 upload to keep desktop lease alive")
	}
	// 升级期间禁止修改连接配置与移除主机。
	a.openHostDialog(h)
	a.hostDlg.target = "other@host.example"
	if !a.editingLiveConnectionChanged(&a.hostDlg) {
		t.Fatal("expected editingLiveConnectionChanged=true while upgrading")
	}
	a.submitHostDialog(locales.In("zh-Hans"), &a.hostDlg, false)
	if a.hostDlg.saving || a.hostDlg.err == "" {
		t.Fatalf("submitHostDialog should block connection changes while upgrading: saving=%v err=%q", a.hostDlg.saving, a.hostDlg.err)
	}
	a.hostDlg.open = false

	// 进入 Stage 2 handoff：BeforeHandoff 切 upgradeHandingOff=true，此时抑制 heartbeat 防止瞬断拆隧道。
	close(enterHandoff)
	<-handoffStarted
	a.runPending()
	b.runPending()
	if k.heartbeat(b) {
		t.Fatal("heartbeat should be suppressed during Stage 2 handoff")
	}
	a.runPending()
	b.runPending()
	if !b.host.snapshot().Connected {
		t.Fatal("heartbeat unexpectedly disconnected host during upgrade")
	}
	// disconnect 在升级期间被阻止。
	k.disconnect(h.ID)
	if !b.host.snapshot().Connected {
		t.Fatal("disconnect should be blocked during upgrade")
	}
	// 不占用 dialMu，可立即加锁验证切换其他主机无阻塞。
	if !k.dialMu.TryLock() {
		t.Fatal("upgradeRemote unexpectedly held dialMu")
	}
	k.dialMu.Unlock()

	close(release)
	for range 40 {
		a.runPending()
		b.runPending()
		if !b.upgrading.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if b.upgrading.Load() || b.upgradeHandingOff.Load() || b.upgradeErr != "" || b.upgradeNotice == "" {
		t.Fatalf("upgrade did not complete cleanly: upgrading=%v handingOff=%v err=%q notice=%q", b.upgrading.Load(), b.upgradeHandingOff.Load(), b.upgradeErr, b.upgradeNotice)
	}
	snap := b.host.snapshot()
	if !snap.Connected || !snap.Controlled || snap.Desktop.InstanceID != "inst-2" || takeCalls.Load() <= beforeTakes {
		t.Fatalf("seamless reattach failed: snap=%+v takes=%d->%d", snap, beforeTakes, takeCalls.Load())
	}
	if b.panes[0] == nil || b.panes[0].id != "term-keep" {
		t.Fatalf("seamless reattach cleared panes: %+v", b.panes[0])
	}
	b.panes[0] = nil // 清理占位 termView，避免切走或 Cleanup 中 ensurePanes 关闭 nil Terminal

	// 3. P1-3 + P1-6 回归：升级期间切走（detach）再切回后取消 Stage 1，应自动重连活动视图而非停在断开态。
	cancelEntered := make(chan struct{})
	k.upgrade = func(ctx context.Context, _ string, _ remote.Options) (rpc.RemoteInfo, bool, error) {
		close(cancelEntered)
		<-ctx.Done()
		return rpc.RemoteInfo{}, false, ctx.Err()
	}
	var redials atomic.Int32
	doneTransport := make(chan struct{})
	t.Cleanup(func() { close(doneTransport) })
	k.dial = func(context.Context, remote.Host, remote.Options) (hostTransport, error) {
		redials.Add(1)
		return hostTransport{client: baseClient, done: doneTransport}, nil
	}
	k.upgradeRemote(h.ID)
	<-cancelEntered
	a.selectLocalHost() // 切走（触发 b.host.detach()）
	// 即使 b 已 detach，编辑连接字段仍被视为热升级中变更而禁止。
	a.openHostDialog(h)
	a.hostDlg.target = "changed@host.example"
	if !a.editingLiveConnectionChanged(&a.hostDlg) {
		t.Fatal("expected editingLiveConnectionChanged=true while detached but still upgrading")
	}
	a.hostDlg.open = false
	a.openHost(h) // 切回仍在升级的主机（不重复拨号）
	if redials.Load() != 0 {
		t.Fatalf("openHost unexpectedly dialed while upgrading: redials=%d", redials.Load())
	}
	k.cancelHostUpgrade(h.ID)
	for range 40 {
		a.runPending()
		b.runPending()
		if !b.upgrading.Load() && b.host.snapshot().Connected {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if redials.Load() != 1 || !b.host.snapshot().Connected {
		t.Fatalf("canceled upgrade after switch-back did not restore active view: redials=%d snap=%+v", redials.Load(), b.host.snapshot())
	}
}
