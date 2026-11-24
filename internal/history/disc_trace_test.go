package history

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanTraceRangeAndClassify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	round1 := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-11-24T08:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Read","input":{"file_path":"/Users/demo/repo/a/b/old.go"}}]}}`,
		`{"type":"user","timestamp":"2026-11-24T08:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":"ok"}]}}`,
	}, "\n") + "\n"
	round2 := strings.Join([]string{
		`{"type":"assistant","timestamp":"2026-11-24T08:01:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c2","name":"Read","input":{"file_path":"/Users/demo/repo/internal/store/discussion.go"}}]}}`,
		`{"type":"assistant","timestamp":"2026-11-24T08:01:02Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c3","name":"Read","input":{"file_path":"/Users/demo/repo/internal/store/discussion.go"}}]}}`,
		`{"type":"assistant","timestamp":"2026-11-24T08:01:03Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c4","name":"Bash","input":{"command":"go test ./internal/store"}}]}}`,
		`{"type":"user","timestamp":"2026-11-24T08:01:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"c4","is_error":true,"content":"fail"}]}}`,
		`{"type":"assistant","timestamp":"2026-11-24T08:01:05Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c5","name":"mcp__github__get_issue","input":{"id":"42"}}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(round1+round2), 0o600); err != nil {
		t.Fatal(err)
	}

	// 只扫第 2 轮偏移范围：不能混入第 1 轮的 old.go
	start := int64(len(round1))
	sum, count, next, err := ScanTrace("claude", "s1", path, start, 0)
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 || next != int64(len(round1)+len(round2)) {
		t.Fatalf("count=%d next=%d want 4, %d", count, next, len(round1)+len(round2))
	}
	if sum.Tools != 2 || sum.Commands != 1 || sum.MCP != 1 {
		t.Fatalf("counts %+v", sum)
	}
	if len(sum.Events) != 3 {
		t.Fatalf("merged events %+v", sum.Events)
	}
	if sum.Events[0].Name != "Read" || sum.Events[0].Count != 2 || sum.Events[0].Detail != "internal/store/discussion.go" {
		t.Fatalf("event[0] %+v", sum.Events[0])
	}
	if sum.Events[1].Kind != "cli" || !sum.Events[1].Failed || sum.Events[1].Detail != "go test ./internal/store" {
		t.Fatalf("event[1] %+v", sum.Events[1])
	}
	if sum.Events[2].Kind != "mcp" || sum.Events[2].Name != "github · get_issue" || sum.Events[2].Detail != "42" {
		t.Fatalf("event[2] %+v", sum.Events[2])
	}
}
