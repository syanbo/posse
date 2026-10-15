package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// uiState 是只有界面关心的状态，存在 $HIVE_HOME/ui.json：放进常驻进程要改协议，
// 而重启常驻进程会断掉所有终端。
type uiState struct {
	Selected     string   `json:"selected,omitempty"`     // 侧栏上次选中的一项，下次打开时选回来
	ProjectOrder []string `json:"projectOrder,omitempty"` // 侧栏里项目的顺序，拖动调整
	RightPanel   string   `json:"rightPanel,omitempty"`   // 右侧开着的面板：panelUsage、panelHistory、panelImages 或空
}

func uiStatePath(home string) string { return filepath.Join(home, "ui.json") }

func loadUIState(home string) uiState {
	var s uiState
	if b, err := os.ReadFile(uiStatePath(home)); err == nil {
		json.Unmarshal(b, &s)
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
