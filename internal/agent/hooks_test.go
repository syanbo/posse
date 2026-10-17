package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMapEvent(t *testing.T) {
	cases := []struct {
		agent, event, payload string
		want                  string // 空串表示不改变状态
	}{
		{"claude", "SessionStart", `{"source":"startup","session_id":"s1"}`, StatusIdle},
		{"claude", "SessionStart", `{"source":"compact"}`, ""},
		{"claude", "UserPromptSubmit", `{}`, StatusWorking},
		{"claude", "PreToolUse", `{"tool_name":"Bash"}`, StatusWorking},
		{"claude", "PreToolUse", `{"tool_name":"AskUserQuestion"}`, StatusWaiting},
		{"claude", "PermissionRequest", `{"tool_name":"Bash"}`, StatusWaiting},
		{"claude", "Notification", `{"notification_type":"idle_prompt"}`, StatusIdle},
		{"claude", "Stop", `{"last_assistant_message":"OK"}`, StatusIdle},
		{"claude", "SessionEnd", `{"reason":"clear"}`, ""},
		{"claude", "SessionEnd", `{"reason":"prompt_input_exit"}`, StatusExited},
		{"claude", "PreToolUse", `{"tool_name":"Bash","agent_id":"sub"}`, ""},
		{"codex", "SessionStart", `{}`, StatusWorking},
		{"codex", "PreToolUse", `{"tool_name":"request_user_input"}`, StatusWaiting},
		{"codex", "Interrupt", `{}`, StatusIdle},
		{"codex", "SessionEnd", `{"reason":"other"}`, StatusExited},
	}
	for _, c := range cases {
		st, ok := MapEvent(c.agent, c.event, json.RawMessage(c.payload))
		if got := map[bool]string{true: st.Status, false: ""}[ok]; got != c.want {
			t.Errorf("%s %s %s = %q, want %q", c.agent, c.event, c.payload, got, c.want)
		}
	}
}

func TestMapEventPrompt(t *testing.T) {
	ask := `{"z":1,"a":2}`
	st, ok := MapEvent("claude", "PreToolUse", json.RawMessage(
		`{"session_id":"s1","transcript_path":"/tmp/s.jsonl","tool_name":"AskUserQuestion","tool_input":`+ask+`}`))
	if !ok || st.Status != StatusWaiting || st.SessionID != "s1" || st.TranscriptPath != "/tmp/s.jsonl" ||
		st.ToolName != "AskUserQuestion" || st.Prompt != ask {
		t.Fatalf("ask %+v ok=%v", st, ok)
	}
	st, ok = MapEvent("codex", "PreToolUse", json.RawMessage(
		`{"tool_name":"request_user_input","tool_input":`+ask+`}`))
	if !ok || st.Status != StatusWaiting || st.ToolName != "request_user_input" || st.Prompt != ask {
		t.Fatalf("codex ask %+v ok=%v", st, ok)
	}

	long := strings.Repeat("界", 250)
	payload := `{"tool_name":"Bash","tool_input":{"file_path":"/tmp/ignored","command":["/bin/zsh","-lc",` + strconv.Quote(long) + `]}}`
	st, ok = MapEvent("claude", "PermissionRequest", json.RawMessage(payload))
	wantSum := strings.Repeat("界", 200)
	wantPrompt := `{"approval":{"tool":"Bash","summary":"` + wantSum + `"}}`
	if !ok || st.ToolName != "Bash" || st.ToolInput != wantSum || st.Prompt != wantPrompt {
		t.Fatalf("approval toolInput=%q prompt=%q ok=%v", st.ToolInput, st.Prompt, ok)
	}
	if strings.Contains(st.ToolInput, "…") || utf8.RuneCountInString(st.ToolInput) != 200 {
		t.Fatalf("summary cut %q (%d)", st.ToolInput, utf8.RuneCountInString(st.ToolInput))
	}
	st, ok = MapEvent("claude", "PermissionRequest", json.RawMessage(`{"tool_name":"Read","tool_input":{"command":"go test"}}`))
	if !ok || st.ToolInput != "go test" || st.Prompt != `{"approval":{"tool":"Read","summary":"go test"}}` {
		t.Fatalf("plain command %+v", st)
	}

	st, ok = MapEvent("claude", "Stop", json.RawMessage(`{"session_id":"s2","last_assistant_message":"done"}`))
	if !ok || st.Status != StatusIdle || st.SessionID != "s2" || st.Prompt != "" || st.ToolName != "" || st.ToolInput != "" {
		t.Fatalf("stop %+v", st)
	}
}

func TestTitleStatus(t *testing.T) {
	if st, ok := TitleStatus("claude", "✳ Simple OK reply", StatusWorking); !ok || st != StatusIdle {
		t.Fatal("claude idle title not detected")
	}
	if _, ok := TitleStatus("claude", "◐ Working", StatusWorking); ok {
		t.Fatal("spinner title treated as idle")
	}
	if _, ok := TitleStatus("claude", "✳ x", StatusWaiting); ok {
		t.Fatal("title must not override waiting")
	}
}

// 标题取自 grok 1.0.58 实际运行时的终端标题
func TestGrokTitleStatus(t *testing.T) {
	cases := []struct{ title, current, want string }{
		{"grok", StatusStarting, StatusIdle},
		{"⠹ - Waiting for response… - grok", StatusIdle, StatusWorking},
		{"⠴ - Responding - grok", StatusWorking, ""},
		{"User requests exact OK reply - grok", StatusWorking, StatusIdle},
		{"Action Required - grok", StatusWorking, StatusWaiting},
	}
	for _, c := range cases {
		st, ok := TitleStatus("grok", c.title, c.current)
		if got := map[bool]string{true: st, false: ""}[ok]; got != c.want {
			t.Errorf("grok %q from %s = %q, want %q", c.title, c.current, got, c.want)
		}
	}
}
