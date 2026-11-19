package app

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestNavigationModulesAndNotices(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	previewDiscussion(a)
	a.disc.panel = false
	tt := newPreviewTester(a, true)
	if _, ok := tt.Find("feat-login"); ok {
		t.Fatal("project tree shown in discussions")
	}
	if _, ok := tt.Find("讨论列表"); !ok {
		t.Fatal("discussion sidebar missing")
	}
	a.err = "navigation test error"
	tt.Key(ui.Cmd, ui.KeyB)
	settle(tt)
	for _, label := range []string{"工作区", "讨论", "navigation test error"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("%q missing with sidebar closed", label)
		}
	}
	if _, ok := tt.Find("讨论列表"); ok {
		t.Fatal("list still shown")
	}
	if err := tt.Click("技能"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.sidebarSlot() {
		t.Fatal("unrelated sidebar shown on skills page")
	}
	if err := tt.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if !a.state.SidebarHidden {
		t.Fatal("module switch changed sidebar preference")
	}
}

func TestChatSidebarArrowKeys(t *testing.T) {
	a := previewApp(t)
	a.chatDir = a.workspaces[0].Path
	a.selected = a.workspaces[0].ID
	tt := newTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.sidebar(c) })
	}, 300, 760)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("Codex"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.activeTerm[a.selected] != "t2" {
		t.Fatal("chat session was not selected")
	}
	i := a.navCur
	if i < 0 || i+1 >= len(a.navOrder) {
		t.Fatal("chat fixture has no next session")
	}
	want := a.navOrder[i+1].term
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if a.activeTerm[a.selected] != want {
		t.Fatalf("down selected %q, want %q", a.activeTerm[a.selected], want)
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if a.activeTerm[a.selected] != "t2" {
		t.Fatal("up did not return to previous chat session")
	}
}

func TestNavigationConnectionErrorWithoutSidebar(t *testing.T) {
	a := previewApp(t)
	a.selected, a.loaded, a.err = settingsBoardID, false, "offline"
	tt := newPreviewTester(a, true)
	if !tt.HasText("offline") {
		t.Fatal("connection error hidden outside workspace module")
	}
}

func TestTaskListVirtualized(t *testing.T) {
	a := previewApp(t)
	now := time.Now()
	a.tasks = nil
	for i := range 80 {
		a.tasks = append(a.tasks, taskInfo{
			ID:        fmt.Sprintf("bulk-%03d", i),
			Title:     fmt.Sprintf("bulk-task-%03d", i),
			Agent:     "codex",
			Workspace: "Apps/main",
			Status:    "done",
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		})
	}
	a.indexTasks()
	a.selected, a.taskHome = taskBoardID, true
	tt := newPreviewTesterSize(a, true, 900, 700)
	if _, ok := tt.Find("bulk-task-000"); !ok {
		t.Fatal("first task missing")
	}
	if _, ok := tt.Find("bulk-task-079"); ok {
		t.Fatal("offscreen task was built")
	}
}

func TestPreviewNavigation(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		a := previewApp(t)
		a.state.TopTiles = []string{"w1", historyBoardID, "w6"}
		savePNG(t, newPreviewTester(a, dark), "navigation-workspaces-"+suffix+".png")
		b := previewApp(t)
		previewDiscussion(b)
		b.disc.panel, b.disc.quote = false, ""
		savePNG(t, newPreviewTester(b, dark), "navigation-discussions-"+suffix+".png")
		b.state.SidebarHidden = true
		b.err = "Connection interrupted"
		savePNG(t, newPreviewTester(b, dark), "navigation-hidden-"+suffix+".png")
		narrow := previewApp(t)
		previewDiscussion(narrow)
		savePNG(t, newPreviewTesterSize(narrow, dark, 900, 560), "navigation-narrow-"+suffix+".png")
	}
}

// 从没有侧栏的页面切回工作区，侧栏下一帧就整个在，不滑进来；点开关收起才走动画。
func TestModuleSwitchSidebarNoSlide(t *testing.T) {
	a := previewApp(t)
	tt := newPreviewTester(a, true)
	if err := tt.Click("技能"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.sidebarShown != 0 {
		t.Fatalf("sidebar shown on skills page: %v", a.sidebarShown)
	}
	if err := tt.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.sidebarShown != 1 {
		t.Fatalf("sidebar slid in after module switch: %v", a.sidebarShown)
	}
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()
	pass(tt, 50*time.Millisecond)
	if a.sidebarShown <= 0 || a.sidebarShown >= 1 {
		t.Fatalf("toggle no longer animates: %v", a.sidebarShown)
	}
}

// 任务、历史和工作区一样有模块侧栏：列表在侧栏里，开关能收起，切过去不滑动。
func TestTaskAndHistorySidebar(t *testing.T) {
	a := previewApp(t)
	tt := newPreviewTester(a, true)
	for _, page := range []string{"任务", "历史"} {
		if err := tt.Click(page); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if !a.sidebarSlot() || a.sidebarShown != 1 {
			t.Fatalf("%s: sidebar not shown at once: slot=%v shown=%v", page, a.sidebarSlot(), a.sidebarShown)
		}
		settle(tt)
	}
	if _, ok := tt.Find("会话列表"); !ok {
		t.Fatal("history list not in sidebar")
	}
	tt.Key(ui.Cmd, ui.KeyB)
	settle(tt)
	if a.sidebarSlot() {
		t.Fatal("history sidebar did not collapse")
	}
	if _, ok := tt.Find("会话列表"); ok {
		t.Fatal("history list still shown with sidebar collapsed")
	}
	// 收着时按搜索键先展开侧栏
	tt.Key(ui.Cmd, ui.KeyF)
	settle(tt)
	if a.state.SidebarHidden || !a.sidebarSlot() {
		t.Fatal("search shortcut did not open sidebar")
	}
}

// 聊天有自己的入口，不再列进工作区的项目树和「运行中」；聊天侧栏照常列它的会话。
func TestChatHiddenFromWorkspaceTree(t *testing.T) {
	a := previewApp(t)
	a.selected = a.workspaces[0].ID
	tt := newTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.sidebar(c) })
	}, 300, 1200)
	tt.Frame()
	if !tt.HasText("Apps") {
		t.Fatal("fixture project missing")
	}
	if !tt.HasText("Chat") {
		t.Fatal("chat project should be listed before chat dir is set")
	}
	a.chatDir = "/Users/x/Chat"
	tt.Frame()
	if tt.HasText("Chat") {
		t.Fatal("chat project still listed in workspace tree")
	}
	a.state.SidebarGroup = sidebarByStatus
	a.runOrder = nil
	tt.Frame()
	for _, w := range a.workspaces {
		if w.ID == "w2" {
			for _, term := range w.Terminals {
				if term.Title != "" && tt.HasText(term.Title) {
					t.Fatalf("chat session %q listed in running view", term.Title)
				}
			}
		}
	}
	a.selected = "w2"
	tt.Frame()
	if !a.isChat(a.selected) {
		t.Fatal("chat workspace not recognized")
	}
}
