//go:build !darwin

package trash_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/trash"
)

func TestMoveUnsupportedPreservesSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	const content = "source must survive unsupported platforms"
	if err := os.WriteFile(source, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	destination, err := trash.Move(source)
	if err == nil || !strings.Contains(err.Error(), "not supported") || destination != "" {
		t.Fatalf("Move = %q, %v; want unsupported error", destination, err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != content {
		t.Fatalf("source changed: content %q, error %v", got, err)
	}
}
