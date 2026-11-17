package daemon

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/history"
)

func TestHistoryResumeDuplicateIDUsesPath(t *testing.T) {
	d, _ := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	t.Cleanup(d.closeHistory)
	id := "44444444-4444-4444-8444-444444444444"
	paths := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(t.TempDir(), name)
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		slug := strings.ReplaceAll(strings.TrimPrefix(dir, "/"), "/", "-")
		path := filepath.Join(home, ".cursor", "projects", slug, "agent-transcripts", id, id+".jsonl")
		sum := md5.Sum([]byte(dir))
		meta := filepath.Join(home, ".cursor", "chats", hex.EncodeToString(sum[:]), id, "meta.json")
		for p, body := range map[string]string{
			path: `{"role":"user","message":{"content":[{"type":"text","text":"selected history"}]}}`,
			meta: `{"schemaVersion":1,"title":"` + name + `","cwd":"` + dir + `"}`,
		} {
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		paths[path] = dir
	}
	if d.refreshHistory(context.Background(), true, 10*time.Second) {
		t.Fatal("index timeout")
	}
	for path, dir := range paths {
		if err := os.Remove(dir); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(historyResumeParams{Session: id, Path: path})
		_, err := d.historyResume(context.Background(), raw)
		// 选中的两个目录都不存在，应各自报出对应目录，且不能启动任何 agent。
		if err == nil || !strings.Contains(err.Error(), dir) {
			t.Fatalf("selected %s: %v", path, err)
		}
	}
	if len(d.terms) != 0 {
		t.Fatal("invalid resume started terminal")
	}
}
