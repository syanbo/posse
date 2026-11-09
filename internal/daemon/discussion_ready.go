package daemon

import (
	"encoding/json"

	"posse/internal/agent"
)

// Codex 的 OSC working 包含后台命令；讨论复用终端要看当前回答是否结束（上一轮已汇报由任务状态保证）。
// Claimed 阻止迟到的同轮 Stop 再次放行，Delivery 只为已领取的下一轮开放投递。
type discussionTurn struct {
	ID       string `json:"id,omitempty"`
	Stopped  bool   `json:"stopped,omitempty"`
	Claimed  string `json:"claimed,omitempty"`
	Delivery bool   `json:"delivery,omitempty"`
}

func (s *session) discussionHook(name, event string, raw json.RawMessage, continued bool) {
	if name != "codex" {
		return
	}
	var p struct {
		TurnID string `json:"turn_id"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch event {
	case "SessionStart", "SessionEnd":
		s.discussionTurn = discussionTurn{}
	case "UserPromptSubmit":
		s.discussionTurn = discussionTurn{ID: p.TurnID}
	case "Stop":
		if p.TurnID != "" && p.TurnID == s.discussionTurn.ID {
			s.discussionTurn.Stopped = !continued
		}
	case "Interrupt":
		// Esc 中断不发 Stop；中断的总是当前这一轮
		if p.TurnID == "" || p.TurnID == s.discussionTurn.ID {
			s.discussionTurn.Stopped = true
		}
	}
}

func (s *session) discussionReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.discussionTurn
	if s.agent == "codex" && t.ID != "" {
		return t.Stopped && t.Claimed != t.ID
	}
	return s.Agent().Status == agent.StatusIdle
}

// 领取前先消费可接收状态；数据库拒绝领取时才退回，期间开始了新回答则不再恢复旧状态。
func (s *session) reserveDiscussion() (func(bool), bool) {
	s.mu.Lock()
	t := s.discussionTurn
	if s.agent != "codex" || t.ID == "" {
		s.mu.Unlock()
		return func(bool) {}, s.Agent().Status == agent.StatusIdle
	}
	if !t.Stopped || t.Claimed == t.ID {
		s.mu.Unlock()
		return nil, false
	}
	s.discussionTurn.Claimed = t.ID
	s.mu.Unlock()
	return func(ok bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.discussionTurn.ID != t.ID {
			return
		}
		if ok {
			s.discussionTurn.Delivery = true
		} else {
			s.discussionTurn.Claimed = t.Claimed
		}
	}, true
}

func (s *session) discussionDelivery() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agent == "codex" && s.discussionTurn.Delivery && s.discussionTurn.Stopped
}
