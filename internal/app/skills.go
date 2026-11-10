package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/history"
	"posse/internal/skills"
	"posse/locales"
)

// 技能管理：侧栏的「技能」页管全局（用户级、账号同步、插件、系统技能和体检），文件栏的「技能」页签只管当前工作区的项目级技能。
// 扫描和各家的读取规则在 internal/skills，方案见 docs/plans/skills.md。

// skillsBoardID 是侧栏里「技能」一项的 ID；工作区 ID 都以 w_ 开头，不会冲突。
const skillsBoardID = "skills"

// skillIssuesKey 是技能页列表里「体检」一行的选中键；技能的选中键是目录，不会和它相同。
const skillIssuesKey = "issues"

// skillsView 是技能页的状态。
type skillsView struct {
	visible bool
	loading bool
	req     int // 每次扫描加一，过时的结果不要
	cat     skills.Catalog
	loaded  bool
	sel     string // 选中技能的目录，或 skillIssuesKey
	reveal  bool   // 方向键换了选中：下一帧把它滚进视野
	query   string
	agent   int // 0 全部，其余是 skills.Agents[agent-1] 用得上的
	byUse   int // 列表排序：0 按名字，1 按用量（多的在前）
	page    int // 0 技能，1 MCP

	mcp    skills.MCPCatalog
	mcpSel string // 选中的 MCP 服务名，或 skillIssuesKey

	// 检查更新：点了才查（要联网），结果一直留着，更新后锁文件里的版本对上了标记自然消失
	updates     *skills.Updates
	checking    bool
	checkReq    int
	onlyUpdates bool // 只看有新版本的

	// 跨会话的用量，单独在后台算：第一次要把各家的会话文件都读一遍（几十秒），之后按文件缓存
	stats        map[string]*history.SkillStat
	mcpStats     map[string]*history.SkillStat // MCP 服务的用量，和技能同一遍算
	statsLoading bool
	statsGen     int // 关掉技能页加一：之前在算的结果不要；页面开着时重扫不影响正在算的用量
	statsDone    int
	statsTotal   int
}

// skillsPanelView 是文件栏「技能」页签的状态：当前工作区的项目级技能。
type skillsPanelView struct {
	key  string // 工作区目录，变了就重扫
	real string // 解开链接后的工作区目录（/tmp 是 /private/tmp 的链接），技能目录是解开的，按它算相对路径
	cat  skills.Catalog
	poll panelPoll
}

// findCodex、findGrok 找 codex、grok 可执行文件，测试里换掉免得拉起真的。
var (
	findCodex = skills.FindCodex
	findGrok  = skills.FindGrok
)

// skillsRefresh 是文件栏的技能页签开着时多久重扫一次：只是 stat 几个目录。
const skillsRefresh = 3 * time.Second

func (a *app) setSkillsVisible(v bool) {
	s := &a.skills
	opened := v && !s.visible
	s.visible = v
	if opened {
		a.scanSkills()
	}
}

// scanSkills 在后台扫全局的技能，Codex 的状态问它的 app-server（插件技能只有它算得准），问不到按文件推算。
func (a *app) scanSkills() {
	s := &a.skills
	s.req++
	s.loading = true
	req := s.req
	go func() {
		codex := findCodex(homeDir)
		// MCP 另起一路扫、扫完自己交给界面：grok 慢时不拖着技能列表
		go func() {
			mcp := skills.ScanMCP(context.Background(), skills.MCPOptions{Home: homeDir, Codex: codex, Grok: findGrok(homeDir)})
			a.update(func() {
				if s.req == req {
					s.mcp = mcp
				}
			})
		}()
		var list []skills.CodexSkill
		if codex != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			list, _ = skills.CodexList(ctx, codex, homeDir)
			cancel()
		}
		cat := skills.Scan(skills.Options{Home: homeDir, Codex: list})
		a.update(func() {
			if s.req != req {
				return
			}
			s.cat, s.loaded, s.loading = cat, true, false
		})
		a.update(a.readPosseSkills)
	}()
	if !s.statsLoading {
		s.statsLoading = true
		gen := s.statsGen
		cache := ""
		if a.home != "" {
			cache = filepath.Join(a.home, "skill-usage.json")
		}
		go func() {
			stats, _ := history.SkillStats(context.Background(), history.Roots{Home: homeDir}, cache, func(done, total int) {
				a.update(func() {
					if s.statsGen == gen {
						s.statsDone, s.statsTotal = done, total
					}
				})
			})
			a.update(func() {
				if s.statsGen != gen {
					return
				}
				s.statsLoading = false
				if stats != nil {
					s.stats, s.mcpStats = stats.Skills, stats.MCP
				}
			})
		}()
	}
}

// checkSkillUpdates 在后台查 npx skills 装的技能有没有新版本。
func (a *app) checkSkillUpdates() {
	s := &a.skills
	s.checkReq++
	req := s.checkReq
	s.checking = true
	go func() {
		u := skills.CheckUpdates(context.Background(), homeDir)
		a.update(func() {
			if s.checkReq != req {
				return
			}
			s.updates, s.checking = &u, false
		})
	}()
}

// updateCount 是有新版本的技能数。
func (s *skillsView) updateCount() int {
	n := 0
	for _, sk := range s.cat.Skills {
		if s.updates.HasUpdate(sk) {
			n++
		}
	}
	return n
}

func (a *app) releaseSkills() {
	s := &a.skills
	s.req++
	*s = skillsView{req: s.req, statsGen: s.statsGen + 1, query: s.query, agent: s.agent, byUse: s.byUse, page: s.page,
		updates: s.updates, checking: s.checking, checkReq: s.checkReq, onlyUpdates: s.onlyUpdates}
}

func (a *app) skillsSidebarItem(c *ui.Context) ui.Element {
	item := a.navTile(c, skillsBoardID, iconSkill, L(c).SkillsTitle(), nil)
	if a.revealSel && a.selected == skillsBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
	return item
}

// skillAgentLabels 是技能页按 agent 筛选的分段。
func skillAgentLabels(m locales.Messages) []string {
	return []string{m.CommonAll(), "Claude", "Codex", "Grok", "Cursor"}
}

// scopePosse 是列表里 Posse 自带技能的一组：它们装在用户目录里，扫描算作用户级，界面上单列出来。
const scopePosse skills.Scope = "posse"

// listScope 是技能在列表里归哪一组。
func listScope(sk skills.Skill) skills.Scope {
	if sk.Meta.Posse && sk.Scope == skills.ScopeUser {
		return scopePosse
	}
	return sk.Scope
}

// scopeLabels 是技能来源的写法和列表里分组的顺序。
var scopeLabels = []struct {
	scope skills.Scope
	label func(locales.Messages) string
}{
	{scopePosse, locales.Messages.SkillsScopePosse},
	{skills.ScopeUser, locales.Messages.SkillsScopeUser},
	{skills.ScopeSynced, locales.Messages.SkillsScopeSynced},
	{skills.ScopePlugin, locales.Messages.SkillsScopePlugin},
	{skills.ScopeSystem, locales.Messages.SkillsScopeSystem},
	{skills.ScopeProject, locales.Messages.SkillsScopeProject},
}

func scopeLabel(m locales.Messages, sc skills.Scope) string {
	for _, s := range scopeLabels {
		if s.scope == sc {
			return s.label(m)
		}
	}
	return string(sc)
}

// shownSkills 是技能页列出的技能：不含项目级（文件栏里看），按搜索词和 agent 筛过。
func (s *skillsView) shownSkills() []skills.Skill {
	// 更新完最后一个就没有可看的了：回到全部，不然列表空着、也没有那一行可以点回去
	if s.onlyUpdates && s.updateCount() == 0 {
		s.onlyUpdates = false
	}
	q := strings.ToLower(strings.TrimSpace(s.query))
	var out []skills.Skill
	for _, sk := range s.cat.Skills {
		if sk.Scope == skills.ScopeProject {
			continue
		}
		if s.agent > 0 && sk.Agents[skills.Agents[s.agent-1]].State == skills.Unseen {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(sk.Name), q) && !strings.Contains(strings.ToLower(sk.Description), q) {
			continue
		}
		if s.onlyUpdates && !s.updates.HasUpdate(sk) {
			continue
		}
		out = append(out, sk)
	}
	order := func(sc skills.Scope) int {
		return slices.IndexFunc(scopeLabels, func(x struct {
			scope skills.Scope
			label func(locales.Messages) string
		}) bool {
			return x.scope == sc
		})
	}
	slices.SortStableFunc(out, func(x, y skills.Skill) int {
		if d := order(listScope(x)) - order(listScope(y)); d != 0 {
			return d
		}
		if s.byUse == 1 && s.stats != nil {
			if d := s.uses(y.Name) - s.uses(x.Name); d != 0 {
				return d
			}
		}
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return out
}

// refreshButton 是顶栏的重新扫描：技能和 MCP 一起扫。
func (a *app) refreshButton(c *ui.Context) {
	s := &a.skills
	refresh := ui.Box(c).Shrink(0)
	if s.loading {
		refresh.Disabled(true).Opacity(0.5)
	}
	refresh.Children(func() {
		if iconButton(c.Key("skills-refresh"), iconRefresh, L(c).SkillsRescan()).Clicked() {
			a.scanSkills()
		}
	})
}

// uses 是技能在所有会话里用过的次数。
func (s *skillsView) uses(name string) int {
	if st := s.stats[name]; st != nil {
		return st.Count
	}
	return 0
}

func (a *app) skillsBoard(c *ui.Context) {
	s := &a.skills
	m := L(c)
	a.band(c, func() {
		// 分页兼作标题：侧栏里叫「技能」，这里分技能和 MCP
		segmented(c, &s.page, m.SkillsPageLabel(), m.SkillsTitle(), "MCP")
		placeholder := m.SkillsSearchSkills()
		if s.page == 1 {
			placeholder = m.SkillsSearchMCP()
		}
		fieldInputSized(c, &s.query, placeholder, 280, 100).Label(m.SkillsSearchLabel())
		ui.Spacer(c)
		segmented(c, &s.agent, m.SkillsAgentFilter(), skillAgentLabels(m)...)
		if s.page == 1 {
			a.refreshButton(c)
			a.closeBoardButton(c)
			return
		}
		if pressable(textButton(c, m.SkillsInstallEllipsis())).Clicked() {
			a.openSkillInstall(m, a.skillsWorkspace(), true)
		}
		check := ui.Box(c).Shrink(0)
		label := m.SkillsCheckUpdates()
		if s.checking {
			label = m.SkillsChecking()
			check.Disabled(true).Opacity(0.5)
		}
		check.Children(func() {
			if pressable(textButton(c, label)).Clicked() {
				a.checkSkillUpdates()
			}
		})
		a.refreshButton(c)
		a.closeBoardButton(c)
	})
	if s.page == 1 {
		a.mcpBody(c)
		return
	}
	list := s.shownSkills()
	if s.sel == "" || s.sel != skillIssuesKey && !slices.ContainsFunc(list, func(sk skills.Skill) bool { return sk.Dir == s.sel }) {
		s.sel = ""
		if len(list) > 0 {
			s.sel = list[0].Dir
		}
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(pageGap).Gap(pageGap).Children(func() {
		// 可以聚焦：点过列表后方向键归它，不被侧栏拿走
		card(c).Width(380).Shrink(0).Clip().Focusable().FocusRing(false).Label(m.SkillsListLabel()).Children(func() {
			a.skillTotals(c)
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(4, 8, 16, 8).Gap(2).Children(func() {
					if !s.loaded {
						usageNote(c, m.CommonScanning())
						return
					}
					if len(list) == 0 {
						usageNote(c, m.SkillsNoMatch())
					}
					var group skills.Scope
					for _, sk := range list {
						if listScope(sk) != group {
							group = listScope(sk)
							n := 0
							for _, x := range list {
								if listScope(x) == group {
									n++
								}
							}
							skillGroupHeader(c, scopeLabel(m, group), n)
						}
						row := a.skillRow(c, sk, s.sel == sk.Dir, func() { s.sel = sk.Dir })
						if s.reveal && s.sel == sk.Dir {
							row.ScrollIntoView()
							s.reveal = false
						}
					}
				})
			})
		})
		ui.Column(c).Grow(1).Shrink(1).Gap(pageGap).Children(func() {
			if s.sel == skillIssuesKey {
				a.skillIssues(c)
				return
			}
			for _, sk := range list {
				if sk.Dir == s.sel {
					a.skillDetail(c, sk)
				}
			}
		})
	})
	keys := make([]string, len(list))
	for i, sk := range list {
		keys[i] = sk.Dir
	}
	a.skillsArrowKeys(c, keys, &s.sel)
}

// skillsArrowKeys 让上下方向键在列表里换选中（技能和 MCP 共用）；焦点在搜索框里、列表上或哪儿都不在时都管用，
// 对话框开着时不管。
func (a *app) skillsArrowKeys(c *ui.Context, keys []string, sel *string) {
	if a.skillInstall.open || a.skillChange.open || a.skillRun.open || a.skillNew.open || a.confirming {
		return
	}
	switch {
	case c.Shortcut(0, ui.KeyDown):
		a.skills.step(keys, sel, 1)
	case c.Shortcut(0, ui.KeyUp):
		a.skills.step(keys, sel, -1)
	}
}

// step 选中 keys 里的下一个（step 为 1）或上一个；在看体检时下键选第一个、上键不动。到头了不动。
func (s *skillsView) step(keys []string, sel *string, step int) {
	cur := slices.Index(keys, *sel)
	j := cur + step
	if cur < 0 {
		j = 0
		if step < 0 {
			return
		}
	}
	if j >= 0 && j < len(keys) {
		*sel, s.reveal = keys[j], true
	}
}

// skillUpdatesLine 是检查更新的结果：有新版本时点一下只看它们（再点回到全部），都是最新的写一句，
// 有查不了的来源一并写上，悬停看原因。
func (a *app) skillUpdatesLine(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	s := &a.skills
	u := s.updates
	if u == nil {
		return
	}
	n := s.updateCount()
	var problems []string
	if len(u.Failed) > 0 {
		problems = append(problems, m.SkillsSourcesFailed(len(u.Failed)))
	}
	var gone []string
	for _, sk := range s.cat.Skills {
		if u.IsGone(sk) {
			gone = append(gone, m.SkillsGoneLine(sk.Name))
		}
	}
	if len(gone) > 0 {
		problems = append(problems, m.SkillsGoneCount(len(gone)))
	}
	failed := strings.Join(problems, m.SkillsClauseSeparator())
	var lines []string
	for _, f := range u.Failed {
		lines = append(lines, m.SkillsSourceFailedLine(f.Source, errtext.Message(f.Err)))
	}
	why := strings.Join(append(lines, gone...), "\n")
	if n == 0 {
		text := m.SkillsUpToDate()
		if failed != "" {
			text = m.SkillsUpToDateWith(failed)
		}
		e := ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted).Margin(0, 20, 8, 20)
		if failed != "" {
			e.Tooltip(why)
		}
		return
	}
	row := ui.Row(c.Key("skill-updates")).Margin(0, 8, 4, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Center).Role(ui.RoleButton).Label(m.SkillsUpdatesLabel())
	selectedBg(c, row, s.onlyUpdates)
	row.Children(func() {
		ui.Icon(c, iconFetch).FontSize(iconSize).TextColor(t.Accent).Shrink(0)
		// 计数不折行，挤不下时让右边的说明折行
		ui.Text(c, m.SkillsUpdatesCount(n)).FontSize(fsBody).FontFeatures("tnum").Shrink(0)
		ui.Spacer(c)
		if failed != "" {
			ui.Text(c, failed).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(1).Tooltip(why)
		}
	})
	if s.onlyUpdates {
		row.Tooltip(m.SkillsShowAllTip())
	} else {
		row.Tooltip(m.SkillsShowUpdatesTip())
	}
	if row.Clicked() {
		s.onlyUpdates = !s.onlyUpdates
	}
}

// skillTotals 是列表顶上的总计：技能数、各家用得上的数，和体检发现的问题（点开看）。
func (a *app) skillTotals(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	s := &a.skills
	if !s.loaded {
		return
	}
	n := 0
	var byAgent [skills.NumAgents]int
	for _, sk := range s.cat.Skills {
		if sk.Scope == skills.ScopeProject {
			continue
		}
		n++
		for _, ag := range skills.Agents {
			if st := sk.Agents[ag].State; st == skills.On || st == skills.Manual {
				byAgent[ag]++
			}
		}
	}
	ui.Column(c).Padding(12, 20, 8, 20).Gap(8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, m.SkillsCount(n)).FontSize(fsBody).FontWeight(fwMedium).FontFeatures("tnum").Grow(1)
			if s.statsLoading && s.stats == nil {
				note := m.SkillsCountingUsage()
				if s.statsTotal > 0 {
					note = m.SkillsCountingUsageProgress(s.statsDone, s.statsTotal)
				}
				ui.Text(c, note).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			} else {
				segmented(c, &s.byUse, m.SkillsSortLabel(), m.SkillsSortName(), m.SkillsSortUsage())
			}
		})
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range skills.Agents {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentIcon(c, ag.String(), smallIcon())
					ui.Text(c, fmt.Sprint(byAgent[ag])).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}).Tooltip(m.SkillsAgentCountTip(agentTitle(ag.String())))
			}
		})
	})
	a.skillUpdatesLine(c)
	a.posseSkillsLine(c)
	if k := len(s.cat.Issues); k > 0 {
		row := ui.Row(c.Key("skill-issues")).Margin(0, 8, 4, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Center).Role(ui.RoleButton).Label(m.SkillsIssuesLabel())
		selectedBg(c, row, s.sel == skillIssuesKey)
		row.Children(func() {
			ui.Icon(c, iconInfo).FontSize(iconSize).TextColor(colors(t).honey).Shrink(0)
			ui.Text(c, m.SkillsIssuesFound(k)).FontSize(fsBody).FontFeatures("tnum").Grow(1)
			ui.Icon(c, iconForward).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		})
		if row.Clicked() {
			s.sel = skillIssuesKey
		}
	}
	hline(c).Margin(0, 20)
}

func skillGroupHeader(c *ui.Context, label string, n int) {
	t := c.Theme()
	ui.Row(c).Padding(12, 12, 4, 12).Gap(6).Children(func() {
		ui.Text(c, label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(colors(t).faint)
	})
}

// skillRow 是列表里的一个技能：名字和说明，行尾是四家的状态。
func (a *app) skillRow(c *ui.Context, sk skills.Skill, selected bool, onClick func()) ui.Element {
	t := c.Theme()
	row := ui.Column(c.Key(sk.Dir)).Padding(8, 12).Gap(2).Radius(rControl).Role(ui.RoleButton).Label(sk.Name)
	selectedBg(c, row, selected)
	row.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, sk.Name).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Grow(1).Shrink(1)
			skillMarks(c, sk, nil)
		})
		if sk.Description != "" {
			ui.Text(c, oneLine(sk.Description)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2).LineHeight(1.35)
		}
		manual := sk.Meta.ModelOff || sk.Meta.UserHidden
		update := a.skills.updates.HasUpdate(sk)
		if a.skills.stats != nil || manual || update {
			ui.Row(c).Gap(12).Children(func() {
				if update {
					ui.Text(c, L(c).SkillsHasUpdate()).FontSize(fsSmall).TextColor(t.Accent)
				}
				// 默认是自动触发，列表里只标例外，免得每行都多两个字
				if manual {
					skillTrigger(c, sk.Meta)
				}
				if a.skills.stats != nil {
					skillUseLine(c, a.skills.stats[sk.Name])
				}
			})
		}
	})
	if row.Clicked() {
		onClick()
	}
	return row
}

// skillMarks 是一行末尾四家的状态：用得上的是 agent 的色块，只能手动调用的淡一些，关了的更淡，
// 读不到的是同形的淡底空块（不用淡图标，免得和关了混淆），四家对齐成列。悬停看原因。toggle 不为空时能开关的那几家点一下切换（先弹框确认）。
func skillMarks(c *ui.Context, sk skills.Skill, toggle func(ag skills.Agent, enable bool)) {
	agentMarks(c, sk.Agents, func(ag skills.Agent) bool { return toggle != nil && skills.CanToggle(ag, sk) }, toggle)
}

// agentMarks 是四家的状态标记，技能和 MCP 共用；can 说哪几家点一下能切换，toggle 为空时都不能。
func agentMarks(c *ui.Context, sts [skills.NumAgents]skills.Status, can func(skills.Agent) bool, toggle func(ag skills.Agent, enable bool)) {
	ui.Row(c).Gap(4).Shrink(0).Children(func() {
		for _, ag := range skills.Agents {
			st := sts[ag]
			size := smallIcon()
			tip := skillStateTip(L(c), ag, st)
			if st.State == skills.Unseen {
				ui.Box(c).Size(size, size).Radius(size * 0.26).Background(colors(c.Theme()).fill).Shrink(0).Tooltip(tip)
				continue
			}
			can := toggle != nil && can(ag)
			if can {
				if st.State == skills.Off {
					tip += "\n" + L(c).SkillsClickToEnable()
				} else {
					tip += "\n" + L(c).SkillsClickToDisable()
				}
			}
			e := agentIcon(c, ag.String(), size).Tooltip(tip)
			switch st.State {
			case skills.Manual:
				e.Opacity(0.55)
			case skills.Off, skills.Shadowed:
				e.Opacity(0.2)
			}
			if can {
				e.Role(ui.RoleButton).Label(agentTitle(ag.String()))
				if pressable(e).Clicked() {
					toggle(ag, st.State == skills.Off)
				}
			}
		}
	})
}

func skillStateText(m locales.Messages, st skills.State) string {
	switch st {
	case skills.On:
		return m.SkillsStateOn()
	case skills.Manual:
		return m.SkillsStateManual()
	case skills.Off:
		return m.SkillsStateOff()
	case skills.Shadowed:
		return m.SkillsStateShadowed()
	}
	return m.SkillsStateUnseen()
}

// skillWhy 是这家对技能是这个状态的原因，按界面语言写。
func skillWhy(m locales.Messages, st skills.Status) string {
	if st.NotRead != "" {
		if len(st.Reads) == 0 {
			return m.SkillsNotRead(st.NotRead)
		}
		return m.SkillsNotReadReads(st.NotRead, strings.Join(st.Reads, m.SkillsListSeparator()))
	}
	return errtext.Message(st.Why)
}

func skillStateTip(m locales.Messages, ag skills.Agent, st skills.Status) string {
	tip := m.SkillsStateTip(agentTitle(ag.String()), skillStateText(m, st.State))
	if why := skillWhy(m, st); why != "" {
		tip += "\n" + why
	}
	if st.Inferred && st.State != skills.Unseen {
		tip += "\n" + m.SkillsInferredNote()
	}
	return tip
}

// skillDetail 是右边的详情：名字、来源和位置，各家的状态，完整的说明。
func (a *app) skillDetail(c *ui.Context, sk skills.Skill) {
	t := c.Theme()
	m := L(c)
	card(c).Padding(cardPad).Gap(12).Shrink(0).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
			tile(c, iconSkill, tileLarge(), colors(t).tint, t.Accent)
			ui.Column(c).Gap(8).Grow(1).Shrink(1).Children(func() {
				titleText(c, sk.Name, fsPage).LineHeight(1.3).MaxLines(2).Selectable()
				var source func()
				if sk.Source != "" {
					source = metaMono(c, nil, sk.Source, m.SkillsSourceTip())
				}
				metaRow(c, meta(c, scopeLabel(m, listScope(sk))), source, metaMono(c, iconFolder, shortPath(sk.Dir), sk.Dir))
			})
			ui.Row(c).Gap(4).Shrink(0).Children(func() {
				if pressable(textButton(c, m.SkillsOpenFile())).Clicked() {
					a.openSkillFile(sk.File)
				}
				a.skillActions(c, sk, a.skillsWorkspace(), true)
				if sk.SourceURL != "" && pressable(iconButton(c.Key("skill-source"), iconOpen, m.SkillsOpenSourcePage())).Clicked() {
					c.OpenURL(sk.SourceURL)
				}
				if pressable(iconButton(c.Key("skill-reveal"), iconFolder, m.CommonRevealInFinder())).Clicked() {
					mygo.Shell.ShowItemInFolder(sk.File)
				}
			})
		})
		if a.skills.updates.IsGone(sk) {
			ui.Text(c, m.SkillsGoneNote()).
				FontSize(fsSmall).TextColor(colors(t).honey).LineHeight(1.45)
		}
		if sk.Description != "" {
			ui.Text(c, sk.Description).FontSize(fsBody).LineHeight(1.55).Selectable()
		}
	})
	if a.skills.stats != nil {
		skillUseCard(c, a.skills.stats[sk.Name])
	}
	card(c).Padding(cardPad).Gap(4).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Margin(0, 0, 4, 0).Children(func() {
			cardTitle(c, m.SkillsAgentStatus())
			skillTrigger(c, sk.Meta)
		})
		for i, ag := range skills.Agents {
			if i > 0 {
				hline(c)
			}
			a.skillAgentRow(c, sk, ag, skills.Options{Home: homeDir})
		}
	})
}

// skillAgentRow 是详情里一家的状态：色块、名字、状态，下面一行是原因和读到它的路径；能开关的行尾有开关。
func (a *app) skillAgentRow(c *ui.Context, sk skills.Skill, ag skills.Agent, o skills.Options) {
	t := c.Theme()
	m := L(c)
	st := sk.Agents[ag]
	ui.Row(c.Key("agent-"+ag.String())).Padding(6, 0).Gap(12).AlignItems(ui.Start).Children(func() {
		e := agentIcon(c, ag.String(), iconSize)
		if st.State != skills.On {
			e.Opacity(0.55)
		}
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, agentTitle(ag.String())).FontSize(fsBody).FontWeight(fwMedium)
				color := t.TextMuted
				switch st.State {
				case skills.On:
					color = colors(t).sage
				case skills.Shadowed:
					color = colors(t).honey
				}
				ui.Text(c, skillStateText(m, st.State)).FontSize(fsSmall).TextColor(color)
				if st.Inferred && st.State != skills.Unseen {
					ui.Text(c, m.SkillsInferred()).FontSize(fsSmall).TextColor(colors(t).faint).Tooltip(m.SkillsCursorInferredTip())
				}
			})
			if why := skillWhy(m, st); why != "" {
				ui.Text(c, why).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			}
			if st.Via != "" {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, shortPath(st.Via)).FontSize(fsSmall).Font(monoFont).TextColor(colors(t).faint).MaxLines(2).Shrink(1)
					// Codex 报的是 SKILL.md 的路径
					// 只看这条路径本身是不是链接：上级目录是链接（如 ~/.claude 链到 dotfiles）时它照样不是软链
					if fi, err := os.Lstat(strings.TrimSuffix(st.Via, string(filepath.Separator)+"SKILL.md")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
						ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Tooltip(m.SkillsSymlinkTip(shortPath(sk.Dir))).Children(func() {
							ui.Icon(c, iconFolderLink).FontSize(fsSmall).TextColor(colors(t).faint)
							ui.Text(c, m.SkillsSymlink()).FontSize(fsSmall).TextColor(colors(t).faint)
						})
					}
				})
			}
		})
		if skills.CanToggle(ag, sk) {
			on := st.State != skills.Off
			if ui.Switch(c, &on).Label(m.SkillsEnableIn(agentTitle(ag.String()))).Changed() {
				a.planSkillToggle(m, o, sk, ag, on)
			}
		} else if link, ok := skills.LinkFor(o, sk, ag); ok {
			// 读不到的：打开就在这家读的目录里建一条链接
			on := false
			if ui.Switch(c, &on).Label(m.SkillsLinkLabel(agentTitle(ag.String()))).Tooltip(m.SkillsLinkTip(shortPath(filepath.Dir(link)))).Changed() {
				a.askLinkSkill(m, sk, ag, link)
			}
		}
	})
}

// issueKinds 是体检里各类问题的顺序、标题和说明。
var issueKinds = []struct {
	kind         skills.IssueKind
	title, about func(locales.Messages) string
}{
	{skills.BrokenLink, locales.Messages.SkillsIssueBrokenLink, locales.Messages.SkillsIssueBrokenLinkNote},
	{skills.Duplicate, locales.Messages.SkillsIssueDuplicate, locales.Messages.SkillsIssueDuplicateNote},
	{skills.LockMissing, locales.Messages.SkillsIssueLockMissing, locales.Messages.SkillsIssueLockMissingNote},
	{skills.RedundantLink, locales.Messages.SkillsIssueRedundantLink, locales.Messages.SkillsIssueRedundantLinkNote},
}

// skillIssues 是体检结果，按类分组。
func (a *app) skillIssues(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(pageGap).Children(func() {
			for _, k := range issueKinds {
				var items []skills.Issue
				for _, is := range a.skills.cat.Issues {
					if is.Kind == k.kind {
						items = append(items, is)
					}
				}
				if len(items) == 0 {
					continue
				}
				card(c).Padding(cardPad).Gap(8).Shrink(0).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						cardTitle(c, k.title(m))
						ui.Text(c, fmt.Sprint(len(items))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
					})
					ui.Text(c, k.about(m)).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
					if k.kind == skills.RedundantLink && len(items) > 1 {
						if pressable(textButton(c, m.SkillsTrashAllLinks(len(items)))).Clicked() {
							a.askTrashLinks(m, items)
						}
					}
					ui.Column(c).Gap(2).Children(func() {
						for _, is := range items {
							a.issueRow(c, is)
						}
					})
				})
			}
		})
	})
}

func (a *app) issueRow(c *ui.Context, is skills.Issue) {
	t := c.Theme()
	m := L(c)
	ui.Column(c.Key(string(is.Kind)+is.Path+is.Other)).Padding(6, 0).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, is.Name).FontSize(fsBody).FontWeight(fwMedium).Shrink(0)
			for _, ag := range is.Agents {
				agentTile(c, ag.String(), smallIcon())
			}
			ui.Spacer(c)
			switch is.Kind {
			case skills.BrokenLink, skills.RedundantLink:
				if pressable(iconButtonSized(c, iconTrash, m.SkillsTrashLinkTip(), compactBox(), smallIcon())).Clicked() {
					a.askTrashLinks(m, []skills.Issue{is})
				}
			case skills.Duplicate:
				if pressable(iconButtonSized(c.Key("dup-reveal"), iconFolder, m.SkillsRevealInactive(), compactBox(), smallIcon())).Clicked() {
					mygo.Shell.ShowItemInFolder(is.Path)
				}
				if pressable(iconButtonSized(c.Key("dup-merge"), iconFolderLink, m.SkillsMergeTip(), compactBox(), smallIcon())).Clicked() {
					a.askMergeDuplicate(m, is)
				}
			case skills.LockMissing:
				if pressable(iconButtonSized(c, iconTrash, m.SkillsRemoveFromLock(), compactBox(), smallIcon())).Clicked() {
					a.askSkillCommand(m, m.SkillsRemoveFromLockConfirm(is.Name), m.SkillsRemoveFromLockBody(), skills.ShellLine(skills.RemoveArgs(is.Name, true)), a.skillsWorkspace(), false)
				}
			}
		})
		var line string
		switch is.Kind {
		case skills.BrokenLink:
			line = shortPath(is.Path) + " → " + shortPath(is.Other)
		case skills.Duplicate:
			line = m.SkillsShadowedDiff(shortPath(is.Path), shortPath(is.Other))
			if is.Same {
				line = m.SkillsShadowedSame(shortPath(is.Path), shortPath(is.Other))
			}
		case skills.LockMissing:
			line = m.SkillsMissingDir(shortPath(is.Other))
		case skills.RedundantLink:
			line = shortPath(is.Path) + " → " + shortPath(is.Other)
		}
		ui.Text(c, line).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(3)
	})
}

// openSkillFile 在上次看的工作区的文件标签里打开 SKILL.md（Markdown 预览会把头部画成属性列表）。
func (a *app) openSkillFile(path string) {
	w := a.workspace(a.lastWS)
	if w == nil && len(a.workspaces) > 0 {
		w = &a.workspaces[0]
	}
	if w == nil {
		return
	}
	a.selected, a.revealSel = w.ID, true
	a.openFile(w, path, 0, [2]int{})
}

// --- 文件栏的「技能」页签

// pollSkillsPanel 在主线程调用：换了工作区就重扫，开着时定时重扫（项目里的技能常是 agent 刚写的）。
func (a *app) pollSkillsPanel(c *ui.Context, w *workspace) {
	p := &a.skillsTab
	if p.key != w.Path {
		*p = skillsPanelView{key: w.Path}
	}
	if !p.poll.due(c, skillsRefresh) {
		return
	}
	key := w.Path
	go func() {
		cat := skills.Scan(skills.Options{Home: homeDir, Dir: key})
		real, err := filepath.EvalSymlinks(key)
		if err != nil {
			real = key
		}
		a.update(func() {
			if p.key != key {
				return
			}
			p.poll.done()
			p.cat, p.real = cat, real
		})
	}()
}

// skillsPanel 是文件栏的「技能」页签：当前工作区的项目级技能（工作区和它往上到仓库根的各级目录里的），点一个打开 SKILL.md。
func (a *app) skillsPanel(c *ui.Context, w *workspace) {
	t := c.Theme()
	m := L(c)
	a.pollSkillsPanel(c, w)
	p := &a.skillsTab
	var list []skills.Skill
	for _, sk := range p.cat.Skills {
		if sk.Scope == skills.ScopeProject {
			list = append(list, sk)
		}
	}
	slices.SortFunc(list, func(x, y skills.Skill) int { return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)) })
	ui.Row(c).Shrink(0).Padding(8, 12, 4, 12).Gap(8).Children(func() {
		if pressable(textButton(c, m.SkillsNewEllipsis())).Clicked() {
			a.skillNew = skillNewView{open: true, ws: w.ID}
		}
		if pressable(textButton(c, m.SkillsInstallEllipsis())).Clicked() {
			a.openSkillInstall(m, w, false)
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(8, 8, 16, 8).Gap(2).Children(func() {
			switch {
			case p.poll.at.IsZero():
				usageNote(c, m.CommonScanning())
			case len(list) == 0:
				usageNote(c, m.SkillsPanelEmpty())
			}
			for _, sk := range list {
				row := ui.Column(c.Key(sk.Dir)).Padding(8, 8).Gap(2).Radius(rControl).Role(ui.RoleButton).Label(sk.Name)
				selectedBg(c, row, false)
				row.Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, sk.Name).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Grow(1).Shrink(1)
						skillMarks(c, sk, func(ag skills.Agent, enable bool) {
							a.planSkillToggle(m, skills.Options{Home: homeDir, Dir: w.Path}, sk, ag, enable)
						})
					})
					if sk.Description != "" {
						ui.Text(c, oneLine(sk.Description)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2).LineHeight(1.35)
					}
					rel := sk.Dir
					if r, err := filepath.Rel(p.real, sk.Dir); err == nil && !strings.HasPrefix(r, "..") {
						rel = r
					}
					ui.Text(c, rel).FontSize(fsSmall).Font(monoFont).TextColor(colors(t).faint).SingleLine()
				})
				if row.Clicked() {
					a.openFile(w, sk.File, 0, [2]int{})
				}
			}
		})
	})
}

// --- 开关：先算好要改的文件和内容，弹框给用户看 diff，确认后写

// skillChangeView 是开关技能的确认框。
type skillChangeView struct {
	open bool
	ch   skills.Change
}

// planSkillToggle 算出改动并弹框确认；算不出来（配置认不出、不用改）时报在错误条上。
func (a *app) planSkillToggle(m locales.Messages, o skills.Options, sk skills.Skill, ag skills.Agent, enable bool) {
	ch, err := skills.PlanToggle(o, sk, ag, enable)
	if err != nil {
		a.err = m.SkillsToggleError(agentTitle(ag.String()), errtext.Text(err))
		return
	}
	a.skillChange = skillChangeView{open: true, ch: ch}
}

// applySkillChange 写入确认过的改动，写完重扫技能页和文件栏的技能页签。
func (a *app) applySkillChange(m locales.Messages, ch skills.Change) {
	go func() {
		err := ch.Apply()
		a.update(func() {
			if err != nil {
				a.err = m.SkillsChangeError(shortPath(ch.File), errtext.Text(err))
			}
			if a.skills.visible {
				a.scanSkills()
			}
			a.skillsTab.poll.at = time.Time{}
		})
	}()
}

// skillChangeDialog 是开关技能的确认框：改哪个文件、diff，确认才写。
func (a *app) skillChangeDialog(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	p := colors(t)
	v := &a.skillChange
	ui.Modal(c, &v.open, func() {
		ch := v.ch
		title := m.SkillsDisableTitle(agentTitle(ch.Agent.String()), ch.Skill)
		if ch.Enable {
			title = m.SkillsEnableTitle(agentTitle(ch.Agent.String()), ch.Skill)
		}
		ui.Column(c).Width(520).Gap(12).Children(func() {
			titleText(c, title, fsTitle)
			ui.Column(c).Gap(4).Children(func() {
				what := m.SkillsWillModify()
				if ch.Before == "" {
					what = m.SkillsWillCreate()
				}
				ui.Text(c, what).FontSize(fsBody).TextColor(t.TextMuted)
				ui.Text(c, shortPath(ch.File)).FontSize(fsSmall).Font(monoFont).Selectable()
			})
			ui.Scroll(c).MaxHeight(sz(260)).Children(func() {
				ui.Column(c).Padding(8, 0).Radius(rCard).Background(p.code).Children(func() {
					for _, l := range skills.Diff(ch.Before, ch.After, 3) {
						row := ui.Row(c).Padding(0, 12)
						fg := t.Text
						text := string(l.Kind) + " " + l.Text
						switch l.Kind {
						case '+':
							row.Background(soft(t, p.sage))
						case '-':
							row.Background(soft(t, t.Danger))
						case 0:
							fg, text = t.TextMuted, "⋯"
						default:
							fg = t.TextMuted
						}
						row.Children(func() {
							ui.Text(c, text).FontSize(fsSmall).Font(monoFont).TextColor(fg).LineHeight(1.5)
						})
					}
				})
			})
			ui.Text(c, m.SkillsReopenNote()).FontSize(fsSmall).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					v.open = false
				}
				if pressable(accentButton(c, m.SkillsConfirmChange())).Clicked() {
					v.open = false
					a.applySkillChange(m, ch)
				}
			})
		})
	})
}

// --- 装、删、更新：拼好 npx skills 的命令，确认后在工作区里开一个终端跑，输出和它的提问都看得见

// skillsWorkspace 是技能页跑命令、打开文件用的工作区：上次看的那个，没有就第一个。
func (a *app) skillsWorkspace() *workspace {
	if w := a.workspace(a.lastWS); w != nil {
		return w
	}
	if len(a.workspaces) > 0 {
		return &a.workspaces[0]
	}
	return nil
}

// skillActions 是详情顶上的更新、删除：npx skills 装的交给它（删除不进废纸篓，连同各家的链接）；
// 自己放的用户级技能移到废纸篓，连同指向它的链接。插件、系统自带和账号同步的归各家自己管。
func (a *app) skillActions(c *ui.Context, sk skills.Skill, w *workspace, global bool) {
	m := L(c)
	switch {
	case listScope(sk) == scopePosse:
		// npx 不查本地来源的更新：和这一版不同时给显眼的更新，按 Posse 的办法再装一次
		if global && slices.Contains(a.posseSkills.st.Outdated, sk.Name) && pressable(accentButton(c.Key("skill-update"), m.SkillsUpdate())).Clicked() {
			a.installPosseSkills(m)
		}
		if sk.Source != "" {
			if pressable(iconButton(c.Key("skill-remove"), iconTrash, m.CommonDelete())).Clicked() {
				a.askSkillRemove(m, sk, w, global)
			}
		} else if pressable(iconButton(c.Key("skill-trash"), iconTrash, m.CommonMoveToTrash())).Clicked() {
			a.askTrashSkill(m, sk)
		}
	case sk.Source != "":
		// 查到有新版本时换成显眼的按钮
		var update ui.Element
		if a.skills.updates.HasUpdate(sk) {
			update = accentButton(c.Key("skill-update"), m.SkillsUpdate())
		} else {
			update = iconButton(c.Key("skill-update"), iconFetch, m.SkillsUpdate())
		}
		if pressable(update).Clicked() {
			a.askSkillCommand(m, m.SkillsUpdateConfirm(sk.Name), m.SkillsUpdateBody(sk.Source), skills.ShellLine(skills.UpdateArgs([]string{sk.Name}, global)), w, false)
		}
		if pressable(iconButton(c.Key("skill-remove"), iconTrash, m.CommonDelete())).Clicked() {
			a.askSkillRemove(m, sk, w, global)
		}
	case sk.Scope == skills.ScopeUser || sk.Scope == skills.ScopeProject:
		if pressable(iconButton(c.Key("skill-trash"), iconTrash, m.CommonMoveToTrash())).Clicked() {
			a.askTrashSkill(m, sk)
		}
	}
}

// askSkillRemove 确认后用 npx skills 删掉它装的技能，连同各家的链接。
func (a *app) askSkillRemove(m locales.Messages, sk skills.Skill, w *workspace, global bool) {
	var list []string
	for _, p := range sk.Paths {
		list = append(list, shortPath(p))
	}
	a.askSkillCommand(m, m.SkillsDeleteConfirm(sk.Name), m.SkillsDeleteBody(strings.Join(list, "\n")), skills.ShellLine(skills.RemoveArgs(sk.Name, global)), w, false)
}

// askTrashSkill 确认后把指向技能的链接和技能目录移到废纸篓（先移链接：中途失败也不留下断链）。
func (a *app) askTrashSkill(m locales.Messages, sk skills.Skill) {
	var paths []string
	for _, p := range sk.Paths {
		if p != sk.Dir {
			paths = append(paths, p)
		}
	}
	paths = append(paths, sk.Dir)
	var list []string
	for _, p := range paths {
		list = append(list, shortPath(p))
	}
	a.confirm(m.SkillsTrashConfirm(sk.Name), m.SkillsTrashBody(strings.Join(list, "\n")), m.CommonMoveToTrash(), func() {
		a.trashPaths(m, paths)
	})
}

// askTrashLinks 确认后把体检找到的断链、多余的链接移到废纸篓（只是链接，它指向的目录不动）。
func (a *app) askTrashLinks(m locales.Messages, items []skills.Issue) {
	var paths, list []string
	for _, is := range items {
		paths = append(paths, is.Path)
		if len(list) < 8 {
			list = append(list, shortPath(is.Path))
		}
	}
	if len(items) > len(list) {
		list = append(list, m.SkillsMoreLinks(len(items)))
	}
	title := m.SkillsTrashLinkConfirm()
	if len(items) > 1 {
		title = m.SkillsTrashLinksConfirm(len(items))
	}
	a.confirm(title, m.SkillsTrashLinksBody(strings.Join(list, "\n")), m.CommonMoveToTrash(), func() {
		a.trashPaths(m, paths)
	})
}

// askMergeDuplicate 确认后把没生效的那份移到废纸篓，原处换成指向生效那份的链接：
// 原来读它的那几家（如 Claude 只读自己的目录）照样读得到，读两份的不再冲突。
func (a *app) askMergeDuplicate(m locales.Messages, is skills.Issue) {
	body := m.SkillsMergeSameBody(shortPath(is.Path), shortPath(is.Other))
	if !is.Same {
		body = m.SkillsMergeDiffBody(shortPath(is.Path), shortPath(is.Other))
	}
	a.confirm(m.SkillsMergeConfirm(is.Name), body, m.SkillsMergeButton(), func() {
		err := mergeDuplicate(is.Path, is.Other)
		a.update(func() {
			if err != nil {
				a.err = m.SkillsMergeError(is.Name, errtext.Text(err))
			}
			if a.skills.visible {
				a.scanSkills()
			}
			a.skillsTab.poll.at = time.Time{}
		})
	})
}

// mergeDuplicate 把 path 移到废纸篓，在原处建指向 winner 真实目录的相对链接（和 npx skills 建的链接一样）。
func mergeDuplicate(path, winner string) error {
	if _, err := filepath.EvalSymlinks(winner); err != nil {
		return err
	}
	if err := mygo.Shell.TrashItem(path); err != nil {
		return err
	}
	return skills.MakeLink(path, winner)
}

// askLinkSkill 确认后在 ag 读的目录里建一条指向 sk 的链接，让读不到它的这家读到。
func (a *app) askLinkSkill(m locales.Messages, sk skills.Skill, ag skills.Agent, link string) {
	name := agentTitle(ag.String())
	body := m.SkillsLinkBody(name, shortPath(link), shortPath(sk.Dir))
	a.confirm(m.SkillsLinkConfirm(name, sk.Name), body, m.SkillsLinkButton(), func() {
		err := skills.MakeLink(link, sk.Dir)
		a.update(func() {
			if err != nil {
				a.err = m.SkillsLinkError(errtext.Text(err))
			}
			if a.skills.visible {
				a.scanSkills()
			}
			a.skillsTab.poll.at = time.Time{}
		})
	})
}

func (a *app) trashPaths(m locales.Messages, paths []string) {
	for _, p := range paths {
		if err := mygo.Shell.TrashItem(p); err != nil {
			a.update(func() { a.err = m.TrashMoveError(errtext.Text(err)) })
			break
		}
	}
	a.update(func() {
		if a.skills.visible {
			a.scanSkills()
		}
		a.skillsTab.poll.at = time.Time{}
	})
}

// skillInstallView 是安装对话框：填好来源先列出里面的技能（npx skills add --list）再勾选，列不出来时退回手填名字。
type skillInstallView struct {
	open   bool
	ws     string
	global bool
	source string
	names  string // 列不出来时手填的名字
	claude bool

	listing   bool
	listReq   int
	listSrc   string // 列出下面这些时的来源，来源改了就作废
	listed    []skills.Listed
	picked    []bool
	installed []bool
	listErr   string
	noNPX     bool // 读不了是因为没装 Node.js：提示去装，手填名字也装不了
}

func (a *app) openSkillInstall(m locales.Messages, w *workspace, global bool) {
	if w == nil {
		a.err = m.SkillsNeedProject()
		return
	}
	// listReq 接着数：上一次没读完的结果回来时对不上号
	a.skillInstall = skillInstallView{open: true, ws: w.ID, global: global, claude: true, listReq: a.skillInstall.listReq}
}

func (v *skillInstallView) current() bool {
	return v.listSrc != "" && v.listSrc == strings.TrimSpace(v.source)
}
func (v *skillInstallView) listedNow() bool { return v.current() && v.listed != nil }
func (v *skillInstallView) failedNow() bool { return v.current() && v.listErr != "" }

func (v *skillInstallView) install() skills.Install {
	in := skills.Install{Source: v.source, Claude: v.claude, Global: v.global}
	if !v.listedNow() {
		in.Skills = strings.Fields(v.names)
	} else if slices.Contains(v.picked, false) {
		for i, l := range v.listed {
			if v.picked[i] {
				in.Skills = append(in.Skills, l.Name)
			}
		}
	}
	return in
}

// listInstallSource 在后台列出来源里的技能，并看哪些已经装在要装去的 .agents/skills 里。
func (a *app) listInstallSource(w *workspace) {
	v := &a.skillInstall
	src := strings.TrimSpace(v.source)
	v.listReq++
	req := v.listReq
	v.listing, v.listSrc, v.listed, v.picked, v.installed, v.listErr, v.noNPX = true, src, nil, nil, nil, "", false
	root := w.Path
	if v.global {
		root = homeDir
	}
	go func() {
		list, err := skills.ListSource(context.Background(), userShell(), w.Path, src)
		installed := make([]bool, len(list))
		for i, l := range list {
			_, serr := os.Stat(filepath.Join(root, ".agents", "skills", l.Name))
			installed[i] = serr == nil
		}
		a.update(func() {
			if v.listReq != req {
				return
			}
			v.listing = false
			if err != nil {
				v.listErr, v.noNPX = errtext.Text(err), errors.Is(err, skills.ErrNoNPX)
				return
			}
			v.listed, v.installed = list, installed
			v.picked = make([]bool, len(list))
			for i := range v.picked {
				v.picked[i] = true
			}
		})
	}()
}

func userShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/zsh"
}

// skillInstallDialog 收来源、要装的技能和给不给 Claude 用，下面实时显示要跑的命令。
func (a *app) skillInstallDialog(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	v := &a.skillInstall
	ui.Modal(c, &v.open, func() {
		w := a.workspace(v.ws)
		ui.Column(c).Width(480).Gap(12).Children(func() {
			title := m.SkillsInstallTitle()
			where := m.SkillsInstallGlobalNote()
			if !v.global && w != nil {
				title = m.SkillsInstallProjectTitle()
				where = m.SkillsInstallProjectNote(w.Label)
			}
			titleText(c, title, fsTitle)
			ui.Text(c, where).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			sourceSubmitted := fieldInput(c.Key("source"), &v.source, m.SkillsSourcePlaceholder()).AutoFocus().Submitted()
			submitted := false
			canList := strings.TrimSpace(v.source) != "" && !v.listing && w != nil && v.install().Check() == nil
			switch {
			case v.listing:
				ui.Text(c, m.SkillsListing()).FontSize(fsSmall).TextColor(t.TextMuted)
			case v.listedNow():
				a.skillInstallPicker(c)
			case v.failedNow() && v.noNPX:
				text := m.SkillsNoNPXDownload()
				if skills.FindBrew() != "" {
					text = m.SkillsNoNPXBrew()
				}
				ui.Text(c, text).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45)
			case v.failedNow():
				ui.Text(c, m.SkillsListError(v.listErr)).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45).Selectable()
				if fieldInput(c.Key("names"), &v.names, m.SkillsNamesPlaceholder()).Submitted() {
					submitted = true
				}
			default:
				ui.Text(c, m.SkillsSourceHint()).FontSize(fsSmall).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Switch(c, &v.claude).Label(m.SkillsAlsoClaude())
				ui.Text(c, m.SkillsAlsoClaude()).FontSize(fsBody)
			})
			ui.Text(c, m.SkillsClaudeNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			// 列出来了按勾选装；列不出来也能照手填的名字装，npx 在终端里会报出具体原因
			ready := (v.listedNow() && slices.Contains(v.picked, true)) || (v.failedNow() && !v.noNPX)
			if err := v.install().Check(); ready && err != nil {
				ready = false
				ui.Text(c, errtext.Text(err)).FontSize(fsSmall).TextColor(t.Danger)
			}
			if ready {
				ui.Text(c, skills.ShellLine(v.install().Args())).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Padding(8, 12).Radius(rCard).Background(colors(t).code).Selectable()
			}
			ui.Text(c, m.SkillsScriptWarning()).FontSize(fsSmall).TextColor(t.TextMuted)
			list, installNode := false, false
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					v.open = false
				}
				if v.failedNow() && pressable(textButton(c, m.SkillsReload())).Clicked() {
					list = true
				}
				// 主按钮做的事（action）和文字分开定：按文字判断，换了语言就认不出了
				const (
					doInstall = iota
					doList
					doInstallNode
				)
				label, action := m.SkillsInstall(), doInstall
				enabled := ready
				switch {
				case v.failedNow() && v.noNPX:
					label, action, enabled = m.SkillsOpenNodejs(), doInstallNode, true
					if skills.FindBrew() != "" {
						label = m.SkillsInstallNode()
					}
				case !v.listedNow() && !v.failedNow():
					label, action, enabled = m.SkillsList(), doList, canList
				}
				run := ui.Box(c).Shrink(0)
				if !enabled {
					run.Disabled(true).Opacity(0.5)
				}
				run.Children(func() {
					if pressable(accentButton(c, label)).Clicked() {
						switch action {
						case doList:
							list = true
						case doInstallNode:
							installNode = true
						default:
							submitted = true
						}
					}
				})
			})
			if installNode {
				if brew := skills.FindBrew(); brew != "" && w != nil {
					// 两个对话框不叠着：装好后再点「安装…」
					v.open = false
					a.askSkillCommand(m, m.SkillsInstallNode(), m.SkillsInstallNodeBody(), installNodeLine(brew), w, true)
				} else {
					c.OpenURL("https://nodejs.org/")
				}
			}
			if sourceSubmitted {
				if v.listedNow() || (v.failedNow() && !v.noNPX) {
					submitted = true
				} else {
					list = true
				}
			}
			if list && canList {
				a.listInstallSource(w)
			} else if submitted && ready && w != nil {
				v.open = false
				a.askSkillCommand(m, m.SkillsInstallTitle(), "", skills.ShellLine(v.install().Args()), w, true)
			}
		})
	})
}

// skillInstallPicker 是来源里的技能：只有一个时直接列出，多个时逐个勾选，默认全选，按来源给的组分开。
func (a *app) skillInstallPicker(c *ui.Context) {
	t := c.Theme()
	v := &a.skillInstall
	item := func(i int, l skills.Listed, check bool) {
		ui.Column(c.Key("skill:" + l.Name)).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if check {
					ui.Checkbox(c, &v.picked[i], l.Name)
				} else {
					ui.Text(c, l.Name).FontSize(fsBody).Font(monoFont)
				}
				if v.installed[i] {
					ui.Text(c, L(c).SkillsInstalled()).FontSize(fsSmall).TextColor(colors(t).faint)
				}
			})
			if l.Description != "" {
				d := ui.Text(c, l.Description).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45).MaxLines(2)
				if check {
					// 和勾选框的文字对齐：mygo 的勾选框是 Space(4) 的框加 Space(2) 的间距
					d.Padding(0, 0, 0, t.Space(6))
				}
			}
		})
	}
	if len(v.listed) == 1 {
		ui.Column(c).Padding(8, 12).Radius(rControl).Background(colors(t).fill).Children(func() {
			item(0, v.listed[0], false)
		})
		return
	}
	ui.Scroll(c).MaxHeight(280).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.CheckboxGroup(c, L(c).SkillsPickAll(len(v.listed)), func() {
				group := ""
				for i, l := range v.listed {
					if l.Group != group {
						group = l.Group
						ui.Text(c, group).FontSize(fsSmall).TextColor(colors(t).faint).Padding(6, 0, 0, 0)
					}
					item(i, l, true)
				}
			})
		})
	})
}

// --- 新建项目技能

// skillNewView 是新建项目技能的对话框。
type skillNewView struct {
	open bool
	ws   string
	name string
}

// skillNameRe 是技能名的写法（各家一致）：小写字母、数字和连字符，首尾是字母或数字。
var skillNameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$`)

// createProjectSkill 在工作区的 .agents/skills 里建技能（Codex、Grok、Cursor 读这里），
// 再在 .claude/skills 里建一条相对链接给 Claude（和 npx skills 的做法一样），返回 SKILL.md 的路径。
// SKILL.md 的模板按界面语言 m 写。
func createProjectSkill(m locales.Messages, root, name string) (string, error) {
	dir := filepath.Join(root, ".agents", "skills", name)
	file := filepath.Join(dir, "SKILL.md")
	if _, err := os.Lstat(dir); err == nil {
		return "", existsError(dir)
	}
	link := filepath.Join(root, ".claude", "skills", name)
	if _, err := os.Lstat(link); err == nil {
		return "", existsError(link)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body := "---\nname: " + name + "\ndescription: " + m.SkillsNewTemplateDescription() + "\n---\n\n# " + name + "\n\n" + m.SkillsNewTemplateSteps() + "\n\n1. \n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", err
	}
	if err := os.Symlink(filepath.Join("..", "..", ".agents", "skills", name), link); err != nil {
		return "", err
	}
	return file, nil
}

// existsError 是新建技能时目录或链接已经有了：界面按语言写这句，不经 errtext。
type existsError string

func (e existsError) Error() string { return shortPath(string(e)) + " already exists" }

func (a *app) skillNewDialog(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	v := &a.skillNew
	ui.Modal(c, &v.open, func() {
		w := a.workspace(v.ws)
		ui.Column(c).Width(420).Gap(12).Children(func() {
			titleText(c, m.SkillsNewTitle(), fsTitle)
			if w != nil {
				ui.Text(c, m.SkillsNewNote(w.Label)).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			}
			submitted := fieldInput(c, &v.name, m.SkillsNewPlaceholder()).AutoFocus().Submitted()
			name := strings.TrimSpace(v.name)
			valid := skillNameRe.MatchString(name)
			if name != "" && !valid {
				ui.Text(c, m.SkillsNewNameRule()).FontSize(fsSmall).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					v.open = false
				}
				create := ui.Box(c).Shrink(0)
				if !valid {
					create.Disabled(true).Opacity(0.5)
				}
				create.Children(func() {
					if pressable(accentButton(c, m.CommonNew())).Clicked() {
						submitted = true
					}
				})
			})
			if submitted && valid && w != nil {
				file, err := createProjectSkill(m, w.Path, name)
				if err != nil {
					reason := errtext.Text(err)
					var ex existsError
					if errors.As(err, &ex) {
						reason = m.SkillsNewExists(shortPath(string(ex)))
					}
					a.err = m.SkillsNewError(reason)
					return
				}
				v.open = false
				a.skillsTab.poll.at = time.Time{}
				a.openFile(w, file, 0, [2]int{})
			}
		})
	})
}

// --- 跨会话的用量

// skillUseLine 是列表行最下面的用量：用过几次、最后一次多久以前；没用过的写出来，好清理。
func skillUseLine(c *ui.Context, st *history.SkillStat) {
	t := c.Theme()
	m := L(c)
	if st == nil || st.Count == 0 {
		ui.Text(c, m.SkillsNeverUsed()).FontSize(fsSmall).TextColor(colors(t).faint)
		return
	}
	ui.Text(c, m.SkillsUsedTimes(st.Count)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
	ui.Text(c, ago(m, st.Last)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(m.SkillsLastUsed(m.AppDateTime(st.Last.Local())))
}

// skillTriggerText 是技能怎么被调起来：SKILL.md 头部的 disable-model-invocation、user-invocable。
func skillTriggerText(l locales.Messages, m skills.Meta) (text, tip string) {
	switch {
	case m.ModelOff:
		return l.SkillsTriggerManual(), l.SkillsTriggerManualTip()
	case m.UserHidden:
		return l.SkillsTriggerAutoOnly(), l.SkillsTriggerAutoOnlyTip()
	}
	return l.SkillsTriggerAuto(), l.SkillsTriggerAutoTip()
}

// skillTrigger 是触发方式的小标签：手动的用强调色，和默认的自动区分开。
func skillTrigger(c *ui.Context, m skills.Meta) {
	t := c.Theme()
	text, tip := skillTriggerText(L(c), m)
	color := t.TextMuted
	if m.ModelOff {
		color = colors(t).honey
	}
	ui.Text(c, text).FontSize(fsSmall).TextColor(color).Tooltip(tip)
}

// skillUseCard 是详情里的用量：次数、会话数、最后一次，和各家各用了几次。
func skillUseCard(c *ui.Context, st *history.SkillStat) {
	t := c.Theme()
	m := L(c)
	card(c).Padding(cardPad).Gap(8).Shrink(0).Children(func() {
		cardTitle(c, m.SkillsUsageTitle())
		if st == nil || st.Count == 0 {
			ui.Text(c, m.SkillsNeverUsedNote()).FontSize(fsBody).TextColor(t.TextMuted)
			return
		}
		metaRow(c, meta(c, m.SkillsUsedTimes(st.Count)), meta(c, m.SkillsSessions(st.Sessions)),
			meta(c, m.SkillsLastUsed(m.AppDateTime(st.Last.Local()))))
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range skills.Agents {
				n := st.ByAgent[ag.String()]
				if n == 0 {
					continue
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentIcon(c, ag.String(), smallIcon())
					ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}).Tooltip(m.SkillsAgentUsedTimes(agentTitle(ag.String()), n))
			}
		})
	})
}
