package agent

import (
	"encoding/json"
	"strings"
	"unicode/utf8"

	"posse/internal/term"
)

// 状态取值。exited 指 agent 本身退出，终端里的 shell 仍在。
const (
	StatusStarting = "starting"
	StatusWorking  = "working"
	StatusWaiting  = "waiting"
	StatusIdle     = "idle"
	StatusExited   = "exited"
)

// hookPayload 是 Claude Code、Codex、Cursor hooks 输入里 posse 用到的字段。
type hookPayload struct {
	SessionID        string          `json:"session_id"`
	TranscriptPath   string          `json:"transcript_path"`
	AgentID          string          `json:"agent_id"`
	ParentToolCall   string          `json:"parent_tool_call_id"` // Cursor 子 agent 的工具调用
	StopStatus       string          `json:"status"`              // Cursor 的 stop：completed、aborted、error
	IsInterrupt      bool            `json:"is_interrupt"`        // 工具失败是因为用户中断了这一轮
	Source           string          `json:"source"`
	Reason           string          `json:"reason"`
	Trigger          string          `json:"trigger"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	LastMessage      string          `json:"last_assistant_message"`
	Error            string          `json:"error"`
	// BackgroundTasks 是 Claude 的 Stop 带的、这一轮结束时还在跑的后台任务
	BackgroundTasks []backgroundTask `json:"background_tasks"`
}

type backgroundTask struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// agentTasks 是算作"子 agent 还在干活"的后台任务类型。后台 shell、monitor 不算：
// dev server 这类会一直挂着，算进去主线程就永远停不下来
var agentTasks = map[string]bool{"subagent": true, "teammate": true, "workflow": true}

// runningSubagents 取出还在跑的子 agent；没有时返回空切片（不是 nil），让状态换掉之前记的那份。
func runningSubagents(p hookPayload) []string {
	ids := []string{}
	for _, t := range p.BackgroundTasks {
		if agentTasks[t.Type] && t.Status == "running" && t.ID != "" {
			ids = append(ids, t.ID)
		}
	}
	return ids
}

// MapEvent 把 hooks 事件映射成 agent 状态；不影响状态的事件返回 false。
func MapEvent(agentName, event string, payload json.RawMessage) (term.AgentState, bool) {
	var p hookPayload
	if len(payload) > 0 {
		json.Unmarshal(payload, &p)
	}
	st := term.AgentState{Event: event, SessionID: p.SessionID, TranscriptPath: p.TranscriptPath}
	// 子 agent 的事件不代表主 agent 的状态
	if p.AgentID != "" || p.ParentToolCall != "" {
		return st, false
	}
	switch agentName {
	case "claude":
		st.Status = claudeStatus(event, p)
		switch event {
		case "Stop", "StopFailure":
			st.Subagents = runningSubagents(p)
		case "SessionStart":
			// 重新打开的会话里没有还在跑的后台任务
			st.Subagents = []string{}
		}
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
		case event == "PreToolUse" && (p.ToolName == claudeAskTool || p.ToolName == codexAskTool):
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

// SubagentStopped 认出 Claude 的 SubagentStop，返回结束的子 agent 的 ID。
// 不认 SubagentStart：后台子 agent 不发它（2.1.306 实测），还在跑的以主线程 Stop 的 background_tasks 为准。
func SubagentStopped(agentName, event string, payload json.RawMessage) (id string, ok bool) {
	if agentName != "claude" || event != "SubagentStop" {
		return "", false
	}
	var p hookPayload
	json.Unmarshal(payload, &p)
	return p.AgentID, p.AgentID != ""
}

// Parked 是主线程已经停下（回到输入框），只是还有子 agent 在跑：状态算工作中，但可以往终端里打提醒。
func Parked(st term.AgentState) bool {
	if st.Status != StatusWorking || len(st.Subagents) == 0 {
		return false
	}
	switch st.Event {
	case "Stop", "StopFailure", "title", "osc-stop":
		return true
	}
	return false
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

// 向用户提问的工具：PreToolUse 只订阅它们（hook 的 matcher），据此记等待用户、给手机答题卡。
const (
	claudeAskTool = "AskUserQuestion"
	codexAskTool  = "request_user_input"
)

func claudeStatus(event string, p hookPayload) string {
	switch event {
	case "UserPromptSubmit", "PostToolUse", "PostToolUseFailure":
		return StatusWorking
	// 只订阅了提问工具的 PreToolUse；其余工具记工作中是给交接前启动、还订阅全部工具的会话
	case "PreToolUse":
		if p.ToolName == claudeAskTool {
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
	// 同 Claude。默认模式下 request_user_input 调用失败、不发 PostToolUse，等待要到下一个工具做完或 Stop 才清掉
	case "PreToolUse":
		if p.ToolName == codexAskTool {
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

// BareTitle 是去掉 agent 状态前缀（✳、转圈符）的终端标题。
func BareTitle(title string) string {
	return strings.TrimSpace(strings.TrimLeft(title, "✳⠀⠁⠂⠃⠄⠅⠆⠇⠈⠉⠊⠋⠌⠍⠎⠏◐◓◑◒ "))
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

// HasHooks 报告这家 agent 启动时注入 hooks（grok 不注入，状态靠 OSC 7501 和标题）。
func HasHooks(agentName string) bool {
	switch agentName {
	case "claude", "codex", "cursor":
		return true
	}
	return false
}

// TitleWaiting 报告标题说 agent 在等批准：Codex、Grok 写 Action Required，Claude、Cursor 的标题不说。
func TitleWaiting(agentName, title string) bool {
	switch agentName {
	case "codex":
		return strings.Contains(title, "Action Required")
	case "grok":
		return strings.Contains(strings.ToLower(title), "action required")
	}
	return false
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

// DeliversOn 是这个 hook 能不能把任务提醒交给 agent，代替往终端里打字：工具做完时随结果进上下文，
// agent 忙着也能在两次工具调用之间读到；本轮结束时让它接着处理。调用方先经 MapEvent 排除了子 agent 的事件。
func DeliversOn(agentName, event string, payload json.RawMessage) bool {
	var p hookPayload
	json.Unmarshal(payload, &p)
	switch agentName + " " + event {
	case "claude PostToolUse", "claude Stop", "codex PostToolUse", "codex Stop", "cursor postToolUse":
		return true
	// 用户中断这一轮时工具也报失败，这时交的上下文随这一轮丢掉：留给空闲后往终端里输入
	case "claude PostToolUseFailure", "cursor postToolUseFailure":
		return !p.IsInterrupt
	// 用户主动停下（aborted）时 Cursor 不读 followup_message，交了也会丢
	case "cursor stop":
		return p.StopStatus == "" || p.StopStatus == "completed"
	}
	return false
}

// EndsTurn 是本轮结束的事件：在这里交了提醒，agent 不会停下，接着处理它。
func EndsTurn(event string) bool { return event == "Stop" || event == "stop" }

// DeliveryOutput 是 posse hook 写到 stdout、把提醒交给 agent 的回应。
func DeliveryOutput(agentName, event, text string) string {
	var v any
	switch {
	case agentName == "cursor" && event == "stop":
		v = map[string]string{"followup_message": text}
	case agentName == "cursor":
		v = map[string]string{"additional_context": text}
	case agentName == "codex" && event == "Stop":
		v = map[string]string{"decision": "block", "reason": text}
	// Claude 的 Stop 也用 additionalContext：decision: block 会显示成 "Stop hook error"
	default:
		v = map[string]any{"hookSpecificOutput": map[string]string{"hookEventName": event, "additionalContext": text}}
	}
	b, _ := json.Marshal(v)
	return string(b)
}
