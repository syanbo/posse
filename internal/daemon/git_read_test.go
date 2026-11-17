package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func readGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	b, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, b, err)
	}
	return strings.TrimSpace(string(b))
}

func readGitRepo(t *testing.T, root string, commit bool) string {
	t.Helper()
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	readGit(t, root, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	readGit(t, root, "add", "a.txt")
	if commit {
		readGit(t, root, "commit", "-qm", "base")
	}
	return root
}

type gitReadTestClient struct {
	Socket  string
	Desktop *rpc.DesktopIdentity
}

func (c *gitReadTestClient) GitDiscover(ctx context.Context, p rpc.GitDiscoverParams) (out rpc.GitDiscoverResult, err error) {
	err = mediaTestCall(ctx, c.Socket, *c.Desktop, "git.discover", p, &out)
	return
}
func (c *gitReadTestClient) GitStatus(ctx context.Context, p rpc.GitStatusParams) (out rpc.GitStatusResult, err error) {
	err = mediaTestCall(ctx, c.Socket, *c.Desktop, "git.status", p, &out)
	return
}
func (c *gitReadTestClient) GitBranches(ctx context.Context, p rpc.GitBranchesParams) (out rpc.GitBranchesResult, err error) {
	err = mediaTestCall(ctx, c.Socket, *c.Desktop, "git.branches", p, &out)
	return
}

func gitReadClient(t *testing.T) (*Daemon, *gitReadTestClient) {
	t.Helper()
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	return d, &gitReadTestClient{Socket: socket, Desktop: &rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer", Epoch: 999}}
}

func TestDesktopGitRead(t *testing.T) {
	d, c := gitReadClient(t)
	for _, commit := range []bool{false, true} {
		t.Run(map[bool]string{false: "unborn", true: "committed"}[commit], func(t *testing.T) {
			root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), commit)
			if commit {
				readGit(t, root, "tag", "v1")
				readGit(t, root, "switch", "-qc", "topic")
				readGit(t, root, "switch", "-q", "main")
				os.WriteFile(filepath.Join(root, "a.txt"), []byte("stash\n"), 0600)
				readGit(t, root, "stash", "push", "-qm", "saved")
				os.WriteFile(filepath.Join(root, "a.txt"), []byte("base\nmore\n"), 0600)
			}
			found, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: root, SingleRepo: true})
			if err != nil || len(found.Repos) != 1 {
				t.Fatalf("discover: %+v %v", found, err)
			}
			st, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root, Full: true})
			if err != nil || st.Status == nil || st.Status.Head != "main" || st.Head["a.txt"].Added != 1 {
				t.Fatalf("status: %+v %v", st, err)
			}
			if st.Identity != found.Repos[0].Identity {
				t.Fatal("identity changed")
			}
			if commit && len(st.Stashes) != 1 {
				t.Fatalf("stashes: %+v", st.Stashes)
			}
			branches, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: root})
			if err != nil {
				t.Fatal(err)
			}
			if commit && (len(branches.Branches) != 2 || len(branches.Tags) != 1 || len(branches.Recent) != 2) {
				t.Fatalf("branches: %+v", branches)
			}
			if !commit && (len(branches.Branches) != 0 || len(branches.Recent) != 0) {
				t.Fatalf("unborn branches: %+v", branches)
			}
		})
	}
	stale := *c.Desktop
	stale.InstanceID = "old-instance"
	for _, method := range []string{"git.discover", "git.status", "git.branches"} {
		desktopErrorCode(t, mediaTestCall(t.Context(), c.Socket, stale, method, map[string]any{"path": t.TempDir()}, nil), rpc.CodeConflict)
		desktopErrorCode(t, rpc.Call(t.Context(), c.Socket, method, map[string]any{"path": t.TempDir()}, nil), rpc.CodeReadOnly)
	}
	d.desktop.mu.Lock()
	owner := d.desktop.owner
	d.desktop.mu.Unlock()
	if owner != "" {
		t.Fatal("read acquired control")
	}
}

func TestDesktopGitDiscoverAliases(t *testing.T) {
	_, c := gitReadClient(t)
	base := t.TempDir()
	root := readGitRepo(t, filepath.Join(base, "source"), true)
	readGitRepo(t, filepath.Join(root, "nested"), true)
	worktree := filepath.Join(base, "worktree")
	readGit(t, root, "worktree", "add", "-qb", "other", worktree)
	space := filepath.Join(base, "space")
	os.Mkdir(space, 0700)
	for _, name := range []string{"first", "second"} {
		if err := os.Symlink(root, filepath.Join(space, name)); err != nil {
			t.Fatal(err)
		}
	}
	found, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: space})
	if err != nil || len(found.Repos) != 2 {
		t.Fatalf("nested/alias discovery: %+v %v", found, err)
	}
	for _, repo := range found.Repos {
		if len(repo.Aliases) != 1 || !strings.HasPrefix(repo.Root, space+"/") || repo.Identity.Root != repo.Real {
			t.Fatalf("aliases: %+v", repo)
		}
	}
	one, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: filepath.Join(space, "first"), SingleRepo: true})
	if err != nil || len(one.Repos) != 1 {
		t.Fatalf("single: %+v %v", one, err)
	}
	a, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: worktree})
	if err != nil {
		t.Fatal(err)
	}
	if a.Identity.CommonDir != b.Identity.CommonDir || a.Identity.GitDir == b.Identity.GitDir || a.Identity.Root == b.Identity.Root {
		t.Fatalf("worktree identities: %+v %+v", a.Identity, b.Identity)
	}
}

func gitReadWrapper(t *testing.T, script string) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n"+script+"\nexec /usr/bin/git \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
}

func TestDesktopGitReadFailures(t *testing.T) {
	_, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	for _, method := range []string{"git.discover", "git.status", "git.branches"} {
		for _, p := range []any{map[string]any{"path": "relative"}, map[string]any{"path": "/nul\x00"}, map[string]any{"path": "/" + strings.Repeat("x", 4096)}, map[string]any{"path": root, "extra": true}} {
			desktopErrorCode(t, mediaTestCall(t.Context(), c.Socket, *c.Desktop, method, p, nil), rpc.CodeBadRequest)
		}
	}
	for _, stage := range []string{"diff", "stash", "reflog"} {
		t.Run(stage, func(t *testing.T) {
			gitReadWrapper(t, "case \" $* \" in *\" "+stage+" \"*) printf 'permission denied in query\\n' >&2; exit 128;; esac")
			var err error
			if stage == "reflog" {
				_, err = c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: root})
			} else {
				_, err = c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root, Full: true})
			}
			if err == nil || !strings.Contains(err.Error(), "permission denied in query") {
				t.Fatalf("lost %s failure: %v", stage, err)
			}
		})
	}
	plain := t.TempDir()
	found, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: plain})
	if err != nil || len(found.Repos) != 0 {
		t.Fatalf("ordinary folder: %+v %v", found, err)
	}
	if _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: plain}); err == nil {
		t.Fatal("nonrepository status succeeded")
	}
	os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("broken HEAD\n"), 0600)
	if _, err := c.GitDiscover(t.Context(), rpc.GitDiscoverParams{Path: root}); err == nil {
		t.Fatal("corrupt repository hidden")
	}
	if _, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root, Full: true}); err == nil {
		t.Fatal("corrupt status succeeded")
	}
	if _, err := c.GitBranches(t.Context(), rpc.GitBranchesParams{Path: root}); err == nil {
		t.Fatal("corrupt branches succeeded")
	}
}

func TestDesktopGitReadReplacement(t *testing.T) {
	_, c := gitReadClient(t)
	base := t.TempDir()
	root := readGitRepo(t, filepath.Join(base, "repo"), true)
	replacement := readGitRepo(t, filepath.Join(base, "next"), true)
	originalInfo, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Stat(replacement)
	if err != nil {
		t.Fatal(err)
	}
	if originalInfo.Size() != replacementInfo.Size() {
		t.Fatal("replacement directory size differs")
	}
	if err := os.Chtimes(replacement, originalInfo.ModTime(), originalInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	before, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("READ_ROOT", root)
	t.Setenv("READ_NEXT", replacement)
	t.Setenv("READ_OLD", filepath.Join(base, "old"))
	gitReadWrapper(t, "case \" $* \" in *\" status \"*) /usr/bin/git \"$@\" || exit $?; /bin/mv \"$READ_ROOT\" \"$READ_OLD\"; /bin/mv \"$READ_NEXT\" \"$READ_ROOT\"; exit 0;; esac")
	_, err = c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root})
	desktopErrorCode(t, err, rpc.CodeConflict)
	after, err := gitx.RepositoryIdentityChecked(t.Context(), root)
	if err != nil || before.Identity.RootInode == after.RootInode {
		t.Fatalf("root was not replaced: %+v %v", after, err)
	}
}

func TestDesktopGitReadLimitsAndCancel(t *testing.T) {
	d, c := gitReadClient(t)
	root := readGitRepo(t, filepath.Join(t.TempDir(), "repo"), true)
	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			redirect := ""
			if stream == "stderr" {
				redirect = " >&2"
			}
			gitReadWrapper(t, "case \" $* \" in *\" status \"*) /bin/dd if=/dev/zero bs=1048576 count=5"+redirect+" 2>/dev/null; exit 0;; esac")
			_, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root})
			if err == nil || !strings.Contains(err.Error(), gitx.ErrReadLimit.Error()) {
				t.Fatalf("%s limit: %v", stream, err)
			}
		})
	}
	t.Run("bounded-error", func(t *testing.T) {
		gitReadWrapper(t, "case \" $* \" in *\" status \"*) /bin/dd if=/dev/zero bs=1048576 count=1 >&2 2>/dev/null; exit 128;; esac")
		_, err := c.GitStatus(t.Context(), rpc.GitStatusParams{Path: root})
		if err == nil || len(err.Error()) > 9<<10 {
			t.Fatalf("unbounded error reply: %v", err)
		}
	})
	if _, err := gitReadResult(t.Context(), map[string]string{"data": strings.Repeat("\x00", rpc.GitReadLimit/2)}); err == nil {
		t.Fatal("JSON expansion bypassed limit")
	}
	gitReadWrapper(t, "case \" $* \" in *\" status \"*) exec /bin/sleep 20;; esac")
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: *c.Desktop, readOnly: true})
	raw, _ := json.Marshal(rpc.GitStatusParams{Path: root})
	_, err := d.gitStatus(ctx, raw)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation: %v", err)
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel2()
	if _, err := c.GitStatus(ctx2, rpc.GitStatusParams{Path: root}); err == nil {
		t.Fatal("cancelled RPC succeeded")
	}
}
