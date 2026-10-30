//go:build darwin

package media

import (
	"errors"
	"image"
	"math"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// cgRect 是 4 个 float64。purego 在 arm64 上按浮点寄存器传这种纯浮点结构，不要再插别的字段。
type cgRect struct{ X, Y, W, H float64 }

// imageIO 是经 ImageIO 解图要用的 CoreFoundation、CoreGraphics、ImageIO 接口，第一次用时加载。
// 只用 C 接口，不经过 Objective-C 的消息发送，可以在后台协程里调用。
var imageIO struct {
	once sync.Once
	ok   bool

	noCache        uintptr // {kCGImageSourceShouldCache: false}：取出的帧不让 ImageIO 自己再留一份
	srgb           uintptr
	keyCallbacks   uintptr
	valueCallbacks uintptr
	// 各格式的属性字典和其中的帧时长、循环次数
	dicts [3]struct{ dict, unclamped, delay, loops uintptr }

	dataCreate     func(alloc uintptr, b unsafe.Pointer, n int) uintptr
	dataNoCopy     func(alloc uintptr, b unsafe.Pointer, n int, dealloc uintptr) uintptr
	nullAlloc      uintptr // kCFAllocatorNull：CFData 释放时不动内容
	release        func(uintptr)
	dictCreate     func(alloc uintptr, keys, values unsafe.Pointer, n int, kcb, vcb uintptr) uintptr
	dictGet        func(d, key uintptr) uintptr
	numberGet      func(n uintptr, typ int, out unsafe.Pointer) bool
	stringGet      func(s uintptr, buf *byte, size int, enc uint32) bool
	sourceCreate   func(data, opts uintptr) uintptr
	sourceCount    func(src uintptr) int
	sourceType     func(src uintptr) uintptr
	sourceProps    func(src, opts uintptr) uintptr
	sourcePropsAt  func(src uintptr, i int, opts uintptr) uintptr
	sourceImageAt  func(src uintptr, i int, opts uintptr) uintptr
	imageWidth     func(img uintptr) int
	imageHeight    func(img uintptr) int
	imageRelease   func(img uintptr)
	bitmapCreate   func(data unsafe.Pointer, w, h, bpc, bpr int, cs uintptr, info uint32) uintptr
	setQuality     func(ctx uintptr, q int32)
	setBlend       func(ctx uintptr, mode int32)
	drawImage      func(ctx uintptr, r cgRect, img uintptr)
	contextRelease func(ctx uintptr)
	poolPush       func() uintptr
	poolPop        func(uintptr)
}

const (
	cfUTF8           = 0x08000100
	cfNumberDouble   = 13
	cgPremulLast     = 1  // kCGImageAlphaPremultipliedLast：RGBA 字节序、预乘，和 image.RGBA 一样
	cgBlendCopy      = 17 // kCGBlendModeCopy：整块覆盖，复用的缓冲不用先清
	cgQualityHigh    = 3
	imageIOFramework = "/System/Library/Frameworks/ImageIO.framework/ImageIO"
)

func loadImageIO() bool {
	imageIO.once.Do(func() {
		cf, err1 := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		cg, err2 := purego.Dlopen("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		io, err3 := purego.Dlopen(imageIOFramework, purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		objc, err4 := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return
		}
		m := &imageIO
		// Dlsym 给的是全局变量的地址，变量里才是 CFStringRef 之类的对象
		word := func(lib uintptr, name string) uintptr {
			p, err := purego.Dlsym(lib, name)
			if err != nil {
				return 0
			}
			return **(**uintptr)(unsafe.Pointer(&p))
		}
		addr := func(lib uintptr, name string) uintptr {
			p, _ := purego.Dlsym(lib, name)
			return p
		}
		m.keyCallbacks, m.valueCallbacks = addr(cf, "kCFTypeDictionaryKeyCallBacks"), addr(cf, "kCFTypeDictionaryValueCallBacks")
		for i, f := range []string{"GIF", "PNG", "WebP"} {
			d := &m.dicts[i]
			pre := "kCGImageProperty" + f
			if f == "PNG" {
				pre = "kCGImagePropertyAPNG"
			}
			d.dict = word(io, "kCGImageProperty"+f+"Dictionary")
			d.unclamped, d.delay, d.loops = word(io, pre+"UnclampedDelayTime"), word(io, pre+"DelayTime"), word(io, pre+"LoopCount")
		}
		purego.RegisterLibFunc(&m.dataCreate, cf, "CFDataCreate")
		purego.RegisterLibFunc(&m.dataNoCopy, cf, "CFDataCreateWithBytesNoCopy")
		purego.RegisterLibFunc(&m.release, cf, "CFRelease")
		purego.RegisterLibFunc(&m.dictCreate, cf, "CFDictionaryCreate")
		purego.RegisterLibFunc(&m.dictGet, cf, "CFDictionaryGetValue")
		purego.RegisterLibFunc(&m.numberGet, cf, "CFNumberGetValue")
		purego.RegisterLibFunc(&m.stringGet, cf, "CFStringGetCString")
		purego.RegisterLibFunc(&m.sourceCreate, io, "CGImageSourceCreateWithData")
		purego.RegisterLibFunc(&m.sourceCount, io, "CGImageSourceGetCount")
		purego.RegisterLibFunc(&m.sourceType, io, "CGImageSourceGetType")
		purego.RegisterLibFunc(&m.sourceProps, io, "CGImageSourceCopyProperties")
		purego.RegisterLibFunc(&m.sourcePropsAt, io, "CGImageSourceCopyPropertiesAtIndex")
		purego.RegisterLibFunc(&m.sourceImageAt, io, "CGImageSourceCreateImageAtIndex")
		purego.RegisterLibFunc(&m.imageWidth, cg, "CGImageGetWidth")
		purego.RegisterLibFunc(&m.imageHeight, cg, "CGImageGetHeight")
		purego.RegisterLibFunc(&m.imageRelease, cg, "CGImageRelease")
		purego.RegisterLibFunc(&m.bitmapCreate, cg, "CGBitmapContextCreate")
		purego.RegisterLibFunc(&m.setQuality, cg, "CGContextSetInterpolationQuality")
		purego.RegisterLibFunc(&m.setBlend, cg, "CGContextSetBlendMode")
		purego.RegisterLibFunc(&m.drawImage, cg, "CGContextDrawImage")
		purego.RegisterLibFunc(&m.contextRelease, cg, "CGContextRelease")
		purego.RegisterLibFunc(&m.poolPush, objc, "objc_autoreleasePoolPush")
		purego.RegisterLibFunc(&m.poolPop, objc, "objc_autoreleasePoolPop")
		var createSpace func(name uintptr) uintptr
		purego.RegisterLibFunc(&createSpace, cg, "CGColorSpaceCreateWithName")
		m.srgb = createSpace(word(cg, "kCGColorSpaceSRGB"))
		m.nullAlloc = word(cf, "kCFAllocatorNull")
		key, no := word(io, "kCGImageSourceShouldCache"), word(cf, "kCFBooleanFalse")
		if m.srgb == 0 || key == 0 || no == 0 || m.nullAlloc == 0 {
			return
		}
		keys, values := [1]uintptr{key}, [1]uintptr{no}
		m.noCache = m.dictCreate(0, unsafe.Pointer(&keys[0]), unsafe.Pointer(&values[0]), 1, m.keyCallbacks, m.valueCallbacks)
		m.ok = m.noCache != 0
	})
	return imageIO.ok
}

// withPool 在自动释放池里调用 f：ImageIO 的解码器内部可能 autorelease 对象，后台协程上没有池会一直留着。
// 池按线程记，push 和 pop 之间不能换线程。
func withPool(f func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p := imageIO.poolPush()
	defer imageIO.poolPop(p)
	f()
}

// Anim 是一张动图（GIF、APNG、动画 WebP），经 ImageIO 逐帧取出合成好的画面。不是并发安全的，用完 Close。
type Anim struct {
	Size   image.Point     // 原始尺寸
	Frame  image.Point     // 解出的每帧尺寸：按 OpenAnim 的上限等比缩小过
	Delays []time.Duration // 每帧显示多久
	Loops  int             // 放几遍，0 是一直放
	Format string          // gif、png、webp
	src    uintptr
	data   []byte // src 读的内容，没有复制
	clean  runtime.Cleanup
}

// ErrNoImageIO 是 ImageIO 加载不了。
var ErrNoImageIO = errors.New("ImageIO unavailable")

// OpenAnim 打开一张动图，每帧最宽 maxW、最多 maxPixels 个像素；只有一帧的不是动图，返回 nil, nil。
func OpenAnim(data []byte, maxW, maxPixels int) (*Anim, error) {
	if !loadImageIO() {
		return nil, ErrNoImageIO
	}
	if !maybeAnimated(data) {
		return nil, nil
	}
	m := &imageIO
	var a *Anim
	var err error
	withPool(func() {
		// 不复制：Anim 留着 data，解码器在它之前释放
		d := m.dataNoCopy(0, unsafe.Pointer(&data[0]), len(data), m.nullAlloc)
		if d == 0 {
			err = errors.New("CFDataCreate failed")
			return
		}
		src := m.sourceCreate(d, 0)
		m.release(d)
		if src == 0 {
			err = errors.New("not an image")
			return
		}
		n := m.sourceCount(src)
		format := utiFormat(m.sourceType(src))
		fi := formatDict(format)
		if n < 2 || fi < 0 {
			m.release(src)
			return
		}
		img := m.sourceImageAt(src, 0, m.noCache)
		if img == 0 {
			m.release(src)
			err = errors.New("cannot decode the first frame")
			return
		}
		w, h := m.imageWidth(img), m.imageHeight(img)
		m.imageRelease(img)
		a = &Anim{Size: image.Pt(w, h), Frame: fitSize(w, h, maxW, maxPixels), Format: format, src: src, data: data}
		a.Delays = make([]time.Duration, n)
		for i := range n {
			a.Delays[i] = frameDelay(m.sourcePropsAt(src, i, 0), fi)
		}
		if p := m.sourceProps(src, 0); p != 0 {
			if v, ok := dictNumber(p, m.dicts[fi].dict, m.dicts[fi].loops); ok && v > 0 {
				a.Loops = int(v)
			}
			m.release(p)
		}
		a.clean = runtime.AddCleanup(a, func(src uintptr) { imageIO.release(src) }, src)
	})
	return a, err
}

// Decode 把第 i 帧画进 dst（Frame 大小）。帧是 ImageIO 合成好的完整画面，按顺序取最快。
func (a *Anim) Decode(i int, dst *image.RGBA) error {
	if a.src == 0 {
		return errors.New("anim closed")
	}
	var err error
	withPool(func() {
		img := imageIO.sourceImageAt(a.src, i, imageIO.noCache)
		if img == 0 {
			err = errors.New("cannot decode frame")
			return
		}
		err = drawRGBA(img, dst)
		imageIO.imageRelease(img)
	})
	return err
}

// Close 释放 ImageIO 的解码器；之后不能再 Decode。
func (a *Anim) Close() {
	if a.src == 0 {
		return
	}
	a.clean.Stop()
	imageIO.release(a.src)
	a.src, a.data = 0, nil
}

// decodeSystem 用 ImageIO 解第一帧，给 Go 解不开、浏览器和预览却能显示的图用（比如带 tRNS 的 RGBA PNG）。
func decodeSystem(data []byte) (image.Image, string, error) {
	if !loadImageIO() {
		return nil, "", ErrNoImageIO
	}
	if len(data) == 0 {
		return nil, "", errors.New("empty image")
	}
	m := &imageIO
	var dst *image.RGBA
	var format string
	err := errors.New("not an image")
	withPool(func() {
		d := m.dataCreate(0, unsafe.Pointer(&data[0]), len(data))
		if d == 0 {
			return
		}
		src := m.sourceCreate(d, 0)
		m.release(d)
		if src == 0 {
			return
		}
		defer m.release(src)
		img := m.sourceImageAt(src, 0, m.noCache)
		if img == 0 {
			return
		}
		defer m.imageRelease(img)
		format = utiFormat(m.sourceType(src))
		dst = image.NewRGBA(image.Rect(0, 0, m.imageWidth(img), m.imageHeight(img)))
		err = drawRGBA(img, dst)
	})
	if err != nil {
		return nil, "", err
	}
	return dst, format, nil
}

// drawRGBA 把 CGImage 缩放画满 dst。
func drawRGBA(img uintptr, dst *image.RGBA) error {
	m := &imageIO
	w, h := dst.Rect.Dx(), dst.Rect.Dy()
	if w <= 0 || h <= 0 || dst.Stride != 4*w {
		return errors.New("bad frame buffer")
	}
	ctx := m.bitmapCreate(unsafe.Pointer(&dst.Pix[0]), w, h, 8, dst.Stride, m.srgb, cgPremulLast)
	if ctx == 0 {
		return errors.New("CGBitmapContextCreate failed")
	}
	m.setBlend(ctx, cgBlendCopy)
	m.setQuality(ctx, cgQualityHigh)
	m.drawImage(ctx, cgRect{W: float64(w), H: float64(h)}, img)
	m.contextRelease(ctx)
	runtime.KeepAlive(dst)
	return nil
}

// fitSize 是 w×h 等比缩到最宽 maxW、最多 maxPixels 个像素后的尺寸，本来就小的不放大。
func fitSize(w, h, maxW, maxPixels int) image.Point {
	k := 1.0
	if maxW > 0 && w > maxW {
		k = float64(maxW) / float64(w)
	}
	if n := float64(w) * float64(h) * k * k; maxPixels > 0 && n > float64(maxPixels) {
		k *= math.Sqrt(float64(maxPixels) / n)
	}
	return image.Pt(max(int(float64(w)*k), 1), max(int(float64(h)*k), 1))
}

// frameDelay 读帧属性里的显示时长并释放属性字典。照浏览器：不超过 10ms 的当成 100ms（很多 GIF 写 0 表示"尽快"）。
func frameDelay(props uintptr, fi int) time.Duration {
	if props == 0 {
		return 100 * time.Millisecond
	}
	defer imageIO.release(props)
	d := &imageIO.dicts[fi]
	// 有不钳的时长就用它：钳过的 DelayTime 把 0 改成了 50ms
	v, ok := dictNumber(props, d.dict, d.unclamped)
	if !ok {
		v, _ = dictNumber(props, d.dict, d.delay)
	}
	if v <= 0.010 {
		return 100 * time.Millisecond
	}
	return time.Duration(math.Round(v*1000)) * time.Millisecond
}

// dictNumber 取 props[dict][key] 的数值。
func dictNumber(props, dict, key uintptr) (float64, bool) {
	m := &imageIO
	if dict == 0 || key == 0 {
		return 0, false
	}
	sub := m.dictGet(props, dict)
	if sub == 0 {
		return 0, false
	}
	n := m.dictGet(sub, key)
	if n == 0 {
		return 0, false
	}
	var v float64
	if !m.numberGet(n, cfNumberDouble, unsafe.Pointer(&v)) {
		return 0, false
	}
	return v, true
}

// utiFormat 把 ImageIO 认出的类型（UTI）换成 image.Decode 那样的格式名。
func utiFormat(uti uintptr) string {
	if uti == 0 {
		return ""
	}
	var buf [128]byte
	if !imageIO.stringGet(uti, &buf[0], len(buf), cfUTF8) {
		return ""
	}
	s := string(buf[:clen(buf[:])])
	switch s {
	case "public.png":
		return "png"
	case "com.compuserve.gif":
		return "gif"
	case "org.webmproject.webp":
		return "webp"
	case "public.jpeg":
		return "jpeg"
	case "public.heic":
		return "heic"
	}
	return s
}

func clen(b []byte) int {
	for i, c := range b {
		if c == 0 {
			return i
		}
	}
	return len(b)
}

// formatDict 是格式在 imageIO.dicts 里的位置，不是动图格式的为 -1。
func formatDict(format string) int {
	switch format {
	case "gif":
		return 0
	case "png":
		return 1
	case "webp":
		return 2
	}
	return -1
}
