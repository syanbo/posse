package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestManualArgs(t *testing.T) {
	l := Launcher{Exe: "/bin/hive", Home: t.TempDir()}
	SetPermissionMode(l.Home, PermissionAsk)
	settings, plugin := filepath.Join(l.Home, "claude-hooks.json"), filepath.Join(l.Home, "claude-plugin")
	for _, c := range []struct {
		cmd    string
		args   []string
		hooked bool
	}{
		{"claude", nil, true},
		{"claude", []string{"--resume", "sid"}, true},
		{"claude", []string{"修一下 mcp 的报错"}, true},
		{"claude", []string{"-p", "hi"}, false},
		{"claude", []string{"mcp", "list"}, false},
		{"claude", []string{"--version"}, false},
		{"codex", []string{"resume", "sid"}, true},
		{"codex", []string{"exec", "do it"}, false},
		{"codex", []string{"login"}, false},
		{"cursor-agent", nil, true},
		{"cursor-agent", []string{"--print", "x"}, false},
		{"cursor-agent", []string{"status"}, false},
	} {
		got, err := l.ManualArgs(c.cmd, c.args)
		if err != nil {
			t.Fatal(err)
		}
		hooked := len(got) > len(c.args)
		if hooked != c.hooked || !slices.Equal(got[len(got)-len(c.args):], c.args) {
			t.Errorf("%s %q -> %q", c.cmd, c.args, got)
		}
	}
	got, _ := l.ManualArgs("claude", []string{"-c"})
	if !slices.Equal(got, []string{"--settings", settings, "--plugin-dir", plugin, "-c"}) {
		t.Errorf("claude -c -> %q", got)
	}
	if _, err := os.Stat(settings); err != nil {
		t.Error("claude settings not written")
	}
	got, _ = l.ManualArgs("codex", nil)
	if len(got) < 2 || got[0] != "-c" || !strings.HasPrefix(got[1], "hooks.") {
		t.Errorf("codex -> %q", got)
	}
}

func TestShellFiles(t *testing.T) {
	home := t.TempDir()
	if err := WriteShellFiles(home, "/Applications/Hive.app/Contents/MacOS/Hive"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(ShimDir(home), "claude"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "exec '/Applications/Hive.app/Contents/MacOS/Hive' agent exec claude -- \"$@\""; !strings.Contains(string(b), want) {
		t.Errorf("shim %q", b)
	}
	if _, err := os.Stat(filepath.Join(ShimDir(home), "grok")); !os.IsNotExist(err) {
		t.Error("grok has no hooks to add, should not be shimmed")
	}
	if _, err := os.Stat(filepath.Join(ZshDir(home), ".zshenv")); err != nil {
		t.Error(".zshenv missing")
	}
	if env := ShellEnv(home, "/bin/bash", ""); env != nil {
		t.Errorf("bash env %q", env)
	}
	if env := ShellEnv(home, "/bin/zsh", "/u/z"); !slices.Contains(env, "ZDOTDIR="+ZshDir(home)) || !slices.Contains(env, "HIVE_USER_ZDOTDIR=/u/z") {
		t.Errorf("zsh env %q", env)
	}
}

func TestLookPathAfter(t *testing.T) {
	shims, real := t.TempDir(), t.TempDir()
	for _, d := range []string{shims, real} {
		os.WriteFile(filepath.Join(d, "claude"), []byte("#!/bin/sh\n"), 0o755)
	}
	path := shims + string(filepath.ListSeparator) + real
	if got := LookPathAfter("claude", path, shims); got != filepath.Join(real, "claude") {
		t.Errorf("found %q", got)
	}
	if got := LookPathAfter("nope", path, shims); got != "" {
		t.Errorf("found %q", got)
	}
}
