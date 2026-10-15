// Package app 是 hive 的原生窗口（MyGo 原生 UI）：侧栏列出项目和工作区，
// 右侧以标签页显示工作区里的终端。终端由常驻进程托管，窗口只接入当前可见的那一个，
// 关掉窗口或切换标签不会影响 agent。
package app

import (
	"context"
	"fmt"
	"log"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"hive/internal/client"
	"hive/internal/procmem"
)

type project struct {
	ID, Name, Path, Kind string
}

type agentState struct {
	Status, Event, Message    string
	SessionID, TranscriptPath string
}

type terminalInfo struct {
	ID, WorkspaceID, Title, Agent string
	Dir                           string // 终端启动时的目录
	AgentState                    *agentState
	Running                       bool
	ExitCode                      *int
}

type workspace struct {
	ID, ProjectID, Name, Path, Kind, Branch, Project, Label string
	Terminals                                               []terminalInfo
}

type taskInfo struct {
	ID, Title, Spec, Coordinator, WorkspaceID, Worker, Agent, Group, Status, Summary, Report string
	Workspace, WorkspaceKind, Branch, BaseRef                                                string
	CreatedAt                                                                                time.Time
	EndedAt                                                                                  *time.Time
	Terminal                                                                                 *terminalInfo
	Questions                                                                                int
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
	at                                               time.Time
}

type daemonStatus struct {
	FootprintKB        int64
	Terminals, Running int
}

// termView 是当前接入的终端：界面侧的终端内核加上到常驻进程的连接。
type termView struct {
	id   string
	conn *client.Conn
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
	loaded     bool
	err        string

	selected     string            // 侧栏选中的工作区 ID
	revealSel    bool              // 下一帧把选中项滚进可见区
	activeTerm   map[string]string // 工作区 ID → 选中的终端 ID
	sectionsOpen map[string]*bool  // 项目 ID → 侧栏分组是否展开
	navOrder     []string          // 侧栏看得见的行，按显示顺序，供方向键移动
	lastStatus   map[string]string // 终端 ID → 上次看到的 agent 状态，用于通知
	lastTask     map[string]string // 任务 ID → 上次看到的状态和待回复数，用于通知
	view         *termView
	fontSize     float32
	home         string  // $HIVE_HOME，ui.json 所在目录；为空时不保存
	state        uiState // 写进 ui.json 的界面状态

	// 任务总览
	taskSel   string                   // 选中的任务 ID
	detail    *taskDetail              // 选中任务的消息等完整内容
	diffs     map[string]*worktreeDiff // worktree 工作区 ID → 改动统计
	replyText string
	noteText  string
	// want 由界面线程写、轮询协程读：总览里正在看哪个任务
	wantMu   sync.Mutex
	wantTask string

	history historyView
	usage   usageView  // 右侧"技能与 MCP"面板
	recent  recentView // 右侧历史面板
	images  imagesView // 右侧图片面板
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
	// overPanelButton 是这一帧指针是否在标签栏的面板按钮上：在按钮上按下交给按钮切换，不算点了面板外面
	overPanelButton bool

	// 派发任务对话框
	dispatching bool
	dWorkspace  string // 工作区显示名
	dTitle      string
	dSpec       string
	dAgents     [3]bool // 与 agentChoices 对应
	dMode       int     // 0 当前目录，1 各自新建 worktree
	dBranch     string

	// 新建工作区对话框
	creating   bool
	newProject string
	newName    string
	newAgent   int // 0 无，1 claude，2 codex
	newPrompt  string

	confirming   bool
	confirmTitle string
	confirmBody  string
	confirmVerb  string // 确认按钮的文字，说清楚会发生什么
	confirmDo    func()

	refreshMu sync.Mutex
}

var agentNames = []string{"", "claude", "codex", "grok"}

// agentChoices 是派发任务时可选的 agent。
var agentChoices = []string{"claude", "codex", "grok"}

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
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:         "Hive",
			Width:         1200,
			Height:        780,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			Vibrancy:      mygo.VibrancySidebar,
			Content:       ui.View(a.build),
		})
		mygo.App.OnNotificationClick(func(id string) {
			// 通知 ID 形如 hive:<工作区>:<终端> 或 hive-task:<任务>
			parts := strings.Split(id, ":")
			switch {
			case len(parts) == 3 && parts[0] == "hive":
				a.win.Update(func() {
					a.selected, a.revealSel = parts[1], true
					a.activeTerm[parts[1]] = parts[2]
				})
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

func (a *app) refresh() {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var ps []project
	var ws []workspace
	var ts []taskInfo
	var st daemonStatus
	err := a.c.EnsureDaemon()
	if err == nil {
		err = a.c.Call(ctx, "project.list", nil, &ps)
	}
	if err == nil {
		err = a.c.Call(ctx, "workspace.list", nil, &ws)
	}
	if err == nil {
		err = a.c.Call(ctx, "task.list", nil, &ts)
	}
	if err == nil {
		err = a.c.Call(ctx, "daemon.status", nil, &st)
	}
	// 总览里选中的任务：拉完整内容，worktree 的改动统计每 5 秒更新一次
	var detail *taskDetail
	var diff *worktreeDiff
	diffFor := ""
	if id := a.wantedTask(); err == nil && id != "" {
		var d taskDetail
		if a.c.Call(ctx, "task.show", map[string]any{"task": id}, &d) == nil {
			detail = &d
			if d.WorkspaceKind == "worktree" && a.diffStale(d.WorkspaceID) {
				var w worktreeDiff
				if a.c.Call(ctx, "workspace.diff", map[string]any{"workspace": d.WorkspaceID}, &w) == nil {
					w.at, diff, diffFor = time.Now(), &w, d.WorkspaceID
				}
			}
		}
	}
	self := int64(procmem.Footprint(os.Getpid()) >> 10)
	a.win.Update(func() {
		if err != nil {
			a.err = err.Error()
			return
		}
		a.err = ""
		a.daemon, a.selfKB = st, self
		a.apply(ps, ws)
		a.applyTasks(ts)
		if detail != nil && detail.ID == a.taskSel {
			a.detail = detail
		}
		if diff != nil {
			a.wantMu.Lock()
			a.diffs[diffFor] = diff
			a.wantMu.Unlock()
		}
	})
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

// diffStale 在轮询协程里调用，所以只读 at，不碰界面状态的其他字段。
func (a *app) diffStale(workspaceID string) bool {
	a.wantMu.Lock()
	defer a.wantMu.Unlock()
	d := a.diffs[workspaceID]
	return d == nil || time.Since(d.at) > 5*time.Second
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
	for _, p := range ps {
		if a.sectionsOpen[p.ID] == nil {
			a.sectionsOpen[p.ID] = new(bool)
		}
	}
	// 只在选中的工作区被删掉时换到第一个；任务和历史不是工作区，要保留
	if a.selected != taskBoardID && a.selected != historyBoardID && a.workspace(a.selected) == nil && len(ws) > 0 {
		a.selected, a.revealSel = ws[0].ID, true
	}
	for _, w := range ws {
		for _, t := range w.Terminals {
			a.noticeStatus(w, t)
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
	case status == "waiting":
		title = agentTitle(t.Agent) + " 等待你处理"
	case status == "idle" && prev == "working":
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

func (a *app) workspacesOf(projectID string) []workspace {
	var out []workspace
	for _, w := range a.workspaces {
		if w.ProjectID == projectID {
			out = append(out, w)
		}
	}
	return out
}

// currentTerminal 返回选中工作区里应显示的终端，记住的那个不在了就换成第一个。
func (a *app) currentTerminal(w *workspace) *terminalInfo {
	if w == nil || len(w.Terminals) == 0 {
		return nil
	}
	id := a.activeTerm[w.ID]
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
	a.linkCard, a.hoverImage = nil, ""
	if id == "" {
		return
	}
	conn := a.c.Attach(id)
	v := &termView{id: id, conn: conn}
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
		// Claude 和 Codex 收到 Ctrl+V 才去读剪贴板里的图片；剪贴板里只有图片时 Cmd+V 拿不到文本，
		// 转成 Ctrl+V，和 Ghostty 里的习惯一致
		OnPaste: func(text string) bool {
			if text != "" || v.term == nil {
				return false
			}
			switch a.terminalAgent(id) {
			case "claude", "codex":
				v.term.Send([]byte{0x16})
				return true
			}
			return false
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
	go saveUIState(a.home, s)
}

// terminalAgent 返回终端里运行的 agent，普通 shell 为空。
func (a *app) terminalAgent(id string) string {
	for _, w := range a.workspaces {
		for _, t := range w.Terminals {
			if t.ID == id {
				return t.Agent
			}
		}
	}
	return ""
}

func (a *app) setFontSize(size float32) {
	a.fontSize = min(max(size, 9), 28)
	if a.view != nil {
		a.view.term.SetFont(a.termFont())
	}
}

// --- 动作：都在后台 goroutine 里调用常驻进程，完成后刷新

func (a *app) startTerminal(workspaceID, agent string) {
	go func() {
		var t terminalInfo
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "terminal.create", map[string]any{"workspace": workspaceID, "agent": agent}, &t)
		a.win.Update(func() {
			if err != nil {
				a.err = err.Error()
				return
			}
			a.selected = workspaceID
			a.activeTerm[workspaceID] = t.ID
		})
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.c.Call(ctx, "terminal.close", map[string]any{"terminal": id}, nil); err != nil {
		a.win.Update(func() { a.err = err.Error() })
	}
	a.refresh()
}

func (a *app) askRemoveWorkspace(w workspace) {
	body := "工作区里的终端会被关闭。"
	if w.Kind == "worktree" {
		body = "会关闭终端并删除 git worktree 目录（分支保留）。有未提交的改动时会拒绝删除。"
	}
	a.confirm("删除工作区 "+displayName(&w)+"？", body, "删除", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.c.Call(ctx, "workspace.rm", map[string]any{"workspace": w.ID, "force": w.Kind != "worktree"}, nil); err != nil {
			a.win.Update(func() { a.err = err.Error() })
		}
		a.refresh()
	})
}

// askRemoveProject 从 Hive 里移除 main 工作区 w 所在的项目：只删记录，不碰磁盘上的文件。
// 常驻进程要求先删掉 worktree 工作区，这里先挡住并说明要删哪些。
func (a *app) askRemoveProject(w workspace) {
	var worktrees []string
	for _, x := range a.workspacesOf(w.ProjectID) {
		if x.Kind == "worktree" {
			worktrees = append(worktrees, x.Name)
		}
	}
	if len(worktrees) > 0 {
		a.err = "先删除项目 " + w.Project + " 里的 worktree 工作区：" + strings.Join(worktrees, "、")
		return
	}
	a.confirm("从 Hive 移除项目 "+w.Project+"？", "只从 Hive 里移除，磁盘上的文件不会删除。项目里运行中的终端会被关闭。", "移除", func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := a.c.Call(ctx, "project.rm", map[string]any{"project": w.ProjectID, "force": true}, nil); err != nil {
			a.win.Update(func() { a.err = err.Error() })
		}
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var p project
	if err := a.c.Call(ctx, "project.add", map[string]any{"path": paths[0]}, &p); err != nil {
		a.win.Update(func() { a.err = err.Error() })
		return
	}
	a.refresh()
}

func (a *app) importOrca() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	report, err := a.c.ImportOrca(ctx, false)
	a.win.Update(func() {
		if err != nil {
			a.err = "从 Orca 导入失败：" + err.Error()
			return
		}
		added := 0
		for _, line := range report {
			if strings.HasSuffix(line, ": added") {
				added++
			}
		}
		a.confirm("已从 Orca 导入", fmt.Sprintf("新增 %d 个，已存在的 %d 个未改动。", added, len(report)-added), "好", nil)
	})
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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var w struct {
		ID        string
		Terminals []terminalInfo
		Setup     *struct {
			OK     bool
			Output string
		}
	}
	err := a.c.Call(ctx, "workspace.create", params, &w)
	a.win.Update(func() {
		if err != nil {
			a.err = err.Error()
			return
		}
		if w.Setup != nil && !w.Setup.OK {
			a.err = "setup 脚本失败：" + truncate(w.Setup.Output, 200)
		}
		a.selected, a.revealSel = w.ID, true
		if len(w.Terminals) > 0 {
			a.activeTerm[w.ID] = w.Terminals[0].ID
		}
	})
	a.refresh()
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
