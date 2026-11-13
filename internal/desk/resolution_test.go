package desk

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

func TestFitResolution(t *testing.T) {
	for _, tt := range []struct {
		name              string
		w, h, edge        int
		resolution, scale uint32
		wantW, wantH      int
	}{
		{"16:9 720p", 3840, 2160, 1170, 720, 1, 1280, 720},
		{"16:10 1080p", 2560, 1600, 1170, 1080, 1, 1728, 1080},
		{"portrait", 1600, 2560, 1170, 720, 1, 720, 1152},
		{"ultrawide", 3440, 1440, 1170, 1080, 1, 1920, 802},
		{"small window", 800, 600, 1170, 1440, 1, 800, 600},
		{"odd window", 801, 601, 1170, 720, 1, 800, 600},
		{"1440 exceeds phone and 1920", 3840, 2160, 1170, 1440, 1, 2560, 1440},
		{"fixed ignores congestion scale", 3840, 2160, 1170, 1440, 2, 2560, 1440},
		{"auto retains long edge", 2560, 1600, 1920, 0, 1, 1920, 1200},
		{"auto halves", 2560, 1600, 1920, 0, 2, 960, 600},
		{"unknown is auto", 2560, 1600, 1920, 900, 2, 960, 600},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w, h := FitResolution(tt.w, tt.h, tt.edge, tt.resolution, tt.scale)
			if w != tt.wantW || h != tt.wantH {
				t.Fatalf("got %dx%d, want %dx%d", w, h, tt.wantW, tt.wantH)
			}
		})
	}
}

func TestResolutionControl(t *testing.T) {
	var b bytes.Buffer
	want := Control{Kind: CtrlResolution, Resolution: 1440, Scale: 1}
	if err := WriteControl(&b, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadControl(&b)
	if err != nil || got.Kind != want.Kind || got.Resolution != 1440 || got.Scale != 1 {
		t.Fatalf("%+v %v", got, err)
	}
	// 旧的、没有 FullWidth/FullHeight 的配置照样可以读。
	raw := MarshalConfig(Config{Codec: CodecHEVC, Width: 800, Height: 600})[:9]
	cfg, err := UnmarshalConfig(raw)
	if err != nil || cfg.FullWidth != 800 || cfg.FullHeight != 600 {
		t.Fatalf("old config %+v %v", cfg, err)
	}
}

func TestResolutionKeepsCongestionAndIndependentFPS(t *testing.T) {
	log := &controlLog{}
	c := &caster{control: log, fps: 60, autoFPS: 60}
	c.configure(Config{Codec: CodecHEVC, Width: 1920, Height: 1200, FullWidth: 1920, FullHeight: 1200})
	at := time.Now().Add(-time.Second)
	c.bitrate = rateFloor
	c.rate.bitrate = rateFloor
	c.rate.lastCut, c.rate.stable = at, at
	c.rate.loss, c.rate.packets = 7, 17
	c.pin(30)
	c.applyFPS(rateFloor)
	if c.scale != 2 {
		t.Fatal("auto did not halve")
	}
	log.take(t)
	if err := c.chooseResolution(1440); err != nil {
		t.Fatal(err)
	}
	cs := log.take(t)
	if len(cs) != 1 || cs[0].Kind != CtrlResolution || cs[0].Resolution != 1440 || cs[0].Scale != 1 {
		t.Fatalf("switch should be atomic: %+v", cs)
	}
	c.configure(Config{Codec: CodecHEVC, Width: 2304, Height: 1440, FullWidth: 2304, FullHeight: 1440})
	if c.bitrate != rateFloor || c.rate.bitrate != rateFloor || !c.rate.lastCut.Equal(at) || c.rate.loss != 7 || c.rate.packets != 17 {
		t.Fatalf("lost congestion: %+v", c.rate)
	}
	if c.scale != 1 || c.fps != 30 {
		t.Fatalf("scale %d fps %d", c.scale, c.fps)
	}
	c.chooseResolution(0)
	if c.scale != 2 || c.fps != 30 {
		t.Fatalf("back to auto scale %d fps %d", c.scale, c.fps)
	}
}

func TestResolutionConfigComparesDimensions(t *testing.T) {
	log := &controlLog{}
	c := &caster{control: log, fps: 60, autoFPS: 60}
	c.configure(Config{Codec: CodecHEVC, Width: 800, Height: 600, FullWidth: 800, FullHeight: 600})
	// 两个小基准都触及 startFloor，输出也一样，仍须更新基准。
	c.configure(Config{Codec: CodecHEVC, Width: 800, Height: 600, FullWidth: 1000, FullHeight: 400})
	if c.fullW != 1000 || c.fullH != 400 {
		t.Fatalf("stale baseline %dx%d", c.fullW, c.fullH)
	}
	c.direct = true
	start, _ := c.startLocked()
	if start != DirectBitrate(1000, 400) {
		t.Fatalf("stale direct budget %d", start)
	}
}

type brokenResolutionControl struct{}

func (brokenResolutionControl) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestResolutionResyncKeepsLatestAfterWriteFailure(t *testing.T) {
	c := &caster{control: brokenResolutionControl{}, bitrate: rateFloor, fps: 15, autoFPS: 10}
	for _, r := range []uint32{720, 1080, 0, 1440} {
		if err := c.chooseResolution(r); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("hidden write failure %v", err)
		}
	}
	log := &controlLog{}
	c.control = log
	c.resync()
	got, ok := lastOf(log.take(t), CtrlResolution)
	if !ok || got.Resolution != 1440 || got.Scale != 1 {
		t.Fatalf("restarted with stale choice %+v", got)
	}
	if c.bitrate != rateFloor {
		t.Fatalf("rate reset %d", c.bitrate)
	}
}
