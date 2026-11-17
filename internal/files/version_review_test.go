package files

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileVersionSymlinkParentTraversal(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "sub", "deep"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	// 不能用 Join 构造：它在访问文件系统前已经清掉 link/..。
	path := dir + "/link/../new"
	v := fileVersion(t, path)
	if _, err := checkedText(context.Background(), path, v, "saved"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sub", "new")); err != nil || string(b) != "saved" {
		t.Fatalf("wrong destination: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "new")); !os.IsNotExist(err) {
		t.Fatalf("created at lexically cleaned path: %v", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(cwd, dir)
	if err != nil {
		t.Fatal(err)
	}
	rel += "/link/../new"
	if _, err := checkedText(context.Background(), rel, fileVersion(t, rel), "relative"); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "sub", "new")); err != nil || string(b) != "relative" {
		t.Fatalf("relative path changed destination: %q %v", b, err)
	}
}

func TestFileVersionRetargetBeforeSave(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(map[bool]string{false: "hardlinks", true: "missing"}[missing], func(t *testing.T) {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
			if missing {
				if err := os.Mkdir(a, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(b, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				write(t, a, "old")
				if err := os.Link(a, b); err != nil {
					t.Fatal(err)
				}
			}
			link := filepath.Join(dir, "link")
			if err := os.Symlink(a, link); err != nil {
				t.Fatal(err)
			}
			path := link
			if missing {
				path += "/new"
			}
			v := fileVersion(t, path)
			if err := os.Remove(link); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(b, link); err != nil {
				t.Fatal(err)
			}
			_, err := checkedText(context.Background(), path, v, "mine")
			if !errors.Is(err, ErrSaveConflict) {
				t.Fatalf("retarget accepted: %v", err)
			}
		})
	}
}

func TestFileVersionCancelledWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	write(t, path, "old")
	v := fileVersion(t, path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := WriteFileChecked(ctx, path, v, func(w io.Writer) error {
		if _, err := io.WriteString(w, "part"); err != nil {
			return err
		}
		cancel()
		n, err := io.WriteString(w, "more")
		if n != 0 || !errors.Is(err, context.Canceled) {
			t.Errorf("write after cancellation: %d %v", n, err)
		}
		return err
	})
	if !errors.Is(err, context.Canceled) || !v.Same(fileVersion(t, path)) {
		t.Fatalf("cancel changed destination: %v", err)
	}
}

func TestFileVersionWriteLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	write(t, path, "old")
	v := fileVersion(t, path)
	chunk := strings.Repeat("x", 32<<10)
	_, err := WriteFileChecked(context.Background(), path, v, func(w io.Writer) error {
		for n := 0; n < MaxSize; n += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return err
			}
		}
		if n, err := io.WriteString(w, "!"); n != 0 || err == nil {
			t.Errorf("oversized output accepted: %d %v", n, err)
		}
		// 即使回调忽略 Writer 的错误，也不能提交已超限的保存。
		return nil
	})
	if err == nil {
		t.Fatal("oversized save succeeded")
	}
	if !v.Same(fileVersion(t, path)) {
		t.Fatal("oversized output replaced original")
	}
	full, err := WriteFileChecked(context.Background(), path, v, func(w io.Writer) error {
		for n := 0; n < MaxSize; n += len(chunk) {
			if _, err := io.WriteString(w, chunk); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil || full.Stamp.Size != MaxSize {
		t.Fatalf("exact limit rejected: %+v %v", full, err)
	}
	if _, err := checkedText(context.Background(), path, full, "small again"); err != nil {
		t.Fatalf("cannot save after exact limit: %v", err)
	}
}

func TestFileAtomicWriteWithoutDirectoryRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	path := filepath.Join(dir, "file")
	write(t, path, "old")
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := WriteFile(path, func(w io.Writer) error {
		_, err := io.WriteString(w, "plain")
		return err
	}); err != nil {
		t.Fatalf("plain save requires directory read permission: %v", err)
	}
	if _, err := checkedText(context.Background(), path, fileVersion(t, path), "checked"); err != nil {
		t.Fatalf("checked save requires directory read permission: %v", err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "checked" {
		t.Fatalf("save failed: %q %v", b, err)
	}
}

func TestFileAtomicCleanupAfterParentMove(t *testing.T) {
	for _, checked := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "checked"}[checked], func(t *testing.T) {
			root := t.TempDir()
			dir, moved := filepath.Join(root, "dir"), filepath.Join(root, "moved")
			path := filepath.Join(dir, "file")
			write(t, path, "old")
			v := fileVersion(t, path)
			fn := func(w io.Writer) error {
				if _, err := io.WriteString(w, "private draft"); err != nil {
					return err
				}
				if err := os.Rename(dir, moved); err != nil {
					t.Fatal(err)
				}
				if checked {
					return nil
				}
				return io.ErrUnexpectedEOF
			}
			var err error
			if checked {
				_, err = WriteFileChecked(context.Background(), path, v, fn)
			} else {
				_, err = WriteFile(path, fn)
			}
			if err == nil {
				t.Fatal("save after parent move succeeded")
			}
			entries, err := os.ReadDir(moved)
			if err != nil || len(entries) != 1 || entries[0].Name() != "file" {
				t.Fatalf("temporary file left in renamed directory: %v %v", entries, err)
			}
			if b, err := os.ReadFile(filepath.Join(moved, "file")); err != nil || string(b) != "old" {
				t.Fatalf("original changed: %q %v", b, err)
			}
		})
	}
}
