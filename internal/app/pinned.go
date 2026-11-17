package app

import (
	"cmp"
	"slices"

	"github.com/egoist/mygo/ui"
)

const topTilesMax = 12

var globalTiles = []string{discBoardID, chatBoardID, taskBoardID, historyBoardID, skillsBoardID, mobileBoardID}

type (
	tileDrag struct{ id string }
	wsDrag   struct{ id string }
)

type topDrag interface {
	topTarget(a *app) (id string, own bool)
}

func (d tileDrag) topTarget(*app) (string, bool)      { return d.id, true }
func (d wsDrag) topTarget(*app) (string, bool)        { return d.id, false }
func (d projectDrag) topTarget(a *app) (string, bool) { return a.projectMain(d.id), false }

// 旧磁贴只拆一次，保留两类各自的相对顺序；离线时不能根据尚未取得的工作区列表删数据。
func migrateNavigation(s *uiState) {
	for _, id := range s.TopTiles {
		dst := &s.PinnedWorkspaces
		if slices.Contains(globalTiles, id) {
			dst = &s.NavigationOrder
		}
		if id != "" && !slices.Contains(*dst, id) {
			*dst = append(*dst, id)
		}
	}
	s.TopTiles = nil
}

func (a *app) topOrder() []string {
	migrateNavigation(&a.state)
	var order []string
	for _, id := range a.state.PinnedWorkspaces {
		// 未连接时保留旧记录；成功取得列表后，不让已消失的工作区占用名额。
		// 查询本身不改存盘数据，下一次显式固定/取消固定时才写回有效顺序。
		if !a.isChat(id) && !slices.Contains(order, id) && (!a.loaded || a.workspace(id) != nil) {
			order = append(order, id)
		}
	}
	return order
}

func (a *app) pinned(id string) bool { return slices.Contains(a.topOrder(), id) }
func (a *app) canPin() bool          { return len(a.topOrder()) < topTilesMax }
func (a *app) pinnable(id string) bool {
	return id != "" && !a.pinned(id) && !a.isChat(id) && !slices.Contains(globalTiles, id)
}

func (a *app) pin(id string) {
	if !a.pinnable(id) || !a.canPin() {
		return
	}
	a.state.PinnedWorkspaces = append(a.topOrder(), id)
	a.saveState()
}

func (a *app) unpin(id string) {
	a.state.PinnedWorkspaces = slices.DeleteFunc(a.topOrder(), func(x string) bool { return x == id })
	a.saveState()
}

// 固定区用紧凑行，不再由网格决定侧栏宽度；空区也接收从项目树拖来的工作区。
func (a *app) topTiles(c *ui.Context) {
	order := a.topOrder()
	var shown []string
	for _, id := range order {
		if a.workspace(id) != nil {
			shown = append(shown, id)
		}
	}
	ui.Scroll(c.Key("pinned-scroll")).Height(min(float32(max(len(shown), 1))*pinnedRowHeight()+8, pinnedMaxHeight())).Shrink(0).Children(func() {
		area := ui.Column(c.Key("pinned-workspaces")).Padding(4, navEnd, 4, trafficLightX-navPad).Shrink(0).MinHeight(pinnedRowHeight()).Label(L(c).SidebarPinned())
		if d, ok := ui.Drop[topDrag](area); ok {
			shown = a.dropTile(d, shown, order)
		}
		a.topSlot = 0
		if d, over := ui.DragOver[topDrag](area); over {
			id, own := d.topTarget(a)
			if own && slices.Contains(shown, id) || !own && a.pinnable(id) && a.canPin() {
				_, y, _ := area.PointerPosition()
				a.topSlot = min(max(int(y/pinnedRowHeight()), 0), len(shown)) + 1
			}
		}
		area.Children(func() {
			if len(shown) == 0 {
				ui.Text(c, L(c).SidebarPinned()).FontSize(fsSmall).TextColor(c.Theme().TextMuted).Padding(4, navPad)
			}
			for _, id := range shown {
				w := a.workspace(id)
				label := cmp.Or(w.Shown, w.Name)
				if w.Kind == "main" {
					label = w.Project
				}
				row := a.navRowAt(c, "pin-"+id, a.selected == id, func() {
					if w.Kind == "main" {
						a.projectGlyph(c, a.projIcons[w.ProjectID], a.wsIcon(w))
					} else {
						ui.Icon(c, a.wsIcon(w)).FontSize(iconSize).TextColor(c.Theme().Accent).Shrink(0)
					}
				}, label, "", "", func() {
					if _, status := a.terminalSummary(w.Terminals); status != "" {
						statusGlyph(c, status, smallIcon())
					}
				})
				row.Drag(tileDrag{id})
				contextMenu(c, row, func(m *popMenu) { a.workspaceMenu(c, m, *w) })
				if row.Clicked() {
					a.selected = id
				}
			}
		})
		if a.topSlot > 0 {
			accent, slot := c.Theme().Accent, a.topSlot-1
			area.DrawOver(func(p *ui.Painter, r ui.Rect) {
				p.Fill(ui.Rect{X: r.X + navPad, Y: r.Y + float32(slot)*pinnedRowHeight(), W: r.W - 2*navPad, H: focusRing}, accent, 0)
			})
		}
	})
}

func (a *app) dropTile(d topDrag, shown, order []string) []string {
	id, own := d.topTarget(a)
	hole := a.topSlot - 1
	if id == "" || hole < 0 || own && !slices.Contains(shown, id) || !own && (!a.pinnable(id) || len(order) >= topTilesMax) {
		return shown
	}
	list := slices.DeleteFunc(slices.Clone(shown), func(x string) bool { return x == id })
	list = slices.Insert(list, min(hole, len(list)), id)
	next := slices.Clone(list)
	for i, x := range order {
		if !slices.Contains(next, x) {
			next = slices.Insert(next, min(i, len(next)), x)
		}
	}
	a.state.PinnedWorkspaces = next
	a.saveState()
	return list
}

func (a *app) projectMain(projectID string) string {
	if ws := a.byProject[projectID]; len(ws) > 0 {
		return ws[0].ID
	}
	return ""
}
