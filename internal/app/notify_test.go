package app

import (
	"testing"

	"posse/internal/agent"
	"posse/locales"
)

func TestNoticeTitle(t *testing.T) {
	claude := terminalInfo{ID: "t1", Agent: "claude"}
	unread := claude
	unread.Unread = true
	cases := []struct {
		name         string
		prev, status string
		t            terminalInfo
		want         string
	}{
		{"进入等待", agent.StatusWorking, agent.StatusWaiting, claude, "Claude 等待你处理"},
		{"空闲直接进入等待", agent.StatusIdle, agent.StatusWaiting, claude, "Claude 等待你处理"},
		{"完成且未读", agent.StatusWorking, agent.StatusIdle, unread, "Claude 已完成"},
		{"完成但手机看着（不算未读）", agent.StatusWorking, agent.StatusIdle, claude, ""},
		{"从等待回到空闲", agent.StatusWaiting, agent.StatusIdle, unread, ""},
		{"状态没变", agent.StatusWaiting, agent.StatusWaiting, claude, ""},
		{"开始工作", agent.StatusIdle, agent.StatusWorking, claude, ""},
		{"普通终端", "", agent.StatusWaiting, terminalInfo{ID: "t2"}, ""},
	}
	for _, c := range cases {
		if got := noticeTitle(locales.In("zh-Hans"), c.prev, c.status, c.t); got != c.want {
			t.Errorf("%s: noticeTitle = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBadgeCount(t *testing.T) {
	st := func(s string) *agentState { return &agentState{Status: s} }
	a := &app{runningFor: map[string]int{"h": 1}}
	a.focused.Store(true)
	a.workspaces = []workspace{
		{ID: "w1", Terminals: []terminalInfo{
			{ID: "a", Agent: "claude", AgentState: st(agent.StatusWaiting)},
			{ID: "b", Agent: "codex", AgentState: st(agent.StatusIdle), Unread: true},
			{ID: "c", Agent: "claude", AgentState: st(agent.StatusWorking)},
			{ID: "d", Agent: "claude", AgentState: st(agent.StatusIdle)},
		}},
		{ID: "w2", Terminals: []terminalInfo{
			// 普通终端和还没恢复的终端不算
			{ID: "e", AgentState: st(agent.StatusWaiting)},
			{ID: "f", Agent: "claude", AgentState: st(agent.StatusWaiting), Lost: true},
			// 派发了任务、停下来等结果的是"等待任务"，侧栏不写待处理，也不算
			{ID: "h", Agent: "claude", AgentState: st(agent.StatusIdle), Unread: true},
		}},
	}
	if got := a.badgeCount(); got != 2 {
		t.Fatalf("badgeCount = %d, want 2", got)
	}
	// 正看着的会话完成了不按未读算，等待照样算
	a.viewing = "b"
	if got := a.badgeCount(); got != 1 {
		t.Fatalf("badgeCount viewing b = %d, want 1", got)
	}
	a.viewing = "a"
	if got := a.badgeCount(); got != 2 {
		t.Fatalf("badgeCount viewing a = %d, want 2", got)
	}
	// 窗口失去焦点后 viewing 还没被下一帧清掉：updateBadge 先清，b 算进去
	a.viewing = "b"
	a.focused.Store(false)
	a.updateBadge()
	if a.viewing != "" {
		t.Fatalf("viewing = %q after blur, want empty", a.viewing)
	}
	if got := a.badgeCount(); got != 2 {
		t.Fatalf("badgeCount after blur = %d, want 2", got)
	}
}

func TestNoticeStatusRespectsSwitch(t *testing.T) {
	a := &app{lastStatus: map[string]string{}}
	a.state.NotifyOff = true
	w := workspace{ID: "w1", Label: "posse"}
	term := terminalInfo{ID: "t1", Agent: "claude", AgentState: &agentState{Status: agent.StatusWorking}}
	a.noticeStatus(w, term)
	term.AgentState = &agentState{Status: agent.StatusWaiting}
	// 关掉通知时直接返回，不碰窗口（a.win 为 nil，调到就会崩），但照样记下状态
	a.noticeStatus(w, term)
	if a.lastStatus["t1"] != agent.StatusWaiting {
		t.Fatalf("lastStatus = %q, want waiting", a.lastStatus["t1"])
	}
}
