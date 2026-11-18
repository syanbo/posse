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
)

func stashFixture(t *testing.T) (*Daemon, string, rpc.DesktopIdentity, rpc.GitFetchParams) {
	t.Helper()
	d, w := newTestDaemon(t)
	root := readGitRepo(t, t.TempDir(), true)
	if err := d.store.SetWorkspacePath(w.ID, root, ""); err != nil {
		t.Fatal(err)
	}
	project, err := d.store.FindProject(w.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	project.Path = root
	if err := d.store.UpdateProject(project); err != nil {
		t.Fatal(err)
	}
	id, err := gitx.RepositoryIdentityChecked(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	return d, desktopServer(t, d), desktopTakeTest(t, d, "stash-owner"), rpc.GitFetchParams{WorkspaceID: w.ID, WorkspacePath: root, Expected: id}
}

func stashParams(t *testing.T, base rpc.GitFetchParams, action, oid string) rpc.GitStashParams {
	t.Helper()
	p := rpc.GitStashParams{GitFetchParams: base, Action: action, OID: oid}
	if action != rpc.GitStashDrop {
		st, err := gitx.ReadStatus(t.Context(), base.Expected.Root)
		if err != nil {
			t.Fatal(err)
		}
		p.Head, p.IndexID = st.Commit, st.IndexID
		if action == rpc.GitStashPush {
			p.Entries, p.MessageHash = st.Entries, rpc.GitCommitHash(nil)
		}
	}
	return p
}
func stashCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitStashParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.stash", Params: raw}
}
func stashRPC(t *testing.T, socket string, owner rpc.DesktopIdentity, seq uint64, p rpc.GitStashParams) rpc.GitStashResult {
	t.Helper()
	var out rpc.GitStashResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", stashCall(owner, seq, p), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func stashSeed(t *testing.T, root, path, value string) string {
	t.Helper()
	pathsWrite(t, root, path, value)
	readGit(t, root, "add", "--", path)
	readGit(t, root, "stash", "push", "--include-untracked", "-m", value)
	return readGit(t, root, "rev-parse", "stash@{0}")
}

func TestDesktopGitStashOIDMoves(t *testing.T) {
	for _, action := range []string{rpc.GitStashApply, rpc.GitStashPop, rpc.GitStashDrop} {
		for _, mode := range []string{"moved", "missing", "duplicate", "rev-changed"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				_, socket, owner, base := stashFixture(t)
				root := base.Expected.Root
				oid := stashSeed(t, root, "a.txt", "original\n")
				p := stashParams(t, base, action, oid)
				newOID := stashSeed(t, root, "new", "new stash\n")
				switch mode {
				case "missing":
					readGit(t, root, "stash", "drop", "stash@{1}")
				case "duplicate":
					readGit(t, root, "stash", "store", "-m", "duplicate", oid)
				case "rev-changed":
					gitReadWrapper(t, `case " $* " in *' rev-parse --verify --end-of-options stash@{'*) printf '%s\n' 1111111111111111111111111111111111111111; exit 0;; esac`)
				}
				out := stashRPC(t, socket, owner, 1, p)
				if mode != "moved" {
					if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
						t.Fatal("stale stash executed", out)
					}
					list, err := gitx.Stashes(t.Context(), root)
					if err != nil || !slices.ContainsFunc(list, func(s gitx.Stash) bool { return s.Hash == newOID }) {
						t.Fatal("new stash removed", list, err)
					}
					return
				}
				if out.Status != rpc.GitFetchSucceeded || !out.Started || len(out.Logs) != 1 {
					t.Fatal(out)
				}
				want := []string{"stash", action}
				if action != rpc.GitStashDrop {
					want = append(want, "--index")
				}
				want = append(want, "stash@{1}")
				if !slices.Equal(out.Logs[0].Args, want) {
					t.Fatal(out.Logs)
				}
				if readGit(t, root, "rev-parse", "stash@{0}") != newOID {
					t.Fatal("new stash changed")
				}
				list, err := gitx.Stashes(t.Context(), root)
				if err != nil || slices.ContainsFunc(list, func(s gitx.Stash) bool { return s.Hash == oid }) != (action == rpc.GitStashApply) {
					t.Fatal(list, err)
				}
				if action != rpc.GitStashDrop && readGit(t, root, "show", ":a.txt") != "original" {
					t.Fatal("original index not restored")
				}
			})
		}
	}
}

func TestDesktopGitStashPush(t *testing.T) {
	for i, message := range []string{"", "--all\n--keep-index", "password=private-value https://person:secret@example.invalid/repo"} {
		t.Run([]string{"empty", "flags", "credentials"}[i], func(t *testing.T) {
			_, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			stashSeed(t, root, "old", "old")
			pathsWrite(t, root, "a.txt", "staged\n")
			readGit(t, root, "add", "a.txt")
			pathsWrite(t, root, "a.txt", "working\n")
			pathsWrite(t, root, "new", "untracked")
			p := stashParams(t, base, rpc.GitStashPush, "")
			p.Message, p.MessageHash = message, rpc.GitCommitHash([]byte(message))
			out := stashRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchSucceeded || !out.Started || out.OID != readGit(t, root, "rev-parse", "stash@{0}") || out.Target.Message != "" || len(out.Logs) != 1 {
				t.Fatal(out)
			}
			want := []string{"stash", "push", "--include-untracked"}
			if message != "" {
				want = append(want, "-m", "[message omitted]")
			}
			if !slices.Equal(out.Logs[0].Args, want) || readGit(t, root, "status", "--porcelain") != "" {
				t.Fatal(out)
			}
			if readGit(t, root, "show", out.OID+":a.txt") != "working" || readGit(t, root, "show", out.OID+"^2:a.txt") != "staged" || readGit(t, root, "show", out.OID+"^3:new") != "untracked" {
				t.Fatal("stash contents differ")
			}
			cached := desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1})
			if strings.Contains(string(cached.Result), "private-value") || strings.Contains(string(cached.Result), "person:secret") {
				t.Fatal("message credentials retained")
			}
			var again rpc.GitStashResult
			if err := rpc.Call(t.Context(), socket, "desktop.call", stashCall(owner, 1, p), &again); err != nil || !reflect.DeepEqual(out, again) {
				t.Fatal("operation not stable", again, err)
			}
		})
	}
}

func TestDesktopGitStashPreconditions(t *testing.T) {
	for _, mode := range []string{"head", "index", "entry", "order", "xy", "orig", "untracked", "conflict", "submodule", "workspace", "identity"} {
		t.Run(mode, func(t *testing.T) {
			d, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			pathsWrite(t, root, "a.txt", "clicked\n")
			pathsWrite(t, root, "new", "clicked\n")
			p := stashParams(t, base, rpc.GitStashPush, "")
			switch mode {
			case "head":
				readGit(t, root, "commit", "--allow-empty", "-qm", "new")
			case "index":
				readGit(t, root, "add", "a.txt")
			case "entry":
				pathsWrite(t, root, "later", "later")
			case "order":
				slices.Reverse(p.Entries)
			case "xy":
				p.Entries[0].X = 'M'
			case "orig":
				p.Entries[0].Orig = "old"
			case "untracked":
				p.Entries[0].Untracked = !p.Entries[0].Untracked
			case "conflict":
				p.Entries[0].Conflict = true
			case "submodule":
				p.Entries[0].Submodule = true
			case "workspace":
				if err := d.store.SetWorkspacePath(base.WorkspaceID, t.TempDir(), ""); err != nil {
					t.Fatal(err)
				}
			case "identity":
				p.Expected.RootInode++
			}
			out := stashRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 || readGit(t, root, "stash", "list") != "" {
				t.Fatal(out)
			}
		})
	}
	for _, action := range []string{rpc.GitStashApply, rpc.GitStashPop, rpc.GitStashDrop} {
		for _, mode := range []string{"head", "index"} {
			t.Run(action+"/"+mode, func(t *testing.T) {
				_, socket, owner, base := stashFixture(t)
				oid := stashSeed(t, base.Expected.Root, "a.txt", "saved")
				p := stashParams(t, base, action, oid)
				if mode == "head" {
					readGit(t, base.Expected.Root, "commit", "--allow-empty", "-qm", "new")
				} else {
					pathsWrite(t, base.Expected.Root, "other", "new")
					readGit(t, base.Expected.Root, "add", "other")
				}
				out := stashRPC(t, socket, owner, 1, p)
				if action == rpc.GitStashDrop {
					if out.Status != rpc.GitFetchSucceeded || !out.Started {
						t.Fatal(out)
					}
				} else if out.Status != rpc.GitFetchConflict || out.Started {
					t.Fatal(out)
				}
			})
		}
	}
}

func TestDesktopGitStashFailure(t *testing.T) {
	t.Run("pop-conflict", func(t *testing.T) {
		_, socket, owner, base := stashFixture(t)
		root := base.Expected.Root
		pathsWrite(t, root, "a.txt", "stash content\n")
		readGit(t, root, "stash", "push", "--include-untracked")
		oid := readGit(t, root, "rev-parse", "stash@{0}")
		pathsWrite(t, root, "a.txt", "new committed content\n")
		readGit(t, root, "commit", "-am", "new")
		out := stashRPC(t, socket, owner, 1, stashParams(t, base, rpc.GitStashPop, oid))
		if out.Status != rpc.GitFetchFailed || !out.Started || len(out.Logs) != 1 || readGit(t, root, "rev-parse", "stash@{0}") != oid {
			t.Fatal("pop conflict deleted stash", out)
		}
		if readGit(t, root, "status", "--porcelain") == "" {
			t.Fatal("failure reset worktree")
		}
	})
	t.Run("empty-failure", func(t *testing.T) {
		_, socket, owner, base := stashFixture(t)
		p := stashParams(t, base, rpc.GitStashPush, "")
		gitReadWrapper(t, `case "$1:$2" in stash:push) echo failed >&2; exit 1;; esac`)
		out := stashRPC(t, socket, owner, 1, p)
		if out.Status != rpc.GitFetchFailed || !out.Started {
			t.Fatal("empty failure became success", out)
		}
	})
}

func TestDesktopGitStashValidation(t *testing.T) {
	_, socket, owner, base := stashFixture(t)
	pathsWrite(t, base.Expected.Root, "new", "new")
	original := stashParams(t, base, rpc.GitStashPush, "")
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.stash", original, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", stashCall(viewer, 1, original), nil), rpc.CodeReadOnly)
	for i, mode := range []string{"long", "nul", "hash", "action", "ref", "unknown"} {
		p := original
		switch mode {
		case "long":
			p.Message = strings.Repeat("x", rpc.GitStashMessageLimit+1)
			p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
		case "nul":
			p.Message = "a\x00b"
			p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
		case "hash":
			p.IndexID = "wrong"
		case "action":
			p.Action = "clear"
		case "ref":
			p = stashParams(t, base, rpc.GitStashDrop, "stash@{0}")
		}
		call := stashCall(owner, uint64(i+1), p)
		if mode == "unknown" {
			call.Params = append(call.Params[:len(call.Params)-1], []byte(`,"ref":"stash@{0}"}`)...)
		}
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", call, nil), rpc.CodeBadRequest)
	}
}

func TestDesktopGitStashSharedLock(t *testing.T) {
	d, socket, owner, base := stashFixture(t)
	root := base.Expected.Root
	otherRoot := filepath.Join(t.TempDir(), "other")
	readGit(t, root, "worktree", "add", "-b", "other", otherRoot)
	w, err := d.store.Workspace(base.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	other := store.Workspace{ID: "stash-other", ProjectID: w.ProjectID, Name: "other", Path: otherRoot, Kind: "folder"}
	if err := d.store.AddWorkspace(other); err != nil {
		t.Fatal(err)
	}
	id, err := gitx.RepositoryIdentityChecked(t.Context(), otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	otherBase := rpc.GitFetchParams{WorkspaceID: other.ID, WorkspacePath: other.Path, Expected: id}
	oid := stashSeed(t, root, "a.txt", "original")
	p := stashParams(t, otherBase, rpc.GitStashDrop, oid)
	release, err := d.lockFetchTarget(t.Context(), base)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	done := make(chan rpc.GitStashResult, 1)
	failures := make(chan error, 1)
	go func() {
		var out rpc.GitStashResult
		err := rpc.Call(t.Context(), socket, "desktop.call", stashCall(owner, 1, p), &out)
		failures <- err
		done <- out
	}()
	waitFor(t, "stash accepted", func() bool {
		return desktopStatusRPC(t, socket, rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}).State == "running"
	})
	select {
	case out := <-done:
		t.Fatal("common-dir lock bypassed", out)
	case <-time.After(100 * time.Millisecond):
	}
	newOID := stashSeed(t, root, "new", "new")
	release()
	if err := <-failures; err != nil {
		t.Fatal(err)
	}
	if out := <-done; out.Status != rpc.GitFetchSucceeded || !out.Started || out.Logs[0].Args[2] != "stash@{1}" {
		t.Fatal(out)
	}
	if readGit(t, root, "rev-parse", "stash@{0}") != newOID {
		t.Fatal("wrong stash dropped")
	}
}

func TestDesktopGitStashDisconnectCancel(t *testing.T) {
	for _, cancelOperation := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect", true: "cancel"}[cancelOperation], func(t *testing.T) {
			d, socket, owner, base := stashFixture(t)
			root := base.Expected.Root
			oid := stashSeed(t, root, "a.txt", "original")
			p := stashParams(t, base, rpc.GitStashPop, oid)
			started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
			t.Setenv("STASH_STARTED", started)
			t.Setenv("STASH_GATE", gate)
			gitReadWrapper(t, `case "$1:$2" in stash:pop) touch "$STASH_STARTED"; while test ! -f "$STASH_GATE"; do sleep 0.01; done;; esac`)
			t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- rpc.Call(ctx, socket, "desktop.call", stashCall(owner, 1, p), nil) }()
			waitFor(t, "stash started", func() bool { _, err := os.Stat(started); return err == nil })
			cancel()
			if err := <-done; err == nil {
				t.Fatal("response not lost")
			}
			current := desktopTakeTest(t, d, "new-owner")
			ref := rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}
			if cancelOperation {
				desktopCancelRPC(t, socket, current, ref)
			} else {
				os.WriteFile(gate, nil, 0600)
			}
			var op rpc.DesktopOperation
			waitFor(t, "stash completed", func() bool { op = desktopStatusRPC(t, socket, ref); return op.State == "done" })
			var out rpc.GitStashResult
			if op.Error != nil || json.Unmarshal(op.Result, &out) != nil || !out.Started {
				t.Fatal(op)
			}
			if cancelOperation {
				if out.Status != rpc.GitFetchCanceled || readGit(t, root, "rev-parse", "stash@{0}") != oid {
					t.Fatal("cancel removed stash", out)
				}
			} else if out.Status != rpc.GitFetchSucceeded || readGit(t, root, "stash", "list") != "" {
				t.Fatal("disconnect canceled stash", out)
			}
		})
	}
}
