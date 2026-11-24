package history

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TraceEvent 是会话里某个区间（如讨论的一轮执行）内的一次工具/命令/技能/网页调用。
type TraceEvent struct {
	At     time.Time `json:"at,omitzero"`
	Kind   string    `json:"kind"` // tool | cli | mcp | skill | web | search
	Name   string    `json:"name"`
	Detail string    `json:"detail,omitempty"`
	Count  int       `json:"count,omitempty"`
	Failed bool      `json:"failed,omitempty"`
}

// TraceSummary 是一轮执行的过程汇总与精简时间线（最多保留最近 maxTraceEvents 条）。
type TraceSummary struct {
	Tools    int          `json:"tools,omitempty"`
	Commands int          `json:"commands,omitempty"`
	Skills   int          `json:"skills,omitempty"`
	MCP      int          `json:"mcp,omitempty"`
	Web      int          `json:"web,omitempty"`
	Events   []TraceEvent `json:"events,omitempty"`
}

const (
	maxTraceEvents = 50
	maxTraceDetail = 100
)

// ScanTrace 扫描会话文件 [startOffset, endOffset) 范围内的工具/命令/技能/网页调用。
// endOffset <= 0 表示读到当前文件末尾。返回汇总、原始调用总次数、本次读到的字节位置。
func ScanTrace(agent, sessionID, reported string, startOffset, endOffset int64) (TraceSummary, int, int64, error) {
	path := TranscriptPath(agent, sessionID, reported)
	if path == "" {
		return TraceSummary{}, 0, startOffset, nil
	}
	var msgs []ChatMessage
	var next int64
	if agent == "grok" {
		var err error
		msgs, next, err = readGrokNew(path, max(0, startOffset))
		if err != nil {
			return TraceSummary{}, 0, startOffset, err
		}
	} else {
		src, size, err := openScanFile(path, os.Open)
		if err != nil {
			return TraceSummary{}, 0, startOffset, err
		}
		defer src.Close()
		if startOffset < 0 || startOffset > size {
			startOffset = 0
		}
		limit := size
		if endOffset > 0 && endOffset < limit {
			limit = endOffset
		}
		if limit <= startOffset {
			return TraceSummary{}, 0, startOffset, nil
		}
		paged := agent == "codex" && codexPaginated(src)
		if _, err := src.Seek(startOffset, io.SeekStart); err != nil {
			return TraceSummary{}, 0, startOffset, err
		}
		buf := make([]byte, limit-startOffset)
		n, err := io.ReadFull(src, buf)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return TraceSummary{}, 0, startOffset, err
		}
		buf = buf[:n]
		if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
			buf = buf[:idx+1]
		} else {
			return TraceSummary{}, 0, startOffset, nil
		}
		next = startOffset + int64(len(buf))
		full := &antigravityFull{path: path}
		off := startOffset
		for len(buf) > 0 {
			var line []byte
			if i := bytes.IndexByte(buf, '\n'); i >= 0 {
				line, buf = buf[:i], buf[i+1:]
			} else {
				line, buf = buf, nil
			}
			lineLen := int64(len(line) + 1)
			raw := bytes.TrimSpace(line)
			if len(raw) > 0 {
				if agent == "antigravity" {
					raw = full.line(raw)
				}
				if m := decodeChatLine(agent, raw, off, paged); m != nil {
					msgs = append(msgs, *m)
				}
			}
			off += lineLen
		}
	}
	return buildTraceSummary(msgs), countTraceCalls(msgs), next, nil
}

func countTraceCalls(msgs []ChatMessage) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == "tool-call" && b.Name != "" {
				n++
			}
		}
	}
	return n
}

func buildTraceSummary(msgs []ChatMessage) TraceSummary {
	var sum TraceSummary
	failed := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == "tool-result" && b.CallID != "" && b.IsError {
				failed[b.CallID] = true
			}
		}
	}
	var events []TraceEvent
	for _, m := range msgs {
		var at time.Time
		if m.Timestamp != nil && *m.Timestamp > 0 {
			at = time.UnixMilli(*m.Timestamp)
		}
		for _, b := range m.Blocks {
			if b.Type != "tool-call" || b.Name == "" {
				continue
			}
			ev := classifyTraceTool(b.Name, b.Input, at, b.CallID != "" && failed[b.CallID])
			switch ev.Kind {
			case "cli":
				sum.Commands++
			case "skill":
				sum.Skills++
			case "mcp":
				sum.MCP++
			case "web", "search":
				sum.Web++
			default:
				sum.Tools++
			}
			if n := len(events); n > 0 && events[n-1].Kind == ev.Kind && events[n-1].Name == ev.Name && events[n-1].Detail == ev.Detail && events[n-1].Failed == ev.Failed {
				if events[n-1].Count == 0 {
					events[n-1].Count = 2
				} else {
					events[n-1].Count++
				}
				continue
			}
			events = append(events, ev)
		}
	}
	if len(events) > maxTraceEvents {
		events = events[len(events)-maxTraceEvents:]
	}
	sum.Events = events
	return sum
}

func classifyTraceTool(name string, input json.RawMessage, at time.Time, failed bool) TraceEvent {
	ev := TraceEvent{At: at, Kind: "tool", Name: name, Failed: failed}
	var fields map[string]any
	if len(input) > 0 {
		json.Unmarshal(input, &fields)
	}
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := fields[k].(string); ok && strings.TrimSpace(v) != "" {
				return oneLineTrace(v, maxTraceDetail)
			}
		}
		return ""
	}
	lower := strings.ToLower(name)
	switch {
	case strings.HasPrefix(name, "mcp__"):
		parts := strings.SplitN(strings.TrimPrefix(name, "mcp__"), "__", 2)
		ev.Kind = "mcp"
		if len(parts) == 2 {
			ev.Name = parts[0] + " · " + parts[1]
		}
		ev.Detail = str("query", "path", "url", "command", "name", "id")
	case lower == "call_mcp_tool" || lower == "calldynamictool":
		ev.Kind = "mcp"
		srv := str("ServerName", "server", "namespace")
		tool := str("ToolName", "tool", "toolName")
		if srv != "" && tool != "" {
			ev.Name = srv + " · " + tool
		} else if tool != "" {
			ev.Name = tool
		}
	case lower == "skill":
		ev.Kind = "skill"
		ev.Name = str("skill", "name")
		if ev.Name == "" {
			ev.Name = name
		}
		ev.Detail = str("args")
	case lower == "bash" || lower == "shell" || lower == "exec_command" || lower == "run_terminal_command" || lower == "run_command" || lower == "commandexecution":
		ev.Kind = "cli"
		ev.Name = "shell"
		ev.Detail = str("command", "CommandLine", "cmd", "script")
	case lower == "websearch" || lower == "search_web" || lower == "web_search_call" || lower == "web.search":
		ev.Kind = "search"
		ev.Name = "search"
		ev.Detail = str("query", "q")
	case lower == "webfetch" || lower == "fetch_url" || lower == "read_url_content":
		ev.Kind = "web"
		ev.Name = "web"
		ev.Detail = str("url", "Url")
	default:
		detail := str("file_path", "path", "Path", "TargetFile", "pattern", "query", "Query", "command", "url", "description")
		if (lower == "read" || lower == "read_file") && strings.HasSuffix(detail, "/SKILL.md") {
			ev.Kind = "skill"
			ev.Name = filepath.Base(filepath.Dir(detail))
		} else {
			ev.Detail = shortTracePath(detail)
		}
	}
	return ev
}

func oneLineTrace(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func shortTracePath(s string) string {
	if !strings.HasPrefix(s, "/") {
		return s
	}
	// 只留最后两级目录和文件名，避免把本机完整目录树铺在卡片里
	parts := strings.Split(filepath.Clean(s), string(filepath.Separator))
	if len(parts) > 3 {
		return filepath.Join(parts[len(parts)-3:]...)
	}
	return s
}
