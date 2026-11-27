package app

import (
	"fmt"
	"os"
	"testing"

	"github.com/egoist/mygo/ui"
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
		// 点击左侧导航栏里的远端主机磁贴切回远端工作区并重新发起连接
		if err := tt.Click("Office Mac"); err != nil {
			t.Fatal(err)
		}
		settle(tt)
		awaitHostUpdate(t, b)
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
			if err := tt.Click("工作区"); err != nil {
				t.Fatal(err)
			}
			settle(tt)
			if v.active.Load() != b || !tt.HasText("Remote machine") || b.selected != "w9" {
				t.Fatal("remote workspace did not return")
			}
		})
	}
}
