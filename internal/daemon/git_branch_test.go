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

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/writecoord"
)

func branchCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitBranchParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.branch", Params: raw}
}
func branchRPC(t *testing.T, socket string, owner rpc.DesktopIdentity, seq uint64, p rpc.GitBranchParams) rpc.GitBranchResult {
	t.Helper()
	var out rpc.GitBranchResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", branchCall(owner, seq, p), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestDesktopGitBranchCommands(t *testing.T) {
	for _, mode := range []string{"local", "remote-local", "track", "noop", "revision", "create", "create-switch", "detached-create", "rename", "rename-current", "delete-remote", "delete-plus"} {
		t.Run(mode, func(t *testing.T) {
			_, w, socket, owner, base, _ := fetchFixture(t)
			oid := readGit(t, w.Path, "rev-parse", "HEAD")
			readGit(t, w.Path, "branch", "topic")
			readGit(t, w.Path, "update-ref", "refs/remotes/origin/topic", oid)
			p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "topic", BranchOID: oid}
			want := []string{"switch", "topic"}
			switch mode {
			case "remote-local":
				p.Remote, p.Branch, p.LocalOID = true, "origin/topic", oid
			case "track":
				readGit(t, w.Path, "branch", "-d", "topic")
				p.Remote, p.Branch = true, "origin/topic"
				want = []string{"switch", "--track", "origin/topic"}
			case "noop":
				p.Branch = "main"
				want = nil
			case "revision":
				readGit(t, w.Path, "tag", "release")
				p = rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchRevision, Revision: "release", RevisionOID: oid}
				want = []string{"switch", "--detach", oid}
			case "create", "create-switch", "detached-create":
				if mode == "detached-create" {
					readGit(t, w.Path, "switch", "--detach", oid)
				}
				p = rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCreate, Branch: "new", FromOID: oid, Checkout: mode == "create-switch"}
				want = []string{"branch", "new", oid}
				if p.Checkout {
					want = []string{"switch", "-c", "new", oid}
				}
			case "rename", "rename-current":
				old := "topic"
				if mode == "rename-current" {
					old = "main"
				}
				p = rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchRename, Branch: old, NewBranch: "renamed", BranchOID: oid}
				want = []string{"branch", "-m", old, "renamed"}
			case "delete-remote", "delete-plus":
				name := "topic"
				if mode == "delete-plus" {
					name = "+main"
					readGit(t, w.Path, "branch", name)
				}
				readGit(t, w.Path, "push", "origin", "refs/heads/"+name+":refs/heads/"+name)
				p.Action, p.Remote, p.Branch = rpc.GitBranchDelete, true, "origin/"+name
				want = []string{"push", "--force-with-lease=refs/heads/" + name + ":" + oid, "origin", "--delete", name}
			}
			out := branchRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchSucceeded || out.OID != oid || out.Unmerged || out.Error != nil {
				t.Fatalf("branch %+v", out)
			}
			if want == nil {
				if out.Started || len(out.Logs) != 0 {
					t.Fatalf("noop %+v", out)
				}
			} else if !out.Started || len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, want) {
				t.Fatalf("command %+v want %q", out, want)
			}
			if p.Action == rpc.GitBranchDelete {
				bare := readGit(t, w.Path, "remote", "get-url", "origin")
				if refs := readGit(t, bare, "for-each-ref", "--format=%(refname)", "refs/heads/"+gitx.LocalOf(p.Branch)); refs != "" {
					t.Fatal("remote branch retained", refs)
				}
			}
			var cached rpc.GitBranchResult
			op := desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1})
			if json.Unmarshal(op.Result, &cached) != nil || !reflect.DeepEqual(cached, out) {
				t.Fatal("operation result lost")
			}
		})
	}
}

func TestDesktopGitBranchChanged(t *testing.T) {
	for _, mode := range []string{"oid", "local-oid", "local-deleted", "local-created", "tracking-oid", "worktree", "current-other-worktree", "delete-current", "delete-worktree", "rename-exists", "rename-worktree", "revision", "from", "exists", "workspace", "identity", "invalid", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			_, w, socket, owner, base, _ := fetchFixture(t)
			oid := readGit(t, w.Path, "rev-parse", "HEAD")
			readGit(t, w.Path, "branch", "topic")
			readGit(t, w.Path, "update-ref", "refs/remotes/origin/topic", oid)
			p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "topic", BranchOID: oid}
			switch mode {
			case "oid":
				p.BranchOID = strings.Repeat("a", 40)
			case "local-oid", "local-deleted", "local-created", "tracking-oid":
				p.Remote, p.Branch, p.LocalOID = true, "origin/topic", oid
				switch mode {
				case "local-oid":
					p.LocalOID = strings.Repeat("a", 40)
				case "local-deleted":
					readGit(t, w.Path, "branch", "-d", "topic")
				case "local-created":
					p.LocalOID = ""
				case "tracking-oid":
					p.BranchOID = strings.Repeat("a", 40)
				}
			case "worktree", "delete-worktree", "rename-worktree":
				readGit(t, w.Path, "worktree", "add", "-q", filepath.Join(t.TempDir(), "wt"), "topic")
				if mode == "delete-worktree" {
					p.Action = rpc.GitBranchDelete
				} else if mode == "rename-worktree" {
					p.Action, p.NewBranch = rpc.GitBranchRename, "renamed"
				}
			case "rename-exists":
				p = rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchRename, Branch: "topic", NewBranch: "main", BranchOID: oid}
			case "current-other-worktree":
				readGit(t, w.Path, "worktree", "add", "--force", "-q", filepath.Join(t.TempDir(), "wt"), "main")
				p.Branch = "main"
			case "delete-current":
				p.Action, p.Branch = rpc.GitBranchDelete, "main"
			case "revision":
				p = rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchRevision, Revision: "main", RevisionOID: strings.Repeat("a", 40)}
			case "from", "exists":
				p = rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCreate, Branch: "topic", FromOID: oid}
				if mode == "from" {
					p.Branch, p.FromOID = "new", strings.Repeat("a", 40)
				}
			case "workspace":
				p.WorkspacePath += "-changed"
			case "identity":
				p.Expected.CommonInode++
			case "invalid":
				p.Branch = "-forced"
			}
			marker := filepath.Join(t.TempDir(), "write")
			t.Setenv("BRANCH_WRITE", marker)
			gitReadWrapper(t, "case \" $* \" in *\" switch \"*|*\" branch \"*|*\" push \"*) /usr/bin/touch \"$BRANCH_WRITE\"; exit 91;; esac")
			request := branchCall(owner, 1, p)
			if mode == "unknown-field" {
				request.Params = append(request.Params[:len(request.Params)-1], []byte(`,"argv":["--force"]}`)...)
			}
			var out rpc.GitBranchResult
			if err := rpc.Call(t.Context(), socket, "desktop.call", request, &out); err != nil || out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatalf("conflict %+v %v", out, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("write command executed")
			}
		})
	}
}

func TestDesktopGitBranchDeleteAndRestore(t *testing.T) {
	_, w, socket, owner, base, _ := fetchFixture(t)
	readGit(t, w.Path, "switch", "-qc", "topic")
	readGit(t, w.Path, "commit", "--allow-empty", "-qm", "unmerged")
	oid := readGit(t, w.Path, "rev-parse", "HEAD")
	readGit(t, w.Path, "switch", "main")
	p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchDelete, Branch: "topic", BranchOID: oid}
	out := branchRPC(t, socket, owner, 1, p)
	if out.Status != rpc.GitFetchSucceeded || !out.Unmerged || out.Started || out.Error != nil || out.OID != "" || readGit(t, w.Path, "rev-parse", "topic") != oid || len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, []string{"branch", "-d", "topic"}) {
		t.Fatalf("unmerged %+v", out)
	}
	p.Force = true
	out = branchRPC(t, socket, owner, 2, p)
	if out.Status != rpc.GitFetchSucceeded || !out.Started || out.Unmerged || out.OID != oid || !slices.Equal(out.Logs[0].Args, []string{"branch", "-D", "topic"}) || readGit(t, w.Path, "for-each-ref", "--format=%(refname)", "refs/heads/topic") != "" {
		t.Fatalf("forced %+v", out)
	}
	restore := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCreate, Branch: "topic", FromOID: out.OID}
	if out = branchRPC(t, socket, owner, 3, restore); out.Status != rpc.GitFetchSucceeded || readGit(t, w.Path, "rev-parse", "topic") != oid || readGit(t, w.Path, "branch", "--show-current") != "main" {
		t.Fatalf("restore %+v", out)
	}
	if out = branchRPC(t, socket, owner, 4, restore); out.Status != rpc.GitFetchConflict || out.Started {
		t.Fatalf("repeated restore %+v", out)
	}
}

func TestDesktopGitBranchRemoteLease(t *testing.T) {
	_, w, socket, owner, base, latest := fetchFixture(t)
	before := readGit(t, w.Path, "rev-parse", "refs/remotes/origin/main")
	p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchDelete, Remote: true, Branch: "origin/main", BranchOID: before}
	out := branchRPC(t, socket, owner, 1, p)
	bare := readGit(t, w.Path, "remote", "get-url", "origin")
	if out.Status != rpc.GitFetchFailed || !out.Started || readGit(t, bare, "rev-parse", "refs/heads/main") != latest || readGit(t, w.Path, "rev-parse", "refs/remotes/origin/main") != before {
		t.Fatalf("lease %+v", out)
	}
	// 跟踪引用变化时连网络命令也不运行。
	p.BranchOID = latest
	out = branchRPC(t, socket, owner, 2, p)
	if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
		t.Fatalf("tracking %+v", out)
	}
}

func TestDesktopGitBranchSaveCoordination(t *testing.T) {
	d, w, socket, owner, base, _ := fetchFixture(t)
	readGit(t, w.Path, "branch", "topic")
	oid := readGit(t, w.Path, "rev-parse", "HEAD")
	p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "topic", BranchOID: oid}
	path := filepath.Join(base.Expected.Root, "a.txt")
	release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Paths: []string{path}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	request := branchCall(owner, 1, p)
	done := make(chan error, 1)
	var out rpc.GitBranchResult
	go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", request, &out) }()
	waitFor(t, "branch queued", func() bool { return desktopOperationGet(t, d, request).State == "running" })
	select {
	case err := <-done:
		t.Fatal("save lock did not block checkout", err)
	case <-time.After(100 * time.Millisecond):
	}
	if readGit(t, w.Path, "branch", "--show-current") != "main" {
		t.Fatal("checkout ran before save release")
	}
	release()
	if err := <-done; err != nil || out.Status != rpc.GitFetchSucceeded {
		t.Fatalf("branch %+v %v", out, err)
	}
	// checkout 执行后仍持有同一锁，保存不能越过它写入。
	started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
	t.Setenv("BRANCH_STARTED", started)
	t.Setenv("BRANCH_GATE", gate)
	gitReadWrapper(t, "case \" $* \" in *\" switch \"*) /usr/bin/git \"$@\" || exit $?; /usr/bin/touch \"$BRANCH_STARTED\"; while test ! -f \"$BRANCH_GATE\"; do /bin/sleep 0.01; done; exit 0;; esac")
	t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
	p.Branch = "main"
	go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", branchCall(owner, 2, p), &out) }()
	waitFor(t, "checkout holding lock", func() bool { _, err := os.Stat(started); return err == nil })
	version, err := files.ReadVersion(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	saved := make(chan error, 1)
	save := saveCall(owner, 3, saveParams(path, version, []byte("saved after checkout\n")))
	go func() { saved <- rpc.Call(t.Context(), socket, "desktop.call", save, nil) }()
	waitFor(t, "save queued", func() bool { return desktopOperationGet(t, d, save).State == "running" })
	select {
	case err := <-saved:
		t.Fatal("checkout lock did not block save", err)
	case <-time.After(100 * time.Millisecond):
	}
	if data, _ := os.ReadFile(path); string(data) == "saved after checkout\n" {
		t.Fatal("save crossed checkout")
	}
	os.WriteFile(gate, nil, 0600)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(path); string(data) != "saved after checkout\n" {
		t.Fatal("save did not finish")
	}
}

func TestDesktopGitBranchDisconnectAndCancel(t *testing.T) {
	d, w, socket, owner, base, _ := fetchFixture(t)
	readGit(t, w.Path, "branch", "topic")
	p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "topic", BranchOID: readGit(t, w.Path, "rev-parse", "HEAD")}
	started := filepath.Join(t.TempDir(), "started")
	t.Setenv("BRANCH_STARTED", started)
	gitReadWrapper(t, "case \" $* \" in *\" switch \"*) /usr/bin/git \"$@\" || exit $?; /usr/bin/touch \"$BRANCH_STARTED\"; exec /bin/sleep 30;; esac")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request := branchCall(owner, 1, p)
	done := make(chan error, 1)
	go func() { done <- rpc.Call(ctx, socket, "desktop.call", request, nil) }()
	waitFor(t, "checkout happened", func() bool { _, err := os.Stat(started); return err == nil })
	cancel()
	if err := <-done; err == nil {
		t.Fatal("reply not lost")
	}
	current := desktopTakeTest(t, d, "new-owner")
	ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
	if out := desktopStatusRPC(t, socket, ref); out.State != "running" {
		t.Fatal("disconnect canceled operation")
	}
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: owner, Operation: ref}, nil), rpc.CodeReadOnly)
	desktopCancelRPC(t, socket, current, ref)
	waitFor(t, "canceled", func() bool { return desktopOperationGet(t, d, request).State == "done" })
	op := desktopStatusRPC(t, socket, ref)
	var out rpc.GitBranchResult
	if json.Unmarshal(op.Result, &out) != nil || out.Status != rpc.GitFetchCanceled || !out.Started || readGit(t, w.Path, "branch", "--show-current") != "topic" {
		t.Fatalf("cancel rolled back %+v", out)
	}
}

func TestDesktopGitBranchQueuedChangesAndGuards(t *testing.T) {
	for _, mode := range []string{"oid", "identity", "local-missing", "local-appeared"} {
		t.Run(mode, func(t *testing.T) {
			d, w, socket, owner, base, _ := fetchFixture(t)
			readGit(t, w.Path, "branch", "topic")
			oid := readGit(t, w.Path, "rev-parse", "HEAD")
			readGit(t, w.Path, "update-ref", "refs/remotes/origin/topic", oid)
			p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "topic", BranchOID: oid}
			if strings.HasPrefix(mode, "local-") {
				p.Remote, p.Branch, p.LocalOID = true, "origin/topic", oid
				if mode == "local-appeared" {
					p.LocalOID = ""
					readGit(t, w.Path, "branch", "-d", "topic")
				}
			}
			release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Paths: []string{base.Expected.Root}})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			request := branchCall(owner, 1, p)
			done := make(chan error, 1)
			var out rpc.GitBranchResult
			go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", request, &out) }()
			waitFor(t, "branch accepted", func() bool { return desktopOperationGet(t, d, request).State == "running" })
			switch mode {
			case "oid":
				readGit(t, w.Path, "commit", "--allow-empty", "-qm", "moved")
				readGit(t, w.Path, "branch", "-f", "topic", "HEAD")
			case "identity":
				if err := d.store.SetWorkspacePath(w.ID, filepath.Join(t.TempDir(), "moved"), ""); err != nil {
					t.Fatal(err)
				}
			case "local-missing":
				readGit(t, w.Path, "branch", "-d", "topic")
			case "local-appeared":
				readGit(t, w.Path, "branch", "topic")
			}
			release()
			if err := <-done; err != nil || out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatalf("queued %+v %v", out, err)
			}
		})
	}
	d, w, socket, owner, base, _ := fetchFixture(t)
	p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "main", BranchOID: readGit(t, w.Path, "rev-parse", "HEAD")}
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.branch", p, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", branchCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	tooLarge := branchCall(owner, 1, p)
	tooLarge.Params = json.RawMessage(`{"padding":"` + strings.Repeat("x", rpc.GitBranchParamsLimit) + `"}`)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", tooLarge, nil), rpc.CodeBadRequest)
	if desktopOperationGet(t, d, tooLarge).State != "unknown" {
		t.Fatal("oversized accepted")
	}
}
func TestDesktopGitBranchBounded(t *testing.T) {
	_, w, socket, owner, base, _ := fetchFixture(t)
	readGit(t, w.Path, "branch", "topic")
	p := rpc.GitBranchParams{GitFetchParams: base, Action: rpc.GitBranchCheckout, Branch: "topic", BranchOID: readGit(t, w.Path, "rev-parse", "HEAD")}
	gitReadWrapper(t, "case \" $* \" in *\" switch \"*) i=0; while test $i -lt 10000; do printf 'branch diagnostic\\n'; i=$((i+1)); done; printf 'remote: secret server message\\nFrom https://person:private-value@example.invalid/repo?key=hidden-value\\n' >&2;; esac")
	out := branchRPC(t, socket, owner, 1, p)
	raw, _ := json.Marshal(out)
	if out.Status != rpc.GitFetchSucceeded || !out.LogsTruncated || len(raw) > rpc.GitBranchResultLimit {
		t.Fatalf("bounded %+v", out)
	}
	for _, secret := range []string{"private-value", "hidden-value", "example.invalid", "secret server message"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("secret leaked")
		}
	}
}
