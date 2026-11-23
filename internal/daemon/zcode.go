package daemon

import (
	"encoding/json"
	"time"

	"posse/internal/agent"
	"posse/internal/host"
)

func (d *Daemon) zcodeDatabase() string {
	d.agents.mu.Lock()
	storage, detected := d.agents.zcode, d.agents.installed != nil
	d.agents.mu.Unlock()
	if !detected {
		storage = agent.CurrentZCodeStorage()
	}
	return storage.Database(d.agentDataHome())
}

func (d *Daemon) zcodeHook(s *session, p hookEventParams) {
	var h agent.ZCodeHook
	if json.Unmarshal(p.Payload, &h) != nil || h.Name != p.Event || !agent.IsZCodeSession(h.SessionID) || h.Timestamp.IsZero() {
		return
	}
	s.zcodeMu.Lock()
	defer s.zcodeMu.Unlock()
	// 任务或工具也能启动 ZCode 并继承终端 ID。只有前台进程自身拉起的 hook 才能更新状态。
	if p.ParentPID <= 0 || p.ParentPID != s.Foreground() {
		return
	}
	if found, _ := agent.OfProcess(p.ParentPID); found != "zcode" {
		return
	}
	if s.zcodePID != p.ParentPID {
		s.zcodePID, s.zcodeTurn, s.zcodeAt = p.ParentPID, "", time.Time{}
	}
	if h.Timestamp.Before(s.zcodeAt) {
		return
	}
	d.foreground(s)
	cur := s.Agent()
	st := host.AgentState{Event: p.Event, SessionID: h.SessionID, At: time.Now()}
	switch p.Event {
	case "SessionStart":
		if cur.SessionID == h.SessionID {
			return
		}
		s.zcodeTurn = ""
	case "UserPromptSubmit":
		if h.TurnID == "" || cur.SessionID != "" && cur.SessionID != h.SessionID {
			return
		}
		s.zcodeTurn = h.TurnID
		st.Status = agent.StatusWorking
	case "Stop":
		if h.TurnID == "" || cur.SessionID != h.SessionID || cur.Status != agent.StatusWorking ||
			s.zcodeTurn != "" && s.zcodeTurn != h.TurnID {
			return
		}
		st.Status = agent.StatusIdle
		st.Message = agent.Truncate(h.Response, 300)
	default:
		return
	}
	s.zcodeAt = h.Timestamp
	s.markHooked("zcode")
	d.setAgent(s, st)
	d.saveResume(s, false)
}
