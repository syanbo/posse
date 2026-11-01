package app

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

// 分栏：一个工作区的主区域左右分成几栏（最多 maxColumns），每栏有自己的标签栏，标签是终端或打开的文件。
// 标签的先后仍按工作区的终端顺序（termOrder）和打开的文件顺序，分栏只记每个标签在哪一栏，侧栏会话的顺序不受影响。
// 选中的标签（activeTerm，文件时加上 filesView.cur）就是焦点栏选中的那个：选中别的栏里的标签，焦点跟着过去；
// 选中还不在任何一栏的标签（新开的终端、新打开的文件）放进焦点栏。每栏只接入它选中的那个终端。

// maxColumns 是最多几栏：再多终端只剩几十列，agent 的界面就挤坏了。
const maxColumns = 3

// columnsState 是一个工作区的分栏，存在 ui.json 的 columns 里；只有一栏时没有记录。
type columnsState struct {
	Cols  []columnState `json:"cols"`
	Focus int           `json:"focus,omitempty"` // 焦点栏在 Cols 里的下标
}

// columnState 是一栏：里面的标签（终端 ID 或 docKey）、选中的那个和占的宽度比例（各栏相对，0 当 1）。
// 终端不在列表里了也不从 Tabs 删：常驻进程交接时可能有一次轮询的列表不全，删了就回不到原来那栏。
type columnState struct {
	Tabs   []string `json:"tabs"`
	Active string   `json:"active,omitempty"`
	Share  float32  `json:"share,omitempty"`
}

func (s columnsState) clone() columnsState {
	s.Cols = slices.Clone(s.Cols)
	for i := range s.Cols {
		s.Cols[i].Tabs = slices.Clone(s.Cols[i].Tabs)
	}
	return s
}

func (c columnState) weight() float32 {
	if c.Share <= 0 {
		return 1
	}
	return c.Share
}

// colView 是这一帧显示的一栏（空栏不显示）。
type colView struct {
	at     int      // 在 columnsState.Cols 里的下标；没分栏时为 -1
	tabs   []string // 这一栏的标签，按标签栏的顺序
	active string   // 选中的标签；文件标签里没有打开的文件时为 filesTabID，什么都没选时为空
	weight float32  // 占主区域宽度的比例，各栏加起来是 1
}

// 文件标签在分栏里的键：路径加前缀，和终端 ID（t_ 开头）、filesTabID 都不会冲突。
const docKeyPrefix = "doc:"

func docKey(d *fileDoc) string { return docKeyPrefix + d.path }

func isDocKey(k string) bool { return strings.HasPrefix(k, docKeyPrefix) }

// isTermKey 报告标签键是终端。
func isTermKey(k string) bool { return k != "" && k != filesTabID && !isDocKey(k) }

// tabKeys 是工作区所有的标签，按标签栏的顺序：先是终端，再是打开的文件。
func (a *app) tabKeys(w *workspace) []string {
	keys := make([]string, 0, len(w.Terminals))
	for _, t := range w.Terminals {
		keys = append(keys, t.ID)
	}
	if v := a.files[w.ID]; v != nil {
		for _, d := range v.docs {
			keys = append(keys, docKey(d))
		}
	}
	return keys
}

// docByKey 是标签键对应的打开的文件，没有时为 nil。
func (a *app) docByKey(w *workspace, key string) *fileDoc {
	v := a.files[w.ID]
	if v == nil || !isDocKey(key) {
		return nil
	}
	path := strings.TrimPrefix(key, docKeyPrefix)
	for _, d := range v.docs {
		if d.path == path {
			return d
		}
	}
	return nil
}

// currentKey 是选中的标签：选中的终端，或文件标签里当前的文件。选的已经不在了时为空。
func (a *app) currentKey(w *workspace) string {
	id := a.activeTerm[w.ID]
	if id == filesTabID {
		if v := a.files[w.ID]; v != nil && v.cur != nil && slices.Contains(v.docs, v.cur) {
			return docKey(v.cur)
		}
		return ""
	}
	if slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == id }) {
		return id
	}
	return ""
}

// selectKey 选中一个标签。focus 表示把键盘焦点也给它（点标签、切换栏），只是跟着焦点栏换时不给。
func (a *app) selectKey(w *workspace, key string, focus bool) {
	switch {
	case isDocKey(key):
		d := a.docByKey(w, key)
		if d == nil {
			return
		}
		v := a.showFiles(w)
		if v.cur != d {
			v.cur = d
			a.saveFilesState(v)
		}
		if focus {
			d.focus = true
		}
	case key == filesTabID:
		a.showFiles(w)
	case key != "":
		a.activeTerm[w.ID] = key
		if focus {
			a.focusTerm = true
		}
	}
}

// columnsOf 是工作区记下的分栏。
func (a *app) columnsOf(wsID string) (columnsState, bool) {
	st, ok := a.state.Columns[wsID]
	return st, ok
}

// setColumns 记下分栏，不到两栏就删掉记录（回到一栏）。
func (a *app) setColumns(wsID string, st columnsState) {
	if len(st.Cols) < 2 {
		a.dropColumns(wsID)
		return
	}
	st.Focus = min(max(st.Focus, 0), len(st.Cols)-1)
	if a.state.Columns == nil {
		a.state.Columns = map[string]columnsState{}
	}
	a.state.Columns[wsID] = st
	a.saveState()
}

func (a *app) dropColumns(wsID string) {
	if _, ok := a.state.Columns[wsID]; !ok {
		return
	}
	delete(a.state.Columns, wsID)
	a.saveState()
}

// syncColumns 定下这一帧显示哪几栏（a.cols）、焦点在哪栏（a.pane）：新标签放进焦点栏，
// 选中的标签在哪栏焦点就在哪栏，选中的不在了就换成焦点栏选中的那个。
func (a *app) syncColumns(w *workspace) {
	a.cols, a.pane, a.split = a.cols[:0], 0, false
	keys := a.tabKeys(w)
	single := func() {
		active := a.currentKey(w)
		if active == "" && a.filesActive(w) {
			active = filesTabID
		}
		a.cols = append(a.cols[:0], colView{at: -1, tabs: keys, active: active, weight: 1})
		a.pane = 0
	}
	saved, ok := a.columnsOf(w.ID)
	if !ok {
		single()
		return
	}
	st := saved.clone()
	where := map[string]int{}
	for i, col := range st.Cols {
		for _, k := range col.Tabs {
			where[k] = i
		}
	}
	st.Focus = min(max(st.Focus, 0), len(st.Cols)-1)
	changed := false
	cur := a.currentKey(w)
	if i, ok := where[cur]; ok && st.Focus != i {
		st.Focus, changed = i, true
	}
	for _, k := range keys {
		if _, ok := where[k]; !ok {
			st.Cols[st.Focus].Tabs = append(st.Cols[st.Focus].Tabs, k)
			where[k] = st.Focus
			changed = true
		}
	}
	if cur != "" && st.Cols[st.Focus].Active != cur {
		st.Cols[st.Focus].Active, changed = cur, true
	}
	if changed {
		a.setColumns(w.ID, st)
	}
	total, focus := float32(0), -1
	for i, col := range st.Cols {
		var tabs []string
		for _, k := range keys {
			if where[k] == i {
				tabs = append(tabs, k)
			}
		}
		if len(tabs) == 0 {
			continue
		}
		active := col.Active
		if !slices.Contains(tabs, active) {
			active = tabs[0]
		}
		if i == st.Focus {
			focus = len(a.cols)
		}
		a.cols = append(a.cols, colView{at: i, tabs: tabs, active: active, weight: col.weight()})
		total += col.weight()
	}
	if len(a.cols) == 0 {
		single()
		return
	}
	for i := range a.cols {
		a.cols[i].weight /= total
	}
	a.pane, a.split = max(focus, 0), len(a.cols) > 1
	// 选中的不在了（关掉、刚打开窗口）：换成焦点栏选中的那个
	if active := a.cols[a.pane].active; a.currentKey(w) != active {
		a.selectKey(w, active, false)
	}
}

// paneIDs 是每栏该接入的终端：选中的是终端的栏。
func (a *app) paneIDs(cur *terminalInfo) [maxColumns]string {
	var ids [maxColumns]string
	for i := range a.cols {
		col := &a.cols[i]
		// 没分栏时选中的终端可能刚由 currentTerminal 定下（还没选过、选的没了）
		if col.at < 0 && cur != nil && !isDocKey(col.active) && col.active != filesTabID {
			col.active = cur.ID
		}
		if isTermKey(col.active) && i < maxColumns {
			ids[i] = col.active
		}
	}
	return ids
}

// splitShown 报告主区域这一帧分成了几栏。
func (a *app) splitShown() bool { return a.split }

// colOf 是标签在这一帧的哪一栏（a.cols 的下标），不在时为 -1。
func (a *app) colOf(key string) int {
	for i, col := range a.cols {
		if slices.Contains(col.tabs, key) {
			return i
		}
	}
	return -1
}

// splitFocusTerm 是焦点栏选中的终端：选中的终端还没定下来（刚打开窗口、从文件标签回来时原来的终端没了）时选它，
// 免得选成第一个终端、把焦点换到它那栏。
func (a *app) splitFocusTerm(w *workspace) string {
	st, ok := a.columnsOf(w.ID)
	if !ok || st.Focus >= len(st.Cols) {
		return ""
	}
	id := st.Cols[st.Focus].Active
	if isTermKey(id) && slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == id }) {
		return id
	}
	return ""
}

// removeKey 把标签从它所在的栏拿掉：它是那栏选中的就换成同一栏里挨着的（先右后左），栏空了就收起，
// 宽度给左边的栏（最左的给右边）。返回拿掉前它在哪栏，不在任何一栏时为 -1。
func (a *app) removeKey(w *workspace, st *columnsState, key string) int {
	from := -1
	for i := range st.Cols {
		if j := slices.Index(st.Cols[i].Tabs, key); j >= 0 {
			st.Cols[i].Tabs = slices.Delete(st.Cols[i].Tabs, j, j+1)
			from = i
		}
	}
	if from < 0 {
		return -1
	}
	// 同一栏里还在的标签，按标签栏的顺序
	var rest []string
	at := -1
	for _, k := range a.tabKeys(w) {
		if k == key {
			at = len(rest)
		} else if slices.Contains(st.Cols[from].Tabs, k) {
			rest = append(rest, k)
		}
	}
	col := &st.Cols[from]
	if col.Active == key || !slices.Contains(rest, col.Active) {
		col.Active = ""
		if len(rest) > 0 {
			col.Active = rest[min(max(at, 0), len(rest)-1)]
		}
	}
	return from
}

// pruneColumns 收起没有标签的栏（不算不在列表里的终端），宽度给左边的栏（最左的给右边），焦点跟着挪。
func (a *app) pruneColumns(w *workspace, st *columnsState) {
	present := map[string]bool{}
	for _, k := range a.tabKeys(w) {
		present[k] = true
	}
	for i := 0; i < len(st.Cols); {
		if slices.ContainsFunc(st.Cols[i].Tabs, func(k string) bool { return present[k] }) || len(st.Cols) == 1 {
			i++
			continue
		}
		if to := max(i-1, 0); to != i || i+1 < len(st.Cols) {
			if i == 0 {
				to = 1
			}
			st.Cols[to].Share = st.Cols[to].weight() + st.Cols[i].weight()
		}
		st.Cols = slices.Delete(st.Cols, i, i+1)
		if st.Focus > i || st.Focus == i && i > 0 {
			st.Focus--
		}
	}
}

// moveTab 把标签移到另一栏：side 为 0 时移进第 target 栏（a.cols 的下标），-1、1 时在它左边、右边新开一栏放它。
// 原来那栏空了就收起；标签选中、焦点给它。
func (a *app) moveTab(w *workspace, key string, target, side int) {
	if target < 0 || target >= len(a.cols) {
		return
	}
	saved, ok := a.columnsOf(w.ID)
	var st columnsState
	at := a.cols[target].at
	if ok && at >= 0 {
		st = saved.clone()
	} else {
		if side == 0 {
			return
		}
		// 还没分栏：所有标签都在一栏
		st = columnsState{Cols: []columnState{{Tabs: a.tabKeys(w), Active: a.currentKey(w)}}}
		at = 0
	}
	a.removeKey(w, &st, key)
	dest := at
	if side != 0 {
		half := st.Cols[at].weight() / 2
		st.Cols[at].Share = half
		if side > 0 {
			dest = at + 1
		}
		st.Cols = slices.Insert(st.Cols, dest, columnState{Share: half})
	}
	st.Cols[dest].Tabs = append(st.Cols[dest].Tabs, key)
	st.Cols[dest].Active = key
	st.Focus = dest
	a.pruneColumns(w, &st)
	a.setColumns(w.ID, st)
	a.selectKey(w, key, true)
}

// canSplitWith 报告把 key 拖到第 i 栏的边上能不能新开一栏：不超过 maxColumns（它原来那栏会空掉、收起的不算），
// 它是第 i 栏唯一的标签时不行（拿出来再放回去还是这样）。
func (a *app) canSplitWith(key string, i int) bool {
	from := a.colOf(key)
	if from == i && len(a.cols[i].tabs) == 1 {
		return false
	}
	emptied := from >= 0 && from != i && len(a.cols[from].tabs) == 1
	return len(a.cols) < maxColumns || emptied
}

// dropColumnKey 在用户关掉标签时把它从分栏里拿掉：焦点栏选中的是它就换成同一栏挨着的，那栏空了焦点给旁边的栏。
func (a *app) dropColumnKey(w *workspace, key string) {
	saved, ok := a.columnsOf(w.ID)
	if !ok {
		return
	}
	st := saved.clone()
	from := a.removeKey(w, &st, key)
	if from < 0 {
		return
	}
	focused := st.Focus == from
	a.pruneColumns(w, &st)
	next := ""
	if st.Focus < len(st.Cols) {
		next = st.Cols[st.Focus].Active
	}
	a.setColumns(w.ID, st)
	if focused || a.currentKey(w) == key {
		a.selectKey(w, next, true)
	}
}

// focusColumn 把焦点换到第 i 栏：选中那栏选中的标签。focus 表示把键盘焦点也给它（切换栏的快捷键），
// 点进那栏时已经有了。
func (a *app) focusColumn(w *workspace, i int, focus bool) {
	if i < 0 || i >= len(a.cols) {
		return
	}
	col := a.cols[i]
	a.pane = i
	if st, ok := a.columnsOf(w.ID); ok && col.at >= 0 && st.Focus != col.at {
		st.Focus = col.at
		a.setColumns(w.ID, st)
	}
	a.selectKey(w, col.active, focus)
}

// splitRight 是「向右分栏」：在焦点栏右边新开一栏，放一个新 shell。
func (a *app) splitRight(w *workspace) {
	if len(a.cols) >= maxColumns {
		a.warn("最多分成三栏", "把标签拖进已有的栏，或先合并")
		return
	}
	// 连按 ⌘D 时终端还没建好：只建一个
	if a.splitPending[w.ID] {
		return
	}
	if a.splitPending == nil {
		a.splitPending = map[string]bool{}
	}
	a.splitPending[w.ID] = true
	wsID := w.ID
	go func() {
		var t terminalInfo
		err := a.call("terminal.create", map[string]any{"workspace": wsID}, &t, 10*time.Second)
		// 先刷新让新终端进列表：列表里还没有它时 moveTab 找不到它所在的栏
		a.refresh()
		a.win.Update(func() {
			delete(a.splitPending, wsID)
			w := a.workspace(wsID)
			if err != nil || w == nil || a.selected != wsID {
				return
			}
			// 新终端这时可能已经被放进焦点栏（新标签都放进焦点栏），moveTab 把它拿出来
			a.syncColumns(w)
			a.moveTab(w, t.ID, a.pane, 1)
		})
	}()
}

// mergeColumns 是「合并所有栏」：所有标签合回一栏，一个都不关，留下焦点栏选中的那个。
func (a *app) mergeColumns(w *workspace) {
	a.dropColumns(w.ID)
	a.focusTerm = true
}

// switchColumn 是「切换栏」：焦点换到下一栏（dir 为 1）或上一栏，到头了绕回来。
func (a *app) switchColumn(w *workspace, dir int) {
	if !a.splitShown() {
		return
	}
	a.focusColumn(w, (a.pane+dir+len(a.cols))%len(a.cols), true)
}

// splitKey 处理终端里按的分栏快捷键（scopeTerminal：编辑器里 ⌘D、⌘] 另有用处）。
func (a *app) splitKey(v *termView, mods ui.Modifiers, key ui.Key) bool {
	w, _ := a.findTerminal(v.id)
	if w == nil {
		return false
	}
	// 刚点进另一栏、界面还没重建时，焦点栏还记着原来那栏：按键是哪栏收到的就以哪栏为准。
	// 界面正要把焦点移到焦点栏（刚切换栏）时不算：这时按键还落在原来那栏
	if i := slices.Index(a.panes[:], v); i >= 0 && i != a.pane && a.splitShown() && !a.focusTerm {
		a.focusColumn(w, i, false)
	}
	switch {
	case a.keyIs("splitRight", mods, key):
		a.splitRight(w)
	case a.keyIs("closeSplit", mods, key):
		if !a.splitShown() {
			return false
		}
		a.mergeColumns(w)
	case a.keyIs("switchPane", mods, key):
		if !a.splitShown() {
			return false
		}
		dir := 1
		if key == ui.KeyBracketLeft {
			dir = -1
		}
		a.switchColumn(w, dir)
	default:
		return false
	}
	return true
}

// columnMenu 是标签右键菜单里分栏的几项。
func (a *app) columnMenu(m *popMenu, w *workspace, key string) {
	i := a.colOf(key)
	if i < 0 {
		return
	}
	if m.Item("移到右侧新栏").Disabled(!a.canSplitWith(key, i)).Chosen() {
		a.moveTab(w, key, i, 1)
	}
	if a.splitShown() {
		if i > 0 && m.Item("移到左边的栏").Chosen() {
			a.moveTab(w, key, i-1, 0)
		}
		if i < len(a.cols)-1 && m.Item("移到右边的栏").Chosen() {
			a.moveTab(w, key, i+1, 0)
		}
		if m.Item("合并所有栏").Keys(a.keyText("closeSplit")).Chosen() {
			a.mergeColumns(w)
		}
	}
}

// colBox 是一栏的容器：标签栏那一行和下面的内容各用一个，宽度按同样的比例分，上下对得齐。
// 最后一栏占剩下的宽度，分栏线的 1 像素也算在里面。
func (a *app) colBox(c *ui.Context, key string, i int, row bool) ui.Element {
	var e ui.Element
	if row {
		e = ui.Row(c.Key(key))
	} else {
		e = ui.Column(c.Key(key))
	}
	if i < len(a.cols)-1 {
		e.BasisPercent(a.cols[i].weight * 100).Grow(0).Shrink(1)
	} else {
		e.Grow(1).Shrink(1).Basis(0)
	}
	return e.MinWidth(0)
}

// 一栏最窄多宽：拖分栏线拖不过它。
func colMinWidth() float32 { return sz(240) }

// dropZone 是把标签拖到一栏内容上的落点。
type dropZone uint8

const (
	zoneNone   dropZone = iota
	zoneLeft            // 在这栏左边新开一栏
	zoneRight           // 在这栏右边新开一栏
	zoneCenter          // 移进这一栏
)

// zoneAt 按指针在第 i 栏内容上的位置算落点：靠左右边缘（栏宽的四分之一，最多 140）是新开一栏，中间是移进这栏。
func (a *app) zoneAt(e ui.Element, i int, key string) dropZone {
	x, _, in := e.PointerPosition()
	if !in {
		return zoneNone
	}
	width := e.Bounds().W
	edge := min(width/4, 140)
	split := a.canSplitWith(key, i)
	switch {
	case x < edge && split:
		return zoneLeft
	case x > width-edge && split:
		return zoneRight
	case a.colOf(key) != i && a.cols[i].at >= 0:
		return zoneCenter
	}
	return zoneNone
}

// columnDrop 让一栏的内容接住拖来的标签：拖着时盖一层强调色的淡底标出落点（新开一栏时是那半边），松手移过去。
func (a *app) columnDrop(c *ui.Context, w *workspace, e ui.Element, i int) {
	if g, over := ui.DragOver[tabDrag](e); over && g.ws == w.ID {
		zone := a.zoneAt(e, i, g.key)
		if zone != zoneNone {
			accent := c.Theme().Accent
			fill := soft(c.Theme(), accent)
			e.DrawOver(func(p *ui.Painter, r ui.Rect) {
				switch zone {
				case zoneLeft:
					r.W /= 2
				case zoneRight:
					r.X += r.W / 2
					r.W /= 2
				}
				r = ui.Rect{X: r.X + 4, Y: r.Y + 4, W: r.W - 8, H: r.H - 8}
				p.Fill(r, fill, rCard)
				p.Stroke(r, accent, rCard, 1)
			})
		}
	}
	if g, ok := ui.Drop[tabDrag](e); ok && g.ws == w.ID {
		switch a.zoneAt(e, i, g.key) {
		case zoneLeft:
			a.moveTab(w, g.key, i, -1)
		case zoneRight:
			a.moveTab(w, g.key, i, 1)
		case zoneCenter:
			a.moveTab(w, g.key, i, 0)
		}
		c.AnimationFrame()
	}
}

// columnGrip 是第 i 栏和第 i+1 栏之间的分栏线。拖动时各栏先不变，只画一条跟着指针的线，
// 松手才改比例：终端宽度一变，程序就要按新列数重排输出，跟着每一帧变会重排几十次。
func (a *app) columnGrip(c *ui.Context, w *workspace, i int, width float32) {
	key := "col-split"
	vline(c).Children(func() {
		grip := ui.Box(c.Key("col-grip-" + strconv.Itoa(i))).Absolute().Top(0).Bottom(0).Left(-3).Width(7).Cursor(ui.CursorResizeEW)
		if dx, _, held := grip.Dragged(); held {
			a.splitDrag += dx
			a.resizingSplit, a.splitGrip = key, i
		} else if a.resizingSplit == key && a.splitGrip == i {
			a.resizingSplit = ""
			a.resizeColumns(w, i, a.splitDrag, width)
			a.splitDrag = 0
		}
		// 双击平分各栏
		if grip.DoubleClicked() {
			a.equalizeColumns(w)
		}
		if grip.Hovered() || a.resizingSplit == key && a.splitGrip == i {
			accent := c.Theme().Accent
			grip.Draw(func(pt *ui.Painter, r ui.Rect) {
				pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
			})
		}
	})
}

// gripShift 是把第 i 条分栏线拖动 dx 之后实际挪的距离：两边的栏都不窄过 colMinWidth。
func (a *app) gripShift(i int, dx, width float32) float32 {
	if i+1 >= len(a.cols) || width <= 0 {
		return 0
	}
	l, r := a.cols[i].weight*width, a.cols[i+1].weight*width
	lo, hi := -(l - min(colMinWidth(), l)), r-min(colMinWidth(), r)
	return min(max(dx, lo), hi)
}

// resizeColumns 在松手时按拖动的距离改第 i 栏和第 i+1 栏的比例。
func (a *app) resizeColumns(w *workspace, i int, dx, width float32) {
	st, ok := a.columnsOf(w.ID)
	if !ok || i+1 >= len(a.cols) || width <= 0 {
		return
	}
	d := a.gripShift(i, dx, width) / width
	st = st.clone()
	// 只记显示的几栏：空着没显示的栏不占宽度
	for j, col := range a.cols {
		wt := col.weight
		switch j {
		case i:
			wt += d
		case i + 1:
			wt -= d
		}
		st.Cols[col.at].Share = wt
	}
	a.setColumns(w.ID, st)
}

// equalizeColumns 把显示的几栏平分宽度。
func (a *app) equalizeColumns(w *workspace) {
	st, ok := a.columnsOf(w.ID)
	if !ok {
		return
	}
	st = st.clone()
	for _, col := range a.cols {
		st.Cols[col.at].Share = 1
	}
	a.setColumns(w.ID, st)
}

// columnsView 画主区域的几栏内容和中间的分栏线；拖分栏线时画一条松手后的位置线。
func (a *app) columnsView(c *ui.Context, w *workspace) {
	row := ui.Row(c.Key("cols")).Grow(1).AlignItems(ui.Stretch)
	width := row.Bounds().W
	// 分栏线各占 1 像素，算比例时去掉
	inner := width - float32(len(a.cols)-1)
	row.Children(func() {
		for i := range a.cols {
			if i > 0 {
				a.columnGrip(c, w, i-1, inner)
			}
			box := a.colBox(c, "col-"+strconv.Itoa(i), i, false)
			box.Children(func() { a.columnBody(c, w, i) })
			a.columnDrop(c, w, box, i)
			a.columnFocus(w, box, i)
		}
	})
	if a.resizingSplit == "col-split" && a.splitGrip+1 < len(a.cols) && inner > 0 {
		x := float32(0)
		for j := 0; j <= a.splitGrip; j++ {
			x += a.cols[j].weight * inner
		}
		x += float32(a.splitGrip) + a.gripShift(a.splitGrip, a.splitDrag, inner)
		accent := c.Theme().Accent
		row.DrawOver(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(ui.Rect{X: r.X + x - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
		})
	}
}

// columnFocus 在键盘焦点进到第 i 栏时（点进它的终端、编辑器）把焦点栏换过去。只看焦点刚进来的那一帧：
// 界面自己移焦点（切换栏、点了别栏的标签）时，焦点还要一帧才离开原来那栏，不能当成用户点了那栏。
func (a *app) columnFocus(w *workspace, e ui.Element, i int) {
	if i >= maxColumns {
		return
	}
	now := e.FocusWithin()
	if now && !a.colFocused[i] && i != a.pane && a.splitShown() {
		a.focusColumn(w, i, false)
	}
	a.colFocused[i] = now
}

// columnBody 画第 i 栏的内容：选中的终端、文件，或空工作区。
func (a *app) columnBody(c *ui.Context, w *workspace, i int) {
	col := a.cols[i]
	switch {
	case col.active == filesTabID:
		a.filesEmpty(c)
	case isDocKey(col.active):
		v := a.filesFor(w)
		d := a.docByKey(w, col.active)
		if d == nil {
			a.filesEmpty(c)
			return
		}
		if d.kind == docLoading && !d.started {
			d.started = true
			a.loadDoc(d)
		}
		ui.Column(c.Key(col.active)).Grow(1).Shrink(1).MinHeight(0).Background(c.Theme().Background).Children(func() {
			a.docArea(c, w, v, d)
		})
	default:
		_, term := a.findTerminal(col.active)
		if term == nil || i >= maxColumns || a.panes[i] == nil {
			if !a.splitShown() {
				a.emptyWorkspace(c, w)
				return
			}
			// 接入失败，下一帧再试；这一栏先空着，别的栏照常
			ui.Box(c.Key("pane-empty")).Grow(1).Background(c.Theme().Background)
			return
		}
		if term.PhoneSized {
			a.phoneSizedBar(c, term.ID)
		}
		a.termPane(c, i, term)
	}
}

// columnStrip 是第 i 栏的标签栏：这栏的标签、新建终端的"+"，最后一栏右边还有分支、面板这些按钮。
// 标签一段按内容宽度，放不下时收窄、左右滚动（切到哪个就滚到哪个）；"+"在滚动区外面，没放满时跟在最后一个标签后面，
// 放满了停在右边。滚动区比标签栏高出一截、被标签栏裁掉：mygo 的滚动条压在标签下半截上，照浏览器的标签栏不显示，
// 触控板和滚轮照常能滚。
func (a *app) columnStrip(c *ui.Context, w *workspace, i int) {
	col := a.cols[i]
	last := i == len(a.cols)-1
	left, right := float32(8), float32(0)
	if i == 0 {
		left += a.barInset(c)
	}
	if last {
		right = iconBarPad()
	}
	seg := a.colBox(c, "strip-"+strconv.Itoa(i), i, true)
	seg.Padding(0, right, 0, left).Gap(2).AlignItems(ui.Center).Clip()
	seg.Children(func() {
		strip := ui.ScrollHorizontal(c.Key("tabs")).Shrink(1).MinWidth(0).
			Height(titleHeight(c) + 24).AlignSelf(ui.Start).AlignItems(ui.Start)
		// 文件栏滑进来、窗口变窄之后，当前标签可能被挤到外面：宽度变了就再滚一次
		if sw := strip.Bounds().W; i < maxColumns && sw != a.tabStripW[i] {
			a.tabStripW[i], a.shownTab[i] = sw, ""
		}
		focused := i == a.pane
		strip.Children(func() {
			ui.Row(c).Height(titleHeight(c)).Gap(2).AlignItems(ui.Center).Children(func() {
				// 拖动排序会在这次遍历里重排终端和文件：遍历副本
				for _, key := range slices.Clone(col.tabs) {
					active := key == col.active
					if isDocKey(key) {
						if d := a.docByKey(w, key); d != nil {
							a.docTab(c, w, a.filesFor(w), d, a.docDetail(w, d), i, active, focused)
						}
						continue
					}
					if _, term := a.findTerminal(key); term != nil {
						a.tab(c, w, *term, i, active, focused)
					}
				}
			})
		})
		ui.Row(c).Shrink(0).Height(titleHeight(c)).AlignItems(ui.Center).Children(func() { a.newTerminalButton(c, w, i) })
		// 标签后面的空处也接住拖来的标签：移进这一栏
		tail := ui.Box(c.Key("strip-tail")).Grow(1).AlignSelf(ui.Stretch)
		if g, over := ui.DragOver[tabDrag](tail); over && g.ws == w.ID && a.colOf(g.key) != i && col.at >= 0 {
			accent := c.Theme().Accent
			tail.DrawOver(func(p *ui.Painter, r ui.Rect) {
				p.Fill(ui.Rect{X: r.X, Y: r.Y + 8, W: 2, H: r.H - 16}, accent, 1)
			})
		}
		if g, ok := ui.Drop[tabDrag](tail); ok && g.ws == w.ID && a.colOf(g.key) != i {
			a.moveTab(w, g.key, i, 0)
			c.AnimationFrame()
		}
		if last {
			ui.Row(c).Shrink(0).Gap(2).AlignItems(ui.Center).Children(func() { a.tabBarButtons(c, w) })
		}
	})
}

// tabMark 在分栏时给焦点栏选中的标签底下画一道强调色的短线，看得出键盘在哪栏。
func tabMark(c *ui.Context, e ui.Element) {
	accent := c.Theme().Accent
	e.DrawOver(func(p *ui.Painter, r ui.Rect) {
		p.Fill(ui.Rect{X: r.X + 8, Y: r.Y + r.H - 2, W: r.W - 16, H: 2}, accent, 1)
	})
}
