// Package mermaid 把 Mermaid 源码画成 mygo 能画的图：Pele 在 QuickJS 里排版、出 SVG，
// 形状交给 mygo 的 SVG，文字抽出来由 mygo 自己排字画（mygo 的 SVG 不画 <text>）。
//
// 移植自 Mayoi（https://github.com/sorrycc/Mayoi，cmd/mayoi/mermaid.go），
// MIT 许可，Copyright (c) 2026 chencheng。
package mermaid

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"modernc.org/quickjs"
)

// peleJS 由 scripts/pele.sh 打包，运行后设置全局变量 Pele。
//
//go:embed pele.js.gz
var peleJS []byte

// Style 是画图用的颜色（Pele 的 CSS 变量取这些值）和文字的字体。
// 颜色都要不透明：Pele 拿它们混色。
type Style struct {
	Bg, Fg, Muted, Line, Surface, SurfaceAlt, Border, Accent, Danger ui.Color
	// Sans 为空是窗口的字体；Mono 是标签里代码的字体。
	Sans, Mono                     string
	Weight, BoldWeight, MonoWeight int
}

// font 是 Pele 量文字时给的样式位对应的字体。
func (st Style) font(size float32, style int) ui.Font {
	f := ui.Font{Family: st.Sans, Size: size, Weight: st.Weight}
	if style&peleBold != 0 {
		f.Weight = st.BoldWeight
	}
	f.Italic = style&peleItalic != 0
	if style&peleMono != 0 {
		f.Family, f.Weight = st.Mono, st.MonoWeight
	}
	if style&(peleSub|peleSup) != 0 {
		f.Size *= smaller
	}
	return f
}

// Pele 量文字时传来的样式位。
const (
	peleBold   = 1
	peleItalic = 2
	peleMono   = 4
	peleSub    = 8
	peleSup    = 16
)

// smaller 是上下标相对正文的字号。
const smaller = 0.8

// Diagram 是画好的图：形状是 SVG，文字是一串标签。
type Diagram struct {
	// tiles 是切开的形状：mygo 画不了超过 4096 像素的 SVG。
	svg    *ui.SVG
	tiles  []tile
	w, h   float32
	labels []label
	// Err 是画不出来的原因，为空表示画好了。
	Err string
	// shaped 是按上次绘制的缩放排好的标签，只在界面线程上用。
	shaped []*ui.TextLayout
	scale  float32
}

// label 是标签里一段同样式的文字，左上角在图的 x, y，绕它转 angle 度。
type label struct {
	text  string
	font  ui.Font
	x, y  float32
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
	if size := float32(math.Round(float64(s)*100) / 100); size != d.scale || len(d.shaped) != len(d.labels) {
		d.scale, d.shaped = size, make([]*ui.TextLayout, len(d.labels))
		for i, l := range d.labels {
			f := l.font
			f.Size *= max(size, 0.01)
			d.shaped[i] = ui.ShapeText(l.text, f, 0)
		}
	}
	for i, l := range d.labels {
		x, y := r.X+l.x*s, r.Y+l.y*s
		if l.angle == 0 {
			pt.TextLayout(d.shaped[i], x, y, l.color)
			continue
		}
		pt.Transform(ui.Rotation(l.angle), x, y, func() { pt.TextLayout(d.shaped[i], x, y, l.color) })
	}
}

// timeout 是一张图最多画多久。
const timeout = 5 * time.Second

// pele 是运行 Pele 的 JS 运行时（QuickJS，和 sqlite 共用 modernc 的 libc），画第一张图时启动（约 90 ms、11 MB），
// 一次画一张。启动后一直留着：modernc 的分配器把释放的页留着复用、没有还给系统的接口，关掉省不下内存，反复开关反而越占越多。
var pele struct {
	sync.Mutex
	vm  *quickjs.VM
	err error
	// style 是正在画的图的样式，量文字用它的字体；measured 是 Pele 记着的那些宽度量的时候用的样式。
	style, measured Style
}

// renderJS 是 posseRender(source, fresh)：用窗口的字体量文字画出 SVG，fresh 时先忘掉量过的宽度；
// 出错时去掉 Pele 附在错误信息里的那段源码，只留一行。
const renderJS = `var posseMeasurer = {width: function (text, size, style) { return posseMeasure(text, size, style) }};
function posseRender(source, fresh) {
	if (fresh) Pele.forgetTextWidths();
	try {
		return Pele.render(source, {measurer: posseMeasurer}).svg;
	} catch (e) {
		var m = String(e && e.message || e);
		if (e && e.snippet) m = m.replace(e.snippet, " ");
		throw new Error(m);
	}
}`

// start 启动运行时，载入 Pele。
func start() error {
	zr, err := gzip.NewReader(bytes.NewReader(peleJS))
	if err != nil {
		return err
	}
	script, err := io.ReadAll(zr)
	if err != nil {
		return err
	}
	vm, err := quickjs.NewVM()
	if err != nil {
		return err
	}
	// JS 的整数到这里是 int，带小数的是 float64
	err = vm.RegisterFunc("posseMeasure", func(text string, size, style any) float64 {
		w, _ := ui.ShapeText(text, pele.style.font(float32(number(size)), int(number(style))), 0).Size()
		return float64(w)
	}, false)
	if err == nil {
		_, err = vm.Eval(string(script), quickjs.EvalGlobal)
	}
	if err == nil {
		_, err = vm.Eval(renderJS, quickjs.EvalGlobal)
	}
	if err != nil {
		vm.Close()
		return err
	}
	vm.SetEvalTimeout(timeout)
	pele.vm, pele.measured = vm, Style{}
	return nil
}

func number(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case float64:
		return n
	}
	return 0
}

// peleRender 用 st 的字体量文字，把 source 画成 SVG，或者说出哪里写错了。
func peleRender(source string, st Style) (string, error) {
	pele.Lock()
	defer pele.Unlock()
	if pele.vm == nil && pele.err == nil {
		pele.err = start()
	}
	if pele.err != nil {
		return "", pele.err
	}
	pele.style = st
	fresh := st.Sans != pele.measured.Sans || st.Mono != pele.measured.Mono ||
		st.Weight != pele.measured.Weight || st.BoldWeight != pele.measured.BoldWeight || st.MonoWeight != pele.measured.MonoWeight
	pele.measured = st
	began := time.Now()
	v, err := pele.vm.Call("posseRender", source, fresh)
	if err != nil {
		if time.Since(began) >= timeout {
			return "", errTimeout
		}
		var ex *quickjs.Error
		if errors.As(err, &ex) && ex.Message != "" {
			return "", errors.New(strings.Join(strings.Fields(ex.Message), " "))
		}
		return "", err
	}
	svg, ok := v.(string)
	if !ok {
		return "", errors.New("Pele 没有画出 SVG")
	}
	return svg, nil
}

var errTimeout = errors.New("画图超时")

// Render 按 st 画出 source 的图。任何协程都能调，同一时间只画一张。
func Render(source string, st Style) *Diagram {
	svg, err := peleRender(source, st)
	if err != nil {
		return &Diagram{Err: err.Error()}
	}
	shapes, texts, err := toSVG(svg, st)
	if err != nil {
		return &Diagram{Err: err.Error()}
	}
	s, err := ui.ParseSVG(shapes)
	if err != nil {
		return &Diagram{Err: err.Error()}
	}
	d := &Diagram{svg: s}
	if d.w, d.h = s.Size(); d.w <= 0 || d.h <= 0 {
		return &Diagram{Err: "图的尺寸为空"}
	}
	if d.tiles, err = tiles(shapes, s, d.w, d.h); err != nil {
		return &Diagram{Err: err.Error()}
	}
	for _, t := range texts {
		d.labels = append(d.labels, t.shape(st)...)
	}
	return d
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

// run 是标签里一段同样式的文字。
type run struct {
	text  string
	size  float32
	style int
	color ui.Color
}

// chunk 是标签里从 x, y（基线）起接连排的几段文字，按 anchor（start、middle、end）对齐，m 把它放进图里。
type chunk struct {
	m      affine
	x, y   float32
	anchor string
	runs   []run
}

// shape 排好这一块的文字，放到图里。
func (t chunk) shape(st Style) []label {
	type piece struct {
		text   string
		font   ui.Font
		x, top float32
		color  ui.Color
	}
	var pieces []piece
	pen := float32(0)
	for _, r := range t.runs {
		if r.text == "" {
			continue
		}
		f := st.font(r.size, r.style)
		l := ui.ShapeText(r.text, f, 0)
		w, h := l.Size()
		m := f.Metrics()
		// 行高里除去上下伸部，剩下的上下各一半，基线在上面那一半加上伸部处
		base := (h-m.Ascent-m.Descent)/2 + m.Ascent
		shift := float32(0)
		switch {
		case r.style&peleSub != 0:
			shift = r.size * 0.2
		case r.style&peleSup != 0:
			shift = -r.size * 0.35
		}
		pieces = append(pieces, piece{r.text, f, pen, shift - base, r.color})
		pen += w
	}
	left := t.x
	switch t.anchor {
	case "middle":
		left -= pen / 2
	case "end":
		left -= pen
	}
	angle := float32(math.Atan2(float64(t.m.b), float64(t.m.a)) * 180 / math.Pi)
	if math.Abs(float64(angle)) < 0.01 {
		angle = 0
	}
	labels := make([]label, 0, len(pieces))
	for _, p := range pieces {
		x, y := t.m.apply(left+p.x, t.y+p.top)
		labels = append(labels, label{text: p.text, font: p.font, x: x, y: y, angle: angle, color: p.color})
	}
	return labels
}

// affine 是二维变换，同 SVG 的 matrix(a b c d e f)。
type affine struct{ a, b, c, d, e, f float32 }

var identity = affine{a: 1, d: 1}

// then 是先 m 后 n。
func (m affine) then(n affine) affine {
	return affine{
		a: n.a*m.a + n.c*m.b, b: n.b*m.a + n.d*m.b,
		c: n.a*m.c + n.c*m.d, d: n.b*m.c + n.d*m.d,
		e: n.a*m.e + n.c*m.f + n.e, f: n.b*m.e + n.d*m.f + n.f,
	}
}

func (m affine) apply(x, y float32) (float32, float32) {
	return m.a*x + m.c*y + m.e, m.b*x + m.d*y + m.f
}

// parseTransform 解析 SVG 的 transform（translate、scale、rotate、skewX、skewY、matrix 的列表）。
func parseTransform(s string) affine {
	m := identity
	for {
		open := strings.IndexByte(s, '(')
		end := strings.IndexByte(s, ')')
		if open < 0 || end < open {
			return m
		}
		name := strings.TrimSpace(strings.Trim(s[:open], " ,\t\n"))
		var n []float32
		for _, f := range strings.FieldsFunc(s[open+1:end], func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
			if v, err := strconv.ParseFloat(f, 32); err == nil {
				n = append(n, float32(v))
			}
		}
		arg := func(i int, def float32) float32 {
			if i < len(n) {
				return n[i]
			}
			return def
		}
		var t affine
		switch name {
		case "translate":
			t = affine{a: 1, d: 1, e: arg(0, 0), f: arg(1, 0)}
		case "scale":
			t = affine{a: arg(0, 1), d: arg(1, arg(0, 1))}
		case "rotate":
			sin, cos := math.Sincos(float64(arg(0, 0)) * math.Pi / 180)
			cx, cy := arg(1, 0), arg(2, 0)
			t = affine{a: 1, d: 1, e: -cx, f: -cy}.
				then(affine{a: float32(cos), b: float32(sin), c: float32(-sin), d: float32(cos)}).
				then(affine{a: 1, d: 1, e: cx, f: cy})
		case "skewX":
			t = affine{a: 1, c: float32(math.Tan(float64(arg(0, 0)) * math.Pi / 180)), d: 1}
		case "skewY":
			t = affine{a: 1, b: float32(math.Tan(float64(arg(0, 0)) * math.Pi / 180)), d: 1}
		case "matrix":
			t = affine{arg(0, 1), arg(1, 0), arg(2, 0), arg(3, 1), arg(4, 0), arg(5, 0)}
		default:
			t = identity
		}
		// 列表里后面的变换在前面的坐标系里做
		m = t.then(m)
		s = s[end+1:]
	}
}

// palette 是 Pele 的 CSS 变量的值，没给的用 Pele 自己的。
func (st Style) palette() map[string]string {
	return map[string]string{
		"--pele-bg": hexColor(st.Bg), "--pele-fg": hexColor(st.Fg), "--pele-muted": hexColor(st.Muted),
		"--pele-line": hexColor(st.Line), "--pele-surface": hexColor(st.Surface),
		"--pele-surface-alt": hexColor(st.SurfaceAlt), "--pele-border": hexColor(st.Border),
		"--pele-accent": hexColor(st.Accent), "--pele-critical": hexColor(st.Danger),
		"--pele-error": hexColor(st.Danger),
	}
}

func hexColor(c ui.Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// frame 是 SVG 元素传给里面元素的东西：位置、填充色和文字样式。
type frame struct {
	m                  affine
	fill               string
	size               float32
	bold, italic, mono bool
	anchor             string
	shift              int
	x, y, dy           string
	preserve           bool
	// under 是节点、子图的 style 给形状的填充色（标签画在它上面），没有是透明；
	// ownFill 表示文字自己的 style 给了填充色。
	under   ui.Color
	ownFill bool
}

// toSVG 把 Pele 的 SVG 变成 mygo 能画的：CSS 变量换成 st 的颜色，文字抽出来交给窗口自己排字。
func toSVG(svg string, st Style) ([]byte, []chunk, error) {
	vars := st.palette()
	resolve := func(v string) string { return cssResolve(v, vars) }
	var (
		out    bytes.Buffer
		chunks []chunk
		stack  = []frame{{m: identity, size: 16, anchor: "start"}}
		// text 是正在读的 text 元素里的那一块，penX、penY 是下一段文字的位置
		text       *chunk
		penX, penY float32
		inText     int
	)
	dec := xml.NewDecoder(strings.NewReader(svg))
	dec.Strict = false
	for {
		tok, err := dec.RawToken()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			top := stack[len(stack)-1]
			f := top
			f.x, f.y, f.dy = "", "", ""
			styleAttr := ""
			for _, at := range t.Attr {
				if at.Name.Local == "style" && at.Name.Space == "" {
					styleAttr = at.Value
				}
			}
			decls := cssDecls(styleAttr)
			if len(stack) == 1 {
				// 根元素的自定义属性是 Pele 给变量起的短名，比如 --_bg 指 --pele-bg
				for _, d := range decls {
					if strings.HasPrefix(d[0], "--") {
						vars[d[0]] = d[1]
					}
				}
			}
			prop := func(name string) (string, bool) {
				for _, d := range decls {
					if d[0] == name {
						return resolve(d[1]), true
					}
				}
				for _, at := range t.Attr {
					if at.Name.Local == name && at.Name.Space == "" {
						return resolve(at.Value), true
					}
				}
				return "", false
			}
			if v, ok := prop("fill"); ok {
				f.fill = v
			}
			// style、classDef 给形状的填充色是这一组标签的底色；给文字的是标签自己的颜色
			for _, d := range decls {
				switch {
				case d[0] != "fill":
				case t.Name.Local == "text" || inText > 0:
					f.ownFill = true
				case len(stack) > 1:
					u := cssParseColor(resolve(d[1]), ui.Color{})
					if v, ok := prop("fill-opacity"); ok {
						if n, err := strconv.ParseFloat(v, 32); err == nil {
							u = u.Alpha(float32(n))
						}
					}
					stack[len(stack)-1].under = u
				}
			}
			if v, ok := prop("font-size"); ok {
				if n, ok := cssLength(v); ok {
					f.size = n
				}
			}
			if v, ok := prop("font"); ok {
				for _, w := range strings.Fields(v) {
					if n, ok := cssLength(w); ok {
						f.size = n
					}
				}
			}
			if v, ok := prop("font-weight"); ok {
				// Pele 没设的变量（如 --_w）表示它的粗体
				f.bold = v == "" || cssBold(v)
			}
			if v, ok := prop("font-style"); ok {
				f.italic = v == "italic" || v == "oblique"
			}
			if v, ok := prop("font-family"); ok && inText > 0 {
				f.mono = strings.Contains(v, "mono") || v == st.Mono
			}
			if v, ok := prop("text-anchor"); ok {
				f.anchor = v
			}
			if v, ok := prop("baseline-shift"); ok {
				switch v {
				case "sub":
					f.shift = peleSub
				case "super":
					f.shift = peleSup
				}
			}
			if v, ok := prop("transform"); ok {
				f.m = parseTransform(v).then(top.m)
			}
			for _, at := range t.Attr {
				switch at.Name.Local {
				case "x":
					f.x = at.Value
				case "y":
					f.y = at.Value
				case "dy":
					f.dy = at.Value
				case "space":
					f.preserve = at.Value == "preserve"
				}
			}
			stack = append(stack, f)
			if t.Name.Local == "text" || inText > 0 {
				if t.Name.Local == "text" {
					chunks = append(chunks, chunk{m: f.m, anchor: f.anchor})
					text = &chunks[len(chunks)-1]
					penX, penY = 0, 0
				}
				inText++
				x, xok := cssLength(f.x)
				y, yok := cssLength(f.y)
				dy, _ := cssLength(f.dy)
				if yok {
					penY = y
				}
				penY += dy
				if xok || yok && t.Name.Local != "text" {
					if !xok {
						x = penX
					}
					if len(text.runs) > 0 {
						chunks = append(chunks, chunk{m: text.m, anchor: f.anchor})
						text = &chunks[len(chunks)-1]
					}
					text.x, text.y, text.anchor = x, penY, f.anchor
					penX = x
				}
				if len(text.runs) == 0 {
					text.y = penY
				}
				continue
			}
			out.WriteByte('<')
			out.WriteString(xmlName(t.Name))
			var keep []string
			for _, d := range decls {
				switch {
				case strings.HasPrefix(d[0], "--"):
				case d[0] == "rx" || d[0] == "ry":
					// mygo 只从属性读矩形的圆角
					if n, ok := cssLength(resolve(d[1])); ok {
						fmt.Fprintf(&out, ` %s="%g"`, d[0], n)
					}
				default:
					keep = append(keep, d[0]+":"+resolve(d[1]))
				}
			}
			for _, at := range t.Attr {
				if at.Name.Local == "style" && at.Name.Space == "" {
					if len(keep) > 0 {
						fmt.Fprintf(&out, ` style="%s"`, xmlEscape(strings.Join(keep, ";")))
					}
					continue
				}
				fmt.Fprintf(&out, ` %s="%s"`, xmlName(at.Name), xmlEscape(resolve(at.Value)))
			}
			out.WriteByte('>')
		case xml.EndElement:
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
			if inText > 0 {
				inText--
				continue
			}
			out.WriteString("</" + xmlName(t.Name) + ">")
		case xml.CharData:
			if inText == 0 {
				xmlEscapeTo(&out, string(t))
				continue
			}
			f := stack[len(stack)-1]
			s := string(t)
			if !f.preserve {
				s = strings.Join(strings.Fields(s), " ")
			}
			if s == "" {
				continue
			}
			style := f.shift
			if f.bold {
				style |= peleBold
			}
			if f.italic {
				style |= peleItalic
			}
			if f.mono {
				style |= peleMono
			}
			text.runs = append(text.runs, run{text: s, size: f.size, style: style, color: f.color(st)})
		}
	}
	return out.Bytes(), chunks, nil
}

// labelContrast 是标签和 style 给形状的填充色之间至少要有的对比度，不够就换颜色。
const labelContrast = 3

// color 是文字的颜色：它的填充色；形状被 style 填了一个和它对比不够的颜色时，取主题的文字色和底色里更清楚的那个。
func (f frame) color(st Style) ui.Color {
	c := cssParseColor(f.fill, st.Fg)
	if f.ownFill || f.under.A == 0 {
		return c
	}
	u := f.under.Over(st.Bg)
	if contrast(c.Over(st.Bg), u) >= labelContrast {
		return c
	}
	if contrast(st.Fg, u) >= contrast(st.Bg, u) {
		return st.Fg
	}
	return st.Bg
}

// contrast 是两个不透明颜色的 WCAG 对比度。
func contrast(x, y ui.Color) float64 {
	lum := func(c ui.Color) float64 {
		ch := func(v uint8) float64 {
			f := float64(v) / 255
			if f <= 0.03928 {
				return f / 12.92
			}
			return math.Pow((f+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
	}
	a, b := lum(x)+0.05, lum(y)+0.05
	return max(a, b) / min(a, b)
}

func xmlName(n xml.Name) string {
	if n.Space != "" {
		return n.Space + ":" + n.Local
	}
	return n.Local
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xmlEscapeTo(&b, s)
	return b.String()
}

func xmlEscapeTo(b *bytes.Buffer, s string) { xml.EscapeText(b, []byte(s)) }

// cssDecls 把 style 属性拆成一条条声明（名字、值）。
func cssDecls(s string) [][2]string {
	var decls [][2]string
	for _, d := range strings.Split(s, ";") {
		name, value, ok := strings.Cut(d, ":")
		if name = strings.TrimSpace(name); ok && name != "" {
			decls = append(decls, [2]string{name, strings.TrimSpace(value)})
		}
	}
	return decls
}

// cssResolve 把 CSS 值里的 var() 换成 vars 里的值或回退值，并算出 color-mix() 的颜色。
func cssResolve(v string, vars map[string]string) string {
	return cssResolveDepth(v, vars, 0)
}

func cssResolveDepth(v string, vars map[string]string, depth int) string {
	if depth > 8 || !strings.Contains(v, "(") {
		return v
	}
	for _, fn := range []string{"var(", "color-mix("} {
		for {
			i := strings.Index(v, fn)
			if i < 0 {
				break
			}
			end := cssClose(v, i+len(fn))
			if end < 0 {
				return v
			}
			args := cssArgs(v[i+len(fn) : end])
			var with string
			if fn == "var(" {
				name := strings.TrimSpace(args[0])
				if val, ok := vars[name]; ok {
					with = cssResolveDepth(val, vars, depth+1)
				} else if len(args) > 1 {
					with = cssResolveDepth(strings.Join(args[1:], ","), vars, depth+1)
				}
			} else {
				with = cssMix(args, vars, depth)
			}
			v = v[:i] + strings.TrimSpace(with) + v[end+1:]
		}
	}
	return v
}

// cssClose 返回 at 之前打开的括号在哪里闭合，没有是 -1。
func cssClose(s string, at int) int {
	depth := 1
	for i := at; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// cssArgs 按逗号拆开 CSS 函数的参数，里面嵌套的函数的逗号不算。
func cssArgs(s string) []string {
	var args []string
	depth, from := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				args = append(args, s[from:i])
				from = i + 1
			}
		}
	}
	return append(args, s[from:])
}

// cssMix 算 color-mix(in srgb, a p%, b q%)。
func cssMix(args []string, vars map[string]string, depth int) string {
	if len(args) != 3 {
		return ""
	}
	color := func(s string) (ui.Color, float32, bool) {
		s = cssResolveDepth(strings.TrimSpace(s), vars, depth+1)
		pct := float32(-1)
		if i := strings.LastIndexByte(s, ' '); i > 0 && strings.HasSuffix(s, "%") {
			if n, err := strconv.ParseFloat(s[i+1:len(s)-1], 32); err == nil {
				pct, s = float32(n)/100, strings.TrimSpace(s[:i])
			}
		}
		c, ok := cssHex(s)
		return c, pct, ok
	}
	a, pa, okA := color(args[1])
	b, pb, okB := color(args[2])
	if !okA || !okB {
		return ""
	}
	switch {
	case pa < 0 && pb < 0:
		pa = 0.5
	case pa < 0:
		pa = 1 - pb
	}
	return hexColor(a.Mix(b, 1-pa))
}

// cssHex 解析 #rgb、#rgba、#rrggbb、#rrggbbaa。
func cssHex(s string) (ui.Color, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return ui.Color{}, false
	}
	h := s[1:]
	if len(h) == 3 || len(h) == 4 {
		var b strings.Builder
		for _, ch := range h {
			b.WriteRune(ch)
			b.WriteRune(ch)
		}
		h = b.String()
	}
	if len(h) != 6 && len(h) != 8 {
		return ui.Color{}, false
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return ui.Color{}, false
	}
	if len(h) == 6 {
		n = n<<8 | 0xff
	}
	return ui.Color{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: uint8(n)}, true
}

// cssParseColor 解析填充色，认不出时是 def。
func cssParseColor(s string, def ui.Color) ui.Color {
	if c, ok := cssHex(s); ok {
		return c
	}
	if c, ok := cssNamed[strings.ToLower(strings.TrimSpace(s))]; ok {
		return c
	}
	if strings.HasPrefix(s, "rgb") {
		if open, end := strings.IndexByte(s, '('), strings.IndexByte(s, ')'); open > 0 && end > open {
			var n []float64
			for _, f := range strings.FieldsFunc(s[open+1:end], func(r rune) bool { return r == ',' || r == ' ' || r == '/' }) {
				v, err := strconv.ParseFloat(strings.TrimSuffix(f, "%"), 64)
				if err != nil {
					return def
				}
				n = append(n, v)
			}
			if len(n) >= 3 {
				alpha := float32(1)
				if len(n) > 3 {
					alpha = float32(n[3])
				}
				return ui.RGBA(uint8(n[0]), uint8(n[1]), uint8(n[2]), alpha)
			}
		}
	}
	return def
}

// cssNamed 是图的 style 里常写的颜色名。
var cssNamed = map[string]ui.Color{
	"black": ui.Hex("#000000"), "white": ui.Hex("#ffffff"), "red": ui.Hex("#ff0000"),
	"green": ui.Hex("#008000"), "blue": ui.Hex("#0000ff"), "yellow": ui.Hex("#ffff00"),
	"orange": ui.Hex("#ffa500"), "purple": ui.Hex("#800080"), "gray": ui.Hex("#808080"),
	"grey": ui.Hex("#808080"), "pink": ui.Hex("#ffc0cb"), "brown": ui.Hex("#a52a2a"),
}

// cssLength 解析像素长度："12"、"12px"、"12.5"。
func cssLength(s string) (float32, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(s), "px")
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(s, 32)
	return float32(n), err == nil
}

// cssBold 判断 font-weight 是不是粗体：bold、bolder 或 600 以上。
func cssBold(v string) bool {
	switch v {
	case "bold", "bolder":
		return true
	}
	n, err := strconv.Atoi(v)
	return err == nil && n >= 600
}
