package app

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/files"
)

func columnsApp() (*app, *workspace) {
	a := &app{activeTerm: map[string]string{}}
	a.workspaces = []workspace{{ID: "w1", Terminals: []terminalInfo{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}}
	return a, &a.workspaces[0]
}

func twoCols(left, right []string, focus int) columnsState {
	return columnsState{Cols: []columnState{{Tabs: left, Active: left[0]}, {Tabs: right, Active: right[0]}}, Focus: focus}
}

// frame 是 build 里每帧定栏和接入的那几步（不接入，只算该接入哪些）。
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
	if !slices.Equal(a.cols[0].tabs, []string{"t1", "t2", "t3"}) {
		t.Fatalf("tabs = %v", a.cols[0].tabs)
	}
	// 还没选过：选第一个
	a.activeTerm = map[string]string{}
	if got := frame(a, w); got[0] != "t1" {
		t.Fatalf("first = %v", got)
	}
}

func TestColumnsFocusFollowsSelection(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1"}, []string{"t2"}, 0)}
	a.activeTerm["w1"] = "t2"
	if got := frame(a, w); got != [maxColumns]string{"t1", "t2"} || a.pane != 1 {
		t.Fatalf("right selected = %v pane %d", got, a.pane)
	}
	// 不在任何一栏的终端（新开的）放进焦点栏，记下来
	if !slices.Equal(a.state.Columns["w1"].Cols[1].Tabs, []string{"t2", "t3"}) {
		t.Fatalf("new tab not in focused column: %+v", a.state.Columns["w1"])
	}
	a.activeTerm["w1"] = "t3"
	if got := frame(a, w); got != [maxColumns]string{"t1", "t3"} || a.pane != 1 {
		t.Fatalf("t3 selected = %v pane %d", got, a.pane)
	}
	// 选中左栏的：焦点过去，右栏还是 t3
	a.activeTerm["w1"] = "t1"
	if got := frame(a, w); got != [maxColumns]string{"t1", "t3"} || a.pane != 0 || a.state.Columns["w1"].Focus != 0 {
		t.Fatalf("left selected = %v pane %d", got, a.pane)
	}
}

// 刚打开窗口时还没选过终端：选焦点栏选中的，不选第一个终端、把焦点换到它那栏。
func TestColumnsSurviveRestart(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 1)}
	if got := frame(a, w); got != [maxColumns]string{"t1", "t3"} || a.pane != 1 || a.activeTerm["w1"] != "t3" {
		t.Fatalf("restart = %v pane %d active %q", got, a.pane, a.activeTerm["w1"])
	}
}

// 终端不在列表里了：那栏不显示，记录留着（交接时可能有一次列表不全），回来还在原来那栏。
func TestColumnsMissingTerminal(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	all := w.Terminals
	w.Terminals = all[:2]
	if frame(a, w); a.splitShown() {
		t.Fatal("a column whose terminals are gone should not show")
	}
	if !slices.Contains(a.state.Columns["w1"].Cols[1].Tabs, "t3") {
		t.Fatal("a missing terminal should stay in its column")
	}
	w.Terminals = all
	if got := frame(a, w); got[1] != "t3" {
		t.Fatalf("back = %v", got)
	}
}

func TestMoveTabSplitsAndCollapses(t *testing.T) {
	a, w := columnsApp()
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	// 没分栏时拖到右边缘：新开一栏
	a.moveTab(w, "t2", 0, 1)
	st := a.state.Columns["w1"]
	if len(st.Cols) != 2 || !slices.Equal(st.Cols[1].Tabs, []string{"t2"}) || st.Focus != 1 || a.activeTerm["w1"] != "t2" {
		t.Fatalf("split = %+v active %q", st, a.activeTerm["w1"])
	}
	if st.Cols[0].weight() != st.Cols[1].weight() {
		t.Fatalf("new column should take half: %+v", st)
	}
	frame(a, w)
	// 左边新开第三栏
	a.moveTab(w, "t3", 0, -1)
	if frame(a, w); len(a.cols) != 3 || a.cols[0].tabs[0] != "t3" || a.pane != 0 {
		t.Fatalf("three = %+v pane %d", a.cols, a.pane)
	}
	// 满三栏：不能再开，除非拖走的标签所在的栏会空掉；一栏唯一的标签拖到自己边上也不行
	w.Terminals = append(w.Terminals, terminalInfo{ID: "t4"})
	frame(a, w) // t4 进焦点栏（t3 那栏）
	if a.canSplitWith("t4", 1) {
		t.Fatal("a fourth column should not open")
	}
	if !a.canSplitWith("t2", 0) {
		t.Fatal("moving a column's only tab keeps three columns")
	}
	if a.canSplitWith("t1", 1) {
		t.Fatal("a column's only tab cannot split its own column")
	}
	a.dropColumnKey(w, "t4")
	w.Terminals = w.Terminals[:3]
	// 移进别的栏：原来那栏空了就收起
	a.moveTab(w, "t2", 1, 0)
	if frame(a, w); len(a.cols) != 2 || !slices.Equal(a.cols[1].tabs, []string{"t1", "t2"}) || a.cols[1].active != "t2" {
		t.Fatalf("collapse = %+v", a.cols)
	}
	// 把最后两栏合成一栏：记录删掉
	a.moveTab(w, "t3", 1, 0)
	if _, ok := a.state.Columns["w1"]; ok {
		t.Fatalf("one column left should drop the record: %+v", a.state.Columns["w1"])
	}
}

func TestDropColumnKey(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1", "t2"}, []string{"t3"}, 0)}
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	// 关掉焦点栏选中的：换成同一栏挨着的，不跳到别的栏
	a.dropColumnKey(w, "t1")
	w.Terminals = slices.Delete(w.Terminals, 0, 1)
	if frame(a, w); a.activeTerm["w1"] != "t2" || a.pane != 0 || !a.splitShown() {
		t.Fatalf("after close active %q pane %d", a.activeTerm["w1"], a.pane)
	}
	// 关掉一栏唯一的标签：那栏收起，只剩一栏时回到不分栏
	a.activeTerm["w1"] = "t3"
	frame(a, w)
	a.dropColumnKey(w, "t3")
	w.Terminals = w.Terminals[:1]
	if _, ok := a.state.Columns["w1"]; ok || a.activeTerm["w1"] != "t2" {
		t.Fatalf("collapse: %+v active %q", a.state.Columns, a.activeTerm["w1"])
	}
}

func TestDocTabsInColumns(t *testing.T) {
	a, w := columnsApp()
	w.Path = "/r"
	v := &filesView{wsID: "w1", root: "/r", tree: files.NewTree("/r")}
	readme, mainGo := &fileDoc{path: "/r/README.md"}, &fileDoc{path: "/r/main.go"}
	v.docs = []*fileDoc{readme, mainGo}
	a.files = map[string]*filesView{"w1": v}
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1", "t2", "t3"}, []string{docKey(readme), docKey(mainGo)}, 0)}
	a.activeTerm["w1"] = "t1"
	if got := frame(a, w); got != [maxColumns]string{"t1"} || a.cols[1].active != docKey(readme) {
		t.Fatalf("doc column = %v %+v", got, a.cols)
	}
	// 点了右栏的文件：焦点到右栏，选中的是文件标签
	a.selectKey(w, docKey(mainGo), true)
	if frame(a, w); a.pane != 1 || !a.filesActive(w) || v.cur != mainGo || !mainGo.focus {
		t.Fatalf("doc focus pane %d cur %v", a.pane, v.cur)
	}
	// 焦点栏关掉最后的文件之外的一个：换成挨着的那个
	a.dropDoc(v, mainGo)
	if frame(a, w); a.pane != 1 || v.cur != readme {
		t.Fatalf("after close pane %d cur %v", a.pane, v.cur)
	}
	// 切换栏：回到终端那栏
	a.switchColumn(w, 1)
	if a.pane != 0 || a.activeTerm["w1"] != "t1" || !a.focusTerm {
		t.Fatalf("switch pane %d active %q", a.pane, a.activeTerm["w1"])
	}
}

func TestResizeColumnsKeepsMinWidth(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1"}, []string{"t2", "t3"}, 0)}
	frame(a, w)
	width := float32(1000)
	a.resizeColumns(w, 0, 100, width)
	frame(a, w)
	if got := a.cols[0].weight * width; got < 599 || got > 601 {
		t.Fatalf("left = %v, want 600", got)
	}
	a.resizeColumns(w, 0, -2000, width)
	frame(a, w)
	if got := a.cols[0].weight * width; got < colMinWidth()-1 || got > colMinWidth()+1 {
		t.Fatalf("left = %v, want min %v", got, colMinWidth())
	}
	a.equalizeColumns(w)
	if frame(a, w); a.cols[0].weight != a.cols[1].weight {
		t.Fatalf("equalize = %+v", a.cols)
	}
}

func TestMigrateSplits(t *testing.T) {
	s := uiState{Splits: map[string]legacySplit{"w1": {Left: "t1", Right: "t2", Percent: 30, Focus: 1}}}
	migrateSplits(&s)
	st := s.Columns["w1"]
	if s.Splits != nil || len(st.Cols) != 2 || st.Cols[0].Tabs[0] != "t1" || st.Cols[1].Tabs[0] != "t2" || st.Focus != 1 || st.Cols[0].Share != 30 {
		t.Fatalf("migrated = %+v", s.Columns)
	}
}

// 同一个终端换到另一栏时沿用原来的视图，不重新接入（这里没有 client，重新接入就会崩）。
func TestEnsurePanesMoves(t *testing.T) {
	a, _ := columnsApp()
	v1, v2 := &termView{id: "t1"}, &termView{id: "t2"}
	a.panes = [maxColumns]*termView{v1, v2}
	a.ensurePanes([maxColumns]string{"t2", "t1"})
	if a.panes[0] != v2 || a.panes[1] != v1 {
		t.Fatalf("panes = %v", a.panes)
	}
}

// 刚点进另一栏、界面还没重建就按快捷键：以收到按键的那栏为准。
func TestSplitKeyUsesPaneThatGotKey(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1"}, []string{"t2", "t3"}, 0)}
	frame(a, w)
	a.panes = [maxColumns]*termView{{id: "t1"}, {id: "t2"}}
	if !a.splitKey(a.panes[1], ui.Cmd|ui.Shift, ui.KeyD) {
		t.Fatal("⇧⌘D not handled")
	}
	if _, ok := a.state.Columns["w1"]; ok || a.activeTerm["w1"] != "t2" {
		t.Fatalf("merge kept %q, want t2", a.activeTerm["w1"])
	}
}

// 连按 ⌘]：第一下之后焦点要到下一帧才移过去，第二下还落在原来那栏，不能据此把焦点栏改回去。
func TestSwitchColumnTwiceBeforeFrame(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1"}, []string{"t2", "t3"}, 1)}
	frame(a, w)
	a.panes = [maxColumns]*termView{{id: "t1"}, {id: "t2"}}
	right := a.panes[1]
	a.splitKey(right, ui.Cmd, ui.KeyBracketRight)
	if a.pane != 0 {
		t.Fatalf("first press: pane %d, want 0", a.pane)
	}
	a.splitKey(right, ui.Cmd, ui.KeyBracketRight)
	if a.pane != 1 || a.state.Columns["w1"].Focus != 1 {
		t.Fatalf("second press: pane %d, want 1", a.pane)
	}
}

// 关掉一栏中间选中的文件：换成挨着的那个文件，不是这栏的第一个标签。
func TestCloseMiddleDocPicksNeighbor(t *testing.T) {
	a, w := columnsApp()
	w.Path = "/r"
	v := &filesView{wsID: "w1", root: "/r", tree: files.NewTree("/r")}
	x, y, z := &fileDoc{path: "/r/a.go"}, &fileDoc{path: "/r/b.go"}, &fileDoc{path: "/r/c.go"}
	v.docs = []*fileDoc{x, y, z}
	a.files = map[string]*filesView{"w1": v}
	a.state.Columns = map[string]columnsState{"w1": twoCols([]string{"t1", docKey(x), docKey(y), docKey(z)}, []string{"t2", "t3"}, 0)}
	a.selectKey(w, docKey(y), true)
	frame(a, w)
	a.dropDoc(v, y)
	if frame(a, w); v.cur != z || !a.filesActive(w) || a.pane != 0 {
		t.Fatalf("after close cur %v files %v pane %d", v.cur, a.filesActive(w), a.pane)
	}
}

// 重开窗口时文件还没恢复：先恢复，焦点栏选中的文件和焦点都还在。
func TestColumnsRestoreDocFocus(t *testing.T) {
	a, w := columnsApp()
	w.Path = t.TempDir()
	readme := w.Path + "/README.md"
	a.state.Files = map[string]filesState{"w1": {Open: []string{readme}, Current: readme}}
	key := docKeyPrefix + readme
	a.state.Columns = map[string]columnsState{"w1": {Focus: 1, Cols: []columnState{
		{Tabs: []string{"t1", "t2", "t3"}, Active: "t1"},
		{Tabs: []string{key}, Active: key},
	}}}
	if frame(a, w); a.pane != 1 || !a.filesActive(w) || a.state.Columns["w1"].Focus != 1 || a.files["w1"].cur.path != readme {
		t.Fatalf("restore pane %d files %v %+v", a.pane, a.filesActive(w), a.state.Columns["w1"])
	}
}

// 被预览替换掉、关掉的文件不留在栏里：再打开时进焦点栏，不让收起的栏又冒出来。
func TestStaleDocKeysPruned(t *testing.T) {
	a, w := columnsApp()
	w.Path = "/r"
	v := &filesView{wsID: "w1", root: "/r", tree: files.NewTree("/r")}
	a.files = map[string]*filesView{"w1": v}
	a.state.Columns = map[string]columnsState{"w1": {Cols: []columnState{
		{Tabs: []string{"t1"}, Active: "t1"}, {Tabs: []string{"t2", "t3"}}, {Tabs: []string{"doc:/r/gone.go"}},
	}}}
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	if st := a.state.Columns["w1"]; len(st.Cols) != 2 {
		t.Fatalf("stale doc column kept: %+v", st)
	}
}

// 拖分栏线时只改显示的几栏，按原来的比例尺度：没显示的栏回来时宽度不变。
func TestResizeKeepsHiddenShare(t *testing.T) {
	a, w := columnsApp()
	a.state.Columns = map[string]columnsState{"w1": {Cols: []columnState{
		{Tabs: []string{"t1"}, Share: 50}, {Tabs: []string{"t2"}, Share: 50}, {Tabs: []string{"t3"}, Share: 50},
	}}}
	all := w.Terminals
	w.Terminals = all[:2]
	frame(a, w)
	a.resizeColumns(w, 0, 100, 1000)
	st := a.state.Columns["w1"]
	if got := st.Cols[0].Share + st.Cols[1].Share; got < 99.9 || got > 100.1 || st.Cols[2].Share != 50 {
		t.Fatalf("shares = %+v", st.Cols)
	}
	w.Terminals = all
	if frame(a, w); a.cols[2].weight < 0.33 || a.cols[2].weight > 0.34 {
		t.Fatalf("hidden column came back at %v", a.cols[2].weight)
	}
}

// 预览标签移到别的栏就常驻；分栏时单击预览只换掉焦点栏里的预览标签。
func TestPreviewDocsAcrossColumns(t *testing.T) {
	a, w := columnsApp()
	w.Path = t.TempDir()
	a.selected = "w1"
	v := a.filesFor(w)
	a.activeTerm["w1"] = "t1"
	frame(a, w)
	a.previewFile(w, w.Path+"/a.go")
	frame(a, w)
	a.moveTab(w, docKeyPrefix+w.Path+"/a.go", 0, 1)
	frame(a, w)
	if v.docs[0].transient {
		t.Fatal("a preview moved to another column should stay")
	}
	// 焦点回到终端栏，单击预览两个文件：第二个换掉第一个，右栏的 a.go 不动
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
