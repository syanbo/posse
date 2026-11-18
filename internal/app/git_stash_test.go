package app

import (
	"context"
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func stashResultFixture(p rpc.GitStashParams, status string) rpc.GitStashResult {
	out := rpc.GitStashResult{Target: p.Metadata(), Status: status, Started: status != rpc.GitFetchConflict, Logs: []gitx.Run{}}
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
		args := []string{"stash", p.Action}
		if p.Action == rpc.GitStashPush {
			args = append(args, "--include-untracked")
			if p.MessageHash != rpc.GitCommitHash(nil) {
				args = append(args, "-m", "[message omitted]")
			}
		} else {
			if p.Action != rpc.GitStashDrop {
				args = append(args, "--index")
			}
			args = append(args, "stash@{1}")
		}
		out.Logs = append(out.Logs, gitx.Run{ID: 1, OpID: 1, Op: "stash", Dir: p.Expected.Root, Args: args, Start: time.Now(), Done: true})
	}
	return out
}
func stashDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitStashResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.stash", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func stashAppFixture(t *testing.T, controlled bool, handler func(rpc.GitStashParams) (any, error)) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f, v := commitAppFixture(t, controlled, func(rpc.GitCommitParams) (any, error) { t.Error("unexpected commit"); return nil, nil })
	v.git.stashes = []gitx.Stash{{Ref: "stash@{0}", Hash: strings.Repeat("c", 40), Message: "On other: saved changes", Branch: "other", When: time.Now()}}
	stashes := slices.Clone(v.git.stashes)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.stash" {
			var q rpc.GitStashParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			return handler(q)
		}
		out, err := normal(ctx, p)
		if p.Method == "git.status" && err == nil {
			result := out.(rpc.GitStatusResult)
			result.Stashes = slices.Clone(stashes)
			return result, nil
		}
		return out, err
	}
	f.mu.Unlock()
	return a, f, v
}
func TestTargetGitStashViewer(t *testing.T) {
	var calls atomic.Int32
	a, _, v := stashAppFixture(t, false, func(p rpc.GitStashParams) (any, error) {
		calls.Add(1)
		return stashResultFixture(p, rpc.GitFetchSucceeded), nil
	})
	g := v.git
	s := g.stashes[0]
	g.autostash, g.autostashConflicts = s.Hash, true
	a.stashPush(appL(), v, g)
	a.stashTake(appL(), v, g, s, rpc.GitStashApply, "Apply", "other", nil)
	a.stashDrop(appL(), v, g, s, false)
	a.stashDrop(appL(), v, g, s, true)
	tt := newTester(func(c *ui.Context) { a.stashRow(c, v, g, s); a.autostashBanner(c, v, g) }, 640, 320)
	tt.Frame()
	if err := tt.Click(s.Message); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("saved changes") || tt.HasText(locales.In("zh-Hans").GitStashPop()) || tt.HasText(locales.In("zh-Hans").GitStashApply()) || tt.HasText(locales.In("zh-Hans").GitStashDeleteEllipsis()) || calls.Load() != 0 || a.err != "" || a.confirmDo != nil || a.promptDlg != nil {
		t.Fatal("viewer invoked stash", tt.Texts(), a.err)
	}
}

func TestTargetGitStashConfirmation(t *testing.T) {
	for _, action := range []string{rpc.GitStashPush, rpc.GitStashApply, rpc.GitStashPop, rpc.GitStashDrop} {
		for _, change := range []string{"none", "head", "index", "entries", "missing", "disconnect"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				var calls atomic.Int32
				a, _, v := stashAppFixture(t, true, func(p rpc.GitStashParams) (any, error) {
					calls.Add(1)
					if p.Action != action || p.Action != rpc.GitStashPush && p.OID != strings.Repeat("c", 40) || strings.Contains(p.OID, "stash@") {
						t.Error("wrong stash identity", p)
					}
					return stashResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				g := v.git
				s := g.stashes[0]
				var send func()
				switch action {
				case rpc.GitStashPush:
					a.stashPush(appL(), v, g)
					if a.promptDlg == nil || !a.promptDlg.optional {
						t.Fatal("missing optional prompt")
					}
					prompt := a.promptDlg
					send = func() { prompt.do("", false) }
				case rpc.GitStashDrop:
					a.stashDrop(appL(), v, g, s, false)
					send = a.confirmDo
				default:
					a.stashTake(appL(), v, g, s, action, "Take", "other", nil)
					send = a.confirmDo
				}
				if send == nil || calls.Load() != 0 {
					t.Fatal("confirmation missing")
				}
				switch change {
				case "head":
					g.status.Commit = strings.Repeat("d", 40)
				case "index":
					g.status.IndexID = strings.Repeat("d", 64)
				case "entries":
					g.status.Entries = append(g.status.Entries, gitx.Entry{Path: "later", X: '?', Y: '?', Untracked: true})
				case "missing":
					g.stashes = nil
				case "disconnect":
					a.host.detach()
					a.runPending()
				}
				send()
				a.runPending()
				allowed := change == "none" || action == rpc.GitStashDrop && (change == "head" || change == "index" || change == "entries") || action == rpc.GitStashPush && change == "missing" || (action == rpc.GitStashApply || action == rpc.GitStashPop) && change == "entries"
				if allowed {
					pumpSaves(t, []*app{a}, func() bool { return len(a.targetStashes) == 1 && a.targetStashes[0].done })
					if calls.Load() != 1 {
						t.Fatal("operation not sent")
					}
				} else if len(a.targetStashes) != 0 || calls.Load() != 0 || a.err != "" {
					t.Fatal("expired confirmation sent", a.err)
				}
			})
		}
	}
}

func TestTargetGitStashUnknownCancelQuery(t *testing.T) {
	var calls, cancels atomic.Int32
	a, f, v := stashAppFixture(t, true, func(rpc.GitStashParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	g := v.git
	oid := g.stashes[0].Hash
	g.autostash, g.autostashSeen = oid, true
	a.stashDrop(appL(), v, g, g.stashes[0], true)
	a.confirmDo()
	a.runPending()
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetStashes) == 1 && a.targetStashes[0].unknown })
	s := a.targetStashes[0]
	params := s.params
	other := &gitState{v: v, repo: g.repo + "/other", identity: g.identity}
	other.identity.Root, other.identity.GitDir = other.repo, other.repo+"/.git"
	v.repos.list = append(v.repos.list, other)
	a.gitNext(other)
	if g.busyStash != s || other.busyStash != s {
		t.Fatal("unknown did not hold common-dir")
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
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.stash", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != s.ref {
			t.Error("query changed ref")
		}
		return stashDoneFixture(ref, stashResultFixture(params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.cancelStash(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
	if s.done || g.busyStash != s || other.busyStash != s || cancels.Load() != 1 {
		t.Fatal("cancel receipt completed stash")
	}
	a.queryStash(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return s.done })
	g.stashes = nil
	tt := newTester(func(c *ui.Context) { a.autostashBanner(c, v, g) }, 640, 200)
	tt.Frame()
	if calls.Load() != 1 || g.busy != "" || other.busy != "" || g.autostash != oid || !reflect.DeepEqual(params, s.params) {
		t.Fatal("query resent or cleared disconnected banner")
	}
}

func TestTargetGitStashCompletion(t *testing.T) {
	for _, mode := range []string{"success", "failure", "disconnect", "lost-control", "closed-dialog"} {
		t.Run(mode, func(t *testing.T) {
			entered, gate := make(chan struct{}), make(chan struct{})
			defer close(gate)
			var refreshes atomic.Int32
			a, f, v := stashAppFixture(t, true, func(p rpc.GitStashParams) (any, error) {
				close(entered)
				<-gate
				status := rpc.GitFetchSucceeded
				if mode == "failure" {
					status = rpc.GitFetchFailed
				}
				return stashResultFixture(p, status), nil
			})
			g := v.git
			oid := g.stashes[0].Hash
			g.autostash = oid
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.status" {
					refreshes.Add(1)
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.stashDrop(appL(), v, g, g.stashes[0], true)
			a.confirmDo()
			a.runPending()
			waitConnectionEvent(t, entered)
			s := a.targetStashes[0]
			switch mode {
			case "disconnect":
				a.host.detach()
				a.runPending()
			case "lost-control":
				a.host.mu.Lock()
				a.host.current.Controlled = false
				a.host.mu.Unlock()
			case "closed-dialog":
				a.confirmDo, a.promptDlg = nil, nil
			}
			gate <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done })
			if mode == "success" || mode == "closed-dialog" {
				pumpSaves(t, []*app{a}, func() bool { return refreshes.Load() == 1 })
				if g.autostash != "" {
					t.Fatal("successful banner not cleared")
				}
			} else if g.autostash != oid || refreshes.Load() != 0 {
				t.Fatal("late or failed result changed view")
			}
		})
	}
}

func TestTargetGitStashOwnerHost(t *testing.T) {
	a, _, v := stashAppFixture(t, true, func(rpc.GitStashParams) (any, error) { t.Fatal("unexpected stash"); return nil, nil })
	owner := workspace{ID: "other-workspace", ProjectID: a.workspaces[0].ProjectID, Branch: "other"}
	a.workspaces = append(a.workspaces, owner)
	a.byProject[owner.ProjectID] = append(a.byProject[owner.ProjectID], owner)
	if got := a.stashOwner(v, "other"); got == nil || got.ID != owner.ID {
		t.Fatal("same-host workspace missing")
	}
	v.remote = false
	if a.stashOwner(v, "other") != nil {
		t.Fatal("cross-host workspace offered")
	}
}

func TestTargetGitStashUnknownQueueRechecks(t *testing.T) {
	var calls atomic.Int32
	a, _, v := stashAppFixture(t, true, func(rpc.GitStashParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
	})
	g := v.git
	target, p, ok := a.stashParams(v, g, rpc.GitStashPush, "")
	if !ok {
		t.Fatal("missing stash target")
	}
	a.startTargetStash(appL(), target, "Stash", p, nil, nil)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetStashes) == 1 && a.targetStashes[0].unknown })
	s := a.targetStashes[0]
	// ready 回调只在原队列出队后运行，模拟旧操作更新了索引状态。
	g.queue = append(g.queue, gitOp{ready: func() { g.status.IndexID = strings.Repeat("e", 64) }})
	a.startTargetStash(appL(), target, "Stash", p, nil, nil)
	if a.confirmDo == nil {
		t.Fatal("unknown lacked confirmation")
	}
	a.confirmDo()
	a.runPending()
	if !s.acknowledged || calls.Load() != 1 || len(a.targetStashes) != 1 || len(g.queue) != 0 || g.busy != "" {
		t.Fatal("stale stash sent after queued change")
	}
}

func TestTargetGitStashConsole(t *testing.T) {
	a, _, v := stashAppFixture(t, true, func(rpc.GitStashParams) (any, error) { return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost") })
	target, p, ok := a.stashParams(v, v.git, rpc.GitStashPush, "")
	if !ok {
		t.Fatal("missing stash target")
	}
	a.startTargetStash(appL(), target, appL().GitStash(), p, nil, nil)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetStashes) == 1 && a.targetStashes[0].unknown })
	tt := newTester(func(c *ui.Context) { a.gitConsole(c, &a.workspaces[0], v) }, 800, 500)
	tt.Frame()
	if !tt.HasText(locales.In("zh-Hans").FetchQuery()) || !tt.HasText(locales.In("zh-Hans").CommonCancel()) || tt.HasText(locales.In("zh-Hans").FetchConsoleScope()) {
		t.Fatal("stash console missing recovery controls", tt.Texts())
	}
}

func TestPreviewTargetGitStash(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, controlled := range []bool{false, true} {
			a, _, v := stashAppFixture(t, controlled, func(p rpc.GitStashParams) (any, error) { return stashResultFixture(p, rpc.GitFetchSucceeded), nil })
			a.openFilesPanel(&a.workspaces[0], sideChanges)
			v.side, v.ch.tab = sideChanges, 1
			v.git.autostash, v.git.autostashConflicts = v.git.stashes[0].Hash, true
			tt := newPreviewTester(a, dark)
			if !tt.HasText("saved changes") {
				t.Fatal("preview missing stash")
			}
			savePNG(t, tt, "git-stash-"+map[bool]string{true: "control", false: "viewer"}[controlled]+map[bool]string{true: "-dark", false: "-light"}[dark]+".png")
		}
	}
}

func TestLocalDesktopStashQueue(t *testing.T) {
	for _, state := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "unknown"} {
		t.Run(state, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			gitRun(t, v.root, "config", "user.name", "Stash test")
			gitRun(t, v.root, "config", "user.email", "stash@example.invalid")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "initial")
			os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
			g := v.git
			g.status, _ = gitx.ReadStatus(t.Context(), v.root)
			target := a.gitTarget(&a.workspaces[0], v, g)
			p := rpc.GitStashParams{GitFetchParams: target.params, Action: rpc.GitStashPush, Head: g.status.Commit, IndexID: g.status.IndexID, Entries: slices.Clone(g.status.Entries), MessageHash: rpc.GitCommitHash(nil)}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var calls, stashs atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "git.stash" {
					return normal(ctx, p)
				}
				var q rpc.GitStashParams
				json.Unmarshal(p.Params, &q)
				calls.Add(1)
				close(entered)
				<-release
				if state == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
				}
				return stashResultFixture(q, state), nil
			}
			f.mu.Unlock()
			a.startTargetStash(appL(), target, "Stash", p, nil, nil)
			waitConnectionEvent(t, entered)
			s := a.targetStashes[0]
			a.gitDo(appL(), g, "Stash", func(ctx context.Context, root string) error {
				stashs.Add(1)
				return gitx.Commit(ctx, root, "queued", false, true)
			})
			if g.busyStash != s || len(g.queue) != 1 || stashs.Load() != 0 {
				t.Fatal("stash did not hold queue")
			}
			release <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if state == "unknown" {
				if stashs.Load() != 0 || g.busyStash != s {
					t.Fatal("unknown released queue")
				}
				f.mu.Lock()
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return stashDoneFixture(ref, stashResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.queryStash(appL(), s)
			}
			pumpSaves(t, []*app{a}, func() bool { return g.busy == "" && stashs.Load() == 1 })
			if calls.Load() != 1 || len(g.queue) != 0 {
				t.Fatal("queue replayed")
			}
		})
	}
}

func TestLocalDesktopStashUnknownConfirmationQueue(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	gitRun(t, v.root, "config", "user.name", "Stash test")
	gitRun(t, v.root, "config", "user.email", "stash@example.invalid")
	gitRun(t, v.root, "add", ".")
	gitRun(t, v.root, "commit", "-qm", "initial")
	os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	g := v.git
	g.status, _ = gitx.ReadStatus(t.Context(), v.root)
	target := a.gitTarget(&a.workspaces[0], v, g)
	p := rpc.GitStashParams{GitFetchParams: target.params, Action: rpc.GitStashPush, Head: g.status.Commit, IndexID: g.status.IndexID, Entries: slices.Clone(g.status.Entries), MessageHash: rpc.GitCommitHash(nil)}
	var calls atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.stash" {
			return normal(ctx, p)
		}
		var q rpc.GitStashParams
		json.Unmarshal(p.Params, &q)
		if calls.Add(1) == 1 {
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return stashResultFixture(q, rpc.GitFetchSucceeded), nil
	}
	f.mu.Unlock()
	a.startTargetStash(appL(), target, "Stash", p, nil, nil)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetStashes) == 1 && a.targetStashes[0].unknown })
	old := a.targetStashes[0]
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.gitDo(appL(), g, "Queued Git", func(ctx context.Context, root string) error {
		close(entered)
		<-release
		_, err := gitx.ReadStatus(ctx, root)
		return err
	})
	a.startTargetStash(appL(), target, "Stash", p, nil, nil)
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
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetStashes) == 2 && a.targetStashes[1].done })
	if calls.Load() != 2 || old.ref == a.targetStashes[1].ref {
		t.Fatal("new operation reused original")
	}
}
