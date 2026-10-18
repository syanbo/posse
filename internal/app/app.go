// Package app 是 hive 的原生窗口（MyGo 原生 UI）：侧栏列出项目和工作区，
// 右侧以标签页显示工作区里的终端。终端由常驻进程托管，窗口只接入当前可见的那一个，
// 关掉窗口或切换标签不会影响 agent。
package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"hive/internal/agent"
	"hive/internal/client"
	"hive/internal/gitx"
	"hive/internal/procmem"
)

type project struct {
	ID, Name, Path, Kind string
}

type agentState struct {
	Status, Message           string
	SessionID, TranscriptPath string
}

type terminalInfo struct {
	ID, WorkspaceID, Title, Agent string
	Dir                           string // 终端启动时的目录
	AgentState                    *agentState
	Running                       bool
	ExitCode                      *int
	Pid                           int // 终端里 shell 的进程，底栏的内存浮窗按它的进程树统计
}

// navTarget 是侧栏一行选中的东西：工作区，或其中的一个 agent 会话（term 非空）。
type navTarget struct{ ws, term string }

type workspace struct {
	ID, ProjectID, Name, Path, Kind, Branch, Project, Label string
	Terminals                                               []terminalInfo
}

type taskInfo struct {
	ID, Title, Spec, Coordinator, WorkspaceID, Worker, Agent, Status, Summary, Report string
	Workspace, WorkspaceKind, Branch, BaseRef                                         string
	CreatedAt                                                                         time.Time
	EndedAt                                                                           *time.Time
	Terminal                                                                          *terminalInfo
	Questions                                                                         int
}

type taskMessage struct {
	ID, TaskID, Kind, Body, ReplyTo string
	CreatedAt                       time.Time
}

// taskDetail 是选中任务的完整内容。
type taskDetail struct {
	taskInfo
	Messages []taskMessage
}

// worktreeDiff 是 worktree 相对基准分支的改动统计。
type worktreeDiff struct {
	Commits, Files, Insertions, Deletions, Untracked int
	Dirty                                            bool
}

type daemonStatus struct {
	FootprintKB int64
	Running     int
	Awake       *awakeStatus // 旧常驻进程没有防休眠时为空
	Version     string       `json:"version"`
	Build       string       `json:"build"` // 旧常驻进程没有这个字段时为空
	Pid         int          `json:"pid"`
	StartedAt   time.Time    `json:"startedAt"`
}

// termView 是当前接入的终端：界面侧的终端内核，它持有到常驻进程的连接。
type termView struct {
	id   string
	term *terminal.Terminal
	// dir 是 shell 报告的当前目录（OSC 7），每帧取一次：终端的回调持着终端的锁，不能再调 term.Dir
	dir string
}

type app struct {
	c   *client.Client
	win *mygo.Window

	projects   []project
	workspaces []workspace
	tasks      []taskInfo
	daemon     daemonStatus
	selfKB     int64
	termKB     int64 // 所有终端进程树的内存，底栏的总内存要加上它
	loaded     bool
	err        string

	// 由 apply、applyTasks 算好的派生数据，免得每帧重算
	byProject   map[string][]workspace // 项目 ID → 它的工作区，主工作区在前
	sorted      []taskInfo             // 任务总览的顺序，见 sortTasks
	workerTitle map[string]string      // worker 终端 ID → 任务标题（已截短），标签页上显示
	navProjects []project              // 侧栏这一帧遍历的项目，复用底层数组

	selected     string            // 侧栏选中的工作区 ID
	revealSel    bool              // 下一帧把选中项滚进可见区
	activeTerm   map[string]string // 工作区 ID → 选中的终端 ID
	sectionsOpen map[string]*bool  // 项目 ID → 侧栏分组是否展开
	navOrder     []navTarget       // 侧栏看得见、可选中的行，按显示顺序，供方向键移动
	navCur       int               // navOrder 里当前选中的行，没有时为 -1
	lastStatus   map[string]string // 终端 ID → 上次看到的 agent 状态，用于通知
	lastTask     map[string]string // 任务 ID → 上次看到的状态和待回复数，用于通知
	// 轮询上次交给界面的数据，没变就不更新界面；refreshMu 保护
	lastPoll  *pollResult
	diffAt    map[string]time.Time // worktree 工作区 ID → 上次统计改动的时间
	termAt    time.Time            // 上次统计终端内存的时间（轮询协程里用）
	lastTerm  int64
	pollForce atomic.Bool // 下一次轮询无论有没有变化都交给界面
	view      *termView
	fontSize  float32
	home      string  // $HIVE_HOME，ui.json 所在目录；为空时不保存
	state     uiState // 写进 ui.json 的界面状态

	// 任务总览
	taskHome  bool                     // 只要总览：不自动打开上次看的任务
	taskSel   string                   // 选中的任务 ID
	detail    *taskDetail              // 选中任务的消息等完整内容
	diffs     map[string]*worktreeDiff // worktree 工作区 ID → 改动统计
	replyText string
	noteText  string
	// wantTask 由界面线程写、轮询协程读：总览里正在看哪个任务
	wantMu   sync.Mutex
	wantTask string

	history     historyView
	usage       usageView             // 右侧"技能与 MCP"面板
	recent      recentView            // 右侧历史面板
	images      imagesView            // 右侧图片面板
	mobile      mobileView            // 侧栏「手机」页：配对码和已配对的手机
	mem         memView               // 底栏的内存浮窗
	emptyHeight float32               // 空工作区页滚动区上一帧的高度
	files       map[string]*filesView // 工作区 ID → 文件标签
	quick       quickOpen             // ⌘P 快速打开
	unsaved     *unsavedAsk           // 问要不要保存修改的对话框
	// 菜单的 ⌘W 记在这里，下一帧再关标签
	menuCloseTab bool
	// Git：分支弹窗、输入框对话框、Push 对话框
	branchPop     *branchPopup
	branchFolds   map[string]bool       // 分支弹窗里各组的展开状态，键是 recent、local、remote:<远端>，没有记过的用默认
	track         map[string]*trackInfo // 工作区 ID → 当前分支的传入、传出提交数，标签栏的分支按钮用
	fetchAt       map[string]time.Time  // 项目 ID → 上次自动 fetch 的时间
	filesPanelRef ui.Ref
	// 文件栏展开的进度：0 收起，1 打开，中间是动画
	filesPanelShown float32
	promptDlg       *promptState
	push            *pushState
	// 关窗口和退出的钩子里要读，用原子量
	dirtyCount atomic.Int32 // 有未保存改动的文件数，每帧更新
	closeOK    atomic.Bool  // 用户处理完未保存的改动，放行这次关闭
	quitting   atomic.Bool  // 这次关窗口是退出引起的
	pendingMu  sync.Mutex
	pending    []func() // 测试里没有窗口时，update 交来的函数
	// imagesDrawn 是这一帧画没画图片面板，没画就放掉它的位图
	imagesDrawn bool
	// linkCard 是终端里点击文件链接或路径后弹出的卡片
	linkCard *linkCard
	// linkChecks 缓存终端里的链接能不能打开（文件还在不在），悬停时每帧都要问
	linkChecks map[string]linkCheck
	// 悬停在终端里的图片链接上时预览：hoverImage 是图片路径，previews 是解码好的预览
	hoverImage     string
	previews       map[string]*ui.Bitmap
	previewLoading map[string]bool
	// lastPanel 是最后打开的右侧面板：关闭动画期间还要画它
	lastPanel string
	shownTab  string // 最近一次滚进可见范围的标签，见 revealTab
	// overPanelButton 是这一帧指针是否在标签栏的面板按钮上：在按钮上按下交给按钮切换，不算点了面板外面
	overPanelButton bool
	resizingPanel   bool    // 正在拖文件栏的左边缘，松手时写 ui.json
	resizingSide    bool    // 正在拖侧栏的右边缘，松手时写 ui.json
	sidebarShown    float32 // 侧栏滑进滑出的进度，1 是完全展开
	resizingLog     bool    // 正在拖 Git 日志浮层的上边缘，松手时写 ui.json
	rollback        rollbackDlg

	// 派发任务对话框
	dispatching bool
	dWorkspace  string // 工作区显示名
	dTitle      string
	dSpec       string
	dAgents     [nAgents]bool // 与 agentChoices 对应
	dMode       int           // 0 当前目录，1 各自新建 worktree
	dBranch     string

	// 新建工作区对话框
	creating   bool
	newProject string
	newName    string
	newAgent   int // agentNames 的序号，0 是不启动 agent
	newPrompt  string

	confirming   bool
	confirmTitle string
	confirmBody  string
	confirmVerb  string // 确认按钮的文字，说清楚会发生什么
	confirmDo    func()

	refreshMu sync.Mutex

	// 常驻进程跑的是旧包时交给本窗口的包接着跑（upgrade.go）
	upgrade upgradeState
}

// agentChoices 是可以在 hive 里启动、派发任务的 agent，菜单和选项都按这个顺序。
var agentChoices = []string{"claude", "codex", "grok", "cursor"}

// nAgents 是 agentChoices 的个数，派发对话框的勾选框按它定长。
const nAgents = 4

// agentNames 与新建工作区对话框的"同时启动"分段一一对应，0 是只建工作区。
var (
	agentNames     = append([]string{""}, agentChoices...)
	newAgentLabels = append([]string{"只建工作区"}, agentTitles(agentChoices)...)
)

func agentTitles(agents []string) []string {
	out := make([]string, len(agents))
	for i, ag := range agents {
		out[i] = agentTitle(ag)
	}
	return out
}

// taskBoardID 是侧栏里"任务"一项的 ID；工作区 ID 都以 w_ 开头，不会冲突。
const taskBoardID = "tasks"

const defaultFontSize = 14

// termFontFamily 与用户的 Ghostty 一致。中文显式回退到 PingFang SC：系统等宽字体的级联
// 会取到比例宽度的"，""。"，在两格里居中后前后各空出一大块。
const termFontFamily = "JetBrainsMono Nerd Font Mono, JetBrains Mono, PingFang SC, monospace"

// Run 打开窗口；必须在 main goroutine 调用。
func Run(c *client.Client) error {
	a := &app{
		c:            c,
		activeTerm:   map[string]string{},
		sectionsOpen: map[string]*bool{},
		lastStatus:   map[string]string{},
		lastTask:     map[string]string{},
		diffs:        map[string]*worktreeDiff{},
		fontSize:     defaultFontSize,
		home:         c.Home,
		state:        loadUIState(c.Home),
	}
	a.selected, a.revealSel = a.state.Selected, true
	if a.state.FontSize > 0 {
		a.fontSize = a.state.FontSize
	}
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:         "Hive",
			Width:         1200,
			Height:        780,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			// 红绿灯在 40 高的顶栏里上下居中，和右边的应用名、侧栏开关、标签对齐（照 Orca）
			TrafficLightPosition: &mygo.Point{X: 16, Y: 13},
			Vibrancy:             mygo.VibrancySidebar,
			Content:              ui.View(a.build),
		})
		a.watchClose()
		a.installMenu()
		mygo.App.OnNotificationClick(func(id string) {
			// 通知 ID 形如 hive:<工作区>:<终端> 或 hive-task:<任务>
			parts := strings.Split(id, ":")
			switch {
			case len(parts) == 3 && parts[0] == "hive":
				a.win.Update(func() { a.openTerminal(parts[1], parts[2]) })
			case len(parts) == 2 && parts[0] == "hive-task":
				a.win.Update(func() { a.openTask(parts[1]) })
			default:
				return
			}
			a.win.Show()
			a.win.Focus()
		})
		go a.poll()
	})
	return mygo.App.Run()
}

// poll 定期从常驻进程拉取项目、工作区和终端状态；常驻进程不在时拉起它。
func (a *app) poll() {
	for {
		a.refresh()
		a.historyTick()
		time.Sleep(time.Second)
	}
}

// pollResult 是一次轮询拿到的、界面要显示的数据。内存按界面显示的精度（MB）记，
// 只有零头变了不算变化。
type pollResult struct {
	err        string
	projects   []project
	workspaces []workspace
	tasks      []taskInfo
	running    int
	daemonMB   string
	selfMB     string
	totalMB    string
	detail     *taskDetail
	diff       *worktreeDiff
	diffFor    string
	daemonKB   int64
	selfKB     int64
	termKB     int64
	awake      *awakeStatus
	daemonInfo daemonStatus // 版本、构建、pid、启动时间，设置页的"关于"用
}

func (a *app) refresh() {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var r pollResult
	var st daemonStatus
	err := a.c.Call(ctx, "project.list", nil, &r.projects)
	if isDialError(err) {
		// 连不上才去拉起常驻进程：每秒都先探一次 socket 是白白多一次连接
		if err = a.c.EnsureDaemon(); err == nil {
			err = a.c.Call(ctx, "project.list", nil, &r.projects)
		}
	}
	if err == nil {
		err = a.c.Call(ctx, "workspace.list", nil, &r.workspaces)
		// 常驻进程只在建工作区时记分支，main 工作区切分支后就过时了；以 HEAD 为准
		for i := range r.workspaces {
			if b := gitx.Head(r.workspaces[i].Path); b != "" {
				r.workspaces[i].Branch = b
			}
		}
	}
	if err == nil {
		err = a.c.Call(ctx, "task.list", nil, &r.tasks)
	}
	if err == nil {
		err = a.c.Call(ctx, "daemon.status", nil, &st)
	}
	// 总览里选中的任务：拉完整内容，worktree 的改动统计每 5 秒更新一次
	if id := a.wantedTask(); err == nil && id != "" {
		var d taskDetail
		if a.c.Call(ctx, "task.show", map[string]any{"task": id}, &d) == nil {
			r.detail = &d
			if d.WorkspaceKind == "worktree" && time.Since(a.diffAt[d.WorkspaceID]) > 5*time.Second {
				var w worktreeDiff
				if a.c.Call(ctx, "workspace.diff", map[string]any{"workspace": d.WorkspaceID}, &w) == nil {
					if a.diffAt == nil {
						a.diffAt = map[string]time.Time{}
					}
					a.diffAt[d.WorkspaceID] = time.Now()
					r.diff, r.diffFor = &w, d.WorkspaceID
				}
			}
		}
	}
	if err != nil {
		r = pollResult{err: err.Error()}
	} else {
		for id := range a.diffAt {
			if !slices.ContainsFunc(r.workspaces, func(w workspace) bool { return w.ID == id }) {
				delete(a.diffAt, id)
			}
		}
		self := int64(procmem.Footprint(os.Getpid()) >> 10)
		// 终端的进程树每 5 秒统计一次：几十个进程，几毫秒
		if time.Since(a.termAt) > 5*time.Second {
			a.termAt, a.lastTerm = time.Now(), terminalsFootprint(r.workspaces)
		}
		r.running, r.daemonKB, r.selfKB, r.termKB, r.awake = st.Running, st.FootprintKB, self, a.lastTerm, st.Awake
		r.daemonInfo = daemonStatus{Version: st.Version, Build: st.Build, Pid: st.Pid, StartedAt: st.StartedAt}
		r.daemonMB, r.selfMB, r.totalMB = mb(st.FootprintKB), mb(self), mb(st.FootprintKB+self+a.lastTerm)
		a.autoUpgrade(r.daemonInfo)
	}
	// 改动统计只在拉到时才有，没拉到时沿用上次的，不算变化
	if r.diff == nil && a.lastPoll != nil && a.lastPoll.diffFor != "" {
		r.diff, r.diffFor = a.lastPoll.diff, a.lastPoll.diffFor
	}
	force := a.pollForce.Swap(false)
	if !force && a.lastPoll != nil && samePoll(a.lastPoll, &r) {
		return
	}
	prevErr := a.lastPoll != nil && a.lastPoll.err != ""
	a.lastPoll = &r
	a.win.Update(func() {
		if r.err != "" {
			a.err = r.err
			return
		}
		// 只清掉轮询自己报的连接错误，动作的报错留着等用户关掉
		if prevErr {
			a.err = ""
		}
		info := r.daemonInfo
		info.FootprintKB, info.Running, info.Awake = r.daemonKB, r.running, r.awake
		a.daemon, a.selfKB, a.termKB = info, r.selfKB, r.termKB
		// apply 会就地排序，r 还要留给下次比较
		a.apply(slices.Clone(r.projects), slices.Clone(r.workspaces))
		a.applyTasks(r.tasks)
		if r.detail != nil && r.detail.ID == a.taskSel {
			a.detail = r.detail
		}
		if r.diff != nil {
			a.diffs[r.diffFor] = r.diff
		}
		a.pruneDiffs()
	})
}

// samePoll 比较两次轮询界面看得到的部分；KB 精度的内存不比。
func samePoll(x, y *pollResult) bool {
	a, b := *x, *y
	a.daemonKB, a.selfKB, b.daemonKB, b.selfKB = 0, 0, 0, 0
	a.termKB, b.termKB = 0, 0
	return reflect.DeepEqual(a, b)
}

// isDialError 判断是不是连不上常驻进程的 socket（没在运行）。
func isDialError(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

func (a *app) wantedTask() string {
	a.wantMu.Lock()
	defer a.wantMu.Unlock()
	return a.wantTask
}

func (a *app) setWantedTask(id string) {
	a.wantMu.Lock()
	a.wantTask = id
	a.wantMu.Unlock()
}

// apply 在主线程更新状态，并为状态变化发通知。
func (a *app) apply(ps []project, ws []workspace) {
	sortProjects(ps, a.state.ProjectOrder)
	sort.SliceStable(ws, func(i, j int) bool {
		if ws[i].Project != ws[j].Project {
			return ws[i].Project < ws[j].Project
		}
		return ws[i].Kind == "main" && ws[j].Kind != "main"
	})
	a.projects, a.workspaces, a.loaded = ps, ws, true
	a.indexWorkspaces()
	for _, p := range ps {
		if a.sectionsOpen[p.ID] == nil {
			open := true
			a.sectionsOpen[p.ID] = &open
		}
	}
	// 只在选中的工作区被删掉时换到第一个；任务、历史、手机和设置不是工作区，要保留
	if a.selected != taskBoardID && a.selected != historyBoardID && a.selected != mobileBoardID && a.selected != settingsBoardID && a.workspace(a.selected) == nil && len(ws) > 0 {
		a.selected, a.revealSel = ws[0].ID, true
	}
	seen := map[string]bool{}
	for _, w := range ws {
		for _, t := range w.Terminals {
			a.noticeStatus(w, t)
			seen[t.ID] = true
		}
	}
	for id := range a.lastStatus {
		if !seen[id] {
			delete(a.lastStatus, id)
		}
	}
	a.pruneFiles()
}

// indexWorkspaces 按项目分好工作区，侧栏每帧要用。
func (a *app) indexWorkspaces() {
	a.byProject = make(map[string][]workspace, len(a.projects))
	for _, w := range a.workspaces {
		a.byProject[w.ProjectID] = append(a.byProject[w.ProjectID], w)
	}
}

// pruneDiffs 去掉已经不在的工作区的改动统计。
func (a *app) pruneDiffs() {
	for id := range a.diffs {
		if a.workspace(id) == nil {
			delete(a.diffs, id)
		}
	}
}

func (a *app) noticeStatus(w workspace, t terminalInfo) {
	status := ""
	if t.AgentState != nil {
		status = t.AgentState.Status
	}
	prev, seen := a.lastStatus[t.ID]
	a.lastStatus[t.ID] = status
	if !seen || prev == status || t.Agent == "" {
		return
	}
	var title string
	switch {
	case status == agent.StatusWaiting:
		title = agentTitle(t.Agent) + " 等待你处理"
	case status == agent.StatusIdle && prev == agent.StatusWorking:
		title = agentTitle(t.Agent) + " 已完成"
	default:
		return
	}
	// 正在看的终端不打扰
	if a.win.IsFocused() && a.selected == w.ID && a.view != nil && a.view.id == t.ID {
		return
	}
	body := w.Label
	if t.AgentState != nil && t.AgentState.Message != "" {
		body += "：" + truncate(t.AgentState.Message, 120)
	}
	n := mygo.NewNotification(mygo.NotificationOptions{ID: "hive:" + w.ID + ":" + t.ID, Title: title, Body: body, Group: w.ID})
	go func() {
		if err := n.Show(); err != nil {
			log.Printf("notification: %v", err)
		}
	}()
}

func (a *app) workspace(id string) *workspace {
	for i := range a.workspaces {
		if a.workspaces[i].ID == id {
			return &a.workspaces[i]
		}
	}
	return nil
}

// findTerminal 找终端和它所在的工作区，找不到返回 nil。
func (a *app) findTerminal(id string) (*workspace, *terminalInfo) {
	for i := range a.workspaces {
		w := &a.workspaces[i]
		for j := range w.Terminals {
			if w.Terminals[j].ID == id {
				return w, &w.Terminals[j]
			}
		}
	}
	return nil, nil
}

// openTerminal 选中工作区并切到其中的终端，侧栏滚到它。
func (a *app) openTerminal(workspaceID, terminalID string) {
	a.selected, a.revealSel = workspaceID, true
	a.activeTerm[workspaceID] = terminalID
}

// installMenu 换掉 mygo 的默认菜单：macOS 先把按键交给菜单，默认的"关闭窗口"占着 ⌘W，界面收不到，
// 关最后一个标签就把窗口连同 app 关了；View 菜单还占着 ⌘R、⌘=、⌘-、⌘0（字号）。⌘W 改成关闭当前标签。
func (a *app) installMenu() {
	mygo.App.SetMenu(mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleAbout},
			mygo.Separator(),
			{Label: "设置…", Accelerator: "Cmd+,", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(a.openSettings)
			}},
			mygo.Separator(),
			{Role: mygo.RoleServices},
			mygo.Separator(),
			{Role: mygo.RoleHide},
			{Role: mygo.RoleHideOthers},
			{Role: mygo.RoleUnhide},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "关闭标签", Accelerator: "Cmd+W", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(func() { a.menuCloseTab = true })
			}},
			{Role: mygo.RoleClose, Accelerator: "Cmd+Shift+W"},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "Window", Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleMinimize},
			{Role: mygo.RoleZoom},
			{Role: mygo.RoleToggleFullScreen},
			mygo.Separator(),
			{Role: mygo.RoleFront},
		}},
	}))
}

// closeTab 是 ⌘W：文件标签里关当前文件（没有文件时回到终端），否则关当前终端（先问）。
func (a *app) closeTab() {
	w := a.workspace(a.selected)
	if a.filesActive(w) {
		if v := a.filesFor(w); v.cur != nil {
			a.closeDoc(v, v.cur)
		} else {
			a.leaveFiles(w)
		}
		return
	}
	if cur := a.currentTerminal(w); cur != nil {
		a.askClose(*cur)
	}
}

// currentTerminal 返回选中工作区里应显示的终端，记住的那个不在了就换成第一个；选中文件标签时为 nil。
func (a *app) currentTerminal(w *workspace) *terminalInfo {
	if w == nil || len(w.Terminals) == 0 {
		return nil
	}
	id := a.activeTerm[w.ID]
	if id == filesTabID {
		return nil
	}
	for i := range w.Terminals {
		if w.Terminals[i].ID == id {
			return &w.Terminals[i]
		}
	}
	a.activeTerm[w.ID] = w.Terminals[0].ID
	return &w.Terminals[0]
}

func (a *app) termFont() terminal.Font {
	return terminal.Font{Family: termFontFamily, Size: a.fontSize}
}

// ensureView 让接入的终端与选中的一致：切换时断开旧的（释放界面侧内存），接入新的。
func (a *app) ensureView(id string) {
	if a.view != nil && a.view.id == id {
		return
	}
	if a.view != nil {
		a.view.term.Close()
		a.view = nil
	}
	// 悬停预览只对这个终端里的链接有用，换终端就放掉
	a.linkCard, a.hoverImage, a.previews = nil, "", nil
	if id == "" {
		return
	}
	conn := a.c.Attach(id)
	v := &termView{id: id}
	light, dark := terminalThemes()
	t, err := terminal.New(terminal.Options{
		Conn:      conn,
		Font:      a.termFont(),
		Theme:     light,
		DarkTheme: dark,
		// 回滚区与常驻进程一致，接入时的快照不会超出
		Scrollback: 4 << 20,
		// Cmd+点击：网址交给系统用默认浏览器打开，路径在访达里打开
		OpenLink:    func(link string) bool { return a.openLink(v, link) },
		LinkUsable:  func(link string) bool { return a.linkUsable(v, link) },
		OnHoverLink: func(link string) { a.onHoverLink(v, link) },
		OnKey:       func(mods ui.Modifiers, key ui.Key) bool { return a.termKey(v, id, mods, key) },
		// 剪贴板里只有图片时 Cmd+V 拿不到文本：和 Orca 一样，由 Hive 把图片存成临时文件、粘贴它的路径，
		// 所有 agent 终端都这样。Claude 和 Codex 会把粘贴的图片路径当成附件，其他 agent 能按路径读图。
		// 让它们收 Ctrl+V 自己读剪贴板也行，但读剪贴板要调系统命令，粘贴时界面会闪一下
		OnPaste: func(text string) bool {
			if text != "" || v.term == nil {
				return false
			}
			if _, t := a.findTerminal(id); t == nil || t.Agent == "" {
				return false
			}
			png := mygo.Clipboard.ReadImage()
			if len(png) == 0 {
				// 不是图片（比如复制的是文件），交给程序自己处理
				v.term.Send([]byte{0x16})
				return true
			}
			go a.pasteImage(v, png)
			return true
		},
		// 连接结束：程序退出时刷新列表；程序仍在运行（被判定跟不上而断开）时重新接入
		OnExit: func(int) {
			a.win.Update(func() {
				if a.view == v {
					if exited, _ := conn.Exited(); !exited {
						a.view.term.Close()
						a.view = nil
					}
				}
			})
			go a.refresh()
		},
	})
	if err != nil {
		a.err = err.Error()
		return
	}
	v.term = t
	a.view = v
}

// saveState 在后台写 ui.json；界面状态丢了不影响使用，失败不提示。
func (a *app) saveState() {
	if a.home == "" {
		return
	}
	s := a.state
	s.ProjectOrder = slices.Clone(s.ProjectOrder)
	s.MobileAddresses = slices.Clone(s.MobileAddresses)
	s.Files = cloneFilesState(s.Files)
	go saveUIState(a.home, s)
}

// pasteImage 把剪贴板里的图片存成临时文件，再把路径粘贴进终端。
func (a *app) pasteImage(v *termView, png []byte) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("hive-paste-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(path, png, 0o600); err != nil {
		a.win.Update(func() { a.err = "粘贴图片失败：" + err.Error() })
		return
	}
	a.win.Update(func() {
		if a.view == v {
			v.term.Paste(path)
		}
	})
}

func (a *app) setFontSize(size float32) {
	a.fontSize = min(max(size, minFontSize), maxFontSize)
	if a.view != nil {
		a.view.term.SetFont(a.termFont())
	}
	if a.state.FontSize != a.fontSize {
		a.state.FontSize = a.fontSize
		a.saveState()
	}
}

const minFontSize, maxFontSize = 9, 28

// --- 动作：都在后台 goroutine 里调用常驻进程，完成后刷新

// call 调用常驻进程，出错时显示在侧栏的提示里。
func (a *app) call(method string, params any, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := a.c.Call(ctx, method, params, out)
	if err != nil {
		a.win.Update(func() { a.err = err.Error() })
	}
	return err
}

func (a *app) startTerminal(workspaceID, agent string) {
	go func() {
		var t terminalInfo
		if a.call("terminal.create", map[string]any{"workspace": workspaceID, "agent": agent}, &t, 10*time.Second) == nil {
			a.win.Update(func() { a.openTerminal(workspaceID, t.ID) })
		}
		a.refresh()
	}()
}

func (a *app) askClose(t terminalInfo) {
	if !t.Running {
		go a.closeTerminal(t.ID)
		return
	}
	name := agentTitle(t.Agent)
	if name == "" {
		name = "终端"
	}
	a.confirm("关闭 "+name+"？", "里面正在运行的程序会被结束。", "关闭", func() { a.closeTerminal(t.ID) })
}

func (a *app) closeTerminal(id string) {
	a.call("terminal.close", map[string]any{"terminal": id}, nil, 10*time.Second)
	a.refresh()
}

func (a *app) askRemoveWorkspace(w workspace) {
	body := "工作区里的终端会被关闭。"
	if w.Kind == "worktree" {
		body = "会关闭终端并删除 git worktree 目录（分支保留）。有未提交的改动时会拒绝删除。"
	}
	a.confirm("删除工作区 "+displayName(&w)+"？", body, "删除", func() {
		a.call("workspace.rm", map[string]any{"workspace": w.ID, "force": w.Kind != "worktree"}, nil, 30*time.Second)
		a.refresh()
	})
}

// askRemoveProject 从 Hive 里移除 main 工作区 w 所在的项目：只删记录，不碰磁盘上的文件。
// 常驻进程要求先删掉 worktree 工作区，这里先挡住并说明要删哪些。
func (a *app) askRemoveProject(w workspace) {
	var worktrees []string
	for _, x := range a.byProject[w.ProjectID] {
		if x.Kind == "worktree" {
			worktrees = append(worktrees, x.Name)
		}
	}
	if len(worktrees) > 0 {
		a.err = "先删除项目 " + w.Project + " 里的 worktree 工作区：" + strings.Join(worktrees, "、")
		return
	}
	a.confirm("从 Hive 移除项目 "+w.Project+"？", "只从 Hive 里移除，磁盘上的文件不会删除。项目里运行中的终端会被关闭。", "移除", func() {
		a.call("project.rm", map[string]any{"project": w.ProjectID, "force": true}, nil, 30*time.Second)
		a.refresh()
	})
}

func (a *app) confirm(title, body, verb string, do func()) {
	a.confirmTitle, a.confirmBody, a.confirmVerb, a.confirmDo = title, body, verb, do
	a.confirming = true
}

func (a *app) addProject() {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: "添加项目", ButtonLabel: "添加", Directory: true})
	if err != nil || len(paths) == 0 {
		return
	}
	if a.call("project.add", map[string]any{"path": paths[0]}, nil, 10*time.Second) != nil {
		return
	}
	a.refresh()
}

func (a *app) openNewWorkspace() {
	a.creating = true
	a.newName, a.newPrompt, a.newAgent = "", "", 1
	if w := a.workspace(a.selected); w != nil {
		a.newProject = w.Project
	} else if len(a.projects) > 0 {
		a.newProject = a.projects[0].Name
	}
}

func (a *app) createWorkspace(params map[string]any) {
	var w struct {
		ID        string
		Terminals []terminalInfo
		Setup     *struct {
			OK     bool
			Output string
		}
	}
	if a.call("workspace.create", params, &w, 2*time.Minute) == nil {
		a.win.Update(func() {
			if w.Setup != nil && !w.Setup.OK {
				a.err = "setup 脚本失败：" + truncate(w.Setup.Output, 200)
			}
			a.selected, a.revealSel = w.ID, true
			if len(w.Terminals) > 0 {
				a.openTerminal(w.ID, w.Terminals[0].ID)
			}
		})
	}
	a.refresh()
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
