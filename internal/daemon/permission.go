package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"time"
)

// PermRequest 是一条挂起等待用户决策的工具权限请求（兼容 Antigravity PreToolUse 与 Claude PermissionRequest）。
type PermRequest struct {
	ID         string          `json:"id"`
	TerminalID string          `json:"terminalId"`
	Agent      string          `json:"agent"`
	Tool       string          `json:"tool"`
	Summary    string          `json:"summary"`
	Payload    json.RawMessage `json:"-"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// PermWatcher 由飞书网关等外部审批端实现，监听权限挂起与解除。
type PermWatcher interface {
	WatchPermission(req PermRequest) bool
	PermissionResolved(reqID, behavior, source string)
}

type permWaiter struct {
	req PermRequest
	ch  chan permOutcome
}

type permOutcome struct {
	behavior string
	source   string
}

// permissionHub 在常驻进程内统一协调手机与飞书的权限审批：任意一端先决定即放行，并通知其余端刷新卡片。
type permissionHub struct {
	mu      sync.Mutex
	waits   map[string]*permWaiter
	watcher PermWatcher
	timeout time.Duration // 飞书审批挂起时长，默认 3 分钟（测试可覆写）
}

func (h *permissionHub) SetWatcher(w PermWatcher) {
	h.mu.Lock()
	h.watcher = w
	h.mu.Unlock()
}

func (h *permissionHub) larkTimeout() time.Duration {
	h.mu.Lock()
	t := h.timeout
	h.mu.Unlock()
	if t > 0 {
		return t
	}
	return 3 * time.Minute
}

// Decide 由飞书卡片回调或测试提交审批决策（allow | deny）。
func (h *permissionHub) Decide(reqID, behavior, source string) bool {
	if behavior != "allow" && behavior != "deny" {
		return false
	}
	h.mu.Lock()
	w := h.waits[reqID]
	h.mu.Unlock()
	if w == nil {
		return false
	}
	select {
	case w.ch <- permOutcome{behavior: behavior, source: source}:
		return true
	default:
		return false
	}
}

// Hold 同时协调飞书审批与手机审批。
// - 当飞书网关认领该终端时，挂起最多 3 分钟，期间手机、飞书卡片按钮或桌面键入任一触发即结束；
// - 飞书未认领时，Claude 回退到原有手机 30 秒挂起逻辑；Antigravity 不挂起直接返回空串。
func (h *permissionHub) Hold(
	ctx context.Context,
	terminalID, agentName string,
	payload json.RawMessage,
	mobileHold func(context.Context) string,
	lastInput func() time.Time,
) string {
	tool, summary := parsePermPayload(agentName, payload)
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	req := PermRequest{
		ID:         "perm_" + hex.EncodeToString(buf[:]),
		TerminalID: terminalID,
		Agent:      agentName,
		Tool:       tool,
		Summary:    summary,
		Payload:    payload,
		CreatedAt:  time.Now(),
	}

	h.mu.Lock()
	watcher := h.watcher
	h.mu.Unlock()

	watchedByLark := watcher != nil && watcher.WatchPermission(req)
	if !watchedByLark {
		if mobileHold != nil {
			return mobileHold(ctx)
		}
		return ""
	}

	base := time.Time{}
	if lastInput != nil {
		base = lastInput()
	}

	ch := make(chan permOutcome, 1)
	h.mu.Lock()
	if h.waits == nil {
		h.waits = map[string]*permWaiter{}
	}
	h.waits[req.ID] = &permWaiter{req: req, ch: ch}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.waits, req.ID)
		h.mu.Unlock()
	}()

	holdCtx, cancel := context.WithTimeout(ctx, h.larkTimeout())
	defer cancel()

	// 若手机也连着同一终端，并行竞态等待，任意一端先批即生效
	if mobileHold != nil {
		go func() {
			if b := mobileHold(holdCtx); b == "allow" || b == "deny" {
				h.Decide(req.ID, b, "mobile")
			}
		}()
	}

	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	out := permOutcome{source: "timeout"}
	for {
		select {
		case res := <-ch:
			out = res
			goto done
		case <-holdCtx.Done():
			if ctx.Err() != nil {
				out.source = "cancelled"
			}
			goto done
		case <-tick.C:
			if lastInput != nil && !lastInput().Equal(base) {
				out.source = "desktop"
				goto done
			}
		}
	}
done:
	if watcher != nil {
		watcher.PermissionResolved(req.ID, out.behavior, out.source)
	}
	return out.behavior
}

// parsePermPayload 从 Claude PermissionRequest 或 Antigravity PreToolUse 负载中提取工具名与一行摘要。
func parsePermPayload(agentName string, payload json.RawMessage) (tool, summary string) {
	if len(payload) == 0 {
		return "", ""
	}
	if agentName == "antigravity" {
		var p struct {
			ToolCall struct {
				Name string         `json:"name"`
				Args map[string]any `json:"args"`
			} `json:"toolCall"`
		}
		if json.Unmarshal(payload, &p) == nil && p.ToolCall.Name != "" {
			return p.ToolCall.Name, permArgsSummary(p.ToolCall.Args)
		}
	}
	var p struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if json.Unmarshal(payload, &p) == nil && p.ToolName != "" {
		return p.ToolName, permArgsSummary(p.ToolInput)
	}
	return "", ""
}

func permArgsSummary(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"CommandLine", "command", "cmd", "Path", "TargetFile", "file_path", "path", "ToolName", "ServerName", "url", "query"} {
		if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
			s := strings.Join(strings.Fields(strings.TrimSpace(v)), " ")
			if key == "ToolName" {
				if srv, ok := args["ServerName"].(string); ok && srv != "" {
					s = srv + " / " + s
				}
			}
			r := []rune(s)
			if len(r) > 200 {
				return string(r[:199]) + "…"
			}
			return s
		}
	}
	b, _ := json.Marshal(args)
	s := strings.TrimSpace(string(b))
	r := []rune(s)
	if len(r) > 200 {
		return string(r[:199]) + "…"
	}
	return s
}
