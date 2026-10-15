package app

import (
	"fmt"
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

// usageSupported 报告能不能统计这个 agent 的会话：cursor 的会话文件里没有技能和 MCP 的记录方式可依。
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
				default:
					usageSection(c, "技能", len(u.data.Skills), "这个会话还没用到技能。", func() {
						for _, s := range u.data.Skills {
							usageRow(c, s.Name, s.Count, 0)
						}
					})
					usageSection(c, "MCP", len(u.data.MCP), "这个会话还没调用 MCP。", func() {
						for _, m := range u.data.MCP {
							ui.Column(c).Gap(1).Children(func() {
								usageRow(c, m.Server, m.Count, 600)
								for _, tool := range m.Tools {
									ui.Box(c).Padding(0, 0, 0, 12).Children(func() {
										usageRow(c, tool.Name, tool.Count, 0)
									})
								}
							})
						}
					})
				}
			})
		})
	})
}

func usageNote(c *ui.Context, text string) {
	ui.Text(c, text).FontSize(12).TextColor(c.Theme().TextMuted).LineHeight(1.5).Padding(0, 4)
}

// usageSection 是面板里的一节：标题带个数，空的时候显示 empty。
func usageSection(c *ui.Context, title string, n int, empty string, rows func()) {
	t := c.Theme()
	ui.Column(c).Gap(4).Children(func() {
		ui.Row(c).Gap(6).Padding(0, 4).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(t.TextMuted)
			if n > 0 {
				ui.Text(c, fmt.Sprint(n)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
			}
		})
		if n == 0 {
			usageNote(c, empty)
			return
		}
		card(c).Padding(6, 8).Gap(1).Children(rows)
	})
}

// usageRow 是一个技能或工具和它用了几次；weight 非零时名称加粗（MCP 服务名）。
func usageRow(c *ui.Context, name string, count int, weight int) {
	t := c.Theme()
	ui.Row(c).Height(24).Gap(8).AlignItems(ui.Center).Children(func() {
		text := ui.Text(c, name).FontSize(12).Font(monoFont).SingleLine().Grow(1).Shrink(1).Tooltip(name)
		if weight != 0 {
			text.FontWeight(weight)
		}
		ui.Text(c, fmt.Sprintf("×%d", count)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
	})
}
