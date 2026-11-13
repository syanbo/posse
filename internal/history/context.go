package history

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
)

// Context 是会话最后一次请求占用的上下文，单位 token。
type Context struct {
	Used            int      `json:"used"`
	Window          int      `json:"window"`
	ReportedPercent *float64 `json:"reportedPercent,omitempty"`
}

// Percent 是已用的百分比，四舍五入到整数，最多 100。
func (c Context) Percent() int {
	if c.ReportedPercent != nil {
		return min(100, max(0, int(math.Round(*c.ReportedPercent))))
	}
	if c.Window <= 0 {
		return 0
	}
	return min(100, (c.Used*100+c.Window/2)/c.Window)
}

const (
	contextChunk = 1 << 20  // 从文件末尾往前每次多读这么多
	contextLimit = 16 << 20 // 最多读到末尾往前这么远，再找不到就算没有
)

// ReadContext 从会话文件末尾往前找最后一次请求的 token 用量。只有 claude 和 codex 的会话文件逐次记录了用量；
// grok 只记每轮的累计用量，cursor 没有记录，都返回 false。
func ReadContext(agent, path, model string) (Context, bool) {
	var parse func(line []byte) (Context, bool)
	var marker []byte
	switch agent {
	case "claude":
		marker, parse = []byte(`"usage"`), func(l []byte) (Context, bool) { return claudeContext(l, model) }
	case "codex":
		marker, parse = []byte(`"token_count"`), codexContext
	default:
		return Context{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return Context{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Context{}, false
	}
	end := fi.Size()
	var tail []byte // 上一块开头不完整的那一行，拼到这一块后面
	for read := int64(0); end > 0 && read < contextLimit; {
		n := min(int64(contextChunk), end)
		buf := make([]byte, n, int(n)+len(tail))
		if _, err := f.ReadAt(buf, end-n); err != nil && err != io.EOF {
			return Context{}, false
		}
		end -= n
		read += n
		buf = append(buf, tail...)
		// 开头一行可能被切断，留给下一块；读到文件头时它是完整的
		first := 0
		if end > 0 {
			i := bytes.IndexByte(buf, '\n')
			if i < 0 {
				tail = buf
				continue
			}
			first = i + 1
		}
		tail = buf[:first]
		lines := buf[first:]
		for len(lines) > 0 {
			i := bytes.LastIndexByte(bytes.TrimRight(lines, "\n"), '\n')
			line := lines[i+1:]
			lines = lines[:i+1]
			if agent == "claude" && claudeCompactBoundary(line) {
				return Context{}, false
			}
			if bytes.Contains(line, marker) {
				if c, ok := parse(line); ok {
					return c, c.Window > 0
				}
			}
		}
	}
	return Context{}, false
}

func claudeContext(line []byte, model string) (Context, bool) {
	var rec struct {
		Type        string `json:"type"`
		IsSidechain bool   `json:"isSidechain"`
		Message     struct {
			Model string `json:"model"`
			Usage *struct {
				Input       int `json:"input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
				Output      int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "assistant" || rec.IsSidechain || rec.Message.Usage == nil {
		return Context{}, false
	}
	u := rec.Message.Usage
	used := u.Input + u.CacheCreate + u.CacheRead
	// 出错、中断时 Claude Code 写的 <synthetic> 回复用量全是 0
	if used == 0 {
		return Context{}, false
	}
	if rec.Message.Model != "" && rec.Message.Model != "<synthetic>" {
		model = rec.Message.Model
	}
	return Context{Used: used, Window: claudeWindow(model, used)}, true
}

// claudeWindow 是 Claude 模型的上下文窗口。会话文件里没有窗口大小，按模型推断：
// 名字带 [1m] 的、Claude 5 起的 Opus 和 Sonnet 是 100 万；其余已知系列按 20 万估算，
// 已用超 20 万时按 100 万估算。认不出的模型返回 0，不给出百分比。
func claudeWindow(model string, used int) int {
	const small, large = 200_000, 1_000_000
	m := strings.ToLower(model)
	if i := strings.LastIndexByte(m, '/'); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.Index(m, "claude-"); i >= 0 {
		m = m[i:]
	}
	m = strings.ReplaceAll(m, ".", "-")
	if strings.Contains(m, "[1m]") {
		return large
	}
	words := strings.Split(strings.TrimPrefix(m, "claude-"), "-")
	// Claude 3 的旧式命名把系列放在版本号后面。
	if len(words) >= 2 && words[0] == "3" {
		for _, word := range words[1:] {
			if word == "opus" || word == "sonnet" || word == "haiku" {
				return small
			}
		}
	}
	if len(words) >= 2 && (words[0] == "opus" || words[0] == "sonnet" || words[0] == "haiku") {
		if major, err := strconv.Atoi(words[1]); err == nil && major > 0 {
			if used > small || (major >= 5 && words[0] != "haiku") {
				return large
			}
			return small
		}
	}
	return 0
}

func codexContext(line []byte) (Context, bool) {
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info *struct {
				Last struct {
					Total int `json:"total_tokens"`
				} `json:"last_token_usage"`
				Window int `json:"model_context_window"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "event_msg" || rec.Payload.Type != "token_count" || rec.Payload.Info == nil {
		return Context{}, false
	}
	info := rec.Payload.Info
	if info.Window <= 0 || info.Last.Total <= 0 {
		return Context{}, false
	}
	return Context{Used: info.Last.Total, Window: info.Window}, true
}

// 压缩摘要之前的 usage 已不再描述当前上下文，反向扫描必须在这里停下。
func claudeCompactBoundary(line []byte) bool {
	if !bytes.Contains(line, []byte("compact_boundary")) && !bytes.Contains(line, []byte("isCompactSummary")) {
		return false
	}
	var rec struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Summary bool   `json:"isCompactSummary"`
	}
	return json.Unmarshal(line, &rec) == nil && ((rec.Type == "system" && rec.Subtype == "compact_boundary") || (rec.Type == "user" && rec.Summary))
}
