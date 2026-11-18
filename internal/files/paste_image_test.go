package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pastePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestPasteImage(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := pastePNG(t)
	path, err := PasteImage(t.Context(), data)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) || filepath.Dir(path) != dir || !strings.HasPrefix(filepath.Base(path), "posse-paste-") || filepath.Ext(path) != ".png" {
		t.Fatalf("path = %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("content = %x, %v", got, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", fi, err)
	}
}

func TestPasteImageValidationAndFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	data := pastePNG(t)
	for _, input := range [][]byte{nil, []byte("not PNG"), data[:20], bytes.Repeat([]byte{0}, MaxSize+1)} {
		path, err := PasteImage(t.Context(), input)
		if err == nil || path != "" {
			t.Fatalf("invalid image saved: %q %v", path, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if path, err := PasteImage(ctx, data); path != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: %q %v", path, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed paste left files: %v %v", entries, err)
	}
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	if path, err := PasteImage(t.Context(), data); path != "" || !os.IsNotExist(err) {
		t.Fatalf("create failure: %q %v", path, err)
	}
}

// 在真实文件写到指定阶段时取消，避免靠定时器碰运气。
type pasteCancelContext struct {
	context.Context
	dir      string
	after    int64
	cancel   context.CancelFunc
	observed bool
}

func (c *pasteCancelContext) Err() error {
	entries, _ := os.ReadDir(c.dir)
	for _, e := range entries {
		fi, err := e.Info()
		if err == nil && fi.Size() >= c.after {
			c.observed = true
			c.cancel()
		}
	}
	return c.Context.Err()
}

func TestPasteImageCancellationCleansFile(t *testing.T) {
	data := append(pastePNG(t), make([]byte, 128<<10)...)
	for _, after := range []int64{0, 64 << 10, int64(len(data))} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			base, cancel := context.WithCancel(t.Context())
			defer cancel()
			ctx := &pasteCancelContext{Context: base, dir: dir, after: after, cancel: cancel}
			path, err := PasteImage(ctx, data)
			if path != "" || !errors.Is(err, context.Canceled) || !ctx.observed {
				t.Fatalf("cancel during write: %q %v observed=%v", path, err, ctx.observed)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("cancel left files: %v %v", entries, err)
			}
		})
	}
}

func TestPasteFileAndMakeDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	body := []byte("hello remote\n\x00binary")
	path, err := PasteFile(t.Context(), "report final.pdf", body)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "report final.pdf" || filepath.Dir(filepath.Dir(path)) != dir {
		t.Fatalf("unexpected paste file path: %q", path)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("content = %q, %v", got, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", fi, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "a\nb", strings.Repeat("x", 256)} {
		if p, err := PasteFile(t.Context(), bad, body); err == nil || p != "" {
			t.Fatalf("invalid name %q accepted: %q", bad, p)
		}
	}

	nested := filepath.Join(t.TempDir(), "a", "b", "c")
	if err := MakeDir(t.Context(), nested); err != nil {
		t.Fatal(err)
	}
	if err := MakeDir(t.Context(), nested); err != nil {
		t.Fatalf("idempotent MakeDir failed: %v", err)
	}
	file := filepath.Join(nested, "plain.txt")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := MakeDir(t.Context(), file); !errors.Is(err, ErrSaveConflict) {
		t.Fatalf("MakeDir over file = %v, want ErrSaveConflict", err)
	}
	broken := filepath.Join(nested, "broken")
	if err := os.Symlink(filepath.Join(nested, "missing"), broken); err != nil {
		t.Fatal(err)
	}
	if err := MakeDir(t.Context(), broken); !errors.Is(err, ErrSaveConflict) {
		t.Fatalf("MakeDir over broken symlink = %v, want ErrSaveConflict", err)
	}
}
