package daemon

import (
	"time"

	"posse/internal/agent"
	"posse/internal/host"
)

// agent 经 OSC 7501 报的状态（Claude、Codex、Grok 都会报；Claude、Grok 要先收到探测的应答，term 里应答）。
// 报过之后这个 agent 的状态以它为准：hooks 只在事件发生时才有，Esc 中断、本轮出错这类没有事件的情况靠它补；
// Claude 收到应答后不再把状态写进标题，标题兜底也就用不上了。hooks 仍然提供会话 ID、答题卡和审批、子 agent、退出。

// Event 记成这两个：oscStopped 是主线程停下（idle、done、error），子 agent 还在跑时算 Parked
const (
	eventOSC        = "osc"
	eventOSCStopped = "osc-stop"
)

// oscAgent 是报告者对应的 agent 名，认不出的不改 agent 状态。
func oscAgent(app string) string {
	switch app {
	case "claude-code":
		return "claude"
	case "codex":
		return "codex"
	case "grok-build":
		return "grok"
	}
	return ""
}

// oscStatus 把 7501 的状态折成 posse 的。done、error 都是这一轮停下了，按空闲算，没看过的由 noteStatus 记成未读。
func oscStatus(state string) string {
	switch state {
	case "working":
		return agent.StatusWorking
	case "blocked":
		return agent.StatusWaiting
	}
	return agent.StatusIdle
}

func (d *Daemon) onProgramStatus(id string, ps host.ProgramStatus) {
	d.mu.Lock()
	s := d.terms[id]
	d.mu.Unlock()
	if s == nil {
		return
	}
	name := d.foreground(s)
	if name == "" || oscAgent(ps.App) != name {
		return
	}
	if ps.State == "clear" {
		s.setOSC("", "")
		return
	}
	status := oscStatus(ps.State)
	// 只跟状态的变化走：同一状态下的说明更新（Claude、Grok 工作中一直在报）不动 agent 状态。
	// hook 记下的等待也因此不会被这期间的工作中冲掉：Claude 的 PermissionRequest 挂着等手机时还在报工作中
	if prev := s.setOSC(name, status); prev == status {
		return
	}
	// 一轮的开始以 hook 或键入为准（见 oscTurnBegun）：Codex 启动时会先报两秒工作中，当成一轮的话刚开的终端就成了未读、还会推送
	if status == agent.StatusWorking && !s.oscTurnBegun() {
		return
	}
	at := time.Now()
	prev, next, ok := s.UpdateAgent(func(cur host.AgentState) (host.AgentState, bool) {
		if cur.Status == status {
			return cur, false
		}
		st := cur
		st.Status, st.At = status, at
		st.Event = eventOSC
		// 等待时的说明是在等什么，离开等待就不再成立；空闲时沿用的是 Stop 带来的最后一条回复
		if cur.Status == agent.StatusWaiting {
			st.Message = ""
		}
		switch status {
		case agent.StatusIdle:
			st.Event = eventOSCStopped
		case agent.StatusWorking:
			st.Message = ""
		case agent.StatusWaiting:
			// hook 已经记了等待时留着它的说明和卡片；7501 先到时用它的说明（Claude 提问时是问题和选项）
			st.Message = ps.Msg
		}
		return keepWorking(st, cur), true
	})
	if ok {
		d.afterAgent(s, prev.Status, next)
	}
}

// titleWaiting 是报过 7501 之后对标题仅剩的用处：Codex、Grok 等批准时标题含 Action Required，
// 万一它们的 7501 这时不报 blocked，等待也不丢。只往等待升；标题不再这样写时退回 7501 报的状态。
func (d *Daemon) titleWaiting(s *session, name, title string) {
	waiting := agent.TitleWaiting(name, title)
	s.mu.Lock()
	osc := s.oscStatus
	s.mu.Unlock()
	at := time.Now()
	prev, next, ok := s.UpdateAgent(func(cur host.AgentState) (host.AgentState, bool) {
		switch {
		case waiting && cur.Status != agent.StatusWaiting:
			// 沿用当前的：空闲时 Stop 带来的最后一条回复留着，手机的会话行不变空
			st := cur
			st.Status, st.Event, st.At = agent.StatusWaiting, "title", at
			return st, true
		case !waiting && cur.Status == agent.StatusWaiting && cur.Event == "title" && osc != agent.StatusWaiting:
			st := cur
			st.Status, st.Event, st.At = osc, eventOSC, at
			if osc == agent.StatusIdle {
				st.Event = eventOSCStopped
			}
			return keepWorking(st, cur), true
		}
		return cur, false
	})
	if ok {
		d.afterAgent(s, prev.Status, next)
	}
}

// setOSC 记下当前 agent 经 7501 报的状态，返回它上一次报的（之前没报过或换了 agent 时为空）。
// name 为空是不再以它为准（clear、换了 agent）。
func (s *session) setOSC(name, status string) (prev string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.osc == name && s.osc == s.agent {
		prev = s.oscStatus
	} else {
		// hook 已经开了一轮（比如交接后的新常驻进程在一轮中间才收到第一条报告）就接着算
		s.oscAt, s.oscTurn = time.Now(), s.Agent().Status == agent.StatusWorking
	}
	s.osc, s.oscStatus = name, status
	return prev
}

// oscTurnBegun 报告 7501 报的工作中能不能算数：没有 hooks 的 agent（Grok，posse 派的、没人键入也一样）只有 7501；有 hooks 的和 Codex
// 要等 hook 报过工作中，或者开始以 7501 为准之后有人往终端里输入过（Codex 启动时先报两秒工作中）。
func (s *session) oscTurnBegun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.oscTurnBegunLocked()
}

func (s *session) oscTurnBegunLocked() bool {
	// 看的是这家有没有 hooks，不是 session.hooked：posse 启动的 Grok 也记成 hooked，但永远等不到 hook；
	// Codex 不论带没带都这样，手动启动的要到第一个 hook 才记成带 hooks，没经同名脚本启动的根本没有
	needsTurn := s.agent == "codex" || s.hooked && agent.HasHooks(s.agent)
	return !needsTurn || s.oscTurn || s.LastInput().After(s.oscAt)
}

// oscActive 报告当前 agent 报过 7501。
func (s *session) oscActive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.osc != "" && s.osc == s.agent
}

// preferOSC 是 hook 事件该记的状态：报过 7501 时工作中、空闲以 7501 为准。等待留给 hook（带着答题卡和审批，
// 手机要用），退出也照 hook。
func (s *session) preferOSC(status string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.osc == "" || s.osc != s.agent {
		return status
	}
	if status == agent.StatusWorking {
		s.oscTurn = true
	}
	// 还没开始一轮时 7501 报的工作中不算数（见 onProgramStatus），hook 照常
	if !s.oscTurnBegunLocked() {
		return status
	}
	switch status {
	case agent.StatusWorking, agent.StatusIdle:
		return s.oscStatus
	}
	return status
}
