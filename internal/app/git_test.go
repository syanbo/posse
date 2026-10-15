package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/gitx"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gitWorkspace 在 filesWorkspace 的基础上做一次提交，再改两个文件、加一个新文件。
func gitWorkspace(t *testing.T, a *app) string {
	t.Helper()
	root := filesWorkspace(t, a)
	gitRun(t, root, "config", "user.email", "t@example.com")
	gitRun(t, root, "config", "user.name", "t")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-q", "-m", "base")
	write := func(rel, text string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.go", strings.Replace(sampleGo, "run(cfg)", "run(cfg)\n\tlog(cfg)", 1)+"\nfunc log(c *config) {}\n")
	write("README.md", "# Hive\n\n多 agent 终端管理器，界面可以随时关闭。\n")
	write("notes.txt", "todo\n")
	return root
}

func openChanges(t *testing.T, a *app, tt *ui.Tester) *filesView {
	t.Helper()
	v := a.showFiles(&a.workspaces[0])
	v.side = sideChanges
	until(t, tt, "git status", func() bool { return v.git.status != nil && len(v.git.rows) > 0 })
	return v
}

func TestGitChangesAndDiff(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	for _, want := range []string{"改动 3", "README.md", "main.go", "notes.txt", "未跟踪"} {
		if !tt.HasText(want) {
			t.Fatalf("changes view lacks %q: %v", want, tt.Texts())
		}
	}
	// 点文件打开 diff，停在第一处改动
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "diff", func() bool { return v.cur != nil && v.cur.kind == docDiff && v.cur.diff.jumped })
	d := v.cur
	if n := len(d.diff.state.Hunks()); n != 1 {
		t.Fatalf("README hunks %d", n)
	}
	// F7：README 只有一处改动，跳到下一个文件 main.go
	tt.Key(0, ui.KeyF7)
	until(t, tt, "next file", func() bool {
		return v.cur != d && v.cur.diff != nil && v.cur.diff.rel == "main.go" && v.cur.kind == docDiff
	})
	main := v.cur
	if n := len(main.diff.state.Hunks()); n != 2 {
		t.Fatalf("main.go hunks %d", n)
	}
	tt.Key(0, ui.KeyF7)
	tt.Frame()
	if main.diff.hunk != 1 {
		t.Fatalf("F7 inside file: hunk %d", main.diff.hunk)
	}
	// ⇧F7 两次：回到第一处，再退回 README 的最后一处
	tt.Key(ui.Shift, ui.KeyF7)
	tt.Key(ui.Shift, ui.KeyF7)
	until(t, tt, "back to README", func() bool { return v.cur == d })
	// 回滚一处：立刻写回磁盘
	a.revertHunk(v, d, 0)
	if b, _ := os.ReadFile(filepath.Join(root, "README.md")); !strings.Contains(string(b), "## 构建") {
		t.Fatalf("README not reverted: %q", b)
	}
	until(t, tt, "status after revert", func() bool { return v.git.changeCount() == 2 })

	// 编辑器行号旁的改动以 HEAD 为基准
	a.openFile(&a.workspaces[0], filepath.Join(root, "main.go"), 0, [2]int{})
	until(t, tt, "diff base", func() bool { return v.cur.kind == docText && v.cur.ed.DiffBase != nil })
	if !strings.Contains(v.cur.headBuf.String(), "run(cfg)\n}") {
		t.Fatal("diff base is not HEAD")
	}

	// 提交：没有已暂存的文件时提交所有已跟踪的改动，未跟踪的留下
	v.side = sideChanges
	v.git.message = "feat: 加日志"
	tt.Frame()
	if err := tt.Click("提交"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit", func() bool { return v.git.busy == "" && v.git.changeCount() == 1 })
	if msg := gitRun(t, root, "log", "-1", "--format=%s"); msg != "feat: 加日志" {
		t.Fatalf("commit message %q", msg)
	}
	if v.git.message != "" {
		t.Fatal("message kept after commit")
	}
	// 回滚未跟踪的文件要确认，确认后删除
	a.askRollback(v, []gitx.Entry{{Path: "notes.txt", Untracked: true}})
	if !a.confirming || !strings.Contains(a.confirmBody, "新文件会被删除") {
		t.Fatalf("rollback should ask: %v %q", a.confirming, a.confirmBody)
	}
	a.confirming = false
	a.confirmDo()
	until(t, tt, "clean", func() bool { return v.git.busy == "" && v.git.changeCount() == 0 })
	if _, err := os.Stat(filepath.Join(root, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("untracked file not deleted")
	}
}

// 只改了缩进：忽略空白时没有改动，关掉后才有
func TestDiffIgnoreWhitespace(t *testing.T) {
	st := editor.DiffState{Old: editor.NewBuffer("a\n  b\nc\n"), New: editor.NewBuffer("a\n\tb\nc\nd\n"), IgnoreWhitespace: true}
	if h := st.Hunks(); len(h) != 1 || h[0].NewStart != 3 {
		t.Fatalf("ignoring whitespace: %+v", h)
	}
	st.IgnoreWhitespace = false
	if h := st.Hunks(); len(h) != 2 {
		t.Fatalf("not ignoring: %+v", h)
	}
}

// 工作区路径经过符号链接时，仓库根目录换成工作区路径的写法
func TestRepoRoot(t *testing.T) {
	real := t.TempDir()
	os.MkdirAll(filepath.Join(real, "repo", "sub"), 0o755)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Join(real, "repo"), link); err != nil {
		t.Fatal(err)
	}
	top, _ := filepath.EvalSymlinks(filepath.Join(real, "repo"))
	if got := repoRoot(link, top); got != link {
		t.Errorf("root: %q", got)
	}
	if got := repoRoot(filepath.Join(link, "sub"), top); got != link {
		t.Errorf("subdir workspace: %q", got)
	}
	if got := repoRoot(link, ""); got != "" {
		t.Errorf("not a repo: %q", got)
	}
}

func TestStashTitle(t *testing.T) {
	for in, want := range map[string]string{"On main: 试验": "试验", "WIP on feat/x: 1a2b3c 改了": "1a2b3c 改了", "自己写的": "自己写的"} {
		if got := stashTitle(in); got != want {
			t.Errorf("stashTitle(%q) = %q", in, got)
		}
	}
}
