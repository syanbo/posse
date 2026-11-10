package daemon

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"posse/internal/store"
)

// 前面的大帧对方还没读完就发描述符：macOS 上 sendmsg 直接报 EMSGSIZE，不会等，sendFile 要自己等对方读走再发。
func TestSendFileAfterLargeFrame(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "sock")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	snd, rcv := conn(fds[0]), conn(fds[1])
	defer snd.Close()
	defer rcv.Close()
	frame := make([]byte, 4<<20)
	got := make(chan error, 1)
	go func() {
		// 对方慢一点才开始读，和刚拉起的新进程一样
		time.Sleep(200 * time.Millisecond)
		if _, err := io.ReadFull(rcv, make([]byte, len(frame))); err != nil {
			got <- err
			return
		}
		f, err := recvFile(rcv, "pty")
		if err == nil {
			f.Close()
		}
		got <- err
	}()
	go func() {
		if _, err := snd.Write(frame); err != nil {
			got <- err
		}
	}()
	time.Sleep(50 * time.Millisecond)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if err := sendFile(snd, r); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}

func TestHandoffCarriesDiscussionReadiness(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.discussionTurn = discussionTurn{ID: "turn-1", Stopped: true}
	rec, f, err := s.Pause()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(handoffBatch{Version: handoffVersion, Terms: []handoffTerm{{Term: rec, Agent: "codex", DiscussionTurn: s.discussionTurn}}})
	if err != nil {
		t.Fatal(err)
	}
	var batch handoffBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		t.Fatal(err)
	}
	d2, _ := newTestDaemon(t)
	if err := d2.adopt(&incoming{batch: batch, ptys: []*os.File{f}}); err != nil {
		t.Fatal(err)
	}
	got := d2.terms[s.ID]
	got.Resume()
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()
	if !got.discussionReady() {
		t.Fatal("handoff lost completed turn")
	}
	finish, ok := got.reserveDiscussion()
	if !ok {
		t.Fatal("adopted turn cannot be claimed")
	}
	finish(true)
	if got.discussionReady() || !got.discussionDelivery() {
		t.Fatal("adopted turn cannot deliver exactly one next round")
	}
}

// 交接过来后对账失败不退出（交来的终端都在这个进程里），普通启动照旧报错。
func TestReconcileRecordsAfterHandoff(t *testing.T) {
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "posse.db"))
	if err != nil {
		t.Fatal(err)
	}
	st.Close() // 之后每一步都报错
	if err := reconcileRecords(st, []string{"t1"}, true); err != nil {
		t.Fatalf("handoff: %v", err)
	}
	if err := reconcileRecords(st, nil, false); err == nil {
		t.Fatal("normal start must still fail")
	}

	// 交接过来的终端接着跑：对账失败也不碰它
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileRecords(st, []string{s.ID}, true); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	_, ok := d.terms[s.ID]
	d.mu.Unlock()
	select {
	case <-s.Exited():
		ok = false
	default:
	}
	if !ok {
		t.Fatal("adopted terminal gone")
	}
}

// 交接前几秒刚退出的终端（旧进程还在等 resumeSettle 才记退出）：新进程记成已退出，不当成等着恢复的 lost。
func TestHandoffRecordsRecentExit(t *testing.T) {
	defer func(v time.Duration) { resumeSettle = v }(resumeSettle)
	resumeSettle = time.Hour // 旧进程的记录还没来得及写
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "exit 3"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("terminal did not exit")
	}
	rec, _, err := s.Pause()
	if err != nil {
		t.Fatal(err)
	}
	if !rec.Ended {
		t.Fatal("exited terminal not exported as ended")
	}
	in := &incoming{batch: handoffBatch{Version: handoffVersion, Terms: []handoffTerm{{Term: rec}}}}
	d.recordEndedAdopted(in.batch.Terms)
	if err := reconcileRecords(d.store, in.ids(), true); err != nil {
		t.Fatal(err)
	}
	lost, err := d.store.LostTerminals(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(lost) != 0 {
		t.Fatalf("exited terminal recorded as lost: %+v", lost)
	}
}
