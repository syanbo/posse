package agent

import (
	"encoding/json"
	"testing"
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
