package app

import (
	"cmp"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
)

// 侧栏顶上的磁贴照 Arc 的置顶网址：任务、历史、技能、手机、聊天和固定的工作区排在一起，一行四块，最多十二块（三行），
// 都能拖动换位置；项目树里的工作区拖上来就固定，固定的拖回项目列表就取消。
const (
	topTilesPerRow = 4
	topTilesMax    = 12
	// 照 Arc，磁贴离侧栏左右两边、顶上和磁贴之间横竖的间隔都一样
	tileGap = 8
)

// globalTiles 是不能取消的全局页面，ui.json 里没记顺序时按这个排。
var globalTiles = []string{taskBoardID, historyBoardID, skillsBoardID, mobileBoardID, chatBoardID}

// tileDrag 是拖动中的顶上磁贴，wsDrag 是从项目树拖出来的工作区（项目下面的工作区行）。
type (
	tileDrag struct{ id string }
	wsDrag   struct{ id string }
)

// topDrag 是能落到顶上的东西：磁贴自己（换位置），工作区行，和项目（单行的项目、多工作区项目的标题行，固定它的主工作区）。
// 顶上只认这一个类型：一个元素只能接一种拖动的值，接两种时后一种会顶掉前一种。
type topDrag interface {
	// topTarget 是落下后占位的 ID，own 表示是顶上已有的磁贴在换位置
	topTarget(a *app) (id string, own bool)
}

func (d tileDrag) topTarget(*app) (string, bool)      { return d.id, true }
func (d wsDrag) topTarget(*app) (string, bool)        { return d.id, false }
func (d projectDrag) topTarget(a *app) (string, bool) { return a.projectMain(d.id), false }

// topOrder 是磁贴的顺序：ui.json 记的，补上没记到的全局页面。固定过、现在不在了的工作区在常驻进程连上以后去掉，
// 没连上时留着（交接时可能有一次轮询的列表不全）。
func (a *app) topOrder() []string {
	order := make([]string, 0, topTilesMax)
	for _, id := range a.state.TopTiles {
		// 聊天工作区已经有「聊天」磁贴：改聊天目录前固定过它的，不再单独画一块
		if a.isChat(id) {
			continue
		}
		if slices.Contains(globalTiles, id) || !a.loaded || a.workspace(id) != nil {
			order = append(order, id)
		}
	}
	for _, id := range globalTiles {
		if !slices.Contains(order, id) {
			order = append(order, id)
		}
	}
	return order
}

func (a *app) pinned(id string) bool { return slices.Contains(a.state.TopTiles, id) }

func (a *app) canPin() bool { return len(a.topOrder()) < topTilesMax }

// pinnable 是工作区能不能固定到顶上：聊天工作区已经有「聊天」磁贴，不再单独固定。
func (a *app) pinnable(id string) bool { return id != "" && !a.pinned(id) && !a.isChat(id) }

// pin 把工作区固定到顶上，排在最后。
func (a *app) pin(id string) {
	if !a.pinnable(id) || !a.canPin() {
		return
	}
	a.state.TopTiles = append(a.topOrder(), id)
	a.saveState()
}

func (a *app) unpin(id string) {
	a.state.TopTiles = slices.DeleteFunc(a.topOrder(), func(x string) bool { return x == id })
	a.saveState()
}

// topTileMotion 是磁贴挪位置、网格变高的动画：照 Arc，不回弹、一下就到。
var topTileMotion = ui.Animation{Spring: ui.SpringOptions{Duration: 320 * time.Millisecond}}

// topTiles 画顶上的磁贴。磁贴按格子自己摆（绝对定位），位置跟着格子动画过去：拖动时指针停在哪一格，
// 那一格就空出来、后面的磁贴往后让，松手就落在那里。拖的是已有的磁贴时，它原来的格子跟着收拢；
// 从项目树拖工作区进来、还放得下时，空出来的那格描一圈强调色。
func (a *app) topTiles(c *ui.Context) {
	order := a.topOrder()
	var shown []string
	for _, id := range order {
		if (id == taskBoardID && len(a.projects) == 0) || (!slices.Contains(globalTiles, id) && a.workspace(id) == nil) {
			continue
		}
		shown = append(shown, id)
	}
	ui.Column(c).Padding(tileGap, tileGap, 4, tileGap).Shrink(0).Children(func() {
		area := ui.Box(c.Key("top-tiles")).Shrink(0)
		// 先收落下的，再按新顺序排：否则松手这一帧会先按旧顺序摆一遍，磁贴来回跳
		if d, ok := ui.Drop[topDrag](area); ok {
			shown = a.dropTile(d, shown, order)
			c.AnimationFrame()
		}
		w := area.LayoutBounds().W
		cw, ch := (w-tileGap*(topTilesPerRow-1))/topTilesPerRow, tileHeight()
		hole, moving, incoming := -1, "", ""
		if d, over := ui.DragOver[topDrag](area); over {
			id, own := d.topTarget(a)
			switch {
			case own:
				moving = id
			case a.pinnable(id) && len(order) < topTilesMax:
				incoming = id
			}
			if x, y, _ := area.PointerPosition(); (moving != "" || incoming != "") && cw > 0 {
				col := min(max(int(x/(cw+tileGap)), 0), topTilesPerRow-1)
				hole = max(int(y/(ch+tileGap)), 0)*topTilesPerRow + col
			}
		}
		list := shown
		if moving != "" && hole >= 0 {
			list = slices.DeleteFunc(slices.Clone(shown), func(id string) bool { return id == moving })
		}
		hole = min(hole, len(list))
		a.topSlot = hole + 1
		cells := len(list)
		if hole >= 0 {
			cells++
		}
		rows := (cells + topTilesPerRow - 1) / topTilesPerRow
		area.Height(area.AnimateTo("h", float32(rows)*ch+float32(max(rows-1, 0))*tileGap, topTileMotion))
		if w <= 0 {
			// 第一帧还不知道宽度，量好了下一帧再摆
			c.AnimationFrame()
			return
		}
		place := func(e ui.Element, slot int) ui.Element {
			x := float32(slot%topTilesPerRow) * (cw + tileGap)
			y := float32(slot/topTilesPerRow) * (ch + tileGap)
			return e.Absolute().Left(e.AnimateTo("x", x, topTileMotion)).Top(e.AnimateTo("y", y, topTileMotion)).Width(cw).Height(ch)
		}
		area.Children(func() {
			slot := 0
			for _, id := range list {
				if slot == hole {
					slot++
				}
				a.placedTile(c, place, id, slot, false)
				slot++
			}
			switch {
			case hole < 0:
			case moving != "":
				// 拖着的磁贴本身留在空格上、看不见：它是拖动的来源，不能不画
				a.placedTile(c, place, moving, hole, true)
			default:
				place(ui.Row(c.Key("top-hole")), hole).Children(func() { dropSlot(c) })
			}
		})
	})
}

// placedTile 把一块磁贴摆到格子 slot 上，按住能拖；hidden 时是正拖着、空出来的那格。
func (a *app) placedTile(c *ui.Context, place func(ui.Element, int) ui.Element, id string, slot int, hidden bool) {
	cell := place(ui.Row(c.Key("cell-"+id)), slot)
	if hidden {
		cell.Opacity(0)
	}
	cell.Children(func() {
		tile := a.topTile(c, id)
		tile.Drag(tileDrag{id})
		if tile.Dragging() && !hidden && !slices.Contains(globalTiles, id) {
			// 固定的工作区拖出了顶上（拖回项目列表就取消固定），原处变淡；全局页面拖不走，不变淡
			tile.Opacity(0.4)
		}
	})
}

// dropTile 把落在顶上的磁贴或工作区放进上一帧空出来的格子，返回新的显示顺序。
func (a *app) dropTile(d topDrag, shown, order []string) []string {
	id, own := d.topTarget(a)
	hole := a.topSlot - 1
	if id == "" || hole < 0 || (!own && (!a.pinnable(id) || len(order) >= topTilesMax)) {
		return shown
	}
	list := slices.DeleteFunc(slices.Clone(shown), func(x string) bool { return x == id })
	list = slices.Insert(list, min(hole, len(list)), id)
	// 不显示的（没有项目时的任务、常驻进程还没报上来的工作区）插回原来的位置，顺序不丢
	next := slices.Clone(list)
	for i, x := range order {
		if !slices.Contains(next, x) {
			next = slices.Insert(next, min(i, len(next)), x)
		}
	}
	a.state.TopTiles = next
	a.saveState()
	return list
}

// projectMain 是项目的主工作区（项目树里排第一个的），拖项目上来时固定它。
func (a *app) projectMain(projectID string) string {
	if ws := a.byProject[projectID]; len(ws) > 0 {
		return ws[0].ID
	}
	return ""
}

// topTile 画一块磁贴：全局页面或固定的工作区。
func (a *app) topTile(c *ui.Context, id string) ui.Element {
	switch id {
	case chatBoardID:
		return a.chatSidebarItem(c)
	case taskBoardID:
		return a.taskSidebarItem(c)
	case historyBoardID:
		return a.historySidebarItem(c)
	case skillsBoardID:
		return a.skillsSidebarItem(c)
	case mobileBoardID:
		return a.mobileSidebarItem(c)
	}
	return a.pinnedTile(c, *a.workspace(id))
}

// dropSlot 是拖工作区进来时空出来的格子：强调色描边加淡底。
func dropSlot(c *ui.Context) {
	t := c.Theme()
	ui.Box(c).Height(tileHeight()).Grow(1).Basis(0).Radius(rControl).Background(colors(t).tint).
		BorderWidth(focusRing, focusRing, focusRing, focusRing).BorderColor(t.Accent)
}

// pinnedTile 是一个固定的工作区：主工作区用项目的图标和名字，别的用工作区自己的。
func (a *app) pinnedTile(c *ui.Context, w workspace) ui.Element {
	label, glyph := cmp.Or(w.Shown, w.Name), a.wsIcon(&w)
	icon := func() { ui.Icon(c, glyph).FontSize(iconSize).TextColor(c.Theme().Accent).Shrink(0) }
	if w.Kind == "main" {
		label = w.Project
		icon = func() { a.projectGlyph(c, a.projIcons[w.ProjectID], glyph) }
	}
	tile := a.workspaceTile(c, "pin-"+w.ID, &w, icon, label)
	if tile.Clicked() {
		a.selected = w.ID
	}
	return tile
}

// workspaceTile 是顶上代表一个工作区的磁贴（固定的工作区、「聊天」）：选中这个工作区时高亮，
// 右上角是它的终端里最要紧的状态，右键是工作区菜单；w 为 nil 时（聊天目录还不是项目）只有图标和名字。
// 点了做什么由调用方决定。
func (a *app) workspaceTile(c *ui.Context, key string, w *workspace, icon func(), label string) ui.Element {
	var badge func()
	if w != nil {
		// 角标只提醒要留意的：看过的完成是平时的样子，每块都挂着反而看不出哪块有事
		if _, status := a.terminalSummary(w.Terminals); status != "" && status != agent.StatusIdle {
			badge = func() { statusGlyph(c, status, smallIcon()) }
		}
	}
	// 不登记进方向键的顺序：项目树里还有它那一行，登记两次 navCur 会跳到后一处
	tile := sideTile(c, key, w != nil && a.selected == w.ID, icon, label, badge)
	if w != nil {
		contextMenu(c, tile, func(m *popMenu) { a.workspaceMenu(c, m, *w) })
	}
	return tile
}
