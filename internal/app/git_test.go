package app

import (
	"bytes"
	"context"
	"errors"
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

	"posse/internal/client"
	"posse/internal/gitx"
	"posse/locales"
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
	write("README.md", "# Posse\n\n多 agent 终端管理器，界面可以随时关闭。\n")
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
	tt := newTester(a.build, 1200, 760)
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
	if d.name(locales.In("zh-Hans")) != "差异：README.md" {
		t.Fatalf("diff tab title %q", d.name(locales.In("zh-Hans")))
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
	a.revertHunk(locales.In("zh-Hans"), v, d, 0)
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
	v.ch.message = "feat: 加日志"
	tt.Frame()
	if err := tt.Click("提交 1 个文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit", func() bool { return v.git.busy == "" && v.git.changeCount() == 1 })
	if msg := gitRun(t, root, "log", "-1", "--format=%s"); msg != "feat: 加日志" {
		t.Fatalf("commit message %q", msg)
	}
	if v.ch.message != "" {
		t.Fatal("message kept after commit")
	}
	// 单独回滚未跟踪的文件也打开回滚对话框，只勾它、默认勾上删除新文件
	a.openRollbackFor(v.git, []string{"notes.txt"}, nil)
	if r := &a.rollback; !r.open || !r.on["notes.txt"] || !r.delNew {
		t.Fatalf("rollback dialog %v %+v %v", r.open, r.on, r.delNew)
	}
	tt.Frame()
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	a.openBranches(w)
	v := a.files[w.ID]
	until(t, tt, "branches", func() bool { return v.git.branches.list != nil && tt.HasText("本地") })
	settle(tt)
	for _, want := range []string{"更新项目", "Push…", "新建分支…", "feat", "agent", "worktree · agent-wt", "远端 origin", "origin/main", "1 个传出提交（本地有、还没推送）"} {
		if !tt.HasText(want) {
			t.Fatalf("popup lacks %q: %v", want, tt.Texts())
		}
	}
	// 不是 posse 工作区的 worktree：菜单能在 Posse 中打开、在访达中显示、复制路径
	if err := tt.Click("agent"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("在 Posse 中打开") || !tt.HasText("在访达中显示 worktree") || !tt.HasText("复制 worktree 路径") {
		t.Fatalf("worktree menu: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	// 左边缘往左拖变宽，松手写进 ui.json
	if r, ok := tt.Find("搜索分支"); ok {
		// 输入框文字左边是框的内边距 12、行 2、弹窗 6，再往里 3 是把手中间
		x, y := r.X-20+3, r.Y+60
		tt.Press(x, y)
		tt.Move(x-50, y)
		tt.Move(x-100, y)
		tt.Release(x-100, y)
		tt.Frame()
		if got := a.branchPopWidth(); got != branchPopDefault+100 || a.resizingBranch {
			t.Fatalf("branch popup width %v after dragging its left edge", got)
		}
	} else {
		t.Fatal("search field missing")
	}
	a.branchPop.query = "fea"
	tt.Frame()
	if tt.HasText("agent") || !tt.HasText("feat") {
		t.Fatal("search did not filter")
	}
	a.branchPop = nil

	// 和当前分支比较：两边独有的提交，选中提交看文件，点文件看 diff；底部的日志浮层先收起
	a.setLogPanel(true)
	a.openCompare(w, v, "feat", "main", false)
	if a.logPanelOpen() {
		t.Fatal("log panel still open over the compare view")
	}
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare && tt.HasText("中但不存在于") })
	cd := v.cur.comp
	if len(cd.onlyOther) != 1 || len(cd.onlyCur) != 1 || cd.onlyOther[0].Subject != "feat: 新功能" {
		t.Fatalf("compare %+v / %+v", cd.onlyOther, cd.onlyCur)
	}
	if err := tt.Click("feat: 新功能"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit files", func() bool { return cd.commit != nil && cd.commit.files != nil && tt.HasText("feat.txt") })
	// 方向键：→ 到右边的文件，只移动高亮、不打开（打开的 diff 会换掉比较页）；← 回到提交，回车不再打开文件
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	tt.Frame()
	if cd.commit.cursor != "feat.txt" || v.cur.comp != cd {
		t.Fatalf("→: cursor %q, compare still current %v", cd.commit.cursor, v.cur.comp == cd)
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if v.cur.comp != cd {
		t.Fatal("← did not leave the file list: Enter opened a diff")
	}
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
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
	a.askDeleteBranch(locales.In("zh-Hans"), v, b)
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
	until(t, tt, "merged listed", func() bool { a.loadBranches(v.git); return gitxBranch(v, "merged").Name != "" })
	a.askDeleteBranch(locales.In("zh-Hans"), v, gitxBranch(v, "merged"))
	until(t, tt, "merged deleted", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--list", "merged") == "" })
	if a.confirming {
		t.Fatal("merged branch should not ask")
	}

	// agent 正在工作时，检出要先确认
	w.Terminals[0].AgentState = &agentState{Status: "working"}
	ran := false
	a.guardAgent(locales.In("zh-Hans"), w, "检出", func() { ran = true })
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
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	openLogWait(t, a, tt, w, "")
	v := a.files[w.ID]
	until(t, tt, "log", func() bool { return v.logCur != nil && v.logCur.kind == docLog && tt.HasText("fix: 本地修复") })
	ld := v.logCur.log
	// 默认只看当前分支，feat 上的提交不列
	if !slices.Equal(ld.filter.Branches, []string{"refs/heads/main"}) || tt.HasText("feat: 新功能") {
		t.Fatalf("default filter %v", ld.filter.Branches)
	}
	ld.filter.Branches = nil
	a.loadLog(v, v.logCur, false)
	until(t, tt, "all branches", func() bool { return !ld.loading && tt.HasText("feat: 新功能") })
	for _, want := range []string{"feat: 新功能", "work", "base", "main", "origin/main"} {
		if !tt.HasText(want) {
			t.Fatalf("log lacks %q: %v", want, tt.Texts())
		}
	}
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
	a.askMessage(locales.In("zh-Hans"), v.git.repo, "改提交信息", ld.commit.info.Hash, "", func(msg string) {
		a.gitDo(locales.In("zh-Hans"), v.git, "改提交信息", func(ctx context.Context, root string) error { return gitx.Reword(ctx, root, ld.commit.info.Hash, msg) })
	})
	until(t, tt, "prompt", func() bool { return a.promptDlg != nil && a.promptDlg.value == "fix: 本地修复" })
	a.promptDlg.do("fix: 本地修复（改过）", false)
	a.promptDlg = nil
	until(t, tt, "reworded", func() bool {
		return v.git.busy == "" && gitRun(t, root, "log", "-1", "--format=%s") == "fix: 本地修复（改过）"
	})

	// 文件历史和 Annotate
	openLogWait(t, a, tt, w, "main.go")
	until(t, tt, "file history", func() bool { return v.logCur != nil && v.logCur.kind == docLog && len(v.logCur.log.commits) == 3 })
	// Annotate 在这个文件自己的标签里开关，不另开标签
	a.openBlame(locales.In("zh-Hans"), w, v, filepath.Join(root, "main.go"))
	d := v.cur
	tabs := len(v.docs)
	until(t, tt, "blame", func() bool {
		return d.blame != nil && !d.blame.loading && len(d.blame.lines) > 0 && tt.HasText("// local edit")
	})
	if d.path != filepath.Join(root, "main.go") || d.name(locales.In("zh-Hans")) != "main.go" {
		t.Fatalf("annotate not in the file tab: %s", d.name(locales.In("zh-Hans")))
	}
	// 再点一次 Annotate 不会多出标签
	a.openBlame(locales.In("zh-Hans"), w, v, filepath.Join(root, "main.go"))
	if len(v.docs) != tabs || v.cur != d {
		t.Fatalf("annotate opened another tab: %d -> %d", tabs, len(v.docs))
	}
	until(t, tt, "blame again", func() bool { return !d.blame.loading })
	// Annotate 里 ⌘F 查找，命中所在的行按需搭出来时铺底色
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	tt.Type("local edit")
	tt.Frame()
	if !d.find.open || d.find.n != 1 || !tt.HasText("1/1") {
		t.Fatalf("annotate find: open=%v n=%d", d.find.open, d.find.n)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
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

// 照 WebStorm：更新前自动储藏的本地改动取回时冲突，不报失败；切到改动页，横幅引去解决，解决完可以删掉储藏
func TestAutostashBanner(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	gitRun(t, root, "switch", "-q", "feat")
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// feat edit\n"), 0o644)
	gitRun(t, root, "commit", "-q", "-am", "feat: 改了 main.go")
	gitRun(t, root, "switch", "-q", "main")
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// 没提交的改动\n"), 0o644)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := a.openFilesPanel(w, sideFiles)
	until(t, tt, "status", func() bool { return v.git.status != nil && a.filesPanelShown == 1 })
	v.side = sideFiles
	// 照 git 的 --autostash：先储藏、更新到 feat，取回时冲突，再把储藏以 "autostash" 存进列表，命令照样成功
	a.gitDo(locales.In("zh-Hans"), v.git, "更新", func(ctx context.Context, root string) error {
		git := func(args ...string) (string, error) {
			cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
			out, err := cmd.CombinedOutput()
			return strings.TrimSpace(string(out)), err
		}
		sha, err := git("stash", "create")
		if err != nil {
			return err
		}
		for _, args := range [][]string{{"reset", "-q", "--hard"}, {"reset", "-q", "--hard", "feat"}} {
			if out, err := git(args...); err != nil {
				return fmt.Errorf("%v: %s", err, out)
			}
		}
		if _, err := git("stash", "apply", sha); err == nil {
			return errors.New("stash applied without conflict")
		}
		if out, err := git("stash", "store", "-m", "autostash", sha); err != nil {
			return fmt.Errorf("%v: %s", err, out)
		}
		return &gitx.AutostashLeft{Stash: sha, Conflicts: true}
	})
	until(t, tt, "banner", func() bool {
		return v.side == sideChanges && tt.HasText("更新前的本地改动和拉下来的提交有冲突（1 个文件）")
	})
	if a.err != "" {
		t.Fatalf("stash conflict reported as failure: %q", a.err)
	}
	ours, theirs := sideNames(locales.In("zh-Hans"), v.git.status.Operation, v.git.autostash != "" && v.git.autostashConflicts)
	if ours != "更新后的版本" || theirs != "我的本地改动" {
		t.Fatal(ours, theirs)
	}
	a.gitDo(locales.In("zh-Hans"), v.git, "解决冲突", func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, "main.go", false) })
	until(t, tt, "resolved", func() bool { return tt.HasText("冲突都解决了") })
	if err := tt.Click("删除储藏…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("删除"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "dropped", func() bool { return v.git.autostash == "" && !tt.HasText("冲突都解决了") })
	if got := gitRun(t, root, "stash", "list"); got != "" {
		t.Fatalf("stash left: %s", got)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := a.openFilesPanel(w, sideFiles)
	until(t, tt, "status", func() bool { return v.git.status != nil && a.filesPanelShown == 1 })
	v.side = sideFiles
	a.gitDo(locales.In("zh-Hans"), v.git, "合并", func(ctx context.Context, root string) error { return gitx.MergeInto(ctx, root, "feat") })
	until(t, tt, "banner", func() bool { return v.side == sideChanges && tt.HasText("正在合并，有 1 个文件冲突。") })
	if !strings.Contains(a.err, "合并失败") {
		t.Fatalf("merge error not shown: %q", a.err)
	}
	ours, theirs := sideNames(locales.In("zh-Hans"), "merge", false)
	if ours != "当前分支的（ours）" || !strings.Contains(theirs, "合入") {
		t.Fatal(ours, theirs)
	}
	a.gitDo(locales.In("zh-Hans"), v.git, "解决冲突", func(ctx context.Context, root string) error { return gitx.TakeSide(ctx, root, "main.go", false) })
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "status", func() bool { return v.git.status != nil && a.filesPanelShown == 1 })
	a.gitDo(locales.In("zh-Hans"), v.git, "合并", func(ctx context.Context, root string) error { return gitx.MergeInto(ctx, root, "feat") })
	until(t, tt, "conflict", func() bool { return v.git.status.Operation == "merge" && v.git.busy == "" })
	os.WriteFile(filepath.Join(root, "main.go"), []byte(sampleGo+"\n// resolved\n"), 0o644)
	a.gitDo(locales.In("zh-Hans"), v.git, "标记为已解决", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, "main.go") })
	until(t, tt, "resolved", func() bool {
		return v.git.busy == "" && !slices.ContainsFunc(v.git.status.Entries, func(e gitx.Entry) bool { return e.Conflict })
	})
	v.ch.message = "merge feat"
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
	tt := newTester(a.build, 1200, 760)
	openLogWait(t, a, tt, &a.workspaces[0], "")
	v := a.files["w1"]
	until(t, tt, "log", func() bool { return v.logCur != nil && v.logCur.kind == docLog && v.git.branches.list != nil })
	ld := v.logCur.log
	// 默认选中当前分支
	until(t, tt, "current branch", func() bool {
		return slices.Equal(ld.filter.Branches, []string{"refs/heads/main"}) && !ld.loading
	})
	main := len(ld.commits)
	settle(tt) // 浮层滑上来之后位置才固定，再去点
	if !tt.HasText("本地") || !tt.HasText("远程") {
		t.Fatalf("ref tree missing: %v", tt.Texts())
	}
	if err := tt.Click("本地分支 main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "all", func() bool { return len(ld.filter.Branches) == 0 && !ld.loading && len(ld.commits) > main })
	all := len(ld.commits)
	if err := tt.Click("本地分支 main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "filtered", func() bool {
		return slices.Equal(ld.filter.Branches, []string{"refs/heads/main"}) && !ld.loading && len(ld.commits) == main
	})
	// ⌘ 单击再加一个分支：两边的提交都列出来
	if err := tt.ClickWith(ui.Cmd, "本地分支 feat"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "two branches", func() bool { return len(ld.filter.Branches) == 2 && !ld.loading && len(ld.commits) > main })
	if !tt.HasText("2 个分支") {
		t.Fatal("filter bar should say 2 个分支")
	}
	if err := tt.ClickWith(ui.Cmd, "本地分支 feat"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "feat removed", func() bool { return len(ld.filter.Branches) == 1 && !ld.loading && len(ld.commits) == main })
	if err := tt.Click("本地分支 main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "all again", func() bool { return len(ld.filter.Branches) == 0 && !ld.loading && len(ld.commits) == all })

	// 方向键：↑ 换到上一个分支并只看它；← 回到组、再 ← 收起；→ 展开、→ 进到第一项、→ 到提交列表
	key := func(k ui.Key) {
		tt.Key(0, k)
		tt.Frame()
		tt.Frame()
	}
	key(ui.KeyUp)
	if ld.refCursor == "l:main" || !strings.HasPrefix(ld.refCursor, "l:") || !slices.Equal(ld.filter.Branches, []string{"refs/heads/" + strings.TrimPrefix(ld.refCursor, "l:")}) {
		t.Fatalf("↑: cursor %q filter %v", ld.refCursor, ld.filter.Branches)
	}
	key(ui.KeyLeft)
	if ld.refCursor != "g:local" {
		t.Fatalf("←: cursor %q", ld.refCursor)
	}
	key(ui.KeyLeft)
	if ld.refsOpen["local"] || tt.HasText("本地分支 main") {
		t.Fatal("← did not fold the group")
	}
	// 回车在组上展开、再回车收起；只输了空格不算筛选，照样能收
	ld.refQuery = " "
	key(ui.KeyEnter)
	if !ld.refsOpen["local"] || !tt.HasText("本地分支 main") {
		t.Fatal("Enter did not open the group")
	}
	key(ui.KeyEnter)
	if ld.refsOpen["local"] || tt.HasText("本地分支 main") {
		t.Fatal("Enter did not fold the group")
	}
	// 筛选时各组都展开、收不起来：回车和点击都不改记下的展开状态，清掉筛选后还是收着的
	ld.refQuery = "ma"
	tt.Frame()
	if !tt.HasText("本地分支 main") {
		t.Fatal("filter did not open the group")
	}
	key(ui.KeyEnter)
	if err := tt.Click("本地"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if ld.refsOpen["local"] {
		t.Fatal("folding a group while filtering changed its saved state")
	}
	ld.refQuery = ""
	ld.refCursor = "g:local"
	tt.Frame()
	if tt.HasText("本地分支 main") {
		t.Fatal("group open after clearing the filter")
	}
	key(ui.KeyRight)
	key(ui.KeyRight)
	if !ld.refsOpen["local"] || !strings.HasPrefix(ld.refCursor, "l:") {
		t.Fatalf("→ →: open %v cursor %q", ld.refsOpen["local"], ld.refCursor)
	}
	until(t, tt, "loaded", func() bool { return !ld.loading })
	key(ui.KeyRight)
	ld.commit = nil
	key(ui.KeyDown)
	if ld.commit == nil || ld.commit.info.Hash != ld.commits[0].Hash {
		t.Fatal("→ on a branch did not move to the commit list")
	}
	// 提交列表里 ← 回到分支树
	key(ui.KeyLeft)
	before := ld.refCursor
	key(ui.KeyDown)
	if ld.refCursor == before {
		t.Fatal("← in the commit list did not go back to the ref tree")
	}
	// 分支上回车和单击一样：正只看它时回到全部分支，再回车又只看它
	until(t, tt, "loaded", func() bool { return !ld.loading })
	if len(ld.filter.Branches) != 1 {
		t.Fatalf("filter %v", ld.filter.Branches)
	}
	only := ld.filter.Branches[0]
	key(ui.KeyEnter)
	if len(ld.filter.Branches) != 0 {
		t.Fatalf("Enter on the viewed branch: filter %v", ld.filter.Branches)
	}
	key(ui.KeyEnter)
	if !slices.Equal(ld.filter.Branches, []string{only}) {
		t.Fatalf("Enter again: filter %v", ld.filter.Branches)
	}
}

// 分支弹窗：远端默认收起，点标题展开；搜索时全部展开。
func TestBranchPopupFolds(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
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
	v.ch.message = "pick"
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
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	until(t, tt, "first fetch", func() bool { _, ok := a.fetchAt[w.ProjectID]; return ok && trackOf(a, "w1").poll.at != time.Time{} })
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
	// 同一项目的另一个 worktree 开着改动页：fetch 期间它也被占住，那边的 git 操作排队
	wt := strings.TrimSpace(gitRun(t, root, "worktree", "list", "--porcelain"))
	wtPath := strings.TrimPrefix(strings.Split(strings.Split(wt, "\n\n")[1], "\n")[0], "worktree ")
	a.workspaces = append(a.workspaces, workspace{ID: "wt", ProjectID: w.ProjectID, Name: "agent", Kind: "worktree", Path: wtPath, Branch: "agent"})
	a.indexWorkspaces()
	w = a.workspace("w1")
	ov := a.openFilesPanel(a.workspace("wt"), sideChanges)
	a.selected = "wt"
	until(t, tt, "other repo", func() bool { return ov.git.repo != "" })
	a.selected = "w1"
	tt.Frame()
	a.fetchAt[w.ProjectID] = time.Time{} // 当作五分钟到了
	until(t, tt, "other busy", func() bool { return ov.git.busy == "Fetch" })
	until(t, tt, "fetched", func() bool { return trackOf(a, "w1").behind == 1 && len(d.comp.onlyOther) == 1 && ov.git.busy == "" })

	// 手动 Fetch 同样重读
	os.WriteFile(filepath.Join(other, "remote2.txt"), []byte("y\n"), 0o644)
	gitRun(t, other, "add", "remote2.txt")
	gitRun(t, other, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "remote 2")
	gitRun(t, other, "push", "-q", "origin", "main")
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	a.gitDo(locales.In("zh-Hans"), v.git, "Fetch", func(ctx context.Context, root string) error { return gitx.Fetch(ctx, root) })
	until(t, tt, "manual fetch", func() bool { return len(d.comp.onlyOther) == 2 })
}

// ⌘K：文件标签在前台时把焦点给提交信息，终端在前台时不注册（留给终端清屏）。
func TestCommitShortcut(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.activeTerm[w.ID] = w.Terminals[0].ID
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if v.ch.focusMsg {
		t.Fatal("⌘K in a terminal should not go to the commit box")
	}
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyK)
	tt.Frame()
	if !v.ch.focusMsg && !tt.Focused("提交信息") {
		t.Fatal("⌘K with a file in front should focus the commit message")
	}
}

// Git 日志浮层：盖在终端上、不改终端尺寸；隐藏按钮和 ⌘9 开关，文件历史可以关掉，整个仓库的日志留着。
func TestLogPanel(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	settle(tt)
	cols, rows := a.panes[0].term.Size()
	tt.Key(ui.Cmd, ui.Key9)
	v := a.files[w.ID]
	until(t, tt, "log", func() bool { return a.logPanelOpen() && v.logCur != nil && v.logCur.kind == docLog })
	settle(tt)
	if c2, r2 := a.panes[0].term.Size(); c2 != cols || r2 != rows {
		t.Fatalf("terminal resized under the panel: %dx%d -> %dx%d", cols, rows, c2, r2)
	}
	if a.filesActive(w) {
		t.Fatal("log opened as a tab")
	}
	openLogWait(t, a, tt, w, "main.go")
	until(t, tt, "history", func() bool { return v.logCur != nil && v.logCur.kind == docLog && tt.HasText("历史：main.go") })
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
	if a.logPanelOpen() || tt.HasText("隐藏 ⌘9") {
		t.Fatal("hide button did not hide the panel")
	}
	tt.Key(ui.Cmd, ui.Key9)
	settle(tt)
	if !a.logPanelOpen() || len(v.logs) != 1 {
		t.Fatalf("⌘9 reopen: open %v logs %d", a.logPanelOpen(), len(v.logs))
	}
}

// 回滚：改动页批量回滚（勾选、新文件只在勾了"删除新文件"时删）、diff 栏回滚整个文件后换到下一个、
// 编辑器改动标记的卡片回滚一处。
func TestRollback(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
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
	if r := &a.rollback; !r.open || !r.on["README.md"] || r.on["main.go"] {
		t.Fatalf("rollback dialog %v %+v", r.open, r.on)
	}
	tt.Frame()
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
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
	tt := newTester(a.build, 1200, 760)
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
	// 点组名也能切换；分段里也有"改动"两个字，按组标题前的勾选框找组名
	clickTitle := func() {
		r, ok := tt.Find("勾选 改动")
		if !ok {
			t.Fatalf("group checkbox missing: %v", tt.Texts())
		}
		tt.ClickAt(r.X+r.W+14, r.Y+r.H/2)
	}
	clickTitle()
	tt.Frame()
	if tt.HasText("README.md") || *v.git.groupOpen(groupChanged) {
		t.Fatal("changed group did not fold by its title")
	}
	clickTitle()
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
	tt := newTester(a.build, 1200, 760)
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
	tt.Frame()
	if r := &a.rollback; !r.open || !r.on["main.go"] || r.on["README.md"] || r.on["notes.txt"] {
		t.Fatalf("rollback dialog %v %+v", r.open, r.on)
	}
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, root, "show", "HEAD:main.go")
	until(t, tt, "reloaded", func() bool {
		return v.git.busy == "" && !d.isModified() && strings.TrimSpace(d.ed.Buffer.String()) == strings.TrimSpace(head)
	})
	until(t, tt, "button gone", func() bool { return !tt.HasText("回滚这个文件…") })

	a.openFile(w, filepath.Join(root, "notes.txt"), 0, [2]int{})
	n := v.cur
	until(t, tt, "notes", func() bool { return n.kind == docText && tt.HasText("回滚这个文件…") })
	a.rollbackDoc(v, n)
	tt.Frame()
	if err := tt.Click("回滚 1 个文件"); err != nil {
		t.Fatal(err)
	}
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	openLogWait(t, a, tt, w, "main.go")
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
	if !tt.HasText(first.Subject) || !tt.HasText(shortRev(locales.In("zh-Hans"), first.Hash)) {
		t.Fatalf("history view lacks the first commit: %v", tt.Texts())
	}
	if err := tt.Click(second.Subject); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "second", func() bool {
		hd := ld.hist.diff
		return ld.hist.sel == second.Hash && hd.diff.newRev == second.Hash && hd.kind == docDiff
	})
	// ↑↓ 换提交，右边跟着换 diff
	tt.Key(0, ui.KeyUp)
	until(t, tt, "↑", func() bool { return ld.hist.sel == first.Hash && ld.hist.diff.diff.newRev == first.Hash })
	tt.Key(0, ui.KeyDown)
	until(t, tt, "↓", func() bool {
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	v.git.busy, v.git.busyKind = "Fetch", gitFetch
	var ran []string
	for i, what := range []string{"检出", "提交"} {
		kind := []gitKind{"", gitCommit}[i]
		a.gitDoKind(locales.In("zh-Hans"), v.git, kind, what, func(ctx context.Context, root string) error {
			a.update(func() { ran = append(ran, what) })
			return nil
		}, nil)
	}
	if len(v.git.queue) != 2 || !v.git.pending(gitCommit) || v.git.busyText(locales.In("zh-Hans")) != "Fetch…，接着检出等 2 个" {
		t.Fatalf("queue %d %q", len(v.git.queue), v.git.busyText(locales.In("zh-Hans")))
	}
	v.git.busy, v.git.busyKind = "", ""
	a.gitNext(v.git)
	until(t, tt, "queued ops ran", func() bool { return len(ran) == 2 && v.git.busy == "" })
	if ran[0] != "检出" || ran[1] != "提交" {
		t.Fatalf("order %v", ran)
	}
}

// 终端里提交之后 HEAD 变了，开着的 diff 跟着重读，不再显示已经提交的改动。
func TestDiffFollowsHead(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], changeSpec("", gitx.Entry{Path: "main.go"}), nil, true)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], changeSpec("", gitx.Entry{Path: "README.md"}), nil, true)
	d := v.cur
	until(t, tt, "diff", func() bool { return d.kind == docDiff && len(d.diff.state.Hunks()) == 1 })
	path := filepath.Join(root, "README.md")
	b, _ := os.ReadFile(path)
	agent := string(b) + "agent 刚写的\n"
	os.WriteFile(path, []byte(agent), 0o644)
	a.revertHunk(locales.In("zh-Hans"), v, d, 0)
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
	tt := newTester(a.build, 1200, 760)
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
	// 左右对照时每处改动在分隔线旁有 >>
	settle(tt)
	if _, ok := tt.Find("回滚这处改动"); !ok {
		t.Fatal("no >> by the divider")
	}
	if !r.open || r.detail != d {
		t.Fatal("dialog should stay on the file")
	}
	// 到头了换到树里的下一个文件（当前已经是最后一处）
	tt.Key(0, ui.KeyF7)
	until(t, tt, "next file", func() bool { return r.detail.diff.rel == "notes.txt" && r.detail.kind == docDiff })
	// "包含在回滚中"就是文件树里的勾选
	if err := tt.Click("包含在回滚中"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !r.on["notes.txt"] {
		t.Fatal("include in rollback")
	}
	r.on["notes.txt"] = false
	// Esc 只关 diff 浮窗，回滚对话框留着
	tt.Key(0, ui.KeyEscape)
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
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	a.gitDoKind(locales.In("zh-Hans"), v.git, gitPush, "Push", func(ctx context.Context, root string) error {
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	ran := false
	a.gitDoKind(locales.In("zh-Hans"), v.git, gitCommit, "提交", func(ctx context.Context, root string) error { ran = true; return nil }, nil)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	w := &a.workspaces[0]
	a.openDiff(w, changeSpec("", gitx.Entry{Path: "main.go"}), nil, true)
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

// 太大的文件先不比较，写两边多大、多少行，点"仍要比较"在后台比；diff 不是磁盘上的文件，不给"用默认应用打开"。
func TestDiffLargeFile(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	line := strings.Repeat("x", 1023) + "\n"
	n := diffMaxSize/len(line) + 1
	os.WriteFile(filepath.Join(root, "big.lock"), []byte(strings.Repeat(line, n)), 0o644)
	a.openDiff(&a.workspaces[0], changeSpec("", gitx.Entry{Path: "big.lock", Untracked: true}), nil, true)
	d := v.cur
	until(t, tt, "too large", func() bool {
		return d.kind == docTooLarge && tt.HasText(fmt.Sprintf("文件超过 %d MB，比较要等一会儿", diffMaxSize>>20)) &&
			tt.HasText(fmt.Sprintf("改之前 0 B · 0 行，改之后 20.0 MB · %s 行", thousands(n)))
	})
	if tt.HasText("用默认应用打开") {
		t.Fatal("virtual diff doc offers to open diff: path")
	}
	if err := tt.Click("仍要比较"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "compared anyway", func() bool { return d.kind == docDiff && len(d.diff.state.Hunks()) == 1 })
	// 编辑器把末尾换行后面也算一行
	if added, _ := d.diff.state.Stats(); added < n {
		t.Fatalf("added %d, want %d", added, n)
	}
}

// 大文件在后台比较，界面线程拿算好的结果；切换忽略空白也在后台重比，结果和在界面线程上算的一样。
func TestDiffComparedAhead(t *testing.T) {
	var old, nw strings.Builder
	for i := range 40000 {
		fmt.Fprintf(&old, "  line %d of a lock file %s\n", i, strings.Repeat("y", 20))
		switch {
		case i%500 == 0:
			fmt.Fprintf(&nw, "  line %d changed %s\n", i, strings.Repeat("y", 20))
		case i%700 == 0:
			fmt.Fprintf(&nw, "\tline %d of a lock file %s\n", i, strings.Repeat("y", 20))
		default:
			fmt.Fprintf(&nw, "  line %d of a lock file %s\n", i, strings.Repeat("y", 20))
		}
	}
	if old.Len()+nw.Len() <= diffAheadSize {
		t.Fatal("texts too small to be compared ahead")
	}
	a := previewApp(t)
	root := gitWorkspace(t, a)
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(old.String()), 0o644)
	gitRun(t, root, "add", "big.txt")
	gitRun(t, root, "commit", "-q", "-m", "big")
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(nw.String()), 0o644)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openDiff(&a.workspaces[0], changeSpec("", gitx.Entry{Path: "big.txt", X: '.', Y: 'M'}), nil, true)
	d := v.cur
	want := func(ignore bool) []editor.DiffHunk {
		s := editor.DiffState{Old: editor.NewBuffer(old.String()), New: editor.NewBuffer(nw.String()), IgnoreWhitespace: ignore}
		return s.Hunks()
	}
	until(t, tt, "diff", func() bool { return d.kind == docDiff && !d.diff.comparing })
	if got := d.diff.state.Hunks(); !slices.Equal(got, want(true)) {
		t.Fatalf("hunks %d, want %d", len(got), len(want(true)))
	}
	a.toggleIgnoreSpace(d)
	if !d.diff.state.IgnoreWhitespace || !d.diff.comparing {
		t.Fatal("big file switched whitespace on the UI thread")
	}
	until(t, tt, "recompared", func() bool { return !d.diff.state.IgnoreWhitespace })
	if got := d.diff.state.Hunks(); !slices.Equal(got, want(false)) {
		t.Fatalf("hunks %d, want %d", len(got), len(want(false)))
	}
}

// 日志：分支和标签同名时按分支筛；作者框里的 "a[" 不报错；读出错时筛选栏还在、可以清除筛选；
// 按作者筛选时不画提交图。
func TestLogFilterEdges(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	gitRun(t, root, "tag", "feat", "main~2") // 和分支 feat 同名、指向更早的提交
	tt := newTester(a.build, 1200, 760)
	openLogWait(t, a, tt, &a.workspaces[0], "")
	v := a.files["w1"]
	d := v.logCur
	ld := d.log
	until(t, tt, "log", func() bool { return d.kind == docLog && !ld.loading && v.git.branches.list != nil })
	ld.filter.Branches = []string{"refs/heads/feat"}
	a.loadLog(v, d, false)
	until(t, tt, "branch feat", func() bool { return !ld.loading && len(ld.commits) > 0 })
	if ld.commits[0].Subject != "feat: 新功能" {
		t.Fatalf("branch feat shows %q", ld.commits[0].Subject)
	}
	ld.filter.Branches = nil
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
	ld.filter.Author, ld.filter.Branches = "", []string{"refs/heads/gone"}
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
	tt := newTester(a.build, 1200, 760)
	v := a.filesFor(w)
	until(t, tt, "repo", func() bool {
		a.refreshGit(v)
		return v.git.status != nil
	})
	ran := false
	a.guardPushed(locales.In("zh-Hans"), v.git.repo, gitRun(t, root, "rev-parse", "HEAD"), func() { ran = true })
	until(t, tt, "local commit", func() bool { return ran })
	if a.confirming {
		t.Fatal("unpushed commit should not ask")
	}
	ran = false
	a.guardPushed(locales.In("zh-Hans"), v.git.repo, gitRun(t, root, "rev-parse", "origin/main"), func() { ran = true })
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
	a.gitDo(locales.In("zh-Hans"), v.git, "Cherry-pick", func(ctx context.Context, root string) error { return gitx.CherryPick(ctx, root, side) })
	until(t, tt, "panel opened", func() bool {
		return v.git.status != nil && v.git.status.Operation == "cherry-pick" && a.filesPanelOpen() && v.side == sideChanges
	})
}

// 提交框：取消"修改上一次提交"时清掉自动填的旧信息；没写信息时提交按钮不可点；⌘K 在储藏分页上切回提交分页。
func TestCommitBoxDetails(t *testing.T) {
	a := previewApp(t)
	gitWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	g := v.git
	a.setAmend(v, true)
	until(t, tt, "last message", func() bool { return v.ch.message == "base" })
	a.setAmend(v, false)
	if v.ch.message != "" {
		t.Fatalf("message kept: %q", v.ch.message)
	}
	tt.Frame()
	if err := tt.Click("提交 2 个文件"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if g.busy != "" || a.err != "" {
		t.Fatalf("commit without message: busy %q err %q", g.busy, a.err)
	}
	v.ch.tab, v.ch.focusMsg = 1, true
	tt.Frame()
	if v.ch.tab != 0 || !tt.HasText("修改上一次提交") {
		t.Fatal("⌘K should switch back to the commit tab")
	}
}

// 分支弹窗：搜索后回车检出第一个对得上的；↑↓ 换选中。
func TestBranchPopupKeyboard(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	openLogWait(t, a, tt, &a.workspaces[0], "")
	v := a.files["w1"]
	ld := v.logCur.log
	until(t, tt, "log", func() bool { return v.logCur != nil && v.logCur.kind == docLog && len(ld.commits) > 2 })
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
	tt := newTester(func(c *ui.Context) {
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
	tt := newTester(a.build, 1200, 760)
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

// Git 操作记录：日志浮层里"操作记录"标签按操作列出界面执行的 git 命令（含操作里顺带的查询），
// 没看过的失败带红点；轮询的查询不记。
func TestGitConsole(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\n"), 0o644)
	a.traceGit()
	defer func() { gitx.Trace = nil }()
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.gitDo(locales.In("zh-Hans"), v.git, "加入 Git", func(ctx context.Context, root string) error { return gitx.Stage(ctx, root, "new.txt") })
	until(t, tt, "idle", func() bool { return v.git.busy == "" })
	a.gitDo(locales.In("zh-Hans"), v.git, "继续", func(ctx context.Context, root string) error { return gitx.Continue(ctx, root, "rebase") })
	until(t, tt, "failed", func() bool { return v.git.busy == "" && len(a.consoleErr) > 0 })
	// 检出不存在的修订，和不是 git 报出来的失败，也都记在各自的操作下面
	a.gitDo(locales.In("zh-Hans"), v.git, "检出", func(ctx context.Context, root string) error { return gitx.CheckoutRevision(ctx, root, "no-such-rev") })
	a.gitDo(locales.In("zh-Hans"), v.git, "合并提交", func(ctx context.Context, root string) error { return errors.New("范围里有合并提交") })
	until(t, tt, "queued ops", func() bool { return v.git.busy == "" && len(v.git.queue) == 0 })
	openLogWait(t, a, tt, w, "")
	until(t, tt, "console tab", func() bool { return tt.HasText("操作记录") && tt.HasText("提交日志") })
	if _, ok := tt.Find("有失败的 git 命令"); !ok {
		t.Fatal("console tab should flag the failed command")
	}
	settle(tt)
	if err := tt.Click("操作记录"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "console", func() bool { return v.console && tt.HasText("git rebase --continue") })
	until(t, tt, "all ops", func() bool { return tt.HasText("范围里有合并提交") })
	for _, want := range []string{"加入 Git", "继续", "检出", "失败", "合并提交", "git add -A -- ':(literal)new.txt'"} {
		if !tt.HasText(want) {
			t.Fatalf("console lacks %q: %v", want, tt.Texts())
		}
	}
	if len(a.consoleErr) > 0 {
		t.Fatal("opening the console should clear the red dot")
	}
	// 别的项目的命令不列在这个项目里
	a.addGitRun(gitx.Run{ID: 9999, Op: "别的项目提交", OpID: 9999, Dir: a.workspaces[3].Path, Args: []string{"commit"}, Start: time.Now(), Done: true})
	tt.Frame()
	if tt.HasText("别的项目提交") {
		t.Fatal("console shows another project's run")
	}
	// 右键命令：是"复制命令"，不是可选文字自带的编辑菜单
	if err := tt.RightClick("git rebase --continue"); err != nil {
		t.Fatal(err)
	}
	if !menuHas(tt, "复制命令") {
		t.Fatalf("context menu %v", tt.Texts())
	}
	chooseMenu(t, tt, "复制命令")
	tt.Frame()
	if got := tt.Clipboard(); got != "git rebase --continue" {
		t.Fatalf("copied %q", got)
	}
	for _, s := range tt.Texts() {
		if strings.Contains(s, "status") {
			t.Fatalf("polling queries should not be logged: %q", s)
		}
	}
	if err := tt.Click("提交日志"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.console {
		t.Fatal("clicking the log tab should leave the console")
	}
}

// 储藏（照 WebStorm）：顶上的按钮先问说明（可以留空）再 git stash；储藏标签里单击一条，"应用"留着它，"弹出"取回并删掉。
func TestStashTab(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	settle(tt)
	if err := tt.Click("储藏更改…（git stash，含未跟踪文件）"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.promptDlg == nil || !a.promptDlg.optional {
		t.Fatal("stash should ask for an optional message")
	}
	a.promptDlg.do("试一下储藏", false)
	a.promptDlg = nil
	until(t, tt, "stashed", func() bool { return v.git.busy == "" && len(v.git.status.Entries) == 0 })
	v.ch.tab = 1
	until(t, tt, "stash row", func() bool { return tt.HasText("试一下储藏") })
	apply := func(item string) {
		t.Helper()
		if err := tt.Click("On main: 试一下储藏"); err != nil {
			t.Fatal(err)
		}
		chooseMenu(t, tt, item)
	}
	apply("应用")
	until(t, tt, "applied", func() bool { return v.git.busy == "" && len(v.git.status.Entries) > 0 })
	if out := gitRun(t, root, "stash", "list"); !strings.Contains(out, "试一下储藏") {
		t.Fatalf("apply should keep the stash: %q", out)
	}
	gitRun(t, root, "checkout", "--", ".")
	gitRun(t, root, "clean", "-fdq")
	apply("弹出")
	until(t, tt, "popped", func() bool { return v.git.busy == "" && len(v.git.status.Entries) > 0 })
	if out := gitRun(t, root, "stash", "list"); strings.TrimSpace(out) != "" {
		t.Fatalf("pop should drop the stash: %q", out)
	}
}

// 分支比较上下两栏默认各一半，拖中间的分隔条调比例，松手写进 ui.json。
func TestCompareSplit(t *testing.T) {
	a := previewApp(t)
	branchWorkspace(t, a)
	tt := newTester(a.build, 1200, 760)
	w := &a.workspaces[0]
	v := a.filesFor(w)
	a.loadBranches(v.git)
	until(t, tt, "repo", func() bool { return v.git.repo != "" })
	a.openCompare(w, v, "feat", "main", false)
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare && v.cur.comp.onlyCur != nil })
	top, ok1 := tt.Find("存在于")
	if !ok1 {
		t.Fatal("no pane title")
	}
	var heads []float32
	for _, s := range []string{"feat: 新功能", "fix: 本地修复"} {
		r, ok := tt.Find(s)
		if !ok {
			t.Fatalf("no %q", s)
		}
		heads = append(heads, r.Y)
	}
	// 两栏一样高：下一栏的提交离上一栏的提交差不多半个区域（窗口 760 高）
	gap := heads[1] - heads[0]
	if gap < 250 {
		t.Fatalf("panes not split evenly: first commit at %v, second at %v (title %v)", heads[0], heads[1], top)
	}
	// 分隔条在下一栏标题上方
	// 下一栏标题行和其中搜索框的文字同一条中线，标题行再往上半个分隔条（5 高）是分隔条的中间
	field, ok := tt.Find("在 main 独有的提交里按信息、哈希或作者筛选")
	if !ok {
		t.Fatal("no search field in the lower pane")
	}
	y := field.Y + field.H/2 - sz(42)/2 - 2.5
	tt.Move(600, y)
	tt.Frame()
	if tt.Cursor() != ui.CursorResizeNS {
		t.Fatalf("cursor at grip %v (y %v, heads %v)", tt.Cursor(), y, heads)
	}
	tt.Press(600, y)
	for i := 1; i <= 4; i++ {
		tt.Move(600, y-float32(i)*25)
		tt.Frame()
	}
	tt.Release(600, y-100)
	tt.Frame()
	if s := a.state.CompareSplit; s >= 0.45 || s <= 0.2 {
		t.Fatalf("split %v after dragging up 100", s)
	}
	if a.resizingCompare {
		t.Fatal("still resizing after release")
	}
	r, _ := tt.Find("fix: 本地修复")
	if d := heads[1] - r.Y; d < 95 || d > 105 {
		t.Fatalf("lower pane moved %v, want 100", d)
	}
}

// 操作记录写盘：完成的命令追加到 git-console.jsonl，下次打开读回、编号接着往下；
// 终端里的命令按时间排进来，之后到的失败标红点；清空后两边更早的都不再显示
func TestGitConsolePersist(t *testing.T) {
	home := t.TempDir()
	a := &app{home: home}
	a.traceGit()
	defer func() { gitx.Trace = nil }()
	t0 := time.Now().Add(-time.Minute)
	a.addGitRun(gitx.Run{ID: 1, Op: "提交", OpID: 1, Dir: "/r", Args: []string{"commit", "-m", "x"}, Start: t0, Done: true})
	a.addGitRun(gitx.Run{ID: 2, Op: "推送", OpID: 2, Dir: "/r", Args: []string{"push"}, Start: t0.Add(20 * time.Second)})
	until := time.Now().Add(2 * time.Second)
	for time.Now().Before(until) {
		if b, _ := os.ReadFile(gitConsolePath(home)); strings.Count(string(b), "\n") == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	b := &app{home: home}
	b.loadGitRuns()
	if len(b.gitRuns) != 1 || b.gitRuns[0].Args[0] != "commit" {
		t.Fatalf("reloaded %+v", b.gitRuns) // 没完成的推送不写盘
	}
	b.addTermRuns([]termGitRun{
		{Seq: 7, Agent: "claude", Dir: "/r", Args: []string{"add", "."}, Start: t0.Add(-time.Second), TookMs: 5},
		{Seq: 8, Dir: "/r", Args: []string{"pull"}, Start: t0.Add(10 * time.Second), Code: 1},
	}, true)
	if len(b.gitRuns) != 3 || b.gitRuns[0].ID != -7 || b.gitRuns[0].Op != termRunOp+"claude" || b.gitRuns[2].Err != "exit code 1" || len(b.consoleErr) > 0 {
		t.Fatalf("merged %+v err=%v", b.gitRuns, b.consoleErr)
	}
	b.addTermRuns([]termGitRun{{Seq: 9, Args: []string{"push"}, Start: time.Now(), Code: 128}, {Seq: 8, Args: []string{"pull"}, Start: t0}}, false)
	if len(b.gitRuns) != 4 || len(b.consoleErr) == 0 {
		t.Fatalf("later failure %d %v", len(b.gitRuns), b.consoleErr)
	}

	b.state.GitConsoleCleared = time.Now()
	b.addTermRuns([]termGitRun{{Seq: 3, Args: []string{"commit"}, Start: t0}}, false)
	if len(b.gitRuns) != 4 {
		t.Fatalf("a run older than the clear was added: %d", len(b.gitRuns))
	}
	c := &app{home: home, state: uiState{GitConsoleCleared: b.state.GitConsoleCleared}}
	c.loadGitRuns()
	if len(c.gitRuns) != 0 {
		t.Fatalf("cleared runs came back: %+v", c.gitRuns)
	}
}

// 操作记录按项目分：命令归到目录最深的工作区所在项目（项目目录里套着别的项目），worktree 算它的项目；
// 红点和清空也只管各自的项目
func TestGitConsoleByProject(t *testing.T) {
	a := &app{workspaces: []workspace{
		{ID: "w1", ProjectID: "outer", Kind: "main", Path: "/x/Apps"},
		{ID: "w2", ProjectID: "inner", Kind: "main", Path: "/x/Apps/posse"},
		{ID: "w3", ProjectID: "inner", Kind: "worktree", Name: "feat", Path: "/h/worktrees/posse/feat"},
	}}
	t0 := time.Now().Add(-time.Minute)
	a.addGitRun(gitx.Run{ID: 1, Dir: "/x/Apps", Args: []string{"fetch"}, Start: t0, Done: true})
	a.addGitRun(gitx.Run{ID: 2, Dir: "/x/Apps/posse/internal", Args: []string{"commit"}, Start: t0, Done: true, Err: "退出码 1"})
	a.addGitRun(gitx.Run{ID: 3, Dir: "/h/worktrees/posse/feat", Args: []string{"push"}, Start: t0, Done: true})
	a.addGitRun(gitx.Run{ID: 4, Dir: "/x/Apps-old", Args: []string{"push"}, Start: t0, Done: true})
	ids := func(p string) (out []int64) {
		for _, r := range a.projectRuns(p) {
			out = append(out, r.ID)
		}
		return
	}
	if got := ids("outer"); !slices.Equal(got, []int64{1}) {
		t.Fatalf("outer %v", got)
	}
	if got := ids("inner"); !slices.Equal(got, []int64{2, 3}) {
		t.Fatalf("inner %v", got)
	}
	if a.runWhere("/h/worktrees/posse/feat") != "feat" || a.runWhere("/x/Apps/posse") != "" {
		t.Fatal("runWhere")
	}
	if a.projectConsoleErr("outer", true) || !a.projectConsoleErr("inner", true) || a.projectConsoleErr("inner", false) {
		t.Fatal("red dot should follow the project")
	}
	a.state.GitConsoleClearedAt = map[string]time.Time{"inner": time.Now()}
	if len(ids("inner")) != 0 || len(ids("outer")) != 1 {
		t.Fatal("clearing one project hid another's runs")
	}
}

// 日志的时间写到分钟：今天、昨天写"今天 14:26"，今年写"10月3日 09:12"，跨年的只写日期；悬停是到秒的完整时间。
func TestCommitTime(t *testing.T) {
	zh := locales.In("zh-Hans")
	now := time.Now()
	at := func(d time.Time, h, m int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), h, m, 5, 0, time.Local)
	}
	if got := commitTime(zh, at(now, 0, 1)); got != "今天 00:01" {
		t.Errorf("today %q", got)
	}
	if got := commitTime(zh, at(now.AddDate(0, 0, -1), 23, 59)); got != "昨天 23:59" {
		t.Errorf("yesterday %q", got)
	}
	old := time.Date(now.Year()-1, 12, 30, 9, 2, 0, 0, time.Local)
	if got := commitTime(zh, old); got != fmt.Sprintf("%d-12-30", now.Year()-1) {
		t.Errorf("last year %q", got)
	}
	if now.YearDay() > 3 {
		d := time.Date(now.Year(), 1, 1, 9, 2, 0, 0, time.Local)
		if got := commitTime(zh, d); got != "1月1日 09:02" {
			t.Errorf("this year %q", got)
		}
	}
	if got := commitStamp(zh, time.Date(2026, 10, 14, 17, 17, 5, 0, time.Local)); got != "2026-10-14 17:17:05 周三" {
		t.Errorf("stamp %q", got)
	}
}

// 终端里的 git 命令目录来自 trace2，是解析过符号链接的（macOS 的 /var 是 /private/var），
// 也归到用原路径登记的工作区
func TestRunWorkspaceRealPath(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || real == dir {
		t.Skipf("temp dir %s has no symlink (%v)", dir, err)
	}
	a := &app{workspaces: []workspace{{ID: "w1", ProjectID: "p1", Kind: "main", Path: dir}}}
	a.addTermRuns([]termGitRun{{Seq: 1, Dir: filepath.Join(real, "sub"), Args: []string{"commit"}, Start: time.Now()}}, true)
	if got := a.projectRuns("p1"); len(got) != 1 {
		t.Fatalf("run under the real path not attributed: %+v", got)
	}
}

// 操作记录每个项目各留 gitConsoleMax 条：一个项目命令多，不挤掉别的项目的；对不上工作区的按目录算。
// 文件攒到两倍时按留着的重写，读回来一样
func TestGitConsolePerProject(t *testing.T) {
	home := t.TempDir()
	ws := []workspace{
		{ID: "w1", ProjectID: "busy", Kind: "main", Path: "/x/busy"},
		{ID: "w2", ProjectID: "quiet", Kind: "main", Path: "/x/quiet"},
	}
	a := &app{home: home, workspaces: ws}
	t0 := time.Now().Add(-time.Hour)
	id := int64(0)
	add := func(dir string) {
		id++
		a.addGitRun(gitx.Run{ID: id, OpID: id, Dir: dir, Args: []string{"fetch"}, Start: t0.Add(time.Duration(id) * time.Second), Done: true})
	}
	add("/x/quiet")
	add("/elsewhere")
	for range 2*gitConsoleMax + 10 {
		add("/x/busy/sub")
	}
	if got := len(a.projectRuns("busy")); got != gitConsoleMax {
		t.Fatalf("busy kept %d", got)
	}
	if len(a.projectRuns("quiet")) != 1 || len(a.gitRuns) != gitConsoleMax+2 {
		t.Fatalf("quiet %d, total %d", len(a.projectRuns("quiet")), len(a.gitRuns))
	}
	if last := a.gitRuns[len(a.gitRuns)-1]; last.ID != id {
		t.Fatalf("newest dropped: %d", last.ID)
	}
	// 等写文件的协程做完，文件已经重写过（行数不到两倍上限）
	var lines int
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(gitConsolePath(home))
		if lines = strings.Count(string(b), "\n"); lines < 2*gitConsoleMax && lines >= gitConsoleMax+2 {
			break
		}
	}
	if lines >= 2*gitConsoleMax {
		t.Fatalf("file not rewritten: %d lines", lines)
	}
	b := &app{home: home, workspaces: ws}
	b.loadGitRuns()
	b.trimGitRuns()
	if len(b.projectRuns("busy")) != gitConsoleMax || len(b.projectRuns("quiet")) != 1 {
		t.Fatalf("reloaded busy %d quiet %d", len(b.projectRuns("busy")), len(b.projectRuns("quiet")))
	}
}

// 连着几次相同、成功又没有输出的操作合成一条；有输出、失败或中间隔着别的操作时不合。
func TestFoldGitOps(t *testing.T) {
	at := time.Date(2026, 10, 18, 14, 0, 0, 0, time.Local)
	fetch := func(id int64, min int, out, err string) gitx.Run {
		return gitx.Run{ID: id, OpID: id, Op: "自动 Fetch", Dir: "/r", Args: []string{"fetch", "--all"}, Start: at.Add(time.Duration(min) * time.Minute), Done: true, Out: out, Err: err}
	}
	runs := []gitx.Run{
		fetch(1, 0, "", ""), fetch(2, 5, "", ""), fetch(3, 10, "", ""),
		fetch(4, 15, "From x\n   a..b  main -> origin/main", ""),
		fetch(5, 20, "", ""), fetch(6, 25, "", "network down"), fetch(7, 30, "", ""),
	}
	ops := foldGitOps(groupGitRuns(runs))
	var got []string
	for _, o := range ops {
		got = append(got, fmt.Sprintf("%d×%d@%s", o.runs[0].ID, o.repeat, o.since.Format("15:04")))
	}
	want := []string{"3×3@14:00", "4×1@14:15", "5×1@14:20", "6×1@14:25", "7×1@14:30"}
	if !slices.Equal(got, want) {
		t.Fatalf("folded %v, want %v", got, want)
	}
}

// 比较页的 Cherry-pick 把另一边独有的提交 cherry-pick 到检出的分支上；选中检出分支自己的提交时不可点。
func TestComparePick(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	v := a.filesFor(w)
	tt := newTester(a.build, 1200, 760)
	a.loadBranches(v.git)
	until(t, tt, "repo", func() bool { return v.git.repo != "" })
	a.openCompare(w, v, "feat", "main", false)
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare })
	cd := v.cur.comp

	a.selectCommit(v.git.repo, &cd.commit, cd.onlyCur[0])
	tt.Frame()
	if err := tt.Click("Cherry-pick"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.confirming || v.git.busy != "" {
		t.Fatal("picked a commit already on the checked-out branch")
	}

	a.selectCommit(v.git.repo, &cd.commit, cd.onlyOther[0])
	tt.Frame()
	if err := tt.Click("Cherry-pick"); err != nil {
		t.Fatal(err)
	}
	// 预览数据里有 agent 在这个工作区工作，先确认
	until(t, tt, "agent guard", func() bool { return a.confirming && tt.HasText("仍然Cherry-pick") })
	if err := tt.Click("仍然Cherry-pick"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "cherry-pick", func() bool {
		_, err := os.Stat(filepath.Join(root, "feat.txt"))
		return err == nil && v.git.busy == ""
	})
	if a.err != "" {
		t.Fatal(a.err)
	}
	if got := strings.TrimSpace(gitRun(t, root, "log", "-1", "--format=%s", "main")); got != "feat: 新功能" {
		t.Fatalf("main head %q", got)
	}
}

// 比较页每栏的搜索只筛这一栏显示的提交；⌘ 点多选、⇧ 点选一段，Cherry-pick 按提交先后应用选中的几个。
func TestComparePickMany(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	gitRun(t, root, "switch", "-q", "feat")
	for _, n := range []string{"a", "b", "c"} {
		os.WriteFile(filepath.Join(root, n+".txt"), []byte(n+"\n"), 0o644)
		gitRun(t, root, "add", n+".txt")
		gitRun(t, root, "commit", "-q", "-m", "feat: 加 "+n)
	}
	gitRun(t, root, "switch", "-q", "main")
	w := &a.workspaces[0]
	v := a.filesFor(w)
	tt := newTester(a.build, 1200, 760)
	a.loadBranches(v.git)
	until(t, tt, "repo", func() bool { return v.git.repo != "" })
	a.openCompare(w, v, "feat", "main", false)
	until(t, tt, "compare", func() bool { return v.cur.kind == docCompare && len(v.cur.comp.onlyOther) == 4 })
	cd := v.cur.comp

	// 两栏各搜各的：上一栏的搜索词不筛下一栏
	cd.queryOther = "加 B"
	tt.Frame()
	if !tt.HasText("1 / 4 个") || tt.HasText("feat: 加 a") || !tt.HasText("feat: 加 b") || !tt.HasText("fix: 本地修复") {
		t.Fatalf("search: %v", tt.Texts())
	}
	cd.queryOther = ""
	tt.Frame()

	// 选中的 a 被搜索藏起来后 ⌘ 点 c，仍是同一栏的多选
	if err := tt.Click("feat: 加 a"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	cd.queryOther = "加 c"
	tt.Frame()
	if err := tt.ClickWith(ui.Cmd, "feat: 加 c"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(cd.selected()) != 2 {
		t.Fatalf("cmd-click with the first pick hidden left %d", len(cd.selected()))
	}
	cd.queryOther = ""
	tt.Frame()

	// 选 a，⇧ 点 c 选上 a..c，再 ⌘ 点去掉 b
	if err := tt.Click("feat: 加 a"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ClickWith(ui.Shift, "feat: 加 c"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(cd.selected()) != 3 {
		t.Fatalf("shift range picked %d", len(cd.selected()))
	}
	if err := tt.ClickWith(ui.Cmd, "feat: 加 b"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(cd.selected()) != 2 {
		t.Fatalf("cmd toggle left %d", len(cd.selected()))
	}
	if err := tt.Click("Cherry-pick"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "agent guard", func() bool { return a.confirming && tt.HasText("仍然Cherry-pick") })
	if err := tt.Click("仍然Cherry-pick"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "cherry-pick", func() bool {
		return v.git.busy == "" && strings.Contains(gitRun(t, root, "log", "-1", "--format=%s", "main"), "加")
	})
	if a.err != "" {
		t.Fatal(a.err)
	}
	if got := strings.TrimSpace(gitRun(t, root, "log", "-2", "--format=%s", "main")); got != "feat: 加 c\nfeat: 加 a" {
		t.Fatalf("main log %q", got)
	}
}

// 文件树照 WebStorm 给有改动的文件和含改动的目录上色，悬停文件名说明状态。
func TestFileTreeGitMarks(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	os.MkdirAll(filepath.Join(root, "pkg", "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "pkg", "sub", "new.go"), []byte("package sub\n"), 0o644)
	gitRun(t, root, "add", "pkg")
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(&a.workspaces[0], sideFiles)
	until(t, tt, "git status", func() bool { return v.git.status != nil && tt.HasText("main.go") })
	m := v.git.treeMarks()
	want := map[string]byte{"main.go": 'M', "notes.txt": '?', "pkg": 'M', "pkg/sub": 'M', "pkg/sub/new.go": 'A'}
	for rel, mark := range want {
		if got := m[filepath.Join(root, rel)]; got != mark {
			t.Errorf("%s marked %q, want %q", rel, got, mark)
		}
	}
	if _, ok := m[root]; ok {
		t.Error("workspace root should not be marked")
	}
	tt.Move(0, 0)
	if r, ok := tt.Find("main.go"); ok {
		tt.Move(r.X+r.W/2, r.Y+r.H/2)
		pass(tt, time.Second)
		tt.Frame()
	}
	if !tt.HasText("已修改") {
		t.Fatalf("no status tooltip: %v", tt.Texts())
	}
}

// 嵌套仓库（nested/）是树里的一项，不是 nested 目录里的一项。
func TestChangeTreeNestedRepo(t *testing.T) {
	rows := changeTree([]string{"nested/", "lib/a.go"}, nil)
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%v:%s:%d", r.dir, r.name, r.depth))
	}
	want := []string{"true:lib:0", "false:a.go:1", "false:nested:0"}
	if !slices.Equal(got, want) {
		t.Errorf("rows = %v, want %v", got, want)
	}
}

// trackOf 是工作区当前仓库的传入传出；还没算时给空的。
func trackOf(a *app, wsID string) *trackInfo {
	w := a.workspace(wsID)
	if v := a.files[wsID]; w != nil && v != nil {
		if tr := a.track[trackKey(w, v.git.repo)]; tr != nil {
			return tr
		}
	}
	return &trackInfo{}
}

// openLogWait 打开日志或文件历史，等找完仓库、日志标签出来。
func openLogWait(t *testing.T, a *app, tt *ui.Tester, w *workspace, path string) {
	t.Helper()
	a.openLog(w, "", path)
	until(t, tt, "log open", func() bool {
		v := a.files[w.ID]
		return v != nil && v.logCur != nil && v.logCur.log.filter.Path == path
	})
}
