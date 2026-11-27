package vt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLibraryManifestMatchesMygoPlugin(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "third_party", "mygo", "plugins", "terminal", "mygo-plugin.json"))
	if err != nil {
		t.Fatalf("read mygo-plugin.json: %v", err)
	}
	var plugin struct {
		Libraries []struct {
			Name  string `json:"name"`
			Files map[string]struct {
				Name   string `json:"name"`
				SHA256 string `json:"sha256"`
				URL    string `json:"url"`
			} `json:"files"`
		} `json:"libraries"`
	}
	if err := json.Unmarshal(raw, &plugin); err != nil {
		t.Fatalf("unmarshal mygo-plugin.json: %v", err)
	}
	for _, l := range plugin.Libraries {
		if l.Name != "libghostty-vt" {
			continue
		}
		if len(l.Files) != len(libraryManifest) {
			t.Fatalf("manifest platform count = %d, want %d", len(libraryManifest), len(l.Files))
		}
		for target, want := range l.Files {
			got, ok := libraryManifest[target]
			if !ok {
				t.Fatalf("missing platform %s in libraryManifest", target)
			}
			if got.Name != want.Name || got.SHA256 != want.SHA256 || got.URL != want.URL {
				t.Fatalf("platform %s = %+v, want %+v", target, got, want)
			}
		}
		return
	}
	t.Fatal("libghostty-vt entry missing in mygo-plugin.json")
}

func TestFindLibraryLocalOrder(t *testing.T) {
	lib, err := LibraryInfo(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binDir := filepath.Join(dir, "versions", "v1")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "posse")
	if err := os.WriteFile(exe, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	dylib := filepath.Join(binDir, lib.Name)
	if err := os.WriteFile(dylib, []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "current-posse")
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(dylib)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := findLibraryLocal(lib, link, "")
	if !ok || got != want {
		t.Fatalf("findLibraryLocal = (%q, %v), want (%q, true)", got, ok, want)
	}
}
