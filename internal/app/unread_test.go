package app

import (
	"testing"

	"hive/internal/agent"
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
	a.markUnread("b")
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
	if !a.unread["b"] {
		t.Fatal("read while unfocused")
	}
	a.focused.Store(true)
	a.markRead(&ts[1])
	if a.unread["b"] {
		t.Fatal("still unread after viewing")
	}
}
