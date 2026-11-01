package app

import (
	"cmp"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/yuin/goldmark/ast"
	"golang.org/x/net/html"
)

// 文件预览照 GitHub 解释 Markdown 里的 HTML：README 常用 <p align="center"> 居中标题和徽章、<img> 控制图片大小、
// <table> 做图文并排、<details> 折叠长内容。只认 GitHub 放行的那些标签，<script>、<style> 这类连内容一起丢掉，
// 认不出的标签只留里面的内容。

// mdImg 是一张图片：Markdown 的 ![]() 或 HTML 的 <img>。
type mdImg struct {
	src, alt, title string
	width, height   mdLen
}

// mdLen 是 HTML 里写的长度：像素，或 pct 为真时的百分比；0 是没写。
type mdLen struct {
	v   float32
	pct bool
}

func parseLen(s string) mdLen {
	s = strings.TrimSpace(s)
	pct := strings.HasSuffix(s, "%")
	s = strings.TrimSuffix(strings.TrimSuffix(s, "%"), "px")
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 32)
	if err != nil || v <= 0 {
		return mdLen{}
	}
	return mdLen{float32(v), pct}
}

// mdGrid 是 HTML 表格：和 GFM 表格不同，格子里可以是标题、段落、图片这些块。
type mdGrid struct {
	rows  [][]mdCell
	cols  int
	width []mdLen // 每列的宽度，取这一列第一个写了 width 的格子
}

type mdCell struct {
	blocks []mdBlock
	header bool     // <th>
	valign ui.Align // 竖直对齐，默认居中（同浏览器）
}

// inlineTag 是段落里开着的一个行内 HTML 标签。
type inlineTag struct {
	tag   string
	style mdStyle
	url   string
}

// tagStyle 把开着的行内标签叠到样式上；链接取最里面那层。
func tagStyle(tags []inlineTag, st mdStyle, url string) (mdStyle, string) {
	for _, t := range tags {
		st |= t.style
		if t.url != "" {
			url = t.url
		}
	}
	return st, url
}

// inlineStyle 是行内标签对应的样式，ok 为假的标签不改样式。
func inlineStyle(tag string) (mdStyle, bool) {
	switch tag {
	case "b", "strong":
		return mdBold, true
	case "i", "em", "cite", "var", "dfn":
		return mdItalic, true
	case "s", "del", "strike":
		return mdStrike, true
	case "code", "kbd", "samp", "tt":
		return mdMono, true
	}
	return 0, false
}

// inlineHTML 处理段落里的一个行内 HTML 片段（goldmark 每个标签给一段）：开、关样式标签改 tags；
// 是图片时返回它，是 <br> 时 brk 为真；注释和认不出的标签不显示。
func inlineHTML(raw string, tags *[]inlineTag) (img *mdImg, brk bool) {
	z := html.NewTokenizer(strings.NewReader(raw))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return img, brk
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			switch tok.Data {
			case "img":
				img = imgOf(attrs(tok))
			case "br":
				brk = true
			case "a":
				*tags = append(*tags, inlineTag{tag: "a", url: attrs(tok)["href"]})
			default:
				if st, ok := inlineStyle(tok.Data); ok {
					*tags = append(*tags, inlineTag{tag: tok.Data, style: st})
				}
			}
		case html.EndTagToken:
			tag := z.Token().Data
			for i := len(*tags) - 1; i >= 0; i-- {
				if (*tags)[i].tag == tag {
					*tags = append((*tags)[:i], (*tags)[i+1:]...)
					break
				}
			}
		}
	}
}

func attrs(tok html.Token) map[string]string {
	m := map[string]string{}
	for _, a := range tok.Attr {
		m[strings.ToLower(a.Key)] = a.Val
	}
	return m
}

func imgOf(a map[string]string) *mdImg {
	if a["src"] == "" {
		return nil
	}
	return &mdImg{src: a["src"], alt: a["alt"], title: a["title"], width: parseLen(a["width"]), height: parseLen(a["height"])}
}

// plainText 是节点里的纯文字，图片的说明文字用。
func plainText(n ast.Node, src []byte) string {
	var b strings.Builder
	ast.Walk(n, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if t, ok := n.(*ast.Text); entering && ok {
			b.Write(t.Value(src))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// trimInlines 去掉分隔用的空项（图片前后插的，免得文字和它合并）和结尾的换行。
func trimInlines(ins []mdInline) []mdInline {
	out := ins[:0]
	for _, in := range ins {
		if in.text != "" || in.img != nil {
			out = append(out, in)
		}
	}
	for len(out) > 0 && out[len(out)-1].img == nil {
		k := len(out) - 1
		out[k].text = strings.TrimRight(out[k].text, "\n")
		if out[k].text != "" {
			break
		}
		out = out[:k]
	}
	return out
}

// hnode 是拼起来的 HTML 树的一个节点：标签、文字，或夹在 HTML 中间、已经转好的 Markdown 块。
// CommonMark 的 HTML 块到空行就结束，<td> 和 </td> 之间空了行写的 Markdown 是单独的块，要挂回所在的标签下面。
type hnode struct {
	tag    string // 空是文字，"#md" 是 Markdown 块
	attrs  map[string]string
	text   string
	blocks []mdBlock
	kids   []*hnode
}

// voidTags 没有结束标签。
var voidTags = map[string]bool{"img": true, "br": true, "hr": true, "source": true, "input": true, "meta": true, "link": true, "wbr": true, "col": true, "area": true, "embed": true, "track": true, "param": true, "base": true}

// htmlBlocks 是文件预览的块转换：HTML 块喂给一棵 HTML 树，标签没关上时后面的 Markdown 块挂在里面，
// 树里的标签全关上了再整棵转成块。
func (m mdConv) htmlBlocks(n ast.Node) []mdBlock {
	var out []mdBlock
	root := &hnode{tag: "#root"}
	var stack []*hnode
	blank, pending := false, false
	flush := func() {
		if len(root.kids) > 0 {
			bs := hblocks(root.kids, 0)
			if len(bs) > 0 {
				bs[0].blank = blank
			}
			out = append(out, bs...)
		}
		root.kids, pending = nil, false
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if !pending {
			blank = blankBefore(c, m.src)
		}
		if h, ok := c.(*ast.HTMLBlock); ok {
			raw := mdLines(h.Lines(), m.src)
			if h.HasClosure() {
				raw += "\n" + string(h.ClosureLine.Value(m.src))
			}
			stack = feedHTML(root, stack, raw)
			pending = true
			if len(stack) == 0 {
				flush()
			}
			continue
		}
		bs := m.block(c)
		if len(stack) > 0 {
			top := stack[len(stack)-1]
			top.kids = append(top.kids, &hnode{tag: "#md", blocks: bs})
			continue
		}
		if len(bs) > 0 {
			bs[0].blank = blankBefore(c, m.src)
		}
		out = append(out, bs...)
	}
	flush()
	return out
}

// feedHTML 把一段 HTML 接到树上，返回还开着的标签。
func feedHTML(root *hnode, stack []*hnode, raw string) []*hnode {
	top := func() *hnode {
		if len(stack) > 0 {
			return stack[len(stack)-1]
		}
		return root
	}
	// closeTo 关掉最近的 tag（遇到 stop 里的标签就不再往外找），找不到不动
	closeTo := func(tags, stop string) {
		for i := len(stack) - 1; i >= 0; i-- {
			t := stack[i].tag
			if strings.Contains(" "+tags+" ", " "+t+" ") {
				stack = stack[:i]
				return
			}
			if strings.Contains(" "+stop+" ", " "+t+" ") {
				return
			}
		}
	}
	z := html.NewTokenizer(strings.NewReader(raw))
	for {
		tt := z.Next()
		switch tt {
		case html.ErrorToken:
			return stack
		case html.TextToken:
			p := top()
			p.kids = append(p.kids, &hnode{text: string(z.Text())})
		case html.StartTagToken, html.SelfClosingTagToken:
			tok := z.Token()
			tag := tok.Data
			// 省略结束标签的写法：新的 <li>、<td>、<tr> 关掉上一个，块级标签关掉没关的 <p>
			switch tag {
			case "li":
				closeTo("li", "ul ol")
			case "td", "th":
				closeTo("td th", "tr table")
			case "tr":
				closeTo("tr", "table thead tbody tfoot")
			case "dt", "dd":
				closeTo("dt dd", "dl")
			}
			if blockTag(tag) {
				if t := top(); t.tag == "p" {
					stack = stack[:len(stack)-1]
				}
			}
			n := &hnode{tag: tag, attrs: attrs(tok)}
			p := top()
			p.kids = append(p.kids, n)
			if tt == html.StartTagToken && !voidTags[tag] {
				stack = append(stack, n)
			}
		case html.EndTagToken:
			tag := z.Token().Data
			for i := len(stack) - 1; i >= 0; i-- {
				if stack[i].tag == tag {
					stack = stack[:i]
					break
				}
			}
		}
	}
}

func blockTag(tag string) bool {
	switch tag {
	case "p", "div", "center", "section", "article", "header", "footer", "main", "nav", "aside", "figure", "figcaption",
		"h1", "h2", "h3", "h4", "h5", "h6", "ul", "ol", "li", "dl", "dt", "dd", "table", "blockquote", "pre", "hr",
		"details", "summary", "address", "hgroup":
		return true
	}
	return false
}

// dropTags 连内容一起不显示。
func dropTags(tag string) bool {
	switch tag {
	case "script", "style", "template", "head", "title", "noscript", "iframe", "object", "video", "audio", "canvas", "svg", "math", "form", "button", "select", "textarea", "input":
		return true
	}
	return false
}

func alignOf(a map[string]string, inherit ui.Align) ui.Align {
	switch strings.ToLower(strings.TrimSpace(a["align"])) {
	case "center", "middle":
		return ui.Center
	case "right":
		return ui.End
	case "left":
		return ui.Start
	}
	return inherit
}

// hconv 把一串 HTML 节点转成块：行内的内容攒成段落，遇到块级标签先收掉段落。
type hconv struct {
	out   []mdBlock
	para  []mdInline
	align ui.Align
}

func hblocks(kids []*hnode, align ui.Align) []mdBlock {
	h := &hconv{align: align}
	for _, k := range kids {
		h.node(k, 0, "", false)
	}
	h.flush()
	return h.out
}

func (h *hconv) add(in mdInline) {
	if k := len(h.para) - 1; k >= 0 && in.img == nil && h.para[k].img == nil && h.para[k].style == in.style && h.para[k].url == in.url {
		h.para[k].text += in.text
		return
	}
	h.para = append(h.para, in)
}

// flush 把攒着的行内内容收成一个段落：去掉首尾和换行两边的空白，只剩空白就不要。
func (h *hconv) flush() {
	ins := h.para
	h.para = nil
	for len(ins) > 0 && ins[0].img == nil {
		ins[0].text = strings.TrimLeft(ins[0].text, " \n")
		if ins[0].text != "" {
			break
		}
		ins = ins[1:]
	}
	for len(ins) > 0 && ins[len(ins)-1].img == nil {
		k := len(ins) - 1
		ins[k].text = strings.TrimRight(ins[k].text, " \n")
		if ins[k].text != "" {
			break
		}
		ins = ins[:k]
	}
	for i := range ins {
		ins[i].text = strings.ReplaceAll(strings.ReplaceAll(ins[i].text, " \n", "\n"), "\n ", "\n")
	}
	if len(ins) > 0 {
		h.out = append(h.out, mdBlock{kind: mdPara, inlines: wikiLinks(ins), textAlign: h.align})
	}
}

// emit 收掉段落后放进转好的块，没设对齐的段落、标题跟着外层的 align。
func (h *hconv) emit(bs ...mdBlock) {
	h.flush()
	for _, b := range bs {
		if b.textAlign == 0 && (b.kind == mdPara || b.kind == mdHeading) {
			b.textAlign = h.align
		}
		h.out = append(h.out, b)
	}
}

// collapse 照 HTML 把连续的空白并成一个空格。
func collapse(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\f' {
			space = true
			continue
		}
		if space {
			b.WriteByte(' ')
			space = false
		}
		b.WriteRune(r)
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

func (h *hconv) node(n *hnode, st mdStyle, url string, pre bool) {
	switch n.tag {
	case "":
		text := n.text
		if !pre {
			text = collapse(html.UnescapeString(text))
			// 段落开头和紧跟换行的空白不要
			if text == " " && (len(h.para) == 0 || strings.HasSuffix(h.para[len(h.para)-1].text, "\n")) {
				return
			}
		} else {
			text = html.UnescapeString(text)
		}
		h.add(mdInline{text: text, style: st, url: url})
		return
	case "#md":
		h.emit(n.blocks...)
		return
	case "#root":
		return
	}
	if dropTags(n.tag) {
		return
	}
	if s, ok := inlineStyle(n.tag); ok {
		h.kids(n, st|s, url, pre)
		return
	}
	switch n.tag {
	case "a":
		if href := n.attrs["href"]; href != "" {
			url = href
		}
		h.kids(n, st, url, pre)
	case "img":
		if img := imgOf(n.attrs); img != nil {
			h.para = append(h.para, mdInline{img: img, url: url})
		}
	case "br":
		h.add(mdInline{text: "\n", style: st, url: url})
	case "picture":
		// 只取里面的 <img>：<source> 按深浅色、屏幕宽度挑图，这里不挑
		h.kids(n, st, url, pre)
	case "h1", "h2", "h3", "h4", "h5", "h6":
		ins := inlinesOf(n, st|mdBold, url)
		h.emit(mdBlock{kind: mdHeading, level: int(n.tag[1] - '0'), inlines: ins, textAlign: alignOf(n.attrs, h.align)})
	case "p", "div", "center", "section", "article", "header", "footer", "main", "nav", "aside", "figure", "figcaption",
		"address", "hgroup", "dl", "dt", "dd", "summary":
		align := alignOf(n.attrs, h.align)
		if n.tag == "center" {
			align = ui.Center
		}
		h.emit(hblocks(n.kids, align)...)
	case "ul", "ol":
		b := mdBlock{kind: mdList, ordered: n.tag == "ol", start: 1}
		if s, err := strconv.Atoi(n.attrs["start"]); err == nil && b.ordered {
			b.start = s
		}
		for _, k := range n.kids {
			if k.tag == "li" {
				b.items = append(b.items, mdItem{blocks: hblocks(k.kids, 0)})
			}
		}
		h.emit(b)
	case "li":
		h.emit(mdBlock{kind: mdList, start: 1, items: []mdItem{{blocks: hblocks(n.kids, 0)}}})
	case "blockquote":
		h.emit(mdBlock{kind: mdQuote, blocks: hblocks(n.kids, 0)})
	case "hr":
		h.emit(mdBlock{kind: mdRule})
	case "pre":
		lang := ""
		var b strings.Builder
		var walk func(n *hnode)
		walk = func(n *hnode) {
			if n.tag == "" {
				b.WriteString(html.UnescapeString(n.text))
			}
			if n.tag == "code" && lang == "" {
				for _, cls := range strings.Fields(n.attrs["class"]) {
					if l, ok := strings.CutPrefix(cls, "language-"); ok {
						lang = l
					}
				}
			}
			for _, k := range n.kids {
				walk(k)
			}
		}
		walk(n)
		h.emit(mdBlock{kind: mdCode, code: strings.Trim(b.String(), "\n"), lang: lang})
	case "table":
		h.emit(tableOf(n))
	case "details":
		open := n.attrs != nil && hasAttr(n.attrs, "open")
		b := mdBlock{kind: mdDetails, open: &open}
		var rest []*hnode
		for _, k := range n.kids {
			if k.tag == "summary" && b.inlines == nil {
				b.inlines = inlinesOf(k, st, url)
				continue
			}
			rest = append(rest, k)
		}
		if b.inlines == nil {
			b.inlines = []mdInline{{text: "详细信息"}}
		}
		b.blocks = hblocks(rest, 0)
		h.emit(b)
	default:
		// 认不出的标签（<span>、<sup>、<font> 等）只留里面的内容
		h.kids(n, st, url, pre)
	}
}

func (h *hconv) kids(n *hnode, st mdStyle, url string, pre bool) {
	for _, k := range n.kids {
		h.node(k, st, url, pre)
	}
}

func hasAttr(a map[string]string, key string) bool {
	_, ok := a[key]
	return ok
}

// inlinesOf 是标签里的行内内容（标题、<summary>），里面的块拼成一段，用换行隔开。
func inlinesOf(n *hnode, st mdStyle, url string) []mdInline {
	h := &hconv{}
	h.kids(n, st, url, false)
	h.flush()
	var out []mdInline
	for i, b := range h.out {
		if i > 0 {
			out = append(out, mdInline{text: "\n"})
		}
		out = append(out, b.inlines...)
	}
	return out
}

// tableOf 转换 <table>：格子里都只有一段文字、第一行是 <th> 时照 GFM 表格画（列宽按内容量），
// 否则是格子里能放任意块的 mdCells。
func tableOf(n *hnode) mdBlock {
	var rows [][]*hnode
	var walk func(n *hnode)
	walk = func(n *hnode) {
		for _, k := range n.kids {
			switch k.tag {
			case "thead", "tbody", "tfoot":
				walk(k)
			case "tr":
				var row []*hnode
				for _, c := range k.kids {
					if c.tag == "td" || c.tag == "th" {
						row = append(row, c)
					}
				}
				rows = append(rows, row)
			}
		}
	}
	walk(n)
	g := &mdGrid{}
	simple := len(rows) > 0
	for ri, row := range rows {
		var cells []mdCell
		g.cols = max(g.cols, len(row))
		for ci, c := range row {
			cell := mdCell{header: c.tag == "th", valign: ui.Center, blocks: hblocks(c.kids, alignOf(c.attrs, 0))}
			switch strings.ToLower(c.attrs["valign"]) {
			case "top":
				cell.valign = ui.Start
			case "bottom":
				cell.valign = ui.End
			}
			for len(g.width) <= ci {
				g.width = append(g.width, mdLen{})
			}
			if w := parseLen(c.attrs["width"]); w.v > 0 && g.width[ci].v == 0 {
				g.width[ci] = w
			}
			if len(cell.blocks) > 1 || len(cell.blocks) == 1 && cell.blocks[0].kind != mdPara || hasImage(cell.blocks) ||
				ri == 0 && !cell.header || c.attrs["width"] != "" {
				simple = false
			}
			cells = append(cells, cell)
		}
		g.rows = append(g.rows, cells)
	}
	if simple {
		b := mdBlock{kind: mdTable, size: &mdTableSize{}}
		for ci := range g.cols {
			var a ui.Align
			if len(g.rows[0]) > ci && len(g.rows[0][ci].blocks) > 0 {
				a = g.rows[0][ci].blocks[0].textAlign
			}
			b.align = append(b.align, a)
		}
		for _, row := range g.rows {
			var r [][]mdInline
			for _, c := range row {
				var ins []mdInline
				if len(c.blocks) > 0 {
					ins = c.blocks[0].inlines
				}
				r = append(r, ins)
			}
			b.rows = append(b.rows, r)
		}
		return b
	}
	return mdBlock{kind: mdCells, grid: g}
}

func hasImage(bs []mdBlock) bool {
	for _, b := range bs {
		for _, in := range b.inlines {
			if in.img != nil {
				return true
			}
		}
	}
	return false
}

// media 搭带图片的段落、标题：按 <br> 和换行分行，每行里图片和文字依次排、放不下就折到下一行，
// 徽章一行排开、<p align="center"> 里的图居中。
func (r mdView) media(c *ui.Context, b mdBlock) {
	justify := ui.Start
	if b.textAlign != 0 {
		justify = b.textAlign
	}
	size, weight := r.body(), 0
	if b.kind == mdHeading {
		size, weight = r.heading(b.level), fwBold
	}
	var lines [][]mdInline
	line := []mdInline{}
	for _, in := range b.inlines {
		if in.img != nil {
			line = append(line, in)
			continue
		}
		parts := strings.Split(in.text, "\n")
		for i, p := range parts {
			if i > 0 {
				lines = append(lines, line)
				line = []mdInline{}
			}
			if p != "" {
				in.text = p
				line = append(line, in)
			}
		}
	}
	lines = append(lines, line)
	gap := r.scaled(4)
	ui.Column(c).Gap(gap).Children(func() {
		for _, line := range lines {
			if len(line) == 0 {
				continue
			}
			ui.Row(c).Wrap().Gap(gap).AlignItems(ui.Center).Justify(justify).Children(func() {
				var text []mdInline
				flush := func() {
					blank := true
					for _, in := range text {
						if strings.TrimSpace(in.text) != "" {
							blank = false
						}
					}
					if !blank {
						e := r.inline(c, text).FontSize(size).LineHeight(r.lead()).Shrink(1)
						if weight != 0 {
							e.FontWeight(weight)
						}
					}
					text = nil
				}
				// 一行只有一张图（截图、配图）时按版心收窄；和别的图、文字排在一起的（徽章、标题前的图标）按自己的宽度
				alone := len(line) == 1
				for _, in := range line {
					if in.img == nil {
						text = append(text, in)
						continue
					}
					flush()
					r.image(c, in.img, in.url, alone)
				}
				flush()
			})
		}
	})
}

// image 搭一张图片：按原图大小（写了 width、height 照写的），最宽不超过版心；载入前和载不了时显示说明文字。
// alone 是这一行只有它一张图。
// 包在链接里的图片点了打开链接。
func (r mdView) image(c *ui.Context, im *mdImg, link string, alone bool) {
	t := c.Theme()
	var e mdImgEntry
	if r.imgs != nil {
		e = r.imgs.get(c, im.src)
	}
	var el ui.Element
	if e.src == nil {
		label := im.alt
		if label == "" {
			label = "图片"
		}
		el = ui.Row(c).Padding(r.scaled(2), r.scaled(8)).Radius(rSmall).Background(codeBackground(t)).Shrink(1).Children(func() {
			ui.Text(c, label).FontSize(r.small()).TextColor(t.TextMuted)
		})
		if e.err != nil {
			el.Tooltip(im.src)
		}
	} else {
		img := ui.Image(c, e.src).MaxWidthPercent(100).Shrink(1)
		w, h := e.w, e.h
		switch {
		case im.width.pct:
			img.WidthPercent(im.width.v)
			w = 0
		case im.width.v > 0:
			if im.height.v > 0 && !im.height.pct {
				h = im.height.v * w / im.width.v
			}
			w = im.width.v
		case im.height.v > 0 && !im.height.pct && h > 0:
			w = w * im.height.v / h
		}
		switch {
		case w > 0 && alone:
			// 定宽的元素在窄的分栏里缩不下去，会把整列撑宽：改成铺满一行、最宽到原图
			img.WidthPercent(100).MaxWidth(w)
		case w > 0:
			img.Width(w)
		}
		if e.w > 0 && e.h > 0 {
			ratio := e.w / e.h
			if im.width.v > 0 && !im.width.pct && im.height.v > 0 && !im.height.pct {
				ratio = im.width.v / im.height.v
			}
			img.AspectRatio(ratio)
		}
		if im.alt != "" {
			img.Label(im.alt)
		}
		if im.title != "" {
			img.Tooltip(im.title)
		}
		el = img
	}
	if webURL(link) {
		el.Cursor(ui.CursorPointer)
		if el.Clicked() {
			c.OpenURL(link)
		}
	}
}

// grid 搭 HTML 表格：列宽照 width 写的（百分比按比例分，像素固定），没写的平分剩下的；
// 外框、格线和表头的底色同 GFM 表格的文件预览。
func (r mdView) grid(c *ui.Context, g *mdGrid) {
	if g.cols == 0 {
		return
	}
	t := c.Theme()
	tracks := make([]ui.Track, g.cols)
	var pct float32
	free := 0
	for i := range tracks {
		var w mdLen
		if i < len(g.width) {
			w = g.width[i]
		}
		switch {
		case w.pct:
			pct += w.v
		case w.v == 0:
			free++
		}
	}
	for i := range tracks {
		var w mdLen
		if i < len(g.width) {
			w = g.width[i]
		}
		switch {
		case w.pct:
			tracks[i] = ui.Fr(w.v)
		case w.v > 0:
			tracks[i] = ui.Fixed(w.v)
		case pct > 0 && pct < 100:
			tracks[i] = ui.Fr((100 - pct) / float32(free))
		case pct > 0:
			tracks[i] = ui.FitContent()
		default:
			tracks[i] = ui.Fr(1)
		}
	}
	ui.Grid(c).ColumnTracks(tracks...).Radius(rRow).BorderWidth(hairline).BorderColor(colors(t).line).Children(func() {
		for ri, row := range g.rows {
			for ci := range g.cols {
				var cell mdCell
				if ci < len(row) {
					cell = row[ci]
				}
				bottom, right := float32(hairline), float32(hairline)
				if ri == len(g.rows)-1 {
					bottom = 0
				}
				if ci == g.cols-1 {
					right = 0
				}
				box := ui.Column(c).Padding(8, 12).BorderWidth(0, right, bottom, 0).BorderColor(colors(t).line).
					AlignSelf(ui.Stretch).Justify(cmp.Or(cell.valign, ui.Center))
				if cell.header {
					box.Background(t.Surface).FontWeight(fwBold)
				}
				var rad [4]float32
				first, last := ri == 0, ri == len(g.rows)-1
				if ci == 0 {
					rad[0], rad[3] = corner(first), corner(last)
				}
				if ci == g.cols-1 {
					rad[1], rad[2] = corner(first), corner(last)
				}
				if rad != [4]float32{} {
					box.Radius(rad[0], rad[1], rad[2], rad[3])
				}
				box.Children(func() { r.page(c, cell.blocks) })
			}
		}
	})
}

// details 搭 <details>：<summary> 一行可点，前面的箭头展开时转向下；内容缩进一格。
func (r mdView) details(c *ui.Context, b mdBlock) {
	t := c.Theme()
	ui.Column(c).Children(func() {
		row := ui.Row(c).Gap(6).AlignItems(ui.Center).AlignSelf(ui.Start).Role(ui.RoleButton).Cursor(ui.CursorPointer)
		row.Children(func() {
			turn := float32(0)
			if *b.open {
				turn = 90
			}
			ui.Icon(c, iconChevron).FontSize(r.small()).TextColor(t.TextMuted).Rotate(turn)
			r.inline(c, b.inlines).FontSize(r.body()).LineHeight(r.lead()).Unselectable()
		})
		if row.Clicked() {
			*b.open = !*b.open
		}
		if *b.open && len(b.blocks) > 0 {
			ui.Column(c).Padding(r.scaled(6), 0, 0, r.scaled(20)).Children(func() { r.page(c, b.blocks) })
		}
	})
}
