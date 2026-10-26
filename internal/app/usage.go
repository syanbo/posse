package app

import (
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/history"
)

// usageView 是右侧"技能与工具"面板的状态：当前终端的会话用到了哪些技能、MCP 工具和命令行程序。
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
				u.err = err.Error()
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
	return a.rightPanel(c, "技能与工具", func() {
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(12).Children(func() {
				switch {
				case cur.Agent == "":
					usageNote(c, "普通终端没有会话记录。")
				case !usageSupported(cur.Agent):
					usageNote(c, agentTitle(cur.Agent)+" 的会话暂不支持统计。")
				case u.err != "":
					usageNote(c, "读会话文件失败："+u.err)
				case !u.found && u.poll.at.IsZero():
					usageNote(c, "正在读取…")
				case !u.found:
					usageNote(c, "还没找到会话文件，agent 开始对话后会出现。")
				case len(u.data.Skills) == 0 && len(u.data.MCP) == 0 && len(u.data.CLI) == 0:
					usageNote(c, "这个会话还没用到技能、MCP 和命令行工具。")
				default:
					a.usageFilter(c)
					skillsOnly := a.state.UsageSkillsOnly
					if skillsOnly && len(u.data.Skills) == 0 && len(u.data.MCP) == 0 {
						usageNote(c, "这个会话还没用到技能和 MCP。")
						return
					}
					a.usageSummary(c, u.data, skillsOnly)
					a.usageTimeline(c, u.data.Events, skillsOnly)
				}
			})
		})
	})
}

// usageFilter 是面板顶上的筛选：全部，或只看技能和 MCP（命令行程序常是几十个 git、adb，把技能挤得看不见）。
// 记在 ui.json 的 usageSkillsOnly，所有会话共用。
func (a *app) usageFilter(c *ui.Context) {
	sel := 0
	if a.state.UsageSkillsOnly {
		sel = 1
	}
	ui.Row(c).Padding(0, 4).Children(func() {
		if segmented(c, &sel, "筛选", "全部", "技能和 MCP") {
			a.state.UsageSkillsOnly = sel == 1
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
	a.notify("已复制 "+name, "", nil)
}

// cliSources 是命令行程序分组的顺序和名字。
var cliSources = []struct{ source, label string }{
	{history.SourceHomebrew, "Homebrew"},
	{history.SourceOther, "其他命令行"},
	{history.SourceSystem, "系统自带"},
}

func cliSourceLabel(source string) string {
	for _, s := range cliSources {
		if s.source == source {
			return s.label
		}
	}
	return ""
}

// usageSummary 是面板顶上的汇总：每个技能、每个 MCP 服务一个小标签，带次数；命令行程序按来源分组（skillsOnly 时不列）。
// 点标签复制名字。
func (a *app) usageSummary(c *ui.Context, u history.Usage, skillsOnly bool) {
	if len(u.Skills) > 0 || len(u.MCP) > 0 {
		ui.Row(c).Wrap().Gap(6).Padding(0, 4).Children(func() {
			for _, sk := range u.Skills {
				a.usageChip(c, "skill-"+sk.Name, iconSkill, colors(c.Theme()).honey, sk.Name, sk.Count)
			}
			for _, m := range u.MCP {
				a.usageChip(c, "mcp-"+m.Server, iconExtensions, mcpColor(c), m.Server, m.Count)
			}
		})
	}
	if skillsOnly {
		return
	}
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
			ui.Text(c, g.label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(c.Theme().TextMuted)
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
		Role(ui.RoleButton).Label("复制 " + name)
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

func mcpColor(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#7AA2F7")
	}
	return ui.Hex("#3B6FD9")
}

func cliColor(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#5FBFA0")
	}
	return ui.Hex("#21866A")
}

// usageStep 是时间线上的一项：连着几次调用同一个工具（或同一个技能）合成一项。
type usageStep struct {
	ev    history.UsageEvent
	first time.Time // 这几次里最早的
	n     int
}

// usageSteps 把逐次记录合并成时间线，新的在前；skillsOnly 时跳过命令行程序。
func usageSteps(events []history.UsageEvent, skillsOnly bool) []usageStep {
	var steps []usageStep
	for _, e := range events {
		if skillsOnly && e.CLI != "" {
			continue
		}
		if k := len(steps) - 1; k >= 0 && steps[k].ev.Skill == e.Skill && steps[k].ev.Server == e.Server && steps[k].ev.Tool == e.Tool && steps[k].ev.CLI == e.CLI {
			steps[k].ev.At, steps[k].ev.Command = e.At, e.Command
			steps[k].n++
			continue
		}
		steps = append(steps, usageStep{ev: e, first: e.At, n: 1})
	}
	slices.Reverse(steps)
	return steps
}

// usageTimeline 画时间线：左边一条竖线串起圆点，技能是琥珀色、MCP 是蓝色、命令行程序是绿色；
// 右边是名字、所属服务（命令行程序是来源）和时间。点名字复制，命令行程序悬停看最近一次的完整命令。
func (a *app) usageTimeline(c *ui.Context, events []history.UsageEvent, skillsOnly bool) {
	t := c.Theme()
	p := colors(t)
	steps := usageSteps(events, skillsOnly)
	if len(steps) == 0 {
		return
	}
	ui.Column(c).Gap(0).Padding(6, 4, 0, 4).Children(func() {
		ui.Text(c, "时间线").FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(0, 0, 8, 0)
		for i, st := range steps {
			color, kind, name := mcpColor(c), st.ev.Server, st.ev.Tool
			switch {
			case st.ev.Skill != "":
				color, kind, name = p.honey, "技能", st.ev.Skill
			case st.ev.CLI != "":
				color, kind, name = cliColor(c), cliSourceLabel(st.ev.Source), st.ev.CLI
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
							Role(ui.RoleButton).Label("复制 " + name)
						if nt.Hovered() {
							nt.TextColor(t.Accent)
						}
						if st.ev.Command != "" {
							nt.Tooltip(st.ev.Command)
						}
						if nt.Clicked() {
							a.copyName(c, name)
						}
						if st.n > 1 {
							ui.Text(c, fmt.Sprintf("×%d", st.n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
						}
						ui.Spacer(c)
						if !st.ev.At.IsZero() {
							ui.Text(c, st.ev.At.Local().Format("15:04")).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0).
								Tooltip(st.first.Local().Format("1月2日 15:04:05") + " 起")
						}
					})
					ui.Text(c, kind).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
				})
			})
		}
	})
}
