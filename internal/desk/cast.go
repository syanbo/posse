package desk

import (
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"slices"
	"sync"
	"time"
)

const (
	castListen = "127.0.0.1:47991"
	castPeer   = "127.0.0.1:47990"
	keyGap     = 250 * time.Millisecond
	paceWindow = 16 * time.Millisecond
	// 发送节奏的下限：码率压到最低时也不要慢于它，免得一个关键帧要发好几秒。只比码率下限高一点：
	// 高太多时链路只有码率下限那么宽的话，关键帧每次都冲爆它
	paceFloor = rateFloor * 6 / 5
	// 排着的画面按发送节奏最多积压这么久，再多就丢普通帧、等关键帧
	queueBudget = time.Second
	// 留着最近发过的这么多个视频包给手机点名补发，按 RTP 序号取模放；8 Mbps 时约 1 秒
	resendSlots = 1024
	// 一秒内补发的量不超过发送节奏的这一份：链路真堵了时补发只会更堵，补不上就等关键帧
	resendShare = 4
)

// reportQuiet 内没有接收报告就暂停发画面（心跳也停，什么都不往外发），reportGone 内报告回来就接着发，
// 还没回来才结束这次发送。切网络、进电梯常常断几秒，以前 3 秒就结束，手机上要手动重连（RustDesk 也是
// 延迟探测超过 2 秒没回只降帧率、不断开）。暂停期间画面不离开 Mac；手机被强退时不会发断开，
// 采集和防休眠最多多留 reportGone。从第一个 hello 起算：开播时手机往往还没发 hello，一开始就计会把订阅掐掉。
var (
	reportQuiet = 3 * time.Second
	reportGone  = 20 * time.Second
)

// helloWait 是还没有对端时最多等多久。
var helloWait = 60 * time.Second

// directHold 内直连地址还有包来，就不理会经中转来的 hello、report
const directHold = 3 * time.Second

// ErrNoReport 是已经有 hello 之后，超过 reportGone 没有收到 report。
var ErrNoReport = errors.New("desk: no report for 20s")

// ErrNoHello 是 helloWait 内没有收到 hello。
var ErrNoHello = errors.New("desk: no hello for 60s")

// Cast 从采集管道读帧，加密后发到 addr。对端先发 hello。control 写给采集子进程。
// 用固定测试密钥，给还没接进常驻进程的命令行试跑用。
func Cast(frames io.Reader, control io.Writer, addr string) error {
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	log.Printf("desk: listening %s", conn.LocalAddr())
	toMobile, toMac := TestKeys()
	return Serve(frames, control, CastConfig{Conn: conn, ToMobile: toMobile, ToMac: toMac, Verbose: true})
}

// LockWriter 让几处同时写控制管道时每条消息完整写完：改码率、要关键帧、转发输入在不同的协程里，
// 超过 PIPE_BUF 的输入会和别的消息交错，子进程读到乱的长度就退出。
func LockWriter(w io.Writer) io.Writer { return &lockedWriter{w: w} }

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// CastConfig 是一次观看的发送参数。
type CastConfig struct {
	// Conn 由调用方绑好，Serve 结束时关掉
	Conn            net.PacketConn
	ToMobile, ToMac [32]byte
	// OnStatus 收子进程报来的状态（StatusLocked 等），在读管道的协程里调
	OnStatus func(code byte)
	// OnDisplays 收子进程报来的屏列表（开始时、切屏后、拔插或改分辨率后），在读管道的协程里调
	OnDisplays func(Displays)
	// OnWindows 收子进程回 CtrlWindows 的窗口列表（Windows 的 JSON 原样），在读管道的协程里调
	OnWindows func([]byte)
	// OnInput 收手机发来的 input 包明文，返回已经执行到的必达事件序号，随即用 beat 回给手机
	OnInput func(plain []byte) (ack uint64)
	// Verbose 每次评估码率都记日志，命令行试跑看曲线用；常驻进程里只记变化，免得 daemon.log 每秒一行
	Verbose bool
	// IsRelay 认出中转服务器的地址。手机同时向直连地址和中转发包时，直连还通就不换到中转
	IsRelay func(net.Addr) bool
}

// Serve 一直发到子进程的管道结束、手机超时不报或 Conn 被关掉。
func Serve(frames io.Reader, control io.Writer, cfg CastConfig) error {
	sealAEAD, err := NewAEAD(cfg.ToMobile)
	if err != nil {
		cfg.Conn.Close()
		return err
	}
	openAEAD, err := NewAEAD(cfg.ToMac)
	if err != nil {
		cfg.Conn.Close()
		return err
	}
	now := time.Now()
	start := PictureBitrate(1920, 1080)
	c := &caster{
		conn: cfg.Conn, sealAEAD: sealAEAD, openAEAD: openAEAD, control: control,
		onStatus: cfg.OnStatus, onDisplays: cfg.OnDisplays, onWindows: cfg.OnWindows, onInput: cfg.OnInput, verbose: cfg.Verbose, isRelay: cfg.IsRelay,
		bitrate: start, fps: 60, fullBit: start, codec: CodecHEVC,
		stopped: make(chan struct{}), quietDeadline: now.Add(helloWait),
	}
	c.rate.reset(now, start, rateCeil)
	readDone := make(chan error, 1)
	go func() { readDone <- c.readFrames(frames) }()
	go c.recv()
	go c.beat()
	var errRun error
	select {
	case errRun = <-readDone:
	case <-c.stopped:
		errRun = c.stopErr
	}
	c.stop(errRun)
	c.mu.Lock()
	drops, queued, rate := c.dropped, c.maxQueued, c.bitrate
	c.mu.Unlock()
	c.histMu.Lock()
	resent, refused := c.resent, c.refused
	c.histMu.Unlock()
	log.Printf("desk: cast done queue-drops=%d max-queue=%d bitrate=%d resent=%d resend-refused=%d", drops, queued, rate, resent, refused)
	return errRun
}

// beatEvery 内画面没变时手机收不到视频，靠 beat 知道 Mac 还在。
// 负载 16 字节：发出时刻（Unix 纳秒，手机用来校时钟）、已执行到的必达输入序号（手机据此停止重发），都是大端。
const beatEvery = time.Second

func (c *caster) sendBeat() {
	c.mu.Lock()
	ack := c.inputAck
	c.mu.Unlock()
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint64(b[8:], ack)
	_ = c.send(PacketBeat, b[:])
}

func (c *caster) beat() {
	t := time.NewTicker(beatEvery)
	defer t.Stop()
	for {
		select {
		case <-c.stopped:
			return
		case <-t.C:
			if c.currentPeer() != nil && !c.paused() {
				c.sendBeat()
			}
		}
	}
}

type caster struct {
	conn               net.PacketConn
	sealAEAD, openAEAD cipher.AEAD
	control            io.Writer
	onStatus           func(byte)
	onDisplays         func(Displays)
	onWindows          func([]byte)
	onInput            func([]byte) uint64
	verbose            bool
	inputAck           uint64
	peer               net.Addr
	peerMu             sync.Mutex
	isRelay            func(net.Addr) bool
	directAt           time.Time // 最近一次从直连地址收到包，peerMu 保护
	replay             Replay
	sendN              uint64
	seq                uint16
	codec              byte
	busy               bool
	sendingKey         bool
	sendGen            uint64
	waitKey            bool
	queue              []Frame // 等着发的帧，按采集顺序；mu 保护
	queueBits          int
	keySend            time.Duration // 上一个关键帧发了多久，关键帧请求的间隔按它拉长
	mu                 sync.Mutex
	bitrate            uint32
	fps                uint32 // 告诉采集子进程的帧率上限，跟着码率走（fpsFor）；mu 保护
	fullBit            uint32 // 这个画面的满码率（PictureBitrate），帧率档位按它的比例算；mu 保护
	haveConfig         bool
	scale              uint32 // 告诉子进程的长边除数，最低一档帧率时为 2；fpsMu 保护
	fpsMu              sync.Mutex
	rate               rateState
	sendGap            time.Duration
	lastCap            time.Time
	lastKey            time.Time
	keyArmed           bool
	maxQueued          int
	dropped            int

	// 发过的视频包明文，给补发用；histMu 保护
	histMu     sync.Mutex
	hist       [resendSlots]sentPacket
	resendAt   time.Time // 这一秒补发额度的起点
	resendBits int
	resent     int
	refused    int

	// 每帧在 Mac 这边耽搁的时间（采集到最后一个包发出），mu 保护，见 ownDelay
	own []ownSample

	quietDeadline time.Time
	seenHello     bool
	// pausedAt 是 reportQuiet 没有报告、停发画面的时刻，没暂停时为零；mu 保护
	pausedAt time.Time
	stopped  chan struct{}
	stopOnce sync.Once
	stopErr  error
}

type ownSample struct {
	at time.Time
	d  time.Duration
}

// 手机报的排队延迟里扣掉的那份按最近这么久里发完的帧算：报告每 100ms 一份，再加上路上的时间
const ownRecent = 300 * time.Millisecond

// noteOwn 记下一帧在 Mac 这边耽搁了多久，留最近 10 秒（和手机算排队延迟的基线一样长）。调用方持有 mu。
func (c *caster) noteOwn(now time.Time, d time.Duration) {
	c.own = append(c.own, ownSample{now, d})
	drop := 0
	for drop < len(c.own) && now.Sub(c.own[drop].at) > 10*time.Second {
		drop++
	}
	c.own = c.own[drop:]
}

// ownDelay 是最近发完的帧在 Mac 这边比最顺的时候多耽搁的时间（中位数减 10 秒内的最小值）。
// 慢链路上一个关键帧按节奏要摊开几百毫秒，排在它后面的帧到手机时都晚了，手机量到的排队延迟里
// 有这一份是我们自己摊开的，不是网络在排队，不能拿来降码率。调用方持有 mu。
func (c *caster) ownDelay(now time.Time) time.Duration {
	if len(c.own) == 0 {
		return 0
	}
	floor := c.own[0].d
	var recent []time.Duration
	for _, s := range c.own {
		floor = min(floor, s.d)
		if now.Sub(s.at) <= ownRecent {
			recent = append(recent, s.d)
		}
	}
	if len(recent) == 0 {
		return 0
	}
	return medianGap(recent) - floor
}

type sentPacket struct {
	seq   uint16
	ok    bool
	plain []byte
}

func (c *caster) remember(seq uint16, plain []byte) {
	c.histMu.Lock()
	c.hist[int(seq)%resendSlots] = sentPacket{seq: seq, ok: true, plain: plain}
	c.histMu.Unlock()
}

// resend 补发手机点名要的视频包，用新的计数器重新加密（同一个计数器的 nonce 不能用两次）。
// 已经不在缓存里的、超出这一秒额度的不补，手机等不到就要关键帧。
func (c *caster) resend(plain []byte) {
	c.mu.Lock()
	budget := int(c.paceRate() / resendShare)
	c.mu.Unlock()
	now := time.Now()
	for i := 0; i+2 <= len(plain); i += 2 {
		seq := binary.BigEndian.Uint16(plain[i:])
		c.histMu.Lock()
		h := c.hist[int(seq)%resendSlots]
		if now.Sub(c.resendAt) >= time.Second {
			c.resendAt, c.resendBits = now, 0
		}
		ok := h.ok && h.seq == seq && c.resendBits+len(h.plain)*8 <= budget
		if ok {
			c.resendBits += len(h.plain) * 8
			c.resent++
		} else {
			c.refused++
		}
		c.histMu.Unlock()
		if ok {
			_ = c.send(PacketVideo, h.plain)
		}
	}
}

func (c *caster) stop(err error) {
	c.stopOnce.Do(func() {
		c.stopErr = err
		close(c.stopped)
		c.conn.Close()
	})
}

func (c *caster) readFrames(frames io.Reader) error {
	for {
		f, err := ReadFrame(frames)
		if err != nil {
			return err
		}
		switch f.Kind {
		case KindConfig:
			cfg, err := UnmarshalConfig(f.Payload)
			if err == nil {
				// 满尺寸没变是我们自己降、升了分辨率（CtrlScale）：码率照旧，编码器重开后按它自己的起始码率跑，
				// 再告诉它一遍。满尺寸变了是新画面（开始、切屏、改分辨率），从这个画面的满码率起步
				full := PictureBitrate(cfg.FullWidth, cfg.FullHeight)
				c.mu.Lock()
				c.codec = cfg.Codec
				rescaled := c.haveConfig && full == c.fullBit
				c.haveConfig = true
				if !rescaled {
					c.bitrate = full
					c.rate.reset(time.Now(), full, rateCeil)
					c.fullBit = full
				}
				bitrate := c.bitrate
				c.mu.Unlock()
				if rescaled {
					_ = WriteControl(c.control, Control{Kind: CtrlBitrate, Bitrate: bitrate})
				} else {
					c.applyFPS(bitrate)
				}
				name := "hevc"
				if cfg.Codec == CodecH264 {
					name = "h264"
				}
				log.Printf("desk: picture %dx%d %s bitrate %d", cfg.Width, cfg.Height, name, bitrate)
			}
		case KindStatus:
			if len(f.Payload) > 0 {
				log.Printf("desk: status %d", f.Payload[0])
				if c.onStatus != nil {
					c.onStatus(f.Payload[0])
				}
			}
		case KindDisplays:
			var d Displays
			if json.Unmarshal(f.Payload, &d) == nil && c.onDisplays != nil {
				c.onDisplays(d)
			}
		case KindWindows:
			if c.onWindows != nil {
				c.onWindows(f.Payload)
			}
		case KindVideo:
			c.submit(f)
		}
	}
}

func (c *caster) paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.pausedAt.IsZero()
}

// pause 在 reportQuiet 没有报告时停发画面，返回是不是刚停的。
func (c *caster) pause() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.pausedAt.IsZero() {
		return false
	}
	c.pausedAt = time.Now()
	return true
}

// resume 在暂停后又收到手机的包时接着发：路刚恢复，码率从一半起步，先丢掉普通帧、要一个关键帧，
// 手机解码器在断流后等的就是它。
func (c *caster) resume() {
	c.mu.Lock()
	if c.pausedAt.IsZero() {
		c.mu.Unlock()
		return
	}
	away := time.Since(c.pausedAt)
	c.pausedAt = time.Time{}
	before := c.bitrate
	bitrate := max(c.bitrate/2, rateFloor)
	c.bitrate = bitrate
	c.rate.reset(time.Now(), bitrate, c.rate.maxBit)
	// 断之前的码率是快速回去的目标
	c.rate.peak = before
	c.waitKey = true
	c.mu.Unlock()
	log.Printf("desk: reports back after %s, resuming at %d", away.Round(100*time.Millisecond), bitrate)
	_ = WriteControl(c.control, Control{Kind: CtrlBitrate, Bitrate: bitrate})
	c.applyFPS(bitrate)
	c.askKey()
}

func (c *caster) submit(f Frame) {
	if c.currentPeer() == nil || c.paused() {
		return
	}
	c.observeCapture()
	c.mu.Lock()
	if f.Keyframe {
		c.waitKey = false
		// 排着的普通帧都在它之前，过时了。正在发的普通帧也打断；正在发的关键帧不打断，
		// 否则慢链路上一个关键帧要发几百毫秒，下一个到来时它永远发不完
		c.dropQueued(false)
		if c.busy && !c.sendingKey {
			c.sendGen++
		}
	}
	if c.waitKey {
		c.dropped++
		c.mu.Unlock()
		return
	}
	c.queue = append(c.queue, f)
	c.queueBits += len(f.Payload) * 8
	c.maxQueued = max(c.maxQueued, len(c.queue))
	// 积压的比按现在的节奏 queueBudget 发得完的还多：链路扛不住。普通帧引用前一帧，丢一帧后面全花，
	// 只能丢掉排着的普通帧、等下一个关键帧
	if !f.Keyframe && c.queueBits > int(c.paceRate()*uint64(queueBudget)/uint64(time.Second)) {
		c.dropQueued(true)
		c.waitKey = true
		c.mu.Unlock()
		c.askKey()
		c.mu.Lock()
	}
	next, ok := c.nextLocked()
	c.mu.Unlock()
	if ok {
		c.startSend(next)
	}
}

// dropQueued 丢掉排着的帧，keepKey 时留下排着的关键帧。调用方持有 mu。
func (c *caster) dropQueued(keepKey bool) {
	kept := c.queue[:0]
	bits := 0
	for _, f := range c.queue {
		if keepKey && f.Keyframe {
			kept = append(kept, f)
			bits += len(f.Payload) * 8
			continue
		}
		c.dropped++
	}
	c.queue, c.queueBits = kept, bits
}

// nextLocked 在没有正在发的帧时取出排头，标成正在发。调用方持有 mu。
func (c *caster) nextLocked() (Frame, bool) {
	if c.busy || len(c.queue) == 0 {
		return Frame{}, false
	}
	f := c.queue[0]
	c.queue = c.queue[1:]
	c.queueBits -= len(f.Payload) * 8
	c.busy = true
	c.sendingKey = f.Keyframe
	return f, true
}

// paceRate 是发画面的节奏（比特每秒）：码率的 1.5 倍。一帧一口气发出去会冲爆慢链路的缓冲，
// 关键帧比 100ms 的码率预算大好几倍（硬件编码器不守），尤其如此。调用方持有 mu。
func (c *caster) paceRate() uint64 {
	return max(uint64(c.bitrate)*3/2, paceFloor)
}

func (c *caster) startSend(f Frame) {
	c.mu.Lock()
	gen := c.sendGen
	seq := c.seq
	codec := c.codec
	rate := c.paceRate()
	c.mu.Unlock()
	pkts := Packetize(f, seq, codec)
	if f.Keyframe {
		log.Printf("desk: send keyframe %d (%d packets)", f.Number, len(pkts))
	}
	go c.sendFrame(pkts, f.Keyframe, gen, seq, rate, time.Unix(0, f.CaptureUnixNano))
}

func (c *caster) sendFrame(pkts [][]byte, key bool, gen uint64, seq0 uint16, rate uint64, captured time.Time) {
	// 按节奏摊开：第 i 个包在 start + span*(i+1)/n 之前不发。按包 Sleep 时，间隔短于调度粒度会被拉长到约 1ms，
	// 所以按累计时刻算，落后了就连着发
	bits := 0
	for _, p := range pkts {
		bits += len(p) * 8
	}
	span := time.Duration(uint64(bits) * uint64(time.Second) / rate)
	if key {
		span = max(span, paceWindow)
	}
	sent := 0
	start := time.Now()
	for i, p := range pkts {
		c.mu.Lock()
		stop := c.sendGen != gen
		c.mu.Unlock()
		if stop {
			break
		}
		if err := c.send(PacketVideo, p); err != nil {
			log.Printf("desk: send: %v", err)
			break
		}
		c.remember(seq0+uint16(i), p)
		sent++
		if i+1 < len(pkts) && span > 2*time.Millisecond {
			at := start.Add(span * time.Duration(i+1) / time.Duration(len(pkts)))
			if d := time.Until(at); d > time.Millisecond {
				time.Sleep(d)
			}
		}
	}
	c.mu.Lock()
	// 没发出去的包不占序号。空号会被接收端记成丢包，码率跟着自己丢掉的帧一路降到下限。
	c.seq = seq0 + uint16(sent)
	if key {
		c.keySend = time.Since(start)
	}
	if sent == len(pkts) && captured.Unix() > 0 {
		now := time.Now()
		c.noteOwn(now, now.Sub(captured))
	}
	c.busy = false
	c.sendingKey = false
	next, ok := c.nextLocked()
	c.mu.Unlock()
	if ok {
		c.startSend(next)
	}
}

func (c *caster) observeCapture() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastCap.IsZero() {
		d := now.Sub(c.lastCap)
		if d >= 8*time.Millisecond && d < 100*time.Millisecond {
			if c.sendGap == 0 {
				c.sendGap = d
			} else {
				c.sendGap = (c.sendGap*7 + d) / 8
			}
		}
	}
	c.lastCap = now
}

func (c *caster) send(kind byte, plain []byte) error {
	c.mu.Lock()
	c.sendN++
	n := c.sendN
	c.mu.Unlock()
	peer := c.currentPeer()
	if peer == nil {
		return nil
	}
	pkt, err := Seal(c.sealAEAD, DirToMobile, kind, n, plain)
	if err != nil {
		return err
	}
	_, err = c.conn.WriteTo(pkt, peer)
	return err
}

func (c *caster) recv() {
	buf := make([]byte, PacketLimit)
	for {
		wait := time.Until(c.quietDeadline)
		if wait <= 0 {
			if c.seenHello && c.pause() {
				log.Printf("desk: no report for %.0fs, pausing the picture", reportQuiet.Seconds())
				c.quietDeadline = time.Now().Add(reportGone - reportQuiet)
				continue
			}
			if c.seenHello {
				log.Printf("desk: no report for %.0fs", reportGone.Seconds())
				c.stop(ErrNoReport)
			} else {
				log.Printf("desk: no hello for %.0fs", helloWait.Seconds())
				c.stop(ErrNoHello)
			}
			return
		}
		if wait > 200*time.Millisecond {
			wait = 200 * time.Millisecond
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(wait))
		n, addr, err := c.conn.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		kind, counter, plain, err := Open(c.openAEAD, DirToMac, buf[:n])
		if err != nil || !c.replay.Accept(counter) {
			continue
		}
		switch kind {
		case PacketHello:
			c.notePeer(addr)
			if !c.seenHello {
				c.seenHello = true
				c.quietDeadline = time.Now().Add(reportQuiet)
			}
			log.Printf("desk: hello from %s", addr)
		case PacketReport:
			if c.seenHello {
				c.quietDeadline = time.Now().Add(reportQuiet)
			}
			c.notePeer(addr)
			c.resume()
			c.onReport(plain)
		case PacketKeyframe:
			c.askKey()
		case PacketNack:
			c.resend(plain)
		case PacketInput:
			if c.onInput != nil {
				ack := c.onInput(plain)
				c.mu.Lock()
				c.inputAck = ack
				c.mu.Unlock()
				c.sendBeat()
			}
		}
	}
}

func (c *caster) currentPeer() net.Addr {
	c.peerMu.Lock()
	defer c.peerMu.Unlock()
	return c.peer
}

// notePeer 在 hello 或 report 解得开时记下源地址。地址变了（切网络）就改发往新地址。
func (c *caster) notePeer(addr net.Addr) {
	if addr == nil {
		return
	}
	c.peerMu.Lock()
	defer c.peerMu.Unlock()
	relay := c.isRelay != nil && c.isRelay(addr)
	if !relay {
		c.directAt = time.Now()
	}
	if c.peer != nil && sameAddr(c.peer, addr) {
		return
	}
	if relay && c.peer != nil && !c.isRelay(c.peer) && time.Since(c.directAt) < directHold {
		return
	}
	next := cloneAddr(addr)
	if c.peer != nil {
		log.Printf("desk: peer %s -> %s", c.peer, next)
	}
	c.peer = next
}

// ReadFrom 交回来的地址只保证到下一次读，对端要留着用，得自己拷一份。
func cloneAddr(a net.Addr) net.Addr {
	u, ok := a.(*net.UDPAddr)
	if !ok {
		return a
	}
	c := *u
	if u.IP != nil {
		c.IP = append(net.IP(nil), u.IP...)
	}
	return &c
}

func sameAddr(a, b net.Addr) bool {
	return a.String() == b.String()
}

func (c *caster) onReport(plain []byte) {
	if len(plain) < 8+4+4+8+8+4 {
		return
	}
	loss := binary.BigEndian.Uint32(plain[8:12])
	packets := binary.BigEndian.Uint32(plain[12:16])
	gap := time.Duration(binary.BigEndian.Uint32(plain[32:36])) * time.Microsecond
	c.mu.Lock()
	if binary.BigEndian.Uint64(plain[24:32]) == 1 {
		q := time.Duration(binary.BigEndian.Uint64(plain[16:24])) * time.Microsecond
		c.rate.addQueue(max(q-c.ownDelay(time.Now()), 0))
	}
	// 帧率上限降下来以后帧间隔本来就长，和采集节奏的估计比要按上限算，不然把降帧当成排队
	base := max(c.sendGap, time.Second/time.Duration(max(c.fps, 1)))
	d := c.rate.add(time.Now(), loss, packets, gap, base)
	if d.changed {
		c.bitrate = d.bitrate
	}
	c.mu.Unlock()
	if !d.evaluated {
		return
	}
	action := "hold"
	if d.changed && d.bitrate < d.from {
		action = "down"
	} else if d.changed && d.bitrate > d.from {
		action = "up"
	}
	if !d.changed && !c.verbose {
		return
	}
	log.Printf("desk: rate %s loss=%.3f packets=%d late=%v gap=%s queue=%s bitrate=%d congested=%v", action, d.loss, d.packets, d.late, d.gap, d.queue, d.bitrate, d.congested)
	if !d.changed {
		return
	}
	_ = WriteControl(c.control, Control{Kind: CtrlBitrate, Bitrate: d.bitrate})
	c.applyFPS(d.bitrate)
}

// applyFPS 按码率定采集的帧率上限，变了才告诉子进程。
// 读管道、收报告两个协程都会调：fpsMu 盖住决定和写控制，子进程收到的顺序和决定的顺序一致。
func (c *caster) applyFPS(bitrate uint32) {
	c.fpsMu.Lock()
	defer c.fpsMu.Unlock()
	c.mu.Lock()
	next := fpsFor(bitrate, c.fullBit, c.fps)
	changed := next != c.fps
	c.fps = next
	c.mu.Unlock()
	if changed {
		log.Printf("desk: frame rate cap %d at bitrate %d", next, bitrate)
		_ = WriteControl(c.control, Control{Kind: CtrlFPS, FPS: next})
	}
	// 降到 15 帧及以下时画面长边减半：关键帧的大小大致跟像素数走，小到三分之一上下，慢链路上才发得完整。
	// 在手机上看，整屏缩在手机宽度里，减半看着差别不大。硬件编码器出关键帧不看码率，满尺寸的关键帧
	// 实测有 200 KB，所以要回到 30 帧（码率约 1.9 Mbps 以上）才升回满尺寸，那时半秒左右能发完
	scale := uint32(1)
	if next <= 15 {
		scale = 2
	}
	if c.scale == 0 {
		c.scale = 1
	}
	if scale != c.scale {
		c.scale = scale
		log.Printf("desk: picture scale 1/%d at bitrate %d", scale, bitrate)
		_ = WriteControl(c.control, Control{Kind: CtrlScale, Scale: scale})
	}
}

// askKey 让子进程出一个关键帧。两次请求至少隔 keyGap，慢链路上还要隔上一个关键帧发送时长的两倍；
// 关键帧正在发或排着时先不要：手机丢包后每 200ms 要一次，不拦着的话关键帧一个接一个，把链路堵死。
func (c *caster) askKey() {
	c.mu.Lock()
	gap := max(keyGap, 2*c.keySend)
	pending := c.sendingKey || slices.ContainsFunc(c.queue, func(f Frame) bool { return f.Keyframe })
	if pending && !c.keyArmed {
		c.keyArmed = true
		time.AfterFunc(gap, c.fireKey)
	}
	if pending {
		c.mu.Unlock()
		return
	}
	if !c.lastKey.IsZero() {
		if wait := gap - time.Since(c.lastKey); wait > 0 {
			// 间隔没到就先记着，到点再要。丢掉这次请求要等手机 200ms 后重试，补帧会晚一截。
			if !c.keyArmed {
				c.keyArmed = true
				time.AfterFunc(wait, c.fireKey)
			}
			c.mu.Unlock()
			return
		}
	}
	c.keyArmed = false
	c.lastKey = time.Now()
	c.mu.Unlock()
	_ = WriteControl(c.control, Control{Kind: CtrlKeyframe})
	log.Printf("desk: keyframe requested")
}

func (c *caster) fireKey() {
	c.mu.Lock()
	c.keyArmed = false
	c.mu.Unlock()
	c.askKey()
}
