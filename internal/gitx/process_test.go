package gitx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestRunWaitDelayStopsDescendants(t *testing.T) {
	testWaitDelayStopsDescendants(t, func(ctx context.Context, dir string) error {
		_, err := run(ctx, dir, "status")
		return err
	})
}

func TestRunRawWaitDelayStopsDescendants(t *testing.T) {
	testWaitDelayStopsDescendants(t, func(ctx context.Context, dir string) error {
		_, err := runRaw(ctx, dir, nil, nil, "status")
		return err
	})
}

func TestRunRawErrorCauses(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		want := context.Canceled
		if deadline {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			want = context.DeadlineExceeded
		}
		_, err := runRaw(ctx, dir, nil, nil, "status", "--porcelain=v2")
		var ge *Error
		if !errors.Is(err, want) || !errors.As(err, &ge) {
			t.Fatalf("deadline=%v: missing cause %v: %v", deadline, want, err)
		}
		if !deadline && ge.Msg != MsgCanceled {
			t.Fatalf("cancel message changed: %q", ge.Msg)
		}
	}
	_, err := runRaw(context.Background(), dir, nil, nil, "branch", "-D", "missing")
	var exit *exec.ExitError
	if !errors.As(err, &exit) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Git error lost its exit cause: %v", err)
	}
}

func testWaitDelayStopsDescendants(t *testing.T, execute func(context.Context, string) error) {
	t.Helper()
	for _, code := range []int{0, 7} {
		t.Run(strconv.Itoa(code), func(t *testing.T) { testParentExitStopsDescendants(t, execute, code) })
	}
}

func testParentExitStopsDescendants(t *testing.T, execute func(context.Context, string) error, code int) {
	t.Helper()
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child")
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nsleep 30 &\nprintf '%s' \"$!\" > \"$TEST_CHILD_PID\"\nexit "+strconv.Itoa(code)+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TEST_CHILD_PID", pidPath)
	if err := execute(context.Background(), dir); err == nil {
		t.Fatal("inherited output pipe did not reach WaitDelay")
	}
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid child pid %q: %v", b, err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for syscall.Kill(pid, 0) != syscall.ESRCH {
		select {
		case <-ctx.Done():
			t.Fatal("Git descendant survived WaitDelay")
		case <-tick.C:
		}
	}
}
