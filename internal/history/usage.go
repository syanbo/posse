package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// UsageItem 是一个技能或 MCP 工具在会话里用了几次。
type UsageItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// MCPUsage 是一个 MCP 服务和它被调用的工具。
type MCPUsage struct {
	Server string      `json:"server"`
	Count  int         `json:"count"`
	Tools  []UsageItem `json:"tools"`
}

// Usage 是一个会话用到的技能和 MCP 工具，按次数从多到少。
type Usage struct {
	Skills []UsageItem `json:"skills"`
	MCP    []MCPUsage  `json:"mcp"`
	// Events 是逐次的使用记录，按会话文件里的顺序（时间从早到晚），只留最近 maxUsageEvents 条
	Events []UsageEvent `json:"events,omitempty"`
}

// UsageEvent 是一次使用：技能（Skill 非空）或 MCP 工具（Server、Tool）。At 取自那一行记录，
// grok、cursor 的会话文件不带时间，为零值。
type UsageEvent struct {
	At     time.Time `json:"at,omitzero"`
	Skill  string    `json:"skill,omitempty"`
	Server string    `json:"server,omitempty"`
	Tool   string    `json:"tool,omitempty"`
}

const maxUsageEvents = 300

// UsageScanner 增量读一个会话文件，统计用到的技能和 MCP 工具。不是并发安全的。
//
// 各家记录方式：
//   - claude：用技能（模型调 Skill 或用户输入斜杠命令）都会注入一段 "Base directory for this skill: <目录>"；
//     MCP 工具名是 mcp__<服务>__<工具>。
//   - codex：用户点名的技能注入成 "<skill><name>…</name>"；MCP 调用是 McpToolCall 条目，同一次调用会记多遍，按 id 去重；
//     执行的命令是 CommandExecution 条目，parsed_cmd 标出了读的文件。
//   - grok：MCP 经 use_tool 调用，tool_name 是 <服务>__<工具>。
//   - cursor：MCP 经 CallDynamicTool 调用，namespace 是服务名（插件带的服务是 plugin-<插件>-<服务>），
//     namespace 为 cursor 的是它自带的工具（WebSearch 等），不算 MCP。
//   - 各家模型自己读 <名字>/SKILL.md（Read、read_file，或 cat、sed 这类读文件的命令）也算用了那个技能。
//     只认读文件的调用：写文件、grep 的参数里提到 SKILL.md 不算，系统提示里的技能列表也不算。
type UsageScanner struct {
	agent, path string
	offset      int64
	skills      map[string]int
	mcp         map[string]map[string]int
	seen        map[string]bool // codex 条目的 id
	events      []UsageEvent
	at          time.Time // 正在解析的那一行的时间
}

func NewUsageScanner(agent, path string) *UsageScanner {
	s := &UsageScanner{agent: agent, path: path}
	s.reset()
	return s
}

func (s *UsageScanner) reset() {
	s.offset = 0
	s.skills, s.mcp, s.seen, s.events = map[string]int{}, map[string]map[string]int{}, map[string]bool{}, nil
}

// Scan 读文件新增的完整行（末尾没写完的一行留到下次），返回到目前为止的统计；文件变短说明被重写，从头再读。
func (s *UsageScanner) Scan() (Usage, error) {
	f, err := os.Open(s.path)
	if err != nil {
		return Usage{}, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Usage{}, err
	}
	if fi.Size() < s.offset {
		s.reset()
	}
	if _, err := f.Seek(s.offset, 0); err != nil {
		return Usage{}, err
	}
	n, _, err := scanLines(f, func(line []byte, _ int64) {
		if hasUsageMarker(line) {
			s.line(line)
		}
	})
	s.offset += n
	return s.result(), err
}

// usageMarkers 先粗筛，只有可能相关的行才解析 JSON。
var usageMarkers = [][]byte{
	[]byte("Base directory for this skill"), []byte("mcp__"), []byte("<skill>"),
	[]byte("McpToolCall"), []byte("use_tool"), []byte("SKILL.md"), []byte("CallDynamicTool"),
}

func hasUsageMarker(line []byte) bool { return containsAny(line, usageMarkers) }

func (s *UsageScanner) line(b []byte) {
	// claude、codex 每行带 timestamp
	var ts struct {
		Timestamp time.Time `json:"timestamp"`
	}
	json.Unmarshal(b, &ts)
	s.at = ts.Timestamp
	switch s.agent {
	case "claude":
		s.claudeLine(b)
	case "codex":
		s.codexLine(b)
	case "grok":
		s.grokLine(b)
	case "cursor":
		s.cursorLine(b)
	}
}

func (s *UsageScanner) cursorLine(b []byte) {
	var r struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	blocks, _ := contentBlocks(r.Message.Content)
	for _, bl := range blocks {
		if bl.Type != "tool_use" {
			continue
		}
		switch bl.Name {
		case "CallDynamicTool":
			var a struct {
				Namespace string `json:"namespace"`
				ToolName  string `json:"toolName"`
			}
			if json.Unmarshal(bl.Input, &a) == nil && a.Namespace != "cursor" {
				s.addMCPTool(a.Namespace, a.ToolName)
			}
		case "Read", "ReadFile":
			a, _ := decodeToolArgs(bl.Input)
			s.skillRead(a.Path)
		case "Shell":
			s.skillCommand(commandOf(bl.Input))
		}
	}
}

func (s *UsageScanner) claudeLine(b []byte) {
	var r struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	blocks, _ := contentBlocks(r.Message.Content)
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			s.skillBase(bl.Text)
		case "tool_use":
			if name, ok := strings.CutPrefix(bl.Name, "mcp__"); ok {
				s.addMCP(name)
			} else if bl.Name == "Read" {
				a, _ := decodeToolArgs(bl.Input)
				s.skillRead(a.FilePath)
			}
		}
	}
}

// skillBase 认出 Claude 用技能时注入的 "Base directory for this skill: <目录>"。
func (s *UsageScanner) skillBase(text string) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(text), "Base directory for this skill:")
	if !ok {
		return
	}
	dir, _, _ := strings.Cut(strings.TrimSpace(rest), "\n")
	if name := filepath.Base(strings.TrimSpace(dir)); name != "" && name != "." && name != "/" {
		s.addSkill(name)
	}
}

// addSkill 记一次技能使用。
func (s *UsageScanner) addSkill(name string) {
	s.skills[name]++
	s.event(UsageEvent{Skill: name})
}

func (s *UsageScanner) event(e UsageEvent) {
	e.At = s.at
	s.events = append(s.events, e)
	if len(s.events) > maxUsageEvents*2 {
		s.events = slices.Clone(s.events[len(s.events)-maxUsageEvents:])
	}
}

// skillRead 认出读文件的工具读的是 <名字>/SKILL.md。
func (s *UsageScanner) skillRead(path string) {
	if filepath.Base(path) != "SKILL.md" {
		return
	}
	if name := filepath.Base(filepath.Dir(path)); name != "" && name != "." && name != "/" {
		s.addSkill(name)
	}
}

// skillCmdRe 认出用 cat、sed 这类命令读 SKILL.md；只看命令开头的程序，避免把 grep、写文件里提到的路径算进去。
var skillCmdRe = regexp.MustCompile(`^\s*(?:cat|sed|head|tail|nl|less|bat)\b[^|;&]*?([A-Za-z0-9_.@:-]+)/SKILL\.md`)

// skillCommand 认出 shell 命令读了某个技能；一条命令只算一个。
func (s *UsageScanner) skillCommand(cmd string) {
	if m := skillCmdRe.FindStringSubmatch(cmd); m != nil {
		s.addSkill(m[1])
	}
}

// addMCP 记一次 MCP 调用，name 是 <服务>__<工具>；服务名里可能有下划线，所以按最后一个 "__" 切。
func (s *UsageScanner) addMCP(name string) {
	i := strings.LastIndex(name, "__")
	if i <= 0 {
		return
	}
	s.addMCPTool(name[:i], name[i+2:])
}

func (s *UsageScanner) addMCPTool(server, tool string) {
	if server == "" || tool == "" {
		return
	}
	if s.mcp[server] == nil {
		s.mcp[server] = map[string]int{}
	}
	s.mcp[server][tool]++
	s.event(UsageEvent{Server: server, Tool: tool})
}

var codexSkillRe = regexp.MustCompile(`^<skill>\s*<name>([^<]+)</name>`)

func (s *UsageScanner) codexLine(b []byte) {
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Text string `json:"text"`
			} `json:"content"`
			Item struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Server    string `json:"server"`
				Tool      string `json:"tool"`
				ParsedCmd []struct {
					Type string `json:"type"`
					Path string `json:"path"`
				} `json:"parsed_cmd"`
			} `json:"item"`
		} `json:"payload"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	p := r.Payload
	switch {
	// 用户消息在 response_item 和 item_completed 里各记一遍，只认 response_item
	case r.Type == "response_item" && p.Type == "message" && p.Role == "user":
		for _, c := range p.Content {
			if m := codexSkillRe.FindStringSubmatch(strings.TrimSpace(c.Text)); m != nil {
				s.addSkill(strings.TrimSpace(m[1]))
			}
		}
	case r.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call"):
		if name, ok := strings.CutPrefix(p.Name, "mcp__"); ok {
			s.addMCP(name)
		} else if p.Type == "function_call" {
			// custom_tool_call 的参数在 input 里，是补丁这类原文，不是命令
			s.skillCommand(commandOf([]byte(p.Arguments)))
		}
	case p.Item.Type == "McpToolCall":
		if s.once(p.Item.ID) {
			s.addMCPTool(p.Item.Server, p.Item.Tool)
		}
	// 新版 Codex 把命令包在 exec 的脚本里跑，每条实际执行的命令记一条 CommandExecution，
	// parsed_cmd 已经标出读了哪个文件
	case p.Item.Type == "CommandExecution":
		if s.once(p.Item.ID) {
			for _, c := range p.Item.ParsedCmd {
				if c.Type == "read" {
					s.skillRead(c.Path)
				}
			}
		}
	}
}

// once 报告 id 是不是第一次见：同一个条目会在开始、完成时各记一遍。没有 id 的照算。
func (s *UsageScanner) once(id string) bool {
	if id == "" {
		return true
	}
	if s.seen[id] {
		return false
	}
	s.seen[id] = true
	return true
}

func (s *UsageScanner) grokLine(b []byte) {
	var r struct {
		ToolCalls []grokToolCall `json:"tool_calls"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	for _, c := range r.ToolCalls {
		if c.Name == "use_tool" {
			var a struct {
				ToolName string `json:"tool_name"`
			}
			if json.Unmarshal([]byte(c.Arguments), &a) == nil {
				s.addMCP(a.ToolName)
			}
			continue
		}
		switch c.Name {
		case "read_file":
			a, _ := decodeToolArgs([]byte(c.Arguments))
			s.skillRead(a.TargetFile)
		case "run_terminal_command":
			s.skillCommand(commandOf([]byte(c.Arguments)))
		}
	}
}

func (s *UsageScanner) result() Usage {
	var u Usage
	u.Events = slices.Clone(s.events[max(0, len(s.events)-maxUsageEvents):])
	for name, n := range s.skills {
		u.Skills = append(u.Skills, UsageItem{name, n})
	}
	sortItems(u.Skills)
	for server, tools := range s.mcp {
		m := MCPUsage{Server: server}
		for tool, n := range tools {
			m.Tools = append(m.Tools, UsageItem{tool, n})
			m.Count += n
		}
		sortItems(m.Tools)
		u.MCP = append(u.MCP, m)
	}
	slices.SortFunc(u.MCP, func(a, b MCPUsage) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Server, b.Server)
	})
	return u
}

func sortItems(items []UsageItem) {
	slices.SortFunc(items, func(a, b UsageItem) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Name, b.Name)
	})
}

// TranscriptPath 找一个会话的文件：hooks 报过路径就用它，否则按各家的目录结构和会话 ID 找。找不到返回空。
func TranscriptPath(agent, sessionID, reported string) string {
	if reported != "" {
		if _, err := os.Stat(reported); err == nil {
			return reported
		}
	}
	if sessionID == "" || strings.ContainsAny(sessionID, `/\*?[`) {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	var pattern string
	switch agent {
	case "claude":
		pattern = filepath.Join(home, ".claude", "projects", "*", sessionID+".jsonl")
	case "grok":
		pattern = filepath.Join(home, ".grok", "sessions", "*", sessionID, "chat_history.jsonl")
	case "codex":
		pattern = filepath.Join(home, ".codex", "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl")
	case "cursor":
		pattern = filepath.Join(home, ".cursor", "projects", "*", "agent-transcripts", sessionID, sessionID+".jsonl")
	default:
		return ""
	}
	matches, _ := filepath.Glob(pattern)
	if len(matches) == 0 {
		return ""
	}
	// 同一个 ID 有多个文件时取最新的
	slices.SortFunc(matches, func(a, b string) int { return int(mtimeOf(b) - mtimeOf(a)) })
	return matches[0]
}
