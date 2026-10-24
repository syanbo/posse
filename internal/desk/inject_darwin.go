//go:build darwin

package desk

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"
)

const (
	cgTapHID    = 0
	cgSourceHID = 1

	cgMouseLeftDown     = 1
	cgMouseLeftUp       = 2
	cgMouseRightDown    = 3
	cgMouseRightUp      = 4
	cgMouseMoved        = 5
	cgMouseLeftDragged  = 6
	cgMouseRightDragged = 7

	cgButtonLeft  = 0
	cgButtonRight = 1

	cgFieldClickState       = 1  // kCGMouseEventClickState
	cgFieldScrollContinuous = 88 // kCGScrollWheelEventIsContinuous
	cgScrollPixel           = 0  // kCGScrollEventUnitPixel

	ucActionDisplay = 3
	ucNoDeadKeys    = 1
	// EventRecord.modifiers 右移 8 位之后的 Shift。0x0200 是没移位的那一档。
	ucShiftState = 0x02

	unicodeChunk = 20
)

// 带修饰键的文字要真正按下这些键。键码从 HID 表取，避免和虚拟键码写两遍。
var modOrder = [...]struct {
	mod byte
	hid uint16
}{
	{ModLCtrl, 0xE0},
	{ModLShift, 0xE1},
	{ModLAlt, 0xE2},
	{ModLGUI, 0xE3},
	{ModRCtrl, 0xE4},
	{ModRShift, 0xE5},
	{ModRAlt, 0xE6},
	{ModRGUI, 0xE7},
}

type keyStroke struct {
	code  uint16
	shift bool
}

type injector struct {
	mu  sync.Mutex
	mac *macAPI
	log func(string, ...any)

	bounds     cgRect
	layout     map[rune]keyStroke
	haveLayout bool
	missing    bool

	left, right bool
	keys        map[uint16]bool
	gate        MoveGate
	source      uintptr
	lastX       float64
	lastY       float64
}

func (e *engine) injector() *injector {
	e.inMu.Lock()
	defer e.inMu.Unlock()
	if e.in != nil {
		return e.in
	}
	in := &injector{mac: e.mac, keys: map[uint16]bool{}, log: e.logf}
	e.mac.withPool(func() { in.prepare() })
	e.in = in
	return in
}

func (e *engine) applyInput(events []Event) {
	if len(events) == 0 {
		return
	}
	in := e.injector()
	if in.missing {
		return
	}
	now := time.Now()
	e.mac.withPool(func() {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.missing {
			return
		}
		in.refreshBounds()
		for _, ev := range events {
			in.one(ev, now)
		}
	})
}

func (e *engine) releaseInput() {
	e.inMu.Lock()
	in := e.in
	e.inMu.Unlock()
	if in == nil {
		return
	}
	e.mac.withPool(func() {
		in.mu.Lock()
		defer in.mu.Unlock()
		in.releaseAll()
	})
}

func (e *engine) inputTest() error {
	if !e.accessibilityTrusted() {
		return fmt.Errorf("desk: accessibility is not allowed; turn on %s in System Settings > Privacy & Security > Accessibility, then run this again", recordingAppName())
	}
	defer e.releaseInput()
	const mid = uint16(32768)
	e.logf("input-test: move to center, click, type a")
	e.applyInput([]Event{{Kind: EventMove, X: mid, Y: mid}})
	time.Sleep(30 * time.Millisecond)
	e.applyInput([]Event{{Kind: EventDown, Button: ButtonLeft, Clicks: 1, X: mid, Y: mid}})
	time.Sleep(40 * time.Millisecond)
	e.applyInput([]Event{{Kind: EventUp, Button: ButtonLeft, Clicks: 1, X: mid, Y: mid}})
	time.Sleep(80 * time.Millisecond)
	e.applyInput([]Event{{Kind: EventText, Text: "a"}})
	return nil
}

// 提示是异步的，这次调用的返回值不会因为弹了框就变成已授权。
func (e *engine) accessibilityTrusted() bool {
	if e.mac.axTrusted == nil {
		return false
	}
	trusted := false
	e.mac.withPool(func() {
		trusted = e.mac.axTrusted()
		if !trusted {
			e.mac.promptAccessibility()
			trusted = e.mac.axTrusted()
		}
	})
	return trusted
}

func (m *macAPI) promptAccessibility() {
	if m.axTrustedOpts == nil || m.clsDict == 0 {
		return
	}
	key := m.axPromptKey
	if key == 0 {
		key = uintptr(m.string("AXTrustedCheckOptionPrompt"))
	}
	dict := m.dictOne(m.clsDict, m.sel("dictionaryWithObject:forKey:"), m.cfTrue, key)
	if dict == 0 {
		return
	}
	m.axTrustedOpts(uintptr(dict))
}

func (in *injector) prepare() {
	m := in.mac
	if m.cgMouse == nil || m.cgPost == nil || m.displayBounds == nil {
		in.missing = true
		in.logf("input symbols missing; injection disabled")
		return
	}
	in.refreshBounds()
	if in.bounds.W < 1 || in.bounds.H < 1 {
		in.missing = true
		in.logf("display bounds unavailable; injection disabled")
		return
	}
	if m.cgSource != nil {
		in.source = m.cgSource(cgSourceHID)
	}
	in.layout = m.keyLayout()
	in.haveLayout = len(in.layout) > 0
	if !in.haveLayout {
		in.logf("keyboard layout unavailable; shortcuts will be skipped")
	}
}

func (in *injector) refreshBounds() {
	if in.mac.displayBounds == nil || in.mac.mainDisplay == nil {
		return
	}
	b := in.mac.displayBounds(in.mac.mainDisplay())
	if b.W >= 1 && b.H >= 1 {
		in.bounds = b
	}
}

func (in *injector) one(ev Event, now time.Time) {
	if in.missing {
		return
	}
	switch ev.Kind {
	case EventMove:
		// 被限速的移动仍然记下位置，松键时用得上。
		p := in.point(ev.X, ev.Y)
		if !in.gate.Allow(now) {
			return
		}
		typ := uint32(cgMouseMoved)
		button := uint32(cgButtonLeft)
		// 左键按着就是拖动；只按着右键走右键拖动；两个都按着算左键。
		if in.left {
			typ = cgMouseLeftDragged
		} else if in.right {
			typ = cgMouseRightDragged
			button = cgButtonRight
		}
		in.postMouse(typ, p, button, 0)
	case EventDown, EventUp:
		in.button(ev)
	case EventScroll:
		in.scroll(ev)
	case EventKeyDown, EventKeyUp:
		in.key(ev)
	case EventText:
		if ev.Mods == 0 {
			in.typeUnicode(ev.Text)
		} else {
			in.typeChord(ev.Text, ev.Mods)
		}
	}
}

func (in *injector) point(x, y uint16) cgPoint {
	px, py := NormPoint(in.bounds.X, in.bounds.Y, in.bounds.W, in.bounds.H, x, y)
	in.lastX, in.lastY = px, py
	return cgPoint{X: px, Y: py}
}

func (in *injector) button(ev Event) {
	var button, downType, upType uint32
	switch ev.Button {
	case ButtonLeft:
		button, downType, upType = cgButtonLeft, cgMouseLeftDown, cgMouseLeftUp
	case ButtonRight:
		button, downType, upType = cgButtonRight, cgMouseRightDown, cgMouseRightUp
	default:
		in.logf("unknown button %d", ev.Button)
		return
	}
	p := in.point(ev.X, ev.Y)
	clicks := int(ev.Clicks)
	if clicks == 0 {
		clicks = 1
	}
	if ev.Kind == EventDown {
		in.postMouse(downType, p, button, clicks)
		if ev.Button == ButtonLeft {
			in.left = true
		} else {
			in.right = true
		}
		return
	}
	in.postMouse(upType, p, button, clicks)
	if ev.Button == ButtonLeft {
		in.left = false
	} else {
		in.right = false
	}
}

func (in *injector) scroll(ev Event) {
	if ev.DX == 0 && ev.DY == 0 {
		return
	}
	if in.mac.cgScroll == nil || in.mac.cgPost == nil {
		return
	}
	gev := in.mac.cgScroll(in.source, cgScrollPixel, 2, int32(ev.DY), int32(ev.DX), 0)
	if gev == 0 {
		return
	}
	defer in.mac.cfRelease(gev)
	// 不设连续滚动时，像素距离会被当成行数。
	if in.mac.cgSetInt != nil {
		in.mac.cgSetInt(gev, cgFieldScrollContinuous, 1)
	}
	in.mac.cgPost(cgTapHID, gev)
}

func (in *injector) key(ev Event) {
	code, ok := HIDToVirtualKey(ev.HID)
	if !ok {
		in.logf("unknown hid 0x%02x", ev.HID)
		return
	}
	down := ev.Kind == EventKeyDown
	in.postKey(code, down, modifierFlags(ev.Mods))
	if down {
		in.keys[code] = true
	} else {
		delete(in.keys, code)
	}
}

// 按下和松开带同一段文字，接收方按 unicode 字符串插入一次。键码 0 不记成按住。
func (in *injector) typeUnicode(s string) {
	if s == "" || in.mac.cgKey == nil || in.mac.cgUnicode == nil || in.mac.cgPost == nil {
		return
	}
	for _, chunk := range splitUTF16(s, unicodeChunk) {
		in.postUnicode(chunk)
	}
}

func (in *injector) postUnicode(chars []uint16) {
	if len(chars) == 0 {
		return
	}
	for _, down := range []bool{true, false} {
		ev := in.mac.cgKey(in.source, 0, down)
		if ev == 0 {
			return
		}
		in.mac.cgUnicode(ev, uintptr(len(chars)), &chars[0])
		in.mac.cgPost(cgTapHID, ev)
		in.mac.cfRelease(ev)
	}
	runtime.KeepAlive(chars)
}

// 带修饰键时不退回 Unicode。临时按下的修饰键结束时松开，原来就按着的不动。
func (in *injector) typeChord(s string, mods byte) {
	if s == "" {
		return
	}
	if !in.haveLayout {
		in.logf("no keyboard layout; skipped shortcut %q", s)
		return
	}
	var strokes []keyStroke
	needShift := false
	for _, r := range s {
		st, ok := in.layout[r]
		if !ok {
			in.logf("no key for %q", string(r))
			continue
		}
		strokes = append(strokes, st)
		if st.shift {
			needShift = true
		}
	}
	if len(strokes) == 0 {
		return
	}
	eff := mods
	if needShift && eff&(ModLShift|ModRShift) == 0 {
		eff |= ModLShift
	}
	var temps []uint16
	for _, bit := range modOrder {
		if eff&bit.mod == 0 {
			continue
		}
		code, ok := HIDToVirtualKey(bit.hid)
		if !ok || in.keys[code] {
			continue
		}
		in.postKey(code, true, modifierFlags(bit.mod))
		in.keys[code] = true
		temps = append(temps, code)
	}
	flags := modifierFlags(eff)
	for _, st := range strokes {
		in.postKey(st.code, true, flags)
		in.postKey(st.code, false, flags)
	}
	for i := len(temps) - 1; i >= 0; i-- {
		in.postKey(temps[i], false, 0)
		delete(in.keys, temps[i])
	}
}

func (in *injector) postMouse(typ uint32, p cgPoint, button uint32, clicks int) {
	if in.mac.cgMouse == nil || in.mac.cgPost == nil {
		return
	}
	ev := in.mac.cgMouse(in.source, typ, p, button)
	if ev == 0 {
		return
	}
	defer in.mac.cfRelease(ev)
	if clicks > 0 && in.mac.cgSetInt != nil {
		in.mac.cgSetInt(ev, cgFieldClickState, int64(clicks))
	}
	// 手机上锁住 ⌘、⇧ 再点按，要成为 ⌘ 点击、⇧ 点击
	if in.mac.cgSetFlags != nil {
		in.mac.cgSetFlags(ev, in.heldFlags())
	}
	in.mac.cgPost(cgTapHID, ev)
}

// heldFlags 是此刻按着的修饰键（经 EventKeyDown 按下、还没松开的）对应的事件标志。
func (in *injector) heldFlags() uint64 {
	var f uint64
	for _, m := range modOrder {
		if code, ok := HIDToVirtualKey(m.hid); ok && in.keys[code] {
			f |= modifierFlags(m.mod)
		}
	}
	return f
}

func (in *injector) postKey(code uint16, down bool, flags uint64) {
	if in.mac.cgKey == nil || in.mac.cgPost == nil {
		return
	}
	ev := in.mac.cgKey(in.source, code, down)
	if ev == 0 {
		return
	}
	defer in.mac.cfRelease(ev)
	if in.mac.cgSetFlags != nil {
		in.mac.cgSetFlags(ev, flags)
	}
	in.mac.cgPost(cgTapHID, ev)
}

// 不只松开修饰键。字母卡住和修饰键卡住是同一件事。先拷贝，边遍历边删会漏键。
func (in *injector) releaseAll() {
	codes := make([]uint16, 0, len(in.keys))
	for code := range in.keys {
		codes = append(codes, code)
	}
	for _, code := range codes {
		in.postKey(code, false, 0)
		delete(in.keys, code)
	}
	p := cgPoint{X: in.lastX, Y: in.lastY}
	if in.left {
		in.postMouse(cgMouseLeftUp, p, cgButtonLeft, 1)
		in.left = false
	}
	if in.right {
		in.postMouse(cgMouseRightUp, p, cgButtonRight, 1)
		in.right = false
	}
	if in.source != 0 && in.mac.cfRelease != nil {
		in.mac.cfRelease(in.source)
		in.source = 0
	}
}

func (in *injector) logf(format string, args ...any) {
	if in.log == nil {
		return
	}
	in.log(format, args...)
}

// keyLayout 用当前 ASCII 键盘布局建字符到键码的表。要在 withPool 里调。
// 布局数据是 Get 来的，不能释放；输入源是 Copy 来的，要释放。
func (m *macAPI) keyLayout() map[rune]keyStroke {
	if m.tisASCII == nil || m.tisProp == nil || m.ucTranslate == nil || m.cfDataPtr == nil || m.tisLayoutKey == 0 {
		return nil
	}
	src := m.tisASCII()
	if src == 0 {
		return nil
	}
	defer m.cfRelease(src)
	data := m.tisProp(src, m.tisLayoutKey)
	if data == 0 {
		return nil
	}
	ptr := m.cfDataPtr(data)
	if ptr == nil {
		return nil
	}
	var kb uint32
	if m.lmKbdType != nil {
		kb = uint32(m.lmKbdType())
	}
	layout := make(map[rune]keyStroke)
	fill := func(mod uint32, shift bool) {
		var dead uint32
		var n uintptr
		var buf [8]uint16
		for code := uint16(0); code < 128; code++ {
			dead = 0
			n = 0
			st := m.ucTranslate(uintptr(ptr), code, ucActionDisplay, mod, kb, ucNoDeadKeys, &dead, uintptr(len(buf)), &n, &buf[0])
			if st != 0 || n != 1 {
				continue
			}
			r := rune(buf[0])
			if r < 0x20 || utf16.IsSurrogate(r) {
				continue
			}
			if _, ok := layout[r]; ok {
				continue
			}
			layout[r] = keyStroke{code: code, shift: shift}
		}
	}
	fill(0, false)
	fill(ucShiftState, true)
	if len(layout) == 0 {
		return nil
	}
	return layout
}
