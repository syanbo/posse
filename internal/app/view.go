package app

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/media"
)

func (a *app) build(c *ui.Context) {
	a.runPending()
	c.SetTheme(posseTheme(c.Theme().Dark))
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
	a.setHistoryVisible(a.selected == historyBoardID)
	a.setMobileVisible(a.selected == mobileBoardID)
	if a.loaded && a.selected != a.state.Selected {
		a.state.Selected = a.selected
		a.saveState()
	}
	w := a.workspace(a.selected)
	if w != nil {
		a.lastWS = w.ID
	}
	cur := a.currentTerminal(w)
	if cur != nil {
		a.ensureView(cur.ID)
	} else {
		a.ensureView("")
	}
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
				case a.selected == mobileBoardID:
					a.mobileBoard(c)
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
		titleText(c, "Posse", fsBody).Width(appNameWidth()).Shrink(0)
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
	a.quickDialog(c)
	a.defDialog(c)
	a.pushDialog(c)
	a.promptDialog(c)
	a.unsavedDialog(c)
	a.rollbackDialog(c)
	a.confirmDialog(c)
	a.sysMenuView(c)
}

func (a *app) shortcuts(c *ui.Context, w *workspace, cur *terminalInfo) {
	if w != nil && c.Shortcut(ui.Cmd, ui.KeyT) {
		a.startTerminal(w.ID, "")
	}
	if w != nil {
		if c.Shortcut(ui.Cmd, ui.KeyP) {
			a.openQuick(w)
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyF) {
			a.openFilesPanel(w, sideSearch).search.focus = true
		}
		// 窗口的快捷键先于焦点拿到按键。⌘L 照旧开关文件栏（编辑器的"选中整行"让出），
		// ⇧⌘G 在编辑器有焦点时让给它的"查找上一个"
		editing := a.filesActive(w) && a.filesFor(w).cur != nil && c.FocusWithin(a.filesFor(w).cur.edRef)
		if c.Shortcut(ui.Cmd, ui.KeyL) {
			if a.filesPanelOpen() {
				a.closeFilesPanel()
			} else {
				a.openFilesPanel(w, a.filesFor(w).side)
			}
		}
		if !editing && c.Shortcut(ui.Cmd|ui.Shift, ui.KeyG) {
			a.toggleFilesPanel(w, sideChanges)
		}
		// ⌘K 在终端里是清屏（termKey），焦点在文件、文件栏上时才是提交；不注册快捷键，终端才收得到这个键
		commitKey := a.filesShown(w) && a.filesFor(w).git.repo != "" && (a.filesActive(w) || c.FocusWithin(a.filesPanelRef))
		if commitKey && c.Shortcut(ui.Cmd, ui.KeyK) {
			a.openFilesPanel(w, sideChanges).git.focusMsg = true
		}
		// ⌥⌘Z：焦点在改动页时批量回滚（编辑器、diff 里是回滚当前这一处，见 gitShortcuts）
		if a.filesPanelOpen() && a.filesFor(w).side == sideChanges && c.FocusWithin(a.filesPanelRef) && c.Shortcut(ui.Cmd|ui.Alt, ui.KeyZ) {
			a.openRollback(a.filesFor(w))
		}
		if w.Branch != "" && c.Shortcut(ui.Cmd|ui.Shift, ui.KeyK) {
			a.openPush(w)
		}
		if w.Branch != "" && c.Shortcut(ui.Cmd|ui.Alt, ui.KeyN) {
			a.askNewBranch(w, a.filesFor(w), "")
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyE) {
			a.toggleFilesPanel(w, sideFiles)
		}
	}
	if a.filesActive(w) {
		v := a.filesFor(w)
		a.gitShortcuts(c, w, v)
		if c.Shortcut(ui.Cmd, ui.KeyS) && v.cur != nil {
			a.saveDoc(v.cur, false)
		}
	}
	// 打包的 app 里 ⌘W 先被菜单拿走（installMenu），菜单只记下来，到这里和快捷键一样在有对话框时不动
	if a.menuCloseTab {
		a.menuCloseTab = false
		if !c.ModalOpen() {
			a.closeTab()
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyW) {
		a.closeTab()
	}
	// ⌃- 回到跳走之前的位置、⌃⇧- 再往前（和 VS Code 一样；⌘[ 是编辑器的减少缩进）
	if w != nil && c.Shortcut(ui.Ctrl, ui.KeyMinus) {
		a.jumpBack(w)
	}
	if w != nil && c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyMinus) {
		a.jumpForward(w)
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		a.openNewWorkspace()
	}
	if c.Shortcut(ui.Cmd, ui.KeyB) {
		a.toggleSidebar()
	}
	if w != nil && w.Branch != "" && c.Shortcut(ui.Cmd, ui.Key9) {
		a.toggleLogPanel(w)
	}
	// 图片标签在前台时 ⌘= ⌘- ⌘0 缩放图片，其余文件标签调编辑器的字号，终端调终端的
	img := a.frontImage(w)
	size, def, setSize := a.fontSize, float32(defaultFontSize), a.setFontSize
	if a.filesActive(w) {
		size, def, setSize = a.editorSize, defaultEditorFontSize, a.setEditorFontSize
	}
	if c.Shortcut(ui.Cmd, ui.KeyEqual) {
		if img != nil {
			img.zoomStep(true)
		} else {
			setSize(size + 1)
		}
	}
	if c.Shortcut(ui.Cmd, ui.KeyMinus) {
		if img != nil {
			img.zoomStep(false)
		} else {
			setSize(size - 1)
		}
	}
	if c.Shortcut(ui.Cmd, ui.Key0) {
		if img != nil {
			img.setZoom(0, 0, 0)
		} else {
			setSize(def)
		}
	}
	if w != nil {
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyBracketRight) || c.Shortcut(ui.Ctrl, ui.KeyTab) {
			a.stepTab(w, 1)
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyBracketLeft) || c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyTab) {
			a.stepTab(w, -1)
		}
		keys := []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9}
		for i, k := range keys {
			if i < len(w.Terminals) && c.Shortcut(ui.Cmd, k) {
				a.activeTerm[w.ID] = w.Terminals[i].ID
			}
		}
	}
}

// isBoard 报告选中的是不是任务、历史、手机、设置这几个不属于工作区的页面。
func isBoard(id string) bool {
	return id == taskBoardID || id == historyBoardID || id == mobileBoardID || id == settingsBoardID
}

// closeBoard 关掉当前的任务、历史、手机或设置页，回到上次看的工作区，放掉页面读进来的内容；
// 再打开时重新读。
func (a *app) closeBoard() {
	switch a.selected {
	case historyBoardID:
		a.releaseHistory()
	case taskBoardID:
		a.detail, a.taskSel, a.taskHome = nil, "", false
	case mobileBoardID:
		a.mobile.devices = nil
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
	if iconButton(c.Key("close-board"), iconClose, "关闭 ⌘W").Clicked() {
		a.closeBoard()
	}
}

// 侧栏的宽度，拖右边缘调整；默认宽度和上下限跟着界面字号缩放，拖过的照拖的宽度。
func sidebarDefault() float32 { return sz(248) }
func sidebarMin() float32     { return sz(200) }
func sidebarMax() float32     { return sz(420) }

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
	label := "收起侧栏 ⌘B"
	if a.state.SidebarHidden {
		label = "展开侧栏 ⌘B"
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

func appNameWidth() float32 { return sz(46) }
func titleLead() float32    { return appNameWidth() + appNameGap + toggleBox }

// barInset 是侧栏不占位置时主区域顶栏左边要让出的宽度：红绿灯和浮在旁边的应用名、侧栏开关。
func (a *app) barInset(c *ui.Context) float32 {
	if a.sidebarSlot() {
		return 0
	}
	return c.TitleBar().Left + titleGap + titleLead()
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
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 12, 0, bar.Left+titleGap).DragWindow().AlignItems(ui.Center).
		BorderWidth(0, 0, hairline, 0).BorderColor(colors(t).line).Children(func() {
		// 应用名浮在这里（见 build），留出它的位置
		ui.Box(c).Width(appNameWidth()).Shrink(0)
		ui.Spacer(c)
		a.sidebarButton(c)
	})
	if !a.loaded {
		msg, color := "正在连接常驻进程…", t.TextMuted
		if a.err != "" {
			msg, color = "连不上常驻进程："+a.err, t.Danger
		}
		ui.Text(c, msg).FontSize(fsSmall).TextColor(color).Padding(6, 16)
	}
	a.navOrder, a.navCur = a.navOrder[:0], -1
	nav := ui.Scroll(c).Grow(1).Focusable().Label("工作区")
	nav.Children(func() {
		ui.Column(c).Padding(4, 12, 12, 12).Gap(2).Children(func() {
			if len(a.projects) > 0 {
				a.taskSidebarItem(c)
			}
			a.historySidebarItem(c)
			a.mobileSidebarItem(c)
			// 任务、历史、手机是全局页面，和下面的项目用一道细线隔开
			hline(c).Margin(6, 12, 6, 12)
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
				toggle = func() { disclosure(c, open, fmt.Sprintf("展开 %d 个会话", len(sessions))) }
			}
			// 单击项目行就展开；已经选中并展开时再单击收起
			wasSelected := a.selected == head.ID
			if row := a.sidebarItem(c, head, p.Name, false, !*open, toggle); row.Clicked() && len(sessions) > 0 {
				*open = !*open || !wasSelected
			}
			collapsible(c, shown, func() { a.sessionRows(c, head, sessions) })
			return
		}
		a.projectHeader(c, p, ws, open)
		collapsible(c, shown, func() {
			ui.Column(c).Gap(2).Padding(0, 0, 0, navIndent).Children(func() {
				for _, w := range ws {
					label := w.Name
					if w.Kind == "main" {
						label = "主工作区"
					}
					a.sidebarItem(c, w, label, true, false, nil)
					a.sessionRows(c, w, agentSessions(w.Terminals))
				}
			})
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

// projectHeader 是有多个工作区的项目的标题行：只管展开收起，不选中工作区；收起时汇总所有工作区的终端。
func (a *app) projectHeader(c *ui.Context, p project, ws []workspace, open *bool) {
	t := c.Theme()
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
		disclosure(c, open, fmt.Sprintf("展开 %d 个工作区", len(ws)))
	}
	icon := func() { ui.Icon(c, iconFolder).FontSize(iconSize).TextColor(t.Accent).Shrink(0) }
	row := a.navRowAt(c, "head", false, icon, p.Name, shortPath(ws[0].Path), "", trailing)
	if row.Clicked() {
		*open = !*open
	}
	contextMenu(c, row, func(m *popMenu) { a.workspaceMenu(m, ws[0]) })
}

// disclosure 是展开箭头，展开时转向下；label 是收起时的说明（"展开…"），展开后说明换成"收起…"。
func disclosure(c *ui.Context, open *bool, label string) {
	if *open {
		label = "收起" + strings.TrimPrefix(label, "展开")
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
}

// summaryTrailing 是行尾的运行终端数和最要紧的状态。
func summaryTrailing(c *ui.Context, running int, status string) {
	if status == agent.StatusWaiting {
		pendingLabel(c)
	} else if running > 0 {
		ui.Text(c, fmt.Sprint(running)).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted).Tooltip(fmt.Sprintf("%d 个终端在运行", running))
	}
	if status != "" {
		statusGlyph(c, status, smallIcon())
	}
}

// agentSessions 是侧栏列出的会话：运行中的 agent 终端，普通 shell 只在标签栏。
func agentSessions(ts []terminalInfo) []terminalInfo {
	var out []terminalInfo
	for _, t := range ts {
		if t.Agent != "" && t.Running {
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
	if cur := a.currentTerminal(w); cur != nil && cur.Agent != "" && cur.Running {
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
	selected := a.selected == w.ID && (collapsed || a.selectedSession(a.workspace(w.ID)) == "")
	if selected || collapsed || len(agentSessions(w.Terminals)) == 0 {
		a.navOrder = append(a.navOrder, navTarget{ws: w.ID})
		if selected {
			a.navCur = len(a.navOrder) - 1
		}
	}
	t := c.Theme()
	icon := func() { ui.Icon(c, a.wsIcon(&w)).FontSize(iconSize).TextColor(t.Accent).Shrink(0) }
	item := a.navRowAt(c, w.ID, selected, icon, label, sub, branch, trailing)
	if item.Clicked() {
		a.selected = w.ID
	}
	if a.revealSel && selected {
		item.ScrollIntoView()
		a.revealSel = false
	}
	contextMenu(c, item, func(m *popMenu) { a.workspaceMenu(m, w) })
	return item
}

// sessionRows 把工作区里的 agent 会话缩进列在它下面，单击切到对应的终端标签。
func (a *app) sessionRows(c *ui.Context, w workspace, sessions []terminalInfo) {
	if len(sessions) == 0 {
		return
	}
	cur := ""
	if a.selected == w.ID {
		cur = a.selectedSession(a.workspace(w.ID))
	}
	// 任务的 worker 排在派发它的会话下面、多缩进一层；派发方不在这个工作区（别的工作区、用户派发）的照常排
	listed := map[string]bool{}
	for _, term := range sessions {
		listed[term.ID] = true
	}
	children := map[string][]terminalInfo{}
	var top []terminalInfo
	for _, term := range sessions {
		if coord := a.coordOf[term.ID]; coord != "" && coord != term.ID && listed[coord] {
			children[coord] = append(children[coord], term)
		} else {
			top = append(top, term)
		}
	}
	// worker 最多嵌套 3 层，这里多留一层余量，防止数据成环时无限递归
	var below func(id string, depth int) []terminalInfo
	below = func(id string, depth int) []terminalInfo {
		var out []terminalInfo
		for _, k := range children[id] {
			out = append(out, k)
			if depth < 4 {
				out = append(out, below(k.ID, depth+1)...)
			}
		}
		return out
	}
	var rows func(list []terminalInfo, depth int)
	rows = func(list []terminalInfo, depth int) {
		ui.Column(c).Gap(2).Padding(0, 0, 0, navIndent).Children(func() {
			for _, term := range list {
				kids := children[term.ID]
				if len(kids) == 0 || depth >= 4 {
					a.navSession(c, w, term, cur == term.ID, nil, nil)
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
				all := below(term.ID, depth+1)
				hidden := slices.ContainsFunc(all, func(k terminalInfo) bool { return k.ID == cur })
				// 要定位的会话收在里面时先展开
				if !*open && hidden && a.revealSel {
					*open = true
				}
				// 收起时当前会话在里面，这一行算选中
				a.navSession(c, w, term, cur == term.ID || (!*open && hidden), all, open)
				if *open {
					rows(kids, depth+1)
				}
			}
		})
	}
	rows(top, 0)
}

// navSession 是侧栏里的一个 agent 会话。有 worker 时 workers 是它下面所有层的 worker，open 是它们是否展开：
// 和项目行一样，单击切到这个会话、收着的顺便展开，已经在看它时再单击收起；收起时行尾写 +N，
// 有 worker 等你处理时这一行也显示待处理。
func (a *app) navSession(c *ui.Context, w workspace, term terminalInfo, selected bool, workers []terminalInfo, open *bool) {
	a.navOrder = append(a.navOrder, navTarget{ws: w.ID, term: term.ID})
	if selected {
		a.navCur = len(a.navOrder) - 1
	}
	// 和标签一样：任务 worker 用任务标题
	name, detail := tabLabel(term)
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
	if status != "" || open != nil {
		trailing = func() {
			switch status {
			case agent.StatusWaiting:
				pendingLabel(c)
			case statusUnread:
				doneLabel(c)
			}
			if folded {
				ui.Text(c, fmt.Sprintf("+%d", len(workers))).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted).
					Tooltip(fmt.Sprintf("收起了 %d 个任务 worker", len(workers)))
			}
			if status != "" {
				a.statusIcon(c, statusID, status, smallIcon())
			}
		}
	}
	icon := func() { agentTile(c, term.Agent, iconSize).Tooltip(name) }
	row := a.navRowAt(c, term.ID, selected, icon, detail, "", "", trailing)
	a.reorderableTerm(c, row, w.ID, term.ID, false)
	if row.Clicked() {
		viewing := a.selected == w.ID && a.activeTerm[w.ID] == term.ID
		if open != nil {
			*open = !*open || !viewing
		}
		a.selected = w.ID
		a.activeTerm[w.ID] = term.ID
	}
	if a.revealSel && selected {
		row.ScrollIntoView()
		a.revealSel = false
	}
	contextMenu(c, row, func(m *popMenu) {
		if m.Item("关闭会话…").Chosen() {
			a.askClose(term)
		}
	})
}

// statusTasks 是界面自己的状态：会话在等它派出去的任务（阻塞在 posse task wait 上，或者停下来等 Posse 提醒），
// 不是在干活，也不用用户处理。
const statusTasks = "tasks"

// sessionStatus 是会话显示的状态：有进行中的任务、自己又没在干别的时是"等待任务"。
func (a *app) sessionStatus(term terminalInfo) string {
	status := ""
	if term.AgentState != nil {
		status = term.AgentState.Status
	}
	if a.runningFor[term.ID] > 0 && (term.WaitingTasks || status == agent.StatusIdle) {
		return statusTasks
	}
	return a.shownStatus(term.ID, status)
}

// workspaceMenu 是工作区行（和项目标题行）的右键菜单。
func (a *app) workspaceMenu(m *popMenu, w workspace) {
	for _, ag := range a.agentChoices() {
		if m.Item("启动 " + agentTitle(ag)).Chosen() {
			a.startTerminal(w.ID, ag)
		}
	}
	if m.Item("打开终端").Chosen() {
		a.startTerminal(w.ID, "")
	}
	m.Separator()
	if m.Item("在访达中打开").Icon(iconFolder).Chosen() {
		if err := mygo.Shell.OpenPath(w.Path); err != nil {
			a.err = "在访达中打开失败：" + err.Error()
		}
	}
	if hasBase(&w) {
		m.Separator()
		if m.Item("合并到 " + w.BaseRef + "…").Chosen() {
			a.askMergeWorkspace(w.ID)
		}
	}
	m.Separator()
	if m.Item("在此项目新建工作区…").Chosen() {
		a.openNewWorkspace()
		a.newProject = w.Project
	}
	// 项目行就是项目目录本身，删不了这个工作区，删的是整个项目
	if w.Kind == "main" {
		if m.Item("移除项目…").Chosen() {
			a.askRemoveProject(w)
		}
	} else if m.Item("删除工作区…").Chosen() {
		a.askRemoveWorkspace(w)
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

// termDrag 是拖动中的终端：标签栏的标签或侧栏的会话，只能落在同一个工作区里。
type termDrag struct{ ws, id string }

// reorderableTerm 让终端标签或侧栏的会话行可以拖动排序，两处共用一个顺序（ui.json 的 termOrder），
// 样子和拖项目一样。across 表示横排的标签，落点画成竖线。
func (a *app) reorderableTerm(c *ui.Context, e ui.Element, wsID, id string, across bool) {
	e.Drag(termDrag{wsID, id})
	if e.Dragging() {
		e.Opacity(0.4)
	}
	w := a.workspace(wsID)
	if w == nil {
		return
	}
	index := func(id string) int {
		return slices.IndexFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == id })
	}
	if d, over := ui.DragOver[termDrag](e); over && d.ws == wsID && d.id != id {
		dropMark(c, e, index(d.id) < index(id), across)
	}
	if d, ok := ui.Drop[termDrag](e); ok && d.ws == wsID && d.id != id {
		order := moveTerminal(w.Terminals, d.id, id)
		if a.state.TermOrder == nil {
			a.state.TermOrder = map[string][]string{}
		}
		a.state.TermOrder[wsID] = order
		sortTerminals(w.Terminals, order)
		a.saveState()
		c.AnimationFrame()
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
	selected := a.selected == id
	a.navOrder = append(a.navOrder, navTarget{ws: id})
	if selected {
		a.navCur = len(a.navOrder) - 1
	}
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

// navRowAt 画侧栏的一行，选中、点击和方向键顺序由调用方处理；icon 画行首的图标。
func (a *app) navRowAt(c *ui.Context, key string, selected bool, icon func(), label, sub, branch string, trailing func()) ui.Element {
	t := c.Theme()
	height, gap := sz(32), float32(2)
	switch {
	case sub != "" && branch != "":
		height, gap = sz(64), 2
	case sub != "" || branch != "":
		height = sz(46)
	}
	row := ui.Row(c.Key(key)).Height(height).Padding(0, 12, 0, 12).Gap(8).Radius(rControl).Shrink(0).AlignItems(ui.Center).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, selected)
	row.Children(func() {
		icon()
		ui.Column(c).Gap(gap).Grow(1).Shrink(1).Children(func() {
			// 选中只靠底色区分，文字不加粗
			ui.Text(c, label).FontSize(fsBody).SingleLine()
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
	if !a.staleApp || !a.loaded {
		return
	}
	t := c.Theme()
	ui.Column(c).Margin(0, 12, 8, 12).Padding(12, 12, 12, 12).Gap(8).Radius(rControl).Background(t.Surface).Children(func() {
		ui.Text(c, "Posse 已更新，重开窗口后生效。重开前，桌面、文稿里的项目可能读不了文件。").FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
		if pressable(accentButton(c, "重开窗口")).Clicked() {
			a.relaunch()
		}
	})
}

func (a *app) errorBar(c *ui.Context) {
	if a.err == "" || !a.loaded {
		return
	}
	t := c.Theme()
	ui.Row(c).Margin(0, 12, 8, 12).Padding(8, 6, 8, 12).Gap(6).Radius(rControl).Background(t.Surface).AlignItems(ui.Start).Children(func() {
		ui.Text(c, a.err).FontSize(fsSmall).TextColor(t.Danger).Grow(1).Shrink(1)
		if iconButtonSized(c, iconClose, "关闭提示", compactBox(), smallIcon()).Clicked() {
			a.err = ""
		}
	})
}

func (a *app) footer(c *ui.Context) {
	t := c.Theme()
	if !a.loaded {
		return
	}
	ui.Row(c).Height(footHeight).Padding(0, 8).Shrink(0).Background(footShade(t)).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line).Children(func() {
		menuButton(c, iconButton(c, iconPlus, "新建"), func(m *popMenu) {
			if m.Item("添加项目…").Chosen() {
				go a.addProject()
			}
			if m.Item("新建工作区…").Shortcut(ui.Cmd, ui.KeyN).Chosen() {
				a.openNewWorkspace()
			}
			if m.Item("派发任务…").Chosen() {
				a.openDispatch()
			}
			m.Separator()
			if m.Item("连接手机…").Chosen() {
				a.openMobile()
			}
		})
		ui.Spacer(c)
		if iconButton(c, iconSettings, "设置 ⌘,").Clicked() {
			a.openSettings()
		}
	})
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
		if t.AgentState != nil {
			if s := a.shownStatus(t.ID, t.AgentState.Status); statusRank(s) > statusRank(status) {
				status = s
			}
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
	case agent.StatusIdle:
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
	}
	if status == agent.StatusWorking && !c.Preferences().ReduceMotion {
		workingStar(c, size, t.Accent).Tooltip(statusText(status))
		return
	}
	if status == statusTasks {
		waitingPulse(c, size, t.Accent, c.Preferences().ReduceMotion).Tooltip(statusText(status))
		return
	}
	if status == statusUnread {
		icon, color = iconBell, t.Accent
	}
	ui.Icon(c, icon).FontSize(size).TextColor(color).Shrink(0).Tooltip(statusText(status))
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
	return ui.Box(c).Size(size, size).Shrink(0).Role(ui.RoleProgress).Label("正在加载").Draw(func(p *ui.Painter, r ui.Rect) {
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

func statusText(status string) string {
	switch status {
	case agent.StatusWaiting:
		return "待处理：agent 在等你批准或回答"
	case agent.StatusWorking:
		return "工作中"
	case agent.StatusIdle:
		return "本轮完成，等你下一步"
	case agent.StatusStarting:
		return "启动中"
	case agent.StatusExited:
		return "已退出"
	case statusTasks:
		return "等待任务：在等它派出去的 agent"
	case statusUnread:
		return "本轮完成，你还没看"
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
	if a.filesShown(w) {
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
				// 开关固定在右上角，和关着时标签栏里的同一个位置（右边留 16，底下让出 1 像素的分隔线），不跟着文件栏滑动
				ui.Row(c).Absolute().Top(0).Right(16).Height(titleHeight(c)).Padding(0, 0, hairline, 0).AlignItems(ui.Center).Children(func() {
					a.filesPanelButton(c, w)
				})
			})
		}
	})
}

// mainColumn 是主区域里文件栏左边的部分：标签栏和终端（或编辑器），浮层面板也浮在这里面。
func (a *app) mainColumn(c *ui.Context, w *workspace, cur *terminalInfo) {
	t := c.Theme()
	p := colors(t)
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 16, 0, 8+a.barInset(c)).Gap(2).Background(p.band).
		BorderWidth(0, 0, hairline, 0).BorderColor(p.line).DragWindow().Clip().Children(func() {
		// 标签一段占剩下的宽度，放不下时左右滚动（切到哪个就滚到哪个）；右边的按钮组一直完整显示。
		// 滚动区比标签栏高出一截、被标签栏裁掉：mygo 的滚动条压在标签下半截上，照浏览器的标签栏不显示，
		// 触控板和滚轮照常能滚
		strip := ui.ScrollHorizontal(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).
			Height(titleHeight(c) + 24).AlignSelf(ui.Start).AlignItems(ui.Start)
		// 文件栏滑进来、窗口变窄之后，当前标签可能被挤到外面：宽度变了就再滚一次
		if sw := strip.Bounds().W; sw != a.tabStripW {
			a.tabStripW, a.shownTab = sw, ""
		}
		strip.Children(func() {
			ui.Row(c).Height(titleHeight(c)).Gap(2).AlignItems(ui.Center).Children(func() {
				// 拖动排序会在这次遍历里重排 w.Terminals：遍历副本
				for _, term := range slices.Clone(w.Terminals) {
					a.tab(c, w, term, cur != nil && cur.ID == term.ID)
				}
				a.docTabs(c, w)
				a.newTerminalButton(c, w)
			})
		})
		ui.Row(c).Shrink(0).Gap(2).AlignItems(ui.Center).Children(func() { a.tabBarButtons(c, w, cur) })
	})
	a.mainBody(c, w, cur)
}

// newTerminalButton 是标签后面的"+"，选 agent 或普通终端。
func (a *app) newTerminalButton(c *ui.Context, w *workspace) {
	menuButton(c, iconButton(c.Key("new"), iconPlus, "新建终端"), func(m *popMenu) {
		for _, ag := range slices.Concat(a.agentChoices(), []string{""}) {
			name := agentTitle(ag)
			if ag == "" {
				name = "终端"
			}
			it := m.Item(name).Lead(func() { agentIcon(c, ag, iconSize) })
			if ag == "" {
				it.Shortcut(ui.Cmd, ui.KeyT)
			}
			if it.Chosen() {
				a.startTerminal(w.ID, ag)
			}
		}
	})
}

// tabBarButtons 是标签栏右边的按钮组：分支、Git 日志、各个面板和文件栏的开关。
func (a *app) tabBarButtons(c *ui.Context, w *workspace, cur *terminalInfo) {
	if w.Branch != "" {
		a.branchButton(c, w)
		b := iconButton(c.Key("git-log"), iconGitLog, "Git 日志 ⌘9")
		if a.logPanelOpen() {
			b.Background(colors(c.Theme()).tint)
		}
		if b.Clicked() {
			a.toggleLogPanel(w)
		}
	}
	// 图片、技能与工具跟着会话走，放在终端右上角（sessionButtons）；历史按项目，在文件栏里
	// 文件栏开着时开关在它的顶栏右端，和关着时同一个位置（照 Orca）
	if !a.filesPanelOpen() && a.filesPanelShown == 0 {
		a.filesPanelButton(c, w)
	}
}

// sessionButtons 是 agent 终端右上角竖排的一组按钮，都跟着这个会话走：worker 回到它的任务、
// 这个会话粘贴过的图片、用到的技能与工具。放在终端里而不是标签栏，标签多时也看得出是哪个会话的。
// 它盖在终端内容上：鼠标不在上面时变淡；底部的箭头收成贴着右边缘的小把手，点把手再展开（记在 ui.json）。
func (a *app) sessionButtons(c *ui.Context, cur *terminalInfo) {
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
		label := "展开会话按钮"
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
	col := ui.Column(c.Key("session-buttons")).Absolute().Top(8).Right(8).Gap(2).Padding(2).Radius(rControl).Background(t.Surface) // 不透明：挡住底下的终端文字，不描边也和终端分得开
	// 鼠标不在上面、也没打开它的面板时变淡，底下的字透得出来
	ours := a.state.RightPanel == panelImages || a.state.RightPanel == panelUsage
	opacity := float32(0.5)
	if col.Hovered() || ours {
		opacity = 1
	}
	col.Opacity(col.AnimateTo("fade", opacity, ui.Animation{Duration: 160 * time.Millisecond, Ease: ui.EaseOut}))
	col.Children(func() {
		// 这一列的按钮不带悬停提示：它们贴着终端内容，提示会盖住旁边的字
		if task != nil && quietIconButton(c.Key("back-to-task"), iconTasks, "返回任务", buttonBox(), iconSize).Clicked() {
			a.openTask(task.ID)
		}
		if media.Supported(cur.Agent) {
			a.panelButton(c, panelImages, iconImage, "这个会话里生成和粘贴的图片")
		}
		if cur.Agent != "" {
			a.panelButton(c, panelUsage, iconExtensions, "这个会话用到的技能与工具")
		}
		hline(c).Margin(2, 4)
		if quietIconButton(c.Key("session-fold"), iconChevron, "收起会话按钮", buttonBox(), iconSize-4).Clicked() {
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
		ui.Text(c, "手机正在看这个终端，按手机屏幕排版").FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if pressable(textButton(c, "改回电脑尺寸")).Clicked() {
			a.reclaimSize(id)
		}
	})
}

// reclaimSize 让常驻进程放掉手机对终端尺寸的占用，按窗口的尺寸重排。
func (a *app) reclaimSize(id string) {
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
			a.update(func() { a.err = "改回电脑尺寸失败：" + err.Error() })
		}
	}()
}

// mainBody 是标签栏下面的内容：终端、编辑器或空工作区，浮层面板浮在它上面。
func (a *app) mainBody(c *ui.Context, w *workspace, cur *terminalInfo) {
	t := c.Theme()
	// 右侧面板浮在上面，不挤压终端：终端宽度一变，程序就要按新列数重排输出
	body := ui.Column(c).Grow(1)
	if a.state.RightPanel != "" {
		a.lastPanel = a.state.RightPanel
	}
	// 打开时滑入淡入，关闭时反向；关掉后动画走完才移除，所以画的是最后打开的那个面板
	target := float32(0)
	if a.state.RightPanel != "" {
		target = 1
	}
	shown := body.AnimateTo("panel", target, ui.Animation{Duration: 180 * time.Millisecond, Ease: ui.EaseOut})
	body.Children(func() {
		switch {
		case a.filesActive(w):
			a.filesPage(c, w)
		case cur == nil || a.view == nil:
			a.emptyWorkspace(c, w)
		default:
			if cur.PhoneSized {
				a.phoneSizedBar(c, cur.ID)
			}
			// 留一点内边距，文字不贴边
			area := ui.Box(c).Grow(1).Background(t.Background).Padding(6, 4, 2, 12)
			a.view.dir = a.view.term.Dir()
			area.Children(func() {
				tv := terminal.View(c.Key(a.view.id), a.view.term).Fill().AutoFocus()
				if a.focusTerm {
					a.focusTerm = false
					tv.Focus()
				}
				// 右键菜单由终端插件交来位置，Posse 自己画（termMenu）
				if v := a.view; a.termCtxID == v.id {
					contextMenuAt(c, &a.termCtx, func(m *popMenu) { a.termMenu(m, v, v.id) })
				}
				a.startingHint(c, a.view, cur)
				a.sessionButtons(c, cur)
			})
			a.termDrop(c, area, cur.Agent)
			a.linkCardView(c, area)
			a.hoverPreviewView(c, area)
		}
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
	name := "终端"
	if cur.Agent != "" {
		name = agentTitle(cur.Agent)
	}
	label := "正在启动 " + name
	ui.Box(c.Key("starting-hint")).Absolute().Fill().Center().Cursor(ui.CursorDefault).Children(func() {
		ui.Column(c).Gap(12).AlignItems(ui.Center).Children(func() {
			posseLoader(c, tileLarge(), t.Accent, c.Preferences().ReduceMotion)
			ui.Text(c, label).FontSize(fsBody).TextColor(t.TextMuted)
		})
	})
}

// firstRun 是还没有项目时的引导：空界面是去行动的邀请。
func (a *app) firstRun(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).Width(440).Padding(24).Gap(16).Children(func() {
			tile(c, iconHexOutline, 48, soft(t, colors(t).honey), colors(t).honey)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, "把项目放进来", fsPage)
				ui.Text(c, "Posse 按项目管理终端和 agent。选一个文件夹作为项目，之后就能在里面开终端和 agent。").FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			ui.Row(c).Gap(8).Children(func() {
				if pressable(accentButton(c, "添加项目…")).Clicked() {
					go a.addProject()
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
	if h := sc.LayoutBounds().H; h != a.emptyHeight {
		a.emptyHeight = h
		c.AnimationFrame()
	}
	sc.Children(func() {
		ui.Column(c).MinHeight(a.emptyHeight).Center().Padding(24).Children(func() {
			ui.Column(c).Width(600).Gap(16).Children(func() {
				ui.Row(c).Gap(16).Padding(0, 4).AlignItems(ui.Center).Children(func() {
					tile(c, a.wsIcon(w), 52, p.tint, t.Accent)
					ui.Column(c).Gap(6).Grow(1).Shrink(1).Children(func() {
						titleText(c, displayName(w), fsPage).MaxLines(1)
						var branch func()
						if w.Branch != "" {
							branch = metaMono(c, iconBranch, w.Branch, "")
						}
						metaRow(c, metaMono(c, iconFolder, shortPath(w.Path), w.Path), branch)
					})
				})
				ui.Grid(c).Columns(len(a.agentChoices()) + 1).Gap(8).Children(func() {
					for _, ag := range a.agentChoices() {
						a.launchTile(c, w, ag, agentVendor(ag))
					}
					a.launchTile(c, w, "", "⌘T")
				})
				ui.Row(c).Gap(12).Padding(2, 4).Justify(ui.Center).Children(func() {
					shortcutHint(c, "⌘T", "新建终端")
					shortcutHint(c, "⌘N", "新建工作区")
					shortcutHint(c, "⌘1…9", "切换终端")
					shortcutHint(c, "⌘P", "打开文件")
				})
			})
		})
	})
}

// launchTile 是空工作区里启动 agent 的一格：agent 的色块图标和名字。
func (a *app) launchTile(c *ui.Context, w *workspace, agent, note string) {
	t := c.Theme()
	name := agentTitle(agent)
	if agent == "" {
		name = "终端"
	}
	b := ui.Column(c.Key("launch-"+name)).Padding(16, 8, 16, 8).Gap(8).Radius(rCard).AlignItems(ui.Center).Label("启动 " + name).Role(ui.RoleButton)
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

func (a *app) tab(c *ui.Context, w *workspace, term terminalInfo, active bool) {
	t := c.Theme()
	// 图标侧内边距比文字侧少 2px；圆角 8，内部 20px 的关闭按钮离边 4px，圆角取 8-4=4
	row := ui.Row(c.Key(term.ID)).Height(sz(28)).Padding(0, 4, 0, 8).Gap(6).Radius(rControl).Shrink(1).MaxWidth(280)
	hovered := row.Hovered()
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case hovered:
		bg = colors(t).wash
	}
	a.revealTab(row, term.ID, active)
	row.Background(row.AnimateColor("bg", bg, hover))
	status := ""
	if term.AgentState != nil && term.Running {
		status = a.sessionStatus(term)
	}
	name, detail := tabLabel(term)
	if title, ok := a.workerTitle[term.ID]; ok && term.Running && term.Name == "" {
		detail = title
	}
	renaming := a.renaming == term.ID
	if renaming && !active {
		// 用快捷键切走了：按现在输入的保存
		a.finishRename(term)
		renaming = false
	}
	if renaming {
		row.Border(focusRing, t.Focus)
	}
	row.Children(func() {
		if status != "" {
			a.statusIcon(c, term.ID, status, smallIcon())
		} else {
			ui.Icon(c, iconTerminal).FontSize(smallIcon()).TextColor(t.TextMuted).Shrink(0)
		}
		// agent 终端用图标代替名字（悬停提示是名字）；还没有标题时照旧写名字，标签不至于只剩图标。
		// 图标和左边的状态格一样大
		waiting := status == agent.StatusWaiting
		if term.Agent != "" {
			agentTile(c, term.Agent, smallIcon())
		}
		if renaming {
			a.renameInput(c, term)
			return
		}
		if term.Agent == "" || detail == "" && !waiting {
			tabText(c, ui.Text(c, name).FontSize(fsSmall).Shrink(1).SingleLine(), name, active)
		}
		// 待处理时只显示这个提示，标题让位，标签不至于挤成一串
		if waiting {
			// 字号、字重和别的标签标题一样，只用琥珀色区分
			label := ui.Text(c, "待处理").FontSize(fsSmall).Shrink(0)
			tabText(c, label, "待处理", active)
			label.TextColor(colors(t).honey)
		} else if detail != "" {
			title := ui.Text(c, detail).FontSize(fsSmall).Shrink(1).SingleLine()
			if term.Agent != "" {
				tabText(c, title, detail, active)
			} else {
				title.TextColor(t.TextMuted).Margin(0, 0, cjkLift, 0)
			}
		}
		closeBtn := ui.Box(c.Key("close")).Size(miniBox(), miniBox()).Radius(rSmall).Center().Shrink(0).Label("关闭终端").Tooltip("关闭终端 ⌘W")
		closeBg := ui.Transparent
		if closeBtn.Hovered() {
			closeBg = t.SurfacePressed
		}
		closeBtn.Background(closeBtn.AnimateColor("bg", closeBg, hover))
		visible := active || hovered
		if !visible {
			closeBtn.Opacity(0)
		}
		closeBtn.Children(func() { ui.Icon(c, iconClose).FontSize(fsSmall).TextColor(t.TextMuted) })
		if visible && closeBtn.Clicked() {
			a.askClose(term)
		}
	})
	// 改名时不能拖：在输入框里拖着选文字会变成拖标签
	if !renaming {
		a.reorderableTerm(c, row, w.ID, term.ID, true)
	}
	if row.Clicked() {
		a.activeTerm[w.ID] = term.ID
	}
	// 双击改名，和 iTerm、Ghostty 一样
	if row.DoubleClicked() && !renaming {
		a.startRename(term)
	}
	// 中键关闭，和浏览器、Orca 一样
	if row.MiddleClicked() {
		a.askClose(term)
	}
	row.Tooltip(tabTooltip(term))
	contextMenu(c, row, func(m *popMenu) { a.tabMenu(m, w, term) })
}

// tabTooltip 是终端标签的悬停提示：完整的名字和标题，标签上只有图标或标题被截断时看得到全文。
func tabTooltip(t terminalInfo) string {
	name := agentTitle(t.Agent)
	title := bareTitle(t)
	if t.Name != "" {
		title = t.Name
	}
	switch {
	case name == "" && title == "":
		return "终端"
	case name == "":
		return title
	case title == "" || strings.EqualFold(title, name) || title == "Claude Code":
		return name
	}
	return name + "\n" + title
}

// tabMenu 是终端标签的右键菜单：关闭这个、其他的、右边的终端（照 Orca）；文件标签不受影响。
func (a *app) tabMenu(m *popMenu, w *workspace, term terminalInfo) {
	if m.Item("重命名…").Chosen() {
		a.startRename(term)
	}
	if term.Name != "" && m.Item("恢复默认名称").Chosen() {
		a.renameTerminal(term.ID, "")
	}
	m.Separator()
	if m.Item("关闭").Shortcut(ui.Cmd, ui.KeyW).Chosen() {
		a.askClose(term)
	}
	i := slices.IndexFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == term.ID })
	if i < 0 {
		return
	}
	others := slices.Concat(w.Terminals[:i], w.Terminals[i+1:])
	right := slices.Clone(w.Terminals[i+1:])
	if m.Item("关闭其他终端").Disabled(len(others) == 0).Chosen() {
		a.askCloseAll(others)
	}
	if m.Item("关闭右侧终端").Disabled(len(right) == 0).Chosen() {
		a.askCloseAll(right)
	}
}

// revealTab 在标签刚变成当前的那一帧把它滚进可见范围：标签多了要左右滚动时，切过去的那个不会藏在外面。
// 只在切换时滚一次，用户自己滚开了不拉回来。
func (a *app) revealTab(row ui.Element, id string, active bool) {
	if active && a.shownTab != id {
		a.shownTab = id
		row.ScrollIntoView()
	}
}

// tabText 是标签上的主文字：选中时加粗，否则用次要色。中文字形在行框里偏下，提半个像素才看着居中。
// 不管选没选中都按加粗的宽度占位，切换标签时文字变粗不会把后面的标签往右挤。
// 占位用 flex basis 而不是固定宽度：固定宽度也是最小宽度，长标题就缩不了，关闭按钮会被挤出标签。
func tabText(c *ui.Context, e ui.Element, text string, active bool) {
	w, _ := c.MeasureText(0, ui.Span{Text: text, Size: fsSmall, Weight: fwMedium})
	e.Basis(w+1).Margin(0, 0, cjkLift, 0)
	if active {
		e.FontWeight(fwMedium)
	} else {
		e.TextColor(c.Theme().TextMuted)
	}
}

// tabLabel 返回标签的名字和补充说明（会话名，没有时是终端标题），分开显示而不是用分隔符拼接。
func tabLabel(t terminalInfo) (string, string) {
	name := agentTitle(t.Agent)
	title := bareTitle(t)
	if t.AgentState != nil && t.AgentState.Title != "" {
		title = t.AgentState.Title
	}
	if t.Name != "" {
		title = t.Name
	}
	detail := ""
	switch {
	case name == "" && title != "":
		name = title
	case name == "":
		name = "终端"
	case title != "" && !strings.EqualFold(title, name) && title != "Claude Code":
		detail = title
	}
	if !t.Running {
		detail = "已退出"
	}
	return truncate(name, 24), truncate(detail, 22)
}

// bareTitle 是去掉 agent 状态前缀（✳、转圈符）的终端标题。
func bareTitle(t terminalInfo) string {
	return strings.TrimSpace(strings.TrimLeft(t.Title, "✳⠀⠁⠂⠃⠄⠅⠆⠇⠈⠉⠊⠋⠌⠍⠎⠏◐◓◑◒ "))
}

// displayName 是工作区的显示名：主工作区就是项目本身。
func displayName(w *workspace) string {
	if w.Kind == "main" {
		return w.Project
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
	ui.Modal(c, &a.confirming, func() {
		ui.Column(c).Width(340).Gap(8).Children(func() {
			titleText(c, a.confirmTitle, fsTitle)
			if a.confirmBody != "" {
				ui.Text(c, a.confirmBody).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if a.confirmDo != nil && pressable(textButton(c, "取消")).Clicked() {
					a.confirming = false
				}
				if pressable(accentButton(c, a.confirmVerb)).Clicked() {
					a.confirming = false
					if a.confirmDo != nil {
						go a.confirmDo()
					}
				}
			})
		})
	})
}

func (a *app) newWorkspaceDialog(c *ui.Context) {
	t := c.Theme()
	ui.Modal(c, &a.creating, func() {
		ui.Column(c).Width(380).Gap(12).Children(func() {
			titleText(c, "新建工作区", fsTitle)
			var names []string
			kind := ""
			for _, p := range a.projects {
				names = append(names, p.Name)
				if p.Name == a.newProject {
					kind = p.Kind
				}
			}
			ui.Column(c).Gap(6).Children(func() {
				ui.Select(c, &a.newProject, names)
				hint := "在同一个文件夹里另开一组终端。"
				if kind == "git" {
					hint = "新建一个 git worktree 和同名分支，改动互不干扰。"
				}
				ui.Text(c, hint).FontSize(fsSmall).TextColor(t.TextMuted)
			})
			submitted := fieldInput(c, &a.newName, "名称，比如 feat-login").AutoFocus().Submitted()
			choices := a.newAgentChoices()
			if a.newAgent >= len(choices) {
				a.newAgent = 0
			}
			labels := agentTitles(choices)
			labels[0] = "只建工作区"
			segmented(c, &a.newAgent, "同时启动", labels...)
			if a.newAgent != 0 {
				if fieldInput(c.Key("prompt"), &a.newPrompt, "交给 agent 的任务（可留空）").Submitted() {
					submitted = true
				}
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					a.creating = false
				}
				if pressable(accentButton(c, "创建工作区")).Clicked() {
					submitted = true
				}
			})
			if submitted && strings.TrimSpace(a.newName) != "" {
				a.creating = false
				params := map[string]any{"project": a.newProject, "name": strings.TrimSpace(a.newName), "agent": choices[a.newAgent], "prompt": a.newPrompt}
				go a.createWorkspace(params)
			}
		})
	})
}
