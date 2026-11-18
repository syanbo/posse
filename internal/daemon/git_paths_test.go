package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func pathsFixture(t *testing.T, commit bool) (*Daemon, string, rpc.DesktopIdentity, rpc.GitFetchParams) {
	t.Helper()
	d, w := newTestDaemon(t)
	readGitRepo(t, w.Path, commit)
	id, err := gitx.RepositoryIdentityChecked(t.Context(), w.Path)
	if err != nil {
		t.Fatal(err)
	}
	return d, desktopServer(t, d), desktopTakeTest(t, d, "paths-owner"), rpc.GitFetchParams{WorkspaceID: w.ID, WorkspacePath: w.Path, Expected: id}
}
func pathsWrite(t *testing.T, root, path, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func pathsParams(t *testing.T, base rpc.GitFetchParams, action string, names ...string) rpc.GitPathsParams {
	t.Helper()
	st, err := gitx.ReadStatus(t.Context(), base.Expected.Root)
	if err != nil {
		t.Fatal(err)
	}
	p := rpc.GitPathsParams{GitFetchParams: base, Action: action, Head: st.Commit}
	for _, name := range names {
		found := false
		for _, e := range st.Entries {
			if e.Path == name {
				found = true
				item := rpc.GitPath{Entry: e}
				if action == rpc.GitPathsRollback || action == rpc.GitPathsTake || action == rpc.GitPathsWrite {
					v, err := files.ReadVersion(t.Context(), filepath.Join(base.Expected.Root, e.Path))
					if err != nil {
						t.Fatal(err)
					}
					item.Version = &v
					if e.Orig != "" {
						v, err := files.ReadVersion(t.Context(), filepath.Join(base.Expected.Root, e.Orig))
						if err != nil {
							t.Fatal(err)
						}
						item.OrigVersion = &v
					}
				}
				if action == rpc.GitPathsRollback {
					if e.Untracked || e.X == 'A' {
						p.Remove = append(p.Remove, item)
					} else {
						p.Restore = append(p.Restore, item)
					}
				} else {
					p.Paths = append(p.Paths, item)
				}
			}
		}
		if !found {
			t.Fatalf("missing status entry %s: %+v", name, st)
		}
	}
	return p
}
func pathsCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitPathsParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.paths", Params: raw}
}
func pathsRPC(t *testing.T, socket string, owner rpc.DesktopIdentity, seq uint64, p rpc.GitPathsParams) rpc.GitPathsResult {
	t.Helper()
	var out rpc.GitPathsResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", pathsCall(owner, seq, p), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestDesktopGitPathsCommands(t *testing.T) {
	for _, mode := range []string{"stage", "unstage", "unborn", "restore", "rename", "remove", "ours", "theirs", "resolve", "write"} {
		t.Run(mode, func(t *testing.T) {
			_, socket, owner, base := pathsFixture(t, mode != "unborn")
			root := base.Expected.Root
			pathsWrite(t, root, "a.txt", "changed\n")
			var p rpc.GitPathsParams
			var want [][]string
			switch mode {
			case "stage":
				pathsWrite(t, root, "[new]*.txt", "new")
				p = pathsParams(t, base, rpc.GitPathsStage, "[new]*.txt")
				want = [][]string{{"add", "-A", "--", ":(literal)[new]*.txt"}}
			case "unstage", "unborn":
				readGit(t, root, "add", "a.txt")
				p = pathsParams(t, base, rpc.GitPathsUnstage, "a.txt")
				want = [][]string{{"restore", "--staged", "--", ":(literal)a.txt"}}
				if mode == "unborn" {
					want = [][]string{{"rm", "--cached", "-r", "-q", "--", ":(literal)a.txt"}}
				}
			case "restore", "rename":
				path := "a.txt"
				if mode == "rename" {
					readGit(t, root, "restore", "--worktree", "a.txt")
					readGit(t, root, "mv", "a.txt", "b.txt")
					path = "b.txt"
				}
				p = pathsParams(t, base, rpc.GitPathsRollback, path)
				want = [][]string{{"restore", "--source=HEAD", "--staged", "--worktree", "--", ":(literal)" + path}}
				if mode == "rename" {
					want[0] = append(want[0], ":(literal)a.txt")
				}
			case "remove":
				pathsWrite(t, root, "[new]*.txt", "new")
				p = pathsParams(t, base, rpc.GitPathsRollback, "[new]*.txt")
				want = [][]string{{"rm", "--cached", "-q", "--ignore-unmatch", "--", ":(literal)[new]*.txt"}}
			case "ours", "theirs", "resolve":
				readGit(t, root, "commit", "-qam", "ours")
				readGit(t, root, "switch", "-c", "other", "HEAD~1")
				pathsWrite(t, root, "a.txt", "theirs\n")
				readGit(t, root, "commit", "-qam", "theirs")
				readGit(t, root, "switch", "main")
				_, _ = gitx.PathsBounded(t.Context(), root, []string{"merge", "other"})
				action := rpc.GitPathsTake
				if mode == "resolve" {
					action = rpc.GitPathsResolve
				}
				p = pathsParams(t, base, action, "a.txt")
				p.Ours = mode == "ours"
				side := "--theirs"
				if p.Ours {
					side = "--ours"
				}
				want = [][]string{{"checkout", side, "--", ":(literal)a.txt"}, {"add", "--", ":(literal)a.txt"}}
				if mode == "resolve" {
					want = [][]string{{"add", "-A", "--", ":(literal)a.txt"}}
				}
			case "write":
				p = pathsParams(t, base, rpc.GitPathsWrite, "a.txt")
				p.Data = []byte("partial\n")
				sum := sha256.Sum256(p.Data)
				p.Hash = hex.EncodeToString(sum[:])
			}
			out := pathsRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchSucceeded || !out.Started || out.Error != nil {
				t.Fatalf("result %+v", out)
			}
			var got [][]string
			for _, log := range out.Logs {
				got = append(got, log.Args)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("commands %q want %q", got, want)
			}
			if mode == "remove" {
				if _, err := os.Stat(filepath.Join(root, "[new]*.txt")); !os.IsNotExist(err) {
					t.Fatal("new file retained")
				}
			}
			if mode == "write" {
				data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
				if string(data) != "partial\n" || out.Version == nil || out.Version.Hash != p.Hash || len(out.Target.Data) != 0 {
					t.Fatal("write result", out)
				}
			}
		})
	}
}
func TestDesktopGitPathsChanged(t *testing.T) {
	for _, mode := range []string{"version", "missing", "head", "unborn-head", "category", "remove-version", "workspace", "identity"} {
		t.Run(mode, func(t *testing.T) {
			d, socket, owner, base := pathsFixture(t, mode != "unborn-head")
			root := base.Expected.Root
			pathsWrite(t, root, "a.txt", "clicked\n")
			p := pathsParams(t, base, rpc.GitPathsRollback, "a.txt")
			switch mode {
			case "version":
				pathsWrite(t, root, "a.txt", "newer\n")
			case "missing":
				os.Remove(filepath.Join(root, "a.txt"))
			case "head":
				readGit(t, root, "commit", "--allow-empty", "-qm", "new head")
			case "unborn-head":
				readGit(t, root, "commit", "-qm", "first")
			case "category":
				pathsWrite(t, root, "new", "new")
				p = pathsParams(t, base, rpc.GitPathsStage, "new")
				readGit(t, root, "add", "new")
			case "remove-version":
				pathsWrite(t, root, "new", "new")
				p = pathsParams(t, base, rpc.GitPathsRollback, "new")
				pathsWrite(t, root, "new", "newer")
			case "workspace":
				if err := d.store.SetWorkspacePath(base.WorkspaceID, t.TempDir(), ""); err != nil {
					t.Fatal(err)
				}
			case "identity":
				p.Expected.RootInode++
			}
			before, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			out := pathsRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatalf("changed %+v", out)
			}
			after, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if string(before) != string(after) {
				t.Fatal("changed file overwritten")
			}
			if mode == "remove-version" {
				data, _ := os.ReadFile(filepath.Join(root, "new"))
				if string(data) != "newer" {
					t.Fatal("changed new file removed")
				}
			}
		})
	}
}
func TestDesktopGitPathsValidation(t *testing.T) {
	_, socket, owner, base := pathsFixture(t, true)
	pathsWrite(t, base.Expected.Root, "new", "new")
	original := pathsParams(t, base, rpc.GitPathsStage, "new")
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.paths", original, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pathsCall(viewer, 1, original), nil), rpc.CodeReadOnly)
	for i, path := range []string{"", "/tmp/a", "../a", "a/../b", "a\\b", "a/./b"} {
		p := original
		p.Paths = append([]rpc.GitPath{}, original.Paths...)
		p.Paths[0].Entry.Path = path
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pathsCall(owner, uint64(i+1), p), nil), rpc.CodeBadRequest)
	}
	call := pathsCall(owner, 20, original)
	call.Params = append(call.Params[:len(call.Params)-1], []byte(`,"argv":["--force"]}`)...)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", call, nil), rpc.CodeBadRequest)
}
func TestDesktopGitPathsSaveLocks(t *testing.T) {
	for _, saveFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "save-first", false: "rollback-first"}[saveFirst], func(t *testing.T) {
			d, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			path := filepath.Join(root, "a.txt")
			pathsWrite(t, root, "a.txt", "clicked\n")
			p := pathsParams(t, base, rpc.GitPathsRollback, "a.txt")
			version := *p.Restore[0].Version
			done := make(chan rpc.GitPathsResult, 1)
			errs := make(chan error, 1)
			start := func() {
				go func() {
					var out rpc.GitPathsResult
					err := rpc.Call(t.Context(), socket, "desktop.call", pathsCall(owner, 1, p), &out)
					errs <- err
					done <- out
				}()
			}
			if saveFirst {
				release, err := d.lockWritePaths(t.Context(), []string{path})
				if err != nil {
					t.Fatal(err)
				}
				start()
				time.Sleep(100 * time.Millisecond)
				select {
				case out := <-done:
					t.Fatal("rollback bypassed save lock", out)
				default:
				}
				pathsWrite(t, root, "a.txt", "saved\n")
				release()
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
				out := <-done
				if out.Status != rpc.GitFetchConflict || out.Started {
					t.Fatal("stale rollback", out)
				}
			} else {
				started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
				t.Setenv("PATHS_STARTED", started)
				t.Setenv("PATHS_GATE", gate)
				gitReadWrapper(t, `case " $* " in *" restore --source=HEAD "*) touch "$PATHS_STARTED"; while test ! -f "$PATHS_GATE"; do sleep 0.01; done;; esac`)
				t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
				start()
				waitFor(t, "restore holds locks", func() bool { _, err := os.Stat(started); return err == nil })
				saved := make(chan error, 1)
				data := []byte("old editor")
				sum := sha256.Sum256(data)
				go func() {
					raw, _ := json.Marshal(rpc.FileSaveParams{Path: path, Expected: version, Data: data, Hash: hex.EncodeToString(sum[:])})
					_, err := d.filesSave(context.WithValue(t.Context(), desktopCallContext{}, desktopRequestContext{identity: owner}), raw)
					saved <- err
				}()
				select {
				case err := <-saved:
					t.Fatal("save bypassed rollback lock", err)
				case <-time.After(100 * time.Millisecond):
				}
				os.WriteFile(gate, nil, 0600)
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
				if out := <-done; out.Status != rpc.GitFetchSucceeded {
					t.Fatal(out)
				}
				desktopErrorCode(t, <-saved, rpc.CodeConflict)
				data, _ = os.ReadFile(path)
				if string(data) != "base\n" {
					t.Fatal("old editor overwrote rollback", string(data))
				}
			}
		})
	}
}
func TestDesktopGitPathsDeleteFailureAndCancel(t *testing.T) {
	for _, mode := range []string{"rm-failed", "during-rm", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			d, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			pathsWrite(t, root, "a.txt", "changed\n")
			pathsWrite(t, root, "new", "new")
			p := pathsParams(t, base, rpc.GitPathsRollback, "a.txt", "new")
			gate := filepath.Join(t.TempDir(), "started")
			t.Setenv("PATHS_STARTED", gate)
			switch mode {
			case "rm-failed":
				gitReadWrapper(t, `case " $* " in *" rm --cached "*) echo failed >&2; exit 1;; esac`)
			case "during-rm":
				gitReadWrapper(t, `case " $* " in *" rm --cached "*) printf newer > new; exit 0;; esac`)
			case "cancel":
				gitReadWrapper(t, `case " $* " in *" rm --cached "*) touch "$PATHS_STARTED"; while :; do sleep 0.01; done;; esac`)
			}
			var out rpc.GitPathsResult
			if mode == "cancel" {
				done := make(chan error, 1)
				go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", pathsCall(owner, 1, p), &out) }()
				waitFor(t, "second command", func() bool { _, err := os.Stat(gate); return err == nil })
				current := desktopTakeTest(t, d, "new-owner")
				var receipt rpc.DesktopOperation
				err := rpc.Call(t.Context(), socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: current, Operation: rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}}, &receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if out.Status != rpc.GitFetchCanceled {
					t.Fatal(out)
				}
			} else {
				out = pathsRPC(t, socket, owner, 1, p)
			}
			if !out.Started || out.Status == rpc.GitFetchSucceeded {
				t.Fatal("failed operation", out)
			}
			data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if string(data) != "base\n" {
				t.Fatal("completed restore was undone")
			}
			data, err := os.ReadFile(filepath.Join(root, "new"))
			if err != nil || !strings.HasPrefix(string(data), "new") {
				t.Fatal("new file removed despite failure", err)
			}
		})
	}
}

func TestDesktopGitPathsWritePreconditions(t *testing.T) {
	for _, mode := range []string{"head", "version", "checksum", "directory"} {
		t.Run(mode, func(t *testing.T) {
			_, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			pathsWrite(t, root, "a.txt", "clicked\n")
			p := pathsParams(t, base, rpc.GitPathsWrite, "a.txt")
			p.Data = []byte("reverted\n")
			sum := sha256.Sum256(p.Data)
			p.Hash = hex.EncodeToString(sum[:])
			switch mode {
			case "head":
				readGit(t, root, "commit", "--allow-empty", "-qm", "changed HEAD")
			case "version":
				pathsWrite(t, root, "a.txt", "newer\n")
			case "checksum":
				p.Hash = strings.Repeat("0", 64)
			case "directory":
				os.Remove(filepath.Join(root, "a.txt"))
				os.Mkdir(filepath.Join(root, "a.txt"), 0700)
			}
			if mode == "checksum" {
				desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pathsCall(owner, 1, p), nil), rpc.CodeBadRequest)
				return
			}
			out := pathsRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatalf("write preflight %+v", out)
			}
			data, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if string(data) == "reverted\n" {
				t.Fatal("stale hunk written")
			}
		})
	}
}
func TestDesktopGitPathsRollbackGroups(t *testing.T) {
	_, socket, owner, base := pathsFixture(t, true)
	root := base.Expected.Root
	pathsWrite(t, root, "a.txt", "changed\n")
	pathsWrite(t, root, "added", "added")
	readGit(t, root, "add", "added")
	pathsWrite(t, root, "new", "new")
	p := pathsParams(t, base, rpc.GitPathsRollback, "a.txt", "new")
	added := pathsParams(t, base, rpc.GitPathsUnstage, "added")
	p.Unstage = added.Paths
	out := pathsRPC(t, socket, owner, 1, p)
	want := [][]string{{"restore", "--source=HEAD", "--staged", "--worktree", "--", ":(literal)a.txt"}, {"restore", "--staged", "--", ":(literal)added"}, {"rm", "--cached", "-q", "--ignore-unmatch", "--", ":(literal)new"}}
	var got [][]string
	for _, log := range out.Logs {
		got = append(got, log.Args)
	}
	if out.Status != rpc.GitFetchSucceeded || !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered rollback %+v", out)
	}
	if data, err := os.ReadFile(filepath.Join(root, "added")); err != nil || string(data) != "added" {
		t.Fatal("unstage deleted file")
	}
	if index := readGit(t, root, "ls-files", "added"); index != "" {
		t.Fatal("new file stayed staged")
	}
}
