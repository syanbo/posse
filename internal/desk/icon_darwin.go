//go:build darwin

package desk

import (
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// iconPixels 是窗口列表里 App 图标的边长：手机上画 22 点，3 倍屏约 66 像素。
const iconPixels = 64

// windowIcons 给窗口列表取图标，一个子进程里只有一份。
var windowIcons appIcons

// appIcons 取 App 图标画成 iconPixels 见方的 PNG，按进程缓存：一次观看里问几次窗口列表，图标不变。
type appIcons struct {
	once sync.Once
	ok   bool

	runningApp func(objc.ID, objc.SEL, int32) objc.ID
	icon       func(objc.ID, objc.SEL) objc.ID
	cgImage    func(objc.ID, objc.SEL, *cgRect, uintptr, uintptr) uintptr

	colorSpace   func() uintptr
	bitmap       func(unsafe.Pointer, uintptr, uintptr, uintptr, uintptr, uintptr, uint32) uintptr
	draw         func(uintptr, cgRect, uintptr)
	bitmapImage  func(uintptr) uintptr
	dataCreate   func(uintptr, int) uintptr
	dataLen      func(uintptr) int
	dataBytes    func(uintptr) unsafe.Pointer
	destCreate   func(uintptr, uintptr, uintptr, uintptr) uintptr
	destAdd      func(uintptr, uintptr, uintptr)
	destFinalize func(uintptr) bool
	cfRelease    func(uintptr)

	mu    sync.Mutex
	cache map[int32][]byte
}

func (a *appIcons) load() {
	objcLib := softDl("/usr/lib/libobjc.A.dylib")
	appKit := softDl("/System/Library/Frameworks/AppKit.framework/AppKit")
	cg := softDl("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
	cf := softDl("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	io := softDl("/System/Library/Frameworks/ImageIO.framework/ImageIO")
	msg := softSym(objcLib, "objc_msgSend")
	if appKit == 0 || msg == 0 {
		return
	}
	purego.RegisterFunc(&a.runningApp, msg)
	purego.RegisterFunc(&a.icon, msg)
	purego.RegisterFunc(&a.cgImage, msg)
	a.colorSpace = softFn[func() uintptr](cg, "CGColorSpaceCreateDeviceRGB")
	a.bitmap = softFn[func(unsafe.Pointer, uintptr, uintptr, uintptr, uintptr, uintptr, uint32) uintptr](cg, "CGBitmapContextCreate")
	a.draw = softFn[func(uintptr, cgRect, uintptr)](cg, "CGContextDrawImage")
	a.bitmapImage = softFn[func(uintptr) uintptr](cg, "CGBitmapContextCreateImage")
	a.dataCreate = softFn[func(uintptr, int) uintptr](cf, "CFDataCreateMutable")
	a.dataLen = softFn[func(uintptr) int](cf, "CFDataGetLength")
	a.dataBytes = softFn[func(uintptr) unsafe.Pointer](cf, "CFDataGetBytePtr")
	a.cfRelease = softFn[func(uintptr)](cf, "CFRelease")
	a.destCreate = softFn[func(uintptr, uintptr, uintptr, uintptr) uintptr](io, "CGImageDestinationCreateWithData")
	a.destAdd = softFn[func(uintptr, uintptr, uintptr)](io, "CGImageDestinationAddImage")
	a.destFinalize = softFn[func(uintptr) bool](io, "CGImageDestinationFinalize")
	a.ok = a.colorSpace != nil && a.bitmap != nil && a.draw != nil && a.bitmapImage != nil &&
		a.dataCreate != nil && a.dataLen != nil && a.dataBytes != nil && a.cfRelease != nil &&
		a.destCreate != nil && a.destAdd != nil && a.destFinalize != nil
	a.cache = map[int32][]byte{}
}

// png 是进程 pid 所属 App 的图标，取不到时为 nil。要在自动释放池里调。
func (a *appIcons) png(m *macAPI, pid int32) []byte {
	a.once.Do(a.load)
	if !a.ok || pid <= 0 {
		return nil
	}
	a.mu.Lock()
	b, hit := a.cache[pid]
	a.mu.Unlock()
	if hit {
		return b
	}
	b = a.render(m, pid)
	a.mu.Lock()
	a.cache[pid] = b
	a.mu.Unlock()
	return b
}

func (a *appIcons) render(m *macAPI, pid int32) []byte {
	app := a.runningApp(objc.ID(objc.GetClass("NSRunningApplication")), m.sel("runningApplicationWithProcessIdentifier:"), pid)
	if app == 0 {
		return nil
	}
	img := a.icon(app, m.sel("icon"))
	if img == 0 {
		return nil
	}
	// NSImage 按建议的大小挑最合适的那一档，再画到固定尺寸的位图上，图标多大一档都一样大
	want := cgRect{W: iconPixels, H: iconPixels}
	src := a.cgImage(img, m.sel("CGImageForProposedRect:context:hints:"), &want, 0, 0)
	if src == 0 {
		return nil
	}
	cs := a.colorSpace()
	if cs == 0 {
		return nil
	}
	defer a.cfRelease(cs)
	// kCGImageAlphaPremultipliedLast
	ctx := a.bitmap(nil, iconPixels, iconPixels, 8, 0, cs, 1)
	if ctx == 0 {
		return nil
	}
	defer a.cfRelease(ctx)
	a.draw(ctx, want, src)
	out := a.bitmapImage(ctx)
	if out == 0 {
		return nil
	}
	defer a.cfRelease(out)
	data := a.dataCreate(0, 0)
	if data == 0 {
		return nil
	}
	defer a.cfRelease(data)
	dest := a.destCreate(data, uintptr(m.string("public.png")), 1, 0)
	if dest == 0 {
		return nil
	}
	defer a.cfRelease(dest)
	a.destAdd(dest, out, 0)
	if !a.destFinalize(dest) {
		return nil
	}
	n := a.dataLen(data)
	if n <= 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(a.dataBytes(data)), n)...)
}
