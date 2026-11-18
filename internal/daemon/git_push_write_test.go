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
	"posse/internal/store"
	"posse/internal/writecoord"
)

func pushWriteFixture(t *testing.T) (*Daemon, store.Workspace, string, rpc.DesktopIdentity, rpc.GitPushParams, string) {
	t.Helper()
	d, w, socket, owner, target, _ := fetchFixture(t)
	readGit(t, w.Path, "fetch", "-q")
	readGit(t, w.Path, "reset", "--hard", "-q", "origin/main")
	before := readGit(t, w.Path, "rev-parse", "HEAD")
	readGit(t, w.Path, "commit", "--allow-empty", "-qm", "local change")
	p := pushWritePreviewParams(t, target)
	return d, w, socket, owner, p, before
}
func pushWritePreviewParams(t *testing.T, target rpc.GitFetchParams) rpc.GitPushParams {
	t.Helper()
	preview, err := gitx.ReadPushPreviewChecked(gitx.WithReadLimit(t.Context(), rpc.GitReadLimit), target.Expected.Root, "main", 500)
	if err != nil {
		t.Fatal(err)
	}
	return rpc.GitPushParams{GitFetchParams: target, Branch: preview.Plan.Branch, Remote: preview.Plan.Remote, RemoteBranch: preview.Plan.RemoteBranch, Baseline: preview.Baseline, BranchOID: preview.BranchOID, TrackingRef: preview.TrackingRef, TrackingOID: preview.TrackingOID, TrackingMissing: preview.TrackingMissing}
}
func pushWriteCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitPushParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.push", Params: raw}
}
func pushRemoteOID(t *testing.T, root string) string {
	t.Helper()
	bare := readGit(t, root, "remote", "get-url", "origin")
	return readGit(t, bare, "rev-parse", "refs/heads/main")
}
func TestDesktopGitPushWrite(t *testing.T) {
	_, w, socket, owner, p, _ := pushWriteFixture(t)
	readGit(t, w.Path, "tag", "-a", "release", "-m", "release")
	p = pushWritePreviewParams(t, p.GitFetchParams)
	p.Tags = true
	var out rpc.GitPushResult
	request := pushWriteCall(owner, 1, p)
	if err := rpc.Call(t.Context(), socket, "desktop.call", request, &out); err != nil || out.Status != rpc.GitFetchSucceeded || !out.Started || out.Error != nil || len(out.Logs) != 1 {
		t.Fatalf("push %+v %v", out, err)
	}
	if pushRemoteOID(t, w.Path) != p.BranchOID {
		t.Fatal("branch not pushed")
	}
	bare := readGit(t, w.Path, "remote", "get-url", "origin")
	readGit(t, bare, "rev-parse", "refs/tags/release")
	if !slices.Equal(out.Logs[0].Args, []string{"push", "--set-upstream", "--follow-tags", "origin", "refs/heads/main:refs/heads/main"}) {
		t.Fatal(out.Logs)
	}
	var again rpc.GitPushResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", request, &again); err != nil || !reflect.DeepEqual(again, out) {
		t.Fatalf("duplicate %+v %v", again, err)
	}
	op := desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1})
	var cached rpc.GitPushResult
	if op.State != "done" || json.Unmarshal(op.Result, &cached) != nil || !reflect.DeepEqual(cached, out) {
		t.Fatal("result not retained")
	}
}
func TestDesktopGitPushWriteChanged(t *testing.T) {
	for _, mode := range []string{"branch", "tracking", "missing", "remote", "remote-branch", "baseline", "oid", "tracking-ref", "tracking-oid", "tracking-missing", "workspace", "identity"} {
		t.Run(mode, func(t *testing.T) {
			d, w, socket, owner, p, before := pushWriteFixture(t)
			switch mode {
			case "branch":
				readGit(t, w.Path, "commit", "--allow-empty", "-qm", "changed branch")
			case "tracking":
				readGit(t, w.Path, "update-ref", p.TrackingRef, p.BranchOID)
			case "missing":
				readGit(t, w.Path, "update-ref", "-d", p.TrackingRef)
			case "remote":
				readGit(t, w.Path, "remote", "add", "fork", t.TempDir())
				readGit(t, w.Path, "config", "remote.pushDefault", "fork")
			case "remote-branch":
				readGit(t, w.Path, "config", "branch.main.remote", "origin")
				readGit(t, w.Path, "config", "branch.main.merge", "refs/heads/other")
			case "baseline":
				p.Baseline = strings.Repeat("a", 64)
			case "oid":
				p.BranchOID = before
			case "tracking-ref":
				p.RemoteBranch = "other"
				p.TrackingRef = "refs/remotes/origin/other"
			case "tracking-oid":
				p.TrackingOID = p.BranchOID
			case "tracking-missing":
				p.TrackingMissing = true
				p.TrackingOID = ""
			case "workspace":
				if err := d.store.SetWorkspacePath(w.ID, t.TempDir(), ""); err != nil {
					t.Fatal(err)
				}
			case "identity":
				p.Expected.RootInode++
			}
			var out rpc.GitPushResult
			if err := rpc.Call(t.Context(), socket, "desktop.call", pushWriteCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatalf("changed %+v %v", out, err)
			}
			if pushRemoteOID(t, w.Path) != before {
				t.Fatal("stale preview pushed")
			}
		})
	}
}
func TestDesktopGitPushWriteQueuedBaseline(t *testing.T) {
	d, w, socket, owner, p, before := pushWriteFixture(t)
	release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Keys: []string{"git:" + resourceKey(p.Expected.CommonDir)}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	request := pushWriteCall(owner, 1, p)
	done := make(chan error, 1)
	var out rpc.GitPushResult
	go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", request, &out) }()
	waitFor(t, "push accepted", func() bool { return desktopOperationGet(t, d, request).State == "running" })
	readGit(t, w.Path, "commit", "--allow-empty", "-qm", "changed while queued")
	release()
	if err := <-done; err != nil || out.Status != rpc.GitFetchConflict || out.Started || pushRemoteOID(t, w.Path) != before {
		t.Fatalf("queued %+v %v", out, err)
	}
}
func TestDesktopGitPushWriteForceIncludes(t *testing.T) {
	_, w, socket, owner, p, _ := pushWriteFixture(t)
	bare := readGit(t, w.Path, "remote", "get-url", "origin")
	other := filepath.Join(t.TempDir(), "other")
	readGit(t, w.Path, "clone", "-q", "-b", "main", bare, other)
	readGit(t, other, "commit", "--allow-empty", "-qm", "not integrated")
	readGit(t, other, "push", "-q", "origin", "main")
	remote := pushRemoteOID(t, w.Path)
	readGit(t, w.Path, "fetch", "-q")
	p = pushWritePreviewParams(t, p.GitFetchParams)
	p.Force = true
	var out rpc.GitPushResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", pushWriteCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchFailed || !out.Started || !out.Rejected || len(out.Logs) != 1 {
		t.Fatalf("force %+v %v", out, err)
	}
	args := out.Logs[0].Args
	if !slices.Contains(args, "--force-with-lease") || !slices.Contains(args, "--force-if-includes") || slices.Contains(args, "--force") || pushRemoteOID(t, w.Path) != remote {
		t.Fatal("unsafe force", args)
	}
}
func TestDesktopGitPushWriteGuards(t *testing.T) {
	_, _, socket, owner, p, _ := pushWriteFixture(t)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.push", p, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pushWriteCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	for i, extra := range []string{`,"refspec":"+main:main"}`, `,"url":"https://example.invalid"}`} {
		request := pushWriteCall(owner, uint64(i+1), p)
		request.Params = append(request.Params[:len(request.Params)-1], extra...)
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", request, nil), rpc.CodeBadRequest)
	}
}
func TestDesktopGitPushWriteBounded(t *testing.T) {
	_, w, socket, owner, p, _ := pushWriteFixture(t)
	gitReadWrapper(t, "case \" $* \" in *\" push \"*) i=0; while test $i -lt 10000; do printf 'ordinary progress\\n'; i=$((i+1)); done; printf 'remote: secret server message\\nTo https://person:private-value@example.invalid/repo?key=hidden-value\\n' >&2;; esac")
	var out rpc.GitPushResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", pushWriteCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchSucceeded || !out.LogsTruncated || pushRemoteOID(t, w.Path) != p.BranchOID {
		t.Fatalf("bounded %+v %v", out, err)
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"private-value", "hidden-value", "example.invalid", "secret server message"} {
		if strings.Contains(string(raw), secret) {
			t.Fatal("leaked output")
		}
	}
	if len(raw) > rpc.GitPushResultLimit {
		t.Fatal("unbounded result")
	}
}
func TestDesktopGitPushWriteMore(t *testing.T) {
	_, w, socket, owner, p, _ := pushWriteFixture(t)
	tree := readGit(t, w.Path, "rev-parse", "HEAD^{tree}")
	tip := p.BranchOID
	for range 500 {
		tip = readGit(t, w.Path, "commit-tree", tree, "-p", tip, "-m", "pending")
	}
	readGit(t, w.Path, "update-ref", "refs/heads/main", tip)
	preview, err := gitx.ReadPushPreviewChecked(gitx.WithReadLimit(t.Context(), rpc.GitReadLimit), w.Path, "main", 500)
	if err != nil || !preview.More || len(preview.Plan.Commits) != 500 {
		t.Fatalf("preview %+v %v", preview, err)
	}
	p = pushWritePreviewParams(t, p.GitFetchParams)
	var out rpc.GitPushResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", pushWriteCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchSucceeded || pushRemoteOID(t, w.Path) != tip {
		t.Fatalf("truncated list blocked push %+v %v", out, err)
	}
}
func TestDesktopGitPushWriteDeadline(t *testing.T) {
	d, _, _, owner, p, _ := pushWriteFixture(t)
	marker := filepath.Join(t.TempDir(), "started")
	t.Setenv("PUSH_STARTED", marker)
	gitReadWrapper(t, "case \" $* \" in *\" push \"*) /usr/bin/touch \"$PUSH_STARTED\"; exec /bin/sleep 30;; esac")
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{identity: owner})
	raw, _ := json.Marshal(p)
	value, err := d.gitPush(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	out := value.(rpc.GitPushResult)
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("push did not start")
	}
	if out.Status != rpc.GitFetchTimedOut || !out.Started {
		t.Fatalf("deadline %+v", out)
	}
}
func TestDesktopGitPushWriteDisconnectTakeoverAndCancel(t *testing.T) {
	for _, mode := range []string{"finish", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d, _, socket, owner, p, _ := pushWriteFixture(t)
			started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
			t.Setenv("PUSH_STARTED", started)
			t.Setenv("PUSH_GATE", gate)
			gitReadWrapper(t, "case \" $* \" in *\" push \"*) /usr/bin/touch \"$PUSH_STARTED\"; while test ! -f \"$PUSH_GATE\"; do /bin/sleep 0.01; done;; esac")
			t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			request := pushWriteCall(owner, 1, p)
			done := make(chan error, 1)
			go func() { done <- rpc.Call(ctx, socket, "desktop.call", request, nil) }()
			waitFor(t, "push started", func() bool { _, err := os.Stat(started); return err == nil })
			cancel()
			if err := <-done; err == nil {
				t.Fatal("response not lost")
			}
			current := desktopTakeTest(t, d, "new-owner")
			ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
			if op := desktopStatusRPC(t, socket, ref); op.State != "running" {
				t.Fatalf("disconnect canceled push %+v", op)
			}
			if mode == "cancel" {
				desktopCancelRPC(t, socket, current, ref)
			} else {
				if err := os.WriteFile(gate, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			waitFor(t, "push finished", func() bool { return desktopOperationGet(t, d, request).State == "done" })
			op := desktopStatusRPC(t, socket, ref)
			var result rpc.GitPushResult
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

func TestDesktopGitPushWritePartialFailure(t *testing.T) {
	_, w, socket, owner, p, _ := pushWriteFixture(t)
	bare := readGit(t, w.Path, "remote", "get-url", "origin")
	hook := filepath.Join(bare, "hooks", "update")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ncase \"$1\" in refs/tags/*) echo 'secret server message'; exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	readGit(t, w.Path, "tag", "-a", "rejected-tag", "-m", "tag")
	p = pushWritePreviewParams(t, p.GitFetchParams)
	p.Tags = true
	var out rpc.GitPushResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", pushWriteCall(owner, 1, p), &out); err != nil || out.Status != rpc.GitFetchFailed || !out.Started {
		t.Fatalf("partial %+v %v", out, err)
	}
	if pushRemoteOID(t, w.Path) != p.BranchOID {
		t.Fatal("partial branch update was lost or rolled back")
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "secret server message") {
		t.Fatal("remote message leaked")
	}
}
