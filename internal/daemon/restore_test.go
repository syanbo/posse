package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/procmem"
	"posse/internal/store"
)

func hook(t *testing.T, d *Daemon, id, event string, payload map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"terminalId": id, "agent": "claude", "event": event, "payload": payload})
	if _, err := d.hookEvent(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
}

func waitCwd(t *testing.T, s *session, dir string) {
	t.Helper()
	want, _ := filepath.EvalSymlinks(dir)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := filepath.EvalSymlinks(procmem.Cwd(s.Foreground())); got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("foreground cwd never became %s", dir)
}

// 常驻进程退出时托管着的终端留着记录；下一个进程启动后列成 Lost，第一次用到时在原来的目录续接原来的会话，
// ID、名字、创建时间不变。
func TestRestoreAfterRestart(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", "/usr/bin:/bin")
	d, w := newTestDaemon(t)
	d.launcher = agent.Launcher{Exe: "/bin/true", Home: d.cfg.Home}
	sub := filepath.Join(w.Path, "sub")
	os.Mkdir(sub, 0o700)

	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", "cd sub && exec sleep 30"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitCwd(t, s, sub)
	hook(t, d, s.ID, "UserPromptSubmit", map[string]any{"session_id": "sid-1"})
	call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": "登录页"})
	plain, err := d.startTerminal(w, []string{"/bin/sh", "-c", `printf '\033]0;✳ 构建\007'; exec sleep 30`}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(3 * time.Second); plain.Title() == "" && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	// 标题由 agentLoop 定时记下，这里直接记一次
	d.saveResume(plain, false)

	// 常驻进程退出：关掉的终端不记成退出
	d.exiting.Store(true)
	d.closeAll()
	if n, err := d.store.MarkRunningLost(time.Now(), nil); err != nil || n != 2 {
		t.Fatalf("MarkRunningLost = %d, %v", n, err)
	}
	if err := d.store.PruneTerminals(); err != nil {
		t.Fatal(err)
	}

	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	list := call(t, d2.terminalList, map[string]any{"workspace": w.ID}).([]terminalInfo)
	if len(list) != 2 || list[0].ID != s.ID || !list[0].Lost || list[0].Agent != "claude" || list[0].Name != "登录页" || list[1].ID != plain.ID || !list[1].Lost || list[1].Title != "" {
		t.Fatalf("list after restart = %+v", list)
	}

	r, err := d2.session(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	cmd := strings.Join(r.Command, " ")
	if !strings.Contains(cmd, "--resume 'sid-1'") && !strings.Contains(cmd, "--resume sid-1") {
		t.Fatalf("restored command %q does not resume sid-1", cmd)
	}
	gotDir, _ := filepath.EvalSymlinks(r.Dir)
	wantDir, _ := filepath.EvalSymlinks(sub)
	// 库里的时间只到毫秒
	if gotDir != wantDir || r.userName() != "登录页" || r.CreatedAt.UnixMilli() != s.CreatedAt.UnixMilli() || r.agentName() != "claude" || r.Agent().SessionID != "sid-1" {
		t.Fatalf("restored dir %q name %q created %v (want %v) agent %q session %q", r.Dir, r.userName(), r.CreatedAt, s.CreatedAt, r.agentName(), r.Agent().SessionID)
	}
	waitScreen(t, r, "已恢复上次的终端")
	list = call(t, d2.terminalList, map[string]any{"workspace": w.ID}).([]terminalInfo)
	if len(list) != 2 || list[0].ID != s.ID || list[0].Lost || !list[0].Running || !list[1].Lost {
		t.Fatalf("list after restoring one = %+v", list)
	}

	// 没恢复的关掉就是删掉记录，不拉起
	call(t, d2.terminalClose, map[string]any{"terminal": plain.ID})
	if d2.liveSession(plain.ID) != nil {
		t.Fatal("closing a lost terminal started it")
	}
	if lost, _ := d2.store.LostTerminals(""); len(lost) != 0 {
		t.Fatalf("lost after close = %+v", lost)
	}
}

// agent 退出后等一会儿才从记录上摘掉；这期间常驻进程开始退出（关机时一起收到 SIGTERM）就留着，下次续接。
func TestResumeKeptWhenExitingWithAgent(t *testing.T) {
	defer func(v time.Duration) { resumeSettle = v }(resumeSettle)
	resumeSettle = 100 * time.Millisecond
	d, w := newTestDaemon(t)
	rec := func(id string) store.Terminal {
		r, err := d.store.TerminalResume(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	start := func() *session {
		s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		hook(t, d, s.ID, "UserPromptSubmit", map[string]any{"session_id": "sid-" + s.ID})
		hook(t, d, s.ID, "SessionEnd", map[string]any{"session_id": "sid-" + s.ID, "reason": "prompt_input_exit"})
		if r := rec(s.ID); r.Agent != "claude" {
			t.Fatalf("agent dropped right away: %+v", r)
		}
		return s
	}

	quit := start()
	time.Sleep(3 * resumeSettle)
	if r := rec(quit.ID); r.Agent != "" || r.SessionID != "" {
		t.Fatalf("agent kept after it quit: %+v", r)
	}

	shutdown := start()
	d.exiting.Store(true)
	time.Sleep(3 * resumeSettle)
	if r := rec(shutdown.ID); r.Agent != "claude" || r.SessionID != "sid-"+shutdown.ID {
		t.Fatalf("agent dropped while the daemon was exiting: %+v", r)
	}
}

// 用户关的终端马上记成退出；程序自己退出的等 resumeSettle 再记，这期间常驻进程开始退出就留着恢复。
func TestExitRecordedAfterSettle(t *testing.T) {
	defer func(v time.Duration) { resumeSettle = v }(resumeSettle)
	resumeSettle = 200 * time.Millisecond
	d, w := newTestDaemon(t)
	closed, err := d.startTerminal(w, []string{"/bin/sh", "-c", "sleep 30"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	quick, err := d.startTerminal(w, []string{"/bin/sh", "-c", "exit 3"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	late, err := d.startTerminal(w, []string{"/bin/sh", "-c", "sleep 1; exit 4"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.closeSession(closed, time.Second)
	<-quick.Exited()
	time.Sleep(2 * resumeSettle)
	<-late.Exited()
	d.exiting.Store(true) // late 退出后马上开始退出：还在等待里
	time.Sleep(2 * resumeSettle)

	if n, err := d.store.MarkRunningLost(time.Now(), nil); err != nil || n != 1 {
		t.Fatalf("MarkRunningLost = %d, %v; want only the terminal that exited during shutdown", n, err)
	}
	if _, err := d.store.LostTerminal(late.ID); err != nil {
		t.Fatalf("terminal that exited during shutdown not kept: %v", err)
	}
}
