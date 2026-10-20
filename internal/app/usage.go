package app

import (
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/history"
)

// usageView 是右侧"技能与 MCP"面板的状态：当前终端的会话用到了哪些技能和 MCP 工具。
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
	return a.rightPanel(c, "技能与 MCP", func() {
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 12, 16, 12).Gap(14).Children(func() {
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
				case len(u.data.Skills) == 0 && len(u.data.MCP) == 0:
					usageNote(c, "这个会话还没用到技能和 MCP。")
				default:
					usageSummary(c, u.data)
					usageTimeline(c, u.data.Events)
				}
			})
		})
	})
}

func usageNote(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted).LineHeight(1.5).Padding(0, 4)
}

// usageSummary 是面板顶上的汇总：每个技能、每个 MCP 服务一个小标签，带次数。
func usageSummary(c *ui.Context, u history.Usage) {
	ui.Row(c).Wrap().Gap(6).Padding(0, 4).Children(func() {
		for _, sk := range u.Skills {
			usageChip(c, iconSkill, colors(c.Theme()).honey, sk.Name, sk.Count)
		}
		for _, m := range u.MCP {
			usageChip(c, iconExtensions, mcpColor(c), m.Server, m.Count)
		}
	})
}

func usageChip(c *ui.Context, icon *ui.SVG, color ui.Color, name string, n int) {
	t := c.Theme()
	ui.Row(c).Height(22).Padding(0, 8, 0, 6).Gap(4).Radius(11).AlignItems(ui.Center).Background(colors(t).fill).
		Tooltip(name).Children(func() {
		ui.Icon(c, icon).FontSize(11).TextColor(color).Shrink(0)
		ui.Text(c, name).FontSize(11).Font(monoFont).SingleLine().MaxWidth(150)
		ui.Text(c, fmt.Sprintf("×%d", n)).FontSize(10.5).FontFeatures("tnum").TextColor(t.TextMuted)
	})
}

func mcpColor(c *ui.Context) ui.Color {
	if c.Theme().Dark {
		return ui.Hex("#7AA2F7")
	}
	return ui.Hex("#3B6FD9")
}

// usageStep 是时间线上的一项：连着几次调用同一个工具（或同一个技能）合成一项。
type usageStep struct {
	ev    history.UsageEvent
	first time.Time // 这几次里最早的
	n     int
}

// usageSteps 把逐次记录合并成时间线，新的在前。
func usageSteps(events []history.UsageEvent) []usageStep {
	var steps []usageStep
	for _, e := range events {
		if k := len(steps) - 1; k >= 0 && steps[k].ev.Skill == e.Skill && steps[k].ev.Server == e.Server && steps[k].ev.Tool == e.Tool {
			steps[k].ev.At = e.At
			steps[k].n++
			continue
		}
		steps = append(steps, usageStep{ev: e, first: e.At, n: 1})
	}
	slices.Reverse(steps)
	return steps
}

// usageTimeline 画时间线：左边一条竖线串起圆点，技能是琥珀色、MCP 是蓝色；右边是名字、所属服务和时间。
func usageTimeline(c *ui.Context, events []history.UsageEvent) {
	t := c.Theme()
	p := colors(t)
	steps := usageSteps(events)
	ui.Column(c).Gap(0).Padding(6, 4, 0, 4).Children(func() {
		ui.Text(c, "时间线").FontSize(11).FontWeight(500).TextColor(t.TextMuted).Padding(0, 0, 8, 0)
		for i, st := range steps {
			color, kind, name := mcpColor(c), st.ev.Server, st.ev.Tool
			if st.ev.Skill != "" {
				color, kind, name = p.honey, "技能", st.ev.Skill
			}
			last := i == len(steps)-1
			ui.Row(c.Key(fmt.Sprint("step-", i))).Gap(10).AlignItems(ui.Stretch).Children(func() {
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
				ui.Column(c).Grow(1).Shrink(1).Gap(2).Padding(0, 0, 14, 0).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						ui.Text(c, name).FontSize(12.5).Font(monoFont).SingleLine().Shrink(1).Tooltip(name)
						if st.n > 1 {
							ui.Text(c, fmt.Sprintf("×%d", st.n)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
						}
						ui.Spacer(c)
						if !st.ev.At.IsZero() {
							ui.Text(c, st.ev.At.Local().Format("15:04")).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0).
								Tooltip(st.first.Local().Format("1月2日 15:04:05") + " 起")
						}
					})
					ui.Text(c, kind).FontSize(11).TextColor(t.TextMuted).SingleLine()
				})
			})
		}
	})
}
