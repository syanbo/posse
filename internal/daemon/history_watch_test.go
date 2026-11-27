package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/history"
)

func TestHistoryPokeWhileFresh(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: root}
	t.Cleanup(d.closeHistory)
	if d.refreshHistory(context.Background(), true, 10*time.Second) {
		t.Fatal("index timeout")
	}
	gen := d.historyGeneration()
	if gen == 0 {
		t.Fatal("generation not bumped")
	}
	d.pokeHistory()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if d.historyGeneration() > gen {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("poke did not reindex while the last run was still fresh")
}

func TestHistoryWatchStopsWhenPageCloses(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := t.TempDir()
	d.history.watchRoots = func() []string { return []string{dir} }
	t.Cleanup(d.closeHistory)
	d.noteHistoryPage()
	waitHistoryWatch(t, d, true)
	d.history.mu.Lock()
	d.history.pageUntil = time.Now().Add(-time.Second)
	d.history.mu.Unlock()
	waitHistoryWatch(t, d, false)
}

func TestHistoryPageFlagStartsWatch(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := t.TempDir()
	d.history.watchRoots = func() []string { return []string{dir} }
	t.Cleanup(d.closeHistory)
	raw, _ := json.Marshal(map[string]any{"history": true})
	res, err := d.status(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.(statusResult); !ok {
		t.Fatalf("status %T", res)
	}
	waitHistoryWatch(t, d, true)
	if _, err := d.status(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	d.history.mu.Lock()
	still := d.history.watchStop != nil
	d.history.mu.Unlock()
	if !still {
		t.Fatal("one status without the flag stopped the watch inside the grace period")
	}
}

func TestHistoryWatchIndexesWrite(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: root}
	dir := filepath.Join(root, ".claude", "projects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	d.history.watchRoots = func() []string { return []string{dir} }
	prev := historyWatchDebounce
	historyWatchDebounce = 50 * time.Millisecond
	t.Cleanup(func() { historyWatchDebounce = prev })
	t.Cleanup(d.closeHistory)
	d.noteHistoryPage()
	waitHistoryWatch(t, d, true)

	id := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(dir, "-work-shop", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"/work/shop","sessionId":"` + id + `","timestamp":"2026-11-22T01:00:00Z","message":{"role":"user","content":"just written"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if d.historyGeneration() > 0 {
			idx, err := d.historyIndex()
			if err != nil {
				t.Fatal(err)
			}
			ss, err := idx.Sessions(context.Background(), history.ListOptions{Cwd: "/work/shop"})
			if err != nil {
				t.Fatal(err)
			}
			if len(ss) == 1 && ss[0].ID == id {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("watch did not index the new session (gen %d)", d.historyGeneration())
}

func TestHistoryRootsSignatureSymlinkAndCancel(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	linkRoot := filepath.Join(linkParent, "projects")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	before, ok := historyRootsSignature(context.Background(), []string{linkRoot})
	if !ok {
		t.Fatal("initial signature canceled")
	}
	if err := os.WriteFile(filepath.Join(realRoot, "session.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, ok := historyRootsSignature(context.Background(), []string{linkRoot})
	if !ok {
		t.Fatal("second signature canceled")
	}
	if after == before {
		t.Fatal("symlinked root did not detect new jsonl file")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, ok := historyRootsSignature(ctx, []string{linkRoot}); ok {
		t.Fatal("canceled context returned ok=true")
	}
}

func waitHistoryWatch(t *testing.T, d *Daemon, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d.history.mu.Lock()
		on := d.history.watchStop != nil
		d.history.mu.Unlock()
		if on == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("watch running = %v, want %v", !want, want)
}
