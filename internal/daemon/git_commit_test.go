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
)

func commitParams(t *testing.T, base rpc.GitFetchParams, action string, names ...string) rpc.GitCommitParams {
	t.Helper()
	st, err := gitx.ReadStatus(t.Context(), base.Expected.Root)
	if err != nil {
		t.Fatal(err)
	}
	p := rpc.GitCommitParams{GitFetchParams: base, Action: action, Head: st.Commit, IndexID: st.IndexID, Message: "subject\n\nbody", MessageHash: rpc.GitCommitHash([]byte("subject\n\nbody"))}
	for _, name := range names {
		i := slices.IndexFunc(st.Entries, func(e gitx.Entry) bool { return e.Path == name })
		if i < 0 {
			t.Fatalf("entry missing %s: %+v", name, st)
		}
		e := st.Entries[i]
		v, err := files.ReadVersion(t.Context(), filepath.Join(base.Expected.Root, e.Path))
		if err != nil {
			t.Fatal(err)
		}
		item := rpc.GitPath{Entry: e, Version: &v}
		if e.Orig != "" {
			v, err := files.ReadVersion(t.Context(), filepath.Join(base.Expected.Root, e.Orig))
			if err != nil {
				t.Fatal(err)
			}
			item.OrigVersion = &v
		}
		p.Paths = append(p.Paths, item)
		if e.Untracked && action == rpc.GitCommitPaths {
			p.Add = append(p.Add, e.Path)
		}
	}
	return p
}
func commitCall(owner rpc.DesktopIdentity, seq uint64, p rpc.GitCommitParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: owner, Sequence: seq, Method: "git.commit", Params: raw}
}
func commitRPC(t *testing.T, socket string, owner rpc.DesktopIdentity, seq uint64, p rpc.GitCommitParams) rpc.GitCommitResult {
	t.Helper()
	var out rpc.GitCommitResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", commitCall(owner, seq, p), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func TestDesktopGitCommitPaths(t *testing.T) {
	for _, mode := range []string{"partial", "literal", "new", "new-not-added", "amend", "missing", "unborn", "rename"} {
		t.Run(mode, func(t *testing.T) {
			_, socket, owner, base := pathsFixture(t, mode != "unborn")
			root := base.Expected.Root
			if mode == "unborn" {
				readGit(t, root, "rm", "--cached", "a.txt")
			}
			pathsWrite(t, root, "a.txt", "selected\n")
			pathsWrite(t, root, "other", "staged\n")
			readGit(t, root, "add", "other")
			names := []string{"a.txt"}
			switch mode {
			case "literal":
				pathsWrite(t, root, "[x]*", "literal")
				pathsWrite(t, root, "xxx", "wild")
				names = []string{"[x]*"}
			case "new", "new-not-added":
				pathsWrite(t, root, "new", "new")
				names = []string{"new"}
			case "amend":
				names = nil
			case "missing":
				if err := os.Remove(filepath.Join(root, "a.txt")); err != nil {
					t.Fatal(err)
				}
			case "rename":
				readGit(t, root, "restore", "--worktree", "a.txt")
				readGit(t, root, "mv", "a.txt", "renamed")
				names = []string{"renamed"}
			}
			p := commitParams(t, base, rpc.GitCommitPaths, names...)
			p.Amend = mode == "amend"
			if mode == "new-not-added" {
				p.Add = nil
			}
			before := readGit(t, root, "ls-files", "-s", "--", "other")
			out := commitRPC(t, socket, owner, 1, p)
			if mode == "new-not-added" {
				if out.Status != rpc.GitFetchFailed || !out.Started || readGit(t, root, "ls-files", "--", "new") != "" {
					t.Fatal("unlisted new file added", out)
				}
				return
			}
			if out.Status != rpc.GitFetchSucceeded || !out.Started || out.Target.Message != "" || len(out.Target.Data) > 0 {
				t.Fatalf("commit %+v", out)
			}
			if readGit(t, root, "ls-tree", "HEAD", "--", "other") != "" || before != readGit(t, root, "ls-files", "-s", "--", "other") {
				t.Fatal("unselected staged file changed")
			}
			var commands [][]string
			for _, r := range out.Logs {
				commands = append(commands, r.Args)
			}
			if !reflect.DeepEqual(commands, rpc.GitCommitCommands(p)) {
				t.Fatalf("commands %q", commands)
			}
			if readGit(t, root, "log", "-1", "--format=%B") != "subject\n\nbody" {
				t.Fatal("message lost")
			}
			switch mode {
			case "amend":
				if readGit(t, root, "show", "HEAD:a.txt") != "base" {
					t.Fatal("amend included worktree")
				}
			case "literal":
				if readGit(t, root, "ls-tree", "HEAD", "--", "xxx") != "" || readGit(t, root, "show", "HEAD:[x]*") != "literal" {
					t.Fatal("literal paths expanded")
				}
			case "missing":
				if readGit(t, root, "ls-tree", "HEAD", "--", "a.txt") != "" {
					t.Fatal("deleted path retained")
				}
			case "rename":
				if readGit(t, root, "ls-tree", "HEAD", "--", "a.txt") != "" || readGit(t, root, "show", "HEAD:renamed") != "base" {
					t.Fatal("rename incomplete")
				}
			}
		})
	}
}
func TestDesktopGitCommitPreconditions(t *testing.T) {
	for _, mode := range []string{"head", "index", "file", "missing", "workspace", "identity"} {
		t.Run(mode, func(t *testing.T) {
			d, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			pathsWrite(t, root, "a.txt", "clicked\n")
			p := commitParams(t, base, rpc.GitCommitPaths, "a.txt")
			switch mode {
			case "head":
				readGit(t, root, "commit", "--allow-empty", "-qm", "new head")
			case "index":
				pathsWrite(t, root, "other", "staged")
				readGit(t, root, "add", "other")
			case "file":
				pathsWrite(t, root, "a.txt", "later\n")
			case "missing":
				os.Remove(filepath.Join(root, "a.txt"))
			case "workspace":
				if err := d.store.SetWorkspacePath(base.WorkspaceID, t.TempDir(), ""); err != nil {
					t.Fatal(err)
				}
			case "identity":
				p.Expected.RootInode++
			}
			head := readGit(t, root, "rev-parse", "HEAD")
			index, _ := os.ReadFile(filepath.Join(root, ".git/index"))
			file, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			out := commitRPC(t, socket, owner, 1, p)
			if out.Status != rpc.GitFetchConflict || out.Started || len(out.Logs) != 0 {
				t.Fatal("stale commit", out)
			}
			afterIndex, _ := os.ReadFile(filepath.Join(root, ".git/index"))
			afterFile, _ := os.ReadFile(filepath.Join(root, "a.txt"))
			if head != readGit(t, root, "rev-parse", "HEAD") || string(index) != string(afterIndex) || string(file) != string(afterFile) {
				t.Fatal("preflight changed repository")
			}
		})
	}
}
func TestDesktopGitCommitWhole(t *testing.T) {
	_, socket, owner, base := pathsFixture(t, true)
	root := base.Expected.Root
	readGit(t, root, "switch", "-c", "other")
	pathsWrite(t, root, "merged", "other")
	readGit(t, root, "add", "merged")
	readGit(t, root, "commit", "-qm", "other")
	readGit(t, root, "switch", "main")
	readGit(t, root, "merge", "--no-ff", "--no-commit", "other")
	pathsWrite(t, root, "a.txt", "resolution\n")
	p := commitParams(t, base, rpc.GitCommitWhole, "a.txt")
	out := commitRPC(t, socket, owner, 1, p)
	if out.Status != rpc.GitFetchSucceeded || readGit(t, root, "show", "HEAD:merged") != "other" || readGit(t, root, "show", "HEAD:a.txt") != "resolution" {
		t.Fatal(out)
	}
	for _, r := range out.Logs {
		if slices.Contains(r.Args, "--only") || slices.Contains(r.Args, "-a") {
			t.Fatal(r.Args)
		}
	}
	if len(strings.Fields(readGit(t, root, "rev-list", "--parents", "-n", "1", "HEAD"))) != 3 {
		t.Fatal("merge not completed")
	}
	pathsWrite(t, root, "a.txt", "next\n")
	p = commitParams(t, base, rpc.GitCommitWhole, "a.txt")
	out = commitRPC(t, socket, owner, 2, p)
	if out.Status != rpc.GitFetchConflict || out.Started {
		t.Fatal("whole outside operation", out)
	}
}
func TestDesktopGitCommitChange(t *testing.T) {
	for _, mode := range []string{"success", "staged", "operation", "literal", "sensitive-name"} {
		t.Run(mode, func(t *testing.T) {
			_, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			path := "a.txt"
			if mode == "literal" || mode == "sensitive-name" {
				path = "[x]*"
				if mode == "sensitive-name" {
					path = "token=value"
				}
				pathsWrite(t, root, path, "base\n")
				readGit(t, root, "add", "--", ":(literal)"+path)
				readGit(t, root, "commit", "-qm", "literal")
			}
			pathsWrite(t, root, path, "working\n")
			pathsWrite(t, root, "other", "staged")
			readGit(t, root, "add", "other")
			if mode == "staged" {
				readGit(t, root, "add", "--", ":(literal)"+path)
			}
			if mode == "operation" {
				pathsWrite(t, root, ".git/CHERRY_PICK_HEAD", readGit(t, root, "rev-parse", "HEAD")+"\n")
			}
			p := commitParams(t, base, rpc.GitCommitChange)
			p.Path = path
			p.Data = []byte("partial\n")
			p.Hash = rpc.GitCommitHash(p.Data)
			objects := readGit(t, root, "count-objects", "-v")
			other := readGit(t, root, "ls-files", "-s", "--", "other")
			out := commitRPC(t, socket, owner, 1, p)
			if mode == "staged" || mode == "operation" {
				if out.Status != rpc.GitFetchFailed || out.Started || len(out.Logs) != 0 || objects != readGit(t, root, "count-objects", "-v") {
					t.Fatal("preflight wrote objects", out)
				}
				if mode == "staged" && (out.Error == nil || out.Error.Message != gitx.ErrStagedChange.Error()) {
					t.Fatal("wrong staged error", out)
				}
				return
			}
			if out.Status != rpc.GitFetchSucceeded || !out.Started || readGit(t, root, "show", "HEAD:"+path) != "partial" || readGit(t, root, "show", ":"+path) != "partial" {
				t.Fatal(out)
			}
			working, _ := os.ReadFile(filepath.Join(root, path))
			if string(working) != "working\n" || other != readGit(t, root, "ls-files", "-s", "--", "other") || readGit(t, root, "ls-tree", "HEAD", "--", "other") != "" {
				t.Fatal("other staged or worktree changed")
			}
		})
	}
}
func TestDesktopGitCommitSaveLocks(t *testing.T) {
	for _, saveFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "save-first", false: "commit-first"}[saveFirst], func(t *testing.T) {
			d, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			path := filepath.Join(root, "a.txt")
			pathsWrite(t, root, "a.txt", "clicked\n")
			p := commitParams(t, base, rpc.GitCommitPaths, "a.txt")
			version := *p.Paths[0].Version
			done := make(chan rpc.GitCommitResult, 1)
			errs := make(chan error, 1)
			start := func() {
				go func() {
					var out rpc.GitCommitResult
					err := rpc.Call(t.Context(), socket, "desktop.call", commitCall(owner, 1, p), &out)
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
				select {
				case out := <-done:
					t.Fatal("commit bypassed save", out)
				case <-time.After(100 * time.Millisecond):
				}
				pathsWrite(t, root, "a.txt", "saved\n")
				release()
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
				if out := <-done; out.Status != rpc.GitFetchConflict || out.Started {
					t.Fatal("stale commit", out)
				}
			} else {
				started, gate := filepath.Join(t.TempDir(), "started"), filepath.Join(t.TempDir(), "gate")
				t.Setenv("COMMIT_STARTED", started)
				t.Setenv("COMMIT_GATE", gate)
				gitReadWrapper(t, `case " $* " in *" commit -F "*) touch "$COMMIT_STARTED"; while test ! -f "$COMMIT_GATE"; do sleep 0.01; done;; esac`)
				t.Cleanup(func() { os.WriteFile(gate, nil, 0600) })
				start()
				waitFor(t, "commit holds locks", func() bool { _, err := os.Stat(started); return err == nil })
				saved := make(chan error, 1)
				data := []byte("old editor\n")
				go func() {
					raw, _ := json.Marshal(rpc.FileSaveParams{Path: path, Expected: version, Data: data, Hash: rpc.GitCommitHash(data)})
					_, err := d.filesSave(context.WithValue(t.Context(), desktopCallContext{}, desktopRequestContext{identity: owner}), raw)
					saved <- err
				}()
				select {
				case err := <-saved:
					t.Fatal("save bypassed commit", err)
				case <-time.After(100 * time.Millisecond):
				}
				os.WriteFile(gate, nil, 0600)
				if err := <-errs; err != nil {
					t.Fatal(err)
				}
				if out := <-done; out.Status != rpc.GitFetchSucceeded {
					t.Fatal(out)
				}
				if err := <-saved; err != nil {
					t.Fatal(err)
				}
				if readGit(t, root, "show", "HEAD:a.txt") != "clicked" {
					t.Fatal("queued old save entered commit")
				}
			}
		})
	}
}
func TestDesktopGitCommitFailureAndCancel(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{true: "cancel", false: "hook"}[cancel], func(t *testing.T) {
			d, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			pathsWrite(t, root, "new", "new")
			p := commitParams(t, base, rpc.GitCommitPaths, "new")
			marker := filepath.Join(t.TempDir(), "hook")
			t.Setenv("COMMIT_STARTED", marker)
			script := "#!/bin/sh\necho rejected >&2\nexit 1\n"
			if cancel {
				script = "#!/bin/sh\ntouch \"$COMMIT_STARTED\"\nwhile :; do sleep 0.01; done\n"
			}
			if err := os.WriteFile(filepath.Join(root, ".git/hooks/pre-commit"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var out rpc.GitCommitResult
			if cancel {
				done := make(chan error, 1)
				go func() { done <- rpc.Call(t.Context(), socket, "desktop.call", commitCall(owner, 1, p), &out) }()
				waitFor(t, "hook", func() bool { _, err := os.Stat(marker); return err == nil })
				current := desktopTakeTest(t, d, "new-owner")
				var receipt rpc.DesktopOperation
				if err := rpc.Call(t.Context(), socket, "desktop.operation.cancel", rpc.DesktopOperationCancel{Desktop: current, Operation: rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}}, &receipt); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
				if out.Status != rpc.GitFetchCanceled {
					t.Fatal(out)
				}
			} else {
				out = commitRPC(t, socket, owner, 1, p)
			}
			if !out.Started || out.Status == rpc.GitFetchSucceeded || readGit(t, root, "rev-parse", "HEAD") != p.Head || readGit(t, root, "show", ":new") != "new" {
				t.Fatal("failed commit reset staging", out)
			}
			for _, r := range out.Logs {
				if slices.Contains(r.Args, "reset") || slices.Contains(r.Args, "--no-verify") {
					t.Fatal("unsafe command", r.Args)
				}
			}
		})
	}
}
func TestDesktopGitCommitValidation(t *testing.T) {
	_, socket, owner, base := pathsFixture(t, true)
	root := base.Expected.Root
	pathsWrite(t, root, "new", "new")
	original := commitParams(t, base, rpc.GitCommitPaths, "new")
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "git.commit", original, nil), rpc.CodeReadOnly)
	viewer := owner
	viewer.ClientID = "viewer"
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", commitCall(viewer, 1, original), nil), rpc.CodeReadOnly)
	for i, mode := range []string{"empty", "long", "hash", "path", "absolute", "unknown"} {
		p := original
		p.Paths = slices.Clone(p.Paths)
		switch mode {
		case "empty":
			p.Message = " "
			p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
		case "long":
			p.Message = strings.Repeat("x", rpc.GitCommitMessageLimit+1)
			p.MessageHash = rpc.GitCommitHash([]byte(p.Message))
		case "hash":
			p.IndexID = ""
		case "path":
			p.Paths[0].Entry.Path = "../new"
		case "absolute":
			p.Paths[0].Entry.Path = "/tmp/new"
		}
		call := commitCall(owner, uint64(i+1), p)
		if mode == "unknown" {
			call.Params = append(call.Params[:len(call.Params)-1], []byte(`,"noVerify":true}`)...)
		}
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", call, nil), rpc.CodeBadRequest)
	}
}

func TestDesktopGitCommitLastMessage(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{true: "head-changed", false: "full-message"}[changed], func(t *testing.T) {
			_, socket, owner, base := pathsFixture(t, true)
			root := base.Expected.Root
			readGit(t, root, "commit", "--amend", "-qm", "subject\n\ncomplete body")
			marker := filepath.Join(t.TempDir(), "commands")
			t.Setenv("COMMIT_COMMANDS", marker)
			script := `case " $* " in *" log -1 --format=%B "*) printf '%s\n' "$*" >> "$COMMIT_COMMANDS";; esac`
			if changed {
				script += `\ncase " $* " in *" log -1 --format=%B "*) /usr/bin/git commit --allow-empty -qm changed;; esac`
				script = strings.ReplaceAll(script, `\ncase`, "\ncase")
			}
			gitReadWrapper(t, script)
			var out rpc.GitStatusResult
			err := mediaTestCall(t.Context(), socket, owner, "git.status", rpc.GitStatusParams{Path: root, LastMessage: true}, &out)
			if changed {
				desktopErrorCode(t, err, rpc.CodeConflict)
			} else if err != nil || out.LastMessage != "subject\n\ncomplete body" || out.Identity != base.Expected {
				t.Fatalf("last message %+v %v", out, err)
			}
			commands, _ := os.ReadFile(marker)
			if string(commands) != "-c core.fsmonitor=false log -1 --format=%B\n" {
				t.Fatal("wrong message read", string(commands))
			}
		})
	}
}
