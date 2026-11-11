//go:build darwin

package desk

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
)

// TestDeskBench 是看桌面的台架：正式的 caster（码率控制、帧率阶梯、降分辨率）加正式的编码器，
// 画面是合成的（不要屏幕录制授权），收包和解码交给 tools/desksession，中间可以接 tools/deskshape。
// 只在设了 POSSE_DESK_BENCH（UDP 监听地址）时跑：
//
//	POSSE_DESK_BENCH=127.0.0.1:48991 POSSE_DESK_BENCH_SECONDS=85 POSSE_DESK_BENCH_OUT=frames.jsonl \
//	    go test ./internal/desk -run TestDeskBench -v -timeout 0
//	go run ./tools/deskshape --listen 127.0.0.1:48981 --forward 127.0.0.1:48991 \
//	    --rate 1Mbps --loss 0.02 --delay 40ms --queue 100ms --seed 9
//	/tmp/desksession --port 48981 --seconds 86   # 编法见 tools/desksession/main.swift
//
// 发送端每次评估码率记一行（rate hold/up/down、排队、码率），frames.jsonl 是每个编出来的帧。
// 画面：深色底上 POSSE_DESK_BENCH_ROWS 行（默认 2）彩色字符，按 POSSE_DESK_BENCH_HZ（默认 30）换内容，
// 像终端里滚动的输出；内容由 POSSE_DESK_BENCH_SEED 定，同一种子每次一样。采集按 min(帧率上限, 换内容的频率) 交帧。
func TestDeskBench(t *testing.T) {
	addr := os.Getenv("POSSE_DESK_BENCH")
	if addr == "" {
		t.Skip("POSSE_DESK_BENCH not set")
	}
	seconds := benchEnv("POSSE_DESK_BENCH_SECONDS", 60)
	rows := benchEnv("POSSE_DESK_BENCH_ROWS", 2)
	hz := benchEnv("POSSE_DESK_BENCH_HZ", 30)
	seed := uint64(benchEnv("POSSE_DESK_BENCH_SEED", 9))
	mac, err := loadMac()
	if err != nil {
		t.Skip(err)
	}
	if err := registerDelegate(mac); err != nil {
		t.Fatal(err)
	}
	const fullW, fullH = 1920, 1204
	ctlR, ctlW := io.Pipe()
	frameR, frameW := io.Pipe()
	e := &engine{mac: mac, opt: Options{Log: os.Stderr, Control: ctlR}, out: frameW, pend: map[uint64]*stamp{},
		stop: make(chan struct{}), wake: make(chan struct{}, 1), nalLen: 4, born: time.Now(), wantLow: os.Getenv("POSSE_DESK_BENCH_NOLOW") == ""}
	e.width, e.height = fullW, fullH
	active = e
	defer func() { active = nil }()
	if err := e.openEncoder(CodecHEVC); err != nil {
		t.Fatal(err)
	}
	var picMu sync.Mutex
	pic := newBenchPicture(t, fullW, fullH)
	config := func() {
		e.write(Frame{Kind: KindConfig, CaptureUnixNano: time.Now().UnixNano(),
			Payload: MarshalConfig(Config{Codec: CodecHEVC, Width: e.width, Height: e.height, FullWidth: fullW, FullHeight: fullH})})
	}

	// 子进程那边对控制的反应：码率、关键帧照正式代码；帧率上限只改编码器的预期帧率（采集节奏在下面）；
	// 降分辨率重开编码器、换画面尺寸
	go func() {
		for {
			c, err := ReadControl(ctlR)
			if err != nil {
				return
			}
			switch c.Kind {
			case CtrlKeyframe:
				e.force.Store(true)
			case CtrlBitrate:
				e.bitrate.Store(c.Bitrate)
				e.bitrateSet.Store(true)
			case CtrlFPS:
				if c.FPS == 0 || c.FPS > 60 {
					continue
				}
				e.fpsCap.Store(c.FPS)
				e.encMu.Lock()
				if e.enc != 0 {
					e.mac.withPool(func() {
						n := e.mac.numLong(e.mac.clsNumber, e.mac.sel("numberWithLongLong:"), int64(c.FPS))
						e.mac.vtSet(e.enc, e.mac.keyFPS, uintptr(n))
					})
				}
				e.encMu.Unlock()
				e.logf("frame rate cap %d", c.FPS)
			case CtrlScale:
				if c.Scale == 0 || c.Scale > 4 || e.scale.Swap(c.Scale) == c.Scale {
					continue
				}
				w, h := fullW/int(c.Scale)&^1, fullH/int(c.Scale)&^1
				picMu.Lock()
				e.encodeMu.Lock()
				err := e.resizeLocked(w, h)
				e.encodeMu.Unlock()
				pic = newBenchPicture(t, w, h)
				picMu.Unlock()
				if err != nil {
					t.Error(err)
					return
				}
				e.logf("picture scale 1/%d", c.Scale)
				config()
			}
		}
	}()

	// 采到的帧原样转给 caster，视频帧另记一行 JSON（和正式子进程交给常驻进程的一样）
	castR, castW := io.Pipe()
	var out io.Writer = io.Discard
	if p := os.Getenv("POSSE_DESK_BENCH_OUT"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		out = f
	}
	start := time.Now()
	go func() {
		defer castW.Close()
		enc := json.NewEncoder(out)
		for {
			f, err := ReadFrame(frameR)
			if err != nil {
				return
			}
			if f.Kind == KindVideo {
				enc.Encode(map[string]any{"t": time.Since(start).Seconds(), "n": f.Number, "capture": f.CaptureUnixNano,
					"bytes": len(f.Payload), "key": f.Keyframe, "fps": e.fpsCap.Load(), "w": e.width})
			}
			if WriteFrame(castW, f) != nil {
				return
			}
		}
	}()

	// 管道没有缓冲：转发的协程起来以后才能写
	config()
	go func() {
		next := time.Now()
		var shown uint64
		beat := time.Now()
		for time.Since(start) < time.Duration(seconds)*time.Second {
			cap := int(e.fpsCap.Load())
			if cap == 0 {
				cap = 60
			}
			next = next.Add(time.Second / time.Duration(min(cap, hz)))
			time.Sleep(time.Until(next))
			// 内容按 hz 换：第几张由时间定，和帧率上限无关
			frame := uint64(time.Since(start) * time.Duration(hz) / time.Second)
			picMu.Lock()
			if frame != shown {
				pic.draw(seed*1_000_003+frame, rows)
				shown = frame
			}
			session := e.holdEncoder()
			if session != 0 {
				e.encode(pic.buf, session)
				e.mac.cfRelease(session)
			}
			picMu.Unlock()
			if time.Since(beat) >= time.Second {
				beat = time.Now()
				e.write(Frame{Kind: KindHeartbeat, CaptureUnixNano: time.Now().UnixNano()})
			}
		}
		e.stopping.Store(true)
		frameW.Close()
	}()

	conn, err := net.ListenPacket("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	toMobile, toMac := TestKeys()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	err = Serve(castR, ctlW, CastConfig{Conn: conn, ToMobile: toMobile, ToMac: toMac, Verbose: true})
	log.Printf("bench: serve ended after %s: %v", time.Since(start).Round(time.Millisecond), err)
	ctlW.Close()
	e.encMu.Lock()
	if e.enc != 0 {
		e.mac.vtComplete(e.enc, cmTime{})
		e.mac.vtInvalidate(e.enc)
		e.mac.cfRelease(e.enc)
		e.enc = 0
	}
	e.encMu.Unlock()
}

func benchEnv(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// benchPicture 是台架的一帧画面（420v 像素缓冲）。
type benchPicture struct {
	buf          uintptr
	lock, unlock func(uintptr, uintptr) int32
	plane        func(uintptr, uintptr) unsafe.Pointer
	stride       func(uintptr, uintptr) uintptr
	w, h         int
}

func newBenchPicture(t *testing.T, w, h int) *benchPicture {
	cv, err := purego.Dlopen("/System/Library/Frameworks/CoreVideo.framework/CoreVideo", purego.RTLD_GLOBAL|purego.RTLD_NOW)
	if err != nil {
		t.Fatal(err)
	}
	p := &benchPicture{w: w, h: h,
		lock:   mustFn[func(uintptr, uintptr) int32](cv, "CVPixelBufferLockBaseAddress"),
		unlock: mustFn[func(uintptr, uintptr) int32](cv, "CVPixelBufferUnlockBaseAddress"),
		plane:  mustFn[func(uintptr, uintptr) unsafe.Pointer](cv, "CVPixelBufferGetBaseAddressOfPlane"),
		stride: mustFn[func(uintptr, uintptr) uintptr](cv, "CVPixelBufferGetBytesPerRowOfPlane"),
	}
	create := mustFn[func(uintptr, uintptr, uintptr, uint32, uintptr, *uintptr) int32](cv, "CVPixelBufferCreate")
	if rc := create(0, uintptr(w), uintptr(h), pixel420v, 0, &p.buf); rc != 0 {
		t.Fatal(fmt.Errorf("CVPixelBufferCreate %d", rc))
	}
	return p
}

// draw 画一屏：深色底，顶部往下 rows 行彩色字符（字形由字符号定，颜色随机），字符大小跟画面宽度走。
func (p *benchPicture) draw(seed uint64, rows int) {
	r := rand.New(rand.NewPCG(seed, 7))
	p.lock(p.buf, 0)
	defer p.unlock(p.buf, 0)
	yb, yst := p.plane(p.buf, 0), int(p.stride(p.buf, 0))
	cb, cst := p.plane(p.buf, 1), int(p.stride(p.buf, 1))
	for y := range p.h {
		line := unsafe.Slice((*byte)(unsafe.Add(yb, y*yst)), p.w)
		for x := range line {
			line[x] = 30
		}
	}
	for y := range p.h / 2 {
		line := unsafe.Slice((*byte)(unsafe.Add(cb, y*cst)), p.w)
		for x := range line {
			line[x] = 128
		}
	}
	cw := max(p.w/120, 2) &^ 1
	ch := cw * 2
	top := p.h / 12
	for row := range rows {
		for col := range p.w / cw {
			c := 1 + r.IntN(60)
			lum := byte(120 + r.IntN(130))
			u, v := byte(r.IntN(256)), byte(r.IntN(256))
			for y := range ch {
				py := top + row*ch + y
				if py >= p.h {
					break
				}
				line := unsafe.Slice((*byte)(unsafe.Add(yb, py*yst)), p.w)
				for x := range cw {
					gx, gy := x*16/cw, y*32/ch
					if (c*7+gx*3+gy*5+(gx*gy)%c)%4 == 0 {
						line[col*cw+x] = lum
					}
				}
				if y%2 == 0 {
					cl := unsafe.Slice((*byte)(unsafe.Add(cb, (py/2)*cst)), p.w)
					for x := 0; x < cw; x += 2 {
						cl[col*cw+x], cl[col*cw+x+1] = u, v
					}
				}
			}
		}
	}
}

// TestDeskQuality 量编码器在固定帧率上限和码率下的画质，不经网络：同一段合成画面编 POSSE_DESK_QUALITY_SECONDS 秒
// （默认 8），录成 mov，另把字符那一条（会变的部分）的原图写成灰度裸数据，给 ffmpeg 的 ssim、psnr 比：
//
//	own.gray   每个编出来的帧对应的原图（和 mov 里的帧一一对应）
//	truth.gray 按 30Hz 显示时钟每一拍那一刻的原图；seen.json 是每一拍屏幕上显示的是第几个编出来的帧（丢了的帧显示上一帧）
//	frames.json 每个编出来的帧的字节数、是不是关键帧
//
// 只在设了 POSSE_DESK_QUALITY（输出目录）时跑；POSSE_DESK_QUALITY_FPS、_RATE、_SCALE 定帧率上限、码率、长边除数，
// POSSE_DESK_BENCH_NOLOW=1 关掉低延迟码率控制，画面参数和 TestDeskBench 一样。
func TestDeskQuality(t *testing.T) {
	dir := os.Getenv("POSSE_DESK_QUALITY")
	if dir == "" {
		t.Skip("POSSE_DESK_QUALITY not set")
	}
	seconds := benchEnv("POSSE_DESK_QUALITY_SECONDS", 8)
	fps := benchEnv("POSSE_DESK_QUALITY_FPS", 15)
	rate := benchEnv("POSSE_DESK_QUALITY_RATE", 800_000)
	scale := benchEnv("POSSE_DESK_QUALITY_SCALE", 1)
	rows := benchEnv("POSSE_DESK_BENCH_ROWS", 2)
	hz := benchEnv("POSSE_DESK_BENCH_HZ", 30)
	seed := uint64(benchEnv("POSSE_DESK_BENCH_SEED", 9))
	mac, err := loadMac()
	if err != nil {
		t.Skip(err)
	}
	if err := registerDelegate(mac); err != nil {
		t.Fatal(err)
	}
	frameR, frameW := io.Pipe()
	e := &engine{mac: mac, opt: Options{Log: io.Discard, Mov: dir + "/out.mov"}, out: frameW, pend: map[uint64]*stamp{},
		stop: make(chan struct{}), wake: make(chan struct{}, 1), nalLen: 4, born: time.Now(), wantLow: os.Getenv("POSSE_DESK_BENCH_NOLOW") == ""}
	e.width, e.height = 1920/scale&^1, 1204/scale&^1
	e.fpsCap.Store(uint32(fps))
	e.bitrate.Store(uint32(rate))
	e.bitrateSet.Store(true)
	active = e
	defer func() { active = nil }()
	if err := e.openEncoder(CodecHEVC); err != nil {
		t.Fatal(err)
	}
	if err := e.openMovie(); err != nil {
		t.Fatal(err)
	}
	type output struct {
		N     uint64 `json:"n"`
		Bytes int    `json:"bytes"`
		Key   bool   `json:"key"`
	}
	var outputs []output
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			f, err := ReadFrame(frameR)
			if err != nil {
				return
			}
			if f.Kind == KindVideo {
				outputs = append(outputs, output{f.Number, len(f.Payload), f.Keyframe})
			}
		}
	}()
	pic := newBenchPicture(t, e.width, e.height)
	type input struct {
		idx uint64
		pts int64
	}
	inputs := map[uint64]input{}
	start := time.Now()
	next := start
	var shown uint64 = ^uint64(0)
	for time.Since(start) < time.Duration(seconds)*time.Second {
		next = next.Add(time.Second / time.Duration(min(fps, hz)))
		time.Sleep(time.Until(next))
		idx := uint64(time.Since(start) * time.Duration(hz) / time.Second)
		if idx != shown {
			pic.draw(seed*1_000_003+idx, rows)
			shown = idx
		}
		e.encode(pic.buf, e.enc)
		inputs[atomic.LoadUint64(&e.frameNumber)] = input{idx, e.lastPTS}
	}
	e.mac.vtComplete(e.enc, cmTime{})
	time.Sleep(300 * time.Millisecond)
	e.mov.finish(e.mac)
	frameW.Close()
	<-done
	if len(outputs) == 0 {
		t.Fatal("no frames")
	}
	// 字符那一条，上下各留一点
	cw := max(e.width/120, 2) &^ 1
	y0 := (e.height/12 - cw) &^ 1
	bandH := (rows*cw*2 + 2*cw) &^ 1
	crop := func(idx uint64) []byte {
		pic.draw(seed*1_000_003+idx, rows)
		pic.lock(pic.buf, 0)
		defer pic.unlock(pic.buf, 0)
		yb, yst := pic.plane(pic.buf, 0), int(pic.stride(pic.buf, 0))
		b := make([]byte, 0, e.width*bandH)
		for y := y0; y < y0+bandH; y++ {
			b = append(b, unsafe.Slice((*byte)(unsafe.Add(yb, y*yst)), e.width)...)
		}
		return b
	}
	own, err := os.Create(dir + "/own.gray")
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	for _, o := range outputs {
		own.Write(crop(inputs[o.N].idx))
	}
	// 显示时钟从第一个编出来的帧开始，每 1/30 秒一拍，到最后一帧为止
	truth, err := os.Create(dir + "/truth.gray")
	if err != nil {
		t.Fatal(err)
	}
	defer truth.Close()
	first, last := inputs[outputs[0].N].pts, inputs[outputs[len(outputs)-1].N].pts
	startOff := start.Sub(e.born).Nanoseconds()
	var seen []int
	cur := 0
	for at := first; at <= last; at += int64(time.Second) / 30 {
		for cur+1 < len(outputs) && inputs[outputs[cur+1].N].pts <= at {
			cur++
		}
		seen = append(seen, cur)
		truth.Write(crop(uint64((at - startOff) * int64(hz) / int64(time.Second))))
	}
	meta := map[string]any{"width": e.width, "height": e.height, "y0": y0, "bandH": bandH, "inputs": len(inputs),
		"seconds": seconds, "fps": fps, "rate": rate, "lowLatency": e.lowLatency, "frames": outputs, "seen": seen}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(dir+"/meta.json", b, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d inputs, %d outputs, %d display ticks", len(inputs), len(outputs), len(seen))
	e.mac.vtInvalidate(e.enc)
	e.mac.cfRelease(e.enc)
}
