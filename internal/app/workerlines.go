package app

import (
	"cmp"
	"maps"
	"math"
	"slices"

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
	const width = guideWidth
	top, bot, mid := r.Y-gap, r.Y+r.H, snap(r.Y+r.H/2)
	for k, more := range g[:d-1] {
		if more {
			x := snap(r.X + guideColumn(k))
			p.StrokePolyline([][2]float32{{x, top}, {x, bot}}, width, c)
		}
	}
	x := snap(r.X + guideColumn(d-1))
	end := r.X + guideEnd(d, hasStatus)
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

// drawGuideHead 在第 depth 层的派发方行 r 里画 worker 竖线的顶端：派发方没有状态图形时，
// 竖线从它的 agent 图标前横着出来，圆角拐下去，接上下一行的竖线（和跨工作区的连线一个画法）。
func drawGuideHead(p *ui.Painter, r ui.Rect, depth int, c ui.Color) {
	x, mid := snap(r.X+guideColumn(depth)), snap(r.Y+r.H/2)
	end := r.X + guideEnd(depth, false)
	radius := float32(rRow)
	pts := [][2]float32{{end, mid}}
	const steps = 8 // 四分之一圆分几段折线
	for i := 0; i <= steps; i++ {
		a := math.Pi*1.5 - float64(i)/steps*math.Pi/2
		pts = append(pts, [2]float32{x + radius + radius*float32(math.Cos(a)), mid + radius + radius*float32(math.Sin(a))})
	}
	pts = append(pts, [2]float32{x, r.Y + r.H})
	p.StrokePolyline(pts, guideWidth, c)
}

// snap 把线的坐标对到半个点上，1 点粗的线落在整像素里不发虚。
func snap(v float32) float32 { return float32(math.Round(float64(v)*2)) / 2 }

// guideEnd 是拐到第 d 层会话行的横线停在哪（相对行的左边）：状态图形前，没有状态时一直连到 agent 图标前。
func guideEnd(d int, hasStatus bool) float32 {
	if hasStatus {
		return float32(d)*navIndent + statusSlotLeft() - guideRoom
	}
	return float32(d+1)*navIndent + navPad - guideRoom
}

// linkEnd 是侧栏里画出来的一行会话，跨工作区连线的一头：depth 是它的嵌套层数，hasStatus 是状态槽里有没有图形。
type linkEnd struct {
	row       *ui.Handle
	depth     int
	hasStatus bool
}

// remoteLinks 是多工作区项目里派到同一项目别的工作区的 worker：两头都在项目的工作区列表里，
// 不画影子行，在列表左边对着项目图标中线的空当里竖着连过去，像一个「[」：最上一头圆角拐下、最下一头圆角拐出，中间的横着接上。
// 一头没画出来（收在折起的派发方下面）就不连。
type remoteLinks struct {
	order   []string            // 派发方，按侧栏里出现的先后
	workers map[string][]string // 派发方 → worker
	rows    map[string]linkEnd  // 这一帧画出来的会话行
	handles map[string]*ui.Handle
}

// handle 是 key 这一行一直复用的 Handle，见 app.linkHandles。
func (l *remoteLinks) handle(key string) *ui.Handle {
	h := l.handles[key]
	if h == nil {
		h = new(ui.Handle)
		l.handles[key] = h
	}
	return h
}

func (l *remoteLinks) link(coord, worker string) {
	if l.workers == nil {
		l.workers = map[string][]string{}
	}
	if _, ok := l.workers[coord]; !ok {
		l.order = append(l.order, coord)
	}
	l.workers[coord] = append(l.workers[coord], worker)
}

// row 记下这一帧画出来的会话行；l 为空（单工作区项目）时不记，也不绑定。
func (l *remoteLinks) row(id string, line ui.Element, depth int, hasStatus bool) {
	if l == nil {
		return
	}
	if l.rows == nil {
		l.rows = map[string]linkEnd{}
	}
	h := l.handle(id)
	line.Bind(h)
	l.rows[id] = linkEnd{row: h, depth: depth, hasStatus: hasStatus}
}

// linkHandleMap 是 app.linkHandles，没有时建一个。
func (a *app) linkHandleMap() map[string]*ui.Handle {
	if a.linkHandles == nil {
		a.linkHandles = map[string]*ui.Handle{}
	}
	return a.linkHandles
}

// pruneLinkHandles 删掉已经不在的会话和项目的 Handle，工作区变了时调。
func (a *app) pruneLinkHandles() {
	if len(a.linkHandles) == 0 {
		return
	}
	alive := map[string]bool{}
	for _, w := range a.workspaces {
		alive["list:"+w.ProjectID] = true
		for _, t := range w.Terminals {
			alive[t.ID] = true
		}
	}
	maps.DeleteFunc(a.linkHandles, func(k string, _ *ui.Handle) bool { return !alive[k] })
}

// linkLaneGap 是几个派发方各自的竖线之间的距离，靠左依次排开。
const linkLaneGap = 5

// draw 在工作区列表 list 画完子项后画连线，r 是列表的框。各行的位置按这一帧排好的框换算到列表里。
func (l *remoteLinks) draw(c *ui.Context, p *ui.Painter, r ui.Rect, list ui.Handle, color ui.Color) {
	origin := list.Bounds(c)
	lane := snap(r.X + navPad + iconSize/2)
	type end struct{ y, x float32 }
	for _, coord := range l.order {
		var ends []end
		for _, id := range append([]string{coord}, l.workers[coord]...) {
			e, ok := l.rows[id]
			if !ok {
				continue
			}
			b := e.row.Bounds(c)
			if b.H <= 0 {
				continue
			}
			x := r.X + b.X - origin.X
			ends = append(ends, end{y: snap(r.Y + b.Y - origin.Y + b.H/2), x: x + guideEnd(e.depth, e.hasStatus)})
		}
		if len(ends) < 2 {
			continue
		}
		slices.SortFunc(ends, func(a, b end) int { return cmp.Compare(a.y, b.y) })
		x := lane
		lane = max(lane-linkLaneGap, r.X+guideWidth)
		first, last := ends[0], ends[len(ends)-1]
		radius := float32(rRow)
		const steps = 8 // 四分之一圆分几段折线
		// ╭：从最上一头往左，圆角拐下来
		pts := [][2]float32{{first.x, first.y}}
		for i := 0; i <= steps; i++ {
			a := math.Pi*1.5 - float64(i)/steps*math.Pi/2
			pts = append(pts, [2]float32{x + radius + radius*float32(math.Cos(a)), first.y + radius + radius*float32(math.Sin(a))})
		}
		// ╰：竖线下到最下一头，圆角拐向右边
		for i := 0; i <= steps; i++ {
			a := math.Pi - float64(i)/steps*math.Pi/2
			pts = append(pts, [2]float32{x + radius + radius*float32(math.Cos(a)), last.y - radius + radius*float32(math.Sin(a))})
		}
		pts = append(pts, [2]float32{last.x, last.y})
		p.StrokePolyline(pts, guideWidth, color)
		for _, e := range ends[1 : len(ends)-1] {
			p.StrokePolyline([][2]float32{{x, e.y}, {e.x, e.y}}, guideWidth, color)
		}
	}
}
