package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/usage"
)

func TestDesktopWriteRPCAndIndependentCLI(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	a := desktopTakeTest(t, d, "A")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	params, _ := json.Marshal(projectAddParams{Path: t.TempDir(), Name: "desktop"})
	p := rpc.DesktopCall{Desktop: a, Method: "project.add", Sequence: 1, Params: params}
	var first, duplicate projectInfo
	if err := rpc.Call(ctx, socket, "desktop.call", p, &first); err != nil {
		t.Fatal(err)
	}
	desktopTakeTest(t, d, "B")
	if err := rpc.Call(ctx, socket, "desktop.call", p, &duplicate); err != nil || first.ID != duplicate.ID {
		t.Fatalf("created another project on retry: %+v %+v %v", first, duplicate, err)
	}
	p.Sequence++
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", p, nil), rpc.CodeReadOnly)
	if err := rpc.Call(ctx, socket, "project.add", projectAddParams{Path: t.TempDir(), Name: "cli"}, nil); err != nil {
		t.Fatalf("independent CLI blocked by desktop ownership: %v", err)
	}
	projects, err := d.store.Projects()
	if err != nil || len(projects) != 3 {
		t.Fatalf("projects = %+v, %v", projects, err)
	}
}

func TestDesktopUsageSnapshotDoesNotStartRefresh(t *testing.T) {
	d := &Daemon{cfg: Config{Home: t.TempDir()}}
	d.usage.store = usage.Open(d.cfg.Home)
	d.usage.store.SetCursorOff(true)
	d.usage.store.SetCodexOff(true)
	d.usage.disabled = []string{"claude"}
	previous := time.Unix(100, 0)
	d.usage.claudeMod = previous
	ctx := context.WithValue(context.Background(), desktopCallContext{}, desktopRequestContext{readOnly: true})
	if _, err := d.usageGet(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.usage.disabled, []string{"claude"}) || d.usage.claudeMod != previous {
		t.Fatal("snapshot reconciled settings and scheduled refresh")
	}
}

func TestDesktopSetupStopsWithOperation(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{})
	done := make(chan *setupResult, 1)
	go func() {
		done <- runSetup(ctx, store.Workspace{Path: dir}, "printf '%s' $$ > "+agent.Quote(pidPath)+"; exec sleep 30")
	}()
	waitFor(t, "setup started", func() bool { b, _ := os.ReadFile(pidPath); return len(b) > 0 })
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid child pid %q: %v", b, err)
	}
	cancel()
	select {
	case result := <-done:
		if result.OK {
			t.Fatal("canceled setup reported success")
		}
	case <-time.After(4 * time.Second):
		t.Fatal("setup ignored operation cancellation")
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("setup child still running: %v", err)
	}
}

func TestDesktopOversizeResultIsUnknownAndBounded(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	srv := rpc.NewServer()
	srv.Handle("project.add", func(context.Context, json.RawMessage) (any, error) {
		return strings.Repeat("x", desktopResultLimit+1), nil
	})
	p := rpc.DesktopCall{Desktop: a, Method: "project.add", Sequence: 1}
	_, err := desktopRequestCall(d, srv, context.Background(), p)
	desktopErrorCode(t, err, rpc.CodeOutcomeUnknown)
	st := desktopOperationGet(t, d, p)
	if st.Error == nil || st.Error.Code != rpc.CodeOutcomeUnknown || len(st.Result) != 0 || d.desktop.operationBytes >= desktopResultLimit {
		t.Fatalf("oversized result retained or hidden: %+v bytes=%d", st, d.desktop.operationBytes)
	}
}
