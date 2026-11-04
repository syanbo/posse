package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/client"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

// multiRepoWorkspace 是一个本身不是仓库的工作区：a 是子目录里的仓库（里面嵌着一个未跟踪的仓库 nested），
// ext 是软链过去的仓库（检出 feat），notes 是普通目录。a/main.go 和 ext/lib.go 各有一处改动。
func multiRepoWorkspace(t *testing.T, a *app) (root string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "ws")
	ext := filepath.Join(base, "ext")
	put := func(p, text string) {
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	initRepo := func(dir, file string) {
		put(filepath.Join(dir, file), "one\ntwo\n")
		gitRun(t, dir, "init", "-q", "-b", "main")
		gitRun(t, dir, "config", "user.email", "t@example.com")
		gitRun(t, dir, "config", "user.name", "t")
		gitRun(t, dir, "add", ".")
		gitRun(t, dir, "commit", "-q", "-m", "base "+file)
	}
	initRepo(filepath.Join(root, "a"), "main.go")
	initRepo(filepath.Join(root, "a", "nested"), "n.go")
	initRepo(ext, "lib.go")
	gitRun(t, ext, "checkout", "-q", "-b", "feat")
	if err := os.Symlink(ext, filepath.Join(root, "ext")); err != nil {
		t.Fatal(err)
	}
	put(filepath.Join(root, "notes", "todo.md"), "x\n")
	put(filepath.Join(root, "a", "main.go"), "one\nTWO\n")
	put(filepath.Join(ext, "lib.go"), "one\ntwo\nthree\n")
	for i := range a.workspaces[:2] {
		a.workspaces[i].Path, a.workspaces[i].Branch = root, ""
	}
	a.projects[0].SingleRepo = false
	a.indexWorkspaces()
	return root
}

func TestMultiRepoWorkspace(t *testing.T) {
	a := previewApp(t)
	root := multiRepoWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	// 文件栏没开过：标签栏先去找仓库，分支按钮写第一个仓库的分支
	until(t, tt, "branch button", func() bool { return a.gitBranch(w) == "main" })
	v := a.openFilesPanel(w, sideChanges)
	repoA, repoN, repoExt := filepath.Join(root, "a"), filepath.Join(root, "a", "nested"), filepath.Join(root, "ext")
	var roots []string
	for _, g := range v.repos.list {
		roots = append(roots, g.repo)
	}
	if !slices.Equal(roots, []string{repoA, repoN, repoExt}) {
		t.Fatalf("repos %v", roots)
	}
	if v.git.repo != repoA {
		t.Fatalf("current %q", v.git.repo)
	}
	until(t, tt, "status", func() bool {
		return v.git.status != nil && v.repoAt(repoExt).status != nil && v.repoAt(repoN).status != nil
	})
	// 嵌套仓库不再是外层的未跟踪目录
	for _, e := range v.git.status.Entries {
		if e.Path != "main.go" {
			t.Fatalf("outer repo lists %q", e.Path)
		}
	}
	// 文件树：各仓库的改动都标出来（软链的写法）
	marks := v.treeMarks()
	if marks[filepath.Join(repoA, "main.go")] != 'M' || marks[filepath.Join(repoExt, "lib.go")] != 'M' || marks[repoExt] != 'M' {
		t.Fatalf("marks %v", marks)
	}

	// 打开软链过去的仓库里的文件：当前仓库跟过去，分支按钮、行号旁的改动都是它的
	a.openFile(w, filepath.Join(repoExt, "lib.go"), 0, [2]int{})
	until(t, tt, "follow ext", func() bool { return v.git.repo == repoExt && a.gitBranch(w) == "feat" })
	doc := v.cur
	until(t, tt, "diff base", func() bool { return doc.kind == docText && doc.ed.DiffBase != nil })
	if got := doc.headBuf.String(); got != "one\ntwo\n" {
		t.Fatalf("head %q", got)
	}

	// 它的 diff 记着自己的仓库：再切到 a 的文件，diff 照样对着 ext
	e := v.git.entry(filepath.Join(repoExt, "lib.go"))
	if e == nil {
		t.Fatal("no entry for lib.go")
	}
	a.openDiff(w, changeSpec(v.git.repo, *e), nil, true)
	diff := v.cur
	until(t, tt, "ext diff", func() bool { return diff.kind == docDiff && len(diff.diff.state.Hunks()) == 1 })
	a.openFile(w, filepath.Join(repoA, "main.go"), 0, [2]int{})
	until(t, tt, "follow a", func() bool { return v.git.repo == repoA })
	if diff.diff.root != repoExt {
		t.Fatalf("diff root %q", diff.diff.root)
	}
	v.cur = diff
	until(t, tt, "follow diff", func() bool { return v.git.repo == repoExt })

	// 文件历史对着文件所在的仓库；整个仓库的日志跟着当前仓库
	g, rel := v.relIn(filepath.Join(repoA, "main.go"))
	a.openLogIn(w, g, "", rel)
	hist := v.logCur
	until(t, tt, "history", func() bool { return hist.kind == docLog && len(hist.log.commits) == 1 })
	if hist.log.root != repoA || hist.log.commits[0].Subject != "base main.go" {
		t.Fatalf("history %q %+v", hist.log.root, hist.log.commits)
	}
	openLogWait(t, a, tt, w, "")
	main := v.logCur
	until(t, tt, "ext log", func() bool { return main.kind == docLog && len(main.log.commits) == 1 })
	if main.log.root != repoExt || main.log.commits[0].Subject != "base lib.go" {
		t.Fatalf("log %q %+v", main.log.root, main.log.commits)
	}
	v.cur = nil
	a.useRepo(v, v.repoAt(repoA))
	until(t, tt, "log follows", func() bool {
		return main.log.root == repoA && len(main.log.commits) == 1 && main.log.commits[0].Subject == "base main.go"
	})

	// 在不是当前仓库的仓库里执行操作：在它自己的目录里执行，读的也是它的状态
	ext := v.repoAt(repoExt)
	a.gitDo(locales.In("zh-Hans"), ext, "提交", func(ctx context.Context, root string) error {
		return gitx.CommitPaths(ctx, root, "ext change", []string{"lib.go"}, nil, false)
	})
	until(t, tt, "ext committed", func() bool { return ext.busy == "" && ext.status != nil && len(ext.status.Entries) == 0 })
	if msg := gitRun(t, repoExt, "log", "-1", "--format=%s"); msg != "ext change" {
		t.Fatalf("ext head %q", msg)
	}
	if v.git.changeCount() != 1 {
		t.Fatal("current repo changed too")
	}

	// 文件清单：各仓库的文件（软链的写法）和普通目录的文件，嵌套仓库的文件只算一次
	a.refreshList(v, true, nil)
	until(t, tt, "list", func() bool { return v.list != nil && v.list.Git })
	for _, f := range []string{"a/main.go", "a/nested/n.go", "ext/lib.go", "notes/todo.md"} {
		if !slices.Contains(v.list.Files, filepath.FromSlash(f)) {
			t.Fatalf("list %v lacks %s", v.list.Files, f)
		}
	}

	// 软链删掉：重新找仓库后它没了，记着它的操作报错，不默默丢掉
	os.Remove(filepath.Join(root, "ext"))
	a.rescanRepos(v)
	until(t, tt, "ext gone", func() bool { return len(v.repos.list) == 2 && v.git.repo != repoExt })
	a.err = ""
	a.gitDo(locales.In("zh-Hans"), ext, "提交", func(ctx context.Context, root string) error { return nil })
	if a.err == "" {
		t.Fatal("operation on removed repo not reported")
	}
}

// 项目关掉"识别多个 Git 仓库"：只认工作区所在的那一个仓库，打开回来重新找。
func TestSingleRepoSwitch(t *testing.T) {
	a := previewApp(t)
	root := multiRepoWorkspace(t, a)
	repoA := filepath.Join(root, "a")
	for i := range a.workspaces[:2] {
		a.workspaces[i].Path = repoA
	}
	a.indexWorkspaces()
	a.projects[0].SingleRepo = true
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "single", func() bool { return v.repos.scanned && v.git.status != nil })
	if len(v.repos.list) != 1 || v.git.repo != repoA {
		t.Fatalf("repos %d current %q", len(v.repos.list), v.git.repo)
	}
	// 只有一个仓库时嵌套的仓库照旧是外层的未跟踪目录
	if !slices.ContainsFunc(v.git.status.Entries, func(e gitx.Entry) bool { return e.Path == "nested/" }) {
		t.Fatalf("entries %+v", v.git.status.Entries)
	}
	a.projects[0].SingleRepo = false
	until(t, tt, "multi", func() bool { return len(v.repos.list) == 2 && !v.repos.single })
}

// 文件栏关着时改开关：标签栏的分支按钮也跟着重新找仓库。
func TestSingleRepoSwitchPanelClosed(t *testing.T) {
	a := previewApp(t)
	multiRepoWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	until(t, tt, "multi", func() bool { return a.gitBranch(w) == "main" })
	if a.filesPanelOpen() {
		t.Fatal("files panel should be closed")
	}
	a.projects[0].SingleRepo = true
	until(t, tt, "single", func() bool { return a.gitBranch(w) == "" })
	a.projects[0].SingleRepo = false
	until(t, tt, "multi again", func() bool { return a.gitBranch(w) == "main" })
}

// 多个仓库的改动页：按仓库分组，一条提交信息在各仓库分别提交；有一个仓库失败时报错、留着信息和它的勾选。
func TestMultiRepoCommit(t *testing.T) {
	a := previewApp(t)
	root := multiRepoWorkspace(t, a)
	repoA, repoExt := filepath.Join(root, "a"), filepath.Join(root, "ext")
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "status", func() bool {
		return a.filesPanelShown == 1 && v.repoAt(repoA) != nil && v.repoAt(repoA).status != nil && v.repoAt(repoExt).status != nil
	})
	gA, gExt := v.repoAt(repoA), v.repoAt(repoExt)
	tt.Frame()
	// 只列有改动的仓库：nested 没改动，不出标题
	for _, want := range []string{"改动 2", "main.go", "lib.go", "feat", "提交 2 个文件到 2 个仓库"} {
		if !tt.HasText(want) {
			t.Fatalf("changes view lacks %q: %v", want, tt.Texts())
		}
	}
	if tt.HasText("a/nested") {
		t.Fatal("clean nested repo listed")
	}
	// 上一次提交是一个仓库的：勾了两个仓库时不能改
	if v.amendRepo() != nil {
		t.Fatal("amend allowed across repos")
	}

	// a 里再有一个已跟踪的改动，用户取消勾选：别的仓库提交失败、再点一次时它也不能被带上
	write := func(p, body string) {
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(repoA, "extra.go"), "package a\n")
	gitRun(t, repoA, "add", "extra.go")
	gitRun(t, repoA, "commit", "-q", "-m", "extra")
	write(filepath.Join(repoA, "extra.go"), "package a // changed\n")
	a.refreshRepo(gA)
	until(t, tt, "extra", func() bool { return gA.entry(filepath.Join(repoA, "extra.go")) != nil })
	extra := func() gitx.Entry { return *gA.entry(filepath.Join(repoA, "extra.go")) }
	gA.setPicked(extra(), false)

	// 两个仓库都被钩子拒掉：两条报错都留着，不是后到的盖掉先到的
	hookA, hook := filepath.Join(repoA, ".git", "hooks", "pre-commit"), filepath.Join(repoExt, ".git", "hooks", "pre-commit")
	for _, h := range []string{hookA, hook} {
		if err := os.WriteFile(h, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	v.ch.message = "both"
	tt.Frame()
	if err := tt.Click("提交 2 个文件到 2 个仓库"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "both failed", func() bool {
		return gA.busy == "" && gExt.busy == "" && strings.Contains(a.err, "在 a 提交失败") && strings.Contains(a.err, "在 ext 提交失败")
	})
	if v.ch.message != "both" {
		t.Fatalf("message %q", v.ch.message)
	}

	// 只有 ext 被拒：a 提交成功，信息留着，ext 的勾选留着
	os.Remove(hookA)
	a.err = ""
	tt.Frame()
	if err := tt.Click("提交 2 个文件到 2 个仓库"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "partial commit", func() bool {
		return gA.busy == "" && gExt.busy == "" && a.err != "" && gA.status != nil && len(gA.status.Entries) == 1
	})
	if gA.picked(extra()) {
		t.Fatal("unchecked extra.go picked again after a partial commit")
	}
	if msg := gitRun(t, repoA, "log", "-1", "--format=%s"); msg != "both" {
		t.Fatalf("a head %q", msg)
	}
	if v.ch.message != "both" || gExt.changeCount() != 1 {
		t.Fatalf("message %q ext changes %d", v.ch.message, gExt.changeCount())
	}
	// 去掉钩子再点一次：只剩 ext，提交后清空信息
	os.Remove(hook)
	a.err = ""
	tt.Frame()
	if err := tt.Click("提交 1 个文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "ext committed", func() bool {
		if a.err != "" {
			t.Fatalf("err %q", a.err)
		}
		return gExt.busy == "" && gExt.status != nil && len(gExt.status.Entries) == 0
	})
	if msg := gitRun(t, repoExt, "log", "-1", "--format=%s"); msg != "both" || v.ch.message != "" || a.err != "" {
		t.Fatalf("ext head %q message %q err %q", msg, v.ch.message, a.err)
	}
}

// 手动换的仓库不会被当前标签换回去；分支按钮写上仓库名；日志的"仓库"组能换仓库；终端里在软链仓库的真实路径下跑的命令归到工作区。
func TestMultiRepoPicker(t *testing.T) {
	a := previewApp(t)
	root := multiRepoWorkspace(t, a)
	repoA, repoExt := filepath.Join(root, "a"), filepath.Join(root, "ext")
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "scanned", func() bool { return v.repos.scanned && v.git.status != nil })
	a.openFile(w, filepath.Join(repoExt, "lib.go"), 0, [2]int{})
	until(t, tt, "follow ext", func() bool { return v.git.repo == repoExt })
	a.useRepo(v, v.repoAt(repoA))
	for range 3 {
		tt.Frame()
	}
	if v.git.repo != repoA {
		t.Fatalf("manual repo choice undone: %q", v.git.repo)
	}
	until(t, tt, "branch label", func() bool { return tt.HasText("a · main") })
	// 当前仓库还没读出状态（或读失败）：只在顶上写，别的仓库的改动照常列
	v.git.status, v.git.err = nil, "boom-repo"
	tt.Frame()
	// lib.go 也开在标签上，看提交框：只剩 ext 的一个文件
	if !tt.HasText("boom-repo") || !tt.HasText("提交 1 个文件") {
		t.Fatalf("current repo without status hides others: %v", tt.Texts())
	}
	a.refreshRepo(v.git)

	openLogWait(t, a, tt, w, "")
	ld := v.logCur
	until(t, tt, "log", func() bool { return ld.kind == docLog && ld.log.root == repoA && tt.HasText("仓库") })
	// 浮层滑上来、停稳之后才点得准；点仓库是幂等的，点到换过去为止
	until(t, tt, "click repo", func() bool {
		tt.Click("仓库 ext")
		return v.git.repo == repoExt
	})
	until(t, tt, "log switched", func() bool {
		return v.git.repo == repoExt && ld.log.root == repoExt && len(ld.log.commits) == 1 && ld.log.commits[0].Subject == "base lib.go"
	})

	real, err := filepath.EvalSymlinks(repoExt)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.runWorkspace(real); got == nil || got.ID != w.ID && got.ID != a.workspaces[1].ID {
		t.Fatalf("run in %s mapped to %v", real, got)
	}
}

// 多仓库目录的项目新建工作区：列出仓库、默认勾上正在看的那个，建的时候把勾的仓库交给常驻进程。
func TestNewWorkspaceRepos(t *testing.T) {
	a := previewApp(t)
	root := multiRepoWorkspace(t, a)
	w := &a.workspaces[0]
	a.projects[0].Kind = "folder"
	dir, err := os.MkdirTemp("/tmp", "posse-nw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	reqs := make(chan rpc.Request, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req rpc.Request
			json.NewDecoder(conn).Decode(&req)
			reqs <- req
			conn.Write([]byte(`{"result":{"ID":"w9"}}` + "\n"))
			conn.Close()
		}
	}()
	a.c = &client.Client{Socket: sock}
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "scanned", func() bool { return v.repos.scanned && v.git.status != nil })
	a.useRepo(v, v.repoAt(filepath.Join(root, "ext")))
	a.selected = w.ID
	a.openNewWorkspace()
	until(t, tt, "repos listed", func() bool { return tt.HasText("a/nested") && tt.HasText("ext") })
	if !a.newRepos["ext"] || a.newRepos["a"] {
		t.Fatalf("default picks %v", a.newRepos)
	}
	if !tt.HasText("给勾上的 1 个仓库各建一个 git worktree 和同名分支，文件夹里别的文件不带过去。") {
		t.Fatalf("hint: %v", tt.Texts())
	}
	// 改动页上也有叫 a 的仓库标题，勾选直接改状态
	a.newRepos["a"], a.newName = true, "feat"
	tt.Frame()
	if err := tt.Click("创建工作区"); err != nil {
		t.Fatal(err)
	}
	select {
	case req := <-reqs:
		var p struct {
			Name  string
			Repos []string
		}
		json.Unmarshal(req.Params, &p)
		if req.Method != "workspace.create" || p.Name != "feat" || !slices.Equal(p.Repos, []string{"a", "ext"}) {
			t.Fatalf("%s %s", req.Method, req.Params)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("workspace.create not called")
	}
}

// 提示框、确认框的回调晚些才执行：用的是发起时的仓库，不是那时的当前仓库。
// 排队的操作出队时仓库已经不在了：报出来，不再执行。
func TestMultiRepoDeferredOps(t *testing.T) {
	a := previewApp(t)
	root := multiRepoWorkspace(t, a)
	repoA, repoExt := filepath.Join(root, "a"), filepath.Join(root, "ext")
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "scanned", func() bool { return v.repos.scanned && v.repoAt(repoA) != nil && v.repoAt(repoExt) != nil })
	gA, gExt := v.repoAt(repoA), v.repoAt(repoExt)
	a.useRepo(v, gA)
	a.askNewBranch(appL(), w, v, "")
	a.useRepo(v, gExt)
	a.promptDlg.do("from-a", false)
	until(t, tt, "branch", func() bool { return gA.busy == "" && gExt.busy == "" })
	if !strings.Contains(gitRun(t, repoA, "branch"), "from-a") || strings.Contains(gitRun(t, repoExt, "branch"), "from-a") {
		t.Fatalf("new branch went to the wrong repo: a=%q ext=%q", gitRun(t, repoA, "branch"), gitRun(t, repoExt, "branch"))
	}
	a.promptDlg = nil

	ran := false
	gA.busy = "x"
	a.gitDo(appL(), gA, "probe", func(ctx context.Context, root string) error { ran = true; return nil })
	v.repos.list = slices.DeleteFunc(slices.Clone(v.repos.list), func(g *gitState) bool { return g == gA })
	gA.busy = ""
	a.err = ""
	a.gitNext(gA)
	tt.Frame()
	if ran || !strings.Contains(a.err, "probe") {
		t.Fatalf("queued op on a removed repo: ran=%v err=%q", ran, a.err)
	}
}

// 工作区是指向仓库子目录的软链：仓库根目录是真实路径，工作区里的文件照样找得到仓库，改动的路径换回工作区的写法。
func TestSymlinkSubdirWorkspace(t *testing.T) {
	a := previewApp(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	os.WriteFile(filepath.Join(repo, "src", "a.txt"), []byte("one\n"), 0o644)
	gitRun(t, repo, "init", "-q", "-b", "main")
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(repo, "src", "a.txt"), []byte("two\n"), 0o644)
	linked := filepath.Join(base, "linked-src")
	if err := os.Symlink(filepath.Join(repo, "src"), linked); err != nil {
		t.Fatal(err)
	}
	for i := range a.workspaces[:2] {
		a.workspaces[i].Path, a.workspaces[i].Branch = linked, ""
	}
	a.indexWorkspaces()
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.openFilesPanel(w, sideChanges)
	until(t, tt, "status", func() bool { return v.repos.scanned && v.git.status != nil && len(v.git.status.Entries) == 1 })
	g, rel := v.relIn(filepath.Join(linked, "a.txt"))
	if g == nil || rel != "src/a.txt" {
		t.Fatalf("relIn = %v %q", g, rel)
	}
	if p := g.absPath(g.status.Entries[0].Path); p != filepath.Join(linked, "a.txt") {
		t.Fatalf("absPath = %q", p)
	}
}
