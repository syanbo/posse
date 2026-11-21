package daemon

import (
	"context"
	"crypto/md5"
	"database/sql"
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

// Cursor IDE 的对话在 state.vscdb 里，cursor-agent 恢复不了：直接说明，不报文件不存在。
func TestHistoryResumeCursorIDEChat(t *testing.T) {
	d, _ := newTestDaemon(t)
	home := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: home}
	t.Cleanup(d.closeHistory)
	const id = "55555555-5555-4555-8555-555555555555"
	path := filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "composer", id)
	raw, _ := json.Marshal(historyResumeParams{Session: id, Path: path})
	db, err := sql.Open("sqlite", filepath.Join(filepath.Dir(filepath.Dir(path)), "state.vscdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0700); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)`,
		`INSERT INTO cursorDiskKV VALUES('composerData:` + id + `', '{"name":"IDE chat","fullConversationHeadersOnly":[{"bubbleId":"b1","type":1}]}')`,
		`INSERT INTO cursorDiskKV VALUES('bubbleId:` + id + `:b1', '{"type":1,"text":"hello"}')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	if d.refreshHistory(context.Background(), true, 10*time.Second) {
		t.Fatal("index timeout")
	}
	_, err = d.historyResume(context.Background(), raw)
	if err == nil || !strings.Contains(err.Error(), "Cursor IDE chats can't be resumed") {
		t.Fatalf("resume IDE chat: %v", err)
	}
	if len(d.terms) != 0 {
		t.Fatal("IDE chat resume started terminal")
	}
}
