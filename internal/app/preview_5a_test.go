package app

import (
	"fmt"
	"os"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/skills"
	"posse/locales"
)

// TestPreview5A 把技能、MCP、技能与工具、内存、痕迹和侧栏「运行中」按中文、英文和伪语言（en-XA 把英文加长、加重音）
// 各出深浅一套，看英文有没有截断、重叠；设置 POSSE_PREVIEW_DIR 才运行。
func TestPreview5A(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	type scene struct {
		name  string
		setup func(a *app, m locales.Messages)
	}
	board := func(sel string) func(a *app, m locales.Messages) {
		return func(a *app, m locales.Messages) { previewSkillsBoard(a, sel) }
	}
	scenes := []scene{
		{name: "skills", setup: board("")},
		{name: "skills-off", setup: board(homeDir + "/.agents/skills/lark-calendar")},
		{name: "skills-unseen", setup: board(homeDir + "/.agents/skills/hypit")},
		{name: "skills-issues", setup: board(skillIssuesKey)},
		{name: "skills-confirm", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, homeDir+"/.agents/skills/lark-calendar")
			before := "{\n  \"skillOverrides\": {\n    \"lark-calendar\": \"off\"\n  },\n  \"theme\": \"dark\"\n}\n"
			after := "{\n  \"theme\": \"dark\"\n}\n"
			a.skillChange = skillChangeView{open: true, ch: skills.Change{Agent: skills.Claude, Skill: "lark-calendar", Enable: true, File: homeDir + "/.claude/settings.json", Before: before, After: after}}
		}},
		{name: "skills-trash-links", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, skillIssuesKey)
			var items []skills.Issue
			for _, is := range a.skills.cat.Issues {
				if is.Kind == skills.RedundantLink || is.Kind == skills.BrokenLink {
					items = append(items, is)
				}
			}
			a.askTrashLinks(m, items)
		}},
		{name: "skills-install", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skillInstall = skillInstallView{open: true, ws: "w1", global: true, claude: true, source: "anthropics/skills", listSrc: "anthropics/skills",
				listed: []skills.Listed{
					{Name: "docx", Description: "Create, read, edit, or manipulate Word documents.", Group: "Document Skills"},
					{Name: "pdf", Description: "Read, merge, split and fill PDF files.", Group: "Document Skills"},
					{Name: "frontend-design", Description: "Create distinctive, production-grade frontend interfaces with high design quality.", Group: "Design"},
				},
				picked: []bool{true, false, true}, installed: []bool{true, false, false}}
		}},
		{name: "skills-install-project", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skillInstall = skillInstallView{open: true, ws: "w1", claude: true}
		}},
		{name: "skills-install-failed", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skillInstall = skillInstallView{open: true, ws: "w1", global: true, claude: true, source: "nobody/nope", listSrc: "nobody/nope",
				listErr: "Failed to clone repository: Authentication failed for https://github.com/nobody/nope.git."}
		}},
		{name: "skills-install-nonpx", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skillInstall = skillInstallView{open: true, ws: "w1", global: true, claude: true, source: "anthropics/skills", listSrc: "anthropics/skills",
				listErr: skills.ErrNoNPX.Error(), noNPX: true}
		}},
		{name: "skills-run", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skillRun = skillRunView{open: true, title: m.SkillsUpdateConfirm("archify"), about: m.SkillsUpdateBody("tt-a1i/archify"),
				line: "DISABLE_TELEMETRY=1 npx -y skills@1.7.4 update archify -g -y", ws: "w1"}
		}},
		{name: "skills-run-failed", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			code := 1
			a.skillRun = skillRunView{open: true, title: m.SkillsInstallTitle(), line: "npx -y skills add nobody/nope -g -y", ws: "w1",
				term: previewShell(t), id: "run", exit: &code}
		}},
		{name: "skills-new", setup: func(a *app, m locales.Messages) {
			previewSkillsTab(a)
			a.skillNew = skillNewView{open: true, ws: a.workspaces[0].ID, name: "Release"}
		}},
		{name: "skills-mcp", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skills.page, a.skills.mcpSel = 1, "iphone-use"
		}},
		{name: "skills-mcp-issues", setup: func(a *app, m locales.Messages) {
			previewSkillsBoard(a, "")
			a.skills.page, a.skills.mcpSel = 1, skillIssuesKey
		}},
		{name: "skills-panel", setup: func(a *app, m locales.Messages) { previewSkillsTab(a) }},
		{name: "traces", setup: func(a *app, m locales.Messages) {
			at := time.Date(2026, 10, 30, 14, 20, 0, 0, time.Local)
			a.traces = tracesView{open: true, title: m.TracesRemoveTitle("posse/anim-image"), verb: m.CommonDelete(),
				body: m.TracesRemoveWorktreeBody() + "\n\n" + m.TracesLeftBehindRemove(),
				rows: []traceRow{
					{traceItem: traceItem{Path: homeDir + "/Downloads/ddt-ios-sim-test", Kind: "created", Agent: "claude", At: at, IsDir: true}, checked: true, size: 1700 << 20},
					{traceItem: traceItem{Path: homeDir + "/Desktop/Works/cl-snap-wt", Kind: "worktree", Agent: "codex", At: at.Add(-time.Hour), IsDir: true}, size: 1300 << 20},
					{traceItem: traceItem{Path: homeDir + "/Downloads/fs-2267.png", Kind: "created", Agent: "claude", At: at.Add(-2 * time.Hour), Running: true}, checked: true, size: 516 << 10},
					{traceItem: traceItem{Path: homeDir + "/.ssh/config", Kind: "config", At: at.Add(-3 * time.Hour)}, size: -1},
				}}
		}},
		{name: "usage", setup: func(a *app, m locales.Messages) {
			a.state.RightPanel = panelUsage
			a.usage = usageView{term: "t1", found: true, poll: panelPoll{at: time.Now()}, data: history.Usage{
				Skills: []history.UsageItem{{Name: "computer-use", Count: 3}, {Name: "chatgpt-imagegen", Count: 1}},
				MCP:    []history.MCPUsage{{Server: "codex-cu", Count: 55}, {Server: "androidmirror", Count: 4}},
				CLI: []history.CLIUsage{
					{Name: "go", Source: history.SourceHomebrew, Count: 12}, {Name: "git", Source: history.SourceSystem, Count: 9},
					{Name: "bsk", Count: 4}, {Name: "adb", Count: 2},
				},
				Web:      []history.WebUsage{{Host: "github.com", Count: 3}, {Host: "docs.rs", Count: 1}},
				Searches: 2,
				Events:   previewUsageEvents(),
			}}
		}},
		{name: "usage-empty", setup: func(a *app, m locales.Messages) {
			a.state.RightPanel = panelUsage
			a.usage = usageView{term: "t1", poll: panelPoll{at: time.Now()}}
		}},
		{name: "memory", setup: func(a *app, m locales.Messages) {
			w := a.workspaces[0]
			claude := []memProc{{"claude", 420 << 10, 1}, {"@sentry/mcp-server", 115 << 10, 1}, {"iphone-use-mcp", 7 << 10, 2}, {"zsh", 5 << 10, 1}}
			a.mem = memView{open: true, poll: panelPoll{at: time.Now().Add(time.Hour)}, terms: 1460 << 10, expanded: map[string]bool{"t1": true},
				rows: []memWorkspace{
					{id: w.ID, label: displayName(&w), kb: 1210 << 10, terms: []memTerminal{
						{w.Terminals[0], 820 << 10, claude}, {w.Terminals[1], 360 << 10, nil}}},
				}}
		}},
		{name: "running", setup: func(a *app, m locales.Messages) { a.state.SidebarGroup = sidebarByStatus }},
	}
	for _, dark := range []bool{true, false} {
		suffix := map[bool]string{true: "dark", false: "light"}[dark]
		for _, lang := range []string{"zh-Hans", "en", "en-XA"} {
			m := locales.In(lang)
			for _, sc := range scenes {
				a := previewApp(t)
				sc.setup(a, m)
				tt := newPreviewTester(a, dark)
				tt.SetLocale(lang)
				tt.Frame()
				pass(tt, 300*time.Millisecond)
				tt.Frame()
				savePNG(t, tt, fmt.Sprintf("5a-%s-%s-%s.png", sc.name, lang, suffix))
			}
		}
	}
}
