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

// memoryButton 是底栏右边的终端数和内存，点开看各工作区。
func (a *app) memoryButton(c *ui.Context) {
	t := c.Theme()
	m := &a.mem
	total := a.daemon.FootprintKB + a.selfKB
	n := a.daemon.Running
	b := ui.Row(c.Key("memory")).Height(24).Padding(0, 8).Gap(6).Radius(6).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(fmt.Sprintf("%d 个终端在运行，内存 %s", n, mb(total))).Tooltip("终端和内存")
	bg := ui.Transparent
	if m.open || b.Hovered() {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Icon(c, iconTerminal).FontSize(13).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, fmt.Sprint(n)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
		ui.Box(c).Size(1, 12).Margin(0, 2).Background(colors(t).line)
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

// memLine 是浮窗里的一行：名称和内存。level 0 是小节合计，1 是一项，2 是终端（缩进、次要色）。
func memLine(c *ui.Context, key string, lead func(), label, value string, level int) {
	t := c.Theme()
	h, fs, pad := float32(28), float32(13), float32(0)
	if level == 2 {
		h, fs, pad = 26, 12, 24
	}
	ui.Row(c.Key(key)).Height(h).Gap(8).Padding(0, 0, 0, pad).AlignItems(ui.Center).Children(func() {
		if lead != nil {
			lead()
		}
		l := ui.Text(c, label).FontSize(fs).Grow(1).Shrink(1).SingleLine()
		v := ui.Text(c, value).FontSize(fs).FontFeatures("tnum").Shrink(0)
		switch level {
		case 0:
			l.FontWeight(500)
			v.FontWeight(500)
		case 2:
			l.TextColor(t.TextMuted)
			v.TextColor(t.TextMuted)
		}
	})
}

// memSection 是浮窗里一节的小标题。
func memSection(c *ui.Context, title, value string) {
	t := c.Theme()
	ui.Row(c).Gap(8).Padding(0, 0, 2, 0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(11).FontWeight(500).TextColor(t.TextMuted).Grow(1)
		ui.Text(c, value).FontSize(11).FontWeight(500).FontFeatures("tnum").TextColor(t.TextMuted)
	})
}

func (a *app) memoryPanel(c *ui.Context) {
	t := c.Theme()
	p := colors(t)
	m := &a.mem
	ui.Column(c).Width(320).Gap(14).Padding(8, 8, 6, 8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			titleText(c, "内存", fsCardTitle).Grow(1)
			ui.Text(c, mb(a.daemon.FootprintKB+a.selfKB+m.terms)).FontSize(fsCardTitle).FontWeight(500).FontFeatures("tnum")
		})
		ui.Column(c).Children(func() {
			memSection(c, "HIVE", mb(a.daemon.FootprintKB+a.selfKB))
			memLine(c, "daemon", nil, "常驻进程（托管所有终端）", mb(a.daemon.FootprintKB), 1)
			memLine(c, "window", nil, fmt.Sprintf("窗口（pid %d）", os.Getpid()), mb(a.selfKB), 1)
		})
		ui.Box(c).Height(1).Background(p.line)
		ui.Column(c).Children(func() {
			if m.rows == nil {
				memSection(c, "终端里的程序", "")
				ui.Text(c, "正在统计…").FontSize(12).TextColor(t.TextMuted).Padding(6, 0)
				return
			}
			memSection(c, "终端里的程序", mb(m.terms))
			if len(m.rows) == 0 {
				ui.Text(c, "没有运行中的终端。").FontSize(12).TextColor(t.TextMuted).Padding(6, 0)
				return
			}
			ui.Scroll(c).MaxHeight(380).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					for _, r := range m.rows {
						ui.Column(c.Key(r.id)).Children(func() {
							memLine(c, "ws", func() {
								ui.Icon(c, iconFolder).FontSize(14).TextColor(t.Accent).Shrink(0)
							}, r.label, mb(r.kb), 0)
							for _, x := range r.terms {
								name, detail := tabLabel(x.term)
								if detail != "" {
									name = detail
								}
								memLine(c, x.term.ID, func() {
									if x.term.Agent != "" {
										agentTile(c, x.term.Agent, 13)
									} else {
										ui.Icon(c, iconTerminal).FontSize(13).TextColor(t.TextMuted).Shrink(0)
									}
								}, name, mb(x.kb), 2)
							}
						})
					}
				})
			})
		})
		ui.Text(c, "终端按进程树统计：shell、agent 和它拉起的子进程（如 MCP 服务），和活动监视器的“内存”一致。").
			FontSize(11).TextColor(t.TextMuted).LineHeight(1.5).Padding(2, 0, 0, 0)
	})
}
