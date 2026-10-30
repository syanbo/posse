// Package diagram 用 mygo 画 internal/mermaid 排好的图：形状交给 mygo 的 SVG（大图切块），文字用窗口的字体排字。
package diagram

import (
	"bytes"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/internal/mermaid"
)

// Style 是图的配色和文字的字体。
type Style struct {
	Bg, Fg, Muted, Line, Surface, SurfaceAlt, Border, Accent, Danger ui.Color
	// Sans 为空是窗口的字体；Mono 是标签里代码的字体。
	Sans, Mono                     string
	Weight, BoldWeight, MonoWeight int
}

func color(c ui.Color) mermaid.Color { return mermaid.Color{R: c.R, G: c.G, B: c.B, A: c.A} }

func (st Style) colors() mermaid.Style {
	return mermaid.Style{
		Bg: color(st.Bg), Fg: color(st.Fg), Muted: color(st.Muted), Line: color(st.Line), Surface: color(st.Surface),
		SurfaceAlt: color(st.SurfaceAlt), Border: color(st.Border), Accent: color(st.Accent), Danger: color(st.Danger),
	}
}

// font 是样式位对应的字体。
func (st Style) font(size float32, style int) ui.Font {
	f := ui.Font{Family: st.Sans, Size: size, Weight: st.Weight}
	if style&mermaid.Bold != 0 {
		f.Weight = st.BoldWeight
	}
	f.Italic = style&mermaid.Italic != 0
	if style&mermaid.Mono != 0 {
		f.Family, f.Weight = st.Mono, st.MonoWeight
	}
	if style&(mermaid.Sub|mermaid.Sup) != 0 {
		f.Size *= mermaid.Smaller
	}
	return f
}

func (st Style) measurer() mermaid.Measurer {
	return mermaid.Measurer{
		Key: fmt.Sprint(st.Sans, "|", st.Mono, "|", st.Weight, "|", st.BoldWeight, "|", st.MonoWeight),
		Width: func(text string, size float32, style int) float32 {
			w, _ := ui.ShapeText(text, st.font(size, style), 0).Size()
			return w
		},
	}
}

// Diagram 是画好的图：形状是 SVG，文字是一串标签。
type Diagram struct {
	// tiles 是切开的形状：mygo 画不了超过 4096 像素的 SVG。
	svg    *ui.SVG
	tiles  []tile
	w, h   float32
	labels []label
	// Err 是画不出来的原因，为空表示画好了。
	Err string
	// shaped 是按上次绘制的缩放排好的标签，只在界面线程上用；gen 是排的时候文本系统的代数，
	// 变了说明字体被整批释放过，这些排版不能再画。
	shaped []*ui.TextLayout
	scale  float32
	gen    uint64
}

// label 是一段同样式的文字：基线起点在图的 x, y，绕它转 angle 度；base 是排字后顶边到基线的距离。
type label struct {
	text  string
	font  ui.Font
	x, y  float32
	base  float32
	angle float32
	color ui.Color
}

// tile 是形状里 x, y, w×h 的那一块。
type tile struct {
	svg        *ui.SVG
	x, y, w, h float32
}

// tileSide 是一块的最长边（DIP）：2 倍屏上放大到 2 倍是 4 倍，仍在 4096 像素以内。
const tileSide = 1024

// Render 按 st 画出 source 的图。任何协程都能调，同一时间只画一张。
func Render(source string, st Style) *Diagram {
	l, err := mermaid.Render(source, st.colors(), st.measurer())
	if err != nil {
		return &Diagram{Err: err.Error()}
	}
	s, err := ui.ParseSVG(l.Shapes)
	if err != nil {
		return &Diagram{Err: err.Error()}
	}
	d := &Diagram{svg: s, w: l.W, h: l.H}
	if d.tiles, err = tiles(l.Shapes, s, d.w, d.h); err != nil {
		return &Diagram{Err: err.Error()}
	}
	for _, lb := range l.Labels {
		f := st.font(lb.Size, lb.Style)
		_, h := ui.ShapeText(lb.Text, f, 0).Size()
		m := f.Metrics()
		// 行高里除去上下伸部，剩下的上下各一半，基线在上面那一半加上伸部处
		base := (h-m.Ascent-m.Descent)/2 + m.Ascent
		c := lb.Color
		d.labels = append(d.labels, label{text: lb.Text, font: f, x: lb.X, y: lb.Y, base: base, angle: lb.Angle,
			color: ui.Color{R: c.R, G: c.G, B: c.B, A: c.A}})
	}
	return d
}

// Size 是图的原始尺寸（DIP）。
func (d *Diagram) Size() (float32, float32) { return d.w, d.h }

// Draw 把图画进 r，按 r 的宽度等比缩放；标签按显示的大小重新排字，放大了也清楚。
func (d *Diagram) Draw(pt *ui.Painter, r ui.Rect) {
	if d.svg == nil || d.w <= 0 || r.W <= 0 {
		return
	}
	s := r.W / d.w
	for _, t := range d.tiles {
		// 每块画到下一块的起点，接缝处不留缝
		x0, y0 := r.X+t.x*s, r.Y+t.y*s
		pt.Image(t.svg, ui.Rect{X: x0, Y: y0, W: r.X + (t.x+t.w)*s - x0, H: r.Y + (t.y+t.h)*s - y0}, ui.FillBox)
	}
	if size, gen := float32(math.Round(float64(s)*100)/100), ui.TextGeneration(); size != d.scale || gen != d.gen || len(d.shaped) != len(d.labels) {
		d.scale, d.gen, d.shaped = size, gen, make([]*ui.TextLayout, len(d.labels))
		for i, l := range d.labels {
			f := l.font
			f.Size *= max(size, 0.01)
			d.shaped[i] = ui.ShapeText(l.text, f, 0)
		}
	}
	for i, l := range d.labels {
		x, y := r.X+l.x*s, r.Y+l.y*s
		if l.angle == 0 {
			pt.TextLayout(d.shaped[i], x, y-l.base*s, l.color)
			continue
		}
		pt.Transform(ui.Rotation(l.angle), x, y, func() { pt.TextLayout(d.shaped[i], x, y-l.base*s, l.color) })
	}
}

// tiles 把 w×h 的形状（s 是整张）切成边长不超过 tileSide 的块。
func tiles(shapes []byte, s *ui.SVG, w, h float32) ([]tile, error) {
	if w <= tileSide && h <= tileSide {
		return []tile{{svg: s, w: w, h: h}}, nil
	}
	open := bytes.Index(shapes, []byte("<svg"))
	end := bytes.IndexByte(shapes[max(open, 0):], '>')
	if open < 0 || end < 0 {
		return []tile{{svg: s, w: w, h: h}}, nil
	}
	end += open
	root := string(shapes[open:end])
	vx, vy, vw, vh := float32(0), float32(0), w, h
	if m := svgViewBox.FindStringSubmatch(root); m != nil {
		if f := strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == ' ' }); len(f) == 4 {
			var n [4]float32
			ok := true
			for i, v := range f {
				x, err := strconv.ParseFloat(v, 32)
				n[i], ok = float32(x), ok && err == nil
			}
			if ok && n[2] > 0 && n[3] > 0 {
				vx, vy, vw, vh = n[0], n[1], n[2], n[3]
			}
		}
	}
	root = svgSizeAttr.ReplaceAllString(root, "")
	var out []tile
	for y := float32(0); y < h; y += tileSide {
		for x := float32(0); x < w; x += tileSide {
			tw, th := min(tileSide, w-x), min(tileSide, h-y)
			var b bytes.Buffer
			b.Write(shapes[:open])
			fmt.Fprintf(&b, `%s viewBox="%g %g %g %g" width="%g" height="%g"`, root,
				vx+x*vw/w, vy+y*vh/h, tw*vw/w, th*vh/h, tw, th)
			b.Write(shapes[end:])
			t, err := ui.ParseSVG(b.Bytes())
			if err != nil {
				return nil, err
			}
			out = append(out, tile{svg: t, x: x, y: y, w: tw, h: th})
		}
	}
	return out, nil
}

var (
	svgViewBox  = regexp.MustCompile(`\sviewBox="([^"]*)"`)
	svgSizeAttr = regexp.MustCompile(`\s(viewBox|width|height|preserveAspectRatio)="[^"]*"`)
)
