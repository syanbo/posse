package app

import (
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// 按着 ⌘ 在触控板上滑动不缩放终端字号（NoWheelZoom），⌘= 仍然放大。
func TestCmdScrollDoesNotZoom(t *testing.T) {
	cols := func(noWheelZoom bool, act func(tt *ui.Tester)) (before, after int) {
		a := previewApp(t)
		done := make(chan struct{})
		t.Cleanup(func() { close(done) })
		term, err := terminal.New(terminal.Options{Conn: nopConn{done}, Font: a.termFont(), NoWheelZoom: noWheelZoom})
		if err != nil {
			t.Skip("terminal unavailable:", err)
		}
		t.Cleanup(func() { term.Close() })
		a.view = &termView{id: "t1", term: term}
		tt := newPreviewTester(a, false)
		tt.ClickAt(600, 300)
		tt.Frame()
		before, _ = term.Size()
		act(tt)
		for range 3 {
			tt.Frame()
		}
		after, _ = term.Size()
		return before, after
	}
	scroll := func(tt *ui.Tester) {
		for range 5 {
			tt.ScrollWith(ui.Cmd, 600, 300, 0, -80)
			tt.Frame()
		}
	}
	// 对照：不开时同样的滑动会放大，列数变少
	if b, a := cols(false, scroll); a >= b {
		t.Fatalf("without NoWheelZoom cols %d -> %d, want fewer", b, a)
	}
	if b, a := cols(true, scroll); a != b {
		t.Fatalf("Cmd+scroll zoomed: cols %d -> %d", b, a)
	}
	if b, a := cols(true, func(tt *ui.Tester) { tt.Key(ui.Cmd, ui.KeyEqual) }); a >= b {
		t.Fatalf("Cmd+= should still zoom: cols %d -> %d", b, a)
	}
}
