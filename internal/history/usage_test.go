package history

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		if _, err := f.WriteString(l); err != nil {
			t.Fatal(err)
		}
	}
}

// counts 只留累计次数，逐次记录另外测。
func counts(u Usage) Usage {
	u.Events = nil
	return u
}

// 逐次记录带上那一行的时间，按会话里的先后排。
func TestUsageEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path,
		`{"type":"assistant","timestamp":"2026-10-16T08:00:00.000Z","message":{"content":[{"type":"tool_use","name":"mcp__androidmirror__screen_open","input":{}}]}}`+"\n",
		`{"type":"user","timestamp":"2026-10-16T08:01:00.000Z","message":{"content":"Base directory for this skill: /x/skills/computer-use"}}`+"\n",
	)
	u, err := NewUsageScanner("claude", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := []UsageEvent{
		{At: time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC), Server: "androidmirror", Tool: "screen_open"},
		{At: time.Date(2026, 10, 16, 8, 1, 0, 0, time.UTC), Skill: "computer-use"},
	}
	if len(u.Events) != 2 || !u.Events[0].At.Equal(want[0].At) || u.Events[0].Tool != "screen_open" || u.Events[1].Skill != "computer-use" || !u.Events[1].At.Equal(want[1].At) {
		t.Fatalf("events %+v", u.Events)
	}
}

func TestUsageClaude(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeLines(t, path,
		// 模型调 Skill 后注入的说明，和用户敲斜杠命令注入的一样
		`{"type":"user","message":{"content":[{"type":"text","text":"Base directory for this skill: /Users/x/.claude/skills/computer-use\n\n# Computer Use"}]}}`+"\n",
		`{"type":"user","message":{"content":"Base directory for this skill: /Users/x/.claude/skills/find-skills\n..."}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__codex-cu__js","input":{}},{"type":"tool_use","name":"mcp__claude_ai_Claude_Docs__batch","input":{}}]}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__codex-cu__js","input":{}}]}}`+"\n",
		// Skill 工具调用本身不算，算的是随后注入的说明
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"computer-use"}}]}}`+"\n",
		// 系统提示里的技能列表不是工具调用
		`{"type":"user","message":{"content":"available: skills/archify/SKILL.md"}}`+"\n",
		// 写文件、grep 的参数里提到 SKILL.md 不算；读 SKILL.md 算
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/t/a_test.go","content":"skills/archify/SKILL.md"}},{"type":"tool_use","name":"Bash","input":{"command":"grep -o skills/x/SKILL.md f"}}]}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/Users/x/.agents/skills/unslop/SKILL.md"}}]}}`+"\n",
	)
	s := NewUsageScanner("claude", path)
	u, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{
		Skills: []UsageItem{{"computer-use", 1}, {"find-skills", 1}, {"unslop", 1}},
		MCP: []MCPUsage{
			{Server: "codex-cu", Count: 2, Tools: []UsageItem{{"js", 2}}},
			{Server: "claude_ai_Claude_Docs", Count: 1, Tools: []UsageItem{{"batch", 1}}},
		},
	}
	if !reflect.DeepEqual(counts(u), want) {
		t.Fatalf("got %+v\nwant %+v", u, want)
	}

	// 写到一半的行留到下次，写完后增量读到
	writeLines(t, path, `{"type":"user","message":{"content":"Base directory for this skill: /x/skills/computer-use"}}`)
	if u, _ = s.Scan(); u.Skills[0] != (UsageItem{"computer-use", 1}) {
		t.Fatalf("half line counted: %+v", u.Skills)
	}
	writeLines(t, path, "\n")
	if u, _ = s.Scan(); u.Skills[0] != (UsageItem{"computer-use", 2}) {
		t.Fatalf("after newline: %+v", u.Skills)
	}

	// 文件被重写变短时从头统计
	os.WriteFile(path, []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__a__b","input":{}}]}}`+"\n"), 0o600)
	if u, _ = s.Scan(); len(u.Skills) != 0 || len(u.MCP) != 1 {
		t.Fatalf("after rewrite: %+v", u)
	}
}

func TestUsageCodex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	writeLines(t, path,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<skill>\n<name>fiction-plot</name>\n<path>/x/SKILL.md</path>"}]}}`+"\n",
		// 同一条用户消息在 item_completed 里又记一遍，不能重复算
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"<skill>\n<name>fiction-plot</name>"}]}}}`+"\n",
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"sed -n 1,80p /Users/x/.agents/skills/iphone-use/SKILL.md\"}"}}`+"\n",
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"grep -rn x /Users/x/skills/runner/SKILL.md\"}"}}`+"\n",
		`{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"cat /Users/x/skills/unslop/SKILL.md\"]}"}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"x1","parsed_cmd":[{"type":"read","cmd":"cat /u/skills/runner/SKILL.md","path":"/u/skills/runner/SKILL.md"}]}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"x1","parsed_cmd":[{"type":"read","path":"/u/skills/runner/SKILL.md"}]}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"x2","parsed_cmd":[{"type":"search","path":"/u/skills/other/SKILL.md"}]}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_started","item":{"type":"McpToolCall","id":"e1","server":"androidmirror","tool":"screen_act"}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"e1","server":"androidmirror","tool":"screen_act"}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"e2","server":"androidmirror","tool":"screen_observe"}}}`+"\n",
	)
	u, err := NewUsageScanner("codex", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{
		Skills: []UsageItem{{"fiction-plot", 1}, {"iphone-use", 1}, {"runner", 1}, {"unslop", 1}},
		MCP:    []MCPUsage{{Server: "androidmirror", Count: 2, Tools: []UsageItem{{"screen_act", 1}, {"screen_observe", 1}}}},
	}
	if !reflect.DeepEqual(counts(u), want) {
		t.Fatalf("got %+v\nwant %+v", u, want)
	}
}

func TestUsageGrok(t *testing.T) {
	path := filepath.Join(t.TempDir(), "chat_history.jsonl")
	writeLines(t, path,
		`{"type":"assistant","tool_calls":[{"name":"read_file","arguments":"{\"target_file\":\"/Users/x/.grok/skills/orchestration/SKILL.md\"}"},{"name":"use_tool","arguments":"{\"tool_name\":\"androidmirror__screen_observe\",\"tool_input\":{}}"}]}`+"\n",
		`{"type":"assistant","tool_calls":[{"name":"use_tool","arguments":"{\"tool_name\":\"androidmirror__screen_observe\"}"},{"name":"use_tool","arguments":"{\"tool_name\":\"mobile__mobile_list_apps\"}"}]}`+"\n",
	)
	u, err := NewUsageScanner("grok", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{
		Skills: []UsageItem{{"orchestration", 1}},
		MCP: []MCPUsage{
			{Server: "androidmirror", Count: 2, Tools: []UsageItem{{"screen_observe", 2}}},
			{Server: "mobile", Count: 1, Tools: []UsageItem{{"mobile_list_apps", 1}}},
		},
	}
	if !reflect.DeepEqual(counts(u), want) {
		t.Fatalf("got %+v\nwant %+v", u, want)
	}
}

func TestUsageCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c1.jsonl")
	writeLines(t, path,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"path":"/Users/x/.claude/skills/browser-skill/SKILL.md"}},{"type":"tool_use","name":"CallDynamicTool","input":{"namespace":"probe","toolName":"ping","arguments":{}}}]}}`+"\n",
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"CallDynamicTool","input":{"namespace":"cursor","toolName":"WebSearch","arguments":{}}},{"type":"tool_use","name":"Shell","input":{"command":"cat ~/.agents/skills/orchestration/SKILL.md"}},{"type":"tool_use","name":"GetDynamicTools","input":{"namespace":"probe","toolName":"ping"}}]}}`+"\n",
	)
	u, err := NewUsageScanner("cursor", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{
		Skills: []UsageItem{{"browser-skill", 1}, {"orchestration", 1}},
		MCP:    []MCPUsage{{Server: "probe", Count: 1, Tools: []UsageItem{{"ping", 1}}}},
	}
	if !reflect.DeepEqual(counts(u), want) {
		t.Fatalf("got %+v\nwant %+v", u, want)
	}
}

// 四家执行的命令都记下用到的程序；Codex 的 function_call 和同 id 的 CommandExecution 只算一次。
func TestUsageCLI(t *testing.T) {
	defer func(b, l, s []string) { brewDirs, brewLinks, systemDirs = b, l, s }(brewDirs, brewLinks, systemDirs)
	brewDirs, brewLinks, systemDirs = nil, nil, nil
	dir := t.TempDir()
	cases := []struct {
		agent, line string
	}{
		{"claude", `{"type":"assistant","timestamp":"2026-10-16T08:00:00.000Z","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git status && gh pr list"}}]}}`},
		{"grok", `{"type":"assistant","tool_calls":[{"name":"run_terminal_command","arguments":"{\"command\":\"git status && gh pr list\"}"}]}`},
		{"cursor", `{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"git status && gh pr list"}}]}}`},
		{"codex", `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"c1","arguments":"{\"cmd\":\"git status && gh pr list\"}"}}` + "\n" +
			`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"c1","command":["/bin/zsh","-lc","git status && gh pr list"]}}}`},
	}
	for _, c := range cases {
		path := filepath.Join(dir, c.agent+".jsonl")
		writeLines(t, path, c.line+"\n")
		u, err := NewUsageScanner(c.agent, path).Scan()
		if err != nil {
			t.Fatal(err)
		}
		want := []CLIUsage{{Name: "gh", Count: 1}, {Name: "git", Count: 1}}
		if !reflect.DeepEqual(u.CLI, want) || len(u.Events) != 2 || u.Events[0].CLI != "git" || u.Events[0].Command != "git status && gh pr list" {
			t.Errorf("%s: cli %+v events %+v", c.agent, u.CLI, u.Events)
		}
	}

	// 新版 Codex 的命令只有 CommandExecution（id 是 exec-…），读 SKILL.md 的照样算技能
	path := filepath.Join(dir, "codex2.jsonl")
	writeLines(t, path,
		`{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"c9","input":"tools.exec_command({cmd:\"go test\"})"}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"exec-1","command":["/bin/zsh","-lc","go test ./... && cat /u/skills/runner/SKILL.md"],"parsed_cmd":[{"type":"unknown"},{"type":"read","path":"/u/skills/runner/SKILL.md"}]}}}`+"\n",
	)
	u, _ := NewUsageScanner("codex", path).Scan()
	if !reflect.DeepEqual(u.CLI, []CLIUsage{{Name: "go", Count: 1}}) || !reflect.DeepEqual(u.Skills, []UsageItem{{"runner", 1}}) {
		t.Fatalf("codex exec: %+v", counts(u))
	}

	// MCP 调用的 function_call（namespace 是 mcp__…）和随后的 McpToolCall 同一个 id，MCP 照算
	path = filepath.Join(dir, "codex3.jsonl")
	writeLines(t, path,
		`{"type":"response_item","payload":{"type":"function_call","name":"js","namespace":"mcp__cua_repl","call_id":"c5","arguments":"{\"code\":\"1\"}"}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"McpToolCall","id":"c5","server":"cua_repl","tool":"js"}}}`+"\n",
	)
	u, _ = NewUsageScanner("codex", path).Scan()
	if len(u.MCP) != 1 || u.MCP[0].Count != 1 {
		t.Fatalf("codex mcp after function_call: %+v", counts(u))
	}
}

// 命令行刷了几百条之后，前面的 MCP 调用仍在时间线里，顺序照旧：两类各留最近 maxUsageEvents 条。
func TestUsageEventsKeepToolsPastCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	mcp := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__codex-cu__js","input":{}}]}}` + "\n"
	bash := `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]}}` + "\n"
	lines := []string{mcp}
	for range maxUsageEvents*2 + 100 {
		lines = append(lines, bash)
	}
	lines = append(lines, mcp)
	writeLines(t, path, lines...)
	u, err := NewUsageScanner("claude", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Events) != maxUsageEvents+2 || u.Events[0].Tool != "js" || u.Events[1].CLI != "git" || u.Events[len(u.Events)-1].Tool != "js" {
		t.Fatalf("%d events, first %+v last %+v", len(u.Events), u.Events[0], u.Events[len(u.Events)-1])
	}
}

// 网址：工具和 MCP 参数里的 url、访问网页的程序命令里的网址、用户自己发的网址按域名计次，
// 本机地址、注入的提醒、echo 里提到的不算；搜索单独计次。
func TestUsageWeb(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "claude.jsonl")
	writeLines(t, path,
		`{"type":"user","timestamp":"2026-10-30T08:00:00.000Z","message":{"content":"看下 https://github.com/a/b，还有 [文档](https://docs.rs/x)。<system-reminder>https://ignored.example.com</system-reminder>"}}`+"\n",
		`{"type":"assistant","timestamp":"2026-10-30T08:01:00.000Z","message":{"content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://github.com/c/d","prompt":"x"}},{"type":"tool_use","name":"WebSearch","input":{"query":"posse terminal"}}]}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"curl -s https://www.example.com/a.json | jq . && curl http://localhost:8080/x"}},{"type":"tool_use","name":"Bash","input":{"command":"echo https://not.counted.com"}}]}}`+"\n",
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__browser__navigate","input":{"url":"https://example.com/b"}},{"type":"tool_use","name":"Bash","input":{"command":"posse web open https://news.ycombinator.com"}}]}}`+"\n",
		// 重复存的请求不再算一次
		`{"type":"api-request-blob","message":{"role":"assistant","content":[{"type":"tool_use","name":"WebFetch","input":{"url":"https://github.com/c/d"}}]}}`+"\n",
		// 工具结果里的网址不是用户发的
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"see https://tool.output.com"}]}}`+"\n",
	)
	u, err := NewUsageScanner("claude", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := []WebUsage{{"example.com", 2}, {"github.com", 2}, {"docs.rs", 1}, {"news.ycombinator.com", 1}}
	if !reflect.DeepEqual(u.Web, want) || u.Searches != 1 {
		t.Fatalf("web %+v searches %d", u.Web, u.Searches)
	}
	var got []string
	for _, e := range u.Events {
		if e.URL != "" {
			got = append(got, e.Via+" "+e.URL)
		} else if e.Query != "" {
			got = append(got, e.Via+" ?"+e.Query)
		}
	}
	wantEv := []string{
		"user https://github.com/a/b", "user https://docs.rs/x",
		"WebFetch https://github.com/c/d", "WebSearch ?posse terminal",
		"curl https://www.example.com/a.json", "browser.navigate https://example.com/b", "posse web https://news.ycombinator.com",
	}
	if !reflect.DeepEqual(got, wantEv) {
		t.Fatalf("events\n%q\nwant\n%q", got, wantEv)
	}
	if !u.Events[0].At.Equal(time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("user url time %v", u.Events[0].At)
	}

	// codex：注入的 AGENTS.md 里的网址不算；新格式的用户消息只读 item_completed，搜索只读 WebSearch 条目
	path = filepath.Join(dir, "codex.jsonl")
	writeLines(t, path,
		`{"type":"session_meta","payload":{"id":"s1","history_mode":"paginated"}}`+"\n",
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"# AGENTS.md instructions\nhttps://agents.example.com"}]}}`+"\n",
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"打开 https://openai.com/x"}]}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"打开 https://openai.com/x"}]}}}`+"\n",
		`{"type":"response_item","payload":{"type":"web_search_call","action":{"type":"search","query":"q1"}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"WebSearch","id":"ws1","query":"q1"}}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"CommandExecution","id":"x1","command":["/bin/zsh","-lc","wget https://go.dev/dl/x.tgz"]}}}`+"\n",
	)
	u, _ = NewUsageScanner("codex", path).Scan()
	if want := []WebUsage{{"go.dev", 1}, {"openai.com", 1}}; !reflect.DeepEqual(u.Web, want) || u.Searches != 1 {
		t.Fatalf("codex web %+v searches %d", u.Web, u.Searches)
	}

	// 新版 codex 的网页工具：web.run 和随后同 id 的 Extension 只算一次；open 里打开的网址算访问，结果里的链接不算
	path = filepath.Join(dir, "codex-run.jsonl")
	writeLines(t, path,
		`{"type":"session_meta","payload":{"id":"s2","history_mode":"paginated"}}`+"\n",
		`{"type":"response_item","payload":{"type":"function_call","name":"run","namespace":"web","call_id":"c1","arguments":"{\"search_query\":[{\"q\":\"a\"},{\"q\":\"b\"}]}"}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","kind":"web.search","id":"c1","query":"a ...","results":[{"url":"https://result.example.com"}]}}}`+"\n",
		`{"type":"response_item","payload":{"type":"function_call","name":"run","namespace":"web","call_id":"c2","arguments":"{\"search_query\":[{\"q\":\"c\"}],\"open\":[{\"ref_id\":\"https://www.reddit.com/r/x\"},{\"ref_id\":\"turn0search1\"}]}"}}`+"\n",
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","kind":"web.search","id":"c2","query":"c"}}}`+"\n",
		// 只有 Extension、没有 web.run 的也算
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"Extension","kind":"web.search","id":"c3","query":"d"}}}`+"\n",
	)
	u, _ = NewUsageScanner("codex", path).Scan()
	if want := []WebUsage{{"reddit.com", 1}}; !reflect.DeepEqual(u.Web, want) || u.Searches != 3 {
		t.Fatalf("codex web.run %+v searches %d", u.Web, u.Searches)
	}
	if e := u.Events[0]; e.Query != "a / b" || e.Via != "web_search" {
		t.Fatalf("codex web.run event %+v", e)
	}

	// grok：用户的话在 <user_query> 里，注入的规则不算；服务端搜索记成搜索
	path = filepath.Join(dir, "grok.jsonl")
	writeLines(t, path,
		`{"type":"user","content":"<rules>https://rules.example.com</rules><user_query>看 https://x.ai/news</user_query>"}`+"\n",
		`{"type":"backend_tool_call","kind":{"tool_type":"web_search","action":{"query":"grok"}}}`+"\n",
	)
	u, _ = NewUsageScanner("grok", path).Scan()
	if want := []WebUsage{{"x.ai", 1}}; !reflect.DeepEqual(u.Web, want) || u.Searches != 1 {
		t.Fatalf("grok web %+v searches %d", u.Web, u.Searches)
	}
}

func TestWebURLs(t *testing.T) {
	got := webURLs(`a https://a.com/x). b "https://b.com/p?q=1", (https://en.wikipedia.org/wiki/Go_(lang)) http://127.0.0.1:3000 https://a.com/x 中文https://c.cn/路径`)
	want := []string{"https://a.com/x", "https://b.com/p?q=1", "https://en.wikipedia.org/wiki/Go_(lang)", "https://c.cn/"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

// 命令里只取访问网页的程序自己参数里的网址。
func TestWebCommandURLs(t *testing.T) {
	cmd := "cat > /tmp/x <<'E'\nposse web open https://heredoc.example.com\nE\n" +
		"GOPROXY=https://goproxy.cn,direct go build && echo https://echo.example.com; " +
		"timeout 5 curl -sL https://a.example.com/x | jq; r=$(sudo -u me wget -qO- \"https://b.example.com\"); " +
		"bash -lc 'posse web goto https://c.example.com' > /tmp/out https://redir.example.com"
	var got []string
	for _, w := range webCommandURLs(cmd, 0) {
		got = append(got, w.via+" "+w.url)
	}
	want := []string{"curl https://a.example.com/x", "wget https://b.example.com", "posse web https://c.example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	if webHost("https://goproxy.cn,direct") != "" {
		t.Fatal("comma host counted")
	}
}
