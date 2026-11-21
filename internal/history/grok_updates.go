package history

import (
	"bytes"
	"encoding/json"
	"strings"
)

// grokUpdateParser 解析 updates.jsonl。这是 Grok 的完整流水：文本按 chunk 追加，
// 同一角色的连续 chunk 合成一条；chat_history.jsonl 只是压缩后的上下文快照。
type grokUpdateParser struct{}

// grokBuf 是还没交出去的一段正文。不写入 sessions.state：updates.jsonl 一有变化就整份重读，
// 读完时 flush 出来的半条会在下次和后续 chunk 重新合成。
type grokBuf struct {
	role string
	text strings.Builder
	at   int64
}

var grokUpdateContent = quoted("user_message_chunk", "agent_message_chunk", "tool_call", "tool_call_update")

func (grokUpdateParser) wants(raw []byte, _ *fileState) bool {
	return hasFieldValue(raw, grokUpdateKey, grokUpdateContent)
}

func (grokUpdateParser) meta(path string, st *fileState) error {
	return grokParser{}.meta(path, st)
}

func (p grokUpdateParser) line(raw []byte, st *fileState) []Message {
	var row struct {
		Timestamp int64 `json:"timestamp"`
		Params    struct {
			Update json.RawMessage `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &row) != nil {
		return nil
	}
	var update grokUpdate
	if json.Unmarshal(row.Params.Update, &update) != nil {
		return nil
	}
	at := grokMillis(row.Timestamp)
	switch update.SessionUpdate {
	case "user_message_chunk", "agent_message_chunk":
		role := "assistant"
		if update.SessionUpdate == "user_message_chunk" {
			role = "user"
		}
		var out []Message
		if st.grokBuf != nil && st.grokBuf.role != role {
			out = p.flush(st)
		}
		if st.grokBuf == nil {
			st.grokBuf = &grokBuf{role: role, at: at}
		}
		if st.grokBuf.at == 0 {
			st.grokBuf.at = at
		}
		st.grokBuf.text.WriteString(grokContentText(update.Content))
		return out
	case "tool_call":
		out := p.flush(st)
		name := update.Title
		if name == "" {
			name = update.Name
		}
		if name == "" {
			name = "tool"
		}
		if t := toolCall(name, unwrapJSON(update.RawInput)); t != "" {
			out = append(out, msg("tool", t, at))
		}
		return out
	case "tool_call_update":
		text, ok := update.result()
		if !ok {
			return nil
		}
		out := p.flush(st)
		if t := clip(text, toolClip); t != "" {
			out = append(out, msg("tool", t, at))
		}
		return out
	}
	return nil
}

func (p grokUpdateParser) flush(st *fileState) []Message {
	buf := st.grokBuf
	st.grokBuf = nil
	if buf == nil {
		return nil
	}
	text := buf.text.String()
	if buf.role == "user" {
		text = grokUserBody(text)
		text = userText(st, text)
	} else {
		text = strings.TrimSpace(text)
	}
	if text == "" {
		return nil
	}
	return []Message{msg(buf.role, text, buf.at)}
}

// grokUpdate 是 updates.jsonl 里 params.update 的一条。content 在正文 chunk 里是对象，
// 在 tool_call_update 里是 [{"type":"content","content":{"type":"text","text":…}}] 数组。
type grokUpdate struct {
	SessionUpdate string          `json:"sessionUpdate"`
	ToolCallID    string          `json:"toolCallId"`
	Title         string          `json:"title"`
	Name          string          `json:"name"`
	Status        string          `json:"status"`
	RawInput      json.RawMessage `json:"rawInput"`
	RawOutput     json.RawMessage `json:"rawOutput"`
	Content       json.RawMessage `json:"content"`
}

// result 取 tool_call_update 的工具结果。进行中的更新只带工具说明，completed / failed 才是结果；
// 没有 content 的结果退回 rawOutput。ok 为 false 表示这条不是结果。
func (u grokUpdate) result() (string, bool) {
	if u.Status != "completed" && u.Status != "failed" {
		return "", false
	}
	if t := grokContentText(u.Content); strings.TrimSpace(t) != "" {
		return t, true
	}
	raw := unwrapJSON(u.RawOutput)
	if len(raw) == 0 {
		return "", true
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, true
	}
	return string(raw), true
}

// grokContentText 取 content 里的文字，对象和数组两种写法都认。
func grokContentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	type text struct {
		Type    string          `json:"type"`
		Text    string          `json:"text"`
		Content json.RawMessage `json:"content"`
	}
	if raw[0] == '{' {
		var t text
		if json.Unmarshal(raw, &t) != nil {
			return ""
		}
		if t.Text == "" && len(t.Content) > 0 {
			return grokContentText(t.Content)
		}
		return t.Text
	}
	if raw[0] != '[' {
		return ""
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return ""
	}
	var parts []string
	for _, it := range items {
		if t := grokContentText(it); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n")
}

// grokUserBody 只留用户真正输入的那段。流水里的 chunk 可能是拆开的 <user_query>，
// 也可能是环境、规则这类注入；没有 query 标签、又带这些壳的整段丢掉。
func grokUserBody(s string) string {
	if parts := grokQuery.FindAllStringSubmatch(s, -1); len(parts) > 0 {
		var texts []string
		for _, m := range parts {
			texts = append(texts, m[1])
		}
		return strings.TrimSpace(strings.Join(texts, "\n"))
	}
	if strings.Contains(s, "<user_info>") || strings.Contains(s, "<rules>") || strings.Contains(s, "<agents_md>") {
		return ""
	}
	return strings.TrimSpace(s)
}

func grokMillis(ts int64) int64 {
	if ts > 0 && ts < 1e12 {
		return ts * 1000
	}
	return ts
}

// unwrapJSON 把再包了一层字符串的 JSON 解开。Grok 的 rawInput 两种都有。
func unwrapJSON(v json.RawMessage) json.RawMessage {
	if len(v) == 0 || string(v) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(v, &s) == nil && json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return v
}
