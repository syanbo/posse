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
	at      time.Time
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
			s.cat, s.loaded, s.loading, s.at = cat, true, false, time.Now()
		})
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
	item := a.navTile(c, skillsBoardID, iconSkill, "技能", nil)
	if a.revealSel && a.selected == skillsBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
	return item
}

// skillAgentLabels 是技能页按 agent 筛选的分段。
var skillAgentLabels = []string{"全部", "Claude", "Codex", "Grok", "Cursor"}

// scopeLabels 是技能来源的写法和列表里分组的顺序。
var scopeLabels = []struct {
	scope skills.Scope
	label string
}{
	{skills.ScopeUser, "用户"},
	{skills.ScopeSynced, "账号同步"},
	{skills.ScopePlugin, "插件"},
	{skills.ScopeSystem, "系统自带"},
	{skills.ScopeProject, "项目"},
}

func scopeLabel(sc skills.Scope) string {
	for _, s := range scopeLabels {
		if s.scope == sc {
			return s.label
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
			label string
		}) bool {
			return x.scope == sc
		})
	}
	slices.SortStableFunc(out, func(x, y skills.Skill) int {
		if d := order(x.Scope) - order(y.Scope); d != 0 {
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
		if iconButton(c.Key("skills-refresh"), iconRefresh, "重新扫描").Clicked() {
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
	a.band(c, func() {
		// 分页兼作标题：侧栏里叫「技能」，这里分技能和 MCP
		segmented(c, &s.page, "技能或 MCP", "技能", "MCP")
		placeholder := "搜索名字和说明"
		if s.page == 1 {
			placeholder = "搜索名字和命令"
		}
		fieldInputSized(c, &s.query, placeholder, 280, 160).Label("搜索")
		ui.Spacer(c)
		segmented(c, &s.agent, "按 agent 筛选", skillAgentLabels...)
		if s.page == 1 {
			a.refreshButton(c)
			a.closeBoardButton(c)
			return
		}
		if pressable(textButton(c, "安装…")).Clicked() {
			a.openSkillInstall(a.skillsWorkspace(), true)
		}
		check := ui.Box(c).Shrink(0)
		label := "检查更新"
		if s.checking {
			label = "正在检查…"
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
		card(c).Width(380).Shrink(0).Clip().Focusable().FocusRing(false).Label("技能列表").Children(func() {
			a.skillTotals(c)
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(4, 8, 16, 8).Gap(2).Children(func() {
					if !s.loaded {
						usageNote(c, "正在扫描…")
						return
					}
					if len(list) == 0 {
						usageNote(c, "没有符合条件的技能。")
					}
					var group skills.Scope
					for _, sk := range list {
						if sk.Scope != group {
							group = sk.Scope
							n := 0
							for _, x := range list {
								if x.Scope == group {
									n++
								}
							}
							skillGroupHeader(c, scopeLabel(group), n)
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
	s := &a.skills
	u := s.updates
	if u == nil {
		return
	}
	n := s.updateCount()
	failed := ""
	if len(u.Failed) > 0 {
		failed = fmt.Sprintf("%d 个来源没查到", len(u.Failed))
	}
	var gone []string
	for _, sk := range s.cat.Skills {
		if u.IsGone(sk) {
			gone = append(gone, sk.Name+"：来源里找不到了，npx skills update 跳过它")
		}
	}
	if len(gone) > 0 {
		if failed != "" {
			failed += "，"
		}
		failed += fmt.Sprintf("%d 个在来源里找不到了", len(gone))
	}
	var lines []string
	for _, f := range u.Failed {
		lines = append(lines, f.Source+"："+errtext.Message(f.Err))
	}
	why := strings.Join(append(lines, gone...), "\n")
	if n == 0 {
		text := "都是最新的"
		if failed != "" {
			text += "，" + failed
		}
		e := ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted).Margin(0, 20, 8, 20)
		if failed != "" {
			e.Tooltip(why)
		}
		return
	}
	row := ui.Row(c.Key("skill-updates")).Margin(0, 8, 4, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Center).Role(ui.RoleButton).Label("有新版本的技能")
	selectedBg(c, row, s.onlyUpdates)
	row.Children(func() {
		ui.Icon(c, iconFetch).FontSize(iconSize).TextColor(t.Accent).Shrink(0)
		ui.Text(c, fmt.Sprintf("%d 个有新版本", n)).FontSize(fsBody).FontFeatures("tnum").Grow(1)
		if failed != "" {
			ui.Text(c, failed).FontSize(fsSmall).TextColor(t.TextMuted).Tooltip(why)
		}
	})
	if s.onlyUpdates {
		row.Tooltip("再点一下看全部")
	} else {
		row.Tooltip("只看有新版本的")
	}
	if row.Clicked() {
		s.onlyUpdates = !s.onlyUpdates
	}
}

// skillTotals 是列表顶上的总计：技能数、各家用得上的数，和体检发现的问题（点开看）。
func (a *app) skillTotals(c *ui.Context) {
	t := c.Theme()
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
			ui.Text(c, fmt.Sprintf("%d 个技能", n)).FontSize(fsBody).FontWeight(fwMedium).FontFeatures("tnum").Grow(1)
			if s.statsLoading && s.stats == nil {
				note := "正在统计用量…"
				if s.statsTotal > 0 {
					note = fmt.Sprintf("正在统计用量 %d/%d", s.statsDone, s.statsTotal)
				}
				ui.Text(c, note).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			} else {
				segmented(c, &s.byUse, "排序", "名字", "用量")
			}
		})
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range skills.Agents {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentIcon(c, ag.String(), smallIcon())
					ui.Text(c, fmt.Sprint(byAgent[ag])).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}).Tooltip(agentTitle(ag.String()) + "用得上的技能")
			}
		})
	})
	a.skillUpdatesLine(c)
	if k := len(s.cat.Issues); k > 0 {
		row := ui.Row(c.Key("skill-issues")).Margin(0, 8, 4, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Center).Role(ui.RoleButton).Label("体检")
		selectedBg(c, row, s.sel == skillIssuesKey)
		row.Children(func() {
			ui.Icon(c, iconInfo).FontSize(iconSize).TextColor(colors(t).honey).Shrink(0)
			ui.Text(c, fmt.Sprintf("体检发现 %d 个问题", k)).FontSize(fsBody).FontFeatures("tnum").Grow(1)
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
					ui.Text(c, "有新版本").FontSize(fsSmall).TextColor(t.Accent)
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
					tip += "\n点一下打开"
				} else {
					tip += "\n点一下关掉"
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

func skillStateText(st skills.State) string {
	switch st {
	case skills.On:
		return "启用"
	case skills.Manual:
		return "只能手动调用"
	case skills.Off:
		return "已关闭"
	case skills.Shadowed:
		return "被同名的盖住"
	}
	return "读不到"
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
	tip := agentTitle(ag.String()) + "：" + skillStateText(st.State)
	if why := skillWhy(m, st); why != "" {
		tip += "\n" + why
	}
	if st.Inferred && st.State != skills.Unseen {
		tip += "\n读取规则是推断的，没有核实"
	}
	return tip
}

// skillDetail 是右边的详情：名字、来源和位置，各家的状态，完整的说明。
func (a *app) skillDetail(c *ui.Context, sk skills.Skill) {
	t := c.Theme()
	card(c).Padding(cardPad).Gap(12).Shrink(0).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
			tile(c, iconSkill, tileLarge(), colors(t).tint, t.Accent)
			ui.Column(c).Gap(8).Grow(1).Shrink(1).Children(func() {
				titleText(c, sk.Name, fsPage).LineHeight(1.3).MaxLines(2).Selectable()
				var source func()
				if sk.Source != "" {
					source = metaMono(c, nil, sk.Source, "npx skills 装的来源")
				}
				metaRow(c, meta(c, scopeLabel(sk.Scope)), source, metaMono(c, iconFolder, shortPath(sk.Dir), sk.Dir))
			})
			ui.Row(c).Gap(4).Shrink(0).Children(func() {
				if pressable(textButton(c, "打开 SKILL.md")).Clicked() {
					a.openSkillFile(sk.File)
				}
				a.skillActions(c, sk, a.skillsWorkspace(), true)
				if sk.SourceURL != "" && pressable(iconButton(c.Key("skill-source"), iconOpen, "打开来源网页")).Clicked() {
					c.OpenURL(sk.SourceURL)
				}
				if pressable(iconButton(c.Key("skill-reveal"), iconFolder, "在访达中显示")).Clicked() {
					mygo.Shell.ShowItemInFolder(sk.File)
				}
			})
		})
		if a.skills.updates.IsGone(sk) {
			ui.Text(c, "来源里已经找不到它了（上游删了、改了名或挪了位置），npx skills update 更新不了。到来源网页看看它换成了什么，装新的再删掉这个。").
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
			cardTitle(c, "各家状态")
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
				ui.Text(c, skillStateText(st.State)).FontSize(fsSmall).TextColor(color)
				if st.Inferred && st.State != skills.Unseen {
					ui.Text(c, "推断").FontSize(fsSmall).TextColor(colors(t).faint).Tooltip("Cursor 读哪些目录没有核实过，按它程序里出现的目录名推断")
				}
			})
			if why := skillWhy(L(c), st); why != "" {
				ui.Text(c, why).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			}
			if st.Via != "" {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, shortPath(st.Via)).FontSize(fsSmall).Font(monoFont).TextColor(colors(t).faint).MaxLines(2).Shrink(1)
					// Codex 报的是 SKILL.md 的路径
					// 只看这条路径本身是不是链接：上级目录是链接（如 ~/.claude 链到 dotfiles）时它照样不是软链
					if fi, err := os.Lstat(strings.TrimSuffix(st.Via, string(filepath.Separator)+"SKILL.md")); err == nil && fi.Mode()&os.ModeSymlink != 0 {
						ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Tooltip("软链，指向 " + shortPath(sk.Dir)).Children(func() {
							ui.Icon(c, iconFolderLink).FontSize(fsSmall).TextColor(colors(t).faint)
							ui.Text(c, "软链").FontSize(fsSmall).TextColor(colors(t).faint)
						})
					}
				})
			}
		})
		if skills.CanToggle(ag, sk) {
			on := st.State != skills.Off
			if ui.Switch(c, &on).Label(agentTitle(ag.String()) + "里启用").Changed() {
				a.planSkillToggle(o, sk, ag, on)
			}
		} else if link, ok := skills.LinkFor(o, sk, ag); ok {
			// 读不到的：打开就在这家读的目录里建一条链接
			on := false
			if ui.Switch(c, &on).Label("让" + agentTitle(ag.String()) + "读到它").Tooltip("在 " + shortPath(filepath.Dir(link)) + " 建一条指向它的链接").Changed() {
				a.askLinkSkill(sk, ag, link)
			}
		}
	})
}

// issueKinds 是体检里各类问题的顺序、标题和说明。
var issueKinds = []struct {
	kind         skills.IssueKind
	title, about string
}{
	{skills.BrokenLink, "断掉的链接", "链接指向的目录已经不在了，各家都读不到。"},
	{skills.Duplicate, "同名冲突", "同一家读得到两份同名的技能，只有优先的那份生效。可以合成一份：没生效的那份移到废纸篓，原处换成指向生效那份的链接，各家都读同一份。"},
	{skills.LockMissing, "锁文件对不上", "npx skills 的锁文件记着它，各家的技能目录里却都没有（多半是手动删掉的）。不要了就从锁文件里删掉；还要就重新安装。"},
	{skills.RedundantLink, "多余的链接", "链接所在目录的那几家本来就从别处读得到它指向的那份，删掉不影响。"},
}

// skillIssues 是体检结果，按类分组。
func (a *app) skillIssues(c *ui.Context) {
	t := c.Theme()
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
						cardTitle(c, k.title)
						ui.Text(c, fmt.Sprint(len(items))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
					})
					ui.Text(c, k.about).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
					if k.kind == skills.RedundantLink && len(items) > 1 {
						if pressable(textButton(c, fmt.Sprintf("全部移到废纸篓（%d 条）", len(items)))).Clicked() {
							a.askTrashLinks(items)
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
	ui.Column(c.Key(string(is.Kind)+is.Path+is.Other)).Padding(6, 0).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, is.Name).FontSize(fsBody).FontWeight(fwMedium).Shrink(0)
			for _, ag := range is.Agents {
				agentTile(c, ag.String(), smallIcon())
			}
			ui.Spacer(c)
			switch is.Kind {
			case skills.BrokenLink, skills.RedundantLink:
				if pressable(iconButtonSized(c, iconTrash, "把这条链接移到废纸篓", compactBox(), smallIcon())).Clicked() {
					a.askTrashLinks([]skills.Issue{is})
				}
			case skills.Duplicate:
				if pressable(iconButtonSized(c.Key("dup-reveal"), iconFolder, "在访达中显示没生效的那份", compactBox(), smallIcon())).Clicked() {
					mygo.Shell.ShowItemInFolder(is.Path)
				}
				if pressable(iconButtonSized(c.Key("dup-merge"), iconFolderLink, "合成一份：换成指向生效那份的链接", compactBox(), smallIcon())).Clicked() {
					a.askMergeDuplicate(is)
				}
			case skills.LockMissing:
				if pressable(iconButtonSized(c, iconTrash, "从锁文件里删掉", compactBox(), smallIcon())).Clicked() {
					a.askSkillCommand("从锁文件里删掉 "+is.Name+"？", "各家的技能目录里都没有它了，npx skills 只会删掉锁文件里的这一项。", skills.ShellLine(skills.RemoveArgs(is.Name, true)), a.skillsWorkspace(), false)
				}
			}
		})
		var line string
		switch is.Kind {
		case skills.BrokenLink:
			line = shortPath(is.Path) + " → " + shortPath(is.Other)
		case skills.Duplicate:
			line = shortPath(is.Path) + " 被 " + shortPath(is.Other) + " 盖住"
			if is.Same {
				line += "，内容相同"
			} else {
				line += "，内容不同"
			}
		case skills.LockMissing:
			line = "目录里没有 " + shortPath(is.Other)
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
		if pressable(textButton(c, "新建…")).Clicked() {
			a.skillNew = skillNewView{open: true, ws: w.ID}
		}
		if pressable(textButton(c, "安装…")).Clicked() {
			a.openSkillInstall(w, false)
		}
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(8, 8, 16, 8).Gap(2).Children(func() {
			switch {
			case p.poll.at.IsZero():
				usageNote(c, "正在扫描…")
			case len(list) == 0:
				usageNote(c, "这个工作区没有项目级技能。放在 .claude/skills、.agents/skills、.grok/skills 或 .cursor/skills 里的技能会列在这里。")
			}
			for _, sk := range list {
				row := ui.Column(c.Key(sk.Dir)).Padding(8, 8).Gap(2).Radius(rControl).Role(ui.RoleButton).Label(sk.Name)
				selectedBg(c, row, false)
				row.Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, sk.Name).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Grow(1).Shrink(1)
						skillMarks(c, sk, func(ag skills.Agent, enable bool) {
							a.planSkillToggle(skills.Options{Home: homeDir, Dir: w.Path}, sk, ag, enable)
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
func (a *app) planSkillToggle(o skills.Options, sk skills.Skill, ag skills.Agent, enable bool) {
	ch, err := skills.PlanToggle(o, sk, ag, enable)
	if err != nil {
		a.err = "没有改" + agentTitle(ag.String()) + "的配置：" + errtext.Text(err)
		return
	}
	a.skillChange = skillChangeView{open: true, ch: ch}
}

// applySkillChange 写入确认过的改动，写完重扫技能页和文件栏的技能页签。
func (a *app) applySkillChange(ch skills.Change) {
	go func() {
		err := ch.Apply()
		a.update(func() {
			if err != nil {
				a.err = "没有改" + shortPath(ch.File) + "：" + errtext.Text(err)
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
	p := colors(t)
	v := &a.skillChange
	ui.Modal(c, &v.open, func() {
		ch := v.ch
		verb := "关掉"
		if ch.Enable {
			verb = "打开"
		}
		ui.Column(c).Width(520).Gap(12).Children(func() {
			titleText(c, fmt.Sprintf("在 %s 里%s %s", agentTitle(ch.Agent.String()), verb, ch.Skill), fsTitle)
			ui.Column(c).Gap(4).Children(func() {
				what := "会修改这个文件："
				if ch.Before == "" {
					what = "会新建这个文件："
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
			ui.Text(c, "已经开着的会话可能要重开才会生效。").FontSize(fsSmall).TextColor(t.TextMuted)
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					v.open = false
				}
				if pressable(accentButton(c, "确认修改")).Clicked() {
					v.open = false
					a.applySkillChange(ch)
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
	switch {
	case sk.Source != "":
		// 查到有新版本时换成显眼的按钮
		var update ui.Element
		if a.skills.updates.HasUpdate(sk) {
			update = accentButton(c.Key("skill-update"), "更新")
		} else {
			update = iconButton(c.Key("skill-update"), iconFetch, "更新")
		}
		if pressable(update).Clicked() {
			a.askSkillCommand("更新 "+sk.Name+"？", "按锁文件里记的来源（"+sk.Source+"）拉最新的版本，覆盖本地的改动。", skills.ShellLine(skills.UpdateArgs([]string{sk.Name}, global)), w, false)
		}
		if pressable(iconButton(c.Key("skill-remove"), iconTrash, "删除")).Clicked() {
			var list []string
			for _, p := range sk.Paths {
				list = append(list, shortPath(p))
			}
			a.askSkillCommand("删除 "+sk.Name+"？", "npx skills 会直接删掉这些（各家目录里同名的都删），不经过废纸篓：\n"+strings.Join(list, "\n"), skills.ShellLine(skills.RemoveArgs(sk.Name, global)), w, false)
		}
	case sk.Scope == skills.ScopeUser || sk.Scope == skills.ScopeProject:
		if pressable(iconButton(c.Key("skill-trash"), iconTrash, "移到废纸篓")).Clicked() {
			a.askTrashSkill(sk)
		}
	}
}

// askTrashSkill 确认后把指向技能的链接和技能目录移到废纸篓（先移链接：中途失败也不留下断链）。
func (a *app) askTrashSkill(sk skills.Skill) {
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
	a.confirm("把 "+sk.Name+" 移到废纸篓？", "这些会移到废纸篓，可以从废纸篓找回：\n"+strings.Join(list, "\n"), "移到废纸篓", func() {
		a.trashPaths(paths)
	})
}

// askTrashLinks 确认后把体检找到的断链、多余的链接移到废纸篓（只是链接，它指向的目录不动）。
func (a *app) askTrashLinks(items []skills.Issue) {
	var paths, list []string
	for _, is := range items {
		paths = append(paths, is.Path)
		if len(list) < 8 {
			list = append(list, shortPath(is.Path))
		}
	}
	if len(items) > len(list) {
		list = append(list, fmt.Sprintf("……共 %d 条", len(items)))
	}
	title := "把这条链接移到废纸篓？"
	if len(items) > 1 {
		title = fmt.Sprintf("把 %d 条链接移到废纸篓？", len(items))
	}
	a.confirm(title, "只移走链接本身，它指向的目录不动，可以从废纸篓找回：\n"+strings.Join(list, "\n"), "移到废纸篓", func() {
		a.trashPaths(paths)
	})
}

// askMergeDuplicate 确认后把没生效的那份移到废纸篓，原处换成指向生效那份的链接：
// 原来读它的那几家（如 Claude 只读自己的目录）照样读得到，读两份的不再冲突。
func (a *app) askMergeDuplicate(is skills.Issue) {
	body := "内容和生效的那份相同，合成后各家读到的不变。"
	if !is.Same {
		body = "两份内容不同：合成后原来读它的那几家也改用生效的那份，它的内容可以从废纸篓找回。"
	}
	body += "\n\n移到废纸篓：" + shortPath(is.Path) + "\n换成链接，指向：" + shortPath(is.Other)
	a.confirm("把 "+is.Name+" 合成一份？", body, "合成一份", func() {
		err := mergeDuplicate(is.Path, is.Other)
		a.update(func() {
			if err != nil {
				a.err = "没有合成 " + is.Name + "：" + errtext.Text(err)
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
func (a *app) askLinkSkill(sk skills.Skill, ag skills.Agent, link string) {
	name := agentTitle(ag.String())
	body := "在 " + name + " 读的目录里建一条链接指向它，不复制文件：\n" + shortPath(link) + " → " + shortPath(sk.Dir) +
		"\n\n以后要关掉用这里的开关；要撤销，把这条链接移到废纸篓。"
	a.confirm("让 "+name+" 读到 "+sk.Name+"？", body, "建链接", func() {
		err := skills.MakeLink(link, sk.Dir)
		a.update(func() {
			if err != nil {
				a.err = "没有建链接：" + errtext.Text(err)
			}
			if a.skills.visible {
				a.scanSkills()
			}
			a.skillsTab.poll.at = time.Time{}
		})
	})
}

func (a *app) trashPaths(paths []string) {
	for _, p := range paths {
		if err := mygo.Shell.TrashItem(p); err != nil {
			a.update(func() { a.err = "移到废纸篓失败：" + errtext.Text(err) })
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

func (a *app) openSkillInstall(w *workspace, global bool) {
	if w == nil {
		a.err = "先添加一个项目：命令要在工作区的终端里跑。"
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
	v := &a.skillInstall
	ui.Modal(c, &v.open, func() {
		w := a.workspace(v.ws)
		ui.Column(c).Width(480).Gap(12).Children(func() {
			title := "安装技能"
			where := "装到用户目录（~/.agents/skills），所有项目都能用。"
			if !v.global && w != nil {
				title = "安装到项目"
				where = "装进「" + w.Label + "」的 .agents/skills，只在这个项目里用。"
			}
			titleText(c, title, fsTitle)
			ui.Text(c, where).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			sourceSubmitted := fieldInput(c.Key("source"), &v.source, "来源：owner/repo、网址或本地目录").AutoFocus().Submitted()
			submitted := false
			canList := strings.TrimSpace(v.source) != "" && !v.listing && w != nil && v.install().Check() == nil
			switch {
			case v.listing:
				ui.Text(c, "正在读取来源里的技能…").FontSize(fsSmall).TextColor(t.TextMuted)
			case v.listedNow():
				a.skillInstallPicker(c)
			case v.failedNow() && v.noNPX:
				how := "到 nodejs.org 下载安装，装好后点「重新读取」。"
				if skills.FindBrew() != "" {
					how = "点「安装 Node.js」用 Homebrew 装，装好后再来安装技能。"
				}
				ui.Text(c, "没找到 npx：安装技能要用 Node.js 自带的 npx。"+how).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45)
			case v.failedNow():
				ui.Text(c, "读不到技能列表："+v.listErr).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45).Selectable()
				if fieldInput(c.Key("names"), &v.names, "只装其中几个（名字用空格分开，留空装全部）").Submitted() {
					submitted = true
				}
			default:
				ui.Text(c, "填好来源后回车，先列出里面有哪些技能再选。").FontSize(fsSmall).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Switch(c, &v.claude).Label("也给 Claude 用")
				ui.Text(c, "也给 Claude 用").FontSize(fsBody)
			})
			ui.Text(c, "Codex、Grok、Cursor 都读 .agents/skills，装进去就都能用；Claude 只读自己的目录，要另建一条链接。").FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			// 列出来了按勾选装；列不出来也能照手填的名字装，npx 在终端里会报出具体原因
			ready := (v.listedNow() && slices.Contains(v.picked, true)) || (v.failedNow() && !v.noNPX)
			if err := v.install().Check(); ready && err != nil {
				ready = false
				ui.Text(c, errtext.Text(err)).FontSize(fsSmall).TextColor(t.Danger)
			}
			if ready {
				ui.Text(c, skills.ShellLine(v.install().Args())).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Padding(8, 12).Radius(rCard).Background(colors(t).code).Selectable()
			}
			ui.Text(c, "装上的技能带着它的脚本，用之前先看一眼里面会跑什么。").FontSize(fsSmall).TextColor(t.TextMuted)
			list, installNode := false, false
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					v.open = false
				}
				if v.failedNow() && pressable(textButton(c, "重新读取")).Clicked() {
					list = true
				}
				// 主按钮做的事（action）和文字分开定：按文字判断，换了语言就认不出了
				const (
					doInstall = iota
					doList
					doInstallNode
				)
				label, action := "安装", doInstall
				enabled := ready
				switch {
				case v.failedNow() && v.noNPX:
					label, action, enabled = "打开 nodejs.org", doInstallNode, true
					if skills.FindBrew() != "" {
						label = "安装 Node.js"
					}
				case !v.listedNow() && !v.failedNow():
					label, action, enabled = "读取", doList, canList
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
					a.askSkillCommand("安装 Node.js", "用 Homebrew 安装 Node.js，装技能要用它自带的 npx。", installNodeLine(brew), w, true)
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
				a.askSkillCommand("安装技能", "", skills.ShellLine(v.install().Args()), w, true)
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
					ui.Text(c, "已装").FontSize(fsSmall).TextColor(colors(t).faint)
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
			ui.CheckboxGroup(c, fmt.Sprintf("全部 %d 个", len(v.listed)), func() {
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
func createProjectSkill(root, name string) (string, error) {
	dir := filepath.Join(root, ".agents", "skills", name)
	file := filepath.Join(dir, "SKILL.md")
	if _, err := os.Lstat(dir); err == nil {
		return "", fmt.Errorf("%s 已经有了", shortPath(dir))
	}
	link := filepath.Join(root, ".claude", "skills", name)
	if _, err := os.Lstat(link); err == nil {
		return "", fmt.Errorf("%s 已经有了", shortPath(link))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	body := "---\nname: " + name + "\ndescription: 写清楚它做什么、什么时候用（agent 靠这一句决定要不要调用）。\n---\n\n# " + name + "\n\n步骤：\n\n1. \n"
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

func (a *app) skillNewDialog(c *ui.Context) {
	t := c.Theme()
	v := &a.skillNew
	ui.Modal(c, &v.open, func() {
		w := a.workspace(v.ws)
		ui.Column(c).Width(420).Gap(12).Children(func() {
			titleText(c, "新建项目技能", fsTitle)
			if w != nil {
				ui.Text(c, "建在「"+w.Label+"」的 .agents/skills 里，再给 Claude 在 .claude/skills 里建一条链接，四家都能用。").FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			}
			submitted := fieldInput(c, &v.name, "名字，比如 release-check").AutoFocus().Submitted()
			name := strings.TrimSpace(v.name)
			valid := skillNameRe.MatchString(name)
			if name != "" && !valid {
				ui.Text(c, "只用小写字母、数字和连字符，首尾是字母或数字。").FontSize(fsSmall).TextColor(t.Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					v.open = false
				}
				create := ui.Box(c).Shrink(0)
				if !valid {
					create.Disabled(true).Opacity(0.5)
				}
				create.Children(func() {
					if pressable(accentButton(c, "新建")).Clicked() {
						submitted = true
					}
				})
			})
			if submitted && valid && w != nil {
				file, err := createProjectSkill(w.Path, name)
				if err != nil {
					a.err = "没有新建：" + errtext.Text(err)
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
	if st == nil || st.Count == 0 {
		ui.Text(c, "会话里没用过").FontSize(fsSmall).TextColor(colors(t).faint)
		return
	}
	ui.Text(c, fmt.Sprintf("用过 %d 次", st.Count)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
	ui.Text(c, ago(st.Last)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip("最近一次 " + st.Last.Local().Format("1月2日 15:04"))
}

// skillTriggerText 是技能怎么被调起来：SKILL.md 头部的 disable-model-invocation、user-invocable。
func skillTriggerText(m skills.Meta) (text, tip string) {
	switch {
	case m.ModelOff:
		return "手动触发", "disable-model-invocation: true：模型不会自己调用，要输入 /名字 调用（Codex 不认这一项，照样自动）"
	case m.UserHidden:
		return "只自动触发", "user-invocable: false：不出现在斜杠命令里，只由模型按说明自己调用"
	}
	return "自动触发", "模型按说明自己判断什么时候用，也能输入 /名字 手动调用"
}

// skillTrigger 是触发方式的小标签：手动的用强调色，和默认的自动区分开。
func skillTrigger(c *ui.Context, m skills.Meta) {
	t := c.Theme()
	text, tip := skillTriggerText(m)
	color := t.TextMuted
	if m.ModelOff {
		color = colors(t).honey
	}
	ui.Text(c, text).FontSize(fsSmall).TextColor(color).Tooltip(tip)
}

// skillUseCard 是详情里的用量：次数、会话数、最后一次，和各家各用了几次。
func skillUseCard(c *ui.Context, st *history.SkillStat) {
	t := c.Theme()
	card(c).Padding(cardPad).Gap(8).Shrink(0).Children(func() {
		cardTitle(c, "用量")
		if st == nil || st.Count == 0 {
			ui.Text(c, "各家的会话记录里都没用过它。").FontSize(fsBody).TextColor(t.TextMuted)
			return
		}
		metaRow(c, meta(c, fmt.Sprintf("用过 %d 次", st.Count)), meta(c, fmt.Sprintf("%d 个会话", st.Sessions)),
			meta(c, "最近一次 "+st.Last.Local().Format("1月2日 15:04")))
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range skills.Agents {
				n := st.ByAgent[ag.String()]
				if n == 0 {
					continue
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentIcon(c, ag.String(), smallIcon())
					ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}).Tooltip(fmt.Sprintf("%s用了 %d 次", agentTitle(ag.String()), n))
			}
		})
	})
}
