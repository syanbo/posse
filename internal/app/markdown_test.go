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
