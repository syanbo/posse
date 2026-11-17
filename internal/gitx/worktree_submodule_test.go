package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func worktreeSubmodule(t *testing.T) (string, string) {
	t.Helper()
	source := t.TempDir()
	initRepo(t, source)
	root := t.TempDir()
	initRepo(t, root)
	gitIn(t, root, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "sub")
	gitIn(t, root, "commit", "-qam", "submodule")
	return root, filepath.Join(root, "sub")
}
func TestWorktreeCompareSubmodules(t *testing.T) {
	root, sub := worktreeSubmodule(t)
	compareWorktreeNative(t, root)
	write(t, filepath.Join(sub, "README"), "dirty\n")
	got := compareWorktreeNative(t, root)
	if len(got.Files) != 1 || got.Files[0].Kind != "gitlink" || !got.Files[0].Dirty {
		t.Fatalf("tracked %+v", got.Files)
	}
	gitIn(t, sub, "reset", "--hard", "-q", "HEAD")
	write(t, filepath.Join(sub, "untracked"), "new\n")
	compareWorktreeNative(t, root)
	gitIn(t, root, "config", "diff.ignoreSubmodules", "none")
	got = compareWorktreeNative(t, root)
	if len(got.Files) != 1 || !got.Files[0].Dirty {
		t.Fatalf("untracked %+v", got.Files)
	}
	os.Remove(filepath.Join(sub, "untracked"))
	gitIn(t, sub, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "head")
	got = compareWorktreeNative(t, root)
	if len(got.Files) != 1 || got.Files[0].Dirty || got.Files[0].BaseOID == got.Files[0].OID {
		t.Fatalf("head %+v", got.Files)
	}
	gitIn(t, root, "add", "sub")
	gitIn(t, root, "commit", "-qm", "updated")
	gitIn(t, root, "submodule", "deinit", "-f", "sub")
	compareWorktreeNative(t, root)
	gitIn(t, root, "-c", "protocol.file.allow=always", "submodule", "update", "--init", "sub")
	gitIn(t, root, "rm", "-qf", "sub")
	write(t, filepath.Join(root, "sub"), "regular\n")
	gitIn(t, root, "add", "sub")
	got = compareWorktreeNative(t, root)
	found := false
	for _, f := range got.Files {
		if f.Path == "sub" {
			found = true
			if f.Status != 'T' || f.BaseMode != "160000" || f.Kind != "file" {
				t.Fatalf("type %+v", f)
			}
		}
	}
	if !found {
		t.Fatal("type change absent")
	}
}
func TestWorktreeCompareModulesFallbackAndStaged(t *testing.T) {
	root, sub := worktreeSubmodule(t)
	original, err := os.ReadFile(filepath.Join(sub, "README"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sub, "README"), "staged\n")
	gitIn(t, sub, "add", "README")
	if err := os.WriteFile(filepath.Join(sub, "README"), original, 0600); err != nil {
		t.Fatal(err)
	}
	got := compareWorktreeNative(t, root)
	if len(got.Files) != 1 || !got.Files[0].Dirty {
		t.Fatalf("staged cancellation %+v", got.Files)
	}
	gitIn(t, root, "config", "--file", ".gitmodules", "submodule.sub.ignore", "all")
	gitIn(t, root, "add", ".gitmodules")
	gitIn(t, root, "commit", "-qm", "ignore")
	os.Remove(filepath.Join(root, ".gitmodules"))
	got = compareWorktreeNative(t, root)
	if len(got.Files) != 1 || got.Files[0].Path != ".gitmodules" {
		t.Fatalf("index fallback %+v", got.Files)
	}
}

func TestWorktreeCompareIgnoreMatrix(t *testing.T) {
	root, sub := worktreeSubmodule(t)
	for _, state := range []string{"tracked", "untracked", "head"} {
		switch state {
		case "tracked":
			write(t, filepath.Join(sub, "README"), "dirty")
		case "untracked":
			gitIn(t, sub, "reset", "--hard", "-q", "HEAD")
			write(t, filepath.Join(sub, "new"), "new")
		case "head":
			os.Remove(filepath.Join(sub, "new"))
			gitIn(t, sub, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "head")
		}
		for _, ignore := range []string{"none", "untracked", "dirty", "all"} {
			t.Run(state+"-"+ignore, func(t *testing.T) {
				gitIn(t, root, "config", "diff.ignoreSubmodules", ignore)
				compareWorktreeNative(t, root)
			})
		}
	}
	gitIn(t, root, "config", "diff.ignoreSubmodules", "all")
	gitIn(t, root, "config", "--file", ".gitmodules", "submodule.sub.ignore", "none")
	compareWorktreeNative(t, root)
	gitIn(t, root, "config", "submodule.sub.ignore", "all")
	compareWorktreeNative(t, root)
}

func TestWorktreeCompareNestedAndExecution(t *testing.T) {
	root, sub := worktreeSubmodule(t)
	grand := t.TempDir()
	initRepo(t, grand)
	gitIn(t, sub, "-c", "protocol.file.allow=always", "submodule", "add", "-q", grand, "nested")
	gitIn(t, sub, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "-qam", "nested")
	gitIn(t, root, "add", "sub")
	gitIn(t, root, "commit", "-qm", "nested")
	write(t, filepath.Join(sub, "nested", "README"), "dirty nested")
	got := compareWorktreeNative(t, root)
	if len(got.Files) != 1 || !got.Files[0].Dirty {
		t.Fatalf("nested %+v", got.Files)
	}
	gitIn(t, filepath.Join(sub, "nested"), "reset", "--hard", "-q", "HEAD")
	write(t, filepath.Join(sub, "nested", "new"), "nested untracked")
	compareWorktreeNative(t, root)
	gitIn(t, root, "config", "diff.ignoreSubmodules", "none")
	compareWorktreeNative(t, root)
	os.Remove(filepath.Join(sub, "nested", "new"))
	write(t, filepath.Join(sub, "nested", "README"), "dirty nested")
	marker := filepath.Join(t.TempDir(), "executed")
	command := "touch '" + marker + "'; exit 1"
	for _, dir := range []string{root, sub, filepath.Join(sub, "nested")} {
		for _, key := range []string{"filter.bad.process", "filter.bad.clean", "diff.external", "diff.bad.textconv", "core.fsmonitor"} {
			gitIn(t, dir, "config", key, command)
		}
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	before, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeTest(t, root, "HEAD", ""); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sub, "nested", ".gitattributes"), "* filter=bad\n")
	if _, err := readWorktreeTest(t, root, "HEAD", ""); err == nil || !strings.Contains(err.Error(), "attribute") {
		t.Fatalf("filter %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("executed: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, ".git", "index"))
	if err != nil || string(before) != string(after) {
		t.Fatal("target index modified")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("cleanup %+v %v", entries, err)
	}
}
