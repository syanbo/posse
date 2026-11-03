package app

import (
	"cmp"
	"slices"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/locales"
)

// sidebarByStatus 是侧栏的「运行中」看法：所有项目里的 agent 会话平铺成一列，按状态排。
const sidebarByStatus = "status"

// sidebarGroupSwitch 是项目列表上面的小标题：左边写「项目」，右边是「只看运行中」的筛选开关，
// 开着时列表换成所有项目里 agent 会话的平铺，标题改写「运行中」。
func (a *app) sidebarGroupSwitch(c *ui.Context) {
	on := a.state.SidebarGroup == sidebarByStatus
	title := L(c).RunningProjects()
	if on {
		title = L(c).RunningTitle()
	}
	// 漏斗和侧栏顶栏的开关、底栏的齿轮、项目行尾的展开箭头竖着对齐（见 sideIconCenter）
	right := sideIconCenter() - compactBox()/2
	ui.Row(c).Padding(6, right, 0, trafficLightX).AlignItems(ui.Center).Shrink(0).Children(func() {
		ui.Text(c, title).FontSize(fsSmall).TextColor(c.Theme().TextMuted)
		ui.Spacer(c)
		if toggleIconButton(c, "sidebar-group", iconFilter, L(c).RunningFilterTip(), on) {
			a.state.SidebarGroup = ""
			if !on {
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

// workspaceWhere 是会话所在的「项目 · 工作区」，项目只有一个工作区时只写项目名。
func workspaceWhere(l locales.Messages, p project, ws []workspace, w workspace) string {
	if len(ws) <= 1 {
		return p.Name
	}
	label := cmp.Or(w.Shown, w.Name)
	if w.Kind == "main" {
		label = l.RunningMainWorkspace()
	}
	return p.Name + " · " + label
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
			where := workspaceWhere(L(c), p, ws, w)
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
		ui.Text(c, L(c).RunningEmpty()).FontSize(fsSmall).TextColor(c.Theme().TextMuted).Padding(6, navPad)
		return
	}
	cur := a.selectedSession(a.workspace(a.selected))
	for _, id := range a.runOrder {
		e := entries[id]
		a.navSession(c, e.w, e.term, id == cur, nil, nil, e.where, nil, false)
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

// sessionsInTree 把一个工作区的会话按「项目」看法里的先后排：派发方后面紧跟它的 worker（逐层），分层按 sessionTree。
// 派到别的工作区的 worker 留在它自己的工作区，这里不列影子。
func (a *app) sessionsInTree(sessions []terminalInfo) []terminalInfo {
	top, children := a.sessionTree(sessions)
	out := make([]terminalInfo, 0, len(sessions))
	seen := map[string]bool{}
	var walk func(list []sessionNode)
	walk = func(list []sessionNode) {
		for _, node := range list {
			t := node.term
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
