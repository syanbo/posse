package app

import (
	"cmp"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/procmem"
)

// 底栏的内存浮窗：hive 自己（常驻进程、窗口）和各工作区终端里跑的程序（shell、agent 和它拉起的子进程）。
// 终端的内存由窗口进程按 pid 统计整棵进程树的 phys_footprint，和活动监视器一致；只在浮窗开着时统计。

type memView struct {
	open  bool
	poll  panelPoll
	rows  []memWorkspace
	terms int64 // 所有终端合计（KB）
}

type memWorkspace struct {
	id, label string
	kb        int64
	terms     []memTerminal
}

type memTerminal struct {
	term terminalInfo
	kb   int64
}

const memRefresh = 3 * time.Second

// loadMemory 在后台统计各终端的进程树。
func (a *app) loadMemory() {
	type job struct {
		ws    workspace
		terms []terminalInfo
	}
	var jobs []job
	for _, w := range a.workspaces {
		var ts []terminalInfo
		for _, t := range w.Terminals {
			if t.Running && t.Pid > 0 {
				ts = append(ts, t)
			}
		}
		if len(ts) > 0 {
			jobs = append(jobs, job{w, ts})
		}
	}
	go func() {
		children := procmem.Children()
		var rows []memWorkspace
		var total int64
		for _, j := range jobs {
			r := memWorkspace{id: j.ws.ID, label: displayName(&j.ws)}
			for _, t := range j.terms {
				kb := int64(procmem.TreeFootprint(t.Pid, children) >> 10)
				r.kb += kb
				r.terms = append(r.terms, memTerminal{t, kb})
			}
			slices.SortFunc(r.terms, func(x, y memTerminal) int { return cmp.Compare(y.kb, x.kb) })
			total += r.kb
			rows = append(rows, r)
		}
		slices.SortFunc(rows, func(x, y memWorkspace) int { return cmp.Compare(y.kb, x.kb) })
		a.update(func() {
			m := &a.mem
			m.poll.done()
			m.rows, m.terms = rows, total
		})
	}()
}

// memoryButton 是底栏的内存：hive 自己占的内存，点开看各工作区。
func (a *app) memoryButton(c *ui.Context) {
	t := c.Theme()
	m := &a.mem
	total := a.daemon.FootprintKB + a.selfKB
	b := ui.Row(c.Key("memory")).Height(24).Padding(0, 6).Gap(5).Radius(6).AlignItems(ui.Center).
		Role(ui.RoleButton).Label("内存 " + mb(total)).Tooltip("内存占用")
	bg := ui.Transparent
	if m.open || b.Hovered() {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Icon(c, iconMemory).FontSize(13).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, mb(total)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
	})
	if b.Clicked() {
		m.open = !m.open
		m.poll = panelPoll{}
	}
	ui.Popover(c, b, &m.open, func() {
		if m.poll.due(c, memRefresh) {
			a.loadMemory()
		}
		a.memoryPanel(c)
	})
}

func (a *app) memoryPanel(c *ui.Context) {
	t := c.Theme()
	m := &a.mem
	line := func(label, value string, strong bool) {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			l := ui.Text(c, label).FontSize(12.5).Grow(1).Shrink(1).SingleLine()
			v := ui.Text(c, value).FontSize(12.5).FontFeatures("tnum").Shrink(0)
			if strong {
				l.FontWeight(500)
				v.FontWeight(500)
			} else {
				l.TextColor(t.TextMuted)
				v.TextColor(t.TextMuted)
			}
		})
	}
	ui.Column(c).Width(300).Gap(8).Padding(4).Children(func() {
		titleText(c, "内存", fsCardTitle)
		line("Hive 自己", mb(a.daemon.FootprintKB+a.selfKB), true)
		ui.Column(c).Gap(4).Padding(0, 0, 0, 12).Children(func() {
			line("常驻进程（托管所有终端）", mb(a.daemon.FootprintKB), false)
			line(fmt.Sprintf("窗口（pid %d）", os.Getpid()), mb(a.selfKB), false)
		})
		ui.Box(c).Height(1).Background(colors(t).line)
		if m.rows == nil {
			ui.Text(c, "正在统计终端…").FontSize(12).TextColor(t.TextMuted)
			return
		}
		line("终端里的程序", mb(m.terms), true)
		if len(m.rows) == 0 {
			ui.Text(c, "没有运行中的终端。").FontSize(12).TextColor(t.TextMuted)
			return
		}
		ui.Scroll(c).MaxHeight(360).Children(func() {
			ui.Column(c).Gap(8).Children(func() {
				for _, r := range m.rows {
					ui.Column(c.Key(r.id)).Gap(4).Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Icon(c, iconFolder).FontSize(13).TextColor(t.Accent).Shrink(0)
							ui.Text(c, r.label).FontSize(12.5).Grow(1).Shrink(1).SingleLine()
							ui.Text(c, mb(r.kb)).FontSize(12.5).FontFeatures("tnum").Shrink(0)
						})
						for _, x := range r.terms {
							ui.Row(c.Key(x.term.ID)).Gap(6).Padding(0, 0, 0, 21).AlignItems(ui.Center).Children(func() {
								if x.term.Agent != "" {
									agentTile(c, x.term.Agent, 12)
								} else {
									ui.Icon(c, iconTerminal).FontSize(12).TextColor(t.TextMuted).Shrink(0)
								}
								name, detail := tabLabel(x.term)
								if detail != "" {
									name = detail
								}
								ui.Text(c, name).FontSize(12).TextColor(t.TextMuted).Grow(1).Shrink(1).SingleLine()
								ui.Text(c, mb(x.kb)).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
							})
						}
					})
				}
			})
		})
		ui.Text(c, "终端的内存按进程树统计：shell、agent 和它拉起的子进程（如 MCP 服务）。和活动监视器的“内存”一致。").
			FontSize(11).TextColor(t.TextMuted).LineHeight(1.45)
	})
}
