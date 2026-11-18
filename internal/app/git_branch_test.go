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
	"posse/internal/agent"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func branchResultFixture(p rpc.GitBranchParams, status string) rpc.GitBranchResult {
	common := fetchResultFixture(p.GitFetchParams, status)
	out := rpc.GitBranchResult{Target: p, Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs}
	oid := p.BranchOID
	if p.Action == rpc.GitBranchCreate {
		oid = p.FromOID
	}
	if p.Action == rpc.GitBranchRevision {
		oid = p.RevisionOID
	}
	if p.Action == rpc.GitBranchCheckout && p.LocalOID != "" {
		oid = p.LocalOID
	}
	if status == rpc.GitFetchSucceeded {
		out.OID = oid
	}
	if len(out.Logs) > 0 {
		out.Logs[0].Op, out.Logs[0].Args = "branch", rpc.GitBranchArgs(p, oid)
	}
	return out
}
func branchDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitBranchResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.branch", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func branchFixture(t *testing.T, controlled bool, handler func(rpc.GitBranchParams) (any, error)) (*app, *hostControlFixture, *filesView, *ui.Tester) {
	t.Helper()
	a, f, v, tt := updateFixture(t, controlled, func(rpc.GitUpdateParams) (any, error) { t.Error("branch invoked update"); return nil, nil })
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.branch" {
			var q rpc.GitBranchParams
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
	return a, f, v, tt
}
func TestTargetGitBranchProduction(t *testing.T) {
	for _, mode := range []string{"viewer", "checkout", "new", "new-switch", "rename", "revision", "head-revision", "current", "worktree"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var received rpc.GitBranchParams
			a, _, v, tt := branchFixture(t, mode != "viewer", func(p rpc.GitBranchParams) (any, error) {
				calls.Add(1)
				received = p
				return branchResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			w := &a.workspaces[0]
			switch mode {
			case "viewer":
				a.err = ""
				a.checkoutBranch(appL(), w, v, v.git.branches.list[1])
				a.askNewBranch(appL(), w, v, "")
				a.askRenameBranch(appL(), w, v, v.git.branches.list[1])
				a.askWorktreeFrom(appL(), w, "other")
				a.attachWorktree(appL(), w, "/target/ext")
				a.askCheckoutRevision(appL(), w, v)
				a.checkoutCommit(appL(), w, v, v.git, strings.Repeat("c", 40))
				a.askDeleteBranch(appL(), v, v.git.branches.list[1])
				if a.promptDlg != nil || a.confirmDo != nil || a.err != "" {
					t.Fatal("viewer prepared action or error")
				}
			case "current":
				a.checkoutBranch(appL(), w, v, v.git.branches.list[0])
			case "worktree":
				other := *w
				other.ID, other.Path, other.Name = "other-workspace", "/target/other", "other"
				a.workspaces = append(a.workspaces, other)
				a.indexWorkspaces()
				b := v.git.branches.list[1]
				b.Worktree = other.Path
				a.checkoutBranch(appL(), &a.workspaces[0], v, b)
				if a.selected != other.ID {
					t.Fatal("did not switch workspace")
				}
			case "new", "new-switch":
				a.askNewBranch(appL(), w, v, "")
				if a.promptDlg == nil || calls.Load() != 0 {
					t.Fatal("prompt missing or sent early")
				}
				prompt := a.promptDlg
				a.promptDlg = nil
				prompt.do("created", mode == "new-switch")
			case "rename":
				a.askRenameBranch(appL(), w, v, v.git.branches.list[1])
				if a.promptDlg == nil || calls.Load() != 0 {
					t.Fatal("rename prompt missing or sent early")
				}
				prompt := a.promptDlg
				a.promptDlg = nil
				prompt.do("renamed", false)
			case "revision":
				a.checkoutCommit(appL(), w, v, v.git, strings.Repeat("c", 40))
			case "head-revision":
				a.askCheckoutRevision(appL(), w, v)
				prompt := a.promptDlg
				a.promptDlg = nil
				prompt.do("HEAD", false)
			default:
				a.checkoutBranch(appL(), w, v, v.git.branches.list[1])
			}
			if mode == "viewer" || mode == "current" || mode == "worktree" {
				tt.Frame()
				if calls.Load() != 0 || len(a.targetBranches) != 0 {
					t.Fatal("no-op sent")
				}
				return
			}
			until(t, tt, "branch done", func() bool { return len(a.targetBranches) == 1 && a.targetBranches[0].done })
			if calls.Load() != 1 || a.err != "" || len(a.toasts) != 0 || v.git.busy != "" {
				t.Fatalf("result %s calls=%d", a.err, calls.Load())
			}
			if received.Expected != v.git.identity || received.WorkspaceID != v.wsID || received.Action == rpc.GitBranchCreate && received.FromOID != strings.Repeat("a", 40) || mode == "head-revision" && received.RevisionOID != strings.Repeat("a", 40) {
				t.Fatalf("target %+v", received)
			}
		})
	}
}
func TestTargetGitBranchExpiredConfirmation(t *testing.T) {
	for _, action := range []string{"checkout", "new", "revision", "delete-remote"} {
		for _, change := range []string{"oid", "control", "repository", "workspace"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				var calls atomic.Int32
				a, _, v, _ := branchFixture(t, true, func(p rpc.GitBranchParams) (any, error) {
					calls.Add(1)
					return branchResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				a.workspaces[0].Terminals = []terminalInfo{{Agent: "codex", Running: true, AgentState: &agentState{Status: agent.StatusWorking}}}
				a.indexWorkspaces()
				w := &a.workspaces[0]
				b := v.git.branches.list[1]
				switch action {
				case "checkout":
					a.checkoutBranch(appL(), w, v, b)
				case "new":
					a.askNewBranch(appL(), w, v, "")
					p := a.promptDlg
					a.promptDlg = nil
					p.do("created", true)
				case "revision":
					a.askCheckoutRevision(appL(), w, v)
					p := a.promptDlg
					a.promptDlg = nil
					p.do("other", false)
				case "delete-remote":
					b.Remote, b.Name = true, "origin/other"
					v.git.branches.list = append(v.git.branches.list, b)
					a.askDeleteBranch(appL(), v, b)
				}
				if a.confirmDo == nil || calls.Load() != 0 {
					t.Fatal("confirmation missing")
				}
				confirm := a.confirmDo
				switch change {
				case "oid":
					if action == "new" {
						cp := *v.git.status
						cp.Commit = strings.Repeat("d", 40)
						v.git.status = &cp
					} else {
						v.git.branches.list[len(v.git.branches.list)-1].OID = strings.Repeat("d", 40)
					}
				case "control":
					takeRequestTestControl(t, a)
				case "repository":
					v.git.identity.RootInode++
				case "workspace":
					a.workspaces[0].Path += "-changed"
				}
				confirm()
				a.runPending()
				if calls.Load() != 0 || len(a.targetBranches) != 0 {
					t.Fatal("stale confirmation sent")
				}
			})
		}
	}
}
func TestTargetGitBranchDeleteConfirmationRestore(t *testing.T) {
	var calls atomic.Int32
	a, _, v, tt := branchFixture(t, true, func(p rpc.GitBranchParams) (any, error) {
		n := calls.Add(1)
		out := branchResultFixture(p, rpc.GitFetchSucceeded)
		switch n {
		case 1:
			if p.Action != rpc.GitBranchDelete || p.Force {
				t.Error("initial delete")
			}
			out.Unmerged, out.Started, out.OID = true, false, ""
			out.Logs[0].Err = "branch not fully merged"
		case 2:
			if !p.Force || p.BranchOID != strings.Repeat("c", 40) {
				t.Error("force lost oid")
			}
		case 3:
			if p.Action != rpc.GitBranchCreate || p.Checkout || p.FromOID != strings.Repeat("c", 40) {
				t.Error("restore lost oid")
			}
		}
		return out, nil
	})
	a.askDeleteBranch(appL(), v, v.git.branches.list[1])
	until(t, tt, "unmerged", func() bool { return a.confirmDo != nil })
	if a.err != "" || len(a.toasts) != 0 || !a.targetBranches[0].result.Unmerged || a.targetBranches[0].result.Started {
		t.Fatal("unmerged reported failure")
	}
	a.confirming = false
	a.confirmDo()
	a.runPending()
	until(t, tt, "delete toast", func() bool { return tt.HasText(appL().BranchDeleted("other")) })
	if calls.Load() != 2 {
		t.Fatal("delete count")
	}
	if err := tt.Click(appL().CommonRestore()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "restore", func() bool { return len(a.targetBranches) == 3 && a.targetBranches[2].done })
	if calls.Load() != 3 || a.err != "" {
		t.Fatal("restore did not finish", a.err)
	}
}

func TestTargetGitBranchUnknown(t *testing.T) {
	var calls, queries atomic.Int32
	a, f, v, tt := branchFixture(t, true, func(p rpc.GitBranchParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	})
	b := v.git.branches.list[1]
	a.checkoutBranch(appL(), &a.workspaces[0], v, b)
	until(t, tt, "unknown branch", func() bool { return len(a.targetBranches) == 1 && a.targetBranches[0].unknown })
	old := a.targetBranches[0]
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		queries.Add(1)
		if ref != old.ref {
			t.Error("query changed ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: ref, State: "unknown"}, nil
	}
	f.mu.Unlock()
	a.queryBranch(appL(), old)
	until(t, tt, "query original", func() bool { return !old.checking })
	if queries.Load() != 1 || calls.Load() != 1 || v.git.busyBranch != old {
		t.Fatal("unknown resent or released")
	}
	a.checkoutBranch(appL(), &a.workspaces[0], v, b)
	if a.confirmDo == nil || calls.Load() != 1 {
		t.Fatal("new branch did not require confirmation")
	}
	tt.Frame()
	if err := tt.Click(a.confirmVerb); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "new branch", func() bool { return len(a.targetBranches) == 2 && a.targetBranches[1].unknown })
	next := a.targetBranches[1]
	if next.ref == old.ref || !old.acknowledged || calls.Load() != 2 {
		t.Fatal("new reference missing")
	}
	f.mu.Lock()
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		return branchDoneFixture(ref, branchResultFixture(old.params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.queryBranch(appL(), old)
	until(t, tt, "old result", func() bool { return old.done })
	if !next.pending() || next.result != nil || v.git.busyBranch != next || len(a.toasts) != 0 {
		t.Fatal("old result resolved new branch")
	}
	openFetchConsole(t, a, v, tt)
	if !tt.HasText(locales.In("zh-Hans").FetchQuery()) {
		t.Fatal("closed popup cannot query branch")
	}
}
func TestTargetGitBranchDisconnectAndCancel(t *testing.T) {
	for _, cancelState := range []string{"running", "unknown", "done"} {
		t.Run(cancelState, func(t *testing.T) {
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls, cancels atomic.Int32
			a, f, v, tt := branchFixture(t, true, func(q rpc.GitBranchParams) (any, error) {
				calls.Add(1)
				close(entered)
				<-finish
				return branchResultFixture(q, rpc.GitFetchSucceeded), nil
			})
			a.checkoutBranch(appL(), &a.workspaces[0], v, v.git.branches.list[1])
			waitConnectionEvent(t, entered)
			a.branchPop = nil
			s := a.targetBranches[0]
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
				return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.branch", State: "running", StartedAt: 1}, nil
			}
			f.mu.Unlock()
			a.cancelBranch(appL(), s)
			if cancels.Load() != 0 {
				t.Fatal("viewer canceled")
			}
			a.queryBranch(appL(), s)
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
					return branchDoneFixture(s.ref, branchResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
				return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.branch", State: "running", StartedAt: 1}, nil
			}
			if cancelState == "done" {
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return branchDoneFixture(ref, branchResultFixture(s.params, rpc.GitFetchCanceled)), nil
				}
			}
			f.mu.Unlock()
			until(t, tt, "reconnected branch button", func() bool { return a.targetGitReady() })
			a.checkoutBranch(appL(), &a.workspaces[0], v, v.git.branches.list[1])
			if calls.Load() != 1 || a.confirmDo != nil {
				t.Fatal("running operation allowed another fetch")
			}
			a.cancelBranch(appL(), s)
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

func TestLocalDesktopBranchQueuesGit(t *testing.T) {
	for _, state := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "unknown"} {
		t.Run(state, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			gitRun(t, v.root, "config", "user.name", "Branch test")
			gitRun(t, v.root, "config", "user.email", "update@example.invalid")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "before branch")
			gitRun(t, v.root, "remote", "add", "origin", v.root)
			gitRun(t, v.root, "config", "branch.main.remote", "origin")
			gitRun(t, v.root, "config", "branch.main.merge", "refs/heads/main")
			gitRun(t, v.root, "update-ref", "refs/remotes/origin/main", "HEAD")
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
			gitRun(t, v.root, "branch", "other")
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
				if p.Method != "git.branch" {
					return normal(ctx, p)
				}
				var q rpc.GitBranchParams
				json.Unmarshal(p.Params, &q)
				calls.Add(1)
				close(entered)
				<-finish
				if state == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
				}
				return branchResultFixture(q, state), nil
			}
			f.mu.Unlock()
			b := v.git.branches.list[1]
			a.checkoutBranch(appL(), &a.workspaces[0], v, b)
			waitConnectionEvent(t, entered)
			s := a.targetBranches[0]
			g := v.git
			a.gitDo(appL(), g, "Commit", func(ctx context.Context, root string) error {
				commits.Add(1)
				return gitx.Commit(ctx, root, "after branch", false, true)
			})
			if g.busyBranch != s || len(g.queue) != 1 || commits.Load() != 0 {
				t.Fatal("running branch did not hold queue")
			}
			finish <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if state == "unknown" {
				if commits.Load() != 0 || g.busyBranch != s || len(g.queue) != 1 {
					t.Fatal("unknown branch released queue")
				}
				f.mu.Lock()
				f.cancelOperation = func(p rpc.DesktopOperationCancel) (any, error) {
					return rpc.DesktopOperation{DesktopOperationRef: p.Operation, Method: "git.branch", State: "running", StartedAt: 1}, nil
				}
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					if ref != s.ref {
						t.Error("changed original ref")
					}
					return branchDoneFixture(ref, branchResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.cancelBranch(appL(), s)
				pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
				if s.done || commits.Load() != 0 || g.busyBranch != s {
					t.Fatal("cancel acknowledgement released queue")
				}
				a.queryBranch(appL(), s)
			}
			pumpSaves(t, []*app{a}, func() bool { return g.busy == "" && commits.Load() == 1 })
			if calls.Load() != 1 || len(g.queue) != 0 || g.busyBranch != nil || gitRun(t, v.root, "log", "-1", "--format=%s") != "after branch" {
				t.Fatalf("queue did not resume once: %s", a.err)
			}
		})
	}
}

func TestPreviewTargetGitBranch(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, mode := range []string{"viewer", "controlled", "unknown"} {
			t.Run(mode+map[bool]string{false: "-light", true: "-dark"}[dark], func(t *testing.T) {
				a, _, v, tt := branchFixture(t, mode != "viewer", func(p rpc.GitBranchParams) (any, error) {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
				})
				if mode == "unknown" {
					target := a.gitTarget(&a.workspaces[0], v, v.git)
					s := &targetBranchOperation{target: target, params: branchParams(target, v.git.branches.list[1], rpc.GitBranchCheckout), project: target.project, workspace: target.workspace, request: target.request, unknown: true, what: appL().BranchCheckout()}
					s.at = time.Now()
					s.ref = rpc.DesktopOperationRef{Desktop: *a.request().client.Desktop, Sequence: 1}
					a.targetBranches = append(a.targetBranches, s)
					a.holdBranch(s, v.git)
					openFetchConsole(t, a, v, tt)
				}
				savePNG(t, newPreviewTester(a, dark), "git-branch-"+mode+map[bool]string{false: "-light", true: "-dark"}[dark]+".png")
			})
		}
	}
}

func TestTargetGitBranchKeyboardAndMenus(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "viewer", true: "controlled"}[controlled], func(t *testing.T) {
			var calls atomic.Int32
			a, _, v, tt := branchFixture(t, controlled, func(p rpc.GitBranchParams) (any, error) {
				calls.Add(1)
				return branchResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			tt.Frame()
			if tt.HasText(locales.In("zh-Hans").BranchNewEllipsis()) != controlled || tt.HasText(locales.In("zh-Hans").BranchCheckoutRevisionEllipsis()) != controlled {
				t.Fatalf("wrong action visibility controlled=%v writable=%v texts=%v", controlled, a.branchControls(), tt.Texts())
			}
			if err := tt.Click("other"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if tt.HasText(locales.In("zh-Hans").BranchCheckout()) != controlled || tt.HasText(locales.In("zh-Hans").BranchNewFrom("other")) != controlled || tt.HasText(locales.In("zh-Hans").BranchRenameEllipsis()) != controlled || tt.HasText(locales.In("zh-Hans").BranchNewWorkspaceFrom("other")) != controlled || tt.HasText(locales.In("zh-Hans").CommonDelete()) != controlled {
				t.Fatal("wrong menu visibility")
			}
			if !tt.HasText(locales.In("zh-Hans").BranchShowInLog()) {
				t.Fatal("missing show in log")
			}
			for _, bad := range []string{locales.In("zh-Hans").BranchRevealWorktree(), locales.In("zh-Hans").BranchCopyWorktreePath()} {
				if tt.HasText(bad) {
					t.Fatal("unsupported menu", bad)
				}
			}
			for _, label := range []string{locales.In("zh-Hans").BranchCheckoutRebaseOnto("target-main"), locales.In("zh-Hans").BranchMergeInto("other", "target-main"), locales.In("zh-Hans").BranchRebaseOnto("target-main", "other")} {
				if tt.HasText(label) != controlled {
					t.Fatal("integration visibility does not match control", label)
				}
			}
			tt.Key(0, ui.KeyEscape)
			tt.Frame()
			a.openBranches(&a.workspaces[0])
			tt.Frame()
			if err := tt.Click(locales.In("zh-Hans").BranchSearch()); err != nil {
				t.Fatal(err)
			}
			tt.Type("other")
			tt.Frame()
			tt.Key(0, ui.KeyEnter)
			tt.Frame()
			if controlled {
				until(t, tt, "keyboard checkout", func() bool { return len(a.targetBranches) == 1 && a.targetBranches[0].done })
			}
			if calls.Load() != map[bool]int32{false: 0, true: 1}[controlled] || a.err != "" {
				t.Fatal("keyboard routing", a.err)
			}
			_ = v
		})
	}
}

func TestLocalDesktopBranchNewWaitsForQueue(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	gitRun(t, v.root, "config", "user.name", "Branch test")
	gitRun(t, v.root, "config", "user.email", "branch@example.invalid")
	gitRun(t, v.root, "add", ".")
	gitRun(t, v.root, "commit", "-qm", "before branch")
	gitRun(t, v.root, "branch", "other")
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	a.loadBranches(v.git)
	pumpSaves(t, []*app{a}, func() bool { return !v.git.branches.loading })
	var calls atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.branch" {
			return normal(ctx, p)
		}
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "response lost")
	}
	f.mu.Unlock()
	b := gitxBranch(v, "other")
	a.checkoutBranch(appL(), &a.workspaces[0], v, b)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetBranches) == 1 && a.targetBranches[0].unknown })
	entered, finish := make(chan struct{}), make(chan struct{})
	defer close(finish)
	a.gitDo(appL(), v.git, "Commit", func(ctx context.Context, root string) error {
		close(entered)
		<-finish
		return gitx.Commit(ctx, root, "queued first", false, true)
	})
	a.checkoutBranch(appL(), &a.workspaces[0], v, b)
	if a.confirmDo == nil {
		t.Fatal("new operation did not ask")
	}
	a.confirming = false
	a.confirmDo()
	a.runPending()
	waitConnectionEvent(t, entered)
	if calls.Load() != 1 || !a.targetBranches[0].acknowledged || len(v.git.queue) != 1 {
		t.Fatal("new branch jumped local queue")
	}
	finish <- struct{}{}
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetBranches) == 2 && a.targetBranches[1].unknown })
	if calls.Load() != 2 || gitRun(t, v.root, "log", "-1", "--format=%s") != "queued first" {
		t.Fatal("queued operation did not finish first")
	}
}

func TestLegacyBranchActions(t *testing.T) {
	a := previewApp(t)
	root := branchWorkspace(t, a)
	w := &a.workspaces[0]
	w.Terminals = nil
	a.indexWorkspaces()
	tt := newTester(a.build, 1200, 760)
	a.openBranches(w)
	v := a.files[w.ID]
	until(t, tt, "legacy branches", func() bool { return v.git.branches.list != nil })
	if a.typedFetch() {
		t.Fatal("fixture has desktop identity")
	}
	a.checkoutBranch(appL(), w, v, gitxBranch(v, "feat"))
	until(t, tt, "legacy checkout", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--show-current") == "feat" })
	oid := gitRun(t, root, "rev-parse", "HEAD")
	a.checkoutCommit(appL(), w, v, v.git, oid)
	until(t, tt, "legacy revision", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--show-current") == "" })
	a.askNewBranch(appL(), w, v, oid)
	prompt := a.promptDlg
	a.promptDlg = nil
	prompt.do("legacy-created", false)
	until(t, tt, "legacy create", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--list", "legacy-created") != "" })
	a.loadBranches(v.git)
	until(t, tt, "created listed", func() bool { return gitxBranch(v, "legacy-created").Name != "" })
	a.askDeleteBranch(appL(), v, gitxBranch(v, "legacy-created"))
	until(t, tt, "legacy deleted", func() bool { return v.git.busy == "" && gitRun(t, root, "branch", "--list", "legacy-created") == "" })
	if len(a.targetBranches) != 0 {
		t.Fatal("legacy branch used RPC")
	}
}
