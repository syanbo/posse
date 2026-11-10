package usage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Claude 的读数有两处，都不用联网：
//   - statusLine 命令的输入：Claude 收到回复后把 rate_limits（five_hour、seven_day，used_percentage 和秒级 resets_at）
//     交给 statusLine，posse 在用户没配 statusLine 时注入一个不输出的 posse statusline 转过来；
//   - ~/.claude.json 的 cachedUsageUtilization：Claude 自己取用量（用户看 /usage）时存的，带 fetchedAtMs。
// 套餐名取 ~/.claude.json 的 oauthAccount（登录时存的账户信息），见 claudePlan。

// ClaudeStatusLine 解析 statusLine 的输入；没有订阅额度（API key、Bedrock 等）时没有 rate_limits。
func ClaudeStatusLine(raw []byte, now time.Time) (Usage, bool) {
	type window struct {
		Used     *float64 `json:"used_percentage"`
		ResetsAt num      `json:"resets_at"`
	}
	var in struct {
		RateLimits *struct {
			FiveHour *window `json:"five_hour"`
			SevenDay *window `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &in) != nil || in.RateLimits == nil {
		return Usage{}, false
	}
	u := Usage{Agent: "claude", UpdatedAt: now.UnixMilli()}
	add := func(w *window, minutes int) {
		if w == nil || w.Used == nil {
			return
		}
		u.Windows = append(u.Windows, Window{Percent: clampPercent(*w.Used), Minutes: minutes, ResetsAt: epochMs(w.ResetsAt.v)})
	}
	add(in.RateLimits.FiveHour, 300)
	add(in.RateLimits.SevenDay, 7*24*60)
	return u, len(u.Windows) > 0
}

// ClaudeConfigPath 是 Claude 的全局状态文件：设了 CLAUDE_CONFIG_DIR 时在那里面。
func ClaudeConfigPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// ClaudeCache 读 Claude 自己缓存的用量。比 statusLine 多几样：按模型计的周额度（limits 里的
// weekly_scoped，比如 Fable）、本周用量用在了哪（seven_day_breakdown）、额外用量（spend，开了才有）。
func ClaudeCache(path string) (Usage, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Usage{}, false
	}
	type window struct {
		Utilization *float64 `json:"utilization"`
		ResetsAt    string   `json:"resets_at"`
	}
	type money struct {
		AmountMinor float64 `json:"amount_minor"`
		Exponent    int     `json:"exponent"`
	}
	var cfg struct {
		Cache *struct {
			FetchedAtMs int64 `json:"fetchedAtMs"`
			Utilization struct {
				FiveHour *window `json:"five_hour"`
				SevenDay *window `json:"seven_day"`
				Opus     *window `json:"seven_day_opus"`
				Sonnet   *window `json:"seven_day_sonnet"`
				Limits   []struct {
					Kind     string   `json:"kind"`
					Percent  *float64 `json:"percent"`
					ResetsAt string   `json:"resets_at"`
					Scope    *struct {
						Model *struct {
							DisplayName string `json:"display_name"`
						} `json:"model"`
					} `json:"scope"`
				} `json:"limits"`
				Breakdown *struct {
					Rows []struct {
						Name    string  `json:"display_name"`
						Percent float64 `json:"percent"`
					} `json:"rows"`
				} `json:"seven_day_breakdown"`
				Spend *struct {
					Enabled bool   `json:"enabled"`
					Used    *money `json:"used"`
					Limit   *money `json:"limit"`
				} `json:"spend"`
			} `json:"utilization"`
		} `json:"cachedUsageUtilization"`
		Account *struct {
			OrgType  string  `json:"organizationType"`
			OrgTier  string  `json:"organizationRateLimitTier"`
			UserTier *string `json:"userRateLimitTier"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(b, &cfg) != nil || cfg.Cache == nil {
		return Usage{}, false
	}
	c := cfg.Cache.Utilization
	u := Usage{Agent: "claude", UpdatedAt: cfg.Cache.FetchedAtMs}
	// 缓存是 Claude 整份存下的接口回复。哪一类的键在、就说明这一类是完整的（键在而里面没有就是确实没有）；
	// 键不在分不清是没有还是这个版本的 Claude 不写，不算完整。从来不带重置卡
	if c.Limits != nil {
		u.Covers |= CoverPools
	}
	if c.Breakdown != nil {
		u.Covers |= CoverShares
	}
	if c.Spend != nil {
		u.Covers |= CoverOnDemand
	}
	if a := cfg.Account; a != nil {
		tier := a.OrgTier
		if a.UserTier != nil && *a.UserTier != "" {
			tier = *a.UserTier
		}
		u.Plan = claudePlan(a.OrgType, tier)
	}
	add := func(w *window, minutes int, name string) {
		if w == nil || w.Utilization == nil {
			return
		}
		u.Windows = append(u.Windows, Window{Percent: clampPercent(*w.Utilization), Minutes: minutes, ResetsAt: isoMs(w.ResetsAt), Name: name})
	}
	const week = 7 * 24 * 60
	add(c.FiveHour, 300, "")
	add(c.SevenDay, week, "")
	for _, l := range c.Limits {
		if l.Kind != "weekly_scoped" || l.Percent == nil || l.Scope == nil || l.Scope.Model == nil || l.Scope.Model.DisplayName == "" {
			continue
		}
		u.Windows = append(u.Windows, Window{Percent: clampPercent(*l.Percent), Minutes: week, ResetsAt: isoMs(l.ResetsAt), Name: l.Scope.Model.DisplayName})
	}
	add(c.Opus, week, "Opus")
	add(c.Sonnet, week, "Sonnet")
	if c.Breakdown != nil {
		var items []Share
		for _, r := range c.Breakdown.Rows {
			if r.Percent > 0 {
				items = append(items, Share{Name: r.Name, Percent: r.Percent})
			}
		}
		if len(items) > 0 {
			u.Shares = &Shares{Items: items}
		}
	}
	if sp := c.Spend; sp != nil && sp.Enabled && sp.Used != nil {
		cents := func(m *money) float64 {
			if m == nil {
				return 0
			}
			v := m.AmountMinor
			for e := m.Exponent; e > 2; e-- {
				v /= 10
			}
			for e := m.Exponent; e < 2; e++ {
				v *= 10
			}
			return v
		}
		u.OnDemand = &Spend{Used: cents(sp.Used), Limit: cents(sp.Limit)}
	}
	return u, len(u.Windows) > 0
}

var claudeMultiplier = regexp.MustCompile(`_(\d+x)$`)

// claudePlan 照 Claude 网页上的叫法写套餐名：organizationType 的 claude_max 写 Max，限额档位
// 带倍数（default_claude_max_5x）时跟在后面，写成 "Max 5x"。认不出的类型去掉 claude_ 前缀、首字母大写。
func claudePlan(orgType, tier string) string {
	name := strings.TrimPrefix(orgType, "claude_")
	if name == "" {
		return ""
	}
	name = titleWord(name)
	if m := claudeMultiplier.FindStringSubmatch(tier); m != nil {
		name += " " + m[1]
	}
	return name
}
