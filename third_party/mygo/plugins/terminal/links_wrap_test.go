package terminal

import (
	"testing"

	"github.com/egoist/mygo/plugins/terminal/internal/vt"
)

// Posse: a link that a long line wrapped opens whole from each of its rows.
func TestLinkAcrossRows(t *testing.T) {
	if err := Load(); err != nil {
		t.Skip(err)
	}
	// rows makes the span of rows from row, each columns x0 up to x1.
	rows := func(row int, xs ...[2]int) linkSpan {
		s := linkSpan{row: row, rows: len(xs)}
		copy(s.x[:], xs)
		return s
	}
	broken := "  x (/tmp/aa/bb/\r\n  cc.ts:24)"
	three := "/tmp/aa/\r\n  bb/dd/\r\n    cc.go:24"
	no := func(string) bool { return false }
	cases := []struct {
		name   string
		out    string
		usable func(string) bool
		at     [2]int // col, row
		want   string
		span   linkSpan
	}{
		{"soft wrap", "see /tmp/aaaa/bbbb/cc.ts:24 ok", nil, [2]int{1, 1}, "/tmp/aaaa/bbbb/cc.ts:24", rows(0, [2]int{4, 20}, [2]int{0, 7})},
		{"soft wrap first row", "see /tmp/aaaa/bbbb/cc.ts:24 ok", nil, [2]int{6, 0}, "/tmp/aaaa/bbbb/cc.ts:24", rows(0, [2]int{4, 20}, [2]int{0, 7})},
		{"broken after slash", broken, nil, [2]int{3, 1}, "/tmp/aa/bb/cc.ts:24", rows(0, [2]int{5, 16}, [2]int{2, 10})},
		{"broken, first row", broken, nil, [2]int{8, 0}, "/tmp/aa/bb/cc.ts:24", rows(0, [2]int{5, 16}, [2]int{2, 10})},
		{"broken, not there", broken, no, [2]int{8, 0}, "/tmp/aa/bb/", rows(0, [2]int{5, 16})},
		{"broken, blank after", broken, nil, [2]int{17, 0}, "", linkSpan{}},
		{"broken, indent", broken, nil, [2]int{1, 1}, "", linkSpan{}},
		{"three rows", three, nil, [2]int{5, 1}, "/tmp/aa/bb/dd/cc.go:24", rows(0, [2]int{0, 8}, [2]int{2, 8}, [2]int{4, 12})},
		{"one row", "a /tmp/x.go b\r\nnext", nil, [2]int{4, 0}, "/tmp/x.go", rows(0, [2]int{2, 11})},
	}
	for _, c := range cases {
		term, err := vt.NewTerminal(20, 4, vt.Effects{})
		if err != nil {
			t.Fatal(err)
		}
		term.Write([]byte(c.out))
		tt := &Terminal{term: term, opts: Options{OpenLink: func(string) bool { return true }, LinkUsable: c.usable}}
		v := &view{t: tt, cols: 20, rows: 4}
		link, _, span := v.linkAt(c.at[0], c.at[1])
		if link != c.want || span != c.span {
			t.Errorf("%s: got %q %+v, want %q %+v", c.name, link, span, c.want, c.span)
		}
	}
}
