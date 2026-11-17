package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/mobile"
	"posse/internal/rpc"
)

func desktopCancelRPC(t *testing.T, socket string, current rpc.DesktopIdentity, ref rpc.DesktopOperationRef) rpc.DesktopOperation {
	t.Helper()
	var out rpc.DesktopOperation
	if err := rpc.Call(t.Context(), socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: current, Operation: ref}, &out); err != nil {
		t.Fatal(err)
	}
	if out.DesktopOperationRef != ref {
		t.Fatalf("cancel changed reference: %+v", out)
	}
	return out
}

func desktopStatusRPC(t *testing.T, socket string, ref rpc.DesktopOperationRef) rpc.DesktopOperation {
	t.Helper()
	var out rpc.DesktopOperation
	if err := rpc.Call(t.Context(), socket, "desktop.operation", ref, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDesktopOperationCancelStates(t *testing.T) {
	d, _ := newTestDaemon(t)
	started, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var finishOnce sync.Once
	var executions atomic.Int32
	socket := desktopServer(t, d, func(srv *rpc.Server) {
		srv.Handle("project.add", func(ctx context.Context, _ json.RawMessage) (any, error) {
			executions.Add(1)
			close(started)
			<-ctx.Done()
			close(canceled)
			<-finish
			return map[string]bool{"committed": true}, nil
		})
	})
	t.Cleanup(func() { finishOnce.Do(func() { close(finish) }) })
	owner := desktopTakeTest(t, d, "owner")
	ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 7}
	if out := desktopCancelRPC(t, socket, owner, ref); out.State != "unknown" {
		t.Fatalf("before acceptance %+v", out)
	}
	p := rpc.DesktopCall{Desktop: owner, Sequence: ref.Sequence, Method: "project.add", Params: json.RawMessage(`{"name":"original"}`)}
	done := make(chan error, 1)
	go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", p, nil) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("operation not accepted")
	}
	if out := desktopStatusRPC(t, socket, ref); out.State != "running" {
		t.Fatalf("running %+v", out)
	}
	for range 2 {
		if out := desktopCancelRPC(t, socket, owner, ref); out.State != "running" || out.FinishedAt != 0 || len(out.Result) != 0 || out.Error != nil {
			t.Fatalf("cancel fabricated completion %+v", out)
		}
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("cancel not signaled")
	}
	changed := p
	changed.Params = json.RawMessage(`{"name":"changed"}`)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", changed, nil), rpc.CodeConflict)
	finishOnce.Do(func() { close(finish) })
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	out := desktopCancelRPC(t, socket, owner, ref)
	if out.State != "done" || out.Error != nil || string(out.Result) != `{"committed":true}` {
		t.Fatalf("lost actual outcome %+v", out)
	}
	if err := rpc.Call(t.Context(), socket, "desktop.call", p, nil); err != nil || executions.Load() != 1 {
		t.Fatalf("replayed completed operation: %d %v", executions.Load(), err)
	}
	unknown := ref
	unknown.Sequence++
	if out := desktopCancelRPC(t, socket, owner, unknown); out.State != "unknown" {
		t.Fatalf("unknown %+v", out)
	}
}

func TestDesktopOperationCancelControl(t *testing.T) {
	d, _ := newTestDaemon(t)
	started := make(chan struct{})
	socket := desktopServer(t, d, func(srv *rpc.Server) {
		srv.Handle("project.add", func(ctx context.Context, _ json.RawMessage) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	})
	a := desktopTakeTest(t, d, "A")
	ref := rpc.DesktopOperationRef{Desktop: a, Sequence: 1}
	done := make(chan error, 1)
	go func() {
		done <- rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: a, Sequence: 1, Method: "project.add"}, nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("operation not accepted")
	}
	b := desktopTakeTest(t, d, "B")
	for _, kind := range []string{"old-owner", "old-epoch", "old-current-instance", "old-operation-instance", "invalid-reference"} {
		p := rpc.DesktopOperationCancel{Desktop: b, Operation: ref}
		code := rpc.CodeReadOnly
		switch kind {
		case "old-owner":
			p.Desktop = a
		case "old-epoch":
			p.Desktop.Epoch--
		case "old-current-instance":
			p.Desktop.InstanceID = "old"
			code = rpc.CodeConflict
		case "old-operation-instance":
			p.Operation.Desktop.InstanceID = "old"
			code = rpc.CodeConflict
		case "invalid-reference":
			p.Operation.Sequence = 0
			code = rpc.CodeBadRequest
		}
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.operation.cancel", p, nil), code)
		if out := desktopStatusRPC(t, socket, ref); out.State != "running" {
			t.Fatalf("%s canceled operation: %+v", kind, out)
		}
	}
	if out := desktopCancelRPC(t, socket, b, ref); out.State != "running" {
		t.Fatalf("takeover cancel %+v", out)
	}
	if err := <-done; err == nil {
		t.Fatal("canceled handler succeeded")
	}
	if out := desktopStatusRPC(t, socket, ref); out.State != "done" || out.Error == nil {
		t.Fatalf("final %+v", out)
	}
	d.desktop.mu.Lock()
	d.desktop.expires = time.Now().Add(-time.Second)
	d.desktop.mu.Unlock()
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: b, Operation: ref}, nil), rpc.CodeReadOnly)
	old := ref
	old.Desktop.InstanceID = "old"
	if out := desktopStatusRPC(t, socket, old); out.State != "unknown" {
		t.Fatalf("old instance %+v", out)
	}
}

func TestDesktopOperationCancelLostResponse(t *testing.T) {
	d, _ := newTestDaemon(t)
	started, acceptedCancel := make(chan struct{}), make(chan struct{})
	var executions, cancellations atomic.Int32
	socket := desktopServer(t, d, func(srv *rpc.Server) {
		srv.Handle("project.add", func(ctx context.Context, _ json.RawMessage) (any, error) {
			executions.Add(1)
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
		srv.Handle("desktop.operation.cancel", func(ctx context.Context, raw json.RawMessage) (any, error) {
			out, err := d.desktopOperationCancel(ctx, raw)
			cancellations.Add(1)
			close(acceptedCancel)
			<-ctx.Done()
			return out, err
		})
	})
	owner := desktopTakeTest(t, d, "owner")
	ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
	done := make(chan error, 1)
	go func() {
		done <- rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: owner, Sequence: 1, Method: "project.add"}, nil)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("operation not accepted")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cancelDone := make(chan error, 1)
	go func() {
		cancelDone <- rpc.Call(ctx, socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: owner, Operation: ref}, nil)
	}()
	select {
	case <-acceptedCancel:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel not accepted")
	}
	cancel()
	if err := <-cancelDone; err == nil {
		t.Fatal("cancel response was not lost")
	}
	if err := <-done; err == nil {
		t.Fatal("accepted cancel was lost")
	}
	out := desktopStatusRPC(t, socket, ref)
	if out.State != "done" || out.Error == nil || executions.Load() != 1 || cancellations.Load() != 1 {
		t.Fatalf("outcome %+v executions=%d cancels=%d", out, executions.Load(), cancellations.Load())
	}
}

func TestDesktopOperationCancelQueuedSave(t *testing.T) {
	d, w := newTestDaemon(t)
	socket := desktopServer(t, d)
	owner := desktopTakeTest(t, d, "owner")
	path := filepath.Join(w.Path, "file")
	if err := os.WriteFile(path, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := files.ReadVersion(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	_, release, err := (mobileHost{d}).BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: w.ID, WorkspacePath: w.Path, Root: w.Path})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	p := saveCall(owner, 1, saveParams(path, base, []byte("saved")))
	ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
	done := make(chan error, 1)
	go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", p, nil) }()
	waitFor(t, "save accepted", func() bool { return desktopOperationGet(t, d, p).State == "running" })
	if out := desktopCancelRPC(t, socket, owner, ref); out.State != "running" {
		t.Fatalf("queued cancel %+v", out)
	}
	if err := <-done; err == nil {
		t.Fatal("canceled queued save succeeded")
	}
	release()
	out := desktopStatusRPC(t, socket, ref)
	if out.State != "done" || out.Error == nil {
		t.Fatalf("queued final %+v", out)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "before" {
		t.Fatalf("canceled save wrote %q %v", data, err)
	}
	d.desktop.mu.Lock()
	bytes, active := d.desktop.saveInputBytes, d.desktop.active
	d.desktop.mu.Unlock()
	if bytes != 0 || active != 0 {
		t.Fatalf("retained input=%d active=%d", bytes, active)
	}
}
