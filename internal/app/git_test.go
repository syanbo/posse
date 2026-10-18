package app

import (
	"bytes"
	"context"
	"fmt"
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

	"hive/internal/client"
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
	// 文件栏滑进来之前点不准
	until(t, tt, "git status", func() bool { return v.git.status != nil && len(v.git.rows) > 0 && a.filesPanelShown == 1 })
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
	if d.name() != "差异：README.md" {
		t.Fatalf("diff tab title %q", d.name())
	}
	if n := len(d.diff.state.Hunks()); n != 1 {
		t.Fatalf("README hunks %d", n)
	}
	// F7：README 只有一处改动，在同一个 diff 标签里换到下一个文件 main.go
	tt.Key(0, ui.KeyF7)
	until(t, tt, "next file", func() bool {
		return v.cur == d && d.diff.rel == "main.go" && d.kind == docDiff
	})
	if n := len(d.diff.state.Hunks()); n != 2 {
		t.Fatalf("main.go hunks %d", n)
	}
	tt.Key(0, ui.KeyF7)
	tt.Frame()
	if d.diff.hunk != 1 {
		t.Fatalf("F7 inside file: hunk %d", d.diff.hunk)
	}
	// ⇧F7 两次：回到第一处，再退回 README 的最后一处
	tt.Key(ui.Shift, ui.KeyF7)
	tt.Key(ui.Shift, ui.KeyF7)
	until(t, tt, "back to README", func() bool { return v.cur == d && d.diff.rel == "README.md" && d.kind == docDiff })
	// 点改动列表里的另一个文件：还是这一个 diff 标签
	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "clicked main.go", func() bool { return d.diff.rel == "main.go" && d.kind == docDiff })
	diffs := 0
	for _, x := range v.docs {
		if x.diff != nil {
			diffs++
		}
	}
	if diffs != 1 || v.cur != d {
		t.Fatalf("%d diff tabs", diffs)
	}
	tt.Key(ui.Shift, ui.KeyF7)
	tt.Key(ui.Shift, ui.KeyF7)
	tt.Key(ui.Shift, ui.KeyF7)
	until(t, tt, "README again", func() bool { return d.diff.rel == "README.md" && d.kind == docDiff })
	// README 是第一个文件、只有一处改动：再往前就到头了，diff 栏下面提示
	tt.Key(ui.Shift, ui.KeyF7)
	until(t, tt, "start hint", func() bool { return tt.HasText("已经是第一处改动") })
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
	a.askRollback(v, []gitx.Entry{{Path: "notes.txt", Untracked: true}}, nil)
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

	// 删除没合并的分支：问一次强制删除；删掉后提示里可以恢复
	b := gitxBranch(v, "feat")
	tip := gitRun(t, root, "rev-parse", "feat")
	a.askDeleteBranch(v, b)
	until(t, tt, "force ask", func() bool { return a.confirming && strings.Contains(a.confirmTitle, "还没合并") })
	a.confirming = false
	a.confirmDo()
	until(t, tt, "deleted", func() bool { return v.git.busy == "" && gitxBranch(v, "feat").Name == "" })
	if out := gitRun(t, root, "branch", "--list", "feat"); out != "" {
		t.Fatalf("feat still exists: %q", out)
	}
	until(t, tt, "restore toast", func() bool { return tt.HasText("已删除分支 feat") })
	if err := tt.Click("恢复"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "restored", func() bool { return v.git.busy == "" && gitxBranch(v, "feat").Name == "feat" })
	if got := gitRun(t, root, "rev-parse", "feat"); got != tip {
		t.Fatalf("restored at %s, want %s", got, tip)
	}
	// 已经合并的分支直接删，不弹确认
	gitRun(t, root, "branch", "merged")
	until(t, tt, "merged listed", func() bool { a.loadBranches(v); return gitxBranch(v, "merged").Name != "" })
	a.askDeleteBranch(v, gitxBranch(v, "merged"))
	until(t, tt, "merged deleted", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--list", "merged") == "" })
	if a.confirming {
		t.Fatal("merged branch should not ask")
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
	until(t, tt, "log", func() bool { return v.logCur.kind == docLog && tt.HasText("fix: 本地修复") })
	for _, want := range []string{"feat: 新功能", "work", "base", "main", "origin/main"} {
		if !tt.HasText(want) {
			t.Fatalf("log lacks %q: %v", want, tt.Texts())
		}
	}
	ld := v.logCur.log
	// 按文字筛选
	ld.text = "新功能"
	a.applyLogFilter(v, v.logCur)
	until(t, tt, "filtered", func() bool { return len(ld.commits) == 1 && !ld.loading })
	ld.text = ""
	a.applyLogFilter(v, v.logCur)
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
	until(t, tt, "file history", func() bool { return v.logCur.kind == docLog && len(v.logCur.log.commits) == 3 })
	// Annotate 在这个文件自己的标签里开关，不另开标签
	a.openBlame(w, v, filepath.Join(root, "main.go"))
	d := v.cur
	tabs := len(v.docs)
	until(t, tt, "blame", func() bool {
		return d.blame != nil && !d.blame.loading && len(d.blame.lines) > 0 && tt.HasText("// local edit")
	})
	if d.path != filepath.Join(root, "main.go") || d.name() != "main.go" {
		t.Fatalf("annotate not in the file tab: %s", d.name())
	}
	// 再点一次 Annotate 不会多出标签
	a.openBlame(w, v, filepath.Join(root, "main.go"))
	if len(v.docs) != tabs || v.cur != d {
		t.Fatalf("annotate opened another tab: %d -> %d", tabs, len(v.docs))
	}
	until(t, tt, "blame again", func() bool { return !d.blame.loading })
	if err := tt.Click("关闭 Annotate"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if d.blame != nil || v.cur != d || len(v.docs) != tabs {
		t.Fatal("closing annotate should go back to the editor in the same tab")
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
	until(t, tt, "log", func() bool { return v.logCur.kind == docLog && v.git.branches.list != nil })
	ld := v.logCur.log
	settle(tt) // 浮层滑上来之后位置才固定，再去点
	all := len(ld.commits)
	if !tt.HasText("本地") || !tt.HasText("远程") {
		t.Fatalf("ref tree missing: %v", tt.Texts())
	}
	if err := tt.Click("本地分支 main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "filtered", func() bool { return ld.filter.Branch == "refs/heads/main" && !ld.loading && len(ld.commits) < all })
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
	// 提交里删掉的图片：新的一边是"已删除"
	gitRun(t, root, "rm", "-qf", "logo.png")
	gitRun(t, root, "commit", "-q", "-m", "rm logo")
	head := gitRun(t, root, "rev-parse", "HEAD")
	a.openDiff(&a.workspaces[0], diffSpec{rel: "logo.png", oldRev: head + "^", newRev: head}, nil, true)
	until(t, tt, "deleted image", func() bool {
		return v.cur.kind == docImageDiff && v.cur.diff.rel == "logo.png" && v.cur.diff.oldRev != "HEAD"
	})
	if im := v.cur.diff.images; im[1].size != -1 || im[1].err != "" {
		t.Fatalf("deleted side %+v", im[1])
	}
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

// ⌘K：文件标签在前台时把焦点给提交信息，终端在前台时不注册（留给终端清屏）。
func TestCommitShortcut(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.activeTerm[w.ID] = w.Terminals[0].ID
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if v.git.focusMsg {
		t.Fatal("⌘K in a terminal should not go to the commit box")
	}
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if !v.git.focusMsg && !tt.Focused("提交信息") {
		t.Fatal("⌘K with a file in front should focus the commit message")
	}
}

// Git 日志浮层：盖在终端上、不改终端尺寸；隐藏按钮和 ⌘9 开关，文件历史可以关掉，整个仓库的日志留着。
func TestLogPanel(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	settle(tt)
	cols, rows := a.view.term.Size()
	tt.Key(ui.Cmd, ui.Key9)
	v := a.files[w.ID]
	until(t, tt, "log", func() bool { return a.state.LogPanel && v.logCur != nil && v.logCur.kind == docLog })
	settle(tt)
	if c2, r2 := a.view.term.Size(); c2 != cols || r2 != rows {
		t.Fatalf("terminal resized under the panel: %dx%d -> %dx%d", cols, rows, c2, r2)
	}
	if a.filesActive(w) {
		t.Fatal("log opened as a tab")
	}
	a.openLog(w, "", "main.go")
	until(t, tt, "history", func() bool { return v.logCur.kind == docLog && tt.HasText("历史：main.go") })
	if err := tt.Click("关闭历史：main.go"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(v.logs) != 1 || v.logCur != v.logs[0] || v.logCur.log.filter.Path != "" {
		t.Fatalf("after closing history: %d logs", len(v.logs))
	}
	if err := tt.Click("隐藏 ⌘9"); err != nil {
		t.Fatal(err)
	}
	settle(tt)
	if a.state.LogPanel || tt.HasText("隐藏 ⌘9") {
		t.Fatal("hide button did not hide the panel")
	}
	tt.Key(ui.Cmd, ui.Key9)
	settle(tt)
	if !a.state.LogPanel || len(v.logs) != 1 {
		t.Fatalf("⌘9 reopen: open %v logs %d", a.state.LogPanel, len(v.logs))
	}
}

// 回滚：改动页批量回滚（勾选、新文件只在勾了"删除新文件"时删）、diff 栏回滚整个文件后换到下一个、
// 编辑器改动标记的卡片回滚一处。
func TestRollback(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	exists := func(rel string) bool { _, err := os.Stat(filepath.Join(root, rel)); return err == nil }

	// 编辑器卡片：main.go 有两处改动，回滚第一处只改编辑器里的内容
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	d := v.cur
	until(t, tt, "diff base", func() bool { return d.kind == docText && d.ed.DiffBase != nil && len(d.ed.Changes()) == 2 })
	d.ed.OnChangeClick(0, 0)
	until(t, tt, "change popup", func() bool { return tt.HasText("第 1 / 2 处") })
	if err := tt.Click("下一处改动"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "second change", func() bool {
		return tt.HasText("第 2 / 2 处") && tt.HasText("这里新增了 2 行，上一次提交时没有")
	})
	if err := tt.Click("上一处改动"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("回滚这一处 ⌥⌘Z"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "reverted in editor", func() bool { return d.change == nil && len(d.ed.Changes()) == 1 })
	if strings.Contains(d.ed.Buffer.String(), "log(cfg)") || !d.isModified() {
		t.Fatalf("first change not reverted in the buffer: %q", d.ed.Buffer.String())
	}
	d.ed.OnChangeClick(0, 0)
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if d.change != nil {
		t.Fatal("Esc did not close the popup")
	}
	d.ed.Run(editor.Undo)
	until(t, tt, "undo", func() bool { return len(d.ed.Changes()) == 2 && !d.isModified() })

	// diff 栏回滚整个文件：README 回滚后换到同一组的下一个文件
	a.openDiff(w, diffSpec{rel: "README.md", oldRev: "HEAD"}, nil, true)
	dd := v.cur
	until(t, tt, "diff", func() bool { return dd.kind == docDiff })
	a.rollbackDiffFile(w, v, dd)
	if !a.confirming {
		t.Fatal("rollback should ask")
	}
	a.confirming = false
	a.confirmDo()
	until(t, tt, "README rolled back", func() bool {
		return v.git.busy == "" && v.git.changeCount() == 2 && dd.diff.rel == "main.go"
	})

	// 批量回滚：默认勾着已跟踪的 main.go，新文件 notes.txt 不勾
	a.openRollback(v)
	r := &a.rollback
	if !r.open || len(r.entries) != 2 || !r.on["main.go"] || r.on["notes.txt"] {
		t.Fatalf("rollback dialog %+v", r.on)
	}
	tt.Frame()
	// 勾上新文件但不删：未跟踪的不动，也不算进按钮上的数
	r.on["notes.txt"] = true
	tt.Frame()
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "batch", func() bool { return v.git.busy == "" && v.git.changeCount() == 1 })
	if !exists("notes.txt") {
		t.Fatal("untracked file deleted without asking")
	}
	// 再回滚一次，勾上"删除新文件"
	a.openRollback(v)
	r.on["notes.txt"], r.delNew = true, true
	tt.Frame()
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "clean", func() bool { return v.git.busy == "" && v.git.changeCount() == 0 })
	if exists("notes.txt") {
		t.Fatal("new file kept with delete checked")
	}
}

// 改动列表的组可以展开收起：收起后只留标题，F7 仍能跳到收起组里的文件。
func TestChangeGroupsFold(t *testing.T) {
	a := previewApp(t)
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	if !tt.HasText("notes.txt") || !tt.HasText("main.go") {
		t.Fatalf("files missing: %v", tt.Texts())
	}
	if err := tt.Click("收起未跟踪"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("notes.txt") || !tt.HasText("main.go") {
		t.Fatal("untracked group did not fold")
	}
	// 点组名也能切换
	if err := tt.Click("改动"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("README.md") || *v.git.groupOpen(groupChanged) {
		t.Fatal("changed group did not fold by its title")
	}
	if err := tt.Click("改动"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("README.md") {
		t.Fatal("changed group did not unfold")
	}
}

// 编辑器顶栏回滚整个文件：没保存的改动一起丢掉、按磁盘重载；新文件回滚就是删掉并关掉标签。
func TestRollbackDoc(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	d := v.cur
	until(t, tt, "open", func() bool { return d.kind == docText && tt.HasText("回滚这个文件…") })
	d.ed.Insert("// 没保存\n")
	tt.Frame()
	if err := tt.Click("回滚这个文件…"); err != nil {
		t.Fatal(err)
	}
	if !a.confirming {
		t.Fatal("rollback should ask")
	}
	a.confirming = false
	a.confirmDo()
	head := gitRun(t, root, "show", "HEAD:main.go")
	until(t, tt, "reloaded", func() bool {
		return v.git.busy == "" && !d.isModified() && strings.TrimSpace(d.ed.Buffer.String()) == strings.TrimSpace(head)
	})
	until(t, tt, "button gone", func() bool { return !tt.HasText("回滚这个文件…") })

	a.openFile(w, filepath.Join(root, "notes.txt"), 0, [2]int{})
	n := v.cur
	until(t, tt, "notes", func() bool { return n.kind == docText && tt.HasText("回滚这个文件…") })
	a.rollbackDoc(v, n)
	a.confirming = false
	a.confirmDo()
	until(t, tt, "deleted", func() bool { return !slices.Contains(v.docs, n) })
	if _, err := os.Stat(filepath.Join(root, "notes.txt")); !os.IsNotExist(err) {
		t.Fatal("new file not deleted")
	}
}

// 文件历史：左边是改过这个文件的提交，默认选第一个；右边是这个提交对这个文件的 diff，换提交就换 diff。
func TestFileHistory(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	a.openLog(w, "", "main.go")
	v := a.files[w.ID]
	d := v.logCur
	until(t, tt, "history", func() bool {
		h := d.log.hist
		return d.kind == docLog && h != nil && h.diff != nil && h.diff.kind == docDiff
	})
	settle(tt)
	ld := d.log
	if len(ld.commits) < 2 {
		t.Fatalf("main.go has %d commits", len(ld.commits))
	}
	first, second := ld.commits[0], ld.commits[1]
	if ld.hist.sel != first.Hash || ld.hist.diff.diff.newRev != first.Hash {
		t.Fatal("first commit not selected")
	}
	if ld.hist.diff.diff.state.Layout != editor.DiffUnified {
		t.Fatal("history diff should start unified")
	}
	if !tt.HasText(first.Subject) || !tt.HasText(shortRev(first.Hash)) {
		t.Fatalf("history view lacks the first commit: %v", tt.Texts())
	}
	if err := tt.Click(second.Subject); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "second", func() bool {
		hd := ld.hist.diff
		return ld.hist.sel == second.Hash && hd.diff.newRev == second.Hash && hd.kind == docDiff
	})
	// 往前跳过头：提示一下
	for range len(ld.hist.diff.diff.state.Hunks()) + 1 {
		if err := tt.Click("上一处差异"); err != nil {
			t.Fatal(err)
		}
	}
	until(t, tt, "hint", func() bool { return tt.HasText("已经是第一处差异") })
}

// 回滚对话框的文件树：目录在前、文件在后，单子目录的中间层并成一行，收起的目录不列下面的行。
func TestRollbackTree(t *testing.T) {
	entries := []gitx.Entry{{Path: "internal/app/view.go"}, {Path: "README.md"}, {Path: "internal/app/rollback.go"}, {Path: "docs/a.md"}, {Path: "docs/plans/b.html"}}
	show := func(rows []treeRow) string {
		var b strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&b, "%s%s", strings.Repeat("  ", r.depth), r.name)
			if r.dir {
				fmt.Fprintf(&b, "/%d", len(r.files))
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	want := "docs/2\n  plans/1\n    b.html\n  a.md\ninternal/app/2\n  rollback.go\n  view.go\nREADME.md\n"
	if got := show(rollbackTree(entries, nil)); got != want {
		t.Fatalf("tree:\n%s\nwant:\n%s", got, want)
	}
	want = "docs/2\ninternal/app/2\n  rollback.go\n  view.go\nREADME.md\n"
	if got := show(rollbackTree(entries, map[string]bool{"docs": true})); got != want {
		t.Fatalf("folded:\n%s", got)
	}
}

// 有操作在执行（自动 Fetch）时，用户的操作排在后面，不被丢掉。
func TestGitOpQueue(t *testing.T) {
	a := previewApp(t)
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	v.git.busy = "Fetch"
	var ran []string
	for _, what := range []string{"检出", "提交"} {
		a.gitDo(v, what, func(ctx context.Context, root string) error {
			a.update(func() { ran = append(ran, what) })
			return nil
		})
	}
	if len(v.git.queue) != 2 || !v.git.pending("提交") || v.git.busyText() != "Fetch…，接着检出等 2 个" {
		t.Fatalf("queue %d %q", len(v.git.queue), v.git.busyText())
	}
	v.git.busy = ""
	a.gitNext(v)
	until(t, tt, "queued ops ran", func() bool { return len(ran) == 2 && v.git.busy == "" })
	if ran[0] != "检出" || ran[1] != "提交" {
		t.Fatalf("order %v", ran)
	}
}

// 终端里提交之后 HEAD 变了，开着的 diff 跟着重读，不再显示已经提交的改动。
func TestDiffFollowsHead(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], changeSpec(gitx.Entry{Path: "main.go"}), nil, true)
	d := v.cur
	until(t, tt, "diff", func() bool { return d.kind == docDiff && len(d.diff.state.Hunks()) == 2 })
	gitRun(t, root, "commit", "-q", "-am", "agent commit")
	until(t, tt, "diff reloaded", func() bool {
		a.refreshGit(v)
		return d.kind == docDiff && len(d.diff.state.Hunks()) == 0
	})
}

// diff 读进来之后文件又被改了：回滚一处不能拿旧内容覆盖新写的。
func TestRevertHunkStaleDisk(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], changeSpec(gitx.Entry{Path: "README.md"}), nil, true)
	d := v.cur
	until(t, tt, "diff", func() bool { return d.kind == docDiff && len(d.diff.state.Hunks()) == 1 })
	path := filepath.Join(root, "README.md")
	b, _ := os.ReadFile(path)
	agent := string(b) + "agent 刚写的\n"
	os.WriteFile(path, []byte(agent), 0o644)
	a.revertHunk(v, d, 0)
	if got, _ := os.ReadFile(path); string(got) != agent {
		t.Fatalf("revert overwrote newer content: %q", got)
	}
	if !strings.Contains(a.err, "刚被改过") {
		t.Fatalf("err %q", a.err)
	}
}

// 回滚对话框里点文件名在右边看 diff：逐处回滚写回磁盘，列表跟着更新；勾选框不打开 diff；F7 跨文件。
func TestRollbackDetail(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openRollback(v)
	r := &a.rollback
	tt.Frame()
	// 文件栏里也有"勾选 README.md"，按对话框里这一行的位置点它的勾选框
	row, ok := tt.Find("查看 README.md")
	if !ok {
		t.Fatal("no README row")
	}
	tt.ClickAt(row.X+4+20+6+7, row.Y+row.H/2)
	tt.Frame()
	if r.detail != nil || r.on["README.md"] {
		t.Fatalf("checkbox should only toggle: detail %v on %v", r.detail != nil, r.on["README.md"])
	}
	if err := tt.Click("查看 main.go"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "detail", func() bool {
		return r.detail != nil && r.detail.kind == docDiff && r.detail.diff.jumped && len(r.detail.diff.state.Hunks()) == 2
	})
	d := r.detail
	tt.Key(0, ui.KeyF7)
	tt.Frame()
	if d.diff.hunk != 1 {
		t.Fatalf("F7 in detail: hunk %d", d.diff.hunk)
	}
	// ⌥⌘Z 只回滚第二处（新加的 func log），第一处留着
	tt.Key(ui.Cmd|ui.Alt, ui.KeyZ)
	b, _ := os.ReadFile(filepath.Join(root, "main.go"))
	if strings.Contains(string(b), "func log") || !strings.Contains(string(b), "log(cfg)") {
		t.Fatalf("hunk revert: %q", b)
	}
	until(t, tt, "one hunk left", func() bool { return len(d.diff.state.Hunks()) == 1 && tt.HasText("第 1 / 1 处  +1 −0") })
	if d.diff.hunk != 0 || !tt.HasText("回滚这一处") {
		t.Fatalf("current hunk after revert: %d", d.diff.hunk)
	}
	if !r.open || r.detail != d {
		t.Fatal("dialog should stay on the file")
	}
	// 到头了换到树里的下一个文件（当前已经是最后一处）
	tt.Key(0, ui.KeyF7)
	until(t, tt, "next file", func() bool { return r.detail.diff.rel == "notes.txt" && r.detail.kind == docDiff })
	if err := tt.Click("收起 diff"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if r.detail != nil || !r.open {
		t.Fatal("close detail")
	}
	// 回滚剩下勾着的 main.go：整个文件恢复
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "rolled back", func() bool { return v.git.busy == "" && v.git.changeCount() == 2 })
}

// 改动卡片的"提交此更改"：只提交这一处，文件里另一处改动留在工作区；复制改之前的内容。
func TestCommitChangeFromPopup(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	d := v.cur
	until(t, tt, "diff base", func() bool { return d.kind == docText && d.ed.DiffBase != nil && len(d.ed.Changes()) == 2 })
	d.ed.OnChangeClick(0, 0)
	until(t, tt, "popup", func() bool { return tt.HasText("第 1 / 2 处") })
	if err := tt.Click("提交此更改"); err != nil {
		t.Fatal(err)
	}
	tt.Type("feat: 只加 log 调用")
	tt.Key(0, ui.KeyEnter)
	until(t, tt, "committed", func() bool { return v.git.busy == "" && d.change == nil && len(d.ed.Changes()) == 1 })
	head := gitRun(t, root, "show", "HEAD:main.go")
	if !strings.Contains(head, "log(cfg)") || strings.Contains(head, "func log") {
		t.Fatalf("HEAD main.go: %q", head)
	}
	if msg := gitRun(t, root, "log", "-1", "--format=%s"); msg != "feat: 只加 log 调用" {
		t.Fatalf("message %q", msg)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "main.go")); !strings.Contains(string(b), "func log") {
		t.Fatal("worktree lost the other change")
	}
	// 改了没保存时不让提交这一处
	d.ed.Buffer.Set("// x\n" + d.ed.Buffer.String())
	d.ed.OnChangeClick(0, 0)
	until(t, tt, "popup again", func() bool { return tt.HasText("先保存再提交这一处") })
}

func TestChangeContent(t *testing.T) {
	base := "a\nb\nc\nd\n"
	buf := "a\nB\nc\nd\nE\n"
	// 只取第二行的修改，不带末尾新加的 E
	if got := changeContent(base, buf, editor.BaseChange{OldStart: 1, OldEnd: 2, NewStart: 1, NewEnd: 2}); got != "a\nB\nc\nd\n" {
		t.Fatalf("modify: %q", got)
	}
	if got := changeContent(base, buf, editor.BaseChange{OldStart: 4, OldEnd: 4, NewStart: 4, NewEnd: 5}); got != "a\nb\nc\nd\nE\n" {
		t.Fatalf("add: %q", got)
	}
	if got := changeContent(base, "a\nd\n", editor.BaseChange{OldStart: 1, OldEnd: 3, NewStart: 1, NewEnd: 1}); got != "a\nd\n" {
		t.Fatalf("delete: %q", got)
	}
}

// 网络操作可以取消：取消后报"已取消"，排着的下一个接着执行。
func TestCancelNetworkOp(t *testing.T) {
	a := previewApp(t)
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	a.gitDo(v, "Push", func(ctx context.Context, root string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	ran := false
	a.gitDo(v, "提交", func(ctx context.Context, root string) error { ran = true; return nil })
	tt.Frame()
	if err := tt.Click("取消 Push"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "canceled", func() bool { return ran && v.git.busy == "" })
	if a.err != "Push已取消" {
		t.Fatalf("err %q", a.err)
	}
}

// 当前文件已经不在改动列表里（刚提交）时 F7 从列表开头接着走；改动数变少后当前位置收回范围内。
func TestStepDiffAfterCommit(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	w := &a.workspaces[0]
	a.openDiff(w, changeSpec(gitx.Entry{Path: "main.go"}), nil, true)
	d := v.cur
	until(t, tt, "diff", func() bool { return d.kind == docDiff && d.diff.jumped && len(d.diff.state.Hunks()) == 2 })
	tt.Key(0, ui.KeyF7)
	tt.Frame()
	if d.diff.hunk != 1 {
		t.Fatalf("hunk %d", d.diff.hunk)
	}
	// 外部只留下一处改动：位置收回到最后一处
	b, _ := os.ReadFile(filepath.Join(root, "main.go"))
	os.WriteFile(filepath.Join(root, "main.go"), []byte(strings.Replace(string(b), "\nfunc log(c *config) {}\n", "", 1)), 0o644)
	until(t, tt, "one hunk", func() bool { a.refreshGit(v); return d.kind == docDiff && len(d.diff.state.Hunks()) == 1 })
	if d.diff.hunk != 0 {
		t.Fatalf("hunk not clamped: %d", d.diff.hunk)
	}
	gitRun(t, root, "commit", "-q", "-am", "agent")
	until(t, tt, "main.go committed", func() bool {
		a.refreshGit(v)
		return v.git.changeCount() == 1 && d.kind == docDiff && len(d.diff.state.Hunks()) == 0
	})
	tt.Key(0, ui.KeyF7)
	until(t, tt, "moved to the list", func() bool { return d.diff.rel == "notes.txt" && d.kind == docDiff })
}

// 大文件不比较，提示一句；diff 不是磁盘上的文件，不给"用默认应用打开"。
func TestDiffLargeFile(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	os.WriteFile(filepath.Join(root, "big.lock"), []byte(strings.Repeat("x\n", diffMaxSize)), 0o644)
	a.openDiff(&a.workspaces[0], changeSpec(gitx.Entry{Path: "big.lock", Untracked: true}), nil, true)
	d := v.cur
	until(t, tt, "too large", func() bool { return d.kind == docTooLarge && tt.HasText(tooLargeDiff) })
	if tt.HasText("用默认应用打开") {
		t.Fatal("virtual diff doc offers to open diff: path")
	}
}

// 日志：分支和标签同名时按分支筛；作者框里的 "a[" 不报错；读出错时筛选栏还在、可以清除筛选；
// 按作者筛选时不画提交图。
func TestLogFilterEdges(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	gitRun(t, root, "tag", "feat", "main~2") // 和分支 feat 同名、指向更早的提交
	tt := ui.NewTester(a.build, 1200, 760)
	a.openLog(&a.workspaces[0], "", "")
	v := a.files["w1"]
	d := v.logCur
	ld := d.log
	until(t, tt, "log", func() bool { return d.kind == docLog && !ld.loading && v.git.branches.list != nil })
	ld.filter.Branch = "refs/heads/feat"
	a.loadLog(v, d, false)
	until(t, tt, "branch feat", func() bool { return !ld.loading && len(ld.commits) > 0 })
	if ld.commits[0].Subject != "feat: 新功能" {
		t.Fatalf("branch feat shows %q", ld.commits[0].Subject)
	}
	ld.filter.Branch = ""
	ld.filter.Author = "a["
	a.loadLog(v, d, false)
	until(t, tt, "author", func() bool { return !ld.loading })
	if ld.err != "" || d.kind != docLog {
		t.Fatalf("author a[: %q", ld.err)
	}
	ld.filter.Author = "t"
	a.loadLog(v, d, false)
	until(t, tt, "author t", func() bool { return !ld.loading && len(ld.commits) > 0 })
	if ld.graph != nil {
		t.Fatal("graph drawn for author filter")
	}
	// 选中的分支被删了：错误写在列表里，清除筛选回到全部
	ld.filter.Author, ld.filter.Branch = "", "refs/heads/gone"
	a.loadLog(v, d, false)
	settle(tt)
	until(t, tt, "error in list", func() bool { return !ld.loading && ld.err != "" && tt.HasText("清除筛选") })
	if d.kind != docLog || !tt.HasText("提交信息或哈希") {
		t.Fatal("filter bar gone on error")
	}
	if err := tt.Click("清除筛选"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "cleared", func() bool { return !ld.loading && ld.err == "" && len(ld.commits) > 0 && ld.graph != nil })
}

// 改写推送过的提交先提醒；没推送的直接往下走。操作停在冲突上时文件栏自动打开到改动页。
func TestGuardPushedAndConflictPanel(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	v := a.filesFor(w)
	until(t, tt, "repo", func() bool {
		if !v.git.checked {
			a.refreshGit(v)
		}
		return v.git.status != nil
	})
	ran := false
	a.guardPushed(v, gitRun(t, root, "rev-parse", "HEAD"), func() { ran = true })
	until(t, tt, "local commit", func() bool { return ran })
	if a.confirming {
		t.Fatal("unpushed commit should not ask")
	}
	ran = false
	a.guardPushed(v, gitRun(t, root, "rev-parse", "origin/main"), func() { ran = true })
	until(t, tt, "pushed asks", func() bool { return a.confirming && strings.Contains(a.confirmBody, "origin/main") })
	a.confirming = false
	a.confirmDo()
	until(t, tt, "then", func() bool { return ran })

	// cherry-pick 冲突：文件栏关着也打开到改动页
	a.closeFilesPanel()
	gitRun(t, root, "switch", "-q", "-c", "side", "HEAD~1")
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main // side\n"), 0o644)
	gitRun(t, root, "commit", "-q", "-am", "side")
	side := gitRun(t, root, "rev-parse", "HEAD")
	gitRun(t, root, "switch", "-q", "main")
	tt.Frame()
	a.gitDo(v, "Cherry-pick", func(ctx context.Context, root string) error { return gitx.CherryPick(ctx, root, side) })
	until(t, tt, "panel opened", func() bool {
		return v.git.status != nil && v.git.status.Operation == "cherry-pick" && a.filesPanelOpen() && v.side == sideChanges
	})
}

// 提交框：取消"修改上一次提交"时清掉自动填的旧信息；没写信息时提交按钮不可点；⌘K 在搁置分页上切回提交分页。
func TestCommitBoxDetails(t *testing.T) {
	a := previewApp(t)
	gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	g := &v.git
	a.setAmend(v, true)
	until(t, tt, "last message", func() bool { return g.message == "base" })
	a.setAmend(v, false)
	if g.message != "" {
		t.Fatalf("message kept: %q", g.message)
	}
	tt.Frame()
	if err := tt.Click("提交 2 个文件"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if g.busy != "" || a.err != "" {
		t.Fatalf("commit without message: busy %q err %q", g.busy, a.err)
	}
	g.tab, g.focusMsg = 1, true
	tt.Frame()
	if g.tab != 0 || !tt.HasText("修改上一次提交") {
		t.Fatal("⌘K should switch back to the commit tab")
	}
}

// 分支弹窗：搜索后回车检出第一个对得上的；↑↓ 换选中。
func TestBranchPopupKeyboard(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openBranches(w)
	v := a.files["w1"]
	until(t, tt, "branches", func() bool { return v.git.branches.list != nil })
	tt.Type("fea")
	tt.Frame()
	bp := a.branchPop
	if bp.hl != 0 || len(bp.rows) == 0 || bp.rows[0].Name != "feat" {
		t.Fatalf("hl %d rows %v", bp.hl, bp.rows)
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if bp.hl != min(1, len(bp.rows)-1) {
		t.Fatalf("down: %d", bp.hl)
	}
	tt.Key(0, ui.KeyUp)
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	// 预览数据里这个工作区有 agent 在工作，检出先确认
	if !a.confirming {
		t.Fatal("checkout should go through guardAgent")
	}
	a.confirming = false
	a.confirmDo()
	until(t, tt, "checked out", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--show-current") == "feat" })
	if a.branchPop != nil {
		t.Fatal("popup should close")
	}
}

// Git 日志：点一个提交后 ↑↓ 换选中。
func TestLogKeyboard(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	a.openLog(&a.workspaces[0], "", "")
	v := a.files["w1"]
	ld := v.logCur.log
	until(t, tt, "log", func() bool { return v.logCur.kind == docLog && len(ld.commits) > 2 })
	settle(tt)
	if err := tt.Click(ld.commits[0].Subject); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "selected", func() bool { return ld.commit != nil && ld.commit.info.Hash == ld.commits[0].Hash })
	tt.Key(0, ui.KeyDown)
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	if ld.commit.info.Hash != ld.commits[2].Hash {
		t.Fatalf("down twice: %s", ld.commit.info.Subject)
	}
	tt.Key(0, ui.KeyUp)
	tt.Frame()
	if ld.commit.info.Hash != ld.commits[1].Hash {
		t.Fatalf("up: %s", ld.commit.info.Subject)
	}
}

// 真的点行号旁的色条：卡片打开，不会被打开它的这次点击当成"点外面"关掉；之后点正文才关。
func TestChangeBarClick(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	var d *fileDoc
	var r ui.Rect
	tt := ui.NewTester(func(c *ui.Context) {
		a.build(c)
		if d != nil {
			r = d.edRef.Bounds(c)
		}
	}, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openFile(&a.workspaces[0], filepath.Join(root, "main.go"), 0, [2]int{})
	d = v.cur
	until(t, tt, "diff base", func() bool { return d.kind == docText && len(d.ed.Changes()) == 2 })
	settle(tt)
	y := r.Y + d.ed.LineBottom(d.ed.Changes()[0].NewStart) - 8
	// 色条在行号栏右边、折叠箭头左边；扫一遍行号栏，至少有一处能点开
	opened := false
	for x := float32(20); x < 60 && !opened; x += 3 {
		tt.ClickAt(r.X+x, y)
		tt.Frame()
		opened = d.change != nil
	}
	if !opened || !tt.HasText("第 1 / 2 处") {
		t.Fatal("clicking the change bar did not open the card")
	}
	tt.ClickAt(r.X+r.W/2, r.Y+40)
	tt.Frame()
	if d.change != nil {
		t.Fatal("clicking the text should close the card")
	}
}

// 改动列表点过一行后：↑↓ 换文件预览 diff，空格勾选，回车打开文件。
func TestChangesKeyboard(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "README diff", func() bool { return v.cur != nil && v.cur.kind == docDiff && v.cur.diff.rel == "README.md" })
	key := func(k ui.Key, want string) {
		t.Helper()
		tt.Key(0, k)
		until(t, tt, want, func() bool { return v.cur != nil && v.cur.kind == docDiff && v.cur.diff.rel == want })
	}
	key(ui.KeyDown, "main.go")
	key(ui.KeyDown, "notes.txt")
	notes := slices.IndexFunc(v.git.status.Entries, func(e gitx.Entry) bool { return e.Path == "notes.txt" })
	e := v.git.status.Entries[notes]
	if v.git.picked(e) {
		t.Fatal("untracked file should start unpicked")
	}
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if !v.git.picked(e) {
		t.Fatal("space should pick notes.txt")
	}
	key(ui.KeyDown, "notes.txt") // 到底不动
	key(ui.KeyUp, "main.go")
	tt.Key(0, ui.KeyEnter)
	until(t, tt, "open main.go", func() bool { return v.cur != nil && v.cur.diff == nil && v.cur.path == filepath.Join(root, "main.go") })
}
