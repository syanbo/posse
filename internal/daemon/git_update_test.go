package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/writecoord"
)

func updateParams(t *testing.T, target rpc.GitFetchParams, name string) rpc.GitUpdateParams {
	t.Helper()
	branches, err := gitx.Branches(t.Context(), target.Expected.Root)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range branches {
		if !b.Remote && b.Name == name {
			return rpc.GitUpdateParams{GitFetchParams: target, Branch: b.Name, BranchOID: b.OID, Upstream: b.Upstream, Current: b.Current}
		}
	}
	t.Fatal("missing branch")
	return rpc.GitUpdateParams{}
}
func updateCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitUpdateParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.update", Params: raw}
}
func updateFile(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestDesktopGitUpdateCurrent(t *testing.T) {
	for _, mode := range []string{"clean-autostash", "autostash-conflict", "rebase-conflict"} {
		t.Run(mode, func(t *testing.T) {
			_, w, socket, owner, target, _ := fetchFixture(t)
			readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
			readGit(t, w.Path, "config", "user.name", "Update test")
			readGit(t, w.Path, "config", "user.email", "update@example.invalid")
			bare := readGit(t, w.Path, "remote", "get-url", "origin")
			seed := filepath.Join(t.TempDir(), "seed")
			readGit(t, w.Path, "clone", "-q", "-b", "main", bare, seed)
			updateFile(t, seed, "a.txt", "remote\n")
			readGit(t, seed, "commit", "-qam", "remote file")
			readGit(t, seed, "push", "-q", "origin", "main")
			remote := readGit(t, seed, "rev-parse", "HEAD")
			// 已存在的 autostash 必须保留，不能误认成这次遗留。
			updateFile(t, w.Path, "a.txt", "old stash\n")
			readGit(t, w.Path, "stash", "push", "-qm", "old")
			old := readGit(t, w.Path, "rev-parse", "stash@{0}")
			readGit(t, w.Path, "stash", "store", "-m", "autostash", old)
			switch mode {
			case "clean-autostash":
				updateFile(t, w.Path, "local.txt", "local commit\n")
				readGit(t, w.Path, "add", "local.txt")
				readGit(t, w.Path, "commit", "-qm", "local commit")
				updateFile(t, w.Path, "local.txt", "dirty\n")
			case "autostash-conflict":
				updateFile(t, w.Path, "a.txt", "dirty\n")
			case "rebase-conflict":
				updateFile(t, w.Path, "a.txt", "local commit\n")
				readGit(t, w.Path, "commit", "-qam", "local commit")
			}
			p := updateParams(t, target, "main")
			var out rpc.GitUpdateResult
			call := updateCall(owner, 1, p)
			if err := rpc.Call(t.Context(), socket, "desktop.call", call, &out); err != nil {
				t.Fatal(err)
			}
			if !out.Started || len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, []string{"pull", "--rebase", "--autostash"}) {
				t.Fatalf("update %+v", out)
			}
			switch mode {
			case "clean-autostash":
				if out.Status != rpc.GitFetchSucceeded || out.Autostash != nil || out.Incoming != 2 {
					t.Fatalf("clean %+v", out)
				}
				if readGit(t, w.Path, "rev-parse", "HEAD^") != remote || readGit(t, w.Path, "log", "-1", "--format=%s") != "local commit" || readGit(t, w.Path, "rev-parse", "stash@{0}") != old {
					t.Fatal("rebase or stash lost")
				}
				b, _ := os.ReadFile(filepath.Join(w.Path, "local.txt"))
				if string(b) != "dirty\n" {
					t.Fatal("dirty changes lost")
				}
			case "autostash-conflict":
				if out.Status != rpc.GitFetchSucceeded || out.Error != nil || out.Autostash == nil || !out.Autostash.Conflicts || out.Autostash.Stash == old || out.Autostash.Stash != readGit(t, w.Path, "rev-parse", "stash@{0}") {
					t.Fatalf("autostash %+v", out)
				}
				if readGit(t, w.Path, "rev-parse", "HEAD") != remote || readGit(t, w.Path, "diff", "--name-only", "--diff-filter=U") != "a.txt" {
					t.Fatal("autostash conflicts not preserved")
				}
			case "rebase-conflict":
				if out.Status != rpc.GitFetchFailed || out.Autostash != nil {
					t.Fatalf("rebase %+v", out)
				}
				if _, err := os.Stat(filepath.Join(p.Expected.GitDir, "rebase-merge")); err != nil {
					t.Fatal("rebase was aborted")
				}
				if readGit(t, w.Path, "diff", "--name-only", "--diff-filter=U") != "a.txt" {
					t.Fatal("conflict missing")
				}
			}
			var cached rpc.GitUpdateResult
			op := desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1})
			if json.Unmarshal(op.Result, &cached) != nil || !reflect.DeepEqual(cached, out) {
				t.Fatal("structured result lost")
			}
		})
	}
}
func TestDesktopGitUpdateOther(t *testing.T) {
	for _, diverged := range []bool{false, true} {
		t.Run(map[bool]string{false: "fast-forward", true: "diverged"}[diverged], func(t *testing.T) {
			_, w, socket, owner, target, latest := fetchFixture(t)
			readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
			if diverged {
				readGit(t, w.Path, "commit", "--allow-empty", "-qm", "local")
			}
			before := readGit(t, w.Path, "rev-parse", "main")
			readGit(t, w.Path, "switch", "-qc", "other")
			p := updateParams(t, target, "main")
			var out rpc.GitUpdateResult
			if err := rpc.Call(t.Context(), socket, "desktop.call", updateCall(owner, 1, p), &out); err != nil {
				t.Fatal(err)
			}
			if !out.Started || len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, []string{"fetch", "origin", "main:refs/heads/main"}) {
				t.Fatalf("fetch %+v", out)
			}
			want, status := latest, rpc.GitFetchSucceeded
			if diverged {
				want, status = before, rpc.GitFetchFailed
			}
			if out.Status != status || readGit(t, w.Path, "rev-parse", "main") != want || readGit(t, w.Path, "branch", "--show-current") != "other" {
				t.Fatalf("other %+v", out)
			}
			if !diverged && out.Incoming != 1 {
				t.Fatal("incoming", out.Incoming)
			}
		})
	}
}
func TestDesktopGitUpdateChanged(t *testing.T) {
	for _, mode := range []string{"oid", "head", "upstream", "other-oid", "other-head", "worktree", "workspace", "identity", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			_, w, socket, owner, target, _ := fetchFixture(t)
			readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
			if strings.HasPrefix(mode, "other-") || mode == "worktree" {
				readGit(t, w.Path, "switch", "-qc", "other")
			}
			p := updateParams(t, target, "main")
			switch mode {
			case "oid":
				readGit(t, w.Path, "commit", "--allow-empty", "-qm", "changed")
			case "head":
				readGit(t, w.Path, "switch", "-qc", "other")
			case "upstream":
				readGit(t, w.Path, "config", "branch.main.merge", "refs/heads/other")
			case "other-head":
				readGit(t, w.Path, "switch", "-q", "main")
			case "other-oid":
				p.BranchOID = strings.Repeat("a", 40)
			case "worktree":
				readGit(t, w.Path, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "main")
			case "workspace":
				p.WorkspacePath += "-changed"
			case "identity":
				p.Expected.RootInode++
			case "deleted":
				readGit(t, w.Path, "switch", "-qc", "other")
				readGit(t, w.Path, "branch", "-D", "main")
			}
			gitReadWrapper(t, "case \" $* \" in *\" pull \"*|*\" fetch \"*) echo forbidden >&2; exit 91;; esac")
			var out rpc.GitUpdateResult
			if err := rpc.Call(t.Context(), socket, "desktop.call", updateCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatalf("changed %+v %v", out, err)
			}
		})
	}
}
func TestDesktopGitUpdateLockAndControl(t *testing.T) {
	d, w, socket, owner, target, _ := fetchFixture(t)
	readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
	p := updateParams(t, target, "main")
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.update", p, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", updateCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Keys: []string{"git:" + resourceKey(p.Expected.CommonDir)}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	request := updateCall(owner, 1, p)
	done := make(chan error, 1)
	var out rpc.GitUpdateResult
	go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", request, &out) }()
	waitFor(t, "update accepted", func() bool { return desktopOperationGet(t, d, request).State == "running" })
	readGit(t, w.Path, "commit", "--allow-empty", "-qm", "changed while queued")
	release()
	if err := <-done; err != nil || out.Status != rpc.GitFetchConflict || out.Started {
		t.Fatalf("queued %+v %v", out, err)
	}
}
func TestDesktopGitUpdateNoUpstream(t *testing.T) {
	_, _, socket, owner, target, _ := fetchFixture(t)
	p := updateParams(t, target, "main")
	var out rpc.GitUpdateResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", updateCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchFailed || out.Started || out.Error == nil || !strings.Contains(out.Error.Message, gitx.ErrNoUpstream.Error()) {
		t.Fatalf("no upstream %+v %v", out, err)
	}
}
func TestDesktopGitUpdateDeadline(t *testing.T) {
	d, w, _, owner, target, _ := fetchFixture(t)
	readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
	p := updateParams(t, target, "main")
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("UPDATE_STARTED", marker)
	gitReadWrapper(t, "case \" $* \" in *\" pull \"*) /usr/bin/touch \"$UPDATE_STARTED\"; exec /bin/sleep 30;; esac")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: owner})
	raw, _ := json.Marshal(p)
	value, err := d.gitUpdate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	out := value.(rpc.GitUpdateResult)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("pull never started")
	}
	if out.Status != rpc.GitFetchTimedOut || !out.Started {
		t.Fatalf("deadline %+v", out)
	}
}
func TestDesktopGitUpdateDisconnectTakeoverAndCancel(t *testing.T) {
	for _, mode := range []string{"finish", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d, w, socket, owner, target, _ := fetchFixture(t)
			readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
			p := updateParams(t, target, "main")
			started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
			t.Setenv("UPDATE_STARTED", started)
			t.Setenv("UPDATE_GATE", gate)
			gitReadWrapper(t, "case \" $* \" in *\" pull \"*) /usr/bin/touch \"$UPDATE_STARTED\"; while test ! -f \"$UPDATE_GATE\"; do /bin/sleep 0.01; done;; esac")
			t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := updateCall(owner, 1, p)
			done := make(chan error, 1)
			go func() { done <- rpc.Call(ctx, socket, "desktop.call", request, nil) }()
			waitFor(t, "update started", func() bool { _, err := os.Stat(started); return err == nil })
			cancel()
			if err := <-done; err == nil {
				t.Fatal("response not lost")
			}
			current := desktopTakeTest(t, d, "new-owner")
			ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
			if op := desktopStatusRPC(t, socket, ref); op.State != "running" {
				t.Fatalf("disconnect canceled update %+v", op)
			}
			if mode == "cancel" {
				desktopCancelRPC(t, socket, current, ref)
			} else {
				if err := os.WriteFile(gate, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "update finished", func() bool { return desktopOperationGet(t, d, request).State == "done" })
			op := desktopStatusRPC(t, socket, ref)
			var result rpc.GitUpdateResult
			if op.Error != nil || json.Unmarshal(op.Result, &result) != nil {
				t.Fatalf("result %+v", op)
			}
			want := rpc.GitFetchSucceeded
			if mode == "cancel" {
				want = rpc.GitFetchCanceled
			}
			if result.Status != want || !result.Started || len(result.Logs) != 1 || result.Logs[0].Canceled != (mode == "cancel") {
				t.Fatalf("outcome %+v", result)
			}
		})
	}
}

func TestDesktopGitUpdateBounded(t *testing.T) {
	_, w, socket, owner, target, latest := fetchFixture(t)
	readGit(t, w.Path, "branch", "--set-upstream-to=origin/main", "main")
	p := updateParams(t, target, "main")
	gitReadWrapper(t, "case \" $* \" in *\" pull \"*) i=0; while test $i -lt 10000; do printf 'ordinary progress\\n'; i=$((i+1)); done; printf 'remote: secret server message\\nFrom https://person:private-value@example.invalid/repo?key=hidden-value\\n' >&2;; esac")
	var out rpc.GitUpdateResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", updateCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchSucceeded || !out.LogsTruncated || readGit(t, w.Path, "rev-parse", "main") != latest {
		t.Fatalf("bounded %+v %v", out, err)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"private-value", "hidden-value", "example.invalid", "secret server message"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("leaked output")
		}
	}
	if len(raw) > rpc.GitUpdateResultLimit {
		t.Fatal("unbounded result")
	}
}

func TestDesktopGitUpdatePlusBranchDoesNotForce(t *testing.T) {
	_, w, socket, owner, target, latest := fetchFixture(t)
	bare := readGit(t, w.Path, "remote", "get-url", "origin")
	readGit(t, bare, "update-ref", "refs/heads/+main", latest)
	readGit(t, w.Path, "update-ref", "refs/remotes/origin/+main", "HEAD")
	readGit(t, w.Path, "branch", "--set-upstream-to=origin/+main", "main")
	readGit(t, w.Path, "commit", "--allow-empty", "-qm", "local divergence")
	before := readGit(t, w.Path, "rev-parse", "main")
	readGit(t, w.Path, "switch", "-qc", "other")
	p := updateParams(t, target, "main")
	var out rpc.GitUpdateResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", updateCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchFailed || !out.Started || readGit(t, w.Path, "rev-parse", "main") != before {
		t.Fatalf("plus branch forced update: %+v %v", out, err)
	}
	if len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, []string{"fetch", "origin", "refs/heads/+main:refs/heads/main"}) {
		t.Fatal(out.Logs)
	}
}
