package app

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// 文件栏：主区域最右边的一栏，顶上分"历史"（当前项目的会话）和"文件"，文件下面再分文件树、检索、改动，照 Orca 的右侧栏。它停靠在那里、挤窄终端和编辑器，
// 不像"图片 / 技能与工具"那样浮在上面、点别处就收起：边看文件边编辑时要一直开着。
// 开关默认按项目记在 ui.json（在 A 项目打开，切到 B 项目不跟着打开），设置里可改成所有项目共用；宽度所有项目共用。
const (
	filesPanelDefault = 300
	filesPanelMin     = 220
	filesPanelMax     = 640
)

func (a *app) filesPanelWidth() float32 {
	w := a.state.FilesPanelWidth
	if w == 0 {
		w = filesPanelDefault
	}
	return min(max(w, filesPanelMin), filesPanelMax)
}

// filesPanelOpen 报告文件栏开着：共用时看共用的开关，否则看选中的工作区所在的项目。
func (a *app) filesPanelOpen() bool {
	if a.state.FilesPanelShared {
		return a.state.FilesPanelOn && a.panelProject() != ""
	}
	return a.state.FilesPanels[a.panelProject()]
}

// setFilesPanel 开关文件栏，变了才写 ui.json。
func (a *app) setFilesPanel(project string, open bool) {
	if !a.state.FilesPanelShared {
		a.setProjectPanel(&a.state.FilesPanels, project, open)
		return
	}
	if a.state.FilesPanelOn != open {
		a.state.FilesPanelOn = open
		a.saveState()
	}
}

// setFilesPanelShared 切换文件栏开关是否所有项目共用：当前项目的开关原样带过去，切换时界面不变。
func (a *app) setFilesPanelShared(shared bool) {
	if a.state.FilesPanelShared == shared {
		return
	}
	open := a.filesPanelOpen()
	a.state.FilesPanelShared = shared
	if shared {
		a.state.FilesPanelOn = open
	} else {
		a.state.FilesPanelOn = false
		if p := a.panelProject(); p != "" {
			if a.state.FilesPanels == nil {
				a.state.FilesPanels = map[string]bool{}
			}
			if open {
				a.state.FilesPanels[p] = true
			} else {
				delete(a.state.FilesPanels, p)
			}
		}
	}
	a.saveState()
}

// panelProject 是选中的工作区所在的项目：文件栏和日志浮层的开关按项目记。
func (a *app) panelProject() string {
	if w := a.workspace(a.selected); w != nil {
		return w.ProjectID
	}
	return ""
}

// setProjectPanel 记下项目的文件栏或日志浮层开没开，变了才写 ui.json。
func (a *app) setProjectPanel(m *map[string]bool, project string, open bool) {
	if project == "" || (*m)[project] == open {
		return
	}
	if open {
		if *m == nil {
			*m = map[string]bool{}
		}
		(*m)[project] = true
	} else {
		delete(*m, project)
	}
	a.saveState()
}

// snapPanels 在每帧开头调用：切到面板开关不一样的项目时，文件栏和日志浮层直接是那个项目的样子，
// 不播滑进滑出（只有点开关时才播）。这一帧别的动画也跟着跳到终点，所以开关一样时不调。
func (a *app) snapPanels(c *ui.Context) {
	now := panelSnap{a.panelProject(), a.filesPanelOpen(), a.logPanelOpen()}
	if now.project != a.lastPanels.project && (now.files != a.lastPanels.files || now.log != a.lastPanels.log) {
		c.DisableAnimations()
	}
	a.lastPanels = now
}

type panelSnap struct {
	project    string
	files, log bool
}

// migratePanels 在拿到工作区列表后调用：旧版不分项目的开关记到当前项目上，删掉的项目不再记着。
func (a *app) migratePanels() {
	if p := a.panelProject(); p != "" && (a.state.FilesPanel || a.state.LogPanel) {
		if a.state.FilesPanel {
			a.setProjectPanel(&a.state.FilesPanels, p, true)
		}
		if a.state.LogPanel {
			a.setProjectPanel(&a.state.LogPanels, p, true)
		}
		a.state.FilesPanel, a.state.LogPanel = false, false
		a.saveState()
	}
	pruned := false
	for _, m := range []map[string]bool{a.state.FilesPanels, a.state.LogPanels} {
		for id := range m {
			if !slices.ContainsFunc(a.projects, func(p project) bool { return p.ID == id }) {
				delete(m, id)
				pruned = true
			}
		}
	}
	if pruned {
		a.saveState()
	}
}

// openFilesPanel 打开工作区所在项目的文件栏并切到 side。
func (a *app) openFilesPanel(w *workspace, side int) *filesView {
	v := a.filesFor(w)
	v.side = side
	a.setFilesPanelTab("")
	a.setFilesPanel(w.ProjectID, true)
	return v
}

// revealInFiles 打开文件栏的文件树，展开到 path 并选中它（链接卡片的"在文件栏中显示"）。
func (a *app) revealInFiles(w *workspace, path string) {
	v := a.openFilesPanel(w, sideFiles)
	a.loadDirs(v, v.tree.Reveal(path))
	v.reveal, v.revealCur, v.revealScroll = path, v.cur, true
}

// revealable 报告文件树能不能定位到 d：diff、日志这类不是文件，工作区外的文件不在树上。
func revealable(v *filesView, d *fileDoc) bool {
	return d != nil && !d.virtual() && strings.HasPrefix(d.path, v.root+string(filepath.Separator))
}

// revealCurrent 在文件树里定位当前文件（⌥F1、文件栏顶栏和文件顶栏的准星）。
func (a *app) revealCurrent(w *workspace) {
	v := a.filesFor(w)
	if revealable(v, v.cur) {
		a.revealInFiles(w, v.cur.path)
		a.saveFilesState(v)
	}
}

// toggleFilesPanel 开着且就在 side 上时关掉，否则打开到 side（⇧⌘E、⇧⌘G）。
func (a *app) toggleFilesPanel(w *workspace, side int) {
	if a.filesPanelOpen() && a.state.FilesPanelTab == "" && a.filesFor(w).side == side {
		a.closeFilesPanel()
		return
	}
	a.openFilesPanel(w, side)
}

func (a *app) closeFilesPanel() {
	a.setFilesPanel(a.panelProject(), false)
}

// filesShown 报告工作区的文件内容这一帧看不看得见：文件栏开着，选中了文件标签，或者别的格里显示着文件。
func (a *app) filesShown(w *workspace) bool {
	if w == nil {
		return false
	}
	if a.filesPanelOpen() || a.filesActive(w) {
		return true
	}
	if w.ID != a.selected {
		return false
	}
	for _, col := range a.cols {
		if isDocKey(col.active) {
			return true
		}
	}
	return false
}

// filesTick 是文件内容看得见时每帧的事：到点轮询磁盘和 git，清单过期就重列，当前文件还没读就读。
func (a *app) filesTick(c *ui.Context, w *workspace) {
	v := a.filesFor(w)
	if v.poll.due(c, filesPollEvery) {
		a.pollFiles(v)
	}
	a.pollRepos(c, v)
	a.refreshList(v, false, nil)
	a.reportListErr(c, v)
	if d := v.cur; a.filesActive(w) && d != nil && d.kind == docLoading && !d.started {
		d.started = true
		a.loadDoc(d)
	}
}

// filesPanelButton 是开关文件栏的按钮：关着时在标签栏最右边，开着时在文件栏顶栏最右边，垫一层强调色淡底。
func (a *app) filesPanelButton(c *ui.Context, w *workspace) {
	b := iconButton(c.Key("files-panel"), iconPanelRight, a.withKey(L(c).FilesPanelTitle(), "filesPanel"))
	if a.filesPanelOpen() {
		b.Background(colors(c.Theme()).tint)
	}
	if b.Clicked() {
		if a.filesPanelOpen() {
			a.closeFilesPanel()
		} else {
			a.setFilesPanel(w.ProjectID, true)
		}
	}
}

// 文件栏顶上的页签（ui.json 的 filesPanelTab），空是"文件"。
const (
	tabHistory = "history"
	tabSkills  = "skills"
)

// setFilesPanelTab 切文件栏顶上的"历史""文件""技能"，变了才写 ui.json。
func (a *app) setFilesPanelTab(tab string) {
	if a.state.FilesPanelTab != tab {
		a.state.FilesPanelTab = tab
		a.saveState()
	}
}

// filesPanel 画文件栏：顶上和标签栏一样高，放"历史""文件""技能"三个图标页签和开关按钮；文件下面一行是文件、检索、改动。
// 左边缘拖动调宽。
func (a *app) filesPanel(c *ui.Context, w *workspace) {
	p := colors(c.Theme())
	v := a.filesFor(w)
	col := ui.Column(c).Width(a.filesPanelWidth()).Shrink(0).Background(p.band).Ref(&a.filesPanelRef)
	col.Children(func() {
		// 右边距和标签栏一样，开关按钮打开前后不挪位置
		ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 16, 0, 12).Gap(4).AlignItems(ui.Center).
			BorderWidth(0, 0, hairline, 0).BorderColor(p.line).DragWindow().Children(func() {
			tab := a.state.FilesPanelTab
			if panelTab(c, iconHistory, L(c).FilesPanelHistoryTab(), tab == tabHistory) {
				a.setFilesPanelTab(tabHistory)
			}
			if panelTab(c, iconFile, L(c).FilesPanelFilesTab(), tab == "") {
				a.setFilesPanelTab("")
			}
			if panelTab(c, iconSkill, L(c).FilesPanelSkillsTab(), tab == tabSkills) {
				a.setFilesPanelTab(tabSkills)
			}
			ui.Spacer(c)
			if tab == "" && !w.Missing && v.side == sideFiles {
				// 没有能定位的文件时置灰：外面包一层先禁用，按钮里的悬停才不亮
				wrap := ui.Box(c).Shrink(0)
				if !revealable(v, v.cur) {
					wrap.Disabled(true).Opacity(0.5)
				}
				wrap.Children(func() {
					if iconButton(c.Key("tree-locate"), iconLocate, a.withKey(L(c).FilesRevealCurrent(), "revealFile")).Clicked() {
						a.revealCurrent(w)
					}
				})
				if iconButton(c.Key("tree-collapse"), iconCollapse, L(c).FilesCollapseAll()).Clicked() {
					v.tree.CollapseAll()
					v.reveal = ""
					a.saveFilesState(v)
				}
			}
			// 开关按钮浮在这里（见 main），留出它的位置
			ui.Box(c).Width(28).Shrink(0)
		})
		switch {
		case a.state.FilesPanelTab == tabHistory:
			a.recentList(c, w)
		case a.state.FilesPanelTab == tabSkills && !w.Missing:
			a.skillsPanel(c, w)
		case w.Missing:
			ui.Text(c, L(c).FilesWorkspaceGone()).FontSize(fsSmall).TextColor(c.Theme().TextMuted).Padding(12, 16)
		default:
			a.filesSide(c, w, v)
		}
		a.filesPanelGrip(c)
	})
}

// panelTab 是文件栏顶上的图标页签：选中的垫一层强调色淡底、图标用正文色。返回这一帧是否点了它。
func panelTab(c *ui.Context, icon *ui.SVG, label string, active bool) bool {
	t := c.Theme()
	b := ui.Box(c.Key("panel-tab-"+label)).Size(30, 28).Radius(rRow).Center().Shrink(0).
		Role(ui.RoleTab).Label(label).Tooltip(label).Checked(active)
	bg, fg := ui.Transparent, t.TextMuted
	switch {
	case active:
		bg, fg = colors(t).tint, t.Text
	case b.Hovered():
		bg, fg = t.SurfaceHover, t.Text
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() { ui.Icon(c, icon).FontSize(iconSize).TextColor(fg) })
	return b.Clicked() && !active
}

// filesSide 是文件栏"文件"页：上面一行分段切换文件树、检索、改动。
func (a *app) filesSide(c *ui.Context, w *workspace, v *filesView) {
	ui.Row(c).Shrink(0).Padding(8, 12, 4, 12).Children(func() {
		// 撑满栏宽：分段平分宽度，带计数的"改动"长一些的才放得下
		ui.Column(c).Grow(1).Children(func() {
			badges := []int{0, 0, v.git.changeCount()}
			if segmentedBadges(c, &v.side, L(c).FilesPanelTitle(), badges, L(c).FilesPanelFilesTab(), L(c).FilesPanelSearchTab(), L(c).FilesPanelChangesTab()) && v.side == sideSearch {
				v.search.focus = true
			}
		})
	})
	switch v.side {
	case sideFiles:
		a.treeView(c, w, v)
	case sideSearch:
		a.searchView(c, w, v)
	default:
		a.changesView(c, w, v)
	}
}

// filesPanelGrip 是文件栏左边缘调宽的把手，松手才写 ui.json。
func (a *app) filesPanelGrip(c *ui.Context) {
	grip := ui.Box(c.Key("files-panel-grip")).Absolute().Top(0).Bottom(0).Left(-3).Width(6).Cursor(ui.CursorResizeEW)
	if dx, _, held := grip.Dragged(); held {
		a.state.FilesPanelWidth = min(max(a.filesPanelWidth()-dx, filesPanelMin), filesPanelMax)
		a.resizingPanel = true
	} else if a.resizingPanel {
		a.resizingPanel = false
		a.saveState()
	}
	if grip.Hovered() || a.resizingPanel {
		accent := c.Theme().Accent
		grip.Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
		})
	}
}
