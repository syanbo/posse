package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/rpc"
)

func TestHistoryTrashRefreshesReplacedSession(t *testing.T) {
	d, _ := newTestDaemon(t)
	s := historyTrashFixture(t, d)
	body, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(strings.ReplaceAll(string(body), s.ID, "77777777-7777-4777-8777-777777777777"))
	if err := os.WriteFile(s.Path, body, 0600); err != nil {
		t.Fatal(err)
	}
	d.history.moveToTrash = func(string) (string, error) { t.Error("replacement moved"); return "", nil }
	raw, _ := json.Marshal(historyTrashParams{Session: s.ID, Path: s.Path})
	_, err = d.historyTrash(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeConflict)
	if got, err := os.ReadFile(s.Path); err != nil || string(got) != string(body) {
		t.Fatal("replacement changed")
	}
}

func TestHistoryTrashIncludesNewCodexPart(t *testing.T) {
	d, _ := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	t.Cleanup(d.closeHistory)
	dir := filepath.Join(home, ".codex", "sessions", "2026", "11", "14")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	first, second := filepath.Join(dir, "rollout-p1.jsonl"), filepath.Join(dir, "rollout-p1_p2.jsonl")
	body := `{"timestamp":"2026-11-14T10:00:00Z","type":"session_meta","payload":{"id":"p1","cwd":"/work","source":"cli","thread_source":"user","history_mode":"paginated"}}` + "\n" + `{"timestamp":"2026-11-14T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage","content":[{"type":"text","text":"target session"}]}}}` + "\n"
	if err := os.WriteFile(first, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if d.refreshHistory(context.Background(), true, 10*time.Second) {
		t.Fatal("index timeout")
	}
	if err := os.WriteFile(second, []byte(strings.ReplaceAll(body, "10:00:", "11:00:")), 0600); err != nil {
		t.Fatal(err)
	}
	moved := map[string]bool{}
	destination := t.TempDir()
	d.history.moveToTrash = func(path string) (string, error) {
		moved[path] = true
		dst := filepath.Join(destination, filepath.Base(path))
		return dst, os.Rename(path, dst)
	}
	raw, _ := json.Marshal(historyTrashParams{Session: "p1", Path: first})
	if _, err := d.historyTrash(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	if len(moved) != 2 || !moved[first] || !moved[second] {
		t.Fatalf("moved: %v", moved)
	}
}
