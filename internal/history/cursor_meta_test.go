package history

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cursor 的会话 ID 会跨目录重复：两个目录里同 ID 的会话各配各的 meta.json。
func TestCursorDuplicateIDPicksOwnMeta(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	id := "44444444-4444-4444-8444-444444444444"
	want := map[string]string{}
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(work, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		slug := strings.ReplaceAll(strings.TrimPrefix(dir, "/"), "/", "-")
		tr := filepath.Join(home, ".cursor", "projects", slug, "agent-transcripts", id, id+".jsonl")
		writeFile(t, tr, `{"role":"user","message":{"content":[{"type":"text","text":"hi"}]}}`)
		sum := md5.Sum([]byte(dir))
		meta := filepath.Join(home, ".cursor", "chats", hex.EncodeToString(sum[:]), id, "meta.json")
		writeFile(t, meta, `{"schemaVersion":1,"title":"`+name+`","cwd":"`+dir+`"}`)
		want[tr] = meta
	}
	n := 0
	for _, src := range discover(Roots{Home: home}) {
		if src.agent != "cursor" {
			continue
		}
		n++
		if src.meta != want[src.transcript] {
			t.Errorf("%s got meta %s, want %s", src.transcript, src.meta, want[src.transcript])
		}
	}
	if n != 2 {
		t.Fatalf("%d cursor sessions", n)
	}
}
