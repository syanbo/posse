package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/store"
)

// 多仓库目录上的 worktree 工作区：只给选中的仓库（含软链过去的、嵌套的）建 worktree，
// 看改动、合并、删除都按仓库逐个来；合并前有一个仓库不能合就一个都不合。
func TestMultiRepoWorktree(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	base := filepath.Join(d.cfg.Home, "src")
	proj := filepath.Join(base, "works")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := func(dir, branch string) {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644)
		git(dir, "init", "-q", "-b", branch)
		git(dir, "add", ".")
		git(dir, "commit", "-q", "-m", "init")
	}
	repo(filepath.Join(proj, "a"), "main")
	repo(filepath.Join(proj, "a", "inner"), "main")
	repo(filepath.Join(proj, "b"), "dev")
	repo(filepath.Join(base, "linked"), "main")
	os.Symlink(filepath.Join(base, "linked"), filepath.Join(proj, "link"))
	os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("notes\n"), 0o644)
	p := store.Project{ID: store.NewID("p_"), Name: "works", Path: proj, Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddProject(p); err != nil {
		t.Fatal(err)
	}

	// 不是仓库、没选仓库的都拒绝；失败时已经建好的撤掉
	if _, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "x", Mode: "worktree"}); err == nil {
		t.Fatal("worktree without repos accepted on a folder project")
	}
	if _, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "x", Repos: []string{"a", "nope"}}); err == nil {
		t.Fatal("unknown repo accepted")
	}
	git(filepath.Join(proj, "b"), "branch", "taken")
	git(filepath.Join(proj, "b"), "worktree", "add", "-q", filepath.Join(base, "taken-wt"), "taken")
	if _, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "taken", Repos: []string{"a", "b"}}); err == nil {
		t.Fatal("branch checked out elsewhere accepted")
	}
	if _, err := os.Stat(filepath.Join(d.cfg.Home, "worktrees", "works", "taken")); err == nil {
		t.Fatal("failed create left its directory")
	}
	if strings.Contains(git(filepath.Join(proj, "a"), "branch"), "taken") {
		t.Fatal("failed create left the branch it made in a")
	}

	w, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "feat", Repos: []string{"a/inner", "a", "b", "link"}})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range w.Repos {
		got = append(got, r.Path+"@"+r.Base)
	}
	if want := []string{"a@main", "a/inner@main", "b@dev", "link@main"}; w.Kind != "worktree" || !slices.Equal(got, want) {
		t.Fatalf("kind %s repos %v", w.Kind, got)
	}
	for _, r := range []string{"a", "a/inner", "b", "link"} {
		if br := git(filepath.Join(w.Path, r), "branch", "--show-current"); br != "feat" {
			t.Fatalf("%s on %q", r, br)
		}
	}
	// 只放选中的仓库，散落的文件不带
	if _, err := os.Stat(filepath.Join(w.Path, "AGENTS.md")); err == nil {
		t.Fatal("loose file copied")
	}
	if stored, err := d.store.Workspace(w.ID); err != nil || len(stored.Repos) != 4 {
		t.Fatalf("stored repos %v %v", stored.Repos, err)
	}

	call := func(method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		var out any
		var err error
		switch method {
		case "diff":
			out, err = d.workspaceDiff(ctx, raw)
		case "merge":
			out, err = d.workspaceMerge(ctx, raw)
		case "rm":
			out, err = d.workspaceRm(ctx, raw)
		}
		b, _ := json.Marshal(out)
		return b, err
	}
	// 嵌套的 worktree 不算外层的未跟踪文件
	raw, err := call("diff", map[string]any{"workspace": w.ID})
	if err != nil || strings.Contains(string(raw), `"untracked":1`) {
		t.Fatalf("diff %s %v", raw, err)
	}
	// 没有提交时同样不把内层算成外层的未跟踪文件
	if _, err := call("merge", map[string]any{"workspace": w.ID}); err == nil || !strings.Contains(err.Error(), "no commits") || strings.Contains(err.Error(), "untracked") {
		t.Fatalf("merge without commits: %v", err)
	}
	// 同名的工作区已经有了：一个分支都不开
	if _, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "feat", Repos: []string{"b"}}); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "dup", Mode: "folder"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "dup", Repos: []string{"a"}}); err == nil || strings.Contains(git(filepath.Join(proj, "a"), "branch"), "dup") {
		t.Fatalf("duplicate name: %v, branches %s", err, git(filepath.Join(proj, "a"), "branch"))
	}

	os.WriteFile(filepath.Join(w.Path, "a", "new.txt"), []byte("a\n"), 0o644)
	git(filepath.Join(w.Path, "a"), "add", "new.txt")
	git(filepath.Join(w.Path, "a"), "commit", "-q", "-m", "in a")
	os.WriteFile(filepath.Join(w.Path, "b", "README"), []byte("changed\n"), 0o644)
	if info := d.workspaceInfo(ctx, w, true); info.Dirty == nil || !*info.Dirty {
		t.Fatal("list --dirty misses the change in b")
	}
	// b 有没提交的改动：一个都不合
	if _, err := call("merge", map[string]any{"workspace": w.ID}); err == nil || !strings.Contains(err.Error(), "worktree b has uncommitted") {
		t.Fatalf("merge with dirty b: %v", err)
	}
	if git(filepath.Join(proj, "a"), "log", "-1", "--format=%s") != "init" {
		t.Fatal("a merged although b blocked")
	}
	git(filepath.Join(w.Path, "b"), "commit", "-q", "-am", "in b")
	raw, err = call("merge", map[string]any{"workspace": w.ID})
	if err != nil || !strings.Contains(string(raw), `"repos":["a","b"]`) {
		t.Fatalf("merge %s %v", raw, err)
	}
	if !strings.Contains(git(filepath.Join(proj, "b"), "log", "--format=%s"), "in b") {
		t.Fatal("b not merged into dev")
	}

	// 从这个工作区派出去的 worker：各仓库从 feat 开出来，合并合进检出 feat 的这个工作区
	ww, err := d.taskWorkspace(ctx, taskStartParams{Workspace: w.ID, Worktree: "worker1"})
	if err != nil || len(ww.Repos) != 4 {
		t.Fatalf("worker workspace %v %v", ww.Repos, err)
	}
	os.WriteFile(filepath.Join(ww.Path, "b", "w.txt"), []byte("w\n"), 0o644)
	git(filepath.Join(ww.Path, "b"), "add", "w.txt")
	git(filepath.Join(ww.Path, "b"), "commit", "-q", "-m", "by worker")
	if raw, err := call("merge", map[string]any{"workspace": ww.ID}); err != nil || !strings.Contains(string(raw), `"repos":["b"]`) {
		t.Fatalf("worker merge %s %v", raw, err)
	}
	if git(filepath.Join(w.Path, "b"), "log", "-1", "--format=%s") != "Merge branch 'worker1' into feat" {
		t.Fatalf("worker merged into %q", git(filepath.Join(w.Path, "b"), "log", "-1", "--format=%s"))
	}
	if _, err := call("rm", map[string]any{"workspace": ww.ID, "force": true, "deleteBranch": true}); err != nil {
		t.Fatal(err)
	}

	// 挪进一个 Git 仓库里再重新指定目录：分支还是 feat，不换成上级仓库的
	parent := filepath.Join(base, "parent")
	repo(parent, "ancestor")
	moved := filepath.Join(parent, "feat")
	if err := os.Rename(w.Path, moved); err != nil {
		t.Fatal(err)
	}
	if _, err := d.workspaceRelocate(ctx, mustJSON(map[string]any{"workspace": w.ID, "path": moved})); err != nil {
		t.Fatal(err)
	}
	if w, err = d.store.Workspace(w.ID); err != nil || w.Branch != "feat" || git(filepath.Join(w.Path, "a"), "branch", "--show-current") != "feat" {
		t.Fatalf("relocated: branch %q path %s %v", w.Branch, w.Path, err)
	}

	// 外层未跟踪的文件名是内层路径的前缀（i 和 inner）：照样算改动，不先拆掉内层
	os.WriteFile(filepath.Join(w.Path, "a", "i"), []byte("x\n"), 0o644)
	if _, err := call("rm", map[string]any{"workspace": w.ID}); err == nil {
		t.Fatal("rm ignored the untracked file i")
	}
	if _, err := os.Stat(filepath.Join(w.Path, "a", "inner", "README")); err != nil {
		t.Fatal("inner worktree removed before the dirty check")
	}
	os.Remove(filepath.Join(w.Path, "a", "i"))

	// 有没提交的改动时不删；force 删掉所有 worktree、分支和目录
	os.WriteFile(filepath.Join(w.Path, "link", "README"), []byte("dirty\n"), 0o644)
	if _, err := call("rm", map[string]any{"workspace": w.ID, "deleteBranch": true}); err == nil || !strings.Contains(err.Error(), "link:") {
		t.Fatalf("rm with dirty link: %v", err)
	}
	if _, err := call("rm", map[string]any{"workspace": w.ID, "deleteBranch": true, "force": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); err == nil {
		t.Fatal("workspace directory left")
	}
	for _, r := range []string{filepath.Join(proj, "a"), filepath.Join(proj, "a", "inner"), filepath.Join(proj, "b"), filepath.Join(base, "linked")} {
		if strings.Contains(git(r, "branch"), "feat") || strings.Contains(git(r, "worktree", "list"), "feat") {
			t.Fatalf("%s keeps feat", r)
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// 工作区目录里还有别的文件：不带 force 时删掉各仓库的 worktree，目录留着并告诉调用方。
func TestMultiRepoWorktreeLeftFiles(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	proj := filepath.Join(d.cfg.Home, "src", "works")
	dir := filepath.Join(proj, "a")
	os.MkdirAll(dir, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	p := store.Project{ID: store.NewID("p_"), Name: "works", Path: proj, Kind: "folder", CreatedAt: time.Now()}
	d.store.AddProject(p)
	w, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "loose", Repos: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(w.Path, "keep.txt"), []byte("x\n"), 0o644)
	out, err := d.workspaceRm(ctx, mustJSON(map[string]any{"workspace": w.ID}))
	if err != nil || out.(map[string]any)["left"] != w.Path {
		t.Fatalf("rm %v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(w.Path, "a")); err == nil {
		t.Fatal("worktree a kept")
	}
}

// 外层仓库跟踪着内层：子模块（检出成空目录，内层有新提交后外层是 M）和指向另一个仓库的软链（检出成软链）。
// 都能放进同一个工作区，合并、删除不把它们当成外层的改动。
func TestMultiRepoWorktreeTrackedNested(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	base := filepath.Join(d.cfg.Home, "src")
	proj := filepath.Join(base, "outer")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=a@b", "-c", "user.name=a", "-c", "protocol.file.allow=always"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	repo := func(dir string) {
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644)
		git(dir, "init", "-q", "-b", "main")
		git(dir, "add", ".")
		git(dir, "commit", "-q", "-m", "init")
	}
	repo(filepath.Join(base, "subsrc"))
	repo(filepath.Join(base, "ext"))
	repo(proj)
	git(proj, "submodule", "add", "-q", filepath.Join(base, "subsrc"), "sub")
	os.Symlink("../ext", filepath.Join(proj, "ext"))
	git(proj, "add", ".")
	git(proj, "commit", "-q", "-m", "nested")
	p := store.Project{ID: store.NewID("p_"), Name: "outer", Path: proj, Kind: "git", CreatedAt: time.Now()}
	d.store.AddProject(p)

	w, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "feat", Repos: []string{".", "sub", "ext"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{".", "sub", "ext"} {
		if br := git(filepath.Join(w.Path, r), "branch", "--show-current"); br != "feat" {
			t.Fatalf("%s on %q", r, br)
		}
	}
	os.WriteFile(filepath.Join(w.Path, "sub", "s.txt"), []byte("s\n"), 0o644)
	git(filepath.Join(w.Path, "sub"), "add", "s.txt")
	git(filepath.Join(w.Path, "sub"), "commit", "-q", "-m", "in sub")
	raw, _ := json.Marshal(map[string]any{"workspace": w.ID})
	out, err := d.workspaceMerge(ctx, raw)
	if b, _ := json.Marshal(out); err != nil || !strings.Contains(string(b), `"repos":["sub"]`) {
		t.Fatalf("merge %s %v", b, err)
	}
	if _, err := d.workspaceRm(ctx, mustJSON(map[string]any{"workspace": w.ID, "deleteBranch": true})); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(w.Path); err == nil {
		t.Fatal("workspace directory left")
	}
}

// b 合回去会冲突：a、b 都不合，不留下只合了一半的结果。
func TestMultiRepoWorktreeMergeConflict(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	proj := filepath.Join(d.cfg.Home, "src", "works")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for _, r := range []string{"a", "b"} {
		dir := filepath.Join(proj, r)
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, "README"), []byte("x\n"), 0o644)
		git(dir, "init", "-q", "-b", "main")
		git(dir, "add", ".")
		git(dir, "commit", "-q", "-m", "init")
	}
	p := store.Project{ID: store.NewID("p_"), Name: "works", Path: proj, Kind: "folder", CreatedAt: time.Now()}
	d.store.AddProject(p)
	w, _, err := d.createWorkspace(ctx, p, workspaceCreateParams{Name: "feat", Repos: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"a", "b"} {
		os.WriteFile(filepath.Join(w.Path, r, "README"), []byte("feat\n"), 0o644)
		git(filepath.Join(w.Path, r), "commit", "-q", "-am", "feat")
	}
	os.WriteFile(filepath.Join(proj, "b", "README"), []byte("main\n"), 0o644)
	git(filepath.Join(proj, "b"), "commit", "-q", "-am", "main side")
	headA := git(filepath.Join(proj, "a"), "rev-parse", "HEAD")
	if _, err := d.workspaceMerge(ctx, mustJSON(map[string]any{"workspace": w.ID})); err == nil || !strings.Contains(err.Error(), "repository b would conflict") {
		t.Fatalf("merge: %v", err)
	}
	if git(filepath.Join(proj, "a"), "rev-parse", "HEAD") != headA {
		t.Fatal("a merged although b would conflict")
	}
	// 查冲突的命令本身失败（git 太旧等）：同样一个都不合
	git(filepath.Join(proj, "b"), "reset", "-q", "--hard", "HEAD~1")
	real, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = merge-tree ] && exit 128; done\nexec "+real+" \"$@\"\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if _, err := d.workspaceMerge(ctx, mustJSON(map[string]any{"workspace": w.ID})); err == nil || !strings.Contains(err.Error(), "cannot check feat for conflicts") {
		t.Fatalf("merge with a failing check: %v", err)
	}
	if git(filepath.Join(proj, "a"), "rev-parse", "HEAD") != headA {
		t.Fatal("a merged although the conflict check failed")
	}
}
