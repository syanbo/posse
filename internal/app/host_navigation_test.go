package app

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
)

func TestHostNavigationModuleOwners(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	a.workspaces = []workspace{{ID: "same-w", Path: "/local/project"}}
	b.workspaces = []workspace{{ID: "same-w", Path: "/remote/project"}, {ID: "chat-w", Kind: "main", Path: "/remote/chat"}}
	a.state.LastWorkspace, b.state.LastWorkspace = "same-w", "same-w"
	b.chatDir = "/remote/chat"
	for _, id := range []string{discBoardID, skillsBoardID, mobileBoardID, settingsBoardID} {
		t.Run(id, func(t *testing.T) {
			v.activate("remote")
			b.selected = "same-w"
			b.openBoard(id)
			if v.active.Load() != a || a.selected != id || v.target != b || b.selected != "same-w" {
				t.Fatal("device module changed workspace target or remote selection")
			}
			a.openBoard(historyBoardID)
			if v.active.Load() != b || b.selected != historyBoardID || a.selected != id {
				t.Fatal("history did not return to the selected host")
			}
			b.openBoard(id)
			a.openBoard(taskBoardID)
			if v.active.Load() != b || !b.taskHome || b.selected != taskBoardID || a.taskHome {
				t.Fatal("tasks changed the wrong host")
			}
			b.openBoard(id)
			a.openChat(appL(), false)
			if v.active.Load() != b || b.selected != "chat-w" {
				t.Fatal("chat did not return to the selected host")
			}
			b.openBoard(id)
			a.closeBoard()
			if v.active.Load() != b || b.selected != "same-w" || v.target != b {
				t.Fatal("closing local page did not restore remote workspace")
			}
		})
	}
	b.openMobile()
	v.activate(localHostID)
	a.openWorkspaces()
	if v.target != a || v.active.Load() != a || a.selected != "same-w" {
		t.Fatal("explicit local host selection retained remote return target")
	}
}

func TestHostNavigationDiscussionIdentity(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	a.refreshQueued.Store(true)
	a.projects = []project{{ID: "local-p", Name: "Local project"}}
	a.disc.sel = "same-discussion"
	a.disc.text, b.disc.text = "local draft", "remote draft"
	v.activate("remote")
	b.openDiscussion("same-discussion")
	if v.active.Load() != b || a.disc.text != "local draft" {
		t.Fatal("remote discussion ID opened a local resource")
	}
	a.openDiscussion("same-discussion")
	if v.active.Load() != a || v.target != b || a.disc.text != "local draft" {
		t.Fatal("local discussion entry lost its owner or draft")
	}
	a.openWorkspaces()
	b.openNewDiscussion()
	if v.active.Load() != a || !a.disc.newOpen || a.disc.newProj != "Local project" || b.disc.newOpen {
		t.Fatal("new discussion used the remote project list")
	}
}

func TestHostNavigationClicks(t *testing.T) {
	k, a, b, _, _, _ := connectionFixture(t)
	v := k.views
	h := remoteTestHost("remote")
	h.Name = "Office Mac"
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()
	b.selected, b.state.LastWorkspace = "remote-w", "remote-w"
	b.workspaces = []workspace{{ID: "remote-w", Path: "/remote/project"}}
	a.selected, a.state.LastWorkspace = "local-w", "local-w"
	a.workspaces = []workspace{{ID: "local-w", Path: "/local/project"}}
	v.activate("remote")
	tt := newTester(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() { v.active.Load().navigation(c) })
	}, 900, 560)
	settle(tt)
	for _, label := range []string{"讨论（本机）", "技能（本机）", "手机（本机）"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		settle(tt)
		if v.active.Load() != a || v.target != b {
			t.Fatal("local navigation selected remote context")
		}
		// 点击左侧导航栏里的远端主机磁贴切回远端工作区（首次未连时发起连接，已连时直接切回不重拨）
		if err := tt.Click("Office Mac"); err != nil {
			t.Fatal(err)
		}
		settle(tt)
		if b.connecting {
			awaitHostUpdate(t, b)
		}
		if v.active.Load() != b || b.selected != "remote-w" {
			t.Fatal("remote host tile did not restore remote workspace")
		}
	}
	// 点击 >_ 固定切回本机工作区
	if err := tt.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if v.active.Load() != a || v.target != a || a.selected != "local-w" {
		t.Fatal("local workspace tile did not switch back to local host")
	}
}

func TestHostNavigationLocalDevicePageRoundTrip(t *testing.T) {
	k, a, b, _, _, _ := connectionFixture(t)
	_, localBase := newHostControlFixture(t)
	a.c = localBase
	a.host = newHostSession(localHostID, "Posse")
	a.connectLocal(context.Background())
	if !a.host.snapshot().Controlled {
		t.Fatal("local initial connect did not take control")
	}
	h := remoteTestHost("remote")
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()

	// 连接远端并自动接管
	a.selectRemoteHost(h)
	awaitHostUpdate(t, b)
	if !b.host.snapshot().Connected || !b.host.snapshot().Controlled {
		t.Fatalf("remote did not connect with control: %+v", b.host.snapshot())
	}

	// 从远端进入本机设置页：本机应自动请求控制，不卡只读
	b.openBoard(settingsBoardID)
	a.connectLocal(context.Background())
	if !a.host.snapshot().Connected || !a.host.snapshot().Controlled {
		t.Fatalf("entering local settings left local read-only: %+v", a.host.snapshot())
	}

	// 从本机设置页切回远端任务页：应自动复用连接并恢复控制，不停在已断开
	a.openBoard(taskBoardID)
	awaitHostUpdate(t, b)
	if k.views.active.Load() != b || !b.host.snapshot().Connected || !b.host.snapshot().Controlled {
		t.Fatalf("returning from local settings to remote task did not seamlessly reconnect: active=%s snap=%+v", k.views.active.Load().viewID, b.host.snapshot())
	}

	// 若用户主动断开远端，再进设置页并切回任务页，不得擅自重连
	k.disconnect(h.ID)
	b.openBoard(settingsBoardID)
	a.openBoard(taskBoardID)
	if b.connecting || b.host.snapshot().Connected {
		t.Fatalf("manually disconnected remote unexpectedly reconnected: connecting=%v snap=%+v", b.connecting, b.host.snapshot())
	}

	// 从设置页经 showModuleReady 返回挂起的远端，动作回调须等重连完成后再以有效控制身份执行
	a.selectRemoteHost(h)
	for i := 0; i < 4 && b.connecting; i++ {
		awaitHostUpdate(t, b)
	}
	b.openBoard(settingsBoardID)
	var ran, writable bool
	a.showModuleReady(workspacesBoardID, func(target *app) {
		ran = true
		writable = target.request().canWrite()
	})
	if ran {
		t.Fatal("showModuleReady invoked callback before remote reconnect finished")
	}
	for i := 0; i < 4 && b.connecting; i++ {
		awaitHostUpdate(t, b)
	}
	if !ran || !writable {
		t.Fatalf("showModuleReady callback after reconnect: ran=%v writable=%v", ran, writable)
	}

	// 离开远端进入设置页后若配置的目标地址变更，返回远端模块不得自动连向新地址
	b.openBoard(settingsBoardID)
	changed := h
	changed.Target = "new-target.example"
	writeHostMenuConfig(t, a, changed)
	a.loadHostMenu()
	a.openBoard(taskBoardID)
	if b.connecting || b.host.snapshot().Connected {
		t.Fatalf("changed host config unexpectedly auto-reconnected: connecting=%v snap=%+v", b.connecting, b.host.snapshot())
	}

	// 尚未连上的建连中状态切入设置页（原建连被取消），返回任务页不得自动重新拨号
	writeHostMenuConfig(t, a, h)
	a.loadHostMenu()
	blockDial := make(chan struct{})
	defer close(blockDial)
	oldDial := k.dial
	k.dial = func(ctx context.Context, host remote.Host, opts remote.Options) (hostTransport, error) {
		select {
		case <-ctx.Done():
			return hostTransport{}, ctx.Err()
		case <-blockDial:
			return oldDial(ctx, host, opts)
		}
	}
	a.selectRemoteHost(h)
	if !b.connecting {
		t.Fatal("expected remote host to be connecting")
	}
	b.openBoard(settingsBoardID)
	a.openBoard(taskBoardID)
	if b.connecting || b.host.snapshot().Connected {
		t.Fatalf("canceled dial unexpectedly auto-reconnected on module return: connecting=%v snap=%+v", b.connecting, b.host.snapshot())
	}
}

func TestHostNavigationNewMenu(t *testing.T) {
	for _, label := range []string{"新建工作区…", "派发任务…", "连接手机…"} {
		t.Run(label, func(t *testing.T) {
			v, a, b := hostViewsFixture(t)
			b.projects = []project{{ID: "remote-p", Name: "Remote project"}}
			b.workspaces = []workspace{{ID: "remote-w", Project: "Remote project", Label: "Remote project/main"}}
			b.selected = "remote-w"
			v.activate("remote")
			b.openSettings()
			tt := newTester(func(c *ui.Context) {
				ui.Row(c).Fill().Children(func() { v.active.Load().navigation(c) })
			}, 900, 560)
			settle(tt)
			if err := tt.Click("新建"); err != nil {
				t.Fatal(err)
			}
			settle(tt)
			if v.active.Load() != a {
				t.Fatal("collecting menu items switched host")
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			settle(tt)
			switch label {
			case "新建工作区…":
				if v.active.Load() != b || !b.creating || b.newRequest.app != b || b.newProject != "Remote project" || a.creating {
					t.Fatal("workspace form used local host")
				}
			case "派发任务…":
				if v.active.Load() != b || !b.dispatching || b.dWorkspace != "Remote project/main" || a.dispatching {
					t.Fatal("dispatch form used local host")
				}
			case "连接手机…":
				if v.active.Load() != a || a.selected != mobileBoardID || v.target != b {
					t.Fatal("phone form changed workspace target")
				}
			}
		})
	}
}

func TestHostNavigationChatContextMenu(t *testing.T) {
	v, a, b := hostViewsFixture(t)
	b.chatDir = "/remote/chat"
	b.workspaces = []workspace{{ID: "chat-w", Project: "Remote chat", Kind: "main", Path: b.chatDir}}
	v.activate("remote")
	b.openSettings()
	tt := newTester(func(c *ui.Context) {
		ui.Row(c).Fill().Children(func() { v.active.Load().navigation(c) })
	}, 900, 560)
	settle(tt)
	if err := tt.RightClick("聊天"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if v.active.Load() != a {
		t.Fatal("opening context menu switched host")
	}
	if err := tt.Click("在此项目新建工作区…"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if v.active.Load() != b || !b.creating || b.newRequest.app != b || b.newProject != "Remote chat" || a.creating {
		t.Fatal("chat menu opened a hidden or local dialog")
	}
}

func TestPreviewHostNavigation(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		t.Run(fmt.Sprint(dark), func(t *testing.T) {
			a, b := previewApp(t), previewApp(t)
			offlineClient(t, a)
			offlineClient(t, b)
			a.home, b.home = "", ""
			previewDiscussion(a)
			a.disc.panel = false
			b.selected, b.state.LastWorkspace = "w9", "w9"
			b.projects[0].Name = "Remote machine"
			a.hostMenu.hosts = []remote.Host{{ID: "remote", Name: "Office Mac", Target: "office"}}
			v := newHostViews(a)
			v.add("remote", b)
			v.activate("remote")
			tt := newTester(v.build, 1200, 760)
			tt.SetDark(dark)
			settle(tt)
			savePNG(t, tt, fmt.Sprintf("host-navigation-remote-%t.png", dark))
			if err := tt.Click("讨论（本机）"); err != nil {
				t.Fatal(err)
			}
			settle(tt)
			if v.active.Load() != a || !tt.HasText("重做登录流程") {
				t.Fatal("local discussion did not render")
			}
			savePNG(t, tt, fmt.Sprintf("host-navigation-discussions-%t.png", dark))
			if err := tt.Click("Office Mac"); err != nil {
				t.Fatal(err)
			}
			settle(tt)
			if v.active.Load() != b || !tt.HasText("Remote machine") || b.selected != "w9" {
				t.Fatal("remote workspace did not return")
			}
		})
	}
}
