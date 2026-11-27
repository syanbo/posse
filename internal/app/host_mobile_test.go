package app

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/remote"
	"posse/internal/rpc"
	"posse/internal/tslink"
)

func TestHostMobileTabControlViewOnlyAndCleanup(t *testing.T) {
	k, a, b, _, _, _ := connectionFixture(t)
	h := remoteTestHost("remote")
	h.Name = "Linux VPS"
	h.Target = "dev@vps.example"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	var pairCalls, rmCalls, devCalls atomic.Int32
	var lastAddr atomic.Value
	lastAddr.Store("")
	devices := []client.MobileDevice{
		{ID: "d1", Name: "iPhone 16 Pro", Phone: "p1", PairedAt: time.Now().Add(-24 * time.Hour), LastSeen: time.Now(), Online: true},
	}

	_, remoteCli := startFakeDaemon(t, map[string]rpc.Handler{
		"desktop.call": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p rpc.DesktopCall
			if err := rpc.Decode(raw, &p); err != nil {
				return nil, err
			}
			switch p.Method {
			case "mobile.pair":
				pairCalls.Add(1)
				var mp struct {
					Rotate  bool   `json:"rotate"`
					Address string `json:"address"`
				}
				_ = json.Unmarshal(p.Params, &mp)
				lastAddr.Store(mp.Address)
				ep := "ws://100.64.0.9:6788"
				if mp.Address != "" {
					ep = "ws://" + mp.Address + ":6788"
				}
				return client.MobilePairResult{
					URL:       "orca://pair?code=REMOTE_CODE_" + mp.Address,
					Code:      "REMOTE_CODE_" + mp.Address,
					Endpoint:  ep,
					Endpoints: []string{ep},
					Locals:    []string{"100.64.0.9", "10.0.0.8"},
				}, nil
			case "mobile.devices":
				devCalls.Add(1)
				return devices, nil
			case "mobile.rm":
				rmCalls.Add(1)
				return map[string]any{"removed": "d1"}, nil
			case "tailscale.status":
				return tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.9"}, nil
			case "relay.status":
				return client.RelayStatusResult{Configured: true, State: "ready"}, nil
			default:
				return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method %s", p.Method)
			}
		},
	})
	remoteCli.Remote = true
	id := rpc.DesktopIdentity{ClientID: "c1", InstanceID: "inst-1", Epoch: 1}
	remoteCli.Desktop = &id
	b.host.current = hostSnapshot{
		HostID: h.ID, Connected: true, Controlled: true, Client: remoteCli,
		Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 1, Owner: "c1"},
	}
	k.views.activate(h.ID)

	// 1. 控制端打开手机配对 Tab：调用远端 mobile.pair + mobile.devices，使用目标主机 Locals 并生成二维码。
	a.openHostDialogTab(h, hostTabMobile)
	for range 40 {
		a.runPending()
		if !a.hostDlg.mobile.loading && a.hostDlg.mobile.qr != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if pairCalls.Load() != 1 || devCalls.Load() != 1 {
		t.Fatalf("expected 1 pair & 1 devices call, got pair=%d dev=%d", pairCalls.Load(), devCalls.Load())
	}
	if !slices.Equal(a.hostDlg.mobile.locals, []string{"100.64.0.9", "10.0.0.8"}) {
		t.Fatalf("unexpected remote locals: %v", a.hostDlg.mobile.locals)
	}
	if a.hostDlg.mobile.qr == nil || a.hostDlg.mobile.code != "REMOTE_CODE_" {
		t.Fatalf("expected QR bitmap and remote code, got qr=%v code=%q", a.hostDlg.mobile.qr != nil, a.hostDlg.mobile.code)
	}

	// 2. 切回概览 Tab 应取消并释放二维码位图。
	a.cancelHostMobile()
	if a.hostDlg.mobile.qr != nil {
		t.Fatal("expected QR bitmap cleared after cancelHostMobile")
	}

	// 3. 查看端（Controlled=false）：绝不发 mobile.pair 写请求，但仍能只读获取设备列表与状态。
	beforePairs := pairCalls.Load()
	b.host.current = hostSnapshot{
		HostID: h.ID, Connected: true, Controlled: false, Client: remoteCli,
		Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 1, Owner: "other"},
	}
	a.openHostDialogTab(h, hostTabMobile)
	for range 40 {
		a.runPending()
		if !a.hostDlg.mobile.loading && !a.hostDlg.mobile.loadedAt.IsZero() {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if pairCalls.Load() != beforePairs {
		t.Fatalf("view-only mode unexpectedly called mobile.pair: %d -> %d", beforePairs, pairCalls.Load())
	}
	if len(a.hostDlg.mobile.devices) != 1 || rmCalls.Load() != 0 {
		t.Fatalf("expected 1 read-only device and 0 rm calls, got devs=%d rm=%d", len(a.hostDlg.mobile.devices), rmCalls.Load())
	}

	// 4. 离屏渲染亮暗两套，确保无布局 panic，并支持 POSSE_PREVIEW_DIR 导出预览图。
	b.host.current = hostSnapshot{
		HostID: h.ID, Connected: true, Controlled: true, Client: remoteCli,
		Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 1, Owner: "c1"},
	}
	a.openHostDialogTab(h, hostTabMobile)
	for range 40 {
		a.runPending()
		if !a.hostDlg.mobile.loading && a.hostDlg.mobile.qr != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	previewDir := os.Getenv("POSSE_PREVIEW_DIR")
	for _, dark := range []bool{false, true} {
		a.state.Theme = "light"
		if dark {
			a.state.Theme = "dark"
		}
		tt := newTester(func(c *ui.Context) {
			c.SetTheme(posseTheme(dark))
			ui.Box(c).Fill().Background(c.Theme().Background).Children(func() {
				a.hostDialog(c)
			})
		}, 760, 1020)
		settle(tt)
		if previewDir != "" {
			name := "host-mobile-light.png"
			if dark {
				name = "host-mobile-dark.png"
			}
			savePNG(t, tt, name)
		}
	}
	a.cancelHostDetails()
	_ = remote.Host{}
}

func TestHostMobilePollReadOnlyStaleConfirmAndDialogReopenRace(t *testing.T) {
	k, a, b, _, _, _ := connectionFixture(t)
	h := remoteTestHost("remote")
	h.Name = "Linux VPS"
	h.Target = "dev@vps.example"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	var pairCalls, rmCalls, devCalls atomic.Int32
	devGate := make(chan struct{})
	var gateOnce atomic.Bool
	_, remoteCli := startFakeDaemon(t, map[string]rpc.Handler{
		"desktop.call": func(ctx context.Context, raw json.RawMessage) (any, error) {
			var p rpc.DesktopCall
			if err := rpc.Decode(raw, &p); err != nil {
				return nil, err
			}
			switch p.Method {
			case "mobile.pair":
				pairCalls.Add(1)
				return client.MobilePairResult{
					URL: "orca://pair?code=CODE1", Code: "CODE1",
					Endpoint: "ws://100.64.0.9:6788", Locals: []string{"100.64.0.9"},
				}, nil
			case "mobile.devices":
				devCalls.Add(1)
				if gateOnce.CompareAndSwap(false, true) {
					select {
					case <-devGate:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return []client.MobileDevice{{ID: "d1", Name: "iPhone", Phone: "p1", PairedAt: time.Now(), LastSeen: time.Now(), Online: true}}, nil
			case "mobile.rm":
				rmCalls.Add(1)
				return map[string]any{"removed": "d1"}, nil
			case "tailscale.status":
				return tslink.Status{Enabled: true, State: tslink.StateRunning, IP: "100.64.0.9"}, nil
			case "relay.status":
				return client.RelayStatusResult{Configured: true, State: "ready"}, nil
			default:
				return nil, rpc.Errorf(rpc.CodeUnknown, "unknown method %s", p.Method)
			}
		},
	})
	remoteCli.Remote = true
	id1 := rpc.DesktopIdentity{ClientID: "c1", InstanceID: "inst-1", Epoch: 1}
	remoteCli.Desktop = &id1
	b.host.current = hostSnapshot{
		HostID: h.ID, Connected: true, Controlled: true, Client: remoteCli,
		Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 1, Owner: "c1"},
	}
	k.views.activate(h.ID)

	// 1. P1-3 竞态回归：mobile.devices 挂起时重开对话框，不能出现对对话框可变字段的 DATA RACE。
	a.openHostDialogTab(h, hostTabMobile)
	time.Sleep(30 * time.Millisecond)
	a.openHostDialogTab(h, hostTabMobile)
	close(devGate)
	for range 40 {
		a.runPending()
		if !a.hostDlg.mobile.loading && a.hostDlg.mobile.qr != nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	// 2. P1-4 回归：例行轮询（poll=true）严格只读，绝不再触发写操作 mobile.pair。
	beforePollPairs := pairCalls.Load()
	beforePollDevs := devCalls.Load()
	a.refreshHostMobile(a, &a.hostDlg, false, true)
	for range 40 {
		a.runPending()
		if !a.hostDlg.mobile.loading && devCalls.Load() > beforePollDevs {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if pairCalls.Load() != beforePollPairs {
		t.Fatalf("routine poll unexpectedly called mobile.pair: %d -> %d", beforePollPairs, pairCalls.Load())
	}

	// 3. P1-2 回归：打开解绑确认框后控制代次变更，点击旧确认框绝不能用新身份提交 mobile.rm。
	a.removeHostMobileDevice(a, &a.hostDlg, b, client.MobileDevice{ID: "d1", Name: "iPhone"})
	if !a.confirming || a.confirmDo == nil {
		t.Fatal("expected confirm dialog to be open")
	}
	oldConfirm := a.confirmDo
	id2 := rpc.DesktopIdentity{ClientID: "c1", InstanceID: "inst-1", Epoch: 2}
	remoteCli2 := *remoteCli
	remoteCli2.Desktop = &id2
	b.host.current = hostSnapshot{
		HostID: h.ID, Connected: true, Controlled: true, Client: &remoteCli2,
		Desktop: rpc.DesktopState{InstanceID: "inst-1", Epoch: 2, Owner: "c1"},
	}
	oldConfirm()
	a.runPending()
	time.Sleep(50 * time.Millisecond)
	if rmCalls.Load() != 0 {
		t.Fatalf("stale confirm dialog submitted mobile.rm under new epoch: rmCalls=%d", rmCalls.Load())
	}

	// 4. 升级开始时 hostMobileTab 必须立即清理凭据与二维码。
	b.upgrading.Store(true)
	tt := newTester(func(c *ui.Context) {
		a.hostMobileTab(c, a, &a.hostDlg)
	}, 600, 700)
	settle(tt)
	if a.hostDlg.mobile.qr != nil || a.hostDlg.mobile.code != "" || a.hostDlg.mobile.url != "" {
		t.Fatalf("expected pairing credentials cleared during upgrade: qr=%v code=%q", a.hostDlg.mobile.qr != nil, a.hostDlg.mobile.code)
	}
	b.upgrading.Store(false)
	a.cancelHostDetails()
}
