package app

import (
	"math"
	"slices"
	"strings"

	"posse/internal/gitx"

	"github.com/egoist/mygo/ui"
)

// 提交图照 WebStorm（IntelliJ 社区版 platform/vcs-log）的做法：
//   - 布局序号（GraphLayoutBuilder）：从各分支头往下深度优先走，先走第一个父提交，一条路走到底换下一个序号；
//   - 每行的元素是这一行的提交和穿过这一行、看得见的线，按布局序号排（GraphElementComparatorByLayoutIndex），排第几就画在第几列；
//   - 一段线跨过 longEdgeSize 行时只在离两头 1 行以内看得见（PrintElementGeneratorImpl），断开处画箭头，点了跳到另一头；
//   - 线从这一行的元素中心连到相邻行的元素中心（SimpleGraphCellPainter），尺寸按 PaintParameters 换算。
const (
	longEdgeSize     = 30
	longEdgePartSize = 1
	maxGraphWidth    = 6 // 推荐宽度的上限：图窄时标题不跟着每行的线左右跳（GraphCommitCellUtil.MAX_GRAPH_WIDTH）
)

// graphEdge 是提交图的一段线：从提交 up 连到它的父提交 down（行号）；父提交还没读出来时 down 为 -1，target 是它的哈希。
type graphEdge struct {
	up, down int
	target   string
}

func (e graphEdge) loaded() bool { return e.down >= 0 }

// graphElem 是一行里的一个元素：提交（edge 为 -1）或一段线。
type graphElem struct{ node, edge int }

// graphLine 是一行里要画的半段线：从这一行第 from 列的中心，连到上一行或下一行第 to 列的中心。
// terminal 是长线断开的那一头：竖着画到行边附近、带箭头，点了跳到线的另一头。
type graphLine struct {
	from, to int
	down     bool
	arrow    bool
	terminal bool
	color    int
	edge     int
}

// graphRow 是一行的提交图。
type graphRow struct {
	node  int // 提交画在第几列
	color int
	lines []graphLine
	width float64 // 这一行要的列数（GraphCommitCellUtil.getGraphWidth 的 maxIndex + 1）
}

type commitGraph struct {
	rows     []graphRow
	edges    []graphEdge
	minWidth float64        // 推荐宽度：每行至少这么宽
	row      map[string]int // 提交哈希在第几行
}

// buildGraph 按读出来的提交（新的在前）建提交图。
func buildGraph(commits []gitx.CommitInfo) *commitGraph {
	n := len(commits)
	g := &commitGraph{rows: make([]graphRow, n)}
	if n == 0 {
		return g
	}
	row := make(map[string]int, n)
	for i, c := range commits {
		row[c.Hash] = i
	}
	g.row = row
	adj := make([][]int, n) // 每个提交连着的线：先连到子提交的，再按父提交的顺序
	down := make([][]int, n)
	hasUp := make([]bool, n)
	for i, c := range commits {
		for _, p := range c.Parents {
			e := graphEdge{up: i, down: -1, target: p}
			if j, ok := row[p]; ok && j > i {
				e.down = j
				hasUp[j] = true
			}
			g.edges = append(g.edges, e)
			down[i] = append(down[i], len(g.edges)-1)
		}
	}
	for id, e := range g.edges {
		if e.loaded() {
			adj[e.down] = append(adj[e.down], id)
		}
	}
	for i := range adj {
		adj[i] = append(adj[i], down[i]...)
	}

	li := layoutIndexes(n, hasUp, down, g.edges)
	elemLI := func(node int) int { return li[node] }
	// 线的布局序号取两头大的那个；颜色也跟着它（PrintElementPresentationManagerImpl.getColorId）
	// 还没读出来的父提交没有布局序号（WebStorm 一次读进全部提交，没有这种情况）：按出现的先后接着编号，
	// 一次合并很多分支时每条线颜色不同，不全是合并提交那一种
	unloaded := map[string]int{}
	next := slices.Max(li) + 1
	for _, e := range g.edges {
		if !e.loaded() {
			if _, ok := unloaded[e.target]; !ok {
				unloaded[e.target] = next
				next++
			}
		}
	}
	edgeColor := func(e graphEdge) int {
		if !e.loaded() {
			return max(li[e.up], unloaded[e.target])
		}
		return max(li[e.up], li[e.down])
	}
	compare2 := func(e graphEdge, node int) int {
		if !e.loaded() {
			return elemLI(e.up) - elemLI(node)
		}
		if m := max(elemLI(e.up), elemLI(e.down)); m != elemLI(node) {
			return m - elemLI(node)
		}
		return e.up - node
	}
	cmp := func(a, b graphElem) int {
		switch {
		case a.edge >= 0 && b.edge >= 0:
			e1, e2 := g.edges[a.edge], g.edges[b.edge]
			switch {
			case !e1.loaded():
				return -compare2(e2, e1.up)
			case !e2.loaded():
				return compare2(e1, e2.up)
			case e1.up == e2.up:
				if e1.down < e2.down {
					return -compare2(e2, e1.down)
				}
				return compare2(e1, e2.down)
			case e1.up < e2.up:
				return compare2(e1, e2.up)
			default:
				return -compare2(e2, e1.up)
			}
		case a.edge >= 0:
			return compare2(g.edges[a.edge], b.node)
		case b.edge >= 0:
			return -compare2(g.edges[b.edge], a.node)
		}
		return 0
	}
	visible := func(e graphEdge, r int) bool {
		return e.down-e.up < longEdgeSize || min(r-e.up, e.down-r) <= longEdgePartSize
	}

	// 每行看得见的元素：提交、穿过这一行的线、上一行连到没读出来的父提交的线
	elems := make([][]graphElem, n)
	var active []int
	for r := range n {
		if r > 0 {
			for _, id := range down[r-1] {
				if e := g.edges[id]; e.loaded() && e.down > r {
					active = append(active, id)
				}
			}
		}
		active = slices.DeleteFunc(active, func(id int) bool { return g.edges[id].down <= r })
		list := []graphElem{{node: r, edge: -1}}
		for _, id := range active {
			if visible(g.edges[id], r) {
				list = append(list, graphElem{node: -1, edge: id})
			}
		}
		if r > 0 {
			for _, id := range down[r-1] {
				if !g.edges[id].loaded() {
					list = append(list, graphElem{node: -1, edge: id})
				}
			}
		}
		slices.SortStableFunc(list, cmp)
		elems[r] = list
	}
	// 每行元素的列号；一次建好，每段线找相邻行的位置时不用逐个扫
	at := make([]map[graphElem]int, n)
	for r, list := range elems {
		at[r] = make(map[graphElem]int, len(list))
		for p, el := range list {
			at[r][el] = p
		}
	}
	pos := func(r int, el graphElem) int {
		if r < 0 || r >= n {
			return -1
		}
		if p, ok := at[r][el]; ok {
			return p
		}
		return -1
	}
	// 线在相邻一行的位置：那一行里有这段线就是它，否则是线那一头的提交（就在那一行时）
	endPos := func(r int, id int, up bool) int {
		if p := pos(r, graphElem{node: -1, edge: id}); p >= 0 {
			return p
		}
		e := g.edges[id]
		end := e.down
		if up {
			end = e.up
		}
		if end == r {
			return pos(r, graphElem{node: r, edge: -1})
		}
		return -1
	}
	// 箭头：长线在离上头 1 行处朝下，离下头 1 行处朝上；连到没读出来的父提交的线在下一行朝下
	arrowAt := func(id, r int) (down, up bool) {
		e := g.edges[id]
		if !e.loaded() {
			return e.up == r-1, false
		}
		if e.down-e.up >= longEdgeSize {
			return r-e.up == longEdgePartSize, e.down-r == longEdgePartSize
		}
		return false, false
	}
	for r := range n {
		gr := &g.rows[r]
		for p, el := range elems[r] {
			if el.edge < 0 {
				gr.node, gr.color = p, li[r]
				for _, id := range adj[r] {
					col := edgeColor(g.edges[id])
					if d := endPos(r+1, id, false); d >= 0 {
						gr.lines = append(gr.lines, graphLine{from: p, to: d, down: true, color: col, edge: id})
					}
					if u := endPos(r-1, id, true); u >= 0 {
						gr.lines = append(gr.lines, graphLine{from: p, to: u, color: col, edge: id})
					}
				}
				continue
			}
			id := el.edge
			col := edgeColor(g.edges[id])
			ad, au := arrowAt(id, r)
			if d := endPos(r+1, id, false); d >= 0 {
				gr.lines = append(gr.lines, graphLine{from: p, to: d, down: true, arrow: ad, color: col, edge: id})
			} else if ad {
				gr.lines = append(gr.lines, graphLine{from: p, to: p, down: true, arrow: true, terminal: true, color: col, edge: id})
			}
			if u := endPos(r-1, id, true); u >= 0 {
				gr.lines = append(gr.lines, graphLine{from: p, to: u, arrow: au, color: col, edge: id})
			} else if au {
				gr.lines = append(gr.lines, graphLine{from: p, to: p, arrow: true, terminal: true, color: col, edge: id})
			}
		}
		w := float64(gr.node)
		for _, l := range gr.lines {
			w = max(w, float64(l.from), float64(l.from+l.to)/2)
		}
		gr.width = w + 1
	}
	g.minWidth = math.Min(maxGraphWidth, recommendedWidth(g.rows))
	return g
}

// layoutIndexes 给每个提交分布局序号（GraphLayoutBuilder.build）：分支头按行号排，从每个头往下深度优先，
// 先走第一个还没分到序号的父提交；走到底（没有还没分到的父提交）且是第一次到这个提交时序号加一。
func layoutIndexes(n int, hasUp []bool, down [][]int, edges []graphEdge) []int {
	li := make([]int, n)
	cur := 1
	var stack []int
	for head := range n {
		if hasUp[head] || li[head] != 0 {
			continue
		}
		stack = append(stack[:0], head)
		for len(stack) > 0 {
			node := stack[len(stack)-1]
			first := li[node] == 0
			if first {
				li[node] = cur
			}
			next := -1
			for _, id := range down[node] {
				if e := edges[id]; e.loaded() && li[e.down] == 0 {
					next = e.down
					break
				}
			}
			if next < 0 {
				if first {
					cur++
				}
				stack = stack[:len(stack)-1]
				continue
			}
			stack = append(stack, next)
		}
	}
	return li
}

// recommendedWidth 是推荐的最小宽度（PrintElementGeneratorImpl.calculateRecommendedWidth 的意思）：
// 前 2 万行宽度的加权平均加一个标准差，越往下的行权重越小。
func recommendedWidth(rows []graphRow) float64 {
	n := min(len(rows), 20000)
	if n <= 1 {
		return float64(n)
	}
	const k = 0.1
	var sum, sq float64
	for i := range n {
		w := math.Floor(rows[i].width)
		weight := 2 / (float64(n) * (k + 1)) * (1 + (k-1)*float64(i)/float64(n-1))
		sum += w * weight
		sq += w * w * weight
	}
	return math.Round(sum + math.Sqrt(max(sq-sum*sum, 0)))
}

// graphMetrics 是提交图的尺寸：PaintParameters 按行高 22 定的值，不随日志 32 的行高放大（放大后线和点显粗），
// 只跟着界面字号缩放；再照 SimpleGraphCellPainter 对齐到奇数个物理像素（2 倍屏上线宽 3、圆点直径 15），线不发虚。
type graphMetrics struct {
	rowH, elem, radius, line, selLine, arrow, gap float32
}

func graphSizes() graphMetrics {
	k := sz(22) / 22
	line := oddPx(1.5 * k)
	diameter := oddPx(8 * k)
	return graphMetrics{rowH: logRowH(), elem: oddPx(16 * k), radius: diameter / 2, line: line,
		selLine: max(oddPx(2.5*k), line+1), arrow: 0.3 * sz(22), gap: 2 * k}
}

// oddPx 按 2 倍屏往下取到奇数个物理像素（PaintUtil.alignToInt(…, FLOOR, ODD)），至少一个。
func oddPx(v float32) float32 {
	px := int(v * 2)
	if px%2 == 0 {
		px--
	}
	return float32(max(px, 1)) / 2
}

// widthPx 是一行提交图占的宽度：这一行的元素和推荐宽度取大的，再加和文字的间隙。
func (g *commitGraph) widthPx(r int) float32 {
	m := graphSizes()
	w := max(g.rows[r].width, g.minWidth)
	return float32(w)*m.elem + m.gap
}

// arrowTarget 是点箭头要跳到的提交：朝下的是线下头的父提交，朝上的是上头的子提交（VisibleGraphImpl.performArrowAction）。
func (g *commitGraph) arrowTarget(commits []gitx.CommitInfo, l graphLine) string {
	e := g.edges[l.edge]
	switch {
	case !l.down:
		return commits[e.up].Hash
	case e.loaded():
		return commits[e.down].Hash
	}
	return e.target
}

// graphCell 画一行的提交图（SimpleGraphCellPainter）。带箭头的线能点：悬停时加粗描边、指针变手、提示要跳到的提交，
// 点了选中那个提交并滚过去，还没读出来的接着往下读。
func (a *app) graphCell(c *ui.Context, v *filesView, d *fileDoc, i int) ui.Element {
	ld := d.log
	m := graphSizes()
	t := c.Theme()
	g := ld.graph
	if g == nil || i >= len(g.rows) {
		// 没有提交图（按作者、文字筛选）时只画点
		return ui.Box(c).Width(m.elem + m.gap).Height(m.rowH).Shrink(0).Draw(func(p *ui.Painter, rect ui.Rect) {
			cx, cy := rect.X+m.elem/2, rect.Y+m.rowH/2
			p.Fill(ui.Rect{X: cx - m.radius, Y: cy - m.radius, W: 2 * m.radius, H: 2 * m.radius}, laneColor(t, 0), m.radius)
		})
	}
	r := g.rows[i]
	ci := ld.commits[i]
	head := strings.HasPrefix(ci.Refs, "HEAD") || strings.Contains(ci.Refs, ", HEAD")
	// 窗口窄时图和标题一起让，放不下的线裁掉（WebStorm 的图只在提交那一列里画），作者、时间列不被挤走；提交点总在
	box := ui.Box(c.Key("graph")).Width(g.widthPx(i)).MinWidth(min(g.widthPx(i), float32(r.node+1)*m.elem+m.gap)).
		Height(m.rowH).Shrink(1).Clip()
	hot := -1 // 悬停的箭头线
	box.Children(func() {
		for n, l := range r.lines {
			if !l.terminal {
				continue
			}
			x := float32(l.from)*m.elem + m.elem/2
			y := float32(0)
			if l.down {
				y = m.rowH / 2
			}
			target := g.arrowTarget(ld.commits, l)
			tip := L(c).LogGraphJumpToHash(shortHash(target))
			if k, ok := g.row[target]; ok && k < len(ld.commits) {
				tc := ld.commits[k]
				tip = L(c).LogGraphJumpTo(shortSubject(tc.Subject), tc.Author, commitStamp(L(c), tc.Time))
			}
			hit := ui.Box(c.Key(n)).Absolute().Left(x-m.elem/2).Top(y).Size(m.elem, m.rowH/2).Role(ui.RoleButton).Label(tip).Tooltip(tip)
			if hit.Hovered() {
				hot = n
			}
			if hit.Clicked() {
				a.jumpToCommit(v, d, target)
			}
		}
	})
	box.Draw(func(p *ui.Painter, rect ui.Rect) {
		// 斜线要画到相邻一行的中心，靠这一格裁成一半；格子被挤窄时右边的线也裁掉，不压到标题上
		p.Clip(rect, 0, func() { drawGraphRow(p, rect, r, m, t, hot, head) })
	})
	return box
}

// drawGraphRow 画一行的线和提交点；hot 是悬停的箭头线（-1 为没有）。
func drawGraphRow(p *ui.Painter, rect ui.Rect, r graphRow, m graphMetrics, t *ui.Theme, hot int, head bool) {
	x := func(pos int) float32 { return rect.X + float32(pos)*m.elem + m.elem/2 }
	mid := rect.Y + m.rowH/2
	paint := func(l graphLine, col ui.Color, width float32) {
		x1, y1 := x(l.from), mid
		var x2, y2, ax, ay float32
		if l.from == l.to {
			gap := float32(0)
			if l.terminal {
				gap = m.radius/2 + 1
			}
			x2, y2 = x1, rect.Y+gap
			if l.down {
				y2 = rect.Y + m.rowH - gap
			}
			ax, ay = x2, y2
		} else {
			// 斜线画到相邻一行的中心，两行各画一半（被格子裁掉），接得上
			x2, y2 = x(l.to), mid-m.rowH
			if l.down {
				y2 = mid + m.rowH
			}
			ax, ay = (x1+x2)/2, (y1+y2)/2
		}
		var path ui.Path
		path.MoveTo(x1, y1).LineTo(x2, y2)
		if l.arrow {
			for _, s := range []float32{1, -1} {
				wx, wy := arrowWing(x1, y1, ax, ay, s, m.arrow)
				path.MoveTo(ax, ay).LineTo(wx, wy)
			}
		}
		p.StrokePath(&path, width, col)
	}
	for n, l := range r.lines {
		if n != hot {
			paint(l, laneColor(t, l.color-1), m.line)
		}
	}
	// 悬停的那段先描一圈正文色、再画本色（选中的元素，MARK_COLOR）
	if hot >= 0 {
		paint(r.lines[hot], t.Text, m.selLine)
		paint(r.lines[hot], laneColor(t, r.lines[hot].color-1), m.line)
	}
	cx := x(r.node)
	col := laneColor(t, r.color-1)
	circle := func(rad float32, c ui.Color) {
		p.Fill(ui.Rect{X: cx - rad, Y: mid - rad, W: 2 * rad, H: 2 * rad}, c, rad)
	}
	if head {
		// HEAD 所在的提交：外圈、底色环、内点（HeadNodePainter）
		outer, delta := m.radius+2*m.radius/4, 2*m.radius/4
		circle(outer, col)
		circle(outer-delta, t.Background)
		circle(outer-2*delta, col)
	} else {
		circle(m.radius, col)
	}
}

// arrowWing 是箭头的一翼：从箭尖 (ax, ay) 朝线的来处 (x1, y1) 转过约 33°（cos² = 0.7），长 length。
func arrowWing(x1, y1, ax, ay, side, length float32) (float32, float32) {
	cos, sin := float32(math.Sqrt(0.7)), side*float32(math.Sqrt(0.3))
	dx, dy := x1-ax, y1-ay
	d := float32(math.Hypot(float64(dx), float64(dy)))
	if d == 0 {
		return ax, ay
	}
	sx, sy := length*dx/d, length*dy/d
	return ax + sx*cos - sy*sin, ay + sx*sin + sy*cos
}

func laneColor(t *ui.Theme, i int) ui.Color { return dataColor(t, max(i, 0)) }

func shortHash(h string) string { return h[:min(len(h), 7)] }

// shortSubject 把提示里的标题截到 50 个字（StringUtil.shortenTextWithEllipsis(subject, 50, 0, "...")）。
func shortSubject(s string) string {
	r := []rune(s)
	if len(r) <= 50 {
		return s
	}
	return string(r[:47]) + "..."
}

// jumpToCommit 选中提交并滚到它那一行；还没读出来时接着往下读，读到为止（读完了还没有就作罢）。
func (a *app) jumpToCommit(v *filesView, d *fileDoc, hash string) {
	ld := d.log
	if k := slices.IndexFunc(ld.commits, func(x gitx.CommitInfo) bool { return x.Hash == hash }); k >= 0 {
		ld.jump = ""
		a.selectCommit(ld.root, &ld.commit, ld.commits[k])
		ld.list.ScrollTo(k, ui.Center)
		ld.focusList = true
		return
	}
	if !ld.more {
		ld.jump = ""
		return
	}
	ld.jump = hash
	if !ld.loading {
		a.loadLog(v, d, true)
	}
}
