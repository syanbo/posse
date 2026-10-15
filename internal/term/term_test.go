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
}
