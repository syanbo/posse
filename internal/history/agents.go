package history

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
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
	// cursor 的标题、目录和时间在 ~/.cursor/chats/<md5(目录)>/<会话>/meta.json。
	// 会话 ID 会跨目录重复（本机实测有不同目录、不同标题的两个会话同一个 ID），所以一个 ID 可能有几份
	cursorMeta := map[string][]string{}
	for _, m := range globFiles(filepath.Join(r.Home, ".cursor", "chats", "*", "*", "meta.json")) {
		id := filepath.Base(filepath.Dir(m))
		cursorMeta[id] = append(cursorMeta[id], m)
	}
	// 还原目录要逐段 stat，只在文件真要重读、记录里又没有目录时，或者要在同 ID 的几份 meta 里挑时才做，同一项目只做一次
	cursorDirs := map[string]string{}
	dirOf := func(project string) string {
		dir, ok := cursorDirs[project]
		if !ok {
			dir = cursorProjectDir(project)
			cursorDirs[project] = dir
		}
		return dir
	}
	for _, p := range globFiles(filepath.Join(r.Home, ".cursor", "projects", "*", "agent-transcripts", "*", "*.jsonl")) {
		project := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(p))))
		src := source{agent: "cursor", transcript: p, parser: cursorParser{}}
		switch metas := cursorMeta[sessionIDFromPath(p)]; len(metas) {
		case 0:
		case 1:
			src.meta = metas[0]
		default:
			// 挑 chats 下目录名是这个项目目录 md5 的那份；还原不出目录就都不用，免得拿别的会话的标题
			if dir := dirOf(project); dir != "" {
				sum := md5.Sum([]byte(dir))
				for _, m := range metas {
					if filepath.Base(filepath.Dir(filepath.Dir(m))) == hex.EncodeToString(sum[:]) {
						src.meta = m
					}
				}
			}
		}
		if src.meta == "" {
			src.cwd = func() string { return dirOf(project) }
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

// toolArgs 是工具参数里用得上的字段。cmd、command 在 codex 里可能是参数数组，先留原文。
type toolArgs struct {
	Command     json.RawMessage `json:"command"`
	Cmd         json.RawMessage `json:"cmd"`
	FilePath    string          `json:"file_path"`
	Path        string          `json:"path"`
	TargetFile  string          `json:"target_file"`
	Pattern     string          `json:"pattern"`
	Query       string          `json:"query"`
	URL         string          `json:"url"`
	Description string          `json:"description"`
	Prompt      string          `json:"prompt"`
}

// decodeToolArgs 解析工具参数。某个字段类型不符（比如 path 是对象）只丢这个字段，
// 其余照常取；不是合法 JSON 时 ok 为假。
func decodeToolArgs(raw []byte) (a toolArgs, ok bool) {
	err := json.Unmarshal(raw, &a)
	var typeErr *json.UnmarshalTypeError
	return a, err == nil || errors.As(err, &typeErr)
}

// commandOf 取工具参数里的命令：cmd、command 可能是字符串，也可能是参数数组。
func commandOf(args []byte) string {
	a, ok := decodeToolArgs(args)
	if !ok {
		return ""
	}
	for _, raw := range []json.RawMessage{a.Cmd, a.Command} {
		if s := rawString(raw); s != "" {
			return s
		}
		var list []string
		if len(raw) > 0 && raw[0] == '[' && json.Unmarshal(raw, &list) == nil && len(list) > 0 {
			// ["bash", "-lc", "cat …"] 取最后一段
			return list[len(list)-1]
		}
	}
	return ""
}

// rawString 取 JSON 字符串的值；不是字符串返回 ""。
func rawString(raw json.RawMessage) string {
	var s string
	if len(raw) > 0 && raw[0] == '"' && json.Unmarshal(raw, &s) == nil {
		return s
	}
	return ""
}

// toolCall 把工具调用压成一行"名称 参数"，参数优先取命令、路径这类可搜的字段。
func toolCall(name string, input json.RawMessage) string {
	if a, ok := decodeToolArgs(input); ok {
		for _, v := range []string{rawString(a.Command), rawString(a.Cmd), a.FilePath, a.Path, a.TargetFile,
			a.Pattern, a.Query, a.URL, a.Description, a.Prompt} {
			if v != "" {
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

// contentBlock 是 claude、cursor 消息内容里的一块。
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Content   json.RawMessage `json:"content"`
	ID        string          `json:"id"`          // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result 对应的调用
	IsError   bool            `json:"is_error"`
}

// contentBlocks 解析字符串或块数组形式的消息内容，字符串当成一个 text 块；两者都不是时 ok 为假。
// 按首字节分派，免得每条内容先按字符串试一遍失败再按数组解析。
func contentBlocks(raw json.RawMessage) (blocks []contentBlock, ok bool) {
	raw = bytes.TrimLeft(raw, " \t\r\n")
	if len(raw) == 0 {
		return nil, false
	}
	switch raw[0] {
	case '"':
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil, false
		}
		return []contentBlock{{Type: "text", Text: s}}, true
	case '[':
		if json.Unmarshal(raw, &blocks) != nil {
			return nil, false
		}
		return blocks, true
	case 'n':
		// null 和空字符串一样，没有内容
		return nil, string(raw) == "null"
	}
	return nil, false
}

// textOf 取字符串或 [{type:text,text}] 数组里的文字。
func textOf(raw json.RawMessage) string {
	blocks, _ := contentBlocks(raw)
	var b []string
	for _, p := range blocks {
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
	Attachment claudeAttachment `json:"attachment"`
}

// claudeAttachment 是 type 为 attachment 的记录。用户在 agent 工作时插进来的话记成 queued_command，
// 不是 type 为 user 的记录；后台任务通知、子 agent 回报也是 queued_command，但来源不是 human。
type claudeAttachment struct {
	Type        string          `json:"type"`
	Prompt      json.RawMessage `json:"prompt"`
	CommandMode string          `json:"commandMode"`
	Origin      struct {
		Kind string `json:"kind"`
	} `json:"origin"`
}

// humanPrompt 返回用户插进来的话（字符串或内容块数组），不是就返回 nil。
func (a claudeAttachment) humanPrompt() json.RawMessage {
	if a.Type != "queued_command" || a.Origin.Kind != "human" || a.CommandMode != "prompt" {
		return nil
	}
	return a.Prompt
}

var (
	claudeCommandName = regexp.MustCompile(`(?s)<command-name>(.*?)</command-name>`)
	claudeCommandArgs = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
)

// claudeTypes 是 claudeParser.line 会处理的记录类型。
var claudeTypes = quoted("user", "assistant", "custom-title", "ai-title", "summary")

var (
	claudeAttachmentType = quoted("attachment")
	claudeQueuedCommand  = []byte(`"queued_command"`)
)

func (claudeParser) wants(raw []byte, _ *fileState) bool {
	return hasFieldValue(raw, typeKey, claudeTypes) ||
		(hasFieldValue(raw, typeKey, claudeAttachmentType) && bytes.Contains(raw, claudeQueuedCommand))
}

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
	case "attachment":
		prompt := r.Attachment.humanPrompt()
		if prompt == nil {
			return nil
		}
		r.Type, r.Message.Content = "user", prompt
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
	blocks, ok := contentBlocks(r.Message.Content)
	if !ok {
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
		m := claudeCommandName.FindStringSubmatch(t)
		if m == nil {
			return ""
		}
		cmd := strings.TrimSpace(m[1])
		if a := claudeCommandArgs.FindStringSubmatch(t); a != nil {
			cmd = strings.TrimSpace(cmd + " " + strings.TrimSpace(a[1]))
		}
		return userText(st, cmd)
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

// codexParser.line 会处理的记录类型：两种格式都要的元数据，加上各自格式读消息的那一类
// （新格式的 item_completed 是 payload 里的 type）。
var (
	codexPagedTypes  = quoted("session_meta", "turn_context", "item_completed")
	codexLegacyTypes = quoted("session_meta", "turn_context", "response_item")
)

func (codexParser) wants(raw []byte, st *fileState) bool {
	if st.Mode == "paginated" {
		return hasFieldValue(raw, typeKey, codexPagedTypes)
	}
	return hasFieldValue(raw, typeKey, codexLegacyTypes)
}

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

var typeKey = []byte(`"type"`)

func quoted(values ...string) [][]byte {
	out := make([][]byte, len(values))
	for i, v := range values {
		out[i] = []byte(`"` + v + `"`)
	}
	return out
}

// hasFieldValue 粗筛一行 JSON：是否有某个 key 字段（带引号）的值是 values 之一（带引号）。
// 冒号两侧可能有空格，所以不能把键和值拼成一个串去找。嵌套对象里的同名字段也算，宁多勿漏：
// 漏掉的行不会进索引，多放过的只是照常解析。只扫一遍，比逐个值 bytes.Contains 省。
func hasFieldValue(raw, key []byte, values [][]byte) bool {
	for {
		i := bytes.Index(raw, key)
		if i < 0 {
			return false
		}
		raw = raw[i+len(key):]
		rest := bytes.TrimLeft(raw, " \t\r\n")
		if len(rest) == 0 || rest[0] != ':' {
			continue
		}
		rest = bytes.TrimLeft(rest[1:], " \t\r\n")
		for _, v := range values {
			if bytes.HasPrefix(rest, v) {
				return true
			}
		}
	}
}

func containsAny(b []byte, subs [][]byte) bool {
	for _, sub := range subs {
		if bytes.Contains(b, sub) {
			return true
		}
	}
	return false
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
		Content json.RawMessage `json:"content"`
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
	blocks, _ := contentBlocks(r.Message.Content)
	var out []Message
	for _, b := range blocks {
		switch {
		case b.Type == "text" && r.Role == "user":
			text, at := cursorUserText(b.Text)
			touch(st, at)
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

// cursorUserText 取出用户消息里真正说的话（<user_query> 里的，没有就去掉 <timestamp>）和时间。
// cursor 记录里没有时间字段，用户消息开头的 <timestamp> 是唯一的时间来源，没有时 at 为 0。
func cursorUserText(raw string) (text string, at int64) {
	if m := cursorTimestamp.FindStringSubmatch(raw); m != nil {
		at = cursorTime(m[1])
	}
	if qs := cursorQuery.FindAllStringSubmatch(raw, -1); qs != nil {
		var parts []string
		for _, q := range qs {
			parts = append(parts, q[1])
		}
		return strings.Join(parts, "\n"), at
	}
	return cursorTimestamp.ReplaceAllString(raw, ""), at
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

// grokToolCall 是 chat_history.jsonl 里助手记录发起的一次工具调用。
type grokToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type grokRecord struct {
	Type        string          `json:"type"`
	Content     json.RawMessage `json:"content"`
	PromptIndex *int            `json:"prompt_index"`
	ModelID     string          `json:"model_id"`
	ToolCallID  string          `json:"tool_call_id"`
	ToolCalls   []grokToolCall  `json:"tool_calls"`
	Synthetic   string          `json:"synthetic_reason"`
	Kind        struct {
		ToolType string `json:"tool_type"`
		Action   struct {
			Query string `json:"query"`
		} `json:"action"`
	} `json:"kind"`
}

var grokQuery = regexp.MustCompile(`(?s)<user_query>(.*?)</user_query>`)

// grokTypes 是 grokParser.line 会处理的记录类型；别的记录只可能带模型。
var (
	grokTypes   = quoted("user", "assistant", "tool_result", "backend_tool_call")
	grokModelID = []byte(`"model_id"`)
)

func (grokParser) wants(raw []byte, _ *fileState) bool {
	return hasFieldValue(raw, typeKey, grokTypes) || bytes.Contains(raw, grokModelID)
}

// grokTimes 是从同目录 updates.jsonl 建的消息时间表：chat_history.jsonl 的记录不带时间。
// 用户消息按 prompt_index 对上 user_message_chunk；agent 的回复按开头对上 agent_message_chunk；
// 工具调用和结果按调用 ID 对上 tool_call。
type grokTimes struct {
	prompts map[int]int64
	chunks  []grokChunk
	next    int // chunks 里下一条还没对上的
	tools   map[string]int64
	// matched 是最后一个对上的回复分段在 updates.jsonl 里的行尾位置
	matched int64
}

type grokChunk struct {
	text    string
	at, end int64 // end 是所在行的行尾位置
}

// grokUpdateTypes 先粗筛 updates.jsonl：大部分字节是工具输出（tool_call_update）和思考过程，用不上。
var (
	grokUpdateKey   = []byte(`"sessionUpdate"`)
	grokUpdateTypes = quoted("user_message_chunk", "agent_message_chunk", "tool_call")
)

// prepare 从上次最后对上的回复分段之后读 updates.jsonl 建时间表：回复按顺序对上分段，
// 这样和整份读时的 next 一致，新回复不会对上已经用过的分段。之前的用户消息和工具调用
// 都已经对上过，不用再读。
func (grokParser) prepare(transcript string, st *fileState) {
	f, err := os.Open(filepath.Join(filepath.Dir(transcript), "updates.jsonl"))
	if err != nil {
		return
	}
	defer f.Close()
	from := st.GrokUpdates
	if fi, err := f.Stat(); err != nil || fi.Size() < from {
		from = 0
	}
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return
	}
	gt := &grokTimes{prompts: map[int]int64{}, tools: map[string]int64{}, matched: from}
	scanLines(f, func(line []byte, n int64) {
		if !hasFieldValue(line, grokUpdateKey, grokUpdateTypes) {
			return
		}
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
		if json.Unmarshal(line, &r) != nil || r.Timestamp == 0 {
			return
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
				gt.chunks = append(gt.chunks, grokChunk{t, at, from + n})
			}
		case "tool_call":
			if id := grokCallID(u.ToolCallID); id != "" {
				gt.tools[id] = at
			}
		}
	})
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
				if c := gt.chunks[i]; strings.HasPrefix(t, c.text) || strings.HasPrefix(c.text, t) {
					gt.next, gt.matched = i+1, c.end
					return c.at
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
	if st.grok != nil {
		st.GrokUpdates = st.grok.matched
	}
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

// codexTitles 是 session_index.jsonl 里的会话名，同一会话后出现的名字为准。
type codexTitles struct {
	mtime  int64
	titles map[string]string
}

// loadCodexTitles 读 session_index.jsonl，文件没变时沿用上次读的。
func (x *Index) loadCodexTitles(r Roots) codexTitles {
	path := filepath.Join(r.Home, ".codex", "session_index.jsonl")
	mtime := mtimeOf(path)
	if mtime == x.codex.mtime && x.codex.titles != nil {
		return x.codex
	}
	ct := codexTitles{mtime: mtime, titles: map[string]string{}}
	f, err := os.Open(path)
	if err != nil {
		x.codex = ct
		return ct
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e codexIndexEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.ID != "" && e.ThreadName != "" {
			ct.titles[e.ID] = e.ThreadName
		}
	}
	x.codex = ct
	return ct
}

// applyCodexTitles 在 session_index.jsonl 变化后把会话名写到已入库的 codex 会话上。
// 会话文件有新内容时，updateFile 在保存前就带上了名字，这里只管只改了名的会话。
func (x *Index) applyCodexTitles(ctx context.Context, r Roots) error {
	path := filepath.Join(r.Home, ".codex", "session_index.jsonl")
	mtime := mtimeOf(path)
	if mtime == 0 {
		return nil
	}
	var seen string
	x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key='codex_titles_mtime'`).Scan(&seen)
	if seen == strconv.FormatInt(mtime, 10) {
		return nil
	}
	titles := x.loadCodexTitles(r).titles
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// 跳过的会话 messages 记 0，不进标题索引，也就不用改名
	rows, err := tx.QueryContext(ctx, `SELECT id, session_id, title FROM sessions WHERE agent='codex' AND messages > 0`)
	if err != nil {
		return err
	}
	type rename struct {
		id    int64
		title string
	}
	var todo []rename
	for rows.Next() {
		var id int64
		var sid, title string
		if err := rows.Scan(&id, &sid, &title); err != nil {
			rows.Close()
			return err
		}
		if t := titles[sid]; t != "" && t != title {
			todo = append(todo, rename{id, t})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(todo) > 0 {
		upd, err := tx.PrepareContext(ctx, `UPDATE sessions SET title=? WHERE id=?`)
		if err != nil {
			return err
		}
		defer upd.Close()
		for _, t := range todo {
			if _, err := upd.ExecContext(ctx, t.title, t.id); err != nil {
				return err
			}
			if err := setTitle(ctx, tx, t.id, t.title, "", false); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('codex_titles_mtime',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
		strconv.FormatInt(mtime, 10)); err != nil {
		return err
	}
	return tx.Commit()
}
