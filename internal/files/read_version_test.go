package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadFileVersionRawBaseline(t *testing.T) {
	dir := t.TempDir()
	path, link := filepath.Join(dir, "file"), filepath.Join(dir, "link")
	data := []byte("raw\r\nbytes\xff")
	if err := os.WriteFile(path, data, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", link); err != nil {
		t.Fatal(err)
	}
	content, version, err := ReadFileVersion(t.Context(), link)
	sum := sha256.Sum256(data)
	real, _ := filepath.EvalSymlinks(path)
	if err != nil || content.Text != string(data) || version.Path != real || version.Hash != hex.EncodeToString(sum[:]) || !version.Stamp.Same(content.Stamp) || version.Stamp.Mode.Perm() != 0o640 {
		t.Fatalf("raw baseline: %+v %v", version, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if content, version, err := ReadFileVersion(ctx, path); !errors.Is(err, context.Canceled) || content.Text != "" || version.Path != "" {
		t.Fatalf("canceled read: %+v %v", version, err)
	}
}

func TestReadFileVersionNonText(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"missing", "binary", "directory", "large"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(dir, mode)
			var want Kind
			switch mode {
			case "binary":
				want = Binary
				if err := os.WriteFile(path, []byte{0}, 0o600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				want = Directory
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "large":
				want = TooLarge
				f, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				err = f.Truncate(MaxSize + 1)
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			content, version, err := ReadFileVersion(t.Context(), path)
			if mode == "missing" {
				if !os.IsNotExist(err) || !content.Stamp.Missing || !version.Stamp.Missing || version.Path == "" || version.Hash != "" {
					t.Fatalf("missing: %+v %v", version, err)
				}
			} else if err != nil || content.Kind != want || content.Text != "" || version.Path != "" {
				t.Fatalf("nontext: %+v %+v %v", content, version, err)
			}
		})
	}
}
