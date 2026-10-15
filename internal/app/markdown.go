package app

import (
	"fmt"
	"strings"

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
	blocks  []mdBlock  // 引用的内容
	items   []mdItem   // 列表项
	ordered bool
	start   int
	rows    [][][]mdInline // 表格，第一行是表头
	align   []ui.Align
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
			out = append(out, mdBlock{kind: mdCode, code: mdLines(c.Lines(), src)})
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
			b := mdBlock{kind: mdTable}
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
		r.inline(c, b.inlines).FontSize(13).LineHeight(1.55)
	case mdHeading:
		size := map[int]float32{1: 17, 2: 15.5, 3: 14}[b.level]
		if size == 0 {
			size = 13
		}
		r.inline(c, b.inlines).FontSize(size).FontWeight(600).LineHeight(1.35).Padding(4, 0, 0, 0)
	case mdCode:
		ui.Box(c).Padding(8, 12).Radius(6).Background(codeBackground(t)).Children(func() {
			highlighted(c, b.code, r.words).Font(monoFont).FontSize(12).LineHeight(1.5)
		})
	case mdQuote:
		ui.Row(c).AlignItems(ui.Stretch).Gap(10).Children(func() {
			ui.Box(c).Width(3).Shrink(0).Radius(1.5).Background(t.Border)
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
					m := ui.Text(c, marker).FontSize(13).LineHeight(1.55).TextColor(t.TextMuted).Width(width).Shrink(0).Unselectable()
					if b.ordered {
						m.FontFeatures("tnum").TextAlign(ui.End)
					}
					ui.Column(c).Grow(1).Shrink(1).Gap(4).Children(func() { inner.blocks(c, it.blocks) })
				})
			}
		})
	case mdRule:
		ui.Box(c).Height(1).Background(t.Border).Margin(6, 0)
	case mdTable:
		cols := 0
		for _, row := range b.rows {
			cols = max(cols, len(row))
		}
		tracks := make([]ui.Track, cols)
		for i := range tracks {
			tracks[i] = ui.FitContent()
		}
		ui.Grid(c).ColumnTracks(tracks...).AlignSelf(ui.Start).Border(1, t.Border).Radius(6).Clip().Children(func() {
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
					box := ui.Box(c).Padding(5, 10).BorderWidth(0, right, bottom, 0).BorderColor(t.Border)
					if ri == 0 {
						box.Background(t.Surface)
					}
					box.Children(func() {
						e := r.inline(c, cell).FontSize(12.5).LineHeight(1.45)
						if ri == 0 {
							e.FontWeight(600)
						}
						if ci < len(b.align) && b.align[ci] != 0 {
							e.TextAlign(b.align[ci])
						}
					})
				}
			}
		})
	}
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
		base.Weight = 600
	}
	if in.style&mdItalic != 0 {
		base.Italic = true
	}
	if in.style&mdStrike != 0 {
		base.Strikethrough = true
	}
	if in.style&mdMono != 0 {
		base.Font, base.Size, base.Background = monoFont, 12, codeBackground(t)
	}
	if in.url != "" {
		base.Color = t.Accent
	}
	return markSpans(in.text, markRanges(in.text, r.words), base, colors(t).honey.Alpha(0.28))
}

func webURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

func codeBackground(t *ui.Theme) ui.Color {
	return t.Text.Alpha(0.06)
}
