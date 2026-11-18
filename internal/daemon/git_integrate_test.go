package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/store"
	"slices"
	"strings"
	"testing"
	"time"
)

func integrateParams(t *testing.T, base rpc.GitFetchParams, action, branch string, oids ...string) rpc.GitIntegrateParams {
	t.Helper()
	st, err := gitx.ReadStatusChecked(t.Context(), base.Expected.Root)
	if err != nil {
		t.Fatal(err)
	}
	p := rpc.GitIntegrateParams{GitFetchParams: base, Action: action, Head: st.Commit, Current: st.Head, OIDs: oids}
	if branch != "" {
		p.Branch, p.BranchOID = branch, readGit(t, base.Expected.Root, "rev-parse", "refs/heads/"+branch)
	}
	if action == rpc.GitIntegrateContinue || action == rpc.GitIntegrateSkip || action == rpc.GitIntegrateAbort {
		p.Operation = st.Operation
	}
	if action == rpc.GitIntegrateContinue {
		p.IndexID = st.IndexID
	}
	return p
}
func integrateCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitIntegrateParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.integrate", Params: raw}
}
func integrateRPC(t *testing.T, socket string, owner rpc.DesktopIdentity, seq uint64, p rpc.GitIntegrateParams) rpc.GitIntegrateResult {
	t.Helper()
	var out rpc.GitIntegrateResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", integrateCall(owner, seq, p), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func integrateCommit(t *testing.T, root, path, value string) string {
	t.Helper()
	pathsWrite(t, root, path, value)
	readGit(t, root, "add", "--", path)
	readGit(t, root, "commit", "-qm", value)
	return readGit(t, root, "rev-parse", "HEAD")
}
func integrateConflict(t *testing.T, root string) (string, string) {
	t.Helper()
	readGit(t, root, "switch", "-c", "topic")
	topic := integrateCommit(t, root, "a.txt", "topic\n")
	readGit(t, root, "switch", "main")
	head := integrateCommit(t, root, "a.txt", "main\n")
	return head, topic
}
func requireIntegrateConflict(t *testing.T, out rpc.GitIntegrateResult) {
	t.Helper()
	if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
		t.Fatalf("stale request ran: %+v", out)
	}
}
func requireIntegrateCommand(t *testing.T, out rpc.GitIntegrateResult, args ...string) {
	t.Helper()
	if !out.Started || len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, args) {
		t.Fatalf("command mismatch: %+v", out)
	}
}

func TestDesktopGitIntegratePreconditions(t *testing.T) {
	for _, action := range []string{rpc.GitIntegrateMerge, rpc.GitIntegrateRebase, rpc.GitIntegrateCheckoutRebase} {
		for _, change := range []string{"target", "head", "current", "identity", "registration", "remote"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				d, socket, owner, base := stashFixture(t)
				root := base.Expected.Root
				readGit(t, root, "branch", "topic")
				p := integrateParams(t, base, action, "topic")
				switch change {
				case "target":
					readGit(t, root, "switch", "topic")
					integrateCommit(t, root, "topic", "new")
					readGit(t, root, "switch", "main")
				case "head":
					integrateCommit(t, root, "new", "new")
				case "current":
					readGit(t, root, "switch", "-c", "other")
				case "identity":
					p.Expected.RootInode++
				case "registration":
					d.store.SetWorkspacePath(base.WorkspaceID, t.TempDir(), "")
				case "remote":
					if action == rpc.GitIntegrateCheckoutRebase {
						return
					}
					p.Remote, p.Branch = true, "origin/topic"
					readGit(t, root, "update-ref", "refs/remotes/origin/topic", p.Head)
					newer := integrateCommit(t, root, "later", "later")
					readGit(t, root, "update-ref", "refs/remotes/origin/topic", newer)
					readGit(t, root, "reset", "--hard", p.Head)
				}
				before := readGit(t, root, "rev-parse", "HEAD")
				requireIntegrateConflict(t, integrateRPC(t, socket, owner, 1, p))
				if readGit(t, root, "rev-parse", "HEAD") != before {
					t.Fatal("changed HEAD")
				}
			})
		}
	}
}

func TestDesktopGitIntegrateMergeContinue(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	head, topic := integrateConflict(t, root)
	out := integrateRPC(t, socket, owner, 1, integrateParams(t, base, rpc.GitIntegrateMerge, "topic"))
	requireIntegrateCommand(t, out, "merge", "--no-edit", "--", "topic")
	if out.Status != rpc.GitFetchFailed {
		t.Fatal(out)
	}
	st, _ := gitx.ReadStatusChecked(t.Context(), root)
	if st.Operation != "merge" || !slices.ContainsFunc(st.Entries, func(e gitx.Entry) bool { return e.Conflict }) {
		t.Fatal("merge aborted", st)
	}
	p := integrateParams(t, base, rpc.GitIntegrateContinue, "")
	requireIntegrateConflict(t, integrateRPC(t, socket, owner, 2, p))
	pathsWrite(t, root, "a.txt", "resolved\n")
	readGit(t, root, "add", "a.txt")
	requireIntegrateConflict(t, integrateRPC(t, socket, owner, 3, p))
	p = integrateParams(t, base, rpc.GitIntegrateContinue, "")
	message, err := os.ReadFile(filepath.Join(base.Expected.GitDir, "MERGE_MSG"))
	if err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(base.Expected.GitDir, "hooks", "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\necho hook-refused >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out = integrateRPC(t, socket, owner, 4, p)
	requireIntegrateCommand(t, out, "commit", "-F", "-")
	st, _ = gitx.ReadStatusChecked(t.Context(), root)
	if out.Status != rpc.GitFetchFailed || st.Operation != "merge" || st.Commit != head {
		t.Fatal("hook failure aborted merge", out, st)
	}
	os.Remove(hook)
	out = integrateRPC(t, socket, owner, 5, p)
	if out.Status != rpc.GitFetchSucceeded {
		t.Fatal(out)
	}
	requireIntegrateCommand(t, out, "commit", "-F", "-")
	if readGit(t, root, "show", "-s", "--format=%P", "HEAD") != head+" "+topic || readGit(t, root, "show", "-s", "--format=%B", "HEAD") != strings.TrimSpace(string(message)) {
		t.Fatal("lost merge parents or MERGE_MSG")
	}
}

func TestDesktopGitIntegrateMergeMessageValidation(t *testing.T) {
	for _, value := range []string{"missing", "", " \n", "x\x00y", string([]byte{0xff}), strings.Repeat("x", rpc.GitCommitMessageLimit+1)} {
		t.Run(string(rune(len(value)+65)), func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			integrateConflict(t, root)
			integrateRPC(t, socket, owner, 1, integrateParams(t, base, rpc.GitIntegrateMerge, "topic"))
			pathsWrite(t, root, "a.txt", "resolved")
			readGit(t, root, "add", "a.txt")
			file := filepath.Join(base.Expected.GitDir, "MERGE_MSG")
			if value == "missing" {
				os.Remove(file)
			} else {
				os.WriteFile(file, []byte(value), 0600)
			}
			requireIntegrateConflict(t, integrateRPC(t, socket, owner, 2, integrateParams(t, base, rpc.GitIntegrateContinue, "")))
		})
	}
}

func TestDesktopGitIntegrateCheckoutRebase(t *testing.T) {
	for _, mode := range []string{"normal", "occupied", "detached", "same"} {
		t.Run(mode, func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			readGit(t, root, "switch", "-c", "topic")
			integrateCommit(t, root, "topic", "topic")
			readGit(t, root, "switch", "main")
			head := integrateCommit(t, root, "main", "main")
			if mode == "detached" {
				readGit(t, root, "switch", "--detach", head)
			}
			p := integrateParams(t, base, rpc.GitIntegrateCheckoutRebase, "topic")
			if mode == "occupied" {
				readGit(t, root, "worktree", "add", filepath.Join(t.TempDir(), "other"), "topic")
			}
			if mode == "same" {
				p.Action = rpc.GitIntegrateRebase
				p.Branch, p.BranchOID = "main", head
			}
			out := integrateRPC(t, socket, owner, 1, p)
			if mode == "occupied" || mode == "same" {
				requireIntegrateConflict(t, out)
				if readGit(t, root, "branch", "--show-current") != "main" || readGit(t, root, "rev-parse", "HEAD") != head {
					t.Fatal("switched occupied branch")
				}
				return
			}
			current := "main"
			if mode == "detached" {
				current = "HEAD"
			}
			requireIntegrateCommand(t, out, "rebase", current, "topic")
			if out.Status != rpc.GitFetchSucceeded || readGit(t, root, "branch", "--show-current") != "topic" || readGit(t, root, "rev-parse", "HEAD^") != head {
				t.Fatal(out)
			}
		})
	}
}

func TestDesktopGitIntegrateCherryPickSequence(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordered", true: "continue"}[conflict], func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			readGit(t, root, "switch", "-c", "topic")
			first := integrateCommit(t, root, "a.txt", "first\n")
			path := "a.txt"
			if conflict {
				path = "second"
			}
			second := integrateCommit(t, root, path, "second\n")
			readGit(t, root, "switch", "main")
			if conflict {
				integrateCommit(t, root, "a.txt", "main\n")
			}
			p := integrateParams(t, base, rpc.GitIntegrateCherryPick, "", first, second)
			out := integrateRPC(t, socket, owner, 1, p)
			requireIntegrateCommand(t, out, "cherry-pick", first, second)
			if conflict {
				st, _ := gitx.ReadStatusChecked(t.Context(), root)
				if out.Status != rpc.GitFetchFailed || st.Operation != "cherry-pick" || readGit(t, root, "rev-parse", "HEAD") != p.Head {
					t.Fatal("pick did not stop", out, st)
				}
				if _, err := os.Stat(filepath.Join(root, "second")); !os.IsNotExist(err) {
					t.Fatal("second commit applied early")
				}
				pathsWrite(t, root, "a.txt", "resolved\n")
				readGit(t, root, "add", "a.txt")
				out = integrateRPC(t, socket, owner, 2, integrateParams(t, base, rpc.GitIntegrateContinue, ""))
				requireIntegrateCommand(t, out, "cherry-pick", "--continue")
			}
			if out.Status != rpc.GitFetchSucceeded {
				t.Fatal(out)
			}
			got, _ := os.ReadFile(filepath.Join(root, path))
			if string(got) != "second\n" || readGit(t, root, "rev-list", "--count", p.Head+"..HEAD") != "2" {
				t.Fatal("sequence lost", string(got))
			}
		})
	}
}

func TestDesktopGitIntegrateRevertSkipAbort(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	head, _ := integrateConflict(t, root)
	integrateRPC(t, socket, owner, 1, integrateParams(t, base, rpc.GitIntegrateMerge, "topic"))
	requireIntegrateConflict(t, integrateRPC(t, socket, owner, 2, integrateParams(t, base, rpc.GitIntegrateSkip, "")))
	p := integrateParams(t, base, rpc.GitIntegrateAbort, "")
	out := integrateRPC(t, socket, owner, 3, p)
	requireIntegrateCommand(t, out, "merge", "--abort")
	if out.Status != rpc.GitFetchSucceeded || readGit(t, root, "rev-parse", "HEAD") != head {
		t.Fatal(out)
	}
	requireIntegrateConflict(t, integrateRPC(t, socket, owner, 4, p))
	changed := integrateCommit(t, root, "a.txt", "changed\n")
	integrateCommit(t, root, "a.txt", "later\n")
	out = integrateRPC(t, socket, owner, 5, integrateParams(t, base, rpc.GitIntegrateRevert, "", changed))
	requireIntegrateCommand(t, out, "revert", "--no-edit", changed)
	st, _ := gitx.ReadStatusChecked(t.Context(), root)
	if out.Status != rpc.GitFetchFailed || st.Operation != "revert" {
		t.Fatal("revert aborted", out, st)
	}
}

func TestDesktopGitIntegrateValidation(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	readGit(t, root, "branch", "topic")
	p := integrateParams(t, base, rpc.GitIntegrateMerge, "topic")
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.integrate", p, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", integrateCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	for i, mode := range []string{"flag", "range", "action", "oid", "unknown", "many", "blob"} {
		q := p
		switch mode {
		case "flag":
			q.Branch = "--abort"
		case "range":
			q.Branch = "main..topic"
		case "action":
			q.Action = "reset"
		case "oid":
			q.Head = "HEAD"
		case "many":
			q = integrateParams(t, base, rpc.GitIntegrateCherryPick, "")
			q.OIDs = make([]string, 501)
			for j := range q.OIDs {
				q.OIDs[j] = q.Head
			}
		case "blob":
			q = integrateParams(t, base, rpc.GitIntegrateCherryPick, "", readGit(t, root, "rev-parse", "HEAD:a.txt"))
		}
		call := integrateCall(owner, uint64(i+1), q)
		if mode == "unknown" {
			call.Params = append(call.Params[:len(call.Params)-1], []byte(`,"force":true}`)...)
		}
		if mode == "blob" {
			requireIntegrateConflict(t, integrateRPC(t, socket, owner, uint64(i+1), q))
			continue
		}
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", call, nil), rpc.CodeBadRequest)
	}
}

func TestDesktopGitIntegrateLockRecheck(t *testing.T) {
	d, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	readGit(t, root, "branch", "topic")
	p := integrateParams(t, base, rpc.GitIntegrateMerge, "topic")
	release, err := d.lockFetchTarget(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan rpc.GitIntegrateResult, 1)
	fail := make(chan error, 1)
	go func() {
		var out rpc.GitIntegrateResult
		fail <- rpc.Call(t.Context(), socket, "desktop.call", integrateCall(owner, 1, p), &out)
		done <- out
	}()
	waitFor(t, "integrate accepted", func() bool {
		return desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}).State == "running"
	})
	select {
	case out := <-done:
		t.Fatal("lock bypass", out)
	case <-time.After(100 * time.Millisecond):
	}
	integrateCommit(t, root, "later", "later")
	release()
	if err := <-fail; err != nil {
		t.Fatal(err)
	}
	requireIntegrateConflict(t, <-done)
}

func TestDesktopGitIntegrateCancelDoesNotAbort(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "cancel"}[stop], func(t *testing.T) {
			d, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			readGit(t, root, "switch", "-c", "topic")
			integrateCommit(t, root, "topic", "topic")
			readGit(t, root, "switch", "main")
			head := integrateCommit(t, root, "main", "main")
			started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
			t.Setenv("INTEGRATE_STARTED", started)
			t.Setenv("INTEGRATE_GATE", gate)
			hook := filepath.Join(base.Expected.GitDir, "hooks", "pre-merge-commit")
			os.WriteFile(hook, []byte("#!/bin/sh\ntouch \"$INTEGRATE_STARTED\"\nwhile test ! -f \"$INTEGRATE_GATE\"; do sleep 0.01; done\n"), 0700)
			aborts := filepath.Join(t.TempDir(), "aborts")
			t.Setenv("INTEGRATE_ABORTS", aborts)
			gitReadWrapper(t, `case "$1:$2" in merge:--abort) touch "$INTEGRATE_ABORTS";; esac`)
			t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := integrateParams(t, base, rpc.GitIntegrateMerge, "topic")
			done := make(chan error, 1)
			go func() { done <- rpc.Call(ctx, socket, "desktop.call", integrateCall(owner, 1, p), nil) }()
			waitFor(t, "merge hook", func() bool { _, err := os.Stat(started); return err == nil })
			cancel()
			if err := <-done; err == nil {
				t.Fatal("reply not lost")
			}
			current := desktopTakeTest(t, d, "new-owner")
			ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
			if stop {
				desktopCancelRPC(t, socket, current, ref)
			} else {
				os.WriteFile(gate, nil, 0600)
			}
			var op rpc.DesktopOperation
			waitFor(t, "merge done", func() bool { op = desktopStatusRPC(t, socket, ref); return op.State == "done" })
			var out rpc.GitIntegrateResult
			if op.Error != nil || json.Unmarshal(op.Result, &out) != nil || !out.Started {
				t.Fatal(op)
			}
			if _, err := os.Stat(aborts); !os.IsNotExist(err) {
				t.Fatal("cancel invoked abort")
			}
			if stop {
				if out.Status != rpc.GitFetchCanceled || readGit(t, root, "rev-parse", "HEAD") != head {
					t.Fatal(out)
				}
			} else if out.Status != rpc.GitFetchSucceeded {
				t.Fatal(out)
			}
		})
	}
}

func TestDesktopGitIntegrateProgress(t *testing.T) {
	for _, operation := range []string{"rebase", "cherry-pick", "revert"} {
		for _, action := range []string{"continue", "skip", "abort"} {
			if operation == "revert" && action == "skip" {
				continue
			}
			t.Run(operation+"/"+action, func(t *testing.T) {
				_, socket, owner, base := stashFixture(t)
				root := base.Expected.Root
				head, topic := integrateConflict(t, root)
				p := integrateParams(t, base, operation, "")
				switch operation {
				case "rebase":
					p = integrateParams(t, base, operation, "topic")
				case "cherry-pick":
					p.OIDs = []string{topic}
				case "revert":
					p.OIDs = []string{head}
					integrateCommit(t, root, "a.txt", "later")
					p = integrateParams(t, base, operation, "", head)
					head = p.Head
				}
				out := integrateRPC(t, socket, owner, 1, p)
				if out.Status != rpc.GitFetchFailed {
					t.Fatal("expected conflict", out)
				}
				if action == "continue" {
					pathsWrite(t, root, "a.txt", "resolved")
					readGit(t, root, "add", "a.txt")
				}
				p = integrateParams(t, base, action, "")
				if action == "skip" {
					pathsWrite(t, root, "unrelated", "index changed")
					readGit(t, root, "add", "unrelated")
				}
				out = integrateRPC(t, socket, owner, 2, p)
				requireIntegrateCommand(t, out, operation, "--"+action)
				if out.Status != rpc.GitFetchSucceeded {
					t.Fatal(out)
				}
				if action == "abort" && readGit(t, root, "rev-parse", "HEAD") != head {
					t.Fatal("abort did not restore original head")
				}
			})
		}
	}
}
func TestDesktopGitIntegrateRejectsShadowedBranch(t *testing.T) {
	for _, current := range []bool{false, true} {
		t.Run(map[bool]string{true: "current", false: "target"}[current], func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			original := readGit(t, root, "rev-parse", "HEAD")
			readGit(t, root, "switch", "-c", "topic")
			integrateCommit(t, root, "topic", "topic")
			readGit(t, root, "switch", "main")
			integrateCommit(t, root, "main", "main")
			action, name := rpc.GitIntegrateMerge, "topic"
			if current {
				action, name = rpc.GitIntegrateCheckoutRebase, "main"
			}
			readGit(t, root, "tag", name, original)
			requireIntegrateConflict(t, integrateRPC(t, socket, owner, 1, integrateParams(t, base, action, "topic")))
		})
	}
}

func TestDesktopGitIntegrateCommonLock(t *testing.T) {
	d, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	readGit(t, root, "branch", "topic")
	otherRoot := filepath.Join(t.TempDir(), "other")
	readGit(t, root, "worktree", "add", "-b", "other", otherRoot)
	w, err := d.store.Workspace(base.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	other := store.Workspace{ID: "integrate-other", ProjectID: w.ProjectID, Name: "other", Path: otherRoot, Kind: "folder"}
	if err := d.store.AddWorkspace(other); err != nil {
		t.Fatal(err)
	}
	id, err := gitx.RepositoryIdentityChecked(t.Context(), otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	otherBase := rpc.GitFetchParams{WorkspaceID: other.ID, WorkspacePath: other.Path, Expected: id}
	p := integrateParams(t, otherBase, rpc.GitIntegrateMerge, "topic")
	release, err := d.lockFetchTarget(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan rpc.GitIntegrateResult, 1)
	fail := make(chan error, 1)
	go func() {
		var out rpc.GitIntegrateResult
		fail <- rpc.Call(t.Context(), socket, "desktop.call", integrateCall(owner, 1, p), &out)
		done <- out
	}()
	waitFor(t, "integrate accepted", func() bool {
		return desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}).State == "running"
	})
	select {
	case out := <-done:
		t.Fatal("common-dir bypassed", out)
	case <-time.After(100 * time.Millisecond):
	}
	newer := integrateCommit(t, root, "new", "new")
	readGit(t, root, "update-ref", "refs/heads/topic", newer)
	release()
	if err := <-fail; err != nil {
		t.Fatal(err)
	}
	requireIntegrateConflict(t, <-done)
}

func TestDesktopGitIntegrateRemoteRefs(t *testing.T) {
	for _, action := range []string{"merge", "rebase"} {
		t.Run(action, func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			readGit(t, root, "switch", "-c", "topic")
			topic := integrateCommit(t, root, "topic", "topic")
			readGit(t, root, "switch", "main")
			readGit(t, root, "update-ref", "refs/remotes/origin/topic", topic)
			p := integrateParams(t, base, action, "")
			p.Branch, p.BranchOID, p.Remote = "origin/topic", topic, true
			out := integrateRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchSucceeded || readGit(t, root, "rev-parse", "HEAD") != topic {
				t.Fatal(out)
			}
			want := []string{"rebase", "origin/topic"}
			if action == "merge" {
				want = []string{"merge", "--no-edit", "--", "origin/topic"}
			}
			requireIntegrateCommand(t, out, want...)
		})
	}
}
