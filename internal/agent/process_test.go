package agent

import (
	"os"
	"path/filepath"
	"testing"
)

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

// agent 这个入口 grok 和 Cursor 都装了，按它实际指向的程序认。
func TestAgentEntry(t *testing.T) {
	dir := t.TempDir()
	link := func(name, target string) string {
		p := filepath.Join(dir, name)
		os.WriteFile(filepath.Join(dir, target), nil, 0o755)
		if err := os.Symlink(filepath.Join(dir, target), p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	grok := link("agent", "grok-1.0.70-macos-aarch64")
	if got := fromProcess(grok, []string{"agent"}); got != "grok" {
		t.Errorf("grok's agent -> %q", got)
	}
	if got := ShimTarget("agent", grok); got != "grok" {
		t.Errorf("shim target %q", got)
	}
	os.MkdirAll(filepath.Join(dir, "cursor-agent"), 0o755)
	cursor := link("agent2", "cursor-agent/cursor-agent")
	if got := ShimTarget("agent", cursor); got != "cursor-agent" {
		t.Errorf("cursor's agent shim target %q", got)
	}
	if got := ShimTarget("agent", filepath.Join(dir, "other")); got != "" {
		t.Errorf("unknown agent shim target %q", got)
	}
}

func TestSessionFromArgs(t *testing.T) {
	const id = "7a4f107a-8c9d-4ba0-b383-1101485378a2"
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"grok", []string{"grok", "--session-id", id, "--rules", "x"}, id},
		{"grok", []string{"agent", "-s", id}, id},
		{"grok", []string{"grok", "--resume=" + id}, id},
		{"grok", []string{"grok", "-r", "我的会话"}, ""},
		{"grok", []string{"grok", "-r"}, ""},
		{"grok", []string{"grok", "--", "--session-id", id}, ""},
		{"claude", []string{"claude", "--resume", id}, id},
		{"cursor", []string{"cursor-agent", "--resume", id}, id},
		{"codex", []string{"codex", "-c", "x=1", "resume", id}, id},
		{"codex", []string{"codex", "resume"}, ""},
		{"grok", nil, ""},
	} {
		if got := SessionFromArgs(c.name, c.args); got != c.want {
			t.Errorf("SessionFromArgs(%s, %q) = %q, want %q", c.name, c.args, got, c.want)
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
