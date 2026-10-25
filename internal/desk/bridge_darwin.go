//go:build darwin

package desk

import (
	"fmt"
	"runtime"
	"structs"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

// cmTime 对齐 CoreMedia 的 CMTime。24 字节，Flags 的最低位表示有效。
type cmTime struct {
	_         structs.HostLayout
	Value     int64
	Timescale int32
	Flags     uint32
	Epoch     int64
}

func cmMake(value int64, scale int32) cmTime {
	return cmTime{Value: value, Timescale: scale, Flags: 1}
}

// cgPoint、cgRect 是 2 个和 4 个 float64。purego 在 arm64 上按浮点寄存器传这种纯浮点结构，不要再插别的字段。
type cgPoint struct{ X, Y float64 }

type cgRect struct{ X, Y, W, H float64 }

type macAPI struct {
	clsShareable, clsFilter, clsConfig, clsStream, clsArray, clsString, clsNumber, clsDict, clsURL, clsWriter, clsInput objc.ID

	getContent     func(objc.ID, objc.SEL, uintptr)
	initFilter     func(objc.ID, objc.SEL, objc.ID, objc.ID) objc.ID
	initFilterApps func(objc.ID, objc.SEL, objc.ID, objc.ID, objc.ID) objc.ID
	initStream     func(objc.ID, objc.SEL, objc.ID, objc.ID, objc.ID) objc.ID
	alloc          func(objc.ID, objc.SEL) objc.ID
	init           func(objc.ID, objc.SEL) objc.ID
	release        func(objc.ID, objc.SEL)
	retain         func(objc.ID, objc.SEL) objc.ID
	count          func(objc.ID, objc.SEL) int
	at             func(objc.ID, objc.SEL, int) objc.ID
	integer        func(objc.ID, objc.SEL) int
	utf8           func(objc.ID, objc.SEL) unsafe.Pointer
	nsString       func(objc.ID, objc.SEL, unsafe.Pointer) objc.ID
	setInt         func(objc.ID, objc.SEL, int)
	setU32         func(objc.ID, objc.SEL, uint32)
	setTime        func(objc.ID, objc.SEL, cmTime)
	setPtr         func(objc.ID, objc.SEL, uintptr)
	responds       func(objc.ID, objc.SEL, objc.SEL) bool
	emptyArray     func(objc.ID, objc.SEL) objc.ID
	numLong        func(objc.ID, objc.SEL, int64) objc.ID
	numDouble      func(objc.ID, objc.SEL, float64) objc.ID
	numBool        func(objc.ID, objc.SEL, int) objc.ID
	dictOne        func(objc.ID, objc.SEL, uintptr, uintptr) objc.ID
	dictN          func(objc.ID, objc.SEL, unsafe.Pointer, unsafe.Pointer, int) objc.ID
	arrayCount     func(objc.ID, objc.SEL, unsafe.Pointer, int) objc.ID
	fileURL        func(objc.ID, objc.SEL, objc.ID) objc.ID
	initWriter     func(objc.ID, objc.SEL, objc.ID, objc.ID, *objc.ID) objc.ID
	addInput       func(objc.ID, objc.SEL, objc.ID)
	startWriting   func(objc.ID, objc.SEL) bool
	startSession   func(objc.ID, objc.SEL, cmTime)
	writerInput    func(objc.ID, objc.SEL, objc.ID, uintptr, uintptr) objc.ID
	setBool        func(objc.ID, objc.SEL, int)
	appendSample   func(objc.ID, objc.SEL, uintptr) bool
	markFinished   func(objc.ID, objc.SEL)
	finishWriting  func(objc.ID, objc.SEL, uintptr)
	addOutput      func(objc.ID, objc.SEL, objc.ID, int, uintptr, *objc.ID) bool
	startCapture   func(objc.ID, objc.SEL, uintptr)
	stopCapture    func(objc.ID, objc.SEL, uintptr)
	assetDuration  func(objc.ID, objc.SEL) cmTime
	urlAsset       func(objc.ID, objc.SEL, objc.ID) objc.ID

	poolPush func() unsafe.Pointer
	poolPop  func(unsafe.Pointer)

	mainDisplay  func() uint32
	pixelsWide   func(uint32) uintptr
	pixelsHigh   func(uint32) uintptr
	copyMode     func(uint32) uintptr
	modeWide     func(uintptr) uintptr
	modeHigh     func(uintptr) uintptr
	modeRelease  func(uintptr)
	colorSpace   func(uintptr) uintptr
	colorRelease func(uintptr)
	queueCreate  func(unsafe.Pointer, uintptr) uintptr

	// activeDisplays 是 CGGetActiveDisplayList；sendObj 是带一个对象参数的 objc_msgSend（objectForKey: 之类）
	activeDisplays func(uint32, *uint32, *uint32) int32
	sendObj        func(objc.ID, objc.SEL, objc.ID) objc.ID
	// updateConfig 是 -[SCStream updateConfiguration:completionHandler:]，运行中改帧率
	updateConfig func(objc.ID, objc.SEL, objc.ID, uintptr)
	// doubleVal 是返回 double 的无参消息（doubleValue），sendInt 是带一个整数参数、返回对象的消息
	doubleVal func(objc.ID, objc.SEL) float64
	sendInt   func(objc.ID, objc.SEL, int) objc.ID

	vtCreate     func(uintptr, int32, int32, uint32, uintptr, uintptr, uintptr, uintptr, uintptr, *uintptr) int32
	vtEncode     func(uintptr, uintptr, cmTime, cmTime, uintptr, uintptr, *uint32) int32
	vtSet        func(uintptr, uintptr, uintptr) int32
	vtComplete   func(uintptr, cmTime) int32
	vtInvalidate func(uintptr)

	sampleImage     func(uintptr) uintptr
	sampleData      func(uintptr) uintptr
	sampleFormat    func(uintptr) uintptr
	sampleAttach    func(uintptr, int32) uintptr
	blockLength     func(uintptr) uintptr
	blockCopy       func(uintptr, uintptr, uintptr, unsafe.Pointer) int32
	hevcSet         func(uintptr, uintptr, *unsafe.Pointer, *uintptr, *uintptr, *int32) int32
	h264Set         func(uintptr, uintptr, *unsafe.Pointer, *uintptr, *uintptr, *int32) int32
	arrayCountCF    func(uintptr) uintptr
	arrayGet        func(uintptr, uintptr) uintptr
	dictGet         func(uintptr, uintptr) uintptr
	cfRetain        func(uintptr) uintptr
	cfRelease       func(uintptr)
	runLoop         func()
	stopLoop        func(uintptr)
	mainLoop        func() uintptr
	perform         func(uintptr, uintptr, uintptr)
	wake            func(uintptr)
	commonModes     uintptr
	preflight       func() bool
	request         func() bool
	sessionDict     func() uintptr
	registerDisplay func(uintptr, uintptr) int32
	addObserver     func(objc.ID, objc.SEL, uintptr, uintptr, uintptr, uintptr)

	keyRealTime, keyReorder, keyDelay, keySpeed, keyFPS, keyBitrate, keyLimits, keyForce uintptr
	keyLowLatency, keyNotSync, cfTrue, cfFalse                                           uintptr
	keyRequireHW, keyPower                                                               uintptr
	keyMatrix709                                                                         uintptr
	keyPrimaries, keyTransfer, keyYCbCr, primaries709, transfer709, matrix709            uintptr

	displayBounds func(uint32) cgRect
	cgMouse       func(uintptr, uint32, cgPoint, uint32) uintptr
	cgScroll      func(uintptr, uint32, uint32, int32, int32, int32) uintptr
	cgKey         func(uintptr, uint16, bool) uintptr
	cgSetInt      func(uintptr, uint32, int64)
	cgGetInt      func(uintptr, uint32) int64
	cgSetFlags    func(uintptr, uint64)
	cgUnicode     func(uintptr, uintptr, *uint16)
	cgPost        func(uint32, uintptr)
	cgGetLoc      func(uintptr) cgPoint
	cgSource      func(int32) uintptr
	axTrusted     func() bool
	axTrustedOpts func(uintptr) bool
	axPromptKey   uintptr
	ucTranslate   func(uintptr, uint16, uint16, uint32, uint32, uint32, *uint32, uintptr, *uintptr, *uint16) int32
	cfDataPtr     func(uintptr) unsafe.Pointer
	tisASCII      func() uintptr
	tisProp       func(uintptr, uintptr) uintptr
	tisLayoutKey  uintptr
	lmKbdType     func() uint8

	// 只看一个窗口：窗口列表，和把窗口提到最前用的辅助功能接口
	cgWindowList func(uint32, uint32) uintptr
	axApp        func(int32) uintptr
	axCopy       func(uintptr, uintptr, *uintptr) int32
	axSet        func(uintptr, uintptr, uintptr) int32
	axAction     func(uintptr, uintptr) int32
	axWindowID   func(uintptr, *uint32) int32
}

func loadMac() (*macAPI, error) {
	if unsafe.Sizeof(cmTime{}) != 24 {
		return nil, fmt.Errorf("desk: CMTime layout is %d bytes", unsafe.Sizeof(cmTime{}))
	}
	objcLib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return nil, err
	}
	for _, path := range []string{
		"/System/Library/Frameworks/Foundation.framework/Foundation",
		"/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation",
		"/System/Library/Frameworks/CoreMedia.framework/CoreMedia",
		"/System/Library/Frameworks/CoreVideo.framework/CoreVideo",
		"/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics",
		"/System/Library/Frameworks/AVFoundation.framework/AVFoundation",
		"/System/Library/Frameworks/VideoToolbox.framework/VideoToolbox",
		"/System/Library/Frameworks/ScreenCaptureKit.framework/ScreenCaptureKit",
	} {
		if _, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
			return nil, fmt.Errorf("desk: %s: %w", path, err)
		}
	}
	m := &macAPI{}
	m.poolPush = mustFn[func() unsafe.Pointer](objcLib, "objc_autoreleasePoolPush")
	m.poolPop = mustFn[func(unsafe.Pointer)](objcLib, "objc_autoreleasePoolPop")
	bind := func(slot any, sym uintptr) { purego.RegisterFunc(slot, sym) }
	msg := mustSym(objcLib, "objc_msgSend")
	bind(&m.getContent, msg)
	bind(&m.initFilter, msg)
	bind(&m.initFilterApps, msg)
	bind(&m.initStream, msg)
	bind(&m.alloc, msg)
	bind(&m.init, msg)
	bind(&m.release, msg)
	bind(&m.retain, msg)
	bind(&m.count, msg)
	bind(&m.at, msg)
	bind(&m.integer, msg)
	bind(&m.utf8, msg)
	bind(&m.nsString, msg)
	bind(&m.setInt, msg)
	bind(&m.setU32, msg)
	bind(&m.setTime, msg)
	bind(&m.setPtr, msg)
	bind(&m.responds, msg)
	bind(&m.emptyArray, msg)
	bind(&m.numLong, msg)
	bind(&m.numDouble, msg)
	bind(&m.numBool, msg)
	bind(&m.dictOne, msg)
	bind(&m.dictN, msg)
	bind(&m.arrayCount, msg)
	bind(&m.fileURL, msg)
	bind(&m.initWriter, msg)
	bind(&m.addInput, msg)
	bind(&m.startWriting, msg)
	bind(&m.startSession, msg)
	bind(&m.writerInput, msg)
	bind(&m.setBool, msg)
	bind(&m.appendSample, msg)
	bind(&m.markFinished, msg)
	bind(&m.finishWriting, msg)
	bind(&m.addOutput, msg)
	bind(&m.startCapture, msg)
	bind(&m.stopCapture, msg)
	bind(&m.assetDuration, msg)
	bind(&m.urlAsset, msg)
	bind(&m.sendObj, msg)
	bind(&m.updateConfig, msg)
	bind(&m.doubleVal, msg)
	bind(&m.sendInt, msg)

	cg := dl("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
	m.mainDisplay = mustFn[func() uint32](cg, "CGMainDisplayID")
	m.activeDisplays = mustFn[func(uint32, *uint32, *uint32) int32](cg, "CGGetActiveDisplayList")
	m.pixelsWide = mustFn[func(uint32) uintptr](cg, "CGDisplayPixelsWide")
	m.pixelsHigh = mustFn[func(uint32) uintptr](cg, "CGDisplayPixelsHigh")
	m.copyMode = mustFn[func(uint32) uintptr](cg, "CGDisplayCopyDisplayMode")
	m.modeWide = mustFn[func(uintptr) uintptr](cg, "CGDisplayModeGetPixelWidth")
	m.modeHigh = mustFn[func(uintptr) uintptr](cg, "CGDisplayModeGetPixelHeight")
	m.modeRelease = mustFn[func(uintptr)](cg, "CGDisplayModeRelease")
	m.colorSpace = mustFn[func(uintptr) uintptr](cg, "CGColorSpaceCreateWithName")
	m.colorRelease = mustFn[func(uintptr)](cg, "CGColorSpaceRelease")
	m.keyMatrix709 = cfSym(cg, "kCGDisplayStreamYCbCrMatrix_ITU_R_709_2")

	libsys := dl("/usr/lib/libSystem.B.dylib")
	m.queueCreate = mustFn[func(unsafe.Pointer, uintptr) uintptr](libsys, "dispatch_queue_create")

	vt := dl("/System/Library/Frameworks/VideoToolbox.framework/VideoToolbox")
	m.vtCreate = mustFn[func(uintptr, int32, int32, uint32, uintptr, uintptr, uintptr, uintptr, uintptr, *uintptr) int32](vt, "VTCompressionSessionCreate")
	m.vtEncode = mustFn[func(uintptr, uintptr, cmTime, cmTime, uintptr, uintptr, *uint32) int32](vt, "VTCompressionSessionEncodeFrame")
	m.vtSet = mustFn[func(uintptr, uintptr, uintptr) int32](vt, "VTSessionSetProperty")
	m.vtComplete = mustFn[func(uintptr, cmTime) int32](vt, "VTCompressionSessionCompleteFrames")
	m.vtInvalidate = mustFn[func(uintptr)](vt, "VTCompressionSessionInvalidate")
	m.keyRealTime = cfSym(vt, "kVTCompressionPropertyKey_RealTime")
	m.keyReorder = cfSym(vt, "kVTCompressionPropertyKey_AllowFrameReordering")
	m.keyDelay = cfSym(vt, "kVTCompressionPropertyKey_MaxFrameDelayCount")
	m.keySpeed = cfSym(vt, "kVTCompressionPropertyKey_PrioritizeEncodingSpeedOverQuality")
	m.keyFPS = cfSym(vt, "kVTCompressionPropertyKey_ExpectedFrameRate")
	m.keyBitrate = cfSym(vt, "kVTCompressionPropertyKey_AverageBitRate")
	m.keyLimits = cfSym(vt, "kVTCompressionPropertyKey_DataRateLimits")
	m.keyForce = cfSym(vt, "kVTEncodeFrameOptionKey_ForceKeyFrame")
	m.keyLowLatency = cfSym(vt, "kVTVideoEncoderSpecification_EnableLowLatencyRateControl")
	m.keyRequireHW = cfSym(vt, "kVTVideoEncoderSpecification_RequireHardwareAcceleratedVideoEncoder")
	m.keyPower = cfSym(vt, "kVTCompressionPropertyKey_MaximizePowerEfficiency")
	m.keyPrimaries = cfSym(vt, "kVTCompressionPropertyKey_ColorPrimaries")
	m.keyTransfer = cfSym(vt, "kVTCompressionPropertyKey_TransferFunction")
	m.keyYCbCr = cfSym(vt, "kVTCompressionPropertyKey_YCbCrMatrix")
	cv := dl("/System/Library/Frameworks/CoreVideo.framework/CoreVideo")
	m.primaries709 = cfSym(cv, "kCVImageBufferColorPrimaries_ITU_R_709_2")
	m.transfer709 = cfSym(cv, "kCVImageBufferTransferFunction_ITU_R_709_2")
	m.matrix709 = cfSym(cv, "kCVImageBufferYCbCrMatrix_ITU_R_709_2")

	cm := dl("/System/Library/Frameworks/CoreMedia.framework/CoreMedia")
	m.sampleImage = mustFn[func(uintptr) uintptr](cm, "CMSampleBufferGetImageBuffer")
	m.sampleData = mustFn[func(uintptr) uintptr](cm, "CMSampleBufferGetDataBuffer")
	m.sampleFormat = mustFn[func(uintptr) uintptr](cm, "CMSampleBufferGetFormatDescription")
	m.sampleAttach = mustFn[func(uintptr, int32) uintptr](cm, "CMSampleBufferGetSampleAttachmentsArray")
	m.blockLength = mustFn[func(uintptr) uintptr](cm, "CMBlockBufferGetDataLength")
	m.blockCopy = mustFn[func(uintptr, uintptr, uintptr, unsafe.Pointer) int32](cm, "CMBlockBufferCopyDataBytes")
	m.hevcSet = mustFn[func(uintptr, uintptr, *unsafe.Pointer, *uintptr, *uintptr, *int32) int32](cm, "CMVideoFormatDescriptionGetHEVCParameterSetAtIndex")
	m.h264Set = mustFn[func(uintptr, uintptr, *unsafe.Pointer, *uintptr, *uintptr, *int32) int32](cm, "CMVideoFormatDescriptionGetH264ParameterSetAtIndex")
	m.keyNotSync = cfSym(cm, "kCMSampleAttachmentKey_NotSync")

	cf := dl("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	m.arrayCountCF = mustFn[func(uintptr) uintptr](cf, "CFArrayGetCount")
	m.arrayGet = mustFn[func(uintptr, uintptr) uintptr](cf, "CFArrayGetValueAtIndex")
	m.dictGet = mustFn[func(uintptr, uintptr) uintptr](cf, "CFDictionaryGetValue")
	m.cfRetain = mustFn[func(uintptr) uintptr](cf, "CFRetain")
	m.cfRelease = mustFn[func(uintptr)](cf, "CFRelease")
	m.cfTrue = cfSym(cf, "kCFBooleanTrue")
	m.cfFalse = cfSym(cf, "kCFBooleanFalse")
	m.runLoop = mustFn[func()](cf, "CFRunLoopRun")
	m.stopLoop = mustFn[func(uintptr)](cf, "CFRunLoopStop")
	m.mainLoop = mustFn[func() uintptr](cf, "CFRunLoopGetMain")
	m.perform = mustFn[func(uintptr, uintptr, uintptr)](cf, "CFRunLoopPerformBlock")
	m.wake = mustFn[func(uintptr)](cf, "CFRunLoopWakeUp")
	m.commonModes = cfSym(cf, "kCFRunLoopCommonModes")
	m.preflight = mustFn[func() bool](cg, "CGPreflightScreenCaptureAccess")
	m.request = mustFn[func() bool](cg, "CGRequestScreenCaptureAccess")
	m.registerDisplay = mustFn[func(uintptr, uintptr) int32](cg, "CGDisplayRegisterReconfigurationCallback")
	if sym, err := purego.Dlsym(cg, "CGSessionCopyCurrentDictionary"); err == nil {
		purego.RegisterFunc(&m.sessionDict, sym)
	}
	bind(&m.addObserver, msg)

	m.clsShareable = objc.ID(objc.GetClass("SCShareableContent"))
	m.clsFilter = objc.ID(objc.GetClass("SCContentFilter"))
	m.clsConfig = objc.ID(objc.GetClass("SCStreamConfiguration"))
	m.clsStream = objc.ID(objc.GetClass("SCStream"))
	m.clsArray = objc.ID(objc.GetClass("NSArray"))
	m.clsString = objc.ID(objc.GetClass("NSString"))
	m.clsNumber = objc.ID(objc.GetClass("NSNumber"))
	m.clsDict = objc.ID(objc.GetClass("NSDictionary"))
	m.clsURL = objc.ID(objc.GetClass("NSURL"))
	m.clsWriter = objc.ID(objc.GetClass("AVAssetWriter"))
	m.clsInput = objc.ID(objc.GetClass("AVAssetWriterInput"))
	if m.clsShareable == 0 || m.clsStream == 0 || m.clsWriter == 0 {
		return nil, fmt.Errorf("desk: ScreenCaptureKit or AVFoundation did not register its classes")
	}
	m.loadInput()
	return m, nil
}

func dl(path string) uintptr {
	h, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		panic(err)
	}
	return h
}

func mustSym(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic(fmt.Sprintf("desk: %s: %v", name, err))
	}
	return p
}

func mustFn[T any](lib uintptr, name string) T {
	var fn T
	purego.RegisterFunc(&fn, mustSym(lib, name))
	return fn
}

func softDl(path string) uintptr {
	h, err := purego.Dlopen(path, purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return 0
	}
	return h
}

func softSym(lib uintptr, name string) uintptr {
	if lib == 0 {
		return 0
	}
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		return 0
	}
	return p
}

func softFn[T any](lib uintptr, name string) (fn T) {
	if p := softSym(lib, name); p != 0 {
		purego.RegisterFunc(&fn, p)
	}
	return fn
}

func softCF(lib uintptr, name string) uintptr {
	p := softSym(lib, name)
	if p == 0 {
		return 0
	}
	return readWord(p)
}

// loadInput 只软绑定。缺符号时采集照旧，注入自己停。
func (m *macAPI) loadInput() {
	cg := softDl("/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics")
	m.displayBounds = softFn[func(uint32) cgRect](cg, "CGDisplayBounds")
	m.cgMouse = softFn[func(uintptr, uint32, cgPoint, uint32) uintptr](cg, "CGEventCreateMouseEvent")
	m.cgScroll = softFn[func(uintptr, uint32, uint32, int32, int32, int32) uintptr](cg, "CGEventCreateScrollWheelEvent2")
	m.cgKey = softFn[func(uintptr, uint16, bool) uintptr](cg, "CGEventCreateKeyboardEvent")
	m.cgSetInt = softFn[func(uintptr, uint32, int64)](cg, "CGEventSetIntegerValueField")
	m.cgGetInt = softFn[func(uintptr, uint32) int64](cg, "CGEventGetIntegerValueField")
	m.cgSetFlags = softFn[func(uintptr, uint64)](cg, "CGEventSetFlags")
	m.cgUnicode = softFn[func(uintptr, uintptr, *uint16)](cg, "CGEventKeyboardSetUnicodeString")
	m.cgPost = softFn[func(uint32, uintptr)](cg, "CGEventPost")
	m.cgGetLoc = softFn[func(uintptr) cgPoint](cg, "CGEventGetLocation")
	m.cgSource = softFn[func(int32) uintptr](cg, "CGEventSourceCreate")
	m.cgWindowList = softFn[func(uint32, uint32) uintptr](cg, "CGWindowListCopyWindowInfo")

	hi := softDl("/System/Library/Frameworks/ApplicationServices.framework/Frameworks/HIServices.framework/HIServices")
	if hi == 0 {
		hi = softDl("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices")
	}
	m.axTrusted = softFn[func() bool](hi, "AXIsProcessTrusted")
	m.axTrustedOpts = softFn[func(uintptr) bool](hi, "AXIsProcessTrustedWithOptions")
	m.axPromptKey = softCF(hi, "kAXTrustedCheckOptionPrompt")
	m.axApp = softFn[func(int32) uintptr](hi, "AXUIElementCreateApplication")
	m.axCopy = softFn[func(uintptr, uintptr, *uintptr) int32](hi, "AXUIElementCopyAttributeValue")
	m.axSet = softFn[func(uintptr, uintptr, uintptr) int32](hi, "AXUIElementSetAttributeValue")
	m.axAction = softFn[func(uintptr, uintptr) int32](hi, "AXUIElementPerformAction")
	// 私有接口，没有公开的办法从辅助功能的窗口对象拿到 CGWindowID
	m.axWindowID = softFn[func(uintptr, *uint32) int32](hi, "_AXUIElementGetWindow")

	carbon := softDl("/System/Library/Frameworks/CoreServices.framework/Frameworks/CarbonCore.framework/CarbonCore")
	if carbon == 0 {
		carbon = softDl("/System/Library/Frameworks/CoreServices.framework/CoreServices")
	}
	m.ucTranslate = softFn[func(uintptr, uint16, uint16, uint32, uint32, uint32, *uint32, uintptr, *uintptr, *uint16) int32](carbon, "UCKeyTranslate")

	cf := softDl("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation")
	m.cfDataPtr = softFn[func(uintptr) unsafe.Pointer](cf, "CFDataGetBytePtr")

	hit := softDl("/System/Library/Frameworks/Carbon.framework/Frameworks/HIToolbox.framework/HIToolbox")
	m.tisASCII = softFn[func() uintptr](hit, "TISCopyCurrentASCIICapableKeyboardLayoutInputSource")
	m.tisProp = softFn[func(uintptr, uintptr) uintptr](hit, "TISGetInputSourceProperty")
	m.tisLayoutKey = softCF(hit, "kTISPropertyUnicodeKeyLayoutData")
	m.lmKbdType = softFn[func() uint8](hit, "LMGetKbdType")
}

// cfSym 取出导出的 CFStringRef / CFBooleanRef。符号地址上存的是指针。
func cfSym(lib uintptr, name string) uintptr {
	p, err := purego.Dlsym(lib, name)
	if err != nil {
		panic(fmt.Sprintf("desk: %s: %v", name, err))
	}
	return readWord(p)
}

func readWord(addr uintptr) uintptr

// displayPixels 是主屏的实际像素。CGDisplayPixelsWide 在 Retina 屏上给的是点数，只有一半。
func (m *macAPI) screenLocked() bool {
	if m.sessionDict == nil {
		return false
	}
	dict := m.sessionDict()
	if dict == 0 {
		return false
	}
	defer m.cfRelease(dict)
	key := m.string("CGSSessionScreenIsLocked")
	return m.dictGet(dict, uintptr(key)) == m.cfTrue
}

func (m *macAPI) displayPixels(id uint32) (int, int) {
	if mode := m.copyMode(id); mode != 0 {
		defer m.modeRelease(mode)
		if w, h := int(m.modeWide(mode)), int(m.modeHigh(mode)); w >= 2 && h >= 2 {
			return w, h
		}
	}
	return int(m.pixelsWide(id)), int(m.pixelsHigh(id))
}

// displayIDs 是现在接着的所有屏。
func (m *macAPI) displayIDs() []uint32 {
	var ids [32]uint32
	var n uint32
	if m.activeDisplays(uint32(len(ids)), &ids[0], &n) != 0 {
		return nil
	}
	return append([]uint32(nil), ids[:n]...)
}

// screenNames 是各块屏在系统设置里显示的名字（NSScreen.localizedName），按 CGDirectDisplayID 取。
// 调用方要在自动释放池里。
func (m *macAPI) screenNames() map[uint32]string {
	out := map[uint32]string{}
	cls := objc.ID(objc.GetClass("NSScreen"))
	if cls == 0 {
		return out
	}
	screens := m.init(cls, m.sel("screens"))
	if screens == 0 {
		return out
	}
	key := m.string("NSScreenNumber")
	for i := range m.count(screens, m.sel("count")) {
		s := m.at(screens, m.sel("objectAtIndex:"), i)
		num := m.sendObj(m.init(s, m.sel("deviceDescription")), m.sel("objectForKey:"), key)
		if num == 0 || !m.responds(s, m.sel("respondsToSelector:"), m.sel("localizedName")) {
			continue
		}
		out[uint32(m.integer(num, m.sel("unsignedIntValue")))] = m.goString(m.init(s, m.sel("localizedName")))
	}
	return out
}

func (m *macAPI) sel(name string) objc.SEL { return objc.RegisterName(name) }

func (m *macAPI) string(s string) objc.ID {
	b := append([]byte(s), 0)
	return m.nsString(m.clsString, m.sel("stringWithUTF8String:"), unsafe.Pointer(&b[0]))
}

func (m *macAPI) goString(o objc.ID) string {
	if o == 0 {
		return ""
	}
	p := m.utf8(o, m.sel("UTF8String"))
	if p == nil {
		return ""
	}
	var n int
	for *(*byte)(unsafe.Add(p, n)) != 0 {
		n++
	}
	// 要拷贝：非 ASCII 的字符串，UTF8String 给的是自动释放的临时缓冲，池一回收就被清掉
	return string(unsafe.Slice((*byte)(p), n))
}

// stayInBackground 让子进程不进程序坞。它是 Hive.app 里的可执行文件，一连上窗口服务器（ScreenCaptureKit 会连）
// 就被当成前台 App，却从不完成启动，程序坞里的图标会一直跳。要在碰 ScreenCaptureKit 之前调。
func (m *macAPI) stayInBackground() {
	if _, err := purego.Dlopen("/System/Library/Frameworks/AppKit.framework/AppKit", purego.RTLD_GLOBAL|purego.RTLD_NOW); err != nil {
		return
	}
	cls := objc.ID(objc.GetClass("NSApplication"))
	if cls == 0 {
		return
	}
	const activationPolicyProhibited = 2
	m.setInt(m.init(cls, m.sel("sharedApplication")), m.sel("setActivationPolicy:"), activationPolicyProhibited)
}

// 自动释放池按线程记：goroutine 在 push 和 pop 之间被挪到别的线程，pop 就会崩。
func (m *macAPI) withPool(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	p := m.poolPush()
	defer m.poolPop(p)
	fn()
}
