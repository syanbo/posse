package app

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"posse/internal/client"
	"posse/internal/rpc"
)

type hostControlFixture struct {
	mu              sync.Mutex
	state           rpc.DesktopState
	takes           int
	heartbeats      int
	statusError     error
	takeError       error
	heartbeatFail   bool
	afterTake       func()
	beforeRelease   func()
	desktopCall     func(context.Context, rpc.DesktopCall) (any, error)
	operation       func(rpc.DesktopOperationRef) (any, error)
	cancelOperation func(rpc.DesktopOperationCancel) (any, error)
	attach          rpc.StreamHandler
	stop            func()
}

func newHostControlFixture(t *testing.T) (*hostControlFixture, *client.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "posse-host-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	f := &hostControlFixture{state: rpc.DesktopState{InstanceID: "instance", Epoch: 1}}
	srv := rpc.NewServer()
	srv.Handle("desktop.status", func(context.Context, json.RawMessage) (any, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.state, f.statusError
	})
	srv.Handle("desktop.take", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p rpc.DesktopRequest
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.takes++
		if f.takeError != nil {
			err := f.takeError
			f.mu.Unlock()
			return nil, err
		}
		if p.InstanceID != f.state.InstanceID || p.Epoch != f.state.Epoch {
			f.mu.Unlock()
			return nil, rpc.Errorf(rpc.CodeConflict, "desktop control changed; refresh before taking control")
		}
		// 与真实 daemon（internal/daemon/desktop.go）一致：自动接管允许本端同一 ClientID 换代重取
		if !p.Automatic || f.state.Owner == "" || f.state.Owner == p.ClientID {
			f.state.Epoch++
			f.state.Owner, f.state.Label = p.ClientID, p.Label
		}
		state, after := f.state, f.afterTake
		f.mu.Unlock()
		if after != nil {
			after()
		}
		return state, nil
	})
	for _, method := range []string{"desktop.heartbeat", "desktop.release"} {
		srv.Handle(method, func(ctx context.Context, raw json.RawMessage) (any, error) {
			var id rpc.DesktopIdentity
			if err := rpc.Decode(raw, &id); err != nil {
				return nil, err
			}
			f.mu.Lock()
			beforeRelease := f.beforeRelease
			f.mu.Unlock()
			if method == "desktop.release" && beforeRelease != nil {
				beforeRelease()
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if method == "desktop.heartbeat" {
				f.heartbeats++
			}
			if id != f.state.Identity(f.state.Owner) || f.state.Owner == "" || method == "desktop.heartbeat" && f.heartbeatFail {
				return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
			}
			if method == "desktop.release" {
				f.state.Epoch++
				f.state.Owner = ""
			}
			return f.state, nil
		})
	}
	srv.Handle("desktop.call", func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p rpc.DesktopCall
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		f.mu.Lock()
		call := f.desktopCall
		f.mu.Unlock()
		if call == nil {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected desktop call")
		}
		return call(ctx, p)
	})
	srv.Handle("desktop.operation", func(_ context.Context, raw json.RawMessage) (any, error) {
		var ref rpc.DesktopOperationRef
		if err := rpc.Decode(raw, &ref); err != nil {
			return nil, err
		}
		f.mu.Lock()
		call := f.operation
		f.mu.Unlock()
		if call == nil {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected operation query")
		}
		return call(ref)
	})
	srv.Handle("desktop.operation.cancel", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p rpc.DesktopOperationCancel
		if err := rpc.Decode(raw, &p); err != nil {
			return nil, err
		}
		f.mu.Lock()
		call := f.cancelOperation
		f.mu.Unlock()
		if call == nil {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected operation cancel")
		}
		return call(p)
	})
	srv.HandleStream("terminal.attach", func(ctx context.Context, raw json.RawMessage, conn net.Conn, reader *bufio.Reader) error {
		f.mu.Lock()
		attach := f.attach
		f.mu.Unlock()
		if attach == nil {
			return rpc.Errorf(rpc.CodeUnknown, "unexpected terminal attach")
		}
		return attach(ctx, raw, conn, reader)
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); srv.Serve(ctx, ln) }()
	f.stop = func() { cancel(); <-done }
	t.Cleanup(func() { f.stop(); os.RemoveAll(dir) })
	return f, &client.Client{Socket: socket}
}

func TestHostSessionDisconnectInvalidatesCallbacks(t *testing.T) {
	f, c := newHostControlFixture(t)
	h := newHostSession("local", "window")
	ctx := context.Background()
	if err := h.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	before := h.snapshot()
	identity := *before.Client.Desktop
	f.stop()
	if err := h.refresh(ctx); err == nil {
		t.Fatal("closed socket reported connected")
	}
	after := h.snapshot()
	if after.Connected || after.Controlled || after.Client != nil || h.matches(before) {
		t.Fatalf("offline state = %+v", after)
	}
	if *before.Client.Desktop != identity || after.Desktop != before.Desktop {
		t.Fatal("disconnect altered old identity or discarded last status")
	}
}

func TestHostSessionLocalAutomaticOnlyOnce(t *testing.T) {
	f, c := newHostControlFixture(t)
	ctx := context.Background()
	h := newHostSession("local", "local window")
	if err := h.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	first := h.snapshot()
	if !first.Controlled || !first.Connected || !h.matches(first) {
		t.Fatalf("initial = %+v", first)
	}
	identity := *first.Client.Desktop
	if err := h.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if h.snapshot().Client != first.Client {
		t.Fatal("heartbeat replaced immutable client")
	}
	lease := h.detach()
	if h.matches(first) || h.snapshot().Client != nil {
		t.Fatal("detached snapshot remains usable")
	}
	if err := lease.release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := h.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	if h.snapshot().Controlled {
		t.Fatal("reconnection automatically took control")
	}
	if *first.Client.Desktop != identity {
		t.Fatal("old client identity changed")
	}
	if err := h.take(ctx, first); err == nil {
		t.Fatal("stale confirmation took control")
	}
	f.mu.Lock()
	takes := f.takes
	f.mu.Unlock()
	if takes != 1 {
		t.Fatalf("automatic take count = %d", takes)
	}
}

func TestHostSessionTakeoverAndHeartbeatLoss(t *testing.T) {
	f, c := newHostControlFixture(t)
	ctx := context.Background()
	a, b := newHostSession("a", "A"), newHostSession("b", "B")
	if err := a.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	old := a.snapshot()
	if err := b.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	if b.snapshot().Controlled {
		t.Fatal("automatic connection stole control")
	}
	if err := b.take(ctx, b.snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := (hostLease{c, *old.Client.Desktop}).release(ctx); err == nil {
		t.Fatal("old release revoked new controller")
	}
	if err := a.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if a.snapshot().Controlled || a.matches(old) {
		t.Fatal("old controller remained writable")
	}
	if a.snapshot().Client.Desktop.ClientID == a.controllerID {
		t.Fatal("viewer retained controller identity")
	}
	if err := b.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	owned := b.snapshot()
	f.mu.Lock()
	f.heartbeatFail = true
	f.mu.Unlock()
	if err := b.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if b.snapshot().Controlled || b.snapshot().Client.Desktop.ClientID == owned.Desktop.Owner {
		t.Fatal("status restored uncertain control")
	}
	f.mu.Lock()
	f.heartbeatFail = false
	f.mu.Unlock()
	if err := b.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if b.snapshot().Controlled {
		t.Fatal("heartbeat recovery automatically restored control")
	}
	if err := b.take(ctx, b.snapshot()); err != nil {
		t.Fatal(err)
	}
	if !b.snapshot().Controlled {
		t.Fatal("explicit retake failed")
	}
}

func TestHostSessionRemoteAndInstanceIdentity(t *testing.T) {
	f, c := newHostControlFixture(t)
	c.Remote = true
	ctx := context.Background()
	a, b := newHostSession("host-a", "A"), newHostSession("host-b", "B")
	if err := a.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := b.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	first := a.snapshot()
	if first.Controlled || b.matches(first) {
		t.Fatal("remote connected with control or shared resource identity")
	}
	f.mu.Lock()
	takes := f.takes
	f.state = rpc.DesktopState{InstanceID: "replacement", Epoch: 1}
	f.mu.Unlock()
	if takes != 0 {
		t.Fatalf("remote automatically took control %d times", takes)
	}
	if err := a.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if a.matches(first) || first.Client.Desktop.InstanceID != "instance" || a.snapshot().Client.Desktop.InstanceID != "replacement" {
		t.Fatal("daemon replacement reused an old binding")
	}
}

func TestHostSessionQueuedConnectInvalidated(t *testing.T) {
	_, c := newHostControlFixture(t)
	h := newHostSession("local", "window")
	generation := h.snapshot().Generation
	h.opMu.Lock()
	done := make(chan error, 1)
	go func() { done <- h.connectAt(context.Background(), c, generation) }()
	h.detach()
	h.opMu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued connect = %v", err)
	}
	if h.snapshot().Connected || h.snapshot().Client != nil {
		t.Fatal("queued connection resurrected detached host")
	}
}

func TestHostSessionLostTakeReplyReconcilesLease(t *testing.T) {
	f, c := newHostControlFixture(t)
	c.Remote = true
	h := newHostSession("remote", "window")
	if err := h.connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	accepted, resume := make(chan struct{}), make(chan struct{})
	defer close(resume)
	f.mu.Lock()
	f.afterTake = func() { close(accepted); <-resume }
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.take(ctx, h.snapshot()) }()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("take did not reach server")
	}
	h.detach()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled response reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("lost response cleanup did not finish")
	}
	f.mu.Lock()
	owner, takes := f.state.Owner, f.takes
	f.mu.Unlock()
	if owner != "" || takes != 1 {
		t.Fatalf("unknown take: owner=%q attempts=%d", owner, takes)
	}
	if h.snapshot().Connected {
		t.Fatal("reconciliation restored detached host")
	}
}

func TestHostSessionLateTakeReleasesOnlyItsLease(t *testing.T) {
	f, c := newHostControlFixture(t)
	c.Remote = true
	ctx := context.Background()
	h := newHostSession("remote", "window")
	if err := h.connect(ctx, c); err != nil {
		t.Fatal(err)
	}
	accepted, resume := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.afterTake = func() { close(accepted); <-resume }
	f.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- h.take(ctx, h.snapshot()) }()
	select {
	case <-accepted:
	case <-time.After(5 * time.Second):
		t.Fatal("take did not reach server")
	}
	h.detach()
	close(resume)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("late take = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("late take did not finish")
	}
	if h.snapshot().Connected || h.snapshot().Client != nil {
		t.Fatal("late take resurrected detached host")
	}
	f.mu.Lock()
	owner := f.state.Owner
	f.mu.Unlock()
	if owner != "" {
		t.Fatalf("late take left owner %q", owner)
	}
}

func TestHostSessionStatusFailureDoesNotConsumeAutomatic(t *testing.T) {
	f, c := newHostControlFixture(t)
	h := newHostSession(localHostID, "window")
	f.mu.Lock()
	f.statusError = rpc.Errorf(rpc.CodeBusy, "not ready")
	f.mu.Unlock()
	if err := h.connect(context.Background(), c); err == nil {
		t.Fatal("status failure reported success")
	}
	f.mu.Lock()
	f.statusError = nil
	f.mu.Unlock()
	if err := h.connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !h.snapshot().Controlled {
		t.Fatal("first successful status did not take control")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.takes != 1 {
		t.Fatalf("takes = %d", f.takes)
	}
}

func TestHostSessionAutomaticPerInstance(t *testing.T) {
	for _, mode := range []string{"empty", "occupied", "remote", "reconnect"} {
		t.Run(mode, func(t *testing.T) {
			f, c := newHostControlFixture(t)
			c.Remote = mode == "remote"
			h := newHostSession(localHostID, "window")
			ctx := context.Background()
			if err := h.connect(ctx, c); err != nil {
				t.Fatal(err)
			}
			before := h.snapshot()
			if mode == "reconnect" {
				if err := h.detach().release(ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			initialTakes := f.takes
			f.state = rpc.DesktopState{InstanceID: "replacement", Epoch: 1}
			if mode == "occupied" {
				f.state.Owner = "other-window"
			}
			f.mu.Unlock()
			var err error
			if mode == "reconnect" {
				err = h.connect(ctx, c)
			} else {
				err = h.refresh(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			controlled := mode == "empty" || mode == "reconnect"
			if h.snapshot().Controlled != controlled || h.matches(before) {
				t.Fatal("instance change reused old control or did not start the empty local session")
			}
			f.mu.Lock()
			got := f.takes - initialTakes
			f.state.Owner = ""
			f.state.Epoch++
			f.mu.Unlock()
			want := 0
			if controlled {
				want = 1
			}
			if got != want {
				t.Fatalf("new instance takes = %d, want %d", got, want)
			}
			if err := h.refresh(ctx); err != nil {
				t.Fatal(err)
			}
			if h.snapshot().Controlled {
				t.Fatal("empty owner restored control in the same instance")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.takes != initialTakes+want {
				t.Fatal("same instance retried automatic take")
			}
		})
	}
}

func TestHostSessionLostAutomaticReply(t *testing.T) {
	f, c := newHostControlFixture(t)
	h := newHostSession(localHostID, "window")
	entered, finish := make(chan struct{}), make(chan struct{})
	defer close(finish)
	f.mu.Lock()
	f.afterTake = func() { close(entered); <-finish }
	f.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.connect(ctx, c) }()
	waitConnectionEvent(t, entered)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("lost automatic reply reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("automatic take cleanup did not finish")
	}
	f.mu.Lock()
	owner, takes := f.state.Owner, f.takes
	f.mu.Unlock()
	if owner != "" || takes != 1 || h.snapshot().Controlled {
		t.Fatal("unknown automatic take was replayed or retained control")
	}
	if err := h.connect(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if h.snapshot().Controlled {
		t.Fatal("reconnect retried uncertain automatic take")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.takes != 1 {
		t.Fatalf("automatic take attempts = %d", f.takes)
	}
}

func TestHostSessionAutoTakeBusyKeepsViewing(t *testing.T) {
	f, c := newHostControlFixture(t)
	c.Remote = true
	f.mu.Lock()
	f.takeError = rpc.Errorf(rpc.CodeBusy, "handoff in progress")
	f.mu.Unlock()
	h := newHostSession("remote", "window")
	if err := h.connectRemoteAt(context.Background(), c, h.snapshot().Generation); err != nil {
		t.Fatalf("connectRemoteAt returned error on busy take: %v", err)
	}
	snap := h.snapshot()
	if !snap.Connected || snap.Controlled {
		t.Fatalf("busy auto-take should preserve viewing session, got %+v", snap)
	}
}
