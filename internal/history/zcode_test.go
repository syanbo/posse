package history

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/agent"
)

func TestZCodeHistorySnapshotIncludesWAL(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "original.sqlite")
	dest := filepath.Join(dir, "snapshot.sqlite")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE texts (value TEXT); INSERT INTO texts VALUES ('in wal')`); err != nil {
		t.Fatal(err)
	}
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	before, wal := read(source), read(source+"-wal")
	if err := snapshotZCodeDB(t.Context(), source, dest); err != nil {
		t.Fatal(err)
	}
	copy, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var text string
	if err := copy.QueryRow(`SELECT value FROM texts`).Scan(&text); err != nil || text != "in wal" {
		t.Fatalf("snapshot text %q: %v", text, err)
	}
	if _, err := copy.Exec(`UPDATE texts SET value='changed'`); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, read(source)) || !bytes.Equal(wal, read(source+"-wal")) {
		t.Fatal("opening and modifying the snapshot changed the original database or WAL")
	}
}

const failedZCodeFixture = "sess_01234567-89ab-4cde-8f01-23456789abcd"
const healthyZCodeFixture = "sess_01234567-89ab-4cde-8f01-23456789abce"

func TestZCodeProtocolFixture(t *testing.T) {
	if os.Args[len(os.Args)-1] != "zcode-protocol-fixture" {
		return
	}
	input := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	for input.Scan() {
		var req struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
			} `json:"params"`
		}
		if json.Unmarshal(input.Bytes(), &req) != nil {
			os.Exit(2)
		}
		var result any
		switch req.Method {
		case "session/list":
			result = map[string]any{"sessions": []zcodeSummary{{ID: failedZCodeFixture}, {ID: healthyZCodeFixture}}}
		case "session/messages":
			message := "healthy fixture"
			if req.Params.SessionID == failedZCodeFixture {
				message = strings.Repeat("x", zcodeFrameLimit)
			}
			result = map[string]any{"messages": []map[string]any{{
				"info":  map[string]string{"messageId": "m1", "sessionId": req.Params.SessionID, "role": "user"},
				"parts": []map[string]string{{"type": "text", "text": message}},
			}}}
		}
		if err := out.Encode(map[string]any{"id": req.ID, "result": result}); err != nil {
			os.Exit(2)
		}
	}
	os.Exit(0)
}

func TestZCodeHistoryContinuesAfterOversizedSession(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "original.sqlite")
	t.Setenv("ZCODE_SESSION_DB_PATH", source)
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE fixture (id INTEGER)`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(dir, "zcode")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\nexec "+agent.Quote(bin)+" -test.run '^TestZCodeProtocolFixture$' -- zcode-protocol-fixture\n"), 0700); err != nil {
		t.Fatal(err)
	}
	x, err := Open(filepath.Join(dir, "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	err = x.Update(t.Context(), Roots{Home: dir, ZCodeCLI: cli}, nil)
	if err == nil || !strings.Contains(err.Error(), failedZCodeFixture) {
		t.Fatalf("oversized session must be reported: %v", err)
	}
	var sid string
	if err := x.db.QueryRow(`SELECT session_id FROM sessions WHERE agent='zcode'`).Scan(&sid); err != nil || sid != healthyZCodeFixture {
		t.Fatalf("healthy session did not survive: %q, %v", sid, err)
	}
}

func TestZCodeUsageReadsNativeSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zcode.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0;
		CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT, time_deleted INTEGER);
		CREATE TABLE part(id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT);
		CREATE TABLE tool_usage(id TEXT PRIMARY KEY, session_id TEXT, tool_call_id TEXT, tool_name TEXT, status TEXT, started_at INTEGER);
		CREATE TABLE model_usage(id TEXT PRIMARY KEY, session_id TEXT, query_source TEXT, started_at INTEGER,
			input_tokens INTEGER, output_tokens INTEGER, cache_creation_input_tokens INTEGER, cache_read_input_tokens INTEGER,
			provider_total_tokens INTEGER, computed_total_tokens INTEGER);`); err != nil {
		t.Fatal(err)
	}
	sid := healthyZCodeFixture
	if _, err := db.Exec(`INSERT INTO session VALUES (?, '/tmp/project', NULL), (?, '/tmp/project', 1)`, sid, failedZCodeFixture); err != nil {
		t.Fatal(err)
	}
	for i, data := range []string{
		`{"type":"tool","callID":"skill","tool":"Skill","state":{"status":"completed","input":{"skill":"review"}}}`,
		`{"type":"tool","callID":"shell","tool":"Bash","state":{"status":"error","input":{"command":"git status"}}}`,
		`{"type":"tool","callID":"shell","tool":"Bash","state":{"status":"completed","input":{"command":"git status"}}}`,
		`{"type":"tool","callID":"search","tool":"custom","state":{"status":"completed","input":{"url":"https://example.com"},
			"metadata":{"display":{"kind":"mcp_tool","serverName":"docs","toolName":"fetch"}}}}`,
		`{"type":"tool","callID":"denied","tool":"mcp__docs__fetch","state":{"status":"completed","metadata":{"providerIsError":true}}}`,
		`{"type":"tool","callID":"running","tool":"Bash","state":{"status":"running","input":{"command":"git log"}}}`,
		`{"type":"tool","callID":"screenshot","tool":"Screenshot","state":{"status":"completed","input":{"source":"browser_turn_end"}}}`,
	} {
		if _, err := db.Exec(`INSERT INTO part VALUES (?, ?, ?, ?, ?)`, i, sid, i, i, data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO tool_usage VALUES
		('1', ?, 'skill', 'Skill', 'completed', 0),
		('2', ?, 'shell', 'Bash', 'completed', 2),
		('3', ?, 'search', 'custom', 'completed', 3),
		('4', ?, 'denied', 'mcp__docs__fetch', 'error', 4),
		('5', ?, 'running', 'Bash', 'running', 5),
		('6', ?, 'no-part', 'Write', 'completed', 7)`, sid, sid, sid, sid, sid, sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_usage VALUES
		('m1', ?, 'main_turn', 1, 100, 10, 0, 20, 110, 110),
		('m2', ?, 'main_turn', 2, 130, 20, 0, 40, 150, 150),
		('m3', ?, 'title_generation', 3, 5, 3, 0, 7, 15, 15)`, sid, sid, sid); err != nil {
		t.Fatal(err)
	}
	u, err := ReadZCodeUsage(t.Context(), path, sid)
	if err != nil || !u.Found || u.Tokens == nil || *u.Tokens != (TokenUsage{Total: 175, Input: 142, Output: 33, Requests: 3}) {
		t.Fatalf("session tokens: %+v, %v", u, err)
	}
	if len(u.Tools) != 3 || u.Tools[0] != (UsageItem{Name: "Bash", Count: 1}) || u.Tools[2] != (UsageItem{Name: "Write", Count: 1}) ||
		len(u.Skills) != 1 || u.Skills[0] != (UsageItem{Name: "review", Count: 1}) ||
		len(u.MCP) != 1 || u.MCP[0].Server != "docs" || u.MCP[0].Count != 1 ||
		len(u.CLI) != 1 || u.CLI[0].Name != "git" || u.CLI[0].Count != 1 ||
		len(u.Web) != 1 || u.Web[0].Host != "example.com" || !u.Events[len(u.Events)-1].Failed {
		t.Fatalf("session tools: %+v", u.Usage)
	}
	if deleted, err := ReadZCodeUsage(t.Context(), path, failedZCodeFixture); err != nil || deleted.Found {
		t.Fatalf("deleted session: %+v, %v", deleted, err)
	}
	const fork = "sess_01234567-89ab-4cde-8f01-23456789abcf"
	if _, err := db.Exec(`INSERT INTO session VALUES (?, '/tmp/project', NULL);
		INSERT INTO part SELECT 'fork-part', ?, time_created, time_updated, data FROM part WHERE id = '2'`, fork, fork); err != nil {
		t.Fatal(err)
	}
	u, err = ReadZCodeUsage(t.Context(), path, fork)
	if err != nil || !u.Found || len(u.Tools)+len(u.CLI)+len(u.Events) != 0 || u.Tokens != nil {
		t.Fatalf("fork inherited usage: %+v, %v", u, err)
	}
}

func TestZCodeUsageReadsPreLedgerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY, directory TEXT);
		CREATE TABLE part(id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, time_updated INTEGER, data TEXT);
		INSERT INTO session VALUES (?, '/tmp/project');
		INSERT INTO part VALUES ('1', ?, 1, 1, '{"type":"tool","tool":"Read","state":{"status":"completed"}}')`,
		healthyZCodeFixture, healthyZCodeFixture); err != nil {
		t.Fatal(err)
	}
	u, err := ReadZCodeUsage(t.Context(), path, healthyZCodeFixture)
	if err != nil || !u.Found || len(u.Tools) != 1 || u.Tools[0].Name != "Read" || u.Tokens != nil {
		t.Fatalf("legacy session: %+v, %v", u, err)
	}
}

func TestZCodeRuntimeSessionRequiresUniqueForegroundRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "zcode.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const child = "sess_01234567-89ab-4cde-8f01-23456789abcf"
	if _, err := db.Exec(`CREATE TABLE session(id TEXT PRIMARY KEY, title TEXT, task_type TEXT, time_deleted INTEGER);
		CREATE TABLE session_runtime_presence(session_id TEXT, process_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session VALUES (?, 'old', 'interactive', NULL), (?, 'current', 'fork', NULL), (?, 'child', 'subagent_child', NULL)`,
		failedZCodeFixture, healthyZCodeFixture, child); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO session_runtime_presence VALUES (?, 41), (?, 42), (?, 42), (?, 42)`,
		failedZCodeFixture, healthyZCodeFixture, healthyZCodeFixture, child); err != nil {
		t.Fatal(err)
	}
	if id, title, ambiguous := ZCodeRuntimeSession(t.Context(), path, 42); id != healthyZCodeFixture || title != "current" || ambiguous {
		t.Fatalf("foreground session = %q, %q, ambiguous=%v", id, title, ambiguous)
	}
	if _, err := db.Exec(`INSERT INTO session_runtime_presence VALUES (?, 42)`, failedZCodeFixture); err != nil {
		t.Fatal(err)
	}
	if id, _, ambiguous := ZCodeRuntimeSession(t.Context(), path, 42); id != "" || !ambiguous {
		t.Fatalf("foreground session = %q, ambiguous=%v", id, ambiguous)
	}
}
