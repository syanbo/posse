package app

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
	"posse/locales"
)

const workspacesBoardID = "workspaces"

type navigationDrag struct{ id string }
type hostDrag struct{ id string }

type tileState int

const (
	tileNormal tileState = iota
	tileCurrentHost
	tileSelected
)

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

func (a *app) workspaceModule() bool { return !isBoard(a.selected) && !a.isChat(a.selected) }
func (a *app) hasContextSidebar() bool {
	switch a.selected {
	case discBoardID, taskBoardID, historyBoardID:
		return true
	}
	return !isBoard(a.selected)
}

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
	return a.showModuleReady(id, nil)
}

func (a *app) showModuleReady(id string, onReady func(*app)) *app {
	owner := a.moduleOwner(id)
	if owner.views != nil {
		if owner == owner.views.local && owner.host != nil {
			owner.host.prepareLocalReturn()
		}
		if owner != owner.views.local && owner.views.suspendedRemote != nil && owner.views.suspendedRemote.id == owner.viewID && owner.views.local.hosts != nil {
			saved := owner.views.suspendedRemote.host
			owner.views.suspendedRemote = nil
			if !owner.connecting && (owner.host == nil || !owner.host.snapshot().Connected) {
				if h, ok := owner.findSavedHost(owner.viewID); ok && h.SameConnection(saved) {
					owner.views.local.hosts.connectRemoteReady(h, true, onReady)
					return owner
				}
			}
		}
		owner.views.show(owner.viewID)
	}
	if onReady != nil {
		onReady(owner)
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
	p := colors(c.Theme())
	local, target := a.windowOwner(), a.moduleOwner(workspacesBoardID)
	// 顶栏的底线和右边的分隔线都避开红绿灯：底线接上侧栏和主区域顶栏的线，竖线从顶栏下面开始
	ui.Column(c.Key("global-navigation")).Width(navigationWidth()).Shrink(0).Background(p.side).Children(func() {
		ui.Box(c).Height(titleHeight(c)).Shrink(0).DragWindow().BorderWidth(0, 0, hairline, 0).BorderColor(p.line)
		ui.Column(c).Grow(1).MinHeight(0).Padding(8, 6).Gap(8).AlignItems(ui.Center).BorderWidth(0, hairline, 0, 0).BorderColor(p.line).Children(func() {
			a.navigationItems(c, local, target)
		})
	})
}

func (a *app) navigationItems(c *ui.Context, local, target *app) {
	m := L(c)
	hosts := a.orderedHosts()
	monograms := hostMonograms(hosts)
	activeID := localHostID
	targetID := localHostID
	if a.views != nil {
		if act := a.views.active.Load(); act != nil {
			activeID = act.viewID
		}
		if a.views.target != nil {
			targetID = a.views.target.viewID
		}
	}
	onWorkspaces := a.workspaceModule()
	ui.Scroll(c).Grow(1).MinHeight(0).Children(func() {
		ui.Column(c).Gap(8).AlignItems(ui.Center).Children(func() {
			secondary := false
			for _, id := range a.navigationOrder() {
				if !secondary && (id == skillsBoardID || id == mobileBoardID) {
					secondary = true
					hline(c).AlignSelf(ui.Stretch).Margin(4, 0)
				}
				var item ui.Element
				switch id {
				case workspacesBoardID:
					st := tileNormal
					if targetID == localHostID {
						if onWorkspaces && activeID == localHostID {
							st = tileSelected
						} else {
							st = tileCurrentHost
						}
					}
					var badge func()
					if activeID == localHostID {
						badge = local.workspacesBadge(c)
					}
					item = navigationTile(c, id, iconTerminal, "", m.SidebarWorkspaces(), st, badge)
					if item.Clicked() {
						a.selectLocalHost()
						local.openWorkspaces()
					}
					var localBuild func(*popMenu)
					contextMenu(c, item, func(menu *popMenu) {
						if localBuild == nil {
							localBuild = a.hostActionsBuild(m, remote.Host{ID: localHostID, Name: m.HostLocal()}, activeID == localHostID)
						}
						localBuild(menu)
					})
					hasTargetTile := false
					for _, h := range hosts {
						if h.ID == targetID {
							hasTargetTile = true
							break
						}
					}
					if targetID == localHostID || !hasTargetTile {
						a.consumeHostSwitchMenu(c, item, m)
					}
					// 远端 SSH 主机紧跟在 >_ 下方平级排开，有远端时画一条分隔线
					if len(hosts) > 0 {
						for _, h := range hosts {
							a.remoteHostTile(c, m, h, monograms[h.ID], hosts, activeID, targetID, onWorkspaces)
						}
						hline(c).AlignSelf(ui.Stretch).Margin(4, 0)
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
}

func (a *app) remoteHostTile(c *ui.Context, m locales.Messages, h remote.Host, monogram string, ordered []remote.Host, activeID, targetID string, onWorkspaces bool) {
	st := tileNormal
	if targetID == h.ID {
		if onWorkspaces && activeID == h.ID {
			st = tileSelected
		} else {
			st = tileCurrentHost
		}
	}
	var badge func()
	if activeID == h.ID && a.views != nil {
		if v := a.views.all[h.ID]; v != nil {
			badge = v.remoteHostBadge(c)
		}
	}
	connState := a.remoteHostConnState(h.ID)
	t, p := c.Theme(), colors(c.Theme())
	tile := ui.Box(c.Key("navigation-host-"+h.ID)).Size(navigationBox(), navigationBox()).Shrink(0).Radius(rControl).Center().Label(h.Name).Role(ui.RoleButton).Focusable()
	fg, bg, border := t.TextMuted, p.fill, p.line
	switch st {
	case tileSelected:
		fg, bg, border = t.Accent, p.tint, t.Accent
	case tileCurrentHost:
		fg, border = t.Accent, t.Accent
		if tile.Hovered() {
			bg = p.wash
		}
	default:
		if tile.Hovered() {
			fg, bg = t.Text, p.wash
		}
	}
	tile.Background(tile.AnimateColor("bg", bg, hover)).BorderWidth(hairline).BorderColor(border).Children(func() {
		a.hostTileGlyph(c, h, monogram, fg)
		if badge != nil {
			ui.Row(c).Absolute().Top(0).Right(0).AlignItems(ui.Center).Children(badge)
		}
		a.remoteHostStatusDot(c, connState)
	})
	if tile.Clicked() {
		a.openHost(h)
	}
	var tileBuild func(*popMenu)
	contextMenu(c, tile, func(menu *popMenu) {
		if tileBuild == nil {
			tileBuild = a.hostActionsBuild(m, h, activeID == h.ID)
		}
		tileBuild(menu)
	})
	if targetID == h.ID {
		a.consumeHostSwitchMenu(c, tile, m)
	}
	tile.Drag(hostDrag{h.ID})
	if d, ok := ui.Drop[hostDrag](tile); ok && d.id != h.ID {
		owner := a.windowOwner()
		ids := make([]string, len(ordered))
		for i, item := range ordered {
			ids[i] = item.ID
		}
		owner.state.HostOrder = moveID(ids, d.id, h.ID)
		owner.saveState()
	}
}

func (a *app) consumeHostSwitchMenu(c *ui.Context, anchor ui.Element, m locales.Messages) {
	owner := a.windowOwner()
	st := ui.Local(anchor, "posse-host-switch", func() menuState { return menuState{} })
	if owner.hostMenu.openRequested {
		owner.hostMenu.openRequested = false
		st.open = true
		st.reset()
	}
	var build func(*popMenu)
	menuAtRight(c, anchor, st, func(menu *popMenu) {
		if build == nil {
			if !st.focused {
				a.loadHostMenu()
			}
			build = a.hostSwitchBuild(m)
		}
		build(menu)
	})
}

type hostConnState int

const (
	hostConnOffline hostConnState = iota
	hostConnConnecting
	hostConnOnline
	hostConnViewing
	hostConnError
)

func (a *app) remoteHostConnState(id string) hostConnState {
	var v *app
	if a.views != nil {
		v = a.views.all[id]
	}
	if v != nil {
		if v.connecting {
			return hostConnConnecting
		}
		if v.host != nil {
			if s := v.host.snapshot(); s.Connected {
				if s.Controlled {
					return hostConnOnline
				}
				return hostConnViewing
			}
		}
	}
	if k := a.windowOwner().hosts; k != nil && k.poolConnected(id) {
		return hostConnOnline
	}
	if v != nil && v.err != "" {
		return hostConnError
	}
	return hostConnOffline
}

// remoteHostStatusDot 画在远端主机磁贴右下角：在线绿点、连接中/查看中琥珀点、错误红点、已断开空心灰圈。
func (a *app) remoteHostStatusDot(c *ui.Context, state hostConnState) {
	t, p := c.Theme(), colors(c.Theme())
	outer := hostStatusDot()
	inset := hostStatusDotInset()
	if state == hostConnOffline {
		inner := hostStatusDotInner()
		ui.Box(c).Absolute().Bottom(inset).Right(inset).Size(outer, outer).Radius(full(outer)).Background(p.side).Center().Children(func() {
			ui.Box(c).Size(inner, inner).Radius(full(inner)).Background(p.side).BorderWidth(hostStatusDotStroke()).BorderColor(t.TextMuted)
		})
		return
	}
	col := p.sage
	switch state {
	case hostConnConnecting, hostConnViewing:
		col = p.honey
	case hostConnError:
		col = t.Danger
	}
	ui.Box(c).Absolute().Bottom(inset).Right(inset).Size(outer, outer).Radius(full(outer)).
		Background(col).BorderWidth(hostStatusDotRing()).BorderColor(p.side)
}

func (a *app) remoteHostBadge(c *ui.Context) func() {
	if a.connecting || (a.host != nil && !a.host.snapshot().Connected) {
		return nil
	}
	return a.workspacesBadge(c)
}

// workspacesBadge 汇总聊天以外所有工作区的会话状态，聊天磁贴自己显示聊天的。
func (a *app) workspacesBadge(c *ui.Context) func() {
	status := ""
	for _, w := range a.workspaces {
		if a.isChat(w.ID) {
			continue
		}
		if _, s := a.terminalSummary(w.Terminals); statusRank(s) > statusRank(status) {
			status = s
		}
	}
	if status == "" {
		return nil
	}
	return func() { statusGlyph(c, status, smallIcon()) }
}

func navigationButton(c *ui.Context, key string, glyph *ui.SVG, label string, selected bool, badge func()) ui.Element {
	st := tileNormal
	if selected {
		st = tileSelected
	}
	return navigationTile(c, key, glyph, "", label, st, badge)
}

func navigationTile(c *ui.Context, key string, glyph *ui.SVG, text, label string, st tileState, badge func()) ui.Element {
	t, p := c.Theme(), colors(c.Theme())
	b := ui.Box(c.Key("navigation-"+key)).Size(navigationBox(), navigationBox()).Shrink(0).Radius(rControl).Center().Label(label).Role(ui.RoleButton).Focusable()
	fg, bg := t.TextMuted, ui.Transparent
	switch st {
	case tileSelected:
		fg, bg = t.Accent, p.tint
	case tileCurrentHost:
		fg = t.Accent
		if b.Hovered() {
			bg = p.wash
		}
	default:
		if b.Hovered() {
			fg, bg = t.Text, p.wash
		}
	}
	b.Background(b.AnimateColor("bg", bg, hover)).Children(func() {
		if glyph != nil {
			ui.Icon(c, glyph).FontSize(navigationIcon()).TextColor(fg)
		} else {
			size := fsTitle
			maxW := navigationBox() - 8
			if w, _ := c.MeasureText(0, ui.Span{Text: text, Size: size, Weight: fwMedium}); w > maxW && w > 0 {
				size *= maxW / w
			}
			ui.Text(c, text).FontSize(size).FontWeight(fwMedium).TextColor(fg).SingleLine()
		}
		if badge != nil {
			ui.Row(c).Absolute().Top(0).Right(0).AlignItems(ui.Center).Children(badge)
		}
	})
	return b
}

func (a *app) newMenu(c *ui.Context, m *popMenu) {
	l := L(c)
	target := a.moduleOwner(workspacesBoardID)
	suspended := target.views != nil && target.views.suspendedRemote != nil && target.views.suspendedRemote.id == target.viewID
	remoteDisabled := a.windowOwner().hosts != nil && target.remoteOffline() && !suspended
	if m.Item(l.SidebarAddProject()).Disabled(remoteDisabled).Chosen() {
		a.showModuleReady(workspacesBoardID, func(target *app) {
			r := target.request()
			go r.addProject(l)
		})
	}
	if m.Item(l.SidebarNewWorkspace()).Disabled(remoteDisabled).Keys(a.keyText("newWorkspace")).Chosen() {
		a.showModuleReady(workspacesBoardID, func(target *app) {
			target.openNewWorkspace()
		})
	}
	if m.Item(l.TasksDispatchEllipsis()).Disabled(remoteDisabled).Chosen() {
		a.showModuleReady(taskBoardID, func(target *app) {
			target.openDispatch()
		})
	}
	m.Separator()
	if m.Item(l.HostAddItem()).Chosen() {
		a.openHostDialog(remote.Host{})
	}
	m.Separator()
	if m.Item(l.SidebarConnectPhone()).Chosen() {
		a.openMobile()
	}
}

func (a *app) sidebarNotices(c *ui.Context) {
	if !a.loaded && !a.remoteHost() {
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

// moduleHeader 是工作区、聊天、讨论、任务、历史这几个模块侧栏顶上的标题行，右边放这个模块的操作；
// 远端主机时把主机名标签紧跟在标题右侧并垂直居中对齐。
// 操作按钮有 buttonBox 和 compactBox 两种大小，行高按大的定，切换模块时标题和下面的列表不跳。
func (a *app) moduleHeader(c *ui.Context, title string, actions func()) ui.Element {
	return ui.Row(c).MinHeight(buttonBox()+20).Padding(12, iconBarPad(), 8, trafficLightX).Gap(8).AlignItems(ui.Center).Shrink(0).Children(func() {
		titleText(c, title, fsTitle).MaxLines(1).Shrink(0)
		a.hostHeaderBadge(c)
		ui.Spacer(c)
		actions()
	})
}

func (a *app) discussionSidebar(c *ui.Context) {
	m := L(c)
	a.moduleHeader(c, a.moduleLabel(c, discBoardID, m.DiscTitle()), func() {
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
