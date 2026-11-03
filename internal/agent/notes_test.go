package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 技能装在用户目录里，启动 agent 时只注入 hooks 和 Claude 的文件去处指路
func TestLaunchInjectsNoSkills(t *testing.T) {
	l := Launcher{Exe: "/bin/posse", Home: t.TempDir()}
	SetPermissionMode(l.Home, PermissionAsk)
	for _, name := range Known {
		r, err := l.Launch(name, Model{}, "do it")
		if err != nil {
			t.Fatal(err)
		}
		cmd := r.Argv[len(r.Argv)-1]
		for _, bad := range []string{"claude-plugin", "developer_instructions", "--rules", "posse task guide"} {
			if strings.Contains(cmd, bad) {
				t.Errorf("%s launch has %q: %q", name, bad, cmd)
			}
		}
		if name == "claude" && !strings.Contains(cmd, "--append-system-prompt "+Quote(notesHint)) {
			t.Errorf("claude lacks the notes hint: %q", cmd)
		}
	}
	if _, err := os.Stat(filepath.Join(l.Home, "claude-plugin")); err == nil {
		t.Error("claude plugin written")
	}
}

func TestRemoveOldPlugins(t *testing.T) {
	home := t.TempDir()
	for _, p := range []string{"claude-plugin/skills/posse-tasks/SKILL.md", "cursor-plugin/skills/posse-tasks/SKILL.md", "cursor-plugin/hooks/hooks.json"} {
		os.MkdirAll(filepath.Join(home, filepath.Dir(p)), 0o755)
		os.WriteFile(filepath.Join(home, p), []byte("x"), 0o644)
	}
	if err := RemoveOldPlugins(home); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{"claude-plugin": false, "cursor-plugin/skills": false, "cursor-plugin/hooks/hooks.json": true} {
		if _, err := os.Stat(filepath.Join(home, p)); (err == nil) != want {
			t.Errorf("%s exists=%v", p, err == nil)
		}
	}
}
