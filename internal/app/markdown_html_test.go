package app

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func kindsOf(bs []mdBlock) []mdKind {
	var out []mdKind
	for _, b := range bs {
		out = append(out, b.kind)
	}
	return out
}

func TestMarkdownHTMLHeader(t *testing.T) {
	src := "<h1 align=\"center\">\n  <img src=\"icon.png\" width=\"64\" /> Posse\n</h1>\n\n" +
		"<p align=\"center\">\n  <strong>多 agent 终端</strong><br/>\n  窗口随时关\n</p>\n\n" +
		"<!-- 注释不显示 -->\n\n" +
		"<p align=\"center\"><a href=\"https://x.dev\"><img src=\"https://img.shields.io/a.svg\" alt=\"stars\"></a> <img src=\"b.png\"></p>\n\n正文\n"
	bs := parseMarkdownDoc(src)
	if want := []mdKind{mdHeading, mdPara, mdPara, mdPara}; !reflect.DeepEqual(kindsOf(bs), want) {
		t.Fatalf("kinds = %v, blocks = %+v", kindsOf(bs), bs)
	}
	h := bs[0]
	if h.level != 1 || h.textAlign != ui.Center || len(h.inlines) != 2 || h.inlines[0].img == nil ||
		h.inlines[0].img.width != (mdLen{64, false}) || h.inlines[1].text != " Posse" {
		t.Errorf("标题 = %+v", h)
	}
	p := bs[1]
	if p.textAlign != ui.Center || len(p.inlines) != 2 || p.inlines[0].style != mdBold || p.inlines[1].text != "\n窗口随时关" {
		t.Errorf("段落 = %+v", p.inlines)
	}
	badges := bs[2].inlines
	if len(badges) != 3 || badges[0].img == nil || badges[0].url != "https://x.dev" || badges[0].img.alt != "stars" || badges[2].img == nil {
		t.Errorf("徽章 = %+v", badges)
	}
	if !bs[3].blank || bs[3].inlines[0].text != "正文" {
		t.Errorf("正文 = %+v", bs[3])
	}
	// 历史页不解释 HTML，照原文显示
	if bs := parseMarkdown(src); bs[0].kind != mdCode {
		t.Errorf("历史页 = %+v", bs[0])
	}
}

// Orca、cmux 的 README 那样：<td> 里空了行写 Markdown，HTML 块被空行切成几段。
func TestMarkdownHTMLTable(t *testing.T) {
	src := "<table>\n<tr>\n<td width=\"40%\" valign=\"top\">\n\n### 手机\n\n看终端、批权限。\n\n</td>\n" +
		"<td width=\"60%\"><img src=\"m.jpg\" alt=\"手机\" width=\"100%\" /></td>\n</tr>\n<tr>\n<td>二</td>\n<td>三</td>\n</tr>\n</table>\n"
	bs := parseMarkdownDoc(src)
	if len(bs) != 1 || bs[0].kind != mdCells {
		t.Fatalf("blocks = %+v", bs)
	}
	g := bs[0].grid
	if g.cols != 2 || len(g.rows) != 2 || !reflect.DeepEqual(g.width, []mdLen{{40, true}, {60, true}}) {
		t.Fatalf("grid = %+v", g)
	}
	cell := g.rows[0][0]
	if cell.valign != ui.Start || !reflect.DeepEqual(kindsOf(cell.blocks), []mdKind{mdHeading, mdPara}) {
		t.Errorf("第一格 = %+v", cell)
	}
	if img := g.rows[0][1].blocks[0].inlines[0].img; img == nil || img.width != (mdLen{100, true}) {
		t.Errorf("第二格 = %+v", g.rows[0][1])
	}
	// 只有文字、表头是 <th> 的照 GFM 表格画
	bs = parseMarkdownDoc("<table><tr><th>项目</th><th align=\"right\">数据</th></tr><tr><td>内存</td><td>21 MB</td></tr></table>\n")
	if len(bs) != 1 || bs[0].kind != mdTable || len(bs[0].rows) != 2 || bs[0].align[1] != ui.End || bs[0].rows[1][1][0].text != "21 MB" {
		t.Errorf("简单表格 = %+v", bs)
	}
}

func TestMarkdownHTMLMisc(t *testing.T) {
	src := "<details>\n<summary>展开看 <b>更多</b></summary>\n\n- 一\n- 二\n\n</details>\n\n" +
		"<div align=\"center\">\n\n## 居中的 Markdown\n\n</div>\n\n" +
		"<pre><code class=\"language-go\">a &lt; b\n</code></pre>\n\n" +
		"<script>alert(1)</script>\n\n" +
		"行内 <kbd>⌘K</kbd> 和 <sup>上标</sup>、![图](a.png \"标题\")\n"
	bs := parseMarkdownDoc(src)
	if want := []mdKind{mdDetails, mdHeading, mdCode, mdPara}; !reflect.DeepEqual(kindsOf(bs), want) {
		t.Fatalf("kinds = %v, blocks = %+v", kindsOf(bs), bs)
	}
	d := bs[0]
	if *d.open || len(d.inlines) != 2 || d.inlines[1].style != mdBold || len(d.blocks) != 1 || d.blocks[0].kind != mdList {
		t.Errorf("details = %+v", d)
	}
	if bs[1].textAlign != ui.Center {
		t.Errorf("div 里的标题没居中：%+v", bs[1])
	}
	if bs[2].lang != "go" || bs[2].code != "a < b" {
		t.Errorf("pre = %+v", bs[2])
	}
	ins := bs[3].inlines
	want := []mdInline{{text: "行内 "}, {text: "⌘K", style: mdMono}, {text: " 和 上标、"}, {img: &mdImg{src: "a.png", alt: "图", title: "标题"}}}
	if !reflect.DeepEqual(ins, want) {
		t.Errorf("行内 = %+v", ins)
	}
}
