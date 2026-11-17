package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
	"posse/internal/store"
)

func TestDesktopClearFullPTYDoesNotBlockStop(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec sleep 30"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "terminal ready", func() bool { return s.ContainsText("READY") })
	fillCtx, cancelFill := context.WithTimeout(context.Background(), 100*time.Millisecond)
	err = s.SendContext(fillCtx, bytes.Repeat([]byte("x"), 1<<20))
	cancelFill()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PTY did not fill: %v", err)
	}
	socket := desktopServer(t, d)
	a := desktopTakeTest(t, d, "A")
	params, _ := json.Marshal(terminalClearParams{Terminal: s.ID, Redraw: true})
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: a, Method: "terminal.clear", Sequence: 1, Params: params}, nil)
	}()
	waitFor(t, "clear waiting for redraw", func() bool { return !s.ContainsText("READY") })
	stopped := make(chan struct{})
	go func() { d.closeAll(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		s.Close(0)
		t.Fatal("clear redraw prevented daemon stop")
	}
	if err := <-result; err == nil {
		t.Fatal("blocked redraw reported success")
	}
}

func TestDesktopSetupWaitDelayReapsChildren(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "child")
	ctx := context.WithValue(context.Background(), desktopCallContext{}, desktopRequestContext{})
	result := runSetup(ctx, store.Workspace{Path: dir}, "sleep 30 & printf '%s' $! > "+agent.Quote(pidPath))
	b, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(b))
	if err != nil || pid <= 0 {
		t.Fatalf("invalid child pid %q: %v", b, err)
	}
	defer syscall.Kill(pid, syscall.SIGKILL)
	if result.OK {
		t.Fatal("unclosed child pipes reported successful setup")
	}
	waitFor(t, "setup descendant exited", func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH })
}

func TestDesktopDuplicateFieldOrderDoesNotReuseResult(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	srv := rpc.NewServer()
	var actual string
	srv.Handle("terminal.rename", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		actual = p.Name
		return p.Name, nil
	})
	p := rpc.DesktopCall{Desktop: a, Method: "terminal.rename", Sequence: 1, Params: json.RawMessage(`{"Name":"A","name":"B"}`)}
	if _, err := desktopRequestCall(d, srv, context.Background(), p); err != nil || actual != "B" {
		t.Fatalf("first rename: %q %v", actual, err)
	}
	p.Params = json.RawMessage(`{"name":"B","Name":"A"}`)
	_, err := desktopRequestCall(d, srv, context.Background(), p)
	desktopErrorCode(t, err, rpc.CodeConflict)
	if actual != "B" {
		t.Fatal("conflicting request executed")
	}
}
