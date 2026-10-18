package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRotateLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.log")
	os.WriteFile(path, make([]byte, 10), 0o600)
	rotateLog(path, 10)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("rotated a log at the limit")
	}
	os.WriteFile(path+".1", []byte("older"), 0o600)
	os.WriteFile(path, make([]byte, 11), 0o600)
	rotateLog(path, 10)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("big log not moved away")
	}
	if b, _ := os.ReadFile(path + ".1"); len(b) != 11 {
		t.Fatalf("daemon.log.1 has %d bytes, want the 11-byte log", len(b))
	}
	rotateLog(filepath.Join(t.TempDir(), "missing.log"), 10)
}
