package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 文件栏：主区域最右边的一栏，放文件树、检索、改动，照 Orca 的右侧栏。它停靠在那里、挤窄终端和编辑器，
// 不像"图片 / 技能与工具 / 历史"那样浮在上面、点别处就收起：边看文件边编辑时要一直开着。
// 开关和宽度记在 ui.json，所有工作区共用。
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

func (a *app) filesPanelOpen() bool { return a.state.FilesPanel }

// openFilesPanel 打开文件栏并切到 side。
func (a *app) openFilesPanel(w *workspace, side int) *filesView {
	v := a.filesFor(w)
	v.side = side
	if !a.filesPanelOpen() {
		a.state.FilesPanel = true
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
		a.state.FilesPanel = false
		a.saveState()
	}
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
