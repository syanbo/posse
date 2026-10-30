package app

import (
	"image"
	"runtime"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/media"
)

const (
	// animBudget 是一张动图的帧全部留着的上限，超过就边放边解、只留两帧
	animBudget = 32 << 20
	// animMaxPixels 是动图每帧最多解出的像素，再大的等比缩小
	animMaxPixels = 4 << 20
	// animLag 是换帧落后多久就不追了，从现在接着放：标签切走过、窗口藏起来过、解码跟不上
	animLag = 250 * time.Millisecond
	// animRetry 是下一帧还没解好时隔多久再看
	animRetry = 10 * time.Millisecond
)

// animPlayer 放一张动图。帧在后台协程里解，画的时候按时间换帧（step），用 Painter.After 约下一次：
// 只重画不重建视图，元素不在视野里（标签切走、窗口藏起来）就不画、不约，也就不放了。
type animPlayer struct {
	bm     *ui.Bitmap
	delays []time.Duration
	loops  int // 放几遍，0 是一直放
	dec    *animDecoder
	shown  int         // bm 上是第几帧
	cur    *image.RGBA // bm 用着的缓冲：边放边解时换下来给后台解下一帧
	due    time.Time   // shown 放到什么时候
	played int         // 放完了几遍
	paused bool
	ended  bool // 按循环次数放完了，停在最后一帧
}

// animDecoder 是后台解帧的协程和界面线程共用的部分；不引用 animPlayer，播放器没人用了由 cleanup 叫协程退出。
type animDecoder struct {
	mu     sync.Mutex
	frames []*image.RGBA // 全部留着时：解好的帧，没解到的为 nil
	stream bool
	next   *image.RGBA // 边放边解时：解好的下一帧
	nextI  int
	req    chan animReq
	quit   chan struct{}
}

type animReq struct {
	i   int
	buf *image.RGBA
}

// newAnimPlayer 解码动图的第一帧，后台接着解其余的；不是动图返回 nil。在后台协程里调用。
func newAnimPlayer(data []byte, maxW int) (*animPlayer, *media.Anim, error) {
	a, err := media.OpenAnim(data, maxW, animMaxPixels)
	if a == nil || err != nil {
		return nil, nil, err
	}
	first := image.NewRGBA(image.Rectangle{Max: a.Frame})
	if err := a.Decode(0, first); err != nil {
		a.Close()
		return nil, nil, err
	}
	n := len(a.Delays)
	d := &animDecoder{quit: make(chan struct{})}
	p := &animPlayer{bm: ui.NewBitmap(first), delays: a.Delays, loops: a.Loops, dec: d, cur: first}
	if a.Frame.X*a.Frame.Y*4*n <= animBudget {
		d.frames = make([]*image.RGBA, n)
		d.frames[0] = first
		go d.decodeAll(a)
	} else {
		d.stream, d.nextI = true, -1
		d.req = make(chan animReq, 1)
		d.req <- animReq{1, image.NewRGBA(image.Rectangle{Max: a.Frame})}
		go d.decodeOnDemand(a)
	}
	runtime.AddCleanup(p, func(d *animDecoder) { close(d.quit) }, d)
	return p, a, nil
}

// decodeAll 依次解出全部帧，解完就放掉 ImageIO 的解码器。
func (d *animDecoder) decodeAll(a *media.Anim) {
	defer a.Close()
	for i := 1; i < len(d.frames); i++ {
		select {
		case <-d.quit:
			return
		default:
		}
		f := image.NewRGBA(image.Rectangle{Max: a.Frame})
		if a.Decode(i, f) != nil {
			// 解不开的帧沿用上一帧
			d.mu.Lock()
			f = d.frames[i-1]
			d.mu.Unlock()
		}
		d.mu.Lock()
		d.frames[i] = f
		d.mu.Unlock()
	}
}

// decodeOnDemand 等界面要下一帧，解进它交回来的缓冲。
func (d *animDecoder) decodeOnDemand(a *media.Anim) {
	defer a.Close()
	for {
		select {
		case <-d.quit:
			return
		case r := <-d.req:
			// 解不开的帧把缓冲原样交回，画面停在上一帧的样子
			a.Decode(r.i, r.buf)
			d.mu.Lock()
			d.next, d.nextI = r.buf, r.i
			d.mu.Unlock()
		}
	}
}

// take 取第 i 帧，还没解好返回 nil。
func (d *animDecoder) take(i int) *image.RGBA {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.stream {
		return d.frames[i]
	}
	if d.nextI != i || d.next == nil {
		return nil
	}
	f := d.next
	d.next, d.nextI = nil, -1
	return f
}

// step 按时间换到该显示的那一帧，返回离下一次换帧还有多久；停着、放完了返回 0。在 Draw 里调用。
func (p *animPlayer) step(now time.Time) time.Duration {
	if p.paused || p.ended {
		return 0
	}
	if p.due.IsZero() || now.Sub(p.due) > animLag {
		p.due = now.Add(p.delays[p.shown])
	}
	n := len(p.delays)
	for !now.Before(p.due) {
		next := (p.shown + 1) % n
		if next == 0 && p.loops > 0 && p.played+1 >= p.loops {
			p.ended = true
			return 0
		}
		f := p.dec.take(next)
		if f == nil {
			return animRetry
		}
		// 落后好几帧时只是换指针，GPU 只传最后那帧
		p.bm.Update(f)
		if p.dec.stream {
			// 换下来的缓冲 bm 不再用了，交给后台解下一帧
			p.dec.req <- animReq{(next + 1) % n, p.cur}
		}
		p.cur, p.shown = f, next
		if next == 0 {
			p.played++
		}
		p.due = p.due.Add(p.delays[next])
	}
	return p.due.Sub(now)
}

// toggle 是播放、暂停；放完了的从头再放。
func (p *animPlayer) toggle() {
	switch {
	case p.ended:
		// 停在最后一帧，接着换回第 0 帧的那次不算放完一遍
		p.ended, p.played, p.due = false, -1, time.Time{}
	case p.paused:
		p.paused, p.due = false, time.Time{}
	default:
		p.paused = true
	}
}

func (p *animPlayer) playing() bool { return !p.paused && !p.ended }

// duration 是放一遍的时长。
func (p *animPlayer) duration() time.Duration {
	var t time.Duration
	for _, d := range p.delays {
		t += d
	}
	return t
}

// animate 在 Draw 里换帧并约下一次重画。
func animate(p *ui.Painter, a *animPlayer) {
	if a == nil {
		return
	}
	if wait := a.step(p.Now()); wait > 0 {
		p.After(wait)
	}
}
