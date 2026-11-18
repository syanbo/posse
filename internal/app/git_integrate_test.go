package app

import (
	"context"
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"os"
	"path/filepath"
	"posse/internal/agent"
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

func integrateResultFixture(p rpc.GitIntegrateParams, status string) rpc.GitIntegrateResult {
	common := fetchResultFixture(p.GitFetchParams, status)
	out := rpc.GitIntegrateResult{Target: p, Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs}
	if len(out.Logs) > 0 {
		out.Logs[0].Op, out.Logs[0].Args = "integrate", rpc.GitIntegrateArgs(p)
	}
	return out
}
func integrateDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitIntegrateResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.integrate", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func integrateAppFixture(t *testing.T, controlled bool, handler func(rpc.GitIntegrateParams) (any, error)) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f, v := commitAppFixture(t, controlled, func(rpc.GitCommitParams) (any, error) { t.Error("unexpected commit"); return nil, nil })
	g := v.git
	g.status.Head = "main"
	g.branches.list = []gitx.Branch{{Name: "main", OID: g.status.Commit, Current: true}, {Name: "topic", OID: strings.Repeat("c", 40)}}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.integrate" {
			var q rpc.GitIntegrateParams
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
	return a, f, v
}
func invokeIntegrate(a *app, v *filesView, action string) {
	w := &a.workspaces[0]
	g := v.git
	switch action {
	case "merge", "rebase", "checkout-rebase":
		a.integrateBranch(appL(), w, v, g.branches.list[1], g.branches.list[0], action)
	case "cherry-pick", "revert":
		a.integrateCommits(appL(), w, v, g, action, []string{strings.Repeat("c", 40)})
	default:
		a.integrateProgress(appL(), w, v, g, action, g.status.Operation)
	}
}
func TestTargetGitIntegrateActions(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		for _, action := range []string{"merge", "rebase", "checkout-rebase", "cherry-pick", "revert", "continue", "skip", "abort"} {
			t.Run(map[bool]string{false: "viewer", true: "control"}[controlled]+"/"+action, func(t *testing.T) {
				var calls atomic.Int32
				a, _, v := integrateAppFixture(t, controlled, func(p rpc.GitIntegrateParams) (any, error) {
					calls.Add(1)
					if p.Action != action || p.Head != strings.Repeat("a", 40) || p.Current != "main" {
						t.Error("wrong inputs", p)
					}
					return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				if action == "continue" || action == "skip" || action == "abort" {
					v.git.status.Operation = "cherry-pick"
				}
				invokeIntegrate(a, v, action)
				if !controlled {
					if calls.Load() != 0 || a.err != "" || a.confirmDo != nil || len(a.targetIntegrations) != 0 {
						t.Fatal("viewer called integration", a.err)
					}
					return
				}
				if action == "abort" {
					if a.confirmDo == nil {
						t.Fatal("missing abort confirmation")
					}
					a.confirmDo()
					a.runPending()
				} else if a.confirmDo != nil {
					t.Fatal("unnecessary confirmation")
				}
				pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].done })
				if calls.Load() != 1 || a.err != "" || v.git.busy != "" {
					t.Fatal("action failed", calls.Load(), a.err)
				}
			})
		}
	}
}
func TestTargetGitIntegrateExpiredConfirmation(t *testing.T) {
	for _, action := range []string{"merge", "rebase", "checkout-rebase", "cherry-pick", "revert", "abort"} {
		for _, change := range []string{"none", "head", "branch", "repository", "workspace", "control", "operation", "disconnect"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				var calls atomic.Int32
				a, _, v := integrateAppFixture(t, true, func(p rpc.GitIntegrateParams) (any, error) {
					calls.Add(1)
					return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				a.workspaces[0].Terminals = []terminalInfo{{Agent: "codex", Running: true, AgentState: &agentState{Status: agent.StatusWorking}}}
				a.indexWorkspaces()
				if action == "abort" {
					v.git.status.Operation = "merge"
				}
				invokeIntegrate(a, v, action)
				send := a.confirmDo
				if send == nil || calls.Load() != 0 {
					t.Fatal("missing guard or confirmation")
				}
				switch change {
				case "head":
					v.git.status.Commit = strings.Repeat("d", 40)
				case "branch":
					v.git.branches.list[1].OID = strings.Repeat("d", 40)
				case "repository":
					v.git.identity.RootInode++
				case "workspace":
					a.workspaces[0].Path += "/changed"
					a.indexWorkspaces()
				case "control":
					a.host.mu.Lock()
					a.host.current.Controlled = false
					a.host.mu.Unlock()
				case "operation":
					v.git.status.Operation = "rebase"
				case "disconnect":
					a.host.detach()
					a.runPending()
				}
				send()
				a.runPending()
				allowed := change == "none" || change == "branch" && (action == "cherry-pick" || action == "revert" || action == "abort")
				if allowed {
					pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].done })
					if calls.Load() != 1 {
						t.Fatal("not sent")
					}
				} else if calls.Load() != 0 || len(a.targetIntegrations) != 0 || a.err != "" {
					t.Fatal("expired confirmation sent", a.err)
				}
			})
		}
	}
}
func TestTargetGitIntegrateUnknownCancelQuery(t *testing.T) {
	var calls, cancels atomic.Int32
	a, f, v := integrateAppFixture(t, true, func(rpc.GitIntegrateParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	invokeIntegrate(a, v, "merge")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].unknown })
	s := a.targetIntegrations[0]
	g := v.git
	params := s.params
	other := &gitState{v: v, repo: g.repo + "/other", identity: g.identity}
	other.identity.Root, other.identity.GitDir = other.repo, other.repo+"/.git"
	v.repos.list = append(v.repos.list, other)
	a.gitNext(other)
	if g.busyIntegrate != s || other.busyIntegrate != s {
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
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.integrate", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != s.ref {
			t.Error("changed original ref")
		}
		return integrateDoneFixture(ref, integrateResultFixture(params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.cancelIntegrate(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
	if s.done || g.busyIntegrate != s || cancels.Load() != 1 {
		t.Fatal("cancel receipt completed operation")
	}
	a.queryIntegrate(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return s.done })
	if calls.Load() != 1 || g.busy != "" || other.busy != "" || !reflect.DeepEqual(params, s.params) {
		t.Fatal("replayed query or changed params")
	}
}
func TestTargetGitIntegrateCompletion(t *testing.T) {
	for _, mode := range []string{"success", "failure", "disconnect", "lost-control", "closed-dialog"} {
		t.Run(mode, func(t *testing.T) {
			entered, gate := make(chan struct{}), make(chan struct{})
			defer close(gate)
			var refreshes, cancels atomic.Int32
			a, f, v := integrateAppFixture(t, true, func(p rpc.GitIntegrateParams) (any, error) {
				close(entered)
				<-gate
				status := rpc.GitFetchSucceeded
				if mode == "failure" {
					status = rpc.GitFetchFailed
				}
				return integrateResultFixture(p, status), nil
			})
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.status" {
					refreshes.Add(1)
				}
				return normal(ctx, p)
			}
			f.cancelOperation = func(rpc.DesktopOperationCancel) (any, error) { cancels.Add(1); return nil, nil }
			f.mu.Unlock()
			invokeIntegrate(a, v, "merge")
			waitConnectionEvent(t, entered)
			s := a.targetIntegrations[0]
			originalSelected := a.selected
			pop := &branchPopup{}
			a.branchPop = pop
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
				pumpSaves(t, []*app{a}, func() bool { return refreshes.Load() > 0 })
			} else if refreshes.Load() != 0 {
				t.Fatal("stale or failed completion refreshed")
			}
			if cancels.Load() != 0 || a.selected != originalSelected || a.branchPop != pop {
				t.Fatal("completion canceled or switched view")
			}
		})
	}
}
func TestTargetGitIntegrateUnknownQueueRechecks(t *testing.T) {
	var calls atomic.Int32
	a, _, v := integrateAppFixture(t, true, func(rpc.GitIntegrateParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
	})
	invokeIntegrate(a, v, "merge")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].unknown })
	s := a.targetIntegrations[0]
	g := v.git
	g.queue = append(g.queue, gitOp{ready: func() { g.status.Commit = strings.Repeat("e", 40) }})
	invokeIntegrate(a, v, "merge")
	if a.confirmDo == nil {
		t.Fatal("unknown lacked confirmation")
	}
	a.confirmDo()
	a.runPending()
	if !s.acknowledged || calls.Load() != 1 || len(g.queue) != 0 || g.busy != "" {
		t.Fatal("stale operation sent after queue")
	}
}
func TestTargetGitIntegrateBanner(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		for _, operation := range []string{"merge", "cherry-pick", "rebase", "revert"} {
			t.Run(operation+map[bool]string{true: "control", false: "viewer"}[controlled], func(t *testing.T) {
				var calls atomic.Int32
				a, _, v := integrateAppFixture(t, controlled, func(p rpc.GitIntegrateParams) (any, error) {
					calls.Add(1)
					if p.Action != "skip" {
						t.Error("conflicting continue executed")
					}
					return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				g := v.git
				g.status.Operation = operation
				g.status.Entries = []gitx.Entry{{Path: "same.txt", Conflict: true}}
				tt := newTester(func(c *ui.Context) { a.operationBanner(c, &a.workspaces[0], v, g) }, 700, 200)
				tt.Frame()
				m := locales.In("zh-Hans")
				if controlled {
					if !tt.HasText(m.CommonContinue()) || !tt.HasText(m.GitAbort()) {
						t.Fatal("missing controls", tt.Texts())
					}
					tt.Click(m.CommonContinue())
					tt.Frame()
					if calls.Load() != 0 {
						t.Fatal("conflicted continue sent")
					}
					if operation == "cherry-pick" || operation == "rebase" {
						if err := tt.Click(m.GitSkipCommit()); err != nil {
							t.Fatal(err)
						}
						tt.Frame()
						pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].done })
						if calls.Load() != 1 {
							t.Fatal("skip not sent")
						}
					}
				} else if slices.Contains(tt.Texts(), m.CommonContinue()) || slices.Contains(tt.Texts(), m.GitAbort()) || slices.Contains(tt.Texts(), m.GitSkipCommit()) || calls.Load() != 0 {
					t.Fatal("viewer controls", tt.Texts())
				}
			})
		}
	}
}
func TestPreviewTargetGitIntegrate(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, controlled := range []bool{false, true} {
			a, f, v := integrateAppFixture(t, controlled, func(p rpc.GitIntegrateParams) (any, error) {
				return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			id := v.git.identity
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.status" {
					return rpc.GitStatusResult{Identity: id, Status: &gitx.Status{Head: "main", Commit: strings.Repeat("a", 40), IndexID: strings.Repeat("b", 64), Operation: "cherry-pick", Entries: []gitx.Entry{{Path: "same.txt", X: 'U', Y: 'U', Conflict: true}}}}, nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.openFilesPanel(&a.workspaces[0], sideChanges)
			v.git.status.Operation = "cherry-pick"
			v.git.status.Entries = []gitx.Entry{{Path: "same.txt", Conflict: true}}
			tt := newPreviewTester(a, dark)
			m := locales.In("zh-Hans")
			if !tt.HasText(m.GitOperationConflicts(m.GitOpCherryPick(), 1)) {
				t.Fatal("missing operation banner", tt.Texts())
			}
			if slices.Contains(tt.Texts(), m.GitAbort()) != controlled {
				t.Fatal("wrong control visibility", tt.Texts())
			}
			savePNG(t, tt, "git-integrate-"+map[bool]string{true: "control", false: "viewer"}[controlled]+map[bool]string{true: "-dark", false: "-light"}[dark]+".png")
		}
	}
}
func TestLocalDesktopIntegrateQueue(t *testing.T) {
	for _, state := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "unknown"} {
		t.Run(state, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			gitRun(t, v.root, "config", "user.name", "Integrate test")
			gitRun(t, v.root, "config", "user.email", "integrate@example.invalid")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "initial")
			os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
			g := v.git
			g.status, _ = gitx.ReadStatus(t.Context(), v.root)
			target := a.gitTarget(&a.workspaces[0], v, g)
			p := rpc.GitIntegrateParams{GitFetchParams: target.params, Action: rpc.GitIntegrateRevert, Head: g.status.Commit, Current: g.status.Head, OIDs: []string{g.status.Commit}}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var calls, integrates atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "git.integrate" {
					return normal(ctx, p)
				}
				var q rpc.GitIntegrateParams
				json.Unmarshal(p.Params, &q)
				calls.Add(1)
				close(entered)
				<-release
				if state == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
				}
				return integrateResultFixture(q, state), nil
			}
			f.mu.Unlock()
			a.startTargetIntegrate(appL(), target, "Integrate", p, nil, nil)
			waitConnectionEvent(t, entered)
			s := a.targetIntegrations[0]
			a.gitDo(appL(), g, "Integrate", func(ctx context.Context, root string) error {
				integrates.Add(1)
				return gitx.Commit(ctx, root, "queued", false, true)
			})
			if g.busyIntegrate != s || len(g.queue) != 1 || integrates.Load() != 0 {
				t.Fatal("integrate did not hold queue")
			}
			release <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if state == "unknown" {
				if integrates.Load() != 0 || g.busyIntegrate != s {
					t.Fatal("unknown released queue")
				}
				f.mu.Lock()
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return integrateDoneFixture(ref, integrateResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.queryIntegrate(appL(), s)
			}
			pumpSaves(t, []*app{a}, func() bool { return g.busy == "" && integrates.Load() == 1 })
			if calls.Load() != 1 || len(g.queue) != 0 {
				t.Fatal("queue replayed")
			}
		})
	}
}

func TestLocalDesktopIntegrateUnknownConfirmationQueue(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	gitRun(t, v.root, "config", "user.name", "Integrate test")
	gitRun(t, v.root, "config", "user.email", "integrate@example.invalid")
	gitRun(t, v.root, "add", ".")
	gitRun(t, v.root, "commit", "-qm", "initial")
	os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	g := v.git
	g.status, _ = gitx.ReadStatus(t.Context(), v.root)
	target := a.gitTarget(&a.workspaces[0], v, g)
	p := rpc.GitIntegrateParams{GitFetchParams: target.params, Action: rpc.GitIntegrateRevert, Head: g.status.Commit, Current: g.status.Head, OIDs: []string{g.status.Commit}}
	var calls atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.integrate" {
			return normal(ctx, p)
		}
		var q rpc.GitIntegrateParams
		json.Unmarshal(p.Params, &q)
		if calls.Add(1) == 1 {
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return integrateResultFixture(q, rpc.GitFetchSucceeded), nil
	}
	f.mu.Unlock()
	a.startTargetIntegrate(appL(), target, "Integrate", p, nil, nil)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].unknown })
	old := a.targetIntegrations[0]
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.gitDo(appL(), g, "Queued Git", func(ctx context.Context, root string) error {
		close(entered)
		<-release
		_, err := gitx.ReadStatus(ctx, root)
		return err
	})
	a.startTargetIntegrate(appL(), target, "Integrate", p, nil, nil)
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
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 2 && a.targetIntegrations[1].done })
	if calls.Load() != 2 || old.ref == a.targetIntegrations[1].ref {
		t.Fatal("new operation reused original")
	}
}

func TestTargetGitIntegrateProductionMenus(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		for _, action := range []string{"merge", "rebase", "checkout-rebase", "cherry-pick", "revert"} {
			t.Run(action+map[bool]string{true: "control", false: "viewer"}[controlled], func(t *testing.T) {
				var calls atomic.Int32
				a, _, v := integrateAppFixture(t, controlled, func(p rpc.GitIntegrateParams) (any, error) {
					calls.Add(1)
					if p.Action != action {
						t.Error(p.Action)
					}
					return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				g := v.git
				w := &a.workspaces[0]
				b, cur := g.branches.list[1], g.branches.list[0]
				ci := gitx.CommitInfo{Hash: b.OID, Parents: []string{cur.OID}, Subject: "topic"}
				d := &fileDoc{log: &logDoc{root: g.repo}}
				tt := newTester(func(c *ui.Context) {
					menuButton(c, ui.Button(c, "Open"), func(menu *popMenu) {
						if action == "cherry-pick" || action == "revert" {
							a.commitMenu(c, menu, w, v, d, ci)
						} else {
							a.branchMenu(c, menu, w, v, b, cur, "", nil, false)
						}
					})
				}, 900, 600)
				tt.Frame()
				if err := tt.Click("Open"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				m := locales.In("zh-Hans")
				label := map[string]string{"merge": m.BranchMergeInto("topic", "main"), "rebase": m.BranchRebaseOnto("main", "topic"), "checkout-rebase": m.BranchCheckoutRebaseOnto("main"), "cherry-pick": m.GitCherryPick(), "revert": m.LogRevertCommit()}[action]
				if !controlled {
					if slices.Contains(tt.Texts(), label) || calls.Load() != 0 {
						t.Fatal("viewer menu contains integration", tt.Texts())
					}
					return
				}
				if err := tt.Click(label); err != nil {
					t.Fatal(label, err, tt.Texts())
				}
				tt.Frame()
				pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].done })
				if calls.Load() != 1 {
					t.Fatal("menu did not send once")
				}
			})
		}
	}
}
func TestTargetGitIntegrateCompareOrder(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{true: "control", false: "viewer"}[controlled], func(t *testing.T) {
			older, newer := strings.Repeat("c", 40), strings.Repeat("d", 40)
			var calls atomic.Int32
			a, _, v := integrateAppFixture(t, controlled, func(p rpc.GitIntegrateParams) (any, error) {
				calls.Add(1)
				if !slices.Equal(p.OIDs, []string{older, newer}) {
					t.Error("compare changed application order", p.OIDs)
				}
				return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			cd := &compareDoc{root: v.git.repo, cur: "main", other: "topic", picked: []string{newer, older}, onlyOther: []gitx.CommitInfo{{Hash: newer}, {Hash: older}}}
			tt := newTester(func(c *ui.Context) { a.comparePick(c, &a.workspaces[0], v, cd) }, 640, 100)
			tt.Frame()
			label := locales.In("zh-Hans").GitCherryPick()
			if !controlled {
				if _, ok := tt.Find(label); ok {
					t.Fatal("viewer compare offered pick")
				}
				return
			}
			if err := tt.Click(label); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].done })
			if calls.Load() != 1 {
				t.Fatal("compare did not send once")
			}
		})
	}
}

func TestTargetGitIntegrateConsole(t *testing.T) {
	a, _, v := integrateAppFixture(t, true, func(rpc.GitIntegrateParams) (any, error) { return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost") })
	invokeIntegrate(a, v, "merge")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].unknown })
	tt := newTester(func(c *ui.Context) { a.gitConsole(c, &a.workspaces[0], v) }, 800, 500)
	tt.Frame()
	m := locales.In("zh-Hans")
	if !tt.HasText(m.FetchQuery()) || !tt.HasText(m.CommonCancel()) || tt.HasText(m.FetchConsoleScope()) {
		t.Fatal("missing recovery controls", tt.Texts())
	}
}

func TestTargetGitIntegrateRediscoveryHold(t *testing.T) {
	a, _, v := integrateAppFixture(t, true, func(rpc.GitIntegrateParams) (any, error) { return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost") })
	invokeIntegrate(a, v, "merge")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].unknown })
	s, old := a.targetIntegrations[0], v.git
	v.repos.list = nil
	v.repos.scanned = false
	a.scanTargetRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	if v.git == old || v.git.busyIntegrate != s {
		t.Fatal("rediscovery lost pending operation")
	}
	a.stashPush(appL(), v, v.git)
	if a.promptDlg != nil || len(a.targetStashes) != 0 {
		t.Fatal("rediscovery allowed another write")
	}
	s.done = true
	a.releaseIntegrate(s)
	if v.git.busy != "" {
		t.Fatal("rediscovered view retained completed operation")
	}
}
func TestLocalDesktopIntegrateRediscoveryHold(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	g := v.git
	s := &targetIntegrateOperation{params: rpc.GitIntegrateParams{GitFetchParams: rpc.GitFetchParams{Expected: g.identity}}, unknown: true, what: "Merge"}
	a.targetIntegrations = append(a.targetIntegrations, s)
	a.holdIntegrate(s, g)
	v.repos.list = nil
	a.applyRepos(v, gitx.Discovery{RealPath: v.root, Repos: []gitx.Repo{{Root: g.repo, Real: g.real}}}, false, time.Time{}, map[string]gitx.RepositoryIdentity{g.repo: g.identity})
	if v.git == g || v.git.busyIntegrate != s {
		t.Fatal("local discovery lost pending operation")
	}
}
