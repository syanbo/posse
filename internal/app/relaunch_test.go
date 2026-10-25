package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestBundleOf(t *testing.T) {
	for exe, want := range map[string]string{
		"/x/Posse.app/Contents/MacOS/Posse": "/x/Posse.app",
		"/x/bin/posse":                      "",
		"/x/Posse/Contents/MacOS/Posse":     "",
	} {
		if got := bundleOf(exe); got != want {
			t.Errorf("bundleOf(%q) = %q, want %q", exe, got, want)
		}
	}
}

// 照 mygo 打包的做法换掉整个 .app：挪开旧的、放上新的、删掉旧的。
func TestExeWatchReplaced(t *testing.T) {
	dir := t.TempDir()
	build := func(root string) string {
		exe := filepath.Join(root, "Posse.app", "Contents", "MacOS", "Posse")
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		return exe
	}
	exe := build(dir)
	w := exeWatch{bundle: bundleOf(exe), exe: exe}
	w.dev, w.ino = statID(t, exe)
	if w.replaced() {
		t.Fatal("not replaced yet")
	}
	build(filepath.Join(dir, "new"))
	old := filepath.Join(dir, ".old")
	if err := os.Rename(filepath.Join(dir, "Posse.app"), old); err != nil {
		t.Fatal(err)
	}
	if !w.replaced() {
		t.Fatal("missing bundle should count as replaced")
	}
	if err := os.Rename(filepath.Join(dir, "new", "Posse.app"), filepath.Join(dir, "Posse.app")); err != nil {
		t.Fatal(err)
	}
	if !w.replaced() {
		t.Fatal("new bundle should count as replaced")
	}
	if (exeWatch{}).replaced() {
		t.Fatal("outside a bundle never counts")
	}
}

func statID(t *testing.T, path string) (int32, uint64) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return st.Dev, st.Ino
}
