package app

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/media"
	"posse/locales"
)

func (a *app) build(c *ui.Context) {
	a.runPending()
	c.SetTheme(posseTheme(c.Theme().Dark))
	a.shade.Store(map[bool]int32{false: 1, true: 2}[c.Theme().Dark])
	// 文字输入框、编辑器这些 mygo 自己的菜单也由 Posse 自绘（sysMenuView）
	c.PresentMenus(a.presentMenu)
	for _, t := range a.toasts {
		c.AddToast(t)
	}
	a.toasts = nil
	a.snapPanels(c)
	t := c.Theme()
	p := colors(t)
	board := a.selected == taskBoardID
	if !board {
		a.setWantedTask("")
	}
	if a.selected != discBoardID {
		a.setWantedDisc("", 0)
	}
	a.setHistoryVisible(a.selected == historyBoardID)
	a.setMobileVisible(a.selected == mobileBoardID)
	a.setSkillsVisible(a.selected == skillsBoardID)
	if a.loaded && a.selected != a.state.Selected {
		a.state.Selected = a.selected
		a.saveState()
	}
	w := a.workspace(a.selected)
	if w != nil {
		a.lastWS = w.ID
	}
	a.cols, a.split, a.pane = a.cols[:0], false, 0
	if w != nil {
		a.syncColumns(w)
	}
	cur := a.currentTerminal(w)
	a.ensurePanes(a.paneIDs(cur))
	// 拖着的分隔线上一帧没画（那一层收起、变了样、切走）：松手的那一帧不会再画它，拖的状态在这里放掉
	if strings.HasPrefix(a.resizingSplit, "split") && !a.gripDrawn {
		a.resizingSplit, a.splitDrag = "", 0
	}
	a.gripDrawn = false

	a.shortcuts(c, w, cur)
	// 看不见的文件内容放掉文件清单，再显示时重列；快速打开要用清单
	for id, v := range a.files {
		if w == nil || id != w.ID || !a.filesShown(w) && !(a.quick.open && a.quick.ws == id) {
			v.list = nil
		}
	}
	a.dirtyCount.Store(int32(len(a.dirtyDocs())))

	a.imagesDrawn = false
	root := ui.Row(c).Fill().AlignItems(ui.Stretch)
	// 收起、展开时侧栏从左边缘滑出滑进。主区域只在展开开始、收起结束时改一次宽度，
	// 免得终端里的程序跟着动画反复按新列数重排（和右边的文件栏一样）
	open := float32(1)
	if a.state.SidebarHidden {
		open = 0
	}
	a.sidebarShown = root.AnimateTo("sidebar", open, ui.Animation{Duration: 200 * time.Millisecond, Ease: ui.EaseOut})
	root.Children(func() {
		if a.sidebarSlot() {
			width := a.sidebarWidth() + 1
			ui.Box(c).Width(width).Shrink(0).Clip().Background(p.side).Children(func() {
				ui.Row(c).Absolute().Top(0).Bottom(0).Left(-(1 - a.sidebarShown) * width).Width(width).AlignItems(ui.Stretch).Children(func() {
					ui.Column(c).Width(a.sidebarWidth()).Shrink(0).Background(p.side).Children(func() { a.sidebar(c) })
					vline(c)
				})
			})
		}
		// 只占剩下的宽度：标签多了也不撑出窗口（右边停靠的文件栏要完整显示）
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Background(p.canvas).Children(func() {
			ui.Column(c).Grow(1).MinHeight(0).Children(func() {
				switch {
				case board:
					a.taskBoard(c)
				case a.selected == historyBoardID:
					a.historyBoard(c)
				case a.selected == discBoardID:
					a.discBoard(c)
				case a.selected == mobileBoardID:
					a.mobileBoard(c)
				case a.selected == skillsBoardID:
					a.skillsBoard(c)
				case a.selected == settingsBoardID:
					a.settingsBoard(c)
				default:
					a.main(c, w, cur)
				}
			})
			a.statusBar(c)
		})
		// 最后画，压在分隔线和主区域上面，才接得到分隔线两侧的拖动
		if !a.state.SidebarHidden {
			a.sidebarGrip(c)
		}
	})
	// 应用名固定在红绿灯右边，不跟着侧栏滑动。侧栏开关展开时在侧栏顶栏最右边，
	// 侧栏不占位置时跟在应用名后面，好再点开
	ui.Row(c).Absolute().Top(0).Left(c.TitleBar().Left + titleGap).Height(titleHeight(c)).Gap(appNameGap).AlignItems(ui.Center).Children(func() {
		titleText(c, appName(), fsBody).Width(appNameWidth(c)).Shrink(0)
		if !a.sidebarSlot() {
			a.sidebarButton(c)
		}
	})
	// 图片面板这一帧没画（收起了、换到别的页面或普通终端）就放掉位图
	if !a.imagesDrawn && a.images.term != "" {
		a.images = imagesView{}
	}
	a.newWorkspaceDialog(c)
	a.dispatchDialog(c)
	a.newDiscussionDialog(c)
	a.editDiscussionDialog(c)
	a.memberDialog(c)
	a.joinDialog(c)
	a.inviteDialog(c)
	a.quickDialog(c)
	a.defDialog(c)
	a.pushDialog(c)
	a.promptDialog(c)
	a.unsavedDialog(c)
	a.rollbackDialog(c)
	a.confirmDialog(c)
	a.tracesDialog(c)
	a.skillChangeDialog(c)
	a.skillInstallDialog(c)
	a.skillRunDialog(c)
	a.skillNewDialog(c)
	a.projectIconDialog(c)
	a.sysMenuView(c)
}

func (a *app) shortcuts(c *ui.Context, w *workspace, cur *terminalInfo) {
	// 离开设置页的快捷键分类就不再录了，免得快捷键一直不认
	if a.keyRec != nil && (a.selected != settingsBoardID || a.settingsTab != "keys") {
		a.keyRec = nil
	}
	if w != nil && a.pressed(c, "newTerminal") {
		a.startTerminal(w.ID, "")
	}
	if a.pressed(c, "newChat") {
		a.openChat(L(c), true)
	}
	// 双击 ⇧（设置里打开）：mygo 数着单独连按两下修饰键的次数，这一帧比上次多了就是又按了一次。
	// 每帧都把计数读掉，开不了（没有工作区、别的对话框开着）时这一下作废，不留到之后
	if mods, n := c.DoubleTap(); n != a.doubleTaps {
		a.doubleTaps = n
		if a.state.DoubleShift && mods == ui.Shift && w != nil && !a.quick.open && a.keyRec == nil && !c.ModalOpen() {
			a.openQuick(w, quickAll)
		}
	}
	if w != nil {
		if a.pressed(c, "quickOpen") {
			a.openQuick(w, quickFiles)
		}
		if a.pressed(c, "searchEverywhere") {
			a.openQuick(w, quickAll)
		}
		if a.pressed(c, "findAction") {
			a.openQuick(w, quickActions)
		}
		if a.pressed(c, "searchFiles") {
			// 照 WebStorm：焦点在文件树上、选中的是目录时只搜这个目录，别处按下搜整个工作区
			v := a.filesFor(w)
			dir := ""
			if a.filesPanelOpen() && c.FocusWithin(v.treeRef) && v.treeSelected() != "" && v.treeSelDir {
				dir = v.treeSel
			}
			a.searchIn(w, dir)
		}
		// 窗口的快捷键先于焦点拿到按键。⌘L 照旧开关文件栏（编辑器的"选中整行"让出），
		// ⇧⌘G 在编辑器有焦点、Markdown 预览、Annotate 或终端开着查找栏时让给它们的"查找上一个"
		doc := a.filesFor(w).cur
		editing := a.filesActive(w) && doc != nil && (c.FocusWithin(doc.edRef) || doc.finding()) ||
			!a.filesActive(w) && a.termFinding(c)
		if a.pressed(c, "filesPanel") {
			if a.filesPanelOpen() {
				a.closeFilesPanel()
			} else {
				a.openFilesPanel(w, a.filesFor(w).side)
			}
		}
		if !a.filesActive(w) {
			a.termFindKeys(c)
		}
		if !editing && a.pressed(c, "changes") {
			a.toggleFilesPanel(w, sideChanges)
		}
		// ⌘K 在终端里是清屏（termKey），焦点在文件、文件栏上时才是提交；不注册快捷键，终端才收得到这个键
		commitKey := a.filesShown(w) && a.filesFor(w).git.repo != "" && (a.filesActive(w) || c.FocusWithin(a.filesPanelRef))
		if commitKey && a.pressed(c, "commit") {
			a.openFilesPanel(w, sideChanges).ch.focusMsg = true
		}
		// ⌥⌘Z：焦点在改动页时批量回滚（编辑器、diff 里是回滚当前这一处，见 gitShortcuts）
		if a.filesPanelOpen() && a.filesFor(w).side == sideChanges && c.FocusWithin(a.filesPanelRef) && a.pressed(c, "rollback") {
			a.openRollback(a.filesFor(w))
		}
		if a.gitBranch(w) != "" && a.pressed(c, "push") {
			a.openPush(L(c), w)
		}
		if a.gitBranch(w) != "" && a.pressed(c, "newBranch") {
			a.askNewBranch(L(c), w, a.filesFor(w), "")
		}
		if a.pressed(c, "fileTree") {
			a.toggleFilesPanel(w, sideFiles)
		}
		if a.pressed(c, "revealFile") {
			a.revealCurrent(w)
		}
	}
	if a.filesActive(w) {
		v := a.filesFor(w)
		a.gitShortcuts(c, w, v)
		if a.pressed(c, "save") && v.cur != nil {
			a.saveDoc(L(c), v.cur, false)
		}
		if v.cur != nil && isJSON(v.cur.path) && a.pressed(c, "formatJSON") {
			a.formatJSON(L(c), v.cur)
		}
	}
	// 打包的 app 里 ⌘W 先被菜单拿走（installMenu），菜单只记下来，到这里和快捷键一样在有对话框时不动
	if a.menuCloseTab {
		a.menuCloseTab = false
		if !c.ModalOpen() {
			a.closeTab(L(c))
		}
	}
	if a.pressed(c, "closeTab") {
		a.closeTab(L(c))
	}
	// ⌃- 回到跳走之前的位置、⌃⇧- 再往前（和 VS Code 一样；⌘[ 是编辑器的减少缩进）
	if w != nil && a.pressed(c, "jumpBack") {
		a.jumpBack(w)
	}
	if w != nil && a.pressed(c, "jumpForward") {
		a.jumpForward(w)
	}
	if a.pressed(c, "newWorkspace") {
		a.openNewWorkspace()
	}
	if a.pressed(c, "toggleSidebar") {
		a.toggleSidebar()
	}
	if w != nil && a.gitBranch(w) != "" && a.pressed(c, "gitLog") {
		a.toggleLogPanel(w)
	}
	// 图片标签在前台时 ⌘= ⌘- ⌘0 缩放图片，文件标签（Markdown 预览也是）、历史页和任务页调编辑器的字号，终端调终端的
	img := a.frontImage(w)
	size, def, setSize := a.fontSize, float32(defaultFontSize), a.setFontSize
	if a.filesActive(w) || a.selected == historyBoardID && a.history.detail != nil || a.selected == taskBoardID && a.task(a.taskSel) != nil && !a.taskHome {
		size, def, setSize = a.editorSize, defaultEditorFontSize, a.setEditorFontSize
	}
	if a.pressed(c, "zoomIn") {
		if img != nil {
			img.zoomStep(true)
		} else {
			setSize(size + 1)
		}
	}
	if a.pressed(c, "zoomOut") {
		if img != nil {
			img.zoomStep(false)
		} else {
			setSize(size - 1)
		}
	}
	if a.pressed(c, "zoomReset") {
		if img != nil {
			img.setZoom(0, 0, 0)
		} else {
			setSize(def)
		}
	}
	if w != nil {
		if a.pressed(c, "nextTab") {
			a.stepTab(w, 1)
		}
		if a.pressed(c, "prevTab") {
			a.stepTab(w, -1)
		}
		keys := []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9}
		for i, k := range keys {
			if i < len(w.Terminals) && a.keyRec == nil && c.Shortcut(ui.Cmd, k) {
				a.activeTerm[w.ID] = w.Terminals[i].ID
			}
		}
	}
}

// isBoard 报告选中的是不是任务、历史、技能、手机、设置这几个不属于工作区的页面。
func isBoard(id string) bool {
	return id == taskBoardID || id == historyBoardID || id == skillsBoardID || id == mobileBoardID || id == settingsBoardID || id == discBoardID
}

// closeBoard 关掉当前的任务、历史、手机或设置页，回到上次看的工作区，放掉页面读进来的内容；
// 再打开时重新读。
func (a *app) closeBoard() {
	switch a.selected {
	case historyBoardID:
		a.releaseHistory()
	case taskBoardID:
		a.detail, a.taskSel, a.taskHome, a.taskScroll = nil, "", false, ""
	case discBoardID:
		// 放掉读进来的消息，再打开时重读；草稿跟着讨论走，关页面时不留
		removePasted(a.disc.atts)
		a.disc = discBoard{list: a.disc.list}
		a.setWantedDisc("", 0)
	case mobileBoardID:
		a.mobile.devices = nil
	case skillsBoardID:
		a.releaseSkills()
	}
	next := a.lastWS
	if a.workspace(next) == nil {
		next = ""
		if len(a.workspaces) > 0 {
			next = a.workspaces[0].ID
		}
	}
	a.selected, a.revealSel = next, true
}

// closeBoardButton 是页面顶栏最右边的关闭按钮。
func (a *app) closeBoardButton(c *ui.Context) {
	if iconButton(c.Key("close-board"), iconClose, a.withKey(L(c).CommonClose(), "closeTab")).Clicked() {
		a.closeBoard()
	}
}

// 侧栏的宽度，拖右边缘调整，拖过的照拖的宽度；默认宽度和上限跟着界面字号缩放。
// 下限由顶上的磁贴算：最窄时一行四块正好是正方形（宽等于 tileHeight，跟着字号），再窄名字就截得认不出了；
// 间隔和其他间距一样不跟字号。拉宽时磁贴跟着变宽。
func sidebarDefault() float32 { return max(sz(272), sidebarMin()) }
func sidebarMin() float32 {
	return topTilesPerRow*tileHeight() + (topTilesPerRow+1)*tileGap
}
func sidebarMax() float32 { return sz(420) }

func (a *app) sidebarWidth() float32 {
	w := a.state.SidebarWidth
	if w == 0 {
		w = sidebarDefault()
	}
	return min(max(w, sidebarMin()), sidebarMax())
}

func (a *app) toggleSidebar() {
	a.state.SidebarHidden = !a.state.SidebarHidden
	a.saveState()
}

// sidebarButton 是收起、展开侧栏的开关。
func (a *app) sidebarButton(c *ui.Context) {
	label := a.withKey(L(c).SidebarHide(), "toggleSidebar")
	if a.state.SidebarHidden {
		label = a.withKey(L(c).SidebarShow(), "toggleSidebar")
	}
	if iconButton(c.Key("sidebar-toggle"), iconPanelLeft, label).Clicked() {
		a.toggleSidebar()
	}
}

// sidebarSlot 报告侧栏占不占位置：展开着，或者收起动画还没走完。
func (a *app) sidebarSlot() bool { return !a.state.SidebarHidden || a.sidebarShown > 0 }

// 红绿灯右边的应用名占的宽度（跟着界面字号），和应用名加侧栏开关一共占的宽度。
const (
	titleGap   = 18 // 红绿灯到应用名
	appNameGap = 12 // 应用名到侧栏开关
	toggleBox  = 28 // iconButton 的方框
)

// appName 是包的显示名：PosseDev 是"Posse Dev"，和日常的窗口分得清。不在 .app 里运行时是 Posse。
var appName = sync.OnceValue(func() string {
	if n := mygo.App.Name(); strings.HasPrefix(n, "Posse") {
		return n
	}
	return "Posse"
})

// appNameWidth 至少照"Posse"留 46，名字更长（Posse Dev）时按实际宽度。名字不变，按字号记下量过的宽度。
func appNameWidth(c *ui.Context) float32 {
	key := [2]float32{fsBody, float32(fwBold)}
	w, ok := appNameWidths[key]
	if !ok {
		w, _ = c.MeasureText(0, ui.Span{Text: appName(), Size: fsBody, Weight: fwBold})
		appNameWidths[key] = w
	}
	return max(sz(46), w)
}

// appNameWidths 只在界面线程读写。
var appNameWidths = map[[2]float32]float32{}

func titleLead(c *ui.Context) float32 { return appNameWidth(c) + appNameGap + toggleBox }

// barInset 是侧栏不占位置时主区域顶栏左边要让出的宽度：红绿灯和浮在旁边的应用名、侧栏开关。
func (a *app) barInset(c *ui.Context) float32 {
	if a.sidebarSlot() {
		return 0
	}
	return c.TitleBar().Left + titleGap + titleLead(c)
}

// sidebarGrip 是侧栏右边缘的拖动条，松手才写 ui.json。
func (a *app) sidebarGrip(c *ui.Context) {
	grip := ui.Box(c.Key("sidebar-grip")).Absolute().Top(0).Bottom(0).Left(a.sidebarWidth() - 2).Width(6).Cursor(ui.CursorResizeEW)
	if dx, _, held := grip.Dragged(); held {
		a.state.SidebarWidth = min(max(a.sidebarWidth()+dx, sidebarMin()), sidebarMax())
		a.resizingSide = true
	} else if a.resizingSide {
		a.resizingSide = false
		a.saveState()
	}
	if grip.Hovered() || a.resizingSide {
		accent := c.Theme().Accent
		grip.Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
		})
	}
}

// titleHeight 是顶栏高度：隐藏标题栏后，红绿灯按钮叠在侧栏顶部。
func titleHeight(c *ui.Context) float32 { return max(c.TitleBar().Height, 40) }

// --- 侧栏

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, iconBarPad(), 0, bar.Left+titleGap).DragWindow().AlignItems(ui.Center).
		BorderWidth(0, 0, hairline, 0).BorderColor(colors(t).line).Children(func() {
		// 应用名浮在这里（见 build），留出它的位置
		ui.Box(c).Width(appNameWidth(c)).Shrink(0)
		ui.Spacer(c)
		a.sidebarButton(c)
	})
	if !a.loaded {
		msg, color := L(c).SidebarConnecting(), t.TextMuted
		if a.err != "" {
			msg, color = L(c).SidebarConnectError(a.err), t.Danger
		}
		ui.Text(c, msg).FontSize(fsSmall).TextColor(color).Padding(6, 16)
	}
	a.navOrder, a.navCur = a.navOrder[:0], -1
	a.topTiles(c)
	if len(a.projects) > 0 {
		a.sidebarGroupSwitch(c)
	}
	nav := ui.Scroll(c).Grow(1).Focusable().Label(L(c).SidebarWorkspaces())
	// 固定的工作区从顶上拖回项目列表就取消固定
	if d, ok := ui.Drop[tileDrag](nav); ok && !slices.Contains(globalTiles, d.id) {
		a.unpin(d.id)
	}
	a.projectDrop(c, nav)
	nav.Children(func() {
		ui.Column(c).Padding(6, navEnd, 12, trafficLightX-navPad).Gap(2).Children(func() {
			if a.state.SidebarGroup == sidebarByStatus {
				a.runningList(c)
				return
			}
			// 要定位的工作区所在的项目
			reveal := ""
			if w := a.workspace(a.selected); a.revealSel && w != nil {
				reveal = w.ProjectID
			}
			// 遍历拷贝：拖放排序会在这一帧里改 a.projects
			a.navProjects = append(a.navProjects[:0], a.projects...)
			for _, p := range a.navProjects {
				if ws := a.byProject[p.ID]; len(ws) > 0 {
					a.projectItem(c, p, ws, reveal == p.ID)
				}
			}
		})
	})
	// 侧栏有焦点时，上下方向键在看得见的行之间移动
	if n := len(a.navOrder); n > 0 {
		i, to := a.navCur, -1
		switch {
		case nav.Shortcut(0, ui.KeyDown):
			to = min(i+1, n-1)
		case nav.Shortcut(0, ui.KeyUp):
			to = max(i-1, 0)
		}
		if to >= 0 {
			if a.navOrder[to].ws == taskBoardID {
				a.showTaskHome()
			}
			a.selected = a.navOrder[to].ws
			if term := a.navOrder[to].term; term != "" {
				a.activeTerm[a.selected] = term
			}
		}
	}
	a.staleBar(c)
	a.updateBar(c)
	a.posseSkillsBar(c)
	a.errorBar(c)
	a.footer(c)
}

// projectItem 是侧栏里的一个项目。只有主工作区时项目行就是它，agent 会话缩进在下面；
// 有其他工作区时多一层：项目标题行下面是各工作区（主工作区单独占一行），会话再缩进在各自工作区下面。
// 收起时项目行汇总所有终端的状态，等你处理也看得到。reveal 表示要定位的工作区在这个项目里。
func (a *app) projectItem(c *ui.Context, p project, ws []workspace, reveal bool) {
	head := ws[0]
	open := a.sectionsOpen[p.ID]
	if open == nil {
		open = new(bool)
		*open = true
		a.sectionsOpen[p.ID] = open
	}
	nested := len(ws) > 1
	// 要定位的行在收起的项目里时先展开；只有主工作区、定位的又不是会话时项目行本身就看得见
	if reveal && a.revealSel && !*open && (nested || a.selectedSession(a.workspace(a.selected)) != "") {
		*open = true
	}
	group := ui.Column(c.Key(p.ID)).Gap(2).Shrink(0)
	// 展开收起时子项的高度跟着动画伸缩（箭头同时转），收起动画走完才不画
	target := float32(0)
	if *open {
		target = 1
	}
	shown := group.AnimateTo("open", target, ui.Animation{Duration: 180 * time.Millisecond, Ease: ui.EaseOut})
	group.Children(func() {
		if !nested {
			sessions := agentSessions(head.Terminals)
			var toggle func()
			if len(sessions) > 0 {
				toggle = func() {
					navDisclosure(c, open, L(c).SidebarShowSessions(len(sessions)), L(c).SidebarHideSessions(len(sessions)))
				}
			}
			// 单击项目行就展开；已经选中并展开时再单击收起
			wasSelected := a.selected == head.ID
			if row := a.sidebarItem(c, head, p.Name, false, !*open, toggle); row.Clicked() && len(sessions) > 0 {
				*open = !*open || !wasSelected
			}
			collapsible(c, shown, func() { a.sessionRows(c, head, sessions, nil) })
			return
		}
		a.projectHeader(c, p, ws, open)
		collapsible(c, shown, func() {
			links := &remoteLinks{handles: a.linkHandleMap()}
			list := ui.Column(c).Gap(2).Padding(0, 0, 0, navIndent)
			list.Children(func() {
				for _, w := range ws {
					label := cmp.Or(w.Shown, w.Name)
					if w.Kind == "main" {
						label = L(c).SidebarMainWorkspace()
					}
					a.sidebarItem(c, w, label, true, false, nil)
					a.sessionRows(c, w, agentSessions(w.Terminals), links)
				}
			})
			if len(links.order) > 0 {
				h := links.handle("list:" + p.ID)
				list.Bind(h)
				color := colors(c.Theme()).guide
				list.DrawOver(func(p *ui.Painter, r ui.Rect) { links.draw(c, p, r, *h, color) })
			}
		})
	})
	a.reorderable(c, group, p.ID)
}

// collapsible 按展开的进度 shown（0 到 1）画 fn：完全展开时照常排，动画中裁到自然高度乘进度并跟着淡入淡出，
// 收起后不画。自然高度取上一帧量到的，刚开始展开的第一帧还没量过，高度为 0，正好是动画的起点。
func collapsible(c *ui.Context, shown float32, fn func()) {
	if shown <= 0.001 {
		return
	}
	wrap := ui.Column(c.Key("collapsible")).Shrink(0).Clip()
	var inner ui.Element
	wrap.Children(func() {
		inner = ui.Column(c).Shrink(0).Gap(2)
		inner.Children(fn)
	})
	if shown < 0.999 {
		wrap.Height(inner.Bounds().H * shown).Opacity(shown)
	}
}

// navIndent 是侧栏下一层相对上一层的缩进。
const navIndent = 24

// statusSlot 是会话行左边的状态槽，占一层缩进的宽度。状态图形对着上一层行首图标的中线，
// 图标调到最大时放不下，就贴着槽的右边。
func statusSlot(c *ui.Context, glyph func()) ui.Element {
	return ui.Row(c).Width(navIndent).Padding(0, 0, 0, statusSlotLeft()).AlignItems(ui.Center).Shrink(0).Children(glyph)
}

// statusSlotLeft 是状态图形在状态槽里离左边的距离。
func statusSlotLeft() float32 { return min(navPad+(iconSize-smallIcon())/2, navIndent-smallIcon()) }

// trafficLightX 是红绿灯红点的左边缘。
const trafficLightX = 16

// iconBarPad 是顶栏、底栏两头放图标按钮时的内边距：按钮自带 (buttonBox-iconSize)/2 的留白，
// 加起来图标离窗口和侧栏的边 trafficLightX，和红点、侧栏行首图标一样。
func iconBarPad() float32 { return trafficLightX - (buttonBox()-iconSize)/2 }

// navPad 是侧栏行左右的内边距，两边一样；列表左边留 trafficLightX-navPad，顶层行首图标的左边缘和红点对齐。
const navPad = 10

// navEnd 是侧栏列表右边的内边距，比左边宽：选中行的底色离滚动条远一点。
const navEnd = 12

// projectHeader 是有多个工作区的项目的标题行：只管展开收起，不选中工作区；收起时汇总所有工作区的终端。
func (a *app) projectHeader(c *ui.Context, p project, ws []workspace, open *bool) {
	running, status := 0, ""
	for i := range ws {
		r, s := a.terminalSummary(ws[i].Terminals)
		running += r
		if statusRank(s) > statusRank(status) {
			status = s
		}
	}
	trailing := func() {
		if !*open {
			summaryTrailing(c, running, status)
		}
		navDisclosure(c, open, L(c).SidebarShowWorkspaces(len(ws)), L(c).SidebarHideWorkspaces(len(ws)))
	}
	icon := func() { a.projectGlyph(c, a.projIcons[p.ID], iconFolder) }
	sub := shortPath(ws[0].Path)
	if a.state.ProjectMetaHidden {
		sub = ""
	}
	row := a.navRowAt(c, "head", false, icon, p.Name, sub, "", trailing)
	if row.Clicked() {
		*open = !*open
	}
	contextMenu(c, row, func(m *popMenu) { a.workspaceMenu(c, m, ws[0]) })
}

// navDisclosure 是侧栏行尾的展开箭头：行尾的内容离行边 navPad、行离列表边 navEnd，箭头要再往右挪一点，
// 才和顶栏、底栏的图标在一条线上。
func navDisclosure(c *ui.Context, open *bool, expand, collapse string) {
	shift := navEnd + navPad + miniBox()/2 - sideIconCenter()
	disclosure(c, open, expand, collapse).Margin(0, -shift, 0, 0)
}

// sideIconCenter 是侧栏右边一列图标的中线离侧栏右边缘的距离：顶栏的侧栏开关、底栏的齿轮（iconBarPad 让图标离边 trafficLightX），
// 项目列表的筛选和各行的展开箭头都对着它。
func sideIconCenter() float32 { return trafficLightX + iconSize/2 }

// disclosure 是展开箭头，展开时转向下；expand、collapse 是收起和展开时的说明（"展开…""收起…"）。
func disclosure(c *ui.Context, open *bool, expand, collapse string) ui.Element {
	label := expand
	if *open {
		label = collapse
	}
	t := c.Theme()
	b := ui.Box(c).Size(miniBox(), miniBox()).Radius(rSmall).Center().Shrink(0).Label(label).Tooltip(label)
	bg, fg := ui.Transparent, t.TextMuted
	if b.Hovered() {
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		turn := float32(0)
		if *open {
			turn = 90
		}
		chev := ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(fg)
		chev.Rotate(chev.AnimateWith("turn", turn, 150*time.Millisecond, ui.EaseOut))
	})
	if b.Clicked() {
		*open = !*open
	}
	return b
}

// summaryTrailing 是行尾的运行终端数和最要紧的状态。
func summaryTrailing(c *ui.Context, running int, status string) {
	if status == agent.StatusWaiting {
		pendingLabel(c)
	} else if running > 0 {
		ui.Text(c, fmt.Sprint(running)).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted).Tooltip(L(c).SidebarRunningTip(running))
	}
	if status != "" {
		statusGlyph(c, status, smallIcon())
	}
}

// agentSessions 是侧栏列出的会话：运行中和等着恢复的 agent 终端，普通 shell 只在标签栏。
func agentSessions(ts []terminalInfo) []terminalInfo {
	var out []terminalInfo
	for _, t := range ts {
		if t.Agent != "" && (t.Running || t.Lost) {
			out = append(out, t)
		}
	}
	return out
}

// selectedSession 返回工作区当前显示的 agent 会话；当前是 shell、文件或没有终端时为空。
func (a *app) selectedSession(w *workspace) string {
	if w == nil || a.filesActive(w) {
		return ""
	}
	if cur := a.currentTerminal(w); cur != nil && cur.Agent != "" && (cur.Running || cur.Lost) {
		return cur.ID
	}
	return ""
}

// sidebarItem 是侧栏里的一个工作区。nested 表示在项目标题下面（主工作区和同目录工作区的路径标题行已经写了）；
// collapsed 表示它的会话收起了，这时行尾汇总会话状态，当前会话也算选中这一行。extra 画在行尾最后（展开箭头）。
func (a *app) sidebarItem(c *ui.Context, w workspace, label string, nested, collapsed bool, extra func()) ui.Element {
	running, status := a.terminalSummary(w.Terminals)
	if !collapsed && len(agentSessions(w.Terminals)) > 0 {
		// 会话在下面各自显示状态
		status = ""
	}
	var trailing func()
	if status != "" || running > 0 || extra != nil {
		trailing = func() {
			summaryTrailing(c, running, status)
			if extra != nil {
				extra()
			}
		}
	}
	// worktree 都在 $POSSE_HOME/worktrees 下，路径截断后看不出什么，改显示分支；
	// 分支默认和工作区同名，同名时只留一行
	sub := shortPath(w.Path)
	if nested {
		sub = ""
	}
	if w.Kind == "worktree" {
		sub = w.Branch
		if sub == label {
			sub = ""
		}
	}
	// 项目目录和同目录的工作区：第二行先写当前分支（worktree 的第二行已经是分支）
	branch := ""
	if w.Kind != "worktree" {
		branch = w.Branch
	}
	if (!nested && a.state.ProjectMetaHidden) || (nested && a.state.WorkspaceMetaHidden) {
		sub, branch = "", ""
	}
	// 加进来的 worktree 名字已经是分支，不再写一遍
	if w.Shown != "" && branch == label {
		branch = ""
	}
	if w.Missing {
		sub, branch = "", ""
		more := trailing
		trailing = func() {
			ui.Text(c, L(c).SidebarMissing()).FontSize(fsSmall).TextColor(c.Theme().TextMuted).Shrink(0)
			if more != nil {
				more()
			}
		}
	}
	selected := a.selected == w.ID && (collapsed || a.selectedSession(a.workspace(w.ID)) == "")
	if selected || collapsed || len(agentSessions(w.Terminals)) == 0 {
		a.navOrder = append(a.navOrder, navTarget{ws: w.ID})
		if selected {
			a.navCur = len(a.navOrder) - 1
		}
	}
	t := c.Theme()
	glyph := a.wsIcon(&w)
	if w.Kind == "main" {
		glyph = iconHome
	}
	icon := func() { ui.Icon(c, glyph).FontSize(iconSize).TextColor(t.Accent).Shrink(0) }
	if !nested {
		// 没有其他工作区时这一行就是项目行，用项目的图标
		icon = func() { a.projectGlyph(c, a.projIcons[w.ProjectID], a.wsIcon(&w)) }
	}
	item := a.navRowAt(c, w.ID, selected, icon, label, sub, branch, trailing)
	if nested {
		// 项目下面的工作区行能拖到顶上固定；单行的项目整行拖的是项目（排序），顶上也接
		item.Drag(wsDrag{w.ID})
		if item.Dragging() {
			item.Opacity(0.4)
		}
	}
	if item.Clicked() {
		a.selected = w.ID
	}
	if a.revealSel && selected {
		item.ScrollIntoView()
		a.revealSel = false
	}
	contextMenu(c, item, func(m *popMenu) { a.workspaceMenu(c, m, w) })
	return item
}

// sessionRows 把工作区里的 agent 会话缩进列在它下面，单击切到对应的终端标签。
// links 不为空时 w 在多工作区项目的列表里：派到同一项目别的工作区的 worker 不画影子行，记进 links 由列表直接连线。
func (a *app) sessionRows(c *ui.Context, w workspace, sessions []terminalInfo, links *remoteLinks) {
	if len(sessions) == 0 {
		return
	}
	cur := ""
	if a.selected == w.ID {
		cur = a.selectedSession(a.workspace(w.ID))
	}
	// 任务的 worker 排在派发它的会话下面、多缩进一层；派发方不在这个工作区（别的工作区、用户派发）的照常排。
	// 派到别的项目的 worker 排在派发方同一工作区的 worker 后面，是一行影子，连上线、点了跳过去；
	// 同一项目里的两头都在侧栏这一块，直接连过去
	top, children := a.sessionTree(sessions)
	for _, term := range sessions {
		for _, k := range a.remoteWorkers[term.ID] {
			if links != nil && k.home.ProjectID == w.ProjectID {
				links.link(term.ID, k.term.ID)
			} else {
				children[term.ID] = append(children[term.ID], k)
			}
		}
	}
	// worker 最多嵌套 3 层，这里多留一层余量，防止数据成环时无限递归；影子行底下不再展开
	var below func(id string, depth int) []terminalInfo
	below = func(id string, depth int) []terminalInfo {
		var out []terminalInfo
		for _, k := range children[id] {
			out = append(out, k.current())
			if depth < 4 && k.home == nil {
				out = append(out, below(k.term.ID, depth+1)...)
			}
		}
		return out
	}
	// 缩进不用 Padding：每行自己画缩进、连接线和状态槽，选中底色铺满整行
	// prefix 是上面几层的连线；nested 时本层的每行再加一列，记它后面还有没有同一个派发方的 worker
	var rows func(list []sessionNode, prefix treeGuide, nested bool)
	rows = func(list []sessionNode, prefix treeGuide, nested bool) {
		ui.Column(c).Gap(navRowGap).Children(func() {
			for i, node := range list {
				term, g := node.term, prefix
				if nested {
					g = append(slices.Clone(prefix), i < len(list)-1)
				}
				if node.home != nil {
					term = node.current()
					a.navSession(c, *node.home, term, false, nil, nil, a.whereOf(c, *node.home), g, true)
					continue
				}
				kids := children[term.ID]
				if len(kids) == 0 || len(g) >= 4 {
					line, hasStatus := a.navSession(c, w, term, cur == term.ID, nil, nil, "", g, false)
					links.row(term.ID, line, len(g), hasStatus)
					continue
				}
				open := a.workersOpen[term.ID]
				if open == nil {
					open = new(bool)
					*open = true
					if a.workersOpen == nil {
						a.workersOpen = map[string]*bool{}
					}
					a.workersOpen[term.ID] = open
				}
				all := below(term.ID, len(g)+1)
				hidden := slices.ContainsFunc(all, func(k terminalInfo) bool { return k.ID == cur })
				// 要定位的会话收在里面时先展开
				if !*open && hidden && a.revealSel {
					*open = true
				}
				// 收起时当前会话在里面，这一行算选中
				line, hasStatus := a.navSession(c, w, term, cur == term.ID || (!*open && hidden), all, open, "", g, false)
				links.row(term.ID, line, len(g), hasStatus)
				if *open {
					rows(kids, g, true)
				}
			}
		})
	}
	rows(top, nil, false)
}

// whereOf 是工作区的「项目 · 工作区」，影子行的第二行写它。
func (a *app) whereOf(c *ui.Context, w workspace) string {
	p := a.projectByID(w.ProjectID)
	if p == nil {
		return cmp.Or(w.Shown, w.Name)
	}
	return workspaceWhere(L(c), *p, a.byProject[w.ProjectID], w)
}

// navRowGap 是侧栏会话行之间的间隔，连接线要跨过它。
const navRowGap = 2

// navSession 是侧栏里的一个 agent 会话。有 worker 时 workers 是它下面所有层的 worker，open 是它们是否展开：
// 和项目行一样，单击切到这个会话、收着的顺便展开，已经在看它时再单击收起；收起时行尾写 +N，
// 有 worker 等你处理时这一行也显示待处理。where 非空时是「运行中」视图里的平铺行：第二行写它在哪个项目和工作区，不能拖动排序。
// guide 是 worker 的连接线，一层嵌套一列：状态槽之前先缩进这么多层，状态紧挨着自己的 agent 图标，
// 竖线走在上面几层派发方的状态槽下面。shadow 是派到别的工作区的 worker 的影子行：w 是它真正所在的工作区，
// 文字淡一些，点了切过去并滚到它真正那一行；方向键不经过它，免得同一个会话走两遍。
// 返回这一行和状态槽里有没有图形，给同一项目里跨工作区的连线定位。
func (a *app) navSession(c *ui.Context, w workspace, term terminalInfo, selected bool, workers []terminalInfo, open *bool, where string, guide treeGuide, shadow bool) (ui.Element, bool) {
	depth := len(guide)
	if !shadow {
		a.navOrder = append(a.navOrder, navTarget{ws: w.ID, term: term.ID})
		if selected {
			a.navCur = len(a.navOrder) - 1
		}
	}
	// 和标签一样：任务 worker 用任务标题
	name, detail := fullLabel(L(c), term)
	if title, ok := a.workerTitle[term.ID]; ok && term.Name == "" {
		detail = title
	}
	if detail == "" {
		detail = name
	}
	status, statusID := a.sessionStatus(term), term.ID
	folded := open != nil && !*open
	if folded {
		// 收起的 worker 里最要紧的状态是待处理或完成未读时，派发方这一行替它们显示
		for _, k := range workers {
			if ks := a.sessionStatus(k); (ks == agent.StatusWaiting || ks == statusUnread) && statusRank(ks) > statusRank(status) {
				status, statusID = ks, k.ID
			}
		}
	}
	var trailing func()
	if status == agent.StatusWaiting || status == statusUnread || folded {
		trailing = func() {
			switch status {
			case agent.StatusWaiting:
				pendingLabel(c)
			case statusUnread:
				doneLabel(c)
			}
			if folded {
				ui.Text(c, fmt.Sprintf("+%d", len(workers))).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted).
					Tooltip(L(c).SidebarFoldedWorkers(len(workers)))
			}
		}
	}
	// 行首一直是 agent 图标，忙起来也认得出是谁；状态画在图标左边的槽里，对着上一层行首图标的中线。
	// 点击、拖动和悬停都按包着状态槽的整行算；底色从第一个可见图形前 navPad 处开始：
	// 有状态图形时铺满整行，状态不会飘在底色外面，槽空着时跳过缩进和空槽，从 agent 图标前开始
	icon := func() { agentTile(c, term.Agent, smallIcon()).Tooltip(name) }
	key := "line-" + term.ID
	if shadow {
		key = "shadow-" + term.ID
	}
	line := ui.Row(c.Key(key)).AlignItems(ui.Center).Radius(rControl).Shrink(0).Label(detail).Role(ui.RoleButton)
	var bg ui.Color
	if status != "" {
		selectedBg(c, line, selected)
	} else {
		bg = selectedColor(c, line, selected)
	}
	// 展开着 worker、自己又没有状态图形时，下面 worker 的竖线没有东西可接，顶端拐过来接到自己的 agent 图标
	head := open != nil && *open && status == ""
	if depth > 0 || head || bg.A > 0 {
		color, hasStatus := colors(c.Theme()).guide, status != ""
		line.Draw(func(p *ui.Painter, r ui.Rect) {
			// 先画底色，连接线横着伸进来接到图标前时压在底色上面
			if bg.A > 0 {
				inset := float32(depth+1) * navIndent
				p.Fill(ui.Rect{X: r.X + inset, Y: r.Y, W: r.W - inset, H: r.H}, bg, rControl)
			}
			drawGuide(p, r, guide, hasStatus, navRowGap, color)
			if head {
				drawGuideHead(p, r, depth, color)
			}
		})
	}
	line.Children(func() {
		if depth > 0 {
			ui.Box(c).Width(float32(depth) * navIndent).Shrink(0)
		}
		slot := statusSlot(c, func() {
			if status != "" {
				a.statusIcon(c, statusID, status, smallIcon())
			}
		})
		content := a.navRowSized(c, term.ID, false, icon, detail, fsSmall, where, "", trailing).Grow(1).Shrink(1).Basis(0).Background(ui.Transparent).Role(ui.RoleNone)
		if shadow {
			slot.Opacity(shadowDim)
			content.Opacity(shadowDim)
		}
	})
	row := line
	if where == "" {
		if ws := a.workspace(w.ID); ws != nil {
			a.reorderableTab(c, row, ws, term.ID, -1)
		}
	}
	if row.Clicked() {
		viewing := a.selected == w.ID && a.activeTerm[w.ID] == term.ID
		if open != nil {
			*open = !*open || !viewing
		}
		a.selected = w.ID
		a.activeTerm[w.ID] = term.ID
		if shadow {
			a.revealSel = true
		}
	}
	if a.revealSel && selected {
		row.ScrollIntoView()
		a.revealSel = false
	}
	contextMenu(c, row, func(m *popMenu) {
		l := L(c)
		a.sessionMenu(l, m, term, func() {
			// 名字在标签上改：先照单击切到这个会话，标签才看得见
			a.selected = w.ID
			a.activeTerm[w.ID] = term.ID
		})
		m.Separator()
		if m.Item(l.SidebarCloseSession()).Icon(iconClose).Danger().Chosen() {
			a.askClose(l, term)
		}
	})
	return line, status != ""
}

// statusTasks 是界面自己的状态：会话在等它派出去的任务（阻塞在 posse task wait 上，或者停下来等 Posse 提醒），
// 不是在干活，也不用用户处理。
const statusTasks = "tasks"

// statusLost 是界面自己的状态：上一个常驻进程留下、还没恢复的终端，切到它时才拉起。
const statusLost = "lost"

// sessionStatus 是会话显示的状态：有进行中的任务、自己又没在干别的时是"等待任务"。
func (a *app) sessionStatus(term terminalInfo) string {
	if term.Lost {
		return statusLost
	}
	status := ""
	if term.AgentState != nil {
		status = term.AgentState.Status
	}
	if a.runningFor[term.ID] > 0 && (term.WaitingTasks || status == agent.StatusIdle) {
		return statusTasks
	}
	return a.shownStatus(term, status)
}

// workspaceMenu 是工作区行（和项目标题行）的右键菜单。
func (a *app) workspaceMenu(c *ui.Context, m *popMenu, w workspace) {
	lm := L(c)
	for _, ag := range a.agentChoices() {
		if m.Item(lm.TermStart(agentTitle(ag))).Lead(func() { agentIcon(c, ag, iconSize) }).Chosen() {
			a.startTerminal(w.ID, ag)
		}
	}
	if m.Item(lm.SidebarOpenTerminal()).Lead(func() { agentIcon(c, "", iconSize) }).Chosen() {
		a.startTerminal(w.ID, "")
	}
	m.Separator()
	if m.Item(lm.CommonOpenInFinder()).Icon(iconFolder).Chosen() {
		if err := mygo.Shell.OpenPath(w.Path); err != nil {
			a.err = lm.SidebarOpenInFinderError(errtext.Text(err))
		}
	}
	if hasBase(&w) {
		m.Separator()
		if m.Item(lm.SidebarMergeInto(w.BaseRef)).Icon(iconMerge).Chosen() {
			a.askMergeWorkspace(lm, w.ID)
		}
	} else if w.Kind == "worktree" && len(w.Repos) > 0 {
		m.Separator()
		if m.Item(lm.SidebarMergeRepos()).Icon(iconMerge).Chosen() {
			a.askMergeRepos(lm, w.ID)
		}
	}
	m.Separator()
	// 聊天工作区已经有「聊天」磁贴
	if !a.isChat(w.ID) {
		if a.pinned(w.ID) {
			if m.Item(lm.SidebarUnpin()).Icon(iconPin).Chosen() {
				a.unpin(w.ID)
			}
		} else if !a.canPin() {
			m.Item(lm.SidebarPinFull(topTilesMax)).Icon(iconPin).Disabled(true)
		} else if m.Item(lm.SidebarPin()).Icon(iconPin).Chosen() {
			a.pin(w.ID)
		}
	}
	if w.Kind == "main" && m.Item(lm.SidebarChangeIcon()).Icon(iconImage).Chosen() {
		if p := a.projectByID(w.ProjectID); p != nil {
			a.editProjectIcon(*p)
		}
	}
	if p := a.projectByID(w.ProjectID); w.Kind == "main" && p != nil && m.Item(lm.SidebarMultiRepo()).Checked(!p.SingleRepo).Chosen() {
		a.setSingleRepo(p.ID, !p.SingleRepo)
	}
	if m.Item(lm.SidebarNewWorkspaceHere()).Icon(iconPlus).Chosen() {
		a.openNewWorkspace()
		a.newProject = w.Project
	}
	// 项目行就是项目目录本身，删不了这个工作区，删的是整个项目
	if w.Kind == "main" {
		if m.Item(lm.SidebarCleanTraces()).Icon(iconEraser).Chosen() {
			a.askCleanTraces(lm, w)
		}
		if m.Item(lm.SidebarRemoveProject()).Icon(iconTrash).Danger().Chosen() {
			a.askRemoveProject(lm, w)
		}
	} else if m.Item(lm.SidebarDeleteWorkspace()).Icon(iconTrash).Danger().Chosen() {
		a.askRemoveWorkspace(lm, w)
	}
}

// projectDrag 是侧栏里拖动的项目。
type projectDrag struct{ id string }

// reorderable 让侧栏里代表项目 id 的行（单行项目或整个分组）可以拖动排序：
// 按住拖到另一个项目上，占到它的位置；拖动中原处变淡，目标上画一道强调色的线表示落点。
func (a *app) reorderable(c *ui.Context, e ui.Element, id string) {
	e.Drag(projectDrag{id})
	if e.Dragging() {
		e.Opacity(0.4)
	}
	if d, over := ui.DragOver[projectDrag](e); over && d.id != id {
		dropMark(c, e, projectIndex(a.projects, d.id) < projectIndex(a.projects, id), false)
	}
	if d, ok := ui.Drop[projectDrag](e); ok && d.id != id {
		a.state.ProjectOrder = moveProject(a.projects, d.id, id)
		sortProjects(a.projects, a.state.ProjectOrder)
		a.saveState()
		c.AnimationFrame() // 这一帧已经按旧顺序画了，再画一帧
	}
}

// tabDrag 是拖动中的标签：标签栏的终端、文件，或侧栏的会话（key 是终端 ID 或 docKey），只能落在同一个工作区里。
type tabDrag struct{ ws, key string }

// reorderableTab 让标签栏的标签（col 是第几格）或侧栏的会话行（col 为 -1）可以拖动：
// 拖到同类的标签上排序，终端的顺序标签栏和侧栏共用（ui.json 的 termOrder），文件的随打开的文件存；
// 落在别的格的标签上还移进那一格。样子和拖项目一样，标签栏里的落点画成竖线。
func (a *app) reorderableTab(c *ui.Context, e ui.Element, w *workspace, key string, col int) {
	e.Drag(tabDrag{w.ID, key})
	if e.Dragging() {
		e.Opacity(0.4)
	}
	index := func(k string) int { return slices.Index(a.tabKeys(w), k) }
	same := func(k string) bool { return isDocKey(k) == isDocKey(key) }
	moves := func(k string) bool { return col >= 0 && a.colOf(k) != col && a.cols[col].id != "" }
	if g, over := ui.DragOver[tabDrag](e); over && g.ws == w.ID && g.key != key && (same(g.key) || moves(g.key)) {
		if same(g.key) {
			dropMark(c, e, index(g.key) < index(key), col >= 0)
		} else {
			dropMark(c, e, true, true)
		}
	}
	if g, ok := ui.Drop[tabDrag](e); ok && g.ws == w.ID && g.key != key {
		if same(g.key) {
			a.reorderTabs(w, g.key, key)
		}
		if moves(g.key) {
			a.moveTab(w, g.key, col, sideIn)
		}
		c.AnimationFrame() // 这一帧已经按旧顺序画了，再画一帧
	}
}

// reorderTabs 把标签 from 挪到同类的标签 to 的位置（往后拖落在它后面，往前拖落在它前面）。
func (a *app) reorderTabs(w *workspace, from, to string) {
	if !isDocKey(from) {
		if isDocKey(to) {
			return
		}
		order := moveTerminal(w.Terminals, from, to)
		if a.state.TermOrder == nil {
			a.state.TermOrder = map[string][]string{}
		}
		a.state.TermOrder[w.ID] = order
		sortTerminals(w.Terminals, order)
		a.saveState()
		return
	}
	v := a.files[w.ID]
	if v == nil || !isDocKey(to) {
		return
	}
	idx := func(k string) int { return slices.IndexFunc(v.docs, func(d *fileDoc) bool { return docKey(d) == k }) }
	if i, j := idx(from), idx(to); i >= 0 && j >= 0 {
		moved := v.docs[i]
		v.docs = slices.Insert(slices.Delete(v.docs, i, i+1), j, moved)
		a.saveFilesState(v)
	}
}

// dropMark 在拖动排序的目标上画一道强调色的线表示落点，after 表示落在它后面（下面或右边）；
// across 表示横排，线是竖的。
func dropMark(c *ui.Context, e ui.Element, after, across bool) {
	accent := c.Theme().Accent
	e.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if across {
			x := r.X
			if after {
				x = r.X + r.W - 2
			}
			p.Fill(ui.Rect{X: x, Y: r.Y + 4, W: 2, H: r.H - 8}, accent, 1)
			return
		}
		y := r.Y - 1
		if after {
			y = r.Y + r.H - 1
		}
		p.Fill(ui.Rect{X: r.X + 8, Y: y, W: r.W - 16, H: 2}, accent, 1)
	})
}

// navRow 是侧栏的一行，sub 非空时在名称下面用等宽小字显示（路径或分支），branch 非空时在 sub 上面单独一行；
// 选中和悬停的样式见 selectedBg。
func (a *app) navRow(c *ui.Context, id string, icon *ui.SVG, label, sub, branch string, trailing func()) ui.Element {
	selected := a.navMark(id)
	accent := c.Theme().Accent
	row := a.navRowAt(c, id, selected, func() { ui.Icon(c, icon).FontSize(iconSize).TextColor(accent).Shrink(0) }, label, sub, branch, trailing)
	if row.Clicked() {
		if id == taskBoardID {
			a.showTaskHome()
		}
		a.selected = id
	}
	return row
}

// navTile 是侧栏顶上的全局页面磁贴：几个平分一行，图标下面写名字；
// badge 非空时画在右上角（待处理、手机在线）。点击和方向键顺序同 navRow。
func (a *app) navTile(c *ui.Context, id string, icon *ui.SVG, label string, badge func()) ui.Element {
	selected := a.navMark(id)
	tile := sideTile(c, id, selected, func() { ui.Icon(c, icon).FontSize(iconSize).TextColor(c.Theme().Accent) }, label, badge)
	if tile.Clicked() {
		if id == taskBoardID {
			a.showTaskHome()
		}
		a.selected = id
	}
	return tile
}

// sideTile 画侧栏顶上的一块磁贴：上面是图标，下面是名字（放不下时截断），badge 在右上角。
func sideTile(c *ui.Context, key string, selected bool, icon func(), label string, badge func()) ui.Element {
	t := c.Theme()
	p := colors(t)
	tile := ui.Column(c.Key(key)).Height(tileHeight()).Grow(1).Basis(0).Gap(4).Radius(rControl).Center().
		Label(label).Role(ui.RoleButton)
	bg := p.fill
	switch {
	case selected:
		bg = p.tint
	case tile.Hovered():
		bg = p.wash
	}
	tile.Background(tile.AnimateColor("bg", bg, hover))
	tile.Children(func() {
		icon()
		name := ui.Text(c, label).FontSize(fsSmall).SingleLine()
		// 名字放不下时截断：宽度取上一帧的，刚出现的磁贴还没量过，先不限
		if w := tile.Bounds().W; w > 0 {
			name.MaxWidth(max(w-12, 0))
		}
		if badge != nil {
			ui.Row(c).Absolute().Top(4).Right(4).AlignItems(ui.Center).Children(badge)
		}
	})
	return tile
}

// navMark 把全局页面 id 登记进侧栏方向键的顺序，返回它是否选中。
func (a *app) navMark(id string) bool {
	selected := a.selected == id
	a.navOrder = append(a.navOrder, navTarget{ws: id})
	if selected {
		a.navCur = len(a.navOrder) - 1
	}
	return selected
}

// tileHeight 是侧栏顶上磁贴的高度，网格的行高和拖进来时的空格都用它。
func tileHeight() float32 { return sz(56) }

// navRowAt 画侧栏的一行，选中、点击和方向键顺序由调用方处理；icon 画行首的图标。
func (a *app) navRowAt(c *ui.Context, key string, selected bool, icon func(), label, sub, branch string, trailing func()) ui.Element {
	return a.navRowSized(c, key, selected, icon, label, fsBody, sub, branch, trailing)
}

// navRowSized 同 navRowAt，名称用 size 字号：agent 会话行照 Orca 用小一号的字。
func (a *app) navRowSized(c *ui.Context, key string, selected bool, icon func(), label string, size float32, sub, branch string, trailing func()) ui.Element {
	t := c.Theme()
	height, gap := sz(32), float32(2)
	switch {
	case sub != "" && branch != "":
		height, gap = sz(64), 2
	case sub != "" || branch != "":
		height = sz(46)
	}
	row := ui.Row(c.Key(key)).Height(height).Padding(0, navPad).Gap(8).Radius(rControl).Shrink(0).AlignItems(ui.Center).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, selected)
	row.Children(func() {
		icon()
		ui.Column(c).Gap(gap).Grow(1).Shrink(1).Children(func() {
			// 选中只靠底色区分，文字不加粗
			ui.Text(c, label).FontSize(size).SingleLine()
			// 分支独占一行，路径在它下面
			if branch != "" {
				ui.Row(c).Gap(2).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconBranch).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
					ui.Text(c, branch).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
				})
			}
			if sub != "" {
				ui.Text(c, sub).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine()
			}
		})
		// 行尾的数字、状态和箭头在整行里上下居中，两行的工作区也一样
		if trailing != nil {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(0).Children(trailing)
		}
	})
	return row
}

// staleBar 提示窗口的 Posse.app 已被换掉：不重开的话，桌面、文稿里的项目读不了文件、跑不了 git。
func (a *app) staleBar(c *ui.Context) {
	if !a.staleApp || !a.loaded || a.staleHidden {
		return
	}
	// 收起只在这次窗口里有效：不重开，桌面、文稿里的项目一直读不了文件，底栏留圆点提醒
	hide := func() { a.staleHidden = true }
	m := L(c)
	sideNotice(c, false, m.SidebarUpdatedTitle(), m.SidebarUpdatedBody(), hide, func() {
		if pressable(accentButton(c, m.SidebarReopenWindow())).Clicked() {
			a.relaunch(L(c))
		}
	})
}

// sideNotice 是侧栏底部的提示卡（重开窗口、新版本已就绪、操作报错）：淡底加图标，和上面的项目列表分得开，
// 一眼看出不是列表里的一项。danger 是报错：红色淡底、警示图标。title 为空时正文接在图标后面；
// onClose、actions 为空时不画。
func sideNotice(c *ui.Context, danger bool, title, body string, onClose func(), actions func()) {
	t := c.Theme()
	bg, icon, tone := colors(t).tint, iconRefresh, t.Accent
	if danger {
		bg, icon, tone = soft(t, t.Danger), iconInfo, t.Danger
	}
	ui.Column(c).Margin(0, 12, 8, 12).Padding(12, 8, 12, 12).Gap(8).Radius(rCard).Background(bg).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Start).Children(func() {
			ui.Icon(c, icon).FontSize(smallIcon()).TextColor(tone).Shrink(0)
			if title != "" {
				ui.Text(c, title).FontSize(fsBody).FontWeight(fwMedium).Grow(1).Shrink(1).SingleLine()
			} else {
				ui.Text(c, body).FontSize(fsSmall).LineHeight(1.45).Grow(1).Shrink(1)
			}
			if onClose != nil {
				if iconButtonSized(c, iconClose, L(c).SidebarDismissNotice(), compactBox(), smallIcon()).Clicked() {
					onClose()
				}
			}
		})
		if title != "" {
			ui.Text(c, body).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
		}
		if actions != nil {
			actions()
		}
	})
}

func (a *app) errorBar(c *ui.Context) {
	if a.err == "" || !a.loaded {
		return
	}
	sideNotice(c, true, "", a.err, func() { a.err = "" }, nil)
}

func (a *app) footer(c *ui.Context) {
	t, l := c.Theme(), L(c)
	if !a.loaded {
		return
	}
	ui.Row(c).Height(footHeight).Padding(0, iconBarPad()).Shrink(0).Background(footShade(t)).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line).Children(func() {
		menuButton(c, iconButton(c, iconPlus, l.CommonNew()), func(m *popMenu) {
			if m.Item(l.SidebarAddProject()).Chosen() {
				go a.addProject(l)
			}
			if m.Item(l.SidebarNewWorkspace()).Keys(a.keyText("newWorkspace")).Chosen() {
				a.openNewWorkspace()
			}
			if m.Item(l.TasksDispatchEllipsis()).Chosen() {
				a.openDispatch()
			}
			m.Separator()
			if m.Item(l.SidebarConnectPhone()).Chosen() {
				a.openMobile()
			}
		})
		ui.Spacer(c)
		if a.staleApp && a.staleHidden {
			a.staleDot(c)
		}
		if iconButton(c, iconSettings, a.withKey(l.SettingsTitle(), "settings")).Clicked() {
			a.openSettings()
		}
	})
}

// staleDot 是收起重开提示后留在齿轮旁的圆点，点了把提示卡放回来。
func (a *app) staleDot(c *ui.Context) {
	t := c.Theme()
	label := L(c).SidebarUpdatedDot()
	b := ui.Box(c).Size(buttonBox(), buttonBox()).Radius(buttonBox() / 4).Center().Shrink(0).Label(label).Tooltip(label)
	bg := ui.Transparent
	if b.Hovered() {
		bg = t.SurfaceHover
	}
	b.Background(bg).Children(func() {
		ui.Box(c).Size(sz(7), sz(7)).Radius(sz(7) / 2).Background(t.Accent)
	})
	if b.Clicked() {
		a.staleHidden = false
	}
}

// footHeight 是侧栏底栏和主区域状态栏的高度：两条并排，看起来是贯穿窗口的一条。
const footHeight = 30

// footShade 是底栏的底色，比侧栏深一层。
func footShade(t *ui.Theme) ui.Color { return colors(t).shade }

// mb 把 KB 写成界面上的内存：1 GB 以上用 GB、一位小数，以下用整数 MB。
func mb(kb int64) string {
	switch {
	case kb <= 0:
		return "—"
	case kb >= 1<<20:
		return fmt.Sprintf("%.1f GB", float64(kb)/(1<<20))
	}
	return fmt.Sprintf("%.0f MB", float64(kb)/1024)
}

// terminalSummary 返回运行中的终端数和其中 agent 最要紧的状态。
func (a *app) terminalSummary(ts []terminalInfo) (running int, status string) {
	for _, t := range ts {
		if !t.Running {
			continue
		}
		running++
		// 和侧栏会话行同一个口径：在等任务的会话不按它本轮完成没看算成铃铛
		if s := a.sessionStatus(t); statusRank(s) > statusRank(status) {
			status = s
		}
	}
	return running, status
}

// statusRank 是汇总状态时的优先级，等你处理最高，其次是完成了还没看的；不认识的状态不参与汇总。
func statusRank(s string) int {
	switch s {
	case agent.StatusWaiting:
		return 5
	case statusUnread:
		return 4
	case agent.StatusWorking:
		return 3
	case statusTasks:
		return 2
	case agent.StatusStarting:
		return 1
	}
	return 0
}

// statusGlyph 画状态的蜂巢格：形状区分进行中与已停下，颜色只是辅助。
func statusGlyph(c *ui.Context, status string, size float32) {
	t := c.Theme()
	p := colors(t)
	icon, color := iconHexOutline, t.TextMuted
	switch status {
	case agent.StatusWaiting:
		icon, color = iconHexAlert, p.honey
	case agent.StatusWorking:
		// 平时画星号（见下）；减少动态效果时退回静止的实心蜂巢格，用中性色
		icon, color = iconHexFill, t.TextMuted
	case agent.StatusIdle:
		icon, color = iconHexOutline, p.sage
	case agent.StatusStarting:
		icon, color = iconHexOutline, t.TextMuted
	case statusLost:
		icon, color = iconHexLost, t.TextMuted
	}
	if status == agent.StatusWorking && !c.Preferences().ReduceMotion {
		workingStar(c, size, t.Accent).Tooltip(statusText(L(c), status))
		return
	}
	if status == statusTasks {
		waitingPulse(c, size, t.Accent, c.Preferences().ReduceMotion).Tooltip(statusText(L(c), status))
		return
	}
	if status == statusUnread {
		icon, color = iconBell, t.Accent
	}
	ui.Icon(c, icon).FontSize(size).TextColor(color).Shrink(0).Tooltip(statusText(L(c), status))
}

// 工作中用 Claude Code 转圈的星号，来回换字符；每 starFrame 换一次。
const starFrame = 160 * time.Millisecond

var starFrames = []string{"·", "✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳", "✢"}

// workingStar 在绘制阶段换字符，只重绘不重建界面，用 After 约好下一次。
// 按同一个时钟算第几帧，几个工作中的图标一起变。
func workingStar(c *ui.Context, size float32, color ui.Color) ui.Element {
	return ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		now := time.Duration(p.Now().UnixNano())
		glyph := starFrames[int(now/starFrame)%len(starFrames)]
		p.After(starFrame - now%starFrame)
		fs := size * 1.15
		w, h := p.MeasureText(0, ui.Span{Text: glyph, Size: fs})
		// 按行框居中时星号的墨迹偏下约八分之一字号（实测），往上挪回来；"·" 本来就在中间
		lift := size * 0.125
		if glyph == "·" {
			lift = size * 0.02
		}
		p.Text(r.X+(r.W-w)/2, r.Y+(r.H-h)/2-lift, glyph, fs, color)
	})
}

// 等待任务的波形照 Orca 后台任务的 Activity 图标：一段亮的像心电图一样从左边进、右边出（pulseSweep），
// 两头加减速，之后停 pulseRest 再来一次。描的时候按 pulseFrame 重绘（只重绘不重建界面），停的时候不重绘：
// 平均每秒约 15 次，和原来 12 步一圈（每秒 10 次）相近；亮段位置按时间连续算，所以帧少也不跳格。
const (
	pulseSweep = 1000 * time.Millisecond
	pulseRest  = 300 * time.Millisecond
	pulseFrame = time.Second / 20
	pulseLen   = 0.4 // 亮段占整条波形的比例
)

// waitingPulse 画等待任务的波形；still 时（减少动态效果）整条画实，不动。
func waitingPulse(c *ui.Context, size float32, color ui.Color, still bool) ui.Element {
	return ui.Box(c).Size(size, size).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
		// 24×24 网格上的波形：平线、起、落、平线
		pts := [][2]float32{{2, 12}, {6.5, 12}, {9.5, 5}, {14.5, 19}, {17.5, 12}, {22, 12}}
		wave := func() *ui.Path {
			var path ui.Path
			k := r.W / 24
			for i, pt := range pts {
				x, y := r.X+pt[0]*k, r.Y+pt[1]*k
				if i == 0 {
					path.MoveTo(x, y)
				} else {
					path.LineTo(x, y)
				}
			}
			return &path
		}
		width := max(1.3, size/9)
		if still {
			p.StrokePath(wave(), width, color)
			return
		}
		p.StrokePath(wave(), width, color.Alpha(0.3))
		// 按同一个时钟算，几个等待中的图标一起动
		now := time.Duration(p.Now().UnixNano())
		at := now % (pulseSweep + pulseRest)
		if at >= pulseSweep {
			p.After(pulseSweep + pulseRest - at)
			return
		}
		p.After(min(pulseFrame-now%pulseFrame, pulseSweep-at))
		// 亮段的头从 0 走到 1+pulseLen，尾巴跟着走出右端
		x := float32(at) / float32(pulseSweep)
		head := smoothstep(x) * (1 + pulseLen)
		p.StrokePath(wave().Trim(max(0, head-pulseLen), min(1, head)), width, color)
	})
}

// posseLoader 是整块内容加载时的蜂巢动画，和手机端 PosseLoader（mobile/lib/ui/loading.dart）一样：
// 七格按真实蜂巢排，外圈六格从右上起顺时针逐格亮起、后面拖约两格的时间渐暗，1.2 秒一圈。
// 亮度按时间连续算，按 pulseFrame（每秒 20 次）重绘就够平滑，只重绘不重建界面；still 时停在一帧上。
func posseLoader(c *ui.Context, size float32, color ui.Color, still bool) ui.Element {
	const cycle = 1200 * time.Millisecond
	return ui.Box(c).Size(size, size).Shrink(0).Role(ui.RoleProgress).Label(L(c).TermLoading()).Draw(func(p *ui.Painter, r ui.Rect) {
		// 横向放得下 3√3 个外接圆半径加两道缝
		const gapRatio = 0.2
		sqrt3 := float32(math.Sqrt(3))
		rad := min(r.W, r.H) / (3*sqrt3 + 2*gapRatio)
		step := sqrt3*rad + gapRatio*rad
		cx, cy := r.X+r.W/2, r.Y+r.H/2
		round := rad * 0.22
		corner := func(x, y float32, i int) (float32, float32) {
			a := math.Pi/3*float64(i%6) - math.Pi/2
			return x + float32(math.Cos(a))*rad, y + float32(math.Sin(a))*rad
		}
		// 圆角六边形：每个角从两边各退 round 处用贝塞尔连过去，填充一次就成形，半透明的颜色不会叠出一圈深色
		cell := func(x, y float32, fill ui.Color) {
			var path ui.Path
			k := round / rad
			for i := range 6 {
				vx, vy := corner(x, y, i)
				px, py := corner(x, y, i+5)
				nx, ny := corner(x, y, i+1)
				fx, fy := vx+(px-vx)*k, vy+(py-vy)*k
				tx, ty := vx+(nx-vx)*k, vy+(ny-vy)*k
				if i == 0 {
					path.MoveTo(fx, fy)
				} else {
					path.LineTo(fx, fy)
				}
				path.QuadTo(vx, vy, tx, ty)
			}
			p.FillPath(path.Close(), fill)
		}
		dim := color.Alpha(0.18)
		cell(cx, cy, color.Alpha(0.28))
		phase := float32(0.2)
		if !still {
			now := time.Duration(p.Now().UnixNano())
			phase = float32(now%cycle) / float32(cycle)
			p.After(pulseFrame - now%pulseFrame)
		}
		t := phase * 6
		for i := range 6 {
			// 从右上那格开始顺时针
			a := math.Pi/3*float64(i) - math.Pi/3
			x, y := cx+float32(math.Cos(a))*step, cy+float32(math.Sin(a))*step
			// 亮过之后约两格的时间里慢慢暗下去
			since := float32(math.Mod(float64(t-float32(i))+6, 6))
			lit := max(0, 1-since/2.4)
			cell(x, y, dim.Mix(color, easeOut(lit)))
		}
	})
}

// easeOut 是先快后慢的缓动（Flutter 的 Curves.easeOut 近似）。
func easeOut(x float32) float32 { return 1 - (1-x)*(1-x)*(1-x) }

// smoothstep 是两头慢中间快的缓动。
func smoothstep(x float32) float32 { return x * x * (3 - 2*x) }

func statusText(m locales.Messages, status string) string {
	switch status {
	case agent.StatusWaiting:
		return m.TermStatusWaiting()
	case agent.StatusWorking:
		return m.TermStatusWorking()
	case agent.StatusIdle:
		return m.TermStatusIdle()
	case agent.StatusStarting:
		return m.TermStatusStarting()
	case agent.StatusExited:
		return m.TermStatusExited()
	case statusTasks:
		return m.TermStatusTasks()
	case statusUnread:
		return m.TermStatusUnread()
	case statusLost:
		return m.TermStatusLost()
	}
	return status
}

// --- 主区域

func (a *app) main(c *ui.Context, w *workspace, cur *terminalInfo) {
	t := c.Theme()
	p := colors(t)
	if w == nil {
		a.band(c, func() {})
		if a.loaded && len(a.projects) == 0 {
			a.firstRun(c)
		}
		return
	}
	a.overPanelButton = false
	a.markRead(cur)
	// 目录不在了就不读：每一下都失败
	if a.filesShown(w) && !w.Missing {
		a.filesTick(c, w)
	}
	// 文件栏停靠在最右边，挤窄左边的标签栏和终端（编辑器）
	// 打开、收起时文件栏从右边缘滑进滑出。终端只在开始（打开）或结束（收起）时改一次宽度：
	// 动画里每帧改宽度会让终端里的程序反复按新列数重排
	row := ui.Row(c).Grow(1).AlignItems(ui.Stretch)
	open := float32(0)
	if a.filesPanelOpen() {
		open = 1
	}
	a.filesPanelShown = row.AnimateTo("files-panel", open, ui.Animation{Duration: 200 * time.Millisecond, Ease: ui.EaseOut})
	row.Children(func() {
		// Basis(0)+Clip：标签多到放不下时左边自己裁掉，不把文件栏挤出窗口
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Clip().Children(func() { a.mainColumn(c, w, cur) })
		if a.filesPanelOpen() || a.filesPanelShown > 0 {
			width := a.filesPanelWidth() + 1
			ui.Box(c).Width(width).Shrink(0).Clip().Background(p.card).Children(func() {
				ui.Row(c).Absolute().Top(0).Bottom(0).Left((1 - a.filesPanelShown) * width).Width(width).AlignItems(ui.Stretch).Children(func() {
					vline(c)
					a.filesPanel(c, w)
				})
				// 开关固定在右上角，和关着时标签栏里的同一个位置（底下让出 1 像素的分隔线），不跟着文件栏滑动
				ui.Row(c).Absolute().Top(0).Right(iconBarPad()).Height(titleHeight(c)).Padding(0, 0, hairline, 0).AlignItems(ui.Center).Children(func() {
					a.filesPanelButton(c, w)
				})
			})
		}
	})
}

// mainColumn 是主区域里文件栏左边的部分：标签栏和终端（或编辑器），浮层面板也浮在这里面。
func (a *app) mainColumn(c *ui.Context, w *workspace, cur *terminalInfo) {
	// 各格自带标签栏，贴着顶边的那几格的标签栏在标题栏的位置；浮层盖在标签栏下面
	ui.Column(c.Key("main-area")).Grow(1).MinHeight(0).Children(func() {
		a.columnsView(c, w)
		a.mainBody(c, w, cur)
	})
}

// newTerminalButton 是第 i 格标签后面的"+"，选 agent 或普通终端，新终端放进这一格。
func (a *app) newTerminalButton(c *ui.Context, w *workspace, i int) {
	menuButton(c, iconButton(c.Key("new"), iconPlus, L(c).TabNewTerminal()), func(m *popMenu) {
		for _, ag := range slices.Concat(a.agentChoices(), []string{""}) {
			name := agentTitle(ag)
			if ag == "" {
				name = L(c).TermName()
			}
			it := m.Item(name).Lead(func() { agentIcon(c, ag, iconSize) })
			if ag == "" {
				it.Keys(a.keyText("newTerminal"))
			}
			if it.Chosen() {
				// 新标签放进焦点格：先把焦点换到这一格
				if i != a.pane {
					a.focusColumn(w, i, false)
				}
				a.startTerminal(w.ID, ag)
			}
		}
	})
}

// tabBarButtons 是标签栏右边的按钮组：分支、Git 日志、各个面板和文件栏的开关。
func (a *app) tabBarButtons(c *ui.Context, w *workspace) {
	if a.gitBranch(w) != "" {
		a.branchButton(c, w)
		b := iconButton(c.Key("git-log"), iconGitLog, a.withKey(L(c).GitLogTitle(), "gitLog"))
		if a.logPanelOpen() {
			b.Background(colors(c.Theme()).tint)
		}
		if b.Clicked() {
			a.toggleLogPanel(w)
		}
	}
	// 图片、技能与工具、命令速查跟着会话走，放在终端右上角（sessionButtons）；历史按项目，在文件栏里
	// 文件栏开着时开关在它的顶栏右端，和关着时同一个位置（照 Orca）
	if !a.filesPanelOpen() && a.filesPanelShown == 0 {
		a.filesPanelButton(c, w)
	}
}

// 会话按钮那一列离右边 findBarRight-sessionColGap：盖在右上角的终端查找栏右移 sessionColWidth 就让开了它，
// 两者之间留 sessionColGap
const (
	sessionColPad = 2
	sessionColGap = 12
)

// sessionColWidth 是会话按钮那一列的宽度。
func sessionColWidth() float32 { return buttonBox() + 2*sessionColPad }

// sessionButtons 是 agent 终端右上角竖排的一组按钮，都跟着这个会话走：worker 回到它的任务、
// 这个会话粘贴过的图片、用到的技能与工具、这家 agent 的命令速查。放在终端里而不是标签栏，标签多时也看得出是哪个会话的。
// 它盖在终端内容上：鼠标不在上面时变淡；底部的箭头收成贴着右边缘的小把手，点把手再展开（记在 ui.json）。
// rightmost 是这一格贴着主区域右边（没分栏，或最右一格）：右侧面板开着时会盖住它的右上角，
// 这一列跟着面板滑入往左让开，停在面板左边。
func (a *app) sessionButtons(c *ui.Context, cur *terminalInfo, rightmost bool) {
	task := a.taskByWorker(cur.ID)
	if cur.Agent == "" && task == nil {
		return
	}
	t := c.Theme()
	toggle := func() {
		a.state.SessionButtonsHidden = !a.state.SessionButtonsHidden
		a.saveState()
	}
	if a.state.SessionButtonsHidden {
		label := L(c).TabShowSessionButtons()
		h := ui.Box(c.Key("session-handle")).Absolute().Top(10).Right(-4).Size(16, 34).Radius(rRow).Center().
			Background(t.Surface).Label(label).Tooltip(label)
		fg := t.TextMuted
		if h.Hovered() {
			h.Background(t.SurfaceHover)
			fg = t.Text
		}
		h.Children(func() {
			// 箭头朝左，指向要展开的方向；往左挪一点，避开伸到边外的那部分
			ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(fg).Rotate(180).Margin(0, 4, 0, 0)
		})
		if h.Clicked() {
			toggle()
		}
		return
	}
	right := float32(findBarRight - sessionColGap)
	if rightmost && a.panelShown > 0 {
		right += a.panelShown * (panelWidth + sessionColGap)
	}
	col := ui.Column(c.Key("session-buttons")).Absolute().Top(8).Right(right).Gap(2).Padding(sessionColPad).Radius(rControl).Background(t.Surface) // 不透明：挡住底下的终端文字，不描边也和终端分得开
	// 鼠标不在上面、也没打开它的面板时变淡，底下的字透得出来
	ours := a.state.RightPanel == panelImages || a.state.RightPanel == panelUsage || a.state.RightPanel == panelCheat
	opacity := float32(0.5)
	if col.Hovered() || ours {
		opacity = 1
	}
	col.Opacity(col.AnimateTo("fade", opacity, ui.Animation{Duration: 160 * time.Millisecond, Ease: ui.EaseOut}))
	col.Children(func() {
		// 这一列的按钮不带悬停提示：它们贴着终端内容，提示会盖住旁边的字
		// agent 派发的 worker 回到派发方的会话，派发方终端不在了才回任务页；用户派发的回任务页
		if task != nil {
			if w, coord := a.coordinatorTerminal(task); coord != nil {
				if quietIconButton(c.Key("back-to-task"), iconTasks, L(c).TabBackToDispatcher(), buttonBox(), iconSize).Clicked() {
					a.openTerminal(w.ID, coord.ID)
				}
			} else if quietIconButton(c.Key("back-to-task"), iconTasks, L(c).TabBackToTask(), buttonBox(), iconSize).Clicked() {
				a.openTask(task.ID)
			}
		}
		if media.Supported(cur.Agent) {
			a.panelButton(c, panelImages, iconImage, L(c).TabSessionImages())
		}
		if cur.Agent != "" {
			a.panelButton(c, panelUsage, iconExtensions, L(c).TabSessionTools())
			a.panelButton(c, panelCheat, iconSlash, L(c).TabCheatSheet())
		}
		hline(c).Margin(2, 4)
		if quietIconButton(c.Key("session-fold"), iconChevron, L(c).TabHideSessionButtons(), buttonBox(), iconSize-4).Clicked() {
			toggle()
		}
	})
}

// phoneSizedBar 是终端上方的一行：手机正看着这个终端，它按手机屏幕排版、只占左边一截；
// 手机切到后台时连接常常还挂着，所以给一个按钮让电脑收回尺寸（手机重新打开这个终端才会再占）。
func (a *app) phoneSizedBar(c *ui.Context, id string) {
	t := c.Theme()
	p := colors(t)
	ui.Row(c.Key("phone-sized")).Height(sz(32)).Shrink(0).Padding(0, 8, 0, 16).Gap(8).AlignItems(ui.Center).
		Background(p.band).BorderWidth(0, 0, hairline, 0).BorderColor(p.line).Children(func() {
		ui.Icon(c, iconPhone).FontSize(fsBody).TextColor(t.Accent).Shrink(0)
		ui.Text(c, L(c).TermPhoneSized()).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if pressable(textButton(c, L(c).TermReclaimSize())).Clicked() {
			a.reclaimSize(L(c), id)
		}
	})
}

// missingBar 是目录不在了、但还开着终端或文件时，标签栏下面的一条提示。
func (a *app) missingBar(c *ui.Context, w *workspace) {
	t, m := c.Theme(), L(c)
	p := colors(t)
	ui.Row(c.Key("missing")).Height(sz(32)).Shrink(0).Padding(0, 8, 0, 16).Gap(8).AlignItems(ui.Center).
		Background(p.band).BorderWidth(0, 0, hairline, 0).BorderColor(p.line).Children(func() {
		ui.Icon(c, iconInfo).FontSize(fsBody).TextColor(t.Danger).Shrink(0)
		ui.Text(c, m.TabMissingFolder(shortPath(w.Path))).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(w.Path)
		ui.Spacer(c)
		if pressable(textButton(c, m.TabLocateFolder())).Clicked() {
			go a.relocateWorkspace(m, *w)
		}
		if pressable(textButton(c, missingRemoveLabel(m, w))).Clicked() {
			a.askRemoveMissing(m, *w)
		}
	})
}

// missingActions 是空工作区页在目录不在了时代替启动按钮的说明和操作。
func (a *app) missingActions(c *ui.Context, w *workspace) {
	t, m := c.Theme(), L(c)
	ui.Column(c).Gap(12).Padding(16).Radius(rCard).Background(soft(t, t.Danger)).Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconInfo).FontSize(smallIcon()).TextColor(t.Danger).Shrink(0)
			ui.Text(c, m.TabMissingTitle()).FontSize(fsBody).FontWeight(fwMedium)
		})
		ui.Text(c, m.TabMissingBody()).
			FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
		ui.Row(c).Gap(8).Children(func() {
			if pressable(accentButton(c, m.TabLocateFolder())).Clicked() {
				go a.relocateWorkspace(m, *w)
			}
			if pressable(textButton(c, missingRemoveLabel(m, w))).Clicked() {
				a.askRemoveMissing(m, *w)
			}
		})
	})
}

// missingRemoveLabel：项目目录不在时移除的是整个项目。
func missingRemoveLabel(m locales.Messages, w *workspace) string {
	if w.Kind == "main" {
		return m.SidebarRemoveProject()
	}
	return m.TabRemoveFromPosse()
}

// reclaimSize 让常驻进程放掉手机对终端尺寸的占用，按窗口的尺寸重排。
func (a *app) reclaimSize(m locales.Messages, id string) {
	for i := range a.workspaces {
		for j := range a.workspaces[i].Terminals {
			if a.workspaces[i].Terminals[j].ID == id {
				a.workspaces[i].Terminals[j].PhoneSized = false
			}
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := a.c.Call(ctx, "terminal.reclaimSize", map[string]any{"terminal": id}, nil); err != nil {
			a.update(func() { a.err = m.TermReclaimError(errtext.Text(err)) })
		}
	}()
}

// mainBody 是标签栏下面的内容：终端、编辑器或空工作区，浮层面板浮在它上面。
func (a *app) mainBody(c *ui.Context, w *workspace, cur *terminalInfo) {
	// 右侧面板、Git 日志这些浮在标签栏下面，不挤压终端：终端宽度一变，程序就要按新列数重排输出。
	// 这一层不接指针，底下的各格照常点得到
	body := ui.Column(c.Key("overlays")).Absolute().Top(titleHeight(c)).Left(0).Right(0).Bottom(0).PassThrough()
	if a.state.RightPanel != "" {
		a.lastPanel = a.state.RightPanel
	}
	// 打开时滑入淡入，关闭时反向；关掉后动画走完才移除，所以画的是最后打开的那个面板
	target := float32(0)
	if a.state.RightPanel != "" {
		target = 1
	}
	shown := body.AnimateTo("panel", target, ui.Animation{Duration: 180 * time.Millisecond, Ease: ui.EaseOut})
	a.panelShown = shown
	body.Children(func() {
		a.logPanel(c, w, body)
		a.branchPopupView(c, w)
		if shown < 0.01 {
			return
		}
		var panel ui.Element
		drawn := false
		switch a.lastPanel {
		case panelUsage:
			if cur != nil && cur.Agent != "" {
				panel, drawn = a.usagePanel(c, cur), true
			}
		case panelCheat:
			if cur != nil && cur.Agent != "" {
				panel, drawn = a.cheatPanel(c, cur), true
			}
		case panelImages:
			if cur != nil && cur.Agent != "" {
				panel, drawn = a.imagesPanel(c, cur), true
				a.imagePreview(c, body)
			}
		}
		if drawn {
			panel.Opacity(shown).Transform(ui.Translation((1-shown)*24, 0))
			// 点面板外面任何地方都收起，这一下照常落到下面的终端或按钮上
			if a.state.RightPanel != "" && panel.PressedOutside() && !a.overPanelButton {
				a.state.RightPanel = ""
				a.saveState()
			}
		}
	})
}

// termPane 画主区域的一格终端：终端视图，和浮在它上面的右键菜单、启动提示、会话按钮、查找栏、链接卡片。
func (a *app) termPane(c *ui.Context, i int, cur *terminalInfo) {
	t := c.Theme()
	v := a.panes[i]
	// 留一点内边距，文字不贴边
	area := ui.Box(c.Key(i)).Grow(1).Background(t.Background).Padding(6, 4, 2, 12).Ref(&a.termAreaRef[i])
	v.dir = v.term.Dir()
	area.Children(func() {
		tv := terminal.View(c.Key(v.id), v.term).Fill()
		// 分栏时只有焦点格自动拿焦点：几格都要的话谁拿到看搭界面的顺序
		if i == a.pane {
			tv.AutoFocus()
		}
		if a.focusTerm && i == a.pane {
			a.focusTerm = false
			tv.Focus()
		}
		// 右键菜单由终端插件交来位置，Posse 自己画（termMenu）
		if a.termCtxID == v.id {
			contextMenuAt(c, &a.termCtx, func(m *popMenu) { a.termMenu(L(c), m, v, v.id) })
		}
		a.startingHint(c, v, cur)
		a.sessionButtons(c, cur, a.cols[i].right)
		a.termFindBar(c, v, cur)
	})
	a.termDrop(c, area, v, cur.Agent)
	a.linkCardView(c, area, v.id)
	a.hoverPreviewView(c, area, v.id)
}

// startingHint 在程序还没画出任何内容时居中画蜂巢加载动画和"正在启动"：新开的终端要先跑完用户的
// shell 配置再启动 agent，Claude 要一两秒才画第一屏，这期间整块空白像是没反应。
// 接入后等 300ms 再显示，切到已有内容的终端时快照还没到不会闪一下；出现过内容就不再检查。
func (a *app) startingHint(c *ui.Context, v *termView, cur *terminalInfo) {
	const settle, giveUp = 300 * time.Millisecond, 20 * time.Second
	if v.painted || !cur.Running {
		return
	}
	since := time.Since(v.attached)
	if since > giveUp || strings.TrimSpace(v.term.Text()) != "" {
		v.painted = true
		return
	}
	c.After(100 * time.Millisecond)
	if since < settle {
		return
	}
	t := c.Theme()
	name := L(c).TermName()
	if cur.Agent != "" {
		name = agentTitle(cur.Agent)
	}
	label := L(c).TermStarting(name)
	ui.Box(c.Key("starting-hint")).Absolute().Fill().Center().Cursor(ui.CursorDefault).Children(func() {
		ui.Column(c).Gap(12).AlignItems(ui.Center).Children(func() {
			posseLoader(c, tileLarge(), t.Accent, c.Preferences().ReduceMotion)
			ui.Text(c, label).FontSize(fsBody).TextColor(t.TextMuted)
		})
	})
}

// firstRun 是还没有项目时的引导：空界面是去行动的邀请。
func (a *app) firstRun(c *ui.Context) {
	t, m := c.Theme(), L(c)
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).MaxWidth(440).Padding(24).Gap(16).Children(func() {
			tile(c, iconHexOutline, 48, soft(t, colors(t).honey), colors(t).honey)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, m.SidebarFirstRunTitle(), fsPage)
				ui.Text(c, m.SidebarFirstRunBody()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			ui.Row(c).Gap(8).Children(func() {
				if pressable(accentButton(c, m.SidebarAddProject())).Clicked() {
					go a.addProject(m)
				}
			})
		})
	})
}

func (a *app) emptyWorkspace(c *ui.Context, w *workspace) {
	t := c.Theme()
	p := colors(t)
	// 名称、启动按钮和快捷键是一整块，在主区域里上下左右居中，名称上面不再套卡片。
	// 窗口矮时可以滚动：滚动区里不能靠 Grow 撑满，用它上一帧的高度当内容的最小高度，变了再画一帧。
	sc := ui.Scroll(c.Key("empty-workspace")).Grow(1)
	// 宽度同理：主区域比 600 窄时（侧栏、右侧面板都开着）跟着收窄，启动格放不下一行就分两行
	if b := sc.LayoutBounds(); b.H != a.emptyHeight || b.W != a.emptyWidth {
		a.emptyHeight, a.emptyWidth = b.H, b.W
		c.AnimationFrame()
	}
	width := float32(600)
	if a.emptyWidth > 0 {
		width = max(min(width, a.emptyWidth-2*emptyPad), launchTileMin())
	}
	agents := a.agentChoices()
	sc.Children(func() {
		ui.Column(c).MinHeight(a.emptyHeight).Center().Padding(emptyPad).Children(func() {
			ui.Column(c).Width(width).Gap(16).Children(func() {
				ui.Row(c).Gap(16).Padding(0, 4).AlignItems(ui.Center).Children(func() {
					tile(c, a.wsIcon(w), 52, p.tint, t.Accent)
					ui.Column(c).Gap(6).Grow(1).Shrink(1).Children(func() {
						titleText(c, displayName(w), fsPage).MaxLines(1)
						var branch func()
						// 加进来的 worktree 标题已经是分支
						if w.Branch != "" && w.Branch != w.Shown {
							branch = metaMono(c, iconBranch, w.Branch, "")
						}
						metaRow(c, metaMono(c, iconFolder, shortPath(w.Path), w.Path), branch)
					})
				})
				if w.Missing {
					a.missingActions(c, w)
					return
				}
				ui.Grid(c).Columns(launchColumns(len(agents)+1, width, launchTileMin())).Gap(launchGap).Children(func() {
					for _, ag := range agents {
						a.launchTile(c, w, ag, agentVendor(ag))
					}
					a.launchTile(c, w, "", a.keyText("newTerminal"))
				})
				ui.Row(c).Gap(12).Padding(2, 4).Justify(ui.Center).Wrap().Children(func() {
					shortcutHint(c, a.keyText("newTerminal"), L(c).EmptyNewTerminal())
					shortcutHint(c, a.keyText("newWorkspace"), L(c).EmptyNewWorkspace())
					shortcutHint(c, "⌘1…9", L(c).EmptySwitchTerminal())
					shortcutHint(c, a.keyText("quickOpen"), L(c).EmptyOpenFile())
				})
			})
		})
	})
}

// 空工作区页的外边距和启动格间距：算宽度和列数也用它们
const (
	emptyPad  = 24
	launchGap = 8
)

// launchTileMin 是启动格的最窄宽度：再窄图标两边贴边、"Anthropic" 放不下。跟着字号和图标大小放大。
func launchTileMin() float32 { return max(sz(96), tileLarge()+32) }

// launchColumns 是 n 个启动格在 width 里排几列（每格至少 minW）：一行放得下就一行，否则分成几行、每行个数尽量平均。
func launchColumns(n int, width, minW float32) int {
	fit := max(int((width+launchGap)/(minW+launchGap)), 1)
	if n <= fit {
		return n
	}
	rows := (n + fit - 1) / fit
	return (n + rows - 1) / rows
}

// launchTile 是空工作区里启动 agent 的一格：agent 的色块图标和名字。
func (a *app) launchTile(c *ui.Context, w *workspace, agent, note string) {
	t := c.Theme()
	name := agentTitle(agent)
	if agent == "" {
		name = L(c).TermName()
	}
	b := ui.Column(c.Key("launch-"+agent)).Padding(16, 8, 16, 8).Gap(8).Radius(rCard).AlignItems(ui.Center).Label(L(c).TermStart(name)).Role(ui.RoleButton)
	bg := colors(t).wash
	if b.Hovered() {
		bg = t.SurfaceHover
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	pressable(b)
	b.Children(func() {
		agentTile(c, agent, tileLarge()).Tooltip("")
		ui.Column(c).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(fsBody).FontWeight(fwMedium)
			ui.Text(c, note).FontSize(fsSmall).TextColor(t.TextMuted)
		})
	})
	if b.Clicked() {
		a.startTerminal(w.ID, agent)
	}
}

func shortcutHint(c *ui.Context, keys, label string) {
	t := c.Theme()
	ui.Row(c).Gap(6).Children(func() {
		ui.Text(c, keys).FontSize(fsSmall).TextColor(t.Text).Padding(2, 6).Radius(rSmall).Background(t.Surface)
		ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted)
	})
}

// tab 是第 i 格标签栏里的一个终端；active 是这格选中的，focused 表示这格是焦点格。
func (a *app) tab(c *ui.Context, w *workspace, term terminalInfo, i int, active, focused bool) {
	t := c.Theme()
	// 右边多留 4：左边的状态图标自带留白，两边都是 8 时文字一侧显得挤
	row := ui.Row(c.Key(term.ID)).Height(sz(28)).Padding(0, 12, 0, 8).Gap(6).Radius(rControl).Shrink(1).MaxWidth(tabMaxW)
	hovered := row.Hovered()
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case hovered:
		bg = colors(t).wash
	}
	a.revealTab(row, i, term.ID, active)
	if active && focused && a.splitShown() {
		tabMark(c, row)
	}
	rowBg := row.AnimateColor("bg", bg, hover)
	row.Background(rowBg)
	status := ""
	if term.AgentState != nil && term.Running || term.Lost {
		status = a.sessionStatus(term)
	}
	name, detail := tabLabel(L(c), term)
	if title, ok := a.workerTitle[term.ID]; ok && term.Running && term.Name == "" {
		detail = truncate(title, 22)
	}
	renaming := a.renaming == term.ID
	if renaming && !active {
		// 用快捷键切走了：按现在输入的保存
		a.finishRename(L(c), term)
		renaming = false
	}
	if renaming {
		row.Border(focusRing, t.Focus)
	}
	waiting := status == agent.StatusWaiting
	showClose := hovered
	showName := term.Agent == "" || detail == "" && !waiting
	showDetail := !waiting && detail != ""
	if !renaming {
		// 自己按字宽截断再交给排版：排版按分到的宽度截，末尾会空出将近一个汉字，标签不收紧
		used, items := float32(8+12), 0
		add := func(w float32) { used += w; items++ }
		if status != "" || term.Agent == "" {
			add(smallIcon())
		}
		if term.Agent != "" {
			add(smallIcon())
		}
		if waiting {
			tw, _ := c.MeasureText(0, ui.Span{Text: L(c).TermPending(), Size: fsSmall})
			add(tw)
		}
		var nw, dw float32
		if showName {
			nw, _ = c.MeasureText(0, ui.Span{Text: name, Size: fsSmall})
			add(nw)
		}
		if showDetail {
			dw, _ = c.MeasureText(0, ui.Span{Text: detail, Size: fsSmall})
			add(dw)
		}
		natural := used + float32(max(items-1, 0))*6
		if natural > tabMaxW {
			avail := tabMaxW - (natural - nw - dw) - 1
			if showName {
				name = fitText(c, name, fsSmall, avail)
				nw, _ = c.MeasureText(0, ui.Span{Text: name, Size: fsSmall})
				avail -= nw
			}
			if showDetail {
				detail = fitText(c, detail, fsSmall, avail)
				showDetail = detail != ""
			}
		}
	}
	row.Children(func() {
		if status != "" {
			a.statusIcon(c, term.ID, status, smallIcon())
		} else if term.Agent == "" {
			ui.Icon(c, iconTerminal).FontSize(smallIcon()).TextColor(t.TextMuted).Shrink(0)
		}
		// agent 终端用图标代替名字（悬停提示是名字）；还没有标题时照旧写名字，标签不至于只剩图标。
		// 图标和左边的状态格一样大
		if term.Agent != "" {
			agentTile(c, term.Agent, smallIcon())
		}
		if renaming {
			a.renameInput(c, term)
			return
		}
		if showName {
			tabText(c, ui.Text(c, name).FontSize(fsSmall).Shrink(1).SingleLine(), active)
		}
		// 待处理时只显示这个提示，标题让位，标签不至于挤成一串
		if waiting {
			// 字号、字重和别的标签标题一样，只用琥珀色区分
			label := ui.Text(c, L(c).TermPending()).FontSize(fsSmall).Shrink(0)
			tabText(c, label, active)
			label.TextColor(colors(t).honey)
		} else if showDetail {
			title := ui.Text(c, detail).FontSize(fsSmall).Shrink(1).SingleLine()
			if term.Agent != "" {
				tabText(c, title, active)
			} else {
				title.TextColor(t.TextMuted).Margin(0, 0, cjkLift, 0)
			}
		}
		// 关闭按钮只在悬停时出现（选中的也一样），平时省下这一格多放标题，切换标签时宽度也不变
		if !showClose {
			return
		}
		// x 浮在右端盖住标题末尾，不占排版位置：悬停时标签不变宽，后面的标签不跟着动。
		// 底下先垫标签栏的底色再叠标签的底色（悬停的底是半透明的），左边一段渐隐进标题
		band := colors(t).band
		ui.Row(c.Key("close-over")).Absolute().Top(0).Bottom(0).Right(2).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(12).Height(miniBox()).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
				fadeIn(p, r, band, rowBg)
			})
			ui.Box(c).Size(miniBox(), miniBox()).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
				p.Fill(r, band, 0)
				p.Fill(r, rowBg, 0)
			}).Children(func() { a.tabClose(c, term) })
		})
	})
	// 改名时不能拖：在输入框里拖着选文字会变成拖标签
	if !renaming {
		a.reorderableTab(c, row, w, term.ID, i)
	}
	if row.Clicked() {
		a.selectKey(w, term.ID, a.splitShown())
	}
	// 双击改名，和 iTerm、Ghostty 一样
	if row.DoubleClicked() && !renaming {
		a.startRename(L(c), term)
	}
	// 中键关闭，和浏览器、Orca 一样
	if row.MiddleClicked() {
		a.askClose(L(c), term)
	}
	row.Tooltip(tabTooltip(L(c), term))
	contextMenu(c, row, func(m *popMenu) { a.tabMenu(L(c), m, w, term) })
}

// tabClose 是终端标签上的关闭按钮。
func (a *app) tabClose(c *ui.Context, term terminalInfo) {
	t := c.Theme()
	closeBtn := ui.Box(c.Key("close")).Size(miniBox(), miniBox()).Radius(rSmall).Center().Shrink(0).Label(L(c).TermClose()).Tooltip(a.withKey(L(c).TermClose(), "closeTab"))
	closeBg := ui.Transparent
	if closeBtn.Hovered() {
		closeBg = t.SurfacePressed
	}
	closeBtn.Background(closeBtn.AnimateColor("bg", closeBg, hover))
	closeBtn.Children(func() { ui.Icon(c, iconClose).FontSize(fsSmall).TextColor(t.TextMuted) })
	if closeBtn.Clicked() {
		a.askClose(L(c), term)
	}
}

// tabTooltip 是终端标签的悬停提示：完整的名字和标题，标签上只有图标或标题被截断时看得到全文。
func tabTooltip(m locales.Messages, t terminalInfo) string {
	name := agentTitle(t.Agent)
	title := bareTitle(t)
	if t.Name != "" {
		title = t.Name
	}
	switch {
	case name == "" && title == "":
		return m.TermName()
	case name == "":
		return title
	case title == "" || strings.EqualFold(title, name) || title == "Claude Code":
		return name
	}
	return name + "\n" + title
}

// tabMenu 是终端标签的右键菜单：关闭这个、其他的、右边的终端（照 Orca）；文件标签不受影响。
func (a *app) tabMenu(l locales.Messages, m *popMenu, w *workspace, term terminalInfo) {
	a.sessionMenu(l, m, term, nil)
	m.Separator()
	a.columnMenu(l, m, w, term.ID)
	m.Separator()
	if m.Item(l.CommonClose()).Icon(iconClose).Keys(a.keyText("closeTab")).Chosen() {
		a.askClose(l, term)
	}
	i := slices.IndexFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == term.ID })
	if i < 0 {
		return
	}
	others := slices.Concat(w.Terminals[:i], w.Terminals[i+1:])
	right := slices.Clone(w.Terminals[i+1:])
	if m.Item(l.TabCloseOthers()).Disabled(len(others) == 0).Chosen() {
		a.askCloseAll(l, others)
	}
	if m.Item(l.TabCloseRight()).Disabled(len(right) == 0).Chosen() {
		a.askCloseAll(l, right)
	}
}

// sessionMenu 是终端标签和侧栏会话行右键菜单共有的几项：改名、复制引用和各个 ID、聊天会话的文件夹。
// reveal 在开始改名前调用（侧栏里要先切到这个会话），为 nil 时不调。
func (a *app) sessionMenu(l locales.Messages, m *popMenu, term terminalInfo, reveal func()) {
	if m.Item(l.TabRename()).Icon(iconPencil).Chosen() {
		if reveal != nil {
			reveal()
		}
		a.startRename(l, term)
	}
	if term.Name != "" && m.Item(l.TabResetName()).Icon(iconRollback).Chosen() {
		a.renameTerminal(term.ID, "")
	}
	m.Separator()
	task := a.taskByWorker(term.ID)
	if m.Item(l.TabCopyRef()).Icon(iconCopy).Chosen() {
		writeClipboard(tabRef(l, term, task))
	}
	if m.Item(l.TabCopyTerminalId()).Icon(iconCopy).Chosen() {
		writeClipboard(term.ID)
	}
	if task != nil && m.Item(l.TabCopyTaskId()).Icon(iconCopy).Chosen() {
		writeClipboard(task.ID)
	}
	if sid := sessionID(term); sid != "" && m.Item(l.TermCopySessionId()).Icon(iconCopy).Chosen() {
		writeClipboard(sid)
	}
	a.chatFolderMenu(l, m, term)
}

// tabRef 是标签右键「复制引用」的一行字：贴给 agent 时带上每个 ID 是什么、用哪条命令看，不用它再去查。
func tabRef(m locales.Messages, t terminalInfo, task *taskInfo) string {
	// 不用 fullLabel：它对退出了的终端把标题换成「已退出」，引用里要的正是跑完的会话叫什么
	name, title := agentTitle(t.Agent), termTitle(t)
	switch {
	case name == "" && title != "":
		name = title
	case name == "":
		name = m.TermName()
	case title != "" && !strings.EqualFold(title, name) && title != "Claude Code":
		name = m.TabRefNamed(name, title)
	}
	parts := []string{m.TabRefTerminal(t.ID)}
	if task != nil {
		parts = append(parts, m.TabRefTask(task.ID))
	}
	if sid := sessionID(t); sid != "" {
		parts = append(parts, m.TabRefSession(sid))
	}
	return m.TabRefLine(name, strings.Join(parts, m.TabRefSeparator()))
}

func sessionID(t terminalInfo) string {
	if t.AgentState == nil {
		return ""
	}
	return t.AgentState.SessionID
}

// revealTab 在标签刚变成当前的那一帧把它滚进可见范围：标签多了要左右滚动时，切过去的那个不会藏在外面。
// 只在切换时滚一次，用户自己滚开了不拉回来。
func (a *app) revealTab(row ui.Element, i int, id string, active bool) {
	if active && i < maxColumns && a.shownTab[i] != id {
		a.shownTab[i] = id
		row.ScrollIntoView()
	}
}

// tabText 是标签上的主文字：没选中时用次要色。中文字形在行框里偏下，提半个像素才看着居中。
// 选中不加粗：长标题缩到标签最大宽度时，加粗的字截断的位置不同，切换时文字会跳。
// tabMaxW 是终端标签的最大宽度，不随字号缩放。
const tabMaxW = 260

// fitText 把 s 截到 size 字号下不超过 maxW 宽，截了的末尾带省略号；连省略号都放不下时是空串。
func fitText(c *ui.Context, s string, size, maxW float32) string {
	width := func(s string) float32 {
		w, _ := c.MeasureText(0, ui.Span{Text: s, Size: size})
		return w
	}
	if width(s) <= maxW {
		return s
	}
	r := []rune(strings.TrimSuffix(s, "…"))
	lo, hi := 0, len(r) // 二分找能放下的最长前缀
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if width(strings.TrimRight(string(r[:mid]), " ")+"…") <= maxW {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 && width("…") > maxW {
		return ""
	}
	return strings.TrimRight(string(r[:lo]), " ") + "…"
}

func tabText(c *ui.Context, e ui.Element, active bool) {
	e.Margin(0, 0, cjkLift, 0)
	if !active {
		e.TextColor(c.Theme().TextMuted)
	}
}

// tabLabel 返回标签的名字和补充说明（会话名，没有时是终端标题），分开显示而不是用分隔符拼接。
// 标签宽度跟着文字走，所以截短；侧栏行宽是定的，用不截短的 fullLabel，放不下才由排版截。
func tabLabel(m locales.Messages, t terminalInfo) (string, string) {
	name, detail := fullLabel(m, t)
	return truncate(name, 24), truncate(detail, 22)
}

// fullLabel 同 tabLabel，不截短。
func fullLabel(m locales.Messages, t terminalInfo) (string, string) {
	name, title := agentTitle(t.Agent), termTitle(t)
	detail := ""
	switch {
	case name == "" && title != "":
		name = title
	case name == "":
		name = m.TermName()
	case title != "" && !strings.EqualFold(title, name) && title != "Claude Code":
		detail = title
	}
	// 还没恢复的照旧写原来的名字，「未恢复」是状态图标
	if !t.Running && !t.Lost {
		detail = m.TermExited()
	}
	return name, detail
}

// termTitle 是终端的标题：用户改的名字优先，其次会话标题，最后是终端标题。
func termTitle(t terminalInfo) string {
	if t.Name != "" {
		return t.Name
	}
	if t.AgentState != nil && t.AgentState.Title != "" {
		return t.AgentState.Title
	}
	return bareTitle(t)
}

// bareTitle 是去掉 agent 状态前缀（✳、转圈符）的终端标题。
func bareTitle(t terminalInfo) string {
	return agent.BareTitle(t.Title)
}

// displayName 是工作区的显示名：主工作区就是项目本身。
func displayName(w *workspace) string {
	if w.Kind == "main" {
		return w.Project
	}
	if w.Shown != "" {
		return w.Project + "/" + w.Shown
	}
	return w.Label
}

func agentTitle(agent string) string {
	switch agent {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "grok":
		return "Grok"
	case "cursor":
		return "Cursor"
	}
	return agent
}

// agentVendor 是空工作区启动格里 agent 名字下面的一行小字。
func agentVendor(agent string) string {
	switch agent {
	case "claude":
		return "Anthropic"
	case "codex":
		return "OpenAI"
	case "grok":
		return "xAI"
	case "cursor":
		return "Cursor"
	}
	return ""
}

// --- 控件

// pressable 给按钮按下时 0.98 的缩放反馈。
func pressable(e ui.Element) ui.Element {
	scale := float32(1)
	if e.Pressed() {
		scale = 0.98
	}
	s := e.AnimateWith("press", scale, 100*time.Millisecond, ui.EaseOut)
	return e.Scale(s, s)
}

// iconButton 是只有图标的无边框按钮，大小跟着设置里的图标大小（buttonBox、iconSize）。
func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return iconButtonSized(c, icon, label, buttonBox(), iconSize)
}

func iconButtonSized(c *ui.Context, icon *ui.SVG, label string, box, glyph float32) ui.Element {
	return quietIconButton(c, icon, label, box, glyph).Tooltip(label)
}

// toggleIconButton 是带开关状态的小图标按钮（图片的棋盘格、改动页的按目录分组），开着时垫强调色淡底；
// 返回这一帧是否被点了。
func toggleIconButton(c *ui.Context, key string, icon *ui.SVG, label string, on bool) bool {
	return toggleIconButtonSized(c, key, icon, label, on, compactBox(), fsBody, rRow)
}

// toggleIconButtonSized 同 toggleIconButton，方框、图标和圆角自定（顶栏里和 iconButton 一样大）。
func toggleIconButtonSized(c *ui.Context, key string, icon *ui.SVG, label string, on bool, box, glyph, radius float32) bool {
	t := c.Theme()
	b := ui.Box(c.Key(key)).Size(box, box).Radius(radius).Center().Shrink(0).
		Role(ui.RoleButton).Label(label).Tooltip(label).Checked(on)
	bg, fg := ui.Transparent, t.TextMuted
	switch {
	case on:
		bg, fg = colors(t).tint, t.Accent
	case b.Hovered():
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() { ui.Icon(c, icon).FontSize(glyph).TextColor(fg) })
	return b.Clicked()
}

// quietIconButton 是不带悬停提示的图标按钮，用在一眼认得出、提示反而挡视线的地方（终端右上角的会话按钮）。
func quietIconButton(c *ui.Context, icon *ui.SVG, label string, box, glyph float32) ui.Element {
	t := c.Theme()
	b := ui.Box(c).Size(box, box).Radius(box / 4).Center().Shrink(0).Label(label)
	bg, fg := ui.Transparent, t.TextMuted
	if b.Hovered() {
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	pressable(b)
	b.Children(func() { ui.Icon(c, icon).FontSize(glyph).TextColor(fg) })
	return b
}

// --- 对话框

func (a *app) confirmDialog(c *ui.Context) {
	t := c.Theme()
	// 点遮罩不关，免得误点丢掉要确认的事；取消按钮和 Esc 照常关
	if ui.Modal(c, &a.confirming, func() {
		ui.Column(c).Width(340).Gap(8).Children(func() {
			titleText(c, a.confirmTitle, fsTitle)
			if a.confirmBody != "" {
				ui.Text(c, a.confirmBody).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				// 键盘：打开时选中取消（关闭、删除这类确认误按回车也不出事），←→ 和 Tab 换按钮，回车、空格按下选中的
				var cancel ui.Element
				if a.confirmDo != nil {
					cancel = confirmButton(c, pressable(textButton(c, L(c).CommonCancel()))).AutoFocus()
					if cancel.Clicked() {
						a.confirming = false
					}
				}
				ok := confirmButton(c, pressable(accentButton(c, a.confirmVerb)))
				if a.confirmDo == nil {
					ok.AutoFocus()
				}
				if ok.Clicked() {
					a.confirming = false
					if a.confirmDo != nil {
						go a.confirmDo()
					}
				}
				if a.confirmDo != nil {
					if cancel.Shortcut(0, ui.KeyRight) {
						ok.Focus()
					}
					if ok.Shortcut(0, ui.KeyLeft) {
						cancel.Focus()
					}
				}
			})
		})
	}).ClickedOutside() {
		a.confirming = true
	}
}

// confirmButton 给确认框的按钮画选中框：打开时焦点不是从键盘来的，mygo 自己的聚焦框不画，这里一直画。
func confirmButton(c *ui.Context, b ui.Element) ui.Element {
	b.FocusRing(false)
	if b.Focused() {
		// 和按钮隔一道缝，强调色的按钮上也看得出
		b.DrawOver(func(p *ui.Painter, r ui.Rect) {
			p.FocusRing(r, [4]float32{rControl, rControl, rControl, rControl})
		})
	}
	return b
}

func (a *app) newWorkspaceDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	ui.Modal(c, &a.creating, func() {
		// 英文的 agent 选择条（"Workspace Only" 加四家 agent）在 380 里放不下
		ui.Column(c).Width(440).Gap(12).Children(func() {
			titleText(c, m.NewWorkspaceTitle(), fsTitle)
			var names []string
			kind := ""
			for _, p := range a.projects {
				names = append(names, p.Name)
				if p.Name == a.newProject {
					kind = p.Kind
				}
			}
			// 不是仓库的项目目录下有仓库（Works 这种）：勾上的仓库各建一个 worktree
			var repos, picked []string
			if kind != "git" {
				repos = a.projectRepos(a.newProject)
				if a.newReposFor != a.newProject {
					a.newRepos, a.newReposFor = map[string]bool{}, a.newProject
				}
				for _, r := range repos {
					if a.newRepos[r] {
						picked = append(picked, r)
					}
				}
			}
			ui.Column(c).Gap(6).Children(func() {
				ui.Select(c, &a.newProject, names)
				hint := m.NewWorkspaceFolderHint()
				switch {
				case kind == "git":
					hint = m.NewWorkspaceGitHint()
				case len(picked) > 0:
					hint = m.NewWorkspaceReposHint(len(picked))
				case len(repos) > 0:
					hint = m.NewWorkspacePickReposHint()
				}
				ui.Text(c, hint).FontSize(fsSmall).TextColor(t.TextMuted)
				if len(repos) > 0 {
					ui.Row(c).Wrap().Gap(12).Padding(4, 0, 0, 0).Children(func() {
						for _, r := range repos {
							on := a.newRepos[r]
							if ui.Checkbox(c.Key("repo:"+r), &on, r).Changed() {
								a.newRepos[r] = on
							}
						}
					})
				}
			})
			submitted := fieldInput(c, &a.newName, m.NewWorkspaceNamePlaceholder()).AutoFocus().Submitted()
			choices := a.newAgentChoices()
			if a.newAgent >= len(choices) {
				a.newAgent = 0
			}
			labels := agentTitles(choices)
			labels[0] = m.NewWorkspaceOnly()
			segmented(c, &a.newAgent, m.NewWorkspaceStartWith(), labels...)
			if a.newAgent != 0 {
				if fieldInput(c.Key("prompt"), &a.newPrompt, m.NewWorkspacePromptPlaceholder()).Submitted() {
					submitted = true
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					a.creating = false
				}
				if pressable(accentButton(c, m.NewWorkspaceCreate())).Clicked() {
					submitted = true
				}
			})
			if submitted && strings.TrimSpace(a.newName) != "" {
				a.creating = false
				params := map[string]any{"project": a.newProject, "name": strings.TrimSpace(a.newName), "agent": choices[a.newAgent], "prompt": a.newPrompt}
				if len(picked) > 0 {
					params["repos"] = picked
				}
				go a.createWorkspace(m, params)
			}
		})
	})
}
