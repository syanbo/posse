package mobile

import "posse/internal/agent"

// agentStatus 复用 agent.ParseStatusLine 从屏幕底部提取 agent 实时状态行。
func agentStatus(screen []string) string {
	return agent.ParseStatusLine(screen)
}
