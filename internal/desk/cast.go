package desk

import (
	"crypto/cipher"
	"encoding/binary"
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

// reportQuiet 内没有接收报告就结束这次发送。手机退到后台时 WebSocket 往往还挂着。
// 从第一个 hello 起算：开播时手机往往还没发 hello，一开始就计 3 秒会把订阅掐掉。
var reportQuiet = 3 * time.Second

// helloWait 是还没有对端时最多等多久。
var helloWait = 60 * time.Second

// ErrNoReport 是已经有 hello 之后，超过 reportQuiet 没有收到 report。
var ErrNoReport = errors.New("desk: no report for 3s")

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
	return Serve(frames, control, CastConfig{Conn: conn, ToMobile: toMobile, ToMac: toMac})
}

// CastConfig 是一次观看的发送参数。
type CastConfig struct {
	// Conn 由调用方绑好，Serve 结束时关掉
	Conn            net.PacketConn
	ToMobile, ToMac [32]byte
	// OnStatus 收子进程报来的状态（StatusLocked 等），在读管道的协程里调
	OnStatus func(code byte)
	// OnInput 收手机发来的 input 包明文，返回已经执行到的必达事件序号，随即用 beat 回给手机
	OnInput func(plain []byte) (ack uint64)
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
		onStatus: cfg.OnStatus, onInput: cfg.OnInput,
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
			if c.currentPeer() != nil {
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
	onInput            func([]byte) uint64
	inputAck           uint64
	peer               net.Addr
	peerMu             sync.Mutex
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
	stopped       chan struct{}
	stopOnce      sync.Once
	stopErr       error
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
		case KindVideo:
			c.submit(f)
		}
	}
}

func (c *caster) submit(f Frame) {
	if c.currentPeer() == nil {
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
			if c.seenHello {
				log.Printf("desk: no report for %.0fs", reportQuiet.Seconds())
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
	if c.peer != nil && sameAddr(c.peer, addr) {
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
	log.Printf("desk: rate %s loss=%.3f packets=%d late=%v gap=%s bitrate=%d congested=%v", action, d.loss, d.packets, d.late, d.gap, d.bitrate, d.congested)
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
