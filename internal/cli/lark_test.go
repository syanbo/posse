package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/lark"
	"posse/internal/rpc"
)

func TestAntigravityPreToolUseHelpers(t *testing.T) {
	if got := antigravityPermissionDecision("allow"); got != `{"decision":"allow"}` {
		t.Fatalf("allow = %q", got)
	}
	if got := antigravityPermissionDecision("deny"); got == "" || got == `{"decision":"allow"}` {
		t.Fatalf("deny = %q", got)
	}
	if got := antigravityPermissionDecision(""); got != `{"decision":"deny","reason":"Permission approval timed out in Posse/Feishu"}` {
		t.Fatalf("timeout fallback = %q", got)
	}

	notesDir := t.TempDir()
	t.Setenv("POSSE_NOTES", notesDir)

	allowed := []string{
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse discussion read --execution x_123"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse task done k_1 --execution x_1 --body-file /tmp/a.md"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"ls -la ~/Desktop/Works | grep -iE 'av4|710|lyg|godfrey' ; echo done"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git -C ~/Desktop/Works/app_av4 diff -- android/README.md"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"cat ~/Desktop/Works/app_av4/ic_launcher.xml 2>/dev/null"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo \"POSSE_NOTES=${POSSE_NOTES}\" ; posse task check k_57f89c0a --execution x_1"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"mkdir -p /tmp/posse-triage && cd /tmp/posse-triage && lark-cli base +record-list --base-token EHz --table-id tbl --page-size 20 --as user | jq ."}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli base +record-search --base-token EHz --table-id tbl --keyword 20261124-001 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli im +messages-mget --message-ids om_1 --as bot"}}}`,
		// 四条真实排查命令（第 3 条按本机 lark-cli 1.0.100 实际支持旗标改为 --output）
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"mkdir -p /tmp/posse-triage && cd /tmp/posse-triage && lark-cli base --help && echo \"---\" && lark-cli base +record-list --base-token EHz --table-id tbl --page-size 10 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"cd /tmp/posse-triage && lark-cli base +url-resolve --url 'https://x.feishu.cn/record/abc' --as user; echo \"---\"; ls -la ~/.agents/skills/ ~/.gemini/config/skills/ 2>/dev/null"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"mkdir -p /tmp/posse-triage/20261125-2360 && cd /tmp/posse-triage/20261125-2360 && lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id rec1 --output /tmp/posse-triage/20261125-2360 --as user || lark-cli base +record-download-attachment --help"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse history show 3188e0dc-983d-43e9-840d-9668ceeddacd 2>&1 | head -n 80; echo \"=== LOG ===\"; posse history show f524c240-9840-49b9-a588-7a8937287bd0 2>&1 | head -n 80"}}}`,
		// 安全临时目录重定向、tee、续行、新增只读命令
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi > /tmp/out.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi | tee -a /tmp/out.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"tar -tvzf /tmp/a.tar.gz"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"cd /tmp/posse-triage && \\\nlark-cli base +record-list --base-token EHz --table-id tbl --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"aliyun sls GetLogs --region cn-hangzhou --project lyg-new-log --logstore prod --body '{\"from\":1700000000,\"to\":1700003600,\"query\":\"86030680\",\"line\":20,\"offset\":0,\"reverse\":true}'"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"tccli cls SearchLog --region ap-nanjing --TopicId 6eea273b-d37a-4cc5-843e-fcb1f34b2553 --From 1700000000000 --To 1700003600000 --QueryString '80356859' --Limit 20"}}}`,
		`{"toolCall":{"name":"write_file","args":{"Path":"` + filepath.Join(notesDir, "discussions", "mb_1", "reply.md") + `"}}}`,
	}
	for _, tc := range allowed {
		if !isSafeLarkToolCall([]byte(tc)) {
			t.Fatalf("expected auto-allow for %s", tc)
		}
	}

	denied := []string{
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse discussion read --execution x_1 && rm -rf /"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse discussion read --execution x_1; whoami"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo $(rm -rf /)"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git commit -m test"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git branch -D old"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git branch new-branch"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"git -c core.pager=sh log"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"find . -delete"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"find . -fprint0 /tmp/a"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sed -i '' 's/a/b/' file.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sed -n '1,20w /tmp/evil' file.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"rg --pre sh foo ."}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"env rm -rf /tmp/x"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sort -o /tmp/out.txt file.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sort --compress-program=sh file.txt"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli base +record-update --base-token EHz --table-id tbl --record-id rec_1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli im +messages-send --chat-id oc_1 --text hi --as bot"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli im +messages-send --chat-id oc_1 --markdown --help --as bot"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli schema --help --help=false"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli schema --help -hh=false"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo $HOME"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"file --vers"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"file --mime-t /dev/null"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"glab api -X POST projects/1/issues"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"glab api graphql"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"glab ci view"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"GITLAB_HOST=gitlab.corp.laoyuegou.com glab api projects/1/issues"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"env"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"printenv"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"curl -sS \"https://sentry.corp.laoyuegou.com/api/0/projects/\""}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"bsk snapshot"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"npx prettier --check ."}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"python3 -c \"print(1)\""}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli drive +resolve-comment --token doc1 --comment-id c1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli drive +version-get --token doc1 --version-id v1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli drive +preview --token doc1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli im +messages-mget --message-ids om_1 --download-resources=true --as bot"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli drive +export --token doc1 --doc-type docx --file-extension pdf"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli base +record-download-attachment --output /Users/x --base-token EHz --table-id tbl --record-id rec1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli base +record-download-attachment --output-dir /tmp/posse-triage/1 --base-token EHz --table-id tbl --record-id rec1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id rec1 --as user"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi > /Users/x/a"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi > /tmp/../etc/a"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"tee /etc/hosts"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sqlite3 /tmp/a.db \"select 1\""}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sqlite3 -readonly /tmp/a.db \"SELECT 1\""}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"sqlite3 -readonly /tmp/a.db \".shell ls\""}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"tar -xf a.tar"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse task close k_1"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"posse terminal send t_1 x"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi && \\\nrm -rf /tmp/x"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi \\\\\nrm -rf /tmp/x"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo a\\\nb"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo a \\\r\nb"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"./ls"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"ls /tmp/*.log"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo {a,b}"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"file -C"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi # comment"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"mkdir -p /Users/dengbo/Desktop/Works/bad"}}}`,
		`{"toolCall":{"name":"write_file","args":{"Path":"/Users/dengbo/Desktop/Works/app_av4/README.md"}}}`,
		`{"toolCall":{"name":"write_file","args":{"Path":"` + filepath.Join(notesDir, "discussions", "..", "escape.md") + `"}}}`,
		`{"toolCall":{"name":"edit_file","args":{"Path":"` + filepath.Join(notesDir, "discussions", "mb_1", "reply.md") + `"}}}`,
		`{"toolCall":{"name":"call_mcp_tool","args":{"ToolName":"click"}}}`,
	}
	for _, tc := range denied {
		if isSafeLarkToolCall([]byte(tc)) {
			t.Fatalf("should require approval for %s", tc)
		}
	}

	// 验证通过环境变量注入 POSSE_LARK_READONLY_HOSTS 与 POSSE_LARK_TRUSTED_SCRIPTS 后放行受信脚本与 Sentry 只读 curl
	scriptDir := t.TempDir()
	scriptPath := filepath.Join(scriptDir, "download-log.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSSE_LARK_READONLY_HOSTS", "sentry.corp.laoyuegou.com,gitlab.corp.laoyuegou.com")
	t.Setenv("POSSE_LARK_TRUSTED_SCRIPTS", scriptPath)
	envAllowed := []string{
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"curl -s -H \"Authorization: Bearer $SENTRY_AUTH_TOKEN\" \"https://sentry.corp.laoyuegou.com/api/0/projects/sentry/duoduotu-app/events/?query=user.id:80356859&per_page=1&full=true\" | jq '.[0].user.data.userId'"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"GITLAB_HOST=gitlab.corp.laoyuegou.com glab api projects/1/issues"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"bash ` + scriptPath + ` --uid 123"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"` + scriptPath + ` --uid 123"}}}`,
	}
	for _, tc := range envAllowed {
		if !isSafeLarkToolCall([]byte(tc)) {
			t.Fatalf("expected env-configured auto-allow for %s", tc)
		}
	}

	// 验证通过 POSSE_LARK_BOT_ID + POSSE_HOME 实时读取配置：双机器人隔离 + 存活 worker 配置清空后即时撤销
	posseHome := t.TempDir()
	scriptA := filepath.Join(t.TempDir(), "a.sh")
	scriptB := filepath.Join(t.TempDir(), "b.sh")
	for _, p := range []string{scriptA, scriptB} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := lark.SaveConfig(posseHome, lark.Config{
		OwnerOpenID: "ou_owner",
		Bots: []lark.BotConfig{
			{ID: "lbot_a", Profile: "p_a", ReadOnlyHosts: []string{"sentry.corp.laoyuegou.com"}, TrustedScripts: []string{scriptA}},
			{ID: "lbot_b", Profile: "p_b", ReadOnlyHosts: []string{"gitlab.corp.laoyuegou.com"}, TrustedScripts: []string{scriptB}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSSE_HOME", posseHome)
	t.Setenv("POSSE_LARK_READONLY_HOSTS", "stale.example.com")
	t.Setenv("POSSE_LARK_TRUSTED_SCRIPTS", scriptB)
	t.Setenv("POSSE_LARK_BOT_ID", "lbot_a")

	sentryCall := []byte(`{"toolCall":{"name":"run_command","args":{"CommandLine":"curl -s -H \"Authorization: Bearer $SENTRY_AUTH_TOKEN\" \"https://sentry.corp.laoyuegou.com/api/0/issues/\""}}}`)
	staleCall := []byte(`{"toolCall":{"name":"run_command","args":{"CommandLine":"curl -s \"https://stale.example.com/api/0/issues/\""}}}`)
	gitlabCall := []byte(`{"toolCall":{"name":"run_command","args":{"CommandLine":"GITLAB_HOST=gitlab.corp.laoyuegou.com glab api projects/1/issues"}}}`)
	scriptACall := []byte(`{"toolCall":{"name":"run_command","args":{"CommandLine":"bash ` + scriptA + `"}}}`)
	scriptBCall := []byte(`{"toolCall":{"name":"run_command","args":{"CommandLine":"bash ` + scriptB + `"}}}`)

	if !isSafeLarkToolCall(sentryCall) || !isSafeLarkToolCall(scriptACall) {
		t.Fatal("bot A should allow its own host and script")
	}
	if isSafeLarkToolCall(staleCall) || isSafeLarkToolCall(gitlabCall) || isSafeLarkToolCall(scriptBCall) {
		t.Fatal("bot A must not inherit stale env or bot B policy")
	}

	t.Setenv("POSSE_LARK_BOT_ID", "lbot_b")
	if !isSafeLarkToolCall(gitlabCall) || !isSafeLarkToolCall(scriptBCall) {
		t.Fatal("bot B should allow its own host and script")
	}
	if isSafeLarkToolCall(sentryCall) || isSafeLarkToolCall(scriptACall) {
		t.Fatal("bot B must not access bot A policy")
	}

	// 清空 lbot_a 策略后，无需重启 worker 即可即时撤销（即使启动环境变量仍保留旧值）
	t.Setenv("POSSE_LARK_BOT_ID", "lbot_a")
	t.Setenv("POSSE_LARK_READONLY_HOSTS", "sentry.corp.laoyuegou.com")
	t.Setenv("POSSE_LARK_TRUSTED_SCRIPTS", scriptA)
	if err := lark.SaveConfig(posseHome, lark.Config{
		OwnerOpenID: "ou_owner",
		Bots: []lark.BotConfig{
			{ID: "lbot_a", Profile: "p_a"},
			{ID: "lbot_b", Profile: "p_b", ReadOnlyHosts: []string{"gitlab.corp.laoyuegou.com"}, TrustedScripts: []string{scriptB}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if isSafeLarkToolCall(sentryCall) || isSafeLarkToolCall(scriptACall) {
		t.Fatal("cleared bot A policy must revoke immediately for running worker")
	}
}

func TestCmdLarkAddAndEditValidationAndPreservation(t *testing.T) {
	home := t.TempDir()
	sockDir, err := os.MkdirTemp("/tmp", "plark-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	sock := filepath.Join(sockDir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	existingScript := filepath.Join(t.TempDir(), "keep.sh")
	storedBot := lark.BotConfig{
		ID:             "lbot_1",
		Name:           "Old Name",
		Profile:        "p_1",
		Enabled:        true,
		AllowedUsers:   []string{"ou_1"},
		AllowedChats:   []string{"oc_1"},
		ReadOnlyHosts:  []string{"sentry.corp.laoyuegou.com"},
		TrustedScripts: []string{existingScript},
	}

	srv := rpc.NewServer()
	srv.Handle("lark.bots.list", func(_ context.Context, _ json.RawMessage) (any, error) {
		return map[string]any{"ownerOpenId": "ou_owner", "bots": []lark.BotConfig{storedBot}}, nil
	})
	srv.Handle("lark.bots.save", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Bot lark.BotConfig `json:"bot"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		if err := lark.ValidateBotPolicy(p.Bot.ReadOnlyHosts, p.Bot.TrustedScripts); err != nil {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "%s", err.Error())
		}
		storedBot = lark.NormalizeBot(p.Bot)
		return storedBot, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	newEnv := func() *env {
		return &env{home: home, socket: sock, out: &bytes.Buffer{}, errOut: &bytes.Buffer{}}
	}

	// lark add 多余位置参数报错
	if err := cmdLarkAdd(newEnv(), []string{"--profile", "p_1", "extra-arg"}); err == nil || !strings.Contains(err.Error(), "unexpected arguments") {
		t.Fatalf("expected unexpected arguments error, got %v", err)
	}
	// lark add 相对路径受信脚本不得静默丢弃，必须报错
	if err := cmdLarkAdd(newEnv(), []string{"--profile", "p_1", "--trusted-script", "relative/bad.sh"}); err == nil || !strings.Contains(err.Error(), "invalid trusted script path") {
		t.Fatalf("expected invalid trusted script error, got %v", err)
	}
	// lark add --id 更新已有机器人时，未显式传入的列表字段必须保留不清空
	if err := cmdLarkAdd(newEnv(), []string{"--id", "lbot_1", "--profile", "p_1", "--name", "Renamed"}); err != nil {
		t.Fatalf("cmdLarkAdd --id update failed: %v", err)
	}
	if storedBot.Name != "Renamed" ||
		len(storedBot.ReadOnlyHosts) != 1 || storedBot.ReadOnlyHosts[0] != "sentry.corp.laoyuegou.com" ||
		len(storedBot.TrustedScripts) != 1 || storedBot.TrustedScripts[0] != existingScript ||
		len(storedBot.AllowedUsers) != 1 || storedBot.AllowedUsers[0] != "ou_1" ||
		len(storedBot.AllowedChats) != 1 || storedBot.AllowedChats[0] != "oc_1" {
		t.Fatalf("cmdLarkAdd --id cleared existing lists: %+v", storedBot)
	}

	// lark edit 超过 1 个位置参数报错
	if err := cmdLarkEdit(newEnv(), []string{"lbot_1", "extra"}); err == nil || !strings.Contains(err.Error(), "too many arguments") {
		t.Fatalf("expected too many arguments error, got %v", err)
	}
	// lark edit 相对路径受信脚本必须报错
	if err := cmdLarkEdit(newEnv(), []string{"lbot_1", "--trusted-script", "./bad.sh"}); err == nil || !strings.Contains(err.Error(), "invalid trusted script path") {
		t.Fatalf("expected invalid trusted script error on edit, got %v", err)
	}
	// lark edit --clear-read-only-hosts 显式清空
	if err := cmdLarkEdit(newEnv(), []string{"lbot_1", "--clear-read-only-hosts"}); err != nil {
		t.Fatalf("cmdLarkEdit clear failed: %v", err)
	}
	if len(storedBot.ReadOnlyHosts) != 0 || len(storedBot.TrustedScripts) != 1 {
		t.Fatalf("unexpected bot after clear-read-only-hosts: %+v", storedBot)
	}
}
