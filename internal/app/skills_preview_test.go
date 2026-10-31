package app

import (
	"time"

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
		return skills.Skill{Name: name, Description: desc, Dir: dir, File: dir + "/SKILL.md", Scope: scope, Source: source, Agents: st, Paths: append([]string{dir}, paths...)}
	}
	return skills.Catalog{CodexLive: true, Skills: []skills.Skill{
		mk("archify", "Create validated architecture, workflow, sequence and data-flow diagrams as explorable standalone HTML with dark/light themes.", skills.ScopeUser, hub+"archify", "tt-a1i/archify",
			[skills.NumAgents]skills.Status{on(cl + "archify"), on(hub + "archify"), on(hub + "archify"), cursor(on(hub + "archify"))}, cl+"archify", home+"/.grok/skills/archify"),
		mk("browser-skill", "Automate the user's logged-in Chromium browser: read pages, fill forms, scrape data, operate tabs.", skills.ScopeUser, hub+"browser-skill", "",
			[skills.NumAgents]skills.Status{{}, on(hub + "browser-skill"), on(hub + "browser-skill"), cursor(on(hub + "browser-skill"))}),
		mk("browser-skill", "Automate the user's logged-in Chromium browser（Claude 目录里的旧副本）", skills.ScopeUser, cl+"browser-skill", "",
			[skills.NumAgents]skills.Status{on(cl + "browser-skill"), {}, {State: skills.Shadowed, Why: "被同名的 ~/.agents/skills/browser-skill 盖住"}, cursor(skills.Status{State: skills.Shadowed, Why: "被同名的 ~/.agents/skills/browser-skill 盖住"})}),
		mk("lark-calendar", "飞书日历：管理日历日程和会议室。查看/搜索日程、创建/更新日程、管理参会人、查询忙闲和推荐时段。", skills.ScopeUser, hub+"lark-calendar", "open.feishu.cn",
			[skills.NumAgents]skills.Status{{State: skills.Off, Why: "settings.json 的 skillOverrides 设为 off"}, on(hub + "lark-calendar"), {State: skills.Off, Why: "config.toml 的 [skills] disabled 里有它"}, cursor(on(hub + "lark-calendar"))}, cl+"lark-calendar"),
		mk("fiction-plot-structure", "长篇小说的情节结构：三幕、英雄之旅、救猫咪节拍表。", skills.ScopeUser, cl+"fiction-plot-structure", "",
			[skills.NumAgents]skills.Status{{State: skills.Manual, Why: "SKILL.md 设了 disable-model-invocation"}, {}, {State: skills.Manual, Why: "SKILL.md 设了 disable-model-invocation"}, cursor(skills.Status{State: skills.Manual, Why: "SKILL.md 设了 disable-model-invocation"})}),
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
		{Kind: skills.Duplicate, Name: "browser-skill", Path: cl + "browser-skill", Other: hub + "browser-skill", Agents: []skills.Agent{skills.Grok, skills.Cursor}},
		{Kind: skills.LockMissing, Name: "herdr", Path: home + "/.agents/.skill-lock.json", Other: hub + "herdr"},
		{Kind: skills.RedundantLink, Name: "archify", Path: home + "/.grok/skills/archify", Other: hub + "archify"},
		{Kind: skills.RedundantLink, Name: "eli5", Path: home + "/.grok/skills/eli5", Other: hub + "eli5"},
	}}
}

// previewSkillsBoard 打开技能页并填好样例，不去扫真的目录。
func previewSkillsBoard(a *app, sel string) {
	cat := previewSkills(homeDir)
	a.selected = skillsBoardID
	a.skills = skillsView{visible: true, loaded: true, cat: cat, at: time.Now(), sel: sel}
	if sel == "" {
		a.skills.sel = cat.Skills[0].Dir
	}
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
