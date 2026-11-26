package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestParseAntigravityModels(t *testing.T) {
	raw := `Fetching available models...
gemini-4-neon-high	Gemini 4 Neon (High)
gemini-4-neon-xhigh	Gemini 4 Neon (XHigh)
gemini-4-neon-medium	Gemini 4 Neon (Medium)
gemini-4-neon-low	Gemini 4 Neon (Low)
gemini-4-carbon-medium	Gemini 4 Carbon (Medium)
gemini-4-carbon-high	Gemini 4 Carbon (High)
gemini-4-carbon-low	Gemini 4 Carbon (Low)
gemini-pro-orchestrator-neon	Orchestrator (Neon)
gpt-oss-120b-medium	GPT-OSS 120B (Medium)
`
	got := ParseAntigravityModels(raw)
	if len(got) != 4 {
		t.Fatalf("got %d models, want 4: %+v", len(got), got)
	}
	if got[0].ID != "gemini-4-neon" || got[0].Label != "Gemini 4 Neon" ||
		!slices.Equal(got[0].Efforts, []string{"low", "medium", "high", "xhigh"}) {
		t.Fatalf("unexpected neon: %+v", got[0])
	}
	if got[1].ID != "gemini-4-carbon" || got[1].Label != "Gemini 4 Carbon" ||
		!slices.Equal(got[1].Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("unexpected carbon: %+v", got[1])
	}
	if got[2].ID != "gemini-pro-orchestrator-neon" || got[2].Label != "Orchestrator (Neon)" || len(got[2].Efforts) != 0 {
		t.Fatalf("unexpected orchestrator: %+v", got[2])
	}
	if got[3].ID != "gpt-oss-120b" || got[3].Label != "GPT-OSS 120B" ||
		!slices.Equal(got[3].Efforts, []string{"medium"}) {
		t.Fatalf("unexpected gpt-oss: %+v", got[3])
	}
}

func TestSplitAntigravityModelEffort(t *testing.T) {
	base, ef, ok := SplitAntigravityModelEffort("gemini-4-neon-xhigh")
	if !ok || base != "gemini-4-neon" || ef != "xhigh" {
		t.Fatalf("got (%q, %q, %v)", base, ef, ok)
	}
	base, ef, ok = SplitAntigravityModelEffort("gemini-pro-orchestrator-neon")
	if ok || base != "gemini-pro-orchestrator-neon" || ef != "" {
		t.Fatalf("expected no split, got (%q, %q, %v)", base, ef, ok)
	}
}

func TestParseCursorModels(t *testing.T) {
	raw := "Available models\n\nauto - Auto (default)\ngpt-5.3-codex-high - Codex 5.3 High\ngrok-4.7-low-fast - Grok 4.7  Low Fast\u200b\u200b\n"
	got := ParseCursorModels(raw)
	if len(got) != 3 {
		t.Fatalf("got %d models, want 3: %+v", len(got), got)
	}
	if !got[0].Default || got[0].ID != "auto" || len(got[0].Efforts) != 0 {
		t.Fatalf("unexpected auto: %+v", got[0])
	}
	if got[2].ID != "grok-4.7-low-fast" || got[2].Label != "Grok 4.7  Low Fast" {
		t.Fatalf("zero-width chars should be stripped: %+v", got[2])
	}
}

func TestParseGrokModels(t *testing.T) {
	raw := "You are not authenticated.\n\nDefault model: grok-4.7\n\nAvailable models:\n  * grok-4.7 (default)\n    grok-4.6\n"
	got := ParseGrokModels(raw)
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(got), got)
	}
	if got[0].ID != "grok-4.7" || !got[0].Default {
		t.Fatalf("unexpected default grok model: %+v", got[0])
	}
	if got[1].ID != "grok-4.6" || got[1].Default {
		t.Fatalf("unexpected second grok model: %+v", got[1])
	}
}

func TestParseCodexModelsCache(t *testing.T) {
	raw := `{
  "models": [
    {
      "slug": "gpt-6.2-sol",
      "display_name": "GPT-6.2-Sol",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [{"effort": "low"}, {"effort": "medium"}, {"effort": "high"}],
      "visibility": "list"
    },
    {
      "slug": "gpt-reserve",
      "display_name": "GPT-Reserve",
      "default_reasoning_level": "medium",
      "supported_reasoning_levels": [{"effort": "medium"}],
      "visibility": "hide"
    }
  ]
}`
	got, err := ParseCodexModelsCache([]byte(raw))
	if err != nil {
		t.Fatalf("ParseCodexModelsCache: %v", err)
	}
	if len(got) != 1 || got[0].ID != "gpt-6.2-sol" || got[0].DefaultEffort != "medium" ||
		!slices.Equal(got[0].Efforts, []string{"low", "medium", "high"}) {
		t.Fatalf("unexpected codex models: %+v", got)
	}
}

func TestClaudeModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"model":"claude-opus-5-5","effortLevel":"high"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ClaudeModels(t.TempDir())
	if got.Source != "static" || len(got.Models) != 5 {
		t.Fatalf("unexpected claude list: %+v", got)
	}
	if got.Models[0].ID != "claude-opus-5-5" || !got.Models[0].Default || got.Models[0].DefaultEffort != "high" {
		t.Fatalf("custom default model should be prepended: %+v", got.Models[0])
	}
}

func TestValidateModel(t *testing.T) {
	if err := ValidateModel("cursor", Model{Name: "gpt-5.3-codex-high"}); err != nil {
		t.Fatalf("cursor without effort should pass: %v", err)
	}
	if err := ValidateModel("cursor", Model{Name: "gpt-5.3-codex", Effort: "high"}); err == nil {
		t.Fatalf("cursor with effort should fail")
	}
	if err := ValidateModel("zcode", Model{Name: "foo"}); err == nil {
		t.Fatalf("zcode with model should fail")
	}
	if err := ValidateModel("antigravity", Model{Name: "gemini-4-neon", Effort: "high"}); err != nil {
		t.Fatalf("antigravity should pass: %v", err)
	}
	if err := ValidateModel("claude", Model{Name: "opus", Effort: "max"}); err != nil {
		t.Fatalf("claude should pass: %v", err)
	}
}
