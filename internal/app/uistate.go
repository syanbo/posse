package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// uiState 是只有界面关心的状态，存在 $POSSE_HOME/ui.json：放进常驻进程要改协议，
// 而重启常驻进程会断掉所有终端。
type uiState struct {
	Selected             string    `json:"selected,omitempty"`             // 侧栏上次选中的一项，下次打开时选回来
	ProjectOrder         []string  `json:"projectOrder,omitempty"`         // 侧栏里项目的顺序，拖动调整
	RightPanel           string    `json:"rightPanel,omitempty"`           // 右侧开着的浮层面板：panelUsage、panelImages 或空
	FilesPanel           bool      `json:"filesPanel,omitempty"`           // 旧版不分项目的文件栏开关，读到后记到当前项目上
	FilesPanelWidth      float32   `json:"filesPanelWidth,omitempty"`      // 文件栏的宽度，0 表示默认
	FilesPanelHistory    bool      `json:"filesPanelHistory,omitempty"`    // 文件栏顶上选的是"历史"（当前项目的会话），不是文件
	FilesPanelShared     bool      `json:"filesPanelShared,omitempty"`     // 文件栏开关所有项目共用（记在 FilesPanelOn），不按项目记
	FilesPanelOn         bool      `json:"filesPanelOn,omitempty"`         // 共用时文件栏开着
	SidebarHidden        bool      `json:"sidebarHidden,omitempty"`        // 左边的侧栏收起了
	SessionButtonsHidden bool      `json:"sessionButtonsHidden,omitempty"` // 终端右上角的会话按钮收成了右边缘的把手
	SidebarWidth         float32   `json:"sidebarWidth,omitempty"`         // 侧栏的宽度，0 表示默认
	ProjectMetaHidden    bool      `json:"projectMetaHidden,omitempty"`    // 侧栏的项目行不显示分支和路径，只留名字
	WorkspaceMetaHidden  bool      `json:"workspaceMetaHidden,omitempty"`  // 侧栏项目下面的工作区行不显示分支，只留名字
	LogPanel             bool      `json:"logPanel,omitempty"`             // 旧版不分项目的日志浮层开关，读到后记到当前项目上
	LogPanelHeight       float32   `json:"logPanelHeight,omitempty"`       // Git 日志浮层的高度，0 表示默认
	BranchPopWidth       float32   `json:"branchPopWidth,omitempty"`       // 分支弹窗的宽度，0 表示默认
	CompareSplit         float32   `json:"compareSplit,omitempty"`         // 分支比较上一栏占的比例，0 表示各一半
	DiffSplit            float32   `json:"diffSplit,omitempty"`            // diff 左右对照时旧版一侧占的比例，0 表示各一半
	LogRefsWidth         float32   `json:"logRefsWidth,omitempty"`         // 日志左边分支树的宽度，0 表示默认
	LogDetailWidth       float32   `json:"logDetailWidth,omitempty"`       // 日志、逐行追溯右边提交详情的宽度，0 表示默认
	MobileAddress        string    `json:"mobileAddress,omitempty"`        // 配对手机时写进配对码的地址，空是自动（Tailscale 或局域网）
	MobileAddresses      []string  `json:"mobileAddresses,omitempty"`      // 用过的自定义地址，配对对话框里可选
	FontSize             float32   `json:"fontSize,omitempty"`             // 终端字号，0 表示默认
	TermFont             string    `json:"termFont,omitempty"`             // 终端字体，空表示默认
	EditorFont           string    `json:"editorFont,omitempty"`           // 编辑器字体，空表示默认
	EditorFontSize       float32   `json:"editorFontSize,omitempty"`       // 编辑器字号，0 表示默认（旧版跟着终端小一号）
	TermWeight           int       `json:"termWeight,omitempty"`           // 终端正文的字重，0 表示默认（400）
	TermBoldWeight       int       `json:"termBoldWeight,omitempty"`       // 终端粗体的字重，0 表示比正文重 200、至少 600
	TermLineHeight       float32   `json:"termLineHeight,omitempty"`       // 终端行高，字体自身行高的倍数，0 表示 1
	TermSmooth           bool      `json:"termSmooth,omitempty"`           // 终端用系统的字体平滑画字（更粗），默认不用
	TermCopyOnSelect     bool      `json:"termCopyOnSelect,omitempty"`     // 终端里选中文字松手就复制到剪贴板，默认不复制
	LinkClickOpens       bool      `json:"linkClickOpens,omitempty"`       // 终端里单击链接直接打开、Cmd+点击弹卡片，默认反过来
	LinkSwapSystem       bool      `json:"linkSwapSystem,omitempty"`       // 终端链接直接打开（Posse 里）和交给系统（浏览器、默认程序）的按法互换
	NotifyOff            bool      `json:"notifyOff,omitempty"`            // 不发 agent 和任务的系统通知
	BadgeOff             bool      `json:"badgeOff,omitempty"`             // Dock 图标上不显示待处理和未读的数
	EditorWeight         int       `json:"editorWeight,omitempty"`         // 编辑器的字重，0 表示默认（400）
	EditorBoldWeight     int       `json:"editorBoldWeight,omitempty"`     // 编辑器粗体（Markdown 标题、加粗）的字重，规则同终端
	EditorLineHeight     float32   `json:"editorLineHeight,omitempty"`     // 编辑器行高，字号的倍数，0 表示 1.5
	EditorSmooth         bool      `json:"editorSmooth,omitempty"`         // 编辑器用系统的字体平滑画字（更粗），默认不用
	UsageRemaining       bool      `json:"usageRemaining,omitempty"`       // 底栏的套餐用量显示剩余，而不是已用
	ImageCheckerOff      bool      `json:"imageCheckerOff,omitempty"`      // 图片预览的透明处不铺棋盘格
	UsageSkillsOnly      bool      `json:"usageSkillsOnly,omitempty"`      // "技能与工具"面板只看技能和 MCP，不列命令行程序
	HistoryByFolder      bool      `json:"historyByFolder,omitempty"`      // 历史页的会话按文件夹分组，而不是按时间
	ChangesByDir         bool      `json:"changesByDir,omitempty"`         // 文件栏改动页的文件按目录排成树，而不是平铺
	IconSize             float32   `json:"iconSize,omitempty"`             // 图标大小（iconSizes 的一档），0 表示默认
	UISmooth             bool      `json:"uiSmooth,omitempty"`             // 界面文字用系统的字体平滑（更粗），默认不用
	UIFontSize           float32   `json:"uiFontSize,omitempty"`           // 界面正文字号，0 表示默认
	UIWeight             int       `json:"uiWeight,omitempty"`             // 界面正文字重，0 表示默认
	UIBoldWeight         int       `json:"uiBoldWeight,omitempty"`         // 界面标题的字重，0 表示默认
	MarkdownFontSize     float32   `json:"markdownFontSize,omitempty"`     // Markdown 预览和历史页对话的正文字号，0 表示跟界面字号
	UpdateCheckedAt      time.Time `json:"updateCheckedAt,omitzero"`       // 上次成功检查更新的时间，距今满 6 小时才自动再查
	UpdateDismissed      string    `json:"updateDismissed,omitempty"`      // 在侧栏关掉了这个版本的"已就绪"卡片
	// GitConsoleCleared 是旧版在 Git 操作记录里点清空的时间（当时不分项目），更早的记录都不再显示
	GitConsoleCleared time.Time `json:"gitConsoleCleared,omitzero"`
	// GitConsoleClearedAt 是各项目在操作记录里点清空的时间，键是项目 ID，更早的不再显示
	GitConsoleClearedAt map[string]time.Time `json:"gitConsoleClearedAt,omitempty"`
	// FilesPanels、LogPanels 是开着文件栏（停靠在右边）、底部 Git 日志浮层的项目，键是项目 ID：
	// 在 A 项目打开，切到 B 项目不跟着打开
	FilesPanels map[string]bool `json:"filesPanels,omitempty"`
	LogPanels   map[string]bool `json:"logPanels,omitempty"`
	// TermOrder 是各工作区终端的顺序（标签栏和侧栏的会话共用），拖动调整，键是工作区 ID
	TermOrder map[string][]string `json:"termOrder,omitempty"`
	// TermNames 是还没交给常驻进程的标签名，键是终端 ID，空字符串表示恢复默认：常驻进程报上来的和它一样就删掉。
	// 旧版界面把所有标签名都存在这里，读到后交给常驻进程；终端没了也删掉
	TermNames map[string]string `json:"termNames,omitempty"`
	// Files 是各工作区文件标签打开的文件和展开的目录，键是工作区 ID
	Files map[string]filesState `json:"files,omitempty"`
	// Keys 是改过的快捷键（keymap.go），键是操作 ID，值是按键写法（Super+Shift+k）；空列表表示不用快捷键
	Keys map[string][]string `json:"keys,omitempty"`
	// Splits 是左右分屏的工作区，键是工作区 ID（split.go）
	Splits map[string]splitState `json:"splits,omitempty"`
}

// filesState 是一个工作区的文件标签，路径都是绝对路径。
type filesState struct {
	Open     []string `json:"open,omitempty"`
	Current  string   `json:"current,omitempty"`
	Expanded []string `json:"expanded,omitempty"`
}

// cloneFilesState 深拷贝，交给写 ui.json 的协程，界面线程接着改原来的不会有数据竞争。
func cloneFilesState(m map[string]filesState) map[string]filesState {
	if m == nil {
		return nil
	}
	out := make(map[string]filesState, len(m))
	for k, v := range m {
		out[k] = filesState{Open: slices.Clone(v.Open), Current: v.Current, Expanded: slices.Clone(v.Expanded)}
	}
	return out
}

func uiStatePath(home string) string { return filepath.Join(home, "ui.json") }

func loadUIState(home string) uiState {
	var s uiState
	if b, err := os.ReadFile(uiStatePath(home)); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.RightPanel == panelFiles {
		s.RightPanel, s.FilesPanel = "", true
	}
	// 历史以前也是浮层面板，现在在文件栏里
	if s.RightPanel == panelHistory {
		s.RightPanel, s.FilesPanelHistory = "", true
	}
	return s
}

// saveUIState 先写临时文件再改名，窗口中途退出也不会留下半个文件。
func saveUIState(home string, s uiState) error {
	return saveUIStateSeq(home, s, uiSaveSeq.Add(1))
}

// 每次保存在后台协程里写，连着改几下就有几个协程同时写：共用的临时文件会被写成两份内容拼起来，
// 晚开始的协程也可能先写完、被早的盖掉。按发起的顺序编号，一次只写一个，比已写的旧就不写。
var (
	uiSaveSeq atomic.Uint64
	uiSave    struct {
		sync.Mutex
		written uint64
	}
)

func saveUIStateSeq(home string, s uiState, seq uint64) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	uiSave.Lock()
	defer uiSave.Unlock()
	if seq < uiSave.written {
		return nil
	}
	tmp := uiStatePath(home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, uiStatePath(home)); err != nil {
		return err
	}
	uiSave.written = seq
	return nil
}

// sortProjects 按 order 排列项目；不在 order 里的（新加的）按名称排在后面。
func sortProjects(ps []project, order []string) {
	rank := func(id string) int {
		if i := slices.Index(order, id); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(ps, func(a, b project) int {
		if ra, rb := rank(a.ID), rank(b.ID); ra != rb {
			return ra - rb
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

// moveProject 把 id 挪到 target 的位置：往下拖落在 target 后面，往上拖落在前面，首尾都能拖到。
func moveProject(ps []project, id, target string) []string {
	order := make([]string, len(ps))
	for i, p := range ps {
		order[i] = p.ID
	}
	return moveID(order, id, target)
}

// sortTerminals 按 order 排列终端；不在 order 里的（新开的）保持常驻进程给的创建顺序，排在后面。
func sortTerminals(ts []terminalInfo, order []string) {
	if len(order) == 0 {
		return
	}
	rank := func(id string) int {
		if i := slices.Index(order, id); i >= 0 {
			return i
		}
		return len(order)
	}
	slices.SortStableFunc(ts, func(a, b terminalInfo) int { return rank(a.ID) - rank(b.ID) })
}

// moveTerminal 把终端 id 挪到 target 的位置，返回新的顺序，规则同 moveProject。
func moveTerminal(ts []terminalInfo, id, target string) []string {
	order := make([]string, len(ts))
	for i, t := range ts {
		order[i] = t.ID
	}
	return moveID(order, id, target)
}

// moveID 把 order 里的 id 挪到 target 的位置。
func moveID(order []string, id, target string) []string {
	from, to := slices.Index(order, id), slices.Index(order, target)
	if from < 0 || to < 0 || from == to {
		return order
	}
	order = slices.Delete(order, from, from+1)
	return slices.Insert(order, to, id)
}

// projectIndex 是项目在当前顺序里的位置，找不到为 -1。
func projectIndex(ps []project, id string) int {
	return slices.IndexFunc(ps, func(p project) bool { return p.ID == id })
}
