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
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func historyResultFixture(p rpc.GitHistoryParams, status string) rpc.GitHistoryResult {
	common := fetchResultFixture(p.GitFetchParams, status)
	out := rpc.GitHistoryResult{Target: p.Metadata(), Status: common.Status, Started: common.Started, Error: common.Error, Logs: common.Logs}
	args := []string{"-c", "commit.cleanup=whitespace", "-c", "rebase.abbreviateCommands=false", "rebase", "-i", "--autostash", "--no-autosquash", "--root"}
	if p.Action == rpc.GitHistoryReset {
		args = []string{"reset", "--" + p.Mode, p.OID}
	}
	if p.Action == rpc.GitHistoryReword && p.OID == p.Head {
		args = []string{"commit", "--amend", "--only", "-F", "-"}
	}
	if len(out.Logs) > 0 {
		out.Logs[0].Op, out.Logs[0].Args = "history", args
	}
	return out
}
func historyAppFixture(t *testing.T, controlled bool, handler func(rpc.GitHistoryParams) (any, error)) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f, v := integrateAppFixture(t, controlled, func(rpc.GitIntegrateParams) (any, error) { t.Error("unexpected integrate"); return nil, nil })
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.history":
			var q rpc.GitHistoryParams
			json.Unmarshal(p.Params, &q)
			return handler(q)
		case "git.remotesContaining":
			var q rpc.GitRemotesContainingParams
			json.Unmarshal(p.Params, &q)
			return rpc.GitRemotesContainingResult{Identity: q.Expected, OID: q.OID, Remotes: []string{}}, nil
		case "git.commitFiles":
			var q rpc.GitCommitFilesParams
			json.Unmarshal(p.Params, &q)
			if !q.FullMessage {
				t.Error("missing body flag")
			}
			return rpc.GitCommitFilesResult{Identity: q.Expected, Commit: gitx.CommitInfo{Hash: q.Hash, Subject: "subject"}, Files: []gitx.FileChange{}, FullMessage: "subject\n\nbody"}, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	return a, f, v
}
func historyInfo(v *filesView) gitx.CommitInfo {
	return gitx.CommitInfo{Hash: v.git.status.Commit, Parents: []string{strings.Repeat("b", 40)}, Subject: "subject"}
}
func invokeHistory(a *app, v *filesView, action string) {
	mode := ""
	if action == rpc.GitHistoryReset {
		mode = "soft"
	}
	a.historyAction(appL(), &a.workspaces[0], v, v.git, historyInfo(v), action, mode)
}
func TestTargetGitHistoryActions(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		for _, action := range []string{"reset", "reword", "squash", "drop"} {
			t.Run(map[bool]string{true: "control", false: "viewer"}[controlled]+"/"+action, func(t *testing.T) {
				var calls, reads atomic.Int32
				a, f, v := historyAppFixture(t, controlled, func(p rpc.GitHistoryParams) (any, error) {
					calls.Add(1)
					if p.Action != action || p.Head != strings.Repeat("a", 40) || p.Current != "main" {
						t.Error(p)
					}
					return historyResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				f.mu.Lock()
				normal := f.desktopCall
				f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
					if p.Method == "git.remotesContaining" || p.Method == "git.commitFiles" {
						reads.Add(1)
					}
					return normal(ctx, p)
				}
				f.mu.Unlock()
				invokeHistory(a, v, action)
				if !controlled {
					if calls.Load() != 0 || reads.Load() != 0 || a.confirmDo != nil || a.promptDlg != nil || a.err != "" {
						t.Fatal("viewer acted")
					}
					return
				}
				if action == "drop" {
					pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil })
					a.confirmDo()
					a.runPending()
				}
				if action == "reword" || action == "squash" {
					pumpSaves(t, []*app{a}, func() bool { return a.promptDlg != nil || a.err != "" })
					if a.promptDlg == nil {
						t.Fatal(a.err)
					}
					a.promptDlg.do("new message", false)
				}
				pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 1 && a.targetHistories[0].done })
				if calls.Load() != 1 || a.err != "" || v.git.busy != "" {
					t.Fatal("action failed", calls.Load(), a.err)
				}
			})
		}
	}
}
func TestTargetGitHistoryExpiredConfirmation(t *testing.T) {
	for _, change := range []string{"none", "head", "branch", "identity", "control", "disconnect"} {
		t.Run(change, func(t *testing.T) {
			var calls atomic.Int32
			a, _, v := historyAppFixture(t, true, func(p rpc.GitHistoryParams) (any, error) {
				calls.Add(1)
				return historyResultFixture(p, rpc.GitFetchSucceeded), nil
			})
			a.workspaces[0].Terminals = []terminalInfo{{Agent: "codex", Running: true, AgentState: &agentState{Status: agent.StatusWorking}}}
			a.indexWorkspaces()
			invokeHistory(a, v, "reset")
			pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil })
			send := a.confirmDo
			switch change {
			case "head":
				v.git.status.Commit = strings.Repeat("c", 40)
			case "branch":
				v.git.status.Head = "other"
			case "identity":
				v.git.identity.RootInode++
			case "control":
				a.host.mu.Lock()
				a.host.current.Controlled = false
				a.host.mu.Unlock()
			case "disconnect":
				a.host.detach()
			}
			send()
			a.runPending()
			if change == "none" {
				pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 1 && a.targetHistories[0].done })
				if calls.Load() != 1 {
					t.Fatal("not sent")
				}
			} else if calls.Load() != 0 || len(a.targetHistories) != 0 {
				t.Fatal("stale confirmation sent")
			}
		})
	}
}

func historyDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitHistoryResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.history", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func TestTargetGitHistoryUnknownCancelQuery(t *testing.T) {
	var calls, cancels atomic.Int32
	a, f, v := historyAppFixture(t, true, func(rpc.GitHistoryParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	invokeHistory(a, v, "reset")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 1 && a.targetHistories[0].unknown })
	s := a.targetHistories[0]
	g := v.git
	params := s.params
	other := &gitState{v: v, repo: g.repo + "/other", identity: g.identity}
	other.identity.Root, other.identity.GitDir = other.repo, other.repo+"/.git"
	v.repos.list = append(v.repos.list, other)
	a.gitNext(other)
	if g.busyHistory != s || other.busyHistory != s {
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
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.history", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != s.ref {
			t.Error("changed original ref")
		}
		return historyDoneFixture(ref, historyResultFixture(params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.cancelHistory(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
	if s.done || g.busyHistory != s || cancels.Load() != 1 {
		t.Fatal("cancel receipt completed operation")
	}
	a.queryHistory(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return s.done })
	if calls.Load() != 1 || g.busy != "" || other.busy != "" || !reflect.DeepEqual(params, s.params) {
		t.Fatal("replayed query or changed params")
	}
}
func TestLocalDesktopHistoryUnknownConfirmationQueue(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	gitRun(t, v.root, "config", "user.name", "History test")
	gitRun(t, v.root, "config", "user.email", "history@example.invalid")
	gitRun(t, v.root, "add", ".")
	gitRun(t, v.root, "commit", "-qm", "initial")
	os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	g := v.git
	g.status, _ = gitx.ReadStatus(t.Context(), v.root)
	target := a.gitTarget(&a.workspaces[0], v, g)
	p := rpc.GitHistoryParams{GitFetchParams: target.params, Action: rpc.GitHistoryReset, Mode: "soft", OID: g.status.Commit, Head: g.status.Commit, Current: g.status.Head}
	var calls atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.history" {
			return normal(ctx, p)
		}
		var q rpc.GitHistoryParams
		json.Unmarshal(p.Params, &q)
		if calls.Add(1) == 1 {
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return historyResultFixture(q, rpc.GitFetchSucceeded), nil
	}
	f.mu.Unlock()
	a.startTargetHistory(appL(), target, "History", p, nil, nil)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 1 && a.targetHistories[0].unknown })
	old := a.targetHistories[0]
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.gitDo(appL(), g, "Queued Git", func(ctx context.Context, root string) error {
		close(entered)
		<-release
		_, err := gitx.ReadStatus(ctx, root)
		return err
	})
	a.startTargetHistory(appL(), target, "History", p, nil, nil)
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
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 2 && a.targetHistories[1].done })
	if calls.Load() != 2 || old.ref == a.targetHistories[1].ref {
		t.Fatal("new operation reused original")
	}
}
func TestTargetGitHistoryRediscoveryHold(t *testing.T) {
	a, _, v := historyAppFixture(t, true, func(rpc.GitHistoryParams) (any, error) { return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost") })
	invokeHistory(a, v, "reset")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 1 && a.targetHistories[0].unknown })
	s, old := a.targetHistories[0], v.git
	v.repos.list = nil
	v.repos.scanned = false
	a.scanTargetRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	if v.git == old || v.git.busyHistory != s {
		t.Fatal("rediscovery lost pending operation")
	}
	a.stashPush(appL(), v, v.git)
	if a.promptDlg != nil || len(a.targetStashes) != 0 {
		t.Fatal("rediscovery allowed another write")
	}
	s.done = true
	a.releaseGitHistory(s)
	if v.git.busy != "" {
		t.Fatal("rediscovered view retained completed operation")
	}
}
func TestLocalDesktopHistoryRediscoveryHold(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	g := v.git
	s := &targetHistoryOperation{params: rpc.GitHistoryParams{GitFetchParams: rpc.GitFetchParams{Expected: g.identity}}, unknown: true, what: "Merge"}
	a.targetHistories = append(a.targetHistories, s)
	a.holdHistory(s, g)
	v.repos.list = nil
	a.applyRepos(v, gitx.Discovery{RealPath: v.root, Repos: []gitx.Repo{{Root: g.repo, Real: g.real}}}, false, time.Time{}, map[string]gitx.RepositoryIdentity{g.repo: g.identity})
	if v.git == g || v.git.busyHistory != s {
		t.Fatal("local discovery lost pending operation")
	}
}

func TestTargetGitHistoryProductionMenu(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{true: "control", false: "viewer"}[controlled], func(t *testing.T) {
			a, _, v := historyAppFixture(t, controlled, func(rpc.GitHistoryParams) (any, error) { t.Error("menu sent without click"); return nil, nil })
			w := &a.workspaces[0]
			d := &fileDoc{log: &logDoc{root: v.git.repo}}
			ci := historyInfo(v)
			tt := newTester(func(c *ui.Context) {
				menuButton(c, ui.Button(c, "Open"), func(menu *popMenu) { a.commitMenu(c, menu, w, v, d, ci) })
			}, 900, 600)
			tt.Frame()
			if err := tt.Click("Open"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			for _, label := range []string{locales.In("zh-Hans").LogResetHere(), locales.In("zh-Hans").LogRewordEllipsis(), locales.In("zh-Hans").LogSquashEllipsis(), locales.In("zh-Hans").LogDropEllipsis()} {
				if tt.HasText(label) != controlled {
					t.Fatal("history menu visibility", label, tt.Texts())
				}
			}
		})
	}
}
func TestPreviewTargetHistoryAndMerge(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, controlled := range []bool{false, true} {
			a, _, v, _, tt := targetDivergenceFixture(t)
			if controlled {
				takeRequestTestControl(t, a)
			}
			waitTargetBase(t, v, tt)
			preview := newPreviewTester(a, dark)
			if preview.HasText(locales.In("zh-Hans").BranchSync()) != controlled {
				t.Fatal("wrong sync visibility")
			}
			savePNG(t, preview, "git-history-base-"+map[bool]string{true: "control", false: "viewer"}[controlled]+map[bool]string{true: "-dark", false: "-light"}[dark]+".png")
		}
	}
	a, _, v := historyAppFixture(t, true, func(rpc.GitHistoryParams) (any, error) { return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost") })
	invokeHistory(a, v, "reset")
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetHistories) == 1 && a.targetHistories[0].unknown })
	for _, dark := range []bool{false, true} {
		tt := newTester(func(c *ui.Context) { a.gitConsole(c, &a.workspaces[0], v) }, 900, 500)
		tt.SetDark(dark)
		tt.Frame()
		if !tt.HasText(locales.In("zh-Hans").FetchQuery()) || !tt.HasText(locales.In("zh-Hans").CommonCancel()) {
			t.Fatal("missing unknown controls")
		}
		savePNG(t, tt, "git-history-unknown-"+map[bool]string{true: "dark", false: "light"}[dark]+".png")
	}
}
