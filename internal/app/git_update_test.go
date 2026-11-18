package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/agent"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func updateResultFixture(p rpc.GitUpdateParams, status string) rpc.GitUpdateResult {
	common := fetchResultFixture(p.GitFetchParams, status)
	out := rpc.GitUpdateResult{Target: p, Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs}
	if len(out.Logs) > 0 {
		args := []string{"pull", "--rebase", "--autostash"}
		if !p.Current {
			remote, branch, _ := strings.Cut(p.Upstream, "/")
			args = []string{"fetch", remote, branch + ":refs/heads/" + p.Branch}
		}
		out.Logs[0].Op, out.Logs[0].Args = "update", args
	}
	return out
}
func updateDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitUpdateResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.update", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func updateBranchesFixture() []gitx.Branch {
	return []gitx.Branch{{Name: "target-main", OID: strings.Repeat("b", 40), Upstream: "origin/target-main", Current: true}, {Name: "other", OID: strings.Repeat("c", 40), Upstream: "origin/other"}}
}
func updateFixture(t *testing.T, controlled bool, handler func(rpc.GitUpdateParams) (any, error)) (*app, *hostControlFixture, *filesView, *ui.Tester) {
	t.Helper()
	a, f, v, _, tt := targetBlameFixture(t)
	if controlled {
		takeRequestTestControl(t, a)
	}
	f.mu.Lock()
	normal := f.desktopCall
	id := v.git.identity
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.status":
			out, err := normal(ctx, p)
			if result, ok := out.(rpc.GitStatusResult); ok {
				result.Stashes = append(result.Stashes, gitx.Stash{Hash: strings.Repeat("d", 40), Message: "autostash"})
				out = result
			}
			return out, err
		case "git.branches":
			return rpc.GitBranchesResult{Identity: id, Branches: updateBranchesFixture()}, nil
		case "git.update":
			var q rpc.GitUpdateParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if p.Sequence == 0 {
				t.Error("missing sequence")
			}
			return handler(q)
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	w := &a.workspaces[0]
	a.fetchAt = map[string]time.Time{fetchKey(w.ProjectID, fetchRel(w, v.git.repo)): time.Now()}
	a.openBranches(w)
	until(t, tt, "update branches", func() bool {
		return len(v.git.branches.list) == 2 && v.git.branches.list[0].Upstream != "" && !v.git.branches.loading
	})
	return a, f, v, tt
}
func TestTargetGitUpdateProduction(t *testing.T) {
	for _, mode := range []string{"viewer", "current", "other", "autostash"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var target rpc.GitUpdateParams
			a, _, v, tt := updateFixture(t, mode != "viewer", func(p rpc.GitUpdateParams) (any, error) {
				calls.Add(1)
				target = p
				out := updateResultFixture(p, rpc.GitFetchSucceeded)
				out.Incoming = 2
				if mode == "autostash" {
					out.Autostash = &gitx.AutostashLeft{Stash: strings.Repeat("d", 40), Conflicts: true}
				}
				return out, nil
			})
			b := v.git.branches.list[0]
			if mode == "other" {
				b = v.git.branches.list[1]
			}
			if mode == "current" || mode == "viewer" {
				if err := tt.Click(locales.In("zh-Hans").BranchUpdateProject()); err != nil {
					t.Fatal(err)
				}
			} else if mode == "other" {
				if err := tt.Click(b.Name); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if err := tt.Click(locales.In("zh-Hans").BranchUpdate()); err != nil {
					t.Fatal(err)
				}
			} else {
				a.updateBranch(appL(), v, appL().BranchUpdate(), b)
			}
			if mode == "viewer" {
				tt.Frame()
				if calls.Load() != 0 || len(a.targetUpdates) != 0 {
					t.Fatal("viewer sent update")
				}
				return
			}
			until(t, tt, "update done", func() bool { return len(a.targetUpdates) == 1 && a.targetUpdates[0].done })
			s := a.targetUpdates[0]
			want := strings.Repeat("a", 40)
			if mode == "other" {
				want = b.OID
			}
			if calls.Load() != 1 || s.result == nil || target.BranchOID != want || target.Branch != b.Name || target.Upstream != b.Upstream || target.Current != b.Current || target.Expected != v.git.identity {
				t.Fatalf("target %+v result %+v", target, s)
			}
			if mode == "autostash" {
				if v.git.autostash != strings.Repeat("d", 40) || !v.git.autostashConflicts || v.side != sideChanges {
					t.Fatalf("old autostash presentation lost: hash=%s conflicts=%v side=%v texts=%v", v.git.autostash, v.git.autostashConflicts, v.side, tt.Texts())
				}
			} else if !tt.HasText(appL().BranchPulled(b.Name, 2)) && !tt.HasText(locales.In("zh-Hans").BranchPulled(b.Name, 2)) {
				t.Fatalf("success count lost: %v", tt.Texts())
			}
			for _, what := range []string{"Checkout", "Merge", "Rebase", "Delete"} {
				a.gitDo(appL(), v.git, what, func(context.Context, string) error { t.Error("remote closure ran"); return nil })
				assertRemoteFileUnsupported(t, a.err, "git.write")
				if len(v.git.queue) != 0 {
					t.Fatal("unsupported operation queued")
				}
			}
		})
	}
}
func TestTargetGitUpdateExpiredAgentConfirmation(t *testing.T) {
	for _, change := range []string{"oid", "head", "upstream", "control", "repository", "workspace"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, _ := updateFixture(t, true, func(p rpc.GitUpdateParams) (any, error) {
				calls.Add(1)
				return updateResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			a.workspaces[0].Terminals = []terminalInfo{{Agent: "codex", Running: true, AgentState: &agentState{Status: agent.StatusWorking}}}
			a.indexWorkspaces()
			a.updateBranch(appL(), v, appL().BranchUpdateProject(), v.git.branches.list[0])
			if a.confirmDo == nil || calls.Load() != 0 {
				t.Fatal("agent confirmation missing")
			}
			confirm := a.confirmDo
			switch change {
			case "oid":
				cp := *v.git.status
				cp.Commit = strings.Repeat("d", 40)
				v.git.status = &cp
			case "head":
				cp := *v.git.status
				cp.Head = "other"
				v.git.status = &cp
			case "upstream":
				v.git.branches.list[0].Upstream = "origin/other"
			case "control":
				takeRequestTestControl(t, a)
			case "repository":
				v.git.identity.RootInode++
			case "workspace":
				a.workspaces[0].Path += "-moved"
			}
			confirm()
			a.runPending()
			if calls.Load() != 0 || len(a.targetUpdates) != 0 {
				t.Fatal("stale confirmation sent update")
			}
		})
	}
}
func TestTargetGitUpdateUnknown(t *testing.T) {
	var calls, queries atomic.Int32
	a, f, v, tt := updateFixture(t, true, func(p rpc.GitUpdateParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	})
	b := v.git.branches.list[0]
	a.updateBranch(appL(), v, appL().BranchUpdateProject(), b)
	until(t, tt, "unknown update", func() bool { return len(a.targetUpdates) == 1 && a.targetUpdates[0].unknown })
	old := a.targetUpdates[0]
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		queries.Add(1)
		if ref != old.ref {
			t.Error("query changed ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: ref, State: "unknown"}, nil
	}
	f.mu.Unlock()
	a.queryUpdate(appL(), old)
	until(t, tt, "query original", func() bool { return !old.checking })
	if queries.Load() != 1 || calls.Load() != 1 || v.git.busyUpdate != old {
		t.Fatal("unknown resent or released")
	}
	a.updateBranch(appL(), v, appL().BranchUpdateProject(), b)
	if a.confirmDo == nil || calls.Load() != 1 {
		t.Fatal("new update did not require confirmation")
	}
	tt.Frame()
	if err := tt.Click(a.confirmVerb); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "new update", func() bool { return len(a.targetUpdates) == 2 && a.targetUpdates[1].unknown })
	next := a.targetUpdates[1]
	if next.ref == old.ref || !old.acknowledged || calls.Load() != 2 {
		t.Fatal("new reference missing")
	}
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		return updateDoneFixture(ref, updateResultFixture(old.params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.queryUpdate(appL(), old)
	until(t, tt, "old result", func() bool { return old.done })
	if !next.pending() || next.result != nil || v.git.busyUpdate != next || len(a.toasts) != 0 {
		t.Fatal("old result resolved new update")
	}
	openFetchConsole(t, a, v, tt)
	if !tt.HasText(locales.In("zh-Hans").FetchQuery()) {
		t.Fatal("closed popup cannot query update")
	}
}
func TestTargetGitUpdateDisconnectAndCancel(t *testing.T) {
	for _, cancelState := range []string{"running", "unknown", "done"} {
		t.Run(cancelState, func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls, cancels atomic.Int32
			a, f, v, tt := updateFixture(t, true, func(q rpc.GitUpdateParams) (any, error) {
				calls.Add(1)
				close(entered)
				<-finish
				return updateResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			a.updateBranch(appL(), v, appL().BranchUpdateProject(), v.git.branches.list[0])
			waitConnectionEvent(t, entered)
			a.branchPop = nil
			s := a.targetUpdates[0]
			oldClient := a.request().client
			a.host.detach()
			a.runPending()
			if calls.Load() != 1 || cancels.Load() != 0 || !s.pending() {
				t.Fatal("disconnect changed operation")
			}
			if err := a.host.connect(context.Background(), oldClient); err != nil {
				t.Fatal(err)
			}
			// 查看身份仍可查询，但不可取消；重新接管才用新的控制身份。
			f.mu.Lock()
			f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.update", State: "running", StartedAt: 1}, nil
			}
			f.mu.Unlock()
			a.cancelUpdate(appL(), s)
			if cancels.Load() != 0 {
				t.Fatal("viewer canceled")
			}
			a.queryUpdate(appL(), s)
			until(t, tt, "running queried", func() bool { return !s.checking })
			takeRequestTestControl(t, a)
			current := *a.request().client.Desktop
			if current == s.ref.Desktop {
				t.Fatal("control identity not replaced")
			}
			f.mu.Lock()
			f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
				cancels.Add(1)
				if p.Desktop != current || p.Operation != s.ref {
					t.Error("cancel borrowed old control or changed ref")
				}
				if cancelState == "unknown" {
					return rpc.DesktopOperation{DesktopOperationRef: s.ref, State: "unknown"}, nil
				}
				if cancelState == "done" {
					return updateDoneFixture(s.ref, updateResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
				return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.update", State: "running", StartedAt: 1}, nil
			}
			if cancelState == "done" {
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return updateDoneFixture(ref, updateResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
			}
			f.mu.Unlock()
			until(t, tt, "reconnected update button", func() bool { return a.targetGitReady() })
			a.updateBranch(appL(), v, appL().BranchUpdateProject(), v.git.branches.list[0])
			if calls.Load() != 1 || a.confirmDo != nil {
				t.Fatal("running operation allowed another fetch")
			}
			a.cancelUpdate(appL(), s)
			until(t, tt, "cancel reply", func() bool { return cancels.Load() == 1 && !s.canceling && !s.checking })
			if cancelState == "done" {
				if !s.done || s.result == nil || s.result.Status != rpc.GitFetchCanceled {
					t.Fatalf("cancel result: %+v", s)
				}
			} else if s.done || !s.pending() || s.unknown != (cancelState == "unknown") {
				t.Fatal("cancel acknowledgement treated as completed")
			}
			// 迟到执行响应不能把已核对的取消改成成功，也不能通知新控制代次。
			a.toasts = nil
			finish <- struct{}{}
			until(t, tt, "original response", func() bool { return s.done })
			if len(a.toasts) != 0 || calls.Load() != 1 {
				t.Fatal("late response notified or resent")
			}
		})
	}
}

func TestLocalDesktopUpdateQueuesGit(t *testing.T) {
	for _, state := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "unknown"} {
		t.Run(state, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			gitRun(t, v.root, "config", "user.name", "Update test")
			gitRun(t, v.root, "config", "user.email", "update@example.invalid")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "before update")
			gitRun(t, v.root, "remote", "add", "origin", v.root)
			gitRun(t, v.root, "config", "branch.main.remote", "origin")
			gitRun(t, v.root, "config", "branch.main.merge", "refs/heads/main")
			gitRun(t, v.root, "update-ref", "refs/remotes/origin/main", "HEAD")
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
			a.loadBranches(v.git)
			pumpSaves(t, []*app{a}, func() bool { return !v.git.branches.loading })
			var err error
			v.git.status, err = gitx.ReadStatus(t.Context(), v.root)
			if err != nil {
				t.Fatal(err)
			}
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls, commits atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "git.update" {
					return normal(ctx, p)
				}
				var q rpc.GitUpdateParams
				json.Unmarshal(p.Params, &q)
				calls.Add(1)
				close(entered)
				<-finish
				if state == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
				}
				return updateResultFixture(q, state), nil
			}
			f.mu.Unlock()
			b := v.git.branches.list[0]
			a.updateBranch(appL(), v, appL().BranchUpdateProject(), b)
			waitConnectionEvent(t, entered)
			s := a.targetUpdates[0]
			g := v.git
			a.gitDo(appL(), g, "Commit", func(ctx context.Context, root string) error {
				commits.Add(1)
				return gitx.Commit(ctx, root, "after update", false, true)
			})
			if g.busyUpdate != s || len(g.queue) != 1 || commits.Load() != 0 {
				t.Fatal("running update did not hold queue")
			}
			finish <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if state == "unknown" {
				if commits.Load() != 0 || g.busyUpdate != s || len(g.queue) != 1 {
					t.Fatal("unknown update released queue")
				}
				f.mu.Lock()
				f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
					return rpc.DesktopOperation{DesktopOperationRef: p.Operation, Method: "git.update", State: "running", StartedAt: 1}, nil
				}
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					if ref != s.ref {
						t.Error("changed original ref")
					}
					return updateDoneFixture(ref, updateResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.cancelUpdate(appL(), s)
				pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
				if s.done || commits.Load() != 0 || g.busyUpdate != s {
					t.Fatal("cancel acknowledgement released queue")
				}
				a.queryUpdate(appL(), s)
			}
			pumpSaves(t, []*app{a}, func() bool { return g.busy == "" && commits.Load() == 1 })
			if calls.Load() != 1 || len(g.queue) != 0 || g.busyUpdate != nil || gitRun(t, v.root, "log", "-1", "--format=%s") != "after update" {
				t.Fatalf("queue did not resume once: %s", a.err)
			}
		})
	}
}

func TestTargetGitUpdateDetachedAutostash(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	defer close(finish)
	a, _, v, tt := updateFixture(t, true, func(p rpc.GitUpdateParams) (any, error) {
		close(entered)
		<-finish
		out := updateResultFixture(p, rpc.GitFetchSucceeded)
		out.Autostash = &gitx.AutostashLeft{Stash: strings.Repeat("d", 40), Conflicts: true}
		return out, nil
	})
	a.updateBranch(appL(), v, appL().BranchUpdateProject(), v.git.branches.list[0])
	waitConnectionEvent(t, entered)
	s := a.targetUpdates[0]
	a.host.detach()
	a.branchPop = nil
	oldSide := v.side
	finish <- struct{}{}
	until(t, tt, "detached autostash", func() bool { return s.done })
	if s.result == nil || s.result.Autostash == nil || v.git.autostash != s.result.Autostash.Stash || !v.git.autostashConflicts || v.side != oldSide || len(a.toasts) != 0 {
		t.Fatal("detached result lost autostash or changed visible page")
	}
}
