// Package app 是 posse 的原生窗口（MyGo 原生 UI）：侧栏列出项目和工作区，
// 右侧以标签页显示工作区里的终端。终端由常驻进程托管，窗口只接入当前可见的那一个，
// 关掉窗口或切换标签不会影响 agent。
package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
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
	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/agent"
	"posse/internal/client"
	"posse/internal/daemon"
	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/procmem"
	"posse/internal/projicon"
	"posse/internal/rpc"
	"posse/locales"
)

type project struct {
	ID, Name, Path, Kind string
	SingleRepo           bool // 关掉了"识别多个 Git 仓库"
}

type agentState struct {
	Status, Message           string
	SessionID, TranscriptPath string
	Title                     string // posse session title 起的会话名
}

type terminalInfo struct {
	ID, WorkspaceID, Title, Agent string
	Dir                           string // 终端启动时的目录
	AgentState                    *agentState
	Running                       bool
	ExitCode                      *int
	Pid                           int    // 终端里 shell 的进程，底栏的内存浮窗按它的进程树统计
	WaitingTasks                  bool   // 里面的 agent 正阻塞在 posse task wait 上（旧常驻进程没有这个字段）
	PhoneSized                    bool   // 手机正看着，终端按手机的尺寸排版（旧常驻进程没有这个字段）
	Name                          string // 用户改的标签名，常驻进程记着（terminal.rename），手机也用它
	Lost                          bool   // 上一个常驻进程留下、还没恢复：接入时常驻进程才拉起它
	Unread                        bool   // 本轮完成了还没看，常驻进程记着（旧常驻进程没有这个字段）
}

// navTarget 是侧栏一行选中的东西：工作区，或其中的一个 agent 会话（term 非空）。
type navTarget struct{ ws, term string }

// wsRepo 是多仓库 worktree 工作区里的一个仓库：在工作区里的相对路径和它的基准分支。
type wsRepo struct{ Path, Base string }

// repoBases 是各仓库的基准分支，去重后连起来，写在"相对 …"里。
func repoBases(repos []wsRepo) string {
	var bases []string
	for _, r := range repos {
		if !slices.Contains(bases, r.Base) {
			bases = append(bases, r.Base)
		}
	}
	return strings.Join(bases, ", ")
}

type workspace struct {
	ID, ProjectID, Name, Path, Kind, Branch, BaseRef, Project, Label string
	Terminals                                                        []terminalInfo
	// Repos 是多仓库目录上的 worktree 工作区里的各个仓库，这时 BaseRef 为空
	Repos []wsRepo
	// Missing 是目录已经不在了（在外面删掉或挪走，又没被 dropGoneWorktrees 自动移除），窗口每秒查一次
	Missing bool `json:"-"`
	// Shown 是从别处加进来的 worktree 显示的名字：它当前的分支（目录名常和仓库同名，看不出是哪个）；
	// 空时用 Name。只用来显示，CLI 和常驻进程照旧认 Name
	Shown string `json:"-"`
}

type taskInfo struct {
	ID, Title, Spec, Coordinator, WorkspaceID, Worker, Agent, Status, Summary, Report string
	DiscussionID                                                                      string // 讨论成员的专用 worker：在讨论里追加，不在任务页
	Workspace, WorkspaceKind, Branch, BaseRef                                         string
	Repos                                                                             []wsRepo
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
	FootprintKB  int64
	Running      int
	Awake        *awakeStatus // 旧常驻进程没有防休眠时为空
	Usage        *planStatus  // 旧常驻进程没有套餐用量时为空
	Version      string       `json:"version"`
	Build        string       `json:"build"` // 旧常驻进程没有这个字段时为空
	Pid          int          `json:"pid"`
	StartedAt    time.Time    `json:"startedAt"`
	GitTraceSeq  int64        `json:"gitTraceSeq"`  // 终端 git 命令记录的最新序号，旧常驻进程没有时为 0
	MobileOnline []string     `json:"mobileOnline"` // 现在连着的手机，旧常驻进程没有这个字段
	DeskViewer   string       `json:"deskViewer"`   // 正在看桌面的手机
	DeskMissing  []string     `json:"deskMissing"`  // 看桌面缺的授权：screen、accessibility
	Agents       []string     `json:"agents"`       // 本机装了的 agent，旧常驻进程没有、或还没检测完时为 nil
}

// termView 是当前接入的终端：界面侧的终端内核，它持有到常驻进程的连接。
type termView struct {
	id   string
	term *terminal.Terminal
	// dir 是 shell 报告的当前目录（OSC 7），每帧取一次：终端的回调持着终端的锁，不能再调 term.Dir
	dir string
	// attached 是接入的时间，painted 是屏幕上出现过内容；两者给 startingHint 用
	attached time.Time
	painted  bool
	find     termFind
}

type app struct {
	c   *client.Client
	win *mygo.Window

	// 讨论页（discussions.go）：轮询在后台协程里读 discWant、discSeq
	disc        discBoard
	discView    discViewer // 讨论里点开的大图
	discThumbs  discThumbs // 讨论里图片附件的缩略图：整个窗口一份，换讨论不清空，后台解码的并发按它算
	discMu      sync.Mutex
	discWant    string // 要取详情的讨论
	discWantRev int64  // 它已有的版本，0 要全量
	discWantGen int64  // 意图的代号，换讨论、跳到最新时加一（discGen）
	discSeq     atomic.Int64
	discLoaded  bool // 拿到过一次列表：之后的变化才发通知
	// trashItem 把文件移到废纸篓，nil 时用 mygo.Shell.TrashItem（要主线程跑着）；测试里换掉
	trashItem func(path string) error
	// mainUpdate 把函数交给主线程跑，为 nil 时用 win.Update；测试里直接跑
	mainUpdate func(func())

	projects   []project
	workspaces []workspace
	tasks      []taskInfo
	daemon     daemonStatus
	planBusy   bool      // 套餐用量正在刷新
	planAutoAt time.Time // 用量浮层上次自动补查的时间
	selfKB     int64
	termKB     int64 // 所有终端进程树的内存，底栏的总内存要加上它
	loaded     bool
	err        string

	// 由 apply、applyTasks 算好的派生数据，免得每帧重算
	byProject     map[string][]workspace   // 项目 ID → 它的工作区，主工作区在前
	remoteWorkers map[string][]sessionNode // 派发方终端 ID → 它派到别的工作区的 worker（影子行），indexRemoteWorkers 算
	// linkHandles 是跨工作区连线要定位的行（会话按终端 ID，工作区列表按 "list:"+项目 ID）。
	// 一直复用同一个：Handle 每绑定一次就在窗口里登记一条、窗口关了才清，每帧新建会越积越多
	linkHandles map[string]*ui.Handle
	sorted      []taskInfo        // 任务总览的顺序，见 sortTasks
	workerTitle map[string]string // worker 终端 ID → 任务标题，标签页和侧栏上显示
	coordOf     map[string]string // worker 终端 ID → 派发它的终端（用户派发的没有），侧栏把 worker 排在它下面
	runningFor  map[string]int    // 派发方终端 ID → 它还在进行中的任务数
	navProjects []project         // 侧栏这一帧遍历的项目，复用底层数组

	selected     string            // 侧栏选中的工作区 ID
	revealSel    bool              // 下一帧把选中项滚进可见区
	activeTerm   map[string]string // 工作区 ID → 选中的终端 ID
	sectionsOpen map[string]*bool  // 项目 ID → 侧栏分组是否展开
	workersOpen  map[string]*bool  // 派发方终端 ID → 侧栏里它的 worker 是否展开（默认展开，不记进 ui.json）
	runOrder     []string          // 侧栏「运行中」里会话的顺序（终端 ID），nil 表示下一帧按状态重排
	topSlot      int               // 侧栏顶上磁贴拖动时空出来的格子加一，0 是没有；松手时按它落
	pollDown     bool              // 最近一次轮询连不上常驻进程，侧栏的会话列表是旧的
	navOrder     []navTarget       // 侧栏看得见、可选中的行，按显示顺序，供方向键移动
	navCur       int               // navOrder 里当前选中的行，没有时为 -1
	lastStatus   map[string]string // 终端 ID → 上次看到的 agent 状态，用于通知
	badge        int               // Dock 角标现在显示的数
	lastTask     map[string]string // 任务 ID → 上次看到的状态和待回复数，用于通知
	// 轮询上次交给界面的数据，没变就不更新界面；refreshMu 保护
	lastPoll      *pollResult
	diffAt        map[string]time.Time // worktree 工作区 ID → 上次统计改动的时间
	paneGone      map[string]time.Time // 分栏里记着、列表里不见了的终端 → 第一次不见的时间（prunePanes）
	paneDue       atomic.Bool          // paneGone 里有等着到期的：轮询结果没变、不走 apply 时轮询也在界面线程上跑一次 prunePanes
	termAt        time.Time            // 上次统计终端内存的时间（轮询协程里用）
	lastTerm      int64
	pollForce     atomic.Bool // 下一次轮询无论有没有变化都交给界面
	refreshQueued atomic.Bool // selectTask 排的那次轮询还没开始
	// shade 是窗口上一帧的深浅：0 还没画过，1 浅色，2 深色；轮询报给常驻进程（statusParams）
	shade atomic.Int32
	// panes 是主区域每格接入的终端（下标同 cols，选中的不是终端的格为 nil）；pane 是焦点所在的格
	panes [maxColumns]*termView
	pane  int
	cols  []colView // 这一帧显示的几格（syncColumns 定），没分栏时只有一格
	tree  *nodeView // 这一帧显示的分割树
	// cellSize 是各格内容区上一帧的大小，判断还分不分得开
	cellSize [maxColumns]ui.Point
	// docFocusPane 是正在画的文件在不在焦点格里（columnBody 设，docArea 用）
	docFocusPane bool
	// carryFind 是被常驻进程断开、等着重新接入的终端的查找栏，键是终端 ID
	carryFind    map[string]termFind
	fontSize     float32
	editorSize   float32
	termFamily   string // 终端字体，空是默认
	editorFamily string // 编辑器字体，空是默认
	settingsTab  string // 设置页左边选中的分类，空是第一个
	profile      profileState
	avatars      map[string]avatarEntry // 头像图片，键是摘要
	locale       string                 // 设置页选的界面语言（mygo.App.ChosenLocale），空是跟随系统
	termPick     fontPick
	editorPick   fontPick
	editorDemo   *editor.State // 设置页编辑器预览的只读编辑器，第一次打开时建
	permission   string        // agent 的权限档位，存在 $POSSE_HOME/agents.json
	disabled     []string      // 设置里关掉的 agent，也在 agents.json
	restoreAll   bool          // 常驻进程启动时恢复全部终端，也在 agents.json
	chatDir      string        // 聊天磁贴的目录，也在 agents.json（chat.go）
	chatAdding   bool          // 正在把聊天目录加成项目
	home         string        // $POSSE_HOME，ui.json 所在目录；为空时不保存
	state        uiState       // 写进 ui.json 的界面状态

	// 任务总览
	taskHome bool // 只要总览：不自动打开上次看的任务
	specMD   struct {
		src    string
		blocks []mdBlock
	} // 任务说明解析好的 Markdown，见 specMarkdown
	taskScroll string                   // 方向键换到的任务：列表建到它时滚进视野
	taskSel    string                   // 选中的任务 ID
	lastWS     string                   // 上次选中的工作区：关掉任务、历史等页面后回到这里
	detail     *taskDetail              // 选中任务的消息等完整内容
	diffs      map[string]*worktreeDiff // worktree 工作区 ID → 改动统计
	replyText  string
	noteText   string
	// wantTask 由界面线程写、轮询协程读：总览里正在看哪个任务
	wantMu   sync.Mutex
	wantTask string

	history      historyView
	usage        usageView             // 右侧"技能与工具"面板
	cheat        cheatView             // 右侧"命令速查"面板
	panelShown   float32               // 右侧面板滑入的进度，1 是停稳了
	recent       recentView            // 右侧历史面板
	skills       skillsView            // 侧栏的「技能」页
	skillsTab    skillsPanelView       // 文件栏的「技能」页签
	skillChange  skillChangeView       // 开关技能的确认框
	skillInstall skillInstallView      // 安装技能的对话框
	skillRun     skillRunView          // 技能的安装、更新、删除：对话框里嵌终端跑命令
	hiddenTerms  map[string]bool       // 不进标签栏和侧栏的终端（技能命令的对话框里那个）
	skillNew     skillNewView          // 新建项目技能的对话框
	posseSkills  posseSkillsView       // Posse 自带技能装了没有（posseskills.go）
	images       imagesView            // 右侧图片面板
	mobile       mobileView            // 侧栏「手机」页：配对码和已配对的手机
	mem          memView               // 底栏的内存浮窗
	planOpen     bool                  // 状态栏的套餐用量详情
	emptyHeight  float32               // 空工作区页滚动区上一帧的高度
	emptyWidth   float32               // 空工作区页滚动区上一帧的宽度
	files        map[string]*filesView // 工作区 ID → 文件标签
	quick        quickOpen             // ⌘P 快速打开
	defPick      defPick               // 几处同名定义的选择对话框（definition.go）
	unsaved      *unsavedAsk           // 问要不要保存修改的对话框
	// 菜单的 ⌘W 记在这里，下一帧再关标签
	menuCloseTab bool
	// 快捷键（keymap.go）：keyRec 是设置页里正在录的一项，keysVer 在改了快捷键后加一，edBind 是按它缓存的编辑器按键
	keyRec     *keyRecording
	keyQuery   string // 设置页快捷键的搜索词
	keysVer    int
	edBind     []editor.Binding
	edBindKeys int
	// Git：分支弹窗、输入框对话框、Push 对话框
	branchPop     *branchPopup
	branchFolds   map[string]bool       // 分支弹窗里各组的展开状态，键是 recent、local、remote:<远端>，没有记过的用默认
	track         map[string]*trackInfo // 工作区 ID → 当前分支的传入、传出提交数，标签栏的分支按钮用
	fetchAt       map[string]time.Time  // 项目 ID → 上次自动 fetch 的时间
	gitRuns       []gitx.Run            // Git 操作记录里的命令（gitconsole.go），所有项目共用
	runWS         map[string]string     // 命令目录 → 所在工作区 ID（runWorkspace），工作区列表变了就清掉
	realPaths     map[string]string     // 工作区路径 → 解析过符号链接的路径
	consoleErr    map[string]bool       // 有没在操作记录里看过的失败命令的目录，标签按项目带红点
	consoleQ      chan consoleWrite     // 交给写 git-console.jsonl 的协程
	consoleLines  int                   // git-console.jsonl 里有几行，攒多了重写
	consoleSorted bool                  // 读回的记录已经按项目限过条数（要等拿到工作区列表）
	consoleOnce   sync.Once
	traceSeq      int64 // 已经取到的终端 git 命令序号（git.trace），只在轮询协程里用
	traceLoaded   bool
	filesPanelRef ui.Ref
	doubleTaps    uint64             // 上一帧看到的 mygo 双击修饰键计数（c.DoubleTap）
	quickGen      int                // 随处搜索里文本、符号检索的编号（nextQuickGen）
	termAreaRef   [maxColumns]ui.Ref // 每一栏的终端内容区（终端和它上面的查找栏）
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
	// viewed 按终端记 Codex 会话看过的图片路径，终端里只打印了文件名时靠它找到文件，见 viewed.go
	viewed map[string]*viewedImages
	// 悬停在终端里的图片链接上时预览：hoverImage 是图片路径，previews 是解码好的预览
	hoverImage string
	hoverTerm  string // 悬停的是哪个终端里的链接：分栏时预览只画在那一栏
	// focusTerm 让下一帧把焦点交给终端：从文件栏拖文件进来后接着就能打字
	focusTerm      bool
	previews       map[string]*ui.Bitmap
	iconEdit       iconEditor                // 更换项目图标的对话框
	projIcons      map[string]projectIcon    // 项目自定义的图标，键是项目 ID
	projIconCache  map[string]ui.ImageSource // 项目图标的图片，键是文件名
	previewLoading map[string]bool
	// lastPanel 是最后打开的右侧面板：关闭动画期间还要画它
	lastPanel string
	shownTab  [maxColumns]string // 各栏最近一次滚进可见范围的标签，见 revealTab
	termCtx   menuState          // 终端内容区的右键菜单（termMenu）
	sysMenu   sysMenu            // mygo 交来的编辑菜单（menu.go 的 presentMenu）
	termCtxID string             // 它是哪个终端的：切走了就不画
	// 正在改名的终端标签（终端 ID）和输入框里的文字，见 tabname.go
	renaming, renameText string
	lastPanels           panelSnap         // 上一帧的项目和它的文件栏、日志浮层开关（snapPanels）
	nameSent             map[string]string // 已经交给常驻进程的标签名（terminal.rename），每个只发一次
	renameQ              chan [2]string    // 交给按顺序发改名的协程（renameWorker）
	renameOnce           sync.Once
	tabStripW            [maxColumns]float32 // 各栏标签栏能放标签的宽度，变了就把当前标签再滚进来
	// overPanelButton 是这一帧指针是否在标签栏的面板按钮上：在按钮上按下交给按钮切换，不算点了面板外面
	overPanelButton bool
	resizingPanel   bool            // 正在拖文件栏的左边缘，松手时写 ui.json
	resizingBranch  bool            // 正在拖分支弹窗的右边缘，松手时写 ui.json
	resizingSide    bool            // 正在拖侧栏的右边缘，松手时写 ui.json
	resizingSplit   string          // 正在拖的分栏线（paneDivider 的 key），松手时写 ui.json
	splitDrag       float32         // 拖分隔线时累计的距离，松手才改比例
	gripDrawn       bool            // 这一帧画了正在拖的分隔线
	split           bool            // 这一帧主区域分成了几格（syncColumns 定）
	focusHidden     bool            // 这一帧焦点格看不见（它的终端一时不在列表里）
	splitPending    map[string]bool // 正在为「向右分栏」新开终端的工作区
	sidebarShown    float32         // 侧栏滑进滑出的进度，1 是完全展开
	resizingLog     bool            // 正在拖 Git 日志浮层的上边缘，松手时写 ui.json
	resizingCompare bool            // 正在拖分支比较两栏之间的分隔条，松手时写 ui.json
	rollback        rollbackDlg
	rollbackBox     dialogBox  // 回滚对话框拖到的位置、拉伸过的尺寸
	rollbackDiffBox dialogBox  // 回滚对话框里点开的 diff 浮窗的
	pushBox         dialogBox  // Push 对话框的
	toasts          []ui.Toast // 等下一帧交给 mygo 显示的提示：后台操作完成时手里没有 Context

	// overBranchButton 是指针是否在状态栏的分支按钮上，作用同 overPanelButton
	overBranchButton bool

	// 派发任务对话框
	dispatching bool
	dWorkspace  string // 工作区显示名
	dTitle      string
	dSpec       string
	dAgents     map[string]bool // 勾选了的 agent
	dMode       int             // 0 当前目录，1 各自新建 worktree
	dBranch     string

	// 新建工作区对话框
	creating   bool
	newProject string
	newName    string
	newAgent   int // newAgentChoices 的序号，0 是不启动 agent
	newPrompt  string
	// 多仓库目录的项目上勾的仓库（在项目目录里的相对路径）：勾了就给它们各建 worktree，不勾是同一目录上的工作区
	newRepos    map[string]bool
	newReposFor string // newRepos 是哪个项目的，换了项目重来

	confirming   bool
	confirmTitle string
	confirmBody  string
	confirmVerb  string // 确认按钮的文字，说清楚会发生什么
	confirmDo    func()
	// confirmAltVerb、confirmAltDo 是确认框的另一个动作（confirmChoice），排在确认按钮左边；为空时没有
	confirmAltVerb string
	confirmAltDo   func()

	traces tracesView // agent 痕迹的确认框（traces.go）

	refreshMu sync.Mutex

	// 常驻进程跑的是旧包时交给本窗口的包接着跑（upgrade.go）
	upgrade upgradeState

	// 窗口自己的 Posse.app 被换掉后提示重开（relaunch.go）
	exe         exeWatch
	exeReplaced atomic.Bool // 轮询线程里只报一次
	staleApp    bool
	dialogOpen  bool       // 系统文件对话框开着（inDialog），不再开第二个
	staleHidden bool       // 这次窗口里收起了重开提示，底栏齿轮旁留一个圆点
	upd         selfUpdate // 自动更新的检查和下载（appupdate.go）
	updating    bool       // 正在换包、交接常驻进程，接着重开窗口

	// 本轮完成了还没看由常驻进程记（terminalInfo.Unread），桌面和手机共用；这里只管怎么画、什么时候报看过（unread.go）
	unreadAt map[string]time.Time // 第一次见到它没看的时刻，铃铛每轮摇动从这时起算
	viewing  string               // 窗口有焦点时正看着的会话，常驻进程还没收到看过之前它不画铃铛
	seenFor  string               // 已经为它报过看过的会话，同一次看只报一次
	focused  atomic.Bool          // 窗口有焦点：在后台时看着的会话也不算看过
}

// agentChoices 是本机装了、又没在设置里关掉的 agent，菜单和选项只列这些，按 agent.Known 的顺序。
func (a *app) agentChoices() []string {
	return slices.DeleteFunc(slices.Clone(a.installedAgents()), func(n string) bool { return slices.Contains(a.disabled, n) })
}

// installedAgents 是常驻进程检测到装了的 agent；旧常驻进程不报、或还没检测完时全列。
func (a *app) installedAgents() []string {
	if a.daemon.Agents == nil {
		return agent.Known
	}
	return a.daemon.Agents
}

// newAgentChoices 与新建工作区对话框的"同时启动"分段一一对应，0 是只建工作区。
func (a *app) newAgentChoices() []string {
	return append([]string{""}, a.agentChoices()...)
}

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

// 窗口最小尺寸：默认宽的侧栏（272）旁边，默认字号的终端还有约 72 列；高度够放下对话框和 30 行左右的终端。
const minWindowWidth, minWindowHeight = 900, 560

// termFontFamily 与用户的 Ghostty 一致。中文显式回退到 PingFang SC：系统等宽字体的级联
// 会取到比例宽度的"，""。"，在两格里居中后前后各空出一大块。
const termFontFamily = "JetBrainsMono Nerd Font Mono, JetBrains Mono, PingFang SC, monospace"

// Run 打开窗口；必须在 main goroutine 调用。
func Run(c *client.Client) error {
	// 同一 home 只开一个窗口：一开始就占住 app.sock，已有窗口在应答就把它叫到前面，自己退出
	ctl, err := listenCtl(CtlSocket(c.Home))
	if errors.Is(err, errCtlBusy) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := rpc.Call(ctx, CtlSocket(c.Home), "app.focus", nil, nil); err != nil {
			return fmt.Errorf("a Posse window is already open for %s but did not respond: %w", c.Home, err)
		}
		fmt.Fprintln(os.Stderr, "posse: a Posse window is already open for", c.Home)
		return nil
	}
	if err != nil {
		log.Printf("posse web: %v", err)
	}
	// 开发包放在别的窗口后面也照常画：验收工具从后面截图、操作它，不用把窗口叫到最前抢用户的屏幕。
	// 日常的包照旧被挡住时不画，省电
	ui.DrawWhenOccluded = daemon.RunningDevBundle()
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
		permission:   agent.PermissionMode(c.Home),
		disabled:     agent.Disabled(c.Home),
		restoreAll:   agent.RestoreAll(c.Home),
		chatDir:      agent.ChatDir(c.Home),
		exe:          newExeWatch(),
	}
	a.selected, a.revealSel = a.state.Selected, true
	var stopCtl func()
	a.projIcons = projicon.Load(a.home)
	a.traceGit()
	if a.state.FontSize > 0 {
		a.fontSize = a.state.FontSize
	}
	a.termFamily, a.editorFamily = a.state.TermFont, a.state.EditorFont
	a.editorSize = editorSizeOf(a.state)
	uiSmooth = a.state.UISmooth
	setUIType(a.state.UIFontSize, a.state.UIWeight, a.state.UIBoldWeight)
	if a.state.IconSize > 0 {
		iconSize = a.state.IconSize
	}
	// Run 之前设好，窗口一出来就是选定的深浅，不先按系统画一帧
	mygo.Theme.SetSource(themeSource(a.state.Theme))
	mygo.App.WhenReady(func() {
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title:         appName(),
			Width:         1200,
			Height:        780,
			MinWidth:      minWindowWidth,
			MinHeight:     minWindowHeight,
			StateKey:      "main",
			TitleBarStyle: mygo.TitleBarHidden,
			// 红绿灯在 40 高的顶栏里上下居中，和右边的应用名、侧栏开关、标签对齐（照 Orca）
			TrafficLightPosition: &mygo.Point{X: trafficLightX, Y: 13},
			Content:              ui.View(a.build),
		})
		a.watchClose()
		a.startLocale()
		a.installMenu()
		a.watchFocus()
		a.startUpdates()
		a.win.Update(a.readPosseSkills)
		mygo.App.OnNotificationClick(func(id string) {
			// 通知 ID 形如 posse:<工作区>:<终端>、posse-task:<任务> 或 posse-disc:<讨论>
			parts := strings.Split(id, ":")
			switch {
			case len(parts) == 3 && parts[0] == "posse":
				a.win.Update(func() { a.openTerminal(parts[1], parts[2]) })
			case len(parts) == 2 && parts[0] == "posse-task":
				a.win.Update(func() { a.openTask(parts[1]) })
			case len(parts) == 2 && parts[0] == "posse-disc":
				a.win.Update(func() { a.openDiscussion(parts[1]) })
			default:
				return
			}
			a.win.Show()
			a.win.Focus()
		})
		go a.poll()
		if ctl != nil {
			stopCtl = a.serveCtl(ctl)
		}
	})
	err = mygo.App.Run()
	if stopCtl != nil {
		stopCtl()
	}
	return err
}

// windowShown 报告窗口在屏幕上：没隐藏、没最小化。
func (a *app) windowShown() bool {
	return a.win != nil && a.win.IsVisible() && !a.win.IsMinimized()
}

// statusParams 是轮询 daemon.status 的参数：带上终端在用的配色，常驻进程换到所有终端上，
// 新终端也按它起（见 daemon 的 windowColors）；窗口还没画过时不带，免得先报错一套
func (a *app) statusParams() any {
	switch a.shade.Load() {
	case 1:
		return map[string]any{"colors": windowColors(false)}
	case 2:
		return map[string]any{"colors": windowColors(true)}
	}
	return nil
}

// poll 定期从常驻进程拉取项目、工作区和终端状态；常驻进程不在时拉起它。
func (a *app) poll() {
	for {
		a.refresh()
		a.checkReplaced()
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
	usage      *planStatus
	daemonInfo daemonStatus // 版本、构建、pid、启动时间（设置页的"关于"用），连着的手机
	disc       *discPoll
}

func (a *app) refresh() {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	a.refreshQueued.Store(false)
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
	// 状态先取：后面的列表在版本对不上的旧常驻进程上可能失败，有了状态照样能发现版本差、自动交接
	statusOK := false
	if err == nil {
		// 带上终端在用的配色：常驻进程换到所有终端上，新终端也按它起（见 daemon 的 windowColors）
		err = a.c.Call(ctx, "daemon.status", a.statusParams(), &st)
		statusOK = err == nil
	}
	if err == nil {
		err = a.c.Call(ctx, "workspace.list", nil, &r.workspaces)
		// 常驻进程只在建工作区时记分支，main 工作区切分支后就过时了；以 HEAD 为准
		for i := range r.workspaces {
			// 多仓库的 worktree 工作区根目录不是仓库，往上找到的是不相干的仓库；分支用记下的那个
			if len(r.workspaces[i].Repos) > 0 {
				continue
			}
			if b := gitx.Head(r.workspaces[i].Path); b != "" {
				r.workspaces[i].Branch = b
			}
		}
		r.workspaces = a.dropGoneWorktrees(ctx, r.workspaces)
		for i := range r.workspaces {
			if _, err := os.Stat(r.workspaces[i].Path); errors.Is(err, fs.ErrNotExist) {
				// 记着的分支是旧的，不再显示；分支按钮和日志浮层也跟着收起
				r.workspaces[i].Missing, r.workspaces[i].Branch = true, ""
			}
		}
		showBranches(r.workspaces)
	}
	if err == nil {
		err = a.c.Call(ctx, "task.list", nil, &r.tasks)
	}
	if err == nil {
		a.pollGitTrace(ctx, st.GitTraceSeq)
		a.pollDiscussions(&r, func(method string, params, out any) error { return a.c.Call(ctx, method, params, out) })
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
		r = pollResult{err: errtext.Text(err)}
		if statusOK {
			r.daemonInfo = daemonStatus{Version: st.Version, Build: st.Build, Pid: st.Pid, StartedAt: st.StartedAt}
			a.autoUpgrade(r.daemonInfo)
			r.err = a.staleDaemonError(appL(), r.daemonInfo, err)
		}
	} else {
		for id := range a.diffAt {
			if !slices.ContainsFunc(r.workspaces, func(w workspace) bool { return w.ID == id }) {
				delete(a.diffAt, id)
			}
		}
		self := int64(procmem.Footprint(os.Getpid()) >> 10)
		// 终端的进程树每 5 秒统计一次：几十个进程，几毫秒。窗口隐藏或最小化时底栏看不见，不统计；
		// 问窗口状态要切到主线程等它，所以也是 5 秒才问一次
		if time.Since(a.termAt) > 5*time.Second {
			a.termAt = time.Now()
			if a.windowShown() {
				a.lastTerm = terminalsFootprint(r.workspaces)
			}
		}
		r.running, r.daemonKB, r.selfKB, r.termKB, r.awake = st.Running, st.FootprintKB, self, a.lastTerm, st.Awake
		r.usage = st.Usage
		r.daemonInfo = daemonStatus{Version: st.Version, Build: st.Build, Pid: st.Pid, StartedAt: st.StartedAt, MobileOnline: st.MobileOnline, DeskViewer: st.DeskViewer, DeskMissing: st.DeskMissing, Agents: st.Agents}
		r.daemonMB, r.selfMB, r.totalMB = mb(st.FootprintKB), mb(self), mb(st.FootprintKB+self+a.lastTerm)
		a.autoUpgrade(r.daemonInfo)
	}
	// 改动统计只在拉到时才有，没拉到时沿用上次的，不算变化
	if r.diff == nil && a.lastPoll != nil && a.lastPoll.diffFor != "" {
		r.diff, r.diffFor = a.lastPoll.diff, a.lastPoll.diffFor
	}
	force := a.pollForce.Swap(false)
	if !force && a.lastPoll != nil && samePoll(a.lastPoll, &r) {
		if r.err == "" && a.paneDue.Load() {
			a.update(func() { a.prunePanes(time.Now()) })
		}
		return
	}
	prevErr := a.lastPoll != nil && a.lastPoll.err != ""
	a.lastPoll = &r
	a.update(func() {
		a.pollDown = r.err != ""
		if r.err != "" {
			a.err = r.err
			// 设置「关于」靠它显示版本和更新、重启常驻进程的按钮
			if r.daemonInfo.Pid != 0 {
				a.daemon = r.daemonInfo
			}
			return
		}
		// 只清掉轮询自己报的连接错误，动作的报错留着等用户关掉
		if prevErr {
			a.err = ""
		}
		info := r.daemonInfo
		info.FootprintKB, info.Running, info.Awake, info.Usage = r.daemonKB, r.running, r.awake, r.usage
		a.daemon, a.selfKB, a.termKB = info, r.selfKB, r.termKB
		// apply 会就地排序，r 还要留给下次比较
		a.apply(slices.Clone(r.projects), slices.Clone(r.workspaces))
		a.applyTasks(r.tasks)
		a.applyDiscussions(r.disc)
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
	a.pruneProjectIcons(ps)
	for i := range ws {
		// ws 是浅拷贝，终端列表还和上次轮询的结果共用，排序前先复制
		ws[i].Terminals = slices.Clone(ws[i].Terminals)
		// 技能命令的对话框里嵌着的终端不进标签栏和侧栏，它的状态交给对话框
		ws[i].Terminals = slices.DeleteFunc(ws[i].Terminals, func(t terminalInfo) bool {
			if a.hiddenTerms[t.ID] {
				a.skillRunExited(t)
				return true
			}
			return false
		})
		sortTerminals(ws[i].Terminals, a.state.TermOrder[ws[i].ID])
		for j := range ws[i].Terminals {
			a.syncTermName(&ws[i].Terminals[j])
		}
	}
	sort.SliceStable(ws, func(i, j int) bool {
		if ws[i].Project != ws[j].Project {
			return ws[i].Project < ws[j].Project
		}
		return ws[i].Kind == "main" && ws[j].Kind != "main"
	})
	a.projects, a.workspaces, a.loaded = ps, ws, true
	a.indexWorkspaces()
	for id := range a.state.TermOrder {
		if a.workspace(id) == nil {
			delete(a.state.TermOrder, id)
		}
	}
	for id := range a.state.TermNames {
		if _, t := a.findTerminal(id); t == nil {
			delete(a.state.TermNames, id)
			delete(a.state.TermNameBases, id)
		}
	}
	a.prunePanes(time.Now())
	for _, p := range ps {
		if a.sectionsOpen[p.ID] == nil {
			open := true
			a.sectionsOpen[p.ID] = &open
		}
	}
	// 只在选中的工作区被删掉时换到第一个；任务、历史、手机和设置不是工作区，要保留
	if !isBoard(a.selected) && a.workspace(a.selected) == nil && len(ws) > 0 {
		a.selected, a.revealSel = ws[0].ID, true
	}
	a.migratePanels()
	if !a.consoleSorted {
		a.consoleSorted = true
		a.trimGitRuns()
	}
	seen := map[string]bool{}
	for _, w := range ws {
		for _, t := range w.Terminals {
			a.noticeStatus(w, t)
			seen[t.ID] = true
			if !t.Unread {
				delete(a.unreadAt, t.ID)
			} else if _, ok := a.unreadAt[t.ID]; !ok {
				if a.unreadAt == nil {
					a.unreadAt = map[string]time.Time{}
				}
				a.unreadAt[t.ID] = time.Now()
			}
		}
	}
	for id := range a.lastStatus {
		if !seen[id] {
			delete(a.lastStatus, id)
		}
	}
	for id := range a.unreadAt {
		if !seen[id] {
			delete(a.unreadAt, id)
		}
	}
	a.updateBadge()
	a.pruneFiles()
}

// indexWorkspaces 按项目分好工作区，侧栏每帧要用。
func (a *app) indexWorkspaces() {
	a.runWS = nil
	a.byProject = make(map[string][]workspace, len(a.projects))
	for _, w := range a.workspaces {
		a.byProject[w.ProjectID] = append(a.byProject[w.ProjectID], w)
	}
	a.indexRemoteWorkers()
	a.pruneLinkHandles()
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
	if !seen || a.state.NotifyOff {
		return
	}
	m := appL()
	title := noticeTitle(m, prev, status, t)
	if title == "" {
		return
	}
	// 正在看的终端不打扰
	if a.win.IsFocused() && a.selected == w.ID && a.showing(t.ID) {
		return
	}
	body := w.Label
	if t.AgentState != nil && t.AgentState.Message != "" {
		body = m.AppNoticeBody(w.Label, truncate(t.AgentState.Message, 120))
	}
	n := mygo.NewNotification(mygo.NotificationOptions{ID: "posse:" + w.ID + ":" + t.ID, Title: title, Body: body, Group: w.ID})
	go func() {
		if err := n.Show(); err != nil {
			log.Printf("notification: %v", err)
		}
	}()
}

// noticeTitle 是终端状态从 prev 变成 status 时系统通知的标题，不用通知时为空。
// 已完成跟着常驻进程的未读：手机看着时完成的不算未读，也就不通知。
func noticeTitle(m locales.Messages, prev, status string, t terminalInfo) string {
	if prev == status || t.Agent == "" {
		return ""
	}
	switch {
	case status == agent.StatusWaiting:
		return m.AppNoticeWaiting(agentTitle(t.Agent))
	case status == agent.StatusIdle && prev == agent.StatusWorking && t.Unread:
		return m.AppNoticeDone(agentTitle(t.Agent))
	}
	return ""
}

// badgeCount 是 Dock 角标的数：侧栏显示成待处理和铃铛的 agent 会话数，按 sessionStatus 算，和侧栏一致
// （等待任务、还没恢复的不算；正看着的会话完成了不算未读，窗口下一帧就报看过）。
func (a *app) badgeCount() int {
	n := 0
	for _, w := range a.workspaces {
		for _, t := range w.Terminals {
			if t.Agent == "" {
				continue
			}
			if s := a.sessionStatus(t); s == agent.StatusWaiting || s == statusUnread {
				n++
			}
		}
	}
	return n
}

// updateBadge 按当前的终端状态更新 Dock 角标，数没变时不调系统。轮询的数据变了、每帧看过的会话换了时都调。
func (a *app) updateBadge() {
	// 窗口失去焦点时不重画，viewing 要到下一帧才清掉：这里先清，切走后完成的会话马上算进角标
	if !a.focused.Load() {
		a.viewing = ""
	}
	n := 0
	if !a.state.BadgeOff {
		n = a.badgeCount()
	}
	// 没有窗口（测试、离屏预览）时 mygo 的应用没在运行，不能调
	if n == a.badge || a.win == nil {
		return
	}
	a.badge = n
	mygo.App.SetBadgeCount(n)
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
			{Label: locales.MenuCheckForUpdates(), Hidden: !mygo.Updater.Configured(), Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(a.openAbout)
				go a.checkUpdate(true)
			}},
			mygo.Separator(),
			{Label: locales.MenuSettings(), Accelerator: a.accel("settings"), Click: func(*mygo.MenuItem, *mygo.Window) {
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
		{Label: locales.MenuFile(), Submenu: []*mygo.MenuItem{
			{Label: locales.MenuCloseTab(), Accelerator: a.accel("closeTab"), Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(func() { a.menuCloseTab = true })
			}},
			{Role: mygo.RoleClose, Accelerator: a.accel("closeWindow")},
		}},
		{Role: mygo.RoleEditMenu},
		// 网页有焦点时键盘在 WebKit 手里，窗口的快捷键收不到；macOS 先把按键交给菜单，放这里网页里也认
		{Label: locales.MenuView(), Submenu: []*mygo.MenuItem{
			{Label: locales.MenuReloadPage(), Accelerator: a.accel("webReload"), Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(func() { a.menuWeb("reload") })
			}},
			{Label: locales.MenuForceReloadPage(), Accelerator: a.accel("webHardReload"), Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(func() { a.menuWeb("hardReload") })
			}},
			mygo.Separator(),
			{Label: locales.MenuMobilePreview(), Accelerator: a.accel("webMobile"), Click: func(*mygo.MenuItem, *mygo.Window) {
				a.win.Update(func() { a.menuWeb("mobile") })
			}},
		}},
		{Label: locales.MenuWindow(), Submenu: []*mygo.MenuItem{
			{Role: mygo.RoleMinimize},
			{Role: mygo.RoleZoom},
			{Role: mygo.RoleToggleFullScreen},
			mygo.Separator(),
			{Role: mygo.RoleFront},
		}},
	}))
}

// accel 是快捷键表里 id 的第一组键在菜单上的写法，没有键时为空（菜单项不带快捷键）。
func (a *app) accel(id string) string {
	for _, kc := range a.keysFor(id) {
		if s := kc.accelerator(); s != "" {
			return s
		}
	}
	return ""
}

// closeTab 是 ⌘W：文件标签里关当前文件（没有文件时回到终端），否则关当前终端（先问）。
func (a *app) closeTab(m locales.Messages) {
	if isBoard(a.selected) {
		a.closeBoard()
		return
	}
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
		a.askClose(m, *cur)
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
	// 焦点格一时看不见（交接时它的终端不在列表里）：画面上的第一格当焦点格，不改记下的选中
	if a.focusHidden && w.ID == a.selected && len(a.cols) > 0 {
		for i := range w.Terminals {
			if w.Terminals[i].ID == a.cols[0].active {
				return &w.Terminals[i]
			}
		}
		return nil
	}
	// 还没选过（刚打开窗口）或选的没了：分了栏时选焦点格选中的终端，否则第一个
	pick := 0
	if f := a.splitFocusTerm(w); f != "" {
		pick = slices.IndexFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == f })
	}
	a.activeTerm[w.ID] = w.Terminals[pick].ID
	return &w.Terminals[pick]
}

// attachView 接入一个终端，失败时报错并返回 nil。
func (a *app) attachView(id string) *termView {
	conn := a.c.Attach(id)
	v := &termView{id: id, attached: time.Now()}
	if f, ok := a.carryFind[id]; ok {
		v.find = f
	}
	light, dark := terminalThemes()
	t, err := terminal.New(terminal.Options{
		Conn:      conn,
		Font:      a.termFont(),
		Theme:     light,
		DarkTheme: dark,
		// 回滚区与常驻进程一致，接入时的快照不会超出
		Scrollback: 4 << 20,
		// 按着 ⌘ 滑触控板（⌘ 点链接、⌘ 选区时常碰到）不缩放字号，照常滚动；⌘=、⌘- 仍可调
		NoWheelZoom:  true,
		CopyOnSelect: a.copyOnSelect(),
		// ⌘= ⌘- ⌘0 由界面的快捷键改设置里的字号（setFontSize），不让终端自己再临时缩放这一个：
		// 两边各改各的，设置页的数字和终端实际的字号会对不上
		OnZoom:      func(terminal.Zoom) bool { return true },
		LinkClick:   func(link string, mods ui.Modifiers) bool { return a.openLink(v, link, mods) },
		LinkUsable:  func(link string) bool { return a.linkUsable(v, link) },
		OnHoverLink: func(link string) { a.onHoverLink(v, link) },
		OnKey:       func(mods ui.Modifiers, key ui.Key) bool { return a.termKey(v, id, mods, key) },
		OnText:      func(text string) bool { return a.termFindText(v, text) },
		// 剪贴板里只有图片时 Cmd+V 拿不到文本：和 Orca 一样，由 Posse 把图片存成临时文件、粘贴它的路径，
		// 所有 agent 终端都这样。Claude 和 Codex 会把粘贴的图片路径当成附件，其他 agent 能按路径读图。
		// 让它们收 Ctrl+V 自己读剪贴板也行，但读剪贴板要调系统命令，粘贴时界面会闪一下
		ContextMenu: func(x, y float32) {
			a.termCtx, a.termCtxID = menuState{open: true, x: x, y: y}, id
		},
		OnPaste: func(d transfer.Data) bool {
			if v.term == nil {
				return false
			}
			// 访达里复制的文件（视频、图片、任何文件）：剪贴板的文本只是文件名，照拖进终端一样贴完整路径
			if paths := copiedFiles(d); len(paths) > 0 {
				agent := ""
				if _, t := a.findTerminal(id); t != nil {
					agent = t.Agent
				}
				sendPieces(v.term, dropPieces(paths, agent))
				return true
			}
			if text, _ := d.Read(transfer.Text); len(text) > 0 {
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
				i := slices.Index(a.panes[:], v)
				if i < 0 {
					return
				}
				if exited, _ := conn.Exited(); !exited {
					// 重新接入的是同一个终端：查找栏照旧（开着的由 termFindBar 按原来的词再搜，关着的留着上次的词）
					if v.find.open || v.find.query != "" {
						if a.carryFind == nil {
							a.carryFind = map[string]termFind{}
						}
						a.carryFind[v.id] = v.find
					}
					v.term.Close()
					a.panes[i] = nil
				}
			})
			go a.refresh()
		},
	})
	if err != nil {
		a.err = errtext.Text(err)
		return nil
	}
	v.term = t
	return v
}

// saveState 在后台写 ui.json；界面状态丢了不影响使用，失败不提示。
func (a *app) saveState() {
	if a.home == "" {
		return
	}
	s := a.state
	s.ProjectOrder = slices.Clone(s.ProjectOrder)
	s.TermOrder = maps.Clone(s.TermOrder)
	s.TermNames = maps.Clone(s.TermNames)
	s.TermNameBases = maps.Clone(s.TermNameBases)
	s.GitConsoleClearedAt = maps.Clone(s.GitConsoleClearedAt)
	s.FilesPanels, s.LogPanels = maps.Clone(s.FilesPanels), maps.Clone(s.LogPanels)
	s.MobileAddresses = slices.Clone(s.MobileAddresses)
	s.Files = cloneFilesState(s.Files)
	s.Keys = maps.Clone(s.Keys)
	s.DiscRead = maps.Clone(s.DiscRead)
	s.Panes = clonePanes(s.Panes)
	// 编号在这里取：协程开始的先后不等于发起的先后
	seq := uiSaveSeq.Add(1)
	go saveUIStateSeq(a.home, s, seq)
}

// pasteImage 把剪贴板里的图片存成临时文件，再把路径粘贴进终端。
func (a *app) pasteImage(v *termView, png []byte) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("posse-paste-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(path, png, 0o600); err != nil {
		a.win.Update(func() { a.err = appL().AppPasteImageError(errtext.Text(err)) })
		return
	}
	a.win.Update(func() {
		if a.live(v) {
			v.term.Paste(path)
		}
	})
}

func (a *app) setFontSize(size float32) {
	a.fontSize = min(max(size, minFontSize), maxFontSize)
	a.eachPane(func(v *termView) { v.term.SetFont(a.termFont()) })
	if a.state.FontSize != a.fontSize {
		a.state.FontSize = a.fontSize
		// 旧版的 ui.json 没有编辑器字号，启动时由终端字号推算；先记下来，免得下次启动跟着新的终端字号变
		if a.state.EditorFontSize == 0 {
			a.state.EditorFontSize = a.editorSize
		}
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
		a.update(func() { a.err = errtext.Text(err) })
	}
	return err
}

func (a *app) startTerminal(workspaceID, agent string) {
	go func() {
		var t terminalInfo
		err := a.call("terminal.create", map[string]any{"workspace": workspaceID, "agent": agent}, &t, 10*time.Second)
		// 先刷新让新终端进列表再选中：列表里还没有它时 currentTerminal 会把选中换回第一个
		a.refresh()
		if err == nil {
			a.win.Update(func() { a.openTerminal(workspaceID, t.ID) })
		}
	}()
}

func (a *app) askClose(m locales.Messages, t terminalInfo) {
	if !t.Running || a.state.CloseNoConfirm {
		go a.closeTerminal(t.ID)
		return
	}
	name := agentTitle(t.Agent)
	if name == "" {
		name = m.TermName()
	}
	a.confirm(m.AppCloseConfirm(name), m.AppCloseBody(), m.CommonClose(), func() { a.closeTerminal(t.ID) })
}

// askCloseAll 一次关掉几个终端：有程序在运行时只问一次。
func (a *app) askCloseAll(m locales.Messages, terms []terminalInfo) {
	running := 0
	for _, t := range terms {
		if t.Running {
			running++
		}
	}
	closeAll := func() {
		for _, t := range terms {
			a.call("terminal.close", map[string]any{"terminal": t.ID}, nil, 10*time.Second)
		}
		a.refresh()
	}
	if running == 0 || a.state.CloseNoConfirm {
		go closeAll()
		return
	}
	a.confirm(m.AppCloseAllConfirm(len(terms)), m.AppCloseAllBody(), m.CommonClose(), closeAll)
}

func (a *app) closeTerminal(id string) {
	// 先从分栏里拿掉：关掉之后轮询可能先一步把它从列表删了，就找不到它在哪个工作区、挨着谁
	a.update(func() {
		if w, _ := a.findTerminal(id); w != nil {
			a.dropColumnKey(w, id)
		}
	})
	a.call("terminal.close", map[string]any{"terminal": id}, nil, 10*time.Second)
	a.refresh()
}

// askRemoveProject 从 Posse 里移除 main 工作区 w 所在的项目：只删记录，不碰磁盘上的文件。
// 常驻进程要求先删掉 worktree 工作区，这里先挡住并说明要删哪些。
func (a *app) askRemoveProject(m locales.Messages, w workspace) {
	var worktrees []string
	for _, x := range a.byProject[w.ProjectID] {
		if x.Kind == "worktree" {
			worktrees = append(worktrees, x.Name)
		}
	}
	gone := w.Missing
	for _, x := range a.byProject[w.ProjectID] {
		if x.Kind == "worktree" && a.repoGone(x) {
			gone = true
		}
	}
	if len(worktrees) > 0 && gone {
		// 项目目录不在了（也可能是磁盘没挂载）：git 删不了它的 worktree，常驻进程带 force 时连同 worktree 工作区一起移除
		var paths []string
		for _, x := range a.byProject[w.ProjectID] {
			if x.Kind == "worktree" && !x.Missing {
				paths = append(paths, x.Path)
			}
		}
		remove := func(discard bool) func() {
			return func() {
				a.call("project.rm", map[string]any{"project": w.ProjectID, "force": true, "discardFiles": discard}, nil, 30*time.Second)
				a.refresh()
			}
		}
		if len(paths) == 0 {
			a.confirm(m.AppRemoveProjectConfirm(w.Project), m.AppRemoveProjectBody(), m.CommonRemove(), remove(false))
			return
		}
		a.confirmChoice(m.AppOrphanProjectTitle(w.Project), m.AppOrphanProjectBody(strings.Join(paths, "\n")),
			m.AppOrphanKeepFiles(), remove(false), m.AppOrphanDeleteFiles(), remove(true))
		return
	}
	if len(worktrees) > 0 {
		a.err = m.AppRemoveWorktreesFirst(w.Project, strings.Join(worktrees, m.AppListSeparator()))
		return
	}
	a.confirm(m.AppRemoveProjectConfirm(w.Project), m.AppRemoveProjectBody(), m.CommonRemove(), func() {
		a.call("project.rm", map[string]any{"project": w.ProjectID, "force": true}, nil, 30*time.Second)
		a.refresh()
	})
}

// showBranches 给从别处加进来的 worktree 填上显示名 Shown：当前分支，游离时不填。
func showBranches(ws []workspace) {
	mainPath := map[string]string{}
	for _, w := range ws {
		if w.Kind == "main" {
			mainPath[w.ProjectID] = w.Path
		}
	}
	for i := range ws {
		w := &ws[i]
		if m := mainPath[w.ProjectID]; w.Kind == "folder" && !w.Missing && m != "" && filepath.Clean(w.Path) != filepath.Clean(m) {
			if head, detached := gitx.HeadRef(w.Path); head != "" && !detached {
				w.Shown = head
			}
		}
	}
}

// repoGone 报告 worktree 工作区 w 用的仓库已经不在了（项目目录被删、挪走，或所在的磁盘没挂载）。
// 这时 git 删不了它，也查不了它目录里有没有没提交的改动。
func (a *app) repoGone(w workspace) bool {
	main := ""
	for _, x := range a.byProject[w.ProjectID] {
		if x.Kind == "main" {
			main = x.Path
		}
	}
	if main == "" {
		return false
	}
	missing := func(p string) bool {
		_, err := os.Stat(p)
		return errors.Is(err, fs.ErrNotExist)
	}
	if missing(main) {
		return true
	}
	if len(w.Repos) == 0 {
		return missing(filepath.Join(main, ".git"))
	}
	for _, r := range w.Repos {
		if missing(filepath.Join(main, r.Path)) {
			return true
		}
	}
	return false
}

// askRemoveOrphan 删仓库已经不在的 worktree 工作区：两个动作，默认只移除记录、留着目录（可能只是磁盘没挂载），
// 另一个连同 $POSSE_HOME/worktrees 里的目录一起删。
func (a *app) askRemoveOrphan(m locales.Messages, w workspace) {
	remove := func(discard bool) func() {
		return func() {
			a.call("workspace.rm", map[string]any{"workspace": w.ID, "force": true, "discardFiles": discard}, nil, 30*time.Second)
			a.refresh()
		}
	}
	a.confirmChoice(m.AppOrphanWorkspaceTitle(displayName(&w)), m.AppOrphanWorkspaceBody(w.Path),
		m.AppOrphanKeepFiles(), remove(false), m.AppOrphanDeleteFiles(), remove(true))
}

// askRemoveMissing 移除目录已经不在的工作区：只删记录，不碰分支；项目目录不在时移除的是整个项目。
func (a *app) askRemoveMissing(m locales.Messages, w workspace) {
	if w.Kind == "main" {
		a.askRemoveProject(m, w)
		return
	}
	repo := ""
	for _, x := range a.byProject[w.ProjectID] {
		if x.Kind == "main" && !x.Missing {
			repo = x.Path
		}
	}
	a.confirm(m.AppRemoveWorkspaceConfirm(displayName(&w)), m.AppRemoveWorkspaceBody(), m.CommonRemove(), func() {
		go func() {
			if a.call("workspace.rm", map[string]any{"workspace": w.ID, "force": true}, nil, 30*time.Second) != nil {
				return
			}
			// 清掉 git 的 worktree 记录，分支才不算还检出在那里
			if repo != "" {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				gitx.PruneWorktrees(ctx, repo)
				cancel()
			}
			a.refresh()
		}()
	})
}

// relocateWorkspace 给目录已经不在的工作区重新选目录（目录在外面挪了地方）。
func (a *app) relocateWorkspace(m locales.Messages, w workspace) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: m.AppRelocateTitle(displayName(&w)), ButtonLabel: m.AppRelocateButton(),
		DefaultPath: nearestDir(w.Path), Directory: true})
	if err != nil || len(paths) == 0 {
		return
	}
	if a.call("workspace.relocate", map[string]any{"workspace": w.ID, "path": paths[0]}, nil, 30*time.Second) != nil {
		return
	}
	a.refresh()
}

// nearestDir 是 path 往上第一个还在的目录，选目录的对话框从这里开始。
func nearestDir(path string) string {
	for d := filepath.Dir(path); ; d = filepath.Dir(d) {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return d
		}
		if d == filepath.Dir(d) {
			return ""
		}
	}
}

func (a *app) confirm(title, body, verb string, do func()) {
	a.confirmTitle, a.confirmBody, a.confirmVerb, a.confirmDo = title, body, verb, do
	a.confirmAltVerb, a.confirmAltDo = "", nil
	a.confirming = true
}

// confirmChoice 是有两个动作的确认框：verb、do 是稳妥的那个，打开时选中它（回车按它）；alt 是会丢东西的那个。
func (a *app) confirmChoice(title, body, verb string, do func(), altVerb string, alt func()) {
	a.confirm(title, body, verb, do)
	a.confirmAltVerb, a.confirmAltDo = altVerb, alt
}

func (a *app) addProject(m locales.Messages) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: m.AppAddProjectTitle(), ButtonLabel: m.AppAddProjectButton(), Directory: true})
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
	a.newRepos, a.newReposFor = map[string]bool{}, ""
	if w := a.workspace(a.selected); w != nil {
		a.newProject = w.Project
		// 默认勾上正在看的仓库
		if v := a.files[w.ID]; v != nil && v.git.repo != "" {
			if rel, ok := repoRel(v, v.git); ok {
				a.newRepos[rel], a.newReposFor = true, w.Project
			}
		}
	} else if len(a.projects) > 0 {
		a.newProject = a.projects[0].Name
	}
}

// projectRepos 是项目目录里找到的仓库（相对项目目录的路径）；项目的文件栏还没找过就先去找，找完下一帧再给。
func (a *app) projectRepos(name string) []string {
	for _, w := range a.workspaces {
		if w.Project != name || w.Kind != "main" {
			continue
		}
		v := a.filesFor(&w)
		if !v.repos.scanned {
			a.scanRepos(v)
			return nil
		}
		var out []string
		for _, g := range v.repos.list {
			if rel, ok := repoRel(v, g); ok {
				out = append(out, rel)
			}
		}
		return out
	}
	return nil
}

// repoRel 是仓库在工作区里的相对路径（斜杠分隔）；工作区所在的上级仓库不算。
func repoRel(v *filesView, g *gitState) (string, bool) {
	rel, err := filepath.Rel(v.root, g.repo)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func (a *app) createWorkspace(m locales.Messages, params map[string]any) {
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
				a.err = m.AppSetupError(truncate(w.Setup.Output, 200))
			}
			a.selected, a.revealSel = w.ID, true
			if len(w.Terminals) > 0 {
				a.openTerminal(w.ID, w.Terminals[0].ID)
			}
		})
	}
	a.refresh()
}

// dropGoneWorktrees 在每秒的轮询里找出目录已被删掉的 worktree 工作区（posse 建的和加进来的），
// 删掉它的记录、清掉 git 的 worktree 记录，返回剩下的。上一级目录也不在时不动：可能是外接盘没挂上。
// 里面还有终端在跑的也不动，等终端退出：删了记录，终端就没有归属了。
func (a *app) dropGoneWorktrees(ctx context.Context, ws []workspace) []workspace {
	mainPath := map[string]string{}
	for _, w := range ws {
		if w.Kind == "main" {
			mainPath[w.ProjectID] = w.Path
		}
	}
	var gone []workspace
	keep := ws[:0:0]
	for _, w := range ws {
		isWT := w.Kind == "worktree" || w.Kind == "folder" && mainPath[w.ProjectID] != "" && filepath.Clean(w.Path) != filepath.Clean(mainPath[w.ProjectID])
		if isWT && !slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.Running }) {
			if _, err := os.Stat(w.Path); errors.Is(err, fs.ErrNotExist) {
				if _, err := os.Stat(filepath.Dir(w.Path)); err == nil {
					gone = append(gone, w)
					continue
				}
			}
		}
		keep = append(keep, w)
	}
	if len(gone) == 0 {
		return ws
	}
	var names []string
	for _, w := range gone {
		if a.c.Call(ctx, "workspace.rm", map[string]any{"workspace": w.ID, "force": true}, nil) != nil {
			keep = append(keep, w)
			continue
		}
		names = append(names, displayName(&w))
		if p := mainPath[w.ProjectID]; p != "" {
			gitx.PruneWorktrees(ctx, p)
		}
	}
	if len(names) > 0 && a.win != nil {
		m := appL()
		a.win.Update(func() { a.notify(m.AppWorkspacesRemoved(strings.Join(names, m.AppListSeparator())), "", nil) })
	}
	return keep
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
