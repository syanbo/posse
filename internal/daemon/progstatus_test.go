package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"posse/internal/agent"
	"posse/internal/host"
)

func oscTerminal(t *testing.T, name string) (*Daemon, *session, func(event string, payload map[string]any)) {
	t.Helper()
	d, ws := newTestDaemon(t)
	s, err := d.startTerminal(ws, []string{"/bin/cat"}, name, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	hook := func(event string, payload map[string]any) {
		t.Helper()
		payload["session_id"] = "sid"
		raw, _ := json.Marshal(map[string]any{"terminalId": s.ID, "agent": name, "event": event, "payload": payload})
		if _, err := d.hookEvent(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
	}
	return d, s, hook
}

func wantStatus(t *testing.T, s *session, status string) host.AgentState {
	t.Helper()
	st := s.Agent()
	if st.Status != status {
		t.Fatalf("status %q, want %q (%+v)", st.Status, status, st)
	}
	return st
}

// 报过 7501 之后工作中、空闲以它为准：Esc 中断没有 hook 也回到空闲，之后晚到的 hook 改不了状态；
// 会话 ID、Stop 带的最后一条回复照旧来自 hook。事件顺序照 Claude Code 2.1.318 实测。
func TestOSCStatusOverridesHooks(t *testing.T) {
	d, s, hook := oscTerminal(t, "claude")
	osc := func(state string) { d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "claude-code"}) }

	hook("UserPromptSubmit", map[string]any{})
	osc("working")
	osc("idle") // 按 Esc 中断：Claude 不发 hook
	wantStatus(t, s, agent.StatusIdle)
	hook("PostToolUse", map[string]any{"tool_name": "Bash"})
	wantStatus(t, s, agent.StatusIdle)

	osc("working")
	hook("Stop", map[string]any{"last_assistant_message": "all done"})
	wantStatus(t, s, agent.StatusWorking)
	osc("done")
	if st := wantStatus(t, s, agent.StatusIdle); st.Message != "all done" || st.SessionID != "sid" {
		t.Fatalf("lost hook fields %+v", st)
	}
	if !s.isUnread() {
		t.Fatal("finished turn not unread")
	}
}

// hook 记下的等待（带审批卡）不被同一状态下的工作中说明冲掉，7501 的 blocked 也留着卡；离开等待才清。
func TestOSCKeepsHookWaiting(t *testing.T) {
	d, s, hook := oscTerminal(t, "claude")
	osc := func(state, msg string) {
		d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "claude-code", Msg: msg})
	}
	hook("UserPromptSubmit", map[string]any{})
	osc("working", "")
	hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "rm -rf build"}})
	st := wantStatus(t, s, agent.StatusWaiting)
	if st.Prompt == "" {
		t.Fatalf("no approval card %+v", st)
	}
	osc("working", "Removing build") // 挂着等手机时还在报工作中
	wantStatus(t, s, agent.StatusWaiting)
	osc("blocked", "permission")
	if st := wantStatus(t, s, agent.StatusWaiting); st.Prompt == "" {
		t.Fatalf("blocked dropped the card %+v", st)
	}
	osc("working", "")
	if st := wantStatus(t, s, agent.StatusWorking); st.Prompt != "" || st.Message != "" {
		t.Fatalf("card left %+v", st)
	}
	// 在等待时按 Esc：回到空闲，不带着等待的说明
	hook("Notification", map[string]any{"notification_type": "permission_prompt", "message": "Claude needs your permission"})
	osc("blocked", "")
	osc("idle", "")
	if st := wantStatus(t, s, agent.StatusIdle); st.Message != "" {
		t.Fatalf("kept waiting message %+v", st)
	}
}

// 7501 先报等待时用它的说明；别的程序报的、clear 之后都回到 hooks 和标题。
func TestOSCOtherAppsAndClear(t *testing.T) {
	d, s, hook := oscTerminal(t, "claude")
	hook("UserPromptSubmit", map[string]any{})
	d.onProgramStatus(s.ID, host.ProgramStatus{State: "blocked", App: "claude-code", Msg: "red or blue?"})
	if st := wantStatus(t, s, agent.StatusWaiting); st.Message != "red or blue?" {
		t.Fatalf("message %+v", st)
	}
	d.onProgramStatus(s.ID, host.ProgramStatus{State: "working", App: "brew"})
	wantStatus(t, s, agent.StatusWaiting)

	d.onProgramStatus(s.ID, host.ProgramStatus{State: "idle", App: "claude-code"})
	// 报过 7501 不再看标题：Claude 这时不往标题里写状态，✳ 不算空闲信号
	d.onProgramStatus(s.ID, host.ProgramStatus{State: "working", App: "claude-code"})
	d.onTitle(s.ID, "✳ Claude Code")
	wantStatus(t, s, agent.StatusWorking)

	d.onProgramStatus(s.ID, host.ProgramStatus{State: "clear", App: "claude-code"})
	hook("Stop", map[string]any{})
	wantStatus(t, s, agent.StatusIdle)
}

// 后台子 agent 还在跑时 7501 报空闲：仍算工作中，但主线程已停下，可以往终端里打提醒。
func TestOSCIdleWithSubagents(t *testing.T) {
	d, s, hook := oscTerminal(t, "claude")
	d.onProgramStatus(s.ID, host.ProgramStatus{State: "working", App: "claude-code"})
	hook("Stop", map[string]any{"background_tasks": []map[string]any{{"id": "a1", "type": "subagent", "status": "running"}}})
	d.onProgramStatus(s.ID, host.ProgramStatus{State: "done", App: "claude-code"})
	if st := wantStatus(t, s, agent.StatusWorking); !agent.Parked(st) {
		t.Fatalf("not parked %+v", st)
	}
}

// Codex 启动时先报两秒工作中：带 hooks 时一轮从 hook 或键入开始，这段不算，刚开的终端不成未读。
func TestOSCIgnoresStartupWork(t *testing.T) {
	d, s, hook := oscTerminal(t, "codex")
	osc := func(state string) { d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "codex"}) }
	before := s.Agent().Status
	osc("idle")
	osc("working")
	if st := s.Agent(); st.Status == agent.StatusWorking {
		t.Fatalf("startup counted as work %+v (was %q)", st, before)
	}
	osc("idle")
	if s.isUnread() {
		t.Fatal("startup left the terminal unread")
	}
	osc("working") // 实测 Codex 0.166.0：键入提交后 7501 比 UserPromptSubmit 先到
	hook("UserPromptSubmit", map[string]any{})
	wantStatus(t, s, agent.StatusWorking)
	osc("done")
	wantStatus(t, s, agent.StatusIdle)
	if !s.isUnread() {
		t.Fatal("finished turn not unread")
	}
}

// 在 shell 里手动启动的 Codex 第一个 hook 之前不算带 hooks，没经同名脚本启动的根本没有：启动时的工作中照样不算。
func TestOSCIgnoresStartupWorkOfManualCodex(t *testing.T) {
	d, s, _ := oscTerminal(t, "codex")
	s.mu.Lock()
	s.hooked = false
	s.mu.Unlock()
	for _, state := range []string{"idle", "working", "idle"} {
		d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "codex"})
		if st := s.Agent(); st.Status == agent.StatusWorking {
			t.Fatalf("startup counted as work after %q: %+v", state, st)
		}
	}
	if s.isUnread() {
		t.Fatal("startup left the terminal unread")
	}
}

// 报过 7501 后标题的 Action Required 仍能升成等待，标题不这样写了退回 7501 报的状态；hook 记的等待不受标题影响。
func TestOSCTitleWaiting(t *testing.T) {
	d, s, hook := oscTerminal(t, "codex")
	osc := func(state string) { d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "codex"}) }
	hook("UserPromptSubmit", map[string]any{})
	osc("working")
	d.onTitle(s.ID, "Action Required - posse")
	wantStatus(t, s, agent.StatusWaiting)
	d.onTitle(s.ID, "⠋ posse")
	wantStatus(t, s, agent.StatusWorking)

	hook("PermissionRequest", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}})
	d.onTitle(s.ID, "⠋ posse")
	if st := wantStatus(t, s, agent.StatusWaiting); st.Prompt == "" {
		t.Fatalf("title dropped the hook card %+v", st)
	}
}

// posse 用 --prompt 派出去、没人键入的 Grok：记着 hooked 也等不到 hook，第一轮的 7501 工作中照样算数，完成记未读。
func TestOSCGrokFirstTurnWithoutInput(t *testing.T) {
	d, s, _ := oscTerminal(t, "grok")
	if !s.isHooked() {
		t.Fatal("posse-started grok expected to be marked hooked")
	}
	for _, state := range []string{"idle", "working"} {
		d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "grok-build"})
	}
	wantStatus(t, s, agent.StatusWorking)
	d.onProgramStatus(s.ID, host.ProgramStatus{State: "done", App: "grok-build"})
	wantStatus(t, s, agent.StatusIdle)
	if !s.isUnread() {
		t.Fatal("finished turn not unread")
	}
}

// 标题升等待、再退回时沿用当前状态：空闲时 Stop 带来的最后一条回复不丢。
func TestOSCTitleWaitingKeepsMessage(t *testing.T) {
	d, s, hook := oscTerminal(t, "codex")
	osc := func(state string) { d.onProgramStatus(s.ID, host.ProgramStatus{State: state, App: "codex"}) }
	hook("UserPromptSubmit", map[string]any{})
	osc("working")
	hook("Stop", map[string]any{"last_assistant_message": "all done"})
	osc("done")
	d.onTitle(s.ID, "Action Required - posse")
	if st := wantStatus(t, s, agent.StatusWaiting); st.Message != "all done" {
		t.Fatalf("waiting lost message %+v", st)
	}
	d.onTitle(s.ID, "posse")
	if st := wantStatus(t, s, agent.StatusIdle); st.Message != "all done" {
		t.Fatalf("idle lost message %+v", st)
	}
}
