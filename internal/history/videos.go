package history

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ToolVideo 是工具输出里提到的一个视频文件：录屏工具返回的路径、命令打印出的产物等。
// 视频本身不在会话文件里，只记路径，文件还在不在由调用方去看。
type ToolVideo struct {
	Path string
	At   time.Time // 最后一次出现在工具输出里的时间
}

// VideoScanner 增量读 Claude 或 Codex 的会话文件，从工具输出里找视频文件的路径。
// 只看工具输出：用户消息里提到的视频不是 agent 产出的。不是并发安全的。
type VideoScanner struct {
	agent  string
	path   string
	offset int64
	videos []ToolVideo
	seen   map[string]int // 路径 → 在 videos 里的位置
}

func NewVideoScanner(agent, path string) *VideoScanner {
	return &VideoScanner{agent: agent, path: path, seen: map[string]int{}}
}

// Path 是扫描的会话文件。
func (s *VideoScanner) Path() string { return s.path }

var videoMarkers = [][]byte{[]byte(".mp4"), []byte(".mov"), []byte(".m4v"), []byte(".webm"), []byte(".MP4"), []byte(".MOV")}

// videoPathRe 认绝对路径或 ~/ 开头的路径，前面得是行首或分隔符，免得把相对路径的后半截当成绝对路径；
// 带空格的路径认不全，工具输出里的产物路径很少带空格。
var videoPathRe = regexp.MustCompile(`(?i)(?:^|[\s"'(\[<=,:])((?:~/|/)[^\s"'<>|*?:]*\.(?:mp4|mov|m4v|webm))\b`)

// Scan 读文件新增的完整行，返回到目前为止的视频，按第一次出现的顺序；文件变短说明被重写，从头再读。
func (s *VideoScanner) Scan() ([]ToolVideo, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() < s.offset {
		s.offset, s.videos, s.seen = 0, nil, map[string]int{}
	}
	if _, err := f.Seek(s.offset, io.SeekStart); err != nil {
		return nil, err
	}
	n, _, err := scanLines(f, func(line []byte, _ int64) {
		if containsAny(line, videoMarkers) {
			s.line(line)
		}
	})
	s.offset += n
	return s.videos, err
}

func (s *VideoScanner) line(b []byte) {
	var outputs []json.RawMessage
	var at time.Time
	switch s.agent {
	case "claude":
		var l claudeImageLine
		if json.Unmarshal(b, &l) != nil || l.Type != "user" {
			return
		}
		var blocks []claudeBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return
		}
		for _, bl := range blocks {
			if bl.Type == "tool_result" {
				outputs = append(outputs, bl.Content)
			}
		}
		at = l.Timestamp
	case "codex":
		var l struct {
			Timestamp time.Time `json:"timestamp"`
			Type      string    `json:"type"`
			Payload   struct {
				Type   string          `json:"type"`
				Output json.RawMessage `json:"output"`
				Item   struct {
					Type             string          `json:"type"`
					AggregatedOutput json.RawMessage `json:"aggregated_output"`
					Result           json.RawMessage `json:"result"`
				} `json:"item"`
			} `json:"payload"`
		}
		if json.Unmarshal(b, &l) != nil {
			return
		}
		p := l.Payload
		switch {
		case l.Type == "response_item" && (p.Type == "function_call_output" || p.Type == "custom_tool_call_output"):
			outputs = append(outputs, p.Output)
		case p.Type == "item_completed" && p.Item.Type == "CommandExecution":
			outputs = append(outputs, p.Item.AggregatedOutput)
		case p.Type == "item_completed" && p.Item.Type == "McpToolCall":
			outputs = append(outputs, p.Item.Result)
		}
		at = l.Timestamp
	}
	for _, raw := range outputs {
		var v any
		if json.Unmarshal(raw, &v) != nil {
			continue
		}
		eachString(v, func(text string) {
			for _, m := range videoPathRe.FindAllStringSubmatch(text, -1) {
				s.add(expandHome(m[1]), at)
			}
		})
	}
}

func (s *VideoScanner) add(path string, at time.Time) {
	path = filepath.Clean(path)
	if i, ok := s.seen[path]; ok {
		s.videos[i].At = at
		return
	}
	s.seen[path] = len(s.videos)
	s.videos = append(s.videos, ToolVideo{Path: path, At: at})
}

// eachString 对 JSON 值里的每个字符串调用 fn。MCP 工具把结果写成 JSON 字符串时，里面的字符串也算。
func eachString(v any, fn func(string)) {
	switch v := v.(type) {
	case string:
		var inner any
		if t := strings.TrimSpace(v); strings.HasPrefix(t, "{") && json.Unmarshal([]byte(t), &inner) == nil {
			eachString(inner, fn)
			return
		}
		fn(v)
	case []any:
		for _, x := range v {
			eachString(x, fn)
		}
	case map[string]any:
		for _, x := range v {
			eachString(x, fn)
		}
	}
}

func expandHome(p string) string {
	if !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[2:])
}
