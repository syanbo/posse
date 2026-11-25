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
	"posse/internal/errtext"
	"posse/internal/host"
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
	if n, err := d.store.MarkRunningLost(nil); err != nil || n != 2 {
		t.Fatalf("MarkRunningLost = %d, %v", n, err)
	}
	if err := d.store.PruneTerminals(); err != nil {
		t.Fatal(err)
	}

	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	list := call(t, d2.terminalList, map[string]any{"workspace": w.ID}).([]terminalInfo)
	if len(list) != 2 || list[0].ID != s.ID || !list[0].Lost || list[0].Agent != "claude" || list[0].Name != "登录页" || list[1].ID != plain.ID || !list[1].Lost || list[1].Title != "构建" {
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
	waitScreen(t, r, errtext.MessageIn(errtext.SystemLang(), srcRestored))
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

	if n, err := d.store.MarkRunningLost(nil); err != nil || n != 1 {
		t.Fatalf("MarkRunningLost = %d, %v; want only the terminal that exited during shutdown", n, err)
	}
	if _, err := d.store.LostTerminal(late.ID); err != nil {
		t.Fatalf("terminal that exited during shutdown not kept: %v", err)
	}
}

// agent 退出回 shell 后常驻进程重启，未恢复终端在桌面和手机上仍保留退出前的会话标题，不退回「终端」/ shell。
func TestLostExitedAgentKeepsLabel(t *testing.T) {
	defer func(v time.Duration) { resumeSettle = v }(resumeSettle)
	resumeSettle = 20 * time.Millisecond
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "antigravity", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTitle("Redock.dev Competitor Feature Analysis", "sid-agy", false)
	d.setAgent(s, host.AgentState{Status: agent.StatusExited, Event: "process", At: time.Now()})
	d.saveResume(s, true)
	d.exiting.Store(true)
	d.closeAll()
	if _, err := d.store.MarkRunningLost(nil); err != nil {
		t.Fatal(err)
	}

	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	list := call(t, d2.terminalList, map[string]any{"workspace": w.ID}).([]terminalInfo)
	if len(list) != 1 || !list[0].Lost || list[0].Agent != "" || list[0].SessionTitle != "Redock.dev Competitor Feature Analysis" {
		t.Fatalf("lost list = %+v", list)
	}
	st := (mobileHost{d2}).State()
	if len(st.Workspaces) != 1 || len(st.Workspaces[0].Terminals) != 1 || st.Workspaces[0].Terminals[0].Title != "Redock.dev Competitor Feature Analysis" {
		t.Fatalf("mobile lost = %+v", st.Workspaces)
	}
}

// 上一轮退出排好的 resumeSettle 定时器在新 agent 运行中触发时不得卡住 settling，后续新 agent 退出仍会正常落盘清空 agent。
func TestResumeSettleTimerDoesNotStickAcrossSecondExit(t *testing.T) {
	defer func(v time.Duration) { resumeSettle = v }(resumeSettle)
	resumeSettle = 60 * time.Millisecond
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hook(t, d, s.ID, "UserPromptSubmit", map[string]any{"session_id": "sid-a"})
	// A 退出并排好第一轮定时器
	hook(t, d, s.ID, "SessionEnd", map[string]any{"session_id": "sid-a", "reason": "prompt_input_exit"})
	// 5 秒内 B 立刻在同一终端启动并保存记录
	hook(t, d, s.ID, "UserPromptSubmit", map[string]any{"session_id": "sid-b"})
	// 等 A 的定时器触发（此时 B 仍在运行）
	time.Sleep(2 * resumeSettle)
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Agent != "claude" || r.SessionID != "sid-b" {
		t.Fatalf("after A timer while B running: %+v %v", r, err)
	}
	// B 再退出，必须能重新排定时器并在 resumeSettle 后落盘清空 agent
	hook(t, d, s.ID, "SessionEnd", map[string]any{"session_id": "sid-b", "reason": "prompt_input_exit"})
	time.Sleep(3 * resumeSettle)
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Agent != "" || r.SessionID != "" {
		t.Fatalf("after B exit settled: %+v %v", r, err)
	}
}

// 续接仍在运行的已命名 Codex 会话时保留显式会话标题；没有 session_id 的新开会话不带旧标题。
func TestRestoreResumedNamedAgentKeepsSessionTitle(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", "/usr/bin:/bin")
	d, w := newTestDaemon(t)
	d.launcher = agent.Launcher{Exe: "/bin/true", Home: d.cfg.Home}

	resumed, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	resumed.SetTitle("Saved Codex title", "sid-codex-1", false)
	d.saveResume(resumed, false)

	fresh, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.saveResume(fresh, false)

	d.exiting.Store(true)
	d.closeAll()
	if _, err := d.store.MarkRunningLost(nil); err != nil {
		t.Fatal(err)
	}

	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	rs, err := d2.session(resumed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info := d2.terminalInfo(rs); info.Agent != "codex" || info.AgentState == nil || info.AgentState.Title != "Saved Codex title" {
		t.Fatalf("restored named codex info = %+v", info)
	}
	foundMobile := false
	for _, mw := range (mobileHost{d2}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == resumed.ID {
				foundMobile = true
				if mt.Title != "Saved Codex title" {
					t.Fatalf("restored named codex mobile = %+v", mt)
				}
			}
		}
	}
	if !foundMobile {
		t.Fatal("restored codex missing from mobile state")
	}

	fs, err := d2.session(fresh.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fs.Agent().Title != "" {
		t.Fatalf("fresh restored codex inherited title %q", fs.Agent().Title)
	}
}

// 续接只有兜底标题（无显式会话标题）的 Codex 和 Grok 时，运行中与退出后桌面和手机都保留兜底标题；
// 新开无 session_id 的 Claude 不继承旧兜底标题。
func TestRestoreResumedFallbackTitleCodexAndGrok(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", "/usr/bin:/bin")
	d, w := newTestDaemon(t)
	d.launcher = agent.Launcher{Exe: "/bin/true", Home: d.cfg.Home}

	addLost := func(agentName, sid, fallback string) string {
		t.Helper()
		id := store.NewID("t_")
		if err := d.store.AddTerminal(store.Terminal{
			ID: id, WorkspaceID: w.ID, Command: []string{"/bin/sh"}, Agent: agentName, Cwd: w.Path, State: "lost", CreatedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := d.store.SetTerminalResume(id, agentName, sid, w.Path, fallback, ""); err != nil {
			t.Fatal(err)
		}
		return id
	}
	codexID := addLost("codex", "sid-codex-fb", "Saved OSC title")
	grokID := addLost("grok", "sid-grok-fb", "Saved Grok convo")
	freshClaudeID := addLost("claude", "", "Old Claude OSC")

	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)

	mobileTitle := func(id string) string {
		t.Helper()
		for _, mw := range (mobileHost{d2}).State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == id {
					return mt.Title
				}
			}
		}
		t.Fatalf("terminal %s missing from mobile", id)
		return ""
	}

	cs, err := d2.session(codexID)
	if err != nil {
		t.Fatal(err)
	}
	if info := d2.terminalInfo(cs); info.FallbackTitle != "Saved OSC title" {
		t.Fatalf("restored codex desktop fallback = %+v", info)
	}
	if got := mobileTitle(codexID); got != "Saved OSC title" {
		t.Fatalf("restored codex mobile title = %q", got)
	}
	d2.setAgent(cs, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", SessionID: "sid-codex-fb", At: time.Now()})
	if got := mobileTitle(codexID); got != "Saved OSC title" {
		t.Fatalf("exited restored codex mobile title = %q", got)
	}

	gs, err := d2.session(grokID)
	if err != nil {
		t.Fatal(err)
	}
	if info := d2.terminalInfo(gs); info.FallbackTitle != "Saved Grok convo" {
		t.Fatalf("restored grok desktop fallback = %+v", info)
	}
	if got := mobileTitle(grokID); got != "Saved Grok convo" {
		t.Fatalf("restored grok mobile title = %q", got)
	}
	d2.setAgent(gs, host.AgentState{Status: agent.StatusExited, Event: "process", At: time.Now()})
	if got := mobileTitle(grokID); got != "Saved Grok convo" {
		t.Fatalf("exited restored grok mobile title = %q", got)
	}

	fc, err := d2.session(freshClaudeID)
	if err != nil {
		t.Fatal(err)
	}
	if info := d2.terminalInfo(fc); info.FallbackTitle != "" || info.SessionTitle != "" {
		t.Fatalf("fresh restored claude inherited old titles: %+v", info)
	}
	if got := mobileTitle(freshClaudeID); got != "" {
		t.Fatalf("fresh restored claude mobile title = %q, want empty", got)
	}
}

// 从旧版常驻进程交接过来的 Grok（交接包不含 grokName/agentTitle 新字段），恢复后遇到 shell OSC 退出仍保留 Grok 会话名。
func TestOldDaemonHandoffGrokRetainsTitleOnExit(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", `printf '\033]0;Actual Grok conversation - grok\007'; exec sleep 30`}, "grok", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "grok OSC title", func() bool { return strings.Contains(s.Title(), "Actual Grok conversation") })
	rec, f, err := s.Pause()
	if err != nil {
		t.Fatal(err)
	}
	// 构造不含 GrokName / AgentTitle 的旧版交接记录
	data, err := json.Marshal(handoffBatch{Version: handoffVersion, Terms: []handoffTerm{{Term: rec, Agent: "grok", HookedPg: 999999}}})
	if err != nil {
		t.Fatal(err)
	}
	var batch handoffBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		t.Fatal(err)
	}
	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	if err := d2.adopt(&incoming{batch: batch, ptys: []*os.File{f}}); err != nil {
		t.Fatal(err)
	}
	got := d2.terms[s.ID]
	got.Resume()
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()

	// 模拟 grok 退出、shell 覆盖 OSC 标题并触发 checkAgentGone
	d2.checkAgentGone(got)
	for _, mw := range (mobileHost{d2}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID {
				if mt.Agent != "" || mt.Title != "Actual Grok conversation" {
					t.Fatalf("adopted grok after exit = %+v", mt)
				}
				return
			}
		}
	}
	t.Fatal("adopted grok missing from mobile state")
}

// 连续两次重启时（Lost → 恢复 → saveResume → 重启 → Lost），目录已规范化且无新 OSC：
// 1. 无 session_id 的 Codex 新开后，第二次重启不能把旧的 Explicit saved title 重新带回来；
// 2. 被停用的已命名 Codex worker 恢复成 shell 后，校验键同步更新，第二次重启仍保留 Explicit saved title 而不是降级成 Worker task。
func TestDoubleRestartReviveUpdatesSQLiteTitles(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", "/usr/bin:/bin")
	d, w := newTestDaemon(t)
	d.launcher = agent.Launcher{Exe: "/bin/true", Home: d.cfg.Home}
	canonPath, err := filepath.EvalSymlinks(w.Path)
	if err != nil {
		canonPath = w.Path
	}

	// 用例 1：无 session_id 的 Codex（有旧显式标题与兜底标题），第一次恢复新开 Codex。
	freshID := store.NewID("t_")
	if err := d.store.AddTerminal(store.Terminal{
		ID: freshID, WorkspaceID: w.ID, Command: []string{"/bin/sh"}, Agent: "codex", Cwd: canonPath, State: "lost", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.store.SetTerminalResume(freshID, "codex", "", canonPath, "Old OSC", "Explicit saved title"); err != nil {
		t.Fatal(err)
	}

	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	fs, err := d2.session(freshID)
	if err != nil {
		t.Fatal(err)
	}
	d2.saveResume(fs, false)
	d2.exiting.Store(true)
	d2.closeAll()
	if _, err := d.store.MarkRunningLost(nil); err != nil {
		t.Fatal(err)
	}
	lostFresh, err := d.store.LostTerminal(freshID)
	if err != nil {
		t.Fatal(err)
	}
	if lostFresh.SessionTitle != "" || lostFresh.FallbackTitle != "" || lostFresh.Label != "" {
		t.Fatalf("second restart resurrected stale titles on fresh codex: %+v", lostFresh)
	}

	// 用例 2：已命名的 Codex worker，停用 codex 后恢复成 shell，连续两次重启后仍保留显式标题优先于任务标题。
	if err := agent.SetEnabled(d.cfg.Home, "codex", false); err != nil {
		t.Fatal(err)
	}
	workerID := store.NewID("t_")
	if err := d.store.AddTerminal(store.Terminal{
		ID: workerID, WorkspaceID: w.ID, Command: []string{"/bin/sh"}, Agent: "codex", Cwd: canonPath, State: "lost", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.store.SetTerminalResume(workerID, "codex", "sid-worker", canonPath, "Old OSC", "Explicit saved title"); err != nil {
		t.Fatal(err)
	}
	if err := d.store.AddTask(store.Task{
		ID: store.NewID("k_"), Title: "Worker task", Spec: "s", WorkspaceID: w.ID, Worker: workerID, Agent: "codex", Status: store.TaskDone, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	mobileTitleIn := func(dm *Daemon, id string) string {
		t.Helper()
		for _, mw := range (mobileHost{dm}).State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == id {
					return mt.Title
				}
			}
		}
		t.Fatalf("terminal %s missing from mobile state", id)
		return ""
	}

	d3 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d3.closeAll)
	ws, err := d3.session(workerID)
	if err != nil {
		t.Fatal(err)
	}
	if got := mobileTitleIn(d3, workerID); got != "Explicit saved title" {
		t.Fatalf("first restore mobile title = %q, want Explicit saved title", got)
	}
	d3.saveResume(ws, false)
	d3.exiting.Store(true)
	d3.closeAll()
	if _, err := d.store.MarkRunningLost(nil); err != nil {
		t.Fatal(err)
	}

	d4 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d4.closeAll)
	lostWorker, err := d.store.LostTerminal(workerID)
	if err != nil {
		t.Fatal(err)
	}
	if lostWorker.Agent != "" || lostWorker.SessionTitle != "Explicit saved title" {
		t.Fatalf("second restart lost worker = %+v, want SessionTitle=Explicit saved title", lostWorker)
	}
	if got := mobileTitleIn(d4, workerID); got != "Explicit saved title" {
		t.Fatalf("second restart mobile title = %q, want Explicit saved title", got)
	}
}

// 当前格式交接明确标记「确认无标题（TitleState=true, AgentTitle=""）」时，即使 PTY 里仍残留上个 agent 的 OSC 标题，
// 接收端也不得把它回填给当前 agent；旧格式交接库里记着不同 agent 时也不得跨 agent 借用旧库标签。
func TestCurrentHandoffEmptyTitleDoesNotAdoptStaleOSC(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", `printf '\033]0;Previous Claude OSC\007'; exec sleep 30`}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "previous claude OSC", func() bool { return s.Title() == "Previous Claude OSC" })
	d.setAgent(s, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	d.saveResume(s, true)

	rawStart, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "sessionStart",
		"payload": map[string]any{"session_id": "cursor-fresh"},
	})
	if _, err := d.hookEvent(context.Background(), rawStart); err != nil {
		t.Fatal(err)
	}

	rec, f, err := s.Pause()
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	ht := handoffTerm{
		Term: rec, Agent: s.agent, Hooked: s.hooked, HookedPg: s.agentPg, Fg: s.fg,
		TitleState: true, GrokName: s.grokName, AgentTitle: s.agentTitle,
		ExitedSessionTitle: s.exitedSessionTitle, ExitedTitle: s.exitedTitle,
	}
	s.mu.Unlock()
	data, err := json.Marshal(handoffBatch{Version: handoffVersion, Terms: []handoffTerm{ht}})
	if err != nil {
		t.Fatal(err)
	}
	var batch handoffBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		t.Fatal(err)
	}
	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	if err := d2.adopt(&incoming{batch: batch, ptys: []*os.File{f}}); err != nil {
		t.Fatal(err)
	}
	got := d2.terms[s.ID]
	got.Resume()
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()

	if info := d2.terminalInfo(got); info.Agent != "cursor" || info.Title != "" || info.FallbackTitle != "" {
		t.Fatalf("adopted cursor desktop leaked stale OSC: %+v", info)
	}
	for _, mw := range (mobileHost{d2}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID && (mt.Agent != "cursor" || mt.Title != "") {
				t.Fatalf("adopted cursor mobile leaked stale OSC: %+v", mt)
			}
		}
	}
	d2.saveResume(got, false)
	if r, err := d2.store.TerminalResume(s.ID); err != nil || r.Agent != "cursor" || r.Label != "" || r.FallbackTitle != "" {
		t.Fatalf("adopted cursor stored leaked stale OSC: %+v, %v", r, err)
	}

	// 旧格式交接（TitleState=false）且库里还留着另一家 agent 的记录时，不得把旧库标签借给新 agent。
	s2, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.SetTerminalResume(s2.ID, "claude", "old-claude-sid", w.Path, "Old Claude DB Label", ""); err != nil {
		t.Fatal(err)
	}
	rec2, f2, err := s2.Pause()
	if err != nil {
		t.Fatal(err)
	}
	data2, err := json.Marshal(handoffBatch{Version: handoffVersion, Terms: []handoffTerm{{Term: rec2, Agent: "cursor"}}})
	if err != nil {
		t.Fatal(err)
	}
	var batch2 handoffBatch
	if err := json.Unmarshal(data2, &batch2); err != nil {
		t.Fatal(err)
	}
	if err := d2.adopt(&incoming{batch: batch2, ptys: []*os.File{f2}}); err != nil {
		t.Fatal(err)
	}
	got2 := d2.terms[s2.ID]
	got2.Resume()
	d.mu.Lock()
	delete(d.terms, s2.ID)
	d.mu.Unlock()
	if info := d2.terminalInfo(got2); info.Title != "" || info.FallbackTitle != "" {
		t.Fatalf("old handoff borrowed mismatched agent DB label: %+v", info)
	}
}

type switchAfterPauseTerminal struct {
	host.Terminal
	afterPause func()
}

func (p *switchAfterPauseTerminal) Pause() (host.Record, *os.File, error) {
	rec, f, err := p.Terminal.Pause()
	if err == nil && p.afterPause != nil {
		p.afterPause()
	}
	return rec, f, err
}

// 交接发送端在 Pause 返回后若并发切换到新 agent，pauseForHandoff 必须在同一 session.mu 临界区内
// 重采 AgentState 覆写 rec.Agent，绝不向接收端发送「新 agent 类型 + 旧会话态」的撕裂记录。
func TestHandoffPauseAtomicallyCapturesAgentAndState(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTitle("Old Claude title", "claude-sid", false)
	s.Terminal = &switchAfterPauseTerminal{
		Terminal: s.Terminal,
		afterPause: func() {
			// Pause 已经把旧 Claude 状态拷进 rec.Agent，此时在途 RPC 把终端切换成已命名 Cursor。
			gen := s.markHooked("cursor", "cursor-sid")
			d.setAgentFor(s, host.AgentState{Status: agent.StatusWorking, SessionID: "cursor-sid", Title: "New Cursor title", At: time.Now()}, gen)
		},
	}

	ht, f, err := pauseForHandoff(s)
	if err != nil {
		t.Fatal(err)
	}
	if ht.Agent != "cursor" || ht.Term.Agent.SessionID != "cursor-sid" || ht.Term.Agent.Title != "New Cursor title" {
		if f != nil {
			f.Close()
		}
		t.Fatalf("torn handoff record: agent=%q termAgent=%+v", ht.Agent, ht.Term.Agent)
	}
	data, err := json.Marshal(handoffBatch{Version: handoffVersion, Terms: []handoffTerm{ht}})
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	var batch handoffBatch
	if err := json.Unmarshal(data, &batch); err != nil {
		f.Close()
		t.Fatal(err)
	}
	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	if err := d2.adopt(&incoming{batch: batch, ptys: []*os.File{f}}); err != nil {
		t.Fatal(err)
	}
	got := d2.terms[s.ID]
	got.Resume()
	d.mu.Lock()
	delete(d.terms, s.ID)
	d.mu.Unlock()
	if info := d2.terminalInfo(got); info.Agent != "cursor" || info.AgentState == nil || info.AgentState.SessionID != "cursor-sid" || info.AgentState.Title != "New Cursor title" {
		t.Fatalf("adopted handoff desktop = %+v", info)
	}
}
