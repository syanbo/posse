package agent

import (
	"path/filepath"
	"testing"
)

func TestNormalizeShellLine(t *testing.T) {
	if got := NormalizeShellLine("echo a \\\n  b"); got != "echo a    b" {
		t.Fatalf("LF continuation = %q", got)
	}
	if got := NormalizeShellLine("echo a \\\r\n  b"); got != "echo a    b" {
		t.Fatalf("CRLF continuation = %q", got)
	}
	// 转义反斜杠后的换行不是续行，必须保留换行符以便后续拒绝
	if got := NormalizeShellLine("echo a \\\\\nrm -rf /"); got != "echo a \\\\\nrm -rf /" {
		t.Fatalf("escaped backslash newline = %q", got)
	}
	// 单引号内的反斜杠+换行不是续行
	if got := NormalizeShellLine("echo 'a\\\nb'"); got != "echo 'a\\\nb'" {
		t.Fatalf("single-quoted newline = %q", got)
	}
}

func TestIsSafeReadOnlyCommandLine(t *testing.T) {
	notesDir := t.TempDir()
	t.Setenv("POSSE_NOTES", notesDir)
	discFile := filepath.Join(notesDir, "discussions", "mb_1", "out.md")

	allowed := []string{
		// 四条真实报障排查命令
		`mkdir -p /tmp/posse-triage && cd /tmp/posse-triage && lark-cli base --help && echo "---" && lark-cli base +record-list --base-token EHz --table-id tbl --page-size 10 --as user`,
		`cd /tmp/posse-triage && lark-cli base +url-resolve --url 'https://x.feishu.cn/record/abc' --as user; echo "---"; ls -la ~/.agents/skills/ ~/.gemini/config/skills/ 2>/dev/null`,
		`mkdir -p /tmp/posse-triage/20261125-2360 && cd /tmp/posse-triage/20261125-2360 && lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id rec1 --output-dir /tmp/posse-triage/20261125-2360 --as user || lark-cli base +record-download-attachment --help`,
		`posse history show 3188e0dc-983d-43e9-840d-9668ceeddacd 2>&1 | head -n 80; echo "=== LOG ==="; posse history show f524c240-9840-49b9-a588-7a8937287bd0 2>&1 | head -n 80`,

		// lark-cli 规则
		`lark-cli --help`,
		`lark-cli -h`,
		`lark-cli schema base`,
		`lark-cli base +record-update --base-token EHz --table-id tbl --record-id r1 --dry-run --as user`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 -o /tmp/posse-triage/att --as user`,
		`lark-cli drive +download --file-token f1 --output ` + discFile,
		`lark-cli drive +download --file-token f1 --output-dir $POSSE_NOTES/discussions/mb_1`,
		`lark-cli im +chat-info --chat-id oc_1 --as bot`,
		`lark-cli calendar +event-query --start 1 --end 2`,

		// posse 只读子命令与帮助
		`posse version`,
		`posse --help`,
		`posse task close --help`,
		`posse --json history list --limit 10`,
		`posse history search "timeout" --here`,
		`posse history brief`,
		`posse discussion list`,
		`posse discussion show d_1`,
		`posse task list`,
		`posse task show k_1`,
		`posse task inbox`,
		`posse task guide`,
		`posse terminal list`,
		`posse workspace list`,
		`posse project list`,
		`posse lark list`,
		`posse lark profiles`,
		`posse lark status`,

		// 重定向与 tee
		`echo hi > /tmp/out.txt`,
		`echo hi >> /tmp/out.txt`,
		`echo hi 1>/tmp/out.txt 2>/dev/null`,
		`echo hi &>/tmp/out.log`,
		`echo hi &>>/tmp/out.log`,
		`echo hi 2> /tmp/err.log`,
		`echo hi >/dev/null`,
		`echo hi > ` + discFile,
		`echo hi >> "$POSSE_NOTES/discussions/mb_1/log.md"`,
		`cat < /tmp/out.txt`,
		`wc -l 0< /tmp/out.txt`,
		`echo hi | tee /tmp/out.txt`,
		`echo hi | tee -a /tmp/out.txt ` + discFile,

		// 续行
		"cd /tmp/posse-triage && \\\nlark-cli base +record-list \\\n  --base-token EHz --table-id tbl --as user",
		"echo hello \\\r\n  world",

		// 新增只读命令
		`du -sh /tmp/posse-triage`,
		`df -h`,
		`tree -L 2 /tmp/posse-triage`,
		`xxd -l 64 /tmp/a.bin`,
		`od -tx1 /tmp/a.bin`,
		`strings /tmp/a.bin | head -n 20`,
		`shasum -a 256 /tmp/a.bin`,
		`md5 /tmp/a.bin`,
		`plutil -p /tmp/Info.plist`,
		`plutil -lint -s /tmp/Info.plist`,
		`unzip -l /tmp/a.zip`,
		`tar -tf /tmp/a.tar`,
		`tar -tvzf /tmp/a.tar.gz`,
		`tar -tjf /tmp/a.tar.bz2`,
		`sqlite3 -readonly /tmp/a.db "SELECT count(*) FROM items;"`,
		`sqlite3 -readonly -header -column /tmp/a.db "PRAGMA table_info(items)"`,
		`sqlite3 -readonly /tmp/a.db ".schema"`,
		`sqlite3 -readonly /tmp/a.db ".tables"`,
		`cat /tmp/a.tsv | column -t`,
	}
	for _, cmd := range allowed {
		if !IsSafeReadOnlyCommandLine(cmd) {
			t.Errorf("expected SAFE for: %s", cmd)
		}
	}

	denied := []string{
		// lark-cli 写操作、未知操作、不安全或缺失下载路径
		`lark-cli base +record-update --base-token EHz --table-id tbl --record-id r1 --as user`,
		`lark-cli im +messages-send --chat-id oc_1 --text hi --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 --text --help --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 --text --dry-run --as bot`,
		`lark-cli base +record-batch-update --base-token EHz --table-id tbl`,
		`lark-cli base +record-get-and-delete --base-token EHz`,
		`lark-cli base +mystery-op --base-token EHz`,
		`lark-cli base +record-download-attachment --output-dir /Users/x --base-token EHz --table-id tbl --record-id r1`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1`,
		`lark-cli base +record-download-attachment --output-dir /tmp/../etc/a --base-token EHz`,
		`lark-cli drive +download --file-token f1`,
		`lark-cli drive +download --file-token f1 --output /Users/x/a.bin`,
		`lark-cli base +record-list --output /Users/x/a.json`,
		`lark-cli base +record-list --output`,
		`lark-cli base +record-download-attachment --output-dir '/tmp/$EVIL'`,

		// posse 写操作
		`posse task close k_1`,
		`posse terminal send t_1 x`,
		`posse terminal create ws -- rm -rf / --help`,
		`posse history resume abc`,
		`posse workspace rm ws1`,
		`posse lark add --profile p1`,

		// 重定向与 tee 越界
		`echo hi > /Users/x/a`,
		`echo hi >> /Users/x/a`,
		`echo hi > /tmp/../etc/a`,
		`echo hi > /tmp`,
		`echo hi > relative.txt`,
		`echo hi > /tmp/$EVIL`,
		`echo hi > '$POSSE_NOTES/discussions/mb_1/a.md'`,
		`echo hi 2> /Users/x/err.log`,
		`echo hi &> /Users/x/all.log`,
		`echo hi >&/tmp/a`,
		`cat <> /tmp/a`,
		`cat <<EOF`,
		`diff <(ls /tmp) <(ls /var)`,
		`tee /etc/hosts`,
		`tee -a /tmp/../etc/hosts`,
		`tee /tmp`,
		`tee`,

		// 续行藏危险命令与裸换行
		"echo hi && \\\nrm -rf /tmp/x",
		"echo hi \\\\\nrm -rf /tmp/x",
		"echo hi\nls",

		// 命令安全边界
		`sqlite3 /tmp/a.db "select 1"`,
		`sqlite3 -readonly /tmp/a.db ".shell ls"`,
		`sqlite3 -readonly /tmp/a.db ".output /tmp/x"`,
		`sqlite3 -readonly /tmp/a.db ".once /tmp/x"`,
		`sqlite3 -readonly /tmp/a.db ".system id"`,
		`sqlite3 -readonly /tmp/a.db "DELETE FROM items"`,
		`sqlite3 -readonly /tmp/a.db "SELECT 1; DELETE FROM items"`,
		`sqlite3 -readonly -init /tmp/evil.sql /tmp/a.db "SELECT 1"`,
		`sqlite3 -readonly /tmp/a.db`,
		`tar -xf a.tar`,
		`tar -cf /tmp/a.tar /tmp/dir`,
		`tar -tf a.tar --use-compress-program=sh`,
		`unzip a.zip`,
		`unzip -d /tmp a.zip`,
		`plutil -convert xml1 /tmp/a.plist`,
		`plutil -replace k -string v /tmp/a.plist`,
		`xxd /tmp/in.bin /tmp/out.txt`,
		`xxd -r /tmp/in.hex`,
		`tree -o /tmp/out.txt`,
		`awk '{print $1}' /tmp/a.txt`,
		`sleep 1 &`,
	}
	for _, cmd := range denied {
		if IsSafeReadOnlyCommandLine(cmd) {
			t.Errorf("expected UNSAFE for: %s", cmd)
		}
	}
}
