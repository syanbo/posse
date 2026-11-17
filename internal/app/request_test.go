package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/rpc"
)

func requestTestApp(t *testing.T, id string) (*app, *hostControlFixture) {
	t.Helper()
	f, c := newHostControlFixture(t)
	c.Remote = true
	h := newHostSession(id, id)
	if err := h.connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	return &app{host: h}, f
}

func takeRequestTestControl(t *testing.T, a *app) {
	t.Helper()
	if err := a.host.take(context.Background(), a.host.snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestAppRequestViewerAndStaleWrite(t *testing.T) {
	a, f := requestTestApp(t, "host-a")
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		calls.Add(1)
		return nil, nil
	}
	f.mu.Unlock()
	viewer := a.request()
	err := viewer.call("terminal.close", map[string]any{"terminal": "same-id"}, nil, time.Second)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly || calls.Load() != 0 {
		t.Fatalf("viewer write = %v, calls = %d", err, calls.Load())
	}
	takeRequestTestControl(t, a)
	a.runPending()
	if a.err != "" {
		t.Fatal("viewer error leaked into the new control identity")
	}
	old := a.request()
	oldID := *old.client.Desktop
	takeRequestTestControl(t, a)
	if err := old.call("terminal.close", nil, nil, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("old write = %v", err)
	}
	if *old.client.Desktop != oldID || calls.Load() != 0 {
		t.Fatal("old request changed identity or reached the socket")
	}
	if err := a.request().call("terminal.close", nil, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("current write calls = %d", calls.Load())
	}
}

func TestAppRequestConfirmationsExpire(t *testing.T) {
	for _, action := range []string{"close", "close-all", "remove-project", "remove-orphan", "remove-missing", "create-workspace"} {
		t.Run(action, func(t *testing.T) {
			a, f := requestTestApp(t, action)
			takeRequestTestControl(t, a)
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
				calls.Add(1)
				return nil, nil
			}
			f.mu.Unlock()
			w := workspace{ID: "same-w", ProjectID: "same-p", Project: "project", Kind: "folder"}
			m := appL()
			switch action {
			case "close":
				a.askClose(m, terminalInfo{ID: "same-t", Running: true})
			case "close-all":
				a.askCloseAll(m, []terminalInfo{{ID: "same-t", Running: true}})
			case "remove-project":
				a.askRemoveProject(m, w)
			case "remove-orphan":
				a.askRemoveOrphan(m, w)
			case "remove-missing":
				a.askRemoveMissing(m, w)
			case "create-workspace":
				a.openNewWorkspace()
			}
			// 同一主机重新接管也不能让旧确认借用新 epoch。
			takeRequestTestControl(t, a)
			if action == "create-workspace" {
				a.newRequest.createWorkspace(m, map[string]any{"project": "same-p", "name": "new"})
			} else {
				if a.confirmDo == nil {
					t.Fatal("missing confirmation")
				}
				a.confirmDo()
				if a.confirmAltDo != nil {
					a.confirmAltDo()
				}
			}
			a.runPending()
			if calls.Load() != 0 || a.err != "" || a.selected != "" {
				t.Fatalf("expired action ran: calls=%d err=%q selected=%q", calls.Load(), a.err, a.selected)
			}
		})
	}
}

func TestAppRequestLateReplyAndQueuedUpdate(t *testing.T) {
	a, fa := requestTestApp(t, "host-a")
	b, fb := requestTestApp(t, "host-b")
	takeRequestTestControl(t, a)
	takeRequestTestControl(t, b)
	entered, finish := make(chan rpc.DesktopCall, 1), make(chan struct{})
	var bCalls atomic.Int32
	fa.mu.Lock()
	fa.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		entered <- p
		<-finish
		return nil, rpc.Errorf(rpc.CodeConflict, "late failure")
	}
	fa.mu.Unlock()
	fb.mu.Lock()
	fb.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { bCalls.Add(1); return nil, nil }
	fb.mu.Unlock()
	old := a.request()
	old.update(func() { a.selected = "same-id" })
	done := make(chan error, 1)
	go func() { done <- old.call("terminal.close", map[string]any{"terminal": "same-id"}, nil, time.Minute) }()
	select {
	case p := <-entered:
		if p.Desktop != *old.client.Desktop || string(p.Params) != `{"terminal":"same-id"}` {
			t.Fatalf("wrong request: %+v", p)
		}
	case <-time.After(time.Minute):
		t.Fatal("request not received")
	}
	a.host.detach()
	if err := b.request().call("terminal.close", map[string]any{"terminal": "same-id"}, nil, time.Second); err != nil {
		t.Fatal(err)
	}
	b.request().update(func() { b.selected = "same-id" })
	close(finish)
	if err := <-done; err == nil {
		t.Fatal("expected original RPC error")
	}
	a.runPending()
	b.runPending()
	if a.selected != "" || a.err != "" || b.selected != "same-id" || b.err != "" || bCalls.Load() != 1 {
		t.Fatalf("cross-host result: a=%q/%q b=%q/%q calls=%d", a.selected, a.err, b.selected, b.err, bCalls.Load())
	}
}

func TestAppRequestRemotePollDoesNotInspectLocalPaths(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	path := filepath.Join(t.TempDir(), "absent-worktree")
	var calls []string
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		calls = append(calls, p.Method)
		switch p.Method {
		case "project.list":
			return []project{{ID: "p", Name: "project"}}, nil
		case "daemon.status":
			return daemonStatus{}, nil
		case "workspace.list":
			return []workspace{{ID: "w", ProjectID: "p", Kind: "worktree", Path: path, Branch: "remote-branch"}}, nil
		case "task.list":
			return []taskInfo{}, nil
		default:
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
		}
	}
	f.mu.Unlock()
	a.refresh()
	if a.lastPoll == nil || a.lastPoll.err != "" || len(a.lastPoll.workspaces) != 1 {
		t.Fatalf("poll = %+v", a.lastPoll)
	}
	w := a.lastPoll.workspaces[0]
	if w.Missing || w.Branch != "remote-branch" || a.lastPoll.termKB != 0 {
		t.Fatalf("local inspection changed remote state: %+v", w)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, method := range calls {
		if method == "workspace.rm" || method == "discussion.list" {
			t.Fatalf("unexpected remote call: %s", method)
		}
	}
}

func TestAppRequestCloseAllContinuesPastMissingTerminal(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	var closed []string
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "terminal.close" {
			return nil, rpc.Errorf(rpc.CodeUnknown, "skip refresh")
		}
		var params struct{ Terminal string }
		if err := rpc.Decode(p.Params, &params); err != nil {
			return nil, err
		}
		f.mu.Lock()
		closed = append(closed, params.Terminal)
		f.mu.Unlock()
		if params.Terminal == "gone" {
			return nil, rpc.Errorf(rpc.CodeNotFound, "terminal not found")
		}
		return nil, nil
	}
	f.mu.Unlock()
	a.askCloseAll(appL(), []terminalInfo{{ID: "gone", Running: true}, {ID: "live", Running: true}})
	a.confirmDo()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !slices.Equal(closed, []string{"gone", "live"}) {
		t.Fatalf("closed = %v", closed)
	}
}

func TestAppRequestRemoteDirectoryActions(t *testing.T) {
	a, _ := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	// 无窗口测试直接调用真实入口；若打开本机系统对话框，测试就不能完成。
	a.addProject(appL())
	a.runPending()
	if a.targetDir == nil || a.err != "" {
		t.Fatal("remote project picker was not opened")
	}
	a.closeTargetDirectory()
	a.relocateWorkspace(appL(), workspace{ID: "w"})
	a.runPending()
	if a.targetDir == nil || a.err != "" {
		t.Fatal("remote relocation picker was not opened")
	}
	a.closeTargetDirectory()
	root := filepath.Join(t.TempDir(), "not-on-this-machine")
	main := workspace{ID: "main", ProjectID: "p", Project: "project", Kind: "main", Path: root}
	wt := workspace{ID: "wt", ProjectID: "p", Kind: "worktree", Path: root + "-wt"}
	a.byProject = map[string][]workspace{"p": {main, wt}}
	a.err = ""
	a.askRemoveProject(appL(), main)
	if a.confirming || a.err == "" {
		t.Fatal("local absence classified a remote project as orphaned")
	}
}

func TestAppRequestDetachedCleanupDoesNotPrune(t *testing.T) {
	f, c := newHostControlFixture(t)
	h := newHostSession("local", "local")
	if err := h.connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	a := &app{host: h}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "base")
	wt := filepath.Join(t.TempDir(), "gone")
	gitRun(t, root, "worktree", "add", "-qb", "worker", wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "workspace.rm" {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
		}
		// 模拟服务端接受删除后、成功响应返回之前发生切换。
		h.detach()
		return nil, nil
	}
	f.mu.Unlock()
	a.dropGoneWorktrees(context.Background(), []workspace{
		{ID: "m", ProjectID: "p", Kind: "main", Path: root},
		{ID: "w", ProjectID: "p", Kind: "worktree", Path: wt},
	})
	if list := gitRun(t, root, "worktree", "list", "--porcelain"); !strings.Contains(list, "refs/heads/worker") {
		t.Fatalf("detached request pruned worktree metadata: %s", list)
	}
}

func TestAppRequestPollReappliesAfterIdentityChange(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		return nil, rpc.Errorf(rpc.CodeConflict, "poll failure")
	}
	f.mu.Unlock()
	a.refresh()
	takeRequestTestControl(t, a)
	a.runPending()
	if a.err != "" {
		t.Fatal("queued poll from old identity applied")
	}
	a.refresh()
	a.runPending()
	if a.err == "" {
		t.Fatal("identical result suppressed the first poll of the new identity")
	}
}
