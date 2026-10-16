package gitx

import (
	"context"
	"path/filepath"
	"testing"
)

// Head 读文件得到的结果要和 git rev-parse 一致：子目录、切分支、worktree、游离 HEAD。
func TestHeadMatchesGit(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()
	check := func(dir string) {
		t.Helper()
		want, _ := CurrentBranch(ctx, dir)
		if want == "HEAD" {
			want = gitIn(t, dir, "rev-parse", "--short=8", "HEAD")
		}
		if got := Head(dir); got != want {
			t.Errorf("Head(%s) = %q, want %q", dir, got, want)
		}
	}
	check(repo)
	check(filepath.Join(repo, "src"))
	gitIn(t, repo, "switch", "-q", "-c", "feat/x")
	check(repo)
	wt := filepath.Join(t.TempDir(), "wt")
	gitIn(t, repo, "worktree", "add", "-q", "-b", "other", wt)
	check(wt)
	gitIn(t, repo, "switch", "-q", "--detach")
	check(repo)
	if got := Head(t.TempDir()); got != "" {
		t.Errorf("non-repo: %q", got)
	}
}
