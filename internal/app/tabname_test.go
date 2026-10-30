package app

import "testing"

func TestTabLabelCustomName(t *testing.T) {
	cases := []struct {
		term         terminalInfo
		name, detail string
	}{
		{terminalInfo{Agent: "claude", Title: "✳ Claude Code", Name: "修登录", Running: true}, "Claude", "修登录"},
		{terminalInfo{Agent: "claude", AgentState: &agentState{Title: "1020｜修复｜x"}, Name: "修登录", Running: true}, "Claude", "修登录"},
		{terminalInfo{Title: "zsh", Name: "构建", Running: true}, "构建", ""},
		{terminalInfo{Title: "zsh", Running: true}, "zsh", ""},
		// 还没恢复的照旧写常驻进程记下的名字，退出了的才标出来
		{terminalInfo{Agent: "claude", Title: "1020｜修复｜x", Lost: true}, "Claude", "1020｜修复｜x"},
		{terminalInfo{Title: "~/r", Lost: true}, "~/r", ""},
		{terminalInfo{Title: "zsh"}, "zsh", "已退出"},
	}
	for _, tc := range cases {
		if name, detail := tabLabel(tc.term); name != tc.name || detail != tc.detail {
			t.Errorf("tabLabel(%+v) = %q, %q; want %q, %q", tc.term, name, detail, tc.name, tc.detail)
		}
	}
}

// 标签名交给常驻进程之前先显示 ui.json 里的；常驻进程报上来一样的名字后不再记着，
// 之后再改名照样先显示本地的。
func TestSyncTermName(t *testing.T) {
	a := &app{state: uiState{TermNames: map[string]string{"t1": "旧版存的名字"}}}
	term := terminalInfo{ID: "t1"}
	a.syncTermName(&term)
	if term.Name != "旧版存的名字" || a.nameSent["t1"] != "旧版存的名字" {
		t.Fatalf("pending name %q sent %v", term.Name, a.nameSent)
	}
	term = terminalInfo{ID: "t1", Name: "旧版存的名字"}
	a.syncTermName(&term)
	if _, ok := a.state.TermNames["t1"]; ok {
		t.Fatal("name the daemon already has should leave ui.json")
	}
	// 恢复默认：常驻进程还报着旧名字时显示默认
	a.renameTerminal("t1", "")
	term = terminalInfo{ID: "t1", Name: "旧版存的名字"}
	a.syncTermName(&term)
	if term.Name != "" {
		t.Fatalf("cleared name shows %q", term.Name)
	}
	term = terminalInfo{ID: "t1"}
	a.syncTermName(&term)
	if len(a.state.TermNames) != 0 {
		t.Fatalf("left %v", a.state.TermNames)
	}
}

func TestTabRef(t *testing.T) {
	cases := []struct {
		term terminalInfo
		task *taskInfo
		want string
	}{
		{terminalInfo{ID: "t_1", Title: "zsh", Running: true}, nil, "zsh：终端 t_1（posse terminal read t_1）"},
		{
			terminalInfo{ID: "t_2", Agent: "cursor", Name: "修登录", AgentState: &agentState{SessionID: "s1"}, Running: true},
			&taskInfo{ID: "k_3"},
			"Cursor「修登录」：终端 t_2（posse terminal read t_2），任务 k_3（posse task show k_3），会话 s1（posse history show s1）",
		},
		// 退出了的不把「已退出」写进名字
		{terminalInfo{ID: "t_4", Agent: "codex"}, nil, "Codex：终端 t_4（posse terminal read t_4）"},
		{terminalInfo{ID: "t_5", Agent: "codex", Name: "修登录"}, nil, "Codex「修登录」：终端 t_5（posse terminal read t_5）"},
		{terminalInfo{ID: "t_6", Name: "已退出", Running: true}, nil, "已退出：终端 t_6（posse terminal read t_6）"},
	}
	for _, tc := range cases {
		if got := tabRef(tc.term, tc.task); got != tc.want {
			t.Errorf("tabRef(%+v) = %q, want %q", tc.term, got, tc.want)
		}
	}
}
