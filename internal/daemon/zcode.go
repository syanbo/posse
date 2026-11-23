package daemon

import (
	"context"
	"encoding/json"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/rpc"
)

func (d *Daemon) zcodeSessionTools(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	return d.readZCodeTools(ctx, p.SessionID)
}

func (d *Daemon) zcodeDatabase() string {
	d.agents.mu.Lock()
	storage, detected := d.agents.zcode, d.agents.installed != nil
	d.agents.mu.Unlock()
	if !detected {
		storage = agent.CurrentZCodeStorage()
	}
	return storage.Database(d.agentDataHome())
}

func (d *Daemon) readZCodeTools(ctx context.Context, sessionID string) (history.SessionUsage, error) {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	return history.ReadZCodeUsage(ctx, d.zcodeDatabase(), sessionID)
}

// zcodeRuntime 在 zcodeMu 内调用。进程同时持有多个顶层会话时只返回 PID，不能用缓存代替当前归属。
func (d *Daemon) zcodeRuntime(ctx context.Context, s *session, path string) (pid int, sessionID string) {
	pid = s.Foreground()
	if name, _ := agent.OfProcess(pid); name != "zcode" {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	sid, title, ambiguous := history.ZCodeRuntimeSession(ctx, path, pid)
	if s.Foreground() != pid || s.agentName() != "zcode" {
		return 0, ""
	}
	if ambiguous {
		return pid, ""
	}
	if sid == "" {
		return 0, ""
	}
	if s.Agent().SessionID != sid {
		s.zcodeTurn = ""
		s.ResetAgent(host.AgentState{SessionID: sid, Title: title, Event: "process", At: time.Now()})
		s.markRead()
	}
	return pid, sid
}

func (d *Daemon) syncZCode(ctx context.Context, s *session) {
	if s.agentName() != "zcode" || !s.Running() || !s.zcodeMu.TryLock() {
		return
	}
	defer s.zcodeMu.Unlock()
	d.zcodeRuntime(ctx, s, d.zcodeDatabase())
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
