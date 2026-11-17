package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/rpc"
)

func pushPreviewParams(t *testing.T, c *gitReadTestClient) rpc.GitPushPreviewParams {
	t.Helper()
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	base := readGit(t, root, "rev-parse", "HEAD")
	readGit(t, root, "remote", "add", "origin", "https://example.invalid/repo")
	readGit(t, root, "update-ref", "refs/remotes/origin/main", base)
	readGit(t, root, "config", "branch.main.remote", "origin")
	readGit(t, root, "config", "branch.main.merge", "refs/heads/main")
	readGit(t, root, "commit", "--allow-empty", "-qm", "ahead")
	f := gitFileTestParams(t, c, root)
	return rpc.GitPushPreviewParams{Path: root, Expected: f.Expected, Branch: "main", Limit: 500}
}
func TestDesktopGitPushPreview(t *testing.T) {
	d, c := gitReadClient(t)
	p := pushPreviewParams(t, c)
	var out rpc.GitPushPreviewResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out); err != nil || out.Identity != p.Expected || out.Plan.Branch != p.Branch || len(out.Plan.Commits) != 1 || out.TrackingMissing {
		t.Fatalf("result %+v %v", out, err)
	}
	raw, _ := json.Marshal(p)
	_, err := d.gitPushPreview(context.Background(), raw)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	p.Branch = "--all"
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out)
	desktopErrorCode(t, err, rpc.CodeBadRequest)
	p.Branch = "main"
	p.Expected.RootInode++
	err = mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out)
	desktopErrorCode(t, err, rpc.CodeConflict)
}
func TestDesktopGitPushPreviewConfigErrors(t *testing.T) {
	for _, key := range []string{"push.default", "remote.origin.mirror", "remote.origin.push", "remote.origin.fetch"} {
		t.Run(key, func(t *testing.T) {
			_, c := gitReadClient(t)
			p := pushPreviewParams(t, c)
			readGit(t, p.Path, "config", key, "https://secret:token@example.invalid/repo")
			var out rpc.GitPushPreviewResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out)
			desktopErrorCode(t, err, rpc.CodeBadRequest)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatal("configuration value leaked")
			}
		})
	}
	for _, phase := range []string{"log", "final-identity"} {
		t.Run(phase, func(t *testing.T) {
			_, c := gitReadClient(t)
			p := pushPreviewParams(t, c)
			t.Setenv("PUSH_ROOT", p.Path)
			t.Setenv("PUSH_IDENTITY_SEEN", filepath.Join(t.TempDir(), "seen"))
			change := "/usr/bin/git -C \"$PUSH_ROOT\" config push.default https://secret:token@example.invalid/repo"
			if phase == "log" {
				gitReadWrapper(t, "case \" $* \" in *\" log \"*) /usr/bin/git \"$@\" || exit $?; "+change+"; exit 0;; esac")
			} else {
				gitReadWrapper(t, "case \" $* \" in *\" rev-parse --path-format=absolute --show-toplevel \"*) if test -f \"$PUSH_IDENTITY_SEEN\"; then "+change+"; else /usr/bin/touch \"$PUSH_IDENTITY_SEEN\"; fi;; esac")
			}
			var out rpc.GitPushPreviewResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out)
			desktopErrorCode(t, err, rpc.CodeBadRequest)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatal("configuration value leaked")
			}
		})
	}
}

func TestDesktopGitPushPreviewChanged(t *testing.T) {
	for _, kind := range []string{"branch", "tracking", "config", "identity"} {
		t.Run(kind, func(t *testing.T) {
			_, c := gitReadClient(t)
			p := pushPreviewParams(t, c)
			t.Setenv("PUSH_ROOT", p.Path)
			t.Setenv("PUSH_HEAD", readGit(t, p.Path, "rev-parse", "HEAD"))
			scripts := map[string]string{
				"branch":   "/usr/bin/git -C \"$PUSH_ROOT\" -c user.name=Test -c user.email=test@example.invalid commit --allow-empty -qm changed",
				"tracking": "/usr/bin/git -C \"$PUSH_ROOT\" update-ref refs/remotes/origin/main \"$PUSH_HEAD\"",
				"config":   "/usr/bin/git -C \"$PUSH_ROOT\" config remote.origin.url https://secret:token@example.invalid/changed",
				"identity": "/bin/mv \"$PUSH_ROOT/.git\" \"$PUSH_ROOT/.git-old\"; /bin/cp -R \"$PUSH_ROOT/.git-old\" \"$PUSH_ROOT/.git\"",
			}
			gitReadWrapper(t, "case \" $* \" in *\" log \"*) /usr/bin/git \"$@\" || exit $?; "+scripts[kind]+"; exit 0;; esac")
			var out rpc.GitPushPreviewResult
			err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out)
			desktopErrorCode(t, err, rpc.CodeConflict)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") {
				t.Fatal("URL leaked")
			}
		})
	}
}
func TestDesktopGitPushPreviewTrackingFailure(t *testing.T) {
	_, c := gitReadClient(t)
	p := pushPreviewParams(t, c)
	gitReadWrapper(t, "case \" $* \" in *\" show-ref \"*) /usr/bin/printf 'tracking ref is not readable\\n' >&2; exit 1;; esac")
	var out rpc.GitPushPreviewResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out); err == nil || !strings.Contains(err.Error(), "tracking ref is not readable") {
		t.Fatalf("tracking failure not preserved: %v", err)
	}
}

func TestDesktopGitPushPreviewMalformedNoReplay(t *testing.T) {
	_, c := gitReadClient(t)
	p := pushPreviewParams(t, c)
	marker := filepath.Join(t.TempDir(), "calls")
	t.Setenv("PUSH_CALLS", marker)
	gitReadWrapper(t, "case \" $* \" in *\" log \"*) /usr/bin/printf x >> \"$PUSH_CALLS\"; /usr/bin/printf 'malformed\\0'; exit 0;; esac")
	var out rpc.GitPushPreviewResult
	if err := mediaTestCall(t.Context(), c.Socket, *c.Desktop, "git.pushPreview", p, &out); err == nil {
		t.Fatal("bad output accepted")
	}
	calls, err := os.ReadFile(marker)
	if err != nil || string(calls) != "x" {
		t.Fatalf("calls %q err=%v", calls, err)
	}
}
