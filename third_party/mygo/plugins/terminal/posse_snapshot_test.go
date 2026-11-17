package terminal

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestPosseSnapshotKeepsHandshakeGrid(t *testing.T) {
	term, conn, _, _ := newViewerTest(t, false, Options{})
	conn.snapshotGate = make(chan struct{})
	conn.snapshotWaiting = make(chan struct{})
	// Posse: 超过读缓冲的快照，最后一行的 X 必须在 20 列处换行。
	conn.snapshot = []byte(strings.Repeat("\x1b[H12345678901234567890X", 5000))
	close(conn.gate)
	viewerTestWait(t, conn.snapshotWaiting)
	term.Resize(120, 40)
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Grow(1) }, 800, 600)
	tt.SetSize(900, 700)
	if !term.ReadOnly() {
		t.Fatal("input enabled before snapshot arrived")
	}
	close(conn.snapshotGate)
	viewerTestWait(t, conn.reading)
	term.mu.Lock()
	cols, rows := term.term.Size()
	row, _ := term.term.RowText(1)
	term.mu.Unlock()
	if cols != 20 || rows != 6 || !strings.HasPrefix(string(row), "X") {
		t.Fatalf("snapshot grid=%dx%d row=%q", cols, rows, string(row))
	}
	if term.ReadOnly() {
		t.Fatal("controller did not enable input")
	}
}

func TestPosseViewerFitsWholeGridAndLinks(t *testing.T) {
	term, conn, _, _ := newViewerTest(t, true, Options{OpenLink: func(string) bool { return true }})
	conn.cols, conn.rows = 80, 40
	close(conn.gate)
	viewerTestWait(t, conn.reading)
	url := "https://example.com/abcdefghijk"
	term.Feed([]byte("\x1b[?1049h\x1b[H" + strings.Repeat(" ", 60) + url + "\x1b[40;70HBOTTOM"))
	tt := ui.NewTester(func(c *ui.Context) { View(c, term).Grow(1) }, 400, 240)
	v := term.v
	if v.cols != 80 || v.rows != 40 {
		t.Fatalf("hit grid=%dx%d", v.cols, v.rows)
	}
	if v.ox+v.cols*v.cellW > int(400*v.scale) || v.oy+v.rows*v.cellH > int(240*v.scale) {
		t.Fatalf("grid clipped: cell=%dx%d scale=%v", v.cellW, v.cellH, v.scale)
	}
	term.mu.Lock()
	link, _, span := v.linkAt(5, 1)
	term.mu.Unlock()
	if link != url || span.row != 0 || span.rows != 2 {
		t.Fatalf("wrapped link=%q span=%+v", link, span)
	}
	x, y := float32(v.ox+72*v.cellW)/v.scale, float32(v.oy+39*v.cellH)/v.scale
	col, row := v.cellAt(x, y)
	if col != 72 || row != 39 {
		t.Fatalf("bottom hit=%d,%d", col, row)
	}
	tt.SetSize(800, 600)
	if cols, rows := term.Size(); cols != 80 || rows != 40 {
		t.Fatalf("window changed grid to %dx%d", cols, rows)
	}
}
