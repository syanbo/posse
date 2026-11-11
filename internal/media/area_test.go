package media

import (
	"bytes"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/png"
	"math"
	"math/rand/v2"
	"runtime"
	"testing"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"
)

// 缩放的几种尺寸：缩一点（不到 2 倍，文件标签最常见的）、不整除、只缩一边（1×N、N×1）、缩很多。
var areaCases = [][4]int{
	{100, 70, 70, 49}, {37, 23, 11, 7}, {1, 100, 1, 37}, {100, 1, 37, 1}, {64, 3, 20, 1}, {5, 5, 5, 5}, {1000, 10, 3, 1}, {7, 300, 2, 101},
}

// 纯色图缩完还是那个颜色（预乘之后的），不透明和半透明都一样。
func TestAreaScaleSolid(t *testing.T) {
	for _, c := range []color.NRGBA{{200, 100, 50, 255}, {0, 0, 0, 255}, {255, 255, 255, 255}, {17, 230, 99, 128}, {255, 0, 0, 1}, {9, 9, 9, 0}} {
		want := color.RGBAModel.Convert(c).(color.RGBA)
		for _, cs := range areaCases {
			src := image.NewNRGBA(image.Rect(0, 0, cs[0], cs[1]))
			draw.Draw(src, src.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
			out := areaScale(src, cs[2], cs[3])
			if out.Rect != image.Rect(0, 0, cs[2], cs[3]) {
				t.Fatalf("%v: size %v", cs, out.Rect)
			}
			for y := 0; y < cs[3]; y++ {
				for x := 0; x < cs[2]; x++ {
					if got := out.RGBAAt(x, y); !near(got, want, 1) {
						t.Fatalf("%v %v at %d,%d: %v, want %v", c, cs, x, y, got, want)
					}
				}
			}
		}
	}
}

// 水平、垂直渐变缩完：每个目标像素等于它盖住的源像素按面积的平均（误差不超过 1），单调、没有台阶状的条带。
func TestAreaScaleGradient(t *testing.T) {
	for _, cs := range [][4]int{{256, 4, 100, 2}, {255, 3, 170, 3}, {1000, 2, 357, 1}, {300, 1, 299, 1}} {
		sw, sh, w, h := cs[0], cs[1], cs[2], cs[3]
		val := func(i, n int) float64 { return float64(i) * 255 / float64(n-1) }
		for _, vertical := range []bool{false, true} {
			if vertical {
				sw, sh, w, h = cs[1], cs[0], cs[3], cs[2]
			} else {
				sw, sh, w, h = cs[0], cs[1], cs[2], cs[3]
			}
			src := image.NewRGBA(image.Rect(0, 0, sw, sh))
			for y := 0; y < sh; y++ {
				for x := 0; x < sw; x++ {
					v := uint8(math.Round(val(x, sw)))
					if vertical {
						v = uint8(math.Round(val(y, sh)))
					}
					src.SetRGBA(x, y, color.RGBA{v, v, v, 255})
				}
			}
			out := areaScale(src, w, h)
			n, m := sw, w
			if vertical {
				n, m = sh, h
			}
			prev := -1
			for i := 0; i < m; i++ {
				// 目标像素 i 盖住源 [i·n/m, (i+1)·n/m)
				lo, hi := float64(i)*float64(n)/float64(m), float64(i+1)*float64(n)/float64(m)
				var sum, wsum float64
				for s := int(lo); s < n && float64(s) < hi; s++ {
					ov := math.Min(hi, float64(s+1)) - math.Max(lo, float64(s))
					px := src.RGBAAt(s, 0)
					if vertical {
						px = src.RGBAAt(0, s)
					}
					sum += float64(px.R) * ov
					wsum += ov
				}
				x, y := i, 0
				if vertical {
					x, y = 0, i
				}
				got := out.RGBAAt(x, y)
				if math.Abs(float64(got.R)-sum/wsum) > 1 || got.R != got.G || got.G != got.B || got.A != 255 {
					t.Fatalf("%v vertical=%v at %d: %v, want %.1f", cs, vertical, i, got, sum/wsum)
				}
				if int(got.R) < prev {
					t.Fatalf("%v vertical=%v not monotonic at %d: %d after %d", cs, vertical, i, got.R, prev)
				}
				prev = int(got.R)
			}
		}
	}
}

// 透明和不透明的像素平均时按预乘算：红色不透明和全透明的蓝色并成半透明的红，不混出紫色。
func TestAreaScalePremultiplied(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{255, 0, 0, 255})
	src.SetNRGBA(1, 0, color.NRGBA{0, 0, 255, 0})
	if got := areaScale(src, 1, 1).RGBAAt(0, 0); got != (color.RGBA{128, 0, 0, 128}) {
		t.Fatalf("got %v", got)
	}
	// 预乘后的颜色不会大于 alpha
	r := rand.New(rand.NewPCG(3, 4))
	big := image.NewNRGBA(image.Rect(0, 0, 97, 53))
	for i := range big.Pix {
		big.Pix[i] = uint8(r.IntN(256))
	}
	out := areaScale(big, 31, 17)
	for i := 0; i < len(out.Pix); i += 4 {
		if out.Pix[i] > out.Pix[i+3] || out.Pix[i+1] > out.Pix[i+3] || out.Pix[i+2] > out.Pix[i+3] {
			t.Fatalf("color over alpha at %d: %v", i/4, out.Pix[i:i+4])
		}
	}
}

// 几种图（RGBA、NRGBA、YCbCr、Gray、调色板）读出来的像素一样时缩出来也一样：各走各的读法，结果和统一换成 RGBA 再缩相同。
func TestAreaScaleSourceTypes(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	base := image.NewRGBA(image.Rect(0, 0, 61, 43))
	for y := 0; y < 43; y++ {
		for x := 0; x < 61; x++ {
			v := uint8(r.IntN(256))
			base.SetRGBA(x, y, color.RGBA{v, uint8(x * 4), uint8(y * 5), 255})
		}
	}
	ycc := image.NewYCbCr(base.Rect, image.YCbCrSubsampleRatio420)
	gray := image.NewGray(base.Rect)
	pal := image.NewPaletted(base.Rect, palette.Plan9)
	nrgba := image.NewNRGBA(base.Rect)
	for y := 0; y < 43; y++ {
		for x := 0; x < 61; x++ {
			c := base.RGBAAt(x, y)
			yy, cb, cr := color.RGBToYCbCr(c.R, c.G, c.B)
			ycc.Y[ycc.YOffset(x, y)] = yy
			ycc.Cb[ycc.COffset(x, y)], ycc.Cr[ycc.COffset(x, y)] = cb, cr
			gray.SetGray(x, y, color.Gray{c.R})
			pal.Set(x, y, c)
			nrgba.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(x*3 + 50)})
		}
	}
	for _, img := range []image.Image{base, nrgba, ycc, gray, pal} {
		asRGBA := image.NewRGBA(img.Bounds())
		draw.Draw(asRGBA, asRGBA.Bounds(), img, image.Point{}, draw.Src)
		got, want := areaScale(img, 23, 15), areaScale(asRGBA, 23, 15)
		for i := range got.Pix {
			if d := int(got.Pix[i]) - int(want.Pix[i]); d > 1 || d < -1 {
				t.Fatalf("%T at byte %d: %d, want %d", img, i, got.Pix[i], want.Pix[i])
			}
		}
	}
	// 源图不从 (0,0) 起（SubImage）
	sub := base.SubImage(image.Rect(10, 5, 50, 35)).(*image.RGBA)
	moved := image.NewRGBA(image.Rect(0, 0, 40, 30))
	draw.Draw(moved, moved.Bounds(), sub, sub.Rect.Min, draw.Src)
	if !bytes.Equal(areaScale(sub, 13, 9).Pix, areaScale(moved, 13, 9).Pix) {
		t.Fatal("sub-image scaled differently")
	}
}

// 原图在门槛以内（或不用缩）时 DecodeLargePreviewBytes 和 DecodePreviewBytes 逐字节相同；超过门槛才走面积平均。
func TestDecodeLargePreviewThreshold(t *testing.T) {
	img := photoLike(120, 80)
	var b bytes.Buffer
	png.Encode(&b, img)
	for _, limit := range []int{1 << 20, 5000, 77} {
		want, _ := DecodePreviewBytes(b.Bytes(), limit)
		got, _ := DecodeLargePreviewBytes(b.Bytes(), limit, 120*80)
		if got.Img.Rect != want.Img.Rect || !bytes.Equal(got.Img.Pix, want.Img.Pix) || got.Size != want.Size {
			t.Fatalf("at the threshold, limit %d: differs", limit)
		}
		got, _ = DecodeLargePreviewBytes(b.Bytes(), limit, 120*80-1)
		if got.Img.Rect != want.Img.Rect || got.Size != want.Size {
			t.Fatalf("over the threshold, limit %d: size %v, want %v", limit, got.Img.Rect, want.Img.Rect)
		}
	}
}

// 和现在的 CatmullRom 结果比：照片风格的图和带 1 像素细线、小字的截图，缩到 0.7 倍（文件标签的大图就是这个比例），
// PSNR 在门槛以上；平均亮度和原图一样（CatmullRom 在白底细线旁冲出 255 被截掉，反而偏暗一点）。
func TestAreaScaleCloseToCatmullRom(t *testing.T) {
	for _, tc := range []struct {
		name string
		img  *image.RGBA
		min  float64
	}{{"photo", photoLike(1400, 1000), 40}, {"screenshot", screenshotLike(1400, 1000), 30}} {
		w, h := 980, 700
		cr := image.NewRGBA(image.Rect(0, 0, w, h))
		xdraw.CatmullRom.Scale(cr, cr.Bounds(), tc.img, tc.img.Rect, xdraw.Src, nil)
		ar := areaScale(tc.img, w, h)
		p, _ := psnr(ar, cr)
		src, a, c := meanRGB(tc.img), meanRGB(ar), meanRGB(cr)
		t.Logf("%s: PSNR %.1f dB vs CatmullRom; mean brightness: source %.2f, area %.2f, CatmullRom %.2f", tc.name, p, src, a, c)
		if p < tc.min || math.Abs(a-src) > 0.1 {
			t.Fatalf("%s: PSNR %.1f, area mean %.2f vs source %.2f", tc.name, p, a, src)
		}
	}
}

func meanRGB(m *image.RGBA) float64 {
	var sum float64
	for i := 0; i < len(m.Pix); i += 4 {
		sum += float64(m.Pix[i]) + float64(m.Pix[i+1]) + float64(m.Pix[i+2])
	}
	return sum / float64(len(m.Pix)/4*3)
}

func near(a, b color.RGBA, d int) bool {
	ok := func(x, y uint8) bool { return int(x)-int(y) <= d && int(y)-int(x) <= d }
	return ok(a.R, b.R) && ok(a.G, b.G) && ok(a.B, b.B) && ok(a.A, b.A)
}

// psnr 是 a、b 的 RGB 峰值信噪比，shift 是 a 比 b 平均亮了多少（0～255）。
func psnr(a, b *image.RGBA) (float64, float64) {
	var se, diff float64
	n := 0
	for i := 0; i < len(a.Pix); i += 4 {
		for c := 0; c < 3; c++ {
			d := float64(a.Pix[i+c]) - float64(b.Pix[i+c])
			se += d * d
			diff += d
			n++
		}
	}
	return 10 * math.Log10(255*255/(se/float64(n))), diff / float64(n)
}

// photoLike 是像照片的图：大块的平滑渐变、几个圆和一点噪声。
func photoLike(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	r := rand.New(rand.NewPCG(7, 8))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fx, fy := float64(x)/float64(w), float64(y)/float64(h)
			v := 120 + 80*math.Sin(fx*7+fy*3) + 40*math.Cos(fy*11-fx*2)
			if dx, dy := fx-0.3, fy-0.4; dx*dx+dy*dy < 0.02 {
				v = 230
			}
			n := r.NormFloat64() * 6
			img.SetRGBA(x, y, color.RGBA{c8(v + n), c8(v*0.8 + 30 + n), c8(255 - v + n), 255})
		}
	}
	return img
}

// screenshotLike 是像截图的图：白底、1 像素的表格线、黑色小字和几块色块。
func screenshotLike(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Rect, image.White, image.Point{}, draw.Src)
	for y := 40; y < h; y += 23 {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{200, 200, 200, 255})
		}
	}
	for x := 120; x < w; x += 211 {
		for y := 0; y < h; y++ {
			img.SetRGBA(x, y, color.RGBA{60, 60, 60, 255})
		}
	}
	draw.Draw(img, image.Rect(0, 0, w, 36), image.NewUniform(color.RGBA{36, 41, 47, 255}), image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.Black, Face: basicfont.Face7x13}
	for y := 58; y < h; y += 23 {
		d.Dot = fixed.P(8, y)
		d.DrawString("func DecodeLargePreviewBytes(data []byte, maxPixels int) // 0123456789 abcdefghijklmnopqrstuvwxyz")
	}
	return img
}

func c8(v float64) uint8 { return uint8(math.Max(0, math.Min(255, v))) }

// naiveArea 是面积平均最直白的写法：对每个目标像素直接积分它盖住的源像素（预乘、float64），给逐像素对照用。
func naiveArea(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	fx, fy := float64(sw)/float64(w), float64(sh)/float64(h)
	px := make([]float64, 4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var sum [4]float64
			x0, x1 := float64(x)*fx, float64(x+1)*fx
			y0, y1 := float64(y)*fy, float64(y+1)*fy
			for sy := int(y0); sy < sh && float64(sy) < y1; sy++ {
				oy := math.Min(y1, float64(sy+1)) - math.Max(y0, float64(sy))
				for sx := int(x0); sx < sw && float64(sx) < x1; sx++ {
					ox := math.Min(x1, float64(sx+1)) - math.Max(x0, float64(sx))
					readRow(src, b.Min.X+sx, b.Min.Y+sy, 1, px)
					for c := range 4 {
						sum[c] += px[c] * ox * oy
					}
				}
			}
			a := clamp8(sum[3] / (fx * fy))
			p := dst.Pix[dst.PixOffset(x, y):]
			for c := range 3 {
				p[c] = min(clamp8(sum[c]/(fx*fy)), a)
			}
			p[3] = a
		}
	}
	return dst
}

// 和直接积分的写法逐像素对照：各种尺寸（不整除、1×N、N×1、缩很多、比源图窄一点），透明的随机图，
// 分段读（宽过 areaChunk）也一样。两边浮点相加的顺序不同：预乘值的平均是有理数，常正好落在 .5 上，
// 这时舍入方向可能不同，允许差 1，且这样的字节不到 1%。
func TestAreaScaleMatchesNaive(t *testing.T) {
	r := rand.New(rand.NewPCG(9, 10))
	cases := append([][4]int{{areaChunk + 77, 3, 1001, 2}, {3, 5000, 2, 333}}, areaCases...)
	for _, cs := range cases {
		src := image.NewNRGBA(image.Rect(0, 0, cs[0], cs[1]))
		for i := range src.Pix {
			src.Pix[i] = uint8(r.IntN(256))
		}
		got, want := areaScale(src, cs[2], cs[3]), naiveArea(src, cs[2], cs[3])
		diff := 0
		for i := range got.Pix {
			d := int(got.Pix[i]) - int(want.Pix[i])
			if d > 1 || d < -1 {
				t.Fatalf("%v byte %d: %d, want %d", cs, i, got.Pix[i], want.Pix[i])
			}
			if d != 0 {
				diff++
			}
		}
		if diff > len(got.Pix)/100+1 {
			t.Fatalf("%v: %d of %d bytes off by one", cs, diff, len(got.Pix))
		}
	}
}

// lineImage 是 w×h、按坐标算颜色的图，不真分配像素：喂给 areaScale 看极窄、极宽的大图额外分配多少。
type lineImage struct{ w, h int }

func (l lineImage) ColorModel() color.Model { return color.RGBAModel }
func (l lineImage) Bounds() image.Rectangle { return image.Rect(0, 0, l.w, l.h) }
func (l lineImage) At(x, y int) color.Color { return l.RGBA64At(x, y) }
func (l lineImage) RGBA64At(x, y int) color.RGBA64 {
	v := uint16(uint8((x+y)*7)) * 257
	return color.RGBA64{v, v / 2, 0xffff - v, 0xffff}
}

// 1×N 和 N×1 的大图：除了目标位图，分配的只有几行目标宽的累加器和一段源像素，和源图多高、多宽无关。
func TestAreaScaleAllocationBounded(t *testing.T) {
	alloc := func(f func()) uint64 {
		var m0, m1 runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&m0)
		f()
		runtime.ReadMemStats(&m1)
		return m1.TotalAlloc - m0.TotalAlloc
	}
	for _, tc := range []struct {
		src  lineImage
		w, h int
	}{{lineImage{1, 4_000_000}, 1, 1000}, {lineImage{4_000_000, 1}, 1000, 1}, {lineImage{2000, 2000}, 700, 700}} {
		var out *image.RGBA
		n := alloc(func() { out = areaScale(tc.src, tc.w, tc.h) })
		bitmap := uint64(len(out.Pix))
		// 三行目标宽的 float64（每像素 4 个）、一段源像素，再加一点零头
		bound := bitmap + uint64(3*4*8*tc.w) + uint64(4*8*areaChunk) + 64<<10
		t.Logf("%dx%d -> %dx%d: allocated %d KB, bitmap %d KB", tc.src.w, tc.src.h, tc.w, tc.h, n>>10, bitmap>>10)
		if n > bound {
			t.Fatalf("%dx%d -> %dx%d: allocated %d B, bound %d B", tc.src.w, tc.src.h, tc.w, tc.h, n, bound)
		}
	}
}
