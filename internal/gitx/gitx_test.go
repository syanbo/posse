package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(context.Background(), dir, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// 未跟踪文件数和是否有已跟踪改动要与 ls-files --others 和 status --untracked-files=no 的结果一致。
func TestDiffSummaryMatchesSeparateCommands(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "t")
	write(t, filepath.Join(dir, "a.txt"), "a\n")
	write(t, filepath.Join(dir, ".gitignore"), "*.log\nbuild/\n")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	gitIn(t, dir, "checkout", "-q", "-b", "feat")
	write(t, filepath.Join(dir, "b.txt"), "b\n")
	gitIn(t, dir, "add", "b.txt")
	gitIn(t, dir, "commit", "-q", "-m", "feat")

	type state struct {
		untracked int
		dirty     bool
	}
	old := func() state {
		var s state
		if out := gitIn(t, dir, "ls-files", "--others", "--exclude-standard"); out != "" {
			s.untracked = strings.Count(out, "\n") + 1
		}
		s.dirty = gitIn(t, dir, "status", "--porcelain", "--untracked-files=no") != ""
		return s
	}
	steps := []func(){
		func() {},
		func() { write(t, filepath.Join(dir, "new.txt"), "n\n") },
		func() {
			write(t, filepath.Join(dir, "deep/x/y.txt"), "y\n")
			write(t, filepath.Join(dir, "deep/z.txt"), "z\n")
			write(t, filepath.Join(dir, "debug.log"), "ignored\n")
			write(t, filepath.Join(dir, "build/out.bin"), "ignored\n")
			write(t, filepath.Join(dir, "with space ü.txt"), "s\n")
		},
		func() { write(t, filepath.Join(dir, "a.txt"), "changed\n") },
		func() { gitIn(t, dir, "add", "-N", "new.txt") },
	}
	for i, step := range steps {
		step()
		c, err := DiffSummary(ctx, dir, "main")
		if err != nil {
			t.Fatal(err)
		}
		want := old()
		if c.Untracked != want.untracked || c.Dirty != want.dirty {
			t.Fatalf("step %d: untracked=%d dirty=%v, want %d %v", i, c.Untracked, c.Dirty, want.untracked, want.dirty)
		}
		if c.Commits != 1 {
			t.Fatalf("step %d: commits=%d", i, c.Commits)
		}
	}
}
