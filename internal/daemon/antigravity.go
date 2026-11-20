package daemon

import (
	"os"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/procmem"
)

// syncAntigravity 给 agy 终端补会话 ID 和标题：agy 没有 hooks、不能预分配会话 ID，也不写终端标题。
// 会话按 agy 进程持有的 presence 锁认（/new、切换会话后跟着换），标题读 agy 自己起的；
// 已经有标题（posse session title 起的）时不覆盖。
func (d *Daemon) syncAntigravity(s *session) {
	if s.agentName() != "antigravity" || !s.Running() {
		return
	}
	s.mu.Lock()
	pg := s.agentPg
	s.mu.Unlock()
	if pg <= 0 {
		if pg = s.Foreground(); pg <= 0 {
			return
		}
		if found, _ := agent.OfProcess(pg); found != "antigravity" {
			return
		}
	}
	home := d.agentDataHome()
	sid := s.Agent().SessionID
	if open := history.AntigravityOpenSession(home, procmem.OpenFiles(pg)); open != "" && open != sid {
		s.SetTitle("", open, false)
		sid = open
	}
	if sid == "" || s.Agent().Title != "" {
		return
	}
	title := history.AntigravityTitle(home, sid)
	if title == "" {
		return
	}
	s.UpdateAgent(func(cur host.AgentState) (host.AgentState, bool) {
		if cur.SessionID != sid || cur.Title != "" {
			return cur, false
		}
		cur.Title = title
		return cur, true
	})
}

// agentDataHome 是各 agent 数据目录所在的家目录，测试里跟着 HistoryRoots。
func (d *Daemon) agentDataHome() string {
	if d.cfg.HistoryRoots != nil {
		return d.cfg.HistoryRoots.Home
	}
	home, _ := os.UserHomeDir()
	return home
}
