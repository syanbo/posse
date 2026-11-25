package cli

import (
	"os"
	"path/filepath"
	"testing"
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
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"GITLAB_HOST=gitlab.corp.laoyuegou.com glab api projects/1/issues"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"aliyun sls GetLogs --project=p1 --logstore=s1"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"tccli cls SearchLog --TopicId t1"}}}`,
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
	t.Setenv("POSSE_LARK_READONLY_HOSTS", "sentry.corp.laoyuegou.com")
	t.Setenv("POSSE_LARK_TRUSTED_SCRIPTS", scriptPath)
	envAllowed := []string{
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"curl -sS -H \"Authorization: Bearer $SENTRY_AUTH_TOKEN\" \"https://sentry.corp.laoyuegou.com/api/0/issues/\""}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"bash ` + scriptPath + ` --uid 123"}}}`,
		`{"toolCall":{"name":"run_command","args":{"CommandLine":"` + scriptPath + ` --uid 123"}}}`,
	}
	for _, tc := range envAllowed {
		if !isSafeLarkToolCall([]byte(tc)) {
			t.Fatalf("expected env-configured auto-allow for %s", tc)
		}
	}
}
