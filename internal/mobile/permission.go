package mobile

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// chatWatch 是一条连接上正在看的对话。terminal 为空表示会话还没对上终端。
type chatWatch struct {
	gen      uint64
	reqID    string
	terminal string
}

type permWait struct {
	ch chan string
}

type permTarget struct {
	conn  *conn
	reqID string
}

func (c *conn) noteChat(key string, gen uint64, reqID string, a chatSub) {
	id := ""
	if t := c.sessionTerminal(a.Agent, a.SessionID); t != nil {
		id = t.ID
	}
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.streamGen[key] != gen {
		return
	}
	if c.chatWatch == nil {
		c.chatWatch = map[string]chatWatch{}
	}
	c.chatWatch[key] = chatWatch{gen: gen, reqID: reqID, terminal: id}
}

func (c *conn) clearChatWatch(key string, gen uint64) {
	c.smu.Lock()
	defer c.smu.Unlock()
	if cur, ok := c.chatWatch[key]; ok && cur.gen == gen {
		delete(c.chatWatch, key)
	}
}

func (s *Server) permissionTargets(terminalID string) []permTarget {
	s.mu.Lock()
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	var out []permTarget
	for _, c := range conns {
		if !c.hasCapability("hive.permission.v1") {
			continue
		}
		c.smu.Lock()
		for _, w := range c.chatWatch {
			if w.terminal == terminalID && w.reqID != "" {
				out = append(out, permTarget{conn: c, reqID: w.reqID})
			}
		}
		c.smu.Unlock()
	}
	return out
}

// HoldPermission 在有带 hive.permission.v1 的手机订阅着这个终端的对话时，
// 把请求推到那条对话流上，并等决定。lastInput 一变或 30 秒到了就返回空字符串。
func (s *Server) HoldPermission(ctx context.Context, terminalID string, payload json.RawMessage, lastInput func() time.Time) string {
	targets := s.permissionTargets(terminalID)
	if len(targets) == 0 {
		return ""
	}
	// 基线取在推送之前：推送过程中的键入也算挂起期间的输入，下一轮就能放开。
	base := time.Time{}
	if lastInput != nil {
		base = lastInput()
	}
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return ""
	}
	id := hex.EncodeToString(buf[:])
	tool, input := permissionBody(payload)
	frame := map[string]any{
		"type": "permission", "requestId": id, "terminal": terminalID, "tool": tool, "input": input,
	}
	delivered := false
	for _, t := range targets {
		if t.conn.stream(t.reqID, frame) == nil {
			delivered = true
		}
	}
	if !delivered {
		return ""
	}
	ch := make(chan string, 1)
	s.permMu.Lock()
	if s.perms == nil {
		s.perms = map[string]*permWait{}
	}
	s.perms[id] = &permWait{ch: ch}
	s.permMu.Unlock()
	defer func() {
		s.permMu.Lock()
		delete(s.perms, id)
		s.permMu.Unlock()
	}()
	if lastInput != nil && !lastInput().Equal(base) {
		return ""
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case behavior := <-ch:
			if behavior == "allow" || behavior == "deny" {
				return behavior
			}
			return ""
		case <-timer.C:
			return ""
		case <-ctx.Done():
			return ""
		case <-tick.C:
			if lastInput != nil && !lastInput().Equal(base) {
				return ""
			}
		}
	}
}

func (s *Server) decidePermission(requestID, behavior string) bool {
	if behavior != "allow" && behavior != "deny" {
		return false
	}
	s.permMu.Lock()
	w := s.perms[requestID]
	s.permMu.Unlock()
	if w == nil {
		return false
	}
	select {
	case w.ch <- behavior:
		return true
	default:
		return false
	}
}

func (c *conn) decidePermission(id string, p json.RawMessage) error {
	if !c.hasCapability("hive.permission.v1") {
		return errorf("method_not_found", "Unknown method: agent.permission.decide")
	}
	var body struct {
		RequestID string `json:"requestId"`
		Behavior  string `json:"behavior"`
	}
	if json.Unmarshal(p, &body) != nil || body.RequestID == "" {
		return c.fail(id, "bad_params", "permission decision")
	}
	if !c.srv.decidePermission(body.RequestID, body.Behavior) {
		return c.fail(id, "not_found", "permission request is gone")
	}
	return c.reply(id, map[string]any{"behavior": body.Behavior})
}

func permissionBody(payload json.RawMessage) (string, any) {
	var p struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if json.Unmarshal(payload, &p) != nil {
		return "", map[string]any{}
	}
	if len(p.ToolInput) == 0 {
		return p.ToolName, map[string]any{}
	}
	var v any
	if json.Unmarshal(p.ToolInput, &v) != nil {
		return p.ToolName, string(p.ToolInput)
	}
	return p.ToolName, v
}
