package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/rpc"
)

// recConn 记下终端发给程序的字节。
type recConn struct {
	done chan struct{}
	in   chan []byte
}

func (c recConn) Read(p []byte) (int, error) { <-c.done; return 0, io.EOF }
func (c recConn) Write(p []byte) (int, error) {
	c.in <- append([]byte(nil), p...)
	return len(p), nil
}
func (c recConn) Close() error { return nil }

func recTerminal(t *testing.T, a *app) (*termView, recConn) {
	t.Helper()
	conn := recConn{done: make(chan struct{}), in: make(chan []byte, 16)}
	t.Cleanup(func() { close(conn.done) })
	term, err := terminal.New(terminal.Options{Conn: conn, Font: terminal.Font{Family: termFontFamily, Size: defaultFontSize}})
	if err != nil {
		t.Skip("terminal unavailable:", err)
	}
	t.Cleanup(func() { term.Close() })
	v := &termView{id: "t1", term: term}
	a.view = v
	return v, conn
}

func sent(t *testing.T, conn recConn) string {
	t.Helper()
	select {
	case b := <-conn.in:
		return string(b)
	case <-time.After(2 * time.Second):
		t.Fatal("nothing sent")
		return ""
	}
}

// 编辑键发 readline 认的控制字符；Shift+Enter 只在 Claude、Codex 里换成 Ctrl+J。
func TestTermEditKeys(t *testing.T) {
	a := previewApp(t)
	v, conn := recTerminal(t, a)
	for _, c := range []struct {
		mods ui.Modifiers
		key  ui.Key
		want string
	}{
		{ui.Cmd, ui.KeyLeft, "\x01"},
		{ui.Cmd, ui.KeyRight, "\x05"},
		{ui.Cmd, ui.KeyBackspace, "\x15"},
		{ui.Alt, ui.KeyLeft, "\x1bb"},
		{ui.Alt, ui.KeyRight, "\x1bf"},
		{ui.Alt, ui.KeyBackspace, "\x1b\x7f"},
		{ui.Shift, ui.KeyEnter, "\n"}, // t1 是 Claude
	} {
		if !a.termKey(v, "t1", c.mods, c.key) {
			t.Fatalf("%v %v not taken", c.mods, c.key)
		}
		if got := sent(t, conn); got != c.want {
			t.Fatalf("%v %v sent %q, want %q", c.mods, c.key, got, c.want)
		}
	}
	if a.termKey(v, "t3", ui.Shift, ui.KeyEnter) {
		t.Fatal("Shift+Enter in a plain shell should stay a plain Enter")
	}
	if a.termKey(v, "t1", 0, ui.KeyLeft) || a.termKey(v, "t1", ui.Cmd, ui.KeyC) {
		t.Fatal("other keys go on to the terminal")
	}
	// 全屏程序里编辑键原样交给程序
	v.term.Feed([]byte("\x1b[?1049h"))
	if a.termKey(v, "t1", ui.Cmd, ui.KeyLeft) {
		t.Fatal("full-screen programs get their keys")
	}
}

// ⌘K 由常驻进程清屏并让程序重画；常驻进程是旧版本（没有 terminal.clear）时只清界面这边并发 Ctrl+L。
func TestTermClear(t *testing.T) {
	a := previewApp(t)
	v, conn := recTerminal(t, a)
	tt := ui.NewTester(a.build, 800, 600)
	sock := filepath.Join(t.TempDir(), "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	srv := rpc.NewServer()
	got := make(chan map[string]any, 1)
	srv.Handle("terminal.clear", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p map[string]any
		json.Unmarshal(raw, &p)
		got <- p
		return nil, nil
	})
	go srv.Serve(ctx, ln)
	a.c = &client.Client{Socket: sock}
	if !a.termKey(v, "t1", ui.Cmd, ui.KeyK) {
		t.Fatal("⌘K not taken")
	}
	select {
	case p := <-got:
		if p["terminal"] != "t1" || p["redraw"] != true {
			t.Fatalf("params %v", p)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("terminal.clear not called")
	}

	// 旧常驻进程
	cancel()
	sock2 := filepath.Join(t.TempDir(), "s2")
	ln2, err := net.Listen("unix", sock2)
	if err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithCancel(context.Background())
	t.Cleanup(cancel2)
	go rpc.NewServer().Serve(ctx2, ln2)
	a.c = &client.Client{Socket: sock2}
	v.term.Feed([]byte("old output\r\n"))
	a.termKey(v, "t1", ui.Cmd, ui.KeyK)
	until(t, tt, "local clear", func() bool { return len(conn.in) > 0 })
	if s := sent(t, conn); s != "\x0c" {
		t.Fatalf("sent %q, want Ctrl+L", s)
	}
	if a.err != "" {
		t.Fatalf("an old daemon is not an error: %q", a.err)
	}
}

// ⌘⇧] / ⌘⇧[ 按标签栏顺序切换，终端之后是文件，到头绕回来。
func TestStepTab(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	v := a.files[w.ID]
	a.activeTerm[w.ID] = w.Terminals[len(w.Terminals)-1].ID
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
	tt.Frame()
	if !a.filesActive(w) || v.cur == nil {
		t.Fatal("after the last terminal comes the file")
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
	tt.Frame()
	if a.activeTerm[w.ID] != w.Terminals[0].ID {
		t.Fatalf("should wrap to the first terminal, got %q", a.activeTerm[w.ID])
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketLeft)
	tt.Frame()
	if !a.filesActive(w) {
		t.Fatal("back from the first terminal is the last tab")
	}
}
