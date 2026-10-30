package app

import (
	"slices"
	"time"

	"github.com/egoist/mygo/ui"
)

// 终端分屏：一个工作区的主区域左右各放一个终端，两栏都是标签栏里已有的终端。
// 选中的终端（activeTerm）就是焦点栏里的那个，所以关闭标签、会话按钮这些照旧作用于它；
// 在标签栏选了不在分屏里的终端时，换掉焦点栏。两栏都接入，仍只接入看得见的终端。

// splitState 是一个工作区的分屏，存在 ui.json 的 splits 里。
type splitState struct {
	Left    string  `json:"left"`
	Right   string  `json:"right"`
	Percent float32 `json:"percent,omitempty"` // 左栏占的百分比，0 是一半
	Focus   int     `json:"focus,omitempty"`   // 焦点在哪栏，0 左 1 右
}

// 左栏的比例拖不出这个范围：再窄终端只剩几列。
const splitMinPercent, splitMaxPercent = 20, 80

func (s splitState) percent() float32 {
	if s.Percent == 0 {
		return 50
	}
	return min(max(s.Percent, splitMinPercent), splitMaxPercent)
}

// splitOf 是工作区开着的分屏。
func (a *app) splitOf(wsID string) (splitState, bool) {
	sp, ok := a.state.Splits[wsID]
	return sp, ok
}

func (a *app) setSplit(wsID string, sp splitState) {
	if a.state.Splits == nil {
		a.state.Splits = map[string]splitState{}
	}
	a.state.Splits[wsID] = sp
	a.saveState()
}

func (a *app) dropSplit(wsID string) {
	if _, ok := a.state.Splits[wsID]; !ok {
		return
	}
	delete(a.state.Splits, wsID)
	a.saveState()
}

// paneIDs 是主区域两栏该接入的终端，没分屏时右栏为空；顺带定下焦点栏（a.pane）和这一帧是不是分屏（a.split）。
// 分屏里的终端不在列表里了就按一栏显示；选中的终端不在分屏里时换掉焦点栏。
// 终端不在了不在这里删 ui.json 的记录：交接时可能有一次轮询的列表不全，删了就回不来；
// 用户在窗口里关掉时由 closeTerminal 删，别处关掉的留着也不显示。
func (a *app) paneIDs(w *workspace, cur *terminalInfo) [2]string {
	a.pane, a.split = 0, false
	if cur == nil {
		return [2]string{}
	}
	sp, ok := a.splitOf(w.ID)
	if !ok || !splitValid(w, sp) {
		return [2]string{cur.ID, ""}
	}
	next := sp
	switch cur.ID {
	case sp.Left:
		next.Focus = 0
	case sp.Right:
		next.Focus = 1
	default:
		if next.Focus == 1 {
			next.Right = cur.ID
		} else {
			next.Left = cur.ID
		}
	}
	if next != sp {
		a.setSplit(w.ID, next)
	}
	a.pane, a.split = next.Focus, true
	return [2]string{next.Left, next.Right}
}

// splitValid 报告分屏的两个终端都还在工作区里。
func splitValid(w *workspace, sp splitState) bool {
	has := func(id string) bool {
		return slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == id })
	}
	return sp.Left != sp.Right && has(sp.Left) && has(sp.Right)
}

// splitFocusTerm 是分屏焦点栏的终端：选中的终端还没定下来（刚打开窗口、第一次选这个工作区、
// 从文件标签回来时原来的终端没了）时选它，免得选成第一个终端、把它换进焦点栏，冲掉记下的分屏。
func (a *app) splitFocusTerm(w *workspace) string {
	sp, ok := a.splitOf(w.ID)
	if !ok || !splitValid(w, sp) {
		return ""
	}
	if sp.Focus == 1 {
		return sp.Right
	}
	return sp.Left
}

// splitShown 报告主区域这一帧是左右两栏。
func (a *app) splitShown() bool { return a.split }

// dropSplitOf 在用户关掉分屏里的终端时收起分屏。
func (a *app) dropSplitOf(id string) {
	for ws, sp := range a.state.Splits {
		if sp.Left == id || sp.Right == id {
			a.dropSplit(ws)
		}
	}
}

// openSplit 把 left、right 两个终端左右放，焦点给 focus 那栏。
func (a *app) openSplit(wsID, left, right string, focus int) {
	sp, _ := a.splitOf(wsID)
	sp.Left, sp.Right, sp.Focus = left, right, focus
	a.setSplit(wsID, sp)
	if focus == 1 {
		a.activeTerm[wsID] = right
	} else {
		a.activeTerm[wsID] = left
	}
	a.focusTerm = true
}

// splitRight 是「向右分屏」：当前终端放到右栏，左栏换成标签栏里它前面的那个（它在最前时是后面那个）。
// 工作区只有这一个终端时新开一个 shell 放在右栏。
func (a *app) splitRight(w *workspace, cur *terminalInfo) {
	if cur == nil {
		return
	}
	if sp, ok := a.splitOf(w.ID); ok && (sp.Left == cur.ID || sp.Right == cur.ID) && a.splitShown() {
		return
	}
	i := slices.IndexFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == cur.ID })
	switch {
	case i < 0:
	case len(w.Terminals) == 1:
		a.splitNew(w.ID, cur.ID)
	case i > 0:
		a.openSplit(w.ID, w.Terminals[i-1].ID, cur.ID, 1)
	default:
		a.openSplit(w.ID, w.Terminals[1].ID, cur.ID, 1)
	}
}

// splitNew 新开一个 shell，和 left 左右放。
func (a *app) splitNew(wsID, left string) {
	// 连按 ⌘D 时终端还没建好：只建一个
	if a.splitPending[wsID] {
		return
	}
	if a.splitPending == nil {
		a.splitPending = map[string]bool{}
	}
	a.splitPending[wsID] = true
	go func() {
		var t terminalInfo
		err := a.call("terminal.create", map[string]any{"workspace": wsID}, &t, 10*time.Second)
		// 先刷新让新终端进列表：列表里还没有它时 paneIDs 会当成关掉了、把分屏收起
		a.refresh()
		a.win.Update(func() {
			delete(a.splitPending, wsID)
			if err == nil {
				a.openSplit(wsID, left, t.ID, 1)
			}
		})
	}()
}

// closeSplit 是「关闭分屏」：留下焦点栏的终端，两个终端都不关。
func (a *app) closeSplit(wsID string) {
	a.dropSplit(wsID)
	a.focusTerm = true
}

// switchPane 是「切换栏」：焦点换到另一栏。
func (a *app) switchPane(wsID string) {
	other := a.panes[1-a.pane]
	if !a.splitShown() || other == nil {
		return
	}
	a.focusPane(wsID, 1-a.pane)
	a.focusTerm = true
}

// focusPane 把选中的终端换成第 i 栏的：点进另一栏、切换栏时。
func (a *app) focusPane(wsID string, i int) {
	v := a.panes[i]
	sp, ok := a.splitOf(wsID)
	if v == nil || !ok {
		return
	}
	a.pane = i
	a.activeTerm[wsID] = v.id
	if sp.Focus != i {
		sp.Focus = i
		a.setSplit(wsID, sp)
	}
}

// splitKey 处理终端里按的分屏快捷键（scopeTerminal：编辑器里 ⌘D、⌘] 另有用处）。
func (a *app) splitKey(v *termView, mods ui.Modifiers, key ui.Key) bool {
	w, _ := a.findTerminal(v.id)
	if w == nil {
		return false
	}
	// 刚点进另一栏、界面还没重建时，焦点栏还记着原来那栏：按键是哪栏收到的就以哪栏为准
	if i := slices.Index(a.panes[:], v); i >= 0 && i != a.pane && a.splitShown() {
		a.focusPane(w.ID, i)
	}
	switch {
	case a.keyIs("splitRight", mods, key):
		_, cur := a.findTerminal(v.id)
		a.splitRight(w, cur)
	case a.keyIs("closeSplit", mods, key):
		if !a.splitShown() {
			return false
		}
		a.closeSplit(w.ID)
	case a.keyIs("switchPane", mods, key):
		if !a.splitShown() {
			return false
		}
		a.switchPane(w.ID)
	default:
		return false
	}
	return true
}

// splitView 画左右两栏和中间的分栏线。拖分栏线时两栏先不变，只画一条跟着指针的线，
// 松手才改比例：终端宽度一变，程序就要按新列数重排输出，跟着每一帧变会重排几十次。
func (a *app) splitView(c *ui.Context, w *workspace) {
	sp, _ := a.splitOf(w.ID)
	pct := sp.percent()
	row := ui.Row(c.Key("split")).Grow(1).AlignItems(ui.Stretch)
	width := row.Bounds().W
	row.Children(func() {
		left := ui.Column(c.Key("pane-0")).BasisPercent(pct).Grow(0).Shrink(1)
		left.Children(func() { a.splitPane(c, w, 0) })
		vline(c).Children(func() {
			grip := ui.Box(c.Key("split-grip")).Absolute().Top(0).Bottom(0).Left(-3).Width(7).Cursor(ui.CursorResizeEW)
			if dx, _, held := grip.Dragged(); held {
				a.splitDrag += dx
				a.resizingSplit = "term-split"
			} else if a.resizingSplit == "term-split" {
				a.resizingSplit = ""
				if width > 1 {
					sp.Percent = min(max(pct+a.splitDrag/(width-1)*100, splitMinPercent), splitMaxPercent)
					a.setSplit(w.ID, sp)
				}
				a.splitDrag = 0
			}
			if grip.Hovered() || a.resizingSplit == "term-split" {
				accent := c.Theme().Accent
				grip.Draw(func(pt *ui.Painter, r ui.Rect) {
					pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
				})
			}
		})
		ui.Column(c.Key("pane-1")).Grow(1).Shrink(1).Basis(0).Children(func() { a.splitPane(c, w, 1) })
	})
	if a.resizingSplit == "term-split" && width > 1 {
		// 松手后的位置：限在和松手时一样的范围里。左栏的比例按去掉 1 像素分栏线后的宽度算
		at := min(max(pct+a.splitDrag/(width-1)*100, splitMinPercent), splitMaxPercent) / 100 * (width - 1)
		accent := c.Theme().Accent
		row.DrawOver(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(ui.Rect{X: r.X + at - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
		})
	}
}

// splitPane 画分屏的一栏：手机占着尺寸时的提示条和终端。
func (a *app) splitPane(c *ui.Context, w *workspace, i int) {
	v := a.panes[i]
	if v == nil {
		// 接入失败，下一帧再试；这一栏先空着，另一栏照常
		ui.Box(c.Key("pane-empty")).Grow(1).Background(c.Theme().Background)
		return
	}
	_, term := a.findTerminal(v.id)
	if term == nil {
		return
	}
	if term.PhoneSized {
		a.phoneSizedBar(c, term.ID)
	}
	// 点进另一栏（终端或它的查找栏刚拿到焦点）：选中的终端跟着换
	if a.termPane(c, i, term) && i != a.pane {
		a.focusPane(w.ID, i)
	}
}

// paneSide 是终端在分屏的哪一栏（0 左 1 右），不在分屏里或没分屏时为 -1。
func (a *app) paneSide(id string) int {
	if !a.splitShown() {
		return -1
	}
	for i, v := range a.panes {
		if v != nil && v.id == id {
			return i
		}
	}
	return -1
}

// openBeside 是标签右键「在右侧打开」：把这个终端放到右栏。没分屏时左栏是现在选中的终端，
// 选中的就是它自己时同「向右分屏」；已经分屏时换掉右栏。
func (a *app) openBeside(w *workspace, term terminalInfo) {
	if sp, ok := a.splitOf(w.ID); ok && a.splitShown() {
		a.openSplit(w.ID, sp.Left, term.ID, 1)
		return
	}
	cur := a.currentTerminal(w)
	if cur == nil || cur.ID == term.ID {
		a.splitRight(w, &term)
		return
	}
	a.openSplit(w.ID, cur.ID, term.ID, 1)
}
