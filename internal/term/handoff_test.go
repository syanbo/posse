package term

import (
	"strings"
	"testing"
	"time"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPauseAdopt(t *testing.T) {
	// 标题在一个 OSC 里，"半个"中文字符留在读到的末尾：交接后都要接上
	script := `printf '\033]2;hello\007'; echo ready; read x; printf 'got %s \344\275' "$x"; sleep 0.2; printf '\240\n'; read y; exit 7`
	old, err := Start(Options{ID: "t_h", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close(time.Second)
	waitFor(t, "ready", func() bool { return old.ContainsText("ready") && old.Title() == "hello" })

	st, f, err := old.Pause()
	if err != nil {
		t.Fatal(err)
	}
	// 暂停期间程序的输出由内核攒着，交接后新终端读到
	f.Write([]byte("abc\n"))
	time.Sleep(100 * time.Millisecond)
	if old.ContainsText("got abc") {
		t.Fatal("paused terminal kept reading")
	}

	exited := make(chan int, 1)
	nt, err := Adopt(Options{OnExit: func(_ *Terminal, code int) { exited <- code }}, st, f)
	if err != nil {
		t.Fatal(err)
	}
	defer nt.Close(time.Second)
	if nt.Title() != "hello" || nt.Pid() != old.Pid() || !nt.ContainsText("ready") {
		t.Fatalf("adopted state: title %q pid %d/%d lines %q", nt.Title(), nt.Pid(), old.Pid(), nt.Lines())
	}
	waitFor(t, "output after handoff", func() bool { return nt.ContainsText("got abc 你") })
	if err := nt.Send([]byte("z\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-exited:
		if code != 7 {
			t.Fatalf("exit code %d, want 7", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("adopted terminal did not see the exit")
	}
	if nt.Running() || nt.ExitCode() != 7 {
		t.Fatalf("running %v code %d", nt.Running(), nt.ExitCode())
	}
}

func TestPauseResume(t *testing.T) {
	tm, err := Start(Options{ID: "t_r", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", `read x; echo "got $x"; sleep 5`}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	_, f, err := tm.Pause()
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	tm.Send([]byte("abc\n"))
	time.Sleep(100 * time.Millisecond)
	if tm.ContainsText("got abc") {
		t.Fatal("paused terminal kept reading")
	}
	tm.Resume()
	waitFor(t, "output after resume", func() bool { return tm.ContainsText("got abc") })
	// 恢复后调尺寸不能把 PTY 改回阻塞模式，第二次暂停照样停得下来
	tm.Resize(100, 30)
	if _, f, err = tm.Pause(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tm.Resume()
}

func TestAdoptEnded(t *testing.T) {
	tm, err := Start(Options{ID: "t_e", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "echo bye; exit 3"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	<-tm.Exited()
	waitFor(t, "bye", func() bool { return tm.ContainsText("bye") })
	st, f, err := tm.Pause()
	if err != nil || f != nil || !st.Ended || st.ExitCode != 3 {
		t.Fatalf("pause ended: %+v %v %v", st.Ended, f, err)
	}
	nt, err := Adopt(Options{}, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer nt.Close(time.Second)
	if nt.Running() || nt.ExitCode() != 3 || !nt.ContainsText("bye") {
		t.Fatalf("adopted ended terminal: running %v code %d", nt.Running(), nt.ExitCode())
	}
	if _, sub := nt.Attach(0, 0); sub != nil {
		if _, ok := <-sub.C; ok {
			t.Fatal("ended terminal stream should be closed")
		}
	}
}

func TestCarry(t *testing.T) {
	cases := []struct{ in, tail string }{
		{"plain", ""},
		{"a\x1b", "\x1b"},
		{"a\x1b[38;5", "\x1b[38;5"},
		{"a\x1b[1mb", ""},
		{"x\x1b]2;tit", "\x1b]2;tit"},
		{"x\x1b]2;t\x07y", ""},
		{"x\x1b]8;;u\x1b", "\x1b]8;;u\x1b"},
		{"x\x1b]8;;u\x1b\\", ""},
		{"中\xe6\x96", "\xe6\x96"},
		{"\x1b(", "\x1b("},
		{"\x1b(B", ""},
	}
	for _, c := range cases {
		if got := string(carry(nil, []byte(c.in))); got != c.tail {
			t.Errorf("carry(%q) = %q, want %q", c.in, got, c.tail)
		}
	}
	// 跨多次读取的序列
	tail := carry(nil, []byte("a\x1b]2;ti"))
	tail = carry(tail, []byte("tle"))
	if string(tail) != "\x1b]2;title" {
		t.Fatalf("tail %q", tail)
	}
	if tail = carry(tail, []byte("\x07ok\xe4")); string(tail) != "\xe4" {
		t.Fatalf("tail %q", tail)
	}
	if !strings.HasPrefix(string(carry(nil, []byte("\x1bP"+strings.Repeat("x", 10)))), "\x1bP") {
		t.Fatal("DCS start not kept")
	}
	if carry(nil, []byte("\x1b_"+strings.Repeat("x", maxTail+1))) != nil {
		t.Fatal("oversized tail kept")
	}
}

func TestAdoptRestoresDeskSize(t *testing.T) {
	tm, err := Start(Options{ID: "t_d", Dir: t.TempDir(), Cols: 100, Rows: 30, Command: []string{"/bin/sh", "-c", "sleep 5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	tm.HoldSize("phone", 40, 20)
	st, f, err := tm.Pause()
	if err != nil {
		t.Fatal(err)
	}
	if st.Cols != 40 || st.DeskCols != 100 || st.DeskRows != 30 {
		t.Fatalf("state size %dx%d desk %dx%d", st.Cols, st.Rows, st.DeskCols, st.DeskRows)
	}
	nt, err := Adopt(Options{}, st, f)
	if err != nil {
		t.Fatal(err)
	}
	defer nt.Close(time.Second)
	waitFor(t, "desk size", func() bool { c, r := nt.Size(); return c == 100 && r == 30 })
}
