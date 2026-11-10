package history

import (
	"bytes"
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

// CLIUsage 是一个命令行程序在几条命令里出现过，Source 见 SourceHomebrew 等。
type CLIUsage struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
	Count  int    `json:"count"`
}

// Usage 是一个会话用到的技能、MCP 工具、命令行程序和网址，按次数从多到少。
type Usage struct {
	Skills []UsageItem `json:"skills"`
	MCP    []MCPUsage  `json:"mcp"`
	CLI    []CLIUsage  `json:"cli,omitempty"`
	Web    []WebUsage  `json:"web,omitempty"`
	// Searches 是网页搜索的次数
	Searches int `json:"searches,omitempty"`
	// Events 是逐次的使用记录，按会话文件里的顺序（时间从早到晚），技能和 MCP、命令行程序、网址各留最近 maxUsageEvents 条
	Events []UsageEvent `json:"events,omitempty"`
}

// UsageEvent 是一次使用：技能（Skill 非空）、MCP 工具（Server、Tool）、命令行程序（CLI、Source，
// Command 是它所在的那条命令，截到 maxEventCommand 个字符）、访问网址（URL）或网页搜索（Query），
// 后两种的 Via 是经什么访问的（工具名、程序名，用户发的是 ViaUser）。At 取自那一行记录，
// grok、cursor 的会话文件不带时间，为零值。
type UsageEvent struct {
	At      time.Time `json:"at,omitzero"`
	Skill   string    `json:"skill,omitempty"`
	Server  string    `json:"server,omitempty"`
	Tool    string    `json:"tool,omitempty"`
	CLI     string    `json:"cli,omitempty"`
	Source  string    `json:"source,omitempty"`
	Command string    `json:"command,omitempty"`
	URL     string    `json:"url,omitempty"`
	Query   string    `json:"query,omitempty"`
	Via     string    `json:"via,omitempty"`
	Failed  bool      `json:"failed,omitempty"` // MCP 调用出错或被拒，不计入次数
}

// isWeb 报告这是不是访问网址或网页搜索。
func (e UsageEvent) isWeb() bool { return e.URL != "" || e.Query != "" }

const (
	maxUsageEvents  = 300
	maxEventCommand = 300
)

// UsageScanner 增量读一个会话文件，统计用到的技能和 MCP 工具。不是并发安全的。
//
// 各家记录方式：
//   - claude：用技能（模型调 Skill 或用户输入斜杠命令）都会注入一段 "Base directory for this skill: <目录>"；
//     在后台子 agent 里跑的技能（code-review 这类内置技能）不注入，认 forked 的启动记录（forkedSlash、forkedTool）；
//     MCP 工具名是 mcp__<服务>__<工具>。
//   - codex：用户点名的技能注入成 "<skill><name>…</name>"；MCP 调用是 McpToolCall 条目，同一次调用会记多遍，按 id 去重；
//     执行的命令是 CommandExecution 条目，parsed_cmd 标出了读的文件。
//   - grok：MCP 经 use_tool 调用，tool_name 是 <服务>__<工具>。
//   - cursor：MCP 经 CallDynamicTool 调用，namespace 是服务名（插件带的服务是 plugin-<插件>-<服务>），
//     namespace 为 cursor 的是它自带的工具（WebSearch 等），不算 MCP。
//   - 执行的命令：claude 的 Bash、codex 的 exec_command/shell（新版另有 CommandExecution，和前者同一个 id）、
//     grok 的 run_terminal_command、cursor 的 Shell。一条命令里的每个程序记一次（见 cliPrograms），
//     来源（Homebrew、系统、其他）按本机的安装位置判断。
//   - 网址：WebFetch 这类工具和 MCP 参数里的 url、curl 这类程序命令里的网址（见 webPrograms）、
//     用户自己发的消息里的网址（复用历史索引的解析，去掉注入的内容），按域名计次，本机地址不算；
//     WebSearch、codex 的网页搜索、grok 的 backend_tool_call 记成搜索。
//   - 技能：Claude 只认上面注入的说明，它启用技能不靠读文件。别家是模型自己读 <名字>/SKILL.md
//     （Read、read_file，或 cat、sed 这类读文件的命令；输出接给 grep 这类过滤程序的是在搜，不算）。
//     codex 的会话带着这次可用的技能清单（developer 消息里的 Available skills，路径写成 r1/<名字>/SKILL.md，
//     r1 是前面列的根目录），有清单时读的文件要对得上清单里的一条才算，名字也取清单里的。
//   - MCP 调用出错或被拒不计入次数，时间线上标成失败：claude 看 tool_result 的 is_error，codex 看 McpToolCall
//     的 status，grok 看 tool_result 以 "Failed to call" 开头；cursor 的会话不记结果，认不出。
//     读 MCP 资源（claude 的 ListMcpResourcesTool、ReadMcpResourceTool，codex 的 list/read_mcp_resource）也算那个服务；
//     codex 读资源的结果认不出出错，照算。
type UsageScanner struct {
	agent, path string
	offset      int64
	skills      map[string]int
	mcp         map[string]map[string]int
	cli         map[string]int
	sources     map[string]string // 程序名 → 来源，第一次见到时查
	seen        map[string]bool   // codex 条目的 id
	web         map[string]int    // 域名 → 次数
	searches    int
	tools, cmds []seqEvent         // 技能和 MCP、命令行程序的调用，n 是先后
	webs        []seqEvent         // 访问网址和搜索
	catalog     map[string]string  // codex 这次可用的技能：SKILL.md 的绝对路径 → 名字
	pending     map[string]mcpCall // 发起了、还没看到结果的 MCP 调用：调用 id → 记下的那次
	seq         int
	at          time.Time // 正在解析的那一行的时间
	parser      parser    // 认出用户自己发的消息
	st          fileState
}

func NewUsageScanner(agent, path string) *UsageScanner {
	s := &UsageScanner{agent: agent, path: path}
	switch agent {
	case "claude":
		s.parser = claudeParser{}
	case "codex":
		s.parser = codexParser{}
	case "grok":
		s.parser = grokParser{}
	case "cursor":
		s.parser = cursorParser{}
	}
	s.reset()
	return s
}

func (s *UsageScanner) reset() {
	s.offset = 0
	s.skills, s.mcp, s.seen, s.tools, s.cmds = map[string]int{}, map[string]map[string]int{}, map[string]bool{}, nil, nil
	s.cli, s.sources = map[string]int{}, map[string]string{}
	s.web, s.searches, s.webs, s.st = map[string]int{}, 0, nil, fileState{}
	s.catalog, s.pending = map[string]string{}, map[string]mcpCall{}
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
		// codex 的会话格式写在 session_meta 里，用户消息按格式从不同的记录读
		if bytes.Contains(line, httpMarker) || bytes.Contains(line, codexMetaMarker) {
			s.userURLs(line)
		}
	})
	s.offset += n
	return s.result(), err
}

// usageMarkers 先粗筛，只有可能相关的行才解析 JSON。
var usageMarkers = [][]byte{
	[]byte("Base directory for this skill"), []byte("mcp__"), []byte("<skill>"),
	[]byte("forked-skill-launch"), []byte(`"status":"forked"`), // claude 在后台跑的技能
	[]byte("McpToolCall"), []byte("use_tool"), []byte("SKILL.md"), []byte("CallDynamicTool"),
	[]byte(`"Bash"`), []byte(`"Shell"`), []byte("run_terminal_command"), []byte("exec_command"),
	[]byte(`"shell"`), []byte("CommandExecution"),
	[]byte("WebFetch"), []byte("WebSearch"), []byte("web_search_call"), []byte("backend_tool_call"),
	[]byte(`"web"`), []byte("web.search"), // codex 的 web.run（namespace 是 web）和 Extension 条目
	[]byte(`"is_error":true`), []byte("Failed to call"), // claude、grok 的工具出错结果
	[]byte("McpResource"), []byte("mcp_resource"), // 读 MCP 资源
}

var (
	httpMarker      = []byte("http")
	codexMetaMarker = []byte(`"session_meta"`)
)

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
				Namespace string          `json:"namespace"`
				ToolName  string          `json:"toolName"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if json.Unmarshal(bl.Input, &a) == nil && a.Namespace != "cursor" {
				s.mcpCall("", a.Namespace, a.ToolName)
				s.toolURL(a.Arguments, a.Namespace+"."+a.ToolName)
			} else if a.Namespace == "cursor" {
				s.webTool(a.ToolName, a.Arguments)
			}
		case "Read", "ReadFile":
			a, _ := decodeToolArgs(bl.Input)
			s.skillRead(a.Path, "")
		case "Shell":
			s.command(commandOf(bl.Input))
		default:
			s.webTool(bl.Name, bl.Input)
		}
	}
}

func (s *UsageScanner) claudeLine(b []byte) {
	var r struct {
		Type    string          `json:"type"`
		Subtype string          `json:"subtype"`
		Content json.RawMessage `json:"content"` // 只有 system 记录在这里放字符串
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		ToolUseResult json.RawMessage `json:"toolUseResult"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	if r.Type == "system" && r.Subtype == "local_command" {
		s.forkedSlash(r.Content)
		return
	}
	// api-request-blob 这类记录把发给模型的消息再存一遍（不带时间），只认 user、assistant
	if r.Type != "user" && r.Type != "assistant" {
		return
	}
	s.forkedTool(r.ToolUseResult)
	blocks, _ := contentBlocks(r.Message.Content)
	for _, bl := range blocks {
		switch bl.Type {
		case "text":
			s.skillBase(bl.Text)
		case "tool_result":
			s.mcpResult(bl.ToolUseID, bl.IsError)
		case "tool_use":
			if name, ok := strings.CutPrefix(bl.Name, "mcp__"); ok {
				s.addMCP(bl.ID, name)
				s.toolURL(bl.Input, mcpVia(name))
			} else if tool, ok := claudeResourceTools[bl.Name]; ok {
				s.mcpCall(bl.ID, serverOf(bl.Input), tool)
			} else if bl.Name == "Bash" {
				// Claude 启用技能会注入说明，读 SKILL.md 的命令只当普通命令记
				s.cliCommand(commandOf(bl.Input))
			} else {
				s.webTool(bl.Name, bl.Input)
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

// forkedSlash 认出用户敲斜杠命令启动的后台技能（code-review 这类内置技能在子 agent 里跑，不注入说明），
// 记成一条 local_command：<forked-skill-launch>{"skillName":…}</forked-skill-launch>。
func (s *UsageScanner) forkedSlash(raw json.RawMessage) {
	var content string
	if json.Unmarshal(raw, &content) != nil {
		return
	}
	_, rest, ok := strings.Cut(content, "<forked-skill-launch>")
	if !ok {
		return
	}
	body, _, _ := strings.Cut(rest, "</forked-skill-launch>")
	var l struct {
		SkillName string `json:"skillName"`
	}
	if json.Unmarshal([]byte(body), &l) == nil && l.SkillName != "" {
		s.addSkill(l.SkillName)
	}
}

// forkedTool 认出模型调 Skill 启动的后台技能：工具结果的 toolUseResult 是 {"status":"forked","commandName":…}。
func (s *UsageScanner) forkedTool(raw json.RawMessage) {
	if len(raw) == 0 || raw[0] != '{' {
		return
	}
	var t struct {
		Status      string `json:"status"`
		CommandName string `json:"commandName"`
	}
	if json.Unmarshal(raw, &t) == nil && t.Status == "forked" && t.CommandName != "" {
		s.addSkill(t.CommandName)
	}
}

// addSkill 记一次技能使用。
func (s *UsageScanner) addSkill(name string) {
	s.skills[name]++
	s.event(UsageEvent{Skill: name})
}

// event 记一次调用。技能和 MCP、命令行程序各留最近 maxUsageEvents 次：命令行一刷就是几百条，
// 合在一起留会把技能挤出时间线，只看技能和 MCP 时整段是空的。
func (s *UsageScanner) event(e UsageEvent) {
	e.At = s.at
	s.seq++
	list := &s.tools
	switch {
	case e.CLI != "":
		list = &s.cmds
	case e.isWeb():
		list = &s.webs
	}
	*list = append(*list, seqEvent{s.seq, e})
	if len(*list) > maxUsageEvents*2 {
		*list = slices.Clone((*list)[len(*list)-maxUsageEvents:])
	}
}

type seqEvent struct {
	n int
	e UsageEvent
}

// skillRead 认出读文件的工具读的是某个技能的 SKILL.md。有技能清单（codex）时要对得上清单里的一条，
// 相对路径接在 cwd 后面。
func (s *UsageScanner) skillRead(path, cwd string) {
	path = strings.Trim(path, `'"`)
	if filepath.Base(path) != "SKILL.md" {
		return
	}
	if len(s.catalog) > 0 {
		if name := s.catalogName(path, cwd); name != "" {
			s.addSkill(name)
		}
		return
	}
	if name := filepath.Base(filepath.Dir(path)); name != "" && name != "." && name != "/" {
		s.addSkill(name)
	}
}

// catalogName 是 path 在技能清单里的名字；对不上时解开符号链接再比（技能根目录常是链接，比如 ~/.claude/skills）。
func (s *UsageScanner) catalogName(path, cwd string) string {
	path = expandShellHome(path)
	if !filepath.IsAbs(path) {
		if cwd == "" {
			return ""
		}
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)
	if name, ok := s.catalog[path]; ok {
		return name
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return s.catalog[real]
	}
	return ""
}

// expandShellHome 展开命令里写的 ~、$HOME、$CODEX_HOME，按本机的环境。
func expandShellHome(path string) string {
	home, _ := os.UserHomeDir()
	codex := os.Getenv("CODEX_HOME")
	if codex == "" {
		codex = filepath.Join(home, ".codex")
	}
	for _, v := range []struct{ prefix, dir string }{
		{"~/", home}, {"$HOME/", home}, {"${HOME}/", home}, {"$CODEX_HOME/", codex}, {"${CODEX_HOME}/", codex},
	} {
		if rest, ok := strings.CutPrefix(path, v.prefix); ok {
			return filepath.Join(v.dir, rest)
		}
	}
	return path
}

// skillCmdRe 认出用 cat、sed 这类命令读 SKILL.md；只看命令开头的程序，避免把 grep、写文件里提到的路径算进去。
var skillCmdRe = regexp.MustCompile(`^\s*(?:cat|sed|head|tail|nl|less|bat)\b[^|;&]*?(\S*/SKILL\.md)`)

// searchPrograms 是在输出里找东西的程序：读 SKILL.md 后接给它们是在搜，模型没有读到整个技能。
var searchPrograms = map[string]bool{"grep": true, "egrep": true, "rg": true, "ag": true, "awk": true, "wc": true, "jq": true}

// cmdSepRe 是命令之间的分隔；2>&1 这类重定向里的 & 不算。
var cmdSepRe = regexp.MustCompile(`\|\||&&|;|\|&?`)

// skillCommand 认出 shell 命令读了某个技能；一条命令只算一个。
func (s *UsageScanner) skillCommand(cmd string) {
	loc := skillCmdRe.FindStringSubmatchIndex(cmd)
	if loc == nil {
		return
	}
	rest := cmd[loc[1]:]
	if m := cmdSepRe.FindStringIndex(rest); m != nil && rest[m[0]] == '|' && rest[m[0]:m[1]] != "||" {
		if f := strings.Fields(rest[m[1]:]); len(f) > 0 && searchPrograms[filepath.Base(f[0])] {
			return
		}
	}
	s.skillRead(cmd[loc[2]:loc[3]], "")
}

// command 处理一条 shell 命令：读技能和用到的程序。
func (s *UsageScanner) command(cmd string) {
	s.skillCommand(cmd)
	s.cliCommand(cmd)
}

// cliCommand 记下一条命令运行的程序和访问的网址。
func (s *UsageScanner) cliCommand(cmd string) {
	for _, p := range cliPrograms(cmd) {
		src, ok := s.sources[p.Name]
		if !ok {
			src = cliSource(p.Name, p.Path)
			s.sources[p.Name] = src
		}
		s.cli[p.Name]++
		s.event(UsageEvent{CLI: p.Name, Source: src, Command: clipRunes(cmd, maxEventCommand)})
	}
	s.webCommand(cmd)
}

// webTool 认出 agent 自带的网页工具：WebFetch 记网址，WebSearch 记搜索。
func (s *UsageScanner) webTool(name string, input []byte) {
	switch name {
	case "WebFetch", "web_fetch":
		s.toolURL(input, name)
	case "WebSearch", "web_search":
		var a struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(input, &a) == nil {
			s.addSearch(a.Query, name)
		}
	}
}

// mcpVia 把 <服务>__<工具> 写成 <服务>.<工具>，当网址的来源。
func mcpVia(name string) string {
	if i := strings.LastIndex(name, "__"); i > 0 {
		return name[:i] + "." + name[i+2:]
	}
	return name
}

// addMCP 记一次 MCP 调用，name 是 <服务>__<工具>；服务名里可能有下划线，所以按最后一个 "__" 切。
func (s *UsageScanner) addMCP(id, name string) {
	if i := strings.LastIndex(name, "__"); i > 0 {
		s.mcpCall(id, name[:i], name[i+2:])
	}
}

// maxPending 是最多记着的未见结果的调用：成功的结果行多半被粗筛跳过，不会来删。
const maxPending = 64

// mcpCall 记一次 MCP 调用；id 不为空时记下它，等结果出错再撤回（mcpResult）。
func (s *UsageScanner) mcpCall(id, server, tool string) {
	if server == "" || tool == "" {
		return
	}
	if s.mcp[server] == nil {
		s.mcp[server] = map[string]int{}
	}
	s.mcp[server][tool]++
	s.event(UsageEvent{Server: server, Tool: tool})
	if id == "" {
		return
	}
	if len(s.pending) >= maxPending {
		// 结果紧跟在调用后面，最早的那些已经等不到了
		for k, c := range s.pending {
			if c.seq <= s.seq-maxPending {
				delete(s.pending, k)
			}
		}
	}
	s.pending[id] = mcpCall{server, tool, s.seq}
}

type mcpCall struct {
	server, tool string
	seq          int // 那次调用在时间线里的序号
}

// mcpResult 处理一次工具调用的结果：是记下的 MCP 调用且出错时，撤回它的次数、在时间线上标成失败。
func (s *UsageScanner) mcpResult(id string, failed bool) {
	c, ok := s.pending[id]
	if !ok {
		return
	}
	delete(s.pending, id)
	if !failed {
		return
	}
	if s.mcp[c.server][c.tool]--; s.mcp[c.server][c.tool] <= 0 {
		delete(s.mcp[c.server], c.tool)
		if len(s.mcp[c.server]) == 0 {
			delete(s.mcp, c.server)
		}
	}
	for i := len(s.tools) - 1; i >= 0; i-- {
		if s.tools[i].n == c.seq {
			s.tools[i].e.Failed = true
			break
		}
	}
}

// claudeResourceTools 是 Claude 读 MCP 资源的工具，算成那个服务的调用。
var claudeResourceTools = map[string]string{"ListMcpResourcesTool": "list_resources", "ReadMcpResourceTool": "read_resource"}

// codexResourceTools 同上，是 codex 的。
var codexResourceTools = map[string]string{
	"list_mcp_resources": "list_resources", "list_mcp_resource_templates": "list_resource_templates", "read_mcp_resource": "read_resource",
}

// serverOf 取读资源工具参数里的服务名；列出全部服务的资源时没有服务名，不算。
func serverOf(input []byte) string {
	var a struct {
		Server string `json:"server"`
	}
	json.Unmarshal(input, &a)
	return a.Server
}

var codexSkillRe = regexp.MustCompile(`^<skill>\s*<name>([^<]+)</name>`)

// parsedCmd 是 codex 拆好的一段命令：type 是 read、search、list_files 这类。
type parsedCmd struct {
	Type string `json:"type"`
	Path string `json:"path"`
}

var (
	// codex 技能清单里的根目录别名：- `r1` = `/Users/x/.codex/skills`
	codexRootRe = regexp.MustCompile("(?m)^- `(r\\d+)` = `([^`]+)`")
	// 清单里的一条：- <名字>: <说明> (file: r1/<名字>/SKILL.md)，名字里可能有冒号（插件技能），不会有空格
	codexCatalogRe = regexp.MustCompile(`(?m)^- (\S+): .*\(file: ([^)\s]+/SKILL\.md)\)\s*$`)
)

// codexCatalog 从 developer 消息里读出这次可用的技能清单。
func (s *UsageScanner) codexCatalog(text string) {
	if !strings.Contains(text, "SKILL.md") {
		return
	}
	roots := map[string]string{}
	for _, m := range codexRootRe.FindAllStringSubmatch(text, -1) {
		roots[m[1]] = m[2]
	}
	for _, m := range codexCatalogRe.FindAllStringSubmatch(text, -1) {
		path := m[2]
		if alias, rest, ok := strings.Cut(path, "/"); ok && roots[alias] != "" {
			path = filepath.Join(roots[alias], rest)
		}
		if filepath.IsAbs(path) {
			s.catalog[filepath.Clean(path)] = m[1]
			if real, err := filepath.EvalSymlinks(path); err == nil {
				s.catalog[real] = m[1]
			}
		}
	}
}

func (s *UsageScanner) codexLine(b []byte) {
	var r struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string `json:"type"`
			Role      string `json:"role"`
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
			Content   []struct {
				Text string `json:"text"`
			} `json:"content"`
			Action struct {
				Query string `json:"query"`
				URL   string `json:"url"`
			} `json:"action"`
			Item struct {
				Type      string          `json:"type"`
				Kind      string          `json:"kind"`
				ID        string          `json:"id"`
				Server    string          `json:"server"`
				Tool      string          `json:"tool"`
				Status    string          `json:"status"`
				Arguments json.RawMessage `json:"arguments"`
				Query     string          `json:"query"`
				Command   json.RawMessage `json:"command"`
				Cwd       string          `json:"cwd"`
				ParsedCmd []parsedCmd     `json:"parsed_cmd"`
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
	case r.Type == "response_item" && p.Type == "message" && p.Role == "developer":
		for _, c := range p.Content {
			s.codexCatalog(c.Text)
		}
	// 新版的网页工具是 web 命名空间的 run，随后同一个 id 再记一条 Extension（kind 为 web.search）
	case r.Type == "response_item" && p.Type == "function_call" && p.Namespace == "web" && p.Name == "run":
		if s.once(webKey(p.CallID)) {
			s.codexWebRun(p.Arguments)
		}
	case r.Type == "response_item" && (p.Type == "function_call" || p.Type == "custom_tool_call"):
		if name, ok := strings.CutPrefix(p.Name, "mcp__"); ok {
			s.addMCP("", name)
			s.toolURL([]byte(p.Arguments), mcpVia(name))
		} else if tool, ok := codexResourceTools[p.Name]; ok {
			s.mcpCall("", serverOf([]byte(p.Arguments)), tool)
		} else if cmd := commandOf([]byte(p.Arguments)); p.Type == "function_call" && cmd != "" && s.once(cmdKey(p.CallID)) {
			// custom_tool_call 的参数在 input 里，是补丁这类原文，不是命令。
			// 同一条命令随后的 CommandExecution 用 call_id 当 id，靠 once 不重复算
			s.command(cmd)
		}
	// 开始、完成时各记一遍：开始时就算上（中断了的、还在跑的也看得到），完成时按 status 撤回失败的
	case p.Item.Type == "McpToolCall":
		if s.once(p.Item.ID) {
			s.mcpCall(p.Item.ID, p.Item.Server, p.Item.Tool)
			s.toolURL(p.Item.Arguments, p.Item.Server+"."+p.Item.Tool)
		}
		if p.Item.Status != "inProgress" {
			s.mcpResult(p.Item.ID, p.Item.Status == "failed")
		}
	// 网页搜索和历史索引一样按格式只读一处：旧格式读 web_search_call（action 是 search 带 query，
	// 或 open_page 带 url），新格式（paginated）读 WebSearch 条目
	case r.Type == "response_item" && p.Type == "web_search_call" && s.st.Mode != "paginated":
		s.addSearch(p.Action.Query, "web_search")
		if p.Action.URL != "" {
			s.addURL(p.Action.URL, "web_search")
		}
	case (p.Item.Type == "WebSearch" || p.Item.Type == "Extension" && p.Item.Kind == "web.search") && s.st.Mode == "paginated":
		if s.once(webKey(p.Item.ID)) {
			s.addSearch(p.Item.Query, "web_search")
		}
	// 新版 Codex 把命令包在 exec 的脚本里跑，每条实际执行的命令记一条 CommandExecution，
	// parsed_cmd 已经标出读了哪个文件
	case p.Item.Type == "CommandExecution":
		if s.once(cmdKey(p.Item.ID)) {
			// 读了接给 grep 这类是在搜，不算读了技能；parsed_cmd 分不出管道和 &&，看原命令里有没有管道
			search := slices.ContainsFunc(p.Item.ParsedCmd, func(c parsedCmd) bool { return c.Type == "search" }) &&
				strings.Contains(string(p.Item.Command), "|")
			for _, c := range p.Item.ParsedCmd {
				if c.Type == "read" && !search {
					s.skillRead(c.Path, p.Item.Cwd)
				}
			}
			if len(p.Item.Command) > 0 {
				s.cliCommand(commandOf(append(append([]byte(`{"command":`), p.Item.Command...), '}')))
			}
		}
	}
}

// codexWebRun 记下 web.run 的一次调用：search_query 算一次搜索（几个搜索词合成一条），
// open 里直接打开的网址算访问；结果里列出的链接不算。
func (s *UsageScanner) codexWebRun(args string) {
	var a struct {
		SearchQuery []struct {
			Q string `json:"q"`
		} `json:"search_query"`
		Open []struct {
			RefID string `json:"ref_id"`
		} `json:"open"`
	}
	if json.Unmarshal([]byte(args), &a) != nil {
		return
	}
	var qs []string
	for _, q := range a.SearchQuery {
		if q.Q != "" {
			qs = append(qs, q.Q)
		}
	}
	s.addSearch(strings.Join(qs, " / "), "web_search")
	for _, o := range a.Open {
		if strings.HasPrefix(o.RefID, "http://") || strings.HasPrefix(o.RefID, "https://") {
			s.addURL(o.RefID, "web_search")
		}
	}
}

// webKey 是网页工具调用在 seen 里的 key：web.run 的 call_id 就是随后 Extension 条目的 id。
func webKey(id string) string {
	if id == "" {
		return ""
	}
	return "web:" + id
}

// cmdKey 是命令在 seen 里的 key：McpToolCall 条目的 id 也是调用它的 function_call 的 call_id，
// 命令和 MCP 调用分开记，免得 MCP 调用被当成重复。没有 id 的照算。
func cmdKey(id string) string {
	if id == "" {
		return ""
	}
	return "cmd:" + id
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
		Type       string          `json:"type"`
		ToolCalls  []grokToolCall  `json:"tool_calls"`
		ToolCallID string          `json:"tool_call_id"`
		Content    json.RawMessage `json:"content"`
		Kind       struct {
			ToolType string `json:"tool_type"`
			Action   struct {
				Query string `json:"query"`
			} `json:"action"`
		} `json:"kind"`
	}
	if json.Unmarshal(b, &r) != nil {
		return
	}
	if r.Type == "tool_result" {
		var text string
		json.Unmarshal(r.Content, &text)
		s.mcpResult(r.ToolCallID, strings.HasPrefix(text, "Failed to call "))
		return
	}
	// grok 在服务端跑的搜索
	if r.Type == "backend_tool_call" {
		s.addSearch(r.Kind.Action.Query, r.Kind.ToolType)
		return
	}
	for _, c := range r.ToolCalls {
		if c.Name == "use_tool" {
			var a struct {
				ToolName  string          `json:"tool_name"`
				ToolInput json.RawMessage `json:"tool_input"`
			}
			if json.Unmarshal([]byte(c.Arguments), &a) == nil {
				s.addMCP(c.ID, a.ToolName)
				s.toolURL(a.ToolInput, mcpVia(a.ToolName))
			}
			continue
		}
		switch c.Name {
		case "read_file":
			a, _ := decodeToolArgs([]byte(c.Arguments))
			s.skillRead(a.TargetFile, "")
		case "run_terminal_command":
			s.command(commandOf([]byte(c.Arguments)))
		default:
			s.webTool(c.Name, []byte(c.Arguments))
		}
	}
}

func (s *UsageScanner) result() Usage {
	var u Usage
	var all []seqEvent
	for _, l := range [][]seqEvent{s.tools, s.cmds, s.webs} {
		all = append(all, l[max(0, len(l)-maxUsageEvents):]...)
	}
	slices.SortFunc(all, func(a, b seqEvent) int { return a.n - b.n })
	for _, e := range all {
		u.Events = append(u.Events, e.e)
	}
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
	for name, n := range s.cli {
		u.CLI = append(u.CLI, CLIUsage{Name: name, Source: s.sources[name], Count: n})
	}
	slices.SortFunc(u.CLI, func(a, b CLIUsage) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Name, b.Name)
	})
	for host, n := range s.web {
		u.Web = append(u.Web, WebUsage{Host: host, Count: n})
	}
	slices.SortFunc(u.Web, func(a, b WebUsage) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Host, b.Host)
	})
	u.Searches = s.searches
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
