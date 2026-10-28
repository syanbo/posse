package mobile

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/term"
)

// 技能与工具：没声明 posse.tools.v1 的连接看不到；会话文件还没出现时 found 为 false，
// 出现后统计到 MCP 调用，再列时只读新增的部分。
func TestSessionTools(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "s1.jsonl")
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	args := map[string]any{"agent": "claude", "sessionId": "posse-tools-test-missing", "transcriptPath": transcript}
	if hidden := p.next(p.call("session.tools", args)); hidden["ok"] != false {
		t.Fatalf("orca connection saw session.tools: %v", hidden)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.tools.v1"},
	})))
	if got := result(t, p.next(p.call("session.tools", args))); got["found"] != false {
		t.Fatalf("before transcript: %v", got)
	}

	line := `{"type":"assistant","timestamp":"2026-10-16T08:00:00.000Z","message":{"content":[{"type":"tool_use","name":"mcp__iphone-use__phone_tap","input":{}}]}}` + "\n"
	os.WriteFile(transcript, []byte(line), 0o644)
	got := result(t, p.next(p.call("session.tools", args)))
	mcp, _ := got["mcp"].([]any)
	events, _ := got["events"].([]any)
	if got["found"] != true || len(mcp) != 1 || len(events) != 1 {
		t.Fatalf("after transcript: %v", got)
	}
	if m := mcp[0].(map[string]any); m["server"] != "iphone-use" || m["count"] != float64(1) {
		t.Fatalf("mcp %v", m)
	}

	f, _ := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line)
	f.Close()
	got = result(t, p.next(p.call("session.tools", args)))
	if m := got["mcp"].([]any)[0].(map[string]any); m["count"] != float64(2) {
		t.Fatalf("incremental %v", m)
	}
}
