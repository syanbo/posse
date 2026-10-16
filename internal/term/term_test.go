package term

import (
	"strings"
	"testing"
	"time"
)

func TestEchoAndQueries(t *testing.T) {
	// 程序发出设备查询并等待回复：常驻进程内的终端内核必须应答
	script := `stty raw -echo; printf '\033[c'; IFS= read -t 2 -r -d c a; stty sane; echo "reply=${a#?} 你好"`
	tm, err := Start(Options{ID: "t_test", Dir: t.TempDir(), Command: []string{"/bin/bash", "--norc", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	select {
	case <-tm.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("program did not exit")
	}
	time.Sleep(100 * time.Millisecond)
	text := strings.Join(tm.Lines(), "\n")
	if !strings.Contains(text, "reply=[?") || !strings.Contains(text, "你好") {
		t.Fatalf("unexpected output:\n%s", text)
	}
	if tm.ExitCode() != 0 {
		t.Fatalf("exit code %d", tm.ExitCode())
	}
}

func TestEnvironStripsHostAgentVars(t *testing.T) {
	t.Setenv("ORCA_PANE_KEY", "x")
	t.Setenv("CLAUDECODE", "1")
	env := strings.Join(Environ([]string{"HIVE_TERMINAL_ID=t_1"}), "\n")
	for _, bad := range []string{"ORCA_PANE_KEY=", "CLAUDECODE="} {
		if strings.Contains(env, bad) {
			t.Fatalf("environment leaks %s", bad)
		}
	}
	if !strings.Contains(env, "HIVE_TERMINAL_ID=t_1") || !strings.Contains(env, "TERM=xterm-256color") {
		t.Fatal("missing expected variables")
	}
	// 程序据此把 [Image #N]、路径输出成超链接；外面设的值（比如 0）不能盖过它
	t.Setenv("FORCE_HYPERLINK", "0")
	env = strings.Join(Environ(nil), "\n")
	if !strings.Contains(env, "FORCE_HYPERLINK=1") || strings.Contains(env, "FORCE_HYPERLINK=0") {
		t.Fatalf("FORCE_HYPERLINK not forced on: %q", env)
	}
}

func TestContainsTextMatchesLines(t *testing.T) {
	script := `printf 'alpha beta   \n\tgamma\t\n\nlast line'; sleep 5`
	tm, err := Start(Options{ID: "t_text", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}, Cols: 40, Rows: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !tm.ContainsText("last line") {
		if time.Now().After(deadline) {
			t.Fatalf("output never appeared:\n%s", strings.Join(tm.Lines(), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, sub := range []string{"alpha", "beta", "beta ", "beta   ", "\tgamma", "gamma\t", "a\n", "beta\n\tgamma", "last line", "missing", " "} {
		want := false
		for _, l := range tm.Lines() {
			if strings.Contains(l, sub) {
				want = true
			}
		}
		if got := tm.ContainsText(sub); got != want {
			t.Errorf("ContainsText(%q) = %v, want %v", sub, got, want)
		}
	}
	if c, r := tm.Size(); c != 40 || r != 10 {
		t.Fatalf("Size = %d, %d", c, r)
	}
	tm.Resize(50, 12)
	if c, r := tm.Size(); c != 50 || r != 12 {
		t.Fatalf("Size after resize = %d, %d", c, r)
	}
}

// 不取输出的接入者排队字节数有上限，超过后被断开。
func TestStalledSubscriberIsBounded(t *testing.T) {
	tm, err := Start(Options{ID: "t_flood", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", `head -c 12000000 /dev/zero | tr '\0' x`}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	_, sub := tm.Attach(0, 0)
	select {
	case <-tm.Exited():
	case <-time.After(20 * time.Second):
		t.Fatal("program did not exit")
	}
	total := 0
	for chunk := range sub.C {
		total += len(chunk)
	}
	if total > maxQueuedBytes || total == 0 {
		t.Fatalf("stalled subscriber got %d bytes queued", total)
	}
}

// Clear 清掉屏幕和回滚区：之后的快照、行里都没有旧内容，接入者收到同样的清屏；redraw 时程序收到 Ctrl+L。
func TestClear(t *testing.T) {
	script := `seq 1 200; stty raw -echo; od -An -c -N1`
	tm, err := Start(Options{ID: "t_clear", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}, Cols: 40, Rows: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for !tm.ContainsText("200") {
		if time.Now().After(deadline) {
			t.Fatal("output never appeared")
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // 等 stty 生效
	_, sub := tm.Attach(0, 0)
	if err := tm.Clear(true); err != nil {
		t.Fatal(err)
	}
	if got := <-sub.C; string(got) != string(clearSeq) {
		t.Fatalf("subscriber got %q", got)
	}
	for _, l := range tm.Lines() {
		if strings.TrimSpace(l) != "" && !strings.Contains(l, "\\f") && !strings.Contains(l, "014") {
			t.Fatalf("old output left after clear: %q", tm.Lines())
		}
	}
	deadline = time.Now().Add(5 * time.Second)
	for !tm.ContainsText(`\f`) && !tm.ContainsText("014") {
		if time.Now().After(deadline) {
			t.Fatalf("program never got Ctrl+L: %q", tm.Lines())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
