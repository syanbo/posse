package gitx

import (
	"context"
	"path/filepath"
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
	gitIn(t, dir, "fetch", "-q")
	// 拉到之后 lease 认得远端的位置，强制推送成功
	if err := Push(ctx, dir, plan, true, false); err != nil {
		t.Fatalf("force with lease after fetch: %v", err)
	}
}
