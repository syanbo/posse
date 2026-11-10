//go:build darwin

package desk

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

const (
	codecHEVCFourCC = 0x68766331
	codecH264FourCC = 0x61766331
	pixel420v       = 0x34323076
	benchWindow     = 2 * time.Second
)

// active 给屏幕采集的 Objective-C 回调用。一个进程里只有这一次采集。
var active *engine

type engine struct {
	mac *macAPI
	opt Options
	out io.Writer

	stream   objc.ID
	block    objc.Block
	observer objc.ID
	// parts 是 stream 用着的过滤器、输出对象（也是它的 delegate，SCStream 只弱引用它）和样本队列：
	// 流确认停下、放掉之后才放，换屏、换窗口、缩放时不再每次漏一份
	parts streamParts

	// 开始时没有辅助功能权限：ticker 每秒复查，打开了报一次 StatusAccessibility，只由 ticker 读写
	axWait bool

	streamMu  sync.Mutex
	displayID uint32
	// shown 是正在看的屏，注入和滚动按它换算；和 displayID 一起改，不用拿 streamMu 就能读
	shown atomic.Uint32
	// fpsCap 是每秒最多采几帧，父进程按码率改（CtrlFPS），0 是 60
	fpsCap atomic.Uint32
	// scale 是画面长边除以几（CtrlScale），0 和 1 都是原样
	scale atomic.Uint32
	// locked 是 Mac 锁着屏：照常采集，打字按键码注入
	locked   atomic.Bool
	dropping atomic.Bool

	// win 是只看的那个窗口（ID 为 0 看整块屏），和 displayID 一起在 streamMu 下改，读用 winMu
	winMu sync.Mutex
	win   watched
	// lockWin 是锁屏时只看着的窗口：锁屏界面不在窗口里，锁屏期间看主屏，解锁后换回它。在 streamMu 下读写
	lockWin uint32
	// gone 是正在看的窗口没了、退回了整块屏，下一次报屏列表时告诉手机
	gone atomic.Bool
	// checking 是每秒的窗口检查还没做完，不叠着做
	checking atomic.Bool

	encMu       sync.Mutex
	enc         uintptr
	codec       byte
	lowLatency  bool
	width       int
	height      int
	nalLen      int
	force       atomic.Bool
	encodeFails atomic.Int32 // 连着失败的编码次数，出一帧清零
	bitrate     atomic.Uint32
	bitrateSet  atomic.Bool
	forceDict   uintptr
	frameNumber uint64

	pendMu sync.Mutex
	pend   map[uint64]*stamp

	// 最近采到的一帧（retain 着）和它交给的编码器。画面停住后 refine 把它再编几遍补清楚
	lastMu      sync.Mutex
	last        uintptr
	lastSession uintptr
	lastAt      time.Time
	refineLeft  int
	wake        chan struct{}
	// 采集回调和 refine 都交帧，编号和时间戳要按交的顺序递增
	encodeMu sync.Mutex

	statMu  sync.Mutex
	samples []time.Duration
	fps     int
	asyncN  int
	phase   string

	wantLow, wantHW, wantPowerOff bool

	mov *movie

	born time.Time // 进程开始，量出第一帧前各步耗时

	writeMu  sync.Mutex
	stopping atomic.Bool
	stop     chan struct{}
	got      chan Frame
	err      error

	inMu sync.Mutex
	in   *injector
}

type stamp struct {
	session  uintptr // 交给哪个编码器的，换了编码器以后旧的报错不算
	capture  int64
	start    time.Time
	number   uint64
	returned atomic.Bool
}

type movie struct {
	writer objc.ID
	input  objc.ID
	url    objc.ID
	path   string
}

func runDarwin(o Options) error {
	if o.Frames == nil {
		o.Frames = os.Stdout
	}
	if o.Control == nil {
		o.Control = os.Stdin
	}
	if o.Log == nil {
		o.Log = os.Stderr
	}
	if o.LongEdge == 0 {
		o.LongEdge = 1920
	}
	if o.Bench && o.Mov != "" {
		return fmt.Errorf("desk: --bench and --mov cannot be used together")
	}
	mac, err := loadMac()
	if err != nil {
		return err
	}
	mac.stayInBackground()
	e := &engine{
		mac: mac, opt: o, out: o.Frames,
		pend: map[uint64]*stamp{}, stop: make(chan struct{}), wake: make(chan struct{}, 1),
		nalLen: 4, born: time.Now(),
	}
	// 低延迟码率控制默认开着。UsingHardwareAcceleratedVideoEncoder 读回来经常是 false，
	// 并不能说明这一路是软件编码，不用它决定开不开。
	e.wantLow = true
	active = e
	logResponsible(o.Log)
	if o.SelfTest {
		return e.selfTest()
	}
	if o.InputTest {
		return e.inputTest()
	}
	if !mac.preflight() {
		// 子进程只在用户要看桌面时才被拉起，这时弹系统授权框：它把 App 加进录屏列表，不弹就得用户自己去找
		mac.request()
		_ = WriteFrame(o.Frames, Frame{Kind: KindStatus, CaptureUnixNano: time.Now().UnixNano(), Payload: []byte{StatusNoScreen}})
		return fmt.Errorf("desk: screen recording is not allowed; turn on %s in System Settings > Privacy & Security > Screen & System Audio Recording", recordingAppName())
	}
	if err := registerDelegate(mac); err != nil {
		return err
	}
	if !e.accessibilityTrusted() {
		// 缺辅助功能不挡画面。提示是异步的，这次仍报未授权，用户打开后再拉起才生效。
		_ = WriteFrame(o.Frames, Frame{Kind: KindStatus, CaptureUnixNano: time.Now().UnixNano(), Payload: []byte{StatusNoAccessibility}})
		e.logf("accessibility is not allowed; turn on %s in System Settings > Privacy & Security > Accessibility", recordingAppName())
		e.axWait = true
	}
	e.force.Store(true)
	// 控制管道和采集都等跑圈起来再开始。提前 CFRunLoopStop 停不住还没开始的跑圈。
	boot := objc.NewBlock(func(objc.Block) {
		go e.control()
		go e.ticker()
		go e.refine()
		if o.Seconds > 0 {
			time.AfterFunc(time.Duration(o.Seconds)*time.Second, func() { e.halt(nil) })
		}
		if e.watchScreen() {
			e.begin()
		}
	})
	mac.perform(mac.mainLoop(), mac.commonModes, uintptr(boot))
	mac.wake(mac.mainLoop())
	mac.runLoop()
	e.summary()
	return e.err
}

func (e *engine) logf(format string, args ...any) {
	fmt.Fprintf(e.opt.Log, "desk: "+format+"\n", args...)
}

func (e *engine) begin() {
	e.block = objc.NewBlock(func(_ objc.Block, content, err objc.ID) {
		// 完成回调返回后这些对象就释放了，字符串和引用要在返回前拿走。
		var msg string
		if err != 0 {
			msg = e.mac.goString(e.mac.init(err, e.mac.sel("localizedDescription")))
		}
		if content != 0 {
			e.mac.retain(content, e.mac.sel("retain"))
		}
		go e.gotContent(content, msg)
	})
	e.mac.getContent(e.mac.clsShareable, e.mac.sel("getShareableContentWithCompletionHandler:"), uintptr(e.block))
}

func (e *engine) gotContent(content objc.ID, msg string) {
	defer func() {
		if content != 0 {
			e.mac.release(content, e.mac.sel("release"))
		}
	}()
	if content == 0 {
		if msg == "" {
			msg = "ScreenCaptureKit returned no displays"
		}
		e.halt(fmt.Errorf("desk: %s", msg))
		return
	}
	e.logf("shareable content after %s", time.Since(e.born).Round(time.Millisecond))
	displays := e.mac.init(content, e.mac.sel("displays"))
	n := e.mac.count(displays, e.mac.sel("count"))
	// 手机记着的那块屏还在就看它，不然看主屏（主屏不在列表里时看第一块）
	main := e.mac.mainDisplay()
	var want, fallback objc.ID
	var wantID, fallbackID uint32
	for i := range n {
		d := e.mac.at(displays, e.mac.sel("objectAtIndex:"), i)
		id := uint32(e.mac.integer(d, e.mac.sel("displayID")))
		if id != 0 && id == e.opt.Display {
			want, wantID = d, id
		}
		if fallback == 0 || id == main {
			fallback, fallbackID = d, id
		}
	}
	display, chosen := want, wantID
	if display == 0 {
		display, chosen = fallback, fallbackID
	}
	pw, ph := e.mac.displayPixels(chosen)
	if display == 0 || pw < 2 || ph < 2 {
		e.halt(fmt.Errorf("desk: no main display"))
		return
	}
	e.displayID = chosen
	e.shown.Store(chosen)
	tw, th := Fit(pw, ph, e.longEdge())
	e.width, e.height = tw, th
	codec := CodecHEVC
	if e.opt.Codec == "h264" {
		codec = CodecH264
	}
	if err := e.openEncoder(codec); err != nil {
		e.halt(err)
		return
	}
	e.writeConfig()
	if err := e.openMovie(); err != nil {
		e.halt(err)
		return
	}
	e.streamMu.Lock()
	err := e.openStream(e.displayFilter(display, content))
	e.streamMu.Unlock()
	if err != nil {
		e.halt(err)
		return
	}
	e.logf("capturing display %d %dx%d -> %dx%d %s low-latency=%v, started after %s", chosen, pw, ph, tw, th, codecName(e.codec), e.lowLatency, time.Since(e.born).Round(time.Millisecond))
	e.writeDisplays()
}

// displayFilter 是采整块屏的过滤器，去掉被排除的 App。
func (e *engine) displayFilter(display, content objc.ID) objc.ID {
	m := e.mac
	if len(e.opt.Exclude) == 0 {
		empty := m.emptyArray(m.clsArray, m.sel("array"))
		return m.initFilter(m.alloc(m.clsFilter, m.sel("alloc")), m.sel("initWithDisplay:excludingWindows:"), display, empty)
	}
	// exceptingWindows 留空：被排除的 App 不把单个窗口再捞回来。
	apps := e.excludedApplications(content)
	windows := m.emptyArray(m.clsArray, m.sel("array"))
	return m.initFilterApps(m.alloc(m.clsFilter, m.sel("alloc")), m.sel("initWithDisplay:excludingApplications:exceptingWindows:"), display, apps, windows)
}

// streamParts 是一个采集流用着、要等流停下才能放的对象。
type streamParts struct {
	filter, del objc.ID
	queue       uintptr
}

// releaseParts 放掉一个已经停下、放掉的流用过的东西。先在样本队列上排一个空任务等它跑完：
// 停下之前排进去的样本回调还在队列里的话，会用到输出对象，不能先放。
func (m *macAPI) releaseParts(p streamParts) {
	if p.queue != 0 {
		m.queueSync(p.queue, 0, drainCallback())
	}
	if p.filter != 0 {
		m.release(p.filter, m.sel("release"))
	}
	if p.del != 0 {
		m.release(p.del, m.sel("release"))
	}
	if p.queue != 0 {
		m.queueRelease(p.queue)
	}
}

func (e *engine) openStream(filter objc.ID) error {
	m := e.mac
	if filter == 0 {
		return fmt.Errorf("desk: content filter failed")
	}
	cfg := e.streamConfig()
	del := m.init(m.alloc(delegateClass, m.sel("alloc")), m.sel("init"))
	stream := m.initStream(m.alloc(m.clsStream, m.sel("alloc")), m.sel("initWithFilter:configuration:delegate:"), filter, cfg, del)
	m.release(cfg, m.sel("release"))
	name := append([]byte("posse.desk.capture"), 0)
	queue := m.queueCreate(unsafe.Pointer(&name[0]), 0)
	parts := streamParts{filter: filter, del: del, queue: queue}
	var nserr objc.ID
	if !m.addOutput(stream, m.sel("addStreamOutput:type:sampleHandlerQueue:error:"), del, 0, queue, &nserr) {
		msg := "could not add a stream output"
		if nserr != 0 {
			msg = m.goString(m.init(nserr, m.sel("localizedDescription")))
		}
		// 还没开始采集：流和它用的东西一起放掉
		m.release(stream, m.sel("release"))
		m.releaseParts(parts)
		return fmt.Errorf("desk: %s", msg)
	}
	e.stream = stream
	e.parts = parts
	started := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) {
		if err == 0 {
			started <- nil
			return
		}
		started <- fmt.Errorf("desk: %s", m.goString(m.init(err, m.sel("localizedDescription"))))
	})
	m.startCapture(stream, m.sel("startCaptureWithCompletionHandler:"), uintptr(block))
	err := <-started
	// 完成回调只调一次，已经调过了
	block.Release()
	return err
}

// streamConfig 是采集设置：输出尺寸、像素格式、帧率上限。开采集和运行中改帧率共用。
func (e *engine) streamConfig() objc.ID {
	m := e.mac
	fps := e.fpsCap.Load()
	if fps == 0 {
		fps = 60
	}
	cfg := m.init(m.alloc(m.clsConfig, m.sel("alloc")), m.sel("init"))
	m.setInt(cfg, m.sel("setWidth:"), e.width)
	m.setInt(cfg, m.sel("setHeight:"), e.height)
	// keepLast 一直留着一帧给 refine，编码器异步编着的也占着，3 个不够轮转
	m.setInt(cfg, m.sel("setQueueDepth:"), 5)
	m.setU32(cfg, m.sel("setPixelFormat:"), pixel420v)
	m.setInt(cfg, m.sel("setShowsCursor:"), 1)
	m.setTime(cfg, m.sel("setMinimumFrameInterval:"), cmMake(1, int32(fps)))
	if m.keyMatrix709 != 0 {
		m.setPtr(cfg, m.sel("setColorMatrix:"), m.keyMatrix709)
	}
	return cfg
}

// setFPS 改采集的帧率上限，编码器的预期帧率跟着改（它按这个分配每帧的比特）。
// ScreenCaptureKit 按间隔丢帧，画面停住时最后一帧照样交出来。父进程只在变了时才发，这里不去重：
// 上一次没改成功，同样的值再来还要再试。
func (e *engine) setFPS(fps uint32) {
	if fps == 0 || fps > 60 {
		return
	}
	e.streamMu.Lock()
	defer e.streamMu.Unlock()
	if e.stream == 0 || e.stopping.Load() {
		e.fpsCap.Store(fps)
		return
	}
	e.fpsCap.Store(fps)
	done := make(chan string, 1)
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) {
		msg := ""
		if err != 0 {
			msg = e.mac.goString(e.mac.init(err, e.mac.sel("localizedDescription")))
		}
		done <- msg
	})
	e.mac.withPool(func() {
		cfg := e.streamConfig()
		e.mac.updateConfig(e.stream, e.mac.sel("updateConfiguration:completionHandler:"), cfg, uintptr(block))
		// 流自己留一份，这里的引用放掉
		e.mac.release(cfg, e.mac.sel("release"))
	})
	select {
	case msg := <-done:
		block.Release()
		if msg != "" {
			e.logf("frame rate %d: %s", fps, msg)
			return
		}
	case <-time.After(2 * time.Second):
		// 回调还可能来，block 不能放
		e.logf("frame rate %d: no answer from ScreenCaptureKit", fps)
		return
	}
	e.encMu.Lock()
	if e.enc != 0 {
		e.mac.withPool(func() {
			n := e.mac.numLong(e.mac.clsNumber, e.mac.sel("numberWithLongLong:"), int64(fps))
			e.mac.vtSet(e.enc, e.mac.keyFPS, uintptr(n))
		})
	}
	e.encMu.Unlock()
	e.logf("frame rate cap %d", fps)
}

func (e *engine) openEncoder(codec byte) error {
	m := e.mac
	fourcc := uint32(codecHEVCFourCC)
	name := "hevc"
	if codec == CodecH264 {
		fourcc = codecH264FourCC
		name = "h264"
	}
	low := e.wantLow
	session, status := e.createSession(fourcc, low)
	if status != 0 && low {
		low = false
		if session != 0 {
			m.cfRelease(session)
		}
		session, status = e.createSession(fourcc, false)
	}
	if status != 0 || session == 0 {
		return fmt.Errorf("desk: %s encoder failed (%d)", name, status)
	}
	e.configure(session)
	e.encMu.Lock()
	e.enc = session
	e.codec = codec
	e.lowLatency = low
	e.encMu.Unlock()
	e.encodeFails.Store(0)
	e.force.Store(true)
	e.logf("%s encoder low-latency rate control %s", name, onOff(low))
	return nil
}

func (e *engine) createSession(fourcc uint32, low bool) (uintptr, int32) {
	m := e.mac
	var keys, vals []uintptr
	if low && m.keyLowLatency != 0 {
		keys = append(keys, m.keyLowLatency)
		vals = append(vals, uintptr(m.numBool(m.clsNumber, m.sel("numberWithBool:"), 1)))
	}
	if e.wantHW && m.keyRequireHW != 0 {
		keys = append(keys, m.keyRequireHW)
		vals = append(vals, uintptr(m.numBool(m.clsNumber, m.sel("numberWithBool:"), 1)))
	}
	var spec uintptr
	if len(keys) == 1 {
		spec = uintptr(m.dictOne(m.clsDict, m.sel("dictionaryWithObject:forKey:"), vals[0], keys[0]))
	} else if len(keys) > 1 {
		spec = uintptr(m.dictN(m.clsDict, m.sel("dictionaryWithObjects:forKeys:count:"), unsafe.Pointer(&vals[0]), unsafe.Pointer(&keys[0]), len(keys)))
	}
	var session uintptr
	status := m.vtCreate(0, int32(e.width), int32(e.height), fourcc, spec, 0, 0, vtCallback, 0, &session)
	return session, status
}

func (e *engine) configure(session uintptr) {
	m := e.mac
	m.withPool(func() {
		m.vtSet(session, m.keyRealTime, m.cfTrue)
		m.vtSet(session, m.keyReorder, m.cfFalse)
		m.vtSet(session, m.keySpeed, m.cfTrue)
		zero := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), 0)
		rate := int64(e.fpsCap.Load())
		if rate == 0 {
			rate = 60
		}
		fps := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), rate)
		m.vtSet(session, m.keyDelay, uintptr(zero))
		m.vtSet(session, m.keyFPS, uintptr(fps))
		m.vtSet(session, m.keyPrimaries, m.primaries709)
		m.vtSet(session, m.keyTransfer, m.transfer709)
		m.vtSet(session, m.keyYCbCr, m.matrix709)
		if e.wantPowerOff && m.keyPower != 0 {
			m.vtSet(session, m.keyPower, m.cfFalse)
		}
		e.applyBitrate(session, e.initialBitrate())
		if e.forceDict == 0 && m.keyForce != 0 {
			num := m.numBool(m.clsNumber, m.sel("numberWithBool:"), 1)
			dict := m.dictOne(m.clsDict, m.sel("dictionaryWithObject:forKey:"), uintptr(num), m.keyForce)
			e.forceDict = uintptr(m.retain(dict, m.sel("retain")))
		}
	})
}

// initialBitrate 是新开编码器的码率：父进程设过就沿用它。降、升分辨率和切屏都会重开编码器，
// 按新画面的起始码率开的话，慢链路上第一个关键帧就大得发不完。
func (e *engine) initialBitrate() uint32 {
	if e.bitrateSet.Load() {
		return e.bitrate.Load()
	}
	return PictureBitrate(e.width, e.height)
}

func (e *engine) applyBitrate(session uintptr, bits uint32) {
	if bits < rateFloor {
		bits = rateFloor
	}
	if bits > rateCeil {
		bits = rateCeil
	}
	m := e.mac
	m.withPool(func() {
		num := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), int64(bits))
		if st := m.vtSet(session, m.keyBitrate, uintptr(num)); st != 0 {
			e.logf("average bitrate rejected (%d)", st)
		}
		// 100ms 窗口的字节数 = 码率 / 80。硬件低延迟编码器对关键帧不一定守这个上限，设失败要看得见。
		bytes := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), int64(bits)/80)
		secs := m.numDouble(m.clsNumber, m.sel("numberWithDouble:"), 0.1)
		ids := [2]uintptr{uintptr(bytes), uintptr(secs)}
		arr := m.arrayCount(m.clsArray, m.sel("arrayWithObjects:count:"), unsafe.Pointer(&ids[0]), 2)
		st := m.vtSet(session, m.keyLimits, uintptr(arr))
		if st != 0 {
			e.logf("data rate limits rejected (%d)", st)
		}
	})
}

func (e *engine) onSample(sample uintptr) {
	if e.stopping.Load() || e.dropping.Load() {
		return
	}
	img := e.mac.sampleImage(sample)
	if img == 0 {
		return
	}
	session := e.holdEncoder()
	if session == 0 {
		return
	}
	e.keepLast(img, session)
	e.encode(img, session)
	e.mac.cfRelease(session)
}

// holdEncoder 取现在的编码器并 retain，用完 cfRelease。采集回调和 refine 都在别的协程里换编码器
// （切屏、改尺寸、停止）的同时编码，不 retain 的话可能拿着已经释放的会话去编。
func (e *engine) holdEncoder() uintptr {
	e.encMu.Lock()
	defer e.encMu.Unlock()
	if e.enc != 0 {
		e.mac.cfRetain(e.enc)
	}
	return e.enc
}

// refineFrames、refineIdle：画面停住后把最后一帧按采集节奏再编 refineFrames 遍。
// ScreenCaptureKit 画面不变就不交新帧，停下前那帧是动着的时候按每帧的码率预算编的，不补就一直糊着。
// 编码器拿同一张图对着上一帧的重建结果编残差，一遍比一遍清楚（Sunshine 这类画面不动也照常出帧的，
// 就是这样变清楚的）：实测 6 Mbps、1920×1080 的文字截图，滚动停下那帧亮度 PSNR 37 dB，
// 再编 10 遍 42.7、40 遍 47.9，和 20 Mbps 直接编的 48.4 差不多，40 遍共约 300 KB，码率控制照样管着。
// 停住的判定至少等 3 个采集间隔：帧率上限降到 10 帧时动着的画面也是 100ms 一帧，插进去补会抢走每帧的预算。
const (
	refineFrames = 40
	refineIdle   = 100 * time.Millisecond
)

func (e *engine) refineWait() time.Duration {
	fps := e.fpsCap.Load()
	if fps == 0 {
		fps = 60
	}
	return max(refineIdle, 3*time.Second/time.Duration(fps))
}

// keepLast 留住刚采到的一帧和它交给的编码器（都 retain，编码器留着地址就不会被新会话复用），叫醒 refine。
func (e *engine) keepLast(img, session uintptr) {
	e.mac.cfRetain(img)
	e.mac.cfRetain(session)
	e.lastMu.Lock()
	oldImg, oldSession := e.last, e.lastSession
	e.last, e.lastSession, e.lastAt, e.refineLeft = img, session, time.Now(), refineFrames
	e.lastMu.Unlock()
	e.dropLast(oldImg, oldSession)
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

func (e *engine) dropLast(img, session uintptr) {
	if img != 0 {
		e.mac.cfRelease(img)
	}
	if session != 0 {
		e.mac.cfRelease(session)
	}
}

// refine 没有要补的帧时睡着，等 keepLast 叫醒。
func (e *engine) refine() {
	defer func() {
		e.lastMu.Lock()
		img, session := e.last, e.lastSession
		e.last, e.lastSession = 0, 0
		e.lastMu.Unlock()
		e.dropLast(img, session)
	}()
	for {
		select {
		case <-e.stop:
			return
		case <-e.wake:
		}
		for e.refineOnce() {
		}
	}
}

// refineOnce 等一个采集间隔，到了停住的时候补编一遍。还有要补的（包括还没停住）返回 true。
func (e *engine) refineOnce() bool {
	fps := e.fpsCap.Load()
	if fps == 0 {
		fps = 60
	}
	select {
	case <-e.stop:
		return false
	case <-time.After(time.Second / time.Duration(fps)):
	}
	if e.stopping.Load() || e.dropping.Load() {
		return false
	}
	session := e.holdEncoder()
	defer func() {
		if session != 0 {
			e.mac.cfRelease(session)
		}
	}()
	e.lastMu.Lock()
	img := e.last
	if img == 0 || e.refineLeft == 0 {
		e.lastMu.Unlock()
		return false
	}
	if time.Since(e.lastAt) < e.refineWait() {
		e.lastMu.Unlock()
		return true
	}
	if session == 0 || e.lastSession != session {
		// 编码器重开过（改尺寸、切屏），旧帧的尺寸可能对不上，等新画面
		e.refineLeft = 0
		e.lastMu.Unlock()
		return false
	}
	e.refineLeft--
	e.mac.cfRetain(img)
	e.lastMu.Unlock()
	e.encode(img, session)
	e.mac.cfRelease(img)
	return true
}

// encode 把一帧交给编码器，不管 img 的引用计数。
func (e *engine) encode(img, session uintptr) {
	e.encodeMu.Lock()
	defer e.encodeMu.Unlock()
	if e.bitrateSet.CompareAndSwap(true, false) {
		e.applyBitrate(session, e.bitrate.Load())
	}
	st := &stamp{session: session, capture: time.Now().UnixNano(), start: time.Now(), number: atomic.AddUint64(&e.frameNumber, 1)}
	e.pendMu.Lock()
	e.pend[st.number] = st
	e.pendMu.Unlock()
	var props uintptr
	if e.force.CompareAndSwap(true, false) {
		props = e.forceDict
	}
	pts := cmMake(int64(st.number), 60)
	var flags uint32
	// 时长无效时 DataRateLimits 不生效，第一帧关键帧会远超 100ms 窗口。
	status := e.mac.vtEncode(session, img, pts, cmMake(1, 60), props, uintptr(st.number), &flags)
	st.returned.Store(true)
	if status != 0 {
		e.pendMu.Lock()
		delete(e.pend, st.number)
		e.pendMu.Unlock()
		e.encMu.Lock()
		current := e.enc == session
		e.encMu.Unlock()
		// 编码器刚被换掉（切屏、停止）时交给旧会话的帧会失败，不算编码出错
		if current {
			e.logf("encode frame: %d", status)
			e.encodeFailed(status)
		}
	}
}

// encodeFailLimit 次编码连着失败就退出，由常驻进程重新拉起子进程、重开编码器（RustDesk 硬件编码连续失败 3 次也是重建）。
const encodeFailLimit = 3

func (e *engine) encodeFailed(status int32) {
	if e.encodeFails.Add(1) == encodeFailLimit {
		go e.halt(fmt.Errorf("desk: encoding failed %d times in a row (%d)", encodeFailLimit, status))
	}
}

func vtOut(_, source uintptr, status int32, _ uint32, sample uintptr) {
	e := active
	if e == nil {
		return
	}
	e.pendMu.Lock()
	st := e.pend[uint64(source)]
	delete(e.pend, uint64(source))
	e.pendMu.Unlock()
	if st != nil && status != 0 {
		e.encMu.Lock()
		current := e.enc == st.session
		e.encMu.Unlock()
		if current {
			e.logf("encoded frame: %d", status)
			e.encodeFailed(status)
		}
	}
	if status != 0 || sample == 0 || st == nil {
		return
	}
	e.encodeFails.Store(0)
	if st.number == 1 {
		e.logf("first frame after %s", time.Since(e.born).Round(time.Millisecond))
	}
	async := st.returned.Load()
	e.note(time.Since(st.start), async)
	payload, key := e.annex(sample)
	if randomAccess(payload) {
		key = true
	}
	if e.mov != nil {
		e.mov.append(e.mac, sample)
	}
	if len(payload) == 0 {
		return
	}
	e.write(Frame{Kind: KindVideo, Keyframe: key, Number: st.number, CaptureUnixNano: st.capture, Payload: payload})
	if e.got != nil {
		select {
		case e.got <- Frame{Kind: KindVideo, Keyframe: key, Payload: payload}:
		default:
		}
	}
}

func (e *engine) annex(sample uintptr) ([]byte, bool) {
	key := e.syncSample(sample)
	format := e.mac.sampleFormat(sample)
	var sets []byte
	if key {
		sets = e.parameterSets(format)
	}
	data := e.mac.sampleData(sample)
	if data == 0 {
		return sets, key
	}
	n := e.mac.blockLength(data)
	if n == 0 {
		return sets, key
	}
	raw := make([]byte, n)
	if e.mac.blockCopy(data, 0, n, unsafe.Pointer(&raw[0])) != 0 {
		return sets, key
	}
	nal := e.nalLen
	if nal <= 0 {
		nal = 4
	}
	out := make([]byte, 0, len(sets)+len(raw)+16)
	out = append(out, sets...)
	for i := 0; i+nal <= len(raw); {
		ln := 0
		for k := range nal {
			ln = ln<<8 | int(raw[i+k])
		}
		i += nal
		if ln <= 0 || i+ln > len(raw) {
			break
		}
		out = append(out, 0, 0, 0, 1)
		out = append(out, raw[i:i+ln]...)
		i += ln
	}
	return out, key
}

func (e *engine) syncSample(sample uintptr) bool {
	arr := e.mac.sampleAttach(sample, 0)
	if arr == 0 || e.mac.arrayCountCF(arr) == 0 {
		return true
	}
	dict := e.mac.arrayGet(arr, 0)
	v := e.mac.dictGet(dict, e.mac.keyNotSync)
	return v == 0 || v == e.mac.cfFalse
}

func (e *engine) parameterSets(format uintptr) []byte {
	if format == 0 {
		return nil
	}
	fn := e.mac.hevcSet
	if e.codec == CodecH264 {
		fn = e.mac.h264Set
	}
	var count uintptr
	var nal int32
	if fn(format, 0, nil, nil, &count, &nal) != 0 || count == 0 {
		return nil
	}
	if nal == 3 || nal == 4 {
		e.nalLen = int(nal)
	}
	var buf []byte
	for i := range count {
		var p unsafe.Pointer
		var sz uintptr
		if fn(format, i, &p, &sz, nil, nil) != 0 || p == nil || sz == 0 {
			break
		}
		buf = append(buf, 0, 0, 0, 1)
		buf = append(buf, unsafe.Slice((*byte)(p), sz)...)
	}
	return buf
}

func (e *engine) writeConfig() {
	pw, ph := e.mac.displayPixels(e.displayID)
	if w := e.watching(); w.ID != 0 {
		pw, ph = w.pw, w.ph
	}
	fw, fh := Fit(pw, ph, e.opt.LongEdge)
	e.write(Frame{
		Kind: KindConfig, CaptureUnixNano: time.Now().UnixNano(),
		Payload: MarshalConfig(Config{Codec: e.codec, Width: e.width, Height: e.height, FullWidth: fw, FullHeight: fh}),
	})
}

// longEdge 是现在的输出长边：手机给的上限，码率压到最低一档时减半（CtrlScale）。
func (e *engine) longEdge() int {
	if s := int(e.scale.Load()); s > 1 {
		return max(e.opt.LongEdge/s, 2)
	}
	return e.opt.LongEdge
}

// setScale 改画面长边的除数，按新尺寸重开编码器和采集（和改分辨率走同一条路）。
func (e *engine) setScale(scale uint32) {
	if scale == 0 || scale > 4 || e.scale.Swap(scale) == scale {
		return
	}
	e.logf("picture scale 1/%d", scale)
	if err := e.rebuildDisplay(0); err != nil {
		e.halt(err)
	}
}

func (e *engine) write(f Frame) {
	e.writeMu.Lock()
	err := WriteFrame(e.out, f)
	e.writeMu.Unlock()
	if err != nil {
		e.halt(nil)
	}
}

func (e *engine) note(d time.Duration, async bool) {
	e.statMu.Lock()
	e.samples = append(e.samples, d)
	e.fps++
	if async {
		e.asyncN++
	}
	e.statMu.Unlock()
}

func (e *engine) ticker() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var elapsed int
	for {
		select {
		case <-e.stop:
			return
		case <-t.C:
			elapsed++
			if !e.opt.Quiet {
				e.logStats(false)
			}
			e.write(Frame{Kind: KindHeartbeat, CaptureUnixNano: time.Now().UnixNano()})
			if e.watching().ID != 0 && e.checking.CompareAndSwap(false, true) {
				go func() {
					defer e.checking.Store(false)
					e.checkWindow()
				}()
			}
			if e.axWait && e.axGranted() {
				e.axWait = false
				e.logf("accessibility allowed")
				e.report(StatusAccessibility)
			}
			if e.opt.Probe {
				switch elapsed {
				case 2:
					e.setProbe("no-lowlat", false, true, false)
				case 4:
					e.setProbe("hw-lowlat-poweroff", true, true, true)
				case 6:
					e.halt(nil)
				}
				continue
			}
			if !e.opt.Bench {
				continue
			}
			if elapsed == int(benchWindow/time.Second) {
				e.switchCodec(CodecH264)
			}
			if elapsed == 2*int(benchWindow/time.Second) {
				e.halt(nil)
			}
		}
	}
}

func (e *engine) setProbe(name string, low, hw, powerOff bool) {
	e.logStats(true)
	e.statMu.Lock()
	e.samples = nil
	e.asyncN = 0
	e.fps = 0
	e.statMu.Unlock()
	e.phase = name
	e.wantLow, e.wantHW, e.wantPowerOff = low, hw, powerOff
	e.encMu.Lock()
	old := e.enc
	e.enc = 0
	e.encMu.Unlock()
	if old != 0 {
		e.mac.vtComplete(old, cmTime{})
		e.mac.vtInvalidate(old)
		e.mac.cfRelease(old)
	}
	if err := e.openEncoder(e.codec); err != nil {
		e.halt(err)
		return
	}
	e.force.Store(true)
	e.logf("probe %s lowlat=%v require-hw=%v poweroff=%v", name, low, hw, powerOff)
}

func (e *engine) phaseLabel() string {
	if e.phase != "" {
		return e.phase
	}
	return codecName(e.codec)
}

func (e *engine) switchCodec(codec byte) {
	e.logStats(true)
	e.statMu.Lock()
	e.samples = nil
	e.statMu.Unlock()
	e.encMu.Lock()
	old := e.enc
	e.enc = 0
	e.encMu.Unlock()
	if old != 0 {
		e.mac.vtComplete(old, cmTime{})
		e.mac.vtInvalidate(old)
		e.mac.cfRelease(old)
	}
	if err := e.openEncoder(codec); err != nil {
		e.halt(err)
		return
	}
	e.writeConfig()
}

func (e *engine) logStats(final bool) {
	e.statMu.Lock()
	samples := append([]time.Duration(nil), e.samples...)
	fps := e.fps
	e.fps = 0
	async := 0
	if len(samples) > 0 {
		async = e.asyncN * 100 / len(samples)
	}
	e.statMu.Unlock()
	if len(samples) == 0 {
		e.logf("%s waiting for frames", e.phaseLabel())
		return
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	tag := "rolling"
	if final {
		tag = "done"
	}
	e.logf("%s %s low-latency=%v async=%d%% n=%d fps=%d p50=%s p95=%s", e.phaseLabel(), tag, e.wantLow, async, len(samples), fps, samples[len(samples)*50/100], samples[min(len(samples)*95/100, len(samples)-1)])
}

// mediaDuration 读 url 处媒体文件的时长。
func (m *macAPI) mediaDuration(url objc.ID) cmTime {
	asset := m.urlAsset(objc.ID(objc.GetClass("AVURLAsset")), m.sel("assetWithURL:"), url)
	// CMTime 有 24 字节：x86_64 上要走 objc_msgSend_stret，objc.Send 按架构选
	return objc.Send[cmTime](asset, m.sel("duration"))
}

func (e *engine) summary() {
	e.logStats(true)
	if e.mov == nil {
		return
	}
	d := e.mac.mediaDuration(e.mov.url)
	if d.Timescale > 0 {
		e.logf("mov %s duration %.2fs", e.mov.path, float64(d.Value)/float64(d.Timescale))
	}
}

func (e *engine) control() {
	for {
		c, err := ReadControl(e.opt.Control)
		if err != nil {
			e.halt(nil)
			return
		}
		switch c.Kind {
		case CtrlStop:
			e.halt(nil)
			return
		case CtrlKeyframe:
			e.force.Store(true)
		case CtrlBitrate:
			e.bitrate.Store(c.Bitrate)
			e.bitrateSet.Store(true)
		case CtrlInput:
			e.applyInput(c.Events)
		case CtrlDisplay:
			// 重开采集要等 ScreenCaptureKit 回调，不挡后面的输入
			go e.switchDisplay(c.Display)
		case CtrlFPS:
			go e.setFPS(c.FPS)
		case CtrlScale:
			go e.setScale(c.Scale)
		case CtrlWindows:
			go e.writeWindows()
		case CtrlWindow:
			go e.switchWindow(c.Window)
		}
	}
}

func (e *engine) halt(err error) {
	if !e.stopping.CompareAndSwap(false, true) {
		return
	}
	e.releaseInput()
	e.err = err
	if err != nil {
		e.logf("%s", err.Error())
	}
	e.streamMu.Lock()
	e.stopStream()
	e.streamMu.Unlock()
	e.encMu.Lock()
	session := e.enc
	e.enc = 0
	e.encMu.Unlock()
	if session != 0 {
		e.mac.vtComplete(session, cmTime{})
		e.mac.vtInvalidate(session)
		e.mac.cfRelease(session)
	}
	if e.mov != nil {
		e.mov.finish(e.mac)
	}
	close(e.stop)
	e.mac.stopLoop(e.mac.mainLoop())
}

func (e *engine) openMovie() error {
	if e.opt.Mov == "" {
		return nil
	}
	_ = os.Remove(e.opt.Mov)
	m := e.mac
	url := m.fileURL(m.clsURL, m.sel("fileURLWithPath:"), m.string(e.opt.Mov))
	var nserr objc.ID
	writer := m.initWriter(m.alloc(m.clsWriter, m.sel("alloc")), m.sel("initWithURL:fileType:error:"), url, m.string("com.apple.quicktime-movie"), &nserr)
	if writer == 0 {
		msg := "could not create a movie"
		if nserr != 0 {
			msg = m.goString(m.init(nserr, m.sel("localizedDescription")))
		}
		return fmt.Errorf("desk: %s", msg)
	}
	e.mov = &movie{writer: writer, url: m.retain(url, m.sel("retain")), path: e.opt.Mov}
	return nil
}

func (mv *movie) append(m *macAPI, sample uintptr) {
	if mv.input == 0 {
		format := m.sampleFormat(sample)
		input := m.writerInput(m.clsInput, m.sel("assetWriterInputWithMediaType:outputSettings:sourceFormatHint:"), m.string("vide"), 0, format)
		if input == 0 {
			return
		}
		input = m.retain(input, m.sel("retain"))
		m.setBool(input, m.sel("setExpectsMediaDataInRealTime:"), 1)
		m.addInput(mv.writer, m.sel("addInput:"), input)
		if !m.startWriting(mv.writer, m.sel("startWriting")) {
			return
		}
		m.startSession(mv.writer, m.sel("startSessionAtSourceTime:"), cmMake(0, 60))
		mv.input = input
	}
	if !m.appendSample(mv.input, m.sel("appendSampleBuffer:"), sample) {
		return
	}
}

func (mv *movie) finish(m *macAPI) {
	if mv.input == 0 {
		return
	}
	m.markFinished(mv.input, m.sel("markAsFinished"))
	done := make(chan struct{})
	block := objc.NewBlock(func(objc.Block) { close(done) })
	m.finishWriting(mv.writer, m.sel("finishWritingWithCompletionHandler:"), uintptr(block))
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// drainCallback 是给 dispatch_sync_f 的空函数，只建一次（purego 的回调个数有上限）
var drainCallback = sync.OnceValue(func() uintptr {
	return purego.NewCallback(func(uintptr) {})
})

var (
	delegateClass   objc.ID
	vtCallback      uintptr
	displayCallback uintptr
	delegateOnce    sync.Once
)

func registerDelegate(m *macAPI) error {
	var err error
	delegateOnce.Do(func() {
		vtCallback = purego.NewCallback(vtOut)
		displayCallback = purego.NewCallback(displayReconfigured)
		var ps []*objc.Protocol
		for _, name := range []string{"SCStreamOutput", "SCStreamDelegate"} {
			if p := objc.GetProtocol(name); p != nil {
				ps = append(ps, p)
			}
		}
		cls, regErr := objc.RegisterClass("PosseDeskOutput", objc.GetClass("NSObject"), ps, nil, []objc.MethodDef{
			{Cmd: m.sel("stream:didOutputSampleBuffer:ofType:"), Fn: streamDidOutput},
			{Cmd: m.sel("stream:didStopWithError:"), Fn: streamDidStop},
			{Cmd: m.sel("screenLocked:"), Fn: screenLocked},
			{Cmd: m.sel("screenUnlocked:"), Fn: screenUnlocked},
		})
		if regErr != nil {
			err = fmt.Errorf("desk: %w", regErr)
			return
		}
		delegateClass = objc.ID(cls)
	})
	return err
}

func (e *engine) selfTest() error {
	if err := registerDelegate(e.mac); err != nil {
		return err
	}
	e.got = make(chan Frame, 4)
	e.width, e.height = 64, 64
	for _, codec := range []byte{CodecHEVC, CodecH264} {
		if err := e.openEncoder(codec); err != nil {
			return err
		}
		buf, err := solid420(64, 64)
		if err != nil {
			return err
		}
		e.force.Store(true)
		st := &stamp{capture: time.Now().UnixNano(), start: time.Now(), number: 1}
		e.pendMu.Lock()
		e.pend[st.number] = st
		e.pendMu.Unlock()
		var flags uint32
		status := e.mac.vtEncode(e.enc, buf, cmMake(1, 60), cmMake(1, 60), e.forceDict, uintptr(st.number), &flags)
		if status != 0 {
			e.mac.cfRelease(buf)
			return fmt.Errorf("desk: selftest encode %s: %d", codecName(codec), status)
		}
		e.mac.vtComplete(e.enc, cmTime{})
		select {
		case f := <-e.got:
			e.logf("selftest %s low-latency=%v bytes=%d key=%v %s", codecName(codec), e.lowLatency, len(f.Payload), f.Keyframe, time.Since(st.start))
			if len(f.Payload) < 5 || f.Payload[0] != 0 || f.Payload[1] != 0 || f.Payload[2] != 0 || f.Payload[3] != 1 {
				e.mac.cfRelease(buf)
				return fmt.Errorf("desk: selftest %s is not Annex B", codecName(codec))
			}
		case <-time.After(3 * time.Second):
			e.mac.cfRelease(buf)
			return fmt.Errorf("desk: selftest %s produced no frame", codecName(codec))
		}
		e.mac.vtInvalidate(e.enc)
		e.mac.cfRelease(e.enc)
		e.enc = 0
		e.mac.cfRelease(buf)
	}
	return nil
}

func solid420(w, h int) (uintptr, error) {
	cv, err := purego.Dlopen("/System/Library/Frameworks/CoreVideo.framework/CoreVideo", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		return 0, err
	}
	create := mustFn[func(uintptr, uintptr, uintptr, uint32, uintptr, *uintptr) int32](cv, "CVPixelBufferCreate")
	lock := mustFn[func(uintptr, uintptr) int32](cv, "CVPixelBufferLockBaseAddress")
	unlock := mustFn[func(uintptr, uintptr) int32](cv, "CVPixelBufferUnlockBaseAddress")
	plane := mustFn[func(uintptr, uintptr) unsafe.Pointer](cv, "CVPixelBufferGetBaseAddressOfPlane")
	stride := mustFn[func(uintptr, uintptr) uintptr](cv, "CVPixelBufferGetBytesPerRowOfPlane")
	var buf uintptr
	if rc := create(0, uintptr(w), uintptr(h), pixel420v, 0, &buf); rc != 0 || buf == 0 {
		return 0, fmt.Errorf("desk: pixel buffer %d", rc)
	}
	lock(buf, 0)
	fill(plane(buf, 0), int(stride(buf, 0)), h, w, 140)
	fill(plane(buf, 1), int(stride(buf, 1)), h/2, w, 128)
	unlock(buf, 0)
	return buf, nil
}

func fill(base unsafe.Pointer, stride, rows, cols int, value byte) {
	if base == nil {
		return
	}
	for row := range rows {
		line := unsafe.Slice((*byte)(unsafe.Add(base, row*stride)), cols)
		for i := range line {
			line[i] = value
		}
	}
}

func (e *engine) watchScreen() bool {
	obs := e.mac.init(e.mac.alloc(delegateClass, e.mac.sel("alloc")), e.mac.sel("init"))
	center := e.mac.init(objc.ID(objc.GetClass("NSDistributedNotificationCenter")), e.mac.sel("defaultCenter"))
	add := e.mac.sel("addObserver:selector:name:object:")
	e.mac.addObserver(center, add, uintptr(obs), uintptr(e.mac.sel("screenLocked:")), uintptr(e.mac.string("com.apple.screenIsLocked")), 0)
	e.mac.addObserver(center, add, uintptr(obs), uintptr(e.mac.sel("screenUnlocked:")), uintptr(e.mac.string("com.apple.screenIsUnlocked")), 0)
	e.observer = obs
	// 先订通知再查：这里在主线程上，通知要等返回后才投递，查完就解锁也不会漏。
	// 没锁也报：上一个子进程可能死在解锁那一刻、没报解锁，手机要靠这一报离开锁屏（重复的由父进程滤掉）
	if e.mac.screenLocked() {
		e.locked.Store(true)
		e.report(StatusLocked)
		e.logf("screen is locked")
		go e.wakeLocked()
	} else {
		e.report(StatusUnlocked)
	}
	if rc := e.mac.registerDisplay(displayCallback, 0); rc != 0 {
		e.logf("display reconfiguration callback: %d", rc)
	}
	return true
}

func (e *engine) report(code byte) {
	e.write(Frame{Kind: KindStatus, CaptureUnixNano: time.Now().UnixNano(), Payload: []byte{code}})
}

func (e *engine) onLocked() {
	if e.stopping.Load() || !e.locked.CompareAndSwap(false, true) {
		return
	}
	e.report(StatusLocked)
	e.logf("screen locked")
	go func() {
		e.streamMu.Lock()
		var err error
		// 锁了马上又解开时解锁的回调可能先跑完，这时不用离开窗口
		w := e.watching()
		left := w.ID != 0 && e.locked.Load() && !e.stopping.Load() && e.stream != 0
		if left {
			e.lockWin = w.ID
			e.logf("leaving window %d while locked", w.ID)
			err = e.displayLocked(e.mac.mainDisplay(), true)
		}
		e.streamMu.Unlock()
		if err != nil {
			e.halt(err)
			return
		}
		if left {
			e.writeDisplays()
		}
	}()
}

func (e *engine) onUnlocked() {
	if e.stopping.Load() || !e.locked.CompareAndSwap(true, false) {
		return
	}
	e.report(StatusUnlocked)
	e.logf("screen unlocked")
	go func() {
		e.streamMu.Lock()
		id := e.lockWin
		e.lockWin = 0
		var err error
		if id != 0 && !e.locked.Load() && !e.stopping.Load() && e.stream != 0 {
			err = e.rebuildWindowLocked(id, true)
		}
		e.streamMu.Unlock()
		if err != nil {
			e.halt(err)
			return
		}
		if id != 0 {
			e.writeDisplays()
		}
	}()
}

// onDisplaysChanged 是拔插屏、改分辨率、改排布：正在看的屏还在就留在它上面，不在了回主屏。
func (e *engine) onDisplaysChanged() {
	if err := e.rebuildDisplay(0); err != nil {
		e.halt(err)
		return
	}
	e.writeDisplays()
}

// switchDisplay 是手机选了另一块屏。那块屏不在了（刚拔掉）就留在现在这块。
func (e *engine) switchDisplay(id uint32) {
	if err := e.rebuildDisplay(id); err != nil {
		e.halt(err)
		return
	}
	e.writeDisplays()
}

// writeDisplays 把所有的屏、正在看的那块和只看的窗口报给父进程。
func (e *engine) writeDisplays() {
	var list []Display
	e.mac.withPool(func() { list = e.displayList() })
	ds := Displays{Current: e.shown.Load(), List: list, Gone: e.gone.Swap(false)}
	if w := e.watching(); w.ID != 0 {
		ds.Window = &w.Window
	}
	b, err := json.Marshal(ds)
	if err != nil {
		return
	}
	e.write(Frame{Kind: KindDisplays, CaptureUnixNano: time.Now().UnixNano(), Payload: b})
}

// rebuildDisplay 换到 want 这块屏重新采，want 为 0 是留在现在看的画面上（只看窗口时按窗口现在的大小重开）。
// 要换的屏不在了：手机点选的（want 不为 0）不动，正在看的没了回主屏。屏和分辨率都没变时什么也不做。
func (e *engine) rebuildDisplay(want uint32) error {
	e.streamMu.Lock()
	defer e.streamMu.Unlock()
	if e.stopping.Load() || e.stream == 0 {
		return nil
	}
	if want != 0 {
		// 锁屏期间手机自己选了屏，解锁后不再换回原来的窗口
		e.lockWin = 0
	}
	if w := e.watching(); want == 0 && w.ID != 0 {
		return e.rebuildWindowLocked(w.ID, false)
	}
	return e.displayLocked(want, false)
}

// displayLocked 是 rebuildDisplay 换整块屏的部分，force 是看的屏和尺寸没变也重开。只看窗口时总是重开。
// 要拿着 streamMu。
func (e *engine) displayLocked(want uint32, force bool) error {
	online := e.mac.displayIDs()
	id := want
	if id == 0 {
		id = e.displayID
	}
	if !slices.Contains(online, id) {
		if want != 0 && !force {
			e.logf("display %d is gone, staying on %d", want, e.displayID)
			return nil
		}
		id = e.mac.mainDisplay()
	}
	pw, ph := e.mac.displayPixels(id)
	tw, th := Fit(pw, ph, e.longEdge())
	windowed := e.watching().ID != 0
	if !force && !windowed && id == e.displayID && tw == e.width && th == e.height {
		return nil
	}
	e.logf("display %d %dx%d -> %d %dx%d", e.displayID, e.width, e.height, id, tw, th)
	display, content, err := e.fetchDisplay(id)
	if err != nil {
		if want != 0 && !force {
			// 旧的采集还没停，接着看现在的画面
			e.logf("switch to display %d: %v", id, err)
			return nil
		}
		return err
	}
	defer e.mac.release(display, e.mac.sel("release"))
	if content != 0 {
		defer e.mac.release(content, e.mac.sel("release"))
	}
	e.stopStream()
	if err := e.resizeLocked(tw, th); err != nil {
		return err
	}
	e.setWatched(watched{})
	e.displayID = id
	e.shown.Store(id)
	e.writeConfig()
	e.force.Store(true)
	return e.openStream(e.displayFilter(display, content))
}

// resizeLocked 在输出尺寸变了时按新尺寸重开编码器。采集要先停。
func (e *engine) resizeLocked(tw, th int) error {
	if tw == e.width && th == e.height {
		return nil
	}
	e.width, e.height = tw, th
	e.encMu.Lock()
	old := e.enc
	e.enc = 0
	e.encMu.Unlock()
	if old != 0 {
		e.mac.vtComplete(old, cmTime{})
		e.mac.vtInvalidate(old)
		e.mac.cfRelease(old)
	}
	return e.openEncoder(e.codec)
}

func (e *engine) fetchDisplay(id uint32) (objc.ID, objc.ID, error) {
	return e.fetchShareable(fmt.Sprintf("display %d", id), func(content objc.ID) objc.ID {
		displays := e.mac.init(content, e.mac.sel("displays"))
		for i := range e.mac.count(displays, e.mac.sel("count")) {
			d := e.mac.at(displays, e.mac.sel("objectAtIndex:"), i)
			if uint32(e.mac.integer(d, e.mac.sel("displayID"))) == id {
				return d
			}
		}
		return 0
	})
}

// fetchShareable 取一次分享内容，用 pick 从里面挑出要采的屏或窗口。返回的两个对象都已 retain，用完要 release。
func (e *engine) fetchShareable(what string, pick func(content objc.ID) objc.ID) (objc.ID, objc.ID, error) {
	type result struct {
		found   objc.ID
		content objc.ID
		err     error
	}
	ch := make(chan result, 1)
	block := objc.NewBlock(func(_ objc.Block, content, nserr objc.ID) {
		if content == 0 {
			msg := "no shareable content"
			if nserr != 0 {
				msg = e.mac.goString(e.mac.init(nserr, e.mac.sel("localizedDescription")))
			}
			ch <- result{err: fmt.Errorf("%s", msg)}
			return
		}
		found := pick(content)
		if found == 0 {
			ch <- result{err: fmt.Errorf("%s is gone", what)}
			return
		}
		ch <- result{found: e.mac.retain(found, e.mac.sel("retain")), content: e.mac.retain(content, e.mac.sel("retain"))}
	})
	e.mac.getContent(e.mac.clsShareable, e.mac.sel("getShareableContentWithCompletionHandler:"), uintptr(block))
	select {
	case r := <-ch:
		return r.found, r.content, r.err
	case <-time.After(3 * time.Second):
		return 0, 0, fmt.Errorf("shareable content timed out")
	}
}

// excludedApplications 从这次分享内容里按 bundle id 找出要排除的 App。
func (e *engine) excludedApplications(content objc.ID) objc.ID {
	m := e.mac
	empty := m.emptyArray(m.clsArray, m.sel("array"))
	if content == 0 {
		for _, id := range e.opt.Exclude {
			e.logf("exclude app %s is not running", id)
		}
		return empty
	}
	list := m.init(content, m.sel("applications"))
	n := m.count(list, m.sel("count"))
	ids := make([]string, n)
	objs := make([]objc.ID, n)
	for i := range n {
		app := m.at(list, m.sel("objectAtIndex:"), i)
		objs[i] = app
		ids[i] = m.goString(m.init(app, m.sel("bundleIdentifier")))
	}
	hit, missing := pickExcluded(ids, e.opt.Exclude)
	for _, id := range missing {
		e.logf("exclude app %s is not running", id)
	}
	if len(hit) == 0 {
		return empty
	}
	ptrs := make([]uintptr, len(hit))
	var names []string
	seen := map[string]bool{}
	for i, idx := range hit {
		ptrs[i] = uintptr(objs[idx])
		if id := ids[idx]; !seen[id] {
			seen[id] = true
			names = append(names, id)
		}
	}
	e.logf("excluding %s", strings.Join(names, ", "))
	return m.arrayCount(m.clsArray, m.sel("arrayWithObjects:count:"), unsafe.Pointer(&ptrs[0]), len(ptrs))
}

func (e *engine) stopStream() {
	stream := e.stream
	if stream == 0 {
		return
	}
	e.stream = 0
	e.dropping.Store(true)
	defer e.dropping.Store(false)
	parts := e.parts
	e.parts = streamParts{}
	done := make(chan struct{})
	var stopErr string
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) {
		// 回调带着 NSError 是没停成（SCStream.h）：流可能还在往队列里交样本
		if err != 0 {
			stopErr = e.mac.goString(e.mac.init(err, e.mac.sel("localizedDescription")))
		}
		close(done)
	})
	e.mac.stopCapture(stream, e.mac.sel("stopCaptureWithCompletionHandler:"), uintptr(block))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		// 回调和样本还可能来：block 和流用着的东西都不能放，留着
		e.logf("stop capture timed out")
		e.mac.release(stream, e.mac.sel("release"))
		return
	}
	block.Release()
	e.mac.release(stream, e.mac.sel("release"))
	if stopErr != "" {
		// 没确认停下：和超时一样，流用着的东西留着
		e.logf("stop capture: %s", stopErr)
		return
	}
	// 排空样本队列要等已经进了回调的样本编完；编码器排满、父进程不读管道时会一直等，不拿着 streamMu 等
	go e.mac.releaseParts(parts)
}

func streamDidOutput(_ objc.ID, _ objc.SEL, _ objc.ID, sample unsafe.Pointer, typ int) {
	if typ != 0 || sample == nil || active == nil {
		return
	}
	active.onSample(uintptr(sample))
}

func screenLocked(_ objc.ID, _ objc.SEL, _ objc.ID) {
	if active != nil {
		active.onLocked()
	}
}

func screenUnlocked(_ objc.ID, _ objc.SEL, _ objc.ID) {
	if active != nil {
		active.onUnlocked()
	}
}

// kCGDisplayBeginConfigurationFlag 置位时改动还没完成，等结束那次再重建。
func displayReconfigured(_ uint32, flags uint32, _ uintptr) {
	if flags&1 != 0 || active == nil {
		return
	}
	go active.onDisplaysChanged()
}

func streamDidStop(_ objc.ID, _ objc.SEL, _ objc.ID, nserr objc.ID) {
	if active == nil {
		return
	}
	if active.stopping.Load() || active.dropping.Load() {
		return
	}
	msg := "capture stopped"
	userStopped := false
	if nserr != 0 {
		msg += ": " + active.mac.goString(active.mac.init(nserr, active.mac.sel("localizedDescription")))
		userStopped = active.mac.integer(nserr, active.mac.sel("code")) == scStreamErrorUserStopped
	}
	// 只看的窗口关掉时，系统不等每秒的检查就先停掉采集（找不到要捕获的窗口）：回到整块屏，不结束
	if active.watching().ID != 0 {
		active.logf("%s", msg)
		go active.windowStreamStopped()
		return
	}
	// 用户在菜单栏停了录屏：正常退出，这次观看结束。别的原因带着错误退出，常驻进程重新拉起子进程
	if userStopped {
		active.logf("%s", msg)
		active.halt(nil)
		return
	}
	active.halt(fmt.Errorf("desk: %s", msg))
}

// scStreamErrorUserStopped 是 SCStreamErrorUserStopped：用户从系统的录屏指示里停了采集。
const scStreamErrorUserStopped = -3817

func logResponsible(w io.Writer) {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW)
	if err != nil {
		return
	}
	sym, err := purego.Dlsym(lib, "responsibility_get_pid_responsible_for_pid")
	if err != nil {
		fmt.Fprintf(w, "desk: pid %d\n", os.Getpid())
		return
	}
	// 这个私有函数直接返回负责进程的 pid，不写输出参数。
	var fn func(int32) int32
	purego.RegisterFunc(&fn, sym)
	fmt.Fprintf(w, "desk: pid %d responsible %d\n", os.Getpid(), fn(int32(os.Getpid())))
}

func recordingAppName() string {
	exe, err := os.Executable()
	if err == nil && strings.Contains(exe, "PosseDev.app") {
		return "Posse Dev"
	}
	return "Posse"
}

func codecName(c byte) string {
	if c == CodecH264 {
		return "h264"
	}
	return "hevc"
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}
