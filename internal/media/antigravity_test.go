package media

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/history"
)

func TestAntigravityArtifacts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "brain", "session")
	transcript := filepath.Join(root, ".system_generated", "logs", "transcript.jsonl")
	for _, name := range []string{".user_uploaded/paste.png", "result/clip.mp4", "result/photo.png", ".hidden/skip.png", ".system_generated/logs/media/skip.png"} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "result", "photo.png"), filepath.Join(root, "alias.png")); err != nil {
		t.Fatal(err)
	}
	pasted, made, err := antigravityMedia(context.Background(), transcript)
	if err != nil || len(pasted) != 1 || len(made) != 2 || pasted[0].Tag != TagPasted {
		t.Fatal(pasted, made, err)
	}
	made = append(made, Item{Tool: &history.ToolImage{Path: pasted[0].Path, OnDisk: true}})
	pasted, made = uniqueAntigravityMedia(pasted, made)
	if len(pasted) != 1 || len(made) != 2 {
		t.Fatal(pasted, made)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := antigravityMedia(ctx, transcript); err != context.Canceled {
		t.Fatal(err)
	}
}

func TestAntigravityUploadsNewestFirst(t *testing.T) {
	root := filepath.Join(t.TempDir(), "brain", "session")
	transcript := filepath.Join(root, ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(transcript), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, ".user_uploaded")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"a.png", "b.png"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(100+int64(i), 0)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
	}
	s := NewScanner("antigravity", "session", transcript)
	pasted, _ := s.Scan()
	if len(pasted) != 2 || pasted[0].Label != "b.png" {
		t.Fatalf("uploads: %+v", pasted)
	}
	pasted, _, err := s.ScanContext(context.Background())
	if err != nil || len(pasted) != 2 || pasted[0].Label != "b.png" {
		t.Fatalf("context uploads: %+v, %v", pasted, err)
	}
}
