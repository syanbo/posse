package gitx

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
	// 更新当前分支：rebase 到远端上，未提交的改动临时储藏再取回
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
	if !NotMerged(err) {
		t.Fatalf("not-merged error not recognized: %v", err)
	}
	// 常驻进程的入口：报错带命令前缀和 git 的原话（含 hint 行），和以前一字不差
	raw := exec.Command("git", "branch", "-d", "side")
	raw.Dir, raw.Env = dir, append(raw.Environ(), "LC_ALL=C")
	stderr, _ := raw.CombinedOutput()
	if want := "git branch -d side: " + strings.TrimSpace(string(stderr)); err.Error() != want {
		t.Fatalf("daemon error changed:\n%s\nwant\n%s", err, want)
	}
	// 界面的入口：只留 git 的原话，没有命令前缀和 hint 行，照样认得出没合并
	uiErr := DeleteLocalBranch(ctx, dir, "side", false)
	var ge *Error
	if !errors.As(uiErr, &ge) || !NotMerged(uiErr) {
		t.Fatalf("ui delete: %T %v", uiErr, uiErr)
	}
	if msg := uiErr.Error(); strings.HasPrefix(msg, "git ") || strings.Contains(msg, "hint:") {
		t.Fatalf("ui error %q", msg)
	}
	tip := Tip(ctx, dir, "refs/heads/side")
	if err := DeleteLocalBranch(ctx, dir, "side", true); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "branch", "side", tip)
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

func TestTags(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	if tags, err := Tags(ctx, dir); err != nil || len(tags) != 0 {
		t.Fatalf("no tags: %v %v", tags, err)
	}
	gitIn(t, dir, "tag", "v0.1.0")
	gitIn(t, dir, "tag", "-a", "v0.2.0", "-m", "release")
	tags, err := Tags(ctx, dir)
	if err != nil || !slices.Contains(tags, "v0.1.0") || !slices.Contains(tags, "v0.2.0") || len(tags) != 2 {
		t.Fatalf("tags: %v %v", tags, err)
	}
}

func TestAheadBehind(t *testing.T) {
	ctx := context.Background()
	dir, bare := withRemote(t)
	if a, b, ok := AheadBehind(ctx, dir); !ok || a != 0 || b != 0 {
		t.Fatalf("in sync: %d %d %v", a, b, ok)
	}
	other := clone(t, bare)
	write(t, filepath.Join(other, "x.txt"), "x\n")
	gitIn(t, other, "add", ".")
	gitIn(t, other, "commit", "-q", "-m", "x")
	gitIn(t, other, "push", "-q")
	write(t, filepath.Join(dir, "y.txt"), "y\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "y")
	gitIn(t, dir, "fetch", "-q")
	if a, b, ok := AheadBehind(ctx, dir); !ok || a != 1 || b != 1 {
		t.Fatalf("diverged: %d %d %v", a, b, ok)
	}
	gitIn(t, dir, "switch", "-q", "-c", "local-only")
	if _, _, ok := AheadBehind(ctx, dir); ok {
		t.Fatal("branch without upstream should report !ok")
	}
}

// 更新当前分支时取回本地改动冲突：git 返回成功，Update 要报出这次留下的那条储藏；按提交删，不会删错。
func TestUpdateAutostashConflict(t *testing.T) {
	ctx := context.Background()
	dir, bare := withRemote(t)
	other := clone(t, bare)
	write(t, filepath.Join(other, "a.txt"), "theirs\n")
	gitIn(t, other, "commit", "-q", "-am", "theirs")
	gitIn(t, other, "push", "-q", "origin", "main")
	// 之前就有的储藏（别的 worktree 存的也在同一个列表里）不算
	write(t, filepath.Join(dir, "old.txt"), "old\n")
	gitIn(t, dir, "stash", "push", "-q", "-u", "-m", "autostash")
	write(t, filepath.Join(dir, "a.txt"), "mine\n")
	list, _ := Branches(ctx, dir)
	b, _ := find(list, "main", false)
	var left *AutostashLeft
	if err := Update(ctx, dir, b); !errors.As(err, &left) || !left.Conflicts {
		t.Fatalf("want AutostashLeft with conflicts, got %v", err)
	}
	if got := gitIn(t, dir, "rev-parse", "stash@{0}"); got != left.Stash {
		t.Fatalf("left %s, stash@{0} is %s", left.Stash, got)
	}
	// 期间又储藏了一条：按提交删的还是 autostash 那条
	gitIn(t, dir, "checkout", "--theirs", "--", "a.txt")
	gitIn(t, dir, "add", "a.txt")
	write(t, filepath.Join(dir, "b.txt"), "b\n")
	gitIn(t, dir, "stash", "push", "-q", "-u", "-m", "mine")
	if err := StashDropHash(ctx, dir, left.Stash); err != nil {
		t.Fatal(err)
	}
	stashes, _ := Stashes(ctx, dir)
	if len(stashes) != 2 || stashes[0].Message != "On main: mine" || stashes[0].Hash == left.Stash {
		t.Fatalf("stashes after drop: %+v", stashes)
	}
	if err := StashDropHash(ctx, dir, left.Stash); err == nil {
		t.Fatal("dropping a gone stash should fail")
	}
}
