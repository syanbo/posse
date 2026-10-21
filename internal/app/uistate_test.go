package app

import (
	"os"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func ids(ps []project) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.ID
	}
	return out
}

func TestMoveProject(t *testing.T) {
	ps := []project{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}}
	for _, tc := range []struct {
		id, target string
		want       []string
	}{
		{"a", "c", []string{"b", "c", "a", "d"}}, // 往下拖落在目标后面
		{"d", "b", []string{"a", "d", "b", "c"}}, // 往上拖落在目标前面
		{"a", "d", []string{"b", "c", "d", "a"}}, // 能拖到最后
		{"d", "a", []string{"d", "a", "b", "c"}}, // 能拖到最前
		{"b", "b", []string{"a", "b", "c", "d"}},
	} {
		if got := moveProject(ps, tc.id, tc.target); !slices.Equal(got, tc.want) {
			t.Errorf("move %s to %s = %v, want %v", tc.id, tc.target, got, tc.want)
		}
	}
}

func TestSortProjects(t *testing.T) {
	ps := []project{{ID: "1", Name: "beta"}, {ID: "2", Name: "Alpha"}, {ID: "3", Name: "gamma"}, {ID: "4", Name: "delta"}}
	// 排过序的在前，新加的按名称排在后面
	sortProjects(ps, []string{"3", "1", "gone"})
	if got := ids(ps); !slices.Equal(got, []string{"3", "1", "2", "4"}) {
		t.Fatalf("order = %v", got)
	}
}

func TestUIStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	if s := loadUIState(home); s.Selected != "" || s.ProjectOrder != nil {
		t.Fatalf("missing file should load empty, got %+v", s)
	}
	want := uiState{Selected: "w_1", ProjectOrder: []string{"p2", "p1"}}
	if err := saveUIState(home, want); err != nil {
		t.Fatal(err)
	}
	got := loadUIState(home)
	if got.Selected != want.Selected || !slices.Equal(got.ProjectOrder, want.ProjectOrder) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// 在侧栏里把 Server 拖到 Apps 上，顺序和 ui.json 都要变；选中项变化也要写进 ui.json。
func TestSidebarReorderAndRemember(t *testing.T) {
	home := t.TempDir()
	a := previewApp(t)
	a.home = home
	tt := newPreviewTester(a, true)
	from, ok := tt.Find("Server")
	if !ok {
		t.Fatal("Server row not found")
	}
	to, ok := tt.Find("Apps")
	if !ok {
		t.Fatal("Apps row not found")
	}
	tt.Press(from.X+from.W/2, from.Y+from.H/2)
	tt.Frame()
	for i := 1; i <= 6; i++ {
		y := from.Y + from.H/2 + (to.Y-from.Y)*float32(i)/6
		tt.Move(from.X+from.W/2, y)
		tt.Frame()
	}
	tt.Release(to.X+to.W/2, to.Y+to.H/2)
	tt.Frame()
	tt.Frame()
	if got := ids(a.projects)[:4]; !slices.Equal(got, []string{"p4", "p1", "p2", "p3"}) {
		t.Fatalf("order after drag = %v", ids(a.projects))
	}
	waitState(t, home, func(s uiState) bool { return len(s.ProjectOrder) > 0 && s.ProjectOrder[0] == "p4" })

	if err := tt.Click("Chat"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	waitState(t, home, func(s uiState) bool { return s.Selected == "w2" })
}

// waitState 等后台写完 ui.json。
func waitState(t *testing.T, home string, ok func(uiState) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(uiStatePath(home)); err == nil && ok(loadUIState(home)) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("ui.json = %+v", loadUIState(home))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// 侧栏最多三层：项目、工作区（只有主工作区时省掉）、agent 会话；shell 不进侧栏。
func TestSidebarTree(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.sectionsOpen = map[string]*bool{}
	tt := newPreviewTester(a, true)
	for _, label := range []string{"重写 hive 界面", "feat-login", "ios-crash"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("%q not shown", label)
		}
	}
	// 标签栏里有 zsh，只能从侧栏的行里看
	for _, n := range a.navOrder {
		if n.term == "t3" {
			t.Fatal("shell terminal shown in sidebar")
		}
	}
	// 单击会话切到这个终端。测试里没有常驻进程，先让界面以为已经接入了它，免得去接入
	// Apps 下面的第一个 Codex 是 t2（feat-login 里的 t5 排在后面）
	a.view.id = "t2"
	if err := tt.Click("Codex"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w1" || a.activeTerm["w1"] != "t2" {
		t.Fatalf("after click: selected %q term %q", a.selected, a.activeTerm["w1"])
	}
	// 项目标题行只展开收起，不改选中
	if err := tt.Click("Skill"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.selected != "w1" || *a.sectionsOpen["p5"] {
		t.Fatalf("after header click: selected %q open %v", a.selected, *a.sectionsOpen["p5"])
	}
	if _, ok := tt.Find("feat-login"); ok {
		t.Fatal("worktree row shown while project is collapsed")
	}
	if err := tt.Click("展开 3 个工作区"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	// ios-crash 没有终端，选中它不会去接入常驻进程
	if err := tt.Click("ios-crash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w9" {
		t.Fatalf("selected %q, want w9", a.selected)
	}
	// 方向键：从 ios-crash 往上是 feat-login 里的会话
	a.navCur = -1
	tt.Frame()
	if a.navCur < 0 || a.navOrder[a.navCur] != (navTarget{ws: "w9"}) {
		t.Fatalf("navCur %d not on w9", a.navCur)
	}
	if prev := a.navOrder[a.navCur-1]; prev != (navTarget{ws: "w6", term: "t5"}) {
		t.Fatalf("row above ios-crash = %+v", prev)
	}
}

// 侧栏：拖右边缘调宽（有上下限），开关和 ⌘B 收起展开，都记进 ui.json。
func TestSidebarToggleAndResize(t *testing.T) {
	home := t.TempDir()
	a := previewApp(t)
	a.home = home
	tt := newPreviewTester(a, true)
	x := float32(sidebarDefault)
	tt.Move(x, 400)
	tt.Press(x, 400)
	tt.Move(x+60, 400)
	tt.Move(x+80, 400)
	tt.Frame()
	tt.Release(x+80, 400)
	tt.Frame()
	if a.state.SidebarWidth != sidebarDefault+80 {
		t.Fatalf("width after drag = %v", a.state.SidebarWidth)
	}
	waitState(t, home, func(s uiState) bool { return s.SidebarWidth == sidebarDefault+80 })
	// 拖得太窄停在下限
	x = a.sidebarWidth()
	tt.Move(x, 400)
	tt.Press(x, 400)
	tt.Move(x-400, 400)
	tt.Frame()
	tt.Release(x-400, 400)
	tt.Frame()
	if a.sidebarWidth() != sidebarMin {
		t.Fatalf("width after narrow drag = %v", a.sidebarWidth())
	}

	if err := tt.Click("收起侧栏 ⌘B"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if !a.state.SidebarHidden {
		t.Fatal("sidebar not hidden")
	}
	if _, ok := tt.Find("任务"); ok {
		t.Fatal("sidebar rows shown while hidden")
	}
	waitState(t, home, func(s uiState) bool { return s.SidebarHidden })
	tt.Key(ui.Cmd, ui.KeyB)
	tt.Frame()
	if a.state.SidebarHidden {
		t.Fatal("⌘B did not show the sidebar")
	}
	if _, ok := tt.Find("收起侧栏 ⌘B"); !ok {
		t.Fatal("toggle missing after showing sidebar")
	}
}

// settle 多画几帧，等滑进滑出的动画（200ms）走完。
func settle(tt *ui.Tester) {
	for range 4 {
		tt.Frame()
		time.Sleep(80 * time.Millisecond)
	}
	tt.Frame()
}

// 右侧面板：点外面收起；点面板里面不收。
func TestRightPanelDismiss(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	a.selected = "w1"
	open := func() { a.state.RightPanel = panelUsage }
	open()
	tt := newPreviewTester(a, true)

	// 终端区域的左上角，离面板很远
	tt.ClickAt(500, 200)
	tt.Frame()
	if a.state.RightPanel != "" {
		t.Fatal("outside click did not close the panel")
	}

	open()
	tt.Frame()
	// 面板里列表下面的空白处（窗口 1200×760，面板贴右边 280 宽，底下是 34 高的状态栏）
	tt.ClickAt(1050, 660)
	tt.Frame()
	if a.state.RightPanel != panelUsage {
		t.Fatal("click inside closed the panel")
	}
}
