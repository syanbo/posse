package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"hive/internal/agent"
	"hive/internal/term"
)

func fakeAwake(t *testing.T) *awake {
	t.Helper()
	a := newAwake(t.TempDir())
	a.spawn = func(bool) (*exec.Cmd, error) {
		cmd := exec.Command("/bin/sleep", "60")
		return cmd, cmd.Start()
	}
	t.Cleanup(a.close)
	return a
}

func TestAwakeModes(t *testing.T) {
	a := fakeAwake(t)
	if st := a.status(); st.Mode != awakeAuto || st.Active {
		t.Fatalf("default %+v", st)
	}
	a.apply(0)
	if !a.status().Active {
		t.Fatal("auto does not hold while hive runs")
	}
	a.apply(2)
	if st := a.status(); !st.Active || st.Working != 2 {
		t.Fatalf("auto with working agents %+v", st)
	}
	a.apply(0)
	if !a.status().Active {
		t.Fatal("auto released while hive still runs")
	}
	// caffeinate 意外退出：下次重算时重新起
	a.mu.Lock()
	a.cmd.Process.Kill()
	<-a.done
	a.mu.Unlock()
	if a.status().Active {
		t.Fatal("dead caffeinate reported active")
	}
	a.apply(0)
	if !a.status().Active {
		t.Fatal("not restarted")
	}
	// 屏幕也亮：和 auto 一样只要常驻进程在就托住，换档时按新参数重起
	var displays []bool
	spawn := a.spawn
	a.spawn = func(display bool) (*exec.Cmd, error) {
		displays = append(displays, display)
		return spawn(display)
	}
	if err := a.setMode(awakeDisplay); err != nil {
		t.Fatal(err)
	}
	a.apply(1)
	if !a.status().Active || len(displays) != 1 || !displays[0] {
		t.Fatalf("display mode active %v spawns %v", a.status().Active, displays)
	}
	a.apply(0)
	if !a.status().Active {
		t.Fatal("display mode released while hive still runs")
	}
	if err := a.setMode(awakeOff); err != nil {
		t.Fatal(err)
	}
	a.apply(3)
	if a.status().Active {
		t.Fatal("off holds")
	}
	// 档位记在文件里，重启常驻进程后沿用
	if b := newAwake(filepath.Dir(a.path)); b.mode != awakeOff {
		t.Fatalf("saved mode %q", b.mode)
	}
	os.WriteFile(a.path, []byte(`{"mode":"bogus"}`), 0o600)
	if b := newAwake(filepath.Dir(a.path)); b.mode != awakeAuto {
		t.Fatalf("bad saved mode gives %q", b.mode)
	}
}

// 工作中的 agent 只是计数，卡住很久的也算在里面；托不托住不看这个数。
func TestWorkingAgents(t *testing.T) {
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.startTerminal(ws, []string{"/bin/cat"}, "", 80, 24); err != nil {
		t.Fatal(err)
	}
	if n := d.workingAgents(); n != 0 {
		t.Fatalf("idle counted %d", n)
	}
	s.SetAgent(term.AgentState{Status: agent.StatusWorking, At: time.Now()})
	if n := d.workingAgents(); n != 1 {
		t.Fatalf("working counted %d", n)
	}
}

// 去掉的"始终"（on）：旧的 awake.json 读成 auto，awake.set 给 on 也按 auto。
func TestAwakeOldOnMode(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "awake.json"), []byte(`{"mode":"on"}`), 0o600)
	if m := newAwake(home).mode; m != awakeAuto {
		t.Fatalf("old on read as %q", m)
	}
	if validAwakeMode("on") || normalAwakeMode("on") != awakeAuto {
		t.Fatal("on still a mode of its own")
	}
}
