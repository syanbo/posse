package app

import (
	"slices"

	"github.com/egoist/mygo/ui"
)

const workspacesBoardID = "workspaces"

type navigationDrag struct{ id string }

func (a *app) navigationOrder() []string {
	migrateNavigation(&a.state)
	order := []string{workspacesBoardID}
	for _, secondary := range []bool{false, true} {
		for _, id := range append(slices.Clone(a.state.NavigationOrder), globalTiles...) {
			if !slices.Contains(globalTiles, id) || slices.Contains(order, id) {
				continue
			}
			if (id == skillsBoardID || id == mobileBoardID) == secondary {
				order = append(order, id)
			}
		}
	}
	return order
}

func (a *app) workspaceModule() bool   { return !isBoard(a.selected) && !a.isChat(a.selected) }
func (a *app) hasContextSidebar() bool { return !isBoard(a.selected) || a.selected == discBoardID }

func localModule(id string) bool {
	return id == discBoardID || id == skillsBoardID || id == mobileBoardID || id == settingsBoardID
}

func (a *app) moduleOwner(id string) *app {
	if a.views == nil {
		return a
	}
	if localModule(id) {
		return a.views.local
	}
	return a.views.target
}

func (a *app) showModule(id string) *app {
	owner := a.moduleOwner(id)
	if owner.views != nil {
		owner.views.show(owner.viewID)
	}
	return owner
}

func (a *app) openBoard(id string) {
	owner := a.showModule(id)
	if id == taskBoardID {
		owner.showTaskHome()
	}
	owner.selected, owner.revealSel = id, true
}

func (a *app) moduleLabel(c *ui.Context, id, label string) string {
	if a.views != nil && a.views.target != a.views.local && localModule(id) {
		return L(c).NavigationLocal(label)
	}
	return label
}

func (a *app) openWorkspaces() {
	a = a.showModule(workspacesBoardID)
	id := a.state.LastWorkspace
	if a.workspace(id) == nil || a.isChat(id) {
		id = ""
		for _, w := range a.workspaces {
			if !a.isChat(w.ID) {
				id = w.ID
				break
			}
		}
	}
	a.selected, a.revealSel = id, true
}

func (a *app) navigation(c *ui.Context) {
	p, m := colors(c.Theme()), L(c)
	local, target := a.windowOwner(), a.moduleOwner(workspacesBoardID)
	ui.Column(c.Key("global-navigation")).Width(navigationWidth()).Shrink(0).Background(p.side).Padding(0, 8, 8, 8).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Height(titleHeight(c)).Shrink(0).DragWindow()
		ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
			ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
				secondary := false
				for _, id := range a.navigationOrder() {
					if !secondary && (id == skillsBoardID || id == mobileBoardID) {
						secondary = true
						hline(c).Margin(4, 8)
					}
					var item ui.Element
					switch id {
					case workspacesBoardID:
						item = navigationButton(c, id, iconTerminal, m.SidebarWorkspaces(), a.workspaceModule(), nil)
						if item.Clicked() {
							a.openWorkspaces()
						}
					case discBoardID:
						item = local.discSidebarItem(c)
					case chatBoardID:
						item = target.chatSidebarItem(c)
					case taskBoardID:
						item = target.taskSidebarItem(c)
					case historyBoardID:
						item = target.historySidebarItem(c)
					case skillsBoardID:
						item = local.skillsSidebarItem(c)
					case mobileBoardID:
						item = local.mobileSidebarItem(c)
					}
					if id == workspacesBoardID {
						continue
					}
					item.Drag(navigationDrag{id})
					if d, ok := ui.Drop[navigationDrag](item); ok && (d.id == skillsBoardID || d.id == mobileBoardID) == secondary {
						a.state.NavigationOrder = moveID(a.navigationOrder()[1:], d.id, id)
						a.saveState()
					}
				}
			})
		})
		menuButton(c, navigationButton(c, "navigation-new", iconPlus, m.CommonNew(), false, nil), func(menu *popMenu) { a.newMenu(c, menu) })
		if a.staleApp && a.staleHidden {
			a.staleDot(c)
		}
		if navigationButton(c, settingsBoardID, iconSettings, a.withKey(a.moduleLabel(c, settingsBoardID, m.SettingsTitle()), "settings"), a.selected == settingsBoardID, nil).Clicked() {
			a.openSettings()
		}
	})
}

func navigationButton(c *ui.Context, key string, glyph *ui.SVG, label string, selected bool, badge func()) ui.Element {
	t, p := c.Theme(), colors(c.Theme())
	b := ui.Box(c.Key("navigation-"+key)).Size(navigationBox(), navigationBox()).Shrink(0).Radius(rControl).Center().Label(label).Tooltip(label).Role(ui.RoleButton).Focusable()
	fg, bg := t.TextMuted, ui.Transparent
	if selected {
		fg, bg = t.Accent, p.tint
	} else if b.Hovered() {
		fg, bg = t.Text, p.wash
	}
	b.Background(b.AnimateColor("bg", bg, hover)).Children(func() {
		ui.Icon(c, glyph).FontSize(navigationIcon()).TextColor(fg)
		if badge != nil {
			ui.Row(c).Absolute().Top(0).Right(0).AlignItems(ui.Center).Children(badge)
		}
	})
	return b
}

func (a *app) newMenu(c *ui.Context, m *popMenu) {
	l := L(c)
	if m.Item(l.SidebarAddProject()).Chosen() {
		r := a.showModule(workspacesBoardID).request()
		go r.addProject(l)
	}
	if m.Item(l.SidebarNewWorkspace()).Keys(a.keyText("newWorkspace")).Chosen() {
		a.showModule(workspacesBoardID).openNewWorkspace()
	}
	if m.Item(l.TasksDispatchEllipsis()).Chosen() {
		a.showModule(taskBoardID).openDispatch()
	}
	m.Separator()
	if m.Item(l.SidebarConnectPhone()).Chosen() {
		a.openMobile()
	}
}

func (a *app) sidebarNotices(c *ui.Context) {
	if !a.loaded {
		msg, color := L(c).SidebarConnecting(), c.Theme().TextMuted
		if a.err != "" {
			msg, color = L(c).SidebarConnectError(a.err), c.Theme().Danger
		}
		ui.Text(c, msg).FontSize(fsSmall).TextColor(color).Padding(6, 16)
	}
	a.staleBar(c)
	a.updateBar(c)
	a.posseSkillsBar(c)
	a.errorBar(c)
}

func (a *app) discussionSidebar(c *ui.Context) {
	m := L(c)
	ui.Row(c).Padding(12, iconBarPad(), 8, trafficLightX).Gap(8).AlignItems(ui.Center).Children(func() {
		titleText(c, a.moduleLabel(c, discBoardID, m.DiscTitle()), fsTitle)
		ui.Spacer(c)
		if iconButton(c, iconPlus, m.DiscNewEllipsis()).Disabled(len(a.projects) == 0).Clicked() && len(a.projects) > 0 {
			a.openNewDiscussion()
		}
	})
	ui.Row(c).Padding(0, 16, 8, 16).Children(func() {
		if textButton(c.Key("disc-join"), m.CollabJoinEllipsis()).Clicked() {
			a.openJoin()
		}
	})
	ui.Scroll(c).Grow(1).MinHeight(0).Label(m.DiscList()).Children(func() {
		ui.Column(c).Gap(4).Children(func() { a.discListView(c) })
	})
}
