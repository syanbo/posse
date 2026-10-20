package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// uiState 是只有界面关心的状态，存在 $HIVE_HOME/ui.json：放进常驻进程要改协议，
// 而重启常驻进程会断掉所有终端。
type uiState struct {
	Selected             string   `json:"selected,omitempty"`             // 侧栏上次选中的一项，下次打开时选回来
	ProjectOrder         []string `json:"projectOrder,omitempty"`         // 侧栏里项目的顺序，拖动调整
	RightPanel           string   `json:"rightPanel,omitempty"`           // 右侧开着的浮层面板：panelUsage、panelHistory、panelImages 或空
	FilesPanel           bool     `json:"filesPanel,omitempty"`           // 文件栏开着（停靠在右边，不是浮层）
	FilesPanelWidth      float32  `json:"filesPanelWidth,omitempty"`      // 文件栏的宽度，0 表示默认
	SidebarHidden        bool     `json:"sidebarHidden,omitempty"`        // 左边的侧栏收起了
	SessionButtonsHidden bool     `json:"sessionButtonsHidden,omitempty"` // 终端右上角的会话按钮收成了右边缘的把手
	SidebarWidth         float32  `json:"sidebarWidth,omitempty"`         // 侧栏的宽度，0 表示默认
	LogPanel             bool     `json:"logPanel,omitempty"`             // 底部的 Git 日志浮层开着
	LogPanelHeight       float32  `json:"logPanelHeight,omitempty"`       // Git 日志浮层的高度，0 表示默认
	CompareSplit         float32  `json:"compareSplit,omitempty"`         // 分支比较上一栏占的比例，0 表示各一半
	MobileAddress        string   `json:"mobileAddress,omitempty"`        // 配对手机时写进配对码的地址，空是自动（Tailscale 或局域网）
	MobileAddresses      []string `json:"mobileAddresses,omitempty"`      // 用过的自定义地址，配对对话框里可选
	FontSize             float32  `json:"fontSize,omitempty"`             // 终端字号，0 表示默认
	UsageRemaining       bool     `json:"usageRemaining,omitempty"`       // 底栏的套餐用量显示剩余，而不是已用
	// GitConsoleCleared 是在 Git 操作记录里点清空的时间，更早的记录（界面的和终端的）都不再显示
	GitConsoleCleared time.Time `json:"gitConsoleCleared,omitzero"`
	// TermOrder 是各工作区终端的顺序（标签栏和侧栏的会话共用），拖动调整，键是工作区 ID
	TermOrder map[string][]string `json:"termOrder,omitempty"`
	// Files 是各工作区文件标签打开的文件和展开的目录，键是工作区 ID
	Files map[string]filesState `json:"files,omitempty"`
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
	return s
}

// saveUIState 先写临时文件再改名，窗口中途退出也不会留下半个文件。
func saveUIState(home string, s uiState) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := uiStatePath(home) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, uiStatePath(home))
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
