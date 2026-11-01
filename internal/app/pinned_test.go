package app

import (
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 顶上的磁贴：项目树里的工作区、单行项目拖上去，落在指针所在的格子；磁贴之间拖动换位置；
// 固定的拖回项目列表就取消，全局页面拖不走。
func TestTopTilesDrag(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	tt := ui.NewTester(a.build, 1200, 1400)
	tt.Frame()
	tt.Frame()
	settle := func() {
		for range 4 {
			tt.Advance(200 * time.Millisecond)
			tt.Frame()
		}
	}
	center := func(s string) (float32, float32) {
		t.Helper()
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("%q missing; texts: %v", s, tt.Texts())
		}
		return r.X + r.W/2, r.Y + r.H/2
	}
	check := func(step string, want ...string) {
		t.Helper()
		if !slices.Equal(a.state.TopTiles, want) {
			t.Fatalf("%s: %v, want %v", step, a.state.TopTiles, want)
		}
	}

	// 多工作区项目下面的 worktree 行拖到「历史」那格：占住第二格，历史往后让
	fx, fy := center("feat-login")
	tx, ty := center("历史")
	drag(tt, fx, fy, tx, ty)
	settle()
	check("pin the worktree", taskBoardID, "w6", historyBoardID, skillsBoardID, mobileBoardID)

	// 单行的项目拖到「手机」那格（第二行第一格），固定它的主工作区
	fx, fy = center("Chat")
	tx, ty = center("手机")
	drag(tt, fx, fy, tx, ty)
	settle()
	check("pin a project", taskBoardID, "w6", historyBoardID, skillsBoardID, "w2", mobileBoardID)

	// 「手机」拖到「任务」那格，排到最前
	fx, fy = center("手机")
	tx, ty = center("任务")
	drag(tt, fx, fy, tx, ty)
	settle()
	check("reorder", mobileBoardID, taskBoardID, "w6", historyBoardID, skillsBoardID, "w2")

	// 固定的 Chat 拖回项目列表就取消；全局页面拖下去不动
	r, _ := tt.Find("项目")
	fx, fy = center("Chat") // 磁贴在项目树的 Chat 上面，Find 找到第一个
	if fy > r.Y {
		t.Fatalf("expected the Chat tile above the project list")
	}
	drag(tt, fx, fy, fx, r.Y+300)
	settle()
	check("unpin", mobileBoardID, taskBoardID, "w6", historyBoardID, skillsBoardID)
	fx, fy = center("技能")
	drag(tt, fx, fy, fx, r.Y+300)
	settle()
	check("a global tile stays", mobileBoardID, taskBoardID, "w6", historyBoardID, skillsBoardID)
}

// 落下时不显示的（没有项目时的任务、还没报上来的工作区）留在原来的位置。
func TestDropTileKeepsHidden(t *testing.T) {
	a := previewApp(t)
	order := []string{taskBoardID, "gone", historyBoardID, skillsBoardID, mobileBoardID}
	shown := []string{historyBoardID, skillsBoardID, mobileBoardID}
	a.topSlot = 1 // 落在第一格
	got := a.dropTile(tileDrag{mobileBoardID}, shown, order)
	if want := []string{mobileBoardID, historyBoardID, skillsBoardID}; !slices.Equal(got, want) {
		t.Fatalf("shown = %v, want %v", got, want)
	}
	if want := []string{taskBoardID, "gone", mobileBoardID, historyBoardID, skillsBoardID}; !slices.Equal(a.state.TopTiles, want) {
		t.Fatalf("saved = %v, want %v", a.state.TopTiles, want)
	}
	// 没有空出来的格子时不动
	a.topSlot = 0
	if got := a.dropTile(tileDrag{historyBoardID}, shown, order); !slices.Equal(got, shown) {
		t.Fatalf("without a slot: %v", got)
	}
}
