package daemon

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/store"
)

func TestAntigravityTaskHandoff(t *testing.T) {
	d, w := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	t.Cleanup(d.closeHistory)
	for _, id := range []string{"55555555-5555-4555-8555-555555555555", "66666666-6666-4666-8666-666666666666"} {
		file := filepath.Join(home, ".claude", "projects", "-test", id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		cwd, _ := json.Marshal(w.Path)
		body := `{"type":"user","cwd":` + string(cwd) + `,"sessionId":"` + id + `","timestamp":"2026-11-14T10:00:00Z","message":{"role":"user","content":"Previous task context"}}` + "\n"
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx := context.Background()
	params, _ := json.Marshal(map[string]string{"cwd": w.Path, "skip": "66666666-6666-4666-8666-666666666666"})
	if _, err := d.historyIndexNow(ctx, json.RawMessage(`{"wait":true}`)); err != nil {
		t.Fatal(err)
	}
	result, err := d.historyBrief(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if out := string(b); !strings.Contains(out, "Previous task context") || !strings.Contains(out, "55555555-5555-4555-8555-555555555555") || strings.Contains(out, "66666666-6666-4666-8666-666666666666") {
		t.Fatalf("unexpected brief: %s", out)
	}
	if prompt := workerPrompt(store.Task{ID: "k_test", Agent: "antigravity", Spec: "Original spec"}, w); !strings.Contains(prompt, "`posse history brief`") || !strings.HasSuffix(prompt, "Original spec") {
		t.Fatal(prompt)
	}
}

func TestAntigravityBriefUsesKnownDirectory(t *testing.T) {
	d, w := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	t.Cleanup(d.closeHistory)
	known, other, unknown := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"
	base := history.AntigravityBase(home)
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	summaries := map[string]any{}
	hookURI := url.URL{Scheme: "file", Path: agent.AntigravityHookDir(t.TempDir())}
	for _, id := range []string{known, other, unknown} {
		summary := map[string]any{"ID": id, "Title": "Previous Antigravity task"}
		switch id {
		case known, unknown:
			summary["WorkspaceURIs"] = []string{hookURI.String()}
		case other:
			u := url.URL{Scheme: "file", Path: t.TempDir()}
			summary["WorkspaceURIs"] = []string{hookURI.String(), u.String()}
		}
		summaries[id] = map[string]any{"summary": summary}
		write(filepath.Join(base, "brain", id, ".system_generated", "logs", "transcript.jsonl"),
			`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-18T07:00:00Z","content":"Previous Antigravity work"}`+"\n")
	}
	b, err := json.Marshal(map[string]any{"conversations": summaries})
	if err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(base, "cache", "conversation_metadata.json"), string(b))
	for _, id := range []string{known, other} {
		if err := d.store.AddTerminal(store.Terminal{ID: "t_" + id, WorkspaceID: w.ID, Agent: "antigravity", SessionID: id, Cwd: w.Path, State: "exited", CreatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.historyIndexNow(t.Context(), json.RawMessage(`{"wait":true}`)); err != nil {
		t.Fatal(err)
	}
	check := func(skip string) {
		t.Helper()
		result := call(t, d.historyBrief, map[string]any{"cwd": resolvePath(w.Path), "skip": skip})
		b, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		out := string(b)
		if strings.Contains(out, known) != (skip == "") || strings.Contains(out, other) || strings.Contains(out, unknown) {
			t.Fatalf("unexpected brief: %s", out)
		}
	}
	check("")
	if err := d.store.RememberAntigravityDir(known, resolvePath(w.Path)); err != nil {
		t.Fatal(err)
	}
	if err := d.store.RememberAntigravityDir(known, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := d.store.PruneTerminals(); err != nil {
		t.Fatal(err)
	}
	check("")
	check(known)
}

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
