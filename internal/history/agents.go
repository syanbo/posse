package history

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// discover 列出四家 agent 的会话文件。
func discover(r Roots) []source {
	var out []source
	claude := filepath.Join(r.Home, ".claude", "projects")
	// 只要项目目录下一层的会话文件；<会话>/subagents/ 里是子 agent 的过程记录
	for _, p := range globFiles(filepath.Join(claude, "*", "*.jsonl")) {
		out = append(out, source{agent: "claude", transcript: p, parser: claudeParser{}})
	}
	codex := filepath.Join(r.Home, ".codex")
	for _, dir := range []string{"sessions", "archived_sessions"} {
		for _, p := range walkFiles(filepath.Join(codex, dir), func(path string, d fs.DirEntry) bool {
			return strings.HasPrefix(d.Name(), "rollout-") && strings.HasSuffix(d.Name(), ".jsonl")
		}) {
			out = append(out, source{agent: "codex", transcript: p, parser: codexParser{}})
		}
	}
	// cursor 的标题、目录和时间在 ~/.cursor/chats/<md5(目录)>/<会话>/meta.json
	cursorMeta := map[string]string{}
	for _, m := range globFiles(filepath.Join(r.Home, ".cursor", "chats", "*", "*", "meta.json")) {
		cursorMeta[filepath.Base(filepath.Dir(m))] = m
	}
	cursorDirs := map[string]string{}
	for _, p := range globFiles(filepath.Join(r.Home, ".cursor", "projects", "*", "agent-transcripts", "*", "*.jsonl")) {
		src := source{agent: "cursor", transcript: p, meta: cursorMeta[sessionIDFromPath(p)], parser: cursorParser{}}
		if src.meta == "" {
			project := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
			dir, ok := cursorDirs[project]
			if !ok {
				dir = cursorProjectDir(project)
				cursorDirs[project] = dir
			}
			src.cwd = dir
		}
		out = append(out, src)
	}
	for _, p := range globFiles(filepath.Join(r.Home, ".grok", "sessions", "*", "*", "chat_history.jsonl")) {
		out = append(out, source{agent: "grok", transcript: p, meta: filepath.Join(filepath.Dir(p), "summary.json"), parser: grokParser{}})
	}
	return out
}

func globFiles(pattern string) []string {
	m, _ := filepath.Glob(pattern)
	return m
}

// --- 共用

// toolClip 是工具调用和输出入库的字符数上限：够搜到命令、路径和报错，又不让大段文件内容撑大索引。
// 和 Orca 一致，同一个词在 Orca 里能搜到的工具输出，这里也能搜到。
const toolClip = 3072

// promptClip 是会话第一句话（无标题时当标题）的字符数上限。
const promptClip = 200

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	i := 0
	for count := 0; i < len(s) && count < n; count++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	if i >= len(s) {
		return s
	}
	return s[:i] + "…"
}

func parseTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// touch 用一条记录的时间更新会话的起止时间。
func touch(st *fileState, at int64) {
	if at == 0 {
		return
	}
	if st.Created == 0 || at < st.Created {
		st.Created = at
	}
	if at > st.Updated {
		st.Updated = at
	}
}

var systemReminder = regexp.MustCompile(`(?s)<system-reminder>.*?</system-reminder>`)

// userText 整理用户输入：去掉 agent 注入的提醒，空的返回 ""。
func userText(st *fileState, s string) string {
	s = strings.TrimSpace(systemReminder.ReplaceAllString(s, ""))
	if s != "" && st.FirstPrompt == "" {
		st.FirstPrompt = clip(strings.Join(strings.Fields(s), " "), promptClip)
	}
	return s
}

// toolCall 把工具调用压成一行"名称 参数"，参数优先取命令、路径这类可搜的字段。
func toolCall(name string, input json.RawMessage) string {
	var fields map[string]any
	if json.Unmarshal(input, &fields) == nil {
		for _, k := range []string{"command", "cmd", "file_path", "path", "target_file", "pattern", "query", "url", "description", "prompt"} {
			if v, ok := fields[k].(string); ok && v != "" {
				return clip(name+" "+v, toolClip)
			}
		}
	}
	var str string
	if json.Unmarshal(input, &str) == nil {
		return clip(name+" "+str, toolClip)
	}
	return clip(name+" "+string(input), toolClip)
}

// textOf 取字符串或 [{type:text,text}] 数组里的文字。
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var b []string
	for _, p := range parts {
		if p.Text != "" {
			b = append(b, p.Text)
		}
	}
	return strings.Join(b, "\n")
}

func msg(role, text string, at int64) Message {
	return Message{Role: role, Text: text, At: fromMS(at)}
}

// --- claude：~/.claude/projects/<编码后的目录>/<会话>.jsonl

type claudeParser struct{}

type claudeBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
}

type claudeRecord struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Cwd              string `json:"cwd"`
	GitBranch        string `json:"gitBranch"`
	SessionID        string `json:"sessionId"`
	Timestamp        string `json:"timestamp"`
	AiTitle          string `json:"aiTitle"`
	CustomTitle      string `json:"customTitle"`
	Summary          string `json:"summary"`
	Message          struct {
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
	} `json:"message"`
}

var claudeCommand = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>.*?(?:<command-args>(.*?)</command-args>)?`)

func (claudeParser) meta(string, *fileState) error { return nil }

func (claudeParser) line(raw []byte, st *fileState) []Message {
	var r claudeRecord
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	// Claude Code 自己生成的提示（如中断说明）记成 <synthetic>，不是模型
	if r.Type == "assistant" && r.Message.Model != "" && r.Message.Model != "<synthetic>" {
		st.Model = r.Message.Model
	}
	switch r.Type {
	case "custom-title":
		// 用户自己起的名字优先于自动标题
		if r.CustomTitle != "" {
			st.Title, st.TitleManual = r.CustomTitle, true
		}
		return nil
	case "ai-title":
		if r.AiTitle != "" && !st.TitleManual {
			st.Title = r.AiTitle
		}
		return nil
	case "summary":
		if r.Summary != "" && st.Title == "" {
			st.Title = r.Summary
		}
		return nil
	case "user", "assistant":
	default:
		return nil
	}
	if r.IsSidechain {
		return nil
	}
	at := parseTime(r.Timestamp)
	touch(st, at)
	if st.Cwd == "" {
		st.Cwd = r.Cwd
	}
	if r.GitBranch != "" && r.GitBranch != "HEAD" {
		st.Branch = r.GitBranch
	}
	if st.SessionID == "" {
		st.SessionID = r.SessionID
	}
	if r.Type == "user" && (r.IsMeta || r.IsCompactSummary) {
		return nil
	}
	var blocks []claudeBlock
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		blocks = []claudeBlock{{Type: "text", Text: s}}
	} else if json.Unmarshal(r.Message.Content, &blocks) != nil {
		return nil
	}
	var out []Message
	for _, b := range blocks {
		switch {
		case b.Type == "text" && r.Type == "user":
			if t := claudeUserText(st, b.Text); t != "" {
				out = append(out, msg("user", t, at))
			}
		case b.Type == "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, msg("assistant", t, at))
			}
		case b.Type == "tool_use":
			out = append(out, msg("tool", toolCall(b.Name, b.Input), at))
		case b.Type == "tool_result":
			if t := clip(textOf(b.Content), toolClip); t != "" {
				out = append(out, msg("tool", t, at))
			}
		}
	}
	return out
}

// claudeUserText 过滤 Claude Code 自己写进用户消息的内容：本地命令的输出、后台任务通知、中断标记。
// 斜杠命令保留成 "/命令 参数"。
func claudeUserText(st *fileState, s string) string {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "<command-name>"), strings.HasPrefix(t, "<command-message>"):
		m := claudeCommand.FindStringSubmatch(t)
		if m == nil {
			return ""
		}
		return userText(st, strings.TrimSpace(m[1]+" "+m[2]))
	case strings.HasPrefix(t, "<local-command-"), strings.HasPrefix(t, "<bash-stdout>"), strings.HasPrefix(t, "<bash-stderr>"),
		strings.HasPrefix(t, "<task-notification>"), strings.HasPrefix(t, "[Request interrupted"):
		return ""
	case strings.HasPrefix(t, "<bash-input>"):
		t = "! " + strings.TrimSuffix(strings.TrimPrefix(t, "<bash-input>"), "</bash-input>")
	}
	return userText(st, t)
}

// --- codex：~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl，标题在 ~/.codex/session_index.jsonl

type codexParser struct{}

type codexRecord struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type         string          `json:"type"`
		ID           string          `json:"id"`
		Cwd          string          `json:"cwd"`
		Timestamp    string          `json:"timestamp"`
		Source       json.RawMessage `json:"source"`
		ThreadSource string          `json:"thread_source"`
		Git          struct {
			Branch string `json:"branch"`
		} `json:"git"`
		Role    string `json:"role"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Input     string          `json:"input"`
		Output    json.RawMessage `json:"output"`
		Action    struct {
			Query   string   `json:"query"`
			Command []string `json:"command"`
		} `json:"action"`
		HistoryMode string    `json:"history_mode"`
		Model       string    `json:"model"`
		Item        codexItem `json:"item"`
	} `json:"payload"`
}

// codexItem 是新格式（history_mode=paginated）里 item_completed 事件的内容。
type codexItem struct {
	Type    string `json:"type"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Text             string   `json:"text"`
	Command          []string `json:"command"`
	AggregatedOutput string   `json:"aggregated_output"`
	Changes          map[string]struct {
		Content     string `json:"content"`
		UnifiedDiff string `json:"unified_diff"`
		Diff        string `json:"diff"`
	} `json:"changes"`
	Query     string          `json:"query"`
	Server    string          `json:"server"`
	Tool      string          `json:"tool"`
	Arguments json.RawMessage `json:"arguments"`
}

func (codexParser) meta(string, *fileState) error { return nil }

// codexInjected 是 Codex 以用户身份写进会话、但不是用户输入的内容的开头。
var codexInjected = []string{
	"<environment_context>", "# AGENTS.md instructions", "<user_instructions>", "<permissions instructions>",
	"<skill>", "<image name=", "</image>", "<turn_aborted>", "<subagent_notification>",
}

var codexShellCommand = regexp.MustCompile(`(?s)^<user_shell_command>\s*<command>\s*(.*?)\s*</command>`)

func (codexParser) line(raw []byte, st *fileState) []Message {
	var r codexRecord
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	p := &r.Payload
	at := parseTime(r.Timestamp)
	switch r.Type {
	case "session_meta":
		if st.SessionID == "" {
			st.SessionID = p.ID
		}
		if st.Cwd == "" {
			st.Cwd = p.Cwd
		}
		if p.Git.Branch != "" {
			st.Branch = p.Git.Branch
		}
		if p.HistoryMode != "" {
			st.Mode = p.HistoryMode
		}
		touch(st, parseTime(p.Timestamp))
		// 自动审批、定时任务等不是用户发起的会话
		if strings.Contains(string(p.Source), `"subagent"`) || (p.ThreadSource != "" && p.ThreadSource != "user") {
			st.Skip = true
		}
		return nil
	case "turn_context":
		if p.Cwd != "" {
			st.Cwd = p.Cwd
		}
		if p.Model != "" {
			st.Model = p.Model
		}
		return nil
	case "event_msg":
		// 新格式的会话以事件为准：其中的用户消息不含 Codex 注入的环境和 AGENTS.md，
		// 改动按文件分开记录；同一内容的 response_item 不再读，免得重复
		if st.Mode != "paginated" || p.Type != "item_completed" {
			return nil
		}
		touch(st, at)
		return codexItemMessages(st, p.Item, at)
	case "response_item":
		if st.Mode == "paginated" {
			return nil
		}
	default:
		return nil
	}
	touch(st, at)
	switch p.Type {
	case "message":
		var texts []string
		for _, c := range p.Content {
			t := strings.TrimSpace(c.Text)
			if t == "" {
				continue
			}
			if p.Role == "user" {
				// 用户在 Codex 里用 ! 直接跑的命令，和 Claude 的一样记成 "! 命令"
				if m := codexShellCommand.FindStringSubmatch(t); m != nil {
					t = "! " + m[1]
				} else if hasAnyPrefix(t, codexInjected) {
					continue
				}
			}
			texts = append(texts, t)
		}
		if len(texts) == 0 {
			return nil
		}
		switch p.Role {
		case "user":
			if t := userText(st, strings.Join(texts, "\n")); t != "" {
				return []Message{msg("user", t, at)}
			}
		case "assistant":
			return []Message{msg("assistant", strings.Join(texts, "\n"), at)}
		}
	case "function_call":
		return []Message{msg("tool", toolCall(p.Name, json.RawMessage(p.Arguments)), at)}
	case "custom_tool_call":
		return []Message{msg("tool", clip(p.Name+" "+p.Input, toolClip), at)}
	case "local_shell_call":
		return []Message{msg("tool", clip("shell "+strings.Join(p.Action.Command, " "), toolClip), at)}
	case "function_call_output", "custom_tool_call_output":
		if t := clip(codexOutput(p.Output), toolClip); t != "" {
			return []Message{msg("tool", t, at)}
		}
	case "web_search_call":
		if p.Action.Query != "" {
			return []Message{msg("tool", "web_search "+p.Action.Query, at)}
		}
	}
	return nil
}

func codexItemMessages(st *fileState, it codexItem, at int64) []Message {
	join := func() string {
		var texts []string
		for _, c := range it.Content {
			if t := strings.TrimSpace(c.Text); t != "" {
				texts = append(texts, t)
			}
		}
		return strings.Join(texts, "\n")
	}
	switch it.Type {
	case "UserMessage":
		if t := userText(st, join()); t != "" {
			return []Message{msg("user", t, at)}
		}
	case "AgentMessage":
		if t := join(); t != "" {
			return []Message{msg("assistant", t, at)}
		}
	case "Plan":
		if t := strings.TrimSpace(it.Text); t != "" {
			return []Message{msg("assistant", t, at)}
		}
	case "CommandExecution":
		cmd := it.Command
		// ["/bin/zsh", "-lc", "命令"] 只留命令本身
		if len(cmd) == 3 && (cmd[1] == "-lc" || cmd[1] == "-c") {
			cmd = cmd[2:]
		}
		return []Message{msg("tool", clip("shell: "+strings.Join(cmd, " ")+"\n"+it.AggregatedOutput, toolClip), at)}
	case "FileChange":
		paths := make([]string, 0, len(it.Changes))
		for path := range it.Changes {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		var out []Message
		for _, path := range paths {
			c := it.Changes[path]
			body := c.UnifiedDiff
			if body == "" {
				body = c.Diff
			}
			if body == "" {
				body = c.Content
			}
			out = append(out, msg("tool", clip("apply_patch: "+path+"\n"+body, toolClip), at))
		}
		return out
	case "WebSearch", "Extension":
		if it.Query != "" {
			return []Message{msg("tool", "web_search "+it.Query, at)}
		}
	case "McpToolCall":
		return []Message{msg("tool", toolCall(it.Server+"."+it.Tool, it.Arguments), at)}
	}
	return nil
}

// codexOutput 取工具输出：可能是字符串、{output: …} 或内容数组。
func codexOutput(raw json.RawMessage) string {
	if t := textOf(raw); t != "" {
		return t
	}
	var o struct {
		Output string `json:"output"`
	}
	json.Unmarshal(raw, &o)
	return o.Output
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// --- cursor：~/.cursor/projects/<编码后的目录>/agent-transcripts/<会话>/<会话>.jsonl

type cursorParser struct{}

type cursorRecord struct {
	Role    string `json:"role"`
	Message struct {
		Content []claudeBlock `json:"content"`
	} `json:"message"`
}

var (
	cursorQuery     = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)
	cursorTimestamp = regexp.MustCompile(`<timestamp>(.*?)</timestamp>`)
	cursorZone      = regexp.MustCompile(`^(.*) \(UTC([+-]\d{1,2})(?::?(\d{2}))?\)$`)
)

func (cursorParser) meta(path string, st *fileState) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m struct {
		Title       string `json:"title"`
		Cwd         string `json:"cwd"`
		CreatedAtMs int64  `json:"createdAtMs"`
		UpdatedAtMs int64  `json:"updatedAtMs"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	if m.Title != "" {
		st.Title = m.Title
	}
	if m.Cwd != "" {
		st.Cwd = m.Cwd
	}
	touch(st, m.CreatedAtMs)
	touch(st, m.UpdatedAtMs)
	return nil
}

func (cursorParser) line(raw []byte, st *fileState) []Message {
	var r cursorRecord
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	var out []Message
	for _, b := range r.Message.Content {
		switch {
		case b.Type == "text" && r.Role == "user":
			// cursor 记录里没有时间字段，用户消息开头的 <timestamp> 是唯一的时间来源
			var at int64
			if m := cursorTimestamp.FindStringSubmatch(b.Text); m != nil {
				at = cursorTime(m[1])
				touch(st, at)
			}
			text := b.Text
			if qs := cursorQuery.FindAllStringSubmatch(text, -1); qs != nil {
				var parts []string
				for _, q := range qs {
					parts = append(parts, q[1])
				}
				text = strings.Join(parts, "\n")
			} else {
				text = cursorTimestamp.ReplaceAllString(text, "")
			}
			if t := userText(st, text); t != "" {
				out = append(out, msg("user", t, at))
			}
		case b.Type == "text":
			if t := strings.TrimSpace(b.Text); t != "" {
				out = append(out, msg("assistant", t, 0))
			}
		case b.Type == "tool_use":
			out = append(out, msg("tool", toolCall(b.Name, b.Input), 0))
		}
	}
	return out
}

// cursorTime 解析 "Friday, Sep 25, 2026, 12:17 PM (UTC+8)"。
func cursorTime(s string) int64 {
	m := cursorZone.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0
	}
	h := 0
	for _, c := range strings.TrimLeft(m[2], "+-") {
		h = h*10 + int(c-'0')
	}
	off := h * 3600
	if m[3] != "" {
		off += (int(m[3][0]-'0')*10 + int(m[3][1]-'0')) * 60
	}
	if strings.HasPrefix(m[2], "-") {
		off = -off
	}
	t, err := time.ParseInLocation("Monday, Jan 2, 2006, 3:04 PM", m[1], time.FixedZone("", off))
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

// cursorProjectDir 把 "Users-dengbo-Desktop-AI-Claude" 还原成目录：编码把 / 换成了 -，
// 目录名本身也可能含 -，所以逐段在文件系统里试探。找不到时返回 ""。
func cursorProjectDir(name string) string {
	parts := strings.Split(name, "-")
	var walk func(dir string, i int) string
	walk = func(dir string, i int) string {
		if i == len(parts) {
			return dir
		}
		// 先试最长的段：目录名里的 - 比路径层级少见，但长名一旦存在就更可能是对的
		for j := len(parts); j > i; j-- {
			seg := strings.Join(parts[i:j], "-")
			if seg == "" {
				continue
			}
			next := filepath.Join(dir, seg)
			if fi, err := os.Stat(next); err == nil && fi.IsDir() {
				if got := walk(next, j); got != "" {
					return got
				}
			}
		}
		return ""
	}
	return walk("/", 0)
}

// --- grok：~/.grok/sessions/<URL 编码的目录>/<会话>/chat_history.jsonl，元数据在同目录 summary.json

type grokParser struct{}

type grokRecord struct {
	Type        string          `json:"type"`
	Content     json.RawMessage `json:"content"`
	PromptIndex *int            `json:"prompt_index"`
	ModelID     string          `json:"model_id"`
	ToolCallID  string          `json:"tool_call_id"`
	ToolCalls   []struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"tool_calls"`
	Kind struct {
		ToolType string `json:"tool_type"`
		Action   struct {
			Query string `json:"query"`
		} `json:"action"`
	} `json:"kind"`
}

var grokQuery = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)

// grokTimes 是从同目录 updates.jsonl 建的消息时间表：chat_history.jsonl 的记录不带时间。
// 用户消息按 prompt_index 对上 user_message_chunk；agent 的回复按开头对上 agent_message_chunk；
// 工具调用和结果按调用 ID 对上 tool_call。
type grokTimes struct {
	prompts map[int]int64
	chunks  []grokChunk
	next    int // chunks 里下一条还没对上的
	tools   map[string]int64
}

type grokChunk struct {
	text string
	at   int64
}

func (grokParser) prepare(transcript string, st *fileState) {
	f, err := os.Open(filepath.Join(filepath.Dir(transcript), "updates.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	gt := &grokTimes{prompts: map[int]int64{}, tools: map[string]int64{}}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), maxLine)
	for sc.Scan() {
		var r struct {
			Timestamp int64 `json:"timestamp"`
			Params    struct {
				Update struct {
					SessionUpdate string `json:"sessionUpdate"`
					ToolCallID    string `json:"toolCallId"`
					Content       struct {
						Text string `json:"text"`
					} `json:"content"`
					Meta struct {
						PromptIndex *int `json:"promptIndex"`
					} `json:"_meta"`
				} `json:"update"`
			} `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &r) != nil || r.Timestamp == 0 {
			continue
		}
		at := r.Timestamp * 1000
		u := r.Params.Update
		switch u.SessionUpdate {
		case "user_message_chunk":
			if pi := u.Meta.PromptIndex; pi != nil {
				if _, ok := gt.prompts[*pi]; !ok {
					gt.prompts[*pi] = at
				}
			}
		case "agent_message_chunk":
			if t := strings.TrimSpace(u.Content.Text); t != "" {
				gt.chunks = append(gt.chunks, grokChunk{t, at})
			}
		case "tool_call":
			if id := grokCallID(u.ToolCallID); id != "" {
				gt.tools[id] = at
			}
		}
	}
	st.grok = gt
}

// grokCallID 取调用 ID 里 "call-" 起的部分：updates 里可能带 "ws_<uuid>_" 这样的前缀。
func grokCallID(id string) string {
	if i := strings.Index(id, "call-"); i >= 0 {
		return id[i:]
	}
	return id
}

// at 找一条记录的时间，找不到返回 0。
func (gt *grokTimes) at(r grokRecord) int64 {
	if gt == nil {
		return 0
	}
	switch r.Type {
	case "user":
		if r.PromptIndex != nil {
			return gt.prompts[*r.PromptIndex]
		}
	case "assistant":
		if t := strings.TrimSpace(textOf(r.Content)); t != "" {
			for i := gt.next; i < len(gt.chunks); i++ {
				if strings.HasPrefix(t, gt.chunks[i].text) || strings.HasPrefix(gt.chunks[i].text, t) {
					gt.next = i + 1
					return gt.chunks[i].at
				}
			}
		}
		for _, c := range r.ToolCalls {
			if at := gt.tools[grokCallID(c.ID)]; at != 0 {
				return at
			}
		}
	case "tool_result":
		return gt.tools[grokCallID(r.ToolCallID)]
	}
	return 0
}

func (grokParser) line(raw []byte, st *fileState) []Message {
	var r grokRecord
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	if r.ModelID != "" {
		st.Model = r.ModelID
	}
	at := st.grok.at(r)
	if at == 0 {
		at = st.LastAt
	} else {
		st.LastAt = at
	}
	switch r.Type {
	case "user":
		// 用户消息之外，grok 还以用户身份注入环境、规则、技能列表，真正的输入在 <user_query> 里
		var parts []string
		for _, m := range grokQuery.FindAllStringSubmatch(textOf(r.Content), -1) {
			parts = append(parts, m[1])
		}
		if t := userText(st, strings.Join(parts, "\n")); t != "" {
			return []Message{msg("user", t, at)}
		}
	case "assistant":
		var out []Message
		if t := strings.TrimSpace(textOf(r.Content)); t != "" {
			out = append(out, msg("assistant", t, at))
		}
		for _, c := range r.ToolCalls {
			out = append(out, msg("tool", toolCall(c.Name, json.RawMessage(c.Arguments)), at))
		}
		return out
	case "tool_result":
		if t := clip(textOf(r.Content), toolClip); t != "" {
			return []Message{msg("tool", t, at)}
		}
	case "backend_tool_call":
		if q := r.Kind.Action.Query; q != "" {
			return []Message{msg("tool", r.Kind.ToolType+" "+q, at)}
		}
	}
	return nil
}

type grokSummary struct {
	Info struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"info"`
	SessionSummary string `json:"session_summary"`
	GeneratedTitle string `json:"generated_title"`
	HeadBranch     string `json:"head_branch"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
	LastActiveAt   string `json:"last_active_at"`
}

func (grokParser) meta(path string, st *fileState) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var s grokSummary
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s.Info.ID != "" {
		st.SessionID = s.Info.ID
	}
	st.Cwd = s.Info.Cwd
	if st.Cwd == "" {
		// 目录名是会话所在目录的 URL 编码
		if dir, err := url.PathUnescape(filepath.Base(filepath.Dir(filepath.Dir(path)))); err == nil {
			st.Cwd = dir
		}
	}
	switch {
	case s.GeneratedTitle != "":
		st.Title = s.GeneratedTitle
	case s.SessionSummary != "":
		st.Title = s.SessionSummary
	}
	if s.HeadBranch != "" {
		st.Branch = s.HeadBranch
	}
	st.Created = parseTime(s.CreatedAt)
	st.Updated = max(parseTime(s.UpdatedAt), parseTime(s.LastActiveAt))
	return nil
}

// --- codex 标题

type codexIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
}

// applyCodexTitles 把 session_index.jsonl 里的会话名写到 codex 会话上；同一会话后出现的名字为准。
func (x *Index) applyCodexTitles(ctx context.Context, r Roots) error {
	path := filepath.Join(r.Home, ".codex", "session_index.jsonl")
	mtime := mtimeOf(path)
	if mtime == 0 {
		return nil
	}
	var seen string
	x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='codex_titles_mtime'`).Scan(&seen)
	// 新会话入库时也要能拿到标题，所以除了文件变化，还要看有没有没标题的 codex 会话
	var untitled int
	x.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions WHERE agent='codex' AND title=''`).Scan(&untitled)
	if seen == strconv.FormatInt(mtime, 10) && untitled == 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	titles := map[string]string{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e codexIndexEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" && e.ThreadName != "" {
			titles[e.ID] = e.ThreadName
		}
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for sid, title := range titles {
		var id int64
		var skip bool
		var state string
		err := tx.QueryRowContext(ctx, `SELECT id, state FROM sessions WHERE agent='codex' AND session_id=? AND title<>?`, sid, title).Scan(&id, &state)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		skip = strings.Contains(state, `"skip":true`)
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET title=? WHERE id=?`, title, id); err != nil {
			return err
		}
		if err := setTitle(ctx, tx, id, title, "", skip); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('codex_titles_mtime',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		strconv.FormatInt(mtime, 10)); err != nil {
		return err
	}
	return tx.Commit()
}
