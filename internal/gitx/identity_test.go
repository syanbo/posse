package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCommonDirCheckedWithoutGitBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	plain := t.TempDir()
	common, err := CommonDirChecked(context.Background(), plain)
	if err != nil || common != "" {
		t.Fatalf("plain dir without git binary: common=%q err=%v", common, err)
	}
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CommonDirChecked(context.Background(), repo); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("git dir without git binary: err=%v, want exec.ErrNotFound", err)
	}
}
