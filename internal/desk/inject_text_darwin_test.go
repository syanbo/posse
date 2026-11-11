//go:build darwin

package desk

import (
	"fmt"
	"testing"
	"unicode/utf16"
	"unsafe"
)

// 记下投出的按键事件：键码、按下/松开、标志、文字
func fakeKeyInjector() (*injector, *[]string) {
	var log []string
	type ev struct {
		code  uint16
		down  bool
		flags uint64
		text  string
	}
	evs := map[uintptr]*ev{}
	var next uintptr
	m := &macAPI{
		cgKey: func(_ uintptr, code uint16, down bool) uintptr {
			next++
			evs[next] = &ev{code: code, down: down}
			return next
		},
		cgSetFlags: func(e uintptr, f uint64) { evs[e].flags = f },
		cgUnicode: func(e uintptr, n uintptr, p *uint16) {
			evs[e].text = string(utf16.Decode(unsafe.Slice(p, int(n))))
		},
		cgPost: func(_ uint32, e uintptr) {
			x := evs[e]
			log = append(log, fmt.Sprintf("%x %v %v %q", x.code, x.down, x.flags&flagShift != 0, x.text))
		},
		cfRelease: func(uintptr) {},
	}
	in := &injector{mac: m, keys: map[uint16]bool{}, haveLayout: true, layout: map[rune]keyStroke{
		'1': {code: 0x12}, '!': {code: 0x12, shift: true}, 'a': {code: 0x00},
	}}
	return in, &log
}

func TestTypeUnicodeUsesLayoutKeys(t *testing.T) {
	in, log := fakeKeyInjector()
	in.typeUnicode("1中文!a")
	want := []string{
		`12 true false "1"`, `12 false false "1"`,
		`0 true false "中文"`, `0 false false "中文"`,
		`38 true true ""`, `12 true true "!"`, `12 false true "!"`, `38 false false ""`,
		`0 true false "a"`, `0 false false "a"`,
	}
	if fmt.Sprint(*log) != fmt.Sprint(want) {
		t.Fatalf("events\n%v\nwant\n%v", *log, want)
	}

	// 已经按着 Shift 时不再临时按下、也不松开
	in, log = fakeKeyInjector()
	in.keys[0x38] = true
	in.typeUnicode("!")
	if want := []string{`12 true true "!"`, `12 false true "!"`}; fmt.Sprint(*log) != fmt.Sprint(want) {
		t.Fatalf("held shift: %v", *log)
	}
	in, log = fakeKeyInjector()
	in.keys[0x3C] = true // 右 Shift
	in.typeUnicode("!")
	if want := []string{`12 true true "!"`, `12 false true "!"`}; fmt.Sprint(*log) != fmt.Sprint(want) {
		t.Fatalf("held right shift: %v", *log)
	}
}

// 只看一个窗口时：纯键盘、文字的批不去问窗口服务器要位置，带鼠标或滚轮的照旧每批都取。
func TestRefreshBoundsOnlyForPointerEvents(t *testing.T) {
	asks := 0
	m := &macAPI{
		cgWindowList:  func(uint32, uint32) uintptr { asks++; return 0 },
		mainDisplay:   func() uint32 { return 1 },
		displayBounds: func(uint32) cgRect { return cgRect{W: 100, H: 100} },
	}
	in := &injector{mac: m, window: func() (uint32, int32) { return 7, 1 }}
	for _, c := range []struct {
		events []Event
		ask    bool
	}{
		{[]Event{{Kind: EventKeyDown}, {Kind: EventKeyUp}}, false},
		{[]Event{{Kind: EventText, Text: "a"}}, false},
		{[]Event{{Kind: EventPassword, Text: "x"}}, false},
		{[]Event{{Kind: EventMove}}, true},
		{[]Event{{Kind: EventText, Text: "a"}, {Kind: EventDown}}, true},
		{[]Event{{Kind: EventUp}}, true},
		{[]Event{{Kind: EventScroll, DY: 1}}, true},
	} {
		asks = 0
		in.refreshFor(c.events)
		if (asks > 0) != c.ask {
			t.Fatalf("%+v: asked window server %d times", c.events, asks)
		}
	}
}
