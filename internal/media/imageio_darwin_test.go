//go:build darwin

package media

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/gif"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// pngChunks 拆出 PNG 的各个 chunk（类型、内容），不含文件头。
func pngChunks(t *testing.T, b []byte) [][2][]byte {
	t.Helper()
	var out [][2][]byte
	for i := 8; i+8 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[i:]))
		out = append(out, [2][]byte{b[i+4 : i+8], b[i+8 : i+8+n]})
		i += 12 + n
	}
	return out
}

func appendChunk(b []byte, typ string, data []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	start := len(b)
	b = append(b, typ...)
	b = append(b, data...)
	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b[start:]))
}

func solid(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// makeAPNG 拼一张 APNG：每帧整张覆盖，delays 以百分之一秒计，loops 为 0 一直放。
func makeAPNG(t *testing.T, frames []*image.RGBA, delays []uint16, loops uint32) []byte {
	t.Helper()
	w, h := frames[0].Rect.Dx(), frames[0].Rect.Dy()
	out := []byte("\x89PNG\r\n\x1a\n")
	seq := uint32(0)
	for i, f := range frames {
		var buf bytes.Buffer
		if err := png.Encode(&buf, f); err != nil {
			t.Fatal(err)
		}
		var idat []byte
		for _, c := range pngChunks(t, buf.Bytes()) {
			switch string(c[0]) {
			case "IHDR":
				if i == 0 {
					out = appendChunk(out, "IHDR", c[1])
					actl := binary.BigEndian.AppendUint32(nil, uint32(len(frames)))
					out = appendChunk(out, "acTL", binary.BigEndian.AppendUint32(actl, loops))
				}
			case "IDAT":
				idat = append(idat, c[1]...)
			}
		}
		fctl := binary.BigEndian.AppendUint32(nil, seq)
		seq++
		fctl = binary.BigEndian.AppendUint32(fctl, uint32(w))
		fctl = binary.BigEndian.AppendUint32(fctl, uint32(h))
		fctl = binary.BigEndian.AppendUint32(fctl, 0)
		fctl = binary.BigEndian.AppendUint32(fctl, 0)
		fctl = binary.BigEndian.AppendUint16(fctl, delays[i])
		fctl = binary.BigEndian.AppendUint16(fctl, 100)
		fctl = append(fctl, 0, 0) // dispose none, blend source
		out = appendChunk(out, "fcTL", fctl)
		if i == 0 {
			out = appendChunk(out, "IDAT", idat)
			continue
		}
		fdat := binary.BigEndian.AppendUint32(nil, seq)
		seq++
		out = appendChunk(out, "fdAT", append(fdat, idat...))
	}
	return appendChunk(out, "IEND", nil)
}

func makeGIF(t *testing.T, frames []*image.RGBA, delays []int, loops int) []byte {
	t.Helper()
	g := &gif.GIF{Delay: delays, LoopCount: loops}
	for _, f := range frames {
		p := image.NewPaletted(f.Rect, palette.Plan9)
		for i := 0; i < len(f.Pix); i += 4 {
			p.Pix[i/4] = uint8(p.Palette.Index(color.RGBA{f.Pix[i], f.Pix[i+1], f.Pix[i+2], 255}))
		}
		g.Image = append(g.Image, p)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

var (
	red   = color.RGBA{255, 0, 0, 255}
	green = color.RGBA{0, 255, 0, 255}
	blue  = color.RGBA{0, 0, 255, 255}
)

func checkFrames(t *testing.T, a *Anim, want []color.RGBA) {
	t.Helper()
	dst := image.NewRGBA(image.Rectangle{Max: a.Frame})
	for i, c := range want {
		if err := a.Decode(i, dst); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		got := dst.RGBAAt(a.Frame.X/2, a.Frame.Y/2)
		if absDiff(got.R, c.R) > 8 || absDiff(got.G, c.G) > 8 || absDiff(got.B, c.B) > 8 || got.A != 255 {
			t.Fatalf("frame %d 中心是 %v，应为 %v", i, got, c)
		}
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

func TestOpenAnimAPNG(t *testing.T) {
	frames := []*image.RGBA{solid(40, 20, red), solid(40, 20, green), solid(40, 20, blue)}
	a, err := OpenAnim(makeAPNG(t, frames, []uint16{5, 20, 0}, 2), 0, 0)
	if err != nil || a == nil {
		t.Fatalf("OpenAnim: %v %v", a, err)
	}
	defer a.Close()
	if a.Format != "png" || a.Size != image.Pt(40, 20) || a.Frame != a.Size || a.Loops != 2 {
		t.Fatalf("%+v", a)
	}
	// 0 时长照浏览器当成 100ms
	want := []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 100 * time.Millisecond}
	for i, d := range want {
		if a.Delays[i] != d {
			t.Fatalf("delays %v, want %v", a.Delays, want)
		}
	}
	checkFrames(t, a, []color.RGBA{red, green, blue})
}

func TestOpenAnimGIFScaled(t *testing.T) {
	frames := []*image.RGBA{solid(400, 200, red), solid(400, 200, blue)}
	// GIF 的 LoopCount 0 是一直放
	a, err := OpenAnim(makeGIF(t, frames, []int{10, 10}, 0), 100, 0)
	if err != nil || a == nil {
		t.Fatalf("OpenAnim: %v %v", a, err)
	}
	defer a.Close()
	if a.Format != "gif" || a.Size != image.Pt(400, 200) || a.Frame != image.Pt(100, 50) || a.Loops != 0 {
		t.Fatalf("%+v", a)
	}
	checkFrames(t, a, []color.RGBA{red, blue})
	if fitSize(4000, 4000, 0, 1<<20) != image.Pt(1024, 1024) {
		t.Fatal(fitSize(4000, 4000, 0, 1<<20))
	}
}

func TestOpenAnimStill(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, solid(10, 10, red))
	if a, err := OpenAnim(buf.Bytes(), 0, 0); a != nil || err != nil {
		t.Fatalf("静态图不是动图：%v %v", a, err)
	}
	if a, err := OpenAnim(makeGIF(t, []*image.RGBA{solid(8, 8, red)}, []int{10}, 0), 0, 0); a != nil || err != nil {
		t.Fatalf("一帧的 GIF 不是动图：%v %v", a, err)
	}
}

func TestOpenAnimWebP(t *testing.T) {
	tool, err := exec.LookPath("gif2webp")
	if err != nil {
		t.Skip("没装 gif2webp")
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "a.gif"), filepath.Join(dir, "a.webp")
	os.WriteFile(in, makeGIF(t, []*image.RGBA{solid(30, 30, red), solid(30, 30, green)}, []int{30, 30}, 0), 0o600)
	if b, err := exec.Command(tool, "-quiet", in, "-o", out).CombinedOutput(); err != nil {
		t.Fatalf("gif2webp: %v %s", err, b)
	}
	b, _ := os.ReadFile(out)
	a, err := OpenAnim(b, 0, 0)
	if err != nil || a == nil {
		t.Fatalf("OpenAnim: %v %v", a, err)
	}
	defer a.Close()
	if a.Format != "webp" || len(a.Delays) != 2 || a.Delays[0] != 300*time.Millisecond {
		t.Fatalf("%+v", a)
	}
	checkFrames(t, a, []color.RGBA{red, green})
}

// RGBA 的 PNG 带 PLTE 和 tRNS：规范不允许 tRNS，Go 拒绝，浏览器和 ImageIO 忽略它照常显示。
func TestDecodeRGBAWithTRNS(t *testing.T) {
	var buf bytes.Buffer
	png.Encode(&buf, solid(12, 6, color.RGBA{0, 0, 255, 255}))
	out := []byte("\x89PNG\r\n\x1a\n")
	for _, c := range pngChunks(t, buf.Bytes()) {
		out = appendChunk(out, string(c[0]), c[1])
		if string(c[0]) == "IHDR" {
			out = appendChunk(out, "PLTE", []byte{0, 0, 0, 255, 255, 255})
			out = appendChunk(out, "tRNS", []byte{0, 255})
		}
	}
	if _, err := png.Decode(bytes.NewReader(out)); err == nil {
		t.Fatal("Go 应该解不开")
	}
	pi, err := DecodePreviewBytes(out, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if pi.Format != "png" || pi.Size != image.Pt(12, 6) || pi.Img.RGBAAt(6, 3) != (color.RGBA{0, 0, 255, 255}) {
		t.Fatalf("%+v %v", pi.Size, pi.Img.RGBAAt(6, 3))
	}
	img, size, err := ScaledImageBytes(out, 6)
	if err != nil || size != image.Pt(12, 6) || img.Rect.Dx() != 6 {
		t.Fatal(err, size)
	}
}
