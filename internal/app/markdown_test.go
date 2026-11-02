package app

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestWikiLinks(t *testing.T) {
	bs := parseMarkdown("界面偏好见 [[feedback-posse-ui-taste]]，打包见 [[feedback-repackage|重新打包]]，`[[不算]]`。")
	want := []mdInline{
		{text: "界面偏好见 "}, {text: "feedback-posse-ui-taste", url: "wiki:feedback-posse-ui-taste"}, {text: "，打包见 "},
		{text: "重新打包", url: "wiki:feedback-repackage"}, {text: "，"}, {text: "[[不算]]", style: mdMono}, {text: "。"},
	}
	if !reflect.DeepEqual(bs[0].inlines, want) {
		t.Errorf("inlines = %+v", bs[0].inlines)
	}
}

func TestFrontMatter(t *testing.T) {
	src := "---\nname: project-posse-status\ndescription: \"重新打包\"指正式包\nmetadata:\n  type: project\ntags:\n  - a\n  - b\n---\n\n正文\n"
	bs := parseMarkdown(src)
	if len(bs) != 2 || bs[0].kind != mdMeta || bs[1].kind != mdPara {
		t.Fatalf("blocks = %+v", bs)
	}
	want := []mdField{{0, "name", "project-posse-status"}, {0, "description", "\"重新打包\"指正式包"}, {0, "metadata", ""}, {1, "type", "project"}, {0, "tags", "a, b"}}
	if !reflect.DeepEqual(bs[0].meta, want) {
		t.Errorf("meta = %+v", bs[0].meta)
	}
	// 记下源文件里块前面有没有空行：文件预览只在空了行的地方空开
	bs = parseMarkdown("测试约束：\n- 一\n- 二\n\n下一段\n")
	if len(bs) != 3 || bs[1].kind != mdList || bs[1].blank || !bs[2].blank {
		t.Errorf("blank = %+v", bs)
	}
	bs = parseMarkdown("1. 一\n   - 二\n\n| 项目 | 数据 |\n|---|---|\n| 内存 | 21 MB |\n```sh\nls\n```\n")
	if len(bs) != 3 || bs[1].kind != mdTable || !bs[1].blank || bs[2].blank {
		t.Errorf("表格前的空行 = %+v", bs)
	}
	// 没有收尾的 ---、或者中间不是键值的，照原文交给 goldmark
	for _, src := range []string{"---\nname: x\n\n正文", "---\n\n就是一条分割线\n---\n"} {
		if bs := parseMarkdown(src); len(bs) > 0 && bs[0].kind == mdMeta {
			t.Errorf("%q 不该当头部：%+v", src, bs)
		}
	}
}

func TestParseMarkdown(t *testing.T) {
	src := "## 结论\n\n可以**重写**，见 [文档](https://x.dev) 和 `posse daemon`\n第二行 ~~旧的~~\n\n" +
		"| 项目 | 数据 |\n|---|--:|\n| 内存 | 1.1 GB |\n\n" +
		"1. 终端\n2. 编排\n   - worktree\n\n- [x] 调研\n- [ ] 手机\n\n> 引用\n\n```go\nfunc main() {}\n```\n\n---\n\n<details>\nraw\n</details>"
	bs := parseMarkdown(src)
	kinds := []mdKind{}
	for _, b := range bs {
		kinds = append(kinds, b.kind)
	}
	if want := []mdKind{mdHeading, mdPara, mdTable, mdList, mdList, mdQuote, mdCode, mdRule, mdCode}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	if bs[0].level != 2 || bs[0].inlines[0].text != "结论" {
		t.Errorf("heading = %+v", bs[0])
	}
	want := []mdInline{
		{text: "可以"}, {text: "重写", style: mdBold}, {text: "，见 "}, {text: "文档", url: "https://x.dev"},
		{text: " 和 "}, {text: "posse daemon", style: mdMono}, {text: "\n第二行 "}, {text: "旧的", style: mdStrike},
	}
	if !reflect.DeepEqual(bs[1].inlines, want) {
		t.Errorf("inlines = %+v", bs[1].inlines)
	}
	if tb := bs[2]; len(tb.rows) != 2 || tb.rows[1][1][0].text != "1.1 GB" || tb.align[1] != ui.End {
		t.Errorf("table = %+v", tb)
	}
	if l := bs[3]; !l.ordered || l.start != 1 || len(l.items) != 2 || l.items[1].blocks[1].kind != mdList {
		t.Errorf("ordered list = %+v", l)
	}
	if l := bs[4]; l.items[0].check != 2 || l.items[1].check != 1 || l.items[0].blocks[0].inlines[0].text != "调研" {
		t.Errorf("task list = %+v", l)
	}
	if bs[6].code != "func main() {}" || bs[8].code != "<details>\nraw\n</details>" {
		t.Errorf("code = %q, html = %q", bs[6].code, bs[8].code)
	}
}

func TestMarkRanges(t *testing.T) {
	got := markRanges("Posse 的 posse，posseX", []string{"posse", "os"})
	if want := [][2]int{{0, 5}, {10, 15}, {18, 23}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("markRanges = %v, want %v", got, want)
	}
}

// 表格比正文宽时：长句折行能放下的铺满正文宽，拆不开的代码放不下的按内容宽左右滚，别的列不被压扁
func TestMarkdownTableWidth(t *testing.T) {
	long := "这一格的句子很长很长，要折行才放得下，再多写几个字让它比窗口宽得多，还要再长一些才够"
	fit := parseMarkdown("| 项目 | 说明 |\n|---|---|\n| 内存 | " + long + " |\n")
	wide := parseMarkdown("| 入口 | 写法 |\n|---|---|\n| 私聊 拍照 | `openAlbum(this, multiple(9).compress(true).allowGif(true).cameraInAlbum(false), CHOOSE_REQUEST)` |\n")
	show := func(bs []mdBlock) *ui.Tester {
		tt := newTester(func(c *ui.Context) {
			ui.Column(c).Fill().Children(func() { mdView{}.blocks(c, bs) })
		}, 400, 400)
		tt.Frame()
		tt.Frame()
		return tt
	}
	tt := show(fit)
	if rl, ok := tt.Find(long); !ok || rl.X+rl.W > 400 || rl.H < 30 {
		t.Fatalf("long cell %+v should wrap inside the window", rl)
	}

	tt = show(wide)
	short, _ := tt.Find("私聊 拍照")
	if short.H > 25 {
		t.Fatalf("short cell squeezed: %+v", short)
	}
	code := "openAlbum(this, multiple(9).compress(true).allowGif(true).cameraInAlbum(false), CHOOSE_REQUEST)"
	before, _ := tt.Find(code)
	if before.H > 3*fsBody*1.45+1 { // 两个空格，最多三行
		t.Fatalf("code cell %+v should wrap only at spaces", before)
	}
	// 整张表比窗口宽，能往左滚
	tt.Scroll(200, 50, 100, 0)
	tt.Frame()
	if after, _ := tt.Find(code); after.X >= before.X {
		t.Fatalf("table did not scroll: %+v -> %+v", before, after)
	}
}

// mermaid 代码块画好后换成图，可以切回源码；写错的照代码块显示，上面一行报错
func TestMermaidBlock(t *testing.T) {
	good := "flowchart LR\n  A[开始] --> B[结束]"
	bs := parseMarkdown("```mermaid\n" + good + "\n```\n\n```mermaid\nsequenceDiagram\n  A->>B hi\n```\n")
	defer delete(mermaidSource, good)
	tt := newTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { mdView{}.blocks(c, bs) })
	}, 800, 900)
	until(t, tt, "mermaid", func() bool { return !diagramsPending() })
	tt.Frame()
	shows := func(prefix string) bool {
		return slices.ContainsFunc(tt.Texts(), func(s string) bool { return strings.HasPrefix(s, prefix) })
	}
	if shows(good) {
		t.Error("画好的图还显示着源码")
	}
	if _, ok := tt.Find("Mermaid 图"); !ok {
		t.Error("没有图")
	}
	if !shows("画不出图：") {
		t.Error("写错的图没有报错")
	}
	if err := tt.Click("查看源码"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !shows(good) || !mermaidSource[good] {
		t.Error("切不到源码")
	}
}

// 一篇文档里的图比缓存上限多时，画完之后再搭界面全部命中，不会互相挤掉、每帧重画
func TestMermaidManyBlocks(t *testing.T) {
	var src strings.Builder
	for i := range diagramCacheMax + 6 {
		fmt.Fprintf(&src, "```mermaid\nflowchart LR\n  A%d --> B%d\n```\n\n", i, i)
	}
	bs := parseMarkdown(src.String())
	tt := newTester(func(c *ui.Context) {
		ui.Scroll(c).Fill().Children(func() { mdView{}.blocks(c, bs) })
	}, 800, 900)
	until(t, tt, "mermaid", func() bool { return !diagramsPending() })
	tt.Frame()
	if diagramsPending() {
		t.Fatal("画好的图又被重画")
	}
}
