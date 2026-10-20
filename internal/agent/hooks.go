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

// hookPayload 是 Claude Code、Codex、Cursor hooks 输入里 hive 用到的字段。
type hookPayload struct {
	SessionID        string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	AgentID          string          `json:"agent_id"`
	Source           string          `json:"source"`
	Reason           string          `json:"reason"`
	Trigger          string          `json:"trigger"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	LastMessage      string          `json:"last_assistant_message"`
	Error            string          `json:"error"`
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
	case "cursor":
		st.Status = cursorStatus(event)
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
	if st.Status == StatusWaiting {
		st.ToolName = p.ToolName
		switch {
		case event == "PreToolUse" && (p.ToolName == "AskUserQuestion" || p.ToolName == "request_user_input"):
			// 答题卡要原样的 tool_input，手机按 questions 画选项
			st.Prompt = string(p.ToolInput)
		case event == "PermissionRequest" && p.ToolName != "":
			summary := approvalSummary(p.ToolInput)
			st.ToolInput = summary
			b, _ := json.Marshal(approvalPrompt{Approval: approvalBody{Tool: p.ToolName, Summary: summary}})
			st.Prompt = string(b)
		}
	}
	return st, true
}

type approvalBody struct {
	Tool    string `json:"tool"`
	Summary string `json:"summary"`
}

type approvalPrompt struct {
	Approval approvalBody `json:"approval"`
}

// approvalSummary 从权限请求的 tool_input 里取一行摘要：命令、路径、URL 或匹配式，最长 200 个字符。
func approvalSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "url", "pattern"} {
		v := m[k]
		if len(v) == 0 {
			continue
		}
		var s string
		if json.Unmarshal(v, &s) == nil && s != "" {
			return cutRunes(s, 200)
		}
		var list []string
		if json.Unmarshal(v, &list) == nil && len(list) > 0 {
			s = strings.Join(list, " ")
			if len(list) >= 3 && (list[1] == "-lc" || list[1] == "-c") {
				s = list[len(list)-1]
			}
			if s != "" {
				return cutRunes(s, 200)
			}
		}
	}
	return ""
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
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

// GrokName 从 grok 的终端标题里取会话名（去掉末尾的 " - grok"）。工作中、待处理时标题是状态
// （"⠹ - Waiting for response… - grok"），不是名字，返回 false。
func GrokName(title string) (string, bool) {
	t := strings.TrimSpace(title)
	if r, _ := utf8.DecodeRuneInString(t); r >= 0x2800 && r <= 0x28FF || strings.Contains(strings.ToLower(t), "action required") {
		return "", false
	}
	t = strings.TrimSpace(strings.TrimSuffix(t, "grok"))
	return strings.TrimSpace(strings.TrimSuffix(t, " -")), true
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

// TitleOnlyStatus 给没有注入 hooks 的 agent（用户在终端里自己启动的）判断状态：
// Claude 只能靠标题，开头是转动的圆形符号就是工作中。其余和 TitleStatus 一样。
func TitleOnlyStatus(agentName, title, current string) (string, bool) {
	if agentName == "claude" {
		st := ""
		switch r, _ := utf8.DecodeRuneInString(title); {
		case r == '✳':
			st = StatusIdle
		case r >= '◐' && r <= '◓', r >= 0x2800 && r <= 0x28FF:
			st = StatusWorking
		}
		if st != "" && st != current {
			return st, true
		}
		return "", false
	}
	return TitleStatus(agentName, title, current)
}

// Truncate 去掉首尾空白后截到 n 个字符，截断时补省略号。
func Truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
