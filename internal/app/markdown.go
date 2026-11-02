package app

import (
	"bytes"
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
	blank   bool         // 源文件里它前面有空行：文件预览只在有空行的地方空开
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
		from := len(out)
		out = append(out, m.block(c)...)
		if from < len(out) {
			out[from].blank = blankBefore(c, m.src)
		}
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

// blankBefore 看源文件里块的第一行上面是不是空行。goldmark 的 HasBlankPreviousLines 对表格不准
// （列表后面空一行再接表格，表格上记的是没有空行），所以按块在源文件里的位置自己看。
func blankBefore(n ast.Node, src []byte) bool {
	at := -1
	ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if t, ok := n.(*ast.Text); ok {
			at = t.Segment.Start
			return ast.WalkStop, nil
		}
		if n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
			at = n.Lines().At(0).Start
			return ast.WalkStop, nil
		}
		return ast.WalkContinue, nil
	})
	if at < 0 {
		return n.HasBlankPreviousLines()
	}
	// 退到这一行的开头，再看上一行
	for at > 0 && src[at-1] != '\n' {
		at--
	}
	if at == 0 {
		return false
	}
	prev := bytes.TrimRight(src[:at-1], " \t\r")
	return len(prev) == 0 || prev[len(prev)-1] == '\n'
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
	size  float32             // 正文字号（设置里的 Markdown 字号），0 跟界面字号；标题、代码和它的差值同界面
	doc   bool                // 文件预览：照长文排版，行距和段距比历史页的回复松
	wiki  func(target string) // 点 [[名字]] 时打开它；为空时只上色、点不了
	imgs  *mdImages           // 文件预览载入图片；为空时图片只显示说明文字
}

// lead 是正文的行高，列表的点、序号按它对齐第一行。
func (r mdView) lead() float32 {
	if r.doc {
		return 1.8
	}
	return 1.6
}

// markLead 是列表标记的行高；历史页沿用原来的 1.55。
func (r mdView) markLead() float32 {
	if r.doc {
		return r.lead()
	}
	return 1.55
}

// dot 是列表圆点占字号的比例：文件预览照 Writer 小一些，行距松了点大了显得重。
func (r mdView) dot() float32 {
	if r.doc {
		return 0.28
	}
	return 0.36
}

// mono 是行内代码和代码块的字号：文件预览用等宽字那一档，和旁边的正文看着一样大。
func (r mdView) mono() float32 {
	if r.doc {
		_, code, _, _ := typeScale(r.body())
		return code
	}
	return r.small()
}

// boxed 是自带底色或边框的块：代码块、表格、引用、分割线。
func boxed(k mdKind) bool {
	return k == mdCode || k == mdTable || k == mdQuote || k == mdRule || k == mdCells
}

// endsBoxed 看一串块是不是以 boxed 的块结尾，列表看它最后一项的最后一块。
func endsBoxed(bs []mdBlock) bool {
	if len(bs) == 0 {
		return false
	}
	last := bs[len(bs)-1]
	if last.kind == mdList && len(last.items) > 0 {
		return endsBoxed(last.items[len(last.items)-1].blocks)
	}
	return boxed(last.kind)
}

// blankLine 是文件预览里一个空行的高度：正文字号乘行高。
func (r mdView) blankLine() float32 { return float32(math.Round(float64(r.body() * r.lead()))) }

// page 搭文件预览的整篇，块与块之间照 Writer 的节奏：源文件里空了行的地方空出一整行的高度
// （标题上面再多一点），没空行的（"测试约束："下面直接接列表）紧挨着排，和折行一样；代码块、表格、引用前后总是空一行。
func (r mdView) page(c *ui.Context, bs []mdBlock) {
	blankLine := r.blankLine()
	for i, b := range bs {
		if i > 0 {
			prev := bs[i-1]
			var gap float32
			switch {
			case prev.kind == mdMeta:
				gap = blankLine + 8
			case b.blank && b.kind == mdHeading:
				gap = blankLine + 8
			case b.blank || boxed(b.kind) || endsBoxed([]mdBlock{prev}):
				// 代码块、表格、引用自带底色或边框，源文件没空行也照空了一行排，不贴着上下的文字
				gap = blankLine
			case b.kind == mdHeading || prev.kind == mdHeading:
				gap = 12
			}
			if gap > 0 {
				ui.Box(c).Height(gap).Shrink(0)
			}
		}
		r.block(c, b)
	}
}

func (r mdView) body() float32  { return cmp.Or(r.size, fsBody) }
func (r mdView) small() float32 { s, _, _, _ := typeScale(r.body()); return s }

// heading 是标题字号：一级同页面标题、二级同卡片标题，再往下和正文一样大。
func (r mdView) heading(level int) float32 {
	_, _, title, page := typeScale(r.body())
	switch level {
	case 1:
		return page
	case 2:
		return title
	}
	return r.body()
}

// sz 同界面的 sz，按 Markdown 的正文字号缩放。
func (r mdView) sz(v float32) float32 { return float32(math.Round(float64(v * r.body() / rowBase))) }

// scaled 是按正文字号缩放的尺寸，v 是默认界面字号下的值。
func (r mdView) scaled(v float32) float32 {
	return float32(math.Round(float64(v * r.body() / defaultUIFontSize)))
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
		if hasImage([]mdBlock{b}) {
			r.media(c, b)
			return
		}
		e := r.inline(c, b.inlines).FontSize(r.body()).LineHeight(r.lead())
		if b.textAlign != 0 {
			e.TextAlign(b.textAlign)
		}
	case mdHeading:
		if hasImage([]mdBlock{b}) {
			r.media(c, b)
			return
		}
		e := r.inline(c, b.inlines).FontSize(r.heading(b.level)).FontWeight(fwBold).LineHeight(1.35).Padding(4, 0, 0, 0)
		if b.textAlign != 0 {
			e.TextAlign(b.textAlign)
		}
	case mdCells:
		r.grid(c, b.grid)
	case mdDetails:
		r.details(c, b)
	case mdCode:
		// mermaid 代码块画成图；画好之前、画不出来、切到源码时照普通代码块显示
		var d *diagram.Diagram
		if strings.EqualFold(b.lang, "mermaid") {
			d = diagramOf(c, b.code)
		}
		drawn := d != nil && d.Err == ""
		// 上面一条语言栏，右边是复制按钮（图多一个源码和图的切换）；按钮按界面尺寸，栏高至少放得下它们
		ui.Column(c).Radius(rCard).Background(codeBackground(t)).Clip().Children(func() {
			ui.Row(c).Height(max(sz(28), r.sz(28))).Padding(0, 4, 0, 12).AlignItems(ui.Center).Children(func() {
				lang := b.lang
				if lang == "" {
					lang = "代码"
				}
				ui.Text(c, lang).FontSize(r.small()).Font(monoFont).TextColor(t.TextMuted)
				ui.Spacer(c)
				if drawn {
					icon, label := iconCode, "查看源码"
					if mermaidSource[b.code] {
						icon, label = iconEye, "查看图"
					}
					if pressable(iconButtonSized(c.Key("source"), icon, label, compactBox(), smallIcon())).Clicked() {
						mermaidSource[b.code] = !mermaidSource[b.code]
					}
				}
				if pressable(iconButtonSized(c.Key("copy"), iconCopy, "复制代码", compactBox(), smallIcon())).Clicked() {
					c.WriteClipboard(b.code)
				}
			})
			if drawn && !mermaidSource[b.code] {
				diagramView(c, d)
				return
			}
			if d != nil && d.Err != "" {
				ui.Text(c, "画不出图："+errtext.Message(d.Err)).FontSize(r.small()).TextColor(t.Danger).Padding(0, 12)
			}
			// 代码不折行，放不下左右滚：目录树、对齐的注释折行后列就乱了
			ui.ScrollHorizontal(c).Children(func() {
				ui.Box(c).Padding(8, 12, 12, 12).Children(func() {
					if r.find == nil {
						highlighted(c, b.code, r.words).Font(monoFont).FontSize(r.mono()).LineHeight(1.5).NoWrap()
						return
					}
					from := r.find.total
					r.find.reveal(ui.RichText(c, r.marked(c, b.code, ui.Span{})...).Font(monoFont).FontSize(r.mono()).LineHeight(1.5).NoWrap(), from)
				})
			})
		})
	case mdQuote:
		ui.Row(c).AlignItems(ui.Stretch).Gap(8).Children(func() {
			ui.Box(c).Width(3).Shrink(0).Radius(full(3)).Background(colors(t).line)
			quote := ui.Column(c).Grow(1).Shrink(1).TextColor(t.TextMuted)
			if r.doc {
				quote.Children(func() { r.page(c, b.blocks) })
			} else {
				quote.Gap(8).Children(func() { r.blocks(c, b.blocks) })
			}
		})
	case mdList:
		width, gap6 := r.scaled(14), float32(6)
		if b.ordered {
			width = r.scaled(float32(len(fmt.Sprint(b.start+len(b.items)-1))*8 + 8))
		}
		if r.doc {
			// 照 Writer：标记占一栏、点在栏中间，正文从约两个字宽处开始，下一层再缩进一栏
			width, gap6 = max(width+gap6, float32(math.Round(float64(r.body()*1.9)))), 0
		}
		inner := r
		inner.depth++
		// 文件预览里一项接一项和折行一样紧，靠行高留白
		gap := float32(4)
		if r.doc {
			gap = 0
		}
		ui.Column(c).Gap(gap).Children(func() {
			for i, it := range b.items {
				// 上一项以代码块、表格、引用结尾时空一行，和整篇里的规则一样
				if r.doc && i > 0 && endsBoxed(b.items[i-1].blocks) {
					ui.Box(c).Height(r.blankLine()).Shrink(0)
				}
				ui.Row(c).Gap(gap6).AlignItems(ui.Start).Children(func() {
					if !b.ordered && it.check == 0 {
						listDot(c, width, r.body(), r.markLead(), r.dot(), r.depth)
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
						marker = fmt.Sprintf("%d.", b.start+i)
					}
					m := ui.Text(c, marker).FontSize(r.body()).LineHeight(r.markLead()).TextColor(t.TextMuted).Width(width).Shrink(0).Unselectable()
					if b.ordered {
						m.FontFeatures("tnum").TextAlign(ui.End)
						if r.doc {
							// 文件预览的标记栏和正文之间不留间隔，序号靠右时自己让开
							m.Padding(0, 6, 0, 0)
						}
					}
					inner.item(c, it.blocks)
				})
			}
		})
	case mdMeta:
		// 照 Writer 的属性面板：左边一列键用次要色，右边是值，都比正文小一号
		ui.Column(c).Gap(6).Children(func() {
			for _, f := range b.meta {
				ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
					r.inline(c, []mdInline{{text: f.key}}).FontSize(r.small()).LineHeight(1.5).TextColor(t.TextMuted).
						Width(r.scaled(128)).Shrink(0).Padding(0, 0, 0, float32(f.depth)*treeIndent)
					r.inline(c, []mdInline{{text: f.value}}).FontSize(r.small()).LineHeight(1.5).Grow(1).Shrink(1)
				})
			}
		})
	case mdRule:
		hline(c).Margin(6, 0)
	case mdTable:
		r.table(c, b)
	}
}

// listDot 画无序列表的点，按层级是实心圆、空心圆、小方块；字体里的 • 太小，自己画，竖直对齐第一行的中线。
func listDot(c *ui.Context, width, size, lead, scale float32, depth int) {
	t := c.Theme()
	d := size * scale
	ui.Box(c).Width(width).Height(size * lead).Shrink(0).Draw(func(p *ui.Painter, rect ui.Rect) {
		dot := ui.Rect{X: rect.X + (rect.W-d)/2, Y: rect.Y + (rect.H-d)/2, W: d, H: d}
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

// 表格一个格子的左右内边距 24，再留 1 的余量：列宽取整后比量到的文字窄一点，文字就折行了
const mdCellPad = 25

// mdTableSize 是表格每列量好的宽度：最小宽是最长的拆不开的词，最大宽是不折行。
// 第一次搭的时候量一次，存在块上；换了字号、字重（key）再重量。
type mdTableSize struct {
	min, max []float32
	key      [3]float32
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
	if key := [3]float32{r.body(), float32(r.headWeight()), float32(fwBold)}; size.min == nil || size.key != key {
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
		g := ui.Grid(c).ColumnTracks(tracks...).Radius(rRow)
		if r.doc {
			// 文件预览照 Writer 画外框和竖线，格子更高：长文里的表格要一眼看清行列
			g.BorderWidth(hairline).BorderColor(colors(c.Theme()).line)
		}
		g.Children(func() { r.cells(c, b, cols) })
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
func (r mdView) cellSpans(c *ui.Context, cell []mdInline, header bool) []ui.Span {
	r.words, r.find = nil, nil
	var spans []ui.Span
	for _, in := range cell {
		for _, sp := range r.spans(c, in) {
			if sp.Size == 0 {
				sp.Size = r.body()
			}
			if header && sp.Weight == 0 {
				sp.Weight = r.headWeight()
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
			// 历史页只画行与行之间的横线，表头靠填充底区分；文件预览另有竖线（外框在表格上）
			bottom, right := float32(hairline), float32(0)
			if ri == len(b.rows)-1 {
				bottom = 0
			}
			if r.doc && ci < cols-1 {
				right = hairline
			}
			box := ui.Box(c).Padding(4, 12).BorderWidth(0, right, bottom, 0).BorderColor(colors(t).line)
			if r.doc {
				box.Padding(8, 12)
			}
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
				e := r.inline(c, cell).FontSize(r.body()).LineHeight(r.cellLead())
				if ri == 0 {
					e.FontWeight(r.headWeight())
				}
				if ci < len(b.align) && b.align[ci] != 0 {
					e.TextAlign(b.align[ci])
				}
			})
		}
	}
}

// item 搭列表项的内容；文件预览里和整篇一样按源文件的空行排，项里的代码块、表格不贴着上一行。
func (r mdView) item(c *ui.Context, bs []mdBlock) {
	if r.doc {
		ui.Column(c).Grow(1).Shrink(1).Children(func() { r.page(c, bs) })
		return
	}
	ui.Column(c).Grow(1).Shrink(1).Gap(4).Children(func() { r.blocks(c, bs) })
}

// cellLead 是表格格子的行高：文件预览和代码块同一档 1.5。
func (r mdView) cellLead() float32 {
	if r.doc {
		return 1.5
	}
	return 1.45
}

// headWeight 是表头的字重：文件预览照 Writer 加粗，历史页的回复用中等。
func (r mdView) headWeight() int {
	if r.doc {
		return fwBold
	}
	return fwMedium
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
						l := ui.Text(c, in.text).TextColor(c.Theme().Accent)
						if l.Hovered() {
							l.Underline()
						}
						if l.Clicked() {
							r.wiki(target)
						}
					} else if webURL(in.url) {
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
		// 圆角让底色只包住字、比字略宽，不铺满行高：几段代码挨着时不连成竖条
		// 行内代码强调色字、代码块的底，同手机端
		base.Font, base.Size, base.Background, base.Radius = monoFont, r.mono(), codeBackground(t), rSmall
		base.Color = t.Accent
	}
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
