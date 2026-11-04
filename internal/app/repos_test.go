package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"posse/internal/gitx"
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
