package app

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestApplyKeepsSelection(t *testing.T) {
	ps := []project{{ID: "p1", Name: "Apps"}}
	ws := []workspace{{ID: "w_1", ProjectID: "p1", Project: "Apps", Kind: "main"}}
	for sel, want := range map[string]string{
		taskBoardID:    taskBoardID,
		historyBoardID: historyBoardID,
		"w_1":          "w_1",
		"w_gone":       "w_1",
	} {
		a := &app{sectionsOpen: map[string]*bool{}, lastStatus: map[string]string{}, selected: sel}
		a.apply(ps, ws)
		if a.selected != want {
			t.Errorf("selected %q after refresh = %q, want %q", sel, a.selected, want)
		}
		if *a.sectionsOpen["p1"] {
			t.Errorf("new section starts open")
		}
	}
}

func TestRevealOpensSection(t *testing.T) {
	a := previewApp(t)
	// 刚启动时还没选中项，刷新后落到第一个工作区，分组都保持收起
	a.selected, a.sectionsOpen = "", map[string]*bool{}
	a.apply(a.projects, a.workspaces)
	ui.NewTester(a.build, 1200, 760).Frame()
	for id, open := range a.sectionsOpen {
		if *open {
			t.Fatalf("section %s open after launch, selected %s", id, a.selected)
		}
	}
	// w8 是 Works（p6）下面的子工作区，Skill（p5）不受影响
	a.selected, a.revealSel = "w8", true
	ui.NewTester(a.build, 1200, 760).Frame()
	if !*a.sectionsOpen["p6"] || *a.sectionsOpen["p5"] || a.revealSel {
		t.Fatalf("after reveal: p6=%v p5=%v revealSel=%v", *a.sectionsOpen["p6"], *a.sectionsOpen["p5"], a.revealSel)
	}
	// 定位项目行本身（主工作区）不用展开
	*a.sectionsOpen["p6"] = false
	a.selected, a.revealSel = "w7", true
	ui.NewTester(a.build, 1200, 760).Frame()
	if *a.sectionsOpen["p6"] {
		t.Fatal("revealing the project row opened it")
	}
	// 用户收起分组后，不定位就不再自动展开
	*a.sectionsOpen["p6"] = false
	ui.NewTester(a.build, 1200, 760).Frame()
	if *a.sectionsOpen["p6"] {
		t.Fatal("section reopened without reveal")
	}
}

func TestShortModel(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":            "Opus 5.5",
		"claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-3-5-sonnet-20241022": "Sonnet 3.5",
		"claude-fable-5-1":           "Fable 5.1",
		"gpt-6-astra":                "GPT-6 Astra",
		"gpt-5.1-codex":              "GPT-5.1 Codex",
		"o3":                         "o3",
		"grok-4.7-build":             "Grok 4.7",
		"grok-code-fast-1":           "Grok Code Fast 1",
	} {
		if got := shortModel(id); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", id, got, want)
		}
	}
}
