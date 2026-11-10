package app

import (
	"time"

	"posse/internal/history"
	"posse/internal/skills"
)

// previewSkills 是技能页和文件栏技能页签的样例：用户级（npx skills 装的、自己写的）、账号同步、插件、系统自带和项目级都有，
// 几种状态（启用、只能手动调用、关了、被同名盖住、读不到）和体检的四类问题都出现。
func previewSkills(home string) skills.Catalog {
	on := func(via string) skills.Status { return skills.Status{State: skills.On, Via: via} }
	cursor := func(st skills.Status) skills.Status { st.Inferred = true; return st }
	hub := home + "/.agents/skills/"
	cl := home + "/.claude/skills/"
	mk := func(name, desc string, scope skills.Scope, dir, source string, st [skills.NumAgents]skills.Status, paths ...string) skills.Skill {
		sk := skills.Skill{Name: name, Description: desc, Dir: dir, File: dir + "/SKILL.md", Scope: scope, Source: source, Agents: st, Paths: append([]string{dir}, paths...)}
		if source != "" {
			sk.SourceURL, sk.LockHash = "https://github.com/"+source, "v1"
		}
		return sk
	}
	modelOff := func(sk skills.Skill) skills.Skill { sk.Meta.ModelOff = true; return sk }
	return skills.Catalog{CodexLive: true, Skills: []skills.Skill{
		mk("archify", "Create validated architecture, workflow, sequence and data-flow diagrams as explorable standalone HTML with dark/light themes.", skills.ScopeUser, hub+"archify", "tt-a1i/archify",
			[skills.NumAgents]skills.Status{on(cl + "archify"), on(hub + "archify"), on(hub + "archify"), cursor(on(hub + "archify"))}, cl+"archify", home+"/.grok/skills/archify"),
		mk("browser-skill", "Automate the user's logged-in Chromium browser: read pages, fill forms, scrape data, operate tabs.", skills.ScopeUser, hub+"browser-skill", "",
			[skills.NumAgents]skills.Status{{}, on(hub + "browser-skill"), on(hub + "browser-skill"), cursor(on(hub + "browser-skill"))}),
		mk("browser-skill", "Automate the user's logged-in Chromium browser（Claude 目录里的旧副本）", skills.ScopeUser, cl+"browser-skill", "",
			[skills.NumAgents]skills.Status{on(cl + "browser-skill"), {}, {State: skills.Shadowed, Why: "被同名的 ~/.agents/skills/browser-skill 盖住"}, cursor(skills.Status{State: skills.Shadowed, Why: "被同名的 ~/.agents/skills/browser-skill 盖住"})}),
		mk("hypit", "Direct and produce videos with Hypit.", skills.ScopeUser, hub+"hypit", "hypit-ai/hypit",
			[skills.NumAgents]skills.Status{{Why: "不读 ~/.agents/skills，它读 ~/.claude/skills"}, on(hub + "hypit"), on(hub + "hypit"), cursor(on(hub + "hypit"))}),
		mk("lark-calendar", "飞书日历：管理日历日程和会议室。查看/搜索日程、创建/更新日程、管理参会人、查询忙闲和推荐时段。", skills.ScopeUser, hub+"lark-calendar", "open.feishu.cn",
			[skills.NumAgents]skills.Status{{State: skills.Off, Why: "settings.json 的 skillOverrides 设为 off"}, on(hub + "lark-calendar"), {State: skills.Off, Why: "config.toml 的 [skills] disabled 里有它"}, cursor(on(hub + "lark-calendar"))}, cl+"lark-calendar"),
		modelOff(mk("fiction-plot-structure", "长篇小说的情节结构：三幕、英雄之旅、救猫咪节拍表。", skills.ScopeUser, cl+"fiction-plot-structure", "",
			[skills.NumAgents]skills.Status{{State: skills.Manual, Why: "SKILL.md 设了 disable-model-invocation"}, {}, {State: skills.Manual, Why: "SKILL.md 设了 disable-model-invocation"}, cursor(skills.Status{State: skills.Manual, Why: "SKILL.md 设了 disable-model-invocation"})})),
		mk("docx", "Create, read, edit, or manipulate Word documents (.docx).", skills.ScopeSynced, cl+"synced/b1/docx", "",
			[skills.NumAgents]skills.Status{on(cl + "synced/b1/docx"), {}, {}, {}}),
		mk("spreadsheets:Spreadsheets", "Build, edit and analyze spreadsheets.", skills.ScopePlugin, home+"/.codex/plugins/cache/openai-primary-runtime/spreadsheets/26.1022/skills/spreadsheets", "",
			[skills.NumAgents]skills.Status{{}, on(""), {}, {}}),
		mk("skill-creator", "Guide for creating effective skills.", skills.ScopeSystem, home+"/.codex/skills/.system/skill-creator", "",
			[skills.NumAgents]skills.Status{{}, on(home + "/.codex/skills/.system/skill-creator"), {}, {}}),
		mk("release", "发布 Mac 版：写 CHANGELOG、跑 scripts/release.sh、核对上传。", skills.ScopeProject, "/Users/x/Desktop/AI/Apps/.claude/skills/release", "",
			[skills.NumAgents]skills.Status{on("/Users/x/Desktop/AI/Apps/.claude/skills/release"), {}, on("/Users/x/Desktop/AI/Apps/.claude/skills/release"), cursor(on(""))}),
		mk("ios-adhoc", "打 iOS 的 Ad Hoc 包并传到 R2。", skills.ScopeProject, "/Users/x/Desktop/AI/Apps/.agents/skills/ios-adhoc", "",
			[skills.NumAgents]skills.Status{{}, {State: skills.Off, Why: "config.toml 的 [[skills.config]] 设为 enabled = false"}, on(""), cursor(on(""))}),
	}, Issues: []skills.Issue{
		{Kind: skills.BrokenLink, Name: "unslop", Path: home + "/.grok/skills/unslop", Other: hub + "unslop"},
		{Kind: skills.Duplicate, Name: "browser-skill", Path: cl + "browser-skill", Other: hub + "browser-skill", Agents: []skills.Agent{skills.Grok, skills.Cursor}, Same: true},
		{Kind: skills.LockMissing, Name: "herdr", Path: home + "/.agents/.skill-lock.json", Other: hub + "herdr"},
		{Kind: skills.RedundantLink, Name: "archify", Path: home + "/.grok/skills/archify", Other: hub + "archify"},
		{Kind: skills.RedundantLink, Name: "eli5", Path: home + "/.grok/skills/eli5", Other: hub + "eli5"},
	}}
}

// previewSkillsBoard 打开技能页并填好样例，不去扫真的目录。
func previewSkillsBoard(a *app, sel string) {
	cat := previewSkills(homeDir)
	a.selected = skillsBoardID
	now := time.Now()
	// 查过更新：archify 有新版本，lark-calendar 在来源里找不到了，一个来源没查到
	updates := &skills.Updates{Latest: map[string]string{"archify": "v2"}, Gone: map[string]bool{"lark-calendar": true}, Failed: []skills.Failure{{Source: "me/gone", Err: "HTTP 404"}}, At: now}
	a.skills = skillsView{visible: true, loaded: true, cat: cat, sel: sel, updates: updates, stats: map[string]*history.SkillStat{
		"archify":       {Name: "archify", Count: 14, Sessions: 11, Last: now.Add(-3 * time.Hour), ByAgent: map[string]int{"claude": 9, "codex": 3, "grok": 2}},
		"browser-skill": {Name: "browser-skill", Count: 60, Sessions: 58, Last: now.Add(-26 * time.Hour), ByAgent: map[string]int{"claude": 53, "codex": 2, "cursor": 4, "grok": 1}},
		"lark-calendar": {Name: "lark-calendar", Count: 2, Sessions: 2, Last: now.Add(-40 * 24 * time.Hour), ByAgent: map[string]int{"claude": 2}},
		"docx":          {Name: "docx", Count: 5, Sessions: 4, Last: now.Add(-6 * 24 * time.Hour), ByAgent: map[string]int{"claude": 5}},
	}}
	if sel == "" {
		a.skills.sel = cat.Skills[0].Dir
	}
	a.skills.mcp = previewMCP(homeDir)
	a.skills.mcpStats = map[string]*history.SkillStat{
		"iphone-use": {Name: "iphone-use", Count: 128, Sessions: 21, Last: now.Add(-2 * time.Hour), ByAgent: map[string]int{"claude": 110, "codex": 18}},
		"codex-cu":   {Name: "codex-cu", Count: 34, Sessions: 9, Last: now.Add(-5 * time.Hour), ByAgent: map[string]int{"claude": 34}},
	}
}

// previewMCP 是 MCP 分页的样例：四家都配的、只有一家配的、插件带的、关着的，几份写法不一样的和启动命令不在的。
func previewMCP(home string) skills.MCPCatalog {
	claude, cursor := home+"/.claude.json", home+"/.cursor/mcp.json"
	codex, grok := home+"/.codex/config.toml", home+"/.grok/config.toml"
	app := "/Applications/iPhoneUse.app/Contents/MacOS/iphone-use-mcp"
	on := func(via string) skills.Status { return skills.Status{State: skills.On, Via: via} }
	return skills.MCPCatalog{Live: [skills.NumAgents]bool{false, true, true, false}, Servers: []skills.MCPServer{
		{Name: "codex-cu", Defs: []skills.MCPDef{{Agent: skills.Claude, File: claude, Transport: "stdio", Command: home + "/.local/bin/codex-cu-mcp", EnvKeys: []string{"CODEX_HOME"}}},
			Agents: [skills.NumAgents]skills.Status{on(claude), {}, on(claude), {}}},
		{Name: "computer-use", Defs: []skills.MCPDef{{Agent: skills.Codex, File: codex, Transport: "stdio", Command: "./Codex Computer Use.app/Contents/MacOS/SkyComputerUseClient", Args: []string{"mcp"}, Disabled: true}},
			Agents: [skills.NumAgents]skills.Status{{}, {State: skills.Off, Via: codex, Why: "config.toml 里 enabled = false"}, {}, {}}},
		{Name: "iphone-use", Defs: []skills.MCPDef{
			{Agent: skills.Claude, File: claude, Transport: "stdio", Command: app},
			{Agent: skills.Codex, File: codex, Transport: "stdio", Command: app},
			{Agent: skills.Grok, File: grok, Transport: "stdio", Command: app},
			{Agent: skills.Cursor, File: cursor, Transport: "stdio", Command: "/Users/x/old/iphone-use-mcp"},
		}, Agents: [skills.NumAgents]skills.Status{on(claude), on(codex), on(grok), {State: skills.On, Via: cursor, Inferred: true}}},
		{Name: "node_repl", Defs: []skills.MCPDef{{Agent: skills.Codex, File: home + "/.codex/plugins/cache/openai-bundled/browser", Plugin: true, Transport: "stdio", Command: "/Applications/ChatGPT.app/Contents/Resources/cua_node/bin/node_repl", EnvKeys: []string{"CODEX_HOME", "NODE_REPL_NODE_PATH"}}},
			Agents: [skills.NumAgents]skills.Status{{}, on(codex), {}, {}}},
		{Name: "sentry", Defs: []skills.MCPDef{{Agent: skills.Grok, File: grok, Transport: "http", URL: "https://mcp.sentry.dev/mcp", HeaderKeys: []string{"Authorization"}}},
			Agents: [skills.NumAgents]skills.Status{{}, {}, on(grok), {}}},
	}, Issues: []skills.MCPIssue{
		{Kind: skills.MCPMissingCommand, Name: "iphone-use", Detail: "/Users/x/old/iphone-use-mcp"},
		{Kind: skills.MCPMismatch, Name: "iphone-use", Detail: "2 种写法"},
	}}
}

// previewSkillsTab 打开文件栏的技能页签并填好样例。
func previewSkillsTab(a *app) {
	w := &a.workspaces[0]
	a.state.FilesPanels = map[string]bool{w.ProjectID: true}
	a.state.FilesPanelTab = tabSkills
	cat := previewSkills(homeDir)
	for i := range cat.Skills {
		cat.Skills[i].Dir = w.Path + cat.Skills[i].Dir[len("/Users/x/Desktop/AI/Apps"):]
	}
	a.skillsTab = skillsPanelView{key: w.Path, real: w.Path, cat: cat, poll: panelPoll{at: time.Now()}}
}
