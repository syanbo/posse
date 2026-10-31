package app

import (
	"cmp"
	"slices"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
)

// sidebarByStatus 是侧栏的「运行中」看法：所有项目里的 agent 会话平铺成一列，按状态排。
const sidebarByStatus = "status"

// sidebarGroupSwitch 是侧栏项目列表上面「项目 | 运行中」的切换。
func (a *app) sidebarGroupSwitch(c *ui.Context) {
	sel := 0
	if a.state.SidebarGroup == sidebarByStatus {
		sel = 1
	}
	ui.Column(c).Padding(6, navEnd, 0, trafficLightX).Shrink(0).Children(func() {
		if segmented(c, &sel, "侧栏分组", "项目", "运行中") {
			a.state.SidebarGroup = ""
			if sel == 1 {
				a.state.SidebarGroup = sidebarByStatus
			}
			// 每次切进来按当下的状态重排一次
			a.runOrder = nil
			a.revealSel = true
			a.saveState()
		}
	})
}

// runRank 是「运行中」里的排序：要你处理的在前，空闲在后，还没恢复的最后。
func runRank(status string) int {
	switch status {
	case agent.StatusWaiting:
		return 5
	case statusUnread:
		return 4
	case agent.StatusWorking, statusTasks:
		return 3
	case agent.StatusStarting:
		return 2
	case statusLost:
		return 0
	}
	return 1
}

type runEntry struct {
	w     workspace
	term  terminalInfo
	where string
}

// runningList 画「运行中」：会话第一次出现时插到和它同状态的那一段末尾，之后状态变了只换状态图形、不挪位置，
// 免得一列会话跟着状态来回跳；切进这个看法、重开窗口时才整列按状态重排。
func (a *app) runningList(c *ui.Context) {
	entries := map[string]runEntry{}
	var natural []string // 照「项目」看法里的先后
	for _, p := range a.projects {
		ws := a.byProject[p.ID]
		for _, w := range ws {
			where := p.Name
			if len(ws) > 1 {
				label := cmp.Or(w.Shown, w.Name)
				if w.Kind == "main" {
					label = "主工作区"
				}
				where += " · " + label
			}
			for _, t := range a.sessionsInTree(agentSessions(w.Terminals)) {
				entries[t.ID] = runEntry{w: w, term: t, where: where}
				natural = append(natural, t.ID)
			}
		}
	}
	rank := func(id string) int { return runRank(a.sessionStatus(entries[id].term)) }
	if a.runOrder == nil {
		a.runOrder = slices.Clone(natural)
		slices.SortStableFunc(a.runOrder, func(x, y string) int { return rank(y) - rank(x) })
		if a.runOrder == nil {
			a.runOrder = []string{}
		}
	} else {
		a.runOrder = slices.DeleteFunc(a.runOrder, func(id string) bool { _, ok := entries[id]; return !ok })
		for _, id := range natural {
			if slices.Contains(a.runOrder, id) {
				continue
			}
			r := rank(id)
			a.runOrder = slices.Insert(a.runOrder, runInsertAt(a.runOrder, r, rank), id)
		}
	}
	if len(a.runOrder) == 0 {
		// 连不上常驻进程时上面或错误条已经写了，不说没有会话
		if !a.loaded || a.pollDown {
			return
		}
		ui.Text(c, "没有运行中的会话").FontSize(fsSmall).TextColor(c.Theme().TextMuted).Padding(6, navPad)
		return
	}
	cur := a.selectedSession(a.workspace(a.selected))
	for _, id := range a.runOrder {
		e := entries[id]
		a.navSession(c, e.w, e.term, id == cur, nil, nil, e.where)
	}
}

// runInsertAt 是新会话在 order 里的位置：排在最后一个状态不比它低的会话后面。已有会话状态变了不挪，
// 列表不一定按状态单调，不能找「第一个比它低的」：那样会插到降了档、还留在前面的会话前头。
func runInsertAt(order []string, r int, rank func(string) int) int {
	for i := len(order) - 1; i >= 0; i-- {
		if rank(order[i]) >= r {
			return i + 1
		}
	}
	return 0
}

// sessionsInTree 把一个工作区的会话按「项目」看法里的先后排：派发方后面紧跟它的 worker（逐层），和 sessionRows 一样。
func (a *app) sessionsInTree(sessions []terminalInfo) []terminalInfo {
	listed := map[string]bool{}
	for _, t := range sessions {
		listed[t.ID] = true
	}
	children := map[string][]terminalInfo{}
	var top []terminalInfo
	for _, t := range sessions {
		if coord := a.coordOf[t.ID]; coord != "" && coord != t.ID && listed[coord] {
			children[coord] = append(children[coord], t)
		} else {
			top = append(top, t)
		}
	}
	out := make([]terminalInfo, 0, len(sessions))
	seen := map[string]bool{}
	var walk func(list []terminalInfo)
	walk = func(list []terminalInfo) {
		for _, t := range list {
			// 数据成环时不重复
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			out = append(out, t)
			walk(children[t.ID])
		}
	}
	walk(top)
	return out
}
