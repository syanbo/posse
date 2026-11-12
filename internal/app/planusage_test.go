package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"posse/internal/usage"
	"posse/locales"
)

func TestCursorPools(t *testing.T) {
	u := usage.Usage{Agent: "cursor", Windows: []usage.Window{
		{Percent: 2.7, Minutes: 44640},
		{Percent: 0.2, Minutes: 44640, Name: "Auto"},
		{Percent: 77, Minutes: 44640, Name: "API"},
	}}
	ws := planWindows(u)
	if len(ws) != 2 || windowName(locales.In("zh-Hans"), ws[0]) != "Cursor 模型" || windowName(locales.In("zh-Hans"), ws[1]) != "其他模型" {
		t.Fatalf("windows = %+v", ws)
	}
	if h := headWindow(u); h.Name != "API" {
		t.Fatalf("head = %+v", h)
	}
	// 只有总额度的旧读数照常显示
	u.Windows = u.Windows[:1]
	if ws := planWindows(u); len(ws) != 1 || headWindow(u).Percent != 2.7 {
		t.Fatalf("windows = %+v", ws)
	}
	// Claude 的分池不影响状态栏取 5 小时
	c := usage.Usage{Agent: "claude", Windows: []usage.Window{
		{Percent: 40, Minutes: 300}, {Percent: 10, Minutes: 10080}, {Percent: 90, Minutes: 10080, Name: "Fable"},
	}}
	if len(planWindows(c)) != 3 || headWindow(c).Minutes != 300 {
		t.Fatalf("claude head = %+v", headWindow(c))
	}
}

func TestCodexExpiredWindow(t *testing.T) {
	now := time.Now()
	u := usage.Usage{Agent: "codex", UpdatedAt: now.Add(-48 * time.Hour).UnixMilli(), Windows: []usage.Window{
		{Minutes: 300, Percent: 65, ResetsAt: now.Add(-time.Hour).UnixMilli()},
		{Minutes: 10080, Percent: 85, ResetsAt: now.Add(42 * time.Minute).UnixMilli()},
	}}
	ws := planWindows(u)
	if !ws[0].Unknown || ws[1].Unknown || u.Windows[0].Unknown {
		t.Fatalf("window expiration or original mutated: %+v", ws)
	}
	a := &app{}
	a.state.UsageRemaining = true
	m := locales.In("en")
	if got := planWindowPercent(m, a, ws[0]); got != "Awaiting update" {
		t.Fatalf("expired window: %q", got)
	}
	if got := planWindowPercent(m, a, ws[1]); got != "Remaining 15%" {
		t.Fatalf("live window: %q", got)
	}
	if planResetText(m, ws[0], now) != "" || !strings.Contains(planReadingAge(m, u, now), "Outdated") {
		t.Fatal("expired reset or stale reading is misleading")
	}
	if !headWindow(usage.Usage{Agent: "codex"}).Unknown {
		t.Fatal("missing reading presented as zero")
	}
}

func TestPreviewPlanFreshness(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		suffix := "light"
		if dark {
			suffix = "dark"
		}
		a := previewApp(t)
		now := time.Now()
		a.daemon.Usage = &planStatus{CodexError: "codex: usage query timed out", Usages: []usage.Usage{
			{Agent: "codex", Plan: "Plus", UpdatedAt: now.Add(-48 * time.Hour).UnixMilli(), Windows: []usage.Window{
				{Minutes: 300, Percent: 65, ResetsAt: now.Add(-time.Hour).UnixMilli()},
				{Minutes: 10080, Percent: 85, ResetsAt: now.Add(42 * time.Minute).UnixMilli()},
			}},
		}}
		a.state.UsageRemaining = true
		a.planOpen = true
		savePNG(t, newPreviewTester(a, dark), "plan-stale-"+suffix+".png")
		a.daemon.Usage = &planStatus{CodexError: "codex: usage query timed out"}
		savePNG(t, newPreviewTester(a, dark), "plan-unavailable-"+suffix+".png")
	}
}
