package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func divergenceParams(t *testing.T, c *gitReadTestClient) rpc.GitDivergenceParams {
	t.Helper()
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	readGit(t, root, "branch", "base")
	if err := os.WriteFile(filepath.Join(root, "conflict"), []byte("ours\n"), 0600); err != nil {
		t.Fatal(err)
	}
	readGit(t, root, "add", ".")
	readGit(t, root, "commit", "-qm", "ours")
	readGit(t, root, "checkout", "-q", "base")
	if err := os.WriteFile(filepath.Join(root, "conflict"), []byte("theirs\n"), 0600); err != nil {
		t.Fatal(err)
	}
	readGit(t, root, "add", ".")
	readGit(t, root, "commit", "-qm", "theirs")
	readGit(t, root, "checkout", "-q", "main")
	p := gitFileTestParams(t, c, root)
	return rpc.GitDivergenceParams{Path: root, Expected: p.Expected, BaseRef: "base"}
}

func TestDesktopGitDivergence(t *testing.T) {
	d, c := gitReadClient(t)
	p := divergenceParams(t, c)
	var out rpc.GitDivergenceResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out); err != nil || out.Prediction != gitx.DivergenceCompleted || len(out.Conflicts) != 1 || out.Identity != p.Expected {
		t.Fatalf("result %+v %v", out, err)
	}
	p.PreviousBaseline = out.Baseline
	out = rpc.GitDivergenceResult{}
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out); err != nil || !out.Unchanged || out.Ahead != 0 || out.Prediction != "" || out.Conflicts != nil {
		t.Fatalf("unchanged %+v %v", out, err)
	}
	p.PreviousBaseline = ""
	readGit(t, p.Path, "config", "merge.renormalize", "true")
	out = rpc.GitDivergenceResult{}
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out); err != nil || out.Prediction != gitx.DivergenceUnavailable || out.Reason != gitx.DivergenceConfig || out.Ahead != 1 || out.Behind != 1 {
		t.Fatalf("unavailable %+v %v", out, err)
	}
	raw, _ := json.Marshal(p)
	_, err := d.gitDivergence(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	p.BaseRef = "--all"
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
	p.BaseRef = "base"
	p.Expected.RootInode++
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out)
	desktopErrorCode(t, err, rpc.CodeConflict)
}

func TestDesktopGitDivergenceChanged(t *testing.T) {
	for _, kind := range []string{"head", "base", "attributes", "config", "identity", "shallow"} {
		t.Run(kind, func(t *testing.T) {
			_, c := gitReadClient(t)
			p := divergenceParams(t, c)
			t.Setenv("DIVERGENCE_ROOT", p.Path)
			t.Setenv("DIVERGENCE_HEAD", readGit(t, p.Path, "rev-parse", "HEAD"))
			t.Setenv("DIVERGENCE_BASE", readGit(t, p.Path, "rev-parse", "base"))
			t.Setenv("DIVERGENCE_COMMON", readGit(t, p.Path, "merge-base", "HEAD", "base"))
			scripts := map[string]string{
				"head":       "/usr/bin/git -C \"$DIVERGENCE_ROOT\" update-ref refs/heads/main \"$DIVERGENCE_BASE\"",
				"base":       "/usr/bin/git -C \"$DIVERGENCE_ROOT\" update-ref refs/heads/base \"$DIVERGENCE_HEAD\"",
				"attributes": "/usr/bin/printf '* merge=union\\n' > \"$DIVERGENCE_ROOT/.git/info/attributes\"",
				"config":     "/usr/bin/git -C \"$DIVERGENCE_ROOT\" config merge.default union",
				"identity":   "/bin/mv \"$DIVERGENCE_ROOT/.git\" \"$DIVERGENCE_ROOT/.git-old\"; /bin/cp -R \"$DIVERGENCE_ROOT/.git-old\" \"$DIVERGENCE_ROOT/.git\"",
				"shallow":    "/usr/bin/printf '%s\\n' \"$DIVERGENCE_COMMON\" > \"$DIVERGENCE_ROOT/.git/shallow\"",
			}
			// merge 子进程已隔离 GIT_*，修改目标配置时也不继承临时 GIT_DIR。
			gitReadWrapper(t, "case \" $* \" in *\" merge-tree \"*) /usr/bin/git \"$@\"; result=$?; unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE; "+scripts[kind]+"; exit $result;; esac")
			var out rpc.GitDivergenceResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out)
			desktopErrorCode(t, err, rpc.CodeConflict)
		})
	}
}

func TestDesktopGitDivergenceMalformedNoReplay(t *testing.T) {
	_, c := gitReadClient(t)
	p := divergenceParams(t, c)
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("DIVERGENCE_CALLS", calls)
	gitReadWrapper(t, "case \" $* \" in *\" merge-tree \"*) /usr/bin/printf x >> \"$DIVERGENCE_CALLS\"; /usr/bin/printf 'malformed\\0'; exit 0;; esac")
	var out rpc.GitDivergenceResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.divergence", p, &out); err == nil {
		t.Fatal("malformed output accepted")
	}
	data, err := os.ReadFile(calls)
	if err != nil || string(data) != "x" {
		t.Fatalf("calls %q %v", data, err)
	}
}
