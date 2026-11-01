package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/files"
)

func columnsApp() (*app, *workspace) {
	a := &app{activeTerm: map[string]string{}, selected: "w1"}
	a.workspaces = []workspace{{ID: "w1", Terminals: []terminalInfo{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}}
	return a, &a.workspaces[0]
}

func leaf(id string, tabs ...string) paneNode {
	return paneNode{ID: id, Tabs: tabs, Active: tabs[0]}
}

// twoCols 是左右两格 p1、p2，焦点在 focus 那格（0 左 1 右）。
func twoCols(left, right []string, focus int) layoutState {
	st := layoutState{Root: paneNode{Split: splitH, Kids: []paneNode{leaf("p1", left...), leaf("p2", right...)}}, Focus: "p1"}
	if focus == 1 {
		st.Focus = "p2"
	}
	return st
}

// frame 是 build 里每帧定格和接入的那几步（不接入，只算该接入哪些）。
func frame(a *app, w *workspace) [maxColumns]string {
	a.syncColumns(w)
	return a.paneIDs(a.currentTerminal(w))
}

func TestColumnsSingle(t *testing.T) {
	a, w := columnsApp()
	a.activeTerm["w1"] = "t2"
	if got := frame(a, w); got != [maxColumns]string{"t2"} || a.splitShown() || len(a.cols) != 1 {
		t.Fatalf("single = %v cols %+v", got, a.cols)
	}
	if !slices.Equal(a.cols[0].tabs, []string{"t1", "t2", "t3"}) || !a.cols[0].top || !a.cols[0].right {
		t.Fatalf("cell = %+v", a.cols[0])
	}
	a.activeTerm = map[string]string{}
	if got := frame(a, w); got[0] != "t1" {
		t.Fatalf("first = %v", got)
	}
}

func TestColumnsFocusFollowsSelection(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1"}, []string{"t2"}, 0)}
	a.activeTerm["w1"] = "t2"
	if got := frame(a, w); got != [maxColumns]string{"t1", "t2"} || a.pane != 1 {
		t.Fatalf("right selected = %v pane %d", got, a.pane)
	}
	// 不在任何一格的终端（新开的）放进焦点格，记下来
	st := a.state.Panes["w1"]
	if !slices.Equal(st.leaf("p2").Tabs, []string{"t2", "t3"}) {
		t.Fatalf("new tab not in focused cell: %+v", st)
	}
	a.activeTerm["w1"] = "t1"
	if got := frame(a, w); got != [maxColumns]string{"t1", "t2"} || a.pane != 0 || a.state.Panes["w1"].Focus != "p1" {
		t.Fatalf("left selected = %v pane %d", got, a.pane)
	}
}

func TestColumnsSurviveRestart(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 1)}
	if got := frame(a, w); got != [maxColumns]string{"t1", "t3"} || a.pane != 1 || a.activeTerm["w1"] != "t3" {
		t.Fatalf("restart = %v pane %d active %q", got, a.pane, a.activeTerm["w1"])
	}
}

func TestColumnsMissingTerminal(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	all := w.Terminals
	w.Terminals = all[:2]
	if frame(a, w); a.splitShown() {
		t.Fatal("a cell whose terminals are gone should not show")
	}
	if st := a.state.Panes["w1"]; st.leaf("p2") == nil || !slices.Contains(st.leaf("p2").Tabs, "t3") {
		t.Fatal("a missing terminal should stay in its cell")
	}
	w.Terminals = all
	if got := frame(a, w); got[1] != "t3" {
		t.Fatalf("back = %v", got)
	}
}

// 任意嵌套：右边那格再上下分，下面那格再左右分；顺序从左上到右下，贴边的标记对。
func TestNestedSplits(t *testing.T) {
	a, w := columnsApp()
	w.Terminals = append(w.Terminals, terminalInfo{ID: "t4"})
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.moveTab(w, "t2", 0, sideRight)
	frame(a, w)
	a.moveTab(w, "t3", 1, sideBottom)
	frame(a, w)
	a.moveTab(w, "t4", 2, sideRight)
	frame(a, w)
	st := a.state.Panes["w1"]
	if st.Root.Split != splitH || len(st.Root.Kids) != 2 || st.Root.Kids[1].Split != splitV || st.Root.Kids[1].Kids[1].Split != splitH {
		t.Fatalf("tree = %+v", st.Root)
	}
	var tabs []string
	for _, c := range a.cols {
		tabs = append(tabs, c.tabs[0])
	}
	if !slices.Equal(tabs, []string{"t1", "t2", "t3", "t4"}) || a.pane != 3 {
		t.Fatalf("order = %v pane %d", tabs, a.pane)
	}
	c := a.cols
	if !c[0].top || !c[0].left || c[0].right || !c[1].top || !c[1].right || c[2].top || c[2].right || c[3].top || !c[3].right {
		t.Fatalf("edges = %+v", c)
	}
	// 满四格：不能再分（拖来的标签所在的格不会空掉时）
	if a.canSplitWith("t9", 1, sideBottom) {
		t.Fatal("a fifth cell should not open")
	}
	// t4 回到 t3 那格：下面那层只剩一格，收掉
	a.moveTab(w, "t4", 2, sideIn)
	frame(a, w)
	if st := a.state.Panes["w1"]; st.Root.Kids[1].Kids[1].Split != "" || len(a.cols) != 3 {
		t.Fatalf("collapse = %+v", st.Root)
	}
}

// 同方向连着分摊平成一层，比例按原来的分。
func TestSameDirectionFlattens(t *testing.T) {
	a, w := columnsApp()
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.moveTab(w, "t2", 0, sideRight)
	frame(a, w)
	a.moveTab(w, "t3", 1, sideRight)
	frame(a, w)
	st := a.state.Panes["w1"]
	if st.Root.Split != splitH || len(st.Root.Kids) != 3 {
		t.Fatalf("tree = %+v", st.Root)
	}
	if a.tree.weight[0] != 0.5 || a.tree.weight[1] != 0.25 {
		t.Fatalf("weights = %v", a.tree.weight)
	}
}

func TestMoveTabSplitsAndCollapses(t *testing.T) {
	a, w := columnsApp()
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.moveTab(w, "t2", 0, sideBottom)
	st := a.state.Panes["w1"]
	if st.Root.Split != splitV || len(st.Root.Kids) != 2 || st.Focus != st.Root.Kids[1].ID || a.activeTerm["w1"] != "t2" {
		t.Fatalf("split = %+v active %q", st, a.activeTerm["w1"])
	}
	frame(a, w)
	// 一格唯一的标签拖到自己那格：不行；拖到紧挨着它那一边：也不行（放下去还是原样）
	if a.canSplitWith("t2", 1, sideTop) || a.canSplitWith("t2", 0, sideBottom) {
		t.Fatal("no-op splits should be refused")
	}
	if !a.canSplitWith("t2", 0, sideTop) {
		t.Fatal("moving above should be allowed")
	}
	// 移回上面那格：下面那格空了就收起，只剩一格时记录删掉
	a.moveTab(w, "t2", 0, sideIn)
	if _, ok := a.state.Panes["w1"]; ok {
		t.Fatalf("one cell left should drop the record: %+v", a.state.Panes["w1"])
	}
}

func TestDropColumnKey(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.dropColumnKey(w, "t1")
	w.Terminals = slices.Delete(w.Terminals, 0, 1)
	if frame(a, w); a.activeTerm["w1"] != "t2" || a.pane != 0 || !a.splitShown() {
		t.Fatalf("after close active %q pane %d", a.activeTerm["w1"], a.pane)
	}
	a.activeTerm["w1"] = "t3"
	frame(a, w)
	a.dropColumnKey(w, "t3")
	w.Terminals = w.Terminals[:1]
	if _, ok := a.state.Panes["w1"]; ok || a.activeTerm["w1"] != "t2" {
		t.Fatalf("collapse: %+v active %q", a.state.Panes, a.activeTerm["w1"])
	}
}

func docsApp(t *testing.T, a *app, w *workspace, paths ...string) (*filesView, []*fileDoc) {
	w.Path = "/r"
	v := &filesView{wsID: "w1", root: "/r", tree: files.NewTree("/r")}
	var docs []*fileDoc
	for _, p := range paths {
		docs = append(docs, &fileDoc{path: "/r/" + p})
	}
	v.docs = slices.Clone(docs)
	a.files = map[string]*filesView{"w1": v}
	return v, docs
}

func TestDocTabsInColumns(t *testing.T) {
	a, w := columnsApp()
	v, d := docsApp(t, a, w, "README.md", "main.go")
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2", "t3"}, []string{docKey(d[0]), docKey(d[1])}, 0)}
	a.activeTerm["w1"] = "t1"
	if got := frame(a, w); got != [maxColumns]string{"t1"} || a.cols[1].active != docKey(d[0]) {
		t.Fatalf("doc cell = %v %+v", got, a.cols)
	}
	a.selectKey(w, docKey(d[1]), true)
	if frame(a, w); a.pane != 1 || !a.filesActive(w) || v.cur != d[1] || !d[1].focus {
		t.Fatalf("doc focus pane %d cur %v", a.pane, v.cur)
	}
	a.dropDoc(v, d[1])
	if frame(a, w); a.pane != 1 || v.cur != d[0] {
		t.Fatalf("after close pane %d cur %v", a.pane, v.cur)
	}
	a.switchColumn(w, 1)
	if a.pane != 0 || a.activeTerm["w1"] != "t1" || !a.focusTerm {
		t.Fatalf("switch pane %d active %q", a.pane, a.activeTerm["w1"])
	}
}

func TestCloseMiddleDocPicksNeighbor(t *testing.T) {
	a, w := columnsApp()
	v, d := docsApp(t, a, w, "a.go", "b.go", "c.go")
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", docKey(d[0]), docKey(d[1]), docKey(d[2])}, []string{"t2", "t3"}, 0)}
	a.selectKey(w, docKey(d[1]), true)
	frame(a, w)
	a.dropDoc(v, d[1])
	if frame(a, w); v.cur != d[2] || !a.filesActive(w) || a.pane != 0 {
		t.Fatalf("after close cur %v files %v pane %d", v.cur, a.filesActive(w), a.pane)
	}
}

func TestColumnsRestoreDocFocus(t *testing.T) {
	a, w := columnsApp()
	w.Path = t.TempDir()
	readme := w.Path + "/README.md"
	a.state.Files = map[string]filesState{"w1": {Open: []string{readme}, Current: readme}}
	key := docKeyPrefix + readme
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t2", "t3"}, []string{key}, 1)}
	if frame(a, w); a.pane != 1 || !a.filesActive(w) || a.state.Panes["w1"].Focus != "p2" || a.files["w1"].cur.path != readme {
		t.Fatalf("restore pane %d files %v %+v", a.pane, a.filesActive(w), a.state.Panes["w1"])
	}
}

func TestStaleDocKeysPruned(t *testing.T) {
	a, w := columnsApp()
	docsApp(t, a, w)
	a.state.Panes = map[string]layoutState{"w1": {Root: paneNode{Split: splitH, Kids: []paneNode{
		leaf("p1", "t1"), leaf("p2", "t2", "t3"), leaf("p3", "doc:/r/gone.go"),
	}}}}
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	if st := a.state.Panes["w1"]; len(st.Root.Kids) != 2 {
		t.Fatalf("stale doc cell kept: %+v", st)
	}
}

func TestPreviewDocsAcrossColumns(t *testing.T) {
	a, w := columnsApp()
	w.Path = t.TempDir()
	v := a.filesFor(w)
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.previewFile(w, w.Path+"/a.go")
	frame(a, w)
	a.moveTab(w, docKeyPrefix+w.Path+"/a.go", 0, sideRight)
	frame(a, w)
	if v.docs[0].transient {
		t.Fatal("a preview moved to another cell should stay")
	}
	a.focusColumn(w, 0, false)
	frame(a, w)
	a.previewFile(w, w.Path+"/b.go")
	frame(a, w)
	a.previewFile(w, w.Path+"/c.go")
	frame(a, w)
	var paths []string
	for _, d := range v.docs {
		paths = append(paths, d.path[len(w.Path)+1:])
	}
	if !slices.Equal(paths, []string{"a.go", "c.go"}) || a.colOf(docKeyPrefix+w.Path+"/c.go") != 0 || a.colOf(docKeyPrefix+w.Path+"/a.go") != 1 {
		t.Fatalf("docs = %v cols %+v", paths, a.cols)
	}
}

func TestResizeKeepsHiddenShare(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": {Root: paneNode{Split: splitH, Kids: []paneNode{
		{ID: "p1", Tabs: []string{"t1"}, Share: 50}, {ID: "p2", Tabs: []string{"t2"}, Share: 50}, {ID: "p3", Tabs: []string{"t3"}, Share: 50},
	}}}}
	all := w.Terminals
	w.Terminals = all[:2]
	frame(a, w)
	a.resizeSplit(w, a.tree, 0, 100, 1000)
	st := a.state.Panes["w1"]
	if got := st.Root.Kids[0].Share + st.Root.Kids[1].Share; got < 99.9 || got > 100.1 || st.Root.Kids[2].Share != 50 {
		t.Fatalf("shares = %+v", st.Root.Kids)
	}
	if st.Root.Kids[0].Share < 59.9 || st.Root.Kids[0].Share > 60.1 {
		t.Fatalf("left = %v, want 60", st.Root.Kids[0].Share)
	}
	w.Terminals = all
	if frame(a, w); a.tree.weight[2] < 0.33 || a.tree.weight[2] > 0.34 {
		t.Fatalf("hidden cell came back at %v", a.tree.weight[2])
	}
	// 拖过头停在最小宽度；双击平分
	a.resizeSplit(w, a.tree, 0, -2000, 1000)
	frame(a, w)
	if got := a.tree.weight[0] * 1000; got < colMinWidth()-1 || got > colMinWidth()+1 {
		t.Fatalf("min = %v", got)
	}
	a.equalizeSplit(w, a.tree)
	if frame(a, w); a.tree.weight[0] != a.tree.weight[2] {
		t.Fatalf("equalize = %v", a.tree.weight)
	}
}

func TestMigrateSplits(t *testing.T) {
	s := uiState{Splits: map[string]legacySplit{"w1": {Left: "t1", Right: "t2", Percent: 30, Focus: 1}}}
	migrateSplits(&s)
	st := s.Panes["w1"]
	if s.Splits != nil || st.Root.Split != splitH || st.Root.Kids[0].Tabs[0] != "t1" || st.Root.Kids[1].Tabs[0] != "t2" || st.Focus != "p2" || st.Root.Kids[0].Share != 30 {
		t.Fatalf("migrated = %+v", s.Panes)
	}
}

func TestEnsurePanesMoves(t *testing.T) {
	a, _ := columnsApp()
	v1, v2 := &termView{id: "t1"}, &termView{id: "t2"}
	a.panes = [maxColumns]*termView{v1, v2}
	a.ensurePanes([maxColumns]string{"t2", "t1"})
	if a.panes[0] != v2 || a.panes[1] != v1 {
		t.Fatalf("panes = %v", a.panes)
	}
}

// ⇧⌘D 是向下分栏；「合并所有分栏」是 ⌃⌘D。
func TestSplitDownKey(t *testing.T) {
	a, w := columnsApp()
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.panes = [maxColumns]*termView{{id: "t1"}}
	a.splitPending = map[string]bool{"w1": true} // 不真的建终端
	if !a.splitKey(a.panes[0], ui.Cmd|ui.Shift, ui.KeyD) {
		t.Fatal("⇧⌘D not handled")
	}
	if !a.keyIs("closeSplit", ui.Cmd|ui.Ctrl, ui.KeyD) {
		t.Fatal("merge should be ⌃⌘D")
	}
}

// 连按 ⌘]：第一下之后焦点要到下一帧才移过去，第二下还落在原来那格，不能据此把焦点格改回去。
func TestSwitchColumnTwiceBeforeFrame(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1"}, []string{"t2", "t3"}, 1)}
	frame(a, w)
	a.panes = [maxColumns]*termView{{id: "t1"}, {id: "t2"}}
	right := a.panes[1]
	a.splitKey(right, ui.Cmd, ui.KeyBracketRight)
	if a.pane != 0 {
		t.Fatalf("first press: pane %d, want 0", a.pane)
	}
	a.splitKey(right, ui.Cmd, ui.KeyBracketRight)
	if a.pane != 1 || a.state.Panes["w1"].Focus != "p2" {
		t.Fatalf("second press: pane %d, want 1", a.pane)
	}
}

func TestSwitchColumnBackward(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": {Root: paneNode{Split: splitH, Kids: []paneNode{leaf("p1", "t1"), leaf("p2", "t2"), leaf("p3", "t3")}}, Focus: "p1"}}
	frame(a, w)
	a.panes = [maxColumns]*termView{{id: "t1"}, {id: "t2"}, {id: "t3"}}
	a.splitKey(a.panes[0], ui.Cmd, ui.KeyBracketLeft)
	if a.pane != 2 || a.activeTerm["w1"] != "t3" {
		t.Fatalf("⌘[ from first: pane %d active %q", a.pane, a.activeTerm["w1"])
	}
}

// 格里只剩一个预览标签时单击别的文件：原地换掉，这格不收起、新的不跑到别格。
func TestPreviewReplacedInPlace(t *testing.T) {
	a, w := columnsApp()
	w.Path = t.TempDir()
	a.filesFor(w)
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.previewFile(w, w.Path+"/a.go")
	frame(a, w)
	a.moveTab(w, docKeyPrefix+w.Path+"/a.go", 0, sideRight)
	frame(a, w)
	// 拖到别格的转成了常驻，再变回预览模拟"这格只剩一个预览标签"
	a.files["w1"].docs[0].transient = true
	a.previewFile(w, w.Path+"/b.go")
	frame(a, w)
	if len(a.cols) != 2 || a.colOf(docKeyPrefix+w.Path+"/b.go") != 1 || a.pane != 1 {
		t.Fatalf("cols %+v pane %d", a.cols, a.pane)
	}
}

// 焦点格的终端一时不在列表里（交接）：选中和焦点都不动，回来后还是原样。
func TestFocusCellHiddenKeepsFocus(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1"}, []string{"t2", "t3"}, 0)}
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	all := w.Terminals
	w.Terminals = all[1:]
	frame(a, w)
	frame(a, w)
	if a.activeTerm["w1"] != "t1" || a.state.Panes["w1"].Focus != "p1" {
		t.Fatalf("hidden: active %q focus %q", a.activeTerm["w1"], a.state.Panes["w1"].Focus)
	}
	w.Terminals = all
	st := a.state.Panes["w1"]
	if frame(a, w); a.pane != 0 || a.activeTerm["w1"] != "t1" || st.leaf("p2").Active != "t2" {
		t.Fatalf("back: pane %d active %q", a.pane, a.activeTerm["w1"])
	}
}

// 关标签时不收起别的格里一时看不见的终端那格。
func TestDropKeyKeepsHiddenCells(t *testing.T) {
	a, w := columnsApp()
	a.state.Panes = map[string]layoutState{"w1": {Root: paneNode{Split: splitH, Kids: []paneNode{leaf("p1", "t1", "t2"), leaf("p2", "t3")}}, Focus: "p1"}}
	a.activeTerm["w1"] = "t1"
	all := w.Terminals
	w.Terminals = all[:2]
	frame(a, w)
	a.dropColumnKey(w, "t2")
	if st := a.state.Panes["w1"]; st.leaf("p2") == nil {
		t.Fatalf("hidden cell dropped: %+v", st)
	}
}
