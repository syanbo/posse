package app

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// 只用 goldmark 的解析器得到语法树，再转成下面这套简单结构缓存起来，每帧按它搭界面。
var mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

type mdKind uint8

const (
	mdPara mdKind = iota
	mdHeading
	mdCode
	mdQuote
	mdList
	mdRule
	mdTable
)

type mdStyle uint8

const (
	mdBold mdStyle = 1 << iota
	mdItalic
	mdStrike
	mdMono
)

type mdInline struct {
	text  string
	style mdStyle
	url   string
}

type mdBlock struct {
	kind    mdKind
	level   int        // 标题级别
	inlines []mdInline // 段落和标题
	code    string     // 代码块
	lang    string     // 代码块的语言（围栏后面写的）
	blocks  []mdBlock  // 引用的内容
	items   []mdItem   // 列表项
	ordered bool
	start   int
	rows    [][][]mdInline // 表格，第一行是表头
	align   []ui.Align
	size    *mdTableSize // 表格量好的列宽，块复制时共用
}

type mdItem struct {
	blocks []mdBlock
	check  int8 // 任务列表：0 不是任务项，1 未完成，2 已完成
}

func parseMarkdown(src string) []mdBlock {
	b := []byte(src)
	return mdBlocks(mdParser.Parse(text.NewReader(b)), b)
}

func mdBlocks(n ast.Node, src []byte) []mdBlock {
	var out []mdBlock
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Paragraph, *ast.TextBlock:
			if ins := mdInlines(c, src); len(ins) > 0 {
				out = append(out, mdBlock{kind: mdPara, inlines: ins})
			}
		case *ast.Heading:
			out = append(out, mdBlock{kind: mdHeading, level: c.Level, inlines: mdInlines(c, src)})
		case *ast.FencedCodeBlock:
			out = append(out, mdBlock{kind: mdCode, code: mdLines(c.Lines(), src), lang: string(c.Language(src))})
		case *ast.CodeBlock:
			out = append(out, mdBlock{kind: mdCode, code: mdLines(c.Lines(), src)})
		case *ast.HTMLBlock:
			// 原样显示，不当 HTML 解释
			code := mdLines(c.Lines(), src)
			if c.HasClosure() {
				code += "\n" + strings.TrimRight(string(c.ClosureLine.Value(src)), "\n")
			}
			out = append(out, mdBlock{kind: mdCode, code: strings.TrimSpace(code)})
		case *ast.Blockquote:
			out = append(out, mdBlock{kind: mdQuote, blocks: mdBlocks(c, src)})
		case *ast.List:
			b := mdBlock{kind: mdList, ordered: c.IsOrdered(), start: c.Start}
			for it := c.FirstChild(); it != nil; it = it.NextSibling() {
				item := mdItem{blocks: mdBlocks(it, src)}
				if p := it.FirstChild(); p != nil {
					if box, ok := p.FirstChild().(*east.TaskCheckBox); ok {
						item.check = 1
						if box.IsChecked {
							item.check = 2
						}
					}
				}
				b.items = append(b.items, item)
			}
			out = append(out, b)
		case *ast.ThematicBreak:
			out = append(out, mdBlock{kind: mdRule})
		case *east.Table:
			b := mdBlock{kind: mdTable, size: &mdTableSize{}}
			for _, a := range c.Alignments {
				b.align = append(b.align, map[east.Alignment]ui.Align{east.AlignRight: ui.End, east.AlignCenter: ui.Center}[a])
			}
			for r := c.FirstChild(); r != nil; r = r.NextSibling() {
				var row [][]mdInline
				for cell := r.FirstChild(); cell != nil; cell = cell.NextSibling() {
					row = append(row, mdInlines(cell, src))
				}
				b.rows = append(b.rows, row)
			}
			out = append(out, b)
		default:
			out = append(out, mdBlocks(c, src)...)
		}
	}
	return out
}

func mdLines(lines *text.Segments, src []byte) string {
	var b strings.Builder
	for i := range lines.Len() {
		seg := lines.At(i)
		b.Write(seg.Value(src))
	}
	return strings.TrimRight(b.String(), "\n")
}

func mdInlines(n ast.Node, src []byte) []mdInline {
	var out []mdInline
	add := func(s string, st mdStyle, url string) {
		if s == "" {
			return
		}
		if k := len(out) - 1; k >= 0 && out[k].style == st && out[k].url == url {
			out[k].text += s
			return
		}
		out = append(out, mdInline{s, st, url})
	}
	var walk func(n ast.Node, st mdStyle, url string)
	walk = func(n ast.Node, st mdStyle, url string) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				add(string(c.Value(src)), st, url)
				// agent 的回复按终端里的样子换行，单个换行也保留
				if c.SoftLineBreak() || c.HardLineBreak() {
					add("\n", st, url)
				}
			case *ast.String:
				add(string(c.Value), st, url)
			case *ast.CodeSpan:
				walk(c, st|mdMono, url)
			case *ast.Emphasis:
				if c.Level >= 2 {
					walk(c, st|mdBold, url)
				} else {
					walk(c, st|mdItalic, url)
				}
			case *east.Strikethrough:
				walk(c, st|mdStrike, url)
			case *ast.Link:
				walk(c, st, string(c.Destination))
			case *ast.Image:
				walk(c, st|mdItalic, string(c.Destination))
			case *ast.AutoLink:
				add(string(c.Label(src)), st, string(c.URL(src)))
			case *ast.RawHTML:
				for i := range c.Segments.Len() {
					seg := c.Segments.At(i)
					add(string(seg.Value(src)), st, url)
				}
			case *east.TaskCheckBox:
				// 列表项前面单独画勾选框
			default:
				walk(c, st, url)
			}
		}
	}
	walk(n, 0, "")
	if k := len(out) - 1; k >= 0 {
		out[k].text = strings.TrimRight(out[k].text, "\n")
	}
	return out
}

// mdView 把解析好的 Markdown 搭成界面，words 是要标出来的搜索词。
type mdView struct {
	words []string
	depth int // 列表嵌套层数
}

func (r mdView) blocks(c *ui.Context, bs []mdBlock) {
	for _, b := range bs {
		r.block(c, b)
	}
}

func (r mdView) block(c *ui.Context, b mdBlock) {
	t := c.Theme()
	switch b.kind {
	case mdPara:
		r.inline(c, b.inlines).FontSize(fsBody).LineHeight(1.6)
	case mdHeading:
		size := map[int]float32{1: fsPage, 2: fsTitle}[b.level]
		if size == 0 {
			size = fsBody
		}
		r.inline(c, b.inlines).FontSize(size).FontWeight(fwBold).LineHeight(1.35).Padding(4, 0, 0, 0)
	case mdCode:
		// 上面一条语言栏，右边是复制按钮
		ui.Column(c).Radius(rCard).Background(codeBackground(t)).Clip().Children(func() {
			ui.Row(c).Height(sz(28)).Padding(0, 4, 0, 12).AlignItems(ui.Center).BorderWidth(0, 0, hairline, 0).BorderColor(colors(t).line).Children(func() {
				lang := b.lang
				if lang == "" {
					lang = "代码"
				}
				ui.Text(c, lang).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
				ui.Spacer(c)
				if pressable(iconButtonSized(c.Key("copy"), iconCopy, "复制代码", compactBox(), smallIcon())).Clicked() {
					c.WriteClipboard(b.code)
				}
			})
			ui.Box(c).Padding(8, 12, 12, 12).Children(func() {
				highlighted(c, b.code, r.words).Font(monoFont).FontSize(fsSmall).LineHeight(1.5)
			})
		})
	case mdQuote:
		ui.Row(c).AlignItems(ui.Stretch).Gap(8).Children(func() {
			ui.Box(c).Width(3).Shrink(0).Radius(full(3)).Background(colors(t).line)
			ui.Column(c).Grow(1).Shrink(1).Gap(8).TextColor(t.TextMuted).Children(func() { r.blocks(c, b.blocks) })
		})
	case mdList:
		width := float32(14)
		if b.ordered {
			width = float32(len(fmt.Sprint(b.start+len(b.items)-1))*8 + 8)
		}
		inner := r
		inner.depth++
		ui.Column(c).Gap(4).Children(func() {
			for i, it := range b.items {
				ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
					marker := []string{"•", "◦", "▪"}[min(r.depth, 2)]
					switch {
					case it.check == 1:
						marker = "☐"
					case it.check == 2:
						marker = "☑"
					case b.ordered:
						marker = fmt.Sprintf("%d.", b.start+i)
					}
					m := ui.Text(c, marker).FontSize(fsBody).LineHeight(1.55).TextColor(t.TextMuted).Width(width).Shrink(0).Unselectable()
					if b.ordered {
						m.FontFeatures("tnum").TextAlign(ui.End)
					}
					ui.Column(c).Grow(1).Shrink(1).Gap(4).Children(func() { inner.blocks(c, it.blocks) })
				})
			}
		})
	case mdRule:
		hline(c).Margin(6, 0)
	case mdTable:
		r.table(c, b)
	}
}

// 表格一个格子的左右内边距 24 加右边框 1，再留 1 的余量：列宽取整后比量到的文字窄一点，文字就折行了
const mdCellPad = 26

// mdTableSize 是表格每列量好的宽度：最小宽是最长的拆不开的词，最大宽是不折行。
// 第一次搭的时候量一次，存在块上；设置里换了界面字号、字重（key）再重量。
type mdTableSize struct {
	min, max []float32
	key      [3]float32
}

// table 搭表格。横向滚动容器给内容的宽度没有上限，表格会按最宽排、长句不再折行，
// 所以列宽自己定：量出每列不折行的宽度和最长的拆不开的词，拿上一帧量到的容器宽度当基准，
// 放得下就按内容宽排；长句折行能放下的，按各列能收缩的余地分掉差额，刚好铺满；
// 连拆不开的词都放不下的，长句的列最宽 360、短的按内容，整张左右滚。
func (r mdView) table(c *ui.Context, b mdBlock) {
	t := c.Theme()
	cols := 0
	for _, row := range b.rows {
		cols = max(cols, len(row))
	}
	size := b.size
	if key := [3]float32{fsBody, float32(fwMedium), float32(fwBold)}; size.min == nil || size.key != key {
		r.measure(c, b, cols)
		size.key = key
	}
	var sumMin, sumMax float32 = 2, 2 // 外框
	for i := range cols {
		sumMin += size.min[i]
		sumMax += size.max[i]
	}
	scroll := ui.ScrollHorizontal(c)
	avail := scroll.LayoutBounds().W
	tracks := make([]ui.Track, cols)
	wide := false
	switch {
	case avail > 0 && sumMax <= avail:
		for i := range tracks {
			tracks[i] = ui.FitContent()
		}
	case avail > 0 && sumMin <= avail:
		for i := range tracks {
			w := size.min[i] + (avail-sumMin)*(size.max[i]-size.min[i])/(sumMax-sumMin)
			tracks[i] = ui.Fixed(w)
		}
	default:
		// 第一帧还不知道容器宽，也先按这样排：放得下的表格和按内容排一样，宽的不会先按最宽闪一下
		wide = avail > 0
		for i := range tracks {
			tracks[i] = ui.Fixed(max(size.min[i], min(size.max[i], 360)))
		}
	}
	if wide {
		scroll.Padding(0, 0, 12, 0) // 滚动条画在表格下面，不压着最后一行
	}
	scroll.Children(func() {
		ui.Grid(c).ColumnTracks(tracks...).Border(hairline, colors(t).line).Radius(rRow).Children(func() { r.cells(c, b, cols) })
	})
}

// measure 量表格每列的最小宽和最大宽。最小宽按空格和每个汉字拆开量，和文字排版的断行处一致；
// 汉字都一样宽，每种样式只量一个。
func (r mdView) measure(c *ui.Context, b mdBlock, cols int) {
	size := b.size
	size.min, size.max = make([]float32, cols), make([]float32, cols)
	cjk := map[ui.Span]float32{}
	for ri, row := range b.rows {
		for ci, cell := range row {
			spans := cellSpans(c, cell, ri == 0)
			w, _ := c.MeasureText(0, spans...)
			size.max[ci] = max(size.max[ci], w+mdCellPad)
			for _, sp := range spans {
				for _, word := range strings.Fields(sp.Text) {
					var run []rune
					flush := func() {
						if len(run) > 0 {
							sp.Text = string(run)
							w, _ := c.MeasureText(0, sp)
							size.min[ci] = max(size.min[ci], w+mdCellPad)
							run = run[:0]
						}
					}
					for _, ch := range word {
						if !wideRune(ch) {
							run = append(run, ch)
							continue
						}
						flush()
						key := sp
						key.Text = ""
						w, ok := cjk[key]
						if !ok {
							sp.Text = "中"
							w, _ = c.MeasureText(0, sp)
							cjk[key] = w
						}
						size.min[ci] = max(size.min[ci], w+mdCellPad)
					}
					flush()
				}
			}
		}
	}
}

// cellSpans 是格子里的文字按搭界面时的字号和字重摆出来的 spans，量宽度用；搜索词的底色不影响宽度，不带。
func cellSpans(c *ui.Context, cell []mdInline, header bool) []ui.Span {
	var spans []ui.Span
	for _, in := range cell {
		for _, sp := range (mdView{}).spans(c, in) {
			if sp.Size == 0 {
				sp.Size = fsBody
			}
			if header && sp.Weight == 0 {
				sp.Weight = fwMedium
			}
			spans = append(spans, sp)
		}
	}
	return spans
}

// wideRune 是排版时前后都能断行的全角字：汉字、假名、谚文和全角标点。
func wideRune(ch rune) bool {
	return unicode.Is(unicode.Han, ch) || unicode.Is(unicode.Hiragana, ch) || unicode.Is(unicode.Katakana, ch) ||
		unicode.Is(unicode.Hangul, ch) || ch >= 0x3000 && ch <= 0x303F || ch >= 0xFF00 && ch <= 0xFF60
}

// cells 搭表格的格子：第一行是表头，四角的格子跟着外框圆角。
func (r mdView) cells(c *ui.Context, b mdBlock, cols int) {
	t := c.Theme()
	for ri, row := range b.rows {
		for ci := range cols {
			var cell []mdInline
			if ci < len(row) {
				cell = row[ci]
			}
			right, bottom := float32(1), float32(1)
			if ci == cols-1 {
				right = 0
			}
			if ri == len(b.rows)-1 {
				bottom = 0
			}
			box := ui.Box(c).Padding(4, 12).BorderWidth(0, right, bottom, 0).BorderColor(colors(t).line)
			if ri == 0 {
				box.Background(t.Surface)
			}
			var rad [4]float32
			first, last := ri == 0, ri == len(b.rows)-1
			if ci == 0 {
				rad[0], rad[3] = corner(first), corner(last)
			}
			if ci == cols-1 {
				rad[1], rad[2] = corner(first), corner(last)
			}
			if rad != [4]float32{} {
				box.Radius(rad[0], rad[1], rad[2], rad[3])
			}
			box.Children(func() {
				e := r.inline(c, cell).FontSize(fsBody).LineHeight(1.45)
				if ri == 0 {
					e.FontWeight(fwMedium)
				}
				if ci < len(b.align) && b.align[ci] != 0 {
					e.TextAlign(b.align[ci])
				}
			})
		}
	}
}

// corner 是四角格子的圆角：外框圆角 6 减去 1 的边框。
func corner(on bool) float32 {
	if on {
		return 5
	}
	return 0
}

// inline 把一段行内内容搭成一个段落；只有 http 链接能点，相对路径之类的只上色。
func (r mdView) inline(c *ui.Context, ins []mdInline) ui.Element {
	for _, in := range ins {
		if webURL(in.url) {
			return ui.RichText(c).Children(func() {
				for _, in := range ins {
					if webURL(in.url) {
						ui.Link(c, in.text, in.url)
					} else {
						ui.RichText(c, r.spans(c, in)...)
					}
				}
			})
		}
	}
	var spans []ui.Span
	for _, in := range ins {
		spans = append(spans, r.spans(c, in)...)
	}
	return ui.RichText(c, spans...)
}

func (r mdView) spans(c *ui.Context, in mdInline) []ui.Span {
	t := c.Theme()
	base := ui.Span{}
	if in.style&mdBold != 0 {
		base.Weight = fwBold
	}
	if in.style&mdItalic != 0 {
		base.Italic = true
	}
	if in.style&mdStrike != 0 {
		base.Strikethrough = true
	}
	if in.style&mdMono != 0 {
		base.Font, base.Size, base.Background = monoFont, fsSmall, codeBackground(t)
	}
	if in.url != "" {
		base.Color = t.Accent
	}
	return markSpans(in.text, markRanges(in.text, r.words), base, colors(t).mark)
}

func webURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

func codeBackground(t *ui.Theme) ui.Color {
	return colors(t).code
}
