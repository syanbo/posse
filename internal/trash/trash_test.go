package trash_test

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/trash"
)

func TestMoveInvalidPathPreservesSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	const content = "source must survive invalid paths"
	if err := os.WriteFile(source, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "relative.txt", source + "\x00suffix"} {
		destination, err := trash.Move(path)
		if err == nil || destination != "" {
			t.Errorf("Move(%q) = %q, %v; want empty destination and error", path, destination, err)
		}
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != content {
		t.Fatalf("source changed: content %q, error %v", got, err)
	}
}
