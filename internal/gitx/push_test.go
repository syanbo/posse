package gitx

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPush(t *testing.T) {
	ctx := context.Background()
	dir, bare := withRemote(t)
	// 跟踪分支之后的一个新提交
	write(t, filepath.Join(dir, "a.txt"), "mine\n")
	gitIn(t, dir, "commit", "-q", "-am", "mine")
	plan, err := PushPreview(ctx, dir, "main")
	if err != nil || plan.NewBranch || plan.Remote != "origin" || plan.RemoteBranch != "main" || len(plan.Commits) != 1 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if err := Push(ctx, dir, plan, false, false); err != nil {
		t.Fatal(err)
	}
	// 新分支：推到 origin 的同名分支并设上游
	gitIn(t, dir, "switch", "-q", "-c", "feat")
	write(t, filepath.Join(dir, "f.txt"), "f\n")
	gitIn(t, dir, "add", "f.txt")
	gitIn(t, dir, "commit", "-q", "-m", "feat")
	plan, _ = PushPreview(ctx, dir, "feat")
	if !plan.NewBranch || len(plan.Commits) != 1 {
		t.Fatalf("new branch plan %+v", plan)
	}
	if err := Push(ctx, dir, plan, false, false); err != nil {
		t.Fatal(err)
	}
	if up := gitIn(t, dir, "rev-parse", "--abbrev-ref", "feat@{u}"); up != "origin/feat" {
		t.Fatalf("upstream %s", up)
	}
	// 别人往 feat 推了提交，本地改写历史后普通推送被拒，强制推送因 lease 也被拒（本地没拉到别人的提交）
	other := clone(t, bare)
	gitIn(t, other, "switch", "-q", "feat")
	write(t, filepath.Join(other, "o.txt"), "o\n")
	gitIn(t, other, "add", "o.txt")
	gitIn(t, other, "commit", "-q", "-m", "other")
	gitIn(t, other, "push", "-q", "origin", "feat")
	gitIn(t, dir, "commit", "-q", "--amend", "-m", "feat amended")
	plan, _ = PushPreview(ctx, dir, "feat")
	if err := Push(ctx, dir, plan, false, false); !Rejected(err) {
		t.Fatalf("expected rejection, got %v", err)
	}
	// 自动 fetch 把 origin/feat 刷新到别人的提交：只拉到、没合进来，强制推送仍被拒（--force-if-includes）
	gitIn(t, dir, "fetch", "-q")
	if err := Push(ctx, dir, plan, true, false); !Rejected(err) {
		t.Fatalf("force after fetch without integrating should be rejected, got %v", err)
	}
	if got := gitIn(t, other, "ls-remote", "origin", "refs/heads/feat"); !strings.HasPrefix(got, gitIn(t, other, "rev-parse", "HEAD")) {
		t.Fatalf("other's commit was overwritten: %s", got)
	}
	// 把别人的提交合进来之后再改写，强制推送成功
	gitIn(t, dir, "rebase", "-q", "origin/feat")
	gitIn(t, dir, "commit", "-q", "--amend", "-m", "feat amended again")
	plan, _ = PushPreview(ctx, dir, "feat")
	if err := Push(ctx, dir, plan, true, false); err != nil {
		t.Fatalf("force after integrating: %v", err)
	}
}

// 推送目标和 git push 一致：remote.pushDefault 指向 fork 时推到 fork 的同名分支、不改上游；
// 跟踪的远端分支被删了时标出来，推送时重新设上游。
func TestPushTarget(t *testing.T) {
	ctx := context.Background()
	dir, _ := withRemote(t)
	fork := filepath.Join(t.TempDir(), "fork.git")
	gitIn(t, dir, "init", "-q", "--bare", "-b", "main", fork)
	gitIn(t, dir, "remote", "add", "fork", fork)
	gitIn(t, dir, "config", "remote.pushDefault", "fork")
	plan, err := PushPreview(ctx, dir, "main")
	if err != nil || plan.Remote != "fork" || plan.RemoteBranch != "main" || !plan.NewBranch || plan.SetUpstream {
		t.Fatalf("fork plan %+v %v", plan, err)
	}
	if err := Push(ctx, dir, plan, false, false); err != nil {
		t.Fatal(err)
	}
	if up := gitIn(t, dir, "rev-parse", "--abbrev-ref", "main@{u}"); up != "origin/main" {
		t.Fatalf("upstream changed to %s", up)
	}
	gitIn(t, dir, "config", "--unset", "remote.pushDefault")

	gitIn(t, dir, "switch", "-q", "-c", "feat")
	gitIn(t, dir, "push", "-q", "-u", "origin", "feat:old-name")
	gitIn(t, dir, "push", "-q", "origin", ":old-name")
	gitIn(t, dir, "fetch", "-q", "-p")
	plan, err = PushPreview(ctx, dir, "feat")
	if err != nil || !plan.Gone || !plan.SetUpstream || plan.RemoteBranch != "old-name" {
		t.Fatalf("gone plan %+v %v", plan, err)
	}
}
