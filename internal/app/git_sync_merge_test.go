package app

import (
	"context"
	"encoding/json"
	"posse/internal/agent"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"strings"
	"sync/atomic"
	"testing"
)

func syncMergeFixture(t *testing.T, controlled bool, handler func(rpc.GitIntegrateParams) (any, error)) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f, v := integrateAppFixture(t, controlled, handler)
	a.workspaces[0].Kind, a.workspaces[0].BaseRef, a.workspaces[0].Branch = "worktree", "base", "main"
	v.realRoot = v.git.identity.Root
	v.git.baseTrack = &targetBaseState{ref: "base", track: trackInfo{baseOK: true, base: gitx.Divergence{Head: v.git.status.Commit, Base: strings.Repeat("c", 40), Ahead: 1, Behind: 1, Conflicts: []string{"conflict"}}}}
	return a, f, v
}
func mergePrediction(v *filesView, state string) rpc.GitDivergenceResult {
	out := rpc.GitDivergenceResult{Identity: v.git.identity, BaseRef: "base", DivergenceRead: gitx.DivergenceRead{Head: strings.Repeat("a", 40), Base: strings.Repeat("c", 40), Ahead: 1, Behind: 1, Baseline: strings.Repeat("b", 64), Prediction: gitx.DivergenceCompleted, Conflicts: []string{}}}
	switch state {
	case "conflict":
		out.Conflicts = []string{"conflict"}
	case "unavailable":
		out.Prediction, out.Reason = gitx.DivergenceUnavailable, gitx.DivergenceConfig
	case "not-needed":
		out.Prediction, out.Behind = gitx.DivergenceNotNeeded, 0
	}
	return out
}
func TestTargetSyncConflictIsExpected(t *testing.T) {
	for _, rebase := range []bool{false, true} {
		t.Run(map[bool]string{true: "rebase", false: "merge"}[rebase], func(t *testing.T) {
			action := "merge"
			if rebase {
				action = "rebase"
			}
			var calls, reads atomic.Int32
			a, f, v := syncMergeFixture(t, true, func(p rpc.GitIntegrateParams) (any, error) {
				calls.Add(1)
				if p.Action != action || p.Branch != "base" || p.BranchOID != strings.Repeat("c", 40) || p.Remote {
					t.Error(p)
				}
				return integrateResultFixture(p, rpc.GitFetchFailed), nil
			})
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.status" {
					reads.Add(1)
					return rpc.GitStatusResult{Identity: v.git.identity, Status: &gitx.Status{Commit: strings.Repeat("a", 40), Head: "main", Operation: action}}, nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.syncBase(appL(), &a.workspaces[0], rebase)
			pumpSaves(t, []*app{a}, func() bool { return len(a.targetIntegrations) == 1 && a.targetIntegrations[0].stopped })
			if calls.Load() != 1 || reads.Load() == 0 || a.err != "" || a.targetIntegrations[0].err != "" {
				t.Fatal("conflict reported failed", calls.Load(), a.err)
			}
		})
	}
}
func TestTargetSyncGates(t *testing.T) {
	for _, mode := range []string{"viewer", "missing", "empty", "head", "base", "control"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v := syncMergeFixture(t, mode != "viewer", func(p rpc.GitIntegrateParams) (any, error) {
				calls.Add(1)
				return integrateResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			if mode == "missing" {
				v.git.baseTrack = nil
			}
			if mode == "empty" {
				v.git.baseTrack.track.base.Base = ""
			}
			if mode == "head" || mode == "base" || mode == "control" {
				a.workspaces[0].Terminals = []terminalInfo{{Agent: "codex", Running: true, AgentState: &agentState{Status: agent.StatusWorking}}}
				a.indexWorkspaces()
			}
			a.syncBase(appL(), &a.workspaces[0], false)
			if mode == "head" || mode == "base" || mode == "control" {
				if a.confirmDo == nil {
					t.Fatal("no agent guard")
				}
				switch mode {
				case "head":
					v.git.status.Commit = strings.Repeat("e", 40)
				case "base":
					v.git.baseTrack.track.base.Base = strings.Repeat("e", 40)
				case "control":
					a.host.mu.Lock()
					a.host.current.Controlled = false
					a.host.mu.Unlock()
				}
				a.confirmDo()
				a.runPending()
			}
			if calls.Load() != 0 || len(a.targetIntegrations) != 0 || a.err != "" {
				t.Fatal("invalid sync sent", a.err)
			}
		})
	}
}
func TestTargetWorkspaceMergePrediction(t *testing.T) {
	for _, state := range []string{"viewer", "conflict", "unavailable", "clear", "not-needed", "multi"} {
		t.Run(state, func(t *testing.T) {
			var calls, reads atomic.Int32
			a, f, v := syncMergeFixture(t, state != "viewer", func(rpc.GitIntegrateParams) (any, error) { t.Error("unexpected sync"); return nil, nil })
			if state == "multi" {
				a.workspaces[0].Repos = []wsRepo{{Path: "one"}}
			}
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "git.divergence":
					reads.Add(1)
					return mergePrediction(v, state), nil
				case "workspace.merge":
					calls.Add(1)
					return workspaceMergeResult{Merged: "main", Into: "base", Repos: []string{"one"}}, nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			if state == "multi" {
				a.askMergeRepos(appL(), v.wsID)
			} else {
				a.askMergeWorkspace(appL(), v.wsID)
			}
			if state == "viewer" {
				if calls.Load() != 0 || reads.Load() != 0 || a.confirmDo != nil || a.err != "" {
					t.Fatal("viewer acted")
				}
				return
			}
			pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil || a.err != "" })
			if state == "conflict" {
				if calls.Load() != 0 || a.confirmTitle != appL().BranchMergeConflicts() {
					t.Fatal("conflicting merge sent")
				}
				return
			}
			if state == "unavailable" {
				if calls.Load() != 0 || a.confirmDo != nil || a.err == "" {
					t.Fatal("unavailable merge sent")
				}
				return
			}
			send := a.confirmDo
			send()
			a.runPending()
			send()
			a.runPending()
			pumpSaves(t, []*app{a}, func() bool { return len(a.targetWorkspaceMerges) == 1 && a.targetWorkspaceMerges[0].done })
			if calls.Load() != 1 || state == "multi" && reads.Load() != 0 || a.err != "" {
				t.Fatal("merge count", calls.Load(), reads.Load(), a.err)
			}
		})
	}
}
func TestTargetWorkspaceMergeLostResponse(t *testing.T) {
	var calls, queries, cancels atomic.Int32
	a, f, v := syncMergeFixture(t, true, func(rpc.GitIntegrateParams) (any, error) { t.Error("unexpected sync"); return nil, nil })
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.divergence":
			return mergePrediction(v, "clear"), nil
		case "workspace.merge":
			calls.Add(1)
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.askMergeWorkspace(appL(), v.wsID)
	pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil })
	a.confirmDo()
	a.runPending()
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetWorkspaceMerges) == 1 && a.targetWorkspaceMerges[0].unknown })
	s := a.targetWorkspaceMerges[0]
	other := &gitState{v: v, repo: v.git.repo + "/other", identity: v.git.identity}
	other.identity.Root = other.repo
	v.repos.list = append(v.repos.list, other)
	a.gitNext(other)
	if other.busyWorkspaceMerge != s || v.git.busyWorkspaceMerge != s {
		t.Fatal("unknown released common-dir")
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
		if p.Desktop != current || p.Operation != s.ref {
			t.Error("wrong cancel ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "workspace.merge", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		queries.Add(1)
		if ref != s.ref {
			t.Error("changed ref")
		}
		raw, _ := json.Marshal(workspaceMergeResult{Merged: "main", Into: "base"})
		return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "workspace.merge", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}, nil
	}
	f.mu.Unlock()
	a.confirmDo = nil
	a.cancelWorkspaceMerge(s)
	pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
	if s.done || v.git.busy == "" || cancels.Load() != 1 {
		t.Fatal("receipt completed merge")
	}
	a.queryWorkspaceMerge(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return s.done })
	if calls.Load() != 1 || queries.Load() != 1 || v.git.busy != "" || other.busy != "" || a.confirmDo != nil {
		t.Fatal("query replayed merge or deletion offered to new control")
	}
}

func TestTargetWorkspaceMergeUnknownQueuesBeforePrediction(t *testing.T) {
	var calls, reads atomic.Int32
	var blocked atomic.Bool
	a, f, v := syncMergeFixture(t, true, func(rpc.GitIntegrateParams) (any, error) { t.Error("unexpected integration"); return nil, nil })
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.divergence":
			reads.Add(1)
			state := "clear"
			if blocked.Load() {
				state = "unavailable"
			}
			return mergePrediction(v, state), nil
		case "workspace.merge":
			calls.Add(1)
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.askMergeWorkspace(appL(), v.wsID)
	pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil })
	a.confirmDo()
	a.runPending()
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetWorkspaceMerges) == 1 && a.targetWorkspaceMerges[0].unknown })
	old := a.targetWorkspaceMerges[0]
	v.git.queue = append(v.git.queue, gitOp{ready: func() { blocked.Store(true) }})
	a.askMergeWorkspace(appL(), v.wsID)
	if a.confirmBody != appL().FetchNewBody() {
		t.Fatal("unknown did not require new confirmation")
	}
	a.confirmDo()
	a.runPending()
	pumpSaves(t, []*app{a}, func() bool { return a.err != "" })
	if !old.acknowledged || !blocked.Load() || reads.Load() != 2 || calls.Load() != 1 || len(v.git.queue) != 0 {
		t.Fatal("new merge overtook queue or skipped fresh prediction", calls.Load(), reads.Load())
	}
}
func TestTargetWorkspaceMergeStaleConfirmation(t *testing.T) {
	for _, mode := range []string{"control", "identity", "workspace", "disconnect", "head", "base"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			a, f, v := syncMergeFixture(t, true, func(rpc.GitIntegrateParams) (any, error) { t.Error("unexpected integration"); return nil, nil })
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.divergence" {
					return mergePrediction(v, "clear"), nil
				}
				if p.Method == "workspace.merge" {
					calls.Add(1)
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.askMergeWorkspace(appL(), v.wsID)
			pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil })
			send := a.confirmDo
			switch mode {
			case "control":
				a.host.mu.Lock()
				a.host.current.Controlled = false
				a.host.mu.Unlock()
			case "head":
				v.git.status.Commit = strings.Repeat("d", 40)
			case "base":
				v.git.baseTrack.track.base.Base = strings.Repeat("d", 40)
			case "identity":
				v.git.identity.RootInode++
			case "workspace":
				a.workspaces[0].Path += "/moved"
			case "disconnect":
				a.host.detach()
			}
			send()
			a.runPending()
			if calls.Load() != 0 || len(a.targetWorkspaceMerges) != 0 || a.err != "" {
				t.Fatal("expired merge sent", a.err)
			}
		})
	}
}
