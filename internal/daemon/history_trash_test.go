package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/rpc"
)

func historyTrashFixture(t *testing.T, d *Daemon) history.Session {
	t.Helper()
	root := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: root}
	t.Cleanup(d.closeHistory)
	id := "66666666-6666-4666-8666-666666666666"
	path := filepath.Join(root, ".claude", "projects", "-target", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","sessionId":"` + id + `","timestamp":"2026-11-14T10:00:00Z","message":{"role":"user","content":"trash target"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if d.refreshHistory(context.Background(), true, 10*time.Second) {
		t.Fatal("index timeout")
	}
	s, err := d.findHistoryPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDesktopHistoryTrash(t *testing.T) {
	d, _ := newTestDaemon(t)
	s := historyTrashFixture(t, d)
	destination := filepath.Join(t.TempDir(), "recovered.jsonl")
	var moves atomic.Int32
	d.history.moveToTrash = func(path string) (string, error) {
		moves.Add(1)
		if path != s.Path {
			t.Error("wrong target path")
		}
		return destination, os.Rename(path, destination)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, _ := json.Marshal(historyTrashParams{Session: s.ID, Path: s.Path})
	p := rpc.DesktopCall{Desktop: rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}, Sequence: 1, Method: "history.trash", Params: raw}
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", p, nil), rpc.CodeReadOnly)
	if moves.Load() != 0 {
		t.Fatal("viewer moved file")
	}
	p.Desktop = desktopTakeTest(t, d, "controller")
	if err := rpc.Call(ctx, socket, "desktop.call", p, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.Path); !os.IsNotExist(err) {
		t.Fatalf("source: %v", err)
	}
	if data, err := os.ReadFile(destination); err != nil || len(data) == 0 {
		t.Fatal("trash content lost")
	}
	idx, _ := d.historyIndex()
	if _, err := idx.FindPath(ctx, s.Path); !errors.Is(err, history.ErrNotFound) {
		t.Fatalf("index retained source: %v", err)
	}
	desktopTakeTest(t, d, "other")
	if err := rpc.Call(ctx, socket, "desktop.call", p, nil); err != nil {
		t.Fatal(err)
	}
	if moves.Load() != 1 {
		t.Fatal("duplicate request moved twice")
	}
	p.Sequence++
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", p, nil), rpc.CodeReadOnly)
}

func TestHistoryTrashRechecksRunning(t *testing.T) {
	d, w := newTestDaemon(t)
	s := historyTrashFixture(t, d)
	terminal, err := d.startTerminal(w, []string{"/bin/cat"}, "claude", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	terminal.SetAgent(host.AgentState{SessionID: s.ID})
	d.history.moveToTrash = func(string) (string, error) { t.Error("running session moved"); return "", nil }
	raw, _ := json.Marshal(historyTrashParams{Session: s.ID, Path: s.Path})
	_, err = d.historyTrash(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeConflict)
	if _, err = os.Stat(s.Path); err != nil {
		t.Fatal(err)
	}
}

func TestHistoryTrashFailureRefreshesPartialMove(t *testing.T) {
	d, _ := newTestDaemon(t)
	s := historyTrashFixture(t, d)
	companion := s.Path[:len(s.Path)-len(".jsonl")]
	if err := os.Mkdir(companion, 0700); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "recovered.jsonl")
	d.history.moveToTrash = func(path string) (string, error) {
		if path == companion {
			return "", errors.New("injected trash failure")
		}
		return destination, os.Rename(path, destination)
	}
	raw, _ := json.Marshal(historyTrashParams{Session: s.ID, Path: s.Path})
	if _, err := d.historyTrash(context.Background(), raw); err == nil {
		t.Fatal("partial failure reported success")
	}
	if _, err := os.Stat(companion); err != nil {
		t.Fatal("failed item not preserved")
	}
	idx, _ := d.historyIndex()
	if _, err := idx.FindPath(context.Background(), s.Path); !errors.Is(err, history.ErrNotFound) {
		t.Fatalf("partial move not reindexed: %v", err)
	}
}

func TestHistoryTrashSelectionAndRestoreBoundary(t *testing.T) {
	d, _ := newTestDaemon(t)
	s := historyTrashFixture(t, d)
	for _, method := range []rpc.Handler{d.historyTrash, d.historyResume} {
		raw, _ := json.Marshal(historyTrashParams{Session: "replaced", Path: s.Path})
		_, err := method(context.Background(), raw)
		desktopErrorCode(t, err, rpc.CodeConflict)
	}
	raw, _ := json.Marshal(historyTrashParams{Session: s.ID, Path: s.Path})
	d.restoreMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := d.historyTrash(ctx, raw); done <- err }()
	cancel()
	d.restoreMu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued deletion ignored cancel: %v", err)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Fatal(err)
	}
}

func TestCursorIDEChatIsDeleted(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: root}
	t.Cleanup(d.closeHistory)
	id := "66666666-6666-4666-8666-666666666666"
	dbPath := filepath.Join(root, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
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
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "composerData:"+id,
		`{"name":"IDE chat","lastUpdatedAt":1790500000000,"fullConversationHeadersOnly":[{"bubbleId":"b1","type":1}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:"+id+":b1", `{"type":1,"text":"hello from the editor"}`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if d.refreshHistory(context.Background(), true, 10*time.Second) {
		t.Fatal("index timeout")
	}
	path := filepath.Join(filepath.Dir(dbPath), "composer", id)
	s, err := d.findHistoryPath(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	other := "77777777-7777-4777-8777-777777777777"
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "composerData:"+other, `{"name":"keep"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "bubbleId:"+other+":b9", `{"type":1,"text":"stay"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO cursorDiskKV(key, value) VALUES(?, ?)`, "agentKv:"+id, `{"note":"not this chat"}`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if err := d.moveHistoryToTrash(context.Background(), historyTrashParams{Session: s.ID, Path: s.Path}); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cursorDiskKV WHERE key=? OR key LIKE ?`, "composerData:"+id, "bubbleId:"+id+":%").Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted chat keys = %d, %v", n, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM cursorDiskKV WHERE key=? OR key=? OR key=?`, "composerData:"+other, "bubbleId:"+other+":b9", "agentKv:"+id).Scan(&n); err != nil || n != 3 {
		t.Fatalf("other keys = %d, %v", n, err)
	}
	if _, err := d.findHistoryPath(context.Background(), path); err == nil {
		t.Fatal("deleted chat stayed in the index")
	}
}

func TestHistoryRefreshAfterOldScan(t *testing.T) {
	d, _ := newTestDaemon(t)
	s := historyTrashFixture(t, d)
	// 模拟删除前已开始、仍持有旧条目的索引轮次。
	old := make(chan struct{})
	d.history.mu.Lock()
	d.history.running = old
	d.history.mu.Unlock()
	destination := filepath.Join(t.TempDir(), "recovered.jsonl")
	if err := os.Rename(s.Path, destination); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.refreshHistoryFiles(context.Background()) }()
	d.history.mu.Lock()
	d.history.running = nil
	d.history.mu.Unlock()
	close(old)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	idx, _ := d.historyIndex()
	if _, err := idx.FindPath(context.Background(), s.Path); !errors.Is(err, history.ErrNotFound) {
		t.Fatalf("old index retained source: %v", err)
	}
}
