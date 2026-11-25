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

// 验证规则键生成会跳过已被只读白名单放行的命令段，且对重定向、无法解析命令、两级 posse 与越界输出目的地严格收敛。
func TestDeriveCommandRuleKeysSkipsReadOnly(t *testing.T) {
	keys, label := deriveCommandRuleKeys("cd /tmp/x && lark-cli base +foo-update --base-token EHz --as user ; ls")
	if len(keys) != 1 || keys[0] != "cmd:lark-cli base +foo-update" || label != "lark-cli base +foo-update" {
		t.Fatalf("unexpected rule keys=%v label=%q", keys, label)
	}

	// 支持「空白 + \ + LF」续行，且只读前缀/后缀被跳过
	keys2, label2 := deriveCommandRuleKeys("mkdir -p /tmp/x && cd /tmp/x && \\\nlark-cli base +foo-update \\\n  --base-token EHz --as user | jq .")
	if len(keys2) != 1 || keys2[0] != "cmd:lark-cli base +foo-update" || label2 != "lark-cli base +foo-update" {
		t.Fatalf("unexpected multiline rule keys=%v label=%q", keys2, label2)
	}

	// posse 记两级（posse <组> <动作>），不同二级动作不会共享规则键
	posseSendKeys, posseSendLabel := deriveCommandRuleKeys("posse task send k_1 hi")
	if len(posseSendKeys) != 1 || posseSendKeys[0] != "cmd:posse task send" || posseSendLabel != "posse task send" {
		t.Fatalf("unexpected posse send rule keys=%v label=%q", posseSendKeys, posseSendLabel)
	}
	posseCloseKeys, posseCloseLabel := deriveCommandRuleKeys("posse --json task close k_1")
	if len(posseCloseKeys) != 1 || posseCloseKeys[0] != "cmd:posse task close" || posseCloseLabel != "posse task close" {
		t.Fatalf("unexpected posse close rule keys=%v label=%q", posseCloseKeys, posseCloseLabel)
	}

	// 不同路径但同 basename 必须产出不同规则键，避免非可信路径命中裸命令或另一路径的同类放行
	gitLocalKeys, gitLocalLabel := deriveCommandRuleKeys("/tmp/x/git status")
	if len(gitLocalKeys) != 1 || gitLocalKeys[0] != "cmd:/tmp/x/git status" || gitLocalLabel != "/tmp/x/git status" {
		t.Fatalf("unexpected path-qualified git rule keys=%v label=%q", gitLocalKeys, gitLocalLabel)
	}
	gitBareKeys, _ := deriveCommandRuleKeys("git commit -m test")
	gitOtherKeys, _ := deriveCommandRuleKeys("/tmp/x/git commit -m test")
	if len(gitBareKeys) != 1 || len(gitOtherKeys) != 1 || gitBareKeys[0] == gitOtherKeys[0] {
		t.Fatalf("same basename at different paths must have distinct keys: bare=%v path=%v", gitBareKeys, gitOtherKeys)
	}
	relPyKeys, _ := deriveCommandRuleKeys("./python3 /tmp/a.py")
	if len(relPyKeys) != 1 || relPyKeys[0] != "cmd:./python3" {
		t.Fatalf("relative path must stay distinct from bare command, got %v", relPyKeys)
	}

	// 全部段均安全时返回空规则键
	keys3, label3 := deriveCommandRuleKeys("cd /tmp/x && ls -la | head -n 10")
	if len(keys3) != 0 || label3 != "" {
		t.Fatalf("all-safe command should produce no rule keys, got keys=%v label=%q", keys3, label3)
	}

	// 需审批段带任何重定向、无法可靠解析（含 glob/花括号/未允许变量）、前置环境变量或指向非安全/隐式输出目的地时，整条命令不生成同类规则键
	noRuleCases := []string{
		"echo hi > /Users/x/a",
		"python3 /tmp/a.py > /Users/x/out.txt",
		"cd /tmp/x && lark-cli base +foo-update --as user 2>&1",
		"echo hi | tee /etc/hosts",
		"lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output /Users/x/a --as user",
		"lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --as user",
		"lark-cli drive +export --token doc1 --doc-type docx --file-extension pdf",
		"lark-cli im +messages-mget --message-ids om_1 --download-resources=true --as bot",
		"FOO=bar python3 /tmp/a.py",
		"python3 /tmp/a.py # comment",
		"python3 '/tmp/unclosed",
		"python3 /tmp/a.py \\\r\n  --flag",
		"python3 /tmp/*.py",
		"python3 /tmp/{a,b}.py",
		"lark-cli base +foo-update --record-id {1..3} --as user",
		"python3 $SECRET_SCRIPT",
	}
	for _, cmd := range noRuleCases {
		if ks, lbl := deriveCommandRuleKeys(cmd); len(ks) != 0 || lbl != "" {
			t.Errorf("expected no session rule key for %q, got keys=%v label=%q", cmd, ks, lbl)
		}
	}

	// 验证「本会话允许同类」：
	// 1. 前后缀只读段变化时仍命中同类
	// 2. 同程序换不同输出目的地（带重定向）不能复用已授权规则，必须再次审批
	// 3. 未授权写段混入已授权复合命令必须再次审批
	var hub permissionHub
	hub.timeout = 5 * time.Second
	w := &stubPermWatcher{reqCh: make(chan PermRequest, 1), resolvedCh: make(chan string, 1)}
	hub.SetWatcher(w)

	p1 := json.RawMessage(`{"toolCall":{"id":"call_10","name":"run_command","args":{"CommandLine":"cd /tmp/a && lark-cli base +foo-update --record-id r1 --as user"}}}`)
	done := make(chan string, 1)
	go func() {
		done <- hub.Hold(context.Background(), "t_rule", "antigravity", p1, nil, nil)
	}()
	req := <-w.reqCh
	if len(req.RuleKeys) != 1 || req.RuleKeys[0] != "cmd:lark-cli base +foo-update" {
		t.Fatalf("unexpected req rule keys: %+v", req.RuleKeys)
	}
	hub.Decide(req.ID, "allow_session", "feishu")
	if got := <-done; got != "allow" {
		t.Fatalf("expected allow, got %q", got)
	}
	<-w.resolvedCh

	p2 := json.RawMessage(`{"toolCall":{"id":"call_11","name":"run_command","args":{"CommandLine":"mkdir -p /tmp/b && cd /tmp/b && lark-cli base +foo-update --record-id r2 --as user | jq ."}}}`)
	if got := hub.Hold(context.Background(), "t_rule", "antigravity", p2, nil, nil); got != "allow" {
		t.Fatalf("expected session-allowed rule to match regardless of safe prefix/suffix, got %q", got)
	}

	// 不同路径同名程序不能命中裸命令的本会话同类规则
	pDiffPath := json.RawMessage(`{"toolCall":{"id":"call_11b","name":"run_command","args":{"CommandLine":"/tmp/evil/lark-cli base +foo-update --record-id r2 --as user"}}}`)
	doneDiff := make(chan string, 1)
	go func() {
		doneDiff <- hub.Hold(context.Background(), "t_rule", "antigravity", pDiffPath, nil, nil)
	}()
	reqDiff := <-w.reqCh
	if len(reqDiff.RuleKeys) != 1 || reqDiff.RuleKeys[0] != "cmd:/tmp/evil/lark-cli base +foo-update" {
		t.Fatalf("unexpected diff-path rule keys: %+v", reqDiff.RuleKeys)
	}
	hub.Decide(reqDiff.ID, "deny", "feishu")
	if got := <-doneDiff; got != "deny" {
		t.Fatalf("expected deny for different-path same-basename command, got %q", got)
	}
	<-w.resolvedCh

	// 同程序带输出重定向到不同目的地：必须重新审批且无同类规则键
	pRedir := json.RawMessage(`{"toolCall":{"id":"call_12","name":"run_command","args":{"CommandLine":"lark-cli base +foo-update --record-id r2 --as user > /Users/x/leak.txt"}}}`)
	doneRedir := make(chan string, 1)
	go func() {
		doneRedir <- hub.Hold(context.Background(), "t_rule", "antigravity", pRedir, nil, nil)
	}()
	reqRedir := <-w.reqCh
	if len(reqRedir.RuleKeys) != 0 {
		t.Fatalf("redirected write command should not produce rule keys, got %+v", reqRedir.RuleKeys)
	}
	hub.Decide(reqRedir.ID, "deny", "feishu")
	if got := <-doneRedir; got != "deny" {
		t.Fatalf("expected deny for redirected write command, got %q", got)
	}
	<-w.resolvedCh

	// 未授权写段混入已授权复合命令：必须重新审批
	pMixed := json.RawMessage(`{"toolCall":{"id":"call_13","name":"run_command","args":{"CommandLine":"cd /tmp/b && lark-cli base +foo-update --record-id r2 --as user && posse task close k_1"}}}`)
	doneMixed := make(chan string, 1)
	go func() {
		doneMixed <- hub.Hold(context.Background(), "t_rule", "antigravity", pMixed, nil, nil)
	}()
	reqMixed := <-w.reqCh
	if len(reqMixed.RuleKeys) != 2 || reqMixed.RuleKeys[0] != "cmd:lark-cli base +foo-update" || reqMixed.RuleKeys[1] != "cmd:posse task close" {
		t.Fatalf("unexpected mixed rule keys: %+v", reqMixed.RuleKeys)
	}
	hub.Decide(reqMixed.ID, "deny", "feishu")
	if got := <-doneMixed; got != "deny" {
		t.Fatalf("expected deny for mixed unauthorized write command, got %q", got)
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
