package app

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/history"
	"posse/locales"
)

// 点汇总里的标签和时间线里的名字都复制名字
func TestUsageCopyName(t *testing.T) {
	a := &app{}
	u := history.Usage{
		Skills: []history.UsageItem{{Name: "posse-tasks", Count: 2}},
		MCP:    []history.MCPUsage{{Server: "androidmirror", Count: 1}},
		Events: []history.UsageEvent{{Server: "androidmirror", Tool: "screen_observe", At: time.Now()}},
	}
	tt := newTester(func(c *ui.Context) {
		ui.Column(c).Children(func() {
			a.usageSummary(c, u, usageAll)
			a.usageTimeline(c, &terminalInfo{}, u.Events, usageAll)
		})
	}, 360, 400)
	tt.Frame()
	for _, name := range []string{"posse-tasks", "androidmirror", "screen_observe"} {
		if err := tt.Click("复制 " + name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
		if got := tt.Clipboard(); got != name {
			t.Fatalf("copied %q, want %q", got, name)
		}
	}
	if len(a.toasts) != 3 || a.toasts[2].Title != "已复制 screen_observe" {
		t.Fatalf("toasts %+v", a.toasts)
	}
}

// 时间线按筛选只列对应的类别；网址的名字是域名，点一下复制完整网址
func TestUsageWebTimeline(t *testing.T) {
	evs := []history.UsageEvent{
		{Skill: "posse-tasks"},
		{CLI: "git", Command: "git status"},
		{URL: "https://www.github.com/a/b?x=1", Via: history.ViaUser},
		{URL: "https://www.github.com/a/b?x=1", Via: history.ViaUser},
		{Query: "posse", Via: "WebSearch"},
	}
	count := func(m usageMode) int { return len(usageSteps(evs, m)) }
	if count(usageAll) != 4 || count(usageSkills) != 1 || count(usageWeb) != 2 {
		t.Fatalf("steps all %d skills %d web %d", count(usageAll), count(usageSkills), count(usageWeb))
	}
	if host, rest := webSite(evs[2].URL); host != "github.com" || rest != "/a/b?x=1" {
		t.Fatalf("site %q %q", host, rest)
	}
	a := &app{}
	tt := newTester(func(c *ui.Context) {
		a.usageTimeline(c, &terminalInfo{}, evs, usageWeb)
	}, 360, 400)
	tt.Frame()
	if err := tt.Click("复制 https://www.github.com/a/b?x=1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if got := tt.Clipboard(); got != evs[2].URL {
		t.Fatalf("copied %q", got)
	}
}

// 起过名的会话在标签和侧栏上写会话名，没起名时写终端标题
func TestTabLabelSessionTitle(t *testing.T) {
	term := terminalInfo{Agent: "claude", Title: "✳ 自动标题", Running: true, AgentState: &agentState{Status: "idle"}}
	if _, d := tabLabel(locales.In("zh-Hans"), term); d != "自动标题" {
		t.Fatalf("untitled %q", d)
	}
	term.AgentState.Title = "1020｜feat｜会话命名"
	if _, d := tabLabel(locales.In("zh-Hans"), term); d != "1020｜feat｜会话命名" {
		t.Fatalf("titled %q", d)
	}
}
