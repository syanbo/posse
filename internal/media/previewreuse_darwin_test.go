package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/image/draw"
)

// previewWithoutReuse 是 DecodePreviewBytes 不走「直接用解出来的 RGBA」那条路时的写法（main 上的原样）。
func previewWithoutReuse(data []byte, maxPixels int) (PreviewImage, error) {
	src, format, err := decodeAny(data)
	if err != nil {
		return PreviewImage{}, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if n := w * h; n > maxPixels {
		k := math.Sqrt(float64(maxPixels) / float64(n))
		w, h = max(int(float64(w)*k), 1), max(int(float64(h)*k), 1)
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	if w == b.Dx() {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Src)
	} else {
		draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	}
	return PreviewImage{Img: dst, Size: b.Size(), Format: format}, nil
}

// 各种宽高比（含 1×N、N×1）、透明和不透明、各条解码路径（PNG、JPEG、GIF、WebP、Go 解不开交给 ImageIO 的），
// 各种像素上限（不缩、只缩高、两边都缩）：输出的尺寸、像素、格式都和不走复用那条路时一样。
func TestDecodePreviewMatchesWithoutReuse(t *testing.T) {
	pattern := func(w, h int, alpha bool) *image.NRGBA {
		img := image.NewNRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				a := uint8(255)
				if alpha {
					a = uint8(40 + (x*31+y*17)%200)
				}
				img.SetNRGBA(x, y, color.NRGBA{uint8(x*37 + y), uint8(y*11 + 3), uint8(x ^ y), a})
			}
		}
		return img
	}
	encPNG := func(img image.Image) []byte { var b bytes.Buffer; png.Encode(&b, img); return b.Bytes() }
	trns := func(img image.Image) []byte { // RGBA 的 PNG 加上 PLTE、tRNS：Go 拒绝，交给 ImageIO
		out := []byte("\x89PNG\r\n\x1a\n")
		for _, c := range pngChunks(t, encPNG(img)) {
			out = appendChunk(out, string(c[0]), c[1])
			if string(c[0]) == "IHDR" {
				out = appendChunk(out, "PLTE", []byte{0, 0, 0, 255, 255, 255})
				out = appendChunk(out, "tRNS", []byte{0, 255})
			}
		}
		return out
	}
	cwebp, _ := exec.LookPath("cwebp")
	encWebP := func(img image.Image) []byte {
		dir := t.TempDir()
		in, out := filepath.Join(dir, "a.png"), filepath.Join(dir, "a.webp")
		os.WriteFile(in, encPNG(img), 0o644)
		if b, err := exec.Command(cwebp, "-quiet", "-lossless", in, "-o", out).CombinedOutput(); err != nil {
			t.Fatalf("cwebp: %v %s", err, b)
		}
		b, _ := os.ReadFile(out)
		return b
	}
	sizes := [][2]int{{1, 100}, {100, 1}, {37, 21}, {8, 8}, {1, 1}, {64, 3}, {3, 64}}
	limits := []int{1 << 20, 50, 25, 7}
	checked := 0
	for _, sz := range sizes {
		for _, alpha := range []bool{false, true} {
			img := pattern(sz[0], sz[1], alpha)
			type enc struct {
				name string
				data []byte
			}
			encs := []enc{{"png", encPNG(img)}}
			if !alpha {
				var j bytes.Buffer
				jpeg.Encode(&j, img, &jpeg.Options{Quality: 90})
				encs = append(encs, enc{"jpeg", j.Bytes()}, enc{"imageio-png", trns(img)})
				opaque := image.NewRGBA(img.Bounds()) // 不透明的 RGBA：Go 的 PNG 解出来是 *image.RGBA，正好走复用那条路
				draw.Draw(opaque, opaque.Bounds(), img, image.Point{}, draw.Src)
				encs = append(encs, enc{"png-rgba", encPNG(opaque)})
			}
			var g bytes.Buffer
			gif.Encode(&g, img, nil)
			encs = append(encs, enc{"gif", g.Bytes()})
			if cwebp != "" {
				encs = append(encs, enc{"webp", encWebP(img)})
			}
			for _, e := range encs {
				for _, limit := range limits {
					name := fmt.Sprintf("%s %dx%d alpha=%v max=%d", e.name, sz[0], sz[1], alpha, limit)
					want, werr := previewWithoutReuse(e.data, limit)
					got, gerr := DecodePreviewBytes(e.data, limit)
					if fmt.Sprint(werr) != fmt.Sprint(gerr) {
						t.Fatalf("%s: err %v, want %v", name, gerr, werr)
					}
					if werr != nil {
						continue
					}
					if got.Img.Rect != want.Img.Rect || got.Size != want.Size || got.Format != want.Format || !bytes.Equal(got.Img.Pix, want.Img.Pix) {
						t.Fatalf("%s: got %v %v %s, want %v %v %s", name, got.Img.Rect, got.Size, got.Format, want.Img.Rect, want.Size, want.Format)
					}
					checked++
				}
			}
		}
	}
	if cwebp == "" {
		t.Log("cwebp not installed: WebP not covered")
	}
	t.Logf("%d cases match", checked)
}
