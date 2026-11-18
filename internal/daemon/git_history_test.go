package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"slices"
	"strings"
	"testing"
	"time"
)

func historyParams(t *testing.T, base rpc.GitFetchParams, action, oid string) rpc.GitHistoryParams {
	t.Helper()
	st, err := gitx.ReadStatusChecked(t.Context(), base.Expected.Root)
	if err != nil {
		t.Fatal(err)
	}
	p := rpc.GitHistoryParams{GitFetchParams: base, Action: action, OID: oid, Head: st.Commit, Current: st.Head}
	if action == rpc.GitHistoryReset {
		p.Mode = "soft"
	}
	if action == rpc.GitHistoryReword || action == rpc.GitHistorySquash {
		p.Message = "new subject\n\nfull message\n#123"
		p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
	}
	return p
}
func historyCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitHistoryParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.history", Params: raw}
}
func historyRPC(t *testing.T, socket string, owner rpc.DesktopIdentity, seq uint64, p rpc.GitHistoryParams) rpc.GitHistoryResult {
	t.Helper()
	var out rpc.GitHistoryResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", historyCall(owner, seq, p), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func requireHistoryConflict(t *testing.T, out rpc.GitHistoryResult) {
	t.Helper()
	if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
		t.Fatalf("stale history executed: %+v", out)
	}
}
func TestDesktopGitHistoryPreconditions(t *testing.T) {
	for _, action := range []string{"reset", "reword", "squash", "drop"} {
		for _, change := range []string{"head", "oid", "current", "identity", "registration", "merge", "rebase", "cherry-pick", "revert", "detached"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				d, socket, owner, base := stashFixture(t)
				root := base.Expected.Root
				oid := integrateCommit(t, root, "two", "second")
				p := historyParams(t, base, action, oid)
				switch change {
				case "head":
					integrateCommit(t, root, "three", "third")
				case "oid":
					p.OID = strings.Repeat("f", 40)
				case "current":
					readGit(t, root, "switch", "-c", "other")
				case "detached":
					readGit(t, root, "checkout", "--detach")
					p.Current = ""
					integrateCommit(t, root, "three", "third")
				case "identity":
					p.Expected.RootInode++
				case "registration":
					d.store.SetWorkspacePath(base.WorkspaceID, t.TempDir(), "")
				default:
					marker := map[string]string{"merge": "MERGE_HEAD", "rebase": "rebase-merge", "cherry-pick": "CHERRY_PICK_HEAD", "revert": "REVERT_HEAD"}[change]
					if change == "rebase" {
						os.Mkdir(filepath.Join(base.Expected.GitDir, marker), 0700)
					} else {
						os.WriteFile(filepath.Join(base.Expected.GitDir, marker), []byte(oid), 0600)
					}
				}
				before := readGit(t, root, "rev-parse", "HEAD")
				requireHistoryConflict(t, historyRPC(t, socket, owner, 1, p))
				if readGit(t, root, "rev-parse", "HEAD") != before {
					t.Fatal("precondition changed HEAD")
				}
			})
		}
	}
}
func TestDesktopGitHistoryResetModes(t *testing.T) {
	for _, mode := range []string{"soft", "mixed", "hard"} {
		t.Run(mode, func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			old := readGit(t, root, "rev-parse", "HEAD")
			integrateCommit(t, root, "a.txt", "committed")
			pathsWrite(t, root, "a.txt", "staged")
			readGit(t, root, "add", "a.txt")
			pathsWrite(t, root, "a.txt", "unstaged")
			p := historyParams(t, base, "reset", old)
			p.Mode = mode
			out := historyRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchSucceeded || !out.Started || len(out.Logs) != 1 || !slices.Equal(out.Logs[0].Args, []string{"reset", "--" + mode, old}) {
				t.Fatal(out)
			}
			if readGit(t, root, "rev-parse", "HEAD") != old {
				t.Fatal("wrong target")
			}
			data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			index := readGit(t, root, "show", ":a.txt")
			switch mode {
			case "soft":
				if index != "staged" || string(data) != "unstaged" {
					t.Fatal("soft lost index or worktree", index, string(data))
				}
			case "mixed":
				if index == "staged" || string(data) != "unstaged" {
					t.Fatal("mixed lost worktree", index, string(data))
				}
			case "hard":
				if string(data) == "unstaged" || index == "staged" {
					t.Fatal("hard preserved changes")
				}
			}
		})
	}
}
func TestDesktopGitHistoryReword(t *testing.T) {
	for _, head := range []bool{true, false} {
		for _, hookFails := range []bool{false, true} {
			t.Run(map[bool]string{true: "head", false: "old"}[head]+map[bool]string{true: "-hook", false: "-ok"}[hookFails], func(t *testing.T) {
				_, socket, owner, base := stashFixture(t)
				root := base.Expected.Root
				oid := integrateCommit(t, root, "second", "second")
				if !head {
					integrateCommit(t, root, "third", "third")
				}
				p := historyParams(t, base, "reword", oid)
				if hookFails {
					os.WriteFile(filepath.Join(base.Expected.GitDir, "hooks", "commit-msg"), []byte("#!/bin/sh\necho hook-refused >&2\nexit 1\n"), 0700)
				}
				out := historyRPC(t, socket, owner, 1, p)
				if !out.Started || len(out.Logs) != 1 {
					t.Fatal(out)
				}
				if head && !slices.Equal(out.Logs[0].Args, []string{"commit", "--amend", "--only", "-F", "-"}) {
					t.Fatal(out)
				}
				if !head && !slices.Contains(out.Logs[0].Args, "--autostash") {
					t.Fatal("not rebase", out)
				}
				if hookFails {
					if out.Status != rpc.GitFetchFailed {
						t.Fatal("hook ignored", out)
					}
					if head && readGit(t, root, "rev-parse", "HEAD") != p.Head {
						t.Fatal("amend hook changed commit")
					}
					if !head {
						st, _ := gitx.ReadStatus(t.Context(), root)
						if st.Operation != "rebase" {
							t.Fatal("rebase was aborted")
						}
					}
				} else {
					if out.Status != rpc.GitFetchSucceeded {
						t.Fatal(out)
					}
					target := "HEAD"
					if !head {
						target = "HEAD^"
					}
					if readGit(t, root, "show", "-s", "--format=%B", target) != p.Message {
						t.Fatal("message lost", readGit(t, root, "show", "-s", "--format=%B", target))
					}
					encoded, _ := json.Marshal(out)
					if strings.Contains(string(encoded), "full message") || strings.Contains(string(encoded), "new subject") {
						t.Fatal("message leaked", string(encoded))
					}
				}
			})
		}
	}
}
func TestDesktopGitHistoryDropSquashAndMerges(t *testing.T) {
	for _, action := range []string{"reword", "squash", "drop"} {
		t.Run(action, func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			rootOID := readGit(t, root, "rev-parse", "HEAD")
			if action == "squash" {
				out := historyRPC(t, socket, owner, 1, historyParams(t, base, action, rootOID))
				requireHistoryConflict(t, out)
				if !strings.Contains(out.Error.Message, "this is the first commit; there is no earlier commit to squash into") {
					t.Fatal(out)
				}
			}
			oid := integrateCommit(t, root, "second", "second")
			integrateCommit(t, root, "third", "third")
			out := historyRPC(t, socket, owner, 2, historyParams(t, base, action, oid))
			if out.Status != rpc.GitFetchSucceeded {
				t.Fatal(out)
			}
			if action == "drop" {
				if readGit(t, root, "rev-list", "--count", "HEAD") != "2" {
					t.Fatal("commit not dropped")
				}
				if _, err := os.Stat(filepath.Join(root, "second")); !os.IsNotExist(err) {
					t.Fatal("dropped change remains")
				}
			}
			if action == "squash" && readGit(t, root, "rev-list", "--count", "HEAD") != "2" {
				t.Fatal("not squashed")
			}
			current := readGit(t, root, "rev-parse", "HEAD")
			readGit(t, root, "switch", "-c", "side")
			integrateCommit(t, root, "side", "side")
			readGit(t, root, "switch", "main")
			readGit(t, root, "merge", "--no-ff", "--no-edit", "side")
			requireHistoryConflict(t, historyRPC(t, socket, owner, 3, historyParams(t, base, action, current)))
			readGit(t, root, "switch", "-c", "unrelated", rootOID)
			outside := integrateCommit(t, root, "outside", "outside")
			readGit(t, root, "switch", "main")
			requireHistoryConflict(t, historyRPC(t, socket, owner, 4, historyParams(t, base, action, outside)))
		})
	}
}
func TestDesktopGitHistoryRemoteConfirmation(t *testing.T) {
	for _, action := range []string{"reset", "reword", "squash", "drop"} {
		t.Run(action, func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			oid := integrateCommit(t, root, "second", "second")
			readGit(t, root, "update-ref", "refs/remotes/origin/main", oid)
			readGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
			p := historyParams(t, base, action, oid)
			p.Remotes = []string{"origin/main"}
			requireHistoryConflict(t, historyRPC(t, socket, owner, 1, p))
			p.Confirmed = true
			readGit(t, root, "update-ref", "refs/remotes/origin/other", oid)
			requireHistoryConflict(t, historyRPC(t, socket, owner, 2, p))
			p.Remotes = append(p.Remotes, "origin/other")
			out := historyRPC(t, socket, owner, 3, p)
			if out.Status != rpc.GitFetchSucceeded {
				t.Fatal(out)
			}
		})
	}
}
func TestDesktopGitHistoryCancelRebase(t *testing.T) {
	d, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	oid := integrateCommit(t, root, "second", "second")
	integrateCommit(t, root, "third", "third")
	ready := filepath.Join(t.TempDir(), "ready")
	hook := "#!/bin/sh\ntouch '" + ready + "'\nsleep 60\n"
	os.WriteFile(filepath.Join(base.Expected.GitDir, "hooks", "commit-msg"), []byte(hook), 0700)
	p := historyParams(t, base, "reword", oid)
	done := make(chan rpc.GitHistoryResult, 1)
	go func() {
		var out rpc.GitHistoryResult
		err := rpc.Call(context.Background(), socket, "desktop.call", historyCall(owner, 1, p), &out)
		if err != nil {
			t.Error(err)
		}
		done <- out
	}()
	for end := time.Now().Add(10 * time.Second); ; {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("hook not entered")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancelOwner := desktopTakeTest(t, d, "new-history-owner")
	var receipt rpc.DesktopOperation
	if err := rpc.Call(t.Context(), socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: cancelOwner, Operation: rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}}, &receipt); err != nil {
		t.Fatal(err)
	}
	out := <-done
	if out.Status != rpc.GitFetchCanceled || !out.Started {
		t.Fatal(out)
	}
	st, _ := gitx.ReadStatus(t.Context(), root)
	if st.Operation != "rebase" {
		t.Fatal("cancel aborted rebase")
	}
	for _, run := range out.Logs {
		if slices.Contains(run.Args, "--abort") || slices.Contains(run.Args, "reset") {
			t.Fatal("cancel rolled back", out)
		}
	}
}

func TestDesktopGitHistoryAutostash(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	oid := integrateCommit(t, root, "a.txt", "second\n")
	integrateCommit(t, root, "third", "third")
	pathsWrite(t, root, "a.txt", "local\n")
	out := historyRPC(t, socket, owner, 1, historyParams(t, base, "drop", oid))
	if out.Status != rpc.GitFetchSucceeded || out.Autostash == nil || !out.Autostash.Conflicts || out.Autostash.Stash != readGit(t, root, "rev-parse", "stash@{0}") {
		t.Fatal("autostash treated as failure", out)
	}
}
func TestDesktopGitHistoryAmendKeepsIndex(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	p := historyParams(t, base, "reword", readGit(t, root, "rev-parse", "HEAD"))
	pathsWrite(t, root, "staged", "staged")
	readGit(t, root, "add", "staged")
	out := historyRPC(t, socket, owner, 1, p)
	if out.Status != rpc.GitFetchSucceeded || readGit(t, root, "diff", "--cached", "--name-only") != "staged" || readGit(t, root, "ls-tree", "--name-only", "HEAD", "staged") != "" {
		t.Fatal("amend consumed staged content", out)
	}
}
func TestDesktopGitHistoryReadAndValidation(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	oid := integrateCommit(t, root, "second", "subject\n\nbody")
	readGit(t, root, "update-ref", "refs/remotes/origin/main", oid)
	readGit(t, root, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	viewer := owner
	viewer.ClientID = "viewer"
	var remotes rpc.GitRemotesContainingResult
	if err := mediaTestCall(t.Context(), socket, viewer, "git.remotesContaining", rpc.GitRemotesContainingParams{Path: root, Expected: base.Expected, OID: oid}, &remotes); err != nil || !slices.Equal(remotes.Remotes, []string{"origin/main"}) {
		t.Fatal(remotes, err)
	}
	integrateCommit(t, root, "third", "third")
	var detail rpc.GitCommitFilesResult
	if err := mediaTestCall(t.Context(), socket, viewer, "git.commitFiles", rpc.GitCommitFilesParams{Path: root, Expected: base.Expected, Hash: oid, FullMessage: true}, &detail); err != nil || detail.FullMessage != "subject\n\nbody" {
		t.Fatal("non-HEAD body", detail, err)
	}
	p := historyParams(t, base, "reword", oid)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.history", p, nil), rpc.CodeReadOnly)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", historyCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	for i, value := range []string{"", " \n", "nul\x00message", strings.Repeat("x", rpc.GitCommitMessageLimit+1)} {
		q := p
		q.Message = value
		q.MessageHash = rpc.GitCommitHash([]byte(value))
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", historyCall(owner, uint64(i+1), q), nil), rpc.CodeBadRequest)
	}
	call := historyCall(owner, 10, p)
	call.Params = append(call.Params[:len(call.Params)-1], []byte(`,"force":true}`)...)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", call, nil), rpc.CodeBadRequest)
}
func TestDesktopGitHistoryLockRecheck(t *testing.T) {
	d, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	p := historyParams(t, base, "reset", readGit(t, root, "rev-parse", "HEAD"))
	release, err := d.lockFetchTarget(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan rpc.GitHistoryResult, 1)
	fail := make(chan error, 1)
	go func() {
		var out rpc.GitHistoryResult
		fail <- rpc.Call(t.Context(), socket, "desktop.call", historyCall(owner, 1, p), &out)
		done <- out
	}()
	waitFor(t, "history accepted", func() bool {
		return desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}).State == "running"
	})
	integrateCommit(t, root, "later", "later")
	release()
	if err := <-fail; err != nil {
		t.Fatal(err)
	}
	requireHistoryConflict(t, <-done)
}
func TestDesktopGitHistoryResolvedOIDChanged(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	oid := readGit(t, root, "rev-parse", "HEAD")
	later := integrateCommit(t, root, "later", "later")
	p := historyParams(t, base, "reset", oid)
	t.Setenv("HISTORY_OLD", oid)
	t.Setenv("HISTORY_NEW", later)
	gitReadWrapper(t, `case " $* " in *" rev-parse --verify --end-of-options $HISTORY_OLD^{commit} "*) printf '%s\n' "$HISTORY_NEW"; exit 0;; esac`)
	requireHistoryConflict(t, historyRPC(t, socket, owner, 1, p))
}
