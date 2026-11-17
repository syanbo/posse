package gitx

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommonDirNonRepositoryMessages(t *testing.T) {
	for _, message := range []string{
		"fatal: not a git repository (or any of the parent directories): .git",
		"fatal: not a git repository (or any parent up to mount point /Volumes/External)\nStopping at filesystem boundary (GIT_DISCOVERY_ACROSS_FILESYSTEM not set).",
	} {
		if !notRepository(message) {
			t.Fatalf("non-repository not recognized: %s", message)
		}
	}
	for _, message := range []string{"fatal: not a git repository: /missing/gitdir", "fatal: cannot chdir: Permission denied", MsgCanceled} {
		if notRepository(message) {
			t.Fatalf("failure hidden as non-repository: %s", message)
		}
	}
}

func TestCommonDirChecked(t *testing.T) {
	dir := t.TempDir()
	if got, err := CommonDirChecked(t.Context(), dir); err != nil || got != "" {
		t.Fatalf("non-repository: %q %v", got, err)
	}
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.name", "Test")
	gitIn(t, dir, "config", "user.email", "test@example.invalid")
	gitIn(t, dir, "commit", "-q", "--allow-empty", "-m", "initial")
	common, err := CommonDirChecked(t.Context(), dir)
	if err != nil || common == "" {
		t.Fatalf("repository identity: %q %v", common, err)
	}
	wt := filepath.Join(t.TempDir(), "linked tree")
	gitIn(t, dir, "worktree", "add", "-q", "-b", "side", wt)
	if got, err := CommonDirChecked(t.Context(), wt); err != nil || got != common {
		t.Fatalf("worktree identity: %q %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := CommonDirChecked(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation hidden as non-repository: %v", err)
	}
	if _, err := CommonDirChecked(t.Context(), filepath.Join(dir, "missing")); err == nil {
		t.Fatal("invalid directory hidden as non-repository")
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /missing/posse-test-gitdir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CommonDirChecked(t.Context(), broken); err == nil {
		t.Fatal("broken gitdir hidden as non-repository")
	}
}
