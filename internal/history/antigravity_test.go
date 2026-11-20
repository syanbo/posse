package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const antigravitySID = "22222222-2222-4222-8222-222222222222"

func antigravityFixture(t *testing.T, home string) string {
	t.Helper()
	path := filepath.Join(home, ".gemini", "antigravity-cli", "brain", antigravitySID, ".system_generated", "logs", "transcript.jsonl")
	writeFile(t, path,
		`{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-18T07:00:00Z","content":"<USER_REQUEST>Fix widget</USER_REQUEST><ADDITIONAL_METADATA>private metadata</ADDITIONAL_METADATA><USER_SETTINGS_CHANGE>hidden settings</USER_SETTINGS_CHANGE>"}`,
		`{"step_index":1,"type":"PLANNER_RESPONSE","created_at":"2026-11-18T07:00:01Z","model":"gemini-example","thinking":"secret reasoning","input_tokens":100,"cache_read_tokens":250,"tool_calls":[{"name":"read_file","args":{"Path":"\"/repo/.agents/skills/check/SKILL.md\""}},{"name":"run_command","args":{"CommandLine":"\"git status\""}}]}`,
		`{"step_index":2,"type":"GENERIC","created_at":"2026-11-18T07:00:02Z","content":"tool result"}`,
		`{"step_index":3,"type":"PLANNER_RESPONSE","created_at":"2026-11-18T07:00:03Z","content":"Fixed widget","thinking":"hidden thoughts"}`,
		`{"step_index":4,"type":"INTERNAL","content":"discard this"}`,
	)
	writeFile(t, filepath.Join(home, ".gemini", "antigravity-cli", "cache", "conversation_metadata.json"), `{"conversations":{"`+antigravitySID+`":{"summary":{"ID":"`+antigravitySID+`","Title":"Widget fix","WorkspaceURIs":["file:///repo/a%20b"],"UpdatedAt":"2026-11-18T07:00:03Z"}}}}`)
	return path
}

// 行格式取自 agy 1.5.11 的真实会话：先读工具说明，再经 call_mcp_tool 调用；结果是紧跟的 GENERIC，出错时 status 为 ERROR。
func TestAntigravityMCPUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeFile(t, path,
		`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-20T09:07:09Z","tool_calls":[{"name":"read_file","args":{"Path":"\"/Users/x/.gemini/antigravity-cli/mcp/droidmirror/device_list.json\""}},{"name":"read_file","args":{"Path":"\"/Users/x/.gemini/antigravity-cli/mcp/droidmirror/device_info.json\""}}]}`,
		`{"step_index":2,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-20T09:07:10Z","content":"schema"}`,
		`{"step_index":3,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-20T09:07:10Z","content":"schema"}`,
		`{"step_index":4,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-20T09:07:10Z","tool_calls":[{"name":"call_mcp_tool","args":{"Arguments":"{}","ServerName":"\"droidmirror\"","ToolName":"\"device_list\""}},{"name":"call_mcp_tool","args":{"Arguments":"{\"url\":\"https://example.com/a\"}","ServerName":"\"droidmirror\"","ToolName":"\"device_info\""}}]}`,
		`{"step_index":5,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-20T09:07:16Z","content":"devices"}`,
		`{"step_index":6,"source":"MODEL","type":"GENERIC","status":"DONE","created_at":"2026-11-20T09:07:16Z","content":"info"}`,
		`{"step_index":7,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-20T09:07:16Z","tool_calls":[{"name":"call_mcp_tool","args":{"Arguments":"{}","ServerName":"\"droidmirror\"","ToolName":"\"screen_act\""}}]}`,
		`{"step_index":8,"source":"MODEL","type":"GENERIC","status":"ERROR","error":"probe failure","created_at":"2026-11-20T09:07:20Z","content":"Encountered error in step execution: probe failure"}`,
	)
	u, err := NewUsageScanner("antigravity", path).Scan()
	if err != nil {
		t.Fatal(err)
	}
	want := []MCPUsage{{Server: "droidmirror", Count: 2, Tools: []UsageItem{{Name: "device_info", Count: 1}, {Name: "device_list", Count: 1}}}}
	if !reflect.DeepEqual(u.MCP, want) {
		t.Fatalf("mcp: %+v", u.MCP)
	}
	var calls, failed int
	for _, e := range u.Events {
		if e.Server != "" {
			calls++
			if e.Failed {
				failed++
				if e.Tool != "screen_act" {
					t.Fatalf("wrong call marked failed: %+v", e)
				}
			}
		}
	}
	if calls != 3 || failed != 1 || len(u.Skills) != 0 {
		t.Fatalf("events: %+v skills: %+v", u.Events, u.Skills)
	}
	if len(u.Web) != 1 || u.Web[0].Host != "example.com" {
		t.Fatalf("web: %+v", u.Web)
	}
}

func TestAntigravityHistory(t *testing.T) {
	home := t.TempDir()
	path := antigravityFixture(t, home)
	t.Setenv("HOME", home)
	x := openIndex(t)
	update(t, x, home)
	sessions, err := x.Sessions(context.Background(), ListOptions{Agent: "antigravity"})
	if err != nil || len(sessions) != 1 {
		t.Fatal(sessions, err)
	}
	s := sessions[0]
	if s.ID != antigravitySID || s.Title != "Widget fix" || s.Cwd != "/repo/a b" || s.Model != "gemini-example" {
		t.Fatal(s)
	}
	if got := search(t, x, "secret", SearchOptions{}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := search(t, x, "private", SearchOptions{}); len(got) != 0 {
		t.Fatal(got)
	}
	if got := TranscriptPath("antigravity", antigravitySID, ""); got != path {
		t.Fatal(got)
	}
	c, ok := ReadContext("antigravity", path, "")
	if !ok || c.Used != 350 || c.Window != 0 {
		t.Fatal(c, ok)
	}
	u, err := NewUsageScanner("antigravity", path).Scan()
	if err != nil || len(u.Skills) != 1 || u.Skills[0].Name != "check" || len(u.CLI) != 1 || u.CLI[0].Name != "git" {
		t.Fatal(u, err)
	}
	page, err := ReadChatTail("antigravity", antigravitySID, path, 20, 0)
	if err != nil || len(page.Messages) != 4 {
		t.Fatal(page, err)
	}
	if page.Messages[0].Blocks[0].Text != "Fix widget" {
		t.Fatal(page.Messages[0])
	}
	before, _ := os.Stat(path)
	appendFile(t, path, `{"step_index":5,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-18T07:00:04Z","content":"Next turn"}`)
	msgs, end, err := ReadChatNew("antigravity", path, before.Size())
	if err != nil || len(msgs) != 1 || msgs[0].Blocks[0].Text != "Next turn" || end <= before.Size() {
		t.Fatal(msgs, end, err)
	}
	if err := CheckTrashSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if got := TrashPaths(home, s); !reflect.DeepEqual(got, []string{AntigravityBrainDir(path)}) {
		t.Fatal(got)
	}
	s.ID = "another"
	if CheckTrashSession(context.Background(), s) == nil || len(TrashPaths(home, s)) != 0 {
		t.Fatal("mismatched identity accepted")
	}
	meta := filepath.Join(home, ".gemini", "antigravity-cli", "cache", "conversation_metadata.json")
	writeFile(t, meta, `{"conversations":{"`+antigravitySID+`":{"is_internal":true}}}`)
	update(t, x, home)
	sessions, err = x.Sessions(context.Background(), ListOptions{Agent: "antigravity"})
	if err != nil || len(sessions) != 0 {
		t.Fatal("internal session indexed", sessions, err)
	}
}

func TestAntigravityFullTranscript(t *testing.T) {
	home := t.TempDir()
	path := antigravityFixture(t, home)
	r := map[string]any{"step_index": 9, "type": "PLANNER_RESPONSE", "created_at": "2026-11-18T07:10:00Z", "content": "short", "truncated_fields": []string{"content"}}
	b, _ := json.Marshal(r)
	appendFile(t, path, string(b))
	r["content"] = "complete response"
	delete(r, "truncated_fields")
	b, _ = json.Marshal(r)
	writeFile(t, filepath.Join(filepath.Dir(path), "transcript_full.jsonl"), string(b))
	page, err := ReadChatTail("antigravity", antigravitySID, path, 1, 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Blocks[0].Text != "complete response" {
		t.Fatal(page, err)
	}
	r["created_at"] = "2026-10-01T00:00:00Z"
	b, _ = json.Marshal(r)
	writeFile(t, filepath.Join(filepath.Dir(path), "transcript_full.jsonl"), string(b))
	page, err = ReadChatTail("antigravity", antigravitySID, path, 1, 0)
	if err != nil || page.Messages[0].Blocks[0].Text != "short" {
		t.Fatal("mismatched full row accepted", page, err)
	}
}

func TestAntigravityMedia(t *testing.T) {
	path := antigravityFixture(t, t.TempDir())
	appendFile(t, path, `{"step_index":10,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"image","media":[{"mime_type":"image/png","uri":"file:///tmp/a%20b.png"}]}`, `{"step_index":11,"type":"GENERIC","media":[{"mime_type":"video/mp4","uri":"file:///tmp/movie.mp4"},{"mime_type":"image/png","uri":"https://example.com/not-local.png"}]}`)
	images, err := NewImageScanner("antigravity", path).Scan()
	if err != nil || len(images) != 1 || images[0].Path != "/tmp/a b.png" || !images[0].Pasted {
		t.Fatal(images, err)
	}
	videos, err := NewVideoScanner("antigravity", path).Scan()
	if err != nil || len(videos) != 1 || !strings.HasSuffix(videos[0].Path, "movie.mp4") {
		t.Fatal(videos, err)
	}
}

func TestAntigravitySubagents(t *testing.T) {
	home := t.TempDir()
	antigravityFixture(t, home)
	base := filepath.Join(home, ".gemini", "antigravity-cli")
	db, err := sql.Open("sqlite", filepath.Join(base, "conversation_summaries.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		`CREATE TABLE conversation_summaries (conversation_id TEXT, parent_conversation_id TEXT, nesting_depth INTEGER)`,
		`CREATE TABLE conversation_parents (conversation_id TEXT, parent_id TEXT)`,
		`INSERT INTO conversation_parents VALUES ('` + antigravitySID + `', 'parent')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if got := discover(Roots{Home: home}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestAntigravityTrashSymlink(t *testing.T) {
	home := t.TempDir()
	path := antigravityFixture(t, home)
	dir := AntigravityBrainDir(path)
	outside := filepath.Join(t.TempDir(), "session")
	if err := os.Rename(dir, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if got := TrashPaths(home, Session{Agent: "antigravity", ID: antigravitySID, Path: path}); len(got) != 0 {
		t.Fatal(got)
	}
}
