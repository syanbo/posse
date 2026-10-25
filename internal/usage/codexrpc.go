package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"time"
)

// 重置卡只有 Codex 自己知道：拉起一次 codex app-server，调 account/rateLimits/read。
// 请求由 Codex 用它自己的登录发出，posse 不碰 auth.json；回应里顺带有最新的 5 小时和每周额度。

// CodexRemote 问 Codex 一次限额和重置卡。
func CodexRemote(ctx context.Context, codex string, now time.Time) (Usage, error) {
	cmd := exec.CommandContext(ctx, codex, "app-server")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Usage{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Usage{}, err
	}
	if err := cmd.Start(); err != nil {
		return Usage{}, err
	}
	defer func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	}()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 64<<10), 4<<20)
	// recv 读到 id 对上的回应；中间的通知跳过
	recv := func(id int) (json.RawMessage, error) {
		for lines.Scan() {
			var m struct {
				ID     *int            `json:"id"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal(lines.Bytes(), &m) != nil || m.ID == nil || *m.ID != id {
				continue
			}
			if m.Error != nil {
				return nil, errors.New("codex: " + m.Error.Message)
			}
			return m.Result, nil
		}
		if err := lines.Err(); err != nil {
			return nil, err
		}
		return nil, io.ErrUnexpectedEOF
	}
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{"clientInfo": map[string]string{"name": "posse", "version": "1"}}}); err != nil {
		return Usage{}, err
	}
	if _, err := recv(1); err != nil {
		return Usage{}, err
	}
	send(map[string]any{"jsonrpc": "2.0", "method": "initialized"})
	if err := send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "account/rateLimits/read", "params": map[string]any{}}); err != nil {
		return Usage{}, err
	}
	raw, err := recv(2)
	if err != nil {
		return Usage{}, err
	}
	u, ok := codexRateLimits(raw, now)
	if !ok {
		return Usage{}, errors.New("codex: no rate limits in response")
	}
	return u, nil
}

func codexRateLimits(raw []byte, now time.Time) (Usage, bool) {
	type window struct {
		Used     *float64 `json:"usedPercent"`
		Minutes  int      `json:"windowDurationMins"`
		ResetsAt num      `json:"resetsAt"`
	}
	var r struct {
		RateLimits *struct {
			Primary   *window `json:"primary"`
			Secondary *window `json:"secondary"`
			PlanType  string  `json:"planType"`
		} `json:"rateLimits"`
		Resets *struct {
			Available int `json:"availableCount"`
			Credits   []struct {
				Status    string `json:"status"`
				ExpiresAt num    `json:"expiresAt"`
			} `json:"credits"`
		} `json:"rateLimitResetCredits"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return Usage{}, false
	}
	u := Usage{Agent: "codex", UpdatedAt: now.UnixMilli()}
	if rl := r.RateLimits; rl != nil {
		u.Plan = titleWord(rl.PlanType)
		for _, w := range []*window{rl.Primary, rl.Secondary} {
			if w != nil && w.Used != nil {
				u.Windows = append(u.Windows, Window{Percent: clampPercent(*w.Used), Minutes: w.Minutes, ResetsAt: epochMs(w.ResetsAt.v)})
			}
		}
	}
	if rs := r.Resets; rs != nil {
		res := &Resets{Count: rs.Available}
		for _, c := range rs.Credits {
			if at := epochMs(c.ExpiresAt.v); c.Status == "available" && at > 0 && (res.ExpiresAt == 0 || at < res.ExpiresAt) {
				res.ExpiresAt = at
			}
		}
		u.Resets = res
	}
	return u, len(u.Windows) > 0 || u.Resets != nil
}
