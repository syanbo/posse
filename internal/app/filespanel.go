package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 文件栏：右侧面板之一（panelFiles），放文件树、检索、改动，照 Orca 的右侧栏。和其他右侧面板一样浮在终端上、
// 点外面收起：挤窄终端会让 agent 的 TUI 按新列数整屏重排。宽度可拖，记在 ui.json，所有工作区共用。
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

func (a *app) filesPanelOpen() bool { return a.state.RightPanel == panelFiles }

// openFilesPanel 打开文件栏并切到 side。
func (a *app) openFilesPanel(w *workspace, side int) *filesView {
	v := a.filesFor(w)
	v.side = side
	if !a.filesPanelOpen() {
		a.state.RightPanel = panelFiles
		a.saveState()
	}
	return v
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
	if a.filesPanelOpen() {
		a.state.RightPanel = ""
		a.saveState()
	}
}

// filesShown 报告工作区的文件内容这一帧看不看得见：文件栏开着，或者选中了文件标签。
func (a *app) filesShown(w *workspace) bool {
	return w != nil && (a.filesPanelOpen() || a.filesActive(w))
}

// dialogOpen 报告有没有对话框开着：从文件栏里弹出的确认、提交、未保存提示不浮在文件栏上，
// 在里面点按钮不能算点了文件栏外面。
func (a *app) dialogOpen() bool {
	return a.confirming || a.unsaved != nil || a.promptDlg != nil || a.push != nil || a.quick.open
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

// filesPanel 画文件栏：顶上是分段和关闭按钮，左边缘拖动调宽。
func (a *app) filesPanel(c *ui.Context, w *workspace) ui.Element {
	v := a.filesFor(w)
	panel := floating(c, ui.Column(c).Absolute().Top(8).Right(8).Bottom(8).Width(a.filesPanelWidth()))
	panel.Children(func() {
		ui.Row(c).Padding(10, 8, 6, 10).Gap(6).AlignItems(ui.Center).Children(func() {
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
			if iconButtonSized(c, iconClose, "关闭 ⌘L", 22, 12).Clicked() {
				a.closeFilesPanel()
			}
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
		grip := ui.Box(c.Key("files-panel-grip")).Absolute().Top(12).Bottom(12).Left(-3).Width(6).Cursor(ui.CursorResizeEW)
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
				pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 1)
			})
		}
	})
	return panel
}
