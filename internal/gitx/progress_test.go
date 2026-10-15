package gitx

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// conflict 让 main 和 side 都改 a.txt，返回仓库。
func conflict(t *testing.T) string {
	t.Helper()
	dir := newRepo(t)
	gitIn(t, dir, "switch", "-q", "-c", "side")
	write(t, filepath.Join(dir, "a.txt"), "side\n")
	gitIn(t, dir, "commit", "-q", "-am", "side")
	gitIn(t, dir, "switch", "-q", "main")
	write(t, filepath.Join(dir, "a.txt"), "main\n")
	gitIn(t, dir, "commit", "-q", "-am", "main")
	return dir
}

func TestMergeConflictTakeTheirsContinue(t *testing.T) {
	ctx := context.Background()
	dir := conflict(t)
	if err := MergeInto(ctx, dir, "side"); err == nil {
		t.Fatal("expected conflict")
	}
	st, _ := ReadStatus(ctx, dir)
	if st.Operation != "merge" {
		t.Fatalf("op %q", st.Operation)
	}
	if err := TakeSide(ctx, dir, "a.txt", false); err != nil {
		t.Fatal(err)
	}
	if err := Continue(ctx, dir, "merge"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "side\n" {
		t.Fatalf("took %q", b)
	}
	if st, _ := ReadStatus(ctx, dir); st.Operation != "" || len(st.Entries) != 0 {
		t.Fatalf("after continue %+v", st)
	}
}

func TestRebaseConflictAbortAndSkip(t *testing.T) {
	ctx := context.Background()
	dir := conflict(t)
	gitIn(t, dir, "switch", "-q", "side")
	before := gitIn(t, dir, "rev-parse", "HEAD")
	if err := Rebase(ctx, dir, "main"); err == nil {
		t.Fatal("expected conflict")
	}
	if st, _ := ReadStatus(ctx, dir); st.Operation != "rebase" {
		t.Fatalf("op %q", st.Operation)
	}
	if err := Abort(ctx, dir, "rebase"); err != nil {
		t.Fatal(err)
	}
	if gitIn(t, dir, "rev-parse", "HEAD") != before {
		t.Fatal("abort did not restore HEAD")
	}
	// 再来一次，这次跳过冲突的提交：side 就等于 main 了
	Rebase(ctx, dir, "main")
	if err := Skip(ctx, dir, "rebase"); err != nil {
		t.Fatal(err)
	}
	if gitIn(t, dir, "rev-parse", "HEAD") != gitIn(t, dir, "rev-parse", "main") {
		t.Fatal("skip did not drop the commit")
	}
	if err := Skip(ctx, dir, "merge"); err == nil {
		t.Fatal("merge cannot skip")
	}
}
