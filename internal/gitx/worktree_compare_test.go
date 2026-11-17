package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func readWorktreeTest(t *testing.T, root, revision, baseline string) (WorktreeComparison, error) {
	t.Helper()
	ctx := historyContext(t)
	id, err := RepositoryIdentityChecked(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	return ReadWorktreeComparison(ctx, id, revision, baseline)
}
func compareWorktreeNative(t *testing.T, root string) WorktreeComparison {
	t.Helper()
	native, err := DiffWithWorkingTree(t.Context(), root, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got, err := readWorktreeTest(t, root, "HEAD", "")
	if err != nil {
		t.Fatal(err)
	}
	actual := []FileChange{}
	for _, f := range got.Files {
		actual = append(actual, f.FileChange)
	}
	if native == nil {
		native = []FileChange{}
	}
	sort.Slice(native, func(i, j int) bool { return native[i].Path < native[j].Path })
	if !reflect.DeepEqual(native, actual) {
		t.Fatalf("native %+v target %+v", native, actual)
	}
	return got
}
func TestWorktreeCompareFiles(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			gitIn(t, root, "init", "-b", "main", "--object-format="+format)
			gitIn(t, root, "config", "user.name", "Test")
			gitIn(t, root, "config", "user.email", "test@example.invalid")
			for _, name := range []string{"a", "removed", "renamed", "directory", ":(glob)*", "line\n雪"} {
				write(t, filepath.Join(root, name), "original "+name+"\n")
			}
			if err := os.Symlink("a", filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			gitIn(t, root, "add", "--all")
			gitIn(t, root, "commit", "-qm", "base")
			write(t, filepath.Join(root, "a"), "changed\x00binary\n")
			os.Remove(filepath.Join(root, "removed"))
			gitIn(t, root, "mv", "renamed", "newname")
			os.Remove(filepath.Join(root, "directory"))
			os.Mkdir(filepath.Join(root, "directory"), 0700)
			os.Remove(filepath.Join(root, "link"))
			os.Symlink("outside-target", filepath.Join(root, "link"))
			write(t, filepath.Join(root, ":(glob)*"), "special\n")
			write(t, filepath.Join(root, "line\n雪"), "special\n")
			write(t, filepath.Join(root, "added"), "staged")
			gitIn(t, root, "add", "added")
			write(t, filepath.Join(root, "untracked"), "ignored")
			got := compareWorktreeNative(t, root)
			if len(got.Files) != 8 {
				t.Fatalf("files %+v", got.Files)
			}
			for _, f := range got.Files {
				if f.Path == "link" && (f.Kind != "symlink" || f.Link != "outside-target") {
					t.Fatalf("link %+v", f)
				}
			}
			checked, err := readWorktreeTest(t, root, got.Commit, got.Baseline)
			if err != nil || checked.Baseline != got.Baseline {
				t.Fatalf("fixed baseline %+v %v", checked, err)
			}
			write(t, filepath.Join(root, "a"), "changed again")
			if _, err := readWorktreeTest(t, root, got.Commit, got.Baseline); !errors.Is(err, ErrWorktreeChanged) {
				t.Fatalf("stale baseline %v", err)
			}
		})
	}
}

func TestWorktreeCompareConversionsAndLimits(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	write(t, filepath.Join(root, "a"), "one\ntwo\n")
	write(t, filepath.Join(root, ".gitattributes"), "a text eol=lf\n")
	gitIn(t, root, "add", "--all")
	gitIn(t, root, "commit", "-qm", "file")
	write(t, filepath.Join(root, "a"), "one\r\ntwo\r\n")
	compareWorktreeNative(t, root)
	for _, flag := range []string{"--skip-worktree", "--assume-unchanged"} {
		gitIn(t, root, "update-index", flag, "a")
		if _, err := readWorktreeTest(t, root, "HEAD", ""); !errors.Is(err, ErrWorktreeUnsupported) {
			t.Fatalf("%s: %v", flag, err)
		}
		gitIn(t, root, "update-index", strings.Replace(flag, "--", "--no-", 1), "a")
	}
	ctx := historyContext(t)
	id, err := RepositoryIdentityChecked(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeNode(ctx, id, 0, &worktreeBudget{bytes: 1}); !errors.Is(err, ErrWorktreeLimit) {
		t.Fatalf("scan budget %v", err)
	}
	oid := gitIn(t, root, "rev-parse", "HEAD:a")
	gitIn(t, root, "update-index", "--force-remove", "a")
	if _, err := runRaw(t.Context(), root, []byte("100644 "+oid+" 1\ta\n100644 "+oid+" 2\ta\n"), nil, "update-index", "--index-info"); err != nil {
		t.Fatal(err)
	}
	if _, err := readWorktreeTest(t, root, "HEAD", ""); !errors.Is(err, ErrFileUnmerged) {
		t.Fatalf("conflict index %v", err)
	}
}
