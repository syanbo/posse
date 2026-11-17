package app

import (
	"os"
	"testing"

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
	if err := tt.Click("任务"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.sidebarSlot() {
		t.Fatal("unrelated sidebar shown on task page")
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

func TestPinnedSectionLeavesRoomForTree(t *testing.T) {
	a := previewApp(t)
	for _, w := range a.workspaces {
		a.pin(w.ID)
	}
	tt := newPreviewTesterSize(a, true, 900, 560)
	r, ok := tt.Find("固定")
	if !ok || r.Y > 250 {
		t.Fatalf("pinned section missing or misplaced: %v", r)
	}
	r, ok = tt.Find("项目")
	if !ok || r.Y > 300 {
		t.Fatalf("pinned section pushed tree outside viewport: %v", r)
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
