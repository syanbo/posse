package app

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 派发对话框里点进另一个输入框，焦点框换色，对话框自己不动。
func TestDispatchFocusKeepsPlace(t *testing.T) {
	a := previewApp(t)
	a.openDispatch()
	tt := ui.NewTester(a.build, 1200, 760)
	settle(tt)
	title, ok := tt.Find("派发任务")
	if !ok {
		t.Fatal("no dialog")
	}
	spec := "要做什么、范围在哪、不能动什么、怎样算完成。worker 看不到你和其他 agent 的对话，这里要写全。"
	if err := tt.Click(spec); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	again, _ := tt.Find("派发任务")
	if math.Abs(float64(again.X-title.X)) > 0.5 || math.Abs(float64(again.Y-title.Y)) > 0.5 {
		t.Fatalf("dialog jumped %v → %v", title, again)
	}
	hint, ok := tt.Find("worker 在这个工作区的目录里工作。")
	if !ok {
		t.Fatal("no hint")
	}
	if err := tt.Click("标题，比如 修复登录页闪退"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	hint2, _ := tt.Find("worker 在这个工作区的目录里工作。")
	if math.Abs(float64(hint2.Y-hint.Y)) > 0.5 {
		t.Fatalf("hint jumped %v → %v", hint, hint2)
	}
}

// 回滚对话框拖标题移动、拖右下角拉伸；再打开时尺寸沿用、位置回到居中。
func TestDialogMoveResize(t *testing.T) {
	a := previewApp(t)
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openRollback(v)
	settle(tt)
	title, ok := tt.Find("回滚改动")
	if !ok {
		t.Fatal("no title")
	}
	drag := func(x, y, dx, dy float32) {
		tt.Press(x, y)
		for i := 1; i <= 4; i++ {
			tt.Move(x+dx*float32(i)/4, y+dy*float32(i)/4)
			tt.Frame()
		}
		tt.Release(x+dx, y+dy)
		tt.Frame()
	}
	drag(title.X+title.W/2, title.Y+title.H/2, 100, 40)
	moved, _ := tt.Find("回滚改动")
	if d := moved.X - title.X; d < 99 || d > 101 || moved.Y-title.Y < 39 || moved.Y-title.Y > 41 {
		t.Fatalf("title moved %v → %v", title, moved)
	}
	box := &a.rollbackBox
	// 标题上面的内边距也能拖
	drag(moved.X+moved.W/2, box.last.Y+12, -20, 10)
	if r, _ := tt.Find("回滚改动"); math.Abs(float64(r.X-(moved.X-20))) > 0.5 || math.Abs(float64(r.Y-(moved.Y+10))) > 0.5 {
		t.Fatalf("dragging top padding: %v → %v", moved, r)
	}
	drag(moved.X+moved.W/2-20, box.last.Y+12, 20, -10)
	w0, h0 := box.last.W, box.last.H
	// Bounds 含拖动的平移，右下角的把手在面板角上
	for _, p := range []struct {
		x, y float32
		cur  ui.Cursor
	}{{box.last.X + w0 - 4, box.last.Y + h0 - 4, ui.CursorResizeNWSE}, {box.last.X + w0 - 3, box.last.Y + h0/2, ui.CursorResizeEW}, {box.last.X + w0/2, box.last.Y + 3, ui.CursorResizeNS}} {
		tt.Move(p.x, p.y)
		tt.Frame()
		if tt.Cursor() != p.cur {
			t.Fatalf("cursor at %v,%v: %v want %v", p.x, p.y, tt.Cursor(), p.cur)
		}
	}
	drag(box.last.X+w0-4, box.last.Y+h0-4, 60, 30)
	for range 3 {
		tt.Frame()
	}
	if box.last.W < w0+59 || box.last.W > w0+61 || box.last.H < h0+29 || box.last.H > h0+31 {
		t.Fatalf("resize %vx%v → %vx%v", w0, h0, box.last.W, box.last.H)
	}
	// 左上角不动
	after, _ := tt.Find("回滚改动")
	if math.Abs(float64(after.X-moved.X)) > 0.5 || math.Abs(float64(after.Y-moved.Y)) > 0.5 {
		t.Fatalf("title shifted on resize %v → %v", moved, after)
	}
	a.rollback.open = false
	tt.Frame()
	a.openRollback(v)
	until(t, tt, "reopened", func() bool { return tt.HasText("回滚改动") })
	settle(tt)
	// 拉宽后再居中，标题左移半个增量
	if again, _ := tt.Find("回滚改动"); math.Abs(float64(again.X-(title.X-30))) > 0.5 || box.last.W < w0+59 {
		t.Fatalf("reopen: title %v want x %v, width %v", again, title.X, box.last.W)
	}
}
