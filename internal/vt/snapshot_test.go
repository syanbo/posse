package vt

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
)

func loadLib(t *testing.T) {
	t.Helper()
	path, err := terminal.LibraryPath()
	if err != nil {
		t.Skip("libghostty-vt unavailable:", err)
	}
	if err := Load(path); err != nil {
		t.Fatal(err)
	}
}

func newTerm(t *testing.T, cols, rows int) *Terminal {
	t.Helper()
	term, err := NewTerminal(cols, rows, Effects{})
	if err != nil {
		t.Fatal(err)
	}
	term.SetScrollback(4 << 20)
	t.Cleanup(term.Free)
	return term
}

// screenLayout 描述终端的可见状态：回滚行数、光标、备用屏，以及屏幕每一格的字符、背景和样式。
func screenLayout(term *Terminal) string {
	sb := term.Scrollbar()
	x, y := term.Cursor()
	var b strings.Builder
	fmt.Fprintf(&b, "total=%d cursor=%d,%d alt=%v\n", sb.Total, x, y, term.AltScreen())
	rs, err := NewRenderState()
	if err != nil {
		panic(err)
	}
	defer rs.Free()
	rs.Update(term)
	rs.Rows()
	for r := 0; rs.NextRow(); r++ {
		var text strings.Builder
		for i, c := range rs.RowCells() {
			if c.Codepoint != 0 {
				text.WriteRune(c.Codepoint)
			} else {
				text.WriteByte(' ')
			}
			if c.Codepoint != 0 || c.HasBackground || c.Style != 0 {
				fmt.Fprintf(&b, "  [%d,%d] %q bg=%v/%v style=%+v\n", r, i, c.Codepoint, c.HasBackground, c.Background, rs.CellStyle(i))
			}
		}
		fmt.Fprintf(&b, "%2d|%s\n", r, strings.TrimRight(text.String(), " "))
	}
	return b.String()
}

func lines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\r\n", i)
	}
	return b.String()
}

func TestSnapshotRestoresLayout(t *testing.T) {
	loadLib(t)
	cases := map[string]string{
		"few lines":         lines(10),
		"exactly full":      lines(29),
		"one scrolled":      lines(30),
		"long scrollback":   lines(300),
		"cleared screen":    lines(300) + "\x1b[H\x1b[2Jprompt$ ",
		"alt screen":        lines(300) + "\x1b[?1049halt 1\r\nalt 2",
		"explicit spaces":   lines(300) + "   \r\n",
		"colored blank":     lines(300) + "\x1b[44m\x1b[K\x1b[0m\r\n",
		"soft wrap":         lines(300) + strings.Repeat("x", 250) + "\r\n",
		"wide chars":        lines(300) + "中文 日本語 👨‍👩‍👧 ok\r\n",
		"cursor mid screen": lines(300) + "\x1b[10;5H",
		"full no newline":   lines(299) + "line 300",
		// 带背景色的格子后跟被光标右移跳过的格子，跳过的格子必须保持默认样式
		"skipped after bg": lines(300) + "\x1b[38;2;215;119;87;48;2;0;0;0m▛▜\x1b[0m\x1b[3C\x1b[1mtitle\x1b[0m\r\n",
		"bg run mid row":   lines(300) + "ab\x1b[48;5;24m\x1b[4X\x1b[0m\x1b[6Ccd\r\n",
		"hyperlink":        lines(300) + "see \x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\ ok\r\n",
		"underline styles": lines(300) + "\x1b[4:3;58;2;255;0;0mcurly\x1b[0m \x1b[4:2mdouble\x1b[0m \x1b[9;53mstrike\x1b[0m\r\n",
		"wide at row end":  lines(300) + strings.Repeat("x", 99) + "中文\r\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			o := newTerm(t, 100, 30)
			o.Write([]byte(input))
			r := newTerm(t, 100, 30)
			r.Write(o.Snapshot())
			if lo, lr := screenLayout(o), screenLayout(r); lo != lr {
				t.Fatalf("restored layout differs\noriginal:\n%s\nrestored:\n%s", lo, lr)
			}
			// 之后的输出落在同样的位置
			o.Write([]byte("NEXT\r\nAFTER"))
			r.Write([]byte("NEXT\r\nAFTER"))
			if lo, lr := screenLayout(o), screenLayout(r); lo != lr {
				t.Fatalf("layout differs after more output\noriginal:\n%s\nrestored:\n%s", lo, lr)
			}
		})
	}
}
