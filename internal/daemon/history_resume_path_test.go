package daemon

import (
	"context"
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/rpc"
	"posse/internal/store"
)

func TestAntigravityResumeUsesKnownDirectory(t *testing.T) {
	d, w := newTestDaemon(t)
	d.cfg.HistoryRoots = &history.Roots{Home: t.TempDir()}
	t.Cleanup(d.closeHistory)
	known, explicit, native := filepath.Join(w.Path, "known' dir"), filepath.Join(w.Path, "explicit"), filepath.Join(w.Path, "native")
	for _, dir := range []string{known, explicit, native} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	other := store.Workspace{ID: store.NewID("w_"), ProjectID: w.ProjectID, Name: "explicit", Path: explicit, Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddWorkspace(other); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	shell := filepath.Join(bin, "sh")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nexport PATH="+agent.Quote(bin)+":/usr/bin:/bin\nexec /bin/sh -c \"$4\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte("#!/bin/sh\nprintf 'TEST_AGY_CWD=%s\\n' \"$(pwd -P)\"\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	d.launcher.Home = d.cfg.Home

	tests := []struct {
		name, savedDir, nativeDir, workspace, wantDir string
	}{
		{name: "saved", savedDir: known, wantDir: known},
		{name: "native wins", savedDir: known, nativeDir: native, wantDir: native},
		{name: "explicit workspace wins over saved directory", savedDir: known, workspace: other.ID, wantDir: explicit},
		{name: "removed directory", savedDir: filepath.Join(w.Path, "removed")},
		{name: "forgotten workspace", savedDir: t.TempDir()},
		{name: "unknown directory"},
	}
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root := d.cfg.HistoryRoots.Home
	summaries := map[string]any{}
	sessions := make([]string, len(tests))
	for i, c := range tests {
		id := fmt.Sprintf("11111111-1111-4111-8111-%012d", i)
		sessions[i] = id
		write(history.AntigravityTranscriptPath(root, id), `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","created_at":"2026-11-18T07:00:00Z","content":"Antigravity work"}`+"\n")
		if c.savedDir != "" {
			if err := d.store.RememberAntigravityDir(id, c.savedDir); err != nil {
				t.Fatal(err)
			}
		}
		if c.nativeDir != "" {
			u := url.URL{Scheme: "file", Path: c.nativeDir}
			summaries[id] = map[string]any{"summary": map[string]any{"ID": id, "WorkspaceURIs": []string{u.String()}}}
		}
	}
	b, _ := json.Marshal(map[string]any{"conversations": summaries})
	write(filepath.Join(history.AntigravityBase(root), "cache", "conversation_metadata.json"), string(b))
	if _, err := d.historyIndexNow(t.Context(), json.RawMessage(`{"wait":true}`)); err != nil {
		t.Fatal(err)
	}
	for i, c := range tests {
		t.Run(c.name, func(t *testing.T) {
			id := sessions[i]
			p, _ := json.Marshal(historyResumeParams{Session: id, Path: history.AntigravityTranscriptPath(root, id), Workspace: c.workspace})
			r, err := d.historyResume(t.Context(), p)
			if c.wantDir == "" {
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != rpc.CodeWorkspaceRequired {
					t.Fatalf("expected workspace picker, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info := r.(historyResumeResult)
			wantWS := w.ID
			if c.workspace != "" {
				wantWS = c.workspace
			}
			if info.WorkspaceID != wantWS || info.AgentState == nil || info.AgentState.SessionID != id {
				t.Fatalf("resumed session: %+v", info)
			}
			waitScreen(t, d.liveSession(info.ID), "TEST_AGY_CWD="+resolvePath(c.wantDir))
			if again, err := d.historyResume(t.Context(), p); err != nil || !again.(historyResumeResult).AlreadyOpen || again.(historyResumeResult).ID != info.ID {
				t.Fatalf("resume did not reuse the terminal: %+v, %v", again, err)
			}
		})
	}
}

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
