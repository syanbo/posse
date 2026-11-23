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
