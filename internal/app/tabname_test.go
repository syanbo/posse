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
	}
	for _, tc := range cases {
		if name, detail := tabLabel(tc.term); name != tc.name || detail != tc.detail {
			t.Errorf("tabLabel(%+v) = %q, %q; want %q, %q", tc.term, name, detail, tc.name, tc.detail)
		}
	}
}
