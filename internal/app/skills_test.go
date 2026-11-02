package app

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/skills"
	"posse/locales"
)

// 技能页的开关：点 Claude 的开关先弹框给 diff，确认后才写 settings.json，写完重扫显示已关闭。
func TestSkillToggleConfirm(t *testing.T) {
	home := t.TempDir()
	old := homeDir
	homeDir = home
	t.Cleanup(func() { homeDir = old })
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	findCodex = func(string) string { return "" } // 不去问真的 codex、grok
	findGrok = func(string) string { return "" }
	t.Cleanup(func() { findCodex, findGrok = skills.FindCodex, skills.FindGrok })
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
	file, err := createProjectSkill(locales.In("zh-Hans"), root, "release-check")
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
	if body, _ := os.ReadFile(file); !strings.Contains(string(body), "\n步骤：\n") {
		t.Errorf("中文模板：%s", body)
	}
	if _, err := createProjectSkill(locales.In("en"), root, "release-check"); err == nil {
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
	findGrok = func(string) string { return "" }
	t.Cleanup(func() { findCodex, findGrok = skills.FindCodex, skills.FindGrok })
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

func TestSkillStep(t *testing.T) {
	keys := []string{"a", "b", "c"}
	s := &skillsView{sel: "b"}
	s.step(keys, &s.sel, 1)
	if s.sel != "c" || !s.reveal {
		t.Fatalf("下键：%q %v", s.sel, s.reveal)
	}
	s.step(keys, &s.sel, 1)
	if s.sel != "c" {
		t.Errorf("到底了不动：%q", s.sel)
	}
	s.sel = skillIssuesKey
	s.step(keys, &s.sel, -1)
	if s.sel != skillIssuesKey {
		t.Errorf("看体检时上键不动：%q", s.sel)
	}
	s.step(keys, &s.sel, 1)
	if s.sel != "a" {
		t.Errorf("看体检时下键选第一个：%q", s.sel)
	}
}

// 技能命令对话框里的终端：不进工作区的终端列表，跑完后对话框拿到退出码。
func TestSkillRunHidden(t *testing.T) {
	a := previewApp(t)
	a.skillRun = skillRunView{open: true, title: "更新 x？", line: "true", ws: "w1", id: "t_run"}
	a.hiddenTerms = map[string]bool{"t_run": true}
	code := 3
	ws := slices.Clone(a.workspaces)
	for i := range ws {
		if ws[i].ID == "w1" {
			ws[i].Terminals = append(slices.Clone(ws[i].Terminals), terminalInfo{ID: "t_run", WorkspaceID: "w1", Running: true})
		}
	}
	a.apply(slices.Clone(a.projects), slices.Clone(ws))
	if _, tm := a.findTerminal("t_run"); tm != nil {
		t.Fatal("藏起来的终端进了列表")
	}
	if a.skillRun.exit != nil {
		t.Fatal("还在跑时不该有退出码")
	}
	for i := range ws {
		for j := range ws[i].Terminals {
			if ws[i].Terminals[j].ID == "t_run" {
				ws[i].Terminals[j].Running, ws[i].Terminals[j].ExitCode = false, &code
			}
		}
	}
	a.apply(slices.Clone(a.projects), slices.Clone(ws))
	if a.skillRun.exit == nil || *a.skillRun.exit != 3 {
		t.Fatalf("退出码 %v", a.skillRun.exit)
	}
}
