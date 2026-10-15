package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"hive/internal/history"
)

func TestSessionTrashPaths(t *testing.T) {
	home := t.TempDir()
	setHome(t, home)
	mk := func(path string, dir bool) string {
		p := filepath.Join(home, path)
		if dir {
			os.MkdirAll(p, 0o755)
		} else {
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, nil, 0o644)
		}
		return p
	}
	claude := mk(".claude/projects/-w/abc.jsonl", false)
	claudeDir := mk(".claude/projects/-w/abc", true)
	codex := mk(".codex/sessions/2026/10/15/rollout-x-c1.jsonl", false)
	grok := mk(".grok/sessions/%2Fw/g1/chat_history.jsonl", false)
	cursor := mk(".cursor/projects/w/agent-transcripts/k1/k1.jsonl", false)
	for _, tc := range []struct {
		s    history.Session
		want []string
	}{
		{history.Session{Agent: "claude", ID: "abc", Path: claude}, []string{claude, claudeDir}},
		{history.Session{Agent: "codex", ID: "c1", Path: codex}, []string{codex}},
		{history.Session{Agent: "grok", ID: "g1", Path: grok}, []string{filepath.Dir(grok)}},
		{history.Session{Agent: "cursor", ID: "k1", Path: cursor}, []string{filepath.Dir(cursor)}},
		// 目录名和会话 ID 对不上、或者不在各家目录下的，一律不动
		{history.Session{Agent: "grok", ID: "other", Path: grok}, nil},
		{history.Session{Agent: "claude", ID: "abc", Path: "/tmp/abc.jsonl"}, nil},
		{history.Session{Agent: "codex", ID: "c1", Path: filepath.Join(home, "Documents/x.jsonl")}, nil},
	} {
		if got := sessionTrashPaths(tc.s); !slices.Equal(got, tc.want) {
			t.Errorf("%s %s: got %v, want %v", tc.s.Agent, tc.s.Path, got, tc.want)
		}
	}
}
