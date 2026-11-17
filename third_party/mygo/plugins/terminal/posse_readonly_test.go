package terminal

import (
	"fmt"
	"image/png"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// Posse: 用通道控制握手，不靠休眠猜读协程走到了哪一步。
type viewerTestConn struct {
	net.Conn
	gate            chan struct{}
	reading         chan struct{}
	once            sync.Once
	viewer          bool
	cols, rows      int
	snapshotGate    chan struct{}
	snapshotWaiting chan struct{}
	snapshot        []byte
}

func (c *viewerTestConn) TerminalState() (int, int, bool, error) {
	<-c.gate
	return c.cols, c.rows, c.viewer, nil
}

func (c *viewerTestConn) TerminalSnapshot() ([]byte, error) {
	if c.snapshotGate != nil {
		close(c.snapshotWaiting)
		<-c.snapshotGate
	}
	return c.snapshot, nil
}

func (c *viewerTestConn) Read(p []byte) (int, error) {
	c.once.Do(func() { close(c.reading) })
	return c.Conn.Read(p)
}

func viewerTestWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Minute):
		t.Fatal("terminal event did not arrive")
	}
}

func newViewerTest(t *testing.T, viewer bool, opts Options) (*Terminal, *viewerTestConn, net.Conn, <-chan []byte) {
	t.Helper()
	local, peer := net.Pipe()
	conn := &viewerTestConn{Conn: local, gate: make(chan struct{}), reading: make(chan struct{}), viewer: viewer, cols: 20, rows: 6}
	opts.Conn = conn
	term, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan []byte, 1)
	go func() { b, _ := io.ReadAll(peer); out <- b }()
	t.Cleanup(func() { term.Close(); peer.Close() })
	return term, conn, peer, out
}

func TestPosseViewerGridAndInput(t *testing.T) {
	var pasted, cleared int
	term, conn, _, out := newViewerTest(t, true, Options{
		OnPaste: func(transfer.Data) bool { pasted++; return true },
		OnClear: func(Clear) bool { cleared++; return true },
	})
	if !term.ReadOnly() {
		t.Fatal("input enabled before handshake")
	}
	term.Send([]byte("before handshake"))
	close(conn.gate)
	viewerTestWait(t, conn.reading)
	if cols, rows := term.Size(); cols != 20 || rows != 6 {
		t.Fatalf("grid = %dx%d", cols, rows)
	}
	term.Feed([]byte("12345678901234567890X"))
	term.mu.Lock()
	row, _ := term.term.RowText(1)
	term.mu.Unlock()
	if !strings.HasPrefix(string(row), "X") {
		t.Fatalf("snapshot wrapped at wrong grid: %q", string(row))
	}

	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Grow(1) }, 500, 260)
	if cols, rows := term.Size(); cols != 20 || rows != 6 {
		t.Fatalf("view resized viewer to %dx%d", cols, rows)
	}
	for i := range 40 {
		term.Feed([]byte(fmt.Sprintf("\r\nline %02d", i)))
	}
	term.Feed([]byte("\x1b[2J\x1b[Halpha beta\r\ngamma delta\x1b[?1000h\x1b[?1006h\x1b[?1004h"))
	tt.Frame()
	v := term.v
	x, y := float32(v.ox)+float32(v.cellW)/2, float32(v.oy)+float32(v.cellH)/2
	tt.Press(x, y)
	tt.Move(x+float32(9*v.cellW), y)
	tt.Release(x+float32(9*v.cellW), y)
	tt.Frame()
	selection := term.Selection()
	if dir := os.Getenv("POSSE_PREVIEW_DIR"); dir != "" {
		for _, dark := range []bool{false, true} {
			tt.SetDark(dark)
			tt.Frame()
			f, err := os.Create(filepath.Join(dir, fmt.Sprintf("terminal-viewer-dark-%t.png", dark)))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(f, tt.Image())
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if !strings.Contains(selection, "alpha") {
		t.Fatalf("mouse tracking prevented selection: %q", selection)
	}
	tt.Key(ui.Cmd, ui.KeyC)
	if tt.Clipboard() != selection {
		t.Fatalf("copy = %q, want %q", tt.Clipboard(), selection)
	}
	term.SelectAll()
	selection = term.Selection()
	tt.Type("ignored")
	tt.Key(0, ui.KeyEnter)
	if term.Selection() != selection {
		t.Fatal("ignored input cleared selection")
	}
	tt.SetClipboard("do not paste")
	term.PasteClipboard()
	tt.Key(ui.Cmd, ui.KeyV)
	tt.Key(ui.Cmd, ui.KeyK)
	term.ClearScreen()
	term.ClearScrollback()
	term.PasteData(transfer.New(transfer.NewItem(transfer.Lazy(transfer.Text, func() ([]byte, error) {
		t.Error("viewer read clipboard provider")
		return []byte("secret"), nil
	}))))
	if pasted != 0 || cleared != 0 {
		t.Fatalf("write callbacks: paste=%d clear=%d", pasted, cleared)
	}
	term.Feed([]byte("\x1b[c\x1b[6n\x1b]10;?\x07\x1b]52;c;?\x07\x1b]52;c;c2VjcmV0\x07"))
	tt.Frame()
	if tt.Clipboard() != "do not paste" {
		t.Fatal("program changed viewer clipboard")
	}
	tt.Scroll(x, y, 0, -100)
	tt.Frame()
	term.mu.Lock()
	atBottom := term.term.AtBottom()
	term.mu.Unlock()
	if atBottom {
		t.Fatal("viewer wheel did not scroll local history")
	}
	term.Feed([]byte("\x1b[?1049h\x1b[?1007h"))
	tt.Scroll(x, y, 0, 100)
	tt.SetSize(300, 160)
	if cols, rows := term.Size(); cols != 20 || rows != 6 {
		t.Fatalf("resized viewer = %dx%d", cols, rows)
	}
	term.Close()
	if b := <-out; len(b) != 0 {
		t.Fatalf("viewer wrote %q", b)
	}
}

func TestPosseHandshakeDropsEarlyInput(t *testing.T) {
	term, conn, _, out := newViewerTest(t, false, Options{})
	term.Send([]byte("early"))
	v := &view{t: term}
	v.keyDown(ui.InputEvent{Key: ui.KeyA})
	close(conn.gate)
	viewerTestWait(t, conn.reading)
	if term.ReadOnly() {
		t.Fatal("controller stayed read-only")
	}
	term.Feed([]byte("\x1b[>3u"))
	v.typed("a")
	v.keyUp(ui.InputEvent{Key: ui.KeyA})
	v.keyUp(ui.InputEvent{Key: ui.KeyB})
	term.Send([]byte("accepted"))
	written := make(chan struct{})
	term.in.pushDone(nil, func() { close(written) })
	viewerTestWait(t, written)
	term.Close()
	if b := <-out; string(b) != "\x1b[98;1:3uaccepted" {
		t.Fatalf("controller wrote %q", b)
	}
}
