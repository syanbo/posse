package history

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestWatchDirs(t *testing.T) {
	home := t.TempDir()
	keep := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := WatchDirs(home)
	if !slices.Equal(got, []string{keep}) {
		t.Fatalf("WatchDirs = %v", got)
	}
	if WatchDirs("") != nil {
		t.Fatal("empty home")
	}
}
