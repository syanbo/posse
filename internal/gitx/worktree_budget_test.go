package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestWorktreeCompareCancelCleanup(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	ctx := historyContext(t)
	id, err := RepositoryIdentityChecked(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(t.TempDir(), "started")
	tmp := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in *\" diff --no-ext-diff \"*) /usr/bin/touch \"$WORKTREE_CANCEL_MARKER\"; exec /bin/sleep 10;; esac\nexec /usr/bin/git \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("WORKTREE_CANCEL_MARKER", marker)
	t.Setenv("TMPDIR", tmp)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := ReadWorktreeComparison(ctx, id, "HEAD", ""); result <- err }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case err := <-result:
			t.Fatalf("returned before cancel: %v", err)
		case <-deadline.C:
			t.Fatal("diff never started")
		case <-ticker.C:
			if _, err := os.Stat(marker); err == nil {
				cancel()
				goto canceled
			}
		}
	}
canceled:
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp leaked %+v %v", entries, err)
	}
}

func TestWorktreeCompareCostSample(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	const count = 200
	data := bytes.Repeat([]byte("x"), 64<<10)
	for i := 0; i < count; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("file-%04d", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, root, "add", "--all")
	gitIn(t, root, "commit", "-qm", "sample")
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	got, err := readWorktreeTest(t, root, "HEAD", "")
	elapsed := time.Since(started)
	runtime.ReadMemStats(&after)
	if err != nil || len(got.Files) != 0 {
		t.Fatalf("sample files=%d err=%v", len(got.Files), err)
	}
	t.Logf("files=%d trackedBytes=%d checksumPasses=2 bufferBytes=%d duration=%s allocatedBytes=%d", count+1, count*len(data)+2, 32<<10, elapsed, after.TotalAlloc-before.TotalAlloc)
}
