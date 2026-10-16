package app

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"hive/internal/agent"
)

func (a *app) build(c *ui.Context) {
	a.runPending()
	c.SetTheme(hiveTheme(c.Theme().Dark))
	t := c.Theme()
	p := colors(t)
	board := a.selected == taskBoardID
	if !board {
		a.setWantedTask("")
	}
	a.setHistoryVisible(a.selected == historyBoardID)
	if a.loaded && a.selected != a.state.Selected {
		a.state.Selected = a.selected
		a.saveState()
	}
	w := a.workspace(a.selected)
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

	vibrant := c.Vibrancy()
	if vibrant {
		c.Root().Background(ui.Transparent)
	}
	a.imagesDrawn = false
	ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
		side := ui.Column(c).Width(248).Shrink(0)
		if !vibrant {
			side.Background(p.side)
		}
		side.Children(func() { a.sidebar(c) })
		ui.Box(c).Width(1).Shrink(0).Background(p.line)
		ui.Column(c).Grow(1).Background(p.canvas).Children(func() {
			switch {
			case board:
				a.taskBoard(c)
			case a.selected == historyBoardID:
				a.historyBoard(c)
			default:
				a.main(c, w, cur)
			}
		})
	})
	// 图片面板这一帧没画（收起了、换到别的页面或普通终端）就放掉位图
	if !a.imagesDrawn && a.images.term != "" {
		a.images = imagesView{}
	}
	a.newWorkspaceDialog(c)
	a.dispatchDialog(c)
	a.mobileDialog(c)
	a.quickDialog(c)
	a.pushDialog(c)
	a.promptDialog(c)
	a.unsavedDialog(c)
	a.confirmDialog(c)
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
		if c.Shortcut(ui.Cmd, ui.KeyL) {
			if a.filesPanelOpen() {
				a.closeFilesPanel()
			} else {
				a.openFilesPanel(w, a.filesFor(w).side)
			}
		}
		if c.Shortcut(ui.Cmd|ui.Shift, ui.KeyG) {
			a.toggleFilesPanel(w, sideChanges)
		}
		if c.Shortcut(ui.Cmd, ui.KeyK) && a.filesShown(w) && a.filesFor(w).git.repo != "" {
			a.openFilesPanel(w, sideChanges).git.focusMsg = true
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
	// 打包的 app 里 ⌘W 先被菜单拿走（installMenu），这里管没有菜单的情况（测试）
	if c.Shortcut(ui.Cmd, ui.KeyW) {
		a.closeTab()
	}
	if c.Shortcut(ui.Cmd, ui.KeyN) {
		a.openNewWorkspace()
	}
	if c.Shortcut(ui.Cmd, ui.KeyEqual) {
		a.setFontSize(a.fontSize + 1)
	}
	if c.Shortcut(ui.Cmd, ui.KeyMinus) {
		a.setFontSize(a.fontSize - 1)
	}
	if c.Shortcut(ui.Cmd, ui.Key0) {
		a.setFontSize(defaultFontSize)
	}
	if w != nil {
		keys := []ui.Key{ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9}
		for i, k := range keys {
			if i < len(w.Terminals) && c.Shortcut(ui.Cmd, k) {
				a.activeTerm[w.ID] = w.Terminals[i].ID
			}
		}
	}
}

// titleHeight 是顶栏高度：隐藏标题栏后，红绿灯按钮叠在侧栏顶部。
func titleHeight(c *ui.Context) float32 { return max(c.TitleBar().Height, 40) }

// --- 侧栏

func (a *app) sidebar(c *ui.Context) {
	t := c.Theme()
	bar := c.TitleBar()
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 10, 0, bar.Left+12).DragWindow().Children(func() {
		ui.Spacer(c)
		iconButton(c, iconPlus, "新建").Menu(func(m *ui.Menu) {
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
	})
	if !a.loaded {
		msg, color := "正在连接常驻进程…", t.TextMuted
		if a.err != "" {
			msg, color = "连不上常驻进程："+a.err, t.Danger
		}
		ui.Text(c, msg).FontSize(12).TextColor(color).Padding(6, 16)
	}
	a.navOrder = a.navOrder[:0]
	nav := ui.Scroll(c).Grow(1).Focusable().Label("工作区")
	nav.Children(func() {
		ui.Column(c).Padding(4, 10, 12, 10).Gap(2).Children(func() {
			if len(a.projects) > 0 {
				a.taskSidebarItem(c)
			}
			a.historySidebarItem(c)
			// 任务、历史是全局页面，和下面的项目用一道细线隔开
			ui.Box(c).Height(1).Shrink(0).Margin(6, 12, 6, 12).Background(colors(t).line)
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
		i := slices.Index(a.navOrder, a.selected)
		switch {
		case nav.Shortcut(0, ui.KeyDown):
			a.selected = a.navOrder[min(i+1, n-1)]
		case nav.Shortcut(0, ui.KeyUp):
			a.selected = a.navOrder[max(i-1, 0)]
		}
	}
	a.errorBar(c)
	a.footer(c)
}

// projectItem 是侧栏里的一个项目：项目行就是它的主工作区，其余工作区缩进在下面，默认收起，单击项目行展开。
// 收起时项目行汇总所有工作区的状态，子工作区等你处理也看得到。
// reveal 表示要定位的工作区在这个项目里。
func (a *app) projectItem(c *ui.Context, p project, ws []workspace, reveal bool) {
	head, rest := ws[0], ws[1:]
	open := a.sectionsOpen[p.ID]
	if open == nil {
		open = new(bool)
		a.sectionsOpen[p.ID] = open
	}
	// 要定位的工作区在收起的项目里时先展开，不然看不到选中项
	if reveal && a.revealSel && a.selected != head.ID {
		*open = true
	}
	group := ui.Column(c.Key(p.ID)).Gap(2).Shrink(0)
	group.Children(func() {
		var also []workspace
		var toggle func()
		if len(rest) > 0 {
			if !*open {
				also = rest
			}
			toggle = func() { disclosure(c, open, len(rest)) }
		}
		// 单击项目行就展开；已经选中并展开时再单击收起
		wasSelected := a.selected == head.ID
		if row := a.sidebarItem(c, head, p.Name, also, toggle); row.Clicked() && len(rest) > 0 {
			*open = !*open || !wasSelected
		}
		if *open {
			ui.Column(c).Gap(2).Padding(0, 0, 0, 22).Children(func() {
				for _, w := range rest {
					a.sidebarItem(c, w, w.Name, nil, nil)
				}
			})
		}
	})
	a.reorderable(c, group, p.ID)
}

// disclosure 是项目行尾的展开箭头，展开时转向下。
func disclosure(c *ui.Context, open *bool, n int) {
	label := fmt.Sprintf("展开 %d 个工作区", n)
	if *open {
		label = "收起"
	}
	t := c.Theme()
	b := ui.Box(c).Size(20, 20).Radius(5).Center().Shrink(0).Label(label).Tooltip(label)
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
		chev := ui.Icon(c, iconChevron).FontSize(12).TextColor(fg)
		chev.Rotate(chev.AnimateWith("turn", turn, 150*time.Millisecond, ui.EaseOut))
	})
	if b.Clicked() {
		*open = !*open
	}
}

// sidebarItem 是侧栏里的一个工作区；also 的终端也计入运行数和状态（收起的子工作区），
// extra 画在行尾最后（展开箭头）。
func (a *app) sidebarItem(c *ui.Context, w workspace, label string, also []workspace, extra func()) ui.Element {
	t := c.Theme()
	running, status := terminalSummary(w.Terminals)
	for i := range also {
		r, s := terminalSummary(also[i].Terminals)
		running += r
		if statusRank(s) > statusRank(status) {
			status = s
		}
	}
	var trailing func()
	if status != "" || running > 0 || extra != nil {
		trailing = func() {
			if status == agent.StatusWaiting {
				pendingLabel(c)
			} else if running > 0 {
				ui.Text(c, fmt.Sprint(running)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(fmt.Sprintf("%d 个终端在运行", running))
			}
			if status != "" {
				statusGlyph(c, status, 12)
			}
			if extra != nil {
				extra()
			}
		}
	}
	// worktree 都在 $HIVE_HOME/worktrees 下，路径截断后看不出什么，改显示分支；
	// 分支默认和工作区同名，同名时只留一行
	sub := shortPath(w.Path)
	if w.Kind == "worktree" {
		sub = w.Branch
		if sub == label {
			sub = ""
		}
	}
	item := a.navRow(c, w.ID, workspaceIcon(w.Kind), label, sub, trailing)
	if a.revealSel && w.ID == a.selected {
		item.ScrollIntoView()
		a.revealSel = false
	}
	item.ContextMenu(func(m *ui.Menu) {
		for _, ag := range agentChoices {
			if m.Item("启动 " + agentTitle(ag)).Chosen() {
				a.startTerminal(w.ID, ag)
			}
		}
		if m.Item("打开终端").Chosen() {
			a.startTerminal(w.ID, "")
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
	})
	return item
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
		accent := c.Theme().Accent
		below := projectIndex(a.projects, d.id) < projectIndex(a.projects, id)
		e.DrawOver(func(p *ui.Painter, r ui.Rect) {
			y := r.Y - 1
			if below {
				y = r.Y + r.H - 1
			}
			p.Fill(ui.Rect{X: r.X + 8, Y: y, W: r.W - 16, H: 2}, accent, 1)
		})
	}
	if d, ok := ui.Drop[projectDrag](e); ok && d.id != id {
		a.state.ProjectOrder = moveProject(a.projects, d.id, id)
		sortProjects(a.projects, a.state.ProjectOrder)
		a.saveState()
		c.AnimationFrame() // 这一帧已经按旧顺序画了，再画一帧
	}
}

// navRow 是侧栏的一行，sub 非空时在名称下面用等宽小字显示（路径或分支）；选中和悬停的样式见 selectable。
func (a *app) navRow(c *ui.Context, id string, icon *ui.SVG, label, sub string, trailing func()) ui.Element {
	t := c.Theme()
	selected := a.selected == id
	a.navOrder = append(a.navOrder, id)
	height := float32(32)
	if sub != "" {
		height = 46
	}
	row := ui.Row(c.Key(id)).Height(height).Padding(0, 10, 0, 12).Gap(10).Radius(8).Shrink(0).Label(label).Role(ui.RoleButton)
	selectable(c, row, selected)
	row.Children(func() {
		ui.Icon(c, icon).FontSize(16).TextColor(t.Accent).Shrink(0)
		// 行尾的状态和箭头跟名称放在第一行，路径独占第二行的整行宽度
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				text := ui.Text(c, label).FontSize(13).SingleLine().Grow(1).Shrink(1)
				if selected {
					text.FontWeight(600)
				}
				if trailing != nil {
					trailing()
				}
			})
			if sub != "" {
				ui.Text(c, sub).FontSize(10).Font(monoFont).TextColor(t.TextMuted).SingleLine()
			}
		})
	})
	if row.Clicked() {
		a.selected = id
	}
	return row
}

func (a *app) errorBar(c *ui.Context) {
	if a.err == "" || !a.loaded {
		return
	}
	t := c.Theme()
	ui.Row(c).Margin(0, 10, 8, 10).Padding(8, 6, 8, 10).Gap(6).Radius(8).Background(t.Surface).AlignItems(ui.Start).Children(func() {
		ui.Text(c, a.err).FontSize(11).TextColor(t.Danger).Grow(1).Shrink(1)
		if iconButtonSized(c, iconClose, "关闭提示", 20, 12).Clicked() {
			a.err = ""
		}
	})
}

func (a *app) footer(c *ui.Context) {
	t := c.Theme()
	if !a.loaded {
		return
	}
	// 底栏比侧栏深一层
	shade := ui.RGBA(0, 0, 0, 0.03)
	if t.Dark {
		shade = ui.RGBA(0, 0, 0, 0.18)
	}
	ui.Row(c).Height(34).Padding(0, 14).Shrink(0).Background(shade).BorderWidth(1, 0, 0, 0).BorderColor(colors(t).line).Children(func() {
		running := "没有运行中的终端"
		if n := a.daemon.Running; n > 0 {
			running = fmt.Sprintf("%d 个终端在运行", n)
		}
		ui.Text(c, running).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
		ui.Spacer(c)
		if pressable(iconButtonSized(c, iconPhone, "连接手机", 24, 14)).Tooltip("连接手机").Clicked() {
			a.openMobile()
		}
		total := a.daemon.FootprintKB + a.selfKB
		tip := fmt.Sprintf("常驻进程 %s，窗口 %s（与活动监视器的“内存”一致）", mb(a.daemon.FootprintKB), mb(a.selfKB))
		ui.Text(c, mb(total)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(tip)
	})
}

func mb(kb int64) string {
	if kb <= 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f MB", float64(kb)/1024)
}

// terminalSummary 返回运行中的终端数和其中 agent 最要紧的状态。
func terminalSummary(ts []terminalInfo) (running int, status string) {
	for _, t := range ts {
		if !t.Running {
			continue
		}
		running++
		if t.AgentState != nil && statusRank(t.AgentState.Status) > statusRank(status) {
			status = t.AgentState.Status
		}
	}
	return running, status
}

// statusRank 是汇总状态时的优先级，等你处理最高；不认识的状态不参与汇总。
func statusRank(s string) int {
	switch s {
	case agent.StatusWaiting:
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
	}
	return status
}

// --- 主区域

func (a *app) main(c *ui.Context, w *workspace, cur *terminalInfo) {
	t := c.Theme()
	p := colors(t)
	if w == nil {
		band(c, func() {})
		if a.loaded && len(a.projects) == 0 {
			a.firstRun(c)
		}
		return
	}
	a.overPanelButton = false
	if a.filesShown(w) {
		a.filesTick(c, w)
	}
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 14, 0, 8).Gap(2).Background(p.band).
		BorderWidth(0, 0, 1, 0).BorderColor(p.line).DragWindow().Children(func() {
		for _, term := range w.Terminals {
			a.tab(c, w, term, cur != nil && cur.ID == term.ID)
		}
		a.docTabs(c, w)
		iconButton(c.Key("new"), iconPlus, "新建终端").Menu(func(m *ui.Menu) {
			for _, ag := range agentChoices {
				if m.Item(agentTitle(ag)).Chosen() {
					a.startTerminal(w.ID, ag)
				}
			}
			if m.Item("终端").Shortcut(ui.Cmd, ui.KeyT).Chosen() {
				a.startTerminal(w.ID, "")
			}
		})
		ui.Spacer(c)
		if w.Branch != "" {
			a.branchButton(c, w)
			if iconButton(c.Key("git-log"), iconGitLog, "Git 日志").Clicked() {
				a.openLog(w, "", "")
			}
		}
		if cur != nil && cur.Agent == "claude" {
			a.panelButton(c, panelImages, iconImage, "这个会话粘贴过的图片")
		}
		if cur != nil && cur.Agent != "" {
			a.panelButton(c, panelUsage, iconExtensions, "这个会话用到的技能与 MCP")
		}
		a.panelButton(c, panelHistory, iconHistory, "历史会话")
		a.panelButton(c, panelFiles, iconPanelRight, "文件栏 ⌘L")
	})
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
			// 留一点内边距，文字不贴边
			area := ui.Box(c).Grow(1).Background(t.Background).Padding(6, 4, 2, 10)
			a.view.dir = a.view.term.Dir()
			area.Children(func() {
				terminal.View(c.Key(a.view.id), a.view.term).Fill().AutoFocus()
			})
			a.linkCardView(c, area)
			a.hoverPreviewView(c, area)
		}
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
		case panelHistory:
			panel, drawn = a.recentPanel(c, w), true
		case panelFiles:
			panel, drawn = a.filesPanel(c, w), true
		case panelImages:
			if cur != nil && cur.Agent != "" {
				panel, drawn = a.imagesPanel(c, cur), true
				a.imagePreview(c)
			}
		}
		if drawn {
			panel.Opacity(shown).Transform(ui.Translation((1-shown)*24, 0))
			// 点面板外面任何地方都收起，这一下照常落到下面的终端或按钮上
			if a.state.RightPanel != "" && !a.dialogOpen() && !a.resizingPanel && panel.PressedOutside() && !a.overPanelButton {
				a.state.RightPanel = ""
				a.saveState()
			}
		}
	})
}

// firstRun 是还没有项目时的引导：空界面是去行动的邀请。
func (a *app) firstRun(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Children(func() {
		card(c).Width(440).Padding(24).Gap(18).Children(func() {
			tile(c, iconHexOutline, 48, colors(t).honey.Alpha(0.16), colors(t).honey)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, "把项目放进来", 20)
				ui.Text(c, "Hive 按项目管理终端和 agent。选一个文件夹作为项目，之后就能在里面开终端和 agent。").FontSize(13).TextColor(t.TextMuted).LineHeight(1.5)
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
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(48, 24).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(600).Gap(14).Children(func() {
				card(c).Padding(20).Children(func() {
					ui.Row(c).Gap(16).Children(func() {
						tile(c, workspaceIcon(w.Kind), 52, p.tint, t.Accent)
						ui.Column(c).Gap(6).Grow(1).Shrink(1).Children(func() {
							titleText(c, displayName(w), 20).MaxLines(1)
							var branch func()
							if w.Branch != "" {
								branch = metaMono(c, iconBranch, w.Branch, "")
							}
							metaRow(c, metaMono(c, iconFolder, shortPath(w.Path), w.Path), branch)
						})
					})
				})
				ui.Grid(c).Columns(len(agentChoices) + 1).Gap(10).Children(func() {
					for _, ag := range agentChoices {
						a.launchTile(c, w, ag, agentVendor(ag))
					}
					a.launchTile(c, w, "", "⌘T")
				})
				ui.Row(c).Gap(14).Padding(2, 4).Children(func() {
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
	b := ui.Column(c.Key("launch-"+name)).Padding(16, 8, 14, 8).Gap(10).Radius(10).AlignItems(ui.Center).Label("启动 " + name).Role(ui.RoleButton)
	bg := colors(t).wash
	if b.Hovered() {
		bg = t.SurfaceHover
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	pressable(b)
	b.Children(func() {
		agentTile(c, agent, 40).Tooltip("")
		ui.Column(c).Gap(2).AlignItems(ui.Center).Children(func() {
			ui.Text(c, name).FontSize(13).FontWeight(500)
			ui.Text(c, note).FontSize(11).TextColor(t.TextMuted)
		})
	})
	if b.Clicked() {
		a.startTerminal(w.ID, agent)
	}
}

func shortcutHint(c *ui.Context, keys, label string) {
	t := c.Theme()
	ui.Row(c).Gap(6).Children(func() {
		ui.Text(c, keys).FontSize(11).TextColor(t.Text).Padding(1, 6).Radius(4).Background(t.Surface)
		ui.Text(c, label).FontSize(11).TextColor(t.TextMuted)
	})
}

func (a *app) tab(c *ui.Context, w *workspace, term terminalInfo, active bool) {
	t := c.Theme()
	// 图标侧内边距比文字侧少 2px；圆角 8，内部 20px 的关闭按钮离边 4px，圆角取 8-4=4
	row := ui.Row(c.Key(term.ID)).Height(28).Padding(0, 4, 0, 8).Gap(6).Radius(8).Shrink(1).MaxWidth(280)
	hovered := row.Hovered()
	bg := ui.Transparent
	switch {
	case active:
		bg = t.SurfacePressed
	case hovered:
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	status := ""
	if term.AgentState != nil && term.Running {
		status = term.AgentState.Status
	}
	name, detail := tabLabel(term)
	if title, ok := a.workerTitle[term.ID]; ok && term.Running {
		detail = title
	}
	row.Children(func() {
		if status != "" {
			statusGlyph(c, status, 13)
		} else {
			ui.Icon(c, iconTerminal).FontSize(14).TextColor(t.TextMuted).Shrink(0)
		}
		// agent 终端用图标代替名字（悬停提示是名字）；还没有标题时照旧写名字，标签不至于只剩图标。
		// 图标和左边的状态格一样大
		waiting := status == agent.StatusWaiting
		if term.Agent != "" {
			agentTile(c, term.Agent, 13)
		}
		if term.Agent == "" || detail == "" && !waiting {
			tabText(c, ui.Text(c, name).FontSize(12).Shrink(0), active)
		}
		// 待处理时只显示这个提示，标题让位，标签不至于挤成一串
		if waiting {
			pendingLabel(c).Shrink(0)
		} else if detail != "" {
			title := ui.Text(c, detail).FontSize(12).Shrink(1).MaxLines(1)
			if term.Agent != "" {
				tabText(c, title, active)
			} else {
				title.TextColor(t.TextMuted).Margin(0, 0, cjkLift, 0)
			}
		}
		closeBtn := ui.Box(c.Key("close")).Size(20, 20).Radius(4).Center().Shrink(0).Label("关闭终端").Tooltip("关闭终端 ⌘W")
		closeBg := ui.Transparent
		if closeBtn.Hovered() {
			closeBg = t.SurfacePressed
		}
		closeBtn.Background(closeBtn.AnimateColor("bg", closeBg, hover))
		visible := active || hovered
		if !visible {
			closeBtn.Opacity(0)
		}
		closeBtn.Children(func() { ui.Icon(c, iconClose).FontSize(12).TextColor(t.TextMuted) })
		if visible && closeBtn.Clicked() {
			a.askClose(term)
		}
	})
	if row.Clicked() {
		a.activeTerm[w.ID] = term.ID
	}
}

// tabText 是标签上的主文字：选中时加粗，否则用次要色。中文字形在行框里偏下，提半个像素才看着居中。
func tabText(c *ui.Context, e ui.Element, active bool) {
	e.Margin(0, 0, cjkLift, 0)
	if active {
		e.FontWeight(500)
	} else {
		e.TextColor(c.Theme().TextMuted)
	}
}

// tabLabel 返回标签的名字和补充说明（终端标题），分开显示而不是用分隔符拼接。
func tabLabel(t terminalInfo) (string, string) {
	name := agentTitle(t.Agent)
	title := strings.TrimSpace(strings.TrimLeft(t.Title, "✳⠀⠁⠂⠃⠄⠅⠆⠇⠈⠉⠊⠋⠌⠍⠎⠏◐◓◑◒ "))
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

// iconButton 是只有图标的无边框按钮，28px 见方。
func iconButton(c *ui.Context, icon *ui.SVG, label string) ui.Element {
	return iconButtonSized(c, icon, label, 28, 16)
}

func iconButtonSized(c *ui.Context, icon *ui.SVG, label string, box, glyph float32) ui.Element {
	t := c.Theme()
	b := ui.Box(c).Size(box, box).Radius(box / 4).Center().Shrink(0).Label(label).Tooltip(label)
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
		ui.Column(c).Width(340).Gap(10).Children(func() {
			titleText(c, a.confirmTitle, fsCardTitle)
			if a.confirmBody != "" {
				ui.Text(c, a.confirmBody).FontSize(13).TextColor(t.TextMuted).LineHeight(1.45)
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
			titleText(c, "新建工作区", fsCardTitle)
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
				ui.Text(c, hint).FontSize(12).TextColor(t.TextMuted)
			})
			submitted := ui.TextInput(c, &a.newName).Placeholder("名称，比如 feat-login").AutoFocus().Submitted()
			segmented(c, &a.newAgent, "同时启动", newAgentLabels...)
			if a.newAgent != 0 {
				if ui.TextInput(c, &a.newPrompt).Placeholder("交给 agent 的任务（可留空）").Submitted() {
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
				params := map[string]any{"project": a.newProject, "name": strings.TrimSpace(a.newName), "agent": agentNames[a.newAgent], "prompt": a.newPrompt}
				go a.createWorkspace(params)
			}
		})
	})
}
