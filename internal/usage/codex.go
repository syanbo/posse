package usage

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Codex 每轮在会话文件里写一条 token_count 事件，payload.rate_limits 是这一刻的限额：
// primary（5 小时）、secondary（每周）各有 used_percent、window_minutes 和秒级 resets_at，另有 plan_type。
// 用 API key 登录时 rate_limits 是 null。

var codexMarker = []byte(`"rate_limits"`)

// CodexRollout 从会话文件末尾往前找最后一份限额。
func CodexRollout(path string) (Usage, bool) {
	return lastLine(path, codexMarker, codexLine)
}

func codexLine(line []byte) (Usage, bool) {
	type window struct {
		Used     *float64 `json:"used_percent"`
		Minutes  int      `json:"window_minutes"`
		ResetsAt num      `json:"resets_at"`
	}
	var rec struct {
		Timestamp string `json:"timestamp"`
		Type      string `json:"type"`
		Payload   struct {
			Type       string `json:"type"`
			RateLimits *struct {
				LimitID   string  `json:"limit_id"`
				Primary   *window `json:"primary"`
				Secondary *window `json:"secondary"`
				PlanType  string  `json:"plan_type"`
			} `json:"rate_limits"`
		} `json:"payload"`
	}
	if !decode(line, &rec) || rec.Type != "event_msg" || rec.Payload.Type != "token_count" {
		return Usage{}, false
	}
	rl := rec.Payload.RateLimits
	// 别的 limit_id 是单独计的模型额度，不是账号的总额度
	if rl == nil || (rl.LimitID != "" && rl.LimitID != "codex") {
		return Usage{}, false
	}
	u := Usage{Agent: "codex", Plan: titleWord(rl.PlanType), UpdatedAt: isoMs(rec.Timestamp)}
	for _, w := range []*window{rl.Primary, rl.Secondary} {
		if w != nil && w.Used != nil {
			u.Windows = append(u.Windows, Window{Percent: clampPercent(*w.Used), Minutes: w.Minutes, ResetsAt: epochMs(w.ResetsAt.v)})
		}
	}
	return u, len(u.Windows) > 0 && u.UpdatedAt > 0
}

// CodexHome 是 Codex 的数据目录，设了 CODEX_HOME 时用它。
func CodexHome() string {
	if dir := os.Getenv("CODEX_HOME"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex")
}

// CodexLatest 从最近的几个会话文件里找最新的限额，常驻进程启动时用一次。
// 会话按 sessions/年/月/日/ 存放，只看最近的日期目录。
func CodexLatest(home string) (Usage, bool) {
	var files []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		if depth == 3 {
			for _, e := range ents {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
					files = append(files, filepath.Join(dir, e.Name()))
				}
			}
			return
		}
		// 目录名是数字，倒序就是从新到旧
		for i := len(ents) - 1; i >= 0 && len(files) < 5; i-- {
			if ents[i].IsDir() {
				walk(filepath.Join(dir, ents[i].Name()), depth+1)
			}
		}
	}
	walk(filepath.Join(home, "sessions"), 0)
	slices.SortFunc(files, func(a, b string) int { return modTime(b) - modTime(a) })
	var best Usage
	for i, f := range files {
		if i == 5 {
			break
		}
		if u, ok := CodexRollout(f); ok && u.UpdatedAt > best.UpdatedAt {
			best = u
		}
	}
	return best, best.UpdatedAt > 0
}

func modTime(path string) int {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return int(fi.ModTime().Unix())
}
