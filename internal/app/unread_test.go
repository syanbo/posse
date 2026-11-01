package app

import (
	"testing"

	"posse/internal/agent"
)

func TestUnreadStatus(t *testing.T) {
	a := &app{}
	idle := &agentState{Status: agent.StatusIdle}
	ts := []terminalInfo{
		{ID: "a", Agent: "claude", Running: true, AgentState: &agentState{Status: agent.StatusWorking}},
		{ID: "b", Agent: "codex", Running: true, AgentState: idle},
	}
	if _, s := a.terminalSummary(ts); s != agent.StatusWorking {
		t.Fatalf("summary = %q, want working", s)
	}
	ts[1].Unread = true
	if s := a.sessionStatus(ts[1]); s != statusUnread {
		t.Fatalf("session = %q, want unread", s)
	}
	// 完成了没看的比工作中要紧，比待处理次要
	if _, s := a.terminalSummary(ts); s != statusUnread {
		t.Fatalf("summary = %q, want unread", s)
	}
	ts[0].AgentState = &agentState{Status: agent.StatusWaiting}
	if _, s := a.terminalSummary(ts); s != agent.StatusWaiting {
		t.Fatalf("summary = %q, want waiting", s)
	}
	// 窗口在后台时看着也不算看过
	a.markRead(&ts[1])
	if a.viewing != "" || a.sessionStatus(ts[1]) != statusUnread {
		t.Fatal("read while unfocused")
	}
	// 有焦点看着时不画铃铛，等常驻进程收到看过
	a.focused.Store(true)
	a.seenFor = ts[1].ID // 不真发 terminal.seen
	a.markRead(&ts[1])
	if s := a.sessionStatus(ts[1]); s != "" {
		t.Fatalf("viewed session = %q, want none", s)
	}
	ts[1].Unread = false
	a.markRead(&ts[1])
	if a.seenFor != "" {
		t.Fatal("seenFor kept after the daemon cleared unread")
	}
}

// 在等任务的会话（侧栏会话行画波形）汇总进工作区、项目和固定磁贴时也是等任务，不因为它本轮完成没看就摇铃铛。
func TestSummaryWaitingTasks(t *testing.T) {
	a := &app{runningFor: map[string]int{"b": 1}}
	ts := []terminalInfo{
		{ID: "a", Agent: "claude", Running: true, AgentState: &agentState{Status: agent.StatusIdle}},
		{ID: "b", Agent: "claude", Running: true, Unread: true, AgentState: &agentState{Status: agent.StatusIdle}},
	}
	if s := a.sessionStatus(ts[1]); s != statusTasks {
		t.Fatalf("session = %q, want tasks", s)
	}
	if _, s := a.terminalSummary(ts); s != statusTasks {
		t.Fatalf("summary = %q, want tasks", s)
	}
	// 别的会话在工作时，汇总是工作中（等任务排在工作中后面）
	ts[0].AgentState = &agentState{Status: agent.StatusWorking}
	if _, s := a.terminalSummary(ts); s != agent.StatusWorking {
		t.Fatalf("summary = %q, want working", s)
	}
}
