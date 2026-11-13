//go:build darwin

package desk

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func resolutionTestEngine(w, h, edge int, out io.Writer) *engine {
	m := &macAPI{
		copyMode:       func(uint32) uintptr { return 0 },
		pixelsWide:     func(uint32) uintptr { return uintptr(w) },
		pixelsHigh:     func(uint32) uintptr { return uintptr(h) },
		activeDisplays: func(_ uint32, ids *uint32, n *uint32) int32 { *ids = 1; *n = 1; return 0 },
	}
	return &engine{mac: m, opt: Options{LongEdge: edge}, out: out, stream: 1, displayID: 1, codec: CodecHEVC}
}

func TestResolutionSameOutputReportsNewBaseline(t *testing.T) {
	var out bytes.Buffer
	e := resolutionTestEngine(3840, 2160, 2560, &out)
	e.width, e.height = 1280, 720
	e.fullW, e.fullH = 2560, 1440
	e.picture.Store(pictureSetting(720, 1))
	if err := e.rebuildDisplay(0); err != nil {
		t.Fatal(err)
	}
	f, err := ReadFrame(&out)
	if err != nil || f.Kind != KindConfig {
		t.Fatalf("baseline update missing: %+v %v", f, err)
	}
	cfg, err := UnmarshalConfig(f.Payload)
	if err != nil || cfg.Width != 1280 || cfg.Height != 720 || cfg.FullWidth != 1280 || cfg.FullHeight != 720 {
		t.Fatalf("stale config %+v %v", cfg, err)
	}
	if e.stream != 1 {
		t.Fatal("unchanged output reopened capture")
	}
	if err := e.rebuildDisplay(0); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("unchanged baseline was reported repeatedly")
	}
}

func TestResolutionConfigPipeClosedWhileCaptureLocked(t *testing.T) {
	e := &engine{out: brokenResolutionControl{}, stop: make(chan struct{}), mac: &macAPI{
		mainLoop: func() uintptr { return 0 }, stopLoop: func(uintptr) {},
	}}
	written := make(chan struct{})
	go func() {
		e.streamMu.Lock()
		e.write(Frame{Kind: KindConfig})
		e.streamMu.Unlock()
		close(written)
	}()
	select {
	case <-written:
	case <-time.After(time.Second):
		t.Fatal("closed config pipe deadlocked capture initialization")
	}
	select {
	case <-e.stop:
	case <-time.After(time.Second):
		t.Fatal("capture did not stop after the config pipe closed")
	}
}

func TestResolutionQueuedRebuildUsesLatestChoice(t *testing.T) {
	e := resolutionTestEngine(800, 600, 1920, io.Discard)
	e.width, e.height, e.fullW, e.fullH = 800, 600, 800, 600
	// 卡住采集锁，模拟上一档还在重建。后来的控制必须先按接收顺序记下。
	e.streamMu.Lock()
	for _, r := range []uint32{720, 1440, 0, 1080, 720, 1440} {
		e.setPicture(r, 2)
	}
	if e.picture.Load() != pictureSetting(1440, 1) {
		t.Fatal("latest selection was not saved before rebuilding")
	}
	e.streamMu.Unlock()
	if err := e.rebuildDisplay(0); err != nil {
		t.Fatal(err)
	}
	e.streamMu.Lock()
	defer e.streamMu.Unlock()
	if e.activePicture != pictureSetting(1440, 1) {
		t.Fatalf("applied stale setting %d", e.activePicture)
	}
}
