package app

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/ui"
)

// 文件栏：主区域最右边的一栏，放文件树、检索、改动，照 Orca 的右侧栏。它停靠在那里、挤窄终端和编辑器，
// 不像"图片 / 技能与工具 / 历史"那样浮在上面、点别处就收起：边看文件边编辑时要一直开着。
// 开关按项目记在 ui.json（在 A 项目打开，切到 B 项目不跟着打开），宽度所有项目共用。
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

// filesPanelOpen 报告选中的工作区所在项目开着文件栏。
func (a *app) filesPanelOpen() bool { return a.state.FilesPanels[a.panelProject()] }

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
	a.setProjectPanel(&a.state.FilesPanels, w.ProjectID, true)
	return v
}

// revealInFiles 打开文件栏的文件树，展开到 path 并选中它（链接卡片的"在文件栏中显示"）。
func (a *app) revealInFiles(w *workspace, path string) {
	v := a.openFilesPanel(w, sideFiles)
	a.loadDirs(v, v.tree.Reveal(path))
	v.reveal, v.revealCur, v.revealScroll = path, v.cur, true
}

// toggleFilesPanel 开着且就在 side 上时关掉，否则打开到 side（⇧⌘E、⇧⌘G）。
func (a *app) toggleFilesPanel(w *workspace, side int) {
	if a.filesPanelOpen() && a.filesFor(w).side == side {
		a.closeFilesPanel()
		return
	}
	a.openFilesPanel(w, side)
}

func (a *app) closeFilesPanel() {
	a.setProjectPanel(&a.state.FilesPanels, a.panelProject(), false)
}

// filesShown 报告工作区的文件内容这一帧看不看得见：文件栏开着，或者选中了文件标签。
func (a *app) filesShown(w *workspace) bool {
	return w != nil && (a.filesPanelOpen() || a.filesActive(w))
}

// filesTick 是文件内容看得见时每帧的事：到点轮询磁盘和 git，清单过期就重列，当前文件还没读就读。
func (a *app) filesTick(c *ui.Context, w *workspace) {
	v := a.filesFor(w)
	if v.poll.due(c, filesPollEvery) {
		a.pollFiles(v)
	}
	if v.git.poll.due(c, gitPollEvery) {
		a.pollGit(v)
	}
	a.refreshList(v, false, nil)
	if d := v.cur; a.filesActive(w) && d != nil && d.kind == docLoading && !d.started {
		d.started = true
		a.loadDoc(d)
	}
}

// filesPanelButton 是开关文件栏的按钮：关着时在标签栏最右边，开着时在文件栏顶栏最右边，垫一层强调色淡底。
func (a *app) filesPanelButton(c *ui.Context, w *workspace) {
	b := iconButton(c.Key("files-panel"), iconPanelRight, "文件栏 ⌘L")
	if a.filesPanelOpen() {
		b.Background(colors(c.Theme()).tint)
	}
	if b.Clicked() {
		if a.filesPanelOpen() {
			a.closeFilesPanel()
		} else {
			a.openFilesPanel(w, a.filesFor(w).side)
		}
	}
}

// filesPanel 画文件栏：顶上和标签栏一样高，放分段和开关按钮；左边缘拖动调宽。
func (a *app) filesPanel(c *ui.Context, w *workspace) {
	p := colors(c.Theme())
	v := a.filesFor(w)
	col := ui.Column(c).Width(a.filesPanelWidth()).Shrink(0).Background(p.band).Ref(&a.filesPanelRef)
	col.Children(func() {
		// 右边距和标签栏一样，开关按钮打开前后不挪位置
		ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 14, 0, 10).Gap(6).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(p.line).DragWindow().Children(func() {
			changes := "改动"
			if n := v.git.changeCount(); n > 0 {
				changes = fmt.Sprintf("改动 %d", n)
			}
			// 撑满栏宽：分段平分宽度，"改动 12"这样长一些的才放得下
			ui.Column(c).Grow(1).Children(func() {
				if segmented(c, &v.side, "文件栏", "文件", "检索", changes) && v.side == sideSearch {
					v.search.focus = true
				}
			})
			// 开关按钮浮在这里（见 main），留出它的位置
			ui.Box(c).Width(28).Shrink(0)
		})
		switch v.side {
		case sideFiles:
			a.treeView(c, w, v)
		case sideSearch:
			a.searchView(c, w, v)
		default:
			a.changesView(c, w, v)
		}
		// 拖动左边缘调宽，松手才写 ui.json
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
	})
}
