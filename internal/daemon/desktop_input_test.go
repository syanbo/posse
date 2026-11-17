package daemon

import (
	"bytes"
	"context"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestDesktopBlockedInputAllowsTakeoverAndStop(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec sleep 30"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "non-reading terminal ready", func() bool { return s.ContainsText("READY") })
	socket := desktopServer(t, d)
	a := desktopTakeTest(t, d, "A")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, reader, err := rpc.Open(ctx, socket, "terminal.attach", terminalAttachParams{Terminal: s.ID, Desktop: &a}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, _, err := rpc.ReadFrame(reader); err != nil {
		t.Fatal(err)
	}
	if err := rpc.WriteFrame(conn, rpc.FrameInput, bytes.Repeat([]byte("x"), 1<<20)); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "desktop input accepted", func() bool { return !s.LastInput().IsZero() })
	done := make(chan error, 1)
	go func() {
		var state rpc.DesktopState
		if err := rpc.Call(ctx, socket, "desktop.status", nil, &state); err != nil {
			done <- err
			return
		}
		done <- rpc.Call(ctx, socket, "desktop.take", rpc.DesktopRequest{ClientID: "B", InstanceID: state.InstanceID, Epoch: state.Epoch}, nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		s.Close(0)
		t.Fatal("blocked PTY prevented takeover")
	}
	stopped := make(chan struct{})
	go func() { d.closeAll(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		s.Close(0)
		t.Fatal("blocked PTY prevented normal stop")
	}
	if s.Running() {
		t.Fatal("normal stop left the terminal running")
	}
}
