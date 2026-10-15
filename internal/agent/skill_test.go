package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudePlugin(t *testing.T) {
	l := Launcher{Exe: "/bin/hive", Home: t.TempDir()}
	r, err := l.Launch("claude", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(l.Home, "claude-plugin")
	if cmd := r.Argv[len(r.Argv)-1]; !strings.Contains(cmd, "--plugin-dir '"+dir+"'") {
		t.Fatalf("plugin dir not passed: %q", cmd)
	}
	var manifest map[string]string
	b, err := os.ReadFile(filepath.Join(dir, ".claude-plugin", "plugin.json"))
	if err != nil || json.Unmarshal(b, &manifest) != nil || manifest["name"] != "hive" {
		t.Fatalf("manifest %s %v", b, err)
	}
	skill := filepath.Join(dir, "skills", SkillName, "SKILL.md")
	b, err = os.ReadFile(skill)
	if err != nil || !strings.HasPrefix(string(b), "---\nname: hive-tasks\ndescription: ") || !strings.Contains(string(b), "`hive task guide`") {
		t.Fatalf("skill %q %v", b, err)
	}
	// 内容没变时不重写，已经打开的 Claude 读到的文件不会被换掉
	old := time.Now().Add(-time.Hour)
	os.Chtimes(skill, old, old)
	if _, err := l.Launch("claude", ""); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(skill); !fi.ModTime().Equal(old) {
		t.Fatal("unchanged skill rewritten")
	}
}

func TestCodexGuideArgs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	args := codexGuideArgs()
	if len(args) != 2 || args[0] != "-c" || !strings.HasPrefix(args[1], "developer_instructions=") {
		t.Fatalf("args %q", args)
	}
	var v string
	if err := json.Unmarshal([]byte(strings.TrimPrefix(args[1], "developer_instructions=")), &v); err != nil || v != guideHint {
		t.Fatalf("value %q %v", v, err)
	}
	// 用户自己设了 developer_instructions：-c 会整个替换它，所以不注入
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"x\"\n  developer_instructions = \"mine\"\n"), 0o644)
	if args := codexGuideArgs(); args != nil {
		t.Fatalf("overrode the user's developer_instructions: %q", args)
	}
	l := Launcher{Exe: "/bin/hive", Home: t.TempDir()}
	os.Remove(filepath.Join(home, "config.toml"))
	r, _ := l.Launch("codex", "")
	if cmd := r.Argv[len(r.Argv)-1]; !strings.Contains(cmd, "developer_instructions=") {
		t.Fatalf("codex launch lacks the guide hint: %q", cmd)
	}
}

func TestGrokRules(t *testing.T) {
	l := Launcher{Exe: "/bin/hive", Home: t.TempDir()}
	r, err := l.Launch("grok", "do it")
	if err != nil {
		t.Fatal(err)
	}
	cmd := r.Argv[len(r.Argv)-1]
	if !strings.Contains(cmd, "--rules "+Quote(guideHint)) || !strings.Contains(cmd, "--verbatim 'do it'") {
		t.Fatalf("grok: %q", cmd)
	}
}
