package app

import "testing"

func splitApp() (*app, *workspace) {
	a := &app{activeTerm: map[string]string{}}
	a.workspaces = []workspace{{ID: "w1", Terminals: []terminalInfo{{ID: "t1"}, {ID: "t2"}, {ID: "t3"}}}}
	return a, &a.workspaces[0]
}

func TestPaneIDs(t *testing.T) {
	a, w := splitApp()
	// 没分屏：只有一栏
	if got := a.paneIDs(w, &w.Terminals[0]); got != [2]string{"t1", ""} || a.pane != 0 {
		t.Fatalf("no split = %v pane %d", got, a.pane)
	}
	a.state.Splits = map[string]splitState{"w1": {Left: "t1", Right: "t2"}}
	// 选中右栏的终端：焦点在右栏
	if got := a.paneIDs(w, &w.Terminals[1]); got != [2]string{"t1", "t2"} || a.pane != 1 {
		t.Fatalf("right selected = %v pane %d", got, a.pane)
	}
	// 在标签栏选了不在分屏里的终端：换掉焦点栏（右栏）
	if got := a.paneIDs(w, &w.Terminals[2]); got != [2]string{"t1", "t3"} || a.pane != 1 {
		t.Fatalf("other selected = %v pane %d", got, a.pane)
	}
	if sp := a.state.Splits["w1"]; sp.Right != "t3" || sp.Focus != 1 {
		t.Fatalf("split = %+v", sp)
	}
	// 右栏的终端关掉了：分屏收起
	w.Terminals = w.Terminals[:2]
	if got := a.paneIDs(w, &w.Terminals[0]); got != [2]string{"t1", ""} {
		t.Fatalf("closed = %v", got)
	}
	if _, ok := a.state.Splits["w1"]; ok {
		t.Fatal("split should be dropped")
	}
	// 选中文件标签：一栏都不接
	a.state.Splits = map[string]splitState{"w1": {Left: "t1", Right: "t2"}}
	if got := a.paneIDs(w, nil); got != [2]string{} {
		t.Fatalf("files tab = %v", got)
	}
}

func TestSplitRight(t *testing.T) {
	a, w := splitApp()
	// 当前终端放到右栏，左栏是它前面的标签
	a.splitRight(w, &w.Terminals[1])
	if sp := a.state.Splits["w1"]; sp.Left != "t1" || sp.Right != "t2" || sp.Focus != 1 || a.activeTerm["w1"] != "t2" {
		t.Fatalf("split = %+v active %q", sp, a.activeTerm["w1"])
	}
	// 在最前时左栏是后面那个
	a, w = splitApp()
	a.splitRight(w, &w.Terminals[0])
	if sp := a.state.Splits["w1"]; sp.Left != "t2" || sp.Right != "t1" {
		t.Fatalf("first = %+v", sp)
	}
	// 已经分屏时「在右侧打开」换掉右栏，左栏和比例不动
	a, w = splitApp()
	a.state.Splits = map[string]splitState{"w1": {Left: "t1", Right: "t2", Percent: 30}}
	a.panes = [2]*termView{{id: "t1"}, {id: "t2"}}
	a.openBeside(w, w.Terminals[2])
	if sp := a.state.Splits["w1"]; sp.Left != "t1" || sp.Right != "t3" || sp.Percent != 30 || a.activeTerm["w1"] != "t3" {
		t.Fatalf("beside = %+v active %q", sp, a.activeTerm["w1"])
	}
}

func TestSwitchAndClosePane(t *testing.T) {
	a, _ := splitApp()
	a.state.Splits = map[string]splitState{"w1": {Left: "t1", Right: "t2"}}
	a.panes = [2]*termView{{id: "t1"}, {id: "t2"}}
	a.switchPane("w1")
	if a.pane != 1 || a.activeTerm["w1"] != "t2" || a.state.Splits["w1"].Focus != 1 {
		t.Fatalf("switch: pane %d active %q", a.pane, a.activeTerm["w1"])
	}
	a.closeSplit("w1")
	if _, ok := a.state.Splits["w1"]; ok || a.activeTerm["w1"] != "t2" {
		t.Fatal("close should drop the split and keep the focused terminal")
	}
}

// 同一个终端换到另一栏时沿用原来的视图，不重新接入（这里没有 client，重新接入就会崩）。
func TestEnsurePanesMoves(t *testing.T) {
	a, _ := splitApp()
	v1, v2 := &termView{id: "t1"}, &termView{id: "t2"}
	a.panes = [2]*termView{v1, v2}
	a.ensurePanes([2]string{"t2", "t1"})
	if a.panes[0] != v2 || a.panes[1] != v1 {
		t.Fatalf("panes = %v", a.panes)
	}
}
