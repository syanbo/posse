package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadContext(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, lines ...string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	claude := write("c.jsonl",
		`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":5}}}`,
		`{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":2,"cache_creation_input_tokens":1000,"cache_read_input_tokens":48998,"output_tokens":100}}}`,
		`{"type":"assistant","message":{"model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}}}`,
		`{"type":"user","message":{"content":"has \"usage\" text"}}`,
	)
	if c, ok := ReadContext("claude", claude, ""); !ok || c.Used != 50000 || c.Window != 200_000 || c.Percent() != 25 {
		t.Fatalf("claude: %+v %v", c, ok)
	}
	codex := write("x.jsonl",
		`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":134806},"model_context_window":828400}}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":null}}`,
	)
	if c, ok := ReadContext("codex", codex, ""); !ok || c.Used != 134806 || c.Window != 828400 || c.Percent() != 16 {
		t.Fatalf("codex: %+v %v", c, ok)
	}
	if _, ok := ReadContext("grok", codex, ""); ok {
		t.Fatal("grok has no context")
	}
	// 用量那行在很长的行之前，跨过读取块的边界
	big := `{"type":"user","pad":"` + strings.Repeat("x", contextChunk+100) + `"}`
	p := write("big.jsonl", `{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":300000}}}`, big, big)
	if c, ok := ReadContext("claude", p, ""); !ok || c.Used != 300000 || c.Window != 1_000_000 {
		t.Fatalf("big: %+v %v", c, ok)
	}
}

func TestClaudeWindow(t *testing.T) {
	for model, want := range map[string]int{
		"claude-opus-5-5": 1_000_000, "claude-sonnet-5-5": 1_000_000, "claude-sonnet-4-5": 200_000,
		"kiro/claude-opus-5.5": 1_000_000, "provider/claude-sonnet-5.5": 1_000_000,
		"us.anthropic.claude-sonnet-4-5-20250929-v1:0": 200_000, "claude-3-5-sonnet-20241022": 200_000,
		"claude-haiku-4-5": 200_000, "claude-opus-4-6[1m]": 1_000_000, "": 0, "provider/custom-model": 0,
	} {
		if got := claudeWindow(model, 1000); got != want {
			t.Errorf("%s: %d", model, got)
		}
	}
}

func TestUnknownClaudeContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.jsonl")
	raw := `{"type":"assistant","message":{"model":"custom","usage":{"input_tokens":168000}}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, ok := ReadContext("claude", path, ""); ok {
		t.Fatalf("unknown capacity: %+v", c)
	}
}

func TestClaudeContextStopsAtCompact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	old := `{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":840000}}}` + "\n"
	for _, boundary := range []string{`{"type":"system","subtype":"compact_boundary"}`, `{"type":"user","isCompactSummary":true}`} {
		if err := os.WriteFile(path, []byte(old+boundary+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if c, ok := ReadContext("claude", path, ""); ok {
			t.Fatalf("pre-compact usage: %+v", c)
		}
		next := `{"type":"assistant","message":{"model":"claude-opus-5-5","usage":{"input_tokens":10000}}}` + "\n"
		if err := os.WriteFile(path, []byte(old+boundary+"\n"+next), 0o600); err != nil {
			t.Fatal(err)
		}
		if c, ok := ReadContext("claude", path, ""); !ok || c.Percent() != 1 {
			t.Fatalf("post-compact usage: %+v %v", c, ok)
		}
	}
}
