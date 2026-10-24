package desk

import (
	"bytes"
	"io"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	in := Frame{Kind: KindVideo, Keyframe: true, Number: 7, CaptureUnixNano: 99, Payload: []byte{0, 0, 0, 1, 0x40}}
	if err := WriteFrame(&buf, in); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, Frame{Kind: KindHeartbeat, Number: 8, CaptureUnixNano: 100}); err != nil {
		t.Fatal(err)
	}
	if err := WriteFrame(&buf, Frame{Kind: KindStatus, CaptureUnixNano: 101, Payload: []byte{StatusLocked}}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindVideo || !got.Keyframe || got.Number != 7 || got.CaptureUnixNano != 99 || string(got.Payload) != string(in.Payload) {
		t.Fatalf("video = %+v", got)
	}
	got, err = ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindHeartbeat || got.Keyframe || len(got.Payload) != 0 {
		t.Fatalf("heartbeat = %+v", got)
	}
	got, err = ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != KindStatus || len(got.Payload) != 1 || got.Payload[0] != StatusLocked {
		t.Fatalf("status = %+v", got)
	}
	if _, err := ReadFrame(&buf); err != io.EOF {
		t.Fatalf("tail = %v", err)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	c := Config{Codec: CodecHEVC, Width: 1920, Height: 1080}
	got, err := UnmarshalConfig(MarshalConfig(c))
	if err != nil || got != c {
		t.Fatalf("config = %+v %v", got, err)
	}
	if _, err := UnmarshalConfig([]byte{9}); err == nil {
		t.Fatal("short config accepted")
	}
}

func TestControlRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	for _, c := range []Control{{Kind: CtrlKeyframe}, {Kind: CtrlBitrate, Bitrate: 7_500_000}, {Kind: CtrlStop}} {
		if err := WriteControl(&buf, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteControl(&buf, Control{Kind: 9}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []Control{{Kind: CtrlKeyframe}, {Kind: CtrlBitrate, Bitrate: 7_500_000}, {Kind: CtrlStop}, {Kind: 9}} {
		got, err := ReadControl(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != want.Kind || got.Bitrate != want.Bitrate || len(got.Events) != len(want.Events) {
			t.Fatalf("control = %+v, want %+v", got, want)
		}
	}
}

func TestFit(t *testing.T) {
	cases := []struct{ w, h, long, ew, eh int }{
		{1280, 720, 1920, 1280, 720},
		{3840, 2160, 1920, 1920, 1080},
		{1921, 1080, 1920, 1920, 1078},
		{1, 1, 1920, 2, 2},
	}
	for _, c := range cases {
		w, h := Fit(c.w, c.h, c.long)
		if w != c.ew || h != c.eh || w%2 != 0 || h%2 != 0 {
			t.Errorf("Fit(%d,%d,%d) = %d,%d, want %d,%d", c.w, c.h, c.long, w, h, c.ew, c.eh)
		}
	}
}
