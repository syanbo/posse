package agent

import "testing"

func TestFromProcess(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"claude"}, "claude"},
		{[]string{"/Users/x/.local/bin/claude", "--resume"}, "claude"},
		{[]string{"node", "/opt/homebrew/bin/codex"}, "codex"},
		{[]string{"/Users/x/.grok/bin/grok", "--session-id", "s"}, "grok"},
		{[]string{"/Users/x/.local/bin/cursor-agent", "--use-system-ca", "/Users/x/.local/share/cursor-agent/versions/1/index.js"}, "cursor"},
		{[]string{"/Users/x/.local/bin/agent", "/Users/x/.local/share/cursor-agent/versions/1/index.js"}, "cursor"},
		{[]string{"/Users/x/bin/agent"}, ""},
		{[]string{"-zsh"}, ""},
		{[]string{"vim", "a.go"}, ""},
		{nil, ""},
	} {
		if got := FromProcess(c.args); got != c.want {
			t.Errorf("FromProcess(%q) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestTitleOnlyStatus(t *testing.T) {
	for _, c := range []struct {
		agent, title, current, want string
		ok                          bool
	}{
		{"claude", "◑ 修 bug", StatusIdle, StatusWorking, true},
		{"claude", "◑ 修 bug", StatusWorking, "", false},
		{"claude", "✳ 修 bug", StatusWorking, StatusIdle, true},
		{"claude", "✳ Claude Code", StatusIdle, "", false},
		{"claude", "zsh", StatusIdle, "", false},
		{"codex", "Action Required", StatusIdle, StatusWaiting, true},
		{"grok", "⠹ - Waiting for response… - grok", StatusIdle, StatusWorking, true},
	} {
		got, ok := TitleOnlyStatus(c.agent, c.title, c.current)
		if got != c.want || ok != c.ok {
			t.Errorf("TitleOnlyStatus(%q, %q, %q) = %q %v, want %q %v", c.agent, c.title, c.current, got, ok, c.want, c.ok)
		}
	}
}
