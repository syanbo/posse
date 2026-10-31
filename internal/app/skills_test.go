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

// 新建项目技能：正本在 .agents/skills，Claude 的 .claude/skills 里是相对链接，扫描出来四家都读得到；重名不覆盖。
func TestCreateProjectSkill(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	file, err := createProjectSkill(root, "release-check")
	if err != nil {
		t.Fatal(err)
	}
	if file != filepath.Join(root, ".agents", "skills", "release-check", "SKILL.md") {
		t.Errorf("SKILL.md 在 %s", file)
	}
	if target, _ := os.Readlink(filepath.Join(root, ".claude", "skills", "release-check")); target != "../../.agents/skills/release-check" {
		t.Errorf("链接指向 %q", target)
	}
	cat := skills.Scan(skills.Options{Home: t.TempDir(), Dir: root})
	if len(cat.Skills) != 1 {
		t.Fatalf("%+v", cat.Skills)
	}
	for _, ag := range skills.Agents {
		if st := cat.Skills[0].Agents[ag].State; st != skills.On {
			t.Errorf("%s：%v", ag, st)
		}
	}
	if _, err := createProjectSkill(root, "release-check"); err == nil {
		t.Error("重名应拒绝")
	}
	if !skillNameRe.MatchString("a1-b") || skillNameRe.MatchString("A") || skillNameRe.MatchString("-a") || skillNameRe.MatchString("a b") {
		t.Error("技能名规则不对")
	}
}

// 第一次统计用量还没算完时重扫（确认开关、刷新）：用量照样算完显示出来，不停在「正在统计」。
func TestSkillStatsSurviveRescan(t *testing.T) {
	home := t.TempDir()
	old := homeDir
	homeDir = home
	t.Cleanup(func() { homeDir = old })
	findCodex = func(string) string { return "" }
	t.Cleanup(func() { findCodex = skills.FindCodex })
	a := previewApp(t)
	a.skills.visible = true
	a.scanSkills()
	a.scanSkills() // 前一次的结果还排在 pending 里
	for range 200 {
		a.runPending()
		if a.skills.stats != nil && !a.skills.statsLoading {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("statsLoading %v stats %v", a.skills.statsLoading, a.skills.stats)
}
