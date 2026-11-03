package app

import (
	"math"

	"github.com/egoist/mygo/ui"
)

// sessionNode 是侧栏会话树里的一行。home 不为空时是派到别的工作区的 worker 的影子行，home 是它真正所在的工作区。
type sessionNode struct {
	term terminalInfo
	home *workspace
}

// current 是这一行的终端。影子行的表是轮询时建的，终端的字段之后可能又在原地改过，从它真正所在的工作区取最新的。
func (n sessionNode) current() terminalInfo {
	if n.home != nil {
		for _, t := range n.home.Terminals {
			if t.ID == n.term.ID {
				return t
			}
		}
	}
	return n.term
}

// sessionTree 把一个工作区的会话分成顶层和派发方下面的 worker：派发方不在这个工作区（别的工作区、用户派发）的算顶层。
// 侧栏的会话树（sessionRows）和「运行中」的先后（sessionsInTree）都按它排。
func (a *app) sessionTree(sessions []terminalInfo) (top []sessionNode, children map[string][]sessionNode) {
	listed := make(map[string]bool, len(sessions))
	for _, t := range sessions {
		listed[t.ID] = true
	}
	children = map[string][]sessionNode{}
	for _, t := range sessions {
		if coord := a.coordOf[t.ID]; coord != "" && coord != t.ID && listed[coord] {
			children[coord] = append(children[coord], sessionNode{term: t})
		} else {
			top = append(top, sessionNode{term: t})
		}
	}
	return top, children
}

// indexRemoteWorkers 记下每个派发方派到别的工作区的 worker，按侧栏里项目、工作区、会话的先后排，侧栏画影子行时查表。
// 工作区和任务列表任一变了都要重算。
func (a *app) indexRemoteWorkers() {
	a.remoteWorkers = nil
	if len(a.coordOf) == 0 {
		return
	}
	home := map[string]string{}
	for _, w := range a.workspaces {
		for _, t := range w.Terminals {
			if t.Agent != "" && (t.Running || t.Lost) {
				home[t.ID] = w.ID
			}
		}
	}
	for _, p := range a.projects {
		for _, w := range a.byProject[p.ID] {
			ws := a.workspace(w.ID)
			for _, t := range agentSessions(w.Terminals) {
				coord := a.coordOf[t.ID]
				if at, ok := home[coord]; !ok || at == w.ID {
					continue
				}
				if a.remoteWorkers == nil {
					a.remoteWorkers = map[string][]sessionNode{}
				}
				a.remoteWorkers[coord] = append(a.remoteWorkers[coord], sessionNode{term: t, home: ws})
			}
		}
	}
}

// treeGuide 是侧栏 worker 行左边的连接线，一层嵌套一列，对应它上面那一层的派发方；顶层会话没有。
// 每列记这一层在本行之后还有没有 worker：最后一列决定画 ├ 还是 ╰，其余列决定竖线要不要穿过本行。
type treeGuide []bool

// guideColumn 是第 k 层派发方状态槽的中线（相对行的左边），竖线走这里。
func guideColumn(k int) float32 {
	return float32(k)*navIndent + statusSlotLeft() + smallIcon()/2
}

// drawGuide 在会话行 r 里画连接线。竖线往上伸出 gap，接上上一行（列里的行间隔 gap），下面的间隔由下一行接；
// 拐过来的横线停在状态图形前，没有状态时一直连到 agent 图标前：不管 worker 什么状态，线都接到它身上。
func drawGuide(p *ui.Painter, r ui.Rect, g treeGuide, hasStatus bool, gap float32, c ui.Color) {
	d := len(g)
	if d == 0 {
		return
	}
	const width, room = guideWidth, guideRoom
	snap := func(v float32) float32 { return float32(math.Round(float64(v)*2)) / 2 }
	top, bot, mid := r.Y-gap, r.Y+r.H, snap(r.Y+r.H/2)
	for k, more := range g[:d-1] {
		if more {
			x := snap(r.X + guideColumn(k))
			p.StrokePolyline([][2]float32{{x, top}, {x, bot}}, width, c)
		}
	}
	x := snap(r.X + guideColumn(d-1))
	end := r.X + float32(d)*navIndent + statusSlotLeft() - room
	if !hasStatus {
		end = r.X + float32(d+1)*navIndent + navPad - room
	}
	if g[d-1] {
		// ├：竖线穿过本行，横线从中间伸出去
		p.StrokePolyline([][2]float32{{x, top}, {x, bot}}, width, c)
		p.StrokePolyline([][2]float32{{x, mid}, {end, mid}}, width, c)
		return
	}
	// ╰：竖线下来到中间，圆角拐向右边
	radius := float32(rRow)
	pts := [][2]float32{{x, top}}
	const steps = 8 // 四分之一圆分几段折线
	for i := 0; i <= steps; i++ {
		a := float64(i) / steps * math.Pi / 2
		pts = append(pts, [2]float32{x + radius - radius*float32(math.Cos(a)), mid - radius + radius*float32(math.Sin(a))})
	}
	pts = append(pts, [2]float32{end, mid})
	p.StrokePolyline(pts, width, c)
}
