package app

import (
	"cmp"
	"fmt"
	"maps"
	"os"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/procmem"
)

// 底栏的内存浮窗：posse 自己（常驻进程、窗口）和各工作区终端里跑的程序（shell、agent 和它拉起的子进程）。
// 终端的内存由窗口进程按 pid 统计整棵进程树的 phys_footprint，和活动监视器一致；只在浮窗开着时统计。

type memView struct {
	open     bool
	expanded map[string]bool // 展开了进程明细的终端
	poll     panelPoll
	rows     []memWorkspace
	terms    int64 // 所有终端合计（KB）
}

type memWorkspace struct {
	id, label string
	kb        int64
	terms     []memTerminal
}

type memTerminal struct {
	term  terminalInfo
	kb    int64
	procs []memProc
}

// memProc 是终端里的一个程序：shell、agent 本体，或 agent 拉起的一棵子进程（同名的合在一起）。
type memProc struct {
	name string
	kb   int64
	n    int
}

// memBreakdown 拆开一个终端的进程树。shell 下面只有一个程序时（agent），再拆一层它拉起的子进程，
// 这样 MCP 服务一个个分开；每个子进程按整棵子树算。
func memBreakdown(shell int) []memProc {
	kb := func(pid int) int64 { return int64(procmem.Footprint(pid) >> 10) }
	tree := func(pid int) int64 { return int64(procmem.TreeFootprint(pid) >> 10) }
	name := func(pid int) string {
		if n := procmem.Name(procmem.Args(pid)); n != "" {
			return n
		}
		return fmt.Sprint("pid ", pid)
	}
	procs := []memProc{{name: name(shell), kb: kb(shell), n: 1}}
	kids := procmem.Children(shell)
	if len(kids) == 1 {
		main := kids[0]
		procs = append(procs, memProc{name: name(main), kb: kb(main), n: 1})
		kids = procmem.Children(main)
	}
	for _, k := range kids {
		p := memProc{name: name(k), kb: tree(k), n: 1}
		if i := slices.IndexFunc(procs, func(x memProc) bool { return x.name == p.name }); i >= 0 {
			procs[i].kb += p.kb
			procs[i].n++
			continue
		}
		procs = append(procs, p)
	}
	slices.SortStableFunc(procs, func(x, y memProc) int { return cmp.Compare(y.kb, x.kb) })
	return procs
}

const memRefresh = 3 * time.Second

// terminalsFootprint 是所有运行中终端的进程树内存之和（KB）。
func terminalsFootprint(ws []workspace) int64 {
	var kb int64
	for _, w := range ws {
		for _, t := range w.Terminals {
			if t.Running && t.Pid > 0 {
				kb += int64(procmem.TreeFootprint(t.Pid) >> 10)
			}
		}
	}
	return kb
}

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
	// 进程明细要逐个读 argv，只给展开了的终端算
	expanded := maps.Clone(a.mem.expanded)
	go func() {
		rows := []memWorkspace{} // 非 nil：nil 表示还没统计完
		var total int64
		for _, j := range jobs {
			r := memWorkspace{id: j.ws.ID, label: displayName(&j.ws)}
			for _, t := range j.terms {
				kb := int64(procmem.TreeFootprint(t.Pid) >> 10)
				r.kb += kb
				var procs []memProc
				if expanded[t.ID] {
					procs = memBreakdown(t.Pid)
				}
				r.terms = append(r.terms, memTerminal{t, kb, procs})
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
			// 统计途中才展开的终端没有明细，接着再统计一次
			for _, r := range rows {
				for _, x := range r.terms {
					if m.expanded[x.term.ID] && x.procs == nil {
						m.poll.at = time.Time{}
					}
				}
			}
		})
	}()
}

// memoryButton 是底栏右边的终端数和总内存（posse 自己加上终端里的程序），点开看各工作区。
func (a *app) memoryButton(c *ui.Context) {
	t := c.Theme()
	m := &a.mem
	total := a.daemon.FootprintKB + a.selfKB + a.termKB
	n := a.daemon.Running
	b := ui.Row(c.Key("memory")).Height(sz(24)).Padding(0, chipPad).Gap(6).Radius(rRow).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(L(c).MemoryButtonLabel(mb(total), n)).Tooltip(L(c).MemoryTip())
	bg := ui.Transparent
	if m.open || b.Hovered() {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Icon(c, iconMemory).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, mb(total)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
		ui.Box(c).Size(1, 12).Margin(0, 2).Background(colors(t).line)
		ui.Icon(c, iconTerminal).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
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

// memLine 是浮窗里的一行：名称和内存。level 0 是小节合计，1 是一项，2 是终端（缩进、次要色），3 是终端里的程序。
func memLine(c *ui.Context, key string, lead func(), label, value string, level int) ui.Element {
	t := c.Theme()
	h, fs, pad := sz(28), fsBody, float32(0)
	switch level {
	case 2:
		h, fs, pad = sz(26), fsSmall, 8
	case 3:
		h, fs, pad = sz(22), fsSmall, 50
	}
	return ui.Row(c.Key(key)).Height(h).Gap(8).Padding(0, 0, 0, pad).AlignItems(ui.Center).Children(func() {
		if lead != nil {
			lead()
		}
		l := ui.Text(c, label).FontSize(fs).Grow(1).Shrink(1).SingleLine()
		v := ui.Text(c, value).FontSize(fs).FontFeatures("tnum").Shrink(0)
		switch level {
		case 0:
			l.FontWeight(fwMedium)
			v.FontWeight(fwMedium)
		case 2, 3:
			l.TextColor(t.TextMuted)
			v.TextColor(t.TextMuted)
		}
		if level == 3 {
			l.Font(monoFont)
		}
	})
}

// memSection 是浮窗里一节的小标题。
func memSection(c *ui.Context, title, value string) {
	t := c.Theme()
	ui.Row(c).Gap(8).Padding(0, 0, 2, 0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Grow(1)
		ui.Text(c, value).FontSize(fsSmall).FontWeight(fwMedium).FontFeatures("tnum").TextColor(t.TextMuted)
	})
}

func (a *app) memoryPanel(c *ui.Context) {
	t := c.Theme()
	l := L(c)
	m := &a.mem
	ui.Column(c).Width(320).Gap(12).Padding(8, 8, 6, 8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			titleText(c, l.MemoryTitle(), fsTitle).Grow(1)
			terms := a.termKB
			if m.rows != nil {
				terms = m.terms
			}
			ui.Text(c, mb(a.daemon.FootprintKB+a.selfKB+terms)).FontSize(fsTitle).FontWeight(fwMedium).FontFeatures("tnum")
		})
		ui.Column(c).Children(func() {
			memSection(c, "POSSE", mb(a.daemon.FootprintKB+a.selfKB))
			memLine(c, "daemon", nil, l.MemoryDaemon(), mb(a.daemon.FootprintKB), 1)
			memLine(c, "window", nil, l.MemoryWindow(fmt.Sprint(os.Getpid())), mb(a.selfKB), 1)
		})
		hline(c)
		ui.Column(c).Children(func() {
			if m.rows == nil {
				memSection(c, l.MemoryTermPrograms(), "")
				ui.Text(c, l.MemoryCounting()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 0)
				return
			}
			memSection(c, l.MemoryTermPrograms(), mb(m.terms))
			if len(m.rows) == 0 {
				ui.Text(c, l.MemoryNoTerminals()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 0)
				return
			}
			// 滚动区向两边扩到浮窗的内边距里，内容再缩回来：滚动条落在右边的留白上，不压着数字
			ui.Scroll(c).MaxHeight(380).Margin(0, -8).Children(func() {
				ui.Column(c).Gap(8).Padding(0, 8).Children(func() {
					for _, r := range m.rows {
						ui.Column(c.Key(r.id)).Children(func() {
							memLine(c, "ws", func() {
								ui.Icon(c, iconFolder).FontSize(fsBody).TextColor(t.Accent).Shrink(0)
							}, r.label, mb(r.kb), 0)
							for _, x := range r.terms {
								name, detail := tabLabel(L(c), x.term)
								if detail != "" {
									name = detail
								}
								open := m.expanded[x.term.ID]
								row := memLine(c, x.term.ID, func() {
									chevron := iconChevron
									if open {
										chevron = iconDown
									}
									ui.Icon(c, chevron).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
									if x.term.Agent != "" {
										agentTile(c, x.term.Agent, smallIcon())
									} else {
										ui.Icon(c, iconTerminal).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
									}
								}, name, mb(x.kb), 2).Role(ui.RoleButton).Radius(rRow)
								if row.Hovered() {
									row.Background(colors(t).wash)
								}
								if row.Clicked() {
									if m.expanded == nil {
										m.expanded = map[string]bool{}
									}
									m.expanded[x.term.ID] = !open
									if !open && x.procs == nil && !m.poll.busy {
										m.poll.at = time.Time{} // 马上统计这个终端的明细
									}
								}
								if open && x.procs == nil {
									memLine(c, x.term.ID+"-loading", nil, l.MemoryCounting(), "", 3)
								}
								if open {
									for i, pr := range x.procs {
										label := pr.name
										if pr.n > 1 {
											label = fmt.Sprintf("%s ×%d", pr.name, pr.n)
										}
										memLine(c, fmt.Sprint(x.term.ID, "-", i), nil, label, mb(pr.kb), 3)
									}
								}
							}
						})
					}
				})
			})
		})
		ui.Text(c, l.MemoryNote()).
			FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Padding(2, 0, 0, 0)
	})
}
