package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func pushResultFixture(p rpc.GitPushParams, status string) rpc.GitPushResult {
	common := fetchResultFixture(p.GitFetchParams, status)
	out := rpc.GitPushResult{Target: p, Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs, LogsTruncated: common.LogsTruncated}
	if len(out.Logs) > 0 {
		args := []string{"push"}
		if p.Force {
			args = append(args, "--force-with-lease", "--force-if-includes")
		}
		if p.Tags {
			args = append(args, "--follow-tags")
		}
		args = append(args, p.Remote, "refs/heads/"+p.Branch+":refs/heads/"+p.RemoteBranch)
		out.Logs[0].Op, out.Logs[0].Args = "push", args
	}
	return out
}
func pushDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitPushResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.push", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func pushWriteFixture(t *testing.T, controlled bool, handler func(rpc.DesktopCall, rpc.GitPushParams) (any, error)) (*app, *hostControlFixture, *filesView, *pushState, *ui.Tester) {
	t.Helper()
	a, f, v, _, tt := targetPushFixture(t, 3)
	if controlled {
		takeRequestTestControl(t, a)
	}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.push" {
			return normal(ctx, p)
		}
		var q rpc.GitPushParams
		if err := json.Unmarshal(p.Params, &q); err != nil {
			return nil, err
		}
		return handler(p, q)
	}
	f.mu.Unlock()
	ps := openTargetPushTest(t, a, tt)
	w := &a.workspaces[0]
	a.fetchAt = map[string]time.Time{fetchKey(w.ProjectID, fetchRel(w, v.git.repo)): time.Now()}
	return a, f, v, ps, tt
}
func TestTargetGitPushWriteProduction(t *testing.T) {
	for _, status := range []string{"viewer", rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "rejected"} {
		t.Run(status, func(t *testing.T) {
			var calls atomic.Int32
			var target rpc.GitPushParams
			a, _, v, ps, tt := pushWriteFixture(t, status != "viewer", func(p rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
				calls.Add(1)
				target = q
				if p.Sequence == 0 {
					t.Error("missing operation sequence")
				}
				actual := status
				if actual == "rejected" {
					actual = rpc.GitFetchFailed
				}
				out := pushResultFixture(q, actual)
				out.Rejected = status == "rejected"
				return out, nil
			})
			m := locales.In("zh-Hans")
			if !tt.HasText(m.ComparePushTitle()) || tt.HasText(m.PushPreviewTitle()) {
				t.Fatal("push dialog title changed")
			}
			if err := tt.Click(m.ComparePushTags()); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if !ps.tags {
				t.Fatal("target tags option disabled")
			}
			if err := tt.Click(m.ComparePush()); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if status == "viewer" {
				a.doPush(appL(), v, ps, false)
				a.doPush(appL(), v, ps, true)
				if calls.Load() != 0 || len(a.targetPushes) != 0 || a.confirmDo != nil {
					t.Fatal("viewer sent push")
				}
				return
			}
			until(t, tt, "push completed", func() bool { return len(a.targetPushes) == 1 && a.targetPushes[0].done })
			s := a.targetPushes[0]
			if calls.Load() != 1 || !target.Tags || !target.Matches(*ps.preview) || target.WorkspaceID != v.wsID || target.Expected != v.git.identity || s.result == nil {
				t.Fatalf("push target or result incorrect: %+v %+v", target, s)
			}
			if status == rpc.GitFetchSucceeded {
				if a.push != nil || !tt.HasText(m.ComparePushed(len(ps.preview.Plan.Commits), target.Remote+"/"+target.RemoteBranch)) {
					t.Fatalf("push success not shown: open=%v texts=%v", a.push != nil, tt.Texts())
				}
			} else {
				if s.result.Started && !tt.HasText(m.PushPartial()) {
					t.Fatal("missing partial update notice")
				}
				if status == "rejected" && s.err != m.ComparePushRejected() {
					t.Fatal("rejection explanation missing")
				}
			}
			if len(a.gitRuns) != 0 {
				t.Fatal("target logs written to local history")
			}
			for _, name := range []string{"Pull", "Checkout"} {
				a.gitDoKind(appL(), v.git, gitPush, name, func(context.Context, string) error { t.Error("remote write closure ran"); return nil }, nil)
				assertRemoteFileUnsupported(t, a.err, "git.write")
			}
		})
	}
}
func TestTargetGitPushWriteExpiredConfirmation(t *testing.T) {
	for _, change := range []string{"control", "host", "repository", "workspace", "baseline", "reload", "close", "tags"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
				calls.Add(1)
				return pushResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			if err := tt.Click(locales.In("zh-Hans").CompareForcePushEllipsis()); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if a.confirmDo == nil {
				t.Fatal("force did not require confirmation")
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
			case "baseline":
				ps.preview.Baseline = strings.Repeat("c", 64)
			case "reload":
				a.loadTargetPush(ps)
			case "close":
				a.closePush(ps)
			case "tags":
				ps.tags = !ps.tags
			}
			confirm()
			a.runPending()
			if calls.Load() != 0 || len(a.targetPushes) != 0 {
				t.Fatal("stale force confirmation pushed")
			}
		})
	}
}
func TestTargetGitPushWriteUnknown(t *testing.T) {
	var calls, queries atomic.Int32
	a, f, _, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, _ rpc.GitPushParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	})
	a.doPush(appL(), ps.view, ps, false)
	until(t, tt, "unknown push", func() bool { return len(a.targetPushes) == 1 && a.targetPushes[0].unknown })
	old := a.targetPushes[0]
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		queries.Add(1)
		if ref != old.ref {
			t.Error("query changed ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: ref, State: "unknown"}, nil
	}
	f.mu.Unlock()
	a.queryPush(appL(), old)
	until(t, tt, "queried original", func() bool { return !old.checking })
	if calls.Load() != 1 || queries.Load() != 1 {
		t.Fatal("lost reply resent push")
	}
	a.doPush(appL(), ps.view, ps, false)
	tt.Frame()
	if a.confirmDo == nil || calls.Load() != 1 {
		t.Fatal("new push was not confirmed")
	}
	if err := tt.Click(appL().PushNewAction()); err != nil {
		t.Fatalf("%v texts=%v confirm=%v", err, tt.Texts(), a.confirming)
	}
	until(t, tt, "new push", func() bool { return len(a.targetPushes) == 2 && a.targetPushes[1].unknown })
	next := a.targetPushes[1]
	if next.ref == old.ref || !old.acknowledged || calls.Load() != 2 {
		t.Fatal("new operation reused old ref")
	}
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		return pushDoneFixture(ref, pushResultFixture(old.params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.toasts = nil
	a.queryPush(appL(), old)
	until(t, tt, "old result retained", func() bool { return old.done })
	if !next.pending() || next.result != nil || len(a.toasts) != 0 || a.push != ps || ps.repository.busyPush != next {
		t.Fatal("old result completed new operation")
	}
	if len(a.pushConsole("p")) != 2 {
		t.Fatal("push history lost")
	}
}
func TestTargetGitPushWriteForce(t *testing.T) {
	var calls atomic.Int32
	a, _, _, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
		calls.Add(1)
		if !q.Force {
			t.Error("force choice lost")
		}
		out := pushResultFixture(q, rpc.GitFetchFailed)
		out.Rejected = true
		return out, nil
	})
	a.doPush(appL(), ps.view, ps, true)
	if a.confirmDo == nil || calls.Load() != 0 {
		t.Fatal("force not confirmed")
	}
	a.confirmDo()
	until(t, tt, "force rejected", func() bool { return len(a.targetPushes) == 1 && a.targetPushes[0].done })
	if calls.Load() != 1 || a.targetPushes[0].err != appL().CompareForceRejected() {
		t.Fatal("force result lost or automatically retried")
	}
}
func TestTargetGitPushWriteNotReady(t *testing.T) {
	for _, mode := range []string{"loading", "waiting", "unloaded", "resume", "missing", "error", "stale", "busy"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, ps, _ := pushWriteFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
				calls.Add(1)
				return pushResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			switch mode {
			case "busy":
				v.git.busy = "Commit"
			case "loading":
				ps.loading = true
			case "waiting":
				ps.waiting = true
			case "unloaded":
				ps.loaded = false
			case "resume":
				ps.resume = true
			case "missing":
				ps.preview = nil
			case "error":
				ps.err = "read failed"
			case "stale":
				a.viewGeneration.Add(1)
			}
			a.doPush(appL(), v, ps, false)
			if calls.Load() != 0 || len(a.targetPushes) != 0 {
				t.Fatal("unready preview pushed")
			}
		})
	}
}
func TestTargetGitPushWriteCapacity(t *testing.T) {
	a := &app{}
	for range targetFetchBytes / rpc.GitPushResultLimit {
		a.targetPushes = append(a.targetPushes, &targetPushOperation{})
	}
	if a.reservePush() {
		t.Fatal("unresolved operations evicted")
	}
	a.targetPushes[0].acknowledged = true
	a.targetPushes[1].done = true
	if !a.reservePush() {
		t.Fatal("completed records not recycled")
	}
}

func TestPreviewTargetGitPushWrite(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, _ rpc.GitPushParams) (any, error) {
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
		})
		a.workspaces[0].Project = "remote"
		a.indexWorkspaces()
		a.doPush(appL(), ps.view, ps, false)
		until(t, tt, "unknown push preview", func() bool { return len(a.targetPushes) == 1 && a.targetPushes[0].unknown })
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "target-git-push-unknown-"+name+".png")
		a.closePush(ps)
		openFetchConsole(t, a, v, tt)
		savePNG(t, newPreviewTester(a, dark), "target-git-push-console-"+name+".png")
	}
}
func TestTargetGitPushWriteDisconnectAndCancel(t *testing.T) {
	for _, cancelState := range []string{"running", "unknown", "done"} {
		t.Run(cancelState, func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls, cancels atomic.Int32
			a, f, v, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
				calls.Add(1)
				close(entered)
				<-finish
				return pushResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			a.doPush(appL(), v, ps, false)
			waitConnectionEvent(t, entered)
			s := a.targetPushes[0]
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
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.push", State: "running", StartedAt: 1}, nil
			}
			f.mu.Unlock()
			a.cancelPush(appL(), s)
			if cancels.Load() != 0 {
				t.Fatal("viewer canceled")
			}
			a.queryPush(appL(), s)
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
					return pushDoneFixture(s.ref, pushResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
				return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.push", State: "running", StartedAt: 1}, nil
			}
			if cancelState == "done" {
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return pushDoneFixture(ref, pushResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
			}
			f.mu.Unlock()
			until(t, tt, "reconnected fetch button", func() bool { return a.targetGitReady() })
			a.doPush(appL(), v, ps, false)
			if calls.Load() != 1 || a.confirmDo != nil {
				t.Fatal("running operation allowed another fetch")
			}
			if err := tt.Click(locales.In("zh-Hans").PushCancel()); err != nil {
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

func localPushWriteFixture(t *testing.T, handler func(rpc.GitPushParams) (any, error)) (*app, *hostControlFixture, *filesView, *pushState, *atomic.Int32) {
	t.Helper()
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, discovery, _ := localDesktopRepo(t, a, f)
	id := discovery.Repos[0].Identity
	previews := &atomic.Int32{}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.status":
			return rpc.GitStatusResult{Identity: id, Status: &gitx.Status{Head: "target-main", Commit: strings.Repeat("a", 40), Entries: []gitx.Entry{}}}, nil
		case "git.pushPreview":
			previews.Add(1)
			var q rpc.GitPushPreviewParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Branch != "target-main" {
				t.Error("used local HEAD instead of target status")
			}
			return rpc.GitPushPreviewResult{Identity: id, PushPreviewRead: gitx.PushPreviewRead{Plan: gitx.PushPlan{Branch: q.Branch, Remote: "origin", RemoteBranch: q.Branch, NewBranch: true, SetUpstream: true, Commits: []gitx.CommitInfo{targetLogCommit(0)}}, BranchOID: targetLogCommit(0).Hash, TrackingRef: "refs/remotes/origin/" + q.Branch, TrackingMissing: true, Baseline: strings.Repeat("b", 64)}}, nil
		case "git.push":
			var q rpc.GitPushParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Expected != id || q.Branch != "target-main" || q.WorkspacePath != v.root {
				t.Error("local push lost target identity")
			}
			return handler(q)
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	targetGitLogHandler(t, f, id, 3)
	targetGitContentHandler(t, f, id, []byte("target revision\n"), []byte("target worktree\n"))
	a.openPush(appL(), &a.workspaces[0])
	pumpSaves(t, []*app{a}, func() bool { return a.push != nil && a.push.loaded && !a.push.loading })
	ps := a.push
	if !ps.remote || !a.pushWritable(ps) {
		t.Fatalf("local preview not ready: %+v", ps)
	}
	return a, f, v, ps, previews
}

func TestLocalDesktopPushWrite(t *testing.T) {
	var pushes atomic.Int32
	a, _, v, ps, previews := localPushWriteFixture(t, func(q rpc.GitPushParams) (any, error) {
		pushes.Add(1)
		return pushResultFixture(q, rpc.GitFetchSucceeded), nil
	})
	a.selectCommit(ps.root, &ps.commit, ps.plan.Commits[0])
	pumpSaves(t, []*app{a}, func() bool { return ps.commit != nil && len(ps.commit.files) == 1 })
	a.pushShow(v, ps, 0, true)
	pumpSaves(t, []*app{a}, func() bool { return ps.detail != nil && ps.detail.kind == docDiff })
	if ps.detail.diff.state.New.String() != "target revision\n" {
		t.Fatal("local preview diff bypassed target")
	}
	a.doPush(appL(), v, ps, false)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetPushes) == 1 && a.targetPushes[0].done })
	if previews.Load() != 1 || pushes.Load() != 1 || a.targetPushes[0].result == nil || a.push != nil {
		t.Fatal("local desktop bypassed target push")
	}
}

func TestLocalDesktopPushWriteQueuesCommit(t *testing.T) {
	for _, result := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "rpc-failed", "unknown-result", "unknown-error", "unknown-new"} {
		t.Run(result, func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var pushes, committed atomic.Int32
			a, f, v, ps, _ := localPushWriteFixture(t, func(q rpc.GitPushParams) (any, error) {
				pushes.Add(1)
				close(entered)
				<-finish
				if strings.HasPrefix(result, "unknown-") {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
				}
				if result == "rpc-failed" {
					return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
				}
				return pushResultFixture(q, result), nil
			})
			gitRun(t, v.root, "config", "user.name", "Push test")
			gitRun(t, v.root, "config", "user.email", "push@example.test")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "before push")
			if err := os.WriteFile(v.root+"/document.txt", []byte("queued change\n"), 0600); err != nil {
				t.Fatal(err)
			}
			g := v.git
			a.doPush(appL(), v, ps, false)
			waitConnectionEvent(t, entered)
			s := a.targetPushes[0]
			if result == rpc.GitFetchSucceeded {
				a.closePush(ps)
			}
			a.gitDoKind(appL(), g, gitCommit, "Commit", func(ctx context.Context, root string) error {
				committed.Add(1)
				return gitx.Commit(ctx, root, "after push", true, false)
			}, nil)
			if len(g.queue) != 1 || g.busyPush != s || g.busyKind != gitPush || committed.Load() != 0 || gitRun(t, v.root, "log", "-1", "--format=%s") != "before push" {
				t.Fatal("local commit bypassed running Push")
			}
			finish <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if strings.HasPrefix(result, "unknown-") {
				if len(g.queue) != 1 || g.busyPush != s || committed.Load() != 0 {
					t.Fatal("unknown Push released local queue")
				}
				f.mu.Lock()
				f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
					return rpc.DesktopOperation{DesktopOperationRef: p.Operation, Method: "git.push", State: "running", StartedAt: 1}, nil
				}
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					if ref != s.ref {
						t.Error("queue resolution queried a different Push")
					}
					if result == "unknown-error" {
						return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.push", State: "done", StartedAt: 1, FinishedAt: 2, Error: rpc.Errorf(rpc.CodeConflict, "repository changed")}, nil
					}
					return pushDoneFixture(ref, pushResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
				f.mu.Unlock()
				a.cancelPush(appL(), s)
				pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
				if len(g.queue) != 1 || g.busyPush != s || committed.Load() != 0 || s.done {
					t.Fatal("cancel acknowledgement released local queue")
				}
				if result == "unknown-new" {
					// 再次查询仍未知，只有用户确认另开才解除旧操作的占用。
					f.mu.Lock()
					f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
						return rpc.DesktopOperation{DesktopOperationRef: ref, State: "unknown"}, nil
					}
					f.mu.Unlock()
					a.queryPush(appL(), s)
					pumpSaves(t, []*app{a}, func() bool { return !s.checking })
					a.doPush(appL(), v, ps, false)
					if a.confirmDo == nil || committed.Load() != 0 {
						t.Fatal("unknown Push was released without confirmation")
					}
					a.confirmDo()
					a.runPending()
					if !s.acknowledged || s.done {
						t.Fatal("new Push confirmation changed old outcome")
					}
				} else {
					a.queryPush(appL(), s)
				}
			}
			pumpSaves(t, []*app{a}, func() bool { return committed.Load() == 1 && g.busy == "" })
			if len(g.queue) != 0 || g.busyPush != nil || gitRun(t, v.root, "log", "-1", "--format=%s") != "after push" || pushes.Load() != 1 {
				t.Fatalf("local commit did not resume once after Push: err=%s", a.err)
			}
		})
	}
}

func TestTargetGitPushWriteEmptySuccess(t *testing.T) {
	a, _, v, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
		return pushResultFixture(q, rpc.GitFetchSucceeded), nil
	})
	ps.preview.Plan.Commits, ps.plan.Commits = nil, nil
	a.doPush(appL(), v, ps, false)
	until(t, tt, "empty push completed", func() bool { return a.push == nil })
	if !tt.HasText(appL().ComparePushedTo(ps.plan.Remote + "/" + ps.plan.RemoteBranch)) {
		t.Fatal("empty push success message changed")
	}
}

func TestTargetGitPushWriteCloseContinues(t *testing.T) {
	entered, finish := make(chan struct{}), make(chan struct{})
	defer close(finish)
	var calls atomic.Int32
	a, _, v, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, q rpc.GitPushParams) (any, error) {
		calls.Add(1)
		close(entered)
		<-finish
		return pushResultFixture(q, rpc.GitFetchSucceeded), nil
	})
	a.doPush(appL(), ps.view, ps, false)
	waitConnectionEvent(t, entered)
	s := a.targetPushes[0]
	for _, name := range []string{"Pull", "Checkout"} {
		a.gitDoKind(appL(), v.git, gitPush, name, func(context.Context, string) error { t.Error("remote write closure ran"); return nil }, nil)
		assertRemoteFileUnsupported(t, a.err, "git.write")
		if len(v.git.queue) != 0 || v.git.busyPush != s {
			t.Fatal("remote unsupported write queued behind Push")
		}
	}
	a.closePush(ps)
	tt.Frame()
	if !s.pending() || a.push != nil {
		t.Fatal("closing canceled push")
	}
	finish <- struct{}{}
	until(t, tt, "closed push finished", func() bool { return s.done })
	if calls.Load() != 1 || s.result == nil || s.result.Status != rpc.GitFetchSucceeded || a.push != nil {
		t.Fatal("closed push lost result")
	}
}

func TestTargetGitPushWriteConsole(t *testing.T) {
	a, f, v, ps, tt := pushWriteFixture(t, true, func(_ rpc.DesktopCall, _ rpc.GitPushParams) (any, error) {
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	})
	a.doPush(appL(), v, ps, false)
	until(t, tt, "unknown console operation", func() bool { return len(a.targetPushes) == 1 && a.targetPushes[0].unknown })
	s := a.targetPushes[0]
	a.closePush(ps)
	openFetchConsole(t, a, v, tt)
	m := locales.In("zh-Hans")
	if !tt.HasText(m.PushUnknown()) || !tt.HasText(m.PushCancel()) {
		t.Fatal("closed push not manageable from Console")
	}
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != s.ref {
			t.Error("Console queried different operation")
		}
		out := pushResultFixture(s.params, rpc.GitFetchFailed)
		out.Logs[0].Out = "target push output"
		return pushDoneFixture(ref, out), nil
	}
	f.mu.Unlock()
	if err := tt.Click(m.FetchQuery()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "console result", func() bool { return s.done && tt.HasText("target push output") })
	if !tt.HasText(m.PushPartial()) {
		t.Fatal("Console omitted partial update warning")
	}
	if err := tt.Click(m.GitConsoleClear()); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if len(a.pushConsole("p")) != 0 {
		t.Fatal("completed push not cleared")
	}
}
