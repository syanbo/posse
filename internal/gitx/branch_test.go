package gitx

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

// withRemote 建一个裸仓库当 origin，把 newRepo 的 main 推上去并跟踪它；返回工作仓库和裸仓库。
func withRemote(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = newRepo(t)
	bare = filepath.Join(t.TempDir(), "origin.git")
	gitIn(t, dir, "init", "-q", "--bare", "-b", "main", bare)
	gitIn(t, dir, "remote", "add", "origin", bare)
	gitIn(t, dir, "push", "-q", "-u", "origin", "main")
	return dir, bare
}

// clone 是"别人"的仓库，用来往 origin 推新提交。
func clone(t *testing.T, bare string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", bare, other)
	gitIn(t, other, "config", "user.email", "o@example.com")
	gitIn(t, other, "config", "user.name", "o")
	return other
}

func find(list []Branch, name string, remote bool) (Branch, bool) {
	i := slices.IndexFunc(list, func(b Branch) bool { return b.Name == name && b.Remote == remote })
	if i < 0 {
		return Branch{}, false
	}
	return list[i], true
}

func TestBranchesAndUpdate(t *testing.T) {
	ctx := context.Background()
	dir, bare := withRemote(t)
	other := clone(t, bare)
	write(t, filepath.Join(other, "a.txt"), "from other\n")
	gitIn(t, other, "commit", "-q", "-am", "other")
	gitIn(t, other, "switch", "-q", "-c", "feat/x")
	gitIn(t, other, "push", "-q", "origin", "main", "feat/x")
	// 本地也有一个提交：和远端分叉
	write(t, filepath.Join(dir, "b.txt"), "mine\n")
	gitIn(t, dir, "add", "b.txt")
	gitIn(t, dir, "commit", "-q", "-m", "mine")
	if err := Fetch(ctx, dir); err != nil {
		t.Fatal(err)
	}
	list, err := Branches(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	main, _ := find(list, "main", false)
	if !main.Current || main.Upstream != "origin/main" || main.Ahead != 1 || main.Behind != 1 || main.Worktree == "" {
		t.Fatalf("main %+v", main)
	}
	if _, ok := find(list, "origin/feat/x", true); !ok {
		t.Fatalf("remote branch missing: %+v", list)
	}
	if _, ok := find(list, "origin/HEAD", true); ok {
		t.Fatal("origin/HEAD listed")
	}
	// 更新当前分支：rebase 到远端上，未提交的改动临时搁置再取回
	write(t, filepath.Join(dir, "src", "b.go"), "package src // dirty\n")
	if err := Update(ctx, dir, main); err != nil {
		t.Fatal(err)
	}
	if n := gitIn(t, dir, "rev-list", "--count", "origin/main..main"); n != "1" {
		t.Fatalf("after update %s ahead", n)
	}
	if st, _ := ReadStatus(ctx, dir); len(st.Entries) != 1 || st.Behind != 0 {
		t.Fatalf("dirty change lost or still behind: %+v", st)
	}
	// 检出远端分支：建同名的本地跟踪分支
	r, _ := find(list, "origin/feat/x", true)
	gitIn(t, dir, "stash", "-q")
	if err := Checkout(ctx, dir, r); err != nil {
		t.Fatal(err)
	}
	if b := gitIn(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); b != "feat/x" {
		t.Fatalf("on %s", b)
	}
	if up := gitIn(t, dir, "rev-parse", "--abbrev-ref", "@{u}"); up != "origin/feat/x" {
		t.Fatalf("upstream %s", up)
	}
	// 更新不是当前分支的 main：分叉时不能快进，报错
	list, _ = Branches(ctx, dir)
	main, _ = find(list, "main", false)
	write(t, filepath.Join(other, "a.txt"), "again\n")
	gitIn(t, other, "switch", "-q", "main")
	gitIn(t, other, "commit", "-q", "-am", "again")
	gitIn(t, other, "push", "-q", "origin", "main")
	if err := Update(ctx, dir, main); err == nil {
		t.Fatal("non-fast-forward update of another branch should fail")
	}
	if recent := RecentBranches(ctx, dir, 5); len(recent) < 2 || recent[0] != "feat/x" || recent[1] != "main" {
		t.Fatalf("recent %v", recent)
	}
}

func TestBranchOps(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	if err := CreateBranch(ctx, dir, "side", "", true); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "s.txt"), "side\n")
	gitIn(t, dir, "add", "s.txt")
	gitIn(t, dir, "commit", "-q", "-m", "side work")
	gitIn(t, dir, "switch", "-q", "main")
	// 比较：只在 side 里的提交；工作区和 side 的文件差异
	cs, err := CommitsBetween(ctx, dir, "main", "side", 100)
	if err != nil || len(cs) != 1 || cs[0].Subject != "side work" || len(cs[0].Parents) != 1 {
		t.Fatalf("commits %+v %v", cs, err)
	}
	files, err := DiffWithWorkingTree(ctx, dir, "side")
	if err != nil || len(files) != 1 || files[0].Path != "s.txt" || files[0].Status != 'D' {
		t.Fatalf("diff with working tree %+v %v", files, err)
	}
	cf, _ := CommitFiles(ctx, dir, cs[0].Hash)
	if len(cf) != 1 || cf[0].Status != 'A' {
		t.Fatalf("commit files %+v", cf)
	}
	// 删除没合并的分支要强制
	err = DeleteBranch(ctx, dir, "side", false)
	if err == nil {
		t.Fatal("deleting unmerged branch should fail")
	}
	if !NotMerged(runErr(ctx, dir, "branch", "-d", "side")) {
		t.Fatalf("not-merged error not recognized: %v", err)
	}
	if err := MergeInto(ctx, dir, "side"); err != nil {
		t.Fatal(err)
	}
	if err := RenameBranch(ctx, dir, "side", "done"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteBranch(ctx, dir, "done", false); err != nil {
		t.Fatal(err)
	}
	// 被另一个 worktree 检出的分支
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, dir, "worktree", "add", "-q", "-b", "agent", wt)
	list, _ := Branches(ctx, dir)
	b, _ := find(list, "agent", false)
	if other, ok := WorktreeOf(b, dir); !ok || other == "" {
		t.Fatalf("worktree of %+v", b)
	}
	m, _ := find(list, "main", false)
	if _, ok := WorktreeOf(m, dir); ok {
		t.Fatal("current worktree reported as other")
	}
}

func runErr(ctx context.Context, dir string, args ...string) error {
	_, err := runRaw(ctx, dir, nil, nil, args...)
	return err
}
