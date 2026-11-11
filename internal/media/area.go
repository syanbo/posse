package media

import (
	"image"
	"image/color"
	"math"
)

// areaScale 用面积平均把 src 缩到 w×h（两边都只缩不放）：每个目标像素是它盖住的那块源像素按覆盖面积的加权平均，
// 在预乘 alpha 下算。按源图逐行流式累加，除了目标位图只用几行 float64 的累加器；
// CatmullRom 的中间缓冲是「目标宽 × 源图高」每格 32 字节，几千万像素的图要上 GB。
func areaScale(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	cols := areaSpans(sw, w)
	rows := areaSpans(sh, h)
	// 每个目标列、目标行分到的权重总和，按它归一化（浮点累加的误差不带进亮度）
	colSum := make([]float64, w)
	for _, c := range cols {
		colSum[c.a] += c.wa
		if c.wb > 0 {
			colSum[c.a+1] += c.wb
		}
	}
	rowSum := make([]float64, h)
	for _, r := range rows {
		rowSum[r.a] += r.wa
		if r.wb > 0 {
			rowSum[r.a+1] += r.wb
		}
	}
	line := make([]float64, 4*sw) // 当前源行，预乘后的 0～255
	row := make([]float64, 4*w)   // 当前源行横向缩好的
	acc := [2][]float64{make([]float64, 4*w), make([]float64, 4*w)}
	base := 0 // acc[0] 对着的目标行
	flush := func(y int, buf []float64) {
		out := dst.Pix[y*dst.Stride:]
		for x := 0; x < w; x++ {
			k := colSum[x] * rowSum[y]
			a := clamp8(buf[4*x+3] / k)
			for c := 0; c < 3; c++ {
				// 预乘的颜色不能大于 alpha
				out[4*x+c] = min(clamp8(buf[4*x+c]/k), a)
			}
			out[4*x+3] = a
		}
	}
	for sy := 0; sy < sh; sy++ {
		readRow(src, b.Min.X, b.Min.Y+sy, sw, line)
		clear(row)
		for sx, c := range cols {
			p := line[4*sx : 4*sx+4]
			q := row[4*c.a : 4*c.a+4]
			q[0] += p[0] * c.wa
			q[1] += p[1] * c.wa
			q[2] += p[2] * c.wa
			q[3] += p[3] * c.wa
			if c.wb > 0 {
				q = row[4*c.a+4 : 4*c.a+8]
				q[0] += p[0] * c.wb
				q[1] += p[1] * c.wb
				q[2] += p[2] * c.wb
				q[3] += p[3] * c.wb
			}
		}
		r := rows[sy]
		for r.a > base {
			flush(base, acc[0])
			acc[0], acc[1] = acc[1], acc[0]
			clear(acc[1])
			base++
		}
		for i, v := range row {
			acc[0][i] += v * r.wa
		}
		if r.wb > 0 {
			for i, v := range row {
				acc[1][i] += v * r.wb
			}
		}
	}
	flush(base, acc[0])
	if base+1 < h {
		flush(base+1, acc[1])
	}
	return dst
}

// areaSpan 是一个源像素（列或行）分给目标的权重：给第 a 个 wa，给第 a+1 个 wb（跨在两个目标像素的边界上时）。
type areaSpan struct {
	a      int
	wa, wb float64
}

// areaSpans 把 n 个源像素分到 m 个目标像素上（m <= n）：目标像素 i 盖住源坐标 [i·n/m, (i+1)·n/m)。
// 按整数算边界，不让浮点误差把最后一列分到界外。
func areaSpans(n, m int) []areaSpan {
	out := make([]areaSpan, n)
	for s := 0; s < n; s++ {
		// 源像素 [s, s+1) 乘 m 之后是 [s·m, (s+1)·m)，目标像素 i 是 [i·n, (i+1)·n)
		lo, hi := s*m, (s+1)*m
		a := lo / n
		edge := (a + 1) * n
		if hi <= edge || a+1 >= m {
			out[s] = areaSpan{a: min(a, m-1), wa: 1}
			continue
		}
		out[s] = areaSpan{a: a, wa: float64(edge-lo) / float64(m), wb: float64(hi-edge) / float64(m)}
	}
	return out
}

func clamp8(v float64) uint8 {
	return uint8(math.Max(0, math.Min(255, math.Round(v))))
}

// readRow 把 src 第 y 行从 x0 起的 n 个像素读成预乘的 0～255（float64），常见的几种图直接读像素数组。
func readRow(src image.Image, x0, y, n int, out []float64) {
	switch m := src.(type) {
	case *image.RGBA:
		p := m.Pix[m.PixOffset(x0, y):]
		for i := 0; i < 4*n; i++ {
			out[i] = float64(p[i])
		}
	case *image.NRGBA:
		p := m.Pix[m.PixOffset(x0, y):]
		for i := 0; i < n; i++ {
			a := float64(p[4*i+3])
			out[4*i] = float64(p[4*i]) * a / 255
			out[4*i+1] = float64(p[4*i+1]) * a / 255
			out[4*i+2] = float64(p[4*i+2]) * a / 255
			out[4*i+3] = a
		}
	case *image.YCbCr:
		for i := 0; i < n; i++ {
			yi, ci := m.YOffset(x0+i, y), m.COffset(x0+i, y)
			r, g, bb := color.YCbCrToRGB(m.Y[yi], m.Cb[ci], m.Cr[ci])
			out[4*i], out[4*i+1], out[4*i+2], out[4*i+3] = float64(r), float64(g), float64(bb), 255
		}
	case *image.Gray:
		p := m.Pix[m.PixOffset(x0, y):]
		for i := 0; i < n; i++ {
			v := float64(p[i])
			out[4*i], out[4*i+1], out[4*i+2], out[4*i+3] = v, v, v, 255
		}
	default:
		for i := 0; i < n; i++ {
			r, g, bb, a := src.At(x0+i, y).RGBA() // 预乘的 0～65535
			out[4*i], out[4*i+1], out[4*i+2], out[4*i+3] = float64(r)/257, float64(g)/257, float64(bb)/257, float64(a)/257
		}
	}
}
