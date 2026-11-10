package app

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/client"
	"posse/internal/rpc"
	"posse/locales"
)

func TestApplyKeepsSelection(t *testing.T) {
	ps := []project{{ID: "p1", Name: "Apps"}}
	ws := []workspace{{ID: "w_1", ProjectID: "p1", Project: "Apps", Kind: "main"}}
	for sel, want := range map[string]string{
		taskBoardID:    taskBoardID,
		historyBoardID: historyBoardID,
		mobileBoardID:  mobileBoardID,
		"w_1":          "w_1",
		"w_gone":       "w_1",
	} {
		a := &app{sectionsOpen: map[string]*bool{}, lastStatus: map[string]string{}, selected: sel}
		a.apply(ps, ws)
		if a.selected != want {
			t.Errorf("selected %q after refresh = %q, want %q", sel, a.selected, want)
		}
		if !*a.sectionsOpen["p1"] {
			t.Errorf("new section starts collapsed")
		}
	}
}

// 点侧栏「任务」回到总览，不留在上次打开的任务里；↑↓ 换选中的任务。
// worker 终端右上角的会话按钮里有「返回任务」。
func TestTaskHome(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.taskSel = "k2"
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	if err := tt.Click("任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != taskBoardID || !a.taskHome || a.taskSel != "" {
		t.Fatalf("after sidebar: selected %q home %v sel %q", a.selected, a.taskHome, a.taskSel)
	}
	tt.Frame()
	if a.taskSel != "" || !a.taskHome {
		t.Fatalf("overview reselected %q home %v", a.taskSel, a.taskHome)
	}
	if _, ok := tt.Find("把活儿派给其他 agent，在这里看进度、回答它们的问题、合入结果。从左边选一个任务看详情。"); !ok {
		t.Fatal("overview hint missing")
	}

	// ↑↓ 换选中的任务：总览里下键选第一个，到头不动
	key := func(k ui.Key) {
		tt.Key(0, k)
		tt.Frame()
	}
	key(ui.KeyDown)
	if a.taskHome || a.taskSel != a.sorted[0].ID {
		t.Fatalf("↓ from overview: home %v sel %q", a.taskHome, a.taskSel)
	}
	key(ui.KeyDown)
	if a.taskSel != a.sorted[1].ID {
		t.Fatalf("↓: sel %q want %q", a.taskSel, a.sorted[1].ID)
	}
	key(ui.KeyUp)
	key(ui.KeyUp)
	if a.taskSel != a.sorted[0].ID {
		t.Fatalf("↑ past the top: sel %q", a.taskSel)
	}
	// 写了一半的草稿、开着的确认框：方向键不换任务
	for _, block := range []func(on bool){
		func(on bool) { a.noteText = map[bool]string{true: "还没发"}[on] },
		func(on bool) { a.confirming = on },
	} {
		block(true)
		key(ui.KeyDown)
		block(false)
		tt.Frame()
		tt.Frame()
		if a.taskSel != a.sorted[0].ID {
			t.Fatalf("↓ changed the task to %q while blocked", a.taskSel)
		}
	}
	// 拦着的都去掉后照常能换（证明上面没换不是因为焦点还在回复框里）
	key(ui.KeyDown)
	if a.taskSel != a.sorted[1].ID {
		t.Fatalf("↓ after unblocking: sel %q", a.taskSel)
	}
	key(ui.KeyUp)
	// 焦点在回复框里时 ↑↓ 归输入框，不换任务
	if !needsUser(a.sorted[0]) {
		t.Fatalf("first task %q should be waiting for a reply", a.sorted[0].ID)
	}
	a.detail = &taskDetail{taskInfo: a.sorted[0], Messages: []taskMessage{{ID: "m1", Kind: "question", Body: "要不要补用例？", CreatedAt: time.Now()}}}
	tt.Frame()
	if err := tt.Click("回答 worker 的提问"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	key(ui.KeyDown)
	if a.taskSel != a.sorted[0].ID {
		t.Fatalf("↓ in the reply box changed the task to %q", a.taskSel)
	}
	// 空的单行补充说明框有焦点时也不换（单行输入框本身不用 ↑↓，键会落到页面上）
	a.detail = &taskDetail{taskInfo: a.sorted[0]}
	a.detail.Questions = 0
	tt.Frame()
	if err := tt.Click("补充说明：worker 会在下一个检查点读到"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	key(ui.KeyDown)
	if a.taskSel != a.sorted[0].ID {
		t.Fatalf("↓ in the empty note box changed the task to %q", a.taskSel)
	}
	// 详情上面不再有「总览」按钮
	if _, ok := tt.Find("总览"); ok {
		t.Fatal("overview button still shown")
	}
}

// worker 终端右上角的按钮：agent 派发的回到派发方的会话，用户派发的回任务页。
func TestWorkerReturnsToTask(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	// t6 是 t1 派发的 k1 的 worker
	a.activeTerm["w1"] = "t6"
	a.panes[0].id = "t6"
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	if _, ok := tt.Find("返回任务"); ok {
		t.Fatal("agent-dispatched worker should not offer the task page")
	}
	if err := tt.Click("返回派发方"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != "w1" || a.activeTerm["w1"] != "t1" {
		t.Fatalf("return to dispatcher: selected %q term %q", a.selected, a.activeTerm["w1"])
	}

	// 用户派发的：没有派发方会话，回任务页
	a.tasks[0].Coordinator = ""
	a.indexTasks()
	a.activeTerm["w1"] = "t6"
	tt.Frame()
	if _, ok := tt.Find("返回派发方"); ok {
		t.Fatal("user-dispatched worker should not offer a dispatcher")
	}
	if err := tt.Click("返回任务"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.selected != taskBoardID {
		t.Fatalf("return to task: selected %q", a.selected)
	}
}

func TestRevealOpensSection(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	// 刚启动时分组都展开，会话都看得见
	a.selected, a.sectionsOpen = "", map[string]*bool{}
	a.apply(a.projects, a.workspaces)
	newTester(a.build, 1200, 760).Frame()
	for id, open := range a.sectionsOpen {
		if !*open {
			t.Fatalf("section %s collapsed after launch", id)
		}
	}
	// 只有主工作区的项目：定位到文件时项目行就看得见，不用展开；定位到会话（shell 也在侧栏里）才展开
	*a.sectionsOpen["p1"] = false
	a.selected, a.revealSel = "w1", true
	a.showFiles(a.workspace("w1"))
	newTester(a.build, 1200, 760).Frame()
	if *a.sectionsOpen["p1"] {
		t.Fatal("revealing the project row opened it")
	}
	a.activeTerm["w1"], a.revealSel = "t3", true
	newTester(a.build, 1200, 760).Frame()
	if !*a.sectionsOpen["p1"] {
		t.Fatal("revealing a session did not open its project")
	}
	// 最后再切到没有终端的 w8（会断开接入的终端）。w8 是 Works（p6）下面的工作区，收起时定位要展开，Skill（p5）不受影响
	*a.sectionsOpen["p6"], *a.sectionsOpen["p5"] = false, false
	a.selected, a.revealSel = "w8", true
	newTester(a.build, 1200, 760).Frame()
	if !*a.sectionsOpen["p6"] || *a.sectionsOpen["p5"] || a.revealSel {
		t.Fatalf("after reveal: p6=%v p5=%v revealSel=%v", *a.sectionsOpen["p6"], *a.sectionsOpen["p5"], a.revealSel)
	}
	// 用户收起分组后，不定位就不再自动展开
	*a.sectionsOpen["p6"] = false
	newTester(a.build, 1200, 760).Frame()
	if *a.sectionsOpen["p6"] {
		t.Fatal("section reopened without reveal")
	}
}

// offlineClient 让界面换终端时去接入一个不存在的常驻进程：接入失败，但不会因为没有客户端崩溃。
func offlineClient(t *testing.T, a *app) {
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "none.sock")}
}

func TestShortModel(t *testing.T) {
	for id, want := range map[string]string{
		"claude-opus-5-5":            "Opus 5.5",
		"claude-sonnet-4-5-20250929": "Sonnet 4.5",
		"claude-3-5-sonnet-20241022": "Sonnet 3.5",
		"claude-fable-5-1":           "Fable 5.1",
		"gpt-6-astra":                "GPT-6 Astra",
		"gpt-5.1-codex":              "GPT-5.1 Codex",
		"o3":                         "o3",
		"grok-4.7-build":             "Grok 4.7",
		"grok-code-fast-1":           "Grok Code Fast 1",
	} {
		if got := shortModel(id); got != want {
			t.Errorf("shortModel(%q) = %q, want %q", id, got, want)
		}
	}
}

// setHome 在测试期间换掉缓存的主目录。
func setHome(t *testing.T, home string) {
	old := homeDir
	homeDir = home
	t.Cleanup(func() { homeDir = old })
}

// 常驻进程不在时 socket 连不上，轮询据此才去拉起它。
func TestIsDialError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := rpc.Call(ctx, filepath.Join(t.TempDir(), "possed.sock"), "project.list", nil, nil)
	if !isDialError(err) {
		t.Fatalf("missing socket: %v is not a dial error", err)
	}
	if isDialError(context.DeadlineExceeded) {
		t.Fatal("timeout counted as dial error")
	}
}

// 常驻进程没报装了哪些时全列；报了就只列这些，一个都没装时为空。
func TestAgentChoices(t *testing.T) {
	a := &app{}
	if got := a.agentChoices(); !slices.Equal(got, agent.Known) {
		t.Fatalf("unknown: %v", got)
	}
	a.daemon.Agents = []string{"codex"}
	if got := a.newAgentChoices(); !slices.Equal(got, []string{"", "codex"}) {
		t.Fatalf("codex only: %v", got)
	}
	a.daemon.Agents = []string{}
	if got := a.agentChoices(); len(got) != 0 {
		t.Fatalf("none: %v", got)
	}
}

// 引用变了要重读的 diff：HEAD、分支名会随 fetch 移动，提交哈希不会。
func TestDiffFixedRevs(t *testing.T) {
	h := "0123456789abcdef0123456789abcdef01234567"
	for _, c := range []struct {
		s    diffSpec
		want bool
	}{
		{diffSpec{oldRev: h + "^", newRev: h}, true},
		{diffSpec{oldRev: h + "~2", newRev: h}, true},
		{diffSpec{oldRev: "", newRev: h}, true},
		{diffSpec{oldRev: h}, true},
		{diffSpec{oldRev: "HEAD"}, false},
		{diffSpec{oldRev: "origin/main"}, false},
		{diffSpec{oldRev: h, newRev: "feature"}, false},
		{diffSpec{oldRev: "abc1234", newRev: h}, false},
	} {
		if got := c.s.fixedRevs(); got != c.want {
			t.Errorf("%+v fixedRevs = %v, want %v", c.s, got, c.want)
		}
	}
}

func TestVersionLabel(t *testing.T) {
	local := time.Local
	time.Local = time.FixedZone("CST", 8*3600)
	defer func() { time.Local = local }()
	for build, want := range map[string]string{
		"":                              "0.1.0",
		"663c0c7":                       "0.1.0（663c0c7）",
		"663c0c7 2026-10-18T05:18:04Z":  "0.1.0（663c0c7，10-18 13:18 构建）",
		"663c0c7+ 2026-10-18T05:18:04Z": "0.1.0（663c0c7，有未提交改动，10-18 13:18 构建）",
		" 2026-10-18T05:18:04Z":         "0.1.0（10-18 13:18 构建）",
	} {
		if got := versionLabel(locales.In("zh-Hans"), "0.1.0", build); got != want {
			t.Errorf("versionLabel(%q) = %q, want %q", build, got, want)
		}
	}
}

// 侧栏：任务的 worker 排在派发它的会话下面；派发方在等任务时状态是"等待任务"，自己在干别的时照常。
func TestSidebarTaskTree(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	var order []string
	for _, n := range a.navOrder {
		if n.ws == "w1" && n.term != "" {
			order = append(order, n.term)
		}
	}
	// t6 是 t1 派发的 worker，排在 t1 后面、t2 前面
	if i := slices.Index(order, "t1"); i < 0 || i+1 >= len(order) || order[i+1] != "t6" {
		t.Fatalf("sidebar order %v", order)
	}
	t1 := a.workspaces[0].Terminals[0]
	t1.AgentState = &agentState{Status: "working"}
	if got := a.sessionStatus(t1); got != "working" {
		t.Fatalf("working coordinator: %q", got)
	}
	t1.WaitingTasks = true
	if got := a.sessionStatus(t1); got != statusTasks {
		t.Fatalf("waiting coordinator: %q", got)
	}
	t1.WaitingTasks, t1.AgentState = false, &agentState{Status: "idle"}
	if got := a.sessionStatus(t1); got != statusTasks {
		t.Fatalf("idle coordinator with running tasks: %q", got)
	}

	// 正在看 t1 时再单击它的行，收起它的 worker：t6 不再列出，行尾写 +1；t6 等你处理时 t1 这一行显示待处理
	clickT1 := func() {
		t.Helper()
		r, ok := tt.Find("重写 posse 界面")
		if !ok || r.X > 300 {
			t.Fatalf("t1 row not found in the sidebar: %v %v", r, ok)
		}
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
		tt.Frame()
	}
	a.selected, a.activeTerm["w1"] = "w1", "t1"
	tt.Frame()
	if _, ok := tt.Find("收起 1 个任务 worker"); ok {
		t.Fatal("the worker group should have no arrow button")
	}
	clickT1()
	terms := func() (out []string) {
		for _, n := range a.navOrder {
			if n.ws == "w1" && n.term != "" {
				out = append(out, n.term)
			}
		}
		return out
	}
	if got := terms(); slices.Contains(got, "t6") || !slices.Contains(got, "t1") {
		t.Fatalf("folded order %v", got)
	}
	if _, ok := tt.Find("+1"); !ok {
		t.Fatal("folded coordinator should show +1")
	}
	ws := &a.workspaces[0]
	t6 := slices.IndexFunc(ws.Terminals, func(x terminalInfo) bool { return x.ID == "t6" })
	ws.Terminals[t6].AgentState = &agentState{Status: "waiting"}
	tt.Frame()
	if r, ok := tt.Find("待处理"); !ok || r.X > 300 {
		t.Fatalf("folded coordinator should surface the worker's pending state: %v %v", r, ok)
	}
	// 收起时当前会话是 t6：t1 这一行算选中
	a.selected, a.activeTerm["w1"] = "w1", "t6"
	tt.Frame()
	if a.navCur < 0 || a.navOrder[a.navCur].term != "t1" {
		t.Fatalf("selection should land on the folded coordinator, got %+v", a.navOrder[a.navCur])
	}
	// 没在看 t1 时单击：切到 t1，顺便展开
	clickT1()
	if a.activeTerm["w1"] != "t1" {
		t.Fatalf("click should switch to t1, got %q", a.activeTerm["w1"])
	}
	if got := terms(); !slices.Contains(got, "t6") {
		t.Fatalf("expanded order %v", got)
	}
}

// 会话按钮收起成右边缘的把手，点把手展开；状态记进 ui.json。
func TestSessionButtonsFold(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	tt := newTester(a.build, 1200, 760)
	settle(tt)
	if _, ok := tt.Find("这个会话里生成和粘贴的图片"); !ok {
		t.Fatal("session buttons missing")
	}
	if err := tt.Click("收起会话按钮"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if !a.state.SessionButtonsHidden {
		t.Fatal("fold not saved")
	}
	if _, ok := tt.Find("这个会话里生成和粘贴的图片"); ok {
		t.Fatal("buttons still shown after folding")
	}
	if err := tt.Click("展开会话按钮"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.state.SessionButtonsHidden {
		t.Fatal("handle should unfold")
	}
	if _, ok := tt.Find("这个会话里生成和粘贴的图片"); !ok {
		t.Fatal("buttons missing after unfolding")
	}
}

// 手机占着终端尺寸时，终端上方有提示和"改回电脑尺寸"，点了提示条就收起。
func TestPhoneSizedBar(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	a.workspaces[0].Terminals[0].PhoneSized = true
	a.selected, a.activeTerm["w1"] = "w1", a.workspaces[0].Terminals[0].ID
	tt := newTester(a.build, 1200, 760)
	settle(tt)
	if !tt.HasText("手机正在看这个终端，按手机屏幕排版") {
		t.Fatalf("bar missing: %v", tt.Texts())
	}
	if err := tt.Click("改回电脑尺寸"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	// 测试里没有常驻进程，错误条上会有"改回电脑尺寸失败"，看提示条本身
	if a.workspaces[0].Terminals[0].PhoneSized || tt.HasText("手机正在看这个终端，按手机屏幕排版") {
		t.Fatal("bar should go away after reclaiming")
	}
}

func TestLaunchColumns(t *testing.T) {
	for _, tc := range []struct {
		n     int
		width float32
		want  int
	}{
		{5, 600, 5}, // 放得下就一行
		{5, 400, 3}, // 一行最多 3 个：分成 3+2
		{5, 300, 2}, // 一行最多 2 个：2+2+1
		{5, 50, 1},  // 比一格还窄也至少一列
		{4, 250, 2}, // 一行最多 2 个：2+2
	} {
		if got := launchColumns(tc.n, tc.width, 96); got != tc.want {
			t.Errorf("launchColumns(%d, %v) = %d, want %d", tc.n, tc.width, got, tc.want)
		}
	}
}
