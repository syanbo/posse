package history

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// 手机原生对话的块大小，对齐 Orca 的 native-chat-rpc-block-sanitize。
const (
	chatTextCap  = 64000
	chatBlockCap = 4000
	chatFanout   = 20
	chatDepth    = 5
	chatChunk    = 64 << 10
)

// ErrChatNotFound 表示会话文件还不存在或路径对不上。
var ErrChatNotFound = errors.New("chat transcript not found")

// ChatMessage 是一条给手机的结构化消息。
type ChatMessage struct {
	ID        string      `json:"id"`
	Role      string      `json:"role"`
	Blocks    []ChatBlock `json:"blocks"`
	Timestamp *int64      `json:"timestamp"`
	Source    string      `json:"source"`
	ParentID  string      `json:"parentId,omitempty"`
	Offset    int64       `json:"transcriptOffset"`
}

// ChatBlock 是消息里的一块。Input 保持 JSON，空字段不出现。
type ChatBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Name    string          `json:"name,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	CallID  string          `json:"callId,omitempty"`
	Output  string          `json:"output,omitempty"`
	IsError bool            `json:"isError,omitempty"`
	Path    string          `json:"path,omitempty"`
	URL     string          `json:"url,omitempty"`
}

// ChatPage 是一页消息。BeforeOffset 是这一页第一条所在行的文件偏移，HasMore 表示它前面还有。
type ChatPage struct {
	Messages     []ChatMessage
	BeforeOffset int64
	HasMore      bool
}

// ReadChatTail 读会话文件里 beforeOffset 之前的最后 limit 条消息。
// beforeOffset <= 0 表示从文件末尾开始。path 为空时按 agent 和 sessionID 找文件。
func ReadChatTail(agent, sessionID, path string, limit int, beforeOffset int64) (ChatPage, error) {
	path, err := chatPath(agent, sessionID, path)
	if err != nil {
		return ChatPage{}, err
	}
	if limit <= 0 {
		limit = 40
	}
	if agent == "grok" {
		return readGrokTail(path, limit, beforeOffset)
	}
	f, err := os.Open(path)
	if err != nil {
		return ChatPage{}, ErrChatNotFound
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ChatPage{}, err
	}
	end := beforeOffset
	if end <= 0 || end > fi.Size() {
		end = fi.Size()
	}
	paged := agent == "codex" && codexPaginated(path)
	msgs, more, err := readBackward(f, end, limit, func(line []byte, off int64) *ChatMessage {
		return decodeChatLine(agent, line, off, paged, nil)
	})
	if err != nil {
		return ChatPage{}, err
	}
	page := ChatPage{Messages: msgs, HasMore: more}
	if len(msgs) > 0 {
		page.BeforeOffset = msgs[0].Offset
	}
	return page, nil
}

// ReadChatNew 读 path 里从 from 起新增的完整行。next 是下一次该从哪里继续；
// 文件尾没有换行的半行留在 next 之前，等它写完。
func ReadChatNew(agent, path string, from int64) (msgs []ChatMessage, next int64, err error) {
	if agent == "grok" {
		return readGrokNew(path, from)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, from, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, from, err
	}
	if from < 0 || from > fi.Size() {
		from = 0
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return nil, from, err
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, from, err
	}
	paged := agent == "codex" && codexPaginated(path)
	complete := b
	next = from + int64(len(b))
	if len(b) > 0 && b[len(b)-1] != '\n' {
		if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
			complete = b[:i+1]
			next = from + int64(i+1)
		} else {
			return nil, from, nil
		}
	}
	off := from
	for len(complete) > 0 {
		i := bytes.IndexByte(complete, '\n')
		line := complete
		if i >= 0 {
			line = complete[:i]
			complete = complete[i+1:]
		} else {
			complete = nil
		}
		if m := decodeChatLine(agent, bytes.TrimRight(line, "\r"), off, paged, nil); m != nil {
			msgs = append(msgs, *m)
		}
		off += int64(len(line))
		if i >= 0 {
			off++
		}
	}
	if msgs == nil {
		msgs = []ChatMessage{}
	}
	return msgs, next, nil
}

func chatPath(agent, sessionID, path string) (string, error) {
	if path == "" {
		path = TranscriptPath(agent, sessionID, "")
	}
	if path == "" {
		return "", ErrChatNotFound
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return "", ErrChatNotFound
	}
	return path, nil
}

func codexPaginated(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 32<<10)
	n, _ := f.Read(buf)
	return bytes.Contains(buf[:n], []byte(`"paginated"`))
}

// readBackward 从 end（不含）往前解码，凑够 limit+1 条就停。返回按时间正序的最后 limit 条。
func readBackward(f *os.File, end int64, limit int, decode func([]byte, int64) *ChatMessage) ([]ChatMessage, bool, error) {
	want := limit + 1
	var newest []ChatMessage
	var pending []byte
	pos := end
	for len(newest) < want && (pos > 0 || len(pending) > 0) {
		var data []byte
		dataOff := pos
		if pos > 0 {
			n := int64(chatChunk)
			if pos < n {
				n = pos
			}
			buf := make([]byte, n)
			if _, err := f.ReadAt(buf, pos-n); err != nil {
				return nil, false, err
			}
			pos -= n
			dataOff = pos
			data = make([]byte, 0, len(buf)+len(pending))
			data = append(data, buf...)
			data = append(data, pending...)
		} else {
			data = pending
			pending = nil
		}
		cut := 0
		if pos > 0 {
			nl := bytes.IndexByte(data, '\n')
			if nl < 0 {
				pending = append([]byte(nil), data...)
				continue
			}
			pending = append([]byte(nil), data[:nl]...)
			cut = nl + 1
		}
		type piece struct {
			off int64
			raw []byte
		}
		var lines []piece
		rest := data[cut:]
		off := dataOff + int64(cut)
		for len(rest) > 0 {
			i := bytes.IndexByte(rest, '\n')
			raw := rest
			adv := len(rest)
			if i >= 0 {
				raw = rest[:i]
				adv = i + 1
				rest = rest[i+1:]
			} else {
				rest = nil
			}
			lines = append(lines, piece{off, bytes.TrimRight(raw, "\r")})
			off += int64(adv)
		}
		for i := len(lines) - 1; i >= 0 && len(newest) < want; i-- {
			if m := decode(lines[i].raw, lines[i].off); m != nil {
				newest = append(newest, *m)
			}
		}
		if pos == 0 {
			break
		}
	}
	more := len(newest) > limit
	if more {
		newest = newest[:limit]
	}
	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	if newest == nil {
		newest = []ChatMessage{}
	}
	return newest, more, nil
}

func readGrokTail(path string, limit int, beforeOffset int64) (ChatPage, error) {
	msgs, _, err := readGrokAll(path)
	if err != nil {
		return ChatPage{}, err
	}
	return window(msgs, limit, beforeOffset), nil
}

func readGrokNew(path string, from int64) ([]ChatMessage, int64, error) {
	msgs, consumed, err := readGrokAll(path)
	if err != nil {
		return nil, from, err
	}
	var out []ChatMessage
	for _, m := range msgs {
		if m.Offset >= from {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []ChatMessage{}
	}
	return out, consumed, nil
}

func readGrokAll(path string) ([]ChatMessage, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, ErrChatNotFound
	}
	defer f.Close()
	st := &fileState{}
	grokParser{}.prepare(path, st)
	var msgs []ChatMessage
	var prev int64
	var last int64
	consumed, _, err := scanLines(f, func(line []byte, n int64) {
		off := prev
		prev = n
		raw := bytes.TrimRight(line, "\r\n")
		if m := decodeGrokLine(raw, off, st.grok, &last); m != nil {
			msgs = append(msgs, *m)
		}
	})
	if err != nil {
		return nil, 0, err
	}
	if msgs == nil {
		msgs = []ChatMessage{}
	}
	return msgs, consumed, nil
}

func window(msgs []ChatMessage, limit int, beforeOffset int64) ChatPage {
	if beforeOffset > 0 {
		cut := msgs[:0]
		for _, m := range msgs {
			if m.Offset < beforeOffset {
				cut = append(cut, m)
			}
		}
		msgs = cut
	}
	page := ChatPage{HasMore: len(msgs) > limit}
	if page.HasMore {
		msgs = msgs[len(msgs)-limit:]
	}
	if msgs == nil {
		msgs = []ChatMessage{}
	}
	page.Messages = msgs
	if len(msgs) > 0 {
		page.BeforeOffset = msgs[0].Offset
	}
	return page
}

func decodeChatLine(agent string, raw []byte, off int64, paged bool, gt *grokTimes) *ChatMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' {
		return nil
	}
	var m *ChatMessage
	switch agent {
	case "claude":
		m = decodeClaudeLine(raw, off)
	case "codex":
		m = decodeCodexLine(raw, off, paged)
	case "grok":
		var last int64
		m = decodeGrokLine(raw, off, gt, &last)
	default:
		return nil
	}
	if m == nil {
		return nil
	}
	for i := range m.Blocks {
		m.Blocks[i] = sanitizeBlock(m.Blocks[i])
	}
	return m
}

func stamp(ms int64) *int64 {
	if ms == 0 {
		return nil
	}
	return &ms
}

func chatID(id, fallback string, off int64) string {
	if id != "" {
		return id
	}
	if fallback != "" {
		return fallback
	}
	return "line-" + strconv.FormatInt(off, 10)
}

type claudeLine struct {
	Type             string `json:"type"`
	UUID             string `json:"uuid"`
	ParentUUID       string `json:"parentUuid"`
	IsMeta           bool   `json:"isMeta"`
	IsSynthetic      bool   `json:"isSynthetic"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Timestamp        string `json:"timestamp"`
	Message          struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	Attachment claudeAttachment `json:"attachment"`
}

type claudePiece struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	Name      string          `json:"name"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Path      string          `json:"path"`
	URL       string          `json:"url"`
	Source    struct {
		Path string `json:"path"`
		URL  string `json:"url"`
		File string `json:"file"`
	} `json:"source"`
}

func decodeClaudeLine(raw []byte, off int64) *ChatMessage {
	var r claudeLine
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	if r.Type == "attachment" {
		if prompt := r.Attachment.humanPrompt(); prompt != nil {
			r.Type, r.Message.Content = "user", prompt
		}
	}
	if r.Type != "user" && r.Type != "assistant" {
		return nil
	}
	pieces, ok := claudePieces(r.Message.Content)
	if !ok {
		return nil
	}
	filtered := r.Type == "user" && (r.IsMeta || r.IsSynthetic || r.IsCompactSummary)
	var blocks []ChatBlock
	thinking := false
	toolish := false
	for _, p := range pieces {
		switch p.Type {
		case "text":
			if filtered {
				if path := imagePathText(p.Text); path != "" {
					blocks = append(blocks, ChatBlock{Type: "image-ref", Path: path})
				}
				continue
			}
			if strings.TrimSpace(p.Text) == "" {
				continue
			}
			blocks = append(blocks, ChatBlock{Type: "text", Text: p.Text})
		case "thinking":
			thinking = true
			if filtered {
				continue
			}
			text := p.Thinking
			if text == "" {
				text = p.Text
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			blocks = append(blocks, ChatBlock{Type: "text", Text: text})
		case "tool_use":
			if filtered {
				continue
			}
			toolish = true
			blocks = append(blocks, ChatBlock{Type: "tool-call", Name: p.Name, Input: p.Input, CallID: p.ID})
		case "tool_result":
			blocks = append(blocks, ChatBlock{Type: "tool-result", Output: textOf(p.Content), IsError: p.IsError, CallID: p.ToolUseID})
		case "image":
			b := ChatBlock{Type: "image-ref", Path: firstNonEmpty(p.Path, p.Source.Path, p.Source.File), URL: firstNonEmpty(p.URL, p.Source.URL)}
			if b.Path == "" && b.URL == "" {
				continue
			}
			blocks = append(blocks, b)
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	role := r.Type
	if r.Type == "user" && allType(blocks, "tool-result") {
		role = "tool"
	}
	if thinking && !toolish {
		role = "reasoning"
	}
	if interrupted(blocks) {
		role = "system"
		blocks = []ChatBlock{{Type: "text", Text: "Request interrupted by user"}}
	}
	return &ChatMessage{
		ID: chatID(r.UUID, r.Message.ID, off), Role: role, Blocks: blocks,
		Timestamp: stamp(parseTime(r.Timestamp)), Source: "transcript", ParentID: r.ParentUUID, Offset: off,
	}
}

func claudePieces(raw json.RawMessage) ([]claudePiece, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, false
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, false
		}
		return []claudePiece{{Type: "text", Text: s}}, true
	case '[':
		var ps []claudePiece
		if json.Unmarshal(raw, &ps) != nil {
			return nil, false
		}
		return ps, true
	default:
		return nil, false
	}
}

func imagePathText(s string) string {
	s = strings.TrimSpace(s)
	low := strings.ToLower(s)
	if !strings.HasPrefix(s, "/") || strings.ContainsAny(s, " \n\t") {
		return ""
	}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp"} {
		if strings.HasSuffix(low, ext) {
			return s
		}
	}
	return ""
}

func interrupted(blocks []ChatBlock) bool {
	for _, b := range blocks {
		if b.Type == "text" && strings.Contains(b.Text, "[Request interrupted by user]") {
			return true
		}
	}
	return false
}

func allType(blocks []ChatBlock, typ string) bool {
	for _, b := range blocks {
		if b.Type != typ {
			return false
		}
	}
	return len(blocks) > 0
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// codexPayload 是 response_item / event_msg 里原生对话要用、而索引记录没有单独列出的字段。
type codexPayload struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	CallID    string          `json:"call_id"`
	Role      string          `json:"role"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     json.RawMessage `json:"input"`
	Output    json.RawMessage `json:"output"`
	Action    json.RawMessage `json:"action"`
	Message   string          `json:"message"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Item struct {
		ID               string   `json:"id"`
		Type             string   `json:"type"`
		Text             string   `json:"text"`
		Command          []string `json:"command"`
		AggregatedOutput string   `json:"aggregated_output"`
		Content          []struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"item"`
}

func decodeCodexLine(raw []byte, off int64, paged bool) *ChatMessage {
	var r struct {
		Timestamp string       `json:"timestamp"`
		Type      string       `json:"type"`
		Payload   codexPayload `json:"payload"`
	}
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	at := parseTime(r.Timestamp)
	switch r.Type {
	case "response_item":
		if paged {
			return nil
		}
		return codexFromPayload(&r.Payload, off, at, false)
	case "event_msg":
		return codexFromPayload(&r.Payload, off, at, paged)
	default:
		return nil
	}
}

func codexFromPayload(p *codexPayload, off, at int64, paged bool) *ChatMessage {
	id := firstNonEmpty(p.ID, p.CallID, p.Item.ID)
	switch p.Type {
	case "user_message":
		return codexText(id, "user", codexTexts(p, true), off, at)
	case "agent_message":
		return codexText(id, "assistant", codexTexts(p, false), off, at)
	case "turn_aborted":
		return &ChatMessage{ID: chatID(id, "", off), Role: "system", Blocks: []ChatBlock{{Type: "text", Text: "Turn aborted"}}, Timestamp: stamp(at), Source: "transcript", Offset: off}
	case "item_completed":
		if !paged {
			return nil
		}
		return codexItemLine(p, off, at)
	case "message":
		role := p.Role
		if role == "" {
			role = "assistant"
		}
		return codexText(id, role, codexTexts(p, role == "user"), off, at)
	case "reasoning":
		var texts []string
		for _, s := range p.Summary {
			if t := strings.TrimSpace(s.Text); t != "" {
				texts = append(texts, t)
			}
		}
		texts = append(texts, codexTexts(p, false)...)
		return codexText(id, "reasoning", texts, off, at)
	case "function_call", "local_shell_call", "custom_tool_call":
		in := codexCallInput(p)
		if p.Name == "" && p.Type == "local_shell_call" {
			p.Name = "shell"
		}
		return &ChatMessage{
			ID: chatID(id, "", off), Role: "assistant", Timestamp: stamp(at), Source: "transcript", Offset: off,
			Blocks: []ChatBlock{{Type: "tool-call", Name: p.Name, CallID: firstNonEmpty(p.CallID, p.ID), Input: in}},
		}
	case "function_call_output", "custom_tool_call_output":
		return &ChatMessage{
			ID: chatID(id, "", off), Role: "tool", Timestamp: stamp(at), Source: "transcript", Offset: off,
			Blocks: []ChatBlock{{Type: "tool-result", CallID: firstNonEmpty(p.CallID, p.ID), Output: codexOutput(p.Output)}},
		}
	default:
		return nil
	}
}

func codexTexts(p *codexPayload, user bool) []string {
	var out []string
	if t := strings.TrimSpace(p.Message); t != "" && !(user && codexInjectedText(t)) {
		out = append(out, t)
	}
	for _, c := range p.Content {
		t := strings.TrimSpace(c.Text)
		if t == "" || (user && codexInjectedText(t)) {
			continue
		}
		out = append(out, t)
	}
	return out
}

func codexInjectedText(t string) bool {
	return hasAnyPrefix(strings.TrimSpace(t), []string{
		"<skill>", "<environment_context>", "# AGENTS.md instructions", "<user_instructions>", "<permissions instructions>",
	})
}

func codexText(id, role string, texts []string, off, at int64) *ChatMessage {
	if len(texts) == 0 {
		return nil
	}
	if role != "user" && role != "assistant" && role != "reasoning" && role != "system" && role != "tool" {
		role = "assistant"
	}
	blocks := make([]ChatBlock, 0, len(texts))
	for _, t := range texts {
		blocks = append(blocks, ChatBlock{Type: "text", Text: t})
	}
	return &ChatMessage{ID: chatID(id, "", off), Role: role, Blocks: blocks, Timestamp: stamp(at), Source: "transcript", Offset: off}
}

func codexCallInput(p *codexPayload) json.RawMessage {
	if p.Arguments != "" {
		if json.Valid([]byte(p.Arguments)) {
			return json.RawMessage(p.Arguments)
		}
		b, _ := json.Marshal(p.Arguments)
		return b
	}
	in := bytes.TrimSpace(p.Input)
	if len(in) > 0 && string(in) != "null" {
		return json.RawMessage(in)
	}
	act := bytes.TrimSpace(p.Action)
	if len(act) > 0 && string(act) != "null" {
		return json.RawMessage(act)
	}
	return nil
}

func codexItemLine(p *codexPayload, off, at int64) *ChatMessage {
	it := p.Item
	id := firstNonEmpty(it.ID, p.ID)
	var texts []string
	for _, c := range it.Content {
		if t := strings.TrimSpace(c.Text); t != "" {
			texts = append(texts, t)
		}
	}
	if t := strings.TrimSpace(it.Text); t != "" {
		texts = append(texts, t)
	}
	switch it.Type {
	case "UserMessage":
		var keep []string
		for _, t := range texts {
			if !codexInjectedText(t) {
				keep = append(keep, t)
			}
		}
		return codexText(id, "user", keep, off, at)
	case "AgentMessage", "Plan":
		return codexText(id, "assistant", texts, off, at)
	case "Reasoning":
		return codexText(id, "reasoning", texts, off, at)
	case "CommandExecution":
		b, _ := json.Marshal(map[string]any{"command": it.Command})
		blocks := []ChatBlock{{Type: "tool-call", Name: "shell", CallID: id, Input: b}}
		if it.AggregatedOutput != "" {
			blocks = append(blocks, ChatBlock{Type: "tool-result", CallID: id, Output: it.AggregatedOutput})
		}
		return &ChatMessage{ID: chatID(id, "", off), Role: "assistant", Blocks: blocks, Timestamp: stamp(at), Source: "transcript", Offset: off}
	default:
		if len(texts) == 0 {
			return nil
		}
		return codexText(id, "assistant", texts, off, at)
	}
}

func decodeGrokLine(raw []byte, off int64, gt *grokTimes, last *int64) *ChatMessage {
	var r grokRecord
	if json.Unmarshal(raw, &r) != nil || r.Type == "" {
		return nil
	}
	at := int64(0)
	if gt != nil {
		at = gt.at(r)
	}
	if at == 0 && last != nil {
		at = *last
	} else if at != 0 && last != nil {
		*last = at
	}
	var blocks []ChatBlock
	role := ""
	switch r.Type {
	case "user":
		text := grokUserText(textOf(r.Content))
		if text == "" {
			return nil
		}
		role = "user"
		blocks = []ChatBlock{{Type: "text", Text: text}}
	case "assistant":
		role = "assistant"
		if t := strings.TrimSpace(textOf(r.Content)); t != "" {
			blocks = append(blocks, ChatBlock{Type: "text", Text: t})
		}
		for _, c := range r.ToolCalls {
			blocks = append(blocks, ChatBlock{Type: "tool-call", Name: c.Name, CallID: c.ID, Input: grokInput(c.Arguments)})
		}
	case "tool_result":
		out := textOf(r.Content)
		if strings.TrimSpace(out) == "" {
			return nil
		}
		role = "tool"
		blocks = []ChatBlock{{Type: "tool-result", Output: out, CallID: r.ToolCallID}}
	case "backend_tool_call":
		if r.Kind.ToolType == "" && r.Kind.Action.Query == "" {
			return nil
		}
		role = "assistant"
		blocks = []ChatBlock{{Type: "tool-call", Name: r.Kind.ToolType, Input: grokInput(r.Kind.Action.Query)}}
	default:
		return nil
	}
	if len(blocks) == 0 {
		return nil
	}
	for i := range blocks {
		blocks[i] = sanitizeBlock(blocks[i])
	}
	return &ChatMessage{ID: "line-" + strconv.FormatInt(off, 10), Role: role, Blocks: blocks, Timestamp: stamp(at), Source: "transcript", Offset: off}
}

func grokUserText(s string) string {
	var parts []string
	for _, m := range grokQuery.FindAllStringSubmatch(s, -1) {
		if t := strings.TrimSpace(m[1]); t != "" {
			parts = append(parts, t)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	t := strings.TrimSpace(s)
	if t == "" || strings.Contains(t, "<") {
		return ""
	}
	return t
}

func grokInput(args string) json.RawMessage {
	trim := strings.TrimSpace(args)
	if trim != "" && json.Valid([]byte(trim)) && (trim[0] == '{' || trim[0] == '[') {
		return json.RawMessage(trim)
	}
	b, _ := json.Marshal(map[string]string{"text": args})
	return b
}

func sanitizeBlock(b ChatBlock) ChatBlock {
	switch b.Type {
	case "text":
		b.Text = cutRunes(b.Text, chatTextCap)
	case "tool-result":
		if utf8.RuneCountInString(b.Output) > chatBlockCap {
			b.Output = cutRunes(b.Output, chatBlockCap) + "\n… (truncated)"
		}
	case "tool-call":
		b.Input = sanitizeInput(b.Input)
	}
	return b
}

func cutRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func sanitizeInput(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		b, _ := json.Marshal(cutRunes(string(raw), chatBlockCap))
		return b
	}
	budget := chatBlockCap
	if inputWithin(v, 0, &budget) {
		return raw
	}
	budget = chatBlockCap
	b, err := json.Marshal(shrinkValue(v, 0, &budget))
	if err != nil {
		return raw
	}
	return b
}

func inputWithin(v any, depth int, budget *int) bool {
	if depth > chatDepth {
		return false
	}
	switch t := v.(type) {
	case map[string]any:
		if len(t) > chatFanout {
			return false
		}
		for _, e := range t {
			if !inputWithin(e, depth+1, budget) {
				return false
			}
		}
	case []any:
		if len(t) > chatFanout {
			return false
		}
		for _, e := range t {
			if !inputWithin(e, depth+1, budget) {
				return false
			}
		}
	case string:
		n := utf8.RuneCountInString(t)
		if n > *budget {
			return false
		}
		*budget -= n
	}
	return true
}

func shrinkValue(v any, depth int, budget *int) any {
	if depth > chatDepth {
		return "…"
	}
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > chatFanout {
			keys = keys[:chatFanout]
		}
		m := map[string]any{}
		for _, k := range keys {
			m[k] = shrinkValue(t[k], depth+1, budget)
		}
		return m
	case []any:
		if len(t) > chatFanout {
			t = t[:chatFanout]
		}
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = shrinkValue(e, depth+1, budget)
		}
		return out
	case string:
		n := utf8.RuneCountInString(t)
		if n > *budget {
			t = cutRunes(t, max(*budget, 0))
			n = utf8.RuneCountInString(t)
		}
		*budget -= n
		return t
	default:
		return v
	}
}
