package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopGitCompare(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	readGit(t, root, "branch", "other")
	readGit(t, root, "commit", "--allow-empty", "-qm", "current")
	file := gitFileTestParams(t, c, root)
	p := rpc.GitCompareParams{Path: root, Expected: file.Expected, Current: "main", Other: "other", Limit: 1000}
	var out rpc.GitCompareResult
	err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.compare", p, &out)
	if err != nil || out.Identity != p.Expected || out.Current != p.Current || out.Other != p.Other || len(out.OnlyCurrent) != 1 || len(out.OnlyOther) != 0 {
		t.Fatalf("result %+v %v", out, err)
	}
	raw, _ := json.Marshal(p)
	_, err = d.gitCompare(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	p.Limit = 1001
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.compare", p, &out)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
	p.Limit = 1000
	p.Expected.RootInode++
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.compare", p, &out)
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitCompareChanged(t *testing.T) {
	for _, kind := range []string{"branch", "pseudoref", "identity", "shallow"} {
		t.Run(kind, func(t *testing.T) {
			_, c := gitReadClient(t)
			root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
			base := readGit(t, root, "rev-parse", "HEAD")
			readGit(t, root, "branch", "other")
			readGit(t, root, "commit", "--allow-empty", "-qm", "current")
			file := gitFileTestParams(t, c, root)
			p := rpc.GitCompareParams{Path: root, Expected: file.Expected, Current: "main", Other: "other", Limit: 1000}
			if kind == "pseudoref" {
				readGit(t, root, "update-ref", "ORIG_HEAD", base)
				p.Other = "ORIG_HEAD"
			}
			t.Setenv("COMPARE_ROOT", root)
			t.Setenv("COMPARE_NEXT", readGit(t, root, "rev-parse", "HEAD"))
			scripts := map[string]string{
				"branch":    "/usr/bin/git -C \"$COMPARE_ROOT\" update-ref refs/heads/other \"$COMPARE_NEXT\"",
				"pseudoref": "/usr/bin/git -C \"$COMPARE_ROOT\" update-ref ORIG_HEAD \"$COMPARE_NEXT\"",
				"identity":  "/bin/mv \"$COMPARE_ROOT/.git\" \"$COMPARE_ROOT/.git-old\"; /bin/cp -R \"$COMPARE_ROOT/.git-old\" \"$COMPARE_ROOT/.git\"",
				"shallow":   "/usr/bin/printf '%s\\n' \"$COMPARE_NEXT\" > \"$COMPARE_ROOT/.git/shallow\"",
			}
			gitReadWrapper(t, "case \" $* \" in *\" log \"*) /usr/bin/git \"$@\" || exit $?; "+scripts[kind]+"; exit 0;; esac")
			var out rpc.GitCompareResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.compare", p, &out)
			desktopErrorCode(t, err, rpc.CodeConflict)
		})
	}
}
