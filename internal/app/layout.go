package app

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/locales"
)

// 分栏：一个工作区的主区域可以左右、上下任意分成几格（最多 maxColumns），是一棵分割树；同方向连着分的摊平成一层。
// 每格有自己的标签栏，标签是终端或打开的文件，每个标签只在一格里。标签的先后仍按工作区的终端顺序（termOrder）
// 和打开的文件顺序，分栏只记每个标签在哪一格，侧栏会话的顺序不受影响。
// 选中的标签（activeTerm，文件时加上 filesView.cur）就是焦点格选中的那个：选中别格里的标签，焦点跟着过去；
// 选中还不在任何一格的标签（新开的终端、新打开的文件）放进焦点格。每格只接入它选中的那个终端。

// maxColumns 是最多几格：再多每格只剩几十列、十几行，agent 的界面就挤坏了。
const maxColumns = 4

// paneNode 是分割树的一个节点：Split 为空时是一格（Tabs、Active、ID），否则按 Split 把 Kids 左右（h）或上下（v）排开。
// 终端不在列表里了也不从 Tabs 删：常驻进程交接时可能有一次轮询的列表不全，删了就回不到原来那格。
type paneNode struct {
	ID     string     `json:"id,omitempty"`
	Split  string     `json:"split,omitempty"`
	Kids   []paneNode `json:"kids,omitempty"`
	Share  float32    `json:"share,omitempty"` // 在上一层里占的比例（同层相对，0 当 1）
	Tabs   []string   `json:"tabs,omitempty"`
	Active string     `json:"active,omitempty"`
}

// layoutState 是一个工作区的分栏，存在 ui.json 的 panes 里；只有一格时没有记录。
type layoutState struct {
	Root  paneNode `json:"root"`
	Focus string   `json:"focus,omitempty"` // 焦点格的 ID
}

const (
	splitH = "h" // 左右排
	splitV = "v" // 上下排
)

func (n paneNode) weight() float32 {
	if n.Share <= 0 {
		return 1
	}
	return n.Share
}

func (n paneNode) clone() paneNode {
	n.Tabs = slices.Clone(n.Tabs)
	n.Kids = slices.Clone(n.Kids)
	for i := range n.Kids {
		n.Kids[i] = n.Kids[i].clone()
	}
	return n
}

func (s layoutState) clone() layoutState {
	s.Root = s.Root.clone()
	return s
}

// leaves 按从左上到右下的顺序列出各格。
func (n *paneNode) leaves(out []*paneNode) []*paneNode {
	if n.Split == "" {
		return append(out, n)
	}
	for i := range n.Kids {
		out = n.Kids[i].leaves(out)
	}
	return out
}

func (s *layoutState) leaf(id string) *paneNode {
	for _, l := range s.Root.leaves(nil) {
		if l.ID == id {
			return l
		}
	}
	return nil
}

// newID 是一格没用过的编号。
func (s *layoutState) newID() string {
	n := 0
	for _, l := range s.Root.leaves(nil) {
		if v, err := strconv.Atoi(strings.TrimPrefix(l.ID, "p")); err == nil && v > n {
			n = v
		}
	}
	return "p" + strconv.Itoa(n+1)
}

// colView 是这一帧显示的一格（空格不显示），按从左上到右下的顺序排在 a.cols 里。
type colView struct {
	id     string   // 格的 ID；没分栏时为空
	tabs   []string // 这一格的标签，按标签栏的顺序
	active string   // 选中的标签；文件标签里没有打开的文件时为 filesTabID，什么都没选时为空
	// 这一格贴着主区域的哪几边：贴顶的标签栏在标题栏的位置，贴右的盖着右侧面板
	top, left, right bool
}

// nodeView 是这一帧显示的分割树：空格去掉，只剩一个的层收掉。
type nodeView struct {
	cell   int // 是一格时它在 a.cols 里的下标，否则为 -1
	split  string
	kids   []*nodeView
	weight []float32 // 各子节点占的比例，加起来是 1
	path   []int     // 在 layoutState.Root 里的位置，改比例时用
	at     []int     // 各子节点在保存的 Kids 里的下标
	key    string    // 元素的键：取第一格的 ID，结构变了元素也认得出
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

// selectKey 选中一个标签。focus 表示把键盘焦点也给它（点标签、切换格），只是跟着焦点格换时不给。
func (a *app) selectKey(w *workspace, key string, focus bool) {
	// 键盘焦点只给看得见的工作区：在侧栏关掉别的工作区的会话时不把焦点拉到当前终端上
	focus = focus && w.ID == a.selected
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

// layoutOf 是工作区记下的分栏。
func (a *app) layoutOf(wsID string) (layoutState, bool) {
	st, ok := a.state.Panes[wsID]
	return st, ok
}

// setLayout 记下分栏，不到两格就删掉记录（回到一格）。焦点格不在了换成第一格。
func (a *app) setLayout(wsID string, st layoutState) {
	ls := st.Root.leaves(nil)
	if len(ls) < 2 {
		a.dropLayout(wsID)
		return
	}
	if st.leaf(st.Focus) == nil {
		st.Focus = ls[0].ID
	}
	if a.state.Panes == nil {
		a.state.Panes = map[string]layoutState{}
	}
	a.state.Panes[wsID] = st
	a.saveState()
}

func (a *app) dropLayout(wsID string) {
	if _, ok := a.state.Panes[wsID]; !ok {
		return
	}
	delete(a.state.Panes, wsID)
	a.saveState()
}

// paneGoneAfter 是分栏里的终端从列表里消失多久才拿掉：交接时终端会从列表里短暂消失，回来还要回到原来那格。
const paneGoneAfter = 30 * time.Second

// prunePanes 清理分栏记录：工作区删了的整份删掉；格里的终端不在列表里超过 paneGoneAfter 的拿掉
// （别处关掉、自己退出的终端），不然 ui.json 和每帧的查找随历史终端数一直增长。
func (a *app) prunePanes(now time.Time) {
	if len(a.state.Panes) == 0 {
		a.paneGone = nil
		a.paneDue.Store(false)
		return
	}
	seen := map[string]bool{}
	changed := false
	for wsID, saved := range a.state.Panes {
		w := a.workspace(wsID)
		if w == nil {
			delete(a.state.Panes, wsID)
			changed = true
			continue
		}
		st := saved.clone()
		drop := false
		for _, leaf := range st.Root.leaves(nil) {
			leaf.Tabs = slices.DeleteFunc(leaf.Tabs, func(k string) bool {
				if !isTermKey(k) || slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == k }) {
					return false
				}
				if a.paneGone == nil {
					a.paneGone = map[string]time.Time{}
				}
				at, ok := a.paneGone[k]
				if !ok {
					a.paneGone[k], seen[k] = now, true
					return false
				}
				if now.Sub(at) < paneGoneAfter {
					seen[k] = true
					return false
				}
				// 拿掉了就不用再记（下面按 seen 清掉）
				drop = true
				return true
			})
			if leaf.Active != "" && !slices.Contains(leaf.Tabs, leaf.Active) && len(leaf.Tabs) > 0 {
				leaf.Active = leaf.Tabs[0]
			}
		}
		if drop {
			prune(&st, func(l *paneNode) bool { return len(l.Tabs) > 0 })
			a.setLayout(wsID, st)
		}
	}
	for k := range a.paneGone {
		if !seen[k] {
			delete(a.paneGone, k)
		}
	}
	a.paneDue.Store(len(a.paneGone) > 0)
	if changed {
		a.saveState()
	}
}

// renameColumnKey 把格里的标签键 from 原地换成 to（预览标签被下一次单击换掉时），不挪格。
func (a *app) renameColumnKey(wsID, from, to string) {
	saved, ok := a.layoutOf(wsID)
	if !ok {
		return
	}
	st := saved.clone()
	for _, l := range st.Root.leaves(nil) {
		if j := slices.Index(l.Tabs, from); j >= 0 {
			l.Tabs[j] = to
			if l.Active == from {
				l.Active = to
			}
			a.setLayout(wsID, st)
			return
		}
	}
}

// prune 收起 keep 说不要的格，只剩一个子节点的层收掉（它占上一层的比例给剩下的那个），
// 同方向套着的层摊平。焦点格收起了，焦点给原来排在它前面的格（最前的给后面的）。
func prune(st *layoutState, keep func(*paneNode) bool) {
	order := st.Root.leaves(nil)
	ids := make([]string, len(order))
	for i, l := range order {
		ids[i] = l.ID
	}
	root, ok := pruneNode(st.Root, keep)
	if !ok {
		root = paneNode{ID: "p1"}
	}
	st.Root = root
	if st.leaf(st.Focus) != nil {
		return
	}
	at := slices.Index(ids, st.Focus)
	for d := 1; at >= 0 && d < len(ids); d++ {
		for _, j := range []int{at - d, at + d} {
			if j >= 0 && j < len(ids) && st.leaf(ids[j]) != nil {
				st.Focus = ids[j]
				return
			}
		}
	}
	if ls := st.Root.leaves(nil); len(ls) > 0 {
		st.Focus = ls[0].ID
	}
}

func pruneNode(n paneNode, keep func(*paneNode) bool) (paneNode, bool) {
	if n.Split == "" {
		return n, keep(&n)
	}
	var kids []paneNode
	for _, k := range n.Kids {
		k, ok := pruneNode(k, keep)
		if !ok {
			continue
		}
		// 同方向的子层摊平：它的子节点按它占的比例分到这一层
		if k.Split == n.Split {
			sum := float32(0)
			for _, g := range k.Kids {
				sum += g.weight()
			}
			for _, g := range k.Kids {
				g.Share = k.weight() * g.weight() / sum
				kids = append(kids, g)
			}
			continue
		}
		kids = append(kids, k)
	}
	switch len(kids) {
	case 0:
		return n, false
	case 1:
		kids[0].Share = n.Share
		return kids[0], true
	}
	n.Kids = kids
	return n, true
}

// syncColumns 定下这一帧显示哪几格（a.cols、a.tree）、焦点在哪格（a.pane）：新标签放进焦点格，
// 选中的标签在哪格焦点就在哪格，选中的不在了就换成焦点格选中的那个。
func (a *app) syncColumns(w *workspace) {
	a.cols, a.tree, a.pane, a.split, a.focusHidden = a.cols[:0], nil, 0, false, false
	saved, ok := a.layoutOf(w.ID)
	// 刚打开窗口、第一次选这个工作区时打开的文件还没恢复：先恢复，不然格里的文件当成不在了
	if ok && a.files[w.ID] == nil && slices.ContainsFunc(saved.Root.leaves(nil), func(l *paneNode) bool { return slices.ContainsFunc(l.Tabs, isDocKey) }) {
		a.filesFor(w)
	}
	keys := a.tabKeys(w)
	single := func() {
		active := a.currentKey(w)
		if active == "" && a.filesActive(w) {
			active = filesTabID
		}
		a.cols = append(a.cols[:0], colView{tabs: keys, active: active, top: true, left: true, right: true})
		a.tree = &nodeView{cell: 0}
		a.pane = 0
	}
	if !ok {
		single()
		return
	}
	st := saved.clone()
	changed := false
	// 关掉、被预览替换掉的文件不留在格里（打开的文件就在窗口进程里，不会像终端列表那样一时不全）；
	// 格里什么都不剩了就收起
	files := a.files[w.ID] != nil
	for _, l := range st.Root.leaves(nil) {
		n := len(l.Tabs)
		if files {
			l.Tabs = slices.DeleteFunc(l.Tabs, func(k string) bool { return isDocKey(k) && !slices.Contains(keys, k) })
		}
		changed = changed || len(l.Tabs) != n || len(l.Tabs) == 0
	}
	if changed {
		prune(&st, func(l *paneNode) bool { return len(l.Tabs) > 0 })
	}
	ls := st.Root.leaves(nil)
	if len(ls) < 2 {
		a.setLayout(w.ID, st)
		single()
		return
	}
	where := map[string]*paneNode{}
	for _, l := range ls {
		for _, k := range l.Tabs {
			where[k] = l
		}
	}
	focus := st.leaf(st.Focus)
	if focus == nil {
		focus, changed = ls[0], true
	}
	cur := a.currentKey(w)
	if l, ok := where[cur]; ok && l != focus {
		focus, changed = l, true
	}
	for _, k := range keys {
		if _, ok := where[k]; !ok {
			focus.Tabs = append(focus.Tabs, k)
			where[k] = focus
			changed = true
		}
	}
	if cur != "" && focus.Active != cur {
		focus.Active, changed = cur, true
	}
	if st.Focus != focus.ID {
		st.Focus, changed = focus.ID, true
	}
	if changed {
		a.setLayout(w.ID, st)
	}
	present := map[string]bool{}
	for _, k := range keys {
		present[k] = true
	}
	a.tree = a.buildView(&st.Root, nil, keys, present, true, true, true)
	if len(a.cols) == 0 {
		single()
		return
	}
	a.pane = slices.IndexFunc(a.cols, func(c colView) bool { return c.id == st.Focus })
	a.split = len(a.cols) > 1
	// 焦点格还在、只是它的终端一时不在列表里（交接）：选中和焦点都不动，画面上先把第一格当焦点格，
	// 免得焦点永久跳到别格
	if a.pane < 0 {
		a.pane, a.focusHidden = 0, true
		return
	}
	// 选中的不在了（关掉、刚打开窗口）：换成焦点格选中的那个
	if active := a.cols[a.pane].active; a.currentKey(w) != active {
		a.selectKey(w, active, false)
	}
}

// buildView 把保存的树 n 变成这一帧显示的树，顺带按顺序把各格放进 a.cols。没有一个标签还在的格不显示。
func (a *app) buildView(n *paneNode, path []int, keys []string, present map[string]bool, top, left, right bool) *nodeView {
	if n.Split == "" {
		var tabs []string
		for _, k := range keys {
			if slices.Contains(n.Tabs, k) {
				tabs = append(tabs, k)
			}
		}
		if len(tabs) == 0 {
			return nil
		}
		active := n.Active
		if !slices.Contains(tabs, active) {
			active = tabs[0]
		}
		a.cols = append(a.cols, colView{id: n.ID, tabs: tabs, active: active, top: top, left: left, right: right})
		return &nodeView{cell: len(a.cols) - 1, path: path, key: n.ID}
	}
	v := &nodeView{cell: -1, split: n.Split, path: path}
	// 先看哪些子节点还显示，才知道谁是第一个、最后一个
	var shown []int
	for i := range n.Kids {
		if hasPresent(&n.Kids[i], present) {
			shown = append(shown, i)
		}
	}
	total := float32(0)
	for j, i := range shown {
		first, last := j == 0, j == len(shown)-1
		kt, kl, kr := top, left, right
		if n.Split == splitH {
			kl, kr = left && first, right && last
		} else {
			kt = top && first
		}
		kid := a.buildView(&n.Kids[i], append(slices.Clone(path), i), keys, present, kt, kl, kr)
		if kid == nil {
			continue
		}
		v.kids = append(v.kids, kid)
		v.at = append(v.at, i)
		v.weight = append(v.weight, n.Kids[i].weight())
		total += n.Kids[i].weight()
	}
	switch len(v.kids) {
	case 0:
		return nil
	case 1:
		return v.kids[0]
	}
	for i := range v.weight {
		v.weight[i] /= total
	}
	v.key = v.kids[0].key
	return v
}

func hasPresent(n *paneNode, present map[string]bool) bool {
	for _, l := range n.leaves(nil) {
		if slices.ContainsFunc(l.Tabs, func(k string) bool { return present[k] }) {
			return true
		}
	}
	return false
}

// paneIDs 是每格该接入的终端：选中的是终端的格。
func (a *app) paneIDs(cur *terminalInfo) [maxColumns]string {
	var ids [maxColumns]string
	for i := range a.cols {
		col := &a.cols[i]
		// 没分栏时选中的终端可能刚由 currentTerminal 定下（还没选过、选的没了）
		if col.id == "" && cur != nil && !isDocKey(col.active) && col.active != filesTabID {
			col.active = cur.ID
		}
		if isTermKey(col.active) && i < maxColumns {
			ids[i] = col.active
		}
	}
	return ids
}

// splitShown 报告主区域这一帧分成了几格。
func (a *app) splitShown() bool { return a.split }

// colOf 是标签在这一帧的哪一格（a.cols 的下标），不在时为 -1。
func (a *app) colOf(key string) int {
	for i, col := range a.cols {
		if slices.Contains(col.tabs, key) {
			return i
		}
	}
	return -1
}

// splitFocusTerm 是焦点格选中的终端：选中的终端还没定下来（刚打开窗口、从文件标签回来时原来的终端没了）时选它，
// 免得选成第一个终端、把焦点换到它那格。
func (a *app) splitFocusTerm(w *workspace) string {
	st, ok := a.layoutOf(w.ID)
	if !ok {
		return ""
	}
	l := st.leaf(st.Focus)
	if l == nil {
		return ""
	}
	if id := l.Active; isTermKey(id) && slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == id }) {
		return id
	}
	return ""
}

// removeKey 把标签从它所在的格拿掉：它是那格选中的就换成同一格里挨着的（先后再前）。返回那一格，不在任何一格时为 nil。
func (a *app) removeKey(w *workspace, st *layoutState, key string) *paneNode {
	var from *paneNode
	for _, l := range st.Root.leaves(nil) {
		if j := slices.Index(l.Tabs, key); j >= 0 {
			l.Tabs = slices.Delete(l.Tabs, j, j+1)
			from = l
		}
	}
	if from == nil {
		return nil
	}
	// 同一格里还在的标签，按标签栏的顺序
	var rest []string
	at := -1
	for _, k := range a.tabKeys(w) {
		if k == key {
			at = len(rest)
		} else if slices.Contains(from.Tabs, k) {
			rest = append(rest, k)
		}
	}
	if from.Active == key || !slices.Contains(rest, from.Active) {
		from.Active = ""
		if len(rest) > 0 {
			from.Active = rest[min(max(at, 0), len(rest)-1)]
		}
	}
	return from
}

// presentKeep 是 prune 的条件：格里还有标签在（不算不在列表里的终端）。
func (a *app) presentKeep(w *workspace) func(*paneNode) bool {
	present := map[string]bool{}
	for _, k := range a.tabKeys(w) {
		present[k] = true
	}
	return func(l *paneNode) bool { return slices.ContainsFunc(l.Tabs, func(k string) bool { return present[k] }) }
}

// 标签放到一格上的位置：移进这一格，或在它的哪一边拆出新的一格。
type dropSide uint8

const (
	sideNone dropSide = iota
	sideIn
	sideLeft
	sideRight
	sideTop
	sideBottom
)

func (s dropSide) axis() string {
	if s == sideTop || s == sideBottom {
		return splitV
	}
	return splitH
}

func (s dropSide) before() bool { return s == sideLeft || s == sideTop }

// insertBeside 在格 id 的 side 那边放上 leaf：上一层就是这个方向时插在它旁边、平分它占的比例，否则把它换成两格的一层。
func insertBeside(n *paneNode, id string, side dropSide, leaf paneNode) bool {
	if n.Split == "" {
		if n.ID != id {
			return false
		}
		old := *n
		old.Share = 0
		leaf.Share = 0
		kids := []paneNode{old, leaf}
		if side.before() {
			kids[0], kids[1] = leaf, old
		}
		*n = paneNode{Split: side.axis(), Share: n.Share, Kids: kids}
		return true
	}
	for i := range n.Kids {
		k := &n.Kids[i]
		if k.Split == "" && k.ID == id && n.Split == side.axis() {
			half := k.weight() / 2
			k.Share, leaf.Share = half, half
			at := i + 1
			if side.before() {
				at = i
			}
			n.Kids = slices.Insert(n.Kids, at, leaf)
			return true
		}
		if insertBeside(k, id, side, leaf) {
			return true
		}
	}
	return false
}

// moveTab 把标签移到第 target 格（a.cols 的下标）：side 为 sideIn 时移进去，否则在它那一边拆出新的一格放它。
// 原来那格空了就收起；标签选中、焦点给它。
func (a *app) moveTab(w *workspace, key string, target int, side dropSide) {
	if target < 0 || target >= len(a.cols) || side == sideNone {
		return
	}
	saved, ok := a.layoutOf(w.ID)
	var st layoutState
	id := a.cols[target].id
	if ok && id != "" {
		st = saved.clone()
	} else {
		if side == sideIn {
			return
		}
		// 还没分栏：所有标签都在一格
		st = layoutState{Root: paneNode{ID: "p1", Tabs: a.tabKeys(w), Active: a.currentKey(w)}}
		id = "p1"
	}
	a.removeKey(w, &st, key)
	// 跨格移动的文件转成常驻：预览标签换格后还被下一次单击换掉，就像被关掉了
	if d := a.docByKey(w, key); d != nil {
		d.transient = false
	}
	dest := id
	if side == sideIn {
		l := st.leaf(id)
		l.Tabs = append(l.Tabs, key)
		l.Active = key
	} else {
		dest = st.newID()
		insertBeside(&st.Root, id, side, paneNode{ID: dest, Tabs: []string{key}, Active: key})
	}
	st.Focus = dest
	prune(&st, a.presentKeep(w))
	a.setLayout(w.ID, st)
	a.selectKey(w, key, true)
}

// 一格最窄多宽、最矮多高：拖分隔线拖不过它，拆不出比它小的格。
func colMinWidth() float32  { return sz(240) }
func rowMinHeight() float32 { return sz(140) }

// canSplitWith 报告把 key 拖到第 i 格 side 那一边能不能拆出新的一格：不超过 maxColumns（它原来那格会空掉、收起的不算），
// 拆开后两半都不小于最小尺寸；它是第 i 格唯一的标签时不行（拿出来再放回去还是这样）；
// 它所在的格正好就挨在第 i 格那一边、只有它一个标签时也不行（放下去还是原样，只改了比例）。
func (a *app) canSplitWith(key string, i int, side dropSide) bool {
	if i < 0 || i >= len(a.cols) {
		return false
	}
	from := a.colOf(key)
	alone := from >= 0 && len(a.cols[from].tabs) == 1
	if from == i && alone {
		return false
	}
	if len(a.cols) >= maxColumns && !(alone && from != i) {
		return false
	}
	// 拆完两半都不能太小
	if size := a.cellSize[i]; size != (ui.Point{}) {
		if side.axis() == splitH && size.X < 2*colMinWidth() || side.axis() == splitV && size.Y < 2*rowMinHeight() {
			return false
		}
	}
	if alone && from >= 0 {
		if st, ok := a.layoutOf(a.selected); ok && adjacent(&st.Root, a.cols[i].id, a.cols[from].id, side) {
			return false
		}
	}
	return true
}

// adjacent 报告格 other 是不是和格 id 同在一层、按 side 的方向紧挨在它那一边。
func adjacent(n *paneNode, id, other string, side dropSide) bool {
	for i := range n.Kids {
		k := &n.Kids[i]
		if k.Split == "" && k.ID == id && n.Split == side.axis() {
			j := i + 1
			if side.before() {
				j = i - 1
			}
			return j >= 0 && j < len(n.Kids) && n.Kids[j].Split == "" && n.Kids[j].ID == other
		}
		if adjacent(k, id, other, side) {
			return true
		}
	}
	return false
}

// dropColumnKey 在用户关掉标签时把它从分栏里拿掉：焦点格选中的是它就换成同一格挨着的，那格空了焦点给旁边的格。
func (a *app) dropColumnKey(w *workspace, key string) {
	saved, ok := a.layoutOf(w.ID)
	if !ok {
		return
	}
	st := saved.clone()
	from := a.removeKey(w, &st, key)
	if from == nil {
		return
	}
	focused := st.Focus == from.ID
	// 只收起这样空掉的格：别的格里一时不在列表里的终端（交接）还要回到原来那格
	prune(&st, func(l *paneNode) bool { return len(l.Tabs) > 0 })
	next := ""
	if l := st.leaf(st.Focus); l != nil {
		next = l.Active
	}
	a.setLayout(w.ID, st)
	if focused || a.currentKey(w) == key {
		a.selectKey(w, next, true)
	}
}

// focusColumn 把焦点换到第 i 格：选中那格选中的标签。focus 表示把键盘焦点也给它（切换格的快捷键），
// 点进那格时已经有了。
func (a *app) focusColumn(w *workspace, i int, focus bool) {
	if i < 0 || i >= len(a.cols) {
		return
	}
	col := a.cols[i]
	a.pane = i
	if st, ok := a.layoutOf(w.ID); ok && col.id != "" && st.Focus != col.id {
		st.Focus = col.id
		a.setLayout(w.ID, st)
	}
	a.selectKey(w, col.active, focus)
}

// splitNew 是「向右分栏」「向下分栏」：在焦点格那一边拆出一格，放一个新 shell。
func (a *app) splitNew(m locales.Messages, w *workspace, side dropSide) {
	if len(a.cols) >= maxColumns {
		a.warn(m.TabSplitMax(), m.TabSplitMaxBody())
		return
	}
	if size := a.cellSize[a.pane]; size != (ui.Point{}) && (side.axis() == splitH && size.X < 2*colMinWidth() || side.axis() == splitV && size.Y < 2*rowMinHeight()) {
		a.warn(m.TabSplitTooSmall(), "")
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
		// 先刷新让新终端进列表：列表里还没有它时 moveTab 找不到它所在的格
		a.refresh()
		a.win.Update(func() {
			delete(a.splitPending, wsID)
			w := a.workspace(wsID)
			if err != nil || w == nil || a.selected != wsID {
				return
			}
			// 新终端这时可能已经被放进焦点格（新标签都放进焦点格），moveTab 把它拿出来。
			// 等终端的时候可能已经拖满了四格：那就留在焦点格
			a.syncColumns(w)
			if !a.canSplitWith(t.ID, a.pane, side) {
				a.selectKey(w, t.ID, true)
				title := m.TabSplitTooSmall()
				if len(a.cols) >= maxColumns {
					title = m.TabSplitMax()
				}
				a.warn(title, m.TabSplitPlacedHere())
				return
			}
			a.moveTab(w, t.ID, a.pane, side)
		})
	}()
}

// mergeColumns 是「合并所有分栏」：所有标签合回一格，一个都不关，留下焦点格选中的那个。
func (a *app) mergeColumns(w *workspace) {
	a.dropLayout(w.ID)
	a.selectKey(w, a.currentKey(w), true)
}

// switchColumn 是「切换分栏」：焦点按从左上到右下的顺序换到下一格（dir 为 1）或上一格，到头了绕回来。
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
	// 刚点进另一格、界面还没重建时，焦点格还记着原来那格：按键是哪格收到的就以哪格为准。
	// 界面正要把焦点移到焦点格（刚切换格）时不算：这时按键还落在原来那格
	if !a.focusTerm {
		a.focusPaneOf(w, v)
	}
	switch {
	case a.keyIs("splitRight", mods, key):
		a.splitNew(appL(), w, sideRight)
	case a.keyIs("splitDown", mods, key):
		a.splitNew(appL(), w, sideBottom)
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

// focusPaneOf 让 v 所在的格成为焦点格（分栏时）。
func (a *app) focusPaneOf(w *workspace, v *termView) {
	if i := slices.Index(a.panes[:], v); i >= 0 && i != a.pane && a.splitShown() {
		a.focusColumn(w, i, false)
	}
}

// splitFrom 是终端右键菜单的分栏：从右键的那一格分出去，不管焦点在哪一格。
func (a *app) splitFrom(w *workspace, v *termView, side dropSide) {
	a.focusPaneOf(w, v)
	a.splitNew(appL(), w, side)
}

// columnMenu 是标签右键菜单里分栏的几项。
func (a *app) columnMenu(lm locales.Messages, m *popMenu, w *workspace, key string) {
	i := a.colOf(key)
	if i < 0 {
		return
	}
	if m.Item(lm.TabMoveRight()).Icon(iconSplitRight).Disabled(!a.canSplitWith(key, i, sideRight)).Chosen() {
		a.moveTab(w, key, i, sideRight)
	}
	if m.Item(lm.TabMoveDown()).Icon(iconSplitDown).Disabled(!a.canSplitWith(key, i, sideBottom)).Chosen() {
		a.moveTab(w, key, i, sideBottom)
	}
	if a.splitShown() && m.Item(lm.TabMergePanes()).Icon(iconPane).Keys(a.keyText("closeSplit")).Chosen() {
		a.mergeColumns(w)
	}
}

// zoneAt 按指针在第 i 格内容上的位置算落点：靠哪条边（那一边长度的四分之一，最多 140）就在那一边拆出新的一格，
// 几条边都够近时取最近的；中间是移进这格。
func (a *app) zoneAt(e ui.Element, i int, key string) dropSide {
	x, y, in := e.PointerPosition()
	if !in {
		return sideNone
	}
	b := e.Bounds()
	ex, ey := min(b.W/4, 140), min(b.H/4, 140)
	best, dist := sideNone, float32(0)
	for _, c := range []struct {
		side  dropSide
		d, at float32
	}{{sideLeft, x, ex}, {sideRight, b.W - x, ex}, {sideTop, y, ey}, {sideBottom, b.H - y, ey}} {
		if c.d < c.at && (best == sideNone || c.d/c.at < dist) && a.canSplitWith(key, i, c.side) {
			best, dist = c.side, c.d/c.at
		}
	}
	if best != sideNone {
		return best
	}
	if a.colOf(key) != i && a.cols[i].id != "" {
		return sideIn
	}
	return sideNone
}

// columnDrop 让一格的内容接住拖来的标签：拖着时盖一层强调色的淡底标出落点（拆出新的一格时是那半边），松手移过去。
func (a *app) columnDrop(c *ui.Context, w *workspace, e ui.Element, i int) {
	if g, over := ui.DragOver[tabDrag](e); over && g.ws == w.ID {
		if zone := a.zoneAt(e, i, g.key); zone != sideNone {
			accent := c.Theme().Accent
			fill := soft(c.Theme(), accent)
			e.DrawOver(func(p *ui.Painter, r ui.Rect) {
				switch zone {
				case sideLeft:
					r.W /= 2
				case sideRight:
					r.X += r.W / 2
					r.W /= 2
				case sideTop:
					r.H /= 2
				case sideBottom:
					r.Y += r.H / 2
					r.H /= 2
				}
				r = ui.Rect{X: r.X + 4, Y: r.Y + 4, W: r.W - 8, H: r.H - 8}
				p.Fill(r, fill, rCard)
				p.Stroke(r, accent, rCard, 1)
			})
		}
	}
	if g, ok := ui.Drop[tabDrag](e); ok && g.ws == w.ID {
		a.moveTab(w, g.key, i, a.zoneAt(e, i, g.key))
		c.AnimationFrame()
	}
}

// gripKey 是分割树里一条分隔线的键：它所在那层的位置和它左边（上边）子节点的序号。
func gripKey(n *nodeView, i int) string {
	var b strings.Builder
	b.WriteString("split")
	for _, p := range n.path {
		b.WriteString("/" + strconv.Itoa(p))
	}
	return b.String() + ":" + strconv.Itoa(i)
}

// gripShift 是把 n 层第 i 条分隔线拖动 d 之后实际挪的距离：两边都不小于最小尺寸。size 是这一层去掉分隔线后的长度。
func gripShift(n *nodeView, i int, d, size float32) float32 {
	if i+1 >= len(n.kids) || size <= 0 {
		return 0
	}
	least := colMinWidth()
	if n.split == splitV {
		least = rowMinHeight()
	}
	l, r := n.weight[i]*size, n.weight[i+1]*size
	lo, hi := -(l - min(least, l)), r-min(least, r)
	return min(max(d, lo), hi)
}

// layoutNode 找到保存的树里 path 那一层。
func layoutNode(st *layoutState, path []int) *paneNode {
	n := &st.Root
	for _, i := range path {
		if n.Split == "" || i >= len(n.Kids) {
			return nil
		}
		n = &n.Kids[i]
	}
	return n
}

// resizeSplit 在松手时按拖动的距离改 n 层第 i、i+1 个子节点的比例。只改显示着的子节点，按它们原来的比例总和换算：
// 没显示的格（终端一时不在列表里）回来时还是原来那么大。
func (a *app) resizeSplit(w *workspace, n *nodeView, i int, d, size float32) {
	st, ok := a.layoutOf(w.ID)
	if !ok || i+1 >= len(n.kids) || size <= 0 {
		return
	}
	st = st.clone()
	node := layoutNode(&st, n.path)
	if node == nil || node.Split == "" {
		return
	}
	shift := gripShift(n, i, d, size) / size
	sum := float32(0)
	for _, j := range n.at {
		sum += node.Kids[j].weight()
	}
	for k, j := range n.at {
		wt := n.weight[k]
		switch k {
		case i:
			wt += shift
		case i + 1:
			wt -= shift
		}
		node.Kids[j].Share = wt * sum
	}
	a.setLayout(w.ID, st)
}

// equalizeSplit 把 n 层显示着的子节点平分。
func (a *app) equalizeSplit(w *workspace, n *nodeView) {
	st, ok := a.layoutOf(w.ID)
	if !ok {
		return
	}
	st = st.clone()
	node := layoutNode(&st, n.path)
	if node == nil || node.Split == "" {
		return
	}
	sum := float32(0)
	for _, j := range n.at {
		sum += node.Kids[j].weight()
	}
	for _, j := range n.at {
		node.Kids[j].Share = sum / float32(len(n.at))
	}
	a.setLayout(w.ID, st)
}

// columnsView 画主区域的分割树：每格是标签栏和下面的内容。
func (a *app) columnsView(c *ui.Context, w *workspace) {
	// 各格的内容区，按下时看按在哪格
	var boxes []ui.Element
	ui.Column(c.Key("cols")).Grow(1).MinHeight(0).Children(func() { a.nodeView(c, w, a.tree, &boxes) })
	a.columnPressed(w, boxes)
}

// nodeView 画分割树的一层：子节点按比例左右或上下排开，中间是可以拖的分隔线。
// 拖分隔线时各格先不变，只画一条跟着指针的线，松手才改比例：终端大小一变，程序就要按新的行列重排输出，
// 跟着每一帧变会重排几十次。
func (a *app) nodeView(c *ui.Context, w *workspace, n *nodeView, boxes *[]ui.Element) {
	if n.cell >= 0 {
		a.cellView(c, w, n.cell, boxes)
		return
	}
	h := n.split == splitH
	var box ui.Element
	if h {
		box = ui.Row(c.Key("n-" + n.key))
	} else {
		box = ui.Column(c.Key("n-" + n.key))
	}
	box.Grow(1).MinWidth(0).MinHeight(0).AlignItems(ui.Stretch)
	b := box.Bounds()
	size := b.W
	if !h {
		size = b.H
	}
	// 分隔线各占 1 像素，算比例时去掉
	size -= float32(len(n.kids) - 1)
	box.Children(func() {
		for i, kid := range n.kids {
			if i > 0 {
				a.splitGrip(c, w, n, i-1, size)
			}
			var e ui.Element
			if h {
				e = ui.Row(c.Key("k-" + kid.key))
			} else {
				e = ui.Column(c.Key("k-" + kid.key))
			}
			if i < len(n.kids)-1 {
				e.BasisPercent(n.weight[i] * 100).Grow(0).Shrink(1)
			} else {
				e.Grow(1).Shrink(1).Basis(0)
			}
			e.MinWidth(0).MinHeight(0).AlignItems(ui.Stretch).Children(func() { a.nodeView(c, w, kid, boxes) })
		}
	})
	for i := range n.kids {
		if key := gripKey(n, i); a.resizingSplit == key && size > 0 {
			at := float32(i)
			for j := 0; j <= i; j++ {
				at += n.weight[j] * size
			}
			at += gripShift(n, i, a.splitDrag, size)
			accent := c.Theme().Accent
			box.DrawOver(func(pt *ui.Painter, r ui.Rect) {
				if h {
					pt.Fill(ui.Rect{X: r.X + at - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
				} else {
					pt.Fill(ui.Rect{X: r.X, Y: r.Y + at - 1, W: r.W, H: 2}, accent, 0)
				}
			})
		}
	}
}

// splitGrip 是 n 层第 i 和第 i+1 个子节点之间的分隔线，双击平分这一层。
func (a *app) splitGrip(c *ui.Context, w *workspace, n *nodeView, i int, size float32) {
	key := gripKey(n, i)
	h := n.split == splitH
	line := vline(c)
	if !h {
		line = hline(c)
	}
	line.Children(func() {
		grip := ui.Box(c.Key(key))
		if h {
			grip.Absolute().Top(0).Bottom(0).Left(-3).Width(7).Cursor(ui.CursorResizeEW)
		} else {
			grip.Absolute().Left(0).Right(0).Top(-3).Height(7).Cursor(ui.CursorResizeNS)
		}
		if dx, dy, held := grip.Dragged(); held {
			if h {
				a.splitDrag += dx
			} else {
				a.splitDrag += dy
			}
			a.resizingSplit = key
		} else if a.resizingSplit == key {
			a.resizingSplit = ""
			a.resizeSplit(w, n, i, a.splitDrag, size)
			a.splitDrag = 0
		}
		if grip.DoubleClicked() {
			a.equalizeSplit(w, n)
		}
		if a.resizingSplit == key {
			a.gripDrawn = true
		}
		if grip.Hovered() || a.resizingSplit == key {
			accent := c.Theme().Accent
			grip.Draw(func(pt *ui.Painter, r ui.Rect) {
				if h {
					pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
				} else {
					pt.Fill(ui.Rect{X: r.X, Y: r.Y + r.H/2 - 1, W: r.W, H: 2}, accent, 0)
				}
			})
		}
	})
}

// cellView 画一格：标签栏和下面的内容。内容接住拖来的标签，按下时换焦点格。
func (a *app) cellView(c *ui.Context, w *workspace, i int, boxes *[]ui.Element) {
	col := a.cols[i]
	ui.Column(c.Key("cell-" + col.id)).Grow(1).MinWidth(0).MinHeight(0).Children(func() {
		a.columnStrip(c, w, i)
		// 目录不在了：提示条在第一格的标签栏下面；空工作区页自己有这些按钮
		if w.Missing && i == 0 && (a.splitShown() || col.active == filesTabID || isDocKey(col.active) || a.panes[0] != nil) {
			a.missingBar(c, w)
		}
		body := ui.Column(c.Key("body")).Grow(1).MinHeight(0)
		if b := body.Bounds(); i < maxColumns {
			a.cellSize[i] = ui.Point{X: b.W, Y: b.H}
		}
		body.Children(func() { a.columnBody(c, w, i) })
		a.columnDrop(c, w, body, i)
		*boxes = append(*boxes, body)
	})
}

// columnPressed 在指针按在某一格的内容上时（点进它的终端、编辑器、预览）把焦点格换过去。
// 按着看在哪一格，不看键盘焦点：拖标签松手、终端自己拿焦点这些不是用户点了那格。
// 按在第 i 格时只有它不报"按在外面"，按在标签栏、侧栏时各格都报。
func (a *app) columnPressed(w *workspace, boxes []ui.Element) {
	if len(boxes) < 2 {
		return
	}
	in, outside := -1, 0
	for i, b := range boxes {
		if b.PressedOutside() {
			outside++
		} else {
			in = i
		}
	}
	if outside == len(boxes)-1 && in >= 0 && in != a.pane {
		a.focusColumn(w, in, false)
	}
}

// columnBody 画第 i 格的内容：选中的终端、文件，或空工作区。
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
		a.docFocusPane = i == a.pane
		// 能拿焦点：Markdown 预览、图片这些没有编辑器的，选中时（d.focus）也把键盘焦点从别格的终端拿过来
		box := ui.Column(c.Key(col.active)).Grow(1).Shrink(1).MinHeight(0).Background(c.Theme().Background).Focusable()
		box.Children(func() {
			a.docArea(c, w, v, d)
		})
		if d.focus && i == a.pane {
			d.focus = false
			box.Focus()
		}
	default:
		_, term := a.findTerminal(col.active)
		if term == nil || i >= maxColumns || a.panes[i] == nil {
			if !a.splitShown() {
				a.emptyWorkspace(c, w)
				return
			}
			// 接入失败，下一帧再试；这一格先空着，别的格照常
			ui.Box(c.Key("pane-empty")).Grow(1).Background(c.Theme().Background)
			return
		}
		if term.PhoneSized {
			a.phoneSizedBar(c, term.ID)
		}
		a.termPane(c, i, term)
	}
}

// columnStrip 是第 i 格的标签栏：这格的标签、新建终端的"+"；贴着顶边的在标题栏的位置（能拖窗口），
// 右上角那一格右边还有分支、面板这些按钮。
// 标签一段按内容宽度，放不下时收窄、左右滚动（切到哪个就滚到哪个）；"+"在滚动区外面，没放满时跟在最后一个标签后面，
// 放满了停在右边。滚动区比标签栏高出一截、被标签栏裁掉：mygo 的滚动条压在标签下半截上，照浏览器的标签栏不显示，
// 触控板和滚轮照常能滚。
func (a *app) columnStrip(c *ui.Context, w *workspace, i int) {
	p := colors(c.Theme())
	col := a.cols[i]
	corner := col.top && col.right
	left, right := float32(8), float32(4)
	if col.top && col.left {
		left += a.barInset(c)
	}
	if corner {
		right = iconBarPad()
	}
	seg := ui.Row(c.Key("strip")).Height(titleHeight(c)).Shrink(0).Padding(0, right, 0, left).Gap(2).AlignItems(ui.Center).
		Background(p.band).BorderWidth(0, 0, hairline, 0).BorderColor(p.line).Clip()
	if col.top {
		seg.DragWindow()
	}
	seg.Children(func() {
		strip := ui.ScrollHorizontal(c.Key("tabs")).Shrink(1).MinWidth(0).
			Height(titleHeight(c) + 24).AlignSelf(ui.Start).AlignItems(ui.Start)
		// 文件栏滑进来、窗口变窄之后，当前标签可能被挤到外面：宽度变了就再滚一次
		if sw := strip.Bounds().W; i < maxColumns && sw != a.tabStripW[i] {
			a.tabStripW[i], a.shownTab[i] = sw, ""
		}
		focused := i == a.pane
		terms := map[string]terminalInfo{}
		for _, t := range w.Terminals {
			terms[t.ID] = t
		}
		docs := map[string]*fileDoc{}
		names := map[string]int{} // 同名文件的个数：同名的标签后面带上级目录名
		if v := a.files[w.ID]; v != nil {
			for _, d := range v.docs {
				docs[docKey(d)] = d
				names[d.name(L(c))]++
			}
		}
		strip.Children(func() {
			ui.Row(c).Height(titleHeight(c)).Gap(2).AlignItems(ui.Center).Children(func() {
				// 拖动排序会在这次遍历里重排终端和文件：遍历副本
				for _, key := range slices.Clone(col.tabs) {
					active := key == col.active
					if d := docs[key]; d != nil {
						a.docTab(c, w, a.filesFor(w), d, docDetail(L(c), d, names), i, active, focused)
					} else if term, ok := terms[key]; ok {
						a.tab(c, w, term, i, active, focused)
					}
				}
			})
		})
		ui.Row(c).Shrink(0).Height(titleHeight(c)).AlignItems(ui.Center).Children(func() { a.newTerminalButton(c, w, i) })
		// 标签后面的空处也接住拖来的标签：移进这一格
		tail := ui.Box(c.Key("strip-tail")).Grow(1).AlignSelf(ui.Stretch)
		if g, over := ui.DragOver[tabDrag](tail); over && g.ws == w.ID && a.colOf(g.key) != i && col.id != "" {
			accent := c.Theme().Accent
			tail.DrawOver(func(p *ui.Painter, r ui.Rect) {
				p.Fill(ui.Rect{X: r.X, Y: r.Y + 8, W: 2, H: r.H - 16}, accent, 1)
			})
		}
		if g, ok := ui.Drop[tabDrag](tail); ok && g.ws == w.ID && a.colOf(g.key) != i {
			a.moveTab(w, g.key, i, sideIn)
			c.AnimationFrame()
		}
		if corner {
			ui.Row(c).Shrink(0).Gap(2).AlignItems(ui.Center).Children(func() { a.tabBarButtons(c, w) })
		}
	})
}

// tabMark 在分栏时给焦点格选中的标签底下画一道强调色的短线，看得出键盘在哪格。
func tabMark(c *ui.Context, e ui.Element) {
	accent := c.Theme().Accent
	e.DrawOver(func(p *ui.Painter, r ui.Rect) {
		p.Fill(ui.Rect{X: r.X + 8, Y: r.Y + r.H - 2, W: r.W - 16, H: 2}, accent, 1)
	})
}
