//go:build darwin

package desk

import (
	"encoding/binary"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// synthEngine 是只开编码器、不采屏的引擎，画面由测试喂（不要屏幕录制授权）。
func synthEngine(t *testing.T, fps, bitrate uint32) *engine {
	t.Helper()
	if testing.Short() {
		t.Skip("encodes for seconds")
	}
	mac, err := loadMac()
	if err != nil {
		t.Skip(err)
	}
	if err := registerDelegate(mac); err != nil {
		t.Fatal(err)
	}
	e := &engine{mac: mac, opt: Options{Log: io.Discard}, out: io.Discard, pend: map[uint64]*stamp{},
		stop: make(chan struct{}), wake: make(chan struct{}, 1), nalLen: 4, born: time.Now(), wantLow: true}
	e.got = make(chan Frame, 4096)
	e.width, e.height = 1280, 720
	e.fpsCap.Store(fps)
	e.bitrate.Store(bitrate)
	e.bitrateSet.Store(true)
	active = e
	t.Cleanup(func() {
		if active == e {
			active = nil
		}
	})
	if err := e.openEncoder(CodecHEVC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if e.enc != 0 {
			e.mac.vtInvalidate(e.enc)
			e.mac.cfRelease(e.enc)
		}
	})
	return e
}

// screenLike 是像屏幕的一帧：大块不变，顶上一条每帧换内容（滚动的文字之类）。
type screenLike struct {
	buf          uintptr
	lock, unlock func(uintptr, uintptr) int32
	plane        func(uintptr, uintptr) unsafe.Pointer
	stride       func(uintptr, uintptr) uintptr
	release      func(uintptr)
	w, h         int
}

func newScreenLike(t *testing.T, w, h int) *screenLike {
	cv, err := purego.Dlopen("/System/Library/Frameworks/CoreVideo.framework/CoreVideo", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		t.Fatal(err)
	}
	s := &screenLike{w: w, h: h,
		lock:   mustFn[func(uintptr, uintptr) int32](cv, "CVPixelBufferLockBaseAddress"),
		unlock: mustFn[func(uintptr, uintptr) int32](cv, "CVPixelBufferUnlockBaseAddress"),
		plane:  mustFn[func(uintptr, uintptr) unsafe.Pointer](cv, "CVPixelBufferGetBaseAddressOfPlane"),
		stride: mustFn[func(uintptr, uintptr) uintptr](cv, "CVPixelBufferGetBytesPerRowOfPlane"),
	}
	create := mustFn[func(uintptr, uintptr, uintptr, uint32, uintptr, *uintptr) int32](cv, "CVPixelBufferCreate")
	if rc := create(0, uintptr(w), uintptr(h), pixel420v, 0, &s.buf); rc != 0 {
		t.Fatal(rc)
	}
	return s
}

func (s *screenLike) draw(seed uint64) {
	r := rand.New(rand.NewPCG(seed, 7))
	s.lock(s.buf, 0)
	for p, rows := range []int{s.h, s.h / 2} {
		base, st := s.plane(s.buf, uintptr(p)), int(s.stride(s.buf, uintptr(p)))
		for row := range rows {
			line := unsafe.Slice((*byte)(unsafe.Add(base, row*st)), s.w)
			for i := range line {
				if row < rows/30 {
					line[i] = byte(r.UintN(256))
				} else {
					line[i] = 128
				}
			}
		}
	}
	s.unlock(s.buf, 0)
}

// feed 按 fps 的真实间隔编 n 帧，返回编出来的字节数。
func feed(e *engine, s *screenLike, from, n int, fps uint32) int {
	for i := range n {
		s.draw(uint64(from + i))
		e.encode(s.buf, e.enc)
		time.Sleep(time.Second / time.Duration(fps))
	}
	e.mac.vtComplete(e.enc, cmTime{})
	time.Sleep(200 * time.Millisecond)
	total := 0
	for len(e.got) > 0 {
		total += len((<-e.got).Payload)
	}
	return total
}

// 帧率上限降到 10 时实际码率还在目标附近：时间戳按 60 帧标的话只有目标的 1/4 到 3/5。
func TestEncoderHoldsRateAtLowFPS(t *testing.T) {
	for _, target := range []uint32{rateFloor, 2_000_000} {
		e := synthEngine(t, 10, target)
		s := newScreenLike(t, e.width, e.height)
		total := feed(e, s, 0, 40, 10)
		got := float64(total*8) / 4 / float64(target)
		t.Logf("target %d bps at 10 fps: %.2f of target", target, got)
		if got < 0.8 || got > 1.2 {
			t.Errorf("target %d bps at 10 fps: actual %.2f of target", target, got)
		}
	}
}

// 同一张图紧挨着再编（refine 补编）、中途重开编码器（改尺寸、切屏）：时间戳接着往后走，照样出帧。
func TestEncoderTimelineAcrossReopen(t *testing.T) {
	e := synthEngine(t, 10, 2_000_000)
	s := newScreenLike(t, e.width, e.height)
	s.draw(1)
	e.encode(s.buf, e.enc)
	first := e.lastPTS
	e.encode(s.buf, e.enc)
	if e.lastPTS <= first {
		t.Fatalf("pts went from %d to %d", first, e.lastPTS)
	}
	feed(e, s, 2, 5, 10)
	before := e.lastPTS
	old := e.enc
	if err := e.openEncoder(CodecHEVC); err != nil {
		t.Fatal(err)
	}
	e.mac.vtInvalidate(old)
	e.mac.cfRelease(old)
	if n := feed(e, s, 10, 10, 10); n == 0 {
		t.Fatal("no frames after reopening the encoder")
	}
	if e.lastPTS <= before {
		t.Fatalf("pts went back after reopening: %d then %d", before, e.lastPTS)
	}
}

// 录下的 mov 时长和真实时长一样（编 20 帧、每帧 100ms，约 2 秒）。
func TestMovieDuration(t *testing.T) {
	e := synthEngine(t, 10, 2_000_000)
	e.opt.Mov = filepath.Join(t.TempDir(), "out.mov")
	if err := e.openMovie(); err != nil {
		t.Fatal(err)
	}
	s := newScreenLike(t, e.width, e.height)
	feed(e, s, 0, 20, 10)
	e.mov.finish(e.mac)
	b, err := os.ReadFile(e.opt.Mov)
	if err != nil {
		t.Fatal(err)
	}
	secs, ok := mvhdSeconds(b)
	if !ok {
		t.Fatal("no mvhd")
	}
	if secs < 1.8 || secs > 2.3 {
		t.Fatalf("movie lasts %.2fs, want about 2s", secs)
	}
}

// mvhdSeconds 读 QuickTime 文件 moov/mvhd 里的时长。
func mvhdSeconds(b []byte) (float64, bool) {
	for i := 0; i+4 <= len(b); {
		if i+8 > len(b) {
			return 0, false
		}
		size, typ := int(binary.BigEndian.Uint32(b[i:])), string(b[i+4:i+8])
		if size < 8 || i+size > len(b) {
			return 0, false
		}
		if typ == "moov" {
			return mvhdSeconds(b[i+8 : i+size])
		}
		if typ == "mvhd" {
			body := b[i+8 : i+size]
			if body[0] == 1 {
				return float64(binary.BigEndian.Uint64(body[24:])) / float64(binary.BigEndian.Uint32(body[20:])), true
			}
			return float64(binary.BigEndian.Uint32(body[16:])) / float64(binary.BigEndian.Uint32(body[12:])), true
		}
		i += size
	}
	return 0, false
}
