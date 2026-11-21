package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// AntigravityBase 是 agy 的数据目录。
func AntigravityBase(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

var antigravityUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// AntigravityOpenSession 从 agy 进程打开着的文件认出它正在用的会话：agy 运行期间一直持有
// presence/<UUID>.lock。认不出或同时持有几个时返回空，不猜。
func AntigravityOpenSession(home string, files []string) string {
	dir := filepath.Join(AntigravityBase(home), "presence")
	// 内核给的是解开符号链接后的路径
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	id := ""
	for _, f := range files {
		if d := filepath.Dir(f); d != dir && d != resolved || filepath.Ext(f) != ".lock" {
			continue
		}
		name := strings.TrimSuffix(filepath.Base(f), ".lock")
		if !antigravityUUID.MatchString(name) || id != "" && id != name {
			return ""
		}
		id = name
	}
	return id
}

// AntigravityTitle 返回 agy 给会话起的标题（首轮回复后写进 conversation_summaries.db，
// 摘要缓存要更晚才有），没有时返回空。
func AntigravityTitle(home, id string) string {
	db := openAntigravitySummaries(AntigravityBase(home))
	if db == nil {
		return ""
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var title string
	if db.QueryRowContext(ctx, `SELECT title FROM conversation_summaries WHERE conversation_id = ?`, id).Scan(&title) != nil {
		return ""
	}
	return strings.TrimSpace(title)
}

func openAntigravitySummaries(base string) *sql.DB {
	path := filepath.Join(base, "conversation_summaries.db")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(100)"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil
	}
	return db
}

// 元数据缓存是 CLI 的摘要快照，原生 SQLite 里还有 protobuf；只读缓存，不改写原生存储。
type antigravityMetadata struct {
	Conversations map[string]struct {
		Internal bool `json:"is_internal"`
		Summary  struct {
			ID                   string
			Title                string
			WorkspaceURIs        []string
			UpdatedAt            string
			Internal             bool
			ParentConversationID string
		} `json:"summary"`
	} `json:"conversations"`
}

type antigravityRecord struct {
	Step        int    `json:"step_index"`
	Type        string `json:"type"`
	Source      string `json:"source"`
	Status      string `json:"status"`
	Created     string `json:"created_at"`
	Content     string `json:"content"`
	Thinking    string `json:"thinking"`
	Model       string `json:"model"`
	InputTokens int    `json:"input_tokens"`
	CacheTokens int    `json:"cache_read_tokens"`
	ToolCalls   []struct {
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"args"`
	} `json:"tool_calls"`
	Media []struct {
		URI  string `json:"uri"`
		MIME string `json:"mime_type"`
	} `json:"media"`
	Truncated []string `json:"truncated_fields"`
}

type antigravityParser struct {
	id   string
	full *antigravityFull
}

var antigravityTypes = quoted("USER_INPUT", "PLANNER_RESPONSE", "GENERIC")
var antigravityRequest = regexp.MustCompile(`(?s)<USER_REQUEST>\s*(.*?)\s*</USER_REQUEST>`)
var antigravityMetadataTags = regexp.MustCompile(`(?s)<(?:ADDITIONAL_METADATA|USER_SETTINGS_CHANGE)>.*?</(?:ADDITIONAL_METADATA|USER_SETTINGS_CHANGE)>`)

func (antigravityParser) wants(raw []byte, _ *fileState) bool {
	return hasFieldValue(raw, typeKey, antigravityTypes)
}

func antigravityUserText(content string) string {
	if matches := antigravityRequest.FindAllStringSubmatch(content, -1); len(matches) > 0 {
		var parts []string
		for _, m := range matches {
			parts = append(parts, m[1])
		}
		return strings.TrimSpace(strings.Join(parts, "\n"))
	}
	return strings.TrimSpace(antigravityMetadataTags.ReplaceAllString(content, ""))
}

// args 的值是 JSON 编码后的字符串（数字、对象也如此），需要再解一层。
func antigravityArgs(args map[string]json.RawMessage) json.RawMessage {
	out := make(map[string]json.RawMessage, len(args))
	for k, v := range args {
		var s string
		if json.Unmarshal(v, &s) == nil && json.Valid([]byte(s)) {
			v = json.RawMessage(s)
		}
		out[k] = v
	}
	b, _ := json.Marshal(out)
	return b
}

func (p antigravityParser) line(raw []byte, st *fileState) []Message {
	var r antigravityRecord
	if json.Unmarshal(raw, &r) != nil || !p.wants(raw, st) {
		return nil
	}
	if p.full != nil && len(r.Truncated) > 0 {
		raw = p.full.line(raw)
		json.Unmarshal(raw, &r)
	}
	if p.id != "" {
		st.SessionID = p.id
	}
	at := parseTime(r.Created)
	touch(st, at)
	if r.Model != "" {
		st.Model = r.Model
	}
	var out []Message
	switch r.Type {
	case "USER_INPUT":
		if r.Source != "" && r.Source != "USER_EXPLICIT" {
			return nil
		}
		if text := userText(st, antigravityUserText(r.Content)); text != "" {
			out = append(out, msg("user", text, at))
		}
	case "PLANNER_RESPONSE":
		out = append(out, thinkMsg(r.Thinking, at)...)
		if text := strings.TrimSpace(r.Content); text != "" {
			out = append(out, msg("assistant", text, at))
		}
		for _, call := range r.ToolCalls {
			out = append(out, msg("tool", toolCall(call.Name, antigravityArgs(call.Args)), at))
		}
	case "GENERIC":
		if text := clip(r.Content, toolClip); text != "" {
			out = append(out, msg("tool", text, at))
		}
	}
	return out
}

func (p antigravityParser) meta(path string, st *fileState) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var data antigravityMetadata
	if err = json.Unmarshal(b, &data); err != nil {
		return err
	}
	st.SessionID = p.id
	if m, ok := data.Conversations[p.id]; ok {
		if m.Internal || m.Summary.Internal || (m.Summary.ID != "" && m.Summary.ID != p.id) {
			st.Skip = true
			return nil
		}
		if m.Summary.ParentConversationID != "" && m.Summary.ParentConversationID != p.id {
			st.Parent = m.Summary.ParentConversationID
		}
		st.Title = m.Summary.Title
		if len(m.Summary.WorkspaceURIs) > 0 {
			st.Cwd = antigravityFileURI(m.Summary.WorkspaceURIs[0])
		}
		touch(st, parseTime(m.Summary.UpdatedAt))
	}
	return nil
}

func antigravityFileURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || !filepath.IsAbs(u.Path) {
		return ""
	}
	return filepath.Clean(u.Path)
}

// AntigravityBrainDir 核对 transcript 的固定层级，避免把任意父目录当成会话目录。
func AntigravityBrainDir(path string) string {
	if filepath.Base(path) != "transcript.jsonl" || filepath.Base(filepath.Dir(path)) != "logs" || filepath.Base(filepath.Dir(filepath.Dir(path))) != ".system_generated" {
		return ""
	}
	dir := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if filepath.Base(filepath.Dir(dir)) != "brain" {
		return ""
	}
	return dir
}

// 完整日志只在遇到截断字段时打开。一次读取建立偏移表，避免为每行反复扫描全文。
type antigravityFull struct {
	path    string
	offsets map[int][2]int64
}

func (f *antigravityFull) line(raw []byte) []byte {
	var rec antigravityRecord
	if f == nil || json.Unmarshal(raw, &rec) != nil || len(rec.Truncated) == 0 {
		return raw
	}
	path := filepath.Join(filepath.Dir(f.path), "transcript_full.jsonl")
	file, err := os.Open(path)
	if err != nil {
		return raw
	}
	defer file.Close()
	if f.offsets == nil {
		f.offsets = map[int][2]int64{}
		scanLines(file, func(line []byte, end int64) {
			var h struct {
				Step int `json:"step_index"`
			}
			if json.Unmarshal(line, &h) == nil {
				f.offsets[h.Step] = [2]int64{end - int64(len(line)), int64(len(line))}
			}
		})
	}
	span, ok := f.offsets[rec.Step]
	if !ok || span[1] > maxLine {
		return raw
	}
	b := make([]byte, span[1])
	if _, err = file.ReadAt(b, span[0]); err != nil {
		return raw
	}
	var full antigravityRecord
	if json.Unmarshal(b, &full) != nil || full.Step != rec.Step || full.Type != rec.Type || full.Created != rec.Created {
		return raw
	}
	return b
}

func decodeAntigravityLine(raw []byte, off int64) *ChatMessage {
	var r antigravityRecord
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	var blocks []ChatBlock
	role := "assistant"
	switch r.Type {
	case "USER_INPUT":
		if r.Source != "" && r.Source != "USER_EXPLICIT" {
			return nil
		}
		role = "user"
		if text := antigravityUserText(r.Content); text != "" {
			blocks = append(blocks, ChatBlock{Type: "text", Text: text})
		}
	case "PLANNER_RESPONSE":
		if text := strings.TrimSpace(r.Thinking); text != "" {
			blocks = append(blocks, ChatBlock{Type: "reasoning", Text: text})
		}
		if text := strings.TrimSpace(r.Content); text != "" {
			blocks = append(blocks, ChatBlock{Type: "text", Text: text})
		}
		for _, call := range r.ToolCalls {
			blocks = append(blocks, ChatBlock{Type: "tool-call", Name: call.Name, Input: antigravityArgs(call.Args)})
		}
	case "GENERIC":
		role = "tool"
		if r.Content != "" {
			blocks = append(blocks, ChatBlock{Type: "tool-result", Output: r.Content, IsError: r.Status == "ERROR"})
		}
	default:
		return nil
	}
	for _, m := range r.Media {
		if strings.HasPrefix(m.MIME, "image/") {
			if path := antigravityFileURI(m.URI); path != "" {
				blocks = append(blocks, ChatBlock{Type: "image", Path: path})
			}
		}
	}
	if len(blocks) == 0 {
		return nil
	}
	if role == "assistant" && onlyReasoning(blocks) {
		role = "reasoning"
	}
	return &ChatMessage{ID: "antigravity-" + strconv.Itoa(r.Step), Role: role, Blocks: blocks, Timestamp: stamp(parseTime(r.Created)), Source: "antigravity", Offset: off}
}

func onlyReasoning(blocks []ChatBlock) bool {
	for _, b := range blocks {
		if b.Type != "reasoning" {
			return false
		}
	}
	return true
}

func antigravityContext(line []byte) (Context, bool) {
	var r antigravityRecord
	if json.Unmarshal(line, &r) != nil || r.Type != "PLANNER_RESPONSE" {
		return Context{}, false
	}
	used := r.InputTokens + r.CacheTokens
	// CLI 不落盘模型窗口大小，保留用量，界面不推测百分比。
	return Context{Used: used}, used > 0
}

func (s *ImageScanner) antigravityLine(raw []byte, offset int64) {
	var r antigravityRecord
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	at, _ := time.Parse(time.RFC3339Nano, r.Created)
	for i, m := range r.Media {
		if path := antigravityFileURI(m.URI); path != "" && strings.HasPrefix(m.MIME, "image/") {
			s.add([]ToolImage{{Path: path, Media: m.MIME, At: at, Index: i, OnDisk: true, Pasted: r.Type == "USER_INPUT"}}, offset)
		}
	}
}

// agy 的工具结果是紧跟在调用后面的 GENERIC 记录，一次调几个工具时按顺序各占一步；
// 结果记录没有调用 id，按步号对应。出错的结果 status 是 ERROR。
func (s *UsageScanner) antigravityLine(raw []byte) {
	var r antigravityRecord
	if json.Unmarshal(raw, &r) != nil {
		return
	}
	if r.Type == "GENERIC" {
		if r.Status == "ERROR" {
			s.mcpResult(strconv.Itoa(r.Step), true)
		}
		return
	}
	if r.Type != "PLANNER_RESPONSE" {
		return
	}
	s.at, _ = time.Parse(time.RFC3339Nano, r.Created)
	for i, call := range r.ToolCalls {
		args := antigravityArgs(call.Args)
		var a struct {
			Path        string
			CommandLine string
			Query       string          `json:"query"`
			ServerName  string          // call_mcp_tool
			ToolName    string          // call_mcp_tool
			Arguments   json.RawMessage // call_mcp_tool
		}
		json.Unmarshal(args, &a)
		switch call.Name {
		case "read_file":
			s.skillRead(a.Path, "")
		case "run_command":
			s.command(a.CommandLine)
		case "search_web":
			s.addSearch(a.Query, call.Name)
		case "call_mcp_tool":
			s.mcpCall(strconv.Itoa(r.Step+1+i), a.ServerName, a.ToolName)
			s.toolURL(a.Arguments, a.ServerName+"."+a.ToolName)
		}
	}
}
