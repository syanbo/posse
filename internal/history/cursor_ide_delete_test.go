package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteCursorIDEChatRemovesOnlyThatChat(t *testing.T) {
	home := t.TempDir()
	id := "66666666-6666-4666-8666-666666666666"
	other := "77777777-7777-4777-8777-777777777777"
	dbPath := filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
	db := openCursorDeleteFixture(t, dbPath)
	execCursor := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "composerData:"+id, `{"name":"gone"}`)
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:"+id+":b1", `{"type":1}`)
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "checkpointId:"+id+":c1", `{}`)
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "messageRequestContext:"+id+":WARM_SUBMIT", `{}`)
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "composerData:"+other, `{"name":"stay"}`)
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:"+other+":b1", `{"type":1}`)
	execCursor(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "agentKv:"+id, `{"note":"not a chat row"}`)
	execCursor(`CREATE TABLE composerHeaders (composerId TEXT PRIMARY KEY, value TEXT)`)
	execCursor(`INSERT INTO composerHeaders(composerId, value) VALUES(?, ?)`, id, `{"name":"gone"}`)
	execCursor(`INSERT INTO composerHeaders(composerId, value) VALUES(?, ?)`, other, `{"name":"stay"}`)
	execCursor(`CREATE TABLE ItemTable (key TEXT PRIMARY KEY, value TEXT)`)
	execCursor(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, "panes",
		`[{"id":"workbench.panel.aichat.view.`+id+`","isHidden":false},{"id":"workbench.panel.aichat.view.`+other+`","isHidden":true}]`)
	execCursor(`INSERT INTO ItemTable(key, value) VALUES(?, ?)`, "unrelated", `plain `+id)
	db.Close()

	if err := DeleteCursorIDEChat(context.Background(), home, id); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cursorDiskKV WHERE key LIKE ?`, "%"+id+"%").Scan(&n); err != nil || n != 1 {
		t.Fatalf("remaining keys containing the id = %d, %v", n, err)
	}
	var key string
	if err := db.QueryRow(`SELECT key FROM cursorDiskKV WHERE instr(key, ?) > 0`, id).Scan(&key); err != nil || key != "agentKv:"+id {
		t.Fatalf("kept key %q, %v", key, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM composerHeaders WHERE composerId=?`, id).Scan(&n); err != nil || n != 0 {
		t.Fatalf("header %d, %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM composerHeaders WHERE composerId=?`, other).Scan(&n); err != nil || n != 1 {
		t.Fatalf("other header %d, %v", n, err)
	}
	var panes, unrelated string
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key='panes'`).Scan(&panes); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(panes), &got); err != nil || len(got) != 1 || got[0]["id"] != "workbench.panel.aichat.view."+other {
		t.Fatal(panes)
	}
	if err := db.QueryRow(`SELECT value FROM ItemTable WHERE key='unrelated'`).Scan(&unrelated); err != nil || unrelated != "plain "+id {
		t.Fatalf("unrelated value %q, %v", unrelated, err)
	}
	if err := DeleteCursorIDEChat(context.Background(), home, "not-a-uuid"); err == nil {
		t.Fatal("accepted a bad id")
	}
	missing := t.TempDir()
	if err := DeleteCursorIDEChat(context.Background(), missing, id); err == nil {
		t.Fatal("missing database succeeded")
	}
	if _, err := os.Stat(filepath.Join(missing, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func openCursorDeleteFixture(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}
	return db
}
