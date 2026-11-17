package daemon

import (
	"errors"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopStopWaitsForAcceptedLaunch(t *testing.T) {
	d, w := newTestDaemon(t)
	d.handoff.startMu.RLock()
	done := make(chan struct{})
	go func() { d.closeAll(); close(done) }()
	waitFor(t, "shutdown admission closed", func() bool { return d.exiting.Load() })
	select {
	case <-done:
		d.handoff.startMu.RUnlock()
		t.Fatal("shutdown skipped accepted launch")
	default:
	}
	d.handoff.startMu.RUnlock()
	<-done
	_, err := d.startTerminal(w, []string{"/bin/sh", "-c", "exit 0"}, "", 80, 24)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeRestarting {
		t.Fatalf("late launch: %v", err)
	}
	if len(d.terms) != 0 {
		t.Fatal("shutdown admitted a new terminal")
	}
}
