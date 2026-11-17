package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func fileVersion(t *testing.T, path string) Version {
	t.Helper()
	v, err := ReadVersion(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func checkedText(ctx context.Context, path string, expected Version, text string) (Version, error) {
	return WriteFileChecked(ctx, path, expected, func(w io.Writer) error {
		_, err := io.WriteString(w, text)
		return err
	})
}

func TestFileVersionCheckedSave(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "symlink"}[linked], func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "run.sh")
			write(t, path, "old\r\n")
			if err := os.Chmod(path, 0o751); err != nil {
				t.Fatal(err)
			}
			if linked {
				link := filepath.Join(dir, "link.sh")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			before := fileVersion(t, path)
			text := "new\r\n\xff"
			after, err := checkedText(context.Background(), path, before, text)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(text))
			if !after.Same(fileVersion(t, path)) || after.Hash != hex.EncodeToString(sum[:]) || after.Stamp.Mode.Perm() != 0o751 || after.Stamp.Ino == before.Stamp.Ino {
				t.Fatalf("wrong saved version: %+v", after)
			}
			if linked {
				fi, err := os.Lstat(path)
				if err != nil || fi.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink replaced: %v", err)
				}
			}
		})
	}
}

func TestFileVersionConflicts(t *testing.T) {
	for _, mode := range []string{"before", "during", "delete", "permissions", "missing-created", "symlink-retarget"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "file")
			write(t, path, "old")
			if mode == "missing-created" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink-retarget" {
				link := filepath.Join(dir, "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			before := fileVersion(t, path)
			change := func() {
				switch mode {
				case "delete":
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				case "permissions":
					if err := os.Chmod(path, 0o600); err != nil {
						t.Fatal(err)
					}
				case "symlink-retarget":
					other := filepath.Join(dir, "other")
					write(t, other, "external")
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(other, path); err != nil {
						t.Fatal(err)
					}
				default:
					write(t, path, "new")
					if !before.Stamp.Missing {
						if err := os.Chtimes(path, before.Stamp.ModTime, before.Stamp.ModTime); err != nil {
							t.Fatal(err)
						}
						if !StatFile(path).Same(before.Stamp) {
							t.Fatal("test did not preserve stamp")
						}
					}
				}
			}
			if mode == "before" {
				change()
			}
			_, err := WriteFileChecked(context.Background(), path, before, func(w io.Writer) error {
				if mode == "before" {
					t.Fatal("stale baseline reached write callback")
				}
				if _, err := io.WriteString(w, "mine"); err != nil {
					return err
				}
				change()
				return nil
			})
			if !errors.Is(err, ErrSaveConflict) {
				t.Fatalf("conflict: %v", err)
			}
			if b, _ := os.ReadFile(path); string(b) == "mine" {
				t.Fatal("conflict overwritten")
			}
			if tmp, _ := filepath.Glob(filepath.Join(dir, ".posse-*")); len(tmp) != 0 {
				t.Fatalf("temporary files left: %v", tmp)
			}
		})
	}
}

func TestFileVersionMissingAndFailures(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new")
	missing := fileVersion(t, path)
	if !missing.Stamp.Missing || missing.Hash != "" {
		t.Fatalf("missing: %+v", missing)
	}
	v, err := checkedText(context.Background(), path, missing, "created")
	if err != nil || !v.Same(fileVersion(t, path)) {
		t.Fatalf("create: %+v %v", v, err)
	}
	for _, cancelBefore := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelBefore {
			cancel()
		}
		_, err := WriteFileChecked(ctx, path, v, func(w io.Writer) error {
			if cancelBefore {
				t.Fatal("cancelled request wrote a file")
			}
			_, err := io.WriteString(w, "cancelled")
			cancel()
			return err
		})
		cancel()
		if !errors.Is(err, context.Canceled) || !v.Same(fileVersion(t, path)) {
			t.Fatalf("cancel: %v", err)
		}
	}
	_, err = WriteFileChecked(context.Background(), path, v, func(io.Writer) error { return io.ErrUnexpectedEOF })
	if !errors.Is(err, io.ErrUnexpectedEOF) || !v.Same(fileVersion(t, path)) {
		t.Fatalf("failed writer changed file: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
}

func TestFileVersionRejectsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "absent"), link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, dir} {
		if _, err := ReadVersion(context.Background(), path); err == nil {
			t.Fatalf("special file accepted: %s", path)
		}
	}
	if _, err := checkedText(context.Background(), link, Version{Stamp: Stamp{Missing: true}}, "bad"); err == nil {
		t.Fatal("dangling link replaced")
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dangling link lost: %v", err)
	}
	big := filepath.Join(dir, "big")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxSize + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadVersion(context.Background(), big); err == nil {
		t.Fatal("oversized file accepted")
	}
}
