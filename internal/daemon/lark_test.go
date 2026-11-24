package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"posse/internal/lark"
)

type stubPermWatcher struct {
	reqCh      chan PermRequest
	resolvedCh chan string
}

func (w *stubPermWatcher) WatchPermission(req PermRequest) bool {
	w.reqCh <- req
	return true
}

func (w *stubPermWatcher) PermissionResolved(reqID, behavior, source string) {
	w.resolvedCh <- reqID + ":" + behavior + ":" + source
}

// 验证 Antigravity PreToolUse 经共享 permissionHub 挂起并由飞书决策放行/拒绝，且解析 toolCall 负载。
func TestPermissionHubAntigravityPreToolUse(t *testing.T) {
	var hub permissionHub
	hub.timeout = 5 * time.Second
	w := &stubPermWatcher{reqCh: make(chan PermRequest, 1), resolvedCh: make(chan string, 1)}
	hub.SetWatcher(w)

	payload := json.RawMessage(`{"toolCall":{"id":"call_1","name":"run_command","args":{"CommandLine":"tail -n 200 /var/log/app.log"}}}`)
	done := make(chan string, 1)
	go func() {
		done <- hub.Hold(context.Background(), "t_agy", "antigravity", payload, nil, nil)
	}()

	req := <-w.reqCh
	if req.Tool != "run_command" || req.Summary != "tail -n 200 /var/log/app.log" || req.Agent != "antigravity" {
		t.Fatalf("unexpected parsed agy perm request: %+v", req)
	}
	if !hub.Decide(req.ID, "allow", "feishu") {
		t.Fatal("Decide returned false")
	}
	if got := <-done; got != "allow" {
		t.Fatalf("expected allow, got %q", got)
	}
	if res := <-w.resolvedCh; res != req.ID+":allow:feishu" {
		t.Fatalf("unexpected resolved event: %q", res)
	}

	// 测试「本会话允许同类」：同类命令记住后第二次不再弹出飞书卡片，但不同命令或拼接危险命令仍会拦截
	p1 := json.RawMessage(`{"toolCall":{"id":"call_2","name":"run_command","args":{"CommandLine":"python3 /tmp/parse_logs.py a.log"}}}`)
	done2 := make(chan string, 1)
	go func() {
		done2 <- hub.Hold(context.Background(), "t_agy", "antigravity", p1, nil, nil)
	}()
	req2 := <-w.reqCh
	if req2.RuleLabel != "python3" {
		t.Fatalf("expected rule label python3, got %q", req2.RuleLabel)
	}
	if !hub.Decide(req2.ID, "allow_session", "feishu") {
		t.Fatal("Decide allow_session returned false")
	}
	if got := <-done2; got != "allow" {
		t.Fatalf("expected allow, got %q", got)
	}
	if res := <-w.resolvedCh; res != req2.ID+":allow:feishu:session" {
		t.Fatalf("unexpected session resolved event: %q", res)
	}

	// 同会话再次调用 python3：直接秒放行，不触发 WatchPermission
	p2 := json.RawMessage(`{"toolCall":{"id":"call_3","name":"run_command","args":{"CommandLine":"python3 /tmp/other.py b.log"}}}`)
	if got := hub.Hold(context.Background(), "t_agy", "antigravity", p2, nil, nil); got != "allow" {
		t.Fatalf("expected session-allowed python3 to bypass prompt, got %q", got)
	}
	select {
	case extra := <-w.reqCh:
		t.Fatalf("session-allowed call should not trigger WatchPermission, got %+v", extra)
	default:
	}

	// 复合命令带未放行的 rm：必须依然弹窗拦截
	pBad := json.RawMessage(`{"toolCall":{"id":"call_4","name":"run_command","args":{"CommandLine":"python3 /tmp/a.py && rm -rf /tmp/x"}}}`)
	doneBad := make(chan string, 1)
	go func() {
		doneBad <- hub.Hold(context.Background(), "t_agy", "antigravity", pBad, nil, nil)
	}()
	reqBad := <-w.reqCh
	hub.Decide(reqBad.ID, "deny", "feishu")
	if got := <-doneBad; got != "deny" {
		t.Fatalf("expected compound unsafe command to require approval and deny, got %q", got)
	}
	<-w.resolvedCh
}

// 验证 lark.bots.save / list / toggle / delete RPC 与持久化配置。
func TestDaemonLarkBotRPC(t *testing.T) {
	d, w, _ := newDiscDaemon(t)
	saved := call(t, d.larkBotSave, map[string]any{
		"ownerOpenId": "ou_test",
		"bot": map[string]any{
			"name":           "Works Triage",
			"profile":        "cli_demo",
			"enabled":        false,
			"workspaceId":    w.ID,
			"agent":          "antigravity",
			"permissionMode": "ask",
			"sessionMode":    "chat",
			"triggerPolicy":  "self",
		},
	}).(lark.BotConfig)
	if saved.ID == "" || saved.Name != "Works Triage" || saved.Agent != "antigravity" {
		t.Fatalf("unexpected saved bot: %+v", saved)
	}
	toggled := call(t, d.larkBotToggle, map[string]any{"id": saved.ID, "enabled": true}).(lark.BotConfig)
	if !toggled.Enabled {
		t.Fatal("expected bot enabled")
	}
	listed := call(t, d.larkBotsList, map[string]any{}).(map[string]any)
	if listed["ownerOpenId"] != "ou_test" {
		t.Fatalf("unexpected ownerOpenId: %+v", listed)
	}
	call(t, d.larkBotDelete, map[string]any{"id": saved.ID})
	cfg, err := lark.LoadConfig(d.cfg.Home)
	if err != nil || len(cfg.Bots) != 0 {
		t.Fatalf("expected empty bots after delete, got %+v err=%v", cfg.Bots, err)
	}
}
