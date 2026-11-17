package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestDesktopGitWorktreeCompare(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	old := gitFileTestParams(t, c, root)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("modified\n"), 0600)
	p := rpc.GitWorktreeCompareParams{Path: root, Expected: old.Expected, Revision: "HEAD"}
	var out rpc.GitWorktreeCompareResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.worktreeCompare", p, &out); err != nil || len(out.Files) != 1 || out.Files[0].Kind != "file" || out.Identity != p.Expected {
		t.Fatalf("result %+v %v", out, err)
	}
	p.Revision, p.Baseline = out.Commit, out.Baseline
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.worktreeCompare", p, &out); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(p)
	_, err := d.gitWorktreeCompare(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed\n"), 0600)
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.worktreeCompare", p, &out)
	desktopErrorCode(t, err, rpc.CodeConflict)
	p.Baseline = ""
	p.Revision = "--all"
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.worktreeCompare", p, &out)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
}

func TestDesktopGitWorktreeChanged(t *testing.T) {
	for _, kind := range []string{"same-stamp", "index", "revision", "identity"} {
		t.Run(kind, func(t *testing.T) {
			_, c := gitReadClient(t)
			root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
			old := gitFileTestParams(t, c, root)
			p := rpc.GitWorktreeCompareParams{Path: root, Expected: old.Expected, Revision: "HEAD"}
			t.Setenv("COMPARE_ROOT", root)
			scripts := map[string]string{
				"same-stamp": "/bin/cp -p \"$COMPARE_ROOT/a.txt\" \"$COMPARE_ROOT/stamp\"; /usr/bin/printf 'edit\\n' > \"$COMPARE_ROOT/a.txt\"; /usr/bin/touch -r \"$COMPARE_ROOT/stamp\" \"$COMPARE_ROOT/a.txt\"",
				"index":      "/usr/bin/git -C \"$COMPARE_ROOT\" update-index --force-remove a.txt",
				"revision":   "/usr/bin/git -C \"$COMPARE_ROOT\" -c user.name=Test -c user.email=test@example.invalid commit --allow-empty -qm changed",
				"identity":   "/bin/mv \"$COMPARE_ROOT/.git\" \"$COMPARE_ROOT/.git-old\"; /bin/cp -R \"$COMPARE_ROOT/.git-old\" \"$COMPARE_ROOT/.git\"",
			}
			// wrapper 在隔离命令完成后改目标；显式清除隔离的重定向再操作测试仓库。
			script := scripts[kind]
			gitReadWrapper(t, "case \" $* \" in *\" diff --no-ext-diff \"*) /usr/bin/git \"$@\" || exit $?; unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE; "+script+"; exit 0;; esac")
			var out rpc.GitWorktreeCompareResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.worktreeCompare", p, &out)
			desktopErrorCode(t, err, rpc.CodeConflict)
		})
	}
}
