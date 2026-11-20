package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func fetchResultFixture(p rpc.GitFetchParams, status string) rpc.GitFetchResult {
	out := rpc.GitFetchResult{Target: p, Status: status, Started: status != rpc.GitFetchConflict, Logs: []gitx.Run{}}
	code := map[string]string{rpc.GitFetchFailed: rpc.CodeInternal, rpc.GitFetchCanceled: "canceled", rpc.GitFetchTimedOut: rpc.CodeTimeout, rpc.GitFetchConflict: rpc.CodeConflict}[status]
	if code != "" {
		out.Error = rpc.Errorf(code, "target fetch failed")
	}
	if out.Started {
		log := gitx.Run{ID: 1, OpID: 1, Op: "fetch", Dir: p.Expected.Root, Args: []string{"fetch", "--all"}, Start: time.Now(), Took: time.Millisecond, Done: true, Out: "target fetch output"}
		if out.Error != nil {
			log.Err = out.Error.Message
		}
		log.Canceled = status == rpc.GitFetchCanceled
		out.Logs = []gitx.Run{log}
	}
	return out
}

func fetchDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitFetchResult) rpc.DesktopOperation {
	b, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.fetch", State: "done", StartedAt: 1, FinishedAt: 2, Result: b}
}

func fetchFixture(t *testing.T, controlled bool, handler func(rpc.DesktopCall, rpc.GitFetchParams) (any, error)) (*app, *hostControlFixture, *filesView, *fileDoc, *ui.Tester) {
	t.Helper()
	a, f, v, d, tt := targetBlameFixture(t)
	// 这里验收手动入口；自动入口单独用分支按钮和可见性测试。
	a.fetchAt = map[string]time.Time{a.workspaces[0].ProjectID: time.Now()}
	if controlled {
		takeRequestTestControl(t, a)
	}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.fetch" {
			return normal(ctx, p)
		}
		var q rpc.GitFetchParams
		if err := json.Unmarshal(p.Params, &q); err != nil {
			return nil, err
		}
		return handler(p, q)
	}
	f.mu.Unlock()
	a.openBranches(&a.workspaces[0])
	until(t, tt, "fetch branch button", func() bool { return v.repos.scanned && tt.HasText(locales.In("zh-Hans").FetchManual()) })
	return a, f, v, d, tt
}

func clickFetch(t *testing.T, tt *ui.Tester) {
	t.Helper()
	label := locales.In("zh-Hans").FetchManual()
	if !tt.HasText(label) {
		label = locales.In("zh-Hans").BranchFetchTip()
	}
	if err := tt.Click(label); err != nil {
		t.Fatal(err)
	}
}

func openFetchConsole(t *testing.T, a *app, v *filesView, tt *ui.Tester) {
	t.Helper()
	a.branchPop = nil
	a.openLog(&a.workspaces[0], "", "")
	until(t, tt, "console tab", func() bool { return tt.HasText(locales.In("zh-Hans").GitConsoleTitle()) })
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if err := tt.Click(locales.In("zh-Hans").GitConsoleTitle()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "console selected", func() bool { return v.console })
}

func TestTargetGitFetchProduction(t *testing.T) {
	for _, status := range []string{"viewer", rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict} {
		t.Run(status, func(t *testing.T) {
			var calls atomic.Int32
			var target rpc.GitFetchParams
			a, _, v, d, tt := fetchFixture(t, status != "viewer", func(p rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				calls.Add(1)
				target = q
				return fetchResultFixture(q, status), nil
			})
			d.ed.Buffer.Set("unsaved fetch draft")
			buf := d.ed.Buffer
			v.ch.message = "uncommitted message"
			clickFetch(t, tt)
			if status == "viewer" {
				a.startFetch(appL(), &a.workspaces[0], v)
				if calls.Load() != 0 || len(a.targetFetches) != 0 {
					t.Fatal("viewer sent Fetch")
				}
				return
			}
			until(t, tt, "typed fetch complete", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].done })
			s := a.targetFetches[0]
			if s.result == nil || s.result.Status != status || target.Expected != v.git.identity || target.WorkspaceID != "w" || target.WorkspacePath != a.workspaces[0].Path || calls.Load() != 1 {
				t.Fatalf("wrong target or result: %+v %s", target, s.err)
			}
			// 成功不留状态行，其余结果要说明
			if tt.HasText(locales.In("zh-Hans").FetchResult(status)) != (status != rpc.GitFetchSucceeded) {
				t.Fatal("wrong result line", tt.Texts())
			}
			if status != rpc.GitFetchSucceeded && status != rpc.GitFetchConflict && !tt.HasText(locales.In("zh-Hans").FetchPartial()) {
				t.Fatal("partial update omitted")
			}
			if d.ed.Buffer != buf || buf.String() != "unsaved fetch draft" || v.ch.message != "uncommitted message" {
				t.Fatal("fetch changed drafts")
			}
			if len(a.gitRuns) != 0 || a.consoleLines != 0 {
				t.Fatal("target logs entered local persistent history")
			}
			a.gitDoKind(appL(), v.git, gitFetch, "Fetch", func(context.Context, string) error { t.Error("remote closure ran"); return nil }, nil)
			assertRemoteFileUnsupported(t, a.err, "git.write")
			a.autoFetch(nil, &a.workspaces[0], v.git.repo, &trackInfo{})
			if calls.Load() != 1 {
				t.Fatal("automatic fetch ran")
			}
		})
	}
}

func TestHideFetchBusyOnlyWhilePending(t *testing.T) {
	a := &app{host: newHostSession(localHostID, "window")}
	g := &gitState{repo: "repo", identity: gitx.RepositoryIdentity{Root: "repo"}, busy: "Fetch", busyKind: gitFetch}
	s := &targetFetch{params: rpc.GitFetchParams{Expected: gitx.RepositoryIdentity{Root: "repo"}}}
	a.targetFetches = []*targetFetch{s}
	if !a.hideFetchBusy(g) {
		t.Fatal("in-progress fetch showed a second busy line")
	}
	g.queue = []gitOp{{}}
	if a.hideFetchBusy(g) {
		t.Fatal("queued operation hid the busy line")
	}
	g.queue = nil
	s.result, s.done = &rpc.GitFetchResult{Status: rpc.GitFetchSucceeded}, true
	if a.hideFetchBusy(g) {
		t.Fatal("completed fetch hid why the button stays disabled")
	}
	plain := &app{}
	if !plain.hideFetchBusy(g) {
		t.Fatal("legacy fetch busy line was not hidden")
	}
}

func TestTargetGitFetchUnknownAndNewOperation(t *testing.T) {
	var calls atomic.Int32
	a, f, v, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	})
	clickFetch(t, tt)
	until(t, tt, "unknown fetch", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].unknown })
	old := a.targetFetches[0]
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != old.ref {
			t.Error("query changed ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: ref, State: "unknown"}, nil
	}
	f.mu.Unlock()
	m := locales.In("zh-Hans")
	if err := tt.Click(m.FetchQuery()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "unknown queried", func() bool { return !old.checking })
	if err := tt.Click(m.FetchRefresh()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "current state refreshed", func() bool { return a.fetchWritable(v) })
	if !old.pending() || !old.unknown || calls.Load() != 1 {
		t.Fatal("read resolved or resent unknown operation")
	}
	clickFetch(t, tt)
	tt.Frame()
	if a.confirmDo == nil || calls.Load() != 1 || !tt.HasText(m.FetchNewBody()) {
		t.Fatal("new fetch missing explicit confirmation")
	}
	if err := tt.Click(m.FetchNewAction()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "new operation unknown", func() bool { return len(a.targetFetches) == 2 && a.targetFetches[1].unknown })
	next := a.targetFetches[1]
	if next.ref == old.ref || !old.acknowledged || calls.Load() != 2 {
		t.Fatal("new operation reused old ref")
	}
	// 旧操作后来查到结果，归档仍属于旧 ref，不结束后来那轮。
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		return fetchDoneFixture(ref, fetchResultFixture(old.params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.toasts = nil
	a.queryFetch(appL(), old)
	until(t, tt, "old archived", func() bool { return old.done })
	if !next.pending() || a.fetchFor(v.git) != next || next.result != nil || len(a.toasts) != 0 {
		t.Fatal("late old result affected new operation")
	}
	if ops := a.fetchConsole("p"); len(ops) != 2 || ops[0].target != old || len(ops[1].runs) != 0 {
		t.Fatal("late log assigned to new operation")
	}
}

func TestTargetGitFetchExpiredConfirmation(t *testing.T) {
	for _, change := range []string{"control", "host", "repository", "workspace"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				calls.Add(1)
				return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
			})
			clickFetch(t, tt)
			until(t, tt, "unknown", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].unknown })
			a.startFetch(appL(), &a.workspaces[0], v)
			if a.confirmDo == nil {
				t.Fatal("no confirmation")
			}
			confirm := a.confirmDo
			switch change {
			case "control":
				takeRequestTestControl(t, a)
			case "host":
				a.host.detach()
			case "repository":
				v.git.identity.RootInode++
			case "workspace":
				a.workspaces[0].Path += "-moved"
			}
			confirm()
			a.runPending()
			if calls.Load() != 1 || len(a.targetFetches) != 1 {
				t.Fatal("stale confirmation sent new operation")
			}
		})
	}
}

func TestTargetGitFetchDisconnectAndCancel(t *testing.T) {
	for _, cancelState := range []string{"running", "unknown", "done"} {
		t.Run(cancelState, func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls, cancels atomic.Int32
			a, f, v, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				calls.Add(1)
				close(entered)
				<-finish
				return fetchResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			clickFetch(t, tt)
			waitConnectionEvent(t, entered)
			s := a.targetFetches[0]
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
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.fetch", State: "running", StartedAt: 1}, nil
			}
			f.mu.Unlock()
			a.cancelFetch(appL(), s)
			if cancels.Load() != 0 {
				t.Fatal("viewer canceled")
			}
			a.queryFetch(appL(), s)
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
					return fetchDoneFixture(s.ref, fetchResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
				return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.fetch", State: "running", StartedAt: 1}, nil
			}
			if cancelState == "done" {
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return fetchDoneFixture(ref, fetchResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
			}
			f.mu.Unlock()
			until(t, tt, "reconnected fetch button", func() bool { return a.fetchWritable(v) })
			a.startFetch(appL(), &a.workspaces[0], v)
			if calls.Load() != 1 || a.confirmDo != nil {
				t.Fatal("running operation allowed another fetch")
			}
			// 进行中只有按钮转圈，没有状态行和查询按钮
			tt.Frame()
			if tt.HasText(locales.In("zh-Hans").FetchQuery()) || tt.HasText(locales.In("zh-Hans").FetchManual()) {
				t.Fatal("running fetch showed a status row or the fetch button", tt.Texts())
			}
			if err := tt.Click(locales.In("zh-Hans").BranchCancelFetch()); err != nil {
				t.Fatal(err)
			}
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

func TestTargetGitFetchConsoleAndCapacity(t *testing.T) {
	a, _, v, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
		out := fetchResultFixture(q, rpc.GitFetchSucceeded)
		out.LogsTruncated = true
		return out, nil
	})
	clickFetch(t, tt)
	until(t, tt, "fetch logs", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].done })
	s := a.targetFetches[0]
	// 目标与本机恰好用了相同 Run.ID/OpID，目标分组仍不并入本机或终端。
	a.gitRuns = []gitx.Run{{ID: 1, OpID: 1, Dir: v.root, Op: "local", Done: true, Start: time.Now()}, {ID: -1, Dir: v.root, Op: "terminal", Done: true, Start: time.Now()}}
	if ops := a.fetchConsole("p"); len(ops) != 1 || ops[0].key() == int64(1) || ops[0].key() == int64(-1) {
		t.Fatal("target IDs collide")
	}
	a.finishFetch(s, *s.result, a.request(), appL())
	if len(a.fetchConsole("p")) != 1 || len(a.gitRuns) != 2 {
		t.Fatal("target result duplicated logs")
	}
	openFetchConsole(t, a, v, tt)
	until(t, tt, "target console", func() bool { return tt.HasText("target fetch output") })
	m := locales.In("zh-Hans")
	if !tt.HasText(m.FetchConsoleScope()) || !tt.HasText(m.FetchLogsTruncated()) {
		t.Fatal("console scope missing", tt.Texts())
	}
	if _, err := os.Stat(filepath.Join(a.home, "git-console.jsonl")); err == nil {
		t.Fatal("target history persisted in local console")
	}
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if err := tt.Click(m.GitConsoleClear()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.fetchConsole("p")) != 0 {
		t.Fatalf("clear did not hide target logs: cleared=%v at=%v texts=%v", a.state.GitConsoleClearedAt, s.at, tt.Texts())
	}
	// 32 个未决操作就预留满 4MiB，不需要创建 500 次真实请求。
	a.targetFetches = nil
	for range targetFetchBytes / rpc.GitFetchResultLimit {
		a.targetFetches = append(a.targetFetches, &targetFetch{})
	}
	if a.reserveFetch() || len(a.targetFetches) != targetFetchBytes/rpc.GitFetchResultLimit {
		t.Fatal("capacity discarded unresolved refs")
	}
	a.targetFetches[0].done = true
	a.targetFetches[1].done = true
	if !a.reserveFetch() {
		t.Fatal("finished operations still reserve maximum response space")
	}
	a.targetFetches = []*targetFetch{{done: true}, {}}
	pending := a.targetFetches[1]
	if !a.reserveFetchWithin(2, targetFetchBytes) || len(a.targetFetches) != 1 || a.targetFetches[0] != pending {
		t.Fatal("count limit discarded pending ref or retained completed history")
	}
}

func TestTargetGitFetchLateResultBudget(t *testing.T) {
	a := previewApp(t)
	budget := 2*rpc.GitFetchResultLimit + rpc.GitFetchParamsLimit + 4096
	var old []*targetFetch
	for i := range 4 {
		if !a.reserveFetchWithin(500, budget) {
			t.Fatal("could not reserve next operation")
		}
		s := &targetFetch{
			ref:     rpc.DesktopOperationRef{Desktop: rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 1}, Sequence: uint64(i + 1)},
			params:  rpc.GitFetchParams{WorkspaceID: "w", WorkspacePath: "/repo", Expected: rpc.GitRepositoryIdentity{Root: "/repo", GitDir: "/repo/.git", CommonDir: "/repo/.git", RootInode: 1, GitInode: 2, CommonInode: 2}},
			unknown: true, acknowledged: true,
		}
		a.targetFetches = append(a.targetFetches, s)
		old = append(old, s)
	}
	if len(a.targetFetches) >= len(old) {
		t.Fatal("acknowledgement released the late-result reservation")
	}
	if !a.reserveFetchWithin(500, budget) {
		t.Fatal("could not reserve replacement operation")
	}
	next := &targetFetch{}
	a.targetFetches = append(a.targetFetches, next)
	for _, s := range old {
		out := fetchResultFixture(s.params, rpc.GitFetchFailed)
		text := strings.Repeat("\x01", 4<<10)
		out.Logs[0].Out, out.Logs[0].Err, out.Error.Message = text, text, text
		raw, _ := json.Marshal(out)
		if len(raw) > rpc.GitFetchResultLimit {
			t.Fatal("fixture exceeds response limit")
		}
		a.finishFetch(s, out, a.request(), appL())
		retained := slices.Contains(a.targetFetches, s)
		if retained != (s.result != nil) {
			t.Fatal("late result restored evicted history or discarded retained history")
		}
	}
	used := 0
	for _, s := range a.targetFetches {
		params, _ := json.Marshal(s.params)
		used += len(params) + len(s.err) + len(s.project) + len(s.workspace) + len(s.ref.Desktop.ClientID) + len(s.ref.Desktop.InstanceID) + 1024
		if !s.done {
			used += rpc.GitFetchResultLimit
		} else {
			result, _ := json.Marshal(s.result)
			used += len(result)
		}
	}
	if used > budget || next.done || next.result != nil {
		t.Fatalf("late results changed replacement or exceeded budget: %d > %d", used, budget)
	}
}

func TestTargetGitFetchEvictedQuery(t *testing.T) {
	a, f, _, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, _ rpc.GitFetchParams) (any, error) {
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	})
	clickFetch(t, tt)
	until(t, tt, "unknown fetch", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].unknown })
	old := a.targetFetches[0]
	old.acknowledged = true
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	var queries atomic.Int32
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		queries.Add(1)
		close(entered)
		<-release
		return fetchDoneFixture(ref, fetchResultFixture(old.params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.queryFetch(appL(), old)
	waitConnectionEvent(t, entered)
	if !a.reserveFetchWithin(1, targetFetchBytes) || len(a.targetFetches) != 0 {
		t.Fatal("acknowledged record not reclaimed")
	}
	next := &targetFetch{params: old.params}
	a.targetFetches = append(a.targetFetches, next)
	release <- struct{}{}
	until(t, tt, "evicted query returned", func() bool { return !old.checking })
	if old.result != nil || next.done || len(a.targetFetches) != 1 || a.targetFetches[0] != next {
		t.Fatal("evicted query restored history or changed next operation")
	}
	a.queryFetch(appL(), old)
	if old.checking || queries.Load() != 1 {
		t.Fatal("evicted callback started an unreserved query")
	}
}

func TestTargetGitFetchSymlinkDiscovery(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(map[bool]string{false: "workspace-root", true: "external-repository"}[nested], func(t *testing.T) {
			var got rpc.GitFetchParams
			a, f, v, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
				got = q
				return fetchResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			id := v.git.identity
			parent := t.TempDir()
			alias := filepath.Join(parent, "linked")
			if err := os.Symlink(id.Root, alias); err != nil {
				t.Fatal(err)
			}
			path, real := alias, id.Root
			if nested {
				path, real = parent, parent
			}
			a.workspaces[0].Path, v.root = path, path
			a.fetchAt[fetchKey(a.workspaces[0].ProjectID, fetchRel(&a.workspaces[0], alias))] = time.Now()
			a.indexWorkspaces()
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.discover" {
					return rpc.GitDiscoverResult{RealPath: real, Repos: []rpc.GitRepository{{Root: alias, Real: id.Root, Identity: id}}}, nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			v.repos.scanned = false
			a.scanRepos(v)
			until(t, tt, "symlink repository discovered", func() bool { return v.repos.scanned && v.git.repo == alias && v.git.real == id.Root })
			a.openBranches(&a.workspaces[0])
			tt.Frame()
			clickFetch(t, tt)
			until(t, tt, "symlink fetch", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].done })
			if got.Expected != id || got.WorkspacePath != path || v.git.repo == id.Root || a.fetchFor(v.git) != a.targetFetches[0] {
				t.Fatalf("wrong symlink target: %+v", got)
			}
		})
	}
}

func TestManualFetchLocalCompatibility(t *testing.T) {
	a := previewApp(t)
	root := gitWorkspace(t, a)
	remote := filepath.Join(t.TempDir(), "source")
	gitRun(t, root, "clone", "-q", root, remote)
	gitRun(t, remote, "branch", "fetch-only")
	gitRun(t, root, "remote", "add", "origin", remote)
	a.traceGit()
	defer func() { gitx.Trace = nil }()
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	v := openChanges(t, a, tt)
	a.openBranches(&a.workspaces[0])
	tt.Frame()
	clickFetch(t, tt)
	until(t, tt, "local fetch", func() bool { return v.git.busy == "" && len(a.gitRuns) > 0 && a.gitRuns[len(a.gitRuns)-1].Done })
	if got := gitRun(t, root, "rev-parse", "refs/remotes/origin/fetch-only"); got != gitRun(t, remote, "rev-parse", "fetch-only") {
		t.Fatal("local fetch did not update tracking ref")
	}
	if len(a.targetFetches) != 0 {
		t.Fatal("legacy local fetch used desktop operations")
	}
}

func TestPreviewTargetGitFetch(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	a, _, v, _, tt := fetchFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitFetchParams) (any, error) {
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
	})
	a.workspaces[0].Project = "remote"
	a.indexWorkspaces()
	clickFetch(t, tt)
	until(t, tt, "unknown fetch preview", func() bool { return len(a.targetFetches) == 1 && a.targetFetches[0].unknown })
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-fetch-unknown-"+name+".png")
	}
	s := a.targetFetches[0]
	s.unknown, s.err = false, ""
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-fetch-running-"+name+".png")
	}
	out := fetchResultFixture(s.params, rpc.GitFetchTimedOut)
	out.Error.Message, out.Logs[0].Err = "timed out", "timed out"
	out.LogsTruncated = true
	a.finishFetch(s, out, a.request(), appL())
	openFetchConsole(t, a, v, tt)
	until(t, tt, "fetch console preview", func() bool { return tt.HasText("target fetch output") })
	for _, dark := range []bool{false, true} {
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-fetch-console-"+name+".png")
	}
}
