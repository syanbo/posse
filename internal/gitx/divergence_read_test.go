package gitx

import (
	"context"
	"crypto/sha256"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func divergenceRepo(t *testing.T, format, name string) string {
	t.Helper()
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main", "--object-format="+format)
	gitIn(t, root, "config", "user.name", "Test")
	gitIn(t, root, "config", "user.email", "test@example.invalid")
	write(t, filepath.Join(root, name), "one\ntwo\nthree\n")
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "base")
	gitIn(t, root, "branch", "base")
	write(t, filepath.Join(root, name), "one\nours\nthree\n")
	gitIn(t, root, "commit", "-qam", "ours")
	gitIn(t, root, "checkout", "-q", "base")
	write(t, filepath.Join(root, name), "one\ntheirs\nthree\n")
	gitIn(t, root, "commit", "-qam", "theirs")
	gitIn(t, root, "checkout", "-q", "main")
	return root
}

func divergenceReadTest(t *testing.T, root, base, previous string) (DivergenceRead, error) {
	t.Helper()
	ctx := historyContext(t)
	id, err := RepositoryIdentityChecked(ctx, root)
	if err != nil {
		return DivergenceRead{}, err
	}
	return ReadDivergenceChecked(ctx, id, base, previous)
}

func divergenceNative(t *testing.T, root, base string) []string {
	t.Helper()
	raw, code, err := quiet(t.Context(), root, "merge-tree", "--write-tree", "--name-only", "--no-messages", "-z", "HEAD", base)
	if err != nil && code != 1 {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
	return parts[1:]
}

func TestDivergenceReadNative(t *testing.T) {
	for _, format := range []string{"sha1", "sha256"} {
		for _, name := range []string{"file", "nested/a *?[x]\\\"\t\nü.txt"} {
			t.Run(format+"/"+name, func(t *testing.T) {
				root := divergenceRepo(t, format, name)
				got, err := divergenceReadTest(t, root, "base", "")
				if err != nil || got.Prediction != DivergenceCompleted || got.Ahead != 1 || got.Behind != 1 || !slices.Equal(got.Conflicts, divergenceNative(t, root, "base")) {
					t.Fatalf("prediction %+v err=%v", got, err)
				}
				again, err := divergenceReadTest(t, root, "base", got.Baseline)
				if err != nil || !again.Unchanged || again.Conflicts != nil || again.Prediction != "" || again.Ahead != 0 {
					t.Fatalf("unchanged %+v %v", again, err)
				}
				for _, attr := range []string{"merge=union", "-merge", "merge=text", "merge=binary"} {
					write(t, filepath.Join(root, ".git", "info", "attributes"), divergenceAttributePattern(name)+" "+attr+"\n")
					changed, err := divergenceReadTest(t, root, "base", got.Baseline)
					if err != nil || changed.Unchanged || changed.Prediction != DivergenceCompleted || !slices.Equal(changed.Conflicts, divergenceNative(t, root, "base")) {
						t.Fatalf("%s %+v %v", attr, changed, err)
					}
				}
			})
		}
	}
}

func TestDivergenceReadRelationships(t *testing.T) {
	root := divergenceRepo(t, "sha1", "file")
	for _, ref := range []string{"HEAD", "HEAD~1"} {
		got, err := divergenceReadTest(t, root, ref, "")
		if err != nil || got.Prediction != DivergenceNotNeeded || got.Behind != 0 || len(got.Conflicts) != 0 {
			t.Fatalf("%s %+v %v", ref, got, err)
		}
	}
	gitIn(t, root, "branch", "ahead")
	gitIn(t, root, "reset", "--hard", "HEAD~1")
	got, err := divergenceReadTest(t, root, "ahead", "")
	if err != nil || got.Ahead != 0 || got.Behind != 1 || got.Prediction != DivergenceNotNeeded {
		t.Fatalf("behind %+v %v", got, err)
	}
	for _, ref := range []string{"missing", "--all", "HEAD..base"} {
		if _, err := divergenceReadTest(t, root, ref, ""); err == nil {
			t.Fatalf("accepted %s", ref)
		}
	}
}

func divergenceInventory(t *testing.T, dir string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(dir, func(name string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !e.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(name)
		if err == nil {
			relative, _ := filepath.Rel(dir, name)
			out[relative] = sha256.Sum256(b)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDivergenceReadAttributesAndIsolation(t *testing.T) {
	root := divergenceRepo(t, "sha1", "nested/file")
	marker := filepath.Join(t.TempDir(), "executed")
	for _, key := range []string{"merge.custom.driver", "filter.bad.process", "diff.external", "core.fsmonitor"} {
		gitIn(t, root, "config", key, "touch '"+marker+"'; exit 1")
	}
	write(t, filepath.Join(root, ".gitattributes"), "* merge=union filter=bad\n")
	write(t, filepath.Join(root, "nested", ".gitattributes"), "file -merge\n")
	before := divergenceInventory(t, filepath.Join(root, ".git"))
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	got, err := divergenceReadTest(t, root, "base", "")
	if err != nil || got.Prediction != DivergenceCompleted || len(got.Conflicts) != 1 {
		t.Fatalf("nested %+v %v", got, err)
	}
	if !reflect.DeepEqual(before, divergenceInventory(t, filepath.Join(root, ".git"))) {
		t.Fatal("target git directory changed")
	}
	write(t, filepath.Join(root, ".git", "info", "attributes"), "* merge=union\n")
	got, err = divergenceReadTest(t, root, "base", "")
	if err != nil || got.Prediction != DivergenceCompleted || len(got.Conflicts) != 0 {
		t.Fatalf("override %+v %v", got, err)
	}
	write(t, filepath.Join(root, ".git", "info", "attributes"), "* merge=custom\n")
	got, err = divergenceReadTest(t, root, "base", "")
	if err != nil || got.Prediction != DivergenceUnavailable || got.Reason != DivergenceAttributes || got.Ahead != 1 {
		t.Fatalf("custom %+v %v", got, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("driver executed: %v", err)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp leak %v %v", entries, err)
	}
}

func TestDivergenceReadRenameDeleteAndConfig(t *testing.T) {
	for _, mode := range []string{"rename", "delete", "clean", "union-default", "diff3", "renormalize"} {
		t.Run(mode, func(t *testing.T) {
			root := divergenceRepo(t, "sha1", "file")
			switch mode {
			case "rename":
				gitIn(t, root, "mv", "file", "renamed")
				gitIn(t, root, "commit", "-qm", "rename")
			case "delete":
				gitIn(t, root, "rm", "file")
				gitIn(t, root, "commit", "-qm", "delete")
			case "clean":
				gitIn(t, root, "reset", "--hard", "HEAD~1")
				write(t, filepath.Join(root, "other"), "x\n")
				gitIn(t, root, "add", ".")
				gitIn(t, root, "commit", "-qm", "other")
			case "union-default":
				gitIn(t, root, "config", "merge.default", "union")
			case "diff3":
				gitIn(t, root, "config", "merge.conflictStyle", "diff3")
			case "renormalize":
				gitIn(t, root, "config", "merge.renormalize", "true")
			}
			got, err := divergenceReadTest(t, root, "base", "")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "renormalize" {
				if got.Prediction != DivergenceUnavailable || got.Reason != DivergenceConfig {
					t.Fatalf("renormalize %+v", got)
				}
			} else if got.Prediction != DivergenceCompleted || !slices.Equal(got.Conflicts, divergenceNative(t, root, "base")) {
				t.Fatalf("prediction %+v", got)
			}
		})
	}
}

func TestDivergenceReadInputLimits(t *testing.T) {
	root := divergenceRepo(t, "sha1", "file")
	gitIn(t, root, "checkout", "-q", "main~1")
	write(t, filepath.Join(root, "large"), strings.Repeat("x", int(divergenceBlobLimit)+1))
	gitIn(t, root, "add", "large")
	gitIn(t, root, "commit", "-qm", "large common resource")
	common := gitIn(t, root, "rev-parse", "HEAD")
	for _, branch := range []string{"main", "base"} {
		gitIn(t, root, "checkout", "-q", branch)
		gitIn(t, root, "rebase", "--onto", common, "HEAD~1")
	}
	gitIn(t, root, "checkout", "-q", "main")
	got, err := divergenceReadTest(t, root, "base", "")
	if err != nil || got.Prediction != DivergenceCompleted {
		t.Fatalf("unchanged resource %+v %v", got, err)
	}
	write(t, filepath.Join(root, "large"), strings.Repeat("y", int(divergenceBlobLimit)+1))
	gitIn(t, root, "commit", "-qam", "change large")
	got, err = divergenceReadTest(t, root, "base", "")
	if err != nil || got.Prediction != DivergenceUnavailable || got.Reason != DivergenceLimit {
		t.Fatalf("changed resource %+v %v", got, err)
	}
	id, err := RepositoryIdentityChecked(historyContext(t), root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadDivergenceChecked(WithReadLimit(t.Context(), 1), id, "base", ""); !errors.Is(err, ErrReadLimit) {
		t.Fatalf("budget %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := ReadDivergenceChecked(WithReadLimit(ctx, 4<<20), id, "base", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
}

func divergenceWrapper(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"+script+"\nexec /usr/bin/git \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestDivergenceReadUnchangedSkipsMerge(t *testing.T) {
	root := divergenceRepo(t, "sha1", "file")
	got, err := divergenceReadTest(t, root, "base", "")
	if err != nil {
		t.Fatal(err)
	}
	divergenceWrapper(t, "case \" $* \" in *\" merge-tree \"*) exit 99;; esac")
	again, err := divergenceReadTest(t, root, "base", got.Baseline)
	if err != nil || !again.Unchanged {
		t.Fatalf("unchanged %+v %v", again, err)
	}
}

func TestDivergenceReadCancelCleanup(t *testing.T) {
	root := divergenceRepo(t, "sha1", "file")
	id, err := RepositoryIdentityChecked(historyContext(t), root)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("DIVERGENCE_MARKER", marker)
	divergenceWrapper(t, "case \" $* \" in *\" merge-tree \"*) /usr/bin/touch \"$DIVERGENCE_MARKER\"; exec /bin/sleep 30;; esac")
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if _, err := os.Stat(marker); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	defer cancel()
	_, err = ReadDivergenceChecked(WithReadLimit(ctx, 4<<20), id, "base", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("merge did not start")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("temp leak %v %v", entries, err)
	}
}
