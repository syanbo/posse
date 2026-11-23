package usage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

// FetchAntigravity 用 agy 自己的账号和配额查询。临时目录避免加载当前项目的配置。
func FetchAntigravity(ctx context.Context, executable string, env []string, now time.Time) (Usage, error) {
	dir, err := os.MkdirTemp("", "posse-agy-usage-")
	if err != nil {
		return Usage{}, err
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, executable, "--settings", `{"disableSlashCommands":false}`, "-p", "/usage", "--output-format", "json", "--print-timeout", "10s")
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return Usage{}, fmt.Errorf("agy /usage: %w", err)
	}
	return parseAntigravityUsage(out, now)
}

type antigravityBucket struct {
	AntigravityStatusQuota
	Name   string `json:"name"`
	ID     string `json:"id"`
	Window string `json:"window"`
}

func parseAntigravityUsage(raw []byte, now time.Time) (Usage, error) {
	var data struct {
		Status  string `json:"status"`
		Turns   *int   `json:"num_turns"`
		Command struct {
			Name string `json:"name"`
			Data struct {
				Groups []struct {
					Name    string              `json:"name"`
					Buckets []antigravityBucket `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		return Usage{}, fmt.Errorf("agy /usage did not return JSON: %w", err)
	}
	if data.Status != "SUCCESS" || data.Turns == nil || *data.Turns != 0 || data.Command.Name != "usage" {
		return Usage{}, fmt.Errorf("agy did not return a quota reading")
	}
	u := Usage{Agent: "antigravity", UpdatedAt: now.UnixMilli()}
	for _, group := range data.Command.Data.Groups {
		counts := make(map[int]int)
		for _, b := range group.Buckets {
			if b.hasFraction() {
				counts[antigravityMinutes(b.Window)]++
			}
		}
		for _, b := range group.Buckets {
			if !b.hasFraction() {
				continue
			}
			minutes := antigravityMinutes(b.Window)
			name := strings.TrimSpace(group.Name)
			if name == "" {
				name = strings.TrimSpace(b.Name)
			} else if counts[minutes] > 1 && b.Name != "" {
				name += " / " + b.Name
			}
			if name == "" {
				name = b.ID
			}
			if name == "" {
				continue
			}
			w := Window{QuotaID: b.ID, Name: name, Minutes: minutes, Percent: (1 - *b.Remaining) * 100}
			if reset, err := time.Parse(time.RFC3339Nano, b.Reset); err == nil && reset.UnixMilli() > 0 {
				w.ResetsAt = reset.UnixMilli()
			}
			u.Windows = append(u.Windows, w)
		}
	}
	if len(u.Windows) == 0 {
		return Usage{}, fmt.Errorf("agy did not return any percentage quotas")
	}
	return u, nil
}

type AntigravityStatusQuota struct {
	Remaining *float64 `json:"remaining_fraction"`
	Reset     string   `json:"reset_time"`
	Disabled  bool     `json:"disabled"`
}

// UpdateAntigravityStatusQuota 把状态栏读数合并到 /usage 确认过的额度池。
func (s *Store) UpdateAntigravityStatusQuota(quota map[string]AntigravityStatusQuota, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.f.Usages, func(u Usage) bool { return u.Agent == "antigravity" })
	if i < 0 || s.f.Off {
		return false
	}
	u := s.f.Usages[i]
	if u.UpdatedAt > now.UnixMilli() {
		return false
	}
	windows := slices.Clone(u.Windows)
	for j, w := range windows {
		q, ok := quota[w.QuotaID]
		if !ok || w.QuotaID == "" || !q.hasFraction() {
			continue
		}
		reset, err := time.Parse(time.RFC3339Nano, q.Reset)
		ms := reset.UnixMilli()
		percent := (1 - *q.Remaining) * 100
		// 多个 agy 终端各有配置缓存，同一个计费窗口内旧读数不能把消耗往回拨。
		if err != nil || ms <= now.UnixMilli() || ms < w.ResetsAt || ms == w.ResetsAt && percent < w.Percent {
			continue
		}
		windows[j].ResetsAt, windows[j].Percent = ms, percent
	}
	if slices.Equal(windows, u.Windows) {
		return false
	}
	u.Windows, u.UpdatedAt = windows, now.UnixMilli()
	s.f.Usages[i] = u
	s.saveLocked()
	return true
}

func (q AntigravityStatusQuota) hasFraction() bool {
	return !q.Disabled && q.Remaining != nil && *q.Remaining >= 0 && *q.Remaining <= 1
}

func antigravityMinutes(window string) int {
	switch window {
	case "five_hours", "five_hour", "5h":
		return 5 * 60
	case "daily":
		return 24 * 60
	case "weekly":
		return 7 * 24 * 60
	case "monthly":
		return 30 * 24 * 60
	}
	return 0
}
