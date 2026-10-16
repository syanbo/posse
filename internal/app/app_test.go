package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/client"
	"hive/internal/rpc"
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
		if !*a.sectionsOpen["p1"] {
			t.Errorf("new section starts collapsed")
		}
	}
}

func TestRevealOpensSection(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	// 刚启动时分组都展开，会话都看得见
	a.selected, a.sectionsOpen = "", map[string]*bool{}
	a.apply(a.projects, a.workspaces)
	ui.NewTester(a.build, 1200, 760).Frame()
	for id, open := range a.sectionsOpen {
		if !*open {
			t.Fatalf("section %s collapsed after launch", id)
		}
	}
	// 只有主工作区的项目：定位到 shell 时项目行就看得见，不用展开；定位到会话才展开
	*a.sectionsOpen["p1"] = false
	a.selected, a.revealSel = "w1", true
	a.activeTerm["w1"] = "t3"
	ui.NewTester(a.build, 1200, 760).Frame()
	if *a.sectionsOpen["p1"] {
		t.Fatal("revealing the project row opened it")
	}
	a.activeTerm["w1"], a.revealSel = "t1", true
	ui.NewTester(a.build, 1200, 760).Frame()
	if !*a.sectionsOpen["p1"] {
		t.Fatal("revealing a session did not open its project")
	}
	// 最后再切到没有终端的 w8（会断开接入的终端）。w8 是 Works（p6）下面的工作区，收起时定位要展开，Skill（p5）不受影响
	*a.sectionsOpen["p6"], *a.sectionsOpen["p5"] = false, false
	a.selected, a.revealSel = "w8", true
	ui.NewTester(a.build, 1200, 760).Frame()
	if !*a.sectionsOpen["p6"] || *a.sectionsOpen["p5"] || a.revealSel {
		t.Fatalf("after reveal: p6=%v p5=%v revealSel=%v", *a.sectionsOpen["p6"], *a.sectionsOpen["p5"], a.revealSel)
	}
	// 用户收起分组后，不定位就不再自动展开
	*a.sectionsOpen["p6"] = false
	ui.NewTester(a.build, 1200, 760).Frame()
	if *a.sectionsOpen["p6"] {
		t.Fatal("section reopened without reveal")
	}
}

// offlineClient 让界面换终端时去接入一个不存在的常驻进程：接入失败，但不会因为没有客户端崩溃。
func offlineClient(t *testing.T, a *app) {
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "none.sock")}
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

// setHome 在测试期间换掉缓存的主目录。
func setHome(t *testing.T, home string) {
	old := homeDir
	homeDir = home
	t.Cleanup(func() { homeDir = old })
}

// 常驻进程不在时 socket 连不上，轮询据此才去拉起它。
func TestIsDialError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := rpc.Call(ctx, filepath.Join(t.TempDir(), "hived.sock"), "project.list", nil, nil)
	if !isDialError(err) {
		t.Fatalf("missing socket: %v is not a dial error", err)
	}
	if isDialError(context.DeadlineExceeded) {
		t.Fatal("timeout counted as dial error")
	}
}

func TestAgentChoicesCount(t *testing.T) {
	if len(agentChoices) != nAgents {
		t.Fatalf("nAgents = %d, agentChoices has %d", nAgents, len(agentChoices))
	}
}
