package desk

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/ebitengine/purego/objc"
)

// CGWindowListOption
const (
	cgListOnScreen       = 1 << 0
	cgListAbove          = 1 << 1
	cgListIncluding      = 1 << 3
	cgListExcludeDesktop = 1 << 4
)

// watched 是正在看的窗口，ID 为 0 时看整块屏。PW、PH 是窗口占的像素（满尺寸），X、Y 是它左上角的全局坐标。
type watched struct {
	Window
	pid    int32
	x, y   float64
	pw, ph int
}

// windowInfos 读系统的窗口列表，最前的在前面。CFArray 和 NSArray 免费桥接，按 Objective-C 对象读。
// 调用方要在自动释放池里。
func (m *macAPI) windowInfos(option, relative uint32) []rawWindow {
	if m.cgWindowList == nil {
		return nil
	}
	arr := m.cgWindowList(option, relative)
	if arr == 0 {
		return nil
	}
	defer m.cfRelease(arr)
	list := objc.ID(arr)
	forKey := m.sel("objectForKey:")
	key := func(name string) objc.ID { return m.string(name) }
	kNum, kPID, kLayer, kAlpha := key("kCGWindowNumber"), key("kCGWindowOwnerPID"), key("kCGWindowLayer"), key("kCGWindowAlpha")
	kOwner, kName, kBounds, kOn := key("kCGWindowOwnerName"), key("kCGWindowName"), key("kCGWindowBounds"), key("kCGWindowIsOnscreen")
	kX, kY, kW, kH := key("X"), key("Y"), key("Width"), key("Height")
	num := func(d, k objc.ID) (int, bool) {
		v := m.sendObj(d, forKey, k)
		if v == 0 {
			return 0, false
		}
		return m.integer(v, m.sel("integerValue")), true
	}
	real := func(d, k objc.ID) float64 {
		v := m.sendObj(d, forKey, k)
		if v == 0 {
			return 0
		}
		return m.doubleVal(v, m.sel("doubleValue"))
	}
	bundles := map[int32]string{}
	var out []rawWindow
	for i := range m.count(list, m.sel("count")) {
		d := m.at(list, m.sel("objectAtIndex:"), i)
		id, _ := num(d, kNum)
		pid, _ := num(d, kPID)
		layer, _ := num(d, kLayer)
		on, _ := num(d, kOn)
		w := rawWindow{
			ID: uint32(id), PID: int32(pid), Layer: layer, Alpha: 1, OnScreen: on != 0,
			App:   m.goString(m.sendObj(d, forKey, kOwner)),
			Title: m.goString(m.sendObj(d, forKey, kName)),
		}
		if v := m.sendObj(d, forKey, kAlpha); v != 0 {
			w.Alpha = m.doubleVal(v, m.sel("doubleValue"))
		}
		if b := m.sendObj(d, forKey, kBounds); b != 0 {
			w.X, w.Y, w.W, w.H = real(b, kX), real(b, kY), real(b, kW), real(b, kH)
		}
		if bundle, ok := bundles[w.PID]; ok {
			w.Bundle = bundle
		} else {
			w.Bundle = m.bundleOf(w.PID)
			bundles[w.PID] = w.Bundle
		}
		out = append(out, w)
	}
	return out
}

// windowInfo 是一个窗口现在的位置、大小和是否在屏幕上；关掉了返回 false。调用方要在自动释放池里。
func (m *macAPI) windowInfo(id uint32) (rawWindow, bool) {
	for _, w := range m.windowInfos(cgListIncluding, id) {
		if w.ID == id {
			return w, true
		}
	}
	return rawWindow{}, false
}

func (m *macAPI) bundleOf(pid int32) string {
	cls := objc.ID(objc.GetClass("NSRunningApplication"))
	if cls == 0 || pid <= 0 {
		return ""
	}
	app := m.sendInt(cls, m.sel("runningApplicationWithProcessIdentifier:"), int(pid))
	if app == 0 {
		return ""
	}
	return m.goString(m.init(app, m.sel("bundleIdentifier")))
}

// frontPID 是最前面的 App 的进程号，拿不到时是 0。调用方要在自动释放池里。
func (m *macAPI) frontPID() int32 {
	cls := objc.ID(objc.GetClass("NSWorkspace"))
	if cls == 0 {
		return 0
	}
	app := m.init(m.init(cls, m.sel("sharedWorkspace")), m.sel("frontmostApplication"))
	if app == 0 {
		return 0
	}
	return int32(m.integer(app, m.sel("processIdentifier")))
}

// raiseWindow 把窗口所在的 App 设成最前，再把这个窗口提到它所有窗口的最前。要辅助功能权限。
// 调用方要在自动释放池里。
func (m *macAPI) raiseWindow(pid int32, id uint32) error {
	if m.axApp == nil || m.axCopy == nil || m.axSet == nil || m.axAction == nil || m.axWindowID == nil {
		return fmt.Errorf("accessibility symbols missing")
	}
	app := m.axApp(pid)
	if app == 0 {
		return fmt.Errorf("no accessibility element for pid %d", pid)
	}
	defer m.cfRelease(app)
	if rc := m.axSet(app, uintptr(m.string("AXFrontmost")), m.cfTrue); rc != 0 {
		return fmt.Errorf("AXFrontmost: %d", rc)
	}
	var wins uintptr
	if rc := m.axCopy(app, uintptr(m.string("AXWindows")), &wins); rc != 0 || wins == 0 {
		return fmt.Errorf("AXWindows: %d", rc)
	}
	defer m.cfRelease(wins)
	for i := range m.arrayCountCF(wins) {
		w := m.arrayGet(wins, i)
		var got uint32
		if m.axWindowID(w, &got) != 0 || got != id {
			continue
		}
		m.axSet(w, uintptr(m.string("AXMain")), m.cfTrue)
		if rc := m.axAction(w, uintptr(m.string("AXRaise"))); rc != 0 {
			return fmt.Errorf("AXRaise: %d", rc)
		}
		return nil
	}
	return fmt.Errorf("window %d not among the app's windows", id)
}

func (e *engine) watching() watched {
	e.winMu.Lock()
	defer e.winMu.Unlock()
	return e.win
}

func (e *engine) setWatched(w watched) {
	e.winMu.Lock()
	e.win = w
	e.winMu.Unlock()
}

// windowTarget 给注入用：正在看的窗口和它的进程，看整块屏时 ID 为 0。
func (e *engine) windowTarget() (uint32, int32) {
	w := e.watching()
	return w.ID, w.pid
}

// displayList 是现在所有的屏。调用方要在自动释放池里。
func (e *engine) displayList() []Display {
	var list []Display
	names := e.mac.screenNames()
	main := e.mac.mainDisplay()
	for _, id := range e.mac.displayIDs() {
		d := Display{ID: id, Name: names[id], Main: id == main}
		if e.mac.displayBounds != nil {
			b := e.mac.displayBounds(id)
			d.X, d.Y, d.Width, d.Height = b.X, b.Y, b.W, b.H
		}
		d.PixelWidth, d.PixelHeight = e.mac.displayPixels(id)
		list = append(list, d)
	}
	return list
}

// writeWindows 回 CtrlWindows：当前空间里看得见、能单独看的窗口。
func (e *engine) writeWindows() {
	var list []Window
	e.mac.withPool(func() {
		raw := e.mac.windowInfos(cgListOnScreen|cgListExcludeDesktop, 0)
		list = pickWindows(raw, int32(os.Getpid()), e.opt.Exclude, e.displayList())
	})
	b, err := json.Marshal(Windows{List: list})
	if err != nil {
		return
	}
	e.write(Frame{Kind: KindWindows, CaptureUnixNano: time.Now().UnixNano(), Payload: b})
}

// switchWindow 是手机选了只看一个窗口。窗口已经不在了就留在现在看的画面上。
func (e *engine) switchWindow(id uint32) {
	e.streamMu.Lock()
	var err error
	switch {
	case e.locked.Load():
		// 锁屏界面不在窗口里，解锁后再换过去
		e.lockWin = id
		e.logf("window %d after unlock", id)
	case !e.stopping.Load() && e.stream != 0:
		err = e.rebuildWindowLocked(id, true)
	}
	e.streamMu.Unlock()
	if err != nil {
		e.halt(err)
		return
	}
	e.writeDisplays()
	// 不拿着 streamMu：辅助功能的调用在对方 App 卡住时要等好几秒
	if w := e.watching(); w.ID == id {
		e.mac.withPool(func() {
			if err := e.mac.raiseWindow(w.pid, id); err != nil {
				e.logf("raise window %d: %v", id, err)
			}
		})
	}
}

// checkWindow 每秒看一次正在看的窗口：大小变了按新尺寸重开，关掉、最小化、切到别的空间了回到整块屏，
// 这两种都告诉手机。只挪了位置不用重开，注入下次按下时会取新位置。
func (e *engine) checkWindow() {
	before := e.watching()
	if before.ID == 0 {
		return
	}
	if err := e.rebuildDisplay(0); err != nil {
		e.halt(err)
		return
	}
	after := e.watching()
	if after.ID != before.ID || after.pw != before.pw || after.ph != before.ph {
		e.writeDisplays()
	}
}

// rebuildWindowLocked 换成只采窗口 id，pick 是手机刚选的（这时窗口不在了不动；每秒的检查发现不在了回到整块屏）。
// 窗口和尺寸都没变时只更新位置。要拿着 streamMu。
func (e *engine) rebuildWindowLocked(id uint32, pick bool) error {
	var info rawWindow
	var ok bool
	var displays []Display
	e.mac.withPool(func() {
		info, ok = e.mac.windowInfo(id)
		displays = e.displayList()
	})
	if !ok || !info.OnScreen {
		if pick {
			e.logf("window %d is gone, staying", id)
			return nil
		}
		return e.windowGoneLocked(id)
	}
	dispID := displayAt(displays, info.X+info.W/2, info.Y+info.H/2)
	var disp Display
	for _, d := range displays {
		if d.ID == dispID {
			disp = d
		}
	}
	pw, ph := windowPixels(info.W, info.H, disp)
	tw, th := Fit(pw, ph, e.longEdge())
	next := watched{
		Window: Window{ID: id, App: cleanName(info.App), Title: cleanName(info.Title), Width: info.W, Height: info.H, Display: dispID},
		pid:    info.PID, x: info.X, y: info.Y, pw: pw, ph: ph,
	}
	cur := e.watching()
	if !pick && cur.ID == id && tw == e.width && th == e.height {
		e.setWatched(next)
		if dispID != 0 {
			e.displayID = dispID
			e.shown.Store(dispID)
		}
		return nil
	}
	win, content, err := e.fetchWindow(id)
	if err != nil {
		if pick {
			e.logf("switch to window %d: %v", id, err)
			return nil
		}
		return e.windowGoneLocked(id)
	}
	defer e.mac.release(win, e.mac.sel("release"))
	defer e.mac.release(content, e.mac.sel("release"))
	e.logf("window %d (%s) %dx%d -> %dx%d", id, info.App, pw, ph, tw, th)
	e.stopStream()
	if err := e.resizeLocked(tw, th); err != nil {
		return err
	}
	if dispID != 0 {
		e.displayID = dispID
		e.shown.Store(dispID)
	}
	e.setWatched(next)
	e.writeConfig()
	e.force.Store(true)
	filter := e.mac.sendObj(e.mac.alloc(e.mac.clsFilter, e.mac.sel("alloc")), e.mac.sel("initWithDesktopIndependentWindow:"), win)
	return e.openStream(filter)
}

// windowStreamStopped 是只看窗口时系统停掉了采集（多半是窗口关了）：这个流已经停了，不用再停，放掉后回到整块屏。
func (e *engine) windowStreamStopped() {
	e.streamMu.Lock()
	var err error
	if w := e.watching(); w.ID != 0 && !e.stopping.Load() {
		if e.stream != 0 {
			e.mac.release(e.stream, e.mac.sel("release"))
			e.stream = 0
		}
		err = e.windowGoneLocked(w.ID)
	}
	e.streamMu.Unlock()
	if err != nil {
		e.halt(err)
		return
	}
	e.writeDisplays()
}

// windowGoneLocked 是正在看的窗口没了：回到它所在的整块屏，手机下一次 displays 里带 gone。
func (e *engine) windowGoneLocked(id uint32) error {
	e.logf("window %d is gone, back to display %d", id, e.displayID)
	e.setWatched(watched{})
	e.gone.Store(true)
	return e.displayLocked(e.displayID, true)
}

func (e *engine) fetchWindow(id uint32) (objc.ID, objc.ID, error) {
	return e.fetchShareable(fmt.Sprintf("window %d", id), func(content objc.ID) objc.ID {
		windows := e.mac.init(content, e.mac.sel("windows"))
		for i := range e.mac.count(windows, e.mac.sel("count")) {
			w := e.mac.at(windows, e.mac.sel("objectAtIndex:"), i)
			if uint32(e.mac.integer(w, e.mac.sel("windowID"))) == id {
				return w
			}
		}
		return 0
	})
}
