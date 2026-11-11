package daemon

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/rpc"
)

// 窗口等 terminal.attach 的回复超时（这里是恢复终端卡着）后重新接入：先前那次接入在常驻进程里随连接关掉收场，
// 不留订阅者；两次接入只拉起一个进程。
func TestAttachTimeoutThenReattach(t *testing.T) {
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

	dir, err := os.MkdirTemp("/tmp", "at")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := rpc.NewServer()
	srv.HandleStream("terminal.attach", d2.terminalAttach)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	params := map[string]any{"terminal": s.ID, "cols": 80, "rows": 24}
	d2.restoreMu.Lock() // 恢复卡住：常驻进程迟迟不回
	short, cancelShort := context.WithTimeout(context.Background(), 200*time.Millisecond)
	_, _, err = rpc.Open(short, sock, "terminal.attach", params, nil)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) {
		d2.restoreMu.Unlock()
		t.Fatalf("first attach: %v", err)
	}
	// 窗口重新接入，这时第一次接入还卡在常驻进程里
	type opened struct {
		conn net.Conn
		err  error
	}
	again := make(chan opened, 1)
	go func() {
		c, _, err := rpc.Open(context.Background(), sock, "terminal.attach", params, nil)
		again <- opened{c, err}
	}()
	time.Sleep(100 * time.Millisecond)
	d2.restoreMu.Unlock()
	o := <-again
	if o.err != nil {
		t.Fatalf("reattach: %v", o.err)
	}
	r := d2.liveSession(s.ID)
	if r == nil {
		t.Fatal("not restored")
	}
	d2.mu.Lock()
	n := len(d2.terms)
	d2.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d terminals after two attaches", n)
	}
	subs := func() int { return r.Terminal.(interface{ Subscribers() int }).Subscribers() }
	waitFor(t, "only the second attach subscribed", func() bool { return subs() == 1 })
	o.conn.Close()
	waitFor(t, "no subscribers after closing", func() bool { return subs() == 0 })
}
