package daemon

import (
	"encoding/json"
	"testing"
)

func TestWantSessionBrief(t *testing.T) {
	startup := json.RawMessage(`{"source":"startup","session_id":"s1"}`)
	if !wantSessionBrief("claude", "SessionStart", startup, nil) {
		t.Fatal("claude startup")
	}
	if !wantSessionBrief("codex", "SessionStart", json.RawMessage(`{}`), []string{"zsh", "-lc", "codex"}) {
		t.Fatal("codex new session")
	}
	for _, c := range []struct {
		agent, event string
		payload      string
		cmd          []string
	}{
		{"claude", "SessionStart", `{"source":"resume"}`, nil},
		{"claude", "SessionStart", `{"source":"compact"}`, nil},
		{"claude", "SessionStart", `{"source":"clear"}`, nil},
		{"claude", "UserPromptSubmit", `{"source":"startup"}`, nil},
		{"grok", "SessionStart", `{"source":"startup"}`, nil},
		{"cursor", "SessionStart", `{}`, nil},
		{"codex", "SessionStart", `{}`, []string{"zsh", "-lc", "codex resume 'sid'"}},
		{"claude", "SessionStart", `{"source":"startup"}`, []string{"zsh", "-lc", "claude --resume 'sid'"}},
	} {
		if wantSessionBrief(c.agent, c.event, json.RawMessage(c.payload), c.cmd) {
			t.Fatalf("should skip %s %s %s", c.agent, c.event, c.payload)
		}
	}
}
