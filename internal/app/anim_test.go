package app

import (
	"image"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func testPlayer(n int, loops int, stream bool) *animPlayer {
	frames := make([]*image.RGBA, n)
	delays := make([]time.Duration, n)
	for i := range frames {
		frames[i] = image.NewRGBA(image.Rect(0, 0, 2, 2))
		delays[i] = 100 * time.Millisecond
	}
	d := &animDecoder{frames: frames, stream: stream, nextI: -1, req: make(chan animReq, 1)}
	if stream {
		d.frames = nil
	}
	return &animPlayer{bm: ui.NewBitmap(frames[0]), delays: delays, loops: loops, dec: d, cur: frames[0]}
}

func TestAnimStep(t *testing.T) {
	p := testPlayer(3, 1, false)
	t0 := time.Unix(1000, 0)
	if w := p.step(t0); w != 100*time.Millisecond || p.shown != 0 {
		t.Fatal(w, p.shown)
	}
	if w := p.step(t0.Add(150 * time.Millisecond)); w != 50*time.Millisecond || p.shown != 1 {
		t.Fatal(w, p.shown)
	}
	if w := p.step(t0.Add(200 * time.Millisecond)); w != 100*time.Millisecond || p.shown != 2 {
		t.Fatal(w, p.shown)
	}
	// 只放一遍：停在最后一帧
	if w := p.step(t0.Add(300 * time.Millisecond)); w != 0 || !p.ended || p.shown != 2 {
		t.Fatal(w, p.ended, p.shown)
	}
	p.toggle()
	if w := p.step(t0.Add(time.Second)); w != 100*time.Millisecond || p.shown != 2 || p.ended {
		t.Fatal("重放", w, p.shown)
	}
	if p.step(t0.Add(1100 * time.Millisecond)); p.shown != 0 {
		t.Fatal(p.shown)
	}
}

func TestAnimStepLagAndPause(t *testing.T) {
	p := testPlayer(4, 0, false)
	t0 := time.Unix(1000, 0)
	p.step(t0)
	// 落后不多时追上：一次走两帧
	if p.step(t0.Add(210 * time.Millisecond)); p.shown != 2 {
		t.Fatal(p.shown)
	}
	// 很久没画（标签切走过）：从现在接着放，不追赶
	if w := p.step(t0.Add(time.Minute)); w != 100*time.Millisecond || p.shown != 2 {
		t.Fatal(w, p.shown)
	}
	p.toggle()
	if w := p.step(t0.Add(2 * time.Minute)); w != 0 || p.shown != 2 {
		t.Fatal("暂停", w, p.shown)
	}
	p.toggle()
	if w := p.step(t0.Add(3 * time.Minute)); w != 100*time.Millisecond || p.shown != 2 {
		t.Fatal("继续", w, p.shown)
	}
}

func TestAnimStepStream(t *testing.T) {
	p := testPlayer(3, 0, true)
	d := p.dec
	t0 := time.Unix(1000, 0)
	p.step(t0)
	// 下一帧还没解好：等一会儿再看，停在第 0 帧
	if w := p.step(t0.Add(100 * time.Millisecond)); w != animRetry || p.shown != 0 {
		t.Fatal(w, p.shown)
	}
	first, f1 := p.cur, image.NewRGBA(image.Rect(0, 0, 2, 2))
	d.next, d.nextI = f1, 1
	if p.step(t0.Add(110 * time.Millisecond)); p.shown != 1 || p.cur != f1 {
		t.Fatal(p.shown)
	}
	// 换下来的缓冲交给后台解第 2 帧
	if r := <-d.req; r.i != 2 || r.buf != first {
		t.Fatal(r.i)
	}
}
