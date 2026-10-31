package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/skills"
)

// 技能页的开关：点 Claude 的开关先弹框给 diff，确认后才写 settings.json，写完重扫显示已关闭。
func TestSkillToggleConfirm(t *testing.T) {
	home := t.TempDir()
	old := homeDir
	homeDir = home
	t.Cleanup(func() { homeDir = old })
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	findCodex = func(string) string { return "" } // 不去问真的 codex
	t.Cleanup(func() { findCodex = skills.FindCodex })
	dir := filepath.Join(home, ".claude", "skills", "demo")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: demo\ndescription: 测试用\n---\n"), 0o644)
	settings := filepath.Join(home, ".claude", "settings.json")
	os.WriteFile(settings, []byte("{\n  \"model\": \"opus\"\n}\n"), 0o644)

	a := previewApp(t)
	offlineClient(t, a)
	a.selected = skillsBoardID
	tt := newPreviewTester(a, true)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for range 200 {
			tt.Frame()
			if ok() {
				return
			}
			pass(tt, 20*time.Millisecond)
		}
		t.Fatal("等不到 " + what)
	}
	waitFor("扫描结果", func() bool { return a.skills.loaded && tt.HasText("demo") })
	if err := tt.Click("Claude里启用"); err != nil {
		t.Fatal(err)
	}
	waitFor("确认框", func() bool { return tt.HasText("确认修改") })
	if ch := a.skillChange.ch; ch.File != settings || ch.Enable || !strings.Contains(ch.After, `"demo": "off"`) {
		t.Fatalf("改动 %+v", ch)
	}
	if b, _ := os.ReadFile(settings); strings.Contains(string(b), "skillOverrides") {
		t.Fatal("确认之前不该写")
	}
	if err := tt.Click("确认修改"); err != nil {
		t.Fatal(err)
	}
	waitFor("重扫后已关闭", func() bool {
		for _, s := range a.skills.cat.Skills {
			if s.Name == "demo" {
				return s.Agents[skills.Claude].State == skills.Off
			}
		}
		return false
	})
	b, _ := os.ReadFile(settings)
	if want := "{\n  \"model\": \"opus\",\n  \"skillOverrides\": {\n    \"demo\": \"off\"\n  }\n}\n"; string(b) != want {
		t.Errorf("settings.json：%q", b)
	}
}
