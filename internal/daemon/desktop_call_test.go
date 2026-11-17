package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/host"
	"posse/internal/rpc"
)

func desktopRequestCall(d *Daemon, srv *rpc.Server, ctx context.Context, p rpc.DesktopCall) (any, error) {
	b, _ := json.Marshal(p)
	return d.desktopCall(srv, ctx, b)
}

func desktopErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func desktopOperationGet(t *testing.T, d *Daemon, p rpc.DesktopCall) rpc.DesktopOperation {
	t.Helper()
	b, _ := json.Marshal(rpc.DesktopOperationRef{Desktop: p.Desktop, Sequence: p.Sequence})
	v, err := d.desktopOperationStatus(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return v.(rpc.DesktopOperation)
}

func TestDesktopCallSurvivesDisconnectAndTakeover(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	var executions atomic.Int32
	srv := rpc.NewServer()
	srv.Handle("project.add", func(ctx context.Context, raw json.RawMessage) (any, error) {
		executions.Add(1)
		close(entered)
		select {
		case <-release:
			return map[string]string{"id": "created"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	p := rpc.DesktopCall{Desktop: a, Sequence: 1, Method: "project.add", Params: json.RawMessage(`{"path":"/project","name":"one"}`)}
	finished := make(chan error, 1)
	go func() { _, err := desktopRequestCall(d, srv, ctx, p); finished <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write not accepted")
	}
	cancel()
	desktopErrorCode(t, <-finished, rpc.CodeOutcomeUnknown)
	if st := desktopOperationGet(t, d, p); st.State != "running" {
		t.Fatalf("disconnect changed accepted operation: %+v", st)
	}
	desktopTakeTest(t, d, "B")
	desktopErrorCode(t, d.desktop.blockHandoff(), rpc.CodeBusy)
	late := p
	late.Sequence++
	_, err := desktopRequestCall(d, srv, context.Background(), late)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	close(release)
	// JSON 的排版空白不影响去重；失去控制仍可取得已接受的结果。
	p.Params = json.RawMessage(`{ "path": "/project", "name": "one" }`)
	v, err := desktopRequestCall(d, srv, context.Background(), p)
	if err != nil || string(v.(json.RawMessage)) != `{"id":"created"}` || executions.Load() != 1 {
		t.Fatalf("duplicate: %s, %v, executions=%d", v, err, executions.Load())
	}
	if st := desktopOperationGet(t, d, p); st.State != "done" || string(st.Result) != `{"id":"created"}` {
		t.Fatalf("result not retained: %+v", st)
	}
	if err := d.desktop.blockHandoff(); err != nil {
		t.Fatal(err)
	}
	p.Params = json.RawMessage(`{"name":"different"}`)
	_, err = desktopRequestCall(d, srv, context.Background(), p)
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopCallCapacityAndShutdown(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	srv := rpc.NewServer()
	var cancelRequest context.CancelFunc
	var stopped atomic.Int32
	srv.Handle("project.add", func(ctx context.Context, _ json.RawMessage) (any, error) {
		cancelRequest()
		<-ctx.Done()
		stopped.Add(1)
		return nil, ctx.Err()
	})
	const slots = desktopCacheLimit / desktopResultLimit
	for i := 1; i <= slots; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancelRequest = cancel
		_, err := desktopRequestCall(d, srv, ctx, rpc.DesktopCall{Desktop: a, Method: "project.add", Sequence: uint64(i)})
		cancel()
		desktopErrorCode(t, err, rpc.CodeOutcomeUnknown)
	}
	_, err := desktopRequestCall(d, srv, context.Background(), rpc.DesktopCall{Desktop: a, Method: "project.add", Sequence: slots + 1})
	desktopErrorCode(t, err, rpc.CodeBusy)
	d.desktop.shutdown()
	if stopped.Load() != slots || d.desktop.active != 0 {
		t.Fatalf("shutdown returned before operations stopped: stopped=%d active=%d", stopped.Load(), d.desktop.active)
	}
	d.desktop.unblock()
	if !d.desktop.blocked {
		t.Fatal("late lock acquisition reopened stopped daemon")
	}
}

func TestDesktopCallEvictedResultIsNotReplayed(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	srv := rpc.NewServer()
	var count atomic.Int32
	srv.Handle("project.add", func(context.Context, json.RawMessage) (any, error) { return count.Add(1), nil })
	p := rpc.DesktopCall{Desktop: a, Method: "project.add", Sequence: 1, Params: json.RawMessage(`{"n":9007199254740992}`)}
	if _, err := desktopRequestCall(d, srv, context.Background(), p); err != nil {
		t.Fatal(err)
	}
	changed := p
	changed.Params = json.RawMessage(`{"n":9007199254740993}`)
	_, err := desktopRequestCall(d, srv, context.Background(), changed)
	desktopErrorCode(t, err, rpc.CodeConflict)
	ref := rpc.DesktopOperationRef{Desktop: a, Sequence: 1}
	d.desktop.mu.Lock()
	d.desktop.operations[ref].value.FinishedAt = time.Now().Add(-2 * desktopResultTTL).UnixMilli()
	d.desktop.mu.Unlock()
	if st := desktopOperationGet(t, d, p); st.State != "unknown" {
		t.Fatal("expired result remained available")
	}
	_, err = desktopRequestCall(d, srv, context.Background(), p)
	desktopErrorCode(t, err, rpc.CodeOutcomeUnknown)
	if count.Load() != 1 {
		t.Fatal("expired operation was executed again")
	}
	p.Desktop.InstanceID = "old"
	if st := desktopOperationGet(t, d, p); st.State != "unknown" {
		t.Fatal("another instance reused an operation")
	}
}

func TestDesktopCallPreservesStructuredError(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	srv := rpc.NewServer()
	srv.Handle("project.add", func(context.Context, json.RawMessage) (any, error) {
		return nil, &rpc.Error{Code: rpc.CodeConflict, Message: "conflict", Data: json.RawMessage(`{"current":"new"}`)}
	})
	p := rpc.DesktopCall{Desktop: a, Method: "project.add", Sequence: 1}
	_, err := desktopRequestCall(d, srv, context.Background(), p)
	desktopErrorCode(t, err, rpc.CodeConflict)
	st := desktopOperationGet(t, d, p)
	if st.Error == nil || string(st.Error.Data) != `{"current":"new"}` {
		t.Fatalf("structured result lost: %+v", st)
	}
}

func TestDesktopCallViewerReadsWithoutRestoring(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	id := desktopTestCall(t, d.desktopStatus, nil).Identity("viewer")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, method := range []string{"project.list", "workspace.list", "terminal.list", "task.list", "usage.get"} {
		if err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: id, Method: method}, nil); err != nil {
			t.Fatalf("%s: %v", method, err)
		}
	}
	for _, method := range []string{"terminal.show", "terminal.read", "terminal.wait"} {
		err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: id, Method: method, Params: json.RawMessage(`{"terminal":"not-live"}`)}, nil)
		desktopErrorCode(t, err, rpc.CodeReadOnly)
	}
	for _, method := range []string{"project.add", "workspace.rm", "terminal.create", "history.resume", "task.send"} {
		err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: id, Method: method, Sequence: 1}, nil)
		desktopErrorCode(t, err, rpc.CodeReadOnly)
	}
	for _, method := range []string{"hook.event", "terminal.send", "terminal.resize", "task.check", "desktop.call", "daemon.stop"} {
		err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: id, Method: method}, nil)
		desktopErrorCode(t, err, rpc.CodeUnknown)
	}
}

func TestDesktopStatusColorsOnlyFromController(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	colors := host.Colors{}
	colors.Background.R = 0x12
	d.desktopWindowReported(context.Background(), colors)
	if d.window.current() != nil {
		t.Fatal("legacy client changed controller colors")
	}
	viewer := context.WithValue(context.Background(), desktopCallContext{}, desktopRequestContext{identity: rpc.DesktopIdentity{ClientID: "viewer", InstanceID: "one"}, readOnly: true})
	d.desktopWindowReported(viewer, colors)
	if d.window.current() != nil {
		t.Fatal("viewer changed colors")
	}
	controller := context.WithValue(context.Background(), desktopCallContext{}, desktopRequestContext{identity: a, readOnly: true})
	d.desktopWindowReported(controller, colors)
	if got := d.window.current(); got == nil || *got != colors {
		t.Fatal("controller could not set colors")
	}
	desktopTakeTest(t, d, "B")
	d.desktopWindowReported(controller, host.Colors{})
	if *d.window.current() != colors {
		t.Fatal("old controller changed colors")
	}
}

func TestDesktopSequenceWindow(t *testing.T) {
	var s desktopSequences
	for _, n := range []uint64{2, 1, 64, 63, 1023, 1024, 2049} {
		if s.seen(n) {
			t.Fatalf("new sequence %d rejected", n)
		}
		s.add(n)
		if !s.seen(n) {
			t.Fatalf("replay %d accepted", n)
		}
	}
	if !s.seen(1024) || s.seen(2048) {
		t.Fatal("old sequence or out-of-order window not handled")
	}
}
