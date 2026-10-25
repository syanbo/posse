package desk

import (
	"crypto/cipher"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

const (
	castListen = "127.0.0.1:47991"
	castPeer   = "127.0.0.1:47990"
	keyGap     = 250 * time.Millisecond
	paceWindow = 16 * time.Millisecond
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
		onStatus: cfg.OnStatus, onDisplays: cfg.OnDisplays, onInput: cfg.OnInput, verbose: cfg.Verbose, isRelay: cfg.IsRelay,
		bitrate: start, codec: CodecHEVC,
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
	log.Printf("desk: cast done queue-drops=%d max-queue=%d bitrate=%d", drops, queued, rate)
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
	held               *Frame
	queued             int
	mu                 sync.Mutex
	bitrate            uint32
	rate               rateState
	sendGap            time.Duration
	lastCap            time.Time
	lastKey            time.Time
	keyArmed           bool
	maxQueued          int
	dropped            int

	quietDeadline time.Time
	seenHello     bool
	// pausedAt 是 reportQuiet 没有报告、停发画面的时刻，没暂停时为零；mu 保护
	pausedAt time.Time
	stopped  chan struct{}
	stopOnce sync.Once
	stopErr  error
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
				start := PictureBitrate(cfg.Width, cfg.Height)
				c.mu.Lock()
				c.codec = cfg.Codec
				c.bitrate = start
				c.rate.reset(time.Now(), start, rateCeil)
				c.mu.Unlock()
				name := "hevc"
				if cfg.Codec == CodecH264 {
					name = "h264"
				}
				log.Printf("desk: picture %dx%d %s bitrate %d", cfg.Width, cfg.Height, name, start)
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
	bitrate := max(c.bitrate/2, rateFloor)
	c.bitrate = bitrate
	c.rate.reset(time.Now(), bitrate, c.rate.maxBit)
	c.waitKey = true
	c.mu.Unlock()
	log.Printf("desk: reports back after %s, resuming at %d", away.Round(100*time.Millisecond), bitrate)
	_ = WriteControl(c.control, Control{Kind: CtrlBitrate, Bitrate: bitrate})
	c.askKey()
}

func (c *caster) submit(f Frame) {
	if c.currentPeer() == nil || c.paused() {
		return
	}
	c.observeCapture()
	c.mu.Lock()
	codec := c.codec
	if f.Keyframe {
		c.waitKey = false
		// 正在发的普通帧已经过时。正在发的关键帧不打断，否则 16ms 匀速发出期间
		// 下一帧采集到来，关键帧永远发不完。
		if c.busy && !c.sendingKey {
			c.sendGen++
		}
		c.held = nil
	}
	if c.waitKey {
		c.dropped++
		c.mu.Unlock()
		return
	}
	if c.busy {
		if c.held != nil {
			if f.Keyframe {
				cp := f
				c.held = &cp
				c.queued = 1
				if !c.sendingKey {
					c.sendGen++
				}
				c.mu.Unlock()
				return
			}
			if c.held.Keyframe {
				// 排队的关键帧还没发。再丢掉它就会在参考链上留一个洞。
				c.dropped++
				c.mu.Unlock()
				return
			}
			// 普通帧被丢掉之后，后面的普通帧仍引用它。停掉没发完的普通帧，等下一张关键帧。
			c.held = nil
			c.queued = 0
			c.waitKey = true
			c.dropped++
			if !c.sendingKey {
				c.sendGen++
			}
			c.mu.Unlock()
			c.askKey()
			return
		}
		cp := f
		c.held = &cp
		c.queued = 1
		if c.queued > c.maxQueued {
			c.maxQueued = c.queued
		}
		c.mu.Unlock()
		return
	}
	c.busy = true
	c.sendingKey = f.Keyframe
	c.mu.Unlock()
	c.startSend(f, codec)
}

func (c *caster) startSend(f Frame, codec byte) {
	c.mu.Lock()
	c.busy = true
	c.sendingKey = f.Keyframe
	gen := c.sendGen
	seq := c.seq
	c.mu.Unlock()
	pkts := Packetize(f, seq, codec)
	if f.Keyframe {
		log.Printf("desk: send keyframe %d (%d packets)", f.Number, len(pkts))
	}
	go c.sendFrame(pkts, f.Keyframe, codec, gen, seq)
}

func (c *caster) sendFrame(pkts [][]byte, key bool, codec byte, gen uint64, seq0 uint16) {
	// 按包 Sleep 时，间隔短于调度粒度会被拉长到约 1ms，几百个包的关键帧要发半秒以上。
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
		sent++
		if key && i+1 < len(pkts) {
			span := int64(paceWindow) * int64(i+1) / int64(len(pkts))
			if d := time.Until(start.Add(time.Duration(span))); d > time.Millisecond {
				time.Sleep(d)
			}
		}
	}
	c.mu.Lock()
	// 没发出去的包不占序号。空号会被接收端记成丢包，码率跟着自己丢掉的帧一路降到下限。
	c.seq = seq0 + uint16(sent)
	var next *Frame
	codec = c.codec
	if c.held != nil && !c.waitKey {
		next = c.held
		c.held = nil
		c.queued = 0
	} else {
		c.held = nil
		c.queued = 0
		c.busy = false
	}
	c.mu.Unlock()
	if next != nil {
		c.startSend(*next, codec)
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
		c.rate.addQueue(time.Duration(binary.BigEndian.Uint64(plain[16:24])) * time.Microsecond)
	}
	base := c.sendGap
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
}

func (c *caster) askKey() {
	c.mu.Lock()
	if !c.lastKey.IsZero() {
		if wait := keyGap - time.Since(c.lastKey); wait > 0 {
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
