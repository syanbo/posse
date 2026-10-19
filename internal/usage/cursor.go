package usage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Cursor 本地不留用量，只能向它查：和 cursor-agent 自己的 /usage 一样调 DashboardService 的
// GetCurrentPeriodUsage（本期用了百分之几、花了多少、计费周期）和 GetPlanInfo（套餐名）。
// token 是 cursor-agent 登录时存进钥匙串的，只读不续：过期了就跳过，等 Cursor 自己刷新。

const cursorAPI = "https://api2.cursor.sh/aiserver.v1.DashboardService/"

// ErrCursorDenied 是用户在钥匙串弹窗里拒绝了，或者没有登录 cursor-agent。
var ErrCursorDenied = errors.New("cursor token unavailable")

// CursorToken 从钥匙串读 cursor-agent 的 access token。
func CursorToken() (string, error) {
	return keychainPassword("cursor-access-token", "cursor-user")
}

// tokenExpired 看 JWT 的 exp；解不出来时当没过期，交给接口去判断。
func tokenExpired(token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(b, &claims) != nil || claims.Exp == 0 {
		return false
	}
	return now.Unix() >= claims.Exp-60
}

// FetchCursor 查一次 Cursor 的用量。
func FetchCursor(ctx context.Context, client *http.Client, token string, now time.Time) (Usage, error) {
	if tokenExpired(token, now) {
		return Usage{}, errors.New("cursor token expired")
	}
	raw, err := cursorCall(ctx, client, token, "GetCurrentPeriodUsage")
	if err != nil {
		return Usage{}, err
	}
	u, ok := cursorUsage(raw, now)
	if !ok {
		return Usage{}, errors.New("cursor: no plan usage in response")
	}
	// 套餐名拿不到不影响用量
	if raw, err := cursorCall(ctx, client, token, "GetPlanInfo"); err == nil {
		var p struct {
			PlanInfo struct {
				PlanName string `json:"planName"`
			} `json:"planInfo"`
		}
		if json.Unmarshal(raw, &p) == nil {
			u.Plan = strings.TrimSpace(p.PlanInfo.PlanName)
		}
	}
	return u, nil
}

func cursorCall(ctx context.Context, client *http.Client, token, method string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cursorAPI+method, bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cursor %s: HTTP %d", method, resp.StatusCode)
	}
	return b, nil
}

// cursorUsage 解析 GetCurrentPeriodUsage：planUsage 的金额单位是美分，计费周期是毫秒时间戳。
func cursorUsage(raw []byte, now time.Time) (Usage, bool) {
	var r struct {
		Enabled   *bool `json:"enabled"`
		PlanUsage *struct {
			TotalSpend   num `json:"totalSpend"`
			Limit        num `json:"limit"`
			Remaining    num `json:"remaining"`
			TotalPercent num `json:"totalPercentUsed"`
			AutoPercent  num `json:"autoPercentUsed"`
			APIPercent   num `json:"apiPercentUsed"`
		} `json:"planUsage"`
		SpendLimit *struct {
			IndividualUsed  num `json:"individualUsed"`
			IndividualLimit num `json:"individualLimit"`
			PooledUsed      num `json:"pooledUsed"`
			PooledLimit     num `json:"pooledLimit"`
		} `json:"spendLimitUsage"`
		CycleStart num `json:"billingCycleStart"`
		CycleEnd   num `json:"billingCycleEnd"`
	}
	if json.Unmarshal(raw, &r) != nil || r.PlanUsage == nil || (r.Enabled != nil && !*r.Enabled) {
		return Usage{}, false
	}
	p := r.PlanUsage
	spent := p.TotalSpend.v
	if !p.TotalSpend.ok && p.Limit.ok && p.Remaining.ok {
		spent = p.Limit.v - p.Remaining.v
	}
	var percent float64
	switch {
	case p.TotalPercent.ok:
		percent = p.TotalPercent.v
	case p.Limit.ok && p.Limit.v > 0:
		percent = spent / p.Limit.v * 100
	default:
		return Usage{}, false
	}
	w := Window{Percent: clampPercent(percent), ResetsAt: epochMs(r.CycleEnd.v)}
	if start := epochMs(r.CycleStart.v); start > 0 && w.ResetsAt > start {
		w.Minutes = int((w.ResetsAt - start) / 60000)
	}
	u := Usage{Agent: "cursor", Windows: []Window{w}, UpdatedAt: now.UnixMilli()}
	// 套餐里 Auto 和第三方模型（API）各是一池，分别有百分比
	for _, pool := range []struct {
		name string
		v    num
	}{{"Auto", p.AutoPercent}, {"API", p.APIPercent}} {
		if pool.v.ok {
			u.Windows = append(u.Windows, Window{Percent: clampPercent(pool.v.v), Minutes: w.Minutes, ResetsAt: w.ResetsAt, Name: pool.name})
		}
	}
	if p.Limit.ok && p.Limit.v > 0 {
		u.Spend = &Spend{Used: spent, Limit: p.Limit.v}
	}
	if sl := r.SpendLimit; sl != nil {
		used, limit := sl.IndividualUsed.v, sl.IndividualLimit.v
		if limit == 0 && sl.PooledLimit.v > 0 {
			used, limit = sl.PooledUsed.v, sl.PooledLimit.v
		}
		if used > 0 || limit > 0 {
			u.OnDemand = &Spend{Used: used, Limit: limit}
		}
	}
	return u, true
}
