package app

import (
	"os"
	"slices"
	"testing"
	"time"
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

// 有多个工作区的项目默认收起：项目行选中主工作区，展开箭头点开后才看到其余工作区。
func TestProjectExpand(t *testing.T) {
	a := previewApp(t)
	a.sectionsOpen = map[string]*bool{}
	tt := newPreviewTester(a, true)
	if _, ok := tt.Find("feat-login"); ok {
		t.Fatal("worktree row shown while project is collapsed")
	}
	// 单击项目行：选中主工作区并展开
	if err := tt.Click("Skill"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w5" || !*a.sectionsOpen["p5"] {
		t.Fatalf("after click: selected %q open %v, want w5 open", a.selected, *a.sectionsOpen["p5"])
	}
	// 已选中并展开时再单击收起
	if err := tt.Click("Skill"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if *a.sectionsOpen["p5"] {
		t.Fatal("second click did not collapse")
	}
	// 箭头只展开收起，不改选中
	a.selected = "w2"
	tt.Frame()
	if err := tt.Click("展开 2 个工作区"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w2" || !*a.sectionsOpen["p5"] {
		t.Fatalf("after toggle: selected %q open %v", a.selected, *a.sectionsOpen["p5"])
	}
	// ios-crash 没有终端，选中它不会去接入常驻进程
	if err := tt.Click("ios-crash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w9" {
		t.Fatalf("selected %q, want w9", a.selected)
	}
}

// 右侧面板：点外面收起；点面板里面不收；开着时点标签栏的按钮是收起，不会收起又打开。
func TestRightPanelDismiss(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	a.selected = "w1"
	open := func() {
		a.state.RightPanel = panelHistory
		a.recent = recentView{key: "0\x00p1", loaded: true, poll: panelPoll{at: time.Now()}, sessions: a.history.sessions}
	}
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
	// 面板里列表下面的空白处（窗口 1200×760，面板贴右边 280 宽）
	tt.ClickAt(1050, 720)
	tt.Frame()
	if a.state.RightPanel != panelHistory {
		t.Fatal("click inside closed the panel")
	}

	b, ok := tt.Find("历史会话")
	if !ok {
		t.Fatal("panel button missing")
	}
	tt.Move(b.X+b.W/2, b.Y+b.H/2)
	tt.Frame()
	tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
	tt.Frame()
	tt.Frame()
	if a.state.RightPanel != "" {
		t.Fatalf("panel button left the panel %q", a.state.RightPanel)
	}
}
