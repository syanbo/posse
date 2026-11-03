//go:build darwin

package desk

import (
	"fmt"
	"math"
	"os"
	"runtime"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/sys/unix"
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

	// lockWakeIdle 内唤出过锁屏密码框就不再唤，直接打字
	lockWakeIdle = 5 * time.Second
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
	// display 是正在看的屏，点击、滚动都按它换算；还没开始采集时为 0，用主屏
	display func() uint32
	// window 是只看的窗口和它的进程（ID 为 0 看整块屏），这时按窗口的位置换算
	window func() (uint32, int32)
	// locked 是 Mac 锁着屏
	locked func() bool
	// woke 是上一次唤出锁屏密码框的时刻
	woke time.Time

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
	// 滚动换成点之后不足 1 的部分，留给下一次，慢慢拖不会被取整吃掉
	scrollX, scrollY float64
}

func (e *engine) injector() *injector {
	e.inMu.Lock()
	defer e.inMu.Unlock()
	if e.in != nil {
		return e.in
	}
	in := &injector{mac: e.mac, keys: map[uint16]bool{}, log: e.logf, display: e.shown.Load, window: e.windowTarget, locked: e.locked.Load}
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

// wakeLocked 是开始看时 Mac 锁着屏：把锁屏界面的密码框叫出来。
func (e *engine) wakeLocked() {
	in := e.injector()
	e.mac.withPool(func() {
		in.mu.Lock()
		defer in.mu.Unlock()
		if in.missing {
			return
		}
		in.wake()
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

// axGranted 只查不弹框。授权在系统设置里打开后立即生效，不用重新拉起子进程。
func (e *engine) axGranted() bool {
	if e.mac.axTrusted == nil {
		return false
	}
	ok := false
	e.mac.withPool(func() { ok = e.mac.axTrusted() })
	return ok
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
	if in.window != nil {
		if id, _ := in.window(); id != 0 {
			// 按着键时不换：拖标题栏时窗口跟着指针走，换了坐标窗口会越跑越远
			if in.left || in.right {
				return
			}
			if w, ok := in.mac.windowInfo(id); ok && w.W >= 1 && w.H >= 1 {
				in.bounds = cgRect{X: w.X, Y: w.Y, W: w.W, H: w.H}
				return
			}
		}
	}
	id := in.mac.mainDisplay()
	if in.display != nil {
		if d := in.display(); d != 0 {
			id = d
		}
	}
	b := in.mac.displayBounds(id)
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
		switch {
		case ev.Mods == 0 && in.locked != nil && in.locked():
			// 密码框没人动一会儿就收起来，收起时打的头几个字不进框
			if now.Sub(in.woke) > lockWakeIdle {
				in.wake()
				time.Sleep(300 * time.Millisecond)
			}
			in.typeKeys(ev.Text)
		case ev.Mods == 0:
			in.typeUnicode(ev.Text)
		default:
			in.typeChord(ev.Text, ev.Mods)
		}
	case EventPassword:
		ls := macLockScreen{in: in}
		if ls.locked() && now.Sub(in.woke) > lockWakeIdle {
			in.wake()
			time.Sleep(300 * time.Millisecond)
		}
		typePassword(ls, ev.Text, in.logf)
	}
}

// macLockScreen 把密码只投给锁屏界面（loginwindow 进程）。
type macLockScreen struct{ in *injector }

func (s macLockScreen) locked() bool { return s.in.mac.screenLocked() }

// target 是这个用户的 loginwindow：锁屏界面由它画、密码框在它的窗口里（LoginUIKit 在 loginwindow.app 里）。
// 快速切换用户时每个会话一个 loginwindow，按 uid 挑自己的。
func (s macLockScreen) target() (int32, bool) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		s.in.logf("list processes: %v", err)
		return 0, false
	}
	for _, p := range procs {
		if unix.ByteSliceToString(p.Proc.P_comm[:]) == "loginwindow" {
			return p.Proc.P_pid, true
		}
	}
	return 0, false
}

// press 按当前键盘布局按一个键，要 Shift 的字带着 Shift；布局里没有的字按 Unicode。
// 不带手机上锁住的修饰键：密码框只要字。
func (s macLockScreen) press(pid int32, r rune) bool {
	in := s.in
	if st, ok := in.layout[r]; ok && in.haveLayout {
		shift, _ := HIDToVirtualKey(0xE1)
		var flags uint64
		if st.shift {
			flags = modifierFlags(ModLShift)
			if !in.postKeyTo(pid, shift, true, flags) {
				return false
			}
		}
		ok := in.postKeyTo(pid, st.code, true, flags) && in.postKeyTo(pid, st.code, false, flags)
		if st.shift {
			ok = in.postKeyTo(pid, shift, false, 0) && ok
		}
		return ok
	}
	if in.mac.cgKey == nil || in.mac.cgUnicode == nil || in.mac.cgPostPid == nil {
		return false
	}
	chars := utf16.Encode([]rune{r})
	for _, down := range []bool{true, false} {
		ev := in.mac.cgKey(in.source, 0, down)
		if ev == 0 {
			return false
		}
		in.mac.cgUnicode(ev, uintptr(len(chars)), &chars[0])
		in.mac.cgPostPid(pid, ev)
		in.mac.cfRelease(ev)
	}
	runtime.KeepAlive(chars)
	return true
}

func (s macLockScreen) enter(pid int32) {
	code, _ := HIDToVirtualKey(0x28)
	s.in.postKeyTo(pid, code, true, 0)
	s.in.postKeyTo(pid, code, false, 0)
}

// postKeyTo 和 postKey 一样，但只投给进程 pid。
func (in *injector) postKeyTo(pid int32, code uint16, down bool, flags uint64) bool {
	if in.mac.cgKey == nil || in.mac.cgPostPid == nil {
		return false
	}
	ev := in.mac.cgKey(in.source, code, down)
	if ev == 0 {
		return false
	}
	defer in.mac.cfRelease(ev)
	if in.mac.cgSetFlags != nil {
		in.mac.cgSetFlags(ev, flags)
	}
	in.mac.cgPostPid(pid, ev)
	return true
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
		in.front()
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

// front 在只看窗口、它不在最前（App 不在最前，或被别的窗口盖着）时先把窗口提上来：
// 点击落在屏幕上那个位置，被盖住时会点到上面的窗口；键盘发给最前的 App。
func (in *injector) front() {
	if in.window == nil {
		return
	}
	id, pid := in.window()
	if id == 0 || pid <= 0 || (in.mac.frontPID() == pid && !in.covered(id)) {
		return
	}
	if err := in.mac.raiseWindow(pid, id); err != nil {
		in.logf("raise window %d: %v", id, err)
		return
	}
	// 窗口服务器换完前后顺序之前发的点击还会落到原来最前的窗口上
	time.Sleep(50 * time.Millisecond)
}

// covered 是有别的普通窗口盖在窗口 id 上。
func (in *injector) covered(id uint32) bool {
	for _, w := range in.mac.windowInfos(cgListAbove, id) {
		if w.ID != id && w.Layer == 0 && w.Alpha > 0 && overlaps(w, in.bounds) {
			return true
		}
	}
	return false
}

func overlaps(w rawWindow, b cgRect) bool {
	return w.X < b.X+b.W && b.X < w.X+w.W && w.Y < b.Y+b.H && b.Y < w.Y+w.H
}

func (in *injector) scroll(ev Event) {
	if ev.DX == 0 && ev.DY == 0 {
		return
	}
	if in.mac.cgScroll == nil || in.mac.cgPost == nil {
		return
	}
	perUnit := in.bounds.H / ScrollPerScreen
	in.scrollX += float64(ev.DX) * perUnit
	in.scrollY += float64(ev.DY) * perUnit
	dx, dy := math.Trunc(in.scrollX), math.Trunc(in.scrollY)
	in.scrollX -= dx
	in.scrollY -= dy
	if dx == 0 && dy == 0 {
		return
	}
	gev := in.mac.cgScroll(in.source, cgScrollPixel, 2, int32(dy), int32(dx), 0)
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
	in.postKey(code, down, modifierFlags(ev.Mods)|keyFlags(code))
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

// typeKeys 按当前键盘布局逐字按键，要 Shift 的字单独按着 Shift。锁屏的密码框认按键，
// Unicode 注入没验证过能不能进去；布局里没有的字仍按 Unicode。
func (in *injector) typeKeys(s string) {
	if !in.haveLayout {
		in.typeUnicode(s)
		return
	}
	shift, _ := HIDToVirtualKey(0xE1)
	for _, r := range s {
		st, ok := in.layout[r]
		if !ok {
			in.typeUnicode(string(r))
			continue
		}
		var flags uint64
		if st.shift && !in.keys[shift] {
			flags = modifierFlags(ModLShift)
			in.postKey(shift, true, flags)
		}
		in.postKey(st.code, true, flags|in.heldFlags())
		in.postKey(st.code, false, flags|in.heldFlags())
		if flags != 0 {
			in.postKey(shift, false, 0)
		}
	}
}

// wake 在锁屏时唤出密码框：声明一次用户活动（屏幕熄了会点亮），再原地挪一下指针。
func (in *injector) wake() {
	in.woke = time.Now()
	m := in.mac
	if m.pmUserActive != nil {
		var id uint32
		if rc := m.pmUserActive(uintptr(m.string("Posse desk")), 0, &id); rc != 0 {
			in.logf("declare user activity: %d", rc)
		}
	}
	if m.cgCreate == nil || m.cgGetLoc == nil {
		return
	}
	cur := m.cgCreate(0)
	if cur == 0 {
		return
	}
	p := m.cgGetLoc(cur)
	m.cfRelease(cur)
	in.postMouse(cgMouseMoved, cgPoint{X: p.X + 1, Y: p.Y}, cgButtonLeft, 0)
	in.postMouse(cgMouseMoved, p, cgButtonLeft, 0)
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
