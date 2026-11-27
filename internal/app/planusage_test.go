package app

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
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

func TestGroupAntigravityWindows(t *testing.T) {
	ws := []usage.Window{
		{Name: "Gemini Models", Minutes: 10080, Percent: 26},
		{Name: "Claude and GPT models", Minutes: 10080, Percent: 0},
		{Name: "Gemini Models", Minutes: 300, Percent: 6},
		{Name: "Claude and GPT models", Minutes: 300, Percent: 0},
	}
	gs := groupAntigravityWindows(ws)
	if len(gs) != 2 || gs[0].name != "Gemini Models" || len(gs[0].windows) != 2 || gs[0].windows[1].Minutes != 300 ||
		gs[1].name != "Claude and GPT models" || len(gs[1].windows) != 2 || gs[1].windows[1].Minutes != 300 {
		t.Fatalf("groups = %+v", gs)
	}
}

func TestAntigravityHeadWindows(t *testing.T) {
	now := time.Now()
	future := now.Add(2 * time.Hour).UnixMilli()
	// 5h + 周：只取主力 Gemini 池，排除三方 3p 池，且短期（5h）在前、长期（周）在后。
	u := usage.Usage{Agent: "antigravity", Windows: []usage.Window{
		{QuotaID: "gemini-weekly", Name: "Gemini Models", Minutes: 10080, Percent: 18, ResetsAt: future},
		{QuotaID: "gemini-5h", Name: "Gemini Models", Minutes: 300, Percent: 17, ResetsAt: future},
		{QuotaID: "3p-weekly", Name: "Claude and GPT models", Minutes: 10080, Percent: 56, ResetsAt: future},
		{QuotaID: "3p-5h", Name: "Claude and GPT models", Minutes: 300, Percent: 41, ResetsAt: future},
	}}
	hs := headWindows(u)
	if len(hs) != 2 || hs[0].QuotaID != "gemini-5h" || hs[1].QuotaID != "gemini-weekly" {
		t.Fatalf("5h+week heads = %+v", hs)
	}
	// 天 + 周：天在前、周在后，中文简称分别为「天」「周」，英文为「1d」「wk」。
	daily := usage.Usage{Agent: "antigravity", Windows: []usage.Window{
		{QuotaID: "gemini-weekly", Name: "Gemini Models", Minutes: 10080, Percent: 22, ResetsAt: future},
		{QuotaID: "gemini-daily", Name: "Gemini Models", Minutes: 1440, Percent: 35, ResetsAt: future},
		{QuotaID: "3p-weekly", Name: "Claude and GPT models", Minutes: 10080, Percent: 95, ResetsAt: future},
	}}
	hs = headWindows(daily)
	if len(hs) != 2 || hs[0].QuotaID != "gemini-daily" || hs[1].QuotaID != "gemini-weekly" {
		t.Fatalf("daily+week heads = %+v", hs)
	}
	if got := windowShort(locales.In("zh-Hans"), 1440); got != "天" {
		t.Fatalf("zh day short = %q", got)
	}
	if got := windowShort(locales.In("en"), 1440); got != "1d" {
		t.Fatalf("en day short = %q", got)
	}
	// 无 quotaId 的旧读数按组名识别主力 Gemini 池，并兼容月周期。
	legacy := usage.Usage{Agent: "antigravity", Windows: []usage.Window{
		{Name: "Claude and GPT models", Minutes: 10080, Percent: 99, ResetsAt: future},
		{Name: "Gemini Models", Minutes: 43200, Percent: 12, ResetsAt: future},
		{Name: "Gemini Models", Minutes: 1440, Percent: 8, ResetsAt: future},
	}}
	hs = headWindows(legacy)
	if len(hs) != 2 || hs[0].Minutes != 1440 || hs[1].Minutes != 43200 {
		t.Fatalf("legacy daily+month heads = %+v", hs)
	}
	// 仅有三方额外池时不将其展示在底部摘要里。
	extraOnly := usage.Usage{Agent: "antigravity", Windows: []usage.Window{
		{QuotaID: "3p-weekly", Name: "Claude and GPT models", Minutes: 10080, Percent: 88, ResetsAt: future},
	}}
	if hs := headWindows(extraOnly); len(hs) != 0 {
		t.Fatalf("extra-only should not appear in status bar: %+v", hs)
	}
	// 状态栏渲染核对：展示主力天与周，不展示三方 56%；Cursor 的月仍正常显示。
	a := previewApp(t)
	a.daemon.Usage = &planStatus{Usages: []usage.Usage{
		{Agent: "cursor", UpdatedAt: now.UnixMilli(), Windows: []usage.Window{
			{Name: "Auto", Minutes: 43200, Percent: 35},
			{Name: "API", Minutes: 43200, Percent: 100},
		}},
		daily,
	}}
	tt := newTester(func(c *ui.Context) { a.planButton(c, a.daemon.Usage) }, 600, 80)
	tt.Frame()
	for _, want := range []string{"月", "100%", "天", "35%", "周", "22%"} {
		if _, ok := tt.Find(want); !ok {
			t.Fatalf("status bar missing %q", want)
		}
	}
	if _, ok := tt.Find("95%"); ok {
		t.Fatal("status bar leaked extra 3p pool percentage")
	}
}

func TestPlanCreditActivityLinkKeyboardAndOverflow(t *testing.T) {
	a := previewApp(t)
	now := time.Now()
	a.daemon.Usage = &planStatus{Usages: []usage.Usage{
		{Agent: "claude", Plan: "Max 5x", UpdatedAt: now.UnixMilli(), Windows: []usage.Window{{Minutes: 300, Percent: 20}, {Minutes: 10080, Percent: 40}}},
		{Agent: "codex", Plan: "Prolite", UpdatedAt: now.UnixMilli(), Windows: []usage.Window{{Minutes: 10080, Percent: 2}}},
		{Agent: "grok", UpdatedAt: now.UnixMilli(), Windows: []usage.Window{{Minutes: 10080, Percent: 15}}},
		{Agent: "cursor", Plan: "Ultra $200/mo", UpdatedAt: now.UnixMilli(), Windows: []usage.Window{{Name: "Auto", Minutes: 44640, Percent: 32}, {Name: "API", Minutes: 44640, Percent: 100}}},
		{Agent: "antigravity", UpdatedAt: now.UnixMilli(), AICredits: &usage.AICredits{Remaining: 0, At: now.UnixMilli()}, Windows: []usage.Window{
			{Name: "Gemini Models", Minutes: 10080, Percent: 26, ResetsAt: now.Add(6 * 24 * time.Hour).UnixMilli()},
			{Name: "Gemini Models", Minutes: 300, Percent: 6, ResetsAt: now.Add(4 * time.Hour).UnixMilli()},
			{Name: "Claude and GPT models", Minutes: 10080, Percent: 0, ResetsAt: now.Add(7 * 24 * time.Hour).UnixMilli()},
			{Name: "Claude and GPT models", Minutes: 300, Percent: 0, ResetsAt: now.Add(5 * time.Hour).UnixMilli()},
		}},
	}}
	// 最小窗口 900x560 + 最大字号下，五家读数滚动且顶栏不被裁出视口。
	defer setUIType(0, 0, 0)
	setUIType(maxUIFontSize, 0, 0)
	a.planOpen = true
	tt := newTester(a.build, 900, 560)
	tt.Frame()
	if r, ok := tt.Find("套餐用量"); !ok || r.Y < 0 || r.W <= 0 || r.H <= 0 {
		t.Fatalf("usage header clipped in 900x560 max font: ok=%v rect=%+v", ok, r)
	}
	if r, ok := tt.Find("剩余"); !ok || r.Y < 0 || r.W <= 0 || r.H <= 0 {
		t.Fatalf("usage toggle clipped in 900x560 max font: ok=%v rect=%+v", ok, r)
	}
	// 单独测试积分活动链接的 Tab 聚焦与回车触发。
	setUIType(0, 0, 0)
	a2 := previewApp(t)
	a2.daemon.Usage = &planStatus{Usages: []usage.Usage{
		{Agent: "antigravity", UpdatedAt: now.UnixMilli(), AICredits: &usage.AICredits{Remaining: 0, At: now.UnixMilli()}, Windows: []usage.Window{
			{Name: "Gemini Models", Minutes: 10080, Percent: 26},
		}},
	}}
	a2.planOpen = true
	kt := newTester(func(c *ui.Context) { a2.planPanel(c, a2.daemon.Usage.Usages) }, 900, 560)
	kt.Frame()
	opened := false
	for range 20 {
		kt.Key(0, ui.KeyTab)
		kt.Key(0, ui.KeyEnter)
		if slices.Contains(kt.OpenedURLs(), "https://one.google.com/ai/activity") {
			opened = true
			break
		}
	}
	if !opened {
		t.Fatalf("Tab+Enter did not open credit activity URL; opened=%v", kt.OpenedURLs())
	}
}

func TestPlanRefreshKeepsLayout(t *testing.T) {
	a := previewApp(t)
	a.daemon.Usage = &planStatus{Usages: []usage.Usage{{Agent: "codex", Plan: "Plus", UpdatedAt: time.Now().UnixMilli(),
		Windows: []usage.Window{{Minutes: 300, Percent: 10}}}}}
	a.planOpen = true
	tt := newPreviewTester(a, false)
	before, ok := tt.Find("剩余")
	if !ok {
		t.Fatal("usage panel not visible")
	}
	a.planBusy = true
	a.daemon.Usage.CodexReading = true
	tt.Frame()
	during, _ := tt.Find("剩余")
	a.planBusy = false
	a.daemon.Usage.CodexReading = false
	tt.Frame()
	after, _ := tt.Find("剩余")
	if during != before || after != before {
		t.Fatalf("refresh moved the panel header: before=%+v during=%+v after=%+v", before, during, after)
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
			{Agent: "antigravity", UpdatedAt: now.UnixMilli(), AICredits: &usage.AICredits{Remaining: 2500, At: now.UnixMilli()}, Windows: []usage.Window{
				{Name: "Gemini Models", Minutes: 10080, Percent: 26, ResetsAt: now.Add(6 * 24 * time.Hour).UnixMilli()},
				{Name: "Gemini Models", Minutes: 300, Percent: 6, ResetsAt: now.Add(4*time.Hour + 9*time.Minute).UnixMilli()},
				{Name: "Claude and GPT models", Minutes: 10080, Percent: 0, ResetsAt: now.Add(7 * 24 * time.Hour).UnixMilli()},
				{Name: "Claude and GPT models", Minutes: 300, Percent: 0, ResetsAt: now.Add(4*time.Hour + 59*time.Minute).UnixMilli()},
			}},
		}}
		a.state.UsageRemaining = true
		a.planOpen = true
		savePNG(t, newPreviewTester(a, dark), "plan-stale-"+suffix+".png")
		a.planBusy = true
		a.daemon.Usage.CodexReading = true
		savePNG(t, newPreviewTester(a, dark), "plan-refreshing-"+suffix+".png")
		a.planBusy = false
		a.daemon.Usage = &planStatus{CodexError: "codex: usage query timed out"}
		savePNG(t, newPreviewTester(a, dark), "plan-unavailable-"+suffix+".png")
	}
}

func TestPlanButtonHiddenWhenNoAgentsInstalled(t *testing.T) {
	a := previewApp(t)
	a.daemon.Agents = []string{}
	a.daemon.Usage = &planStatus{CodexError: "codex: not logged in; run codex login"}
	tt := newTester(func(c *ui.Context) { a.planButton(c, a.daemon.Usage) }, 600, 80)
	tt.Frame()
	if len(tt.Texts()) != 0 {
		t.Fatalf("planButton should hide when no agent CLIs are installed, got %v", tt.Texts())
	}
	// 安装了其他 agent 但未安装 codex 时，也不应合成 Codex 报错占位，正常回落到暂无用量
	a.daemon.Agents = []string{"claude"}
	tt.Frame()
	if tt.HasText("本期") {
		t.Fatalf("planButton synthesized codex chip when codex is not installed: %v", tt.Texts())
	}
}

func TestAwakeButtonHiddenOnLinuxRemote(t *testing.T) {
	a := previewApp(t)
	a.daemon.Awake = &awakeStatus{Mode: "auto", Active: false}
	a.c = &client.Client{Platform: "linux", Remote: true}
	tt := newTester(func(c *ui.Context) { a.awakeButton(c) }, 240, 60)
	tt.Frame()
	if len(tt.Texts()) != 0 {
		t.Fatalf("awakeButton should hide on linux remote, got %v", tt.Texts())
	}
	a.c.Platform = "darwin"
	tt.Frame()
	if len(tt.Texts()) == 0 {
		t.Fatal("awakeButton should render on darwin")
	}
}
