package desk

import (
	"bytes"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// controlLog 记下写给子进程的控制消息。
type controlLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *controlLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

// take 取出到现在为止写的控制消息并清空。
func (l *controlLog) take(t *testing.T) []Control {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Control
	for l.buf.Len() > 0 {
		c, err := ReadControl(&l.buf)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func lastOf(cs []Control, kind byte) (Control, bool) {
	for i := len(cs) - 1; i >= 0; i-- {
		if cs[i].Kind == kind {
			return cs[i], true
		}
	}
	return Control{}, false
}

func TestPinnedFPS(t *testing.T) {
	for v, want := range map[int64]uint32{0: 0, 60: 60, 30: 30, 15: 15, 10: 0, 45: 0, -30: 0, 120: 0, 1 << 40: 0} {
		if got := PinnedFPS(v); got != want {
			t.Errorf("PinnedFPS(%d) = %d, want %d", v, got, want)
		}
	}
}

// 手选帧率后码率怎么变帧率上限都不变；画面减不减半仍只看码率
func TestPinnedFPSIgnoresBitrate(t *testing.T) {
	full := PictureBitrate(1920, 1246)
	log := &controlLog{}
	c := &caster{control: log, fps: 60, autoFPS: 60, fullBit: full, bitrate: full}

	c.pin(15)
	cs := log.take(t)
	if f, ok := lastOf(cs, CtrlFPS); !ok || f.FPS != 15 {
		t.Fatalf("pin 15 sent %v", cs)
	}
	// 码率满着，手选 15 帧不减半
	if _, ok := lastOf(cs, CtrlScale); ok || c.scale > 1 {
		t.Fatalf("pinned 15 at full bitrate halved the picture: %v", cs)
	}

	// 真实调用时 applyFPS 拿到的就是 c.bitrate
	for _, b := range []uint32{full / 3, full / 6, rateFloor} {
		c.bitrate = b
		c.applyFPS(b)
	}
	cs = log.take(t)
	if _, ok := lastOf(cs, CtrlFPS); ok {
		t.Fatalf("bitrate changed the pinned frame rate: %v", cs)
	}
	if s, ok := lastOf(cs, CtrlScale); !ok || s.Scale != 2 {
		t.Fatalf("low bitrate did not halve the picture: %v", cs)
	}
	if c.autoFPS != 10 || c.fps != 15 {
		t.Fatalf("auto %d fps %d", c.autoFPS, c.fps)
	}

	// 手选 60 帧时低码率照样减半
	c.pin(60)
	cs = log.take(t)
	if f, ok := lastOf(cs, CtrlFPS); !ok || f.FPS != 60 {
		t.Fatalf("pin 60 sent %v", cs)
	}
	if c.scale != 2 {
		t.Fatalf("pinned 60 at low bitrate scale 1/%d", c.scale)
	}

	// 回到自动：帧率换成按码率该用的档位
	c.pin(0)
	cs = log.take(t)
	if f, ok := lastOf(cs, CtrlFPS); !ok || f.FPS != 10 {
		t.Fatalf("back to auto sent %v", cs)
	}
	// 自动时照旧跟着码率走，码率回来了升回满尺寸
	c.bitrate = full
	c.applyFPS(full)
	cs = log.take(t)
	if f, ok := lastOf(cs, CtrlFPS); !ok || f.FPS != 60 {
		t.Fatalf("auto at full bitrate sent %v", cs)
	}
	if s, ok := lastOf(cs, CtrlScale); !ok || s.Scale != 1 {
		t.Fatalf("full bitrate kept the half picture: %v", cs)
	}

	// 再手选 30，非法值当自动
	c.pin(30)
	if f, ok := lastOf(log.take(t), CtrlFPS); !ok || f.FPS != 30 {
		t.Fatal("pin 30")
	}
	c.pin(45)
	if f, ok := lastOf(log.take(t), CtrlFPS); !ok || f.FPS != 60 || c.pinned != 0 {
		t.Fatalf("pin 45 pinned %d", c.pinned)
	}
}

// 子进程重启后，手选的帧率上限照样告诉新子进程
func TestResyncKeepsPinnedFPS(t *testing.T) {
	full := PictureBitrate(1920, 1246)
	log := &controlLog{}
	c := &caster{control: log, fps: 60, autoFPS: 60, fullBit: full, bitrate: full}
	c.pin(30)
	log.take(t)
	c.resync()
	cs := log.take(t)
	if f, ok := lastOf(cs, CtrlFPS); !ok || f.FPS != 30 {
		t.Fatalf("resync sent %v", cs)
	}
	if _, ok := lastOf(cs, CtrlScale); ok {
		t.Fatalf("resync halved the picture: %v", cs)
	}

	// 手选 60、码率低：新子进程默认就是 60 帧，减半要再说一遍
	c.bitrate = rateFloor
	c.applyFPS(rateFloor)
	c.pin(60)
	log.take(t)
	c.resync()
	cs = log.take(t)
	if f, ok := lastOf(cs, CtrlFPS); ok && f.FPS != 60 {
		t.Fatalf("resync sent fps %d", f.FPS)
	}
	if s, ok := lastOf(cs, CtrlScale); !ok || s.Scale != 2 {
		t.Fatalf("resync lost the half picture: %v", cs)
	}
}

// Serve 带着手选值开始，第一帧配置后就告诉子进程；观看中经 FPSChanges 改，换了新画面（切屏）手选值不丢
func TestServePinsFPS(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	toMobile, toMac := TestKeys()
	framesR, framesW := io.Pipe()
	log := &controlLog{}
	changes := make(chan uint32)
	done := make(chan error, 1)
	go func() {
		done <- Serve(framesR, log, CastConfig{Conn: conn, ToMobile: toMobile, ToMac: toMac, FPS: 15, FPSChanges: changes})
	}()
	var seen []Control
	waitFPS := func(want uint32) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			seen = append(seen, log.take(t)...)
			if f, ok := lastOf(seen, CtrlFPS); ok && f.FPS == want {
				seen = nil
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("no fps %d in %v", want, seen)
	}
	WriteFrame(framesW, Frame{Kind: KindConfig, Payload: MarshalConfig(Config{Codec: CodecHEVC, Width: 1920, Height: 1080})})
	waitFPS(15)
	changes <- 30
	waitFPS(30)
	// 切到另一块屏：新画面按满码率起步，手选的 30 帧照旧，不发别的帧率
	WriteFrame(framesW, Frame{Kind: KindConfig, Payload: MarshalConfig(Config{Codec: CodecHEVC, Width: 2560, Height: 1440})})
	time.Sleep(100 * time.Millisecond)
	if f, ok := lastOf(log.take(t), CtrlFPS); ok {
		t.Fatalf("new picture changed the pinned fps to %d", f.FPS)
	}
	changes <- 0
	waitFPS(60)
	framesW.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return")
	}
}
