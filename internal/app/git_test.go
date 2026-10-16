package app

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

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
	v := a.openFilesPanel(&a.workspaces[0], sideChanges)
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

	// 提交：默认勾着已跟踪的改动，未跟踪的不勾、留下
	v.side = sideChanges
	v.git.message = "feat: 加日志"
	tt.Frame()
	if err := tt.Click("提交 1 个文件"); err != nil {
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

// branchWorkspace 在 gitWorkspace 的基础上：提交掉改动，建一个裸仓库当 origin 并推上去，
// 再建一个比 main 多一个提交的 feat 分支和一个检出在另一个 worktree 里的 agent 分支。
func branchWorkspace(t *testing.T, a *app) string {
	t.Helper()
	root := gitWorkspace(t, a)
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-q", "-m", "work")
	gitRun(t, root, "branch", "-M", "main")
	bare := filepath.Join(t.TempDir(), "origin.git")
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", bare)
	gitRun(t, root, "remote", "add", "origin", bare)
	gitRun(t, root, "push", "-q", "-u", "origin", "main")
	gitRun(t, root, "switch", "-q", "-c", "feat")
	os.WriteFile(filepath.Join(root, "feat.txt"), []byte("feat\n"), 0o644)
	gitRun(t, root, "add", "feat.txt")
	gitRun(t, root, "commit", "-q", "-m", "feat: 新功能")
	gitRun(t, root, "switch", "-q", "main")
	gitRun(t, root, "worktree", "add", "-q", "-b", "agent", filepath.Join(t.TempDir(), "agent-wt"))
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// local edit\n"), 0o644)
	gitRun(t, root, "commit", "-q", "-am", "fix: 本地修复")
	a.workspaces[0].Branch = "main"
	a.indexWorkspaces()
	return root
}

func TestBranchPopupAndCompare(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	a.openBranches(w)
	v := a.files[w.ID]
	until(t, tt, "branches", func() bool { return v.git.branches.list != nil && tt.HasText("本地") })
	for _, want := range []string{"更新项目", "Push…", "新建分支…", "feat", "agent", "在另一个 worktree", "远端 origin", "origin/main", "1 个传出提交（本地有、还没推送）"} {
		if !tt.HasText(want) {
			t.Fatalf("popup lacks %q: %v", want, tt.Texts())
		}
	}
	a.branchPop.query = "fea"
	tt.Frame()
	if tt.HasText("agent") || !tt.HasText("feat") {
		t.Fatal("search did not filter")
	}
	a.branchPop = nil

	// 和当前分支比较：两边独有的提交，选中提交看文件，点文件看 diff
	a.openCompare(w, v, "feat", "main", false)
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare && tt.HasText("中但不存在于") })
	cd := v.cur.comp
	if len(cd.onlyOther) != 1 || len(cd.onlyCur) != 1 || cd.onlyOther[0].Subject != "feat: 新功能" {
		t.Fatalf("compare %+v / %+v", cd.onlyOther, cd.onlyCur)
	}
	if err := tt.Click("feat: 新功能"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit files", func() bool { return cd.commit != nil && cd.commit.files != nil && tt.HasText("feat.txt") })
	if err := tt.Click("feat.txt"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit diff", func() bool { return v.cur.diff != nil && v.cur.kind == docDiff })
	if v.cur.diff.oldRev != "" || v.cur.diff.state.New.String() != "feat\n" {
		t.Fatalf("added file diff: old %q new %q", v.cur.diff.oldRev, v.cur.diff.state.New.String())
	}
	// 和工作区比较
	a.openCompare(w, v, "feat", "main", true)
	until(t, tt, "files compare", func() bool { return v.cur.kind == docCompare && tt.HasText("工作区和 feat 有 2 个文件不同") })

	// 删除没合并的分支：确认后再问一次强制删除
	b := gitxBranch(v, "feat")
	a.askDeleteBranch(v, b)
	a.confirming = false
	a.confirmDo()
	until(t, tt, "force ask", func() bool { return a.confirming && strings.Contains(a.confirmTitle, "还没合并") })
	a.confirming = false
	a.confirmDo()
	until(t, tt, "deleted", func() bool { return v.git.busy == "" && gitxBranch(v, "feat").Name == "" })
	if out := gitRun(t, root, "branch", "--list", "feat"); out != "" {
		t.Fatalf("feat still exists: %q", out)
	}

	// agent 正在工作时，检出要先确认
	w.Terminals[0].AgentState = &agentState{Status: "working"}
	ran := false
	a.guardAgent(w, "检出", func() { ran = true })
	if ran || !a.confirming || !strings.Contains(a.confirmTitle, "正在这个工作区里工作") {
		t.Fatalf("guard did not ask: ran=%v %q", ran, a.confirmTitle)
	}
}

func gitxBranch(v *filesView, name string) gitx.Branch {
	for _, b := range v.git.branches.list {
		if b.Name == name && !b.Remote {
			return b
		}
	}
	return gitx.Branch{}
}

func TestPushDialog(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	until(t, tt, "push plan", func() bool { return a.push != nil && a.push.loaded && tt.HasText("fix: 本地修复") })
	if !tt.HasText("main → origin : main") {
		t.Fatalf("push header: %v", tt.Texts())
	}
	if err := tt.Click("推送"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "pushed", func() bool { return a.push == nil && a.files[w.ID].git.busy == "" })
	if n := gitRun(t, root, "rev-list", "--count", "origin/main..main"); n != "0" {
		t.Fatalf("still %s ahead", n)
	}
}

// 提交图：m 合并了 b，b 和 a 都从 r 分出来
//
//	m
//	|\
//	a b
//	|/
//	r
func TestBuildGraph(t *testing.T) {
	cs := []gitx.CommitInfo{
		{Hash: "m", Parents: []string{"a", "b"}},
		{Hash: "a", Parents: []string{"r"}},
		{Hash: "b", Parents: []string{"r"}},
		{Hash: "r"},
	}
	g := buildGraph(cs)
	if g[0].lane != 0 || len(g[0].out) != 2 || g[0].parents[0] != 0 || g[0].parents[1] != 1 {
		t.Fatalf("merge row %+v", g[0])
	}
	if g[1].lane != 0 || g[2].lane != 1 {
		t.Fatalf("a on %d, b on %d", g[1].lane, g[2].lane)
	}
	// r 上两条线汇合：之后只剩没有线
	if g[3].lane != 0 || len(g[3].in) != 2 || len(g[3].out) != 0 {
		t.Fatalf("root row %+v", g[3])
	}
	if refs := refNames("HEAD -> main, origin/main, tag: v1"); len(refs) != 3 || !refs[0].head || refs[2].name != "v1" {
		t.Fatalf("refs %+v", refs)
	}
}

func TestLogBlameAndReword(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	a.openLog(w, "", "")
	v := a.files[w.ID]
	until(t, tt, "log", func() bool { return v.cur.kind == docLog && tt.HasText("fix: 本地修复") })
	for _, want := range []string{"feat: 新功能", "work", "base", "main", "origin/main"} {
		if !tt.HasText(want) {
			t.Fatalf("log lacks %q: %v", want, tt.Texts())
		}
	}
	ld := v.cur.log
	// 按文字筛选
	ld.text = "新功能"
	a.applyLogFilter(v, v.cur)
	until(t, tt, "filtered", func() bool { return len(ld.commits) == 1 && !ld.loading })
	ld.text = ""
	a.applyLogFilter(v, v.cur)
	until(t, tt, "unfiltered", func() bool { return len(ld.commits) == 4 && !ld.loading })
	// 选中提交看详情
	if err := tt.Click("fix: 本地修复"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "detail", func() bool { return ld.commit != nil && ld.commit.files != nil && tt.HasText("main.go") })
	// 改提交信息：读出原信息填进对话框，改完执行
	a.askMessage(v, "改提交信息", ld.commit.info.Hash, "", func(msg string) {
		a.gitDo(v, "改提交信息", func(ctx context.Context, root string) error { return gitx.Reword(ctx, root, ld.commit.info.Hash, msg) })
	})
	until(t, tt, "prompt", func() bool { return a.promptDlg != nil && a.promptDlg.value == "fix: 本地修复" })
	a.promptDlg.do("fix: 本地修复（改过）", false)
	a.promptDlg = nil
	until(t, tt, "reworded", func() bool {
		return v.git.busy == "" && gitRun(t, root, "log", "-1", "--format=%s") == "fix: 本地修复（改过）"
	})

	// 文件历史和 Annotate
	a.openLog(w, "", "main.go")
	until(t, tt, "file history", func() bool { return v.cur.kind == docLog && len(v.cur.log.commits) == 3 })
	a.openBlame(w, v, filepath.Join(root, "main.go"))
	until(t, tt, "blame", func() bool { return v.cur.kind == docBlame && tt.HasText("// local edit") })
	if !tt.HasText("t") || len(v.cur.blame.lines) == 0 {
		t.Fatal("blame lines missing")
	}
	// 日志、Annotate 不记进 ui.json
	for _, p := range a.state.Files[w.ID].Open {
		if strings.HasPrefix(p, "log:") || strings.HasPrefix(p, "blame:") {
			t.Fatalf("virtual doc saved: %s", p)
		}
	}
}

// 合并停在冲突上：自动切到改动，横幅说明冲突，采用一方后继续
func TestConflictBanner(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	gitRun(t, root, "switch", "-q", "feat")
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// feat edit\n"), 0o644)
	gitRun(t, root, "commit", "-q", "-am", "feat: 也改了 main.go")
	gitRun(t, root, "switch", "-q", "main")
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := a.openFilesPanel(w, sideFiles)
	until(t, tt, "status", func() bool { return v.git.status != nil })
	v.side = sideFiles
	a.gitDo(v, "合并", func(ctx context.Context, root string) error { return gitx.MergeInto(ctx, root, "feat") })
	until(t, tt, "banner", func() bool { return v.side == sideChanges && tt.HasText("正在合并，有 1 个文件冲突。") })
	if !strings.Contains(a.err, "合并失败") {
		t.Fatalf("merge error not shown: %q", a.err)
	}
	ours, theirs := sideNames("merge")
	if ours != "当前分支的（ours）" || !strings.Contains(theirs, "合入") {
		t.Fatal(ours, theirs)
	}
	a.gitDo(v, "解决冲突", func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, "main.go", false) })
	until(t, tt, "resolved", func() bool { return tt.HasText("正在合并，没有冲突了，可以继续。") })
	if err := tt.Click("继续"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "merged", func() bool { return v.git.status.Operation == "" && v.git.busy == "" })
	if b, _ := os.ReadFile(filepath.Join(root, "main.go")); !strings.Contains(string(b), "// feat edit") {
		t.Fatal("theirs not taken")
	}
}

// 合并停在冲突上，手动解决后从改动页提交：git 不许 commit --only，要整体提交成合并提交。
func TestCommitDuringMerge(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	gitRun(t, root, "switch", "-q", "feat")
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// feat edit\n"), 0o644)
	gitRun(t, root, "commit", "-q", "-am", "feat: 也改了 main.go")
	gitRun(t, root, "switch", "-q", "main")
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "status", func() bool { return v.git.status != nil })
	a.gitDo(v, "合并", func(ctx context.Context, root string) error { return gitx.MergeInto(ctx, root, "feat") })
	until(t, tt, "conflict", func() bool { return v.git.status.Operation == "merge" && v.git.busy == "" })
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// resolved\n"), 0o644)
	a.gitDo(v, "标记为已解决", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, "main.go") })
	until(t, tt, "resolved", func() bool {
		return v.git.busy == "" && !slices.ContainsFunc(v.git.status.Entries, func(e gitx.Entry) bool { return e.Conflict })
	})
	v.git.message = "merge feat"
	until(t, tt, "button", func() bool { return tt.HasText("提交合并结果") })
	a.err = ""
	if err := tt.Click("提交合并结果"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "committed", func() bool {
		return v.git.busy == "" && (a.err != "" || gitRun(t, root, "log", "-1", "--format=%s") == "merge feat")
	})
	if a.err != "" {
		t.Fatal(a.err)
	}
	if parents := gitRun(t, root, "log", "-1", "--format=%P"); len(strings.Fields(parents)) != 2 {
		t.Fatalf("not a merge commit: %q", parents)
	}
}

// 日志左边的分支树：点一个分支只看它的提交，再点一次回到全部分支。
func TestLogRefTree(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	a.openLog(&a.workspaces[0], "", "")
	v := a.files["w1"]
	until(t, tt, "log", func() bool { return v.cur.kind == docLog && v.git.branches.list != nil })
	ld := v.cur.log
	all := len(ld.commits)
	if !tt.HasText("本地") || !tt.HasText("远程") {
		t.Fatalf("ref tree missing: %v", tt.Texts())
	}
	if err := tt.Click("本地分支 main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "filtered", func() bool { return ld.filter.Branch == "main" && !ld.loading && len(ld.commits) < all })
	if err := tt.Click("本地分支 main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "all again", func() bool { return ld.filter.Branch == "" && !ld.loading && len(ld.commits) == all })
}

// 分支弹窗：远端默认收起，点标题展开；搜索时全部展开。
func TestBranchPopupFolds(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	a.openBranches(&a.workspaces[0])
	v := a.files["w1"]
	until(t, tt, "branches", func() bool { return v.git.branches.list != nil && tt.HasText("远端 origin") })
	// 本地 main 后面也写着跟踪的 origin/main，远端那组展开后这几个字多出一处
	remoteRow := func() bool { return countText(tt, "origin/main") > 2 }
	if remoteRow() {
		t.Fatal("remote group should start collapsed")
	}
	if err := tt.Click("远端 origin"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "expanded", remoteRow)
	if err := tt.Click("远端 origin"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "collapsed", func() bool { return !remoteRow() })
	a.branchPop.query = "main"
	until(t, tt, "search expands", remoteRow)
}

// countText 是界面上恰好显示 s 的文字有几处。
func countText(tt *ui.Tester, s string) int {
	n := 0
	for _, x := range tt.Texts() {
		if x == s {
			n++
		}
	}
	return n
}

// 勾选提交：取消勾选的文件（即使已经暂存）不进提交、留在暂存区；勾上的新文件一起提交。
func TestCommitPicked(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	gitRun(t, root, "add", "README.md")
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	if !tt.HasText("提交 2 个文件") {
		t.Fatalf("default picks tracked changes: %v", tt.Texts())
	}
	for _, name := range []string{"勾选 README.md", "勾选 notes.txt"} {
		if err := tt.Click(name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if v.git.picked(gitx.Entry{Path: "README.md"}) || !v.git.picked(gitx.Entry{Path: "notes.txt", Untracked: true}) {
		t.Fatal("clicking the boxes should flip the picks")
	}
	v.git.message = "pick"
	tt.Frame()
	if err := tt.Click("提交 2 个文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit", func() bool { return v.git.busy == "" && gitRun(t, root, "log", "-1", "--format=%s") == "pick" })
	files := gitRun(t, root, "show", "--name-status", "--format=", "HEAD")
	if !strings.Contains(files, "main.go") || !strings.Contains(files, "notes.txt") || strings.Contains(files, "README.md") {
		t.Fatalf("committed files:\n%s", files)
	}
	if st := gitRun(t, root, "status", "--porcelain"); !strings.Contains(st, "M  README.md") {
		t.Fatalf("README.md should stay staged: %q", st)
	}
}

// 图片的 diff：两边都解码成图，带原始尺寸；新增的图左边标成新文件。
func TestImageDiff(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	pngOf := func(w, h int) []byte {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		var b bytes.Buffer
		png.Encode(&b, img)
		return b.Bytes()
	}
	os.WriteFile(filepath.Join(root, "logo.png"), pngOf(40, 20), 0o644)
	gitRun(t, root, "add", "logo.png")
	gitRun(t, root, "commit", "-q", "-m", "logo")
	os.WriteFile(filepath.Join(root, "logo.png"), pngOf(80, 30), 0o644)
	os.WriteFile(filepath.Join(root, "new.png"), pngOf(10, 10), 0o644)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], diffSpec{rel: "logo.png", oldRev: "HEAD"}, nil, true)
	until(t, tt, "image diff", func() bool { return v.cur.kind == docImageDiff })
	im := v.cur.diff.images
	if im[0].w != 40 || im[0].h != 20 || im[1].w != 80 || im[1].h != 30 {
		t.Fatalf("sizes %dx%d → %dx%d", im[0].w, im[0].h, im[1].w, im[1].h)
	}
	if !tt.HasText("40×20") || !tt.HasText("80×30") {
		t.Fatalf("dimensions not shown: %v", tt.Texts())
	}
	a.openDiff(&a.workspaces[0], diffSpec{rel: "new.png", oldRev: "HEAD"}, nil, true)
	until(t, tt, "new image", func() bool { return v.cur.kind == docImageDiff && tt.HasText("新文件") })
}

// 分支按钮第一次画出来就自动 fetch，远端的新提交算进传入；五分钟内不再拉。到点再拉时，开着的分支比较跟着重读。
func TestAutoFetch(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	until(t, tt, "first fetch", func() bool { _, ok := a.fetchAt[w.ProjectID]; return ok && a.track["w1"].poll.at != time.Time{} })
	at := a.fetchAt[w.ProjectID]
	tt.Frame()
	if !a.fetchAt[w.ProjectID].Equal(at) {
		t.Fatal("should not fetch again within the interval")
	}
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "repo", func() bool { return v.git.repo != "" })
	a.openCompare(w, v, "origin/main", "main", false)
	d := v.cur
	until(t, tt, "compare", func() bool { return d.kind == docCompare || d.err != "" })
	if d.err != "" {
		t.Fatal(d.err)
	}
	if len(d.comp.onlyOther) != 0 {
		t.Fatalf("origin/main has nothing new yet: %d", len(d.comp.onlyOther))
	}

	other := filepath.Join(t.TempDir(), "other")
	gitRun(t, root, "clone", "-q", gitRun(t, root, "remote", "get-url", "origin"), other)
	os.WriteFile(filepath.Join(other, "remote.txt"), []byte("x\n"), 0o644)
	gitRun(t, other, "add", "remote.txt")
	gitRun(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "remote")
	gitRun(t, other, "push", "-q", "origin", "main")
	a.fetchAt[w.ProjectID] = time.Time{} // 当作五分钟到了
	until(t, tt, "fetched", func() bool { return a.track["w1"].behind == 1 && len(d.comp.onlyOther) == 1 })

	// 手动 Fetch 同样重读
	os.WriteFile(filepath.Join(other, "remote2.txt"), []byte("y\n"), 0o644)
	gitRun(t, other, "add", "remote2.txt")
	gitRun(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "remote 2")
	gitRun(t, other, "push", "-q", "origin", "main")
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	a.gitDo(v, "Fetch", func(ctx context.Context, root string) error { return gitx.Fetch(ctx, root) })
	until(t, tt, "manual fetch", func() bool { return len(d.comp.onlyOther) == 2 })
}
