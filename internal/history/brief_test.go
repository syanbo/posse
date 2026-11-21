package history

import (
	"context"
	"strings"
	"testing"
)

func TestBriefRecentSessions(t *testing.T) {
	home := t.TempDir()
	dir := "/work/shop"
	older := "11111111-1111-4111-8111-111111111111"
	newer := "22222222-2222-4222-8222-222222222222"
	other := "33333333-3333-4333-8333-333333333333"
	writeFile(t, home+"/.claude/projects/-work-shop/"+older+".jsonl",
		`{"type":"user","cwd":"`+dir+`","sessionId":"`+older+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"old question"}}`,
		`{"type":"assistant","timestamp":"2026-10-01T10:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hidden thoughts"},{"type":"text","text":"old answer"},{"type":"tool_use","name":"Bash","input":{"command":"echo secret-tool"}}]}}`,
		`{"type":"user","timestamp":"2026-10-01T10:00:02Z","message":{"role":"user","content":[{"type":"tool_result","content":"12G of caches"}]}}`)
	writeFile(t, home+"/.claude/projects/-work-shop/"+newer+".jsonl",
		`{"type":"user","cwd":"`+dir+`/pkg","sessionId":"`+newer+`","timestamp":"2026-10-02T10:00:00Z","message":{"role":"user","content":"new question"}}`,
		`{"type":"assistant","timestamp":"2026-10-02T10:00:01Z","message":{"role":"assistant","content":[{"type":"text","text":"new answer"}]}}`,
		`{"type":"ai-title","aiTitle":"newer title","sessionId":"`+newer+`"}`)
	writeFile(t, home+"/.claude/projects/-work-other/"+other+".jsonl",
		`{"type":"user","cwd":"/work/other","sessionId":"`+other+`","timestamp":"2026-10-03T10:00:00Z","message":{"role":"user","content":"elsewhere"}}`)
	x := openIndex(t)
	update(t, x, home)

	got, err := x.Brief(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "posse history show <id>") || !strings.Contains(got, newer) || !strings.Contains(got, "newer title") || !strings.Contains(got, "new answer") {
		t.Fatalf("brief:\n%s", got)
	}
	if !strings.Contains(got, older) || !strings.Contains(got, "old answer") {
		t.Fatalf("older session missing:\n%s", got)
	}
	if strings.Contains(got, "hidden thoughts") || strings.Contains(got, "12G") || strings.Contains(got, "secret-tool") || strings.Contains(got, "elsewhere") {
		t.Fatalf("brief included thinking, tools, or another directory:\n%s", got)
	}
	if strings.Index(got, newer) > strings.Index(got, older) {
		t.Fatalf("newer session should come first:\n%s", got)
	}
	skipped, err := x.Brief(context.Background(), dir, newer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(skipped, newer) || !strings.Contains(skipped, older) {
		t.Fatalf("skip:\n%s", skipped)
	}
	empty, err := x.Brief(context.Background(), "/work/none", "")
	if err != nil || empty != "" {
		t.Fatalf("empty dir: %q %v", empty, err)
	}
}
