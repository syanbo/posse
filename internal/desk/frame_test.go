package desk

import (
	"bytes"
	"io"
	"strings"
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
	// 屏列表帧能读回来
	if err := WriteFrame(&buf, Frame{Kind: KindDisplays, Payload: []byte(`{"current":2}`)}); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFrame(&buf); err != nil || got.Kind != KindDisplays || string(got.Payload) != `{"current":2}` {
		t.Fatalf("displays = %+v, %v", got, err)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	c := Config{Codec: CodecHEVC, Width: 960, Height: 540, FullWidth: 1920, FullHeight: 1080}
	got, err := UnmarshalConfig(MarshalConfig(c))
	if err != nil || got != c {
		t.Fatalf("config = %+v %v", got, err)
	}
	// 不报满尺寸的（旧子进程只有 9 字节）读出来满尺寸就是输出尺寸
	old := MarshalConfig(Config{Codec: CodecHEVC, Width: 1920, Height: 1080})[:9]
	if got, err := UnmarshalConfig(old); err != nil || got.FullWidth != 1920 || got.FullHeight != 1080 {
		t.Fatalf("old config = %+v %v", got, err)
	}
	if _, err := UnmarshalConfig([]byte{9}); err == nil {
		t.Fatal("short config accepted")
	}
}

func TestControlRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	for _, c := range []Control{{Kind: CtrlKeyframe}, {Kind: CtrlBitrate, Bitrate: 7_500_000}, {Kind: CtrlDisplay, Display: 69734662}, {Kind: CtrlFPS, FPS: 15}, {Kind: CtrlWindows}, {Kind: CtrlWindow, Window: 4242}} {
		if err := WriteControl(&buf, c); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteControl(&buf, Control{Kind: 99}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []Control{{Kind: CtrlKeyframe}, {Kind: CtrlBitrate, Bitrate: 7_500_000}, {Kind: CtrlDisplay, Display: 69734662}, {Kind: CtrlFPS, FPS: 15}, {Kind: CtrlWindows}, {Kind: CtrlWindow, Window: 4242}, {Kind: 99}} {
		got, err := ReadControl(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if got.Kind != want.Kind || got.Bitrate != want.Bitrate || got.Display != want.Display || got.FPS != want.FPS || got.Window != want.Window || len(got.Events) != len(want.Events) {
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

// 小画面在好网络上也从 1.5 Mbps、60 帧起步，差网络再往 400 kbps、10 帧降
func TestSmallPictureStartsAtFullFrameRate(t *testing.T) {
	for _, size := range [][2]int{{640, 400}, {960, 600}, {1920, 1246}} {
		start := PictureBitrate(size[0], size[1])
		if start < startFloor {
			t.Fatalf("%v starts at %d", size, start)
		}
		if got := fpsFor(start, start, 60); got != 60 {
			t.Fatalf("%v starts at %d fps", size, got)
		}
		if got := fpsFor(rateFloor, start, 60); size[0] == 1920 && got != 10 {
			t.Fatalf("%v at the floor runs %d fps", size, got)
		}
	}
}

func TestBadControlNamesItsKind(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{2, 0, 0, 0, CtrlFPS, 9})
	if _, err := ReadControl(&buf); err == nil || !strings.Contains(err.Error(), "control 6") {
		t.Fatalf("err = %v", err)
	}
}

func TestDirectBitrate(t *testing.T) {
	for _, c := range []struct {
		w, h int
		want uint32
	}{
		{1920, 1080, 20_000_000},
		{2560, 1440, 40_000_000},
		{3840, 2160, rateCeil},
		{640, 360, 2_000_000},
	} {
		if got := DirectBitrate(c.w, c.h); got != c.want {
			t.Errorf("DirectBitrate(%d, %d) = %d, want %d", c.w, c.h, got, c.want)
		}
	}
	if got := DirectBitrate(1920, 1246); got < 23_000_000 || got > 25_000_000 {
		t.Errorf("1920x1246 = %d", got)
	}
}
