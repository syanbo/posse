package app

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func autoFetchFixture(t *testing.T, remote, controlled bool, handler func(context.Context, rpc.DesktopCall, rpc.GitFetchParams) (any, error)) (*app, *hostControlFixture, *filesView, *ui.Tester, *atomic.Int32) {
	t.Helper()
	var a *app
	var f *hostControlFixture
	var v *filesView
	if remote {
		a, f, v, _ = remoteGitFixture(t)
		if controlled {
			takeRequestTestControl(t, a)
		}
	} else {
		a, f = localDesktopFixture(t)
		connectLocalDesktop(t, a)
		if !controlled {
			if err := a.host.yield(a.host.snapshot()).release(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		v, _, _ = localDesktopRepo(t, a, f)
		a.selected = "w"
	}
	discoverTargetGit(t, a, v)
	var discoveries atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.discover" {
			discoveries.Add(1)
		}
		if p.Method != "git.fetch" {
			return normal(ctx, p)
		}
		var q rpc.GitFetchParams
		if err := json.Unmarshal(p.Params, &q); err != nil {
			return nil, err
		}
		return handler(ctx, p, q)
	}
	f.mu.Unlock()
	a.selected = settingsBoardID
	tt := newTester(func(c *ui.Context) {
		a.runPending()
		if w := a.workspace(a.selected); w != nil && a.displayed() == a {
			a.branchButton(c, w)
		}
	}, 600, 100)
	a.selected = "w"
	t.Cleanup(func() {
		pumpSaves(t, []*app{a}, func() bool {
			for _, view := range a.files {
				if view.repos.scanning || view.git.poll.busy || view.git.branches.loading {
					return false
				}
			}
			for _, tr := range a.track {
				if tr.poll.busy {
					return false
				}
			}
			return true
		})
	})
	return a, f, v, tt, &discoveries
}

func expireAutoFetch(a *app) {
	for key := range a.fetchAt {
		a.fetchAt[key] = time.Time{}
	}
	for key := range a.fetchRetry {
		a.fetchRetry[key] = time.Time{}
	}
}

func TestAutoFetchTargetControlAndVisibility(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, controlled := range []bool{false, true} {
			name := map[bool]string{false: "local", true: "remote"}[remote] + "/" + map[bool]string{false: "viewer", true: "controlled"}[controlled]
			t.Run(name, func(t *testing.T) {
				var calls atomic.Int32
				var got rpc.GitFetchParams
				a, _, v, tt, discoveries := autoFetchFixture(t, remote, controlled, func(_ context.Context, _ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
					calls.Add(1)
					got = q
					return fetchResultFixture(q, rpc.GitFetchSucceeded), nil
				})
				if remote {
					v.git.branches.list = []gitx.Branch{{Name: "stale-reference"}}
				}
				for _, page := range []string{settingsBoardID, discBoardID, "another-workspace"} {
					a.selected = page
					tt.Frame()
				}
				if len(a.fetchAt) != 0 || len(a.targetFetches) != 0 {
					t.Fatal("hidden workspace initiated Fetch")
				}
				a.selected = "w"
				tt.Frame()
				if !controlled {
					if calls.Load() != 0 || len(a.targetFetches) != 0 || len(a.fetchAt) != 0 {
						t.Fatal("viewer initiated Fetch")
					}
					return
				}
				until(t, tt, "automatic target Fetch", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].done })
				s := a.targetFetches[0]
				if remote {
					until(t, tt, "references refreshed", func() bool {
						return !v.git.branches.loading && len(v.git.branches.list) > 0 && v.git.branches.list[0].Name == "target-main"
					})
				}
				if calls.Load() != 1 || got.Expected != v.git.identity || got.WorkspaceID != "w" || got.WorkspacePath != v.root || !s.automatic || s.result == nil || s.result.Status != rpc.GitFetchSucceeded {
					t.Fatalf("incorrect automatic Fetch: %+v, calls=%d", s, calls.Load())
				}
				if discoveries.Load() != 0 || len(a.toasts) != 0 || a.err != "" {
					t.Fatalf("automatic Fetch rediscovered or notified: discoveries=%d, err=%s", discoveries.Load(), a.err)
				}
				at := a.fetchAt["p"]
				for range 3 {
					a.fetchRetry = nil
					tt.Frame()
				}
				if calls.Load() != 1 || a.fetchAt["p"] != at {
					t.Fatal("Fetch repeated within five minutes")
				}
				for _, page := range []string{settingsBoardID, discBoardID} {
					expireAutoFetch(a)
					a.selected = page
					tt.Frame()
				}
				if calls.Load() != 1 {
					t.Fatal("Fetch started behind another page")
				}
				if err := a.host.yield(a.host.snapshot()).release(context.Background()); err != nil {
					t.Fatal(err)
				}
				a.selected = "w"
				expireAutoFetch(a)
				tt.Frame()
				if calls.Load() != 1 || len(a.targetFetches) != 1 {
					t.Fatal("released window initiated Fetch")
				}
			})
		}
	}
}

func TestAutoFetchTargetDefersPreparation(t *testing.T) {
	for _, block := range []string{"identity", "busy", "capacity", "invalid-identity"} {
		t.Run(block, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, _, discoveries := autoFetchFixture(t, true, true, func(_ context.Context, _ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				calls.Add(1)
				return fetchResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			id := v.git.identity
			switch block {
			case "identity":
				v.git.identity = rpc.GitRepositoryIdentity{}
			case "invalid-identity":
				v.git.identity.RootInode = 0
			case "busy":
				v.git.busy = "Commit"
			case "capacity":
				for range targetFetchBytes / rpc.GitFetchResultLimit {
					a.targetFetches = append(a.targetFetches, &targetFetch{})
				}
			}
			// 只驱动自动 Fetch，避免正常仓库轮询替测试解除身份阻塞。
			tt := newTester(func(c *ui.Context) { a.runPending(); a.autoFetch(c, &a.workspaces[0], v.git.repo, &trackInfo{}) }, 100, 100)
			tt.Frame()
			retry := a.fetchRetry["p"]
			if retry.IsZero() || len(a.fetchAt) != 0 || calls.Load() != 0 || a.err != "" {
				t.Fatal("blocked preparation recorded an attempt or reported failure")
			}
			v.git.identity, v.git.busy, a.targetFetches = id, "", nil
			tt.Frame()
			if a.fetchRetry["p"] != retry || calls.Load() != 0 || len(a.fetchAt) != 0 {
				t.Fatal("preparation retried every frame")
			}
			a.fetchRetry = nil
			until(t, tt, "preparation recovered", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].done })
			if calls.Load() != 1 || discoveries.Load() != 0 || len(a.toasts) != 0 || a.err != "" {
				t.Fatal("automatic retry rediscovered, duplicated or notified")
			}
		})
	}
}

func TestAutoFetchTargetUnknownQueriesOriginal(t *testing.T) {
	var calls, queries atomic.Int32
	a, f, v, tt, _ := autoFetchFixture(t, true, true, func(_ context.Context, _ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	until(t, tt, "unknown automatic Fetch", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].unknown })
	s := a.targetFetches[0]
	for _, state := range []string{"unknown", "running", "done"} {
		f.mu.Lock()
		f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
			queries.Add(1)
			if ref != s.ref {
				t.Error("automatic query changed the original ref")
			}
			if state == "done" {
				return fetchDoneFixture(ref, fetchResultFixture(s.params, rpc.GitFetchSucceeded)), nil
			}
			return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.fetch", State: state, StartedAt: 1}, nil
		}
		f.mu.Unlock()
		before := queries.Load()
		expireAutoFetch(a)
		tt.Frame()
		until(t, tt, "original operation query", func() bool { return queries.Load() == before+1 && !s.checking })
		if calls.Load() != 1 || len(a.targetFetches) != 1 || a.confirmDo != nil || len(a.toasts) != 0 || a.err != "" {
			t.Fatal("unknown Fetch resent, confirmed or notified")
		}
		if state != "done" && (!s.pending() || v.git.busy != "Fetch") {
			t.Fatal("unresolved operation released the repository")
		}
	}
	if !s.done || v.git.busy != "" || s.result == nil {
		t.Fatal("queried completion did not release the repository")
	}
}

func TestAutoFetchTargetAcceptedSurvivesDeparture(t *testing.T) {
	for _, departure := range []string{"page", "release", "disconnect", "host"} {
		t.Run(departure, func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls, cancels atomic.Int32
			a, f, v, tt, _ := autoFetchFixture(t, true, true, func(ctx context.Context, _ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				calls.Add(1)
				close(entered)
				select {
				case <-finish:
				case <-ctx.Done():
					t.Error("view lifetime canceled accepted Fetch")
				}
				return fetchResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			f.mu.Lock()
			f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
				cancels.Add(1)
				return rpc.DesktopOperation{DesktopOperationRef: p.Operation, State: "running", Method: "git.fetch", StartedAt: 1}, nil
			}
			f.mu.Unlock()
			tt.Frame()
			waitConnectionEvent(t, entered)
			s := a.targetFetches[0]
			switch departure {
			case "page":
				a.selected = settingsBoardID
			case "release":
				if err := a.host.yield(a.host.snapshot()).release(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "disconnect":
				a.host.detach()
			case "host":
				a.views = &hostViews{local: a}
				a.views.active.Store(&app{})
			}
			expireAutoFetch(a)
			tt.Frame()
			if s.done || v.git.busy != "Fetch" || calls.Load() != 1 || cancels.Load() != 0 {
				t.Fatal("departing released, canceled or repeated accepted Fetch")
			}
			finish <- struct{}{}
			until(t, tt, "late automatic Fetch result", func() bool { return s.done })
			if calls.Load() != 1 || cancels.Load() != 0 || v.git.busy != "" || len(a.toasts) != 0 || a.err != "" {
				t.Fatal("late automatic result resent, notified or retained busy")
			}
		})
	}
}

func TestAutoFetchTargetSharedRepositoryAndQueue(t *testing.T) {
	finish := make(chan struct{})
	defer close(finish)
	var calls atomic.Int32
	a, _, v, tt, _ := autoFetchFixture(t, false, true, func(_ context.Context, _ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
		calls.Add(1)
		<-finish
		return fetchResultFixture(q, rpc.GitFetchSucceeded), nil
	})
	w := workspace{ID: "other", ProjectID: "p", Path: t.TempDir()}
	a.workspaces = append(a.workspaces, w)
	a.indexWorkspaces()
	other := &filesView{wsID: w.ID, root: w.Path}
	newGitStates(other)
	g := &gitState{v: other, repo: w.Path}
	other.git, other.repos.list, other.repos.scanned = g, []*gitState{g}, true
	a.files[w.ID] = other
	until(t, tt, "automatic Fetch running", func() bool { return calls.Load() == 1 })
	s := a.targetFetches[0]
	if g.busy != "Fetch" || v.git.busy != "Fetch" || a.fetchFor(g) != s {
		t.Fatal("same project repository was not occupied")
	}
	var queued atomic.Bool
	a.gitDoKind(appL(), g, gitFetch, "queued", func(context.Context, string) error { queued.Store(true); return nil }, nil)
	if len(g.queue) != 1 || queued.Load() {
		t.Fatal("manual Git did not queue behind automatic Fetch")
	}
	late := &filesView{wsID: "late", root: t.TempDir()}
	newGitStates(late)
	lateGit := &gitState{v: late, repo: late.root}
	late.git, late.repos.list = lateGit, []*gitState{lateGit}
	a.files[late.wsID] = late
	a.workspaces = append(a.workspaces, workspace{ID: late.wsID, ProjectID: "p", Path: late.root})
	a.indexWorkspaces()
	var lateQueued atomic.Bool
	a.gitDoKind(appL(), lateGit, gitFetch, "late queued", func(context.Context, string) error { lateQueued.Store(true); return nil }, nil)
	if len(lateGit.queue) != 1 || lateQueued.Load() || lateGit.busy != "Fetch" {
		t.Fatal("newly opened repository bypassed pending automatic Fetch")
	}
	a.selected = w.ID
	expireAutoFetch(a)
	tt.Frame()
	if calls.Load() != 1 || len(a.targetFetches) != 1 {
		t.Fatal("another workspace resent running Fetch")
	}
	// 当前成功结果的刷新仍使用原来的已发现身份；不在新工作区重新发现。
	a.fetchAt["p"] = time.Now()
	finish <- struct{}{}
	until(t, tt, "manual queue resumed", func() bool { return s.done && queued.Load() && lateQueued.Load() && g.busy == "" && lateGit.busy == "" })
	if calls.Load() != 1 || len(a.toasts) != 0 {
		t.Fatal("shared five minute interval was lost")
	}
	if fetchKey("p", fetchRel(&workspace{Path: "/one"}, "/one/nested")) != fetchKey("p", fetchRel(&workspace{Path: "/two"}, "/two/nested")) || fetchKey("p", ".") == fetchKey("p", "nested") {
		t.Fatal("relative repositories do not share the correct interval")
	}
}

func TestAutoFetchTargetFailureIsQuiet(t *testing.T) {
	for _, status := range []string{rpc.GitFetchFailed, rpc.GitFetchConflict, "rpc-error"} {
		t.Run(status, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, tt, _ := autoFetchFixture(t, true, true, func(_ context.Context, _ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				calls.Add(1)
				if status == "rpc-error" {
					return nil, rpc.Errorf(rpc.CodeInternal, "fetch failed")
				}
				return fetchResultFixture(q, status), nil
			})
			until(t, tt, "known automatic failure", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].done })
			a.fetchRetry = nil
			tt.Frame()
			if calls.Load() != 1 || a.err != "" || len(a.toasts) != 0 || v.git.busy != "" || a.confirmDo != nil {
				t.Fatal("known failure retried early, notified or retained repository")
			}
			expireAutoFetch(a)
			until(t, tt, "next interval", func() bool { return len(a.targetFetches) == 2 && a.targetFetches[1].done })
			if calls.Load() != 2 || a.err != "" || len(a.toasts) != 0 {
				t.Fatal("known failure did not wait for next interval")
			}
		})
	}
}

func TestAutoFetchTargetCancelAndWriteGuards(t *testing.T) {
	var calls, cancels atomic.Int32
	a, f, v, tt, _ := autoFetchFixture(t, true, true, func(_ context.Context, _ rpc.DesktopCall, _ rpc.GitFetchParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	until(t, tt, "unknown automatic Fetch", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].unknown })
	s := a.targetFetches[0]
	f.mu.Lock()
	f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
		cancels.Add(1)
		if p.Operation != s.ref || p.Desktop != s.ref.Desktop {
			t.Error("cancel changed target or control identity")
		}
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.fetch", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		return fetchDoneFixture(ref, fetchResultFixture(s.params, rpc.GitFetchCanceled)), nil
	}
	f.mu.Unlock()
	v.git.cancel()
	until(t, tt, "cancel accepted", func() bool { return cancels.Load() == 1 && !s.canceling })
	if s.done || !s.pending() || v.git.busy != "Fetch" {
		t.Fatal("cancellation acknowledgement released operation")
	}
	a.fetchRetry = nil
	tt.Frame()
	until(t, tt, "cancel completed", func() bool { return s.done })
	if s.result == nil || s.result.Status != rpc.GitFetchCanceled || v.git.busy != "" || calls.Load() != 1 {
		t.Fatal("cancel result lost or Fetch replayed")
	}
	for _, what := range []string{"push", "pull", "checkout"} {
		a.err = ""
		a.gitDoKind(appL(), v.git, "", what, func(context.Context, string) error { t.Error("remote write closure executed"); return nil }, nil)
		assertRemoteFileUnsupported(t, a.err, "git.write")
		if v.git.busy != "" || len(v.git.queue) != 0 {
			t.Fatal("unmigrated remote write entered queue")
		}
	}
}
