package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"hive/internal/term"
)

// 状态取值。exited 指 agent 本身退出，终端里的 shell 仍在。
const (
	StatusStarting = "starting"
	StatusWorking  = "working"
	StatusWaiting  = "waiting"
	StatusIdle     = "idle"
	StatusExited   = "exited"
)

// hookPayload 是 Claude Code 和 Codex hooks 输入里 hive 用到的字段。
type hookPayload struct {
	SessionID        string `json:"session_id"`
	TranscriptPath   string `json:"transcript_path"`
	AgentID          string `json:"agent_id"`
	Source           string `json:"source"`
	Reason           string `json:"reason"`
	Trigger          string `json:"trigger"`
	ToolName         string `json:"tool_name"`
	NotificationType string `json:"notification_type"`
	Message          string `json:"message"`
	LastMessage      string `json:"last_assistant_message"`
	Error            string `json:"error"`
}

// MapEvent 把 hooks 事件映射成 agent 状态；不影响状态的事件返回 false。
func MapEvent(agentName, event string, payload json.RawMessage) (term.AgentState, bool) {
	var p hookPayload
	if len(payload) > 0 {
		json.Unmarshal(payload, &p)
	}
	st := term.AgentState{Event: event, SessionID: p.SessionID, TranscriptPath: p.TranscriptPath}
	// 子 agent 的事件不代表主 agent 的状态
	if p.AgentID != "" {
		return st, false
	}
	switch agentName {
	case "claude":
		st.Status = claudeStatus(event, p)
	case "codex":
		st.Status = codexStatus(event, p)
	}
	if st.Status == "" {
		return st, false
	}
	switch {
	case st.Status == StatusWaiting && p.Message != "":
		st.Message = p.Message
	case st.Status == StatusWaiting && p.ToolName != "":
		st.Message = p.ToolName
	case event == "Stop":
		st.Message = Truncate(p.LastMessage, 300)
	case event == "StopFailure":
		st.Message = p.Error
	}
	return st, true
}

func claudeStatus(event string, p hookPayload) string {
	switch event {
	case "UserPromptSubmit", "PostToolUse", "PostToolUseFailure":
		return StatusWorking
	case "PreToolUse":
		if p.ToolName == "AskUserQuestion" {
			return StatusWaiting
		}
		return StatusWorking
	case "PermissionRequest":
		return StatusWaiting
	case "Notification":
		switch p.NotificationType {
		case "permission_prompt":
			return StatusWaiting
		case "idle_prompt":
			return StatusIdle
		}
	case "Stop", "StopFailure":
		return StatusIdle
	case "SessionStart":
		// compact 只是压缩上下文，不是新会话的边界
		if p.Source != "compact" {
			return StatusIdle
		}
	case "PostCompact":
		if p.Trigger == "manual" {
			return StatusIdle
		}
	case "SessionEnd":
		switch p.Reason {
		case "prompt_input_exit", "logout", "other":
			return StatusExited
		}
	}
	return ""
}

func codexStatus(event string, p hookPayload) string {
	switch event {
	// Codex 的 SessionStart 在第一轮首次请求模型前触发，此时已经在工作
	case "SessionStart", "UserPromptSubmit", "PostToolUse":
		return StatusWorking
	case "PreToolUse":
		if p.ToolName == "request_user_input" {
			return StatusWaiting
		}
		return StatusWorking
	case "PermissionRequest":
		return StatusWaiting
	case "Stop", "Interrupt":
		return StatusIdle
	case "SessionEnd":
		return StatusExited
	}
	return ""
}

// TitleStatus 用终端标题补 hooks 的盲区：Claude 被 Esc 中断时不发 hook，
// Codex 本轮出错时不发 hook，grok 没有注入 hooks。只在标题给出明确信号时返回。
func TitleStatus(agentName, title, current string) (string, bool) {
	switch agentName {
	case "claude":
		// 空闲时标题以 ✳ 开头，工作中是转动的圆形符号
		if current == StatusWorking && strings.HasPrefix(title, "✳") {
			return StatusIdle, true
		}
	case "codex":
		if strings.Contains(title, "Action Required") && current != StatusWaiting {
			return StatusWaiting, true
		}
	case "grok":
		// 工作中形如 "⠹ - Waiting for response… - grok"，空闲是 "grok" 或 "会话名 - grok"
		st := StatusIdle
		if strings.Contains(strings.ToLower(title), "action required") {
			st = StatusWaiting
		} else if r, _ := utf8.DecodeRuneInString(title); r >= 0x2800 && r <= 0x28FF {
			st = StatusWorking
		}
		if st != current {
			return st, true
		}
	}
	return "", false
}

// Truncate 去掉首尾空白后截到 n 个字符，截断时补省略号。
func Truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
