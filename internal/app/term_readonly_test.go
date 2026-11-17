package app

import (
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// clearTerminal 是快捷键和自绘菜单共用的 RPC 路径，不能只测试插件的 OnClear。
func TestReadOnlyTerminalClearKey(t *testing.T) {
	a := previewApp(t)
	conn := recConn{done: make(chan struct{}), in: make(chan []byte, 16)}
	defer close(conn.done)
	v := &termView{id: "t1"}
	term, err := terminal.New(terminal.Options{Conn: conn, ReadOnly: true,
		OnKey: func(mods ui.Modifiers, key ui.Key) bool { return a.termKey(v, v.id, mods, key) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	v.term = term
	a.panes[0] = v
	// 无 Client：只读操作必须在启动 RPC 协程之前结束。
	a.c = nil
	term.Feed([]byte("keep this screen"))
	tt := newTester(func(c *ui.Context) { terminal.View(c, term).Grow(1) }, 600, 300)
	tt.ClickAt(20, 20)
	tt.Key(ui.Cmd, ui.KeyK)
	a.clearTerminal(appL(), v, v.id)
	if term.Text() != "keep this screen" {
		t.Fatalf("screen cleared: %q", term.Text())
	}
	select {
	case b := <-conn.in:
		t.Fatalf("clear sent input %q", b)
	default:
	}
}
