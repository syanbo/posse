package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/mobile"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/term"
)

// 手机端历史转给常驻进程的 history.* 接口：列表、搜索和原来一样；继续一个不存在的会话报 not_found。
func TestMobileHistory(t *testing.T) {
	d, w := newTestDaemon(t)
	roots := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: roots}
	t.Cleanup(d.closeHistory)
	sid := "55555555-5555-4555-8555-555555555555"
	path := filepath.Join(roots, ".claude", "projects", "-proj", sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"type":"user","cwd":"`+w.Path+`","sessionId":"`+sid+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"手机上看历史"}}
`), 0o644)
	h := mobileHost{d}
	ctx := context.Background()
	d.refreshHistory(ctx, true, 0)
	list, err := h.History(ctx, "history.list", json.RawMessage(`{"limit":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if ss := list.(historyListResult).Sessions; len(ss) != 1 || ss[0].ID != sid || ss[0].Path != path {
		t.Fatalf("list %+v", ss)
	}
	found, err := h.History(ctx, "history.search", json.RawMessage(`{"query":"历史"}`))
	if err != nil || len(found.(historySearchResult).Hits) != 1 {
		t.Fatalf("search %+v %v", found, err)
	}
	_, err = h.History(ctx, "history.resume", json.RawMessage(`{"session":"nope"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeNotFound {
		t.Fatalf("resume unknown %v", err)
	}
}

// 桌面改的标签名记在常驻进程里，手机的标签用它；清空后恢复默认，数据库里也跟着改。
func TestTerminalRenameShowsOnMobile(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	title := func() string {
		for _, mw := range (mobileHost{d}).State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == s.ID {
					return mt.Title
				}
			}
		}
		return "<missing>"
	}
	info := call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": " 登录页重构 "}).(terminalInfo)
	if info.Name != "登录页重构" || title() != "登录页重构" {
		t.Fatalf("renamed: info %q, mobile %q", info.Name, title())
	}
	if r, _ := d.store.TerminalResume(s.ID); r.Title != "登录页重构" {
		t.Fatalf("stored %q", r.Title)
	}
	call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": ""})
	if got := title(); got == "登录页重构" {
		t.Fatalf("cleared name still shown: %q", got)
	}
	if r, _ := d.store.TerminalResume(s.ID); r.Title != "" {
		t.Fatalf("stored after clear %q", r.Title)
	}
}

// 手机上工作区的分支以 HEAD 为准：数据库里记的是建工作区时的分支，在项目目录里切了分支也要跟着变。
func TestMobileBranchFollowsHead(t *testing.T) {
	d, w := newTestDaemon(t)
	os.MkdirAll(filepath.Join(w.Path, ".git"), 0o755)
	os.WriteFile(filepath.Join(w.Path, ".git", "HEAD"), []byte("ref: refs/heads/develop_710\n"), 0o644)
	for _, mw := range (mobileHost{d}).State().Workspaces {
		if mw.ID == w.ID && mw.Branch != "develop_710" {
			t.Fatalf("branch = %q, want develop_710", mw.Branch)
		}
	}
}

// terminal send 的方向键跟着程序的光标键模式：开着应用模式（DECCKM）时发 ESC O A，ncurses 程序只认这种。
func TestSendKeysFollowCursorMode(t *testing.T) {
	d, w := newTestDaemon(t)
	script := `printf '\033[?1h'; stty -icanon -echo; printf 'ready\n'; head -c 3 | od -An -c`
	info := call(t, d.terminalCreate, map[string]any{"workspace": w.ID, "command": []string{"/bin/sh", "-c", script + "; sleep 5"}}).(terminalInfo)
	s, err := d.session(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	waitScreen(t, s, "ready")
	call(t, d.terminalSend, map[string]any{"terminal": info.ID, "keys": []string{"up"}})
	waitScreen(t, s, "033   O   A")
}

// 手机对话流按会话 ID 找终端：和以前从 State 里找的结果一样（按工作区的顺序、工作区里按开的先后，
// 只看还在运行的、对上 agent），只是不读 HEAD、不查任务。同一会话开在几个工作区的终端里时，
// 选的是前面那个工作区里的，不是最早开的。
func TestMobileSessionTerminalMatchesState(t *testing.T) {
	d, w1 := newTestDaemon(t)
	extra := func(name string, after time.Duration) store.Workspace {
		w := w1
		w.ID, w.Name, w.Kind, w.CreatedAt = store.NewID("w_"), name, "folder", w1.CreatedAt.Add(after)
		if err := d.store.AddWorkspace(w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	w2, w3 := extra("second", time.Second), extra("third", 2*time.Second)
	start := func(w store.Workspace, cmd, agentName, sid string) *session {
		s, err := d.startTerminal(w, []string{"/bin/sh", "-c", cmd}, "", 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		s.agent = agentName
		s.mu.Unlock()
		s.SetAgent(term.AgentState{Status: "idle", SessionID: sid})
		time.Sleep(5 * time.Millisecond)
		return s
	}
	// 最早开的在后面的工作区里；前面的工作区里有已退出的、agent 不同的，和后开的那个
	earliest := start(w2, "sleep 30", "grok", "dup")
	start(w3, "sleep 30", "grok", "dup")
	exited := start(w1, "exit 0", "grok", "dup")
	start(w1, "sleep 30", "codex", "dup")
	later := start(w1, "sleep 30", "grok", "dup")
	start(w3, "sleep 30", "claude", "only3")
	start(w2, "exit 0", "claude", "gone")
	waitFor(t, "exited terminals stop", func() bool { return !exited.Running() })
	h := mobileHost{d}
	viaState := func(agentName, sid string) host.Terminal {
		for _, mw := range h.State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.SessionID == sid && (agentName == "" || mt.Agent == agentName) {
					return h.Terminal(mt.ID)
				}
			}
		}
		return nil
	}
	for _, q := range [][2]string{{"grok", "dup"}, {"", "dup"}, {"codex", "dup"}, {"claude", "dup"}, {"claude", "only3"}, {"", "only3"}, {"grok", "only3"}, {"claude", "gone"}, {"", "nope"}} {
		got, want := h.SessionTerminal(q[0], q[1]), viaState(q[0], q[1])
		if got != want {
			t.Fatalf("%v: got %v, want %v", q, got, want)
		}
	}
	if got := h.SessionTerminal("grok", "dup"); got == nil || got.Info().ID != later.ID || later.ID == earliest.ID {
		t.Fatalf("session in two workspaces: got %v, want %s from the earlier workspace (earliest opened is %s)", got, later.ID, earliest.ID)
	}
}

// agent 退出回 shell 后，手机端标签名保留退出前的名称（任务标题、会话标题、grok 名、OSC 标题或 agent 名），
// 不突然变回 "shell"；同一终端再次启动新 agent 后恢复按新会话显示。
func TestMobileKeepsTitleWhenAgentExits(t *testing.T) {
	d, w := newTestDaemon(t)
	find := func(id string) mobile.Terminal {
		t.Helper()
		for _, mw := range (mobileHost{d}).State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == id {
					return mt
				}
			}
		}
		t.Fatalf("terminal %s not in mobile state", id)
		return mobile.Terminal{}
	}

	// 1. 任务 worker（未起会话标题）：运行中和退出回 shell 后都用任务标题，不被 shell 标题盖掉。
	worker, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.store.AddTask(store.Task{
		ID: store.NewID("k_"), Title: "Step 1 审查：internal/tslink 与方案文档", Spec: "review",
		WorkspaceID: w.ID, Worker: worker.ID, Agent: "codex", Status: store.TaskDone, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if mt := find(worker.ID); mt.Title != "Step 1 审查：internal/tslink 与方案文档" || mt.Agent != "codex" {
		t.Fatalf("running worker = %+v", mt)
	}
	d.setAgent(worker, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	if mt := find(worker.ID); mt.Title != "Step 1 审查：internal/tslink 与方案文档" || mt.Agent != "" {
		t.Fatalf("exited worker = %+v", mt)
	}

	// 2. 带会话标题的普通 agent：不同 session_id 的退出事件不误摘，对应会话退出后保留会话标题。
	named, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	named.SetTitle("Redock.dev Competitor Feature Analysis", "sid-1", false)
	if mt := find(named.ID); mt.Title != "Redock.dev Competitor Feature Analysis" {
		t.Fatalf("running named = %+v", mt)
	}
	d.setAgent(named, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", SessionID: "other", At: time.Now()})
	if mt := find(named.ID); mt.Title != "Redock.dev Competitor Feature Analysis" || mt.Agent != "codex" {
		t.Fatalf("mismatched SessionEnd cleared codex = %+v", mt)
	}
	d.setAgent(named, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", SessionID: "sid-1", At: time.Now()})
	if mt := find(named.ID); mt.Title != "Redock.dev Competitor Feature Analysis" || mt.Agent != "" {
		t.Fatalf("exited named = %+v", mt)
	}
	// 同一终端再启动新 agent 时清掉上次退出留下的旧标题。
	named.markHooked("claude", "sid-2")
	named.SetAgent(host.AgentState{Status: agent.StatusIdle, SessionID: "sid-2", At: time.Now()})
	if mt := find(named.ID); mt.Title != "" || mt.Agent != "claude" {
		t.Fatalf("restarted agent kept old title: %+v", mt)
	}

	fakeBin := func(name string) string {
		p := filepath.Join(t.TempDir(), name)
		if err := os.Symlink("/bin/cat", p); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// 3. Grok 会话：退出前清掉 OSC 标题也不丢 grokName。
	grok, err := d.startTerminal(w, []string{fakeBin("grok")}, "grok", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.onTitle(grok.ID, "修登录 - grok")
	d.onTitle(grok.ID, "")
	if mt := find(grok.ID); mt.Title != "修登录" {
		t.Fatalf("running grok = %+v", mt)
	}
	d.setAgent(grok, host.AgentState{Status: agent.StatusExited, Event: "process", At: time.Now()})
	if mt := find(grok.ID); mt.Title != "修登录" || mt.Agent != "" {
		t.Fatalf("exited grok = %+v", mt)
	}

	// 4. 无会话标题、仅靠 OSC 标题的 agent（Claude）：退出清标题后仍保留去掉状态前缀的原名。
	claude, err := d.startTerminal(w, []string{fakeBin("claude")}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.onTitle(claude.ID, "✳ Fix login")
	d.onTitle(claude.ID, "")
	d.setAgent(claude, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	if mt := find(claude.ID); mt.Title != "Fix login" || mt.Agent != "" {
		t.Fatalf("exited claude = %+v", mt)
	}

	// 5. 未命名且无 OSC 标题的 agent：退出后保留原 agent 名，不回退为 shell。
	bare, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.setAgent(bare, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	if mt := find(bare.ID); mt.Title != "cursor" || mt.Agent != "" {
		t.Fatalf("exited bare = %+v", mt)
	}

	// 6. 未恢复（lost）且 agent 已摘掉的任务 worker 仍显示任务标题。
	lostID := store.NewID("t_")
	if err := d.store.AddTerminal(store.Terminal{
		ID: lostID, WorkspaceID: w.ID, Command: []string{"/bin/zsh"}, Cwd: w.Path, State: "lost", CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.store.AddTask(store.Task{
		ID: store.NewID("k_"), Title: "Accept Step 7", Spec: "accept",
		WorkspaceID: w.ID, Worker: lostID, Agent: "codex", Status: store.TaskDone, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	d.noLost.Store(false)
	if mt := find(lostID); mt.Title != "Accept Step 7" || !mt.Lost {
		t.Fatalf("lost worker = %+v", mt)
	}
}

// Grok 进程退出后若 shell 写入 OSC "shell" 触发 checkAgentGone，手机仍保留 Grok 会话名而不被 "shell" 覆盖。
func TestMobileGrokExitIgnoresShellOSCTitle(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", `printf '\033]0;shell\007'; exec sleep 30`}, "grok", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "shell OSC title", func() bool { return s.Title() == "shell" })
	s.mu.Lock()
	s.grokName = "Grok conversation"
	s.agentPg = 999999 // 模拟已消失的 grok 进程组
	s.mu.Unlock()
	d.checkAgentGone(s)
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID {
				if mt.Agent != "" || mt.Title != "Grok conversation" {
					t.Fatalf("grok after shell OSC = %+v", mt)
				}
				return
			}
		}
	}
	t.Fatal("grok terminal missing from mobile state")
}

// 同一终端上上一轮 agent 的过期退出事件不能摘掉已经启动的新 agent，也不能留下旧标题。
func TestStaleExitDoesNotClearNewAgent(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_, oldGen := s.agentIdentity()
	s.SetTitle("Old Cursor Title", "cur-1", false)
	// 新 agent Claude 启动，代际递增。
	newGen := s.markHooked("claude", "cl-1")
	d.setAgentFor(s, host.AgentState{Status: agent.StatusWorking, SessionID: "cl-1", Title: "New Claude Title", At: time.Now()}, newGen)
	// 迟到的 Cursor 退出事件带着旧代际到达，必须被忽略。
	d.setAgentFor(s, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", SessionID: "cur-1", At: time.Now()}, oldGen)
	if s.agentName() != "claude" || s.Agent().Status != agent.StatusWorking || s.Agent().Title != "New Claude Title" {
		t.Fatalf("stale exit corrupted new agent: name=%q state=%+v", s.agentName(), s.Agent())
	}
	// 迟到的 Cursor 原生小写 sessionEnd hook 同样不能篡改当前运行中的 Claude。
	raw, _ := json.Marshal(map[string]any{"terminalId": s.ID, "agent": "cursor", "event": "sessionEnd", "payload": map[string]any{"session_id": "cur-1"}})
	if _, err := d.hookEvent(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if s.agentName() != "claude" || s.Agent().Status != agent.StatusWorking || s.Agent().Title != "New Claude Title" {
		t.Fatalf("cross-agent sessionEnd corrupted new agent: name=%q state=%+v", s.agentName(), s.Agent())
	}
}

// Codex 延迟退出复查确认进程消失后，不采纳 shell 已写入的 OSC "shell" 标题。
func TestCodexDelayedExitIgnoresShellOSCTitle(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", `printf '\033]0;shell\007'; exec sleep 30`}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "shell OSC title", func() bool { return s.Title() == "shell" })
	_, gen := s.agentIdentity()
	d.codexSessionEnded(s, "sid-codex", "sid-codex", gen)
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID {
				if mt.Agent != "" || mt.Title != "codex" {
					t.Fatalf("codex after delayed exit = %+v", mt)
				}
				// 桌面端同样通过 FallbackTitle 保留退出前兜底标题，不退回 shell OSC。
				if info := d.terminalInfo(s); info.FallbackTitle != "codex" {
					t.Fatalf("desktop fallback = %+v", info)
				}
				return
			}
		}
	}
	t.Fatal("codex terminal missing from mobile state")
}

// 无会话名和有会话名的 worker 在运行 → 退出 → 重启未恢复 → 恢复 shell 四步中标题保持一致，
// 且退出后支持手动改名与清空恢复。
func TestMobileWorkerTitleStableAcrossLifecycle(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PATH", "/usr/bin:/bin")
	d, w := newTestDaemon(t)
	d.launcher = agent.Launcher{Exe: "/bin/true", Home: d.cfg.Home}

	findIn := func(dm *Daemon, id string) mobile.Terminal {
		t.Helper()
		for _, mw := range (mobileHost{dm}).State().Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == id {
					return mt
				}
			}
		}
		t.Fatalf("terminal %s not in mobile state", id)
		return mobile.Terminal{}
	}

	unnamed, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	named, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range []store.Task{
		{ID: store.NewID("k_"), Title: "Worker task", Spec: "s", WorkspaceID: w.ID, Worker: unnamed.ID, Agent: "codex", Status: store.TaskDone, CreatedAt: time.Now()},
		{ID: store.NewID("k_"), Title: "Worker task", Spec: "s", WorkspaceID: w.ID, Worker: named.ID, Agent: "codex", Status: store.TaskDone, CreatedAt: time.Now()},
	} {
		if err := d.store.AddTask(tk); err != nil {
			t.Fatal(err)
		}
	}
	named.SetTitle("Named session", "sid-named", false)

	// 1. 运行中
	if got := findIn(d, unnamed.ID).Title; got != "Worker task" {
		t.Fatalf("running unnamed = %q", got)
	}
	if got := findIn(d, named.ID).Title; got != "Named session" {
		t.Fatalf("running named = %q", got)
	}

	// 2. 退出回 shell
	d.setAgent(unnamed, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	d.setAgent(named, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	d.saveResume(unnamed, true)
	d.saveResume(named, true)
	if got := findIn(d, unnamed.ID).Title; got != "Worker task" {
		t.Fatalf("exited unnamed = %q", got)
	}
	if got := findIn(d, named.ID).Title; got != "Named session" {
		t.Fatalf("exited named = %q", got)
	}

	// 退出后手动改名与清空改名
	call(t, d.terminalRename, map[string]any{"terminal": named.ID, "name": "Manual"})
	if got := findIn(d, named.ID).Title; got != "Manual" {
		t.Fatalf("renamed exited = %q", got)
	}
	call(t, d.terminalRename, map[string]any{"terminal": named.ID, "name": ""})
	if got := findIn(d, named.ID).Title; got != "Named session" {
		t.Fatalf("cleared rename = %q", got)
	}

	// 3. 重启变成 Lost
	d.exiting.Store(true)
	d.closeAll()
	if _, err := d.store.MarkRunningLost(nil); err != nil {
		t.Fatal(err)
	}
	d2 := &Daemon{cfg: d.cfg, store: d.store, terms: map[string]*session{}, tchanged: make(chan struct{}), launcher: d.launcher}
	t.Cleanup(d2.closeAll)
	if got := findIn(d2, unnamed.ID).Title; got != "Worker task" {
		t.Fatalf("lost unnamed = %q", got)
	}
	if got := findIn(d2, named.ID).Title; got != "Named session" {
		t.Fatalf("lost named = %q", got)
	}

	// 4. 恢复为 shell
	if _, err := d2.session(unnamed.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.session(named.ID); err != nil {
		t.Fatal(err)
	}
	if got := findIn(d2, unnamed.ID).Title; got != "Worker task" {
		t.Fatalf("restored unnamed = %q", got)
	}
	if got := findIn(d2, named.ID).Title; got != "Named session" {
		t.Fatalf("restored named = %q", got)
	}
}

// Claude 退出后同一终端启动 Cursor 只收到 sessionStart 时，不继承 Claude 的退出态、旧标题、旧会话 ID 与旧库记录。
func TestCursorSessionStartAfterClaudeExitResetsState(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTitle("Old Claude title", "old-claude", false)
	d.saveResume(s, false)
	rawEnd, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "claude", "event": "SessionEnd",
		"payload": map[string]any{"session_id": "old-claude", "reason": "prompt_input_exit"},
	})
	if _, err := d.hookEvent(context.Background(), rawEnd); err != nil {
		t.Fatal(err)
	}
	d.saveResume(s, true)

	rawStart, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "sessionStart",
		"payload": map[string]any{"session_id": "new-cursor"},
	})
	if _, err := d.hookEvent(context.Background(), rawStart); err != nil {
		t.Fatal(err)
	}
	if s.agentName() != "cursor" {
		t.Fatalf("agent = %q, want cursor", s.agentName())
	}
	st := s.Agent()
	if st.Status == agent.StatusExited || st.Title != "" || st.SessionID != "new-cursor" {
		t.Fatalf("cursor state inherited claude: %+v", st)
	}
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID {
				if mt.Agent != "cursor" || mt.Status == agent.StatusExited || mt.Title != "" || mt.SessionID != "new-cursor" {
					t.Fatalf("mobile cursor = %+v", mt)
				}
			}
		}
	}
	r, err := d.store.TerminalResume(s.ID)
	if err != nil || r.Agent != "cursor" || r.SessionID != "new-cursor" || r.SessionTitle != "" || r.Label != "" || r.FallbackTitle != "" {
		t.Fatalf("stored cursor resume = %+v, %v", r, err)
	}
}

// 被映射层忽略的子 agent 退出包（带 agent_id 的 Codex SessionEnd）不能认领正在跑 Claude 的终端。
func TestSubagentSessionEndCannotClaimTerminal(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAgent(host.AgentState{Status: agent.StatusWorking, SessionID: "cl-main", Title: "Claude Main", At: time.Now()})
	raw, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "codex", "event": "SessionEnd",
		"payload": map[string]any{"session_id": "codex-sub", "agent_id": "sub-1", "reason": "other"},
	})
	if _, err := d.hookEvent(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if s.agentName() != "claude" || s.Agent().SessionID != "cl-main" || s.Agent().Status != agent.StatusWorking {
		t.Fatalf("subagent SessionEnd hijacked terminal: agent=%q state=%+v", s.agentName(), s.Agent())
	}
}

// 同一 agent 进程切换会话后，已经进入提交阶段的旧会话退出包不能把新会话摘掉。
func TestSameProcessSessionSwitchRejectsStaleExit(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_, gen := s.agentIdentity()
	d.setAgentFor(s, host.AgentState{Status: agent.StatusWorking, SessionID: "previous-sid", At: time.Now()}, gen)
	// 同进程、同代际切到新会话
	d.setAgentFor(s, host.AgentState{Status: agent.StatusWorking, SessionID: "fresh-sid", Title: "Fresh", At: time.Now()}, gen)
	// 旧会话退出包带着相同代际到达，必须因 SessionID 不符被拒
	d.setAgentFor(s, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", SessionID: "previous-sid", At: time.Now()}, gen)
	if s.agentName() != "claude" || s.Agent().SessionID != "fresh-sid" || s.Agent().Status != agent.StatusWorking {
		t.Fatalf("stale exit cleared switched session: agent=%q state=%+v", s.agentName(), s.Agent())
	}
	// Codex 延迟退出回调同样不能摘掉同代际的新会话
	codex, err := d.startTerminal(w, []string{"/bin/cat"}, "codex", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	_, cgen := codex.agentIdentity()
	d.setAgentFor(codex, host.AgentState{Status: agent.StatusWorking, SessionID: "fresh-codex", At: time.Now()}, cgen)
	d.codexSessionEnded(codex, "old-codex", "old-codex", cgen)
	if codex.agentName() != "codex" || codex.Agent().SessionID != "fresh-codex" || codex.Agent().Status != agent.StatusWorking {
		t.Fatalf("delayed codex exit cleared switched session: agent=%q state=%+v", codex.agentName(), codex.Agent())
	}
}

// 改名与 agent 切换重叠时，旧改名提交必须返回冲突并保留新实例的手动名与会话状态。
func TestSessionTitleConflictOnAgentSwitchPreservesManualName(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	oldAgent, oldGen := s.agentIdentity()
	s.SetAgent(host.AgentState{Status: agent.StatusIdle, SessionID: "cur-1", At: time.Now()})

	// 模拟改名暂停期间终端切换到 Claude 并被用户手动命名
	newGen := s.markHooked("claude", "cl-2")
	d.setAgentFor(s, host.AgentState{Status: agent.StatusWorking, SessionID: "cl-2", At: time.Now()}, newGen)
	call(t, d.terminalRename, map[string]any{"terminal": s.ID, "name": "Fresh manual name"})

	err = d.applySessionTitle(s, oldAgent, oldGen, "cur-1", "cur-1", "Stale Cursor Title", false)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeConflict {
		t.Fatalf("applySessionTitle err = %v, want CodeConflict", err)
	}
	if s.userName() != "Fresh manual name" || s.agentName() != "claude" || s.Agent().SessionID != "cl-2" || s.Agent().Title != "" {
		t.Fatalf("stale applySessionTitle mutated new agent: name=%q agent=%q state=%+v", s.userName(), s.agentName(), s.Agent())
	}
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Title != "Fresh manual name" {
		t.Fatalf("stored title = %+v, %v; want Fresh manual name", r, err)
	}
}

// 同一终端切到新 agent 时，PTY 里残留的旧 OSC 标题不能串给新 agent；
// 反过来，已确认的 OSC 标题在被清空后立即保存，桌面、手机与 Lost 记录都保留它。
func TestConfirmedOSCTitleIsolatedAcrossAgentSwitchAndClear(t *testing.T) {
	d, w := newTestDaemon(t)
	// 1. 旧 OSC 仍在：PTY 写入 "Previous Claude OSC" 后 Claude 退出，Cursor 发 sessionStart 且未写新 OSC。
	s, err := d.startTerminal(w, []string{"/bin/sh", "-c", `printf '\033]0;Previous Claude OSC\007'; exec sleep 30`}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "previous OSC title", func() bool { return s.Title() == "Previous Claude OSC" })
	d.setAgent(s, host.AgentState{Status: agent.StatusExited, Event: "SessionEnd", At: time.Now()})
	d.saveResume(s, true)

	rawStart, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "sessionStart",
		"payload": map[string]any{"session_id": "cursor-fresh"},
	})
	if _, err := d.hookEvent(context.Background(), rawStart); err != nil {
		t.Fatal(err)
	}
	if info := d.terminalInfo(s); info.Agent != "cursor" || info.Title != "" || info.FallbackTitle != "" {
		t.Fatalf("desktop cursor leaked previous OSC: %+v", info)
	}
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID && mt.Title != "" {
				t.Fatalf("mobile cursor leaked previous OSC: %+v", mt)
			}
		}
	}
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Agent != "cursor" || r.Label != "" || r.FallbackTitle != "" {
		t.Fatalf("stored cursor leaked previous OSC: %+v, %v", r, err)
	}
	// Cursor 退出时也不能把 PTY 残留的旧 Claude OSC 捕获成退出兜底名
	rawEnd, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "sessionEnd",
		"payload": map[string]any{"session_id": "cursor-fresh"},
	})
	if _, err := d.hookEvent(context.Background(), rawEnd); err != nil {
		t.Fatal(err)
	}
	if info := d.terminalInfo(s); info.FallbackTitle != "cursor" {
		t.Fatalf("exited cursor fallback = %+v, want cursor", info)
	}

	// 2. 已确认 OSC 被清空后立即保存：桌面、手机与 Lost 记录都保留确认过的标题。
	fakeClaude := filepath.Join(t.TempDir(), "claude")
	if err := os.Symlink("/bin/cat", fakeClaude); err != nil {
		t.Fatal(err)
	}
	c, err := d.startTerminal(w, []string{fakeClaude}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	d.onTitle(c.ID, "✳ Confirmed Claude OSC")
	d.onTitle(c.ID, "")
	d.saveResume(c, false)
	if info := d.terminalInfo(c); info.Title != "Confirmed Claude OSC" || info.FallbackTitle != "Confirmed Claude OSC" {
		t.Fatalf("desktop cleared OSC = %+v", info)
	}
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == c.ID && mt.Title != "Confirmed Claude OSC" {
				t.Fatalf("mobile cleared OSC = %+v", mt)
			}
		}
	}
	if r, err := d.store.TerminalResume(c.ID); err != nil || r.FallbackTitle != "Confirmed Claude OSC" || r.Label != "Confirmed Claude OSC" {
		t.Fatalf("stored cleared OSC = %+v, %v", r, err)
	}
}

// Cursor 同实例先收到工具事件进入工作中，随后收到带 session_id 的 sessionStart 补记 ID 时不清空当前状态。
func TestCursorSessionStartPreservesWorkingState(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "cursor", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	rawTool, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "postToolUse",
		"payload": map[string]any{"tool_name": "Bash"},
	})
	if _, err := d.hookEvent(context.Background(), rawTool); err != nil {
		t.Fatal(err)
	}
	before := s.Agent()
	if before.Status != agent.StatusWorking || before.Event != "postToolUse" || before.SessionID != "" {
		t.Fatalf("before sessionStart = %+v", before)
	}
	rawStart, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "sessionStart",
		"payload": map[string]any{"session_id": "cur-bound-1"},
	})
	if _, err := d.hookEvent(context.Background(), rawStart); err != nil {
		t.Fatal(err)
	}
	after := s.Agent()
	if after.SessionID != "cur-bound-1" || after.Status != agent.StatusWorking || after.Event != "postToolUse" {
		t.Fatalf("sessionStart cleared working state: before=%+v after=%+v", before, after)
	}
}

// 终端里已有运行中的 Claude 时，迟到或嵌套的异家普通 hook（Cursor stop）不能换掉当前 agent，
// 而已绑定的 Claude 让出前台期间仍能正常上报状态。
func TestLateCrossAgentHookIgnoredWhileAnotherAgentRuns(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hook(t, d, s.ID, "UserPromptSubmit", map[string]any{"session_id": "claude-current"})
	d.saveResume(s, false)

	rawLate, _ := json.Marshal(map[string]any{
		"terminalId": s.ID, "agent": "cursor", "event": "stop",
		"payload": map[string]any{"session_id": "cursor-old"},
	})
	if _, err := d.hookEvent(context.Background(), rawLate); err != nil {
		t.Fatal(err)
	}
	if s.agentName() != "claude" || s.Agent().SessionID != "claude-current" || s.Agent().Status != agent.StatusWorking {
		t.Fatalf("late cursor hook hijacked claude: agent=%q state=%+v", s.agentName(), s.Agent())
	}
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Agent != "claude" || r.SessionID != "claude-current" {
		t.Fatalf("stored resume corrupted by late hook: %+v, %v", r, err)
	}

	// 已绑定的 Claude 在前台不是 claude（模拟让出前台开编辑器）时仍可正常上报 Stop。
	hook(t, d, s.ID, "Stop", map[string]any{"session_id": "claude-current"})
	if s.agentName() != "claude" || s.Agent().Status != agent.StatusIdle {
		t.Fatalf("bound claude could not report while yielded: agent=%q state=%+v", s.agentName(), s.Agent())
	}
}

type earlyTitleHost struct {
	host.Local
	got chan struct{}
}

func (h *earlyTitleHost) Start(opts host.StartOptions) (host.Terminal, error) {
	orig := opts.OnTitle
	opts.OnTitle = func(id, title string) {
		if orig != nil {
			orig(id, title)
		}
		if title != "" {
			select {
			case h.got <- struct{}{}:
			default:
			}
		}
	}
	t, err := h.Local.Start(opts)
	if err != nil {
		return nil, err
	}
	select {
	case <-h.got:
	case <-time.After(5 * time.Second):
	}
	return t, nil
}

// 启动阶段唯一的 OSC 标题在 d.terms[id] 登记前就到达时，桌面、手机与恢复记录都必须保留该标题。
func TestStartupSingleEarlyOSCTitlePreserved(t *testing.T) {
	d, w := newTestDaemon(t)
	d.host = &earlyTitleHost{got: make(chan struct{}, 1)}
	script := `exec -a claude /bin/sh -c 'printf "\033]0;Initial Claude title\007"; sleep 30'`
	s, err := d.startTerminal(w, []string{"/bin/zsh", "-f", "-c", script}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title() != "Initial Claude title" {
		t.Fatalf("raw PTY title = %q, want Initial Claude title", s.Title())
	}
	if info := d.terminalInfo(s); info.Title != "Initial Claude title" || info.FallbackTitle != "Initial Claude title" {
		t.Fatalf("desktop early OSC = %+v", info)
	}
	found := false
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID {
				found = true
				if mt.Title != "Initial Claude title" {
					t.Fatalf("mobile early OSC = %+v", mt)
				}
			}
		}
	}
	if !found {
		t.Fatal("terminal missing from mobile state")
	}
	d.saveResume(s, false)
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Label != "Initial Claude title" || r.FallbackTitle != "Initial Claude title" {
		t.Fatalf("stored early OSC = %+v, %v", r, err)
	}
}

type loginShellExecHost struct {
	host.Local
	t       *testing.T
	sigPath string
	got     chan struct{}
}

func (h *loginShellExecHost) Start(opts host.StartOptions) (host.Terminal, error) {
	orig := opts.OnTitle
	opts.OnTitle = func(id, title string) {
		if orig != nil {
			orig(id, title)
		}
		if title == "Login shell title" {
			_ = os.WriteFile(h.sigPath, []byte("go"), 0o600)
			select {
			case h.got <- struct{}{}:
			default:
			}
		}
	}
	t, err := h.Local.Start(opts)
	if err != nil {
		return nil, err
	}
	select {
	case <-h.got:
	case <-time.After(5 * time.Second):
		h.t.Fatal("timed out waiting for login shell OSC")
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pg := t.Foreground(); pg > 0 {
			if found, _ := agent.OfProcess(pg); found == "claude" {
				return t, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatal("timed out waiting for login shell to exec into claude")
	return t, nil
}

// 启动阶段登录 shell 先发出 OSC 标题，再 exec 成被识别为 Claude 的进程时，
// 回放不得把登录 shell 的标题误归给随后启动的 Claude。
func TestStartupLoginShellOSCTitleNotAttributedToExecAgent(t *testing.T) {
	d, w := newTestDaemon(t)
	sigPath := filepath.Join(t.TempDir(), "exec.go")
	d.host = &loginShellExecHost{t: t, sigPath: sigPath, got: make(chan struct{}, 1)}
	script := `printf '\033]0;Login shell title\007'; while [ ! -f "` + sigPath + `" ]; do sleep 0.01; done; exec -a claude /bin/sh -c 'sleep 30; true'`
	s, err := d.startTerminal(w, []string{"/bin/zsh", "-f", "-c", script}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title() != "Login shell title" {
		t.Fatalf("raw PTY title = %q, want Login shell title", s.Title())
	}
	if info := d.terminalInfo(s); info.Title != "" || info.FallbackTitle != "" {
		t.Fatalf("desktop attributed login shell OSC to claude: %+v", info)
	}
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID && mt.Title != "" {
				t.Fatalf("mobile attributed login shell OSC to claude: %+v", mt)
			}
		}
	}
	d.saveResume(s, false)
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Label != "" || r.FallbackTitle != "" {
		t.Fatalf("stored attributed login shell OSC to claude: %+v, %v", r, err)
	}
}

type twoClaudeGroupsHost struct {
	host.Local
	t       *testing.T
	sigPath string
	got     chan struct{}
}

func (h *twoClaudeGroupsHost) Start(opts host.StartOptions) (host.Terminal, error) {
	orig := opts.OnTitle
	opts.OnTitle = func(id, title string) {
		if orig != nil {
			orig(id, title)
		}
		if title == "Old Claude process title" {
			select {
			case h.got <- struct{}{}:
			default:
			}
		}
	}
	t, err := h.Local.Start(opts)
	if err != nil {
		return nil, err
	}
	select {
	case <-h.got:
	case <-time.After(5 * time.Second):
		h.t.Fatal("timed out waiting for first claude OSC")
	}
	firstPg := t.Foreground()
	if firstPg <= 0 {
		h.t.Fatal("first claude foreground pgid is 0")
	}
	if found, _ := agent.OfProcess(firstPg); found != "claude" {
		h.t.Fatalf("first foreground pg %d = %q, want claude", firstPg, found)
	}
	if err := os.WriteFile(h.sigPath, []byte("go"), 0o600); err != nil {
		h.t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if pg := t.Foreground(); pg > 0 && pg != firstPg {
			if found, _ := agent.OfProcess(pg); found == "claude" {
				return t, nil
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for second distinct claude process group (firstPg=%d)", firstPg)
	return t, nil
}

// 启动阶段第一个被识别为 Claude 的进程组发出 OSC 标题后退出，另一个不同进程组的 Claude 进入前台且不发标题时，
// 回放必须按进程组拒绝旧标题，桌面、手机与 SQLite 都不得污染新 Claude 进程组。
func TestStartupDistinctSameAgentProcessGroupRejectsOldOSC(t *testing.T) {
	d, w := newTestDaemon(t)
	sigPath := filepath.Join(t.TempDir(), "next.go")
	d.host = &twoClaudeGroupsHost{t: t, sigPath: sigPath, got: make(chan struct{}, 1)}
	script := `(exec -a claude /bin/sh -c 'printf "\033]0;Old Claude process title\007"; while [ ! -f "` + sigPath + `" ]; do sleep 0.01; done'); (exec -a claude /bin/sh -c 'sleep 30; true')`
	s, err := d.startTerminal(w, []string{"/bin/zsh", "-f", "-i", "-m", "-c", script}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	if s.Title() != "Old Claude process title" {
		t.Fatalf("raw PTY title = %q, want Old Claude process title", s.Title())
	}
	if info := d.terminalInfo(s); info.Title != "" || info.FallbackTitle != "" {
		t.Fatalf("desktop leaked old claude group OSC: %+v", info)
	}
	for _, mw := range (mobileHost{d}).State().Workspaces {
		for _, mt := range mw.Terminals {
			if mt.ID == s.ID && mt.Title != "" {
				t.Fatalf("mobile leaked old claude group OSC: %+v", mt)
			}
		}
	}
	d.saveResume(s, false)
	if r, err := d.store.TerminalResume(s.ID); err != nil || r.Label != "" || r.FallbackTitle != "" {
		t.Fatalf("stored leaked old claude group OSC: %+v, %v", r, err)
	}
}

type gatedAgentTerminal struct {
	host.Terminal
	arm     chan struct{}
	entered chan struct{}
	release chan struct{}
}

func (g *gatedAgentTerminal) Agent() host.AgentState {
	select {
	case <-g.arm:
		g.entered <- struct{}{}
		<-g.release
	default:
	}
	return g.Terminal.Agent()
}

// 手机和桌面查询正在切换 agent 的终端时，不能拼出新 agent 搭配旧标题/旧会话 ID 的撕裂快照。
func TestSnapshotViewAtomicDuringAgentSwitch(t *testing.T) {
	d, w := newTestDaemon(t)
	s, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	s.SetTitle("Old Claude title", "claude-sid", false)
	gt := &gatedAgentTerminal{
		Terminal: s.Terminal,
		arm:      make(chan struct{}, 1),
		entered:  make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	s.Terminal = gt

	// 闸门卡在手机查询读 AgentState 的瞬间，并发发起切到已命名 Cursor。
	gt.arm <- struct{}{}
	stateCh := make(chan mobile.State, 1)
	go func() {
		stateCh <- (mobileHost{d}).State()
	}()
	<-gt.entered

	switched := make(chan struct{})
	go func() {
		gen := s.markHooked("cursor", "cursor-sid")
		d.setAgentFor(s, host.AgentState{Status: agent.StatusWorking, SessionID: "cursor-sid", Title: "New Cursor title", At: time.Now()}, gen)
		close(switched)
	}()
	// 给并发切换足够时间撞上 s.mu，验证 snapshotView 持锁隔离了此次切换。
	time.Sleep(15 * time.Millisecond)
	close(gt.release)

	st := <-stateCh
	<-switched
	find := func(ms mobile.State) mobile.Terminal {
		t.Helper()
		for _, mw := range ms.Workspaces {
			for _, mt := range mw.Terminals {
				if mt.ID == s.ID {
					return mt
				}
			}
		}
		t.Fatal("terminal missing from mobile state")
		return mobile.Terminal{}
	}
	snap1 := find(st)
	if snap1.Agent != "claude" || snap1.SessionID != "claude-sid" || snap1.Title != "Old Claude title" {
		t.Fatalf("torn mobile snapshot during switch = %+v", snap1)
	}
	snap2 := find((mobileHost{d}).State())
	if snap2.Agent != "cursor" || snap2.SessionID != "cursor-sid" || snap2.Title != "New Cursor title" {
		t.Fatalf("post-switch mobile snapshot = %+v", snap2)
	}
	if info := d.terminalInfo(s); info.Agent != "cursor" || info.AgentState == nil || info.AgentState.SessionID != "cursor-sid" || info.AgentState.Title != "New Cursor title" {
		t.Fatalf("post-switch desktop info = %+v", info)
	}
}
