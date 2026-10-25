package mobile

import "testing"

// 输入框发来的多行文字按一次粘贴送进去，不让第一个换行把前半段提交掉
func TestPasteText(t *testing.T) {
	for _, c := range []struct {
		text      string
		bracketed bool
		want      string
	}{
		{"一行", true, "一行"},
		{"第一行\n第二行", true, "\x1b[200~第一行\n第二行\x1b[201~"},
		{"a\r\nb", true, "\x1b[200~a\r\nb\x1b[201~"},
		// 不能借文字里的结束标记提前结束粘贴
		{"a\n\x1b[201~rm -rf x\n", true, "\x1b[200~a\nrm -rf x\n\x1b[201~"},
		{"第一行\n第二行", false, "第一行\n第二行"},
	} {
		if got := string(pasteText(c.text, c.bracketed)); got != c.want {
			t.Errorf("pasteText(%q, %v) = %q, want %q", c.text, c.bracketed, got, c.want)
		}
	}
}
