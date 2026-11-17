package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopGitBlame(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	p0 := gitFileTestParams(t, c, root)
	p := rpc.GitBlameParams{Path: root, Expected: p0.Expected, File: "a.txt"}
	var out rpc.GitBlameResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blame", p, &out); err != nil || len(out.Lines) == 0 || out.Identity != p.Expected || out.File != p.File {
		t.Fatalf("result %+v %v", out, err)
	}
	raw, _ := json.Marshal(p)
	_, err := d.gitBlame(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	p.File = "../escape"
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blame", p, &out)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
	p.File = "a.txt"
	p.Expected.RootInode++
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blame", p, &out)
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitBlameInputsChanged(t *testing.T) {
	for _, kind := range []string{"file", "head", "index", "attrs", "identity"} {
		t.Run(kind, func(t *testing.T) {
			_, c := gitReadClient(t)
			root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
			p0 := gitFileTestParams(t, c, root)
			p := rpc.GitBlameParams{Path: root, Expected: p0.Expected, File: "a.txt"}
			t.Setenv("BLAME_ROOT", root)
			scripts := map[string]string{
				"file":     "/bin/cp -p \"$BLAME_ROOT/a.txt\" \"$BLAME_ROOT/stamp\"; /usr/bin/printf 'edit\\n' > \"$BLAME_ROOT/a.txt\"; /usr/bin/touch -r \"$BLAME_ROOT/stamp\" \"$BLAME_ROOT/a.txt\"",
				"head":     "/usr/bin/git -C \"$BLAME_ROOT\" -c user.name=Test -c user.email=test@example.invalid commit --allow-empty -qm changed",
				"index":    "/usr/bin/git -C \"$BLAME_ROOT\" update-index --force-remove a.txt",
				"attrs":    "/usr/bin/printf '* -text\\n' > \"$BLAME_ROOT/.gitattributes\"",
				"identity": "/bin/mv \"$BLAME_ROOT/.git\" \"$BLAME_ROOT/.git-old\"; /bin/cp -R \"$BLAME_ROOT/.git-old\" \"$BLAME_ROOT/.git\"",
			}
			gitReadWrapper(t, "case \" $* \" in *\" blame \"*) /usr/bin/git \"$@\" || exit $?; "+scripts[kind]+"; exit 0;; esac")
			var out rpc.GitBlameResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blame", p, &out)
			desktopErrorCode(t, err, rpc.CodeConflict)
		})
	}
}

func TestDesktopGitBlameNotText(t *testing.T) {
	_, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	p0 := gitFileTestParams(t, c, root)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte{'a', 0}, 0600); err != nil {
		t.Fatal(err)
	}
	var out rpc.GitBlameResult
	err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.blame", rpc.GitBlameParams{Path: root, Expected: p0.Expected, File: "a.txt"}, &out)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
}
