package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/writecoord"
)

func fetchFixture(t *testing.T) (*Daemon, store.Workspace, string, rpc.DesktopIdentity, rpc.GitFetchParams, string) {
	t.Helper()
	d, w := newTestDaemon(t)
	readGitRepo(t, w.Path, true)
	bare := filepath.Join(t.TempDir(), "remote.git")
	readGit(t, w.Path, "init", "--bare", "-q", bare)
	readGit(t, w.Path, "remote", "add", "origin", bare)
	readGit(t, w.Path, "push", "-q", "origin", "main")
	seed := filepath.Join(t.TempDir(), "seed")
	readGit(t, w.Path, "clone", "-q", "-b", "main", bare, seed)
	readGit(t, seed, "commit", "--allow-empty", "-qm", "remote change")
	readGit(t, seed, "push", "-q", "origin", "main")
	latest := readGit(t, seed, "rev-parse", "HEAD")
	id, err := gitx.RepositoryIdentityChecked(gitx.WithReadLimit(t.Context(), rpc.GitReadLimit), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	owner := desktopTakeTest(t, d, "owner")
	return d, w, socket, owner, rpc.GitFetchParams{WorkspaceID: w.ID, WorkspacePath: w.Path, Expected: id}, latest
}

func fetchCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitFetchParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.fetch", Params: raw}
}

func TestDesktopGitFetch(t *testing.T) {
	_, w, socket, owner, p, latest := fetchFixture(t)
	readGit(t, w.Path, "update-ref", "refs/remotes/origin/stale", "HEAD")
	request := fetchCall(owner, 1, p)
	var out rpc.GitFetchResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", request, &out); err != nil || out.Status != rpc.GitFetchSucceeded || !out.Started || out.Error != nil || len(out.Logs) != 1 {
		t.Fatalf("fetch %+v %v", out, err)
	}
	if got := readGit(t, w.Path, "rev-parse", "refs/remotes/origin/main"); got != latest {
		t.Fatalf("did not fetch %s", got)
	}
	readGit(t, w.Path, "rev-parse", "--verify", "refs/remotes/origin/stale")
	if !reflect.DeepEqual(out.Logs[0].Args, []string{"fetch", "--all"}) {
		t.Fatalf("changed fetch args %+v", out.Logs)
	}
	ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
	op := desktopStatusRPC(t, socket, ref)
	var cached rpc.GitFetchResult
	if op.State != "done" || op.Error != nil || json.Unmarshal(op.Result, &cached) != nil || !reflect.DeepEqual(out, cached) {
		t.Fatalf("cached outcome %+v", op)
	}
	var again rpc.GitFetchResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", request, &again); err != nil || !reflect.DeepEqual(again, out) {
		t.Fatalf("duplicate %+v %v", again, err)
	}
	request.Params = json.RawMessage(`{"workspaceID":"different"}`)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", request, nil), rpc.CodeConflict)
}

func TestDesktopGitFetchGuards(t *testing.T) {
	d, _, socket, owner, p, _ := fetchFixture(t)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.fetch", p, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", fetchCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	old := owner
	old.Epoch--
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", fetchCall(old, 1, p), nil), rpc.CodeReadOnly)
	oversized := fetchCall(owner, 1, p)
	oversized.Params = json.RawMessage(`{"ignored":"` + strings.Repeat("x", rpc.GitFetchParamsLimit) + `"}`)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", oversized, nil), rpc.CodeBadRequest)
	if out := desktopOperationGet(t, d, oversized); out.State != "unknown" {
		t.Fatal("oversize accepted")
	}
	bad := fetchCall(owner, 2, p)
	bad.Params = append(bad.Params[:len(bad.Params)-1], []byte(`,"argv":["--prune"]}`)...)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", bad, nil), rpc.CodeBadRequest)
}

func TestDesktopGitFetchPartialFailureAndLogs(t *testing.T) {
	for _, mode := range []string{"partial", "large"} {
		t.Run(mode, func(t *testing.T) {
			_, w, socket, owner, p, latest := fetchFixture(t)
			if mode == "partial" {
				readGit(t, w.Path, "remote", "add", "broken", filepath.Join(t.TempDir(), "missing"))
			} else {
				gitReadWrapper(t, "case \" $* \" in *\" fetch \"*) i=0; while test $i -lt 10000; do printf 'ordinary progress\\n'; i=$((i+1)); done; printf 'From https://person:private-value@example.invalid/repo?key=hidden-value\\n' >&2;; esac")
			}
			var out rpc.GitFetchResult
			err := rpc.Call(t.Context(), socket, "desktop.call", fetchCall(owner, 1, p), &out)
			if err != nil || !out.Started || len(out.Logs) != 1 {
				t.Fatalf("result %+v %v", out, err)
			}
			if mode == "partial" && (out.Status != rpc.GitFetchFailed || out.Error == nil || out.Logs[0].Err == "") {
				t.Fatalf("partial %+v", out)
			}
			if mode == "large" && (out.Status != rpc.GitFetchSucceeded || !out.LogsTruncated || out.Error != nil) {
				t.Fatalf("large %+v", out)
			}
			if got := readGit(t, w.Path, "rev-parse", "refs/remotes/origin/main"); got != latest {
				t.Fatal("partial refs were rolled back or not updated")
			}
			raw, _ := json.Marshal(out)
			if len(raw) > rpc.GitFetchResultLimit || strings.Contains(string(raw), "private-value") || strings.Contains(string(raw), "hidden-value") || strings.Contains(string(raw), "example.invalid") {
				t.Fatalf("unbounded or leaking result (%d bytes)", len(raw))
			}
			op := desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1})
			if op.Error != nil || len(op.Result) > rpc.GitFetchResultLimit {
				t.Fatalf("result evicted %+v", op)
			}
		})
	}
}

func TestDesktopGitFetchDisconnectTakeoverAndCancel(t *testing.T) {
	for _, mode := range []string{"finish", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d, _, socket, owner, p, _ := fetchFixture(t)
			started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
			t.Setenv("FETCH_STARTED", started)
			t.Setenv("FETCH_GATE", gate)
			gitReadWrapper(t, "case \" $* \" in *\" fetch \"*) /usr/bin/touch \"$FETCH_STARTED\"; while test ! -f \"$FETCH_GATE\"; do /bin/sleep 0.01; done;; esac")
			t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := fetchCall(owner, 1, p)
			done := make(chan error, 1)
			go func() { done <- rpc.Call(ctx, socket, "desktop.call", request, nil) }()
			waitFor(t, "fetch started", func() bool { _, err := os.Stat(started); return err == nil })
			cancel()
			if err := <-done; err == nil {
				t.Fatal("response not lost")
			}
			current := desktopTakeTest(t, d, "new-owner")
			ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
			if op := desktopStatusRPC(t, socket, ref); op.State != "running" {
				t.Fatalf("disconnect canceled fetch %+v", op)
			}
			if mode == "cancel" {
				desktopCancelRPC(t, socket, current, ref)
			} else {
				if err := os.WriteFile(gate, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "fetch finished", func() bool { return desktopOperationGet(t, d, request).State == "done" })
			op := desktopStatusRPC(t, socket, ref)
			var result rpc.GitFetchResult
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

func TestDesktopGitFetchQueuedTargetChanged(t *testing.T) {
	for _, mode := range []string{"identity", "workspace", "project", "alias", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d, w, socket, owner, p, _ := fetchFixture(t)
			if mode == "alias" {
				alias := filepath.Join(t.TempDir(), "workspace")
				if err := os.Symlink(w.Path, alias); err != nil {
					t.Fatal(err)
				}
				if err := d.store.SetWorkspacePath(w.ID, alias, ""); err != nil {
					t.Fatal(err)
				}
				p.WorkspacePath = alias
			}
			counter := filepath.Join(t.TempDir(), "queries")
			t.Setenv("FETCH_QUERIES", counter)
			gitReadWrapper(t, "case \" $* \" in *\" --git-common-dir \"*) /usr/bin/git \"$@\" || exit $?; printf x >> \"$FETCH_QUERIES\"; exit 0;; esac")
			if _, err := d.fetchTargetSnapshot(t.Context(), p); err != nil {
				t.Fatal(err)
			}
			queries, err := os.ReadFile(counter)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(counter, nil, 0600); err != nil {
				t.Fatal(err)
			}
			release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Paths: []string{resourceKey(p.Expected.Root)}})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			request := fetchCall(owner, 1, p)
			done := make(chan rpc.GitFetchResult, 1)
			failures := make(chan error, 1)
			go func() {
				var out rpc.GitFetchResult
				err := rpc.Call(t.Context(), socket, "desktop.call", request, &out)
				done <- out
				failures <- err
			}()
			waitFor(t, "initial scope inspected", func() bool { b, _ := os.ReadFile(counter); return len(b) >= len(queries) })
			switch mode {
			case "identity":
				if err := os.Rename(filepath.Join(w.Path, ".git"), filepath.Join(w.Path, ".git-old")); err != nil {
					t.Fatal(err)
				}
				readGit(t, w.Path, "init", "-q", "-b", "main")
			case "workspace":
				if err := d.store.SetWorkspacePath(w.ID, t.TempDir(), ""); err != nil {
					t.Fatal(err)
				}
			case "project":
				project, err := d.store.FindProject(w.ProjectID)
				if err != nil {
					t.Fatal(err)
				}
				project.SingleRepo = !project.SingleRepo
				if err := d.store.UpdateProject(project); err != nil {
					t.Fatal(err)
				}
			case "alias":
				if err := os.Remove(p.WorkspacePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), p.WorkspacePath); err != nil {
					t.Fatal(err)
				}
			case "cancel":
				desktopCancelRPC(t, socket, owner, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1})
			}
			release()
			out, err := <-done, <-failures
			want := rpc.GitFetchConflict
			if mode == "cancel" {
				want = rpc.GitFetchCanceled
			}
			if err != nil || out.Status != want || out.Started || len(out.Logs) != 0 {
				t.Fatalf("queued target %+v %v", out, err)
			}
		})
	}
}

func TestDesktopGitFetchDeadline(t *testing.T) {
	d, _, _, owner, p, _ := fetchFixture(t)
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("FETCH_STARTED", marker)
	gitReadWrapper(t, "case \" $* \" in *\" fetch \"*) /usr/bin/touch \"$FETCH_STARTED\"; exec /bin/sleep 30;; esac")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: owner})
	raw, _ := json.Marshal(p)
	value, err := d.gitFetch(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	out := value.(rpc.GitFetchResult)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("fetch did not reach execution")
	}
	if out.Status != rpc.GitFetchTimedOut || !out.Started || out.Error == nil || len(out.Logs) != 1 {
		t.Fatalf("deadline %+v", out)
	}
}

func TestFetchTargetDiscoveryAndCoordination(t *testing.T) {
	for _, mode := range []string{"parent", "multi", "alias"} {
		t.Run(mode, func(t *testing.T) {
			d, w := newTestDaemon(t)
			root, workspace := w.Path, w.Path
			switch mode {
			case "parent":
				workspace = filepath.Join(w.Path, "subdir")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
			case "multi":
				root = filepath.Join(w.Path, "repo")
			case "alias":
				root = filepath.Join(t.TempDir(), "repo")
				if err := os.Symlink(root, filepath.Join(w.Path, "alias")); err != nil {
					t.Fatal(err)
				}
			}
			readGitRepo(t, root, true)
			if err := d.store.SetWorkspacePath(w.ID, workspace, ""); err != nil {
				t.Fatal(err)
			}
			id, err := gitx.RepositoryIdentityChecked(gitx.WithReadLimit(t.Context(), rpc.GitReadLimit), root)
			if err != nil {
				t.Fatal(err)
			}
			p := rpc.GitFetchParams{WorkspaceID: w.ID, WorkspacePath: workspace, Expected: id}
			release, err := d.lockFetchTarget(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			for name, scope := range map[string]writecoord.Scope{
				"file":            {Paths: []string{resourceKey(filepath.Join(id.Root, "a.txt"))}},
				"common-dir":      {Keys: []string{"git:" + resourceKey(id.CommonDir)}},
				"project-removal": {Keys: []string{"project:" + w.ProjectID}},
			} {
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
				unblock, err := d.writes.Acquire(ctx, scope)
				cancel()
				if unblock != nil {
					unblock()
				}
				if err == nil {
					t.Fatalf("%s did not share coordination", name)
				}
			}
			release()
			owner := desktopTakeTest(t, d, "owner")
			ctx := context.WithValue(t.Context(), desktopCallContext{}, desktopRequestContext{identity: owner})
			raw, _ := json.Marshal(p)
			value, err := d.gitFetch(ctx, raw)
			if err != nil {
				t.Fatal(err)
			}
			if out := value.(rpc.GitFetchResult); out.Status != rpc.GitFetchSucceeded {
				t.Fatalf("%s fetch %+v", mode, out)
			}
		})
	}
}

func TestFetchResultJSONBound(t *testing.T) {
	_, _, _, _, p, _ := fetchFixture(t)
	out := rpc.GitFetchResult{Target: p, Started: true, Logs: []gitx.Run{}}
	for i := 0; i < rpc.GitFetchLogLimit; i++ {
		out.Logs = append(out.Logs, gitx.Run{ID: int64(i + 1), Out: strings.Repeat("\x00", 32<<10), Err: strings.Repeat("\x00", 32<<10), Args: []string{"fetch", "--all"}, Dir: p.Expected.Root, Op: "fetch", OpID: 1, Start: time.Now(), Done: true})
	}
	result := finishFetch(out, nil)
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > rpc.GitFetchResultLimit || !result.LogsTruncated || result.Status != rpc.GitFetchSucceeded {
		t.Fatalf("bounded %s %v", strconv.Itoa(len(raw)), err)
	}
}
