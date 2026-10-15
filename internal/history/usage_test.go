package history

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
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
	if !reflect.DeepEqual(u, want) {
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
	if !reflect.DeepEqual(u, want) {
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
	if !reflect.DeepEqual(u, want) {
		t.Fatalf("got %+v\nwant %+v", u, want)
	}
}
