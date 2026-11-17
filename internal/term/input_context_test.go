package term

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

func TestSendContextStopsAtFullPTY(t *testing.T) {
	tm, err := Start(Options{ID: "blocked-input", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "stty raw -echo; printf READY; exec sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		changed := tm.Changed()
		if tm.ContainsText("READY") {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("terminal did not become ready")
		case <-changed:
		}
	}
	writeCtx, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	if err := tm.SendContext(writeCtx, bytes.Repeat([]byte("x"), 1<<20)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("full PTY write: %v", err)
	}
	// 不改变 PTY 的共享期限，另一类输入仍可等待；取消等待写入门禁也应及时返回。
	blocked := make(chan error, 1)
	go func() { blocked <- tm.Inject(bytes.Repeat([]byte("y"), 1<<20)) }()
	short, shortCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer shortCancel()
	if err := tm.SendContext(short, []byte("z")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued input: %v", err)
	}
	tm.Close(0)
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("PTY close did not release input writer")
	}
}

func TestWatchSizeReconnectsOnResize(t *testing.T) {
	tm, err := Start(Options{ID: "view-grid", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "exec sleep 30"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	_, mobile := tm.Watch()
	defer tm.Detach(mobile)
	_, viewer, c, r := tm.WatchSize()
	defer tm.Detach(viewer)
	if c != 80 || r != 24 || !tm.answer.Load() {
		t.Fatal("viewer changed size or query ownership")
	}
	tm.Resize(120, 24)
	if !viewer.Kicked() {
		t.Fatal("viewer kept parsing the old grid")
	}
	select {
	case _, ok := <-viewer.C:
		if ok {
			t.Fatal("viewer stream still open")
		}
	default:
		t.Fatal("viewer not disconnected")
	}
	select {
	case _, ok := <-mobile.C:
		if !ok {
			t.Fatal("desktop resize disconnected mobile Watch")
		}
	default:
	}
	_, next, c, r := tm.WatchSize()
	defer tm.Detach(next)
	if c != 120 || r != 24 {
		t.Fatalf("new snapshot grid=%dx%d", c, r)
	}
	_, controller := tm.Attach(120, 24)
	if tm.answer.Load() {
		t.Fatal("controller did not take query ownership")
	}
	tm.Detach(controller)
	if !tm.answer.Load() {
		t.Fatal("query ownership did not return to daemon")
	}
}
