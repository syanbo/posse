package app

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"slices"
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
	a.panes[0] = v
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
	tt := newTester(a.build, 800, 600)
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

// ⌘⇧] / ⌘⇧[ 按侧栏顺序切换：shell 之后是文件，再是 agent 会话，到头了切到侧栏里相邻的工作区。
func TestStepTab(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	v := a.files[w.ID]
	sessions := agentSessions(w.Terminals)
	if len(sessions) == 0 {
		t.Fatal("the fixture workspace has no agent sessions")
	}
	for _, term := range w.Terminals {
		if !slices.ContainsFunc(sessions, func(s terminalInfo) bool { return s.ID == term.ID }) {
			a.activeTerm[w.ID] = term.ID
		}
	}
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
	tt.Frame()
	if !a.filesActive(w) || v.cur == nil {
		t.Fatal("after the last shell comes the file")
	}
	for range sessions {
		tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
		tt.Frame()
		if a.selected != w.ID || a.filesActive(w) || a.selectedSession(w) == "" {
			t.Fatalf("after the file come the sessions, selected %q", a.selected)
		}
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
	tt.Frame()
	next := a.stepWorkspace(w, 1)
	if next == nil || a.selected != next.ID || a.selected == w.ID {
		t.Fatalf("after the last tab should move to the next workspace, selected %q", a.selected)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketLeft)
	tt.Frame()
	if tabs := a.tabOrder(w); a.selected != w.ID || a.activeTerm[w.ID] != tabs[len(tabs)-1].term {
		t.Fatalf("back from the next workspace is the last tab here, selected %q", a.selected)
	}
}

// 侧栏放不下时，切过去的那一行滚进可见区，绕回顶上时再滚回来。
func TestStepTabScrollsSidebar(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.activeTerm["w1"] = "t3"
	const height = 330
	tt := newTester(a.build, 1200, height)
	tt.Frame()
	visible := func(s string) bool {
		r, ok := tt.Find(s)
		return ok && r.Y >= 0 && r.Y+r.H <= height
	}
	if visible("feat-login") {
		t.Fatal("the fixture should start with feat-login below the fold")
	}
	for range 10 {
		tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
		tt.Frame()
		if a.selected == "w6" {
			break
		}
	}
	tt.Frame()
	if a.selected != "w6" || !visible("feat-login") {
		t.Fatalf("selected %q, feat-login visible %v", a.selected, visible("feat-login"))
	}
	// 绕回 Apps：先是 shell，再是第一个会话
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyBracketRight)
	tt.Frame()
	tt.Frame()
	if a.activeTerm[a.selected] != "t1" || !visible("重写 posse 界面") {
		t.Fatalf("after wrapping: %s/%s", a.selected, a.activeTerm[a.selected])
	}
}

// ⌘F 打开终端的查找栏：数命中、⌘G 往上跳、Esc 关掉清高亮；Claude 终端里搜不到时给出"在历史里搜"，点了带着词去历史页。
func TestTermFind(t *testing.T) {
	a := previewApp(t)
	v := a.panes[0]
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v.term.Feed([]byte("build ok\r\nBUILD failed\r\nrebuild\r\n"))
	if !a.termKey(v, "t1", ui.Cmd, ui.KeyF) || !v.find.open {
		t.Fatal("⌘F should open the find bar")
	}
	tt.Frame()
	tt.Type("build")
	until(t, tt, "matches", func() bool { st := v.term.SearchStatus(); return st.Total == 3 && !st.Searching })
	if st := v.term.SearchStatus(); st.Selected != 1 || !tt.HasText("1/3") {
		t.Fatalf("status %+v, texts %q", st, tt.Texts())
	}
	tt.Key(ui.Cmd, ui.KeyG)
	tt.Frame()
	if st := v.term.SearchStatus(); st.Selected != 2 {
		t.Fatalf("⌘G: selected %d", st.Selected)
	}
	// 查找栏开着时 ⇧⌘G 是往下，不去开关改动页
	tt.Key(ui.Cmd|ui.Shift, ui.KeyG)
	tt.Frame()
	if st := v.term.SearchStatus(); st.Selected != 1 || a.filesPanelOpen() {
		t.Fatalf("⇧⌘G: selected %d, files panel %v", st.Selected, a.filesPanelOpen())
	}

	// Esc 关掉、清高亮；再按 ⌘F 接着搜上次的词
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if v.find.open || v.term.SearchStatus().Text != "" {
		t.Fatal("Esc should close the find bar and end the search")
	}
	a.termKey(v, "t1", ui.Cmd, ui.KeyF)
	tt.Frame()
	if st := v.term.SearchStatus(); st.Text != "build" {
		t.Fatalf("reopened search %q", st.Text)
	}

	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("zzz-nowhere")
	until(t, tt, "no match", func() bool {
		st := v.term.SearchStatus()
		return st.Total == 0 && !st.Searching && tt.HasText("无结果")
	})
	if err := tt.Click("在历史里搜"); err != nil {
		t.Fatal(err)
	}
	if a.selected != historyBoardID || a.history.query != "zzz-nowhere" || historyAgents[a.history.agent] != "claude" {
		t.Fatalf("history: selected %q query %q agent %d", a.selected, a.history.query, a.history.agent)
	}
}

// 带着开着的查找栏的新终端（重新接入后 ensureView 从 carryFind 恢复的就是这样）：画过一帧后按原来的词搜，并选中最新的命中。
func TestTermFindCarried(t *testing.T) {
	a := previewApp(t)
	a.panes[0].find = termFind{open: true, query: "view"}
	tt := newTester(a.build, 1200, 760)
	until(t, tt, "searched", func() bool {
		st := a.panes[0].term.SearchStatus()
		return st.Text == "view" && st.Total == 2 && st.Selected == 1
	})
}

// ⌘F 之后、查找栏还没搭出来（输入框没拿到焦点）时打进终端的字接进搜索词，不发给程序；输入框拿到焦点后不再截。
func TestTermFindEarlyText(t *testing.T) {
	a := previewApp(t)
	v, conn := recTerminal(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v.find = termFind{query: "old"}
	a.termKey(v, "t1", ui.Cmd, ui.KeyF)
	if !a.termFindText(v, "1") || !a.termFindText(v, "9") || v.find.query != "old19" {
		t.Fatalf("early text: query %q", v.find.query)
	}
	// 同一段时间里的回车、退格、⌃C、方向键也不发给程序：退格删搜索词的最后一个字
	for _, k := range []struct {
		mods ui.Modifiers
		key  ui.Key
	}{{0, ui.KeyEnter}, {0, ui.KeyBackspace}, {ui.Ctrl, ui.KeyC}, {0, ui.KeyUp}} {
		if !a.termKey(v, "t1", k.mods, k.key) {
			t.Fatalf("%v %v went on to the program", k.mods, k.key)
		}
	}
	if v.find.query != "old1" {
		t.Fatalf("backspace: query %q", v.find.query)
	}
	// 出字的键和带 Command 的键不在这里拿
	if a.termKey(v, "t1", 0, ui.KeyA) || a.termKey(v, "t1", ui.Cmd, ui.KeyG) {
		t.Fatal("text and Command keys should not be taken here")
	}
	tt.Frame()
	if v.find.focus || a.termFindText(v, "x") {
		t.Fatal("text after the field has focus should go on to the terminal")
	}
	select {
	case b := <-conn.in:
		t.Fatalf("sent %q to the program", b)
	default:
	}
}
