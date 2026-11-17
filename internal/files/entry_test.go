package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEntryVersion(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, target string }{{"link", "file"}, {"broken", "missing"}, {"loop", "loop"}} {
		if err := os.Symlink(item.target, filepath.Join(root, item.name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(root, "dir"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "link", "broken", "loop", "dir"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			v, err := ReadEntryVersion(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			parent, _ := filepath.EvalSymlinks(root)
			if v.Path != filepath.Join(parent, name) || v.Stamp.Missing {
				t.Fatalf("entry=%+v", v)
			}
			var moved string
			_, err = TrashEntryChecked(t.Context(), path, v, func(path string) (string, error) { moved = path; return "destination", nil })
			if err != nil || moved != v.Path {
				t.Fatal("entry did not retain its identity")
			}
		})
	}
	v, err := ReadEntryVersion(t.Context(), file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file, file+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = TrashEntryChecked(t.Context(), file, v, func(string) (string, error) { t.Fatal("moved replacement"); return "", nil })
	if !errors.Is(err, ErrEntryChanged) {
		t.Fatalf("changed=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadEntryVersion(ctx, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	for _, path := range []string{"relative", "/", root + "/.", root + "/..", root + "/bad\x00"} {
		if _, err := ReadEntryVersion(t.Context(), path); err == nil {
			t.Errorf("accepted %q", path)
		}
	}
}

func TestEntryVersionParentAlias(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "target", "child"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "target", "child"), filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "target", "file")
	if err := os.WriteFile(path, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	v, err := ReadEntryVersion(t.Context(), root+"/alias/../file")
	if err != nil {
		t.Fatal(err)
	}
	want, err := ReadEntryVersion(t.Context(), path)
	if err != nil || !v.Same(want) {
		t.Fatalf("alias=%+v want=%+v err=%v", v, want, err)
	}
}
