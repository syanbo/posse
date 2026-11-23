package mobile

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/term"
)

func (h *fakeHost) ZCodeSessionTools(ctx context.Context, id string) (history.SessionUsage, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return history.SessionUsage{}, err
	}
	return history.ReadZCodeUsage(ctx, agent.ZCodeDatabase(home), id)
}

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

	path := filepath.Join(t.TempDir(), "zcode.sqlite")
	t.Setenv("ZCODE_SESSION_DB_PATH", path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const sid = "sess_29b8ce03-b68a-4c32-b17d-0cf68ec2174f"
	if _, err := db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT, time_deleted INTEGER);
		CREATE TABLE part(id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT);
		CREATE TABLE model_usage(id TEXT PRIMARY KEY, session_id TEXT, query_source TEXT, started_at INTEGER,
			input_tokens INTEGER, output_tokens INTEGER, cache_creation_input_tokens INTEGER, cache_read_input_tokens INTEGER,
			provider_total_tokens INTEGER, computed_total_tokens INTEGER);
		INSERT INTO session VALUES (?, '/tmp/project', NULL);
		INSERT INTO part VALUES ('tool', ?, 1, 1, '{"type":"tool","tool":"Read","state":{"status":"completed"}}');
		INSERT INTO model_usage VALUES ('req', ?, 'main_turn', 1, 30, 15, 0, 0, 45, 45);`, sid, sid, sid); err != nil {
		t.Fatal(err)
	}
	got = result(t, p.next(p.call("session.tools", map[string]any{"agent": "zcode", "sessionId": sid})))
	tools, _ := got["tools"].([]any)
	tokens, _ := got["tokens"].(map[string]any)
	if got["found"] != true || len(tools) != 1 || tools[0].(map[string]any)["name"] != "Read" || tokens["total"] != float64(45) {
		t.Fatalf("ZCode tools and tokens: %v", got)
	}
}
