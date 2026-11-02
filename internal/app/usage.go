package app

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/history"
	"posse/locales"
)

// usageView 是右侧"技能与工具"面板的状态：当前终端的会话用到了哪些技能、MCP 工具、命令行程序和网址。
// 会话文件在窗口进程里增量只读解析，不经过常驻进程。
type usageView struct {
	term    string // 统计的是哪个终端
	scanner *history.UsageScanner
	data    history.Usage
	found   bool // 找到了会话文件
	err     string
	poll    panelPoll
}

// usageRefresh 是面板开着时多久增量读一次会话文件。
const usageRefresh = 3 * time.Second

// usageSupported 报告能不能统计这个 agent 的会话。
func usageSupported(agent string) bool {
	return agent == "claude" || agent == "codex" || agent == "grok" || agent == "cursor"
}

// pollUsage 在主线程调用：换了终端就重来，到点了就在后台读会话文件的新内容。
func (a *app) pollUsage(c *ui.Context, cur *terminalInfo) {
	u := &a.usage
	if u.term != cur.ID {
		*u = usageView{term: cur.ID}
	}
	if !usageSupported(cur.Agent) || !u.poll.due(c, usageRefresh) {
		return
	}
	id, agent, scanner := cur.ID, cur.Agent, u.scanner
	var sid, reported string
	if cur.AgentState != nil {
		sid, reported = cur.AgentState.SessionID, cur.AgentState.TranscriptPath
	}
	go func() {
		// 会话文件要等 agent 写下第一条记录才出现，找不到就下次再找
		if scanner == nil {
			if path := history.TranscriptPath(agent, sid, reported); path != "" {
				scanner = history.NewUsageScanner(agent, path)
			}
		}
		var data history.Usage
		var err error
		if scanner != nil {
			data, err = scanner.Scan()
		}
		a.win.Update(func() {
			u := &a.usage
			if u.term != id {
				return
			}
			u.poll.done()
			u.scanner, u.found = scanner, scanner != nil
			u.err = ""
			if err != nil {
				u.err = errtext.Text(err)
			} else if scanner != nil {
				u.data = data
			}
		})
	}()
}

// usagePanel 是终端右侧的面板。
func (a *app) usagePanel(c *ui.Context, cur *terminalInfo) ui.Element {
	a.pollUsage(c, cur)
	u := &a.usage
	m := L(c)
	return a.rightPanel(c, m.UsageToolsTitle(), func() {
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(12).Children(func() {
				switch {
				case cur.Agent == "":
					usageNote(c, m.UsageToolsPlainTerminal())
				case !usageSupported(cur.Agent):
					usageNote(c, m.UsageToolsUnsupported(agentTitle(cur.Agent)))
				case u.err != "":
					usageNote(c, m.UsageToolsReadError(u.err))
				case !u.found && u.poll.at.IsZero():
					usageNote(c, m.CommonLoading())
				case !u.found:
					usageNote(c, m.UsageToolsNoFile())
				case len(u.data.Skills) == 0 && len(u.data.MCP) == 0 && len(u.data.CLI) == 0 && len(u.data.Web) == 0 && u.data.Searches == 0:
					usageNote(c, m.UsageToolsNone())
				default:
					a.usageFilter(c)
					mode := a.usageMode()
					switch {
					case mode == usageSkills && len(u.data.Skills) == 0 && len(u.data.MCP) == 0:
						usageNote(c, m.UsageToolsNoSkills())
						return
					case mode == usageWeb && len(u.data.Web) == 0 && u.data.Searches == 0:
						usageNote(c, m.UsageToolsNoWeb())
						return
					}
					a.usageSummary(c, u.data, mode)
					a.usageTimeline(c, cur, u.data.Events, mode)
				}
			})
		})
	})
}

// usageMode 是面板的筛选：全部、只看技能和 MCP、只看网址。
type usageMode int

const (
	usageAll usageMode = iota
	usageSkills
	usageWeb
)

// usageMode 读 ui.json 里的筛选：usageSkillsOnly、usageWebOnly 最多一个为真。
func (a *app) usageMode() usageMode {
	switch {
	case a.state.UsageSkillsOnly:
		return usageSkills
	case a.state.UsageWebOnly:
		return usageWeb
	}
	return usageAll
}

// usageFilter 是面板顶上的筛选：全部、只看技能和 MCP（命令行程序常是几十个 git、adb，把技能挤得看不见）、
// 只看网址。记在 ui.json 的 usageSkillsOnly、usageWebOnly，所有会话共用。
func (a *app) usageFilter(c *ui.Context) {
	sel := int(a.usageMode())
	m := L(c)
	ui.Row(c).Padding(0, 4).Children(func() {
		if segmented(c, &sel, m.UsageToolsFilter(), m.CommonAll(), m.UsageToolsSkillsMCP(), m.UsageToolsWeb()) {
			a.state.UsageSkillsOnly, a.state.UsageWebOnly = sel == int(usageSkills), sel == int(usageWeb)
			a.saveState()
		}
	})
}

func usageNote(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(fsSmall).TextColor(c.Theme().TextMuted).LineHeight(1.5).Padding(0, 4)
}

// copyName 把技能、MCP 服务或工具的名字复制到剪贴板：常要拿去搜配置、写进提示里。
func (a *app) copyName(c *ui.Context, name string) {
	c.WriteClipboard(name)
	a.notify(L(c).UsageToolsCopied(name), "", nil)
}

// cliSources 是命令行程序分组的顺序和名字。
var cliSources = []struct {
	source string
	label  func(locales.Messages) string
}{
	{history.SourceHomebrew, func(locales.Messages) string { return "Homebrew" }},
	{history.SourceOther, locales.Messages.UsageToolsCLIOther},
	{history.SourceSystem, locales.Messages.UsageToolsCLISystem},
}

func cliSourceLabel(m locales.Messages, source string) string {
	for _, s := range cliSources {
		if s.source == source {
			return s.label(m)
		}
	}
	return ""
}

// usageSummary 是面板顶上的汇总：每个技能、每个 MCP 服务一个小标签，带次数；命令行程序按来源分组，
// 网址按域名一组（各自按筛选列）。点标签复制名字。
func (a *app) usageSummary(c *ui.Context, u history.Usage, mode usageMode) {
	if mode != usageWeb && (len(u.Skills) > 0 || len(u.MCP) > 0) {
		ui.Row(c).Wrap().Gap(6).Padding(0, 4).Children(func() {
			for _, sk := range u.Skills {
				a.usageChip(c, "skill-"+sk.Name, iconSkill, colors(c.Theme()).honey, sk.Name, sk.Count)
			}
			for _, m := range u.MCP {
				a.usageChip(c, "mcp-"+m.Server, iconExtensions, mcpColor(c), m.Server, m.Count)
			}
		})
	}
	if mode == usageSkills {
		return
	}
	if mode == usageAll {
		a.usageCLI(c, u)
	}
	if len(u.Web) > 0 || u.Searches > 0 {
		ui.Column(c).Gap(6).Padding(0, 4).Children(func() {
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, L(c).UsageToolsWeb()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(c.Theme().TextMuted)
				if u.Searches > 0 {
					ui.Text(c, L(c).UsageToolsSearches(u.Searches)).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted)
				}
			})
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, it := range u.Web {
					a.usageChip(c, "web-"+it.Host, iconGlobe, webColor(c), it.Host, it.Count)
				}
			})
		})
	}
}

// usageCLI 是命令行程序的汇总，按来源分组。
func (a *app) usageCLI(c *ui.Context, u history.Usage) {
	for _, g := range cliSources {
		var items []history.CLIUsage
		for _, it := range u.CLI {
			if it.Source == g.source {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			continue
		}
		ui.Column(c).Gap(6).Padding(0, 4).Children(func() {
			ui.Text(c, g.label(L(c))).FontSize(fsSmall).FontWeight(fwMedium).TextColor(c.Theme().TextMuted)
			ui.Row(c).Wrap().Gap(6).Children(func() {
				for _, it := range items {
					a.usageChip(c, "cli-"+it.Name, iconTerminal, cliColor(c), it.Name, it.Count)
				}
			})
		})
	}
}

func (a *app) usageChip(c *ui.Context, key string, icon *ui.SVG, color ui.Color, name string, n int) {
	t := c.Theme()
	b := ui.Row(c.Key(key)).Height(sz(22)).Padding(0, 8, 0, 6).Gap(4).Radius(full(sz(22))).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(L(c).UsageToolsCopyLabel(name))
	bg := colors(t).fill
	if b.Hovered() {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Icon(c, icon).FontSize(fsSmall).TextColor(color).Shrink(0)
		ui.Text(c, name).FontSize(fsSmall).Font(monoFont).SingleLine().MaxWidth(150)
		ui.Text(c, fmt.Sprintf("×%d", n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
	})
	if b.Clicked() {
		a.copyName(c, name)
	}
}

func mcpColor(c *ui.Context) ui.Color { return colors(c.Theme()).sky }

// cliColor 是命令行程序的颜色：数据色里的绿。
func cliColor(c *ui.Context) ui.Color { return dataColor(c.Theme(), 2) }

// webColor 是网址的颜色：数据色里的紫。
func webColor(c *ui.Context) ui.Color { return dataColor(c.Theme(), 3) }

// webSite 把网址拆成显示用的域名（去掉 www.）和其余部分。
func webSite(raw string) (host, rest string) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw, ""
	}
	host = strings.TrimPrefix(u.Hostname(), "www.")
	rest = u.EscapedPath()
	if u.RawQuery != "" {
		rest += "?" + u.RawQuery
	}
	if rest == "/" {
		rest = ""
	}
	return host, rest
}

// webVia 是网址的来源说明：用户发的写"你发的"，其余是工具名或程序名。
func webVia(m locales.Messages, via string) string {
	if via == history.ViaUser {
		return m.UsageToolsViaUser()
	}
	return via
}

// usageStep 是时间线上的一项：连着几次调用同一个工具（或同一个技能）合成一项。
type usageStep struct {
	ev    history.UsageEvent
	first time.Time // 这几次里最早的
	n     int
}

// usageSteps 把逐次记录合并成时间线，新的在前；按筛选跳过别的类别。
func usageSteps(events []history.UsageEvent, mode usageMode) []usageStep {
	var steps []usageStep
	for _, e := range events {
		web := e.URL != "" || e.Query != ""
		if mode == usageSkills && (e.CLI != "" || web) || mode == usageWeb && !web {
			continue
		}
		if k := len(steps) - 1; k >= 0 && steps[k].ev.Skill == e.Skill && steps[k].ev.Server == e.Server && steps[k].ev.Tool == e.Tool && steps[k].ev.CLI == e.CLI &&
			steps[k].ev.URL == e.URL && steps[k].ev.Query == e.Query && steps[k].ev.Via == e.Via && steps[k].ev.Failed == e.Failed {
			steps[k].ev.At, steps[k].ev.Command = e.At, e.Command
			steps[k].n++
			continue
		}
		steps = append(steps, usageStep{ev: e, first: e.At, n: 1})
	}
	slices.Reverse(steps)
	return steps
}

// usageTimeline 画时间线：左边一条竖线串起圆点，技能是琥珀色、MCP 是蓝色、命令行程序是绿色、网址是紫色；
// 右边是名字、所属服务（命令行程序是来源，网址是来源和路径）和时间。点名字复制，命令行程序悬停看最近一次的完整命令；
// 网址复制完整网址，悬停看完整网址，⌘ 点击在网页标签里打开、⇧⌘ 点击用浏览器打开（设置里互换了就反过来）。
func (a *app) usageTimeline(c *ui.Context, cur *terminalInfo, events []history.UsageEvent, mode usageMode) {
	t := c.Theme()
	m := L(c)
	p := colors(t)
	steps := usageSteps(events, mode)
	if len(steps) == 0 {
		return
	}
	ui.Column(c).Gap(0).Padding(6, 4, 0, 4).Children(func() {
		ui.Text(c, m.UsageToolsTimeline()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(0, 0, 8, 0)
		for i, st := range steps {
			color, kind, name := mcpColor(c), st.ev.Server, st.ev.Tool
			if st.ev.Failed {
				kind += " · " + m.CommonFailed() // 出错或被拒的 MCP 调用，没算进次数
			}
			copied, tip := "", st.ev.Command
			switch {
			case st.ev.Skill != "":
				color, kind, name = p.honey, m.UsageToolsSkill(), st.ev.Skill
			case st.ev.CLI != "":
				color, kind, name = cliColor(c), cliSourceLabel(m, st.ev.Source), st.ev.CLI
			case st.ev.URL != "":
				host, rest := webSite(st.ev.URL)
				color, kind, name, copied = webColor(c), webVia(m, st.ev.Via), host, st.ev.URL
				if rest != "" {
					kind += " · " + rest
				}
				tip = m.UsageToolsURLTip(st.ev.URL)
				if a.state.LinkSwapSystem {
					tip = m.UsageToolsURLTipSwapped(st.ev.URL)
				}
			case st.ev.Query != "":
				color, kind, name = webColor(c), m.UsageToolsSearchVia(webVia(m, st.ev.Via)), st.ev.Query
			}
			if copied == "" {
				copied = name
			}
			last := i == len(steps)-1
			ui.Row(c.Key(fmt.Sprint("step-", i))).Gap(8).AlignItems(ui.Stretch).Children(func() {
				// 圆点和连到下一项的线
				rail := ui.Box(c).Width(12).Shrink(0)
				line := p.line
				rail.Draw(func(pt *ui.Painter, r ui.Rect) {
					cx := r.X + r.W/2
					if !last {
						pt.Fill(ui.Rect{X: cx - 0.75, Y: r.Y + 14, W: 1.5, H: r.H - 14}, line, 0)
					}
					pt.Fill(ui.Rect{X: cx - 4, Y: r.Y + 5, W: 8, H: 8}, color, 4)
				})
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Padding(0, 0, 16, 0).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						nt := ui.Text(c, name).FontSize(fsCode).Font(monoFont).SingleLine().Shrink(1).
							Role(ui.RoleButton).Label(m.UsageToolsCopyLabel(copied))
						if nt.Hovered() {
							nt.TextColor(t.Accent)
						}
						if tip != "" {
							nt.Tooltip(tip)
						}
						if nt.Clicked() {
							if st.ev.URL == "" || !a.openUsageURL(c, cur, st.ev.URL, nt.ClickModifiers()) {
								a.copyName(c, copied)
							}
						}
						if st.n > 1 {
							ui.Text(c, fmt.Sprintf("×%d", st.n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
						}
						ui.Spacer(c)
						if !st.ev.At.IsZero() {
							ui.Text(c, st.ev.At.Local().Format("15:04")).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0).
								Tooltip(m.UsageToolsSince(st.first.Local().Format("1月2日 15:04:05")))
						}
					})
					ui.Text(c, kind).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
				})
			})
		}
	})
}

// openUsageURL 按点击的修饰键打开时间线里的网址：⌘ 在终端所在工作区的网页标签里打开，⇧⌘ 用浏览器打开
// （设置里互换了直接打开和交给系统的按法就反过来）。没按修饰键返回 false，照常复制。
func (a *app) openUsageURL(c *ui.Context, cur *terminalInfo, link string, mods ui.Modifiers) bool {
	if mods&ui.Cmd == 0 {
		return false
	}
	system := mods&ui.Shift != 0
	if a.state.LinkSwapSystem {
		system = !system
	}
	w := a.workspace(cur.WorkspaceID)
	if system || w == nil {
		c.OpenURL(link)
	} else {
		a.openWeb(w, link)
	}
	return true
}
