package desk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"slices"
	"testing"
	"time"
)

func TestCastStopsWithoutHello(t *testing.T) {
	oldQuiet, oldGone, oldHello := reportQuiet, reportGone, helloWait
	reportQuiet, reportGone = 200*time.Millisecond, 300*time.Millisecond
	helloWait = 400 * time.Millisecond
	t.Cleanup(func() {
		reportQuiet, reportGone, helloWait = oldQuiet, oldGone, oldHello
	})

	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	addr := freeUDP(t)
	start := time.Now()
	err := Cast(pr, io.Discard, addr)
	if !errors.Is(err, ErrNoHello) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("stopped after %s", elapsed)
	}
}

func TestCastQuietStartsAfterHello(t *testing.T) {
	oldQuiet, oldGone, oldHello := reportQuiet, reportGone, helloWait
	reportQuiet, reportGone = 300*time.Millisecond, 600*time.Millisecond
	helloWait = 5 * time.Second
	t.Cleanup(func() {
		reportQuiet, reportGone, helloWait = oldQuiet, oldGone, oldHello
	})

	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	addr := freeUDP(t)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Cast(pr, io.Discard, addr) }()

	select {
	case err := <-done:
		t.Fatalf("stopped before hello: %v after %s", err, time.Since(start))
	case <-time.After(800 * time.Millisecond):
	}

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_, toMac := TestKeys()
	aead, err := NewAEAD(toMac)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := Seal(aead, DirToMac, PacketHello, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}
	helloAt := time.Now()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNoReport) {
			t.Fatalf("err = %v", err)
		}
		// 先暂停，到 reportGone 才结束
		sinceHello := time.Since(helloAt)
		if sinceHello < 500*time.Millisecond || sinceHello > 2*time.Second {
			t.Fatalf("report timer %s after hello, total %s", sinceHello, time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not stop after hello without reports")
	}
}

func TestCastReportResetsQuiet(t *testing.T) {
	oldQuiet, oldGone, oldHello := reportQuiet, reportGone, helloWait
	reportQuiet, reportGone = 300*time.Millisecond, 500*time.Millisecond
	helloWait = 3 * time.Second
	t.Cleanup(func() {
		reportQuiet, reportGone, helloWait = oldQuiet, oldGone, oldHello
	})

	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	addr := freeUDP(t)
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- Cast(pr, io.Discard, addr) }()
	time.Sleep(30 * time.Millisecond)

	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_, toMac := TestKeys()
	aead, err := NewAEAD(toMac)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := Seal(aead, DirToMac, PacketHello, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(hello); err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 36)
	binary.BigEndian.PutUint32(body[12:16], 100)
	var n uint64 = 1
	until := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(until) {
		n++
		pkt, err := Seal(aead, DirToMac, PacketReport, n, body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(pkt); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("stopped while reports were flowing: %v after %s", err, time.Since(start))
	default:
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrNoReport) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) < 1500*time.Millisecond {
			t.Fatalf("quiet timer did not reset, stopped after %s", time.Since(start))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop after reports ended")
	}
}

// 断流超过 reportQuiet 先暂停：不结束，报告回来就接着发，请一个关键帧、码率减半
func TestCastPausesAndResumes(t *testing.T) {
	oldQuiet, oldGone, oldHello := reportQuiet, reportGone, helloWait
	reportQuiet, reportGone = 200*time.Millisecond, 3*time.Second
	helloWait = 3 * time.Second
	t.Cleanup(func() {
		reportQuiet, reportGone, helloWait = oldQuiet, oldGone, oldHello
	})

	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	ctrlR, ctrlW := io.Pipe()
	controls := make(chan Control, 16)
	go func() {
		for {
			c, err := ReadControl(ctrlR)
			if err != nil {
				return
			}
			controls <- c
		}
	}()
	addr := freeUDP(t)
	done := make(chan error, 1)
	go func() { done <- Cast(pr, ctrlW, addr) }()
	time.Sleep(30 * time.Millisecond)
	conn, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	_, toMac := TestKeys()
	aead, err := NewAEAD(toMac)
	if err != nil {
		t.Fatal(err)
	}
	var n uint64
	send := func(kind byte, body []byte) {
		n++
		pkt, err := Seal(aead, DirToMac, kind, n, body)
		if err != nil {
			t.Fatal(err)
		}
		conn.Write(pkt)
	}
	send(PacketHello, nil)
	// 断 1 秒：超过 reportQuiet，没到 reportGone
	time.Sleep(time.Second)
	select {
	case err := <-done:
		t.Fatalf("stopped during a short outage: %v", err)
	default:
	}
	body := make([]byte, 36)
	binary.BigEndian.PutUint32(body[12:16], 100)
	send(PacketReport, body)
	var key, bitrate bool
	deadline := time.After(2 * time.Second)
	for !key || !bitrate {
		select {
		case c := <-controls:
			key = key || c.Kind == CtrlKeyframe
			bitrate = bitrate || (c.Kind == CtrlBitrate && c.Bitrate < PictureBitrate(1920, 1080))
		case <-deadline:
			t.Fatalf("after resuming: keyframe %v, lower bitrate %v", key, bitrate)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("stopped after resuming: %v", err)
	default:
	}
}

func TestAbortedSendDoesNotSkipSequence(t *testing.T) {
	toMobile, _ := TestKeys()
	seal, err := NewAEAD(toMobile)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	sink, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sink.Close() })
	c := &caster{conn: conn, sealAEAD: seal, peer: sink.LocalAddr()}
	payload := append([]byte{0, 0, 0, 1}, bytes.Repeat([]byte{0x11}, 30000)...)
	frame := Frame{Kind: KindVideo, Number: 1, Payload: payload}
	pkts := Packetize(frame, 0, CodecHEVC)
	if len(pkts) < 8 {
		t.Fatalf("packets %d", len(pkts))
	}
	done := make(chan struct{})
	go func() {
		c.sendFrame(pkts, true, 0, 0, 6_000_000, time.Time{})
		close(done)
	}()
	time.Sleep(4 * time.Millisecond)
	c.mu.Lock()
	c.sendGen++
	c.mu.Unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("aborted send did not return")
	}
	c.mu.Lock()
	seq := c.seq
	c.mu.Unlock()
	if seq == 0 || int(seq) >= len(pkts) {
		t.Fatalf("seq=%d packets=%d", seq, len(pkts))
	}
	_ = sink.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 2048)
	var got []uint16
	for {
		n, _, err := sink.ReadFrom(buf)
		if err != nil {
			break
		}
		_, _, plain, err := Open(seal, DirToMobile, buf[:n])
		if err != nil || len(plain) < 21 {
			t.Fatal(err)
		}
		got = append(got, binary.BigEndian.Uint16(plain[19:21]))
	}
	if len(got) != int(seq) {
		t.Fatalf("received %d, seq advanced %d", len(got), seq)
	}
	for i, s := range got {
		if s != uint16(i) {
			t.Fatalf("seq[%d]=%d", i, s)
		}
	}
}

func TestPeerFollowsHelloAndReport(t *testing.T) {
	toMobile, toMac := TestKeys()
	sealMobile, err := NewAEAD(toMobile)
	if err != nil {
		t.Fatal(err)
	}
	sealMac, err := NewAEAD(toMac)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c := &caster{
		conn: conn, sealAEAD: sealMobile, openAEAD: sealMac,
		stopped: make(chan struct{}), quietDeadline: time.Now().Add(time.Minute),
		bitrate: 2_000_000, codec: CodecHEVC,
	}
	c.rate.reset(time.Now(), c.bitrate, rateCeil)
	go c.recv()
	t.Cleanup(func() { c.stop(nil) })

	mac := conn.LocalAddr().String()
	a := dialUDP(t, mac)
	b := dialUDP(t, mac)
	hello, err := Seal(sealMac, DirToMac, PacketHello, 1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Write(hello); err != nil {
		t.Fatal(err)
	}
	if !waitPeer(c, a.LocalAddr().String()) {
		t.Fatalf("peer after hello = %v", c.currentPeer())
	}
	body := make([]byte, 36)
	binary.BigEndian.PutUint32(body[12:16], 10)
	report, err := Seal(sealMac, DirToMac, PacketReport, 2, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Write(report); err != nil {
		t.Fatal(err)
	}
	if !waitPeer(c, b.LocalAddr().String()) {
		t.Fatalf("peer after report = %v, want %s", c.currentPeer(), b.LocalAddr())
	}
	other := dialUDP(t, mac)
	if _, err := other.Write([]byte("not a packet")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if got := c.currentPeer().String(); got != b.LocalAddr().String() {
		t.Fatalf("undecryptable packet moved the peer to %s", got)
	}
	if err := c.send(PacketBeat, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1500)
	n, err := b.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	kind, _, plain, err := Open(sealMobile, DirToMobile, buf[:n])
	if err != nil || kind != PacketBeat || string(plain) != "ping" {
		t.Fatalf("video path = %d %q %v", kind, plain, err)
	}
}

func freeUDP(t *testing.T) string {
	t.Helper()
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.LocalAddr().String()
	ln.Close()
	return addr
}

func dialUDP(t *testing.T, addr string) *net.UDPConn {
	t.Helper()
	c, err := net.Dial("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c.(*net.UDPConn)
}

func waitPeer(c *caster, want string) bool {
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p := c.currentPeer(); p != nil && p.String() == want {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// 慢链路上一帧按码率的 1.5 倍摊开发，不一口气冲进去
func TestFramesArePacedToTheBitrate(t *testing.T) {
	toMobile, _ := TestKeys()
	seal, _ := NewAEAD(toMobile)
	conn, _ := net.ListenPacket("udp", "127.0.0.1:0")
	t.Cleanup(func() { conn.Close() })
	sink, _ := net.ListenPacket("udp", "127.0.0.1:0")
	t.Cleanup(func() { sink.Close() })
	c := &caster{conn: conn, sealAEAD: seal, peer: sink.LocalAddr()}
	frame := Frame{Kind: KindVideo, Number: 1, Keyframe: true, Payload: append([]byte{0, 0, 0, 1}, bytes.Repeat([]byte{0x22}, 12000)...)}
	pkts := Packetize(frame, 0, CodecHEVC)
	start := time.Now()
	c.sendFrame(pkts, true, 0, 0, 600_000, time.Time{})
	// 12 KB 多一点按 600 kbps 要 160ms 以上
	if d := time.Since(start); d < 120*time.Millisecond || d > time.Second {
		t.Fatalf("sent in %s", d)
	}
}

// 正在发的时候来的帧排队，不丢；积压超过一秒的量才丢普通帧、要关键帧；关键帧在路上时不再要
func TestQueueKeepsFramesUntilTheBudget(t *testing.T) {
	ctrlR, ctrlW := io.Pipe()
	controls := make(chan Control, 16)
	go func() {
		for {
			c, err := ReadControl(ctrlR)
			if err != nil {
				return
			}
			controls <- c
		}
	}()
	c := &caster{control: ctrlW, peer: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9}, bitrate: rateFloor}
	c.busy, c.sendingKey = true, true // 一个关键帧正在发
	small := Frame{Kind: KindVideo, Payload: make([]byte, 2000)}
	for range 5 {
		c.submit(small)
	}
	c.mu.Lock()
	queued, dropped, wait := len(c.queue), c.dropped, c.waitKey
	c.mu.Unlock()
	if queued != 5 || dropped != 0 || wait {
		t.Fatalf("queued %d dropped %d waitKey %v", queued, dropped, wait)
	}
	// 600 kbps 一秒是 75 KB：再来一个 80 KB 的普通帧就超了
	c.submit(Frame{Kind: KindVideo, Payload: make([]byte, 80_000)})
	c.mu.Lock()
	queued, wait = len(c.queue), c.waitKey
	c.mu.Unlock()
	if queued != 0 || !wait {
		t.Fatalf("over budget: queued %d waitKey %v", queued, wait)
	}
	// 关键帧还在发：这次不要，排到之后再要
	select {
	case got := <-controls:
		t.Fatalf("asked for a keyframe while one was sending: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
	c.mu.Lock()
	armed := c.keyArmed
	c.busy, c.sendingKey = false, false
	c.mu.Unlock()
	if !armed {
		t.Fatal("the request was not kept for later")
	}
	select {
	case got := <-controls:
		if got.Kind != CtrlKeyframe {
			t.Fatalf("control %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no keyframe after the one in flight finished")
	}
}

// 码率压到最低一档：帧率降到 10、长边减半（15 帧起就减半）；子进程按半尺寸重报配置时码率照旧，不按新画面重新起步
func TestLowestStepHalvesThePicture(t *testing.T) {
	ctrlR, ctrlW := io.Pipe()
	controls := make(chan Control, 16)
	go func() {
		for {
			c, err := ReadControl(ctrlR)
			if err != nil {
				return
			}
			controls <- c
		}
	}()
	framesR, framesW := io.Pipe()
	c := &caster{control: ctrlW, fps: 60, stopped: make(chan struct{})}
	go c.readFrames(framesR)
	WriteFrame(framesW, Frame{Kind: KindConfig, Payload: MarshalConfig(Config{Codec: CodecHEVC, Width: 1920, Height: 1246})})
	full := PictureBitrate(1920, 1246)
	waitFor := func(want func(Control) bool, what string) Control {
		t.Helper()
		deadline := time.After(2 * time.Second)
		for {
			select {
			case got := <-controls:
				if want(got) {
					return got
				}
			case <-deadline:
				t.Fatalf("no %s", what)
			}
		}
	}
	time.Sleep(50 * time.Millisecond)
	c.mu.Lock()
	if c.bitrate != full || c.fullBit != full {
		t.Fatalf("start bitrate %d full %d", c.bitrate, c.fullBit)
	}
	c.bitrate = rateFloor
	c.mu.Unlock()
	c.applyFPS(rateFloor)
	waitFor(func(x Control) bool { return x.Kind == CtrlFPS && x.FPS == 10 }, "fps 10")
	waitFor(func(x Control) bool { return x.Kind == CtrlScale && x.Scale == 2 }, "scale 1/2")

	WriteFrame(framesW, Frame{Kind: KindConfig, Payload: MarshalConfig(Config{Codec: CodecHEVC, Width: 960, Height: 622, FullWidth: 1920, FullHeight: 1246})})
	got := waitFor(func(x Control) bool { return x.Kind == CtrlBitrate }, "bitrate after rescale")
	if got.Bitrate != rateFloor {
		t.Fatalf("bitrate after rescale %d", got.Bitrate)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bitrate != rateFloor || c.fullBit != full {
		t.Fatalf("rescale reset the rate: bitrate %d full %d", c.bitrate, c.fullBit)
	}
	framesW.Close()
}

// 手机点名的包用新计数器原样补发；不在缓存里的不补；超过这一秒的额度不补
func TestNackResendsRememberedPackets(t *testing.T) {
	toMobile, _ := TestKeys()
	seal, _ := NewAEAD(toMobile)
	conn, _ := net.ListenPacket("udp", "127.0.0.1:0")
	t.Cleanup(func() { conn.Close() })
	sink, _ := net.ListenPacket("udp", "127.0.0.1:0")
	t.Cleanup(func() { sink.Close() })
	c := &caster{conn: conn, sealAEAD: seal, peer: sink.LocalAddr(), bitrate: 4_000_000}
	frame := Frame{Kind: KindVideo, Number: 1, Payload: append([]byte{0, 0, 0, 1}, bytes.Repeat([]byte{0x33}, 6000)...)}
	pkts := Packetize(frame, 100, CodecHEVC)
	c.sendFrame(pkts, false, 0, 100, 50_000_000, time.Time{})
	read := func() (counters []uint64, seqs []uint16) {
		_ = sink.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		buf := make([]byte, 2048)
		for {
			n, _, err := sink.ReadFrom(buf)
			if err != nil {
				return
			}
			kind, counter, plain, err := Open(seal, DirToMobile, buf[:n])
			if err != nil || kind != PacketVideo {
				t.Fatalf("kind %d err %v", kind, err)
			}
			counters = append(counters, counter)
			seqs = append(seqs, binary.BigEndian.Uint16(plain[19:21]))
		}
	}
	first, _ := read()
	nack := []byte{0, 102, 0, 101, 0x30, 0x39} // 102、101，和一个没发过的 12345
	c.resend(nack)
	counters, seqs := read()
	if len(seqs) != 2 || seqs[0] != 102 || seqs[1] != 101 {
		t.Fatalf("resent %v", seqs)
	}
	if counters[0] <= first[len(first)-1] {
		t.Fatalf("resend reused counter %d", counters[0])
	}
	// 额度是发送节奏的四分之一：4 Mbps 时 1.5 Mbit，一个包 9.6 kbit，最多再补 150 个左右
	for range 200 {
		c.resend([]byte{0, 101})
	}
	_, more := read()
	if len(more) < 100 || len(more) > 160 {
		t.Fatalf("resent %d past the budget", len(more))
	}
}

// 关键帧按节奏摊开时，后面的帧在 Mac 这边多等的那份不算网络排队
func TestOwnDelayIsTheExcessOverTheFloor(t *testing.T) {
	c := &caster{}
	now := time.Unix(1_700_000_000, 0)
	for i := range 20 {
		c.noteOwn(now.Add(time.Duration(i)*100*time.Millisecond), 10*time.Millisecond)
	}
	at := now.Add(2 * time.Second)
	for i := range 5 {
		c.noteOwn(at.Add(time.Duration(i)*20*time.Millisecond), 310*time.Millisecond)
	}
	if d := c.ownDelay(at.Add(100 * time.Millisecond)); d != 300*time.Millisecond {
		t.Fatalf("own delay %s", d)
	}
	if d := c.ownDelay(at.Add(2 * time.Second)); d != 0 {
		t.Fatalf("stale samples still count: %s", d)
	}
	// 10 秒前的样本不再算基线
	c.noteOwn(at.Add(20*time.Second), 50*time.Millisecond)
	if len(c.own) != 1 {
		t.Fatalf("kept %d old samples", len(c.own))
	}
}

// 手机的 hello 之前编出来的帧攒着，hello 一到就从关键帧起发出去：画面不动时不会再有新帧
func TestFramesBeforeHelloWaitForThePeer(t *testing.T) {
	c := &caster{control: io.Discard, bitrate: rateFloor}
	c.busy, c.sendingKey = true, true // 不真发，只看排队
	c.submit(Frame{Kind: KindVideo, Number: 1, Payload: make([]byte, 100)})
	c.submit(Frame{Kind: KindVideo, Number: 2, Keyframe: true, Payload: make([]byte, 100)})
	c.submit(Frame{Kind: KindVideo, Number: 3, Payload: make([]byte, 100)})
	c.mu.Lock()
	queued, early := len(c.queue), len(c.early)
	c.mu.Unlock()
	if queued != 0 || early != 2 {
		t.Fatalf("before hello: queued %d early %d", queued, early)
	}
	c.notePeer(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	c.sendEarly()
	c.submit(Frame{Kind: KindVideo, Number: 4, Payload: make([]byte, 100)})
	c.mu.Lock()
	var got []uint64
	for _, f := range c.queue {
		got = append(got, f.Number)
	}
	c.mu.Unlock()
	if !slices.Equal(got, []uint64{2, 3, 4}) {
		t.Fatalf("after hello queued %v", got)
	}
}

// 常驻进程重新拉起子进程后，码率、帧率上限和长边除数按这边记着的补发给新子进程
func TestRestartedHelperGetsTheCurrentSettings(t *testing.T) {
	ctrlR, ctrlW := io.Pipe()
	got := make(chan Control, 8)
	go func() {
		for {
			c, err := ReadControl(ctrlR)
			if err != nil {
				return
			}
			got <- c
		}
	}()
	c := &caster{control: ctrlW, bitrate: 900_000, fps: 15, scale: 2, stopped: make(chan struct{})}
	pr, pw := io.Pipe()
	go c.readFrames(pr)
	if err := WriteFrame(pw, Frame{Kind: KindStatus, Payload: []byte{StatusRestarted}}); err != nil {
		t.Fatal(err)
	}
	want := []Control{{Kind: CtrlBitrate, Bitrate: 900_000}, {Kind: CtrlFPS, FPS: 15}, {Kind: CtrlScale, Scale: 2}}
	for _, w := range want {
		select {
		case x := <-got:
			if x.Kind != w.Kind || x.Bitrate != w.Bitrate || x.FPS != w.FPS || x.Scale != w.Scale {
				t.Fatalf("got %+v, want %+v", x, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("no %+v", w)
		}
	}
	pw.Close()
	ctrlW.Close()
}

// 画面配置比手机的 hello 先到：先按中转起步，第一次认出直连换成直连的起始码率；
// 之后换线路只改上限，降下来的码率不跳回去
func TestPathSetsStartBitrate(t *testing.T) {
	var control bytes.Buffer
	c := &caster{control: &control, fps: 60, codec: CodecHEVC}
	c.fullW, c.fullH = 1920, 1246
	c.haveConfig = true
	c.fullBit = PictureBitrate(1920, 1246)
	c.bitrate = c.fullBit
	c.rate.reset(time.Now(), c.bitrate, relayCeil)

	c.setPath(true, true)
	if want := DirectBitrate(1920, 1246); c.bitrate != want || c.rate.maxBit != rateCeil {
		t.Fatalf("direct: bitrate %d ceiling %d, want %d %d", c.bitrate, c.rate.maxBit, want, rateCeil)
	}
	if ctl, err := ReadControl(&control); err != nil || ctl.Kind != CtrlBitrate || ctl.Bitrate != c.bitrate {
		t.Fatalf("control %+v %v", ctl, err)
	}
	c.setPath(false, false)
	if c.bitrate != relayCeil || c.rate.maxBit != relayCeil || c.rate.bitrate != relayCeil {
		t.Fatalf("relay: bitrate %d ceiling %d", c.bitrate, c.rate.maxBit)
	}
	// 拥堵降到 2 Mbps 后打洞成直连：只放开上限
	c.bitrate, c.rate.bitrate = 2_000_000, 2_000_000
	c.setPath(true, false)
	if c.bitrate != 2_000_000 || c.rate.maxBit != rateCeil {
		t.Fatalf("punched: bitrate %d ceiling %d", c.bitrate, c.rate.maxBit)
	}
}
