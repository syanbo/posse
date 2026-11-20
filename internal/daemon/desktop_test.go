package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/rpc"
)

func desktopTestCall(t *testing.T, h rpc.Handler, p any) rpc.DesktopState {
	t.Helper()
	b, _ := json.Marshal(p)
	v, err := h(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return v.(rpc.DesktopState)
}

func desktopTakeTest(t *testing.T, d *Daemon, id string) rpc.DesktopIdentity {
	t.Helper()
	st := desktopTestCall(t, d.desktopStatus, nil)
	return desktopTestCall(t, d.desktopTake, rpc.DesktopRequest{InstanceID: d.instanceID, ClientID: id, Epoch: st.Epoch}).Identity(id)
}

func TestDesktopControlEpochAndLease(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	done, err := d.beginDesktopRestore(&a)
	if err != nil {
		t.Fatal(err)
	}
	b := desktopTakeTest(t, d, "B")
	if a.Epoch == b.Epoch {
		t.Fatal("control epoch reused")
	}
	called := false
	if d.desktopInput(&a, 0, func() { called = true }) || called {
		t.Fatal("stale input accepted")
	}
	raw, _ := json.Marshal(a)
	if _, err := d.desktopHeartbeat(context.Background(), raw); err == nil {
		t.Fatal("old heartbeat reclaimed control")
	}
	if _, err := d.desktopRelease(context.Background(), raw); err == nil {
		t.Fatal("old release cleared new owner")
	}
	if err := d.desktop.blockHandoff(); err == nil {
		t.Fatal("handoff accepted during restore")
	}
	done()
	if err := d.desktop.blockHandoff(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.beginDesktopRestore(&b); err == nil {
		t.Fatal("write accepted while handoff blocked")
	}
	d.desktop.unblock()
	d.desktop.mu.Lock()
	d.desktop.expires = time.Now().Add(-time.Second)
	d.desktop.mu.Unlock()
	st := desktopTestCall(t, d.desktopStatus, nil)
	if st.Owner != "" || st.Epoch == b.Epoch {
		t.Fatal("expired control retained")
	}
	if d.desktopInput(&b, 0, func() { called = true }) {
		t.Fatal("expired input accepted")
	}
	c := desktopTakeTest(t, d, "C")
	c.InstanceID = "previous-daemon"
	if d.desktopInput(&c, 0, func() { called = true }) {
		t.Fatal("previous daemon identity accepted")
	}
}

func TestDesktopAutomaticDoesNotSteal(t *testing.T) {
	d := &Daemon{instanceID: "one"}
	t.Cleanup(d.desktop.shutdown)
	a := desktopTakeTest(t, d, "A")
	st := desktopTestCall(t, d.desktopTake, rpc.DesktopRequest{InstanceID: d.instanceID, ClientID: "B", Epoch: a.Epoch, Automatic: true})
	if st.Owner != "A" || st.Epoch != a.Epoch {
		t.Fatal("automatic claim stole control")
	}
	st = desktopTestCall(t, d.desktopRelease, a)
	st = desktopTestCall(t, d.desktopTake, rpc.DesktopRequest{InstanceID: d.instanceID, ClientID: "B", Epoch: st.Epoch, Automatic: true})
	if st.Owner != "B" {
		t.Fatal("new local client could not claim vacant control")
	}
}

func desktopServer(t *testing.T, d *Daemon, configure ...func(*rpc.Server)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ps-desktop-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "s"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	d.ln = newGateListener(ln)
	ctx, cancel := context.WithCancel(context.Background())
	srv := rpc.NewServer()
	d.register(srv, cancel)
	for _, setup := range configure {
		setup(srv)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, d.ln) }()
	t.Cleanup(func() { cancel(); <-done; d.desktop.shutdown() })
	return ln.Addr().String()
}

func TestAttachReportsPhoneHeldSize(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "cat"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	attach := func() terminalAttachResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var result terminalAttachResult
		conn, _, err := rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Cols: 80, Rows: 24}, &result)
		if err != nil {
			t.Fatal(err)
		}
		conn.Close()
		waitFor(t, "attach detached", func() bool { return s.Terminal.(interface{ Subscribers() int }).Subscribers() == 0 })
		return result
	}
	if r := attach(); r.Held {
		t.Fatalf("free terminal reported held: %+v", r)
	}
	// 手机占的尺寸和窗口要的一样，单看尺寸分不出来，要靠 held
	s.HoldSize("phone", 80, 24)
	if r := attach(); !r.Held || r.Cols != 80 || r.Rows != 24 {
		t.Fatalf("held terminal: %+v", r)
	}
	s.ReleaseSize("phone")
	if r := attach(); r.Held {
		t.Fatalf("released terminal reported held: %+v", r)
	}
}

func TestDesktopViewerAndTakeover(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "cat"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	legacy, reader, err := rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Cols: 80, Rows: 24}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	if _, _, err := rpc.ReadFrame(reader); err != nil {
		t.Fatal(err)
	}
	a := desktopTakeTest(t, d, "A")
	legacy.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := rpc.ReadFrame(reader); err == nil {
		t.Fatal("legacy control connection survived takeover")
	}
	st := desktopTestCall(t, d.desktopStatus, nil)
	viewer := st.Identity("viewer")
	var result terminalAttachResult
	conn, reader, err := rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Cols: 150, Rows: 60, Desktop: &viewer}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ReadOnly || result.Cols != 80 || result.Rows != 24 {
		t.Fatalf("viewer changed terminal: %+v", result)
	}
	if _, _, err := rpc.ReadFrame(reader); err != nil {
		t.Fatal(err)
	}
	before := s.LastInput()
	rpc.WriteFrame(conn, rpc.FrameInput, []byte("viewer-input\n"))
	rpc.WriteFrame(conn, rpc.FrameResize, rpc.ResizePayload(120, 40))
	conn.Close()
	waitFor(t, "viewer detached", func() bool { return s.Terminal.(interface{ Subscribers() int }).Subscribers() == 0 })
	if cols, rows := s.Size(); cols != 80 || rows != 24 || !s.LastInput().Equal(before) {
		t.Fatal("viewer input or resize reached PTY")
	}
	controller, reader, err := rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Cols: 90, Rows: 25, Desktop: &a}, &result)
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	if _, _, err := rpc.ReadFrame(reader); err != nil {
		t.Fatal(err)
	}
	if err := rpc.WriteFrame(controller, rpc.FrameInput, []byte("controller-input\n")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "controller input", func() bool { return s.ContainsText("controller-input") })
	b := desktopTakeTest(t, d, "B")
	controller.SetReadDeadline(time.Now().Add(time.Second))
	for {
		if _, _, err := rpc.ReadFrame(reader); err != nil {
			break
		}
	}
	if d.desktopInput(&a, 0, func() { s.Resize(200, 90) }) {
		t.Fatal("late resize accepted")
	}
	if !d.desktopInput(&b, 0, func() { s.Resize(100, 30) }) {
		t.Fatal("new controller rejected")
	}
	if cols, rows := s.Size(); cols != 100 || rows != 30 {
		t.Fatal("new control did not reach terminal")
	}
	if !s.Running() {
		t.Fatal("takeover stopped terminal")
	}
}

func TestDesktopViewerDoesNotRestore(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "exec sleep 30"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.exiting.Store(true)
	d.closeAll()
	if _, err := d.store.MarkRunningLost(nil); err != nil {
		t.Fatal(err)
	}
	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	socket := desktopServer(t, d2)
	st := desktopTestCall(t, d2.desktopStatus, nil)
	viewer := st.Identity("viewer")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Desktop: &viewer}, nil)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
		t.Fatalf("view restore error: %v", err)
	}
	for _, method := range []string{"terminal.show", "terminal.read", "terminal.wait"} {
		params, _ := json.Marshal(terminalSel{Terminal: s.ID})
		err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: method, Params: params}, nil)
		desktopErrorCode(t, err, rpc.CodeReadOnly)
	}
	if d2.liveSession(s.ID) != nil {
		t.Fatal("viewing launched terminal")
	}
	if _, err := d2.store.LostTerminal(s.ID); err != nil {
		t.Fatal("viewing changed lost record")
	}
	a := desktopTakeTest(t, d2, "A")
	conn, _, err := rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Desktop: &a, Cols: 80, Rows: 24}, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if d2.liveSession(s.ID) == nil {
		t.Fatal("controller did not restore terminal")
	}
}
