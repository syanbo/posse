package gitx

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
)

func TestBaseDivergence(t *testing.T) {
	ctx := context.Background()
	dir := newRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	if err := AddWorktree(ctx, dir, wt, "feat", "main"); err != nil {
		t.Fatal(err)
	}
	d, err := BaseDivergence(ctx, wt, "main", nil)
	if err != nil || d.Ahead != 0 || d.Behind != 0 {
		t.Fatalf("fresh worktree: %+v %v", d, err)
	}
	write(t, filepath.Join(wt, "a.txt"), "feat\n")
	write(t, filepath.Join(wt, "new.txt"), "x\n")
	gitIn(t, wt, "add", ".")
	gitIn(t, wt, "commit", "-q", "-m", "feat")
	// 基准分支只改别的文件：落后但不冲突
	write(t, filepath.Join(dir, "src", "b.go"), "package src\n\n// main\n")
	gitIn(t, dir, "commit", "-qam", "main b")
	d, err = BaseDivergence(ctx, wt, "main", nil)
	if err != nil || d.Ahead != 1 || d.Behind != 1 || len(d.Conflicts) != 0 {
		t.Fatalf("no conflict: %+v %v", d, err)
	}
	write(t, filepath.Join(dir, "a.txt"), "main\n")
	gitIn(t, dir, "commit", "-qam", "main a")
	d, err = BaseDivergence(ctx, wt, "main", nil)
	if err != nil || d.Ahead != 1 || d.Behind != 2 || !slices.Equal(d.Conflicts, []string{"a.txt"}) {
		t.Fatalf("conflict: %+v %v", d, err)
	}
	// 两边都没动：沿用上次的结果，不再算
	stale := d
	stale.Conflicts = []string{"沿用"}
	if again, _ := BaseDivergence(ctx, wt, "main", &stale); len(again.Conflicts) != 1 || again.Conflicts[0] != "沿用" {
		t.Fatalf("unchanged sides should reuse prev: %+v", again)
	}
	// 预测不动工作区和暂存区
	if st, _ := ReadStatus(ctx, wt); len(st.Entries) != 0 || st.Operation != "" {
		t.Fatalf("merge-tree touched the worktree: %+v", st)
	}
	if _, err := BaseDivergence(ctx, wt, "gone", nil); err == nil {
		t.Fatal("missing base should fail")
	}
}

func TestStashBranch(t *testing.T) {
	for msg, want := range map[string]string{
		"On feat/x: 说明":                 "feat/x",
		"WIP on main: abc1234 提交信息: 冒号": "main",
		"WIP on (no branch): abc 信息":    "",
		"自己写的":                          "",
	} {
		if got := stashBranch(msg); got != want {
			t.Errorf("stashBranch(%q) = %q, want %q", msg, got, want)
		}
	}
}
