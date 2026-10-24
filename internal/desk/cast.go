package desk

import (
	"encoding/binary"
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

// Cast 从采集管道读帧，加密后发到 addr。对端先发 hello。control 写给采集子进程。
func Cast(frames io.Reader, control io.Writer, addr string) error {
	toMobile, toMac := TestKeys()
	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	c := &caster{
		conn: conn, toMobile: toMobile, toMac: toMac, control: control,
		bitrate: PictureBitrate(1920, 1080), maxBit: 20_000_000,
		stable: time.Now(), heard: time.Now(),
	}
	log.Printf("desk: listening %s", conn.LocalAddr())
	go c.recv()
	return c.play(frames)
}

type caster struct {
	conn            net.PacketConn
	toMobile, toMac [32]byte
	control         io.Writer
	peer            net.Addr
	peerMu          sync.Mutex
	replay          Replay
	sendN           uint64
	seq             uint16
	busy            bool
	waitKey         bool
	held            *Frame
	queued          int
	mu              sync.Mutex
	bitrate, maxBit uint32
	sendGap         time.Duration
	lastCap         time.Time
	lateN           int
	stable          time.Time
	heard           time.Time
	lastKey         time.Time
	maxQueued       int
	dropped         int
}

func (c *caster) play(frames io.Reader) error {
	err := c.readFrames(frames)
	log.Printf("desk: cast done queue-drops=%d max-queue=%d bitrate=%d", c.dropped, c.maxQueued, c.bitrate)
	return err
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
				c.bitrate = PictureBitrate(cfg.Width, cfg.Height)
				c.maxBit = c.bitrate
				log.Printf("desk: picture %dx%d bitrate %d", cfg.Width, cfg.Height, c.bitrate)
			}
		case KindStatus:
			if len(f.Payload) > 0 {
				log.Printf("desk: status %d", f.Payload[0])
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
	if f.Keyframe {
		c.waitKey = false
		c.held = nil
	}
	if c.waitKey {
		c.dropped++
		c.mu.Unlock()
		return
	}
	if c.busy {
		if c.held != nil {
			// 队列里已经有一帧，再来的丢掉。丢掉的帧不占 RTP 序号：
			// 下一帧必须是关键帧，接收端看到的序号仍然连续，不会把残帧解出来。
			c.held = nil
			c.queued = 0
			c.waitKey = true
			c.dropped++
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
	c.mu.Unlock()
	c.startSend(f)
}

func (c *caster) startSend(f Frame) {
	pkts := Packetize(f, c.seq)
	c.seq += uint16(len(pkts))
	if f.Keyframe {
		log.Printf("desk: send keyframe %d (%d packets)", f.Number, len(pkts))
	}
	go c.sendFrame(pkts, f.Keyframe)
}

func (c *caster) sendFrame(pkts [][]byte, key bool) {
	gap := time.Duration(0)
	if key && len(pkts) > 1 {
		gap = paceWindow / time.Duration(len(pkts))
	}
	for i, p := range pkts {
		if err := c.send(PacketVideo, p); err != nil {
			log.Printf("desk: send: %v", err)
			break
		}
		if gap > 0 && i+1 < len(pkts) {
			time.Sleep(gap)
		}
	}
	c.mu.Lock()
	var next *Frame
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
		c.startSend(*next)
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
	pkt, err := Seal(c.toMobile, DirToMobile, kind, n, plain)
	if err != nil {
		return err
	}
	_, err = c.conn.WriteTo(pkt, peer)
	return err
}

func (c *caster) recv() {
	buf := make([]byte, PacketLimit)
	quiet := time.NewTimer(3 * time.Second)
	defer quiet.Stop()
	for {
		c.conn.SetReadDeadline(time.Now().Add(time.Second))
		n, addr, err := c.conn.ReadFrom(buf)
		if err != nil {
			continue
		}
		kind, counter, plain, err := Open(c.toMac, DirToMac, buf[:n])
		if err != nil || !c.replay.Accept(counter) {
			continue
		}
		switch kind {
		case PacketHello:
			c.peerMu.Lock()
			c.peer = addr
			c.peerMu.Unlock()
			c.touchReport()
			log.Printf("desk: hello from %s", addr)
		case PacketReport:
			c.touchReport()
			c.onReport(plain)
		case PacketKeyframe:
			c.touchReport()
			c.askKey()
		}
		_ = quiet
	}
}

func (c *caster) currentPeer() net.Addr {
	c.peerMu.Lock()
	defer c.peerMu.Unlock()
	return c.peer
}

func (c *caster) onReport(plain []byte) {
	if len(plain) < 8+4+4+8+8+4 {
		return
	}
	loss := binary.BigEndian.Uint32(plain[8:12])
	packets := binary.BigEndian.Uint32(plain[12:16])
	gap := time.Duration(binary.BigEndian.Uint32(plain[32:36])) * time.Microsecond
	frac := 0.0
	if packets > 0 {
		frac = float64(loss) / float64(packets)
	}
	next := c.bitrate
	// 和发送端自己的采集间隔比。接收间隔连续两窗都长出 15ms，才当成在排队。
	c.mu.Lock()
	base := c.sendGap
	c.mu.Unlock()
	if base == 0 {
		base = 17 * time.Millisecond
	}
	late := false
	if gap >= 8*time.Millisecond && gap > base+15*time.Millisecond {
		c.lateN++
		late = c.lateN >= 2
	} else if gap > 0 {
		c.lateN = 0
	}
	if frac > 0.02 || late {
		next = c.bitrate * 3 / 4
		c.stable = time.Now()
	} else if time.Since(c.stable) >= 2*time.Second && c.bitrate < c.maxBit {
		next = c.bitrate + c.bitrate/20
		if next > c.maxBit {
			next = c.maxBit
		}
		c.stable = time.Now()
	}
	if next < 1_500_000 {
		next = 1_500_000
	}
	if next == c.bitrate {
		return
	}
	log.Printf("desk: bitrate %d -> %d loss=%.3f gap=%s queue-drops=%d", c.bitrate, next, frac, gap, c.dropped)
	c.bitrate = next
	_ = WriteControl(c.control, Control{Kind: CtrlBitrate, Bitrate: next})
}

func (c *caster) askKey() {
	c.mu.Lock()
	if time.Since(c.lastKey) < keyGap {
		c.mu.Unlock()
		return
	}
	c.lastKey = time.Now()
	c.mu.Unlock()
	_ = WriteControl(c.control, Control{Kind: CtrlKeyframe})
	log.Printf("desk: keyframe requested")
}

func (c *caster) touchReport() {
	c.mu.Lock()
	c.heard = time.Now()
	c.mu.Unlock()
}
