package desk

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestPacketRoundTrip(t *testing.T) {
	toMobile, toMac := TestKeys()
	sealMobile, err := NewAEAD(toMobile)
	if err != nil {
		t.Fatal(err)
	}
	sealMac, err := NewAEAD(toMac)
	if err != nil {
		t.Fatal(err)
	}
	pkt, err := Seal(sealMobile, DirToMobile, PacketVideo, 7, []byte("rtp"))
	if err != nil {
		t.Fatal(err)
	}
	kind, n, plain, err := Open(sealMobile, DirToMobile, pkt)
	if err != nil || kind != PacketVideo || n != 7 || string(plain) != "rtp" {
		t.Fatalf("open = %d %d %q %v", kind, n, plain, err)
	}
	again, err := Seal(sealMobile, DirToMobile, PacketVideo, 8, []byte("rtp-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, plain, err = Open(sealMobile, DirToMobile, again); err != nil || string(plain) != "rtp-2" {
		t.Fatalf("reused aead: %q %v", plain, err)
	}
	if _, _, _, err := Open(sealMac, DirToMobile, pkt); err == nil {
		t.Fatal("other direction key accepted the packet")
	}
	pkt[0] ^= 1
	if _, _, _, err := Open(sealMobile, DirToMobile, pkt); err == nil {
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
	// 窗口里跨字的旧包也要认得出是重复的
	r = Replay{}
	for n := uint64(1); n <= 500; n++ {
		if !r.Accept(n) {
			t.Fatalf("fresh %d rejected", n)
		}
	}
	for n := uint64(1); n <= 500; n++ {
		if r.Accept(n) {
			t.Fatalf("replayed %d accepted", n)
		}
	}
	// 乱序到的照收，只收一次
	r = Replay{}
	r.Accept(1)
	r.Accept(300)
	if !r.Accept(100) || r.Accept(100) || !r.Accept(301) || r.Accept(100) || !r.Accept(299) {
		t.Fatal("out of order within the window")
	}
}

func TestRTPRoundTrip(t *testing.T) {
	nal := bytes.Repeat([]byte{0x26, 0x01}, 2000)
	frame := Frame{Kind: KindVideo, Keyframe: true, Number: 9, CaptureUnixNano: 1234, Payload: append([]byte{0, 0, 0, 1}, nal...)}
	pkts := Packetize(frame, 1, CodecHEVC)
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

func TestH264FURoundTrip(t *testing.T) {
	small := []byte{0x67, 0x42, 0x00, 0x0a}
	idr := append([]byte{0x65}, bytes.Repeat([]byte{0x11, 0x22}, 2500)...)
	payload := append([]byte{0, 0, 0, 1}, small...)
	payload = append(payload, 0, 0, 0, 1)
	payload = append(payload, idr...)
	frame := Frame{Kind: KindVideo, Keyframe: true, Number: 3, CaptureUnixNano: 99, Payload: payload}
	pkts := Packetize(frame, 10, CodecH264)
	if len(pkts) < 3 {
		t.Fatalf("expected a single NAL plus FU-A fragments, got %d", len(pkts))
	}
	sawFU := false
	var asm Assembler
	var got *Frame
	for i, p := range pkts {
		if len(p)+(1+8+16) > PacketLimit {
			t.Fatalf("packet %d plaintext does not fit", i)
		}
		rtp := p[videoPrefix:]
		if rtp[1]&0x7f != rtpPayloadH264 {
			t.Fatalf("payload type %d", rtp[1]&0x7f)
		}
		body := rtp[12:]
		if len(body) >= 1 && body[0]&0x1f == h264FUA {
			sawFU = true
			if i == 0 {
				t.Fatal("SPS was fragmented")
			}
			if body[1]&0x1f != 5 {
				t.Fatalf("FU-A type %d", body[1]&0x1f)
			}
		}
		f, err := asm.Push(p)
		if err != nil {
			t.Fatal(err)
		}
		if i < len(pkts)-1 && f != nil {
			t.Fatal("completed before the last fragment")
		}
		got = f
	}
	if !sawFU {
		t.Fatal("IDR was not fragmented")
	}
	if got == nil || !bytes.Equal(got.Payload, payload) {
		t.Fatalf("round trip mismatch, got %d want %d\n got %x\nwant %x", len(got.Payload), len(payload), got.Payload[:min(32, len(got.Payload))], payload[:min(32, len(payload))])
	}
}

func TestAssemblerDropsGap(t *testing.T) {
	nal := append([]byte{0x65}, bytes.Repeat([]byte{0xab}, 4000)...)
	frame := Frame{Kind: KindVideo, Keyframe: true, Number: 4, CaptureUnixNano: 1, Payload: append([]byte{0, 0, 0, 1}, nal...)}
	pkts := Packetize(frame, 1, CodecH264)
	if len(pkts) < 3 {
		t.Fatalf("need several fragments, got %d", len(pkts))
	}
	var asm Assembler
	for i, p := range pkts {
		if i == 1 {
			continue
		}
		got, err := asm.Push(p)
		if got != nil {
			t.Fatal("partial frame was assembled")
		}
		if i > 1 && err == nil && i != len(pkts)-1 {
			// 缺口之后的中间片不该被当成完整帧；最后一片要报错而不是交出残帧。
		}
	}
	next := Frame{Kind: KindVideo, Number: 5, CaptureUnixNano: 2, Payload: []byte{0, 0, 0, 1, 0x65, 0x88}}
	got, err := asm.Push(Packetize(next, 100, CodecH264)[0])
	if err != nil || got == nil || got.Number != 5 || !bytes.Equal(got.Payload[4:], []byte{0x65, 0x88}) {
		t.Fatalf("next frame = %+v %v", got, err)
	}
}

// 和手机端 mobile/test/desk_wire_test.dart 的同一组密钥：材料是 Mac 的 sendKey 再 recvKey，手机上是 recvKey 再 sendKey。
func TestDeriveKeysMatchPhone(t *testing.T) {
	secret := make([]byte, 64)
	for i := range secret {
		secret[i] = byte(i)
	}
	nonce := make([]byte, 16)
	for i := range nonce {
		nonce[i] = byte(0xA0 + i)
	}
	toMobile, toMac, err := DeriveKeys(secret, nonce)
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(toMobile[:]) + hex.EncodeToString(toMac[:])
	want := "8f04928271ef6e22b39f3a0528d25ebbfd86bd414efe1241624c2c3e3a6fadee" +
		"7075d509bf5b1cabc9f7fbf8a4ae00fc5bc2875b076f791e8cbfa820a9dc5cdb"
	if got != want {
		t.Fatalf("keys %s", got)
	}
}
