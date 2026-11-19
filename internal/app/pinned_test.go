package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestNavigationMigration(t *testing.T) {
	s := uiState{TopTiles: []string{historyBoardID, "w1", taskBoardID, "gone", "w1", mobileBoardID}}
	migrateNavigation(&s)
	if !slices.Equal(s.NavigationOrder, []string{historyBoardID, taskBoardID, mobileBoardID}) || !slices.Equal(s.PinnedWorkspaces, []string{"w1", "gone"}) || s.TopTiles != nil {
		t.Fatalf("migration: %+v", s)
	}
	migrateNavigation(&s)
	if !slices.Equal(s.PinnedWorkspaces, []string{"w1", "gone"}) {
		t.Fatal("migration repeated")
	}
}

func TestChatNavigation(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	w := &a.workspaces[0]
	a.chatDir = w.Path
	a.selected = "w6"
	tt := newTester(a.build, 1200, 1400)
	tt.Frame()
	tt.Frame()
	if err := tt.Click("聊天"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if a.selected != w.ID {
		t.Fatalf("chat selected=%q", a.selected)
	}
	if err := tt.Click("工作区"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w6" {
		t.Fatalf("workspace selected=%q", a.selected)
	}
}

func TestWorkspaceSidebarHasNoPinnedSection(t *testing.T) {
	a := previewApp(t)
	a.state.PinnedWorkspaces = []string{a.workspaces[0].ID, "w6"}
	tt := newTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.sidebar(c) })
	}, 320, 760)
	tt.Frame()
	if _, ok := tt.Find("固定"); ok {
		t.Fatal("pinned section still shown")
	}
	if _, ok := tt.Find("项目"); !ok {
		t.Fatal("project tree missing")
	}
	if _, ok := tt.Find("固定到顶部"); ok {
		t.Fatal("pin action still shown")
	}
}
