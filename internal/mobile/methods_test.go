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

// 工作区名和桌面侧栏一样：项目目录是项目名，posse 建的 worktree 是工作区名，加进来的 worktree 是当前分支
func TestDisplayName(t *testing.T) {
	st := State{Projects: []Project{{ID: "p1", Name: "demo"}}}
	for _, c := range []struct {
		w    Workspace
		want string
	}{
		{Workspace{ProjectID: "p1", Kind: "main", Name: "main", Branch: "dev"}, "demo"},
		{Workspace{ProjectID: "p1", Kind: "worktree", Name: "login", Branch: "feat/login"}, "login"},
		{Workspace{ProjectID: "p1", Kind: "folder", Name: "实验", Branch: "dev"}, "实验"},
		{Workspace{ProjectID: "p1", Kind: "folder", Name: "demo", Branch: "fix", Attached: true, Shown: "fix"}, "fix"},
		{Workspace{ProjectID: "p1", Kind: "folder", Name: "demo", Branch: "1a2b3c4d", Attached: true}, "demo"},
	} {
		if got := displayName(st, c.w); got != c.want {
			t.Errorf("displayName(%+v) = %q, want %q", c.w, got, c.want)
		}
	}
}
