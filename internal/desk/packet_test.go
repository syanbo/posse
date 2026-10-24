package desk

import (
	"bytes"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	toMobile, toMac := TestKeys()
	pkt, err := Seal(toMobile, DirToMobile, PacketVideo, 7, []byte("rtp"))
	if err != nil {
		t.Fatal(err)
	}
	kind, n, plain, err := Open(toMobile, DirToMobile, pkt)
	if err != nil || kind != PacketVideo || n != 7 || string(plain) != "rtp" {
		t.Fatalf("open = %d %d %q %v", kind, n, plain, err)
	}
	if _, _, _, err := Open(toMac, DirToMobile, pkt); err == nil {
		t.Fatal("other direction key accepted the packet")
	}
	pkt[0] ^= 1
	if _, _, _, err := Open(toMobile, DirToMobile, pkt); err == nil {
		t.Fatal("tampered type accepted")
	}
}

func TestReplayWindow(t *testing.T) {
	var r Replay
	if !r.Accept(5) || !r.Accept(4) || !r.Accept(6) || r.Accept(5) || r.Accept(0) {
		t.Fatal("basic window")
	}
	if !r.Accept(6+1024) || r.Accept(6) {
		t.Fatal("advance drops the old window")
	}
}

func TestRTPRoundTrip(t *testing.T) {
	nal := bytes.Repeat([]byte{0x26, 0x01}, 2000)
	frame := Frame{Kind: KindVideo, Keyframe: true, Number: 9, CaptureUnixNano: 1234, Payload: append([]byte{0, 0, 0, 1}, nal...)}
	pkts := Packetize(frame, 1)
	if len(pkts) < 2 {
		t.Fatalf("expected fragments, got %d", len(pkts))
	}
	var asm Assembler
	var got *Frame
	for i, p := range pkts {
		f, err := asm.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		if i < len(pkts)-1 && f != nil {
			t.Fatal("completed before the last fragment")
		}
		got = f
	}
	if got == nil || !got.Keyframe || got.Number != 9 || got.CaptureUnixNano != 1234 || !bytes.Equal(got.Payload[4:], nal) {
		t.Fatalf("frame = %+v", got)
	}
}
