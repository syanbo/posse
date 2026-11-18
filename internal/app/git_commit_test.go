package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func commitResultFixture(p rpc.GitCommitParams, status string) rpc.GitCommitResult {
	out := rpc.GitCommitResult{Target: p.Metadata(), Status: status, Started: status != rpc.GitFetchConflict, Logs: []gitx.Run{}}
	if status != rpc.GitFetchSucceeded {
		code := rpc.CodeInternal
		switch status {
		case rpc.GitFetchConflict:
			code = rpc.CodeConflict
		case rpc.GitFetchCanceled:
			code = "canceled"
		case rpc.GitFetchTimedOut:
			code = rpc.CodeTimeout
		}
		out.Error = rpc.Errorf(code, "test result")
	}
	if out.Started {
		commands := rpc.GitCommitCommands(p)
		if p.Action == rpc.GitCommitChange {
			commands = [][]string{{"commit", "-q", "-F", "-"}}
		}
		for i, args := range commands {
			out.Logs = append(out.Logs, gitx.Run{ID: int64(i + 1), OpID: 1, Op: "commit", Dir: p.Expected.Root, Args: args, Start: time.Now(), Done: true})
		}
	}
	return out
}
func commitDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitCommitResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.commit", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func commitAppFixture(t *testing.T, controlled bool, handler func(rpc.GitCommitParams) (any, error)) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f, v := pathsAppFixture(t, controlled, func(rpc.GitPathsParams) (any, error) { t.Error("unexpected paths write"); return nil, nil })
	v.git.status.IndexID = strings.Repeat("b", 64)
	id := v.git.identity
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.commit":
			var q rpc.GitCommitParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if p.Sequence == 0 {
				t.Error("missing sequence")
			}
			return handler(q)
		case "files.version":
			var q rpc.FileVersionParams
			json.Unmarshal(p.Params, &q)
			return fileSaveVersion(q.Path, "clicked"), nil
		case "git.status":
			var q rpc.GitStatusParams
			json.Unmarshal(p.Params, &q)
			identity := id
			identity.Root = q.Path
			identity.GitDir = filepath.Join(q.Path, ".git")
			identity.CommonDir = identity.GitDir
			return rpc.GitStatusResult{Identity: identity, Status: &gitx.Status{Commit: strings.Repeat("a", 40), IndexID: strings.Repeat("b", 64), Entries: []gitx.Entry{{Path: "same.txt", X: '.', Y: 'M'}}}, LastMessage: "old subject\n\ncomplete body"}, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	return a, f, v
}
func TestTargetGitCommitBatch(t *testing.T) {
	for _, mode := range []string{"success", "one-failed", "amend", "viewer", "shared-common"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v := commitAppFixture(t, mode != "viewer", func(p rpc.GitCommitParams) (any, error) {
				calls.Add(1)
				if p.Message != "same subject\n\nsame body" || p.Head != strings.Repeat("a", 40) || p.IndexID != strings.Repeat("b", 64) || !rpc.ValidGitCommitParams(p) {
					t.Error("wrong clicked params", p)
				}
				state := rpc.GitFetchSucceeded
				if mode == "one-failed" && strings.HasSuffix(p.Expected.Root, "second") {
					state = rpc.GitFetchFailed
				}
				return commitResultFixture(p, state), nil
			})
			first := v.git
			second := &gitState{v: v, repo: filepath.Join(v.root, "second"), identity: first.identity, status: &gitx.Status{Commit: first.status.Commit, IndexID: first.status.IndexID, Entries: append([]gitx.Entry{}, first.status.Entries...)}}
			second.identity.Root = second.repo
			second.identity.GitDir = filepath.Join(second.repo, ".git")
			second.identity.CommonDir = second.identity.GitDir
			if mode == "shared-common" {
				second.identity.CommonDir = first.identity.CommonDir
			}
			first.picks = map[string]bool{"same.txt": true}
			second.picks = map[string]bool{"same.txt": true}
			v.repos.list = append(v.repos.list, second)
			v.ch.message = "same subject\n\nsame body"
			v.ch.amend = mode == "amend"
			a.commit(appL(), v)
			if mode == "viewer" || mode == "amend" {
				if calls.Load() != 0 || len(a.targetCommits) != 0 {
					t.Fatal("forbidden commit sent")
				}
				if mode == "viewer" && a.err != "" {
					t.Fatal(a.err)
				}
				if mode == "amend" && a.err != appL().GitAmendOneRepo() {
					t.Fatal("missing amend error", a.err)
				}
				return
			}
			pumpSaves(t, []*app{a}, func() bool { return len(a.targetCommits) == 2 && a.targetCommits[0].done && a.targetCommits[1].done })
			if calls.Load() != 2 {
				t.Fatal("not one commit per repository", calls.Load())
			}
			if mode == "one-failed" {
				if v.ch.message == "" || len(first.picks) != 0 || !second.picks["same.txt"] {
					t.Fatal("batch failure cleared input or failed picks")
				}
			} else if v.ch.message != "" || v.ch.amend || len(first.picks) != 0 || len(second.picks) != 0 {
				t.Fatal("successful batch not cleared")
			}
		})
	}
}
func TestTargetGitCommitViewerAndAmend(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{true: "control", false: "viewer"}[controlled], func(t *testing.T) {
			var calls, reads atomic.Int32
			a, f, v := commitAppFixture(t, controlled, func(p rpc.GitCommitParams) (any, error) {
				calls.Add(1)
				return commitResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) { reads.Add(1); return normal(ctx, p) }
			f.mu.Unlock()
			a.setAmend(v, true)
			if controlled {
				pumpSaves(t, []*app{a}, func() bool { return v.ch.message != "" })
				if v.ch.message != "old subject\n\ncomplete body" || reads.Load() != 1 {
					t.Fatal("message not target full body")
				}
			}
			if a.err != "" || v.ch.amend != controlled || !controlled && reads.Load() != 0 {
				t.Fatal("amend guard", a.err)
			}
			tt := newTester(func(c *ui.Context) { a.commitBox(c, v) }, 640, 300)
			tt.Frame()
			if tt.HasText(locales.In("zh-Hans").GitAmend()) != controlled {
				t.Fatal("commit box visibility")
			}
			if !controlled {
				v.ch.message = "keep"
				a.commit(appL(), v)
				d := &fileDoc{}
				a.targetCommitChange(appL(), v, d, &changePop{}, "same.txt", []byte("x"), "message")
				if reads.Load() != 0 || calls.Load() != 0 || a.err != "" {
					t.Fatal("viewer invoked RPC")
				}
			}
		})
	}
}
func TestTargetGitCommitChangeGuard(t *testing.T) {
	for _, modified := range []bool{false, true} {
		t.Run(map[bool]string{true: "unsaved", false: "saved"}[modified], func(t *testing.T) {
			var calls atomic.Int32
			a, _, v := commitAppFixture(t, true, func(p rpc.GitCommitParams) (any, error) {
				calls.Add(1)
				if p.Action != rpc.GitCommitChange || string(p.Data) != "partial\n" || p.Path != "same.txt" {
					t.Error("change bytes lost", p)
				}
				return commitResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			cp := &changePop{}
			d := &fileDoc{path: filepath.Join(v.root, "same.txt"), kind: docText, headFor: v.git.status.Commit, headBuf: editor.NewBuffer("base\n"), ed: editor.State{Buffer: editor.NewBuffer("working\n")}, change: cp}
			v.docs = []*fileDoc{d}
			if modified {
				d.ed.Buffer.Set("unsaved\n")
			}
			a.targetCommitChange(appL(), v, d, cp, "same.txt", []byte("partial\n"), "subject")
			if modified {
				tt := newTester(func(c *ui.Context) { a.commitChangeInput(c, v, d, cp, "same.txt", editor.BaseChange{}) }, 640, 120)
				tt.Frame()
				if calls.Load() != 0 || !tt.HasText(locales.In("zh-Hans").GitSaveBeforeCommit()) {
					t.Fatal("unsaved change not blocked")
				}
				return
			}
			pumpSaves(t, []*app{a}, func() bool { return len(a.targetCommits) == 1 && a.targetCommits[0].done })
			if calls.Load() != 1 || d.change != nil || d.ed.Buffer.String() != "working\n" {
				t.Fatal("single change result")
			}
		})
	}
}
func TestTargetGitCommitUnknownCancelQuery(t *testing.T) {
	var calls, cancels atomic.Int32
	a, f, v := commitAppFixture(t, true, func(rpc.GitCommitParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	v.ch.message = "keep on disconnect"
	a.commit(appL(), v)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetCommits) == 1 && a.targetCommits[0].unknown })
	s := a.targetCommits[0]
	params := s.params
	g := v.git
	if g.busyCommit != s {
		t.Fatal("unknown released repository")
	}
	old := a.request().client
	a.host.detach()
	a.runPending()
	if err := a.host.connect(context.Background(), old); err != nil {
		t.Fatal(err)
	}
	takeRequestTestControl(t, a)
	current := *a.request().client.Desktop
	f.mu.Lock()
	f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
		cancels.Add(1)
		if p.Operation != s.ref || p.Desktop != current {
			t.Error("wrong cancel identity/ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.commit", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != s.ref {
			t.Error("query changed ref")
		}
		return commitDoneFixture(ref, commitResultFixture(params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.cancelCommit(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
	if s.done || g.busyCommit != s || cancels.Load() != 1 {
		t.Fatal("cancel receipt completed commit")
	}
	a.queryCommit(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return s.done })
	if calls.Load() != 1 || g.busy != "" || v.ch.message != "keep on disconnect" || !reflect.DeepEqual(params, s.params) {
		t.Fatal("query resent or cleared disconnected input")
	}
}

func TestPreviewTargetGitCommit(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, controlled := range []bool{false, true} {
			t.Run(map[bool]string{true: "control", false: "viewer"}[controlled]+map[bool]string{true: "-dark", false: "-light"}[dark], func(t *testing.T) {
				a, _, v := commitAppFixture(t, controlled, func(p rpc.GitCommitParams) (any, error) { return commitResultFixture(p, rpc.GitFetchSucceeded), nil })
				v.ch.message = "fix: preserve staged changes"
				a.openFilesPanel(&a.workspaces[0], sideChanges)
				v.side = sideChanges
				savePNG(t, newPreviewTester(a, dark), "git-commit-"+map[bool]string{true: "control", false: "viewer"}[controlled]+map[bool]string{true: "-dark", false: "-light"}[dark]+".png")
			})
		}
	}
}

func TestLocalDesktopCommitQueue(t *testing.T) {
	for _, state := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "unknown"} {
		t.Run(state, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			gitRun(t, v.root, "config", "user.name", "Commit test")
			gitRun(t, v.root, "config", "user.email", "commit@example.invalid")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "initial")
			os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
			g := v.git
			g.status, _ = gitx.ReadStatus(t.Context(), v.root)
			target := a.gitTarget(&a.workspaces[0], v, g)
			p := rpc.GitCommitParams{GitFetchParams: target.params, Action: rpc.GitCommitPaths, Head: g.status.Commit, IndexID: g.status.IndexID, Message: "subject", MessageHash: rpc.GitCommitHash([]byte("subject")), Amend: true}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var calls, commits atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "git.commit" {
					return normal(ctx, p)
				}
				var q rpc.GitCommitParams
				json.Unmarshal(p.Params, &q)
				calls.Add(1)
				close(entered)
				<-release
				if state == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
				}
				return commitResultFixture(q, state), nil
			}
			f.mu.Unlock()
			a.startTargetCommit(appL(), target, "Commit", p, nil, nil)
			waitConnectionEvent(t, entered)
			s := a.targetCommits[0]
			a.gitDo(appL(), g, "Commit", func(ctx context.Context, root string) error {
				commits.Add(1)
				return gitx.Commit(ctx, root, "queued", false, true)
			})
			if g.busyCommit != s || len(g.queue) != 1 || commits.Load() != 0 {
				t.Fatal("commit did not hold queue")
			}
			release <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if state == "unknown" {
				if commits.Load() != 0 || g.busyCommit != s {
					t.Fatal("unknown released queue")
				}
				f.mu.Lock()
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return commitDoneFixture(ref, commitResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.queryCommit(appL(), s)
			}
			pumpSaves(t, []*app{a}, func() bool { return g.busy == "" && commits.Load() == 1 })
			if calls.Load() != 1 || len(g.queue) != 0 {
				t.Fatal("queue replayed")
			}
		})
	}
}

func TestLocalDesktopCommitUnknownConfirmationQueue(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	gitRun(t, v.root, "config", "user.name", "Commit test")
	gitRun(t, v.root, "config", "user.email", "commit@example.invalid")
	gitRun(t, v.root, "add", ".")
	gitRun(t, v.root, "commit", "-qm", "initial")
	os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	g := v.git
	g.status, _ = gitx.ReadStatus(t.Context(), v.root)
	target := a.gitTarget(&a.workspaces[0], v, g)
	p := rpc.GitCommitParams{GitFetchParams: target.params, Action: rpc.GitCommitPaths, Head: g.status.Commit, IndexID: g.status.IndexID, Message: "subject", MessageHash: rpc.GitCommitHash([]byte("subject")), Amend: true}
	var calls atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.commit" {
			return normal(ctx, p)
		}
		var q rpc.GitCommitParams
		json.Unmarshal(p.Params, &q)
		if calls.Add(1) == 1 {
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return commitResultFixture(q, rpc.GitFetchSucceeded), nil
	}
	f.mu.Unlock()
	a.startTargetCommit(appL(), target, "Commit", p, nil, nil)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetCommits) == 1 && a.targetCommits[0].unknown })
	old := a.targetCommits[0]
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.gitDo(appL(), g, "Queued Git", func(ctx context.Context, root string) error {
		close(entered)
		<-release
		_, err := gitx.ReadStatus(ctx, root)
		return err
	})
	a.startTargetCommit(appL(), target, "Commit", p, nil, nil)
	if a.confirmDo == nil || calls.Load() != 1 || old.acknowledged {
		t.Fatal("unknown operation lacked confirmation")
	}
	a.confirmDo()
	a.runPending()
	waitConnectionEvent(t, entered)
	if !old.acknowledged || calls.Load() != 1 || g.busy != "Queued Git" {
		t.Fatal("new operation overtook queued Git")
	}
	release <- struct{}{}
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetCommits) == 2 && a.targetCommits[1].done })
	if calls.Load() != 2 || old.ref == a.targetCommits[1].ref {
		t.Fatal("new operation reused original")
	}
}
