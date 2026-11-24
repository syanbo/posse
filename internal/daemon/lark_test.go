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
