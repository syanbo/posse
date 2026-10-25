package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestPermissionArgs(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	if m := PermissionMode(l.Home); m != PermissionSkip {
		t.Fatalf("default mode %q", m)
	}
	for name, flag := range map[string]string{
		"claude": " --dangerously-skip-permissions",
		"codex":  " --dangerously-bypass-approvals-and-sandbox",
		"grok":   " --permission-mode bypassPermissions",
		"cursor": " --yolo",
	} {
		r, err := l.Launch(name, Model{}, "do it")
		if err != nil {
			t.Fatal(err)
		}
		if cmd := r.Argv[len(r.Argv)-1]; !strings.Contains(cmd, flag+" 'do it'") {
			t.Errorf("launch %s: %q", name, cmd)
		}
		r, err = l.Resume(name, "sid", "")
		if err != nil {
			t.Fatal(err)
		}
		if cmd := r.Argv[len(r.Argv)-1]; !strings.Contains(cmd, flag) {
			t.Errorf("resume %s: %q", name, cmd)
		}
	}
	// codex 的顶层参数要在 resume 子命令前面
	r, _ := l.Resume("codex", "sid", "")
	if cmd := r.Argv[len(r.Argv)-1]; !strings.Contains(cmd, " --dangerously-bypass-approvals-and-sandbox resume 'sid'") {
		t.Errorf("codex resume: %q", cmd)
	}

	got, _ := l.ManualArgs("claude", []string{"-c"})
	if !slices.Contains(got, "--dangerously-skip-permissions") {
		t.Errorf("manual claude: %q", got)
	}
	// 用户自己指定了权限时不补
	for _, c := range []struct {
		cmd, flag string
		args      []string
	}{
		{"claude", "--dangerously-skip-permissions", []string{"--permission-mode=plan"}},
		{"codex", "--dangerously-bypass-approvals-and-sandbox", []string{"-a", "never"}},
		{"codex", "--dangerously-bypass-approvals-and-sandbox", []string{"--approve-for-me"}},
		{"codex", "--dangerously-bypass-approvals-and-sandbox", []string{"--sandbox", "read-only", "resume"}},
		{"cursor-agent", "--yolo", []string{"--force"}},
	} {
		got, _ := l.ManualArgs(c.cmd, c.args)
		if n := strings.Count(strings.Join(got, " "), c.flag); n != 0 {
			t.Errorf("%s %q -> %q", c.cmd, c.args, got)
		}
	}

	if err := SetPermissionMode(l.Home, PermissionAsk); err != nil {
		t.Fatal(err)
	}
	if m := PermissionMode(l.Home); m != PermissionAsk {
		t.Fatalf("mode %q after set", m)
	}
	r, _ = l.Launch("claude", Model{}, "")
	if cmd := r.Argv[len(r.Argv)-1]; strings.Contains(cmd, "dangerously") {
		t.Errorf("ask mode still skips: %q", cmd)
	}
}
