package desk

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

func TestCastStopsWithoutHello(t *testing.T) {
	oldQuiet, oldHello := reportQuiet, helloWait
	reportQuiet = 200 * time.Millisecond
	helloWait = 400 * time.Millisecond
	t.Cleanup(func() {
		reportQuiet, helloWait = oldQuiet, oldHello
	})

	pr, pw := io.Pipe()
	t.Cleanup(func() { pw.Close() })
	addr := freeUDP(t)
	start := time.Now()
	err := Cast(pr, io.Discard, addr)
	if !errors.Is(err, errNoHello) {
		t.Fatalf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed < 300*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("stopped after %s", elapsed)
	}
}

func TestCastQuietStartsAfterHello(t *testing.T) {
	oldQuiet, oldHello := reportQuiet, helloWait
	reportQuiet = 300 * time.Millisecond
	helloWait = 5 * time.Second
	t.Cleanup(func() {
		reportQuiet, helloWait = oldQuiet, oldHello
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
		if !errors.Is(err, errNoReport) {
			t.Fatalf("err = %v", err)
		}
		sinceHello := time.Since(helloAt)
		if sinceHello < 200*time.Millisecond || sinceHello > 2*time.Second {
			t.Fatalf("report timer %s after hello, total %s", sinceHello, time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not stop after hello without reports")
	}
}

func TestCastReportResetsQuiet(t *testing.T) {
	oldQuiet, oldHello := reportQuiet, helloWait
	reportQuiet = 500 * time.Millisecond
	helloWait = 3 * time.Second
	t.Cleanup(func() {
		reportQuiet, helloWait = oldQuiet, oldHello
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
		if !errors.Is(err, errNoReport) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) < 1500*time.Millisecond {
			t.Fatalf("quiet timer did not reset, stopped after %s", time.Since(start))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not stop after reports ended")
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
		c.sendFrame(pkts, true, CodecHEVC, 0, 0)
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
