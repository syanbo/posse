package app

import (
	"reflect"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestParseMarkdown(t *testing.T) {
	src := "## 结论\n\n可以**重写**，见 [文档](https://x.dev) 和 `hive daemon`\n第二行 ~~旧的~~\n\n" +
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
		{text: " 和 "}, {text: "hive daemon", style: mdMono}, {text: "\n第二行 "}, {text: "旧的", style: mdStrike},
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
	got := markRanges("Hive 的 hive，hiveX", []string{"hive", "iv"})
	if want := [][2]int{{0, 4}, {9, 13}, {16, 20}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("markRanges = %v, want %v", got, want)
	}
}

// 表格比正文宽时：长句折行能放下的铺满正文宽，拆不开的代码放不下的按内容宽左右滚，别的列不被压扁
func TestMarkdownTableWidth(t *testing.T) {
	long := "这一格的句子很长很长，要折行才放得下，再多写几个字让它比窗口宽得多，还要再长一些才够"
	fit := parseMarkdown("| 项目 | 说明 |\n|---|---|\n| 内存 | " + long + " |\n")
	wide := parseMarkdown("| 入口 | 写法 |\n|---|---|\n| 私聊 拍照 | `openAlbum(this, multiple(9).compress(true).allowGif(true).cameraInAlbum(false), CHOOSE_REQUEST)` |\n")
	show := func(bs []mdBlock) *ui.Tester {
		tt := ui.NewTester(func(c *ui.Context) {
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
	if before.H > 60 {
		t.Fatalf("code cell %+v should wrap only at spaces", before)
	}
	// 整张表比窗口宽，能往左滚
	tt.Scroll(200, 50, 100, 0)
	tt.Frame()
	if after, _ := tt.Find(code); after.X >= before.X {
		t.Fatalf("table did not scroll: %+v -> %+v", before, after)
	}
}
