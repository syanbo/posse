package mobile

import (
	"testing"
	"time"
)

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

func TestPosseStatus(t *testing.T) {
	cases := []struct {
		t    Terminal
		want string
	}{
		{Terminal{Running: true, Status: "starting"}, "starting"},
		{Terminal{Running: true, Status: "working"}, "working"},
		{Terminal{Running: true, Status: "idle"}, "idle"},
		{Terminal{Running: true, Status: "idle", Unread: true}, "unread"},
		// 等任务比没看要紧，和桌面一样
		{Terminal{Running: true, Status: "idle", Unread: true, Tasks: true}, "tasks"},
		{Terminal{Running: true, Status: "working", Tasks: true}, "tasks"},
		{Terminal{Lost: true}, "lost"},
		{Terminal{Status: "idle"}, ""},
		{Terminal{Running: true}, ""},
	}
	for _, c := range cases {
		if got := posseStatus(c.t); got != c.want {
			t.Errorf("posseStatus(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
	// Orca 的 App 只看 state：等任务时照旧是 working
	row := agentRow(Terminal{Running: true, Status: "working", Tasks: true}, time.Now())
	if row["state"] != "working" || row["posseStatus"] != "tasks" {
		t.Fatalf("row = %v / %v", row["state"], row["posseStatus"])
	}
}

// 工作区全是还没恢复的终端时，手机也要知道去拉会话列表；在跑的个数照旧只数在跑的
func TestWorktreeRowCountsLost(t *testing.T) {
	w := Workspace{ID: "w", ProjectID: "p", Terminals: []Terminal{{ID: "a", Lost: true}, {ID: "b", Lost: true}}}
	row := worktreeRow(State{}, w, time.Now())
	if row["liveTerminalCount"] != 0 || row["posseTerminalCount"] != 2 {
		t.Fatalf("live = %v, posse = %v", row["liveTerminalCount"], row["posseTerminalCount"])
	}
}

// agent 退出回 shell 时标题保持不变，快照版本仍应递增以便手机去掉 launchAgent 并更新图标。
func TestSessionTabsVersionChangesWhenAgentExitsWithSameTitle(t *testing.T) {
	srv := &Server{tabs: map[string]*tabState{}}
	w := Workspace{ID: "w1", ProjectID: "p1", Terminals: []Terminal{
		{ID: "t1", Title: "Step 1 审查", Agent: "codex", Running: true},
	}}
	v1 := srv.sessionTabs(State{}, w)["snapshotVersion"].(int)
	w.Terminals[0].Agent = ""
	res := srv.sessionTabs(State{}, w)
	v2 := res["snapshotVersion"].(int)
	if v2 == v1 {
		t.Fatalf("snapshotVersion did not change on agent exit: v1=%d v2=%d", v1, v2)
	}
	tabs := res["tabs"].([]map[string]any)
	if tabs[0]["title"] != "Step 1 审查" || tabs[0]["launchAgent"] != nil {
		t.Fatalf("tab after exit = %+v", tabs[0])
	}
}
