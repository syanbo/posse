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
