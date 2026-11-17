package app

import (
	"fmt"
	"slices"
	"testing"
)

func TestPinnedWorkspacesDrag(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	tt := newTester(a.build, 1200, 1400)
	tt.Frame()
	tt.Frame()
	center := func(s string) (float32, float32) {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("%q missing: %v", s, tt.Texts())
		}
		return r.X + r.W/2, r.Y + r.H/2
	}
	fx, fy := center("feat-login")
	tx, ty := center("固定")
	drag(tt, fx, fy, tx, ty)
	settle(tt)
	if !slices.Equal(a.state.PinnedWorkspaces, []string{"w6"}) {
		t.Fatalf("pin: %v", a.state.PinnedWorkspaces)
	}
	fx, fy = center("Chat")
	tx, ty = center("固定")
	drag(tt, fx, fy, tx, ty)
	settle(tt)
	if !a.pinned("w2") {
		t.Fatalf("pin project: %v", a.state.PinnedWorkspaces)
	}
	fx, fy = center("Chat")
	tx, ty = center("项目")
	drag(tt, fx, fy, tx, ty+100)
	settle(tt)
	if a.pinned("w2") {
		t.Fatal("drag to tree did not unpin")
	}
}

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

func TestDeletedPinsDoNotConsumeCapacity(t *testing.T) {
	a := previewApp(t)
	for i := range topTilesMax {
		a.state.PinnedWorkspaces = append(a.state.PinnedWorkspaces, fmt.Sprintf("gone-%d", i))
	}
	a.loaded = false
	if a.canPin() || len(a.topOrder()) != topTilesMax {
		t.Fatal("offline snapshot should retain pinned records")
	}
	a.loaded = true
	if !a.canPin() || len(a.topOrder()) != 0 || len(a.state.PinnedWorkspaces) != topTilesMax {
		t.Fatal("missing pins should not consume capacity or be erased by a read")
	}
	a.pin("w1")
	if !slices.Equal(a.state.PinnedWorkspaces, []string{"w1"}) {
		t.Fatalf("explicit pin did not reclaim absent records: %v", a.state.PinnedWorkspaces)
	}
}

func TestDropTileKeepsHidden(t *testing.T) {
	a := previewApp(t)
	order, shown := []string{"w1", "gone", "w2", "w6"}, []string{"w1", "w2", "w6"}
	a.topSlot = 1
	got := a.dropTile(tileDrag{"w6"}, shown, order)
	if !slices.Equal(got, []string{"w6", "w1", "w2"}) || !slices.Equal(a.state.PinnedWorkspaces, []string{"w6", "gone", "w1", "w2"}) {
		t.Fatalf("shown=%v saved=%v", got, a.state.PinnedWorkspaces)
	}
	a.topSlot = 0
	if got := a.dropTile(tileDrag{"w2"}, shown, order); !slices.Equal(got, shown) {
		t.Fatalf("without slot: %v", got)
	}
}

func TestChatNavigation(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	w := &a.workspaces[0]
	a.chatDir = w.Path
	if !a.isChat(w.ID) || a.pinnable(w.ID) {
		t.Fatal("chat can be pinned")
	}
	a.state.TopTiles = []string{w.ID, historyBoardID}
	if slices.Contains(a.topOrder(), w.ID) {
		t.Fatal("chat shown twice")
	}
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
