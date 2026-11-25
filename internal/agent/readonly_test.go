package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestNormalizeShellLine(t *testing.T) {
	// 空白 + 反斜杠 + LF 规整为空格（与 shell 语义一致）
	if got := NormalizeShellLine("echo a \\\n  b"); got != "echo a   b" {
		t.Fatalf("whitespace+LF continuation = %q", got)
	}
	// 反斜杠紧贴前一个字符不是允许的续行，必须保留换行以便拒绝
	if got := NormalizeShellLine("echo a\\\nb"); got != "echo a\\\nb" {
		t.Fatalf("tight backslash+LF should not collapse: %q", got)
	}
	// 任何 CRLF 均不规整，保留 \r 以便拒绝
	if got := NormalizeShellLine("echo a \\\r\n  b"); got != "echo a \\\r\n  b" {
		t.Fatalf("CRLF should not collapse: %q", got)
	}
	// 转义反斜杠后的换行不是续行
	if got := NormalizeShellLine("echo a \\\\\nrm -rf /"); got != "echo a \\\\\nrm -rf /" {
		t.Fatalf("escaped backslash newline = %q", got)
	}
	// 单引号内的反斜杠+换行不是续行
	if got := NormalizeShellLine("echo 'a \\\nb'"); got != "echo 'a \\\nb'" {
		t.Fatalf("single-quoted newline = %q", got)
	}
}

func TestIsSafeReadOnlyCommandLine(t *testing.T) {
	notesDir := t.TempDir()
	t.Setenv("POSSE_NOTES", notesDir)
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	posseHome := t.TempDir()
	t.Setenv("POSSE_HOME", posseHome)
	discFile := filepath.Join(notesDir, "discussions", "mb_1", "out.md")

	allowed := []string{
		// 四条真实报障排查命令（第 3 条按本机 lark-cli 1.0.100 实际支持旗标改为 --output）
		`mkdir -p /tmp/posse-triage && cd /tmp/posse-triage && lark-cli base --help && echo "---" && lark-cli base +record-list --base-token EHz --table-id tbl --page-size 10 --as user`,
		`cd /tmp/posse-triage && lark-cli base +url-resolve --url 'https://x.feishu.cn/record/abc' --as user; echo "---"; ls -la ~/.agents/skills/ ~/.gemini/config/skills/ 2>/dev/null`,
		`mkdir -p /tmp/posse-triage/20261125-2360 && cd /tmp/posse-triage/20261125-2360 && lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id rec1 --output /tmp/posse-triage/20261125-2360 --as user || lark-cli base +record-download-attachment --help`,
		`posse history show 3188e0dc-983d-43e9-840d-9668ceeddacd 2>&1 | head -n 80; echo "=== LOG ==="; posse history show f524c240-9840-49b9-a588-7a8937287bd0 2>&1 | head -n 80`,

		// A. 空白 + \ + LF 续行；引号内 # 不算注释
		"cd /tmp/posse-triage && \\\nlark-cli base +record-list \\\n  --base-token EHz --table-id tbl --as user",
		`grep "#define" /tmp/a.h`,
		`echo "a\"b" 'c#d'`,

		// B. 双引号内允许的变量与 ~ 开头
		`echo "POSSE_NOTES=${POSSE_NOTES}"`,
		`echo "$POSSE_NOTES" "$HOME" "${HOME}"`,
		`ls -la ~/Desktop/Works`,
		`file /tmp/a.bin`,
		`file -b /tmp/a.bin`,
		`file -bi /tmp/a.bin`,
		`file -b -i -L -z /tmp/a.bin`,

		// C. 白名单绝对路径程序
		`/bin/ls -la /tmp`,
		`/usr/bin/git status`,
		`/opt/homebrew/bin/rg foo /tmp`,
		filepath.Join(homeDir, ".local", "bin", "rg") + ` foo /tmp`,
		`"$HOME/.local/bin/rg" foo /tmp`,
		filepath.Join(posseHome, "bin", "posse") + ` history list`,

		// E. lark-cli 规则
		`lark-cli --help`,
		`lark-cli -h`,
		`lark-cli schema base`,
		`lark-cli base +record-update --base-token EHz --table-id tbl --record-id r1 --dry-run --as user`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output /tmp/posse-triage/att --as user`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output=` + discFile + ` --as user`,
		`lark-cli drive +download --file-token f1 --output ` + discFile,
		`lark-cli drive +download --file-token f1 --output-dir "$POSSE_NOTES/discussions/mb_1"`,
		`lark-cli drive +export --token doc1 --doc-type docx --file-extension pdf --output-dir /tmp/posse-triage`,
		`lark-cli im +messages-mget --message-ids om_1 --download-resources=false --as bot`,

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

		// F. 收紧后的只读工具正例
		`rg -n "error" /tmp`,
		`rg -e fooz /tmp`,
		`sed -n '1,20p' /tmp/a.txt`,
		`sed -n '1,$p' /tmp/a.txt`,
		`sed -n '10p' /tmp/a.txt`,
		`sed -n '/error/p' /tmp/a.txt`,
		`sed -n -e '1,5p' /tmp/a.txt`,
		`find /tmp -name '*.log'`,
		`sort -u /tmp/a.txt`,
		`git status`,
		`git -C ~/Desktop/Works/app_av4 diff -- android/README.md`,
		`git log -n 5 --oneline`,
		`git show HEAD:README.md`,
		`git branch`,
		`git branch -a`,
		`git branch -r`,
		`git branch -v`,
		`git branch -vv`,
		`git branch --show-current`,
		`git branch --list 'feat/*'`,
		`git branch --merged main`,
		`git branch --no-merged main`,
		`git branch --contains HEAD`,
		`du -sh /tmp/posse-triage`,
		`df -h`,
		`tree -L 2 /tmp/posse-triage`,
		`xxd /tmp/a.bin`,
		`xxd -l 64 -s 16 -c 16 -g 4 -p /tmp/a.bin`,
		`od -tx1 /tmp/a.bin`,
		`strings /tmp/a.bin | head -n 20`,
		`shasum -a 256 /tmp/a.bin`,
		`md5 /tmp/a.bin`,
		`plutil -p /tmp/Info.plist`,
		`plutil -lint /tmp/Info.plist`,
		`unzip -l /tmp/a.zip`,
		`unzip -Z /tmp/a.zip`,
		`unzip -Z -1 /tmp/a.zip`,
		`unzip -Z1 /tmp/a.zip`,
		`tar -t`,
		`tar -tf /tmp/a.tar`,
		`tar -tvf /tmp/a.tar`,
		`tar -tzf /tmp/a.tar.gz`,
		`tar -tvzf /tmp/a.tar.gz`,
		`tar -t -v -z -f /tmp/a.tar.gz`,
		`cat /tmp/a.tsv | column -t`,
	}
	for _, cmd := range allowed {
		if !IsSafeReadOnlyCommandLine(cmd) {
			t.Errorf("expected SAFE for: %s", cmd)
		}
	}

	denied := []string{
		// A. 续行与注释拒绝
		"echo a\\\nb",
		"echo a \\\r\n  b",
		"echo hi && \\\nrm -rf /tmp/x",
		"echo hi \\\\\nrm -rf /tmp/x",
		"echo hi\nls",
		`echo hi # comment`,
		`ls /tmp # ; rm -rf /`,

		// B. 变量展开（引号外一律拒绝）、修饰符、任何 token 内的未引号 glob/花括号、波浪号、裸赋值段、未引号括号
		`echo $HOME`,
		`echo ${HOME}`,
		`echo $POSSE_NOTES`,
		`echo ${POSSE_NOTES}`,
		`$HOME/.local/bin/rg foo /tmp`,
		`xxd $HOME`,
		`lark-cli drive +download --file-token f1 --output-dir $POSSE_NOTES/discussions/mb_1`,
		`echo $PATH`,
		`echo "$USER"`,
		`echo ${HOME:-/etc}`,
		`echo "${POSSE_NOTES/a/b}"`,
		`echo $HOME_EXTRA`,
		`echo $POSSE_NOTES_EXTRA`,
		`echo $'rm'`,
		`ls /tmp/*.log`,
		`ls /tmp/a?`,
		`ls /tmp/[ab]`,
		`echo {a,b}`,
		`echo {1..3}`,
		`xxd /tmp/{a,b}`,
		`echo hi > /tmp/out*.txt`,
		`echo hi > /tmp/out?.txt`,
		`echo hi > /tmp/out[1].txt`,
		`echo hi | tee /tmp/out*.txt`,
		`mkdir -p /tmp/dir*`,
		`lark-cli drive +export --token x --output-dir /tmp/dir*`,
		`ls ~root`,
		`ls ~+`,
		`ls foo~bar`,
		`git diff HEAD~1`,
		`X=y`,
		`PATH=/tmp/evil && ls`,
		`FOO=/bin/ls`,
		`ls /tmp/*(e:'rm -rf /':)`,
		`(ls /tmp)`,

		// C. 非白名单程序路径与相对路径
		`./ls`,
		`../bin/ls`,
		`/tmp/ls`,
		`/bin/../tmp/ls`,
		`/bin/sub/ls`,

		// E. lark-cli 写操作、取消动词推断后的未列名快捷命令、help/dry-run 重复与赋值、等号布尔、隐式写盘、错误旗标
		`lark-cli base +record-update --base-token EHz --table-id tbl --record-id r1 --as user`,
		`lark-cli im +messages-send --chat-id oc_1 --text hi --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 --text --help --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 --markdown --help --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 -q --help --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 --text --dry-run --as bot`,
		`lark-cli im +messages-send --chat-id oc_1 --markdown --dry-run --as bot`,
		`lark-cli schema --help --help=false`,
		`lark-cli schema --help=false --help`,
		`lark-cli schema --help -hh=false`,
		`lark-cli base +record-update --help -hv`,
		`lark-cli base +record-update --help -hh`,
		`lark-cli base +record-update --dry-run -x`,
		`lark-cli base +record-update --help=true`,
		`lark-cli base +record-update --help --help`,
		`lark-cli base +record-update -h --help`,
		`lark-cli base +record-update --dry-run --dry-run=false`,
		`lark-cli base +record-update --dry-run=false --dry-run`,
		`lark-cli base +record-update --dry-run=true`,
		`lark-cli base +record-update --dry-run --dry-run`,
		`lark-cli drive +resolve-comment --token doc1 --comment-id c1 --as user`,
		`lark-cli drive +version-get --token doc1 --version-id v1 --as user`,
		`lark-cli drive +preview --token doc1 --as user`,
		`lark-cli im +chat-info --chat-id oc_1 --as bot`,
		`lark-cli calendar +event-query --start 1 --end 2`,
		`lark-cli base +record-batch-update --base-token EHz --table-id tbl`,
		`lark-cli base +record-get-and-delete --base-token EHz`,
		`lark-cli base +mystery-op --base-token EHz`,
		`lark-cli base +record-download-attachment --output /Users/x --base-token EHz --table-id tbl --record-id r1`,
		`lark-cli base +record-download-attachment --output-dir /tmp/posse-triage/1 --base-token EHz --table-id tbl --record-id r1`,
		`lark-cli base +record-download-attachment -o /tmp/posse-triage/1 --base-token EHz --table-id tbl --record-id r1`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1`,
		`lark-cli base +record-download-attachment --output /tmp/../etc/a --base-token EHz`,
		`lark-cli drive +export --token doc1 --doc-type docx --file-extension pdf`,
		`lark-cli drive +export --token doc1 --output /tmp/posse-triage`,
		`lark-cli drive +export --token doc1 --output-dir /Users/x`,
		`lark-cli im +messages-mget --message-ids om_1 --download-resources --as bot`,
		`lark-cli im +messages-mget --message-ids om_1 --download-resources=true --as bot`,
		`lark-cli im +messages-mget --message-ids om_1 --download-resources=1 --as bot`,
		`lark-cli im +messages-mget --message-ids om_1 --download-resources=invalid --as bot`,
		`lark-cli im +chat-messages-list --chat-id oc_1 --download-resources=true --as bot`,
		`lark-cli im +threads-messages-list --thread-id th_1 --download-resources --as bot`,
		`lark-cli drive +download --file-token f1`,
		`lark-cli drive +download --file-token f1 --output /Users/x/a.bin`,
		`lark-cli base +record-list --output /Users/x/a.json`,
		`lark-cli base +record-list --output`,

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
		`echo hi > '/tmp/trailing '`,
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

		// F. 收紧后的工具负例
		`file -C`,
		`file --compile`,
		`file --compile=/tmp/magic`,
		`file -m /tmp/magic /tmp/a.bin`,
		`file --magic-file=/tmp/magic /tmp/a.bin`,
		`file -bC /tmp/magic`,
		`file --vers`,
		`file --mime-t /dev/null`,
		`file -b --mime-type /tmp/a.bin`,
		`rg --pre sh foo /tmp`,
		`rg --pre=sh foo /tmp`,
		`rg --pre-glob '*.pdf' foo /tmp`,
		`rg --pre-glob=*.pdf foo /tmp`,
		`rg --search-zip foo /tmp`,
		`rg -z foo /tmp`,
		`rg -zn foo /tmp`,
		`sed -n '1,20w /tmp/evil' /tmp/a.txt`,
		`sed -n '1e id' /tmp/a.txt`,
		`sed -n 's/a/b/p' /tmp/a.txt`,
		`sed -n '/a/b/p' /tmp/a.txt`,
		`sed -n -f /tmp/evil.sed /tmp/a.txt`,
		`sed -i '' 's/a/b/' /tmp/a.txt`,
		`find /tmp -fprint0 /tmp/out.txt`,
		`find /tmp -fprint /tmp/out.txt`,
		`find /tmp -fprintf /tmp/out.txt '%p\n'`,
		`find /tmp -fls /tmp/out.txt`,
		`sort --compress-program=sh /tmp/a.txt`,
		`sort --compress-program sh /tmp/a.txt`,
		`sort -T /tmp/dir /tmp/a.txt`,
		`sort --temporary-directory=/tmp/dir /tmp/a.txt`,
		`git -c core.pager=sh log`,
		`git diff --output=/tmp/patch.diff`,
		`git diff --output /tmp/patch.diff`,
		`git log --output=/tmp/log.txt`,
		`git show --ext-diff HEAD`,
		`git diff --ext-diff`,
		`git show --textconv HEAD:a`,
		`git branch new-branch`,
		`git branch -a new-branch`,
		`git branch -D old`,
		`sqlite3 -readonly /tmp/a.db "SELECT 1"`,
		`sqlite3 /tmp/a.db "select 1"`,
		`sqlite3 -readonly /tmp/a.db ".shell ls"`,
		`tar -xf /tmp/a.tar`,
		`tar -cf /tmp/a.tar /tmp/dir`,
		`tar -tf host:a.tar`,
		`tar -tvf user@host:/tmp/a.tar`,
		`tar -tjf /tmp/a.tar.bz2`,
		`tar -tf /tmp/a.tar --use-compress-program=sh`,
		`unzip /tmp/a.zip`,
		`unzip -d /tmp /tmp/a.zip`,
		`unzip -lq /tmp/a.zip`,
		`unzip -1 /tmp/a.zip`,
		`unzip -l -Z /tmp/a.zip`,
		`plutil -convert xml1 /tmp/a.plist`,
		`plutil -lint -s /tmp/a.plist`,
		`xxd /tmp/in.bin /tmp/out.txt`,
		`xxd -r /tmp/in.hex`,
		`xxd -i /tmp/in.bin`,
		`xxd -l abc /tmp/in.bin`,
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

func TestScratchPathSymlinkAndTraversal(t *testing.T) {
	tmpRoot := t.TempDir()
	notesDir := t.TempDir()
	discDir := filepath.Join(notesDir, "discussions")
	if err := os.MkdirAll(discDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()

	// 1. 指向根外的父目录软链接
	outLink := filepath.Join(tmpRoot, "escape_dir")
	if err := os.Symlink(outsideDir, outLink); err != nil {
		t.Fatal(err)
	}
	// 2. 即使指向根内的末级软链接也必须拒绝
	realFile := filepath.Join(tmpRoot, "real.txt")
	if err := os.WriteFile(realFile, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	leafLink := filepath.Join(tmpRoot, "leaf_link.txt")
	if err := os.Symlink(realFile, leafLink); err != nil {
		t.Fatal(err)
	}
	// 3. 悬空末级软链接（指向根外不存在文件）
	danglingLink := filepath.Join(tmpRoot, "dangling.txt")
	if err := os.Symlink(filepath.Join(outsideDir, "ghost.txt"), danglingLink); err != nil {
		t.Fatal(err)
	}
	// 4. discussions 目录下的根外软链接
	discOutLink := filepath.Join(discDir, "escape")
	if err := os.Symlink(outsideDir, discOutLink); err != nil {
		t.Fatal(err)
	}
	// 5. 指向根外文件的硬链接（nlink > 1）
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardLink := filepath.Join(tmpRoot, "hardlink.txt")
	if err := os.Link(outsideFile, hardLink); err != nil {
		t.Fatal(err)
	}
	discHardLink := filepath.Join(discDir, "hardlink.md")
	if err := os.Link(outsideFile, discHardLink); err != nil {
		t.Fatal(err)
	}
	// 6. 非普通文件：已有目录与 FIFO 命名管道
	existingSubDir := filepath.Join(tmpRoot, "existing_dir")
	if err := os.MkdirAll(existingSubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifoPath := filepath.Join(tmpRoot, "pipe.fifo")
	if err := syscall.Mkfifo(fifoPath, 0o600); err != nil {
		t.Fatal(err)
	}

	roots := ScratchRoots{TmpDir: tmpRoot, NotesDir: notesDir}

	// 正常落盘（包含尚不存在的子目录、普通单链接文件及目录写目标）应放行
	safeCmds := []string{
		`echo hi > ` + filepath.Join(tmpRoot, "newdir", "out.txt"),
		`echo hi > ` + realFile,
		`echo hi | tee ` + filepath.Join(tmpRoot, "sub", "out.txt"),
		`mkdir -p ` + filepath.Join(tmpRoot, "newdir", "child"),
		`mkdir -p ` + existingSubDir,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output ` + filepath.Join(tmpRoot, "dl") + ` --as user`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output ` + existingSubDir + ` --as user`,
		`echo hi > ` + filepath.Join(discDir, "mb_1", "reply.md"),
	}
	for _, cmd := range safeCmds {
		if !IsSafeReadOnlyCommandLineWithRoots(cmd, roots) {
			t.Errorf("expected SAFE with injected roots for: %s", cmd)
		}
	}
	if !IsSafeDiscussionWritePath(filepath.Join(discDir, "mb_1", "reply.md"), roots) {
		t.Fatal("expected safe discussion write path")
	}

	// 根外父链接、末级链接、悬空链接、硬链接、目录/FIFO 非普通文件、.. 越界、symlink/.. 均必须拒绝
	unsafeCmds := []string{
		`echo hi > ` + filepath.Join(outLink, "new.txt"),
		`echo hi | tee ` + filepath.Join(outLink, "new.txt"),
		`mkdir -p ` + filepath.Join(outLink, "sub"),
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output ` + filepath.Join(outLink, "dl") + ` --as user`,
		`echo hi > ` + leafLink,
		`echo hi | tee ` + leafLink,
		`echo hi > ` + danglingLink,
		`mkdir -p ` + outLink,
		`echo hi > ` + hardLink,
		`echo hi | tee ` + hardLink,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output ` + hardLink + ` --as user`,
		`echo hi > ` + existingSubDir,
		`echo hi | tee ` + existingSubDir,
		`echo hi > ` + fifoPath,
		`echo hi | tee ` + fifoPath,
		`mkdir -p ` + fifoPath,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output ` + fifoPath + ` --as user`,
		`echo hi > ` + tmpRoot + `/../outside.txt`,
		`echo hi > ` + outLink + `/../safe_looking.txt`,
		`echo hi > ` + filepath.Join(discOutLink, "evil.md"),
	}
	for _, cmd := range unsafeCmds {
		if IsSafeReadOnlyCommandLineWithRoots(cmd, roots) {
			t.Errorf("expected UNSAFE with injected roots for: %s", cmd)
		}
	}
	if IsSafeDiscussionWritePath(filepath.Join(discOutLink, "evil.md"), roots) {
		t.Fatal("expected discussion symlink escape to be rejected")
	}
	if IsSafeDiscussionWritePath(discHardLink, roots) {
		t.Fatal("expected discussion hardlink to be rejected")
	}
	if IsSafeDiscussionWritePath(filepath.Join(discDir, "..", "escape.md"), roots) {
		t.Fatal("expected discussion .. escape to be rejected")
	}

	// 7. $POSSE_NOTES/discussions 根本身或 $POSSE_NOTES 本身为软链接时，不视为安全根
	symDiscParent := t.TempDir()
	symDisc := filepath.Join(symDiscParent, "discussions")
	if err := os.Symlink(outsideDir, symDisc); err != nil {
		t.Fatal(err)
	}
	rootsSymDisc := ScratchRoots{TmpDir: tmpRoot, NotesDir: symDiscParent}
	if IsSafeReadOnlyCommandLineWithRoots(`echo hi > `+filepath.Join(symDisc, "a.md"), rootsSymDisc) {
		t.Fatal("symlinked discussions root should not be trusted")
	}
	if IsSafeDiscussionWritePath(filepath.Join(symDisc, "a.md"), rootsSymDisc) {
		t.Fatal("symlinked discussions root should reject write_file")
	}

	symNotesBase := t.TempDir()
	symNotes := filepath.Join(symNotesBase, "notes_link")
	if err := os.Symlink(outsideDir, symNotes); err != nil {
		t.Fatal(err)
	}
	rootsSymNotes := ScratchRoots{TmpDir: tmpRoot, NotesDir: symNotes}
	if IsSafeReadOnlyCommandLineWithRoots(`echo hi > `+filepath.Join(symNotes, "discussions", "a.md"), rootsSymNotes) {
		t.Fatal("symlinked POSSE_NOTES root should not be trusted")
	}
	if IsSafeDiscussionWritePath(filepath.Join(symNotes, "discussions", "a.md"), rootsSymNotes) {
		t.Fatal("symlinked POSSE_NOTES root should reject write_file")
	}
}

func TestRound2QueryCommandsAndTrustedScripts(t *testing.T) {
	tmpRoot := t.TempDir()
	notesDir := t.TempDir()
	outsideDir := t.TempDir()

	// 准备受信脚本目录与单个受信脚本、以及指向目录外的软链逃逸脚本
	trustedDir := filepath.Join(t.TempDir(), "trusted-scripts")
	if err := os.MkdirAll(trustedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dirScript := filepath.Join(trustedDir, "fetch.sh")
	if err := os.WriteFile(dirScript, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	singleScript := filepath.Join(t.TempDir(), "download-log.sh")
	if err := os.WriteFile(singleScript, []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	outsideScript := filepath.Join(outsideDir, "evil.sh")
	if err := os.WriteFile(outsideScript, []byte("#!/bin/sh\nrm -rf /\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	escapeLink := filepath.Join(trustedDir, "escape.sh")
	if err := os.Symlink(outsideScript, escapeLink); err != nil {
		t.Fatal(err)
	}
	tmpOutLink := filepath.Join(tmpRoot, "outlink")
	if err := os.Symlink(outsideDir, tmpOutLink); err != nil {
		t.Fatal(err)
	}
	existingTmpDir := filepath.Join(tmpRoot, "existing-dir")
	if err := os.MkdirAll(existingTmpDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existingTmpFile := filepath.Join(tmpRoot, "existing-file.log")
	if err := os.WriteFile(existingTmpFile, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}

	roots := ScratchRoots{
		TmpDir:         tmpRoot,
		NotesDir:       notesDir,
		ReadOnlyHosts:  []string{"sentry.corp.laoyuegou.com", "gitlab.corp.laoyuegou.com", "sentry-alt.example.com:8443"},
		TrustedScripts: []string{singleScript, trustedDir + "/"},
	}

	ossOut1 := filepath.Join(tmpRoot, "oss-out-1")
	ossCp1 := filepath.Join(tmpRoot, "oss-cp-1")
	ossOut2 := filepath.Join(tmpRoot, "oss-out-2")
	ossCp2 := filepath.Join(tmpRoot, "oss-cp-2")

	allowed := []string{
		// 真实业务 Works 报障排查命令（3 条必过正例）
		`curl -s -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/projects/sentry/duoduotu-app/events/?query=user.id:80356859&per_page=1&full=true" | jq '.[0].user.data.userId'`,
		`aliyun sls GetLogs --region cn-hangzhou --project lyg-new-log --logstore prod --body '{"from":1700000000,"to":1700003600,"query":"86030680","line":20,"offset":0,"reverse":true}'`,
		`tccli cls SearchLog --region ap-nanjing --TopicId 6eea273b-d37a-4cc5-843e-fcb1f34b2553 --From 1700000000000 --To 1700003600000 --QueryString '80356859' --Limit 20`,

		// 1. glab
		`glab api projects/123/issues`,
		`glab api --paginate projects/123/merge_requests`,
		`glab api -X GET projects/123/pipelines`,
		`glab api --method=get -H "Accept: application/json" projects/123`,
		`glab api -H "Content-Type: application/json" projects/123`,
		`GITLAB_HOST=gitlab.corp.laoyuegou.com glab api projects/123/issues`,
		`GITLAB_HOST=gitlab.corp.laoyuegou.com glab mr list -R group/proj`,
		`glab repo view group/proj`,
		`glab repo view https://gitlab.corp.laoyuegou.com/group/proj`,
		`glab mr view 42 -c --comments`,
		`glab mr list -R group/sub/proj -A -P 10 --output=json`,
		`glab mr diff 42 --color=never --raw`,
		`glab issue view 7`,
		`glab issue list --all -P 20`,
		`glab release view v1.2.3`,
		`glab release list -F json`,
		`glab ci list --status=failed -P 10`,
		`glab ci status`,
		`glab ci status --branch main -c`,
		`glab repo clone group/proj ` + filepath.Join(tmpRoot, "repo1"),
		`glab repo clone https://gitlab.corp.laoyuegou.com/group/proj ` + filepath.Join(tmpRoot, "repo-https"),
		`glab repo clone group/proj ` + filepath.Join(tmpRoot, "repo2") + ` -- --depth 1 -b main --single-branch`,

		// 2. aliyun & ossutil
		`aliyun sls GetLogs --project=p1 --logstore=s1 --from=1 --to=2`,
		`aliyun --profile prod --region cn-hangzhou sls GetHistograms --project=p1`,
		`aliyun sls ListProject`,
		`aliyun sls ListLogStores --project=p1`,
		`aliyun sls DescribeLogstore --project=p1`,
		`aliyun oss ls oss://my-bucket/logs/`,
		`aliyun oss ls -s --limited-num 20 oss://my-bucket/logs/`,
		`aliyun oss stat oss://my-bucket/logs/a.log`,
		`aliyun ossutil ls oss://my-bucket/logs/`,
		`aliyun ossutil stat oss://my-bucket/logs/a.log`,
		`ossutil ls oss://my-bucket/logs/`,
		`ossutil stat oss://my-bucket/logs/a.log`,
		`aliyun oss cp --output-dir ` + ossOut1 + ` --checkpoint-dir ` + ossCp1 + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir=` + existingTmpDir + ` --checkpoint-dir=` + ossCp1 + ` oss://my-bucket/logs/a.log ` + existingTmpFile,
		`ossutil cp -r --output-dir ` + existingTmpDir + ` --checkpoint-dir ` + ossCp2 + ` oss://my-bucket/logs/ ` + filepath.Join(tmpRoot, "logs-new"),
		`lark-cli base +record-list --base-token EHz --table-id tbl --format json --as user`,
		`lark-cli base +record-list --base-token EHz --table-id tbl --json --as user`,
		`lark-cli base +record-search --base-token EHz --table-id tbl --keyword x --search-field Name --format=json --as user`,
		`lark-cli base +record-list --base-token EHz --table-id tbl --output ` + filepath.Join(tmpRoot, "rec.ndjson") + ` --as user`,

		// 3. tccli
		`tccli cls SearchLog --TopicId t1 --From 1 --To 2 --Query 'error'`,
		`tccli --region ap-shanghai cls DescribeTopics`,
		`tccli cls DescribeLogsets`,
		`tccli cls DescribeLogContext --TopicId t1`,
		`tccli cvm GetRegions`,
		`tccli cvm ListInstances`,

		// 4. curl (Sentry 只读接口)
		`curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -fsS -g -G -m 10 --compressed -H "Authorization: Bearer ${SENTRY_AUTH_TOKEN}" "https://sentry.corp.laoyuegou.com/api/0/issues/123/events/latest/"`,
		`curl --globoff -sS -X GET -w '\n%{http_code}\t%{response_code}\t%{content_type}\t%{size_download}\t%{time_total}' -o ` + filepath.Join(tmpRoot, "sentry.json") + ` -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/organizations/org/issues/"`,
		`curl -sSg -H "authorization: bearer $SENTRY_AUTH_TOKEN" "https://sentry-alt.example.com:8443/api/0/projects/"`,

		// 5. 机器人级受信脚本
		`bash ` + singleScript + ` --uid 12345 --out ` + filepath.Join(tmpRoot, "client.log"),
		`sh ` + dirScript + ` --date 20261125`,
		singleScript + ` --uid 12345 > ` + filepath.Join(tmpRoot, "out.log"),
		dirScript + ` --date 20261125 | head -n 50`,
	}
	for _, cmd := range allowed {
		if !IsSafeReadOnlyCommandLineWithRoots(cmd, roots) {
			t.Errorf("expected SAFE for Round 2 cmd: %s", cmd)
		}
	}

	denied := []string{
		// 1. glab 负例
		`glab ci view`,
		`glab ci status --live`,
		`glab ci status -l`,
		`glab ci status --wait`,
		`glab ci status -w`,
		`glab api -X POST projects/1/issues`,
		`glab api --method DELETE projects/1/issues/1`,
		`glab api graphql`,
		`glab api /graphql`,
		`glab api api/graphql?query=x`,
		`glab api projects/../graphql`,
		`glab api https://evil.com/api/v4/projects`,
		`glab api //evil.com/api/v4/projects`,
		`glab api projects/123%2F456/issues`,
		`glab api 'projects\123'`,
		`glab api --hostname evil.com projects/1/issues`,
		`glab api -f title=x projects/1/issues`,
		`glab api -F body=@/etc/passwd projects/1/issues`,
		`glab api --field title=x projects/1/issues`,
		`glab api --raw-field title=x projects/1/issues`,
		`glab api --input /tmp/a.json projects/1/issues`,
		`glab api -H "X-HTTP-Method-Override: POST" projects/1/issues`,
		`glab api -H "Host: evil.com" projects/1/issues`,
		`glab api -H "Authorization: Bearer secret" projects/1/issues`,
		`glab api -H "PRIVATE-TOKEN: secret" projects/1/issues`,
		`glab api -H "@file" projects/1/issues`,
		`glab api --unknown-flag projects/1/issues`,
		`glab mr view 42 --web`,
		`glab mr view 42 -cw`,
		`glab mr view 42 --unknown`,
		`glab mr list -w`,
		`glab mr list --hostname evil.com`,
		`glab mr list -R https://evil.com/group/proj`,
		`glab mr list -R git@gitlab.com:group/proj.git`,
		`glab mr list -R ssh://gitlab.corp.laoyuegou.com/group/proj`,
		`glab mr list -R http://gitlab.corp.laoyuegou.com/group/proj`,
		`glab mr list -R group/../evil`,
		`glab mr create --title x`,
		`glab repo view https://evil.com/group/proj`,
		`glab repo view singleword`,
		`glab issue delete 1`,
		`glab repo clone group/proj`,
		`glab repo clone group/proj /Users/x/repo`,
		`glab repo clone group/proj ` + existingTmpDir,
		`glab repo clone https://evil.com/group/proj ` + filepath.Join(tmpRoot, "r-evil"),
		`glab repo clone git@gitlab.com:group/proj.git ` + filepath.Join(tmpRoot, "r-ssh"),
		`glab repo clone group/proj ` + filepath.Join(tmpOutLink, "repo"),
		`glab repo clone group/proj ` + filepath.Join(tmpRoot, "r3") + ` -- -c core.sshCommand=evil`,
		`glab repo clone group/proj ` + filepath.Join(tmpRoot, "r3") + ` -- --upload-pack=evil`,
		`glab repo clone group/proj ` + filepath.Join(tmpRoot, "r3") + ` -- --template=/tmp/t`,
		`glab repo clone group/proj ` + filepath.Join(tmpRoot, "r3") + ` -- -o upstream`,
		`OTHER_HOST=x glab mr list`,
		`GITLAB_HOST=bad/host glab mr list`,
		`GITLAB_HOST=unconfigured.example.com glab api projects/1/issues`,
		`GITLAB_HOST=gitlab.corp.laoyuegou.com ls /tmp`,

		// 2. aliyun / ossutil 负例
		`aliyun sls DeleteLogStore --project=p1 --logstore=s1`,
		`aliyun sls CreateLogStore --project=p1 --logstore=s1`,
		`aliyun sls PutLogs --project=p1`,
		`aliyun sls GetSecret --project=p1`,
		`aliyun sls GetAccessKey --project=p1`,
		`aliyun --endpoint evil.com sls GetLogs --project=p1`,
		`aliyun sls GetLogs --endpoint evil.com --project=p1`,
		`aliyun sls GetLogs --config-path /tmp/cfg --project=p1`,
		`aliyun sls GetLogs --mode AK --project=p1`,
		`aliyun sls GetLogs --header X:Y --project=p1`,
		`aliyun sls GetLogs --force --project=p1`,
		`aliyun sls GetLogs -y --project=p1`,
		`aliyun sls GetLogs --access-key-id ak --project=p1`,
		`aliyun sls GetLogs --sts-token tok --project=p1`,
		`aliyun sls GetLogs --ram-role-arn arn --project=p1`,
		`aliyun sls GetLogs --body @/etc/passwd`,
		`aliyun sls GetLogs --body file:///etc/passwd`,
		`aliyun sls GetLogs --unknown x`,
		`aliyun oss ls --loglevel debug oss://my-bucket/`,
		`aliyun oss ls -e evil.com oss://my-bucket/`,
		`aliyun oss ls -c /tmp/cfg oss://my-bucket/`,
		`aliyun oss ls -o /tmp/out oss://my-bucket/`,
		`aliyun oss ls --checkpoint-dir /tmp/cp oss://my-bucket/`,
		`aliyun oss ls --snapshot-path /tmp/sp oss://my-bucket/`,
		`aliyun oss ls --unknown oss://my-bucket/`,
		`aliyun oss ls /local/path`,
		`aliyun oss ls oss://b/1 oss://b/2`,
		`ossutil stat --loglevel=info oss://my-bucket/a.log`,
		`aliyun oss cp oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir ` + ossOut1 + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir /Users/x/out --checkpoint-dir ` + ossCp1 + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir ` + existingTmpFile + ` --checkpoint-dir ` + ossCp1 + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir ` + ossOut1 + ` --checkpoint-dir ` + existingTmpFile + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --encoding-type url --output-dir ` + ossOut1 + ` --checkpoint-dir ` + ossCp1 + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a%2fb.log"),
		`aliyun oss cp --encoding-type=url --output-dir ` + ossOut1 + ` --checkpoint-dir ` + ossCp1 + ` oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir ` + ossOut1 + ` --checkpoint-dir ` + ossCp1 + ` -e evil.com oss://my-bucket/logs/a.log ` + filepath.Join(tmpRoot, "a.log"),
		`aliyun oss cp --output-dir ` + ossOut1 + ` --checkpoint-dir ` + ossCp1 + ` oss://my-bucket/logs/a.log ` + existingTmpDir,
		`ossutil cp -r --output-dir ` + ossOut2 + ` --checkpoint-dir ` + ossCp2 + ` oss://my-bucket/logs/ ` + existingTmpDir,
		`aliyun --profile ../evil sls GetLogs --project=p1`,
		`aliyun oss cp a.txt oss://b/`,
		`aliyun oss cp ` + filepath.Join(tmpRoot, "a.txt") + ` oss://b/a.txt`,
		`aliyun oss cp oss://b/a.txt /Users/x/a.txt`,
		`aliyun oss cp oss://b/a.txt ./a.txt`,
		`aliyun oss cp oss://b/a.txt ` + filepath.Join(tmpOutLink, "a.txt"),
		`aliyun oss rm oss://b/a.txt`,
		`aliyun oss sync oss://b/ ` + filepath.Join(tmpRoot, "sync"),
		`ossutil rm oss://b/a.txt`,
		`ossutil cp a.txt oss://b/`,
		`aliyun configure`,
		`aliyun ram ListUsers`,
		`aliyun sts GetCallerIdentity`,

		// 2b. lark-cli base 列表/搜索在未知当前目录缺省落盘 ndjson 的负例
		`lark-cli base +record-list --base-token EHz --table-id tbl --as user`,
		`lark-cli base +record-search --base-token EHz --table-id tbl --keyword x --search-field Name --as user`,
		`lark-cli base +record-search --base-token EHz --table-id tbl --json '{"keyword":"x","search_fields":["Name"]}' --as user`,
		`cd /tmp/posse-triage ; lark-cli base +record-list --base-token EHz --table-id tbl --as user`,
		`cd /tmp/posse-triage || lark-cli base +record-list --base-token EHz --table-id tbl --as user`,
		`cd /tmp/posse-triage | lark-cli base +record-list --base-token EHz --table-id tbl --as user`,

		// 3. tccli 负例（含 argparse 缩写、尾部缺值、路径 profile 与非白名单动作）
		`tccli cls DeleteTopic --TopicId t1`,
		`tccli cls CreateTopic --TopicName t1`,
		`tccli cls DescribeMetricSubscribePreview --TopicId t1`,
		`tccli auth login`,
		`tccli configure set secretId x`,
		`tccli sts AssumeRole`,
		`tccli sts GetFederationToken`,
		`tccli lke GetAppSecret`,
		`tccli faceid GetFaceIdToken`,
		`tccli ssm GetSecretValue`,
		`tccli kms DescribeKey`,
		`tccli cvm DescribeKeyPairs`,
		`tccli cls GetLogDownloadUrl`,
		`tccli --endpoint evil.com cls DescribeTopics`,
		`tccli --profile ../evil cls DescribeTopics`,
		`tccli cls SearchLog --profile /tmp/cfg --TopicId t1`,
		`tccli cls SearchLog --endpoint evil.com --TopicId t1`,
		`tccli cls SearchLog --endp evil.com --TopicId t1`,
		`tccli cls SearchLog --cli-in file:///etc/passwd`,
		`tccli cls SearchLog --reg ap-nanjing --TopicId t1`,
		`tccli cls SearchLog --use-cvminstance-role`,
		`tccli cls SearchLog --TopicId`,
		`tccli cls SearchLog --proxy http://127.0.0.1:8080 --TopicId t1`,
		`tccli cls SearchLog --cli-input-json file:///etc/passwd`,
		`tccli cls SearchLog --TopicId @/etc/passwd`,
		`tccli cls DescribeTopics DeleteTopic`,

		// 4. curl 负例
		`curl -fsSL "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -L "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl --location "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl --max-time=5 "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl --request=GET "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl --header="Accept: application/json" "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl --write-out='%{http_code}' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl --output=` + filepath.Join(tmpRoot, "eq.json") + ` "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -w '%output{/tmp/x}' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -w '%{stderr}' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -w '@file' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -w 'x@y' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -w '%{json}' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -o ` + filepath.Join(tmpRoot, "a.json") + ` -o ` + filepath.Join(tmpRoot, "b.json") + ` "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -o '` + filepath.Join(tmpRoot, "out_#1.json") + `' "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -o ` + existingTmpDir + ` "https://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -sS "http://sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -sS "https://sentry.corp.laoyuegou.com:8443/api/0/projects/"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/{a,b}"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/[1-2]"`,
		`curl -sS 'https://sentry.corp.laoyuegou.com/api/0/a\b'`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/a b"`,
		`curl -sS "https://user:pass@sentry.corp.laoyuegou.com/api/0/projects/"`,
		`curl -sS "https://evil.com/api/0/projects/"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/admin/"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/../../admin/"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/%2e%2e/admin/"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/a%2fb"`,
		`curl -sS "https://sentry.corp.laoyuegou.com/api/0/a%5cb"`,
		`curl -d x "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl --data-raw x "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -X POST "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -F a=b "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -T /etc/hosts "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -K /tmp/cfg "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -x http://127.0.0.1:8080 "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl --unix-socket /tmp/sock "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -u a:b "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -O "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -o /Users/x/out.json "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -o ` + filepath.Join(tmpOutLink, "out.json") + ` "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "Host: evil.com" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "Cookie: a=1" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "X-Custom-Header: val" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "X-Custom: $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "Authorization: Bearer $SENTRY_AUTH_TOKEN extra" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "Authorization: Bearer lit" -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "X: $OTHER" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H "X-HTTP-Method-Override: DELETE" "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl -H @/etc/hosts "https://sentry.corp.laoyuegou.com/api/0/issues/"`,
		`curl "https://sentry.corp.laoyuegou.com/api/0/issues/?t=$SENTRY_AUTH_TOKEN"`,
		`echo "$SENTRY_AUTH_TOKEN"`,

		// 5. 环境变量全量打印一律拒绝
		`env`,
		`printenv`,
		`printenv SENTRY_AUTH_TOKEN`,

		// 6. 受信脚本负例（软链逃逸、未信任路径、bash -c、越界重定向、非法参数）
		`bash ` + escapeLink,
		escapeLink,
		`bash ` + outsideScript,
		outsideScript,
		`bash -c "echo hi"`,
		`bash ` + singleScript + ` > /Users/x/out.log`,
		`bash ` + singleScript + ` $OTHER`,
		`bash ` + singleScript + ` /tmp/*.log`,

		// 7. 明确不放行工具
		`bsk snapshot`,
		`bsk evaluate "document.title"`,
		`npx prettier --check .`,
		`uvx ruff check .`,
		`pip list`,
		`python -c "print(1)"`,
		`python3 -c "print(1)"`,
	}
	for _, cmd := range denied {
		if IsSafeReadOnlyCommandLineWithRoots(cmd, roots) {
			t.Errorf("expected UNSAFE for Round 2 cmd: %s", cmd)
		}
	}

	// 受信脚本与自动可写目录（TmpDir / NotesDir / DiscussionsDir）必须双向互斥
	tmpScript := filepath.Join(tmpRoot, "in-tmp.sh")
	if err := os.WriteFile(tmpScript, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	discDir := filepath.Join(notesDir, "discussions")
	if err := os.MkdirAll(discDir, 0o755); err != nil {
		t.Fatal(err)
	}
	discScript := filepath.Join(discDir, "in-disc.sh")
	if err := os.WriteFile(discScript, []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	overlapRoots := ScratchRoots{
		TmpDir:   tmpRoot,
		NotesDir: notesDir,
		TrustedScripts: []string{
			tmpScript, tmpRoot + "/", strings.ToUpper(tmpRoot) + "/",
			discScript, discDir + "/", strings.ToUpper(discDir) + "/",
			notesDir + "/", filepath.Dir(tmpRoot) + "/",
		},
	}
	for _, bad := range []string{
		`bash ` + tmpScript,
		tmpScript,
		`bash ` + strings.ToUpper(tmpRoot) + `/in-tmp.sh`,
		`bash ` + discScript,
		discScript,
		`bash ` + singleScript, // singleScript 在 t.TempDir() 下，若信任其与 tmpRoot 共同祖先也必须拒绝
	} {
		if IsSafeReadOnlyCommandLineWithRoots(bad, overlapRoots) {
			t.Errorf("trusted script overlapping writable roots must be denied: %s", bad)
		}
	}

	// 未配置 readOnlyHosts 时，所有 curl 一律拒绝
	emptyHostRoots := ScratchRoots{TmpDir: tmpRoot, NotesDir: notesDir}
	if IsSafeReadOnlyCommandLineWithRoots(`curl -sS -H "Authorization: Bearer $SENTRY_AUTH_TOKEN" "https://sentry.corp.laoyuegou.com/api/0/projects/"`, emptyHostRoots) {
		t.Fatal("curl must be denied when readOnlyHosts is empty")
	}
}
