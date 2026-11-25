package agent

import (
	"os"
	"path/filepath"
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

		// B. 允许的变量与 ~ 开头
		`echo "POSSE_NOTES=${POSSE_NOTES}"`,
		`echo "$POSSE_NOTES" "$HOME" "${HOME}"`,
		`ls -la ~/Desktop/Works`,
		`file -b --mime-type /tmp/a.bin`,

		// C. 白名单绝对路径程序
		`/bin/ls -la /tmp`,
		`/usr/bin/git status`,
		`/opt/homebrew/bin/rg foo /tmp`,
		filepath.Join(homeDir, ".local", "bin", "rg") + ` foo /tmp`,
		`$HOME/.local/bin/rg foo /tmp`,
		filepath.Join(posseHome, "bin", "posse") + ` history list`,

		// E. lark-cli 规则
		`lark-cli --help`,
		`lark-cli -h`,
		`lark-cli schema base`,
		`lark-cli base +record-update --base-token EHz --table-id tbl --record-id r1 --dry-run --as user`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output /tmp/posse-triage/att --as user`,
		`lark-cli base +record-download-attachment --base-token EHz --table-id tbl --record-id r1 --output=` + discFile + ` --as user`,
		`lark-cli drive +download --file-token f1 --output ` + discFile,
		`lark-cli drive +download --file-token f1 --output-dir $POSSE_NOTES/discussions/mb_1`,
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

		// B. 变量展开、修饰符、任何 token 内的未引号 glob/花括号、波浪号、裸赋值段、未引号括号
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
