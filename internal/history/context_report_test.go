package history

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReportedContext(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, []byte("transcript\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := func(session string, window any) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"session_id": session, "transcript_path": path, "context_window": window})
		if err := ReportClaudeContext(home, b); err != nil {
			t.Fatal(err)
		}
	}
	report("s", map[string]any{"context_window_size": 1000000, "used_percentage": 16.7, "current_usage": map[string]int{"input_tokens": 1000, "cache_read_input_tokens": 166000, "output_tokens": 9000}})
	c, ok := ReadReportedContext(home, "claude", "s", path)
	if !ok || c.Percent() != 17 || c.Window != 1000000 || c.Used != 167000 {
		t.Fatalf("official: %+v %v", c, ok)
	}
	for _, a := range []struct{ agent, session string }{{"codex", "s"}, {"claude", "other"}} {
		if _, ok := ReadReportedContext(home, a.agent, a.session, path); ok {
			t.Fatal("cross-session context")
		}
	}
	appendLine := func(line string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	appendLine(`{"type":"system","subtype":"turn_duration"}`)
	appendLine(`{"type":"assistant","isSidechain":true,"message":{"usage":{"input_tokens":8000}}}`)
	if c, ok := ReadReportedContext(home, "claude", "s", path); !ok || c.Percent() != 17 {
		t.Fatalf("metadata invalidated official: %+v %v", c, ok)
	}
	report("s", map[string]any{"context_window_size": 1000000, "current_usage": map[string]int{"input_tokens": 168000, "output_tokens": 9000}})
	if c, ok := ReadReportedContext(home, "claude", "s", path); !ok || c.Percent() != 17 || c.Used != 168000 {
		t.Fatalf("fallback: %+v %v", c, ok)
	}
	report("s", map[string]any{"context_window_size": 1000000, "used_percentage": 0})
	if c, ok := ReadReportedContext(home, "claude", "s", path); !ok || c.ReportedPercent == nil || c.Percent() != 0 {
		t.Fatalf("zero: %+v %v", c, ok)
	}
	report("s", map[string]any{"context_window_size": 1000000, "used_percentage": nil, "current_usage": nil})
	if c, ok := ReadReportedContext(home, "claude", "s", path); !ok || c.Window != 0 || c.ReportedPercent != nil {
		t.Fatalf("compact: %+v %v", c, ok)
	}
	appendLine(`{"type":"system","subtype":"compact_boundary"}`)
	appendLine(`{"type":"user","isCompactSummary":true,"message":{"content":"summary"}}`)
	if c, ok := ReadReportedContext(home, "claude", "s", path); !ok || c.Window != 0 {
		t.Fatalf("compact metadata: %+v %v", c, ok)
	}
	// 会话后来由没有注入 statusLine 的 Claude 续写，旧官方读数不能一直覆盖新用量。
	appendLine(`{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":12000}}}`)
	if _, ok := ReadReportedContext(home, "claude", "s", path); ok {
		t.Fatal("stale report")
	}
}

func TestReportedContextWithoutTranscriptExpires(t *testing.T) {
	home := t.TempDir()
	if err := ReportClaudeContext(home, []byte(`{"session_id":"s","context_window":{"used_percentage":17}}`)); err != nil {
		t.Fatal(err)
	}
	if c, ok := ReadReportedContext(home, "claude", "s", ""); !ok || c.Percent() != 17 {
		t.Fatalf("fresh: %+v %v", c, ok)
	}
	path := contextReportPath(home, "s")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var r contextReport
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	r.At = time.Now().Add(-2 * time.Minute).UnixNano()
	b, _ = json.Marshal(r)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadReportedContext(home, "claude", "s", ""); ok {
		t.Fatal("unverifiable report never expired")
	}
}

func TestReportedContextPartialUsage(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "s.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","message":`), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"session_id": "s", "transcript_path": path, "context_window": map[string]any{"used_percentage": 17}})
	if err := ReportClaudeContext(home, b); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"model":"claude-opus-5-5","usage":{"input_tokens":200000}}}` + "\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadReportedContext(home, "claude", "s", path); ok {
		t.Fatal("missed completed usage")
	}
}

func TestReportedContextDuplicateBlocks(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "s.jsonl")
	block := `{"type":"assistant","message":{"id":"reply-1","model":"group/codex-opus5.5","usage":{"input_tokens":1000,"cache_read_input_tokens":166000},"content":[{"type":"thinking","thinking":"thinking"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"session_id": "s", "transcript_path": path, "context_window": map[string]any{"context_window_size": 1000000, "used_percentage": 16.7, "current_usage": map[string]int{"input_tokens": 1000, "cache_read_input_tokens": 166000}}})
	if err := ReportClaudeContext(home, b); err != nil {
		t.Fatal(err)
	}
	appendLine := func(line string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	appendLine(`{"type":"assistant","message":{"id":"reply-1","model":"group/codex-opus5.5","usage":{"input_tokens":1000,"cache_read_input_tokens":166000},"content":[{"type":"tool_use","name":"Shell"}]}}`)
	if c, ok := ReadReportedContext(home, "claude", "s", path); !ok || c.Percent() != 17 {
		t.Fatalf("duplicate block discarded official: %+v %v", c, ok)
	}
	appendLine(`{"type":"assistant","message":{"id":"reply-2","model":"group/codex-opus5.5","usage":{"input_tokens":2000,"cache_read_input_tokens":166000}}}`)
	if _, ok := ReadReportedContext(home, "claude", "s", path); ok {
		t.Fatal("new usage kept stale official")
	}
	// 压缩之后即使用量恰好相同，也应读取新回复，不保留边界前的官方读数。
	if err := os.WriteFile(path, []byte(block), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReportClaudeContext(home, b); err != nil {
		t.Fatal(err)
	}
	appendLine(`{"type":"system","subtype":"compact_boundary"}`)
	appendLine(block)
	if _, ok := ReadReportedContext(home, "claude", "s", path); ok {
		t.Fatal("post-compact usage kept pre-compact official")
	}
}
