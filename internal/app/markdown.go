package app

import (
	"cmp"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"posse/internal/errtext"
	"posse/internal/mermaid/diagram"
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
	mdMeta
	mdCells
	mdDetails
)

type mdStyle uint8

const (
	mdBold mdStyle = 1 << iota
	mdItalic
	mdStrike
	mdMono
	mdKbd   // <kbd>：同行内代码
	mdSmall // <small>：90%
	mdSup   // <sup>、<sub>：75%，画不了上下标，只缩小
)

type mdInline struct {
	text  string
	style mdStyle
	url   string
	img   *mdImg // 图片（文件预览）：text 为空，url 是包着它的链接
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
	meta    []mdField    // 文件开头的 YAML 头部
	// 以下只出自文件预览里的 HTML（markdown_html.go）
	textAlign ui.Align // 段落、标题的对齐（align="center" 等）
	grid      *mdGrid  // HTML 表格：格子里是任意的块
	open      *bool    // <details> 是否展开，inlines 是 <summary>，blocks 是内容；块复制时共用
}

// mdField 是 YAML 头部的一项，depth 是缩进层数（嵌套的键）。
type mdField struct {
	depth      int
	key, value string
}

type mdItem struct {
	blocks []mdBlock
	check  int8 // 任务列表：0 不是任务项，1 未完成，2 已完成
}

// parseMarkdown 解析历史页、任务页里 agent 写的 Markdown：其中的 HTML 照原文显示，agent 在正文里提到的标签不会被吃掉。
func parseMarkdown(src string) []mdBlock { return parseMarkdownWith(src, false) }

// parseMarkdownDoc 解析文件预览的 Markdown：照 GitHub 解释其中的 HTML（README 常用它居中、排图、做表格）。
func parseMarkdownDoc(src string) []mdBlock { return parseMarkdownWith(src, true) }

func parseMarkdownWith(src string, html bool) []mdBlock {
	meta, body := frontMatter(src)
	b := []byte(body)
	out := mdConv{src: b, html: html}.blocks(mdParser.Parse(text.NewReader(b)))
	if len(meta) > 0 {
		out = append([]mdBlock{{kind: mdMeta, meta: meta}}, out...)
	}
	return out
}

// frontMatter 摘出开头 --- 围起来的 YAML 头部（agent 的记忆、技能和静态网站的文章都这样写），返回各项和剩下的正文。
// goldmark 不认它：第一个 --- 成了分割线，中间几行连同第二个 --- 成了二级标题。
// 只认简单的写法：键: 值、嵌套的键、列表项和折行的值接在上一项后面；认不出来的照原文交给 goldmark。
func frontMatter(src string) ([]mdField, string) {
	rest, ok := strings.CutPrefix(strings.TrimPrefix(src, "\ufeff"), "---\n")
	if !ok {
		rest, ok = strings.CutPrefix(src, "---\r\n")
	}
	if !ok {
		return nil, src
	}
	var meta []mdField
	for rest != "" {
		var line string
		line, rest, _ = strings.Cut(rest, "\n")
		line = strings.TrimRight(line, "\r")
		if line == "---" || line == "..." {
			return meta, rest
		}
		item := strings.TrimLeft(line, " ")
		if item == "" || strings.HasPrefix(item, "#") {
			continue
		}
		key, value, kv := strings.Cut(item, ":")
		if kv && (value == "" || value[0] == ' ') && key != "" && !strings.ContainsAny(key, " \t\"'") {
			meta = append(meta, mdField{depth: (len(line) - len(item)) / 2, key: key, value: yamlScalar(value)})
			continue
		}
		if len(meta) == 0 {
			return nil, src
		}
		last := &meta[len(meta)-1]
		item = yamlScalar(strings.TrimPrefix(item, "- "))
		if last.value == "" {
			last.value = item
		} else {
			last.value += ", " + item
		}
	}
	return nil, src
}

// yamlScalar 去掉 YAML 值的引号；双引号里的转义照 Go 的规则解开，解不开的原样留着。
func yamlScalar(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		if s, err := strconv.Unquote(v); err == nil {
			return s
		}
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}

// mdConv 把 goldmark 的语法树转成 mdBlock；html 为真时解释其中的 HTML（见 markdown_html.go）。
type mdConv struct {
	src  []byte
	html bool
}

func (m mdConv) blocks(n ast.Node) []mdBlock {
	if m.html {
		return m.htmlBlocks(n)
	}
	var out []mdBlock
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, m.block(c)...)
	}
	return out
}

// block 转换一个块节点，HTML 块照原文当代码块。
func (m mdConv) block(c ast.Node) []mdBlock {
	src := m.src
	var out []mdBlock
	switch c := c.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		if ins := m.inlines(c); len(ins) > 0 {
			out = append(out, mdBlock{kind: mdPara, inlines: ins})
		}
	case *ast.Heading:
		out = append(out, mdBlock{kind: mdHeading, level: c.Level, inlines: m.inlines(c)})
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
		out = append(out, mdBlock{kind: mdQuote, blocks: m.blocks(c)})
	case *ast.List:
		b := mdBlock{kind: mdList, ordered: c.IsOrdered(), start: c.Start}
		for it := c.FirstChild(); it != nil; it = it.NextSibling() {
			item := mdItem{blocks: m.blocks(it)}
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
				row = append(row, m.inlines(cell))
			}
			b.rows = append(b.rows, row)
		}
		out = append(out, b)
	default:
		out = append(out, m.blocks(c)...)
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

func (m mdConv) inlines(n ast.Node) []mdInline {
	src := m.src
	var out []mdInline
	// 文件预览里段落中的行内标签（<b>、<a href>、<img> 等）：开着的标签叠在后面的文字上
	var tags []inlineTag
	add := func(s string, st mdStyle, url string) {
		if s == "" {
			return
		}
		st, url = tagStyle(tags, st, url)
		if k := len(out) - 1; k >= 0 && out[k].style == st && out[k].url == url {
			out[k].text += s
			return
		}
		out = append(out, mdInline{text: s, style: st, url: url})
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
				if m.html {
					// 图片后面紧跟的文字不和它合并
					out = append(out, mdInline{img: &mdImg{src: string(c.Destination), alt: plainText(c, src), title: string(c.Title)}, url: url})
					out = append(out, mdInline{})
					continue
				}
				walk(c, st|mdItalic, string(c.Destination))
			case *ast.AutoLink:
				add(string(c.Label(src)), st, string(c.URL(src)))
			case *ast.RawHTML:
				if m.html {
					var raw strings.Builder
					for i := range c.Segments.Len() {
						seg := c.Segments.At(i)
						raw.Write(seg.Value(src))
					}
					img, brk := inlineHTML(raw.String(), &tags)
					if img != nil {
						_, u := tagStyle(tags, st, url)
						out = append(out, mdInline{img: img, url: u}, mdInline{})
					}
					if brk {
						add("\n", st, url)
					}
					continue
				}
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
	return wikiLinks(trimInlines(out))
}

// wikiRe 认 [[名字]]、[[名字|显示的字]]（Obsidian 和 agent 记忆里互相引用的写法）。
var wikiRe = regexp.MustCompile(`\[\[([^\[\]|\n]+)(?:\|([^\[\]\n]+))?\]\]`)

// wikiLinks 把普通文字里的 [[名字]] 拆成链接，url 记成 "wiki:名字"；goldmark 把它当没定义的引用，原样留在文字里。
func wikiLinks(ins []mdInline) []mdInline {
	var out []mdInline
	for _, in := range ins {
		if in.url != "" || in.style&mdMono != 0 || !strings.Contains(in.text, "[[") {
			out = append(out, in)
			continue
		}
		last := 0
		for _, m := range wikiRe.FindAllStringSubmatchIndex(in.text, -1) {
			if m[0] > last {
				out = append(out, mdInline{text: in.text[last:m[0]], style: in.style})
			}
			target := strings.TrimSpace(in.text[m[2]:m[3]])
			label := target
			if m[4] >= 0 {
				label = strings.TrimSpace(in.text[m[4]:m[5]])
			}
			out = append(out, mdInline{text: label, style: in.style, url: "wiki:" + target})
			last = m[1]
		}
		if last < len(in.text) {
			out = append(out, mdInline{text: in.text[last:], style: in.style})
		}
	}
	return out
}

// mdView 把解析好的 Markdown 搭成界面，words 是要标出来的搜索词。
type mdView struct {
	words []string
	depth int                 // 列表嵌套层数
	find  *docFind            // 文件预览的查找栏：命中按搭界面的顺序编号，当前那个换深底色、滚进视野
	size  float32             // 正文字号（设置里的 Markdown 字号），0 跟界面字号；标题、代码按它的倍数
	text  float32             // 当前这段文字的字号（标题里是标题字号），行内代码这类按它的倍数；0 是正文
	wiki  func(target string) // 点 [[名字]] 时打开它；为空时只上色、点不了
	imgs  *mdImages           // 文件预览载入图片；为空时图片只显示说明文字
}

// 排版照 GitHub 的 Markdown 样式（github-markdown-css）：那里正文 16px，其余字号、间距多写成正文的倍数，
// 这里照比例换到正文字号上（em），相当于 GitHub 整体按正文字号 / 16 缩放。

// em 把 GitHub 样式里正文 16px 时的尺寸换到当前正文字号上。
func (r mdView) em(px float32) float32 { return px * r.body() / 16 }

// gapEm 是块间距这类要对齐像素的尺寸。
func (r mdView) gapEm(px float32) float32 { return float32(math.Round(float64(r.em(px)))) }

// lead 是正文的行高，列表的点、序号按它对齐第一行。
func (r mdView) lead() float32 { return 1.5 }

// mono 是代码块的字号：正文的 85%（行内代码按所在文字的 85%，见 spans）。
func (r mdView) mono() float32 { return r.body() * 0.85 }

// page 搭一串块，块与块之间照 GitHub：一般空 16，标题上面和分割线上下空 24；不看源文件里的空行。
func (r mdView) page(c *ui.Context, bs []mdBlock) {
	for i, b := range bs {
		if i > 0 {
			gap := r.gapEm(16)
			if b.kind == mdHeading || b.kind == mdRule || bs[i-1].kind == mdRule {
				gap = r.gapEm(24)
			}
			ui.Box(c).Height(gap).Shrink(0)
		}
		r.block(c, b)
	}
}

func (r mdView) body() float32  { return cmp.Or(r.size, fsBody) }
func (r mdView) small() float32 { s, _, _, _ := typeScale(r.body()); return s }

// headingScale 是各级标题对正文的倍数（GitHub 的 2em、1.5em、1.25em、1em、.875em、.85em）。
var headingScale = [...]float32{2, 1.5, 1.25, 1, 0.875, 0.85}

func (r mdView) heading(level int) float32 {
	return r.body() * headingScale[min(max(level, 1), 6)-1]
}

// textSize 是当前这段文字的字号。
func (r mdView) textSize() float32 { return cmp.Or(r.text, r.body()) }

func (r mdView) block(c *ui.Context, b mdBlock) {
	t := c.Theme()
	switch b.kind {
	case mdPara:
		if hasImage([]mdBlock{b}) {
			r.media(c, b)
			return
		}
		e := r.inline(c, b.inlines).FontSize(r.body()).LineHeight(r.lead())
		if b.textAlign != 0 {
			e.TextAlign(b.textAlign)
		}
	case mdHeading:
		r.headingBlock(c, b)
	case mdCells:
		r.grid(c, b.grid)
	case mdDetails:
		r.details(c, b)
	case mdCode:
		r.codeBlock(c, b)
	case mdQuote:
		// 左边一条 .25em 的竖线，内容隔 1em、用次要色
		ui.Row(c).AlignItems(ui.Stretch).Gap(r.gapEm(16)).Children(func() {
			ui.Box(c).Width(r.em(4)).Shrink(0).Background(colors(t).line)
			ui.Column(c).Grow(1).Shrink(1).TextColor(t.TextMuted).Children(func() { r.page(c, b.blocks) })
		})
	case mdList:
		r.list(c, b)
	case mdMeta:
		// 照 Writer 的属性面板：左边一列键用次要色，右边是值，都比正文小一号
		ui.Column(c).Gap(6).Children(func() {
			for _, f := range b.meta {
				ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
					r.inline(c, []mdInline{{text: f.key}}).FontSize(r.small()).LineHeight(1.5).TextColor(t.TextMuted).
						Width(r.gapEm(146)).Shrink(0).Padding(0, 0, 0, float32(f.depth)*treeIndent)
					r.inline(c, []mdInline{{text: f.value}}).FontSize(r.small()).LineHeight(1.5).Grow(1).Shrink(1)
				})
			}
		})
	case mdRule:
		// GitHub 的分割线是一条 .25em 高的色带
		ui.Box(c).Height(r.em(4)).Shrink(0).Background(colors(t).line)
	case mdTable:
		r.table(c, b)
	}
}

// headingBlock 搭标题：行高 1.25，一二级下面有一条细线、和字隔 .3em，六级用次要色。
func (r mdView) headingBlock(c *ui.Context, b mdBlock) {
	t := c.Theme()
	size := r.heading(b.level)
	box := ui.Column(c)
	if b.level <= 2 {
		box.Padding(0, 0, float32(math.Round(float64(size*0.3))), 0).BorderWidth(0, 0, hairline, 0).BorderColor(colors(t).line)
	}
	if b.level == 6 {
		box.TextColor(t.TextMuted)
	}
	r.text = size
	box.Children(func() {
		if hasImage([]mdBlock{b}) {
			r.media(c, b)
			return
		}
		e := r.inline(c, b.inlines).FontSize(size).FontWeight(fwBold).LineHeight(1.25)
		if b.textAlign != 0 {
			e.TextAlign(b.textAlign)
		}
	})
}

// codeBlock 搭代码块：内边距 16、行高 1.45，不折行、放不下左右滚（目录树、对齐的注释折行后列就乱了）。
// 复制按钮照 GitHub 悬停时浮在右上角；mermaid 代码块画成图，按钮多一个源码和图的切换。
func (r mdView) codeBlock(c *ui.Context, b mdBlock) {
	t := c.Theme()
	var d *diagram.Diagram
	if strings.EqualFold(b.lang, "mermaid") {
		d = diagramOf(c, b.code)
	}
	drawn := d != nil && d.Err == ""
	pad := r.gapEm(16)
	box := ui.Column(c).Radius(rRow).Background(codeBackground(t)).Clip()
	box.Children(func() {
		switch {
		case drawn && !mermaidSource[b.code]:
			diagramView(c, d)
		default:
			if d != nil && d.Err != "" {
				ui.Text(c, L(c).MarkdownDiagramFailed(errtext.Message(d.Err))).FontSize(r.small()).TextColor(t.Danger).Padding(pad, pad, 0, pad)
			}
			ui.ScrollHorizontal(c).Children(func() {
				ui.Box(c).Padding(pad).Children(func() {
					if r.find == nil {
						highlighted(c, b.code, r.words).Font(monoFont).FontSize(r.mono()).LineHeight(1.45).NoWrap()
						return
					}
					from := r.find.total
					r.find.reveal(ui.RichText(c, r.marked(c, b.code, ui.Span{})...).Font(monoFont).FontSize(r.mono()).LineHeight(1.45).NoWrap(), from)
				})
			})
		}
		if !box.Hovered() {
			return
		}
		ui.Row(c).Absolute().Top(r.gapEm(8)).Right(r.gapEm(8)).Gap(2).Padding(2).Radius(rRow).
			Background(colors(t).card).BorderWidth(hairline).BorderColor(colors(t).line).Children(func() {
			if drawn {
				icon, label := iconCode, L(c).FilesViewSource()
				if mermaidSource[b.code] {
					icon, label = iconEye, L(c).MarkdownViewDiagram()
				}
				if pressable(iconButtonSized(c.Key("source"), icon, label, compactBox(), smallIcon())).Clicked() {
					mermaidSource[b.code] = !mermaidSource[b.code]
				}
			}
			if pressable(iconButtonSized(c.Key("copy"), iconCopy, L(c).MarkdownCopyCode(), compactBox(), smallIcon())).Clicked() {
				c.WriteClipboard(b.code)
			}
		})
	})
}

// list 搭列表：标记在左边 2em 宽的一栏里、贴着正文，项与项隔 .25em；嵌套的有序列表照 GitHub 换成罗马数字、再换成字母。
func (r mdView) list(c *ui.Context, b mdBlock) {
	t := c.Theme()
	width := r.gapEm(32)
	if b.ordered {
		// 序号长了（100.、xviii.）栏放宽，不压到正文上；等宽数字约 0.6 个字宽
		n := 0
		for i := range b.items {
			n = max(n, len(listNumber(b.start+i, r.depth))+1)
		}
		width = max(width, float32(math.Ceil(float64(float32(n)*r.body()*0.6)))+r.gapEm(6))
	}
	inner := r
	inner.depth++
	ui.Column(c).Gap(r.gapEm(4)).Children(func() {
		for i, it := range b.items {
			ui.Row(c).AlignItems(ui.Start).Children(func() {
				if !b.ordered && it.check == 0 {
					listDot(c, width, r.body(), r.lead(), r.depth)
					inner.item(c, it.blocks)
					return
				}
				var marker string
				switch {
				case it.check == 1:
					marker = "☐"
				case it.check == 2:
					marker = "☑"
				case b.ordered:
					marker = listNumber(b.start+i, r.depth) + "."
				}
				m := ui.Text(c, marker).FontSize(r.body()).LineHeight(r.lead()).TextColor(t.TextMuted).Width(width).Shrink(0).
					TextAlign(ui.End).Padding(0, r.gapEm(6), 0, 0).Unselectable()
				if b.ordered {
					m.FontFeatures("tnum")
				}
				inner.item(c, it.blocks)
			})
		}
	})
}

// listNumber 是有序列表第 n 项的序号：第一层数字，嵌在列表里的一层罗马数字，再往里字母。
func listNumber(n, depth int) string {
	switch {
	case depth == 0 || n <= 0:
		return fmt.Sprint(n)
	case depth == 1:
		return roman(n)
	}
	var s []byte
	for ; n > 0; n = (n - 1) / 26 {
		s = append([]byte{byte('a' + (n-1)%26)}, s...)
	}
	return string(s)
}

// roman 是小写罗马数字。
func roman(n int) string {
	vals := []int{1000, 900, 500, 400, 100, 90, 50, 40, 10, 9, 5, 4, 1}
	syms := []string{"m", "cm", "d", "cd", "c", "xc", "l", "xl", "x", "ix", "v", "iv", "i"}
	var b strings.Builder
	for i, v := range vals {
		for ; n >= v; n -= v {
			b.WriteString(syms[i])
		}
	}
	return b.String()
}

// listDot 画无序列表的点，按层级是实心圆、空心圆、小方块；字体里的 • 太小，自己画，竖直对齐第一行的中线，
// 横向照浏览器停在正文前约 .7em 处。
func listDot(c *ui.Context, width, size, lead float32, depth int) {
	t := c.Theme()
	d := size * 0.3
	ui.Box(c).Width(width).Height(size * lead).Shrink(0).Draw(func(p *ui.Painter, rect ui.Rect) {
		dot := ui.Rect{X: rect.X + rect.W - size*0.7 - d/2, Y: rect.Y + (rect.H-d)/2, W: d, H: d}
		switch min(depth, 2) {
		case 0:
			p.Fill(dot, t.TextMuted, d/2)
		case 1:
			p.Stroke(dot, t.TextMuted, d/2, 1.2)
		default:
			p.Fill(dot, t.TextMuted, 1)
		}
	})
}

// cellPadX、cellPadY 是表格格子的内边距（GitHub 的 6px 13px）。
func (r mdView) cellPadX() float32 { return r.gapEm(13) }
func (r mdView) cellPadY() float32 { return r.gapEm(6) }

// cellPad 是量列宽时加上的左右内边距，再留 1 的余量：列宽取整后比量到的文字窄一点，文字就折行了。
func (r mdView) cellPad() float32 { return 2*r.cellPadX() + 1 }

// mdTableSize 是表格每列量好的宽度：最小宽是最长的拆不开的词，最大宽是不折行。
// 第一次搭的时候量一次，存在块上；换了字号、字重（key）再重量。
type mdTableSize struct {
	min, max []float32
	key      [2]float32
}

// table 搭表格。横向滚动容器给内容的宽度没有上限，表格会按最宽排、长句不再折行，
// 所以列宽自己定：量出每列不折行的宽度和最长的拆不开的词，拿上一帧量到的容器宽度当基准，
// 放得下就按内容宽排；长句折行能放下的，按各列能收缩的余地分掉差额，刚好铺满；
// 连拆不开的词都放不下的，长句的列最宽 360、短的按内容，整张左右滚。
func (r mdView) table(c *ui.Context, b mdBlock) {
	cols := 0
	for _, row := range b.rows {
		cols = max(cols, len(row))
	}
	size := b.size
	if key := [2]float32{r.body(), float32(fwBold)}; size.min == nil || size.key != key {
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
		ui.Grid(c).ColumnTracks(tracks...).BorderWidth(hairline).BorderColor(colors(c.Theme()).line).
			Children(func() { r.cells(c, b, cols) })
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
			spans := r.cellSpans(c, cell, ri == 0)
			w, _ := c.MeasureText(0, spans...)
			size.max[ci] = max(size.max[ci], w+r.cellPad())
			for _, sp := range spans {
				for _, word := range strings.Fields(sp.Text) {
					var run []rune
					flush := func() {
						if len(run) > 0 {
							sp.Text = string(run)
							w, _ := c.MeasureText(0, sp)
							size.min[ci] = max(size.min[ci], w+r.cellPad())
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
							sp.Text = string(rune(0x4E2D)) // 「中」：量一个全角字的宽度
							w, _ = c.MeasureText(0, sp)
							cjk[key] = w
						}
						size.min[ci] = max(size.min[ci], w+r.cellPad())
					}
					flush()
				}
			}
		}
	}
}

// cellSpans 是格子里的文字按搭界面时的字号和字重摆出来的 spans，量宽度用；搜索词的底色不影响宽度，不带。
func (r mdView) cellSpans(c *ui.Context, cell []mdInline, header bool) []ui.Span {
	r.words, r.find = nil, nil
	var spans []ui.Span
	for _, in := range cell {
		for _, sp := range r.spans(c, in) {
			if sp.Size == 0 {
				sp.Size = r.body()
			}
			if header && sp.Weight == 0 {
				sp.Weight = fwBold
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

// cells 搭表格的格子：照 GitHub 每格四边都有线，表头加粗，表体的偶数行垫底色。
func (r mdView) cells(c *ui.Context, b mdBlock, cols int) {
	t := c.Theme()
	for ri, row := range b.rows {
		for ci := range cols {
			var cell []mdInline
			if ci < len(row) {
				cell = row[ci]
			}
			box := ui.Box(c).Padding(r.cellPadY(), r.cellPadX()).BorderColor(colors(t).line).
				BorderWidth(0, cellEdge(ci < cols-1), cellEdge(ri < len(b.rows)-1), 0)
			if stripe(ri, true) {
				box.Background(codeBackground(t))
			}
			box.Children(func() {
				e := r.inline(c, cell).FontSize(r.body()).LineHeight(r.lead())
				if ri == 0 {
					e.FontWeight(fwBold)
				}
				if ci < len(b.align) && b.align[ci] != 0 {
					e.TextAlign(b.align[ci])
				}
			})
		}
	}
}

// stripe 报告表格第 ri 行要不要垫底色：照 GitHub 表体的偶数行垫，第一行是表头时表体从第二行算起。
func stripe(ri int, head bool) bool {
	if head {
		return ri > 0 && ri%2 == 0
	}
	return ri%2 == 1
}

// cellEdge 是格子右边、下边的线：挨着外框的那边不画，外框画在表格上。
func cellEdge(inner bool) float32 {
	if inner {
		return hairline
	}
	return 0
}

// item 搭列表项的内容：块之间照整篇的间距，嵌套的列表紧接着上一行。
func (r mdView) item(c *ui.Context, bs []mdBlock) {
	ui.Column(c).Grow(1).Shrink(1).Children(func() {
		for i, b := range bs {
			if i > 0 && b.kind != mdList {
				ui.Box(c).Height(r.gapEm(16)).Shrink(0)
			}
			r.block(c, b)
		}
	})
}

// inline 把一段行内内容搭成一个段落；只有 http 链接能点，相对路径之类的只上色。
func (r mdView) inline(c *ui.Context, ins []mdInline) ui.Element {
	if r.find != nil {
		from := r.find.total
		return r.find.reveal(r.inlineEl(c, ins), from)
	}
	return r.inlineEl(c, ins)
}

func (r mdView) inlineEl(c *ui.Context, ins []mdInline) ui.Element {
	for _, in := range ins {
		if webURL(in.url) || r.wiki != nil && strings.HasPrefix(in.url, "wiki:") {
			return ui.RichText(c).Children(func() {
				for _, in := range ins {
					if target, ok := strings.CutPrefix(in.url, "wiki:"); ok && r.wiki != nil {
						// 链接照 GitHub 一直带下划线
						if ui.Text(c, in.text).TextColor(c.Theme().Accent).Underline().Clicked() {
							r.wiki(target)
						}
					} else if webURL(in.url) {
						ui.Link(c, in.text, in.url).Underline()
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
	switch {
	case in.style&mdMono != 0, in.style&mdKbd != 0:
		// <kbd> 在 GitHub 是 11px 带边框的键帽，span 画不了边框，按行内代码画，不然太小认不出
		// 圆角让底色只包住字、比字略宽，不铺满行高：几段代码挨着时不连成竖条
		base.Font, base.Size, base.Background, base.Radius = monoFont, r.textSize()*0.85, codeBackground(t), rRow
	case in.style&mdSup != 0:
		base.Size = r.textSize() * 0.75
	case in.style&mdSmall != 0:
		base.Size = r.textSize() * 0.9
	}
	// 走到这里的链接点不了（能点的由 inlineEl 交给 ui.Link），只上色不加下划线，免得看着像能点
	if in.url != "" {
		base.Color = t.Accent
	}
	return r.marked(c, in.text, base)
}

// marked 把 text 里的命中铺上底色；有查找栏时顺带给命中编号，当前那个用深一点的底。
func (r mdView) marked(c *ui.Context, text string, base ui.Span) []ui.Span {
	p := colors(c.Theme())
	if r.find == nil {
		return markSpans(text, markRanges(text, r.words), base, p.mark)
	}
	marks := markRanges(text, r.find.words())
	out := markSpans(text, marks, base, p.mark)
	for i := range marks {
		// markSpans 的结果里命中的段在奇数位置
		if r.find.total == r.find.cur {
			out[2*i+1].Background = p.markOn
		}
		r.find.total++
	}
	return out
}

func webURL(u string) bool {
	return strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://")
}

func codeBackground(t *ui.Theme) ui.Color {
	return colors(t).code
}
