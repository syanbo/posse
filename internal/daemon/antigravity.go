package daemon

import (
	"os"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/procmem"
)

// syncAntigravity 给 agy 终端补会话 ID 和标题：agy 没有 hooks、不能预分配会话 ID，也不写终端标题。
// 会话按 agy 进程持有的 presence 锁认，后续原生改名也一起同步。
func (d *Daemon) syncAntigravity(s *session) {
	if s.agentName() != "antigravity" || !s.Running() {
		return
	}
	s.mu.Lock()
	pg := s.agentPg
	s.mu.Unlock()
	if pg <= 0 {
		pg = s.Foreground()
	}
	sid := d.antigravityPresenceSessionID(pg)
	if sid == "" {
		return
	}
	if s.Agent().SessionID != sid {
		s.SetTitle("", sid, false)
	}
	title := history.AntigravityTitle(d.agentDataHome(), sid)
	if title == "" {
		return
	}
	s.UpdateAgent(func(cur host.AgentState) (host.AgentState, bool) {
		if cur.SessionID != sid || cur.NativeTitle == title {
			return cur, false
		}
		// 首次看到摘要时可能已经有 Posse 自定义标题；之后原生标题变化才覆盖它。
		if cur.Title == "" || cur.NativeTitle != "" {
			cur.Title = title
		}
		cur.NativeTitle = title
		return cur, true
	})
}

func (d *Daemon) antigravityPresenceSessionID(pg int) string {
	if pg <= 0 {
		return ""
	}
	if found, _ := agent.OfProcess(pg); found != "antigravity" {
		return ""
	}
	return history.AntigravityOpenSession(d.agentDataHome(), procmem.OpenFiles(pg))
}

// agentDataHome 是各 agent 数据目录所在的家目录，测试里跟着 HistoryRoots。
func (d *Daemon) agentDataHome() string {
	if d.cfg.HistoryRoots != nil {
		return d.cfg.HistoryRoots.Home
	}
	home, _ := os.UserHomeDir()
	return home
}
