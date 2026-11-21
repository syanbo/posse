package daemon

import (
	"context"
	"encoding/json"
	"strings"

	"posse/internal/agent"
)

// wantSessionBrief 是新开的 Claude、Codex 才带上这段目录里的近期会话。
// 恢复、压缩、清空都不带：Claude 看 source，Codex 的 SessionStart 不带 source，再看启动命令是不是恢复。
func wantSessionBrief(agentName, event string, payload json.RawMessage, command []string) bool {
	if event != "SessionStart" || (agentName != "claude" && agentName != "codex") {
		return false
	}
	var src struct {
		Source string `json:"source"`
	}
	json.Unmarshal(payload, &src)
	switch src.Source {
	case "", "startup":
	default:
		return false
	}
	return !commandResumes(command)
}

func commandResumes(cmd []string) bool {
	text := strings.Join(cmd, " ")
	return strings.Contains(text, "--resume") || strings.Contains(text, " resume '")
}

// sessionBrief 从已有索引读这个终端所在目录的最近几条会话。不在这里刷新索引：hook 只有两秒。
func (d *Daemon) sessionBrief(ctx context.Context, s *session, p hookEventParams) string {
	if s == nil || !wantSessionBrief(p.Agent, p.Event, p.Payload, s.Command) || s.Dir == "" {
		return ""
	}
	idx, err := d.historyIndex()
	if err != nil {
		return ""
	}
	text, err := idx.Brief(ctx, s.Dir, agent.SessionOf(p.Payload))
	if err != nil {
		return ""
	}
	return text
}
