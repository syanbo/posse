package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopGitDiscoverParentLinkChanged(t *testing.T) {
	_, c := gitReadClient(t)
	base := t.TempDir()
	first := readGitRepo(t, filepath.Join(base, "first"), true)
	second := readGitRepo(t, filepath.Join(base, "second"), true)
	os.Mkdir(filepath.Join(first, "child"), 0700)
	os.Mkdir(filepath.Join(second, "child"), 0700)
	link := filepath.Join(base, "link")
	if err := os.Symlink(filepath.Join(first, "child"), link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("READ_LINK", link)
	t.Setenv("READ_NEXT", filepath.Join(second, "child"))
	gitReadWrapper(t, "case \" $* \" in *\" ls-files \"*) /bin/rm \"$READ_LINK\"; /bin/ln -s \"$READ_NEXT\" \"$READ_LINK\";; esac")
	_, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: link + "/.."})
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitReadGitDirChanged(t *testing.T) {
	_, c := gitReadClient(t)
	base := t.TempDir()
	root := readGitRepo(t, filepath.Join(base, "repo"), true)
	first, second := filepath.Join(base, "first"), filepath.Join(base, "second")
	readGit(t, root, "worktree", "add", "-qb", "one", first)
	readGit(t, root, "worktree", "add", "-qb", "two", second)
	a, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: first})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: second})
	if err != nil {
		t.Fatal(err)
	}
	if a.Identity.CommonDir != b.Identity.CommonDir {
		t.Fatal("fixture does not share common dir")
	}
	t.Setenv("READ_FIRST", filepath.Join(first, ".git"))
	t.Setenv("READ_SECOND", filepath.Join(second, ".git"))
	gitReadWrapper(t, "case \" $* \" in *\" status \"*) /usr/bin/git \"$@\" || exit $?; /bin/cp \"$READ_SECOND\" \"$READ_FIRST\"; exit 0;; esac")
	_, err = c.GitStatus(t.Context(), rpc.GitStatusParams{Path: first})
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitDiscoverPermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions")
	}
	_, c := gitReadClient(t)
	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(denied, 0700) })
	_, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: root})
	desktopErrorCode(t, err, rpc.CodePermissionDenied)
}
