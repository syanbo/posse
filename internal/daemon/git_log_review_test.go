package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func TestDesktopGitHistoryReviewBoundaries(t *testing.T) {
	_, c := gitReadClient(t)
	source := readGitRepo(t, filepath.Join(t.TempDir(), "source"), true)
	if err := os.WriteFile(filepath.Join(source, "next"), []byte("next\n"), 0600); err != nil {
		t.Fatal(err)
	}
	message := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(message, []byte("caf\xe9\x1e\x1f end\n"), 0600); err != nil {
		t.Fatal(err)
	}
	readGit(t, source, "config", "i18n.commitEncoding", "ISO-8859-1")
	readGit(t, source, "add", "next")
	readGit(t, source, "commit", "-q", "-F", message)
	clone := filepath.Join(t.TempDir(), "clone")
	readGit(t, source, "clone", "--depth=1", "file://"+source, clone)
	p := logTestParams(t, c, clone)
	page, err := logTestCall(t, c, p)
	if err != nil || len(page.Commits) != 1 || page.Commits[0].Subject != "café\x1e\x1f end" {
		t.Fatalf("RPC encoded log %+v %v", page, err)
	}
	q := rpc.GitCommitFilesParams{Path: clone, Expected: p.Expected, Hash: page.Commits[0].Hash}
	detail, err := commitTestCall(t, c, q)
	if err != nil || !detail.Shallow || detail.DiffBase != "" || len(detail.Commit.Parents) != 1 || len(detail.Files) != 2 || detail.Commit.Subject != "café\x1e\x1f end" {
		t.Fatalf("RPC shallow detail %+v %v", detail, err)
	}
	readGit(t, clone, "fetch", "--deepen=1", "origin")
	detail, err = commitTestCall(t, c, q)
	if err != nil || detail.Shallow || detail.DiffBase != detail.Commit.Parents[0] || len(detail.Files) != 1 {
		t.Fatalf("RPC deepened detail %+v %v", detail, err)
	}
	p.Skip = page.NextSkip
	p.Baseline = page.Baseline
	_, err = logTestCall(t, c, p)
	desktopErrorCode(t, err, rpc.CodeConflict)

	rootParams := logTestParams(t, c, source)
	readGit(t, source, "update-ref", "ORIG_HEAD", "HEAD")
	rootParams.Filter = gitx.LogFilter{Branches: []string{"ORIG_HEAD"}}
	t.Setenv("READ_ROOT", source)
	gitReadWrapper(t, "case \" $* \" in *\" log \"*) /usr/bin/git \"$@\" || exit $?; /usr/bin/git -C \"$READ_ROOT\" update-ref ORIG_HEAD HEAD~1; exit 0;; esac")
	_, err = logTestCall(t, c, rootParams)
	desktopErrorCode(t, err, rpc.CodeConflict)
}
