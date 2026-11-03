package app

import (
	"testing"

	"posse/internal/skills"
)

func TestPosseSkillsBar(t *testing.T) {
	a := previewApp(t)
	tt := newPreviewTester(a, false)
	// 没读过安装情况（测试不开窗口）时不出提示
	if tt.HasText("安装 Posse 技能") {
		t.Fatal("notice before reading the install state")
	}
	a.posseSkills = posseSkillsView{read: true, st: skills.PosseState{Missing: skills.PosseNames()}}
	tt.Frame()
	if !tt.HasText("安装 Posse 技能") || !tt.HasText("安装") {
		t.Fatal("no install notice for missing skills")
	}
	// 「查看」打开技能页
	if err := tt.Click("查看"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != skillsBoardID {
		t.Fatalf("selected %q", a.selected)
	}
	// 关掉后这一版不再提示
	if err := tt.Click("关闭提示"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.state.PosseSkillsPrompted != skills.PosseVersion() || tt.HasText("安装 Posse 技能") {
		t.Fatalf("dismissed: %q", a.state.PosseSkillsPrompted)
	}
	// 内容换了一版：装过的提示更新
	a.state.PosseSkillsPrompted = "older"
	a.posseSkills.st = skills.PosseState{Outdated: []string{skills.TasksSkill}, NPX: true, Claude: true}
	tt.Frame()
	if !tt.HasText("更新 Posse 技能") {
		t.Fatal("no update notice for outdated skills")
	}
	// 都是这一版、或同名的被用户占着：不提示
	a.posseSkills.st = skills.PosseState{Taken: skills.PosseNames()}
	tt.Frame()
	if tt.HasText("更新 Posse 技能") || tt.HasText("安装 Posse 技能") {
		t.Fatal("notice with nothing to install")
	}
}
