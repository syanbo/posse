package history

import (
	"reflect"
	"testing"
)

func TestSplitCJK(t *testing.T) {
	for in, want := range map[string]string{
		"清理磁盘":      " 清 理 磁 盘",
		"P2编排 done": "P2 编 排 done",
		"hook.json": "hook.json",
		"カタカナ와한글":   " カ タ カ ナ 와 한 글",
		"中文，标点":     " 中 文 ， 标 点",
	} {
		if got := splitCJK(in); got != want {
			t.Errorf("splitCJK(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMatchExpr(t *testing.T) {
	for in, want := range map[string]string{
		"清理磁盘":                 `" 清 理 磁 盘"`,
		"hook 磁盘":              `"hook" * AND " 磁 盘"`,
		`"exact phrase" posse`: `"exact phrase" AND "posse" *`,
		`say "hi`:              `"say" * AND "hi"`,
		"view.go":              `"view.go" *`,
		"-- ...":               ``,
		`a"b`:                  `"a""b" *`,
	} {
		if got := matchExpr(parseTerms(in)); got != want {
			t.Errorf("matchExpr(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSnippet(t *testing.T) {
	text := "第一行\n\n这里讨论了 Posse 的历史搜索，以及 posse 怎么索引会话。" + string(make([]rune, 0)) + "后面还有很多很多内容内容内容内容内容内容内容"
	got, spans := snippet(text, needles(parseTerms("posse 搜索")), 30)
	if got[:3] != "…" && got[:len("第一行")] != "第一行" {
		t.Fatalf("snippet = %q", got)
	}
	var marked []string
	for _, s := range spans {
		marked = append(marked, got[s.Start:s.End])
	}
	if !reflect.DeepEqual(marked, []string{"Posse", "搜索", "posse"}) {
		t.Fatalf("snippet %q marked %q", got, marked)
	}
}
