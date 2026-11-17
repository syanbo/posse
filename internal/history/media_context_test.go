package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func boundedImageLine(data []byte) []byte {
	return []byte(fmt.Sprintf("{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"tool_result\",\"content\":[{\"type\":\"image\",\"source\":{\"type\":\"base64\",\"data\":\"%s\"}}]}]}}\n", base64.StdEncoding.EncodeToString(data)))
}

func TestMediaContextIncrementalAndIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := boundedImageLine([]byte("first image"))
	if err := os.WriteFile(path, line, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewImageScanner("claude", path)
	images, err := s.ScanContext(t.Context())
	if err != nil || len(images) != 1 || images[0].LineHash != sha256.Sum256(line) {
		t.Fatalf("scan = %+v %v", images, err)
	}
	first := images[0]
	if b, err := ReadToolImageContext(t.Context(), path, first, 50<<20); err != nil || string(b) != "first image" {
		t.Fatalf("read = %q %v", b, err)
	}
	if _, err := ReadToolImageContext(t.Context(), path, first, 1); err == nil {
		t.Fatal("decoded limit ignored")
	}
	badDigest := first
	badDigest.Digest = sha256.Sum256([]byte("different"))
	if _, err := ReadToolImageContext(t.Context(), path, badDigest, 50<<20); !errors.Is(err, errImageGone) {
		t.Fatalf("digest = %v", err)
	}
	if err := os.WriteFile(path, append(line, boundedImageLine([]byte("second image"))...), 0o600); err != nil {
		t.Fatal(err)
	}
	images, err = s.ScanContext(t.Context())
	if err != nil || len(images) != 2 || images[1].Offset != int64(len(line)) {
		t.Fatalf("incremental = %+v %v", images, err)
	}
	if images, err = s.ScanContext(t.Context()); err != nil || len(images) != 2 {
		t.Fatalf("unchanged = %d %v", len(images), err)
	}
	if err := os.WriteFile(path, boundedImageLine([]byte("other image")), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadToolImageContext(t.Context(), path, first, 50<<20); !errors.Is(err, errImageGone) {
		t.Fatalf("rewritten = %v", err)
	}
	if images, err = s.ScanContext(t.Context()); err != nil || len(images) != 1 || images[0].LineHash == first.LineHash {
		t.Fatalf("rewrite rescan = %+v %v", images, err)
	}
}

func TestMediaContextLimitsAndCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	line := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"one","name":"tool","input":{"file_path":"` + strings.Repeat("x", MediaScanLimit) + `"}}]}}` + "\n"
	os.WriteFile(path, []byte(line), 0o600)
	if _, err := NewImageScanner("claude", path).ScanContext(t.Context()); !errors.Is(err, ErrMediaScanLimit) {
		t.Fatalf("metadata limit = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewImageScanner("claude", path).ScanContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan cancel = %v", err)
	}
	if _, err := NewVideoScanner("claude", path).ScanContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("video cancel = %v", err)
	}
	if _, err := ReadToolImageContext(ctx, path, ToolImage{}, 50<<20); !errors.Is(err, context.Canceled) {
		t.Fatalf("read cancel = %v", err)
	}
	if _, err := ReadMediaBytes(t.Context(), bytes.NewReader([]byte("oversized")), 2); err == nil {
		t.Fatal("stream limit ignored")
	}
	if _, err := NewImageScanner("claude", t.TempDir()).ScanContext(t.Context()); err == nil {
		t.Fatal("directory scanned")
	}
}
