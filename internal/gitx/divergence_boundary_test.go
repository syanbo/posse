package gitx

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestDivergenceReadUnavailable(t *testing.T) {
	for _, kind := range []string{"shallow", "unrelated", "multiple-bases", "submodule", "old-git"} {
		t.Run(kind, func(t *testing.T) {
			root := divergenceRepo(t, "sha1", "file")
			head := gitIn(t, root, "rev-parse", "HEAD")
			base := gitIn(t, root, "rev-parse", "base")
			tree := gitIn(t, root, "rev-parse", "HEAD^{tree}")
			reason := ""
			switch kind {
			case "shallow":
				write(t, filepath.Join(root, ".git", "shallow"), gitIn(t, root, "merge-base", "HEAD", "base")+"\n")
				reason = DivergenceShallow
			case "unrelated":
				oid := gitIn(t, root, "commit-tree", tree, "-m", "unrelated")
				gitIn(t, root, "update-ref", "refs/heads/base", oid)
				reason = DivergenceUnrelated
			case "multiple-bases":
				a := gitIn(t, root, "commit-tree", tree, "-p", head, "-p", base, "-m", "merge one")
				b := gitIn(t, root, "commit-tree", gitIn(t, root, "rev-parse", "base^{tree}"), "-p", base, "-p", head, "-m", "merge two")
				gitIn(t, root, "update-ref", "refs/heads/main", a)
				gitIn(t, root, "update-ref", "refs/heads/base", b)
				reason = DivergenceMultipleBases
			case "submodule":
				gitIn(t, root, "update-index", "--add", "--cacheinfo", "160000,"+head+",sub")
				gitIn(t, root, "commit", "-qm", "submodule")
				reason = DivergenceSubmodule
			case "old-git":
				divergenceWrapper(t, "case \" $* \" in *\" merge-tree \"*) /usr/bin/printf \"error: unknown option 'write-tree'\\n\" >&2; exit 129;; esac")
				reason = DivergenceGit
			}
			got, err := divergenceReadTest(t, root, "base", "")
			if err != nil || got.Prediction != DivergenceUnavailable || got.Reason != reason || got.Ahead == 0 || got.Behind == 0 || len(got.Conflicts) != 0 {
				t.Fatalf("result %+v %v", got, err)
			}
		})
	}
}

func TestDivergenceReadLinkedAndAlternates(t *testing.T) {
	root := divergenceRepo(t, "sha1", "file")
	linked := filepath.Join(t.TempDir(), "linked")
	gitIn(t, root, "worktree", "add", "-q", "-b", "linked", linked, "main")
	got, err := divergenceReadTest(t, linked, "base", "")
	if err != nil || got.Prediction != DivergenceCompleted || len(got.Conflicts) != 1 {
		t.Fatalf("linked %+v %v", got, err)
	}
	clone := filepath.Join(t.TempDir(), "clone")
	gitIn(t, root, "clone", "-q", "--shared", root, clone)
	got, err = divergenceReadTest(t, clone, "origin/base", "")
	if err != nil || got.Prediction != DivergenceCompleted || len(got.Conflicts) != 1 {
		t.Fatalf("alternates %+v %v", got, err)
	}
}

func TestDivergenceReadEffectiveAttributes(t *testing.T) {
	for _, source := range []string{"global", "index-fallback", "committed", "committed-absent", "worktree"} {
		t.Run(source, func(t *testing.T) {
			root := divergenceRepo(t, "sha1", "file")
			switch source {
			case "global":
				file := filepath.Join(t.TempDir(), "attributes")
				write(t, file, "* merge=union\n")
				gitIn(t, root, "config", "core.attributesFile", file)
			case "index-fallback", "committed", "committed-absent", "worktree":
				write(t, filepath.Join(root, ".gitattributes"), "* merge=union\n")
				if source != "worktree" {
					gitIn(t, root, "add", ".gitattributes")
				}
				if source == "committed" || source == "committed-absent" {
					gitIn(t, root, "commit", "-qm", "attributes")
				}
				if source == "index-fallback" || source == "committed-absent" {
					if err := os.Remove(filepath.Join(root, ".gitattributes")); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := divergenceReadTest(t, root, "base", "")
			want := 0
			if source == "index-fallback" || source == "committed-absent" {
				want = 1
			}
			if err != nil || got.Prediction != DivergenceCompleted || len(got.Conflicts) != want || len(divergenceNative(t, root, "base")) != want {
				t.Fatalf("result %+v %v", got, err)
			}
		})
	}
}

func TestDivergenceReadTemporaryLimits(t *testing.T) {
	for _, kind := range []string{"file", "total", "output"} {
		t.Run(kind, func(t *testing.T) {
			root := divergenceRepo(t, "sha1", "file")
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			scripts := map[string]string{
				"file":   "exec /bin/dd if=/dev/zero of=\"$GIT_DIR/objects/large\" bs=1048576 count=17 2>/dev/null",
				"total":  "for n in 1 2 3 4 5 6 7; do /bin/dd if=/dev/zero of=\"$GIT_DIR/objects/part$n\" bs=1048576 count=10 2>/dev/null || exit 98; done; exec /bin/sleep 30",
				"output": "exec /usr/bin/yes malformed",
			}
			divergenceWrapper(t, "case \" $* \" in *\" merge-tree \"*) "+scripts[kind]+";; esac")
			got, err := divergenceReadTest(t, root, "base", "")
			if kind == "output" {
				if !errors.Is(err, ErrReadLimit) {
					t.Fatalf("output budget %v", err)
				}
			} else if err != nil || got.Prediction != DivergenceUnavailable || got.Reason != DivergenceLimit {
				t.Fatalf("disk budget %+v %v", got, err)
			}
			entries, e := os.ReadDir(tmp)
			if e != nil || len(entries) != 0 {
				t.Fatalf("temp leak %v %v", entries, e)
			}
		})
	}
}

func TestDivergenceChildFileLimit(t *testing.T) {
	var before, after syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &before); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0700); err != nil {
		t.Fatal(err)
	}
	divergenceWrapper(t, "exec /bin/dd if=/dev/zero of=\"$GIT_DIR/large\" bs=1048576 count=17 2>/dev/null")
	_, _, reason, err := runDivergenceMerge(historyContext(t), dir, gitDir, nil)
	if err != nil || reason != DivergenceLimit {
		t.Fatalf("limit %q %v", reason, err)
	}
	info, err := os.Stat(filepath.Join(gitDir, "large"))
	if err != nil {
		t.Fatal(err)
	}
	// sh 的单位因系统而异；两种单位都必须限制在承诺的 16 MiB 内。
	if info.Size() != 8<<20 && info.Size() != 16<<20 {
		t.Fatalf("file size %d", info.Size())
	}
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &after); err != nil || before != after {
		t.Fatalf("parent rlimit changed: before=%+v after=%+v err=%v", before, after, err)
	}
	t.Logf("child file limit %d bytes; parent rlimit unchanged", info.Size())
}

func TestDivergenceReadTotalInputLimit(t *testing.T) {
	root := divergenceRepo(t, "sha1", "file")
	for i, name := range []string{"one", "two", "three", "four", "five"} {
		write(t, filepath.Join(root, name), strings.Repeat(string(rune('a'+i)), 7<<20))
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "changed resources")
	got, err := divergenceReadTest(t, root, "base", "")
	if err != nil || got.Prediction != DivergenceUnavailable || got.Reason != DivergenceLimit {
		t.Fatalf("total input %+v %v", got, err)
	}
}

func TestDivergenceConfigInvalidOutput(t *testing.T) {
	for _, raw := range []string{"not-null-terminated", "missing-value\x00"} {
		if _, _, err := divergenceConfig([]byte(raw)); !errors.Is(err, ErrDivergenceOutput) {
			t.Fatalf("accepted %q", raw)
		}
	}
	root := divergenceRepo(t, "sha1", "file")
	divergenceWrapper(t, "case \" $* \" in *\" merge-tree \"*) /usr/bin/printf 'real read failure\\n' >&2; exit 128;; esac")
	if got, err := divergenceReadTest(t, root, "base", ""); err == nil || got.Prediction == DivergenceUnavailable {
		t.Fatalf("read failure hidden %+v %v", got, err)
	}
	if ValidDivergenceQuery("main", strings.Repeat("z", 64)) {
		t.Fatal("invalid baseline accepted")
	}
}
