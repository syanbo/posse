// Package mermaid 把 Mermaid 源码排成图：Pele（Obsidian 开源的 Mermaid 渲染器）在 QuickJS 里排版、出 SVG，
// 这里把 SVG 的颜色换成主题色，再把文字抽出来，交给调用方用自己的字体画：桌面的 mygo 不画 SVG 里的文字，
// 手机端也要用手机的字体。量文字宽度的函数由调用方给，排出来的标签和它画的字一致。不依赖 mygo，手机的 Go 库也用它。
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

	"modernc.org/quickjs"
)

// peleJS 由 scripts/pele.sh 打包，运行后设置全局变量 Pele。
//
//go:embed pele.js.gz
var peleJS []byte

// Color 是 sRGB 颜色，A 是不透明度。
type Color struct{ R, G, B, A uint8 }

func rgba(r, g, b uint8, alpha float32) Color { return Color{r, g, b, alphaByte(alpha)} }

func alphaByte(a float32) uint8 { return uint8(max(0, min(a, 1))*255 + 0.5) }

// ParseHex 解析 #rgb、#rgba、#rrggbb、#rrggbbaa，认不出时是透明黑和 false。
func ParseHex(s string) (Color, bool) { return cssHex(s) }

func hex(s string) Color {
	c, ok := cssHex(s)
	if !ok {
		panic("mermaid: bad color " + s)
	}
	return c
}

// Alpha 把不透明度乘上 a。
func (c Color) Alpha(a float32) Color {
	c.A = alphaByte(float32(c.A) / 255 * a)
	return c
}

// Mix 是 c 和 o 按 t 混合（0 是 c，1 是 o）。
func (c Color) Mix(o Color, t float32) Color {
	t = max(0, min(t, 1))
	m := func(a, b uint8) uint8 { return uint8(float32(a)*(1-t) + float32(b)*t + 0.5) }
	return Color{m(c.R, o.R), m(c.G, o.G), m(c.B, o.B), m(c.A, o.A)}
}

// Over 是 c 叠在不透明的 base 上的颜色。
func (c Color) Over(base Color) Color {
	o := c.Mix(base, 1-float32(c.A)/255)
	o.A = base.A
	return o
}

// Style 是画图用的颜色，Pele 的 CSS 变量取这些值。都要不透明：Pele 拿它们混色。
type Style struct {
	Bg, Fg, Muted, Line, Surface, SurfaceAlt, Border, Accent, Danger Color
}

// 标签文字的样式位：Pele 量文字时给的，也是 Label.Style。
const (
	Bold   = 1
	Italic = 2
	Mono   = 4
	Sub    = 8
	Sup    = 16
)

// Smaller 是上下标相对正文的字号：Sub、Sup 的文字按 size*Smaller 量和画。
const Smaller = 0.8

// Measurer 量一段文字的宽度（size 是 Pele 给的字号，style 是样式位）。Key 标识量法（字体、字重），
// 变了 Pele 要忘掉量过的宽度。
type Measurer struct {
	Key   string
	Width func(text string, size float32, style int) float32
}

// Layout 是排好的图：Shapes 是不带文字的 SVG（颜色已换成 Style 的），W×H 是它的尺寸，Labels 是要另画的文字。
type Layout struct {
	Shapes []byte
	W, H   float32
	Labels []Label
}

// Label 是标签里一段同样式的文字：基线的起点在图的 X, Y，绕这一点转 Angle 度。
type Label struct {
	Text  string
	Size  float32
	Style int
	X, Y  float32
	Angle float32
	Color Color
}

// timeout 是一张图最多画多久。
const timeout = 5 * time.Second

// pele 是运行 Pele 的 JS 运行时（QuickJS，和 sqlite 共用 modernc 的 libc），画第一张图时启动（约 90 ms、11 MB），
// 一次画一张。启动后一直留着：modernc 的分配器把释放的页留着复用、没有还给系统的接口，关掉省不下内存，反复开关反而越占越多。
var pele struct {
	sync.Mutex
	vm  *quickjs.VM
	err error
	// measure 是正在画的图的量法；measured 是 Pele 记着的那些宽度用的量法。
	measure  Measurer
	measured string
}

// renderJS 是 posseRender(source, fresh)：用调用方的量法排出 SVG，fresh 时先忘掉量过的宽度；
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
		return float64(pele.measure.Width(text, float32(number(size)), int(number(style))))
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
	pele.vm, pele.measured = vm, ""
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

// peleRender 用 m 量文字，把 source 画成 SVG，或者说出哪里写错了。
func peleRender(source string, m Measurer) (string, error) {
	pele.Lock()
	defer pele.Unlock()
	if pele.vm == nil && pele.err == nil {
		pele.err = start()
	}
	if pele.err != nil {
		return "", pele.err
	}
	pele.measure = m
	fresh := m.Key != pele.measured
	pele.measured = m.Key
	// 画完放掉量法：它可能带着整张字宽表（手机），不该跟着常驻的运行时留到下一张
	defer func() { pele.measure = Measurer{} }()
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

// Render 按 st 的颜色、m 的量法排出 source 的图，或者说出哪里写错了。任何协程都能调，同一时间只画一张。
func Render(source string, st Style, m Measurer) (*Layout, error) {
	svg, err := peleRender(source, m)
	if err != nil {
		return nil, err
	}
	shapes, chunks, err := toSVG(svg, st)
	if err != nil {
		return nil, err
	}
	l := &Layout{Shapes: shapes}
	if l.W, l.H = svgSize(shapes); l.W <= 0 || l.H <= 0 {
		return nil, errors.New("图的尺寸为空")
	}
	for _, c := range chunks {
		l.Labels = append(l.Labels, c.shape(m)...)
	}
	return l, nil
}

var (
	svgRoot   = regexp.MustCompile(`<svg[^>]*>`)
	svgWidth  = regexp.MustCompile(`\swidth="([^"]*)"`)
	svgHeight = regexp.MustCompile(`\sheight="([^"]*)"`)
	svgView   = regexp.MustCompile(`\sviewBox="([^"]*)"`)
)

// svgSize 是根元素的 width、height，没有时取 viewBox 的宽高。
func svgSize(shapes []byte) (float32, float32) {
	root := svgRoot.Find(shapes)
	w, okW := attrLength(svgWidth, root)
	h, okH := attrLength(svgHeight, root)
	if okW && okH {
		return w, h
	}
	if m := svgView.FindSubmatch(root); m != nil {
		if f := strings.FieldsFunc(string(m[1]), func(r rune) bool { return r == ',' || r == ' ' }); len(f) == 4 {
			w, errW := strconv.ParseFloat(f[2], 32)
			h, errH := strconv.ParseFloat(f[3], 32)
			if errW == nil && errH == nil {
				return float32(w), float32(h)
			}
		}
	}
	return 0, 0
}

func attrLength(re *regexp.Regexp, root []byte) (float32, bool) {
	m := re.FindSubmatch(root)
	if m == nil {
		return 0, false
	}
	return cssLength(string(m[1]))
}

// run 是标签里一段同样式的文字。
type run struct {
	text  string
	size  float32
	style int
	color Color
}

// chunk 是标签里从 x, y（基线）起接连排的几段文字，按 anchor（start、middle、end）对齐，m 把它放进图里。
type chunk struct {
	m      affine
	x, y   float32
	anchor string
	runs   []run
}

// shape 用 m 量出每段的宽度，按对齐方式排好，放到图里。
func (t chunk) shape(m Measurer) []Label {
	type piece struct {
		run
		x, shift float32
	}
	var pieces []piece
	pen := float32(0)
	for _, r := range t.runs {
		if r.text == "" {
			continue
		}
		shift := float32(0)
		switch {
		case r.style&Sub != 0:
			shift = r.size * 0.2
		case r.style&Sup != 0:
			shift = -r.size * 0.35
		}
		pieces = append(pieces, piece{r, pen, shift})
		pen += m.Width(r.text, r.size, r.style)
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
	labels := make([]Label, 0, len(pieces))
	for _, p := range pieces {
		x, y := t.m.apply(left+p.x, t.y+p.shift)
		labels = append(labels, Label{Text: p.text, Size: p.size, Style: p.style, X: x, Y: y, Angle: angle, Color: p.color})
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

func hexColor(c Color) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

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
	under   Color
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
					u := cssParseColor(resolve(d[1]), Color{})
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
				f.mono = strings.Contains(v, "mono")
			}
			if v, ok := prop("text-anchor"); ok {
				f.anchor = v
			}
			if v, ok := prop("baseline-shift"); ok {
				switch v {
				case "sub":
					f.shift = Sub
				case "super":
					f.shift = Sup
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
				style |= Bold
			}
			if f.italic {
				style |= Italic
			}
			if f.mono {
				style |= Mono
			}
			text.runs = append(text.runs, run{text: s, size: f.size, style: style, color: f.color(st)})
		}
	}
	return out.Bytes(), chunks, nil
}

// labelContrast 是标签和 style 给形状的填充色之间至少要有的对比度，不够就换颜色。
const labelContrast = 3

// color 是文字的颜色：它的填充色；形状被 style 填了一个和它对比不够的颜色时，取主题的文字色和底色里更清楚的那个。
func (f frame) color(st Style) Color {
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
func contrast(x, y Color) float64 {
	lum := func(c Color) float64 {
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
	color := func(s string) (Color, float32, bool) {
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
func cssHex(s string) (Color, bool) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "#") {
		return Color{}, false
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
		return Color{}, false
	}
	n, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return Color{}, false
	}
	if len(h) == 6 {
		n = n<<8 | 0xff
	}
	return Color{R: uint8(n >> 24), G: uint8(n >> 16), B: uint8(n >> 8), A: uint8(n)}, true
}

// cssParseColor 解析填充色，认不出时是 def。
func cssParseColor(s string, def Color) Color {
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
				return rgba(uint8(n[0]), uint8(n[1]), uint8(n[2]), alpha)
			}
		}
	}
	return def
}

// cssNamed 是图的 style 里常写的颜色名。
var cssNamed = map[string]Color{
	"black": hex("#000000"), "white": hex("#ffffff"), "red": hex("#ff0000"),
	"green": hex("#008000"), "blue": hex("#0000ff"), "yellow": hex("#ffff00"),
	"orange": hex("#ffa500"), "purple": hex("#800080"), "gray": hex("#808080"),
	"grey": hex("#808080"), "pink": hex("#ffc0cb"), "brown": hex("#a52a2a"),
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
