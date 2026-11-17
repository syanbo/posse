package app

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/client"
	"posse/internal/rpc"
)

type hostAttachEvent struct {
	identity rpc.DesktopIdentity
	terminal string
	closed   chan struct{}
}

func hostAttachRecorder(f *hostControlFixture) <-chan hostAttachEvent {
	events := make(chan hostAttachEvent, 8)
	f.mu.Lock()
	f.attach = func(ctx context.Context, raw json.RawMessage, conn net.Conn, reader *bufio.Reader) error {
		var p struct {
			Terminal string               `json:"terminal"`
			Desktop  *rpc.DesktopIdentity `json:"desktop"`
		}
		if err := rpc.Decode(raw, &p); err != nil {
			return err
		}
		if p.Desktop == nil {
			return rpc.Errorf(rpc.CodeReadOnly, "desktop identity required")
		}
		closed := make(chan struct{})
		defer close(closed)
		f.mu.Lock()
		readOnly := *p.Desktop != f.state.Identity(f.state.Owner)
		f.mu.Unlock()
		rpc.WriteResult(conn, map[string]any{"cols": 80, "rows": 24, "readOnly": readOnly})
		if err := rpc.WriteFrame(conn, rpc.FrameSnapshot, []byte("connected")); err != nil {
			return err
		}
		events <- hostAttachEvent{*p.Desktop, p.Terminal, closed}
		_, err := io.Copy(io.Discard, reader)
		return err
	}
	f.mu.Unlock()
	return events
}

func awaitHostAttach(t *testing.T, events <-chan hostAttachEvent) hostAttachEvent {
	t.Helper()
	select {
	case e := <-events:
		return e
	case <-time.After(3 * time.Second):
		t.Fatal("terminal did not attach with desktop identity")
	}
	return hostAttachEvent{}
}

func awaitHostClose(t *testing.T, closed <-chan struct{}) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("old terminal connection remained open")
	}
}

func awaitHostUpdate(t *testing.T, a *app) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		a.pendingMu.Lock()
		n := len(a.pending)
		a.pendingMu.Unlock()
		if n != 0 {
			a.runPending()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("UI result was not queued")
}

func TestHostTerminalIdentityAndReconnect(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	events := hostAttachRecorder(f)
	ids := [maxColumns]string{"same-t"}
	a.ensurePanes(ids)
	t.Cleanup(func() { a.ensurePanes([maxColumns]string{}) })
	first := a.panes[0]
	if first == nil {
		t.Fatal("viewer was not attached")
	}
	first.term.Resize(80, 24)
	viewer := awaitHostAttach(t, events)
	if viewer.identity != *a.request().client.Desktop || !first.term.ReadOnly() {
		t.Fatal("viewer identity or mode lost")
	}
	first.find.query, first.find.open = "needle", true
	if err := a.host.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.ensurePanes(ids)
	if a.panes[0] != first {
		t.Fatal("heartbeat unnecessarily recreated renderer")
	}

	takeRequestTestControl(t, a)
	if a.live(first) {
		t.Fatal("stale renderer still considered live")
	}
	a.ensurePanes(ids)
	second := a.panes[0]
	if second == nil || second == first || second.find.query != "needle" || !second.find.open {
		t.Fatal("identity change lost renderer state or reused old renderer")
	}
	second.term.Resize(80, 24)
	controller := awaitHostAttach(t, events)
	awaitHostClose(t, viewer.closed)
	if controller.identity != *a.request().client.Desktop || controller.identity == viewer.identity {
		t.Fatal("controller reused viewer identity")
	}
	deadline := time.Now().Add(time.Second)
	for second.term.ReadOnly() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if second.term.ReadOnly() {
		t.Fatal("controller renderer remained read-only")
	}
	a.runPending()
	if a.panes[0] != second {
		t.Fatal("late exit callback removed new renderer")
	}

	a.host.detach()
	a.ensurePanes(ids)
	awaitHostClose(t, controller.closed)
	if a.panes[0] != nil || a.attachView("same-t") != nil {
		t.Fatal("disconnected host attached through base client")
	}
}

func TestHostTerminalSkillUsesCapturedIdentity(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	events := hostAttachRecorder(f)
	created := make(chan rpc.DesktopIdentity, 1)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "terminal.create" {
			created <- p.Desktop
			return terminalInfo{ID: "skill-t"}, nil
		}
		return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected desktop call")
	}
	f.mu.Unlock()
	a.askSkillCommand(appL(), "Install", "", "true", &workspace{ID: "w"}, true)
	awaitHostUpdate(t, a)
	if a.skillRun.term == nil || !a.hiddenTerms["skill-t"] {
		t.Fatalf("skill terminal not attached: %s", a.skillRun.err)
	}
	a.skillRun.term.Resize(80, 24)
	attached := awaitHostAttach(t, events)
	if attached.identity != <-created || attached.terminal != "skill-t" {
		t.Fatal("skill create and attach used different identities")
	}
	a.detachSkillRun()
	awaitHostClose(t, attached.closed)
	if a.hiddenTerms["skill-t"] || a.skillRun.term != nil {
		t.Fatal("detaching left hidden renderer or terminal")
	}
}

func TestHostTerminalSkillOldConfirmation(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		calls.Add(1)
		return terminalInfo{ID: "wrong"}, nil
	}
	f.mu.Unlock()
	a.askSkillCommand(appL(), "Install", "", "true", &workspace{ID: "w"}, false)
	takeRequestTestControl(t, a)
	a.startSkillRun(appL())
	awaitHostUpdate(t, a)
	if calls.Load() != 0 || a.skillRun.starting || a.skillRun.err == "" || a.skillRun.term != nil {
		t.Fatal("old confirmation acquired a new desktop identity")
	}
}

func TestHostTerminalSkillLateCreateAfterSwitch(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	accepted, finish := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "terminal.create" {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected desktop call")
		}
		close(accepted)
		select {
		case <-finish:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return terminalInfo{ID: "skill-t"}, nil
	}
	f.mu.Unlock()
	v := newHostViews(a)
	v.add("other", &app{c: &client.Client{Remote: true}})
	a.askSkillCommand(appL(), "Install", "", "true", &workspace{ID: "w"}, true)
	select {
	case <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("create was not accepted")
	}
	v.activate("other")
	close(finish)
	awaitHostUpdate(t, a)
	if a.skillRun.term != nil || a.skillRun.starting || a.skillRun.open || len(a.hiddenTerms) != 0 {
		t.Fatal("late creation reopened hidden skill dialog")
	}
}
