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
	if a.status().Active {
		t.Fatal("auto holds with no agent working")
	}
	a.apply(2)
	if st := a.status(); !st.Active || st.Working != 2 {
		t.Fatalf("auto with working agents %+v", st)
	}
	a.apply(0)
	if a.status().Active {
		t.Fatal("auto still holds after agents went idle")
	}
	if err := a.setMode(awakeOn); err != nil {
		t.Fatal(err)
	}
	a.apply(0)
	if !a.status().Active {
		t.Fatal("on does not hold")
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
	// 屏幕也亮：和 auto 一样看 agent，换档时按新参数重起
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
	if a.status().Active {
		t.Fatal("display mode holds with no agent working")
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

// 只有状态新鲜的工作中 agent 才算：卡在工作中两小时以上的不再托住。
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
	s.SetAgent(term.AgentState{Status: agent.StatusWorking, At: time.Now().Add(-awakeStale - time.Minute)})
	if n := d.workingAgents(); n != 0 {
		t.Fatalf("stale counted %d", n)
	}
}
