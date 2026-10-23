package term

import (
	"strings"
	"testing"
	"time"

	"hive/internal/vt"
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
	// 从 agent 的工具调用里拉起 Hive.app 时带进来的非交互环境
	t.Setenv("NO_COLOR", "1")
	t.Setenv("FORCE_COLOR", "0")
	t.Setenv("GIT_EDITOR", "true")
	t.Setenv("GROK_AGENT", "1")
	t.Setenv("GRADLE_OPTS", "-Dorg.gradle.console=plain")
	t.Setenv("MAVEN_OPTS", "-Xmx2g")
	env := strings.Join(Environ([]string{"HIVE_TERMINAL_ID=t_1"}), "\n")
	if !strings.Contains(env, "MAVEN_OPTS=-Xmx2g") {
		t.Fatal("user's MAVEN_OPTS dropped")
	}
	for _, bad := range []string{"ORCA_PANE_KEY=", "CLAUDECODE=", "NO_COLOR=", "FORCE_COLOR=", "GIT_EDITOR=", "GROK_AGENT=", "GRADLE_OPTS="} {
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

// 电脑收回尺寸：放掉手机的占用、按界面最后要的尺寸重排；手机之后离开不再改尺寸。
func TestReclaimSize(t *testing.T) {
	tm, err := Start(Options{ID: "t_r", Dir: t.TempDir(), Cols: 100, Rows: 30, Command: []string{"/bin/sh", "-c", "sleep 5"}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	tm.HoldSize("phone", 40, 20)
	tm.Resize(120, 32) // 手机看着时界面改了窗口大小，先记下
	if !tm.SizeHeld() {
		t.Fatal("held")
	}
	if c, r := tm.Size(); c != 40 || r != 20 {
		t.Fatalf("phone size %dx%d", c, r)
	}
	tm.ReclaimSize()
	if c, r := tm.Size(); tm.SizeHeld() || c != 120 || r != 32 {
		t.Fatalf("reclaimed %dx%d held %v", c, r, tm.SizeHeld())
	}
	tm.ReleaseSize("phone")
	if c, r := tm.Size(); c != 120 || r != 32 {
		t.Fatalf("after phone left %dx%d", c, r)
	}
}

func TestWatchCarriesPartialSequence(t *testing.T) {
	// 先输出半个 SGR 序列，等它被读进内核后接入，再输出后半截
	script := `printf '\033[3'; sleep 1; printf '1mRED\033[0m'; sleep 5`
	tm, err := Start(Options{ID: "t_tail", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		tm.smu.Lock()
		n := len(tm.tail)
		tm.smu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial sequence never arrived")
		}
		time.Sleep(20 * time.Millisecond)
	}
	snap, sub := tm.Watch()
	defer tm.Detach(sub)
	if !strings.HasSuffix(string(snap), "\x1b[3") {
		t.Fatalf("snapshot does not end with the partial sequence: %q", snap[max(0, len(snap)-20):])
	}
	select {
	case rest := <-sub.C:
		if !strings.HasPrefix(string(rest), "1mRED") {
			t.Fatalf("rest %q", rest)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("rest never arrived")
	}
}

// 程序退出前不再读输入、鼠标跟踪还开着时，鼠标移动的报告堆在 PTY 里；程序关掉鼠标跟踪时丢掉它们，
// 后面的 shell 读不到一串"35;12;8M"。最后写进去的是键盘输入时不丢。
func TestMouseReportsDroppedWhenTrackingEnds(t *testing.T) {
	for _, c := range []struct {
		name, after, want string
	}{
		{"mouse", "", "got=[]"},
		{"typed", "x", "8Mx]"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// 开鼠标跟踪后睡一会儿（这时不读输入），关掉跟踪，再读 PTY 里剩下的
			script := `stty raw -echo; printf '\033[?1000h\033[?1003h\033[?1006hready\r\n'; sleep 1; printf '\033[?1006l\033[?1003l\033[?1000l'; sleep 0.5; perl -e 'use IO::Select; $s = IO::Select->new(\*STDIN); $b = ""; while ($s->can_read(0.3)) { sysread(STDIN, $c, 256) or last; $b .= $c } $b =~ s/\e/^[/g; print "got=[$b]\r\n"'; stty sane`
			tm, err := Start(Options{ID: "t_mouse", Dir: t.TempDir(), Command: []string{"/bin/bash", "--norc", "-c", script}})
			if err != nil {
				t.Fatal(err)
			}
			defer tm.Close(time.Second)
			deadline := time.Now().Add(5 * time.Second)
			for !tm.ContainsText("ready") {
				if time.Now().After(deadline) {
					t.Fatal("program never started")
				}
				time.Sleep(10 * time.Millisecond)
			}
			for i := range 5 {
				tm.Send([]byte("\x1b[<35;" + string(rune('1'+i)) + ";8M"))
			}
			if c.after != "" {
				tm.Send([]byte(c.after))
			}
			select {
			case <-tm.Exited():
			case <-time.After(10 * time.Second):
				t.Fatal("program did not exit")
			}
			time.Sleep(100 * time.Millisecond)
			if !tm.ContainsText(c.want) {
				t.Fatalf("want %s, screen:\n%s", c.want, strings.Join(tm.Lines(), "\n"))
			}
		})
	}
}

// 行数变多时把回滚区拉回屏幕：光标不在最后一行（TUI 的输入框下面还有状态行）时 libghostty 只会在底部加空行。
// 尺寸稳定后重建内核，内容贴着底部、光标和内容的相对位置不变；接入者被断开，重新接入拿到的快照重放出来和内核一样
func TestResizePullsBackScrollback(t *testing.T) {
	for _, cols := range []int{100, 200} {
		script := `seq 1 60; printf 'status\033[8A'; sleep 5`
		tm, err := Start(Options{ID: "t_pull", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}, Cols: 100, Rows: 30})
		if err != nil {
			t.Fatal(err)
		}
		waitFor(t, "output", func() bool { return tm.ContainsText("status") })
		_, sub := tm.Attach(0, 0)
		tm.Resize(cols, 40)
		if c, r := tm.Size(); c != cols || r != 40 {
			t.Fatalf("size %dx%d", c, r)
		}
		select {
		case got, ok := <-sub.C:
			if ok {
				t.Fatalf("unexpected output %q", got[:min(len(got), 20)])
			}
		case <-time.After(2 * time.Second):
			t.Fatal("subscriber was not kicked")
		}
		if !sub.Kicked() {
			t.Fatal("subscriber not marked kicked")
		}
		tm.smu.Lock()
		_, cy := tm.emu.Cursor()
		last, _ := tm.emu.RowText(39)
		tm.smu.Unlock()
		if cy != 31 || !strings.HasPrefix(string(last), "status") {
			t.Fatalf("cols %d: cursor row %d, last row %q", cols, cy, string(last))
		}
		// 重新接入：快照重放出来和内核一样
		snap, sub2 := tm.Attach(cols, 40)
		peer, err := vt.NewTerminal(cols, 40, vt.Effects{})
		if err != nil {
			t.Fatal(err)
		}
		peer.Write(snap)
		if px, py := peer.Cursor(); py != 31 || px != 6 {
			t.Fatalf("peer cursor (%d,%d)", px, py)
		}
		if got, want := peer.Text(), tm.Lines(); strings.Join(want, "\n") != strings.TrimRight(got, " \n") {
			t.Fatalf("peer differs:\n%s\n---\n%s", got, strings.Join(want, "\n"))
		}
		peer.Free()
		tm.Detach(sub2)
		tm.Close(time.Second)
	}
}

// 光标已经在最后一行时 libghostty 自己会拉回回滚区，不重建、不断开接入者
func TestResizeAtBottomNoResync(t *testing.T) {
	tm, err := Start(Options{ID: "t_pull2", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", "seq 1 60; sleep 5"}, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	waitFor(t, "output", func() bool { return tm.ContainsText("60") })
	_, sub := tm.Attach(0, 0)
	defer tm.Detach(sub)
	tm.Resize(100, 40)
	select {
	case got := <-sub.C:
		t.Fatalf("unexpected output or close %q", got)
	case <-time.After(400 * time.Millisecond):
	}
	tm.smu.Lock()
	_, cy := tm.emu.Cursor()
	tm.smu.Unlock()
	if cy != 39 {
		t.Fatalf("cursor row %d", cy)
	}
}

// 程序设了滚动区域（自己管着整屏）时不重建
func TestResizeKeepsScrollRegion(t *testing.T) {
	script := `printf '\033[5;25r'; seq 1 60; printf 'status\033[8A'; sleep 5`
	tm, err := Start(Options{ID: "t_pull3", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	waitFor(t, "output", func() bool { return tm.ContainsText("status") })
	_, sub := tm.Attach(0, 0)
	defer tm.Detach(sub)
	tm.Resize(100, 40)
	select {
	case got, ok := <-sub.C:
		t.Fatalf("unexpected output or close %q ok=%v", got, ok)
	case <-time.After(400 * time.Millisecond):
	}
}

// 手机看完离开、改回更高的电脑尺寸时也拉回来
func TestReleaseSizePullsBack(t *testing.T) {
	script := `seq 1 60; printf 'status\033[8A'; sleep 5`
	tm, err := Start(Options{ID: "t_pull4", Dir: t.TempDir(), Command: []string{"/bin/sh", "-c", script}, Cols: 100, Rows: 30})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	waitFor(t, "output", func() bool { return tm.ContainsText("status") })
	tm.HoldSize("phone", 100, 20)
	_, sub := tm.Attach(0, 0)
	tm.ReleaseSize("phone")
	select {
	case _, ok := <-sub.C:
		if ok {
			t.Fatal("unexpected output")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber was not kicked")
	}
	tm.smu.Lock()
	_, cy := tm.emu.Cursor()
	last, _ := tm.emu.RowText(29)
	tm.smu.Unlock()
	if cy != 21 || !strings.HasPrefix(string(last), "status") {
		t.Fatalf("cursor row %d, last row %q", cy, string(last))
	}
}
