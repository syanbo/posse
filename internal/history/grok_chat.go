package history

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ChatWatchPath 是手机订阅该 stat 的会话文件。Grok 有非空 updates.jsonl 时盯着它，
// 没有或是空文件才用 chat_history.jsonl；别的 agent 跟 TranscriptPath 一样。
func ChatWatchPath(agent, sessionID, reported string) string {
	if agent == "grok" {
		return grokWatch(sessionID, reported)
	}
	if reported != "" {
		return reported
	}
	return TranscriptPath(agent, sessionID, "")
}

// grokChatFile 有非空 updates.jsonl 时用它当对话正文。传入的路径可以是流水，也可以是同目录的快照。
func grokChatFile(path string) string {
	if path == "" {
		return ""
	}
	upd := path
	if filepath.Base(path) != "updates.jsonl" {
		upd = filepath.Join(filepath.Dir(path), "updates.jsonl")
	}
	if fi, err := os.Stat(upd); err == nil && !fi.IsDir() && fi.Size() > 0 {
		return upd
	}
	if filepath.Base(path) == "updates.jsonl" {
		chat := filepath.Join(filepath.Dir(path), "chat_history.jsonl")
		if fi, err := os.Stat(chat); err == nil && !fi.IsDir() && fi.Size() > 0 {
			return chat
		}
	}
	return path
}

// grokSnapshot 把 updates.jsonl 换成同目录的 chat_history.jsonl（存在时）。历史索引记的是流水，
// 技能用量、工具、图片扫描仍按快照的格式读。
func grokSnapshot(path string) string {
	if filepath.Base(path) != "updates.jsonl" {
		return path
	}
	chat := filepath.Join(filepath.Dir(path), "chat_history.jsonl")
	if fi, err := os.Stat(chat); err == nil && !fi.IsDir() {
		return chat
	}
	return path
}

func grokWatch(sessionID, reported string) string {
	if reported != "" {
		return grokChatFile(reported)
	}
	if sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	if p := newestGrokFile(filepath.Join(home, ".grok", "sessions", "*", sessionID, "updates.jsonl"), true); p != "" {
		return p
	}
	return newestGrokFile(filepath.Join(home, ".grok", "sessions", "*", sessionID, "chat_history.jsonl"), false)
}

func newestGrokFile(pattern string, nonEmpty bool) string {
	matches, _ := filepath.Glob(pattern)
	best := ""
	var bestM int64
	for _, p := range matches {
		fi, err := os.Stat(p)
		if err != nil || fi.IsDir() || (nonEmpty && fi.Size() == 0) {
			continue
		}
		mt := mtimeOf(p)
		if best == "" || mt > bestM {
			best, bestM = p, mt
		}
	}
	return best
}

// grokChatSeg 是还没交出去的一段正文。工具调用挂在这段助手消息上，结果另成一条。
type grokChatSeg struct {
	role  string
	text  strings.Builder
	tools []ChatBlock
	at    int64
	off   int64
	end   int64
}

func (s *grokChatSeg) message() *ChatMessage {
	var blocks []ChatBlock
	if s.role == "user" {
		if t := grokUserBody(s.text.String()); t != "" {
			blocks = append(blocks, ChatBlock{Type: "text", Text: t})
		}
	} else if t := strings.TrimSpace(s.text.String()); t != "" {
		blocks = append(blocks, ChatBlock{Type: "text", Text: t})
	}
	blocks = append(blocks, s.tools...)
	if len(blocks) == 0 {
		return nil
	}
	for i := range blocks {
		blocks[i] = sanitizeBlock(blocks[i])
	}
	role := s.role
	if role != "user" && role != "reasoning" {
		role = "assistant"
	}
	return &ChatMessage{
		ID: "line-" + strconv.FormatInt(s.off, 10), Role: role, Blocks: blocks,
		Timestamp: stamp(s.at), Source: "transcript", Offset: s.off,
	}
}

type grokBuilt struct {
	msg ChatMessage
	end int64
}

// readGrokUpdateNew 读 updates.jsonl 里的完整行。from 之前结束的消息不再返回；
// 起始位置在 from 之前、但这一轮又长了的那条会带着同一个 id 再返回，手机按 id 替换。
func readGrokUpdateNew(path string, from int64) ([]ChatMessage, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, from, ErrChatNotFound
	}
	defer f.Close()
	var seg *grokChatSeg
	var built []grokBuilt
	flush := func() {
		if seg == nil {
			return
		}
		m := seg.message()
		end := seg.end
		seg = nil
		if m != nil {
			built = append(built, grokBuilt{*m, end})
		}
	}
	var prev int64
	n, _, err := scanLines(f, func(line []byte, n int64) {
		off := prev
		prev = n
		applyGrokUpdateLine(bytes.TrimRight(line, "\r\n"), off, n, &seg, flush, &built)
	})
	if err != nil {
		return nil, from, err
	}
	flush()
	msgs := make([]ChatMessage, 0, len(built))
	for _, b := range built {
		if b.end > from {
			msgs = append(msgs, b.msg)
		}
	}
	return msgs, n, nil
}

func applyGrokUpdateLine(raw []byte, off, end int64, seg **grokChatSeg, flush func(), built *[]grokBuilt) {
	var row struct {
		Timestamp int64 `json:"timestamp"`
		Params    struct {
			Update json.RawMessage `json:"update"`
		} `json:"params"`
	}
	if json.Unmarshal(raw, &row) != nil || len(row.Params.Update) == 0 {
		return
	}
	var update grokUpdate
	if json.Unmarshal(row.Params.Update, &update) != nil {
		return
	}
	at := grokMillis(row.Timestamp)
	switch update.SessionUpdate {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk":
		role := "assistant"
		switch update.SessionUpdate {
		case "user_message_chunk":
			role = "user"
		case "agent_thought_chunk":
			role = "reasoning"
		}
		// 每个用户 chunk 是一次提问，不和前一条用户消息合并（同 grokUpdateParser）。
		if *seg != nil && ((*seg).role != role || role == "user") {
			flush()
		}
		if *seg == nil {
			*seg = &grokChatSeg{role: role, at: at, off: off, end: end}
		}
		if (*seg).at == 0 {
			(*seg).at = at
		}
		(*seg).end = end
		(*seg).text.WriteString(grokContentText(update.Content))
	case "tool_call":
		if *seg != nil && (*seg).role != "assistant" {
			flush()
		}
		if *seg == nil {
			*seg = &grokChatSeg{role: "assistant", at: at, off: off, end: end}
		}
		if (*seg).at == 0 {
			(*seg).at = at
		}
		(*seg).end = end
		name := update.Title
		if name == "" {
			name = update.Name
		}
		if name == "" {
			name = "tool"
		}
		(*seg).tools = append((*seg).tools, ChatBlock{
			Type: "tool-call", Name: name, CallID: grokCallID(update.ToolCallID), Input: grokBlockInput(update.RawInput),
		})
	case "tool_call_update":
		text, ok := update.result()
		if !ok {
			return
		}
		flush()
		if strings.TrimSpace(text) == "" {
			return
		}
		block := sanitizeBlock(ChatBlock{Type: "tool-result", Output: text, CallID: grokCallID(update.ToolCallID)})
		*built = append(*built, grokBuilt{
			msg: ChatMessage{
				ID: "line-" + strconv.FormatInt(off, 10), Role: "tool", Blocks: []ChatBlock{block},
				Timestamp: stamp(at), Source: "transcript", Offset: off,
			},
			end: end,
		})
	}
}

func grokBlockInput(raw json.RawMessage) json.RawMessage {
	raw = unwrapJSON(bytes.TrimSpace(raw))
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '{' || raw[0] == '[' {
		return append(json.RawMessage(nil), raw...)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return grokInput(s)
	}
	return grokInput(string(raw))
}
