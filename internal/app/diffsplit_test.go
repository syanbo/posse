package app

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 左右对照的分界线能拖：拖过去之后比例跟着变，松手后不再报告拖动中，拖动条跟到新位置
func TestDiffSplitDrag(t *testing.T) {
	d := editor.DiffState{Layout: editor.DiffSplit, Old: editor.NewBuffer("old line\n" + strings.Repeat("same\n", 5)), New: editor.NewBuffer("new line\n" + strings.Repeat("same\n", 5))}
	tt := newTester(func(c *ui.Context) {
		editor.DiffView(c, &d).Fill()
	}, 600, 300)
	tt.Frame()
	tt.Frame()
	tt.Press(300, 150)
	tt.Move(320, 150)
	tt.Frame()
	tt.Move(420, 150)
	tt.Frame()
	if !d.SplitDragging || d.Split < 0.65 || d.Split > 0.75 {
		t.Fatalf("dragging %v split %v", d.SplitDragging, d.Split)
	}
	tt.Release(420, 150)
	tt.Frame()
	tt.Frame()
	if d.SplitDragging || d.Split < 0.65 {
		t.Fatalf("after release: dragging %v split %v", d.SplitDragging, d.Split)
	}
	// 分界线挪过去之后，再从新位置拖才算数：原来的位置已经不是分界线
	tt.Press(300, 150)
	tt.Move(200, 150)
	tt.Frame()
	if d.SplitDragging {
		t.Fatal("old divider position still drags")
	}
	tt.Release(200, 150)
}
