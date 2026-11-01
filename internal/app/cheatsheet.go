package app

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/cheatsheet"
	"posse/internal/skills"
)

// cheatView 是右侧"命令速查"面板的状态：当前会话这家 agent 的内置命令、按键、输入前缀，
// 和它在这个工作区用得上的技能。内置的部分是 Posse 带的表，技能和本机版本打开时在后台读。
type cheatView struct {
	term    string // 查的是哪个终端
	agent   string // 和终端里的哪家 agent：同一个终端里可能退出一家、又启动另一家
	query   string
	version string // 终端里正在运行的 agent 的版本，认不出时为空
	skills  []cheatSkill
	loaded  bool // 读过一次技能和版本
	poll    panelPoll
}

// cheatSkill 是一个能手动调用的技能；builtin 是 agent 自带的（扫到的「系统自带」目录）。
type cheatSkill struct {
	name, desc string
	builtin    bool
}

// 面板的页签，顺序就是界面上的顺序；选的哪个记在 ui.json 的 cheatTab，所有会话共用。
const (
	cheatBuiltinSkills = iota
	cheatSkills
	cheatCommands
	cheatKeys
)

var cheatTabs = []string{"内置技能", "技能", "内置命令", "快捷键"}

// cheatRefresh 是面板开着时多久重新认一次版本、扫一次技能：会话里可能换了 agent 版本、装了技能。
// 还没认出版本时 cheatRetry 后再认：刚开的终端要先跑完登录 shell 的配置，agent 进程还没起来。
const (
	cheatRefresh = 30 * time.Second
	cheatRetry   = 3 * time.Second
)

// skillAgent 把 agent 名字换成技能扫描里的那一家。
func skillAgent(name string) (skills.Agent, bool) {
	for _, ag := range skills.Agents {
		if ag.String() == name {
			return ag, true
		}
	}
	return 0, false
}

// pollCheat 在主线程调用：换了终端就重来，到点了在后台认版本、扫技能。
func (a *app) pollCheat(c *ui.Context, cur *terminalInfo) {
	v := &a.cheat
	if v.term != cur.ID || v.agent != cur.Agent {
		*v = cheatView{term: cur.ID, agent: cur.Agent}
	}
	every := cheatRefresh
	if v.loaded && v.version == "" && cur.Running {
		every = cheatRetry
	}
	if !v.poll.due(c, every) {
		return
	}
	id, agentName, pid, dir := cur.ID, cur.Agent, cur.Pid, cur.Dir
	if w := a.workspace(cur.WorkspaceID); w != nil {
		dir = w.Path
	}
	go func() {
		version := cheatsheet.Running(pid, agentName)
		var list []cheatSkill
		if ag, ok := skillAgent(agentName); ok {
			cat := skills.Scan(skills.Options{Home: homeDir, Dir: dir})
			for _, s := range cat.Skills {
				st := s.Agents[ag].State
				// Claude 账号同步的技能在 Claude 里带前缀（同步目录名推不出来），按名字贴进去不对，不列
				if (st == skills.On || st == skills.Manual) && !s.Meta.UserHidden && !(ag == skills.Claude && s.Scope == skills.ScopeSynced) {
					list = append(list, cheatSkill{name: s.Name, desc: s.Description, builtin: s.Scope == skills.ScopeSystem})
				}
			}
			slices.SortFunc(list, func(x, y cheatSkill) int { return strings.Compare(x.name, y.name) })
		}
		a.win.Update(func() {
			v := &a.cheat
			if v.term != id || v.agent != agentName {
				return
			}
			v.poll.done()
			v.version, v.skills, v.loaded = version, list, true
		})
	}()
}

// cheatPanel 是终端右侧的面板：上面是搜索框、版本和页签，下面按组列出当前页签的条目；
// 搜索时不分页签，各页签里匹配的都列出来。命令、前缀和技能点一下贴进输入框。
func (a *app) cheatPanel(c *ui.Context, cur *terminalInfo) ui.Element {
	a.pollCheat(c, cur)
	v := &a.cheat
	sheet, ok := cheatsheet.For(cur.Agent)
	return a.rightPanel(c, "命令速查", func() {
		if !ok {
			ui.Column(c).Padding(0, 12, 16, 12).Children(func() {
				usageNote(c, agentTitle(cur.Agent)+" 还没有速查表。")
			})
			return
		}
		q := strings.ToLower(strings.TrimSpace(v.query))
		tab := min(max(a.state.CheatTab, 0), len(cheatTabs)-1)
		ui.Column(c).Padding(0, 12, 8, 12).Gap(8).Children(func() {
			fieldInputSized(c, &v.query, "搜索命令、技能和说明", 0, 0).Label("搜索")
			a.cheatVersion(c, cur, sheet)
			// 直接放在列里：被拉到和搜索框一样宽，各段平分
			if q == "" && segmentedTight(c, &tab, "分类", cheatTabs...) {
				a.state.CheatTab = tab
				a.saveState()
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(4, 12, 16, 12).Gap(16).Children(func() {
				shown := 0
				for i := range cheatTabs {
					if q == "" && i != tab {
						continue
					}
					shown += a.cheatTab(c, cur, sheet, i, q)
				}
				if shown == 0 {
					switch {
					case q != "":
						usageNote(c, "没有匹配「"+v.query+"」的命令和技能。")
					case !v.loaded && (tab == cheatBuiltinSkills || tab == cheatSkills):
						usageNote(c, "正在读取…")
					case tab == cheatBuiltinSkills:
						usageNote(c, "没有找到 "+agentTitle(cur.Agent)+" 自带的技能。")
					case tab == cheatSkills:
						usageNote(c, "这个工作区里没有 "+agentTitle(cur.Agent)+" 能手动调用的技能。")
					}
				}
				if sheet.Docs != "" && q == "" && tab == cheatCommands {
					ui.Row(c).Padding(0, 4).Children(func() {
						if pressable(textButton(c, "官方文档")).Clicked() {
							c.OpenURL(sheet.Docs)
						}
					})
				}
			})
		})
	})
}

// cheatTab 画一个页签里匹配 q 的各组，返回画了几条。内置技能是表里编在程序里的那些加上扫到的系统自带的；
// 内置命令带上输入前缀；技能只有扫到的。
func (a *app) cheatTab(c *ui.Context, cur *terminalInfo, sheet cheatsheet.Sheet, tab int, q string) int {
	shown := 0
	for i, sec := range sheet.Sections {
		want := cheatCommands
		switch sec.Kind {
		case cheatsheet.KindSkill:
			want = cheatBuiltinSkills
		case cheatsheet.KindKey:
			want = cheatKeys
		}
		if want != tab {
			continue
		}
		items := slices.DeleteFunc(slices.Clone(sec.Items), func(it cheatsheet.Item) bool { return !cheatMatch(q, it.Name, it.Desc) })
		if len(items) == 0 {
			continue
		}
		shown += len(items)
		a.cheatSection(c, fmt.Sprint("sec-", i), cheatGroupTitle(sec.Title, tab, q), func() {
			for j, it := range items {
				a.cheatRow(c, cur, sec.Kind, it, cheatsheet.Insert(sec.Kind, it), j)
			}
		})
	}
	if tab != cheatBuiltinSkills && tab != cheatSkills {
		return shown
	}
	var sk []cheatSkill
	for _, s := range a.cheat.skills {
		if s.builtin == (tab == cheatBuiltinSkills) && cheatMatch(q, s.name, s.desc) {
			sk = append(sk, s)
		}
	}
	if len(sk) == 0 {
		return shown
	}
	title := "技能"
	if tab == cheatBuiltinSkills {
		// 和上面编在程序里的那组分开：这些装在 agent 自己的目录里，随它升级更新
		title = "内置技能"
		if shown > 0 {
			title = "随安装附带"
		}
	}
	a.cheatSection(c, fmt.Sprint("skills-", tab), cheatGroupTitle(title, tab, q), func() {
		for j, s := range sk {
			it := cheatsheet.Item{Name: sheet.SkillPrefix + s.name, Desc: s.desc}
			a.cheatRow(c, cur, cheatsheet.KindSkill, it, it.Name+" ", j)
		}
	})
	return shown + len(sk)
}

// cheatMatch 报告条目是否匹配搜索词（已转成小写）：名字或说明里包含就算。
func cheatMatch(q, name, desc string) bool {
	return q == "" || strings.Contains(strings.ToLower(name), q) || strings.Contains(strings.ToLower(desc), q)
}

// cheatVersion 是搜索框下面的一行：本机在跑的版本和表对照的版本；本机更新时提示可能有新命令，给更新记录的链接。
func (a *app) cheatVersion(c *ui.Context, cur *terminalInfo, sheet cheatsheet.Sheet) {
	t := c.Theme()
	v := &a.cheat
	text := agentTitle(cur.Agent) + " " + sheet.Version
	color := t.TextMuted
	newer := false
	switch {
	case !v.loaded, v.version == "":
		text = "速查表对照 " + text
	case cheatsheet.Compare(v.version, sheet.Version) > 0:
		text = "本机 " + v.version + " 比速查表（" + sheet.Version + "）新，可能有新命令"
		color, newer = colors(t).honey, true
	case cheatsheet.Compare(v.version, sheet.Version) < 0:
		text = "本机 " + v.version + " 比速查表（" + sheet.Version + "）旧，有的命令可能还没有"
		color = colors(t).honey
	default:
		text = agentTitle(cur.Agent) + " " + v.version
	}
	ui.Column(c).Gap(8).Padding(0, 4).Children(func() {
		ui.Text(c, text).FontSize(fsSmall).FontFeatures("tnum").TextColor(color).LineHeight(1.4)
		if newer && sheet.Changelog != "" {
			ui.Row(c).Children(func() {
				if pressable(textButton(c, "看更新记录")).Clicked() {
					c.OpenURL(sheet.Changelog)
				}
			})
		}
	})
}

// cheatGroupTitle 是一组的标题：不搜索时和所在页签同名的不再写一遍（空串）；搜索时各页签混在一起，照写。
func cheatGroupTitle(title string, tab int, q string) string {
	if q == "" && title == cheatTabs[tab] {
		return ""
	}
	return title
}

func (a *app) cheatSection(c *ui.Context, key any, title string, body func()) {
	ui.Column(c.Key(key)).Gap(2).Children(func() {
		if title == "" {
			body()
			return
		}
		ui.Text(c, title).FontSize(fsSmall).FontWeight(fwMedium).TextColor(c.Theme().TextMuted).Padding(0, 4, 4, 4)
		body()
	})
}

// cheatRow 是一条：名字（等宽）和参数提示，下一行是说明。命令、前缀和技能能点，点了贴进输入框；
// text 为空的（按键）只看，名字画成键帽。会丢掉对话的命令名字用警示色。
func (a *app) cheatRow(c *ui.Context, cur *terminalInfo, kind cheatsheet.Kind, it cheatsheet.Item, text string, i int) {
	t := c.Theme()
	// 细线隔开相邻两条，和条目文字同宽（两头让开行的内边距）
	if i > 0 {
		hline(c).Margin(0, 6)
	}
	row := ui.Column(c.Key(i)).Gap(4).Padding(8, 6).Radius(rRow)
	if text != "" {
		row.Role(ui.RoleButton).Label("贴进输入框 " + it.Name)
		bg := ui.Transparent
		if row.Hovered() {
			bg = colors(t).wash
		}
		row.Background(row.AnimateColor("bg", bg, hover))
	} else {
		row.Cursor(ui.CursorDefault)
	}
	row.Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			if kind == cheatsheet.KindKey {
				// 不用 keyChip：它的底是 Surface，和面板几乎同色，深色下看不出是键帽
				ui.Box(c).Padding(2, 8).Radius(rSmall).Background(colors(t).fillHi).Shrink(0).Children(func() {
					ui.Text(c, it.Name).FontSize(fsSmall).FontFeatures("tnum")
				})
				return
			}
			color := t.Text
			if it.Warn {
				color = t.Danger
			}
			ui.Text(c, it.Name).FontSize(fsSmall).Font(monoFont).TextColor(color).SingleLine().Shrink(1)
			if it.Args != "" {
				ui.Text(c, it.Args).FontSize(fsSmall).Font(monoFont).TextColor(colors(t).faint).SingleLine().Shrink(1)
			}
		})
		if it.Desc != "" {
			ui.Text(c, it.Desc).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.4).MaxLines(2)
		}
	})
	if text != "" && row.Clicked() {
		a.insertIntoTerminal(cur, text)
	}
}

// insertIntoTerminal 把文字当键入发进终端（不回车），收起面板、把焦点还给这个终端：
// 斜杠命令照键入发，agent 才会弹出它的补全，用户确认参数后自己回车。
// agent 停在审批、选择菜单上时不发：那里单个字母就是选项（Codex 的 a 是"批准且不再问"）。
func (a *app) insertIntoTerminal(cur *terminalInfo, text string) {
	if cur.AgentState != nil && cur.AgentState.Status == agent.StatusWaiting {
		a.err = agentTitle(cur.Agent) + " 正等你处理确认，处理完再贴命令"
		return
	}
	for i, v := range a.panes {
		if v != nil && v.id == cur.ID {
			v.term.Send([]byte(text))
			a.focusPane(cur.WorkspaceID, i) // 不分屏时只有一栏，它什么也不做
			a.focusTerm = true
			a.state.RightPanel = ""
			a.saveState()
			return
		}
	}
}
