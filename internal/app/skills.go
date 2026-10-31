package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/skills"
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
	query   string
	agent   int // 0 全部，其余是 skills.Agents[agent-1] 用得上的
}

// skillsPanelView 是文件栏「技能」页签的状态：当前工作区的项目级技能。
type skillsPanelView struct {
	key  string // 工作区目录，变了就重扫
	cat  skills.Catalog
	poll panelPoll
}

// findCodex 找 codex 可执行文件，测试里换掉免得拉起真的 codex。
var findCodex = skills.FindCodex

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
		var list []skills.CodexSkill
		if codex := findCodex(homeDir); codex != "" {
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
}

func (a *app) releaseSkills() {
	s := &a.skills
	s.req++
	*s = skillsView{req: s.req, query: s.query, agent: s.agent}
}

func (a *app) skillsSidebarItem(c *ui.Context) {
	item := a.navRow(c, skillsBoardID, iconSkill, "技能", "", "", nil)
	if a.revealSel && a.selected == skillsBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
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
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	return out
}

func (a *app) skillsBoard(c *ui.Context) {
	s := &a.skills
	a.band(c, func() {
		titleText(c, "技能", fsTitle).Margin(0, 6, 0, 0).Shrink(0)
		fieldInputSized(c, &s.query, "搜索名字和说明", 280, 160).Label("搜索技能")
		ui.Spacer(c)
		segmented(c, &s.agent, "按 agent 筛选", skillAgentLabels...)
		refresh := ui.Box(c).Shrink(0)
		if s.loading {
			refresh.Disabled(true).Opacity(0.5)
		}
		refresh.Children(func() {
			if iconButton(c.Key("skills-refresh"), iconRefresh, "重新扫描").Clicked() {
				a.scanSkills()
			}
		})
		a.closeBoardButton(c)
	})
	list := s.shownSkills()
	if s.sel == "" || s.sel != skillIssuesKey && !slices.ContainsFunc(list, func(sk skills.Skill) bool { return sk.Dir == s.sel }) {
		s.sel = ""
		if len(list) > 0 {
			s.sel = list[0].Dir
		}
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(12).Gap(8).Children(func() {
		card(c).Width(380).Shrink(0).Clip().Label("技能列表").Children(func() {
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
						a.skillRow(c, sk, s.sel == sk.Dir, func() { s.sel = sk.Dir })
					}
				})
			})
		})
		ui.Column(c).Grow(1).Shrink(1).Gap(8).Children(func() {
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
		ui.Text(c, fmt.Sprintf("%d 个技能", n)).FontSize(fsBody).FontWeight(fwMedium).FontFeatures("tnum")
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range skills.Agents {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentIcon(c, ag.String(), smallIcon())
					ui.Text(c, fmt.Sprint(byAgent[ag])).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}).Tooltip(agentTitle(ag.String()) + "用得上的技能")
			}
		})
	})
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
func (a *app) skillRow(c *ui.Context, sk skills.Skill, selected bool, onClick func()) {
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
	})
	if row.Clicked() {
		onClick()
	}
}

// skillMarks 是一行末尾四家的状态：用得上的是 agent 的色块，只能手动调用的淡一些，关了的更淡，
// 读不到的只留一个空位，四家对齐成列。悬停看原因。toggle 不为空时能开关的那几家点一下切换（先弹框确认）。
func skillMarks(c *ui.Context, sk skills.Skill, toggle func(ag skills.Agent, enable bool)) {
	ui.Row(c).Gap(4).Shrink(0).Children(func() {
		for _, ag := range skills.Agents {
			st := sk.Agents[ag]
			size := smallIcon()
			tip := skillStateTip(ag, st)
			if st.State == skills.Unseen {
				ui.Box(c).Size(size, size).Shrink(0).Tooltip(tip)
				continue
			}
			can := toggle != nil && skills.CanToggle(ag, sk)
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

func skillStateTip(ag skills.Agent, st skills.Status) string {
	tip := agentTitle(ag.String()) + "：" + skillStateText(st.State)
	if st.Why != "" {
		tip += "\n" + st.Why
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
				if pressable(iconButton(c.Key("skill-reveal"), iconFolder, "在访达中显示")).Clicked() {
					mygo.Shell.ShowItemInFolder(sk.File)
				}
			})
		})
		if sk.Description != "" {
			ui.Text(c, sk.Description).FontSize(fsBody).LineHeight(1.55).Selectable()
		}
	})
	card(c).Padding(cardPad).Gap(4).Shrink(0).Children(func() {
		cardTitle(c, "各家状态").Margin(0, 0, 4, 0)
		for _, ag := range skills.Agents {
			a.skillAgentRow(c, sk, ag, skills.Options{Home: homeDir})
		}
	})
	if len(sk.Paths) > 1 {
		card(c).Padding(cardPad).Gap(6).Shrink(0).Children(func() {
			cardTitle(c, "出现的位置").Margin(0, 0, 2, 0)
			for _, p := range sk.Paths {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					icon := iconFolder
					if p != sk.Dir {
						icon = iconFolderLink
					}
					ui.Icon(c, icon).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
					ui.Text(c, shortPath(p)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(2).Shrink(1)
				})
			}
		})
	}
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
			if st.Why != "" {
				ui.Text(c, st.Why).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			}
			if st.Via != "" {
				ui.Text(c, shortPath(st.Via)).FontSize(fsSmall).Font(monoFont).TextColor(colors(t).faint).MaxLines(2)
			}
		})
		if skills.CanToggle(ag, sk) {
			on := st.State != skills.Off
			if ui.Switch(c, &on).Label(agentTitle(ag.String()) + "里启用").Changed() {
				a.planSkillToggle(o, sk, ag, on)
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
	{skills.Duplicate, "同名冲突", "同一家读得到两份同名但内容不同的技能，只有优先的那份生效。"},
	{skills.LockMissing, "锁文件对不上", "npx skills 的锁文件记着它，~/.agents/skills 里却没有，npx skills update 会再装回来。"},
	{skills.RedundantLink, "多余的链接", "链接所在目录的那几家本来就从别处读得到它指向的那份，删掉不影响。"},
}

// skillIssues 是体检结果，按类分组。
func (a *app) skillIssues(c *ui.Context) {
	t := c.Theme()
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(8).Children(func() {
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
					ui.Column(c).Gap(2).Children(func() {
						for _, is := range items {
							issueRow(c, is)
						}
					})
				})
			}
		})
	})
}

func issueRow(c *ui.Context, is skills.Issue) {
	t := c.Theme()
	ui.Column(c).Padding(6, 0).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, is.Name).FontSize(fsBody).FontWeight(fwMedium).Shrink(0)
			for _, ag := range is.Agents {
				agentTile(c, ag.String(), smallIcon())
			}
		})
		var line string
		switch is.Kind {
		case skills.BrokenLink:
			line = shortPath(is.Path) + " → " + shortPath(is.Other)
		case skills.Duplicate:
			line = shortPath(is.Path) + " 被 " + shortPath(is.Other) + " 盖住"
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
		a.update(func() {
			if p.key != key {
				return
			}
			p.poll.done()
			p.cat = cat
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
					if r, err := filepath.Rel(w.Path, sk.Dir); err == nil {
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
		a.err = "没有改" + agentTitle(ag.String()) + "的配置：" + err.Error()
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
				a.err = "没有改" + shortPath(ch.File) + "：" + err.Error()
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
