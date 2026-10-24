//go:build darwin

package desk

import (
	"fmt"
	"io"
	"os"
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

	streamMu  sync.Mutex
	displayID uint32
	locked    atomic.Bool
	dropping  atomic.Bool

	encMu       sync.Mutex
	enc         uintptr
	codec       byte
	lowLatency  bool
	width       int
	height      int
	nalLen      int
	force       atomic.Bool
	bitrate     atomic.Uint32
	bitrateSet  atomic.Bool
	forceDict   uintptr
	frameNumber uint64

	pendMu sync.Mutex
	pend   map[uint64]*stamp

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
}

type stamp struct {
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
	e := &engine{
		mac: mac, opt: o, out: o.Frames,
		pend: map[uint64]*stamp{}, stop: make(chan struct{}),
		nalLen: 4, born: time.Now(),
	}
	e.wantLow = true
	active = e
	logResponsible(o.Log)
	if o.SelfTest {
		return e.selfTest()
	}
	if !mac.preflight() {
		// 子进程只在用户要看桌面时才被拉起，这时弹系统授权框：它把 App 加进录屏列表，不弹就得用户自己去找
		mac.request()
		return fmt.Errorf("desk: screen recording is not allowed; turn on %s in System Settings > Privacy & Security > Screen & System Audio Recording", recordingAppName())
	}
	if err := registerDelegate(mac); err != nil {
		return err
	}
	e.force.Store(true)
	// 控制管道和采集都等跑圈起来再开始。提前 CFRunLoopStop 停不住还没开始的跑圈。
	boot := objc.NewBlock(func(objc.Block) {
		go e.control()
		go e.ticker()
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
	main := e.mac.mainDisplay()
	var display objc.ID
	var pw, ph int
	for i := range n {
		d := e.mac.at(displays, e.mac.sel("objectAtIndex:"), i)
		id := uint32(e.mac.integer(d, e.mac.sel("displayID")))
		if display == 0 || id == main {
			display = d
			pw, ph = e.mac.displayPixels(id)
		}
	}
	if display == 0 || pw < 2 || ph < 2 {
		e.halt(fmt.Errorf("desk: no main display"))
		return
	}
	e.displayID = main
	tw, th := Fit(pw, ph, e.opt.LongEdge)
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
	err := e.openStream(display)
	e.streamMu.Unlock()
	if err != nil {
		e.halt(err)
		return
	}
	e.logf("capturing %dx%d -> %dx%d %s low-latency=%v, started after %s", pw, ph, tw, th, codecName(e.codec), e.lowLatency, time.Since(e.born).Round(time.Millisecond))
}

func (e *engine) openStream(display objc.ID) error {
	m := e.mac
	empty := m.emptyArray(m.clsArray, m.sel("array"))
	filter := m.initFilter(m.alloc(m.clsFilter, m.sel("alloc")), m.sel("initWithDisplay:excludingWindows:"), display, empty)
	cfg := m.init(m.alloc(m.clsConfig, m.sel("alloc")), m.sel("init"))
	m.setInt(cfg, m.sel("setWidth:"), e.width)
	m.setInt(cfg, m.sel("setHeight:"), e.height)
	m.setInt(cfg, m.sel("setQueueDepth:"), 3)
	m.setU32(cfg, m.sel("setPixelFormat:"), pixel420v)
	m.setInt(cfg, m.sel("setShowsCursor:"), 1)
	m.setTime(cfg, m.sel("setMinimumFrameInterval:"), cmMake(1, 60))
	if m.keyMatrix709 != 0 {
		m.setPtr(cfg, m.sel("setColorMatrix:"), m.keyMatrix709)
	}
	del := m.init(m.alloc(delegateClass, m.sel("alloc")), m.sel("init"))
	stream := m.initStream(m.alloc(m.clsStream, m.sel("alloc")), m.sel("initWithFilter:configuration:delegate:"), filter, cfg, del)
	name := append([]byte("hive.desk.capture"), 0)
	queue := m.queueCreate(unsafe.Pointer(&name[0]), 0)
	var nserr objc.ID
	if !m.addOutput(stream, m.sel("addStreamOutput:type:sampleHandlerQueue:error:"), del, 0, queue, &nserr) {
		msg := "could not add a stream output"
		if nserr != 0 {
			msg = m.goString(m.init(nserr, m.sel("localizedDescription")))
		}
		return fmt.Errorf("desk: %s", msg)
	}
	e.stream = stream
	started := make(chan error, 1)
	block := objc.NewBlock(func(_ objc.Block, err objc.ID) {
		if err == 0 {
			started <- nil
			return
		}
		started <- fmt.Errorf("desk: %s", m.goString(m.init(err, m.sel("localizedDescription"))))
	})
	m.startCapture(stream, m.sel("startCaptureWithCompletionHandler:"), uintptr(block))
	return <-started
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
		fps := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), 60)
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

func (e *engine) initialBitrate() uint32 {
	return PictureBitrate(e.width, e.height)
}

func (e *engine) applyBitrate(session uintptr, bits uint32) {
	if bits < 1_500_000 {
		bits = 1_500_000
	}
	if bits > 20_000_000 {
		bits = 20_000_000
	}
	m := e.mac
	m.withPool(func() {
		num := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), int64(bits))
		m.vtSet(session, m.keyBitrate, uintptr(num))
		bytes := m.numLong(m.clsNumber, m.sel("numberWithLongLong:"), int64(bits)/80)
		secs := m.numDouble(m.clsNumber, m.sel("numberWithDouble:"), 0.1)
		ids := [2]uintptr{uintptr(bytes), uintptr(secs)}
		arr := m.arrayCount(m.clsArray, m.sel("arrayWithObjects:count:"), unsafe.Pointer(&ids[0]), 2)
		m.vtSet(session, m.keyLimits, uintptr(arr))
	})
}

func (e *engine) onSample(sample uintptr) {
	if e.locked.Load() || e.stopping.Load() || e.dropping.Load() {
		return
	}
	img := e.mac.sampleImage(sample)
	if img == 0 {
		return
	}
	e.mac.cfRetain(img)
	e.encMu.Lock()
	session := e.enc
	e.encMu.Unlock()
	if session == 0 {
		e.mac.cfRelease(img)
		return
	}
	if e.bitrateSet.CompareAndSwap(true, false) {
		e.applyBitrate(session, e.bitrate.Load())
	}
	st := &stamp{capture: time.Now().UnixNano(), start: time.Now(), number: atomic.AddUint64(&e.frameNumber, 1)}
	e.pendMu.Lock()
	e.pend[st.number] = st
	e.pendMu.Unlock()
	var props uintptr
	if e.force.CompareAndSwap(true, false) {
		props = e.forceDict
	}
	pts := cmMake(int64(st.number), 60)
	var flags uint32
	status := e.mac.vtEncode(session, img, pts, cmTime{}, props, uintptr(st.number), &flags)
	st.returned.Store(true)
	e.mac.cfRelease(img)
	if status != 0 {
		e.pendMu.Lock()
		delete(e.pend, st.number)
		e.pendMu.Unlock()
		e.logf("encode frame: %d", status)
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
	if status != 0 || sample == 0 || st == nil {
		return
	}
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
	e.write(Frame{
		Kind: KindConfig, CaptureUnixNano: time.Now().UnixNano(),
		Payload: MarshalConfig(Config{Codec: e.codec, Width: e.width, Height: e.height}),
	})
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
			e.logStats(false)
			e.write(Frame{Kind: KindHeartbeat, CaptureUnixNano: time.Now().UnixNano()})
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
	e.logf("probe %s lowlat=%v require-hw=%v poweroff=%v hardware=%v", name, low, hw, powerOff, e.hardware())
}

func (e *engine) phaseLabel() string {
	if e.phase != "" {
		return e.phase
	}
	return codecName(e.codec)
}

func (e *engine) hardware() bool {
	e.encMu.Lock()
	session := e.enc
	e.encMu.Unlock()
	if session == 0 || e.mac.vtCopy == nil || e.mac.keyUsingHW == 0 {
		return false
	}
	var value uintptr
	if e.mac.vtCopy(session, e.mac.keyUsingHW, 0, &value) != 0 || value == 0 {
		return false
	}
	defer e.mac.cfRelease(value)
	return value == e.mac.cfTrue
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
	e.logf("%s %s low-latency=%v hw=%v async=%d%% n=%d fps=%d p50=%s p95=%s", e.phaseLabel(), tag, e.wantLow, e.hardware(), async, len(samples), fps, samples[len(samples)*50/100], samples[min(len(samples)*95/100, len(samples)-1)])
}

func (e *engine) summary() {
	e.logStats(true)
	if e.mov == nil {
		return
	}
	asset := e.mac.urlAsset(objc.ID(objc.GetClass("AVURLAsset")), e.mac.sel("assetWithURL:"), e.mov.url)
	d := e.mac.assetDuration(asset, e.mac.sel("duration"))
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
		}
	}
}

func (e *engine) halt(err error) {
	if !e.stopping.CompareAndSwap(false, true) {
		return
	}
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
		cls, regErr := objc.RegisterClass("HiveDeskOutput", objc.GetClass("NSObject"), ps, nil, []objc.MethodDef{
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
		status := e.mac.vtEncode(e.enc, buf, cmMake(1, 60), cmTime{}, e.forceDict, uintptr(st.number), &flags)
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
	if e.mac.screenLocked() {
		e.report(StatusLocked)
		e.halt(fmt.Errorf("desk: locked"))
		return false
	}
	obs := e.mac.init(e.mac.alloc(delegateClass, e.mac.sel("alloc")), e.mac.sel("init"))
	center := e.mac.init(objc.ID(objc.GetClass("NSDistributedNotificationCenter")), e.mac.sel("defaultCenter"))
	add := e.mac.sel("addObserver:selector:name:object:")
	e.mac.addObserver(center, add, uintptr(obs), uintptr(e.mac.sel("screenLocked:")), uintptr(e.mac.string("com.apple.screenIsLocked")), 0)
	e.mac.addObserver(center, add, uintptr(obs), uintptr(e.mac.sel("screenUnlocked:")), uintptr(e.mac.string("com.apple.screenIsUnlocked")), 0)
	e.observer = obs
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
	go func() {
		e.streamMu.Lock()
		e.stopStream()
		e.streamMu.Unlock()
		e.report(StatusLocked)
		e.logf("screen locked")
	}()
}

func (e *engine) onUnlocked() {
	if e.stopping.Load() || !e.locked.CompareAndSwap(true, false) {
		return
	}
	e.report(StatusUnlocked)
	e.logf("screen unlocked")
}

func (e *engine) onDisplaysChanged() {
	if err := e.rebuildDisplay(); err != nil {
		e.halt(err)
	}
}

func (e *engine) rebuildDisplay() error {
	e.streamMu.Lock()
	defer e.streamMu.Unlock()
	if e.stopping.Load() || e.locked.Load() || e.stream == 0 {
		return nil
	}
	id := e.mac.mainDisplay()
	pw, ph := e.mac.displayPixels(id)
	tw, th := Fit(pw, ph, e.opt.LongEdge)
	if id == e.displayID && tw == e.width && th == e.height {
		return nil
	}
	e.logf("display changed %dx%d -> %dx%d", e.width, e.height, tw, th)
	display, err := e.fetchDisplay(id)
	if err != nil {
		return err
	}
	defer e.mac.release(display, e.mac.sel("release"))
	e.stopStream()
	if tw != e.width || th != e.height {
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
		if err := e.openEncoder(e.codec); err != nil {
			return err
		}
	}
	e.displayID = id
	e.writeConfig()
	e.force.Store(true)
	return e.openStream(display)
}

func (e *engine) fetchDisplay(id uint32) (objc.ID, error) {
	type result struct {
		display objc.ID
		err     error
	}
	ch := make(chan result, 1)
	block := objc.NewBlock(func(_ objc.Block, content, nserr objc.ID) {
		if content == 0 {
			msg := "no displays"
			if nserr != 0 {
				msg = e.mac.goString(e.mac.init(nserr, e.mac.sel("localizedDescription")))
			}
			ch <- result{err: fmt.Errorf("%s", msg)}
			return
		}
		displays := e.mac.init(content, e.mac.sel("displays"))
		n := e.mac.count(displays, e.mac.sel("count"))
		var found objc.ID
		for i := range n {
			d := e.mac.at(displays, e.mac.sel("objectAtIndex:"), i)
			if uint32(e.mac.integer(d, e.mac.sel("displayID"))) == id {
				found = e.mac.retain(d, e.mac.sel("retain"))
				break
			}
		}
		if found == 0 {
			ch <- result{err: fmt.Errorf("display %d is gone", id)}
			return
		}
		ch <- result{display: found}
	})
	e.mac.getContent(e.mac.clsShareable, e.mac.sel("getShareableContentWithCompletionHandler:"), uintptr(block))
	select {
	case r := <-ch:
		return r.display, r.err
	case <-time.After(3 * time.Second):
		return 0, fmt.Errorf("display list timed out")
	}
}

func (e *engine) stopStream() {
	stream := e.stream
	if stream == 0 {
		return
	}
	e.stream = 0
	e.dropping.Store(true)
	defer e.dropping.Store(false)
	done := make(chan struct{})
	block := objc.NewBlock(func(_ objc.Block, _ objc.ID) { close(done) })
	e.mac.stopCapture(stream, e.mac.sel("stopCaptureWithCompletionHandler:"), uintptr(block))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		e.logf("stop capture timed out")
	}
	e.mac.release(stream, e.mac.sel("release"))
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
	if nserr != 0 {
		active.logf("capture stopped: %s", active.mac.goString(active.mac.init(nserr, active.mac.sel("localizedDescription"))))
	}
	active.halt(nil)
}

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
	if err == nil && strings.Contains(exe, "HiveDev.app") {
		return "Hive Dev"
	}
	return "Hive"
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
