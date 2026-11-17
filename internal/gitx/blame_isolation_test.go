package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlameReadIsolation(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	write(t, filepath.Join(root, "a"), "hello\nworld\n")
	gitIn(t, root, "add", "a")
	gitIn(t, root, "commit", "-qm", "file")
	marker := filepath.Join(t.TempDir(), "executed")
	command := "touch '" + marker + "'; exit 1"
	for _, key := range []string{"filter.bad.clean", "filter.bad.process", "diff.bad.textconv", "diff.external", "core.fsmonitor"} {
		gitIn(t, root, "config", key, command)
	}
	gitIn(t, root, "config", "blame.ignoreRevsFile", filepath.Join(root, "nonexistent-ignore"))
	write(t, filepath.Join(root, ".gitattributes"), "* diff=bad\n")
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	got, err := blameReadTest(t, root, "a")
	if err != nil || len(got.Lines) != 2 {
		t.Fatalf("isolation %+v %v", got, err)
	}
	for _, attr := range []string{"filter=bad", "ident", "working-tree-encoding=UTF-16"} {
		write(t, filepath.Join(root, ".gitattributes"), "* "+attr+"\n")
		if _, err := blameReadTest(t, root, "a"); err == nil || !strings.Contains(err.Error(), "does not support") {
			t.Fatalf("attr %s: %v", attr, err)
		}
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("external command ran: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(after) != string(before) {
		t.Fatal("target index changed")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp leak %v %v", entries, err)
	}
}

func TestBlameReadMatchesDisk(t *testing.T) {
	for _, mode := range []string{"staged", "renamed", "autocrlf"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			initRepo(t, root)
			name, data := "tracked", "first\nsecond\nthird\n"
			if mode == "autocrlf" {
				gitIn(t, root, "config", "core.autocrlf", "true")
				data = strings.ReplaceAll(data, "\n", "\r\n")
			}
			write(t, filepath.Join(root, name), data)
			gitIn(t, root, "add", name)
			gitIn(t, root, "commit", "-qm", "tracked")
			if mode == "renamed" {
				gitIn(t, root, "mv", name, "moved")
				name = "moved"
			}
			if mode == "staged" {
				write(t, filepath.Join(root, name), "first\nstaged\nthird\n")
				gitIn(t, root, "add", name)
				write(t, filepath.Join(root, name), "first\nstaged\ndisk\n")
			}
			native, err := Blame(t.Context(), root, name)
			if err != nil {
				t.Fatal(err)
			}
			got, err := blameReadTest(t, root, name)
			if err != nil || len(got.Lines) != len(native) {
				t.Fatalf("result %+v %v", got, err)
			}
			for i := range native {
				if native[i].Hash != got.Lines[i].Hash {
					t.Fatalf("line %d native=%s target=%s", i, native[i].Hash, got.Lines[i].Hash)
				}
			}
		})
	}
}

func TestBlameReadWorktreeShallowAndAlternates(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	write(t, filepath.Join(root, "old"), "first\nsecond\n")
	gitIn(t, root, "add", "old")
	gitIn(t, root, "commit", "-qm", "old")
	original := gitIn(t, root, "rev-parse", "HEAD")
	gitIn(t, root, "mv", "old", "new")
	gitIn(t, root, "commit", "-qm", "rename")
	branch := filepath.Join(t.TempDir(), "linked")
	gitIn(t, root, "worktree", "add", "-qb", "linked", branch)
	shared := filepath.Join(t.TempDir(), "shared")
	gitIn(t, root, "clone", "--shared", root, shared)
	shallow := filepath.Join(t.TempDir(), "shallow")
	gitIn(t, root, "clone", "--depth=1", "file://"+root, shallow)
	for _, dir := range []string{root, branch, shared, shallow} {
		got, err := blameReadTest(t, dir, "new")
		if err != nil || len(got.Lines) != 2 {
			t.Fatalf("%s: %+v %v", dir, got, err)
		}
		want := original
		if dir == shallow {
			want = got.Head
		}
		if got.Lines[0].Hash != want {
			t.Fatalf("%s: hash %s want %s", dir, got.Lines[0].Hash, want)
		}
	}
}
