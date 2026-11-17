package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func targetDivergenceFixture(t *testing.T) (*app, *hostControlFixture, *filesView, *fileDoc, *ui.Tester) {
	t.Helper()
	a, f, v, d, tt := targetBlameFixture(t)
	a.workspaces[0].Kind, a.workspaces[0].BaseRef = "worktree", "base-main"
	id := v.git.identity
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.divergence" {
			return normal(ctx, p)
		}
		var q rpc.GitDivergenceParams
		if err := json.Unmarshal(p.Params, &q); err != nil {
			return nil, err
		}
		if q.Path != id.Root || q.Expected != id {
			return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
		}
		out := rpc.GitDivergenceResult{Identity: id, BaseRef: q.BaseRef, DivergenceRead: gitx.DivergenceRead{
			Head: targetLogCommit(0).Hash, Base: targetLogCommit(2000).Hash, Ahead: 1, Behind: 2,
			Prediction: gitx.DivergenceCompleted, Conflicts: []string{"src/conflict.go"}, Baseline: strings.Repeat("b", 64),
		}}
		if q.PreviousBaseline == out.Baseline {
			out.DivergenceRead = gitx.DivergenceRead{Head: out.Head, Base: out.Base, Baseline: out.Baseline, Unchanged: true}
		}
		return out, nil
	}
	f.mu.Unlock()
	a.openFilesPanel(&a.workspaces[0], sideChanges)
	return a, f, v, d, tt
}

func waitTargetBase(t *testing.T, v *filesView, tt *ui.Tester) *targetBaseState {
	t.Helper()
	until(t, tt, "target base comparison", func() bool {
		return v.git.baseTrack != nil && v.git.baseTrack.track.baseOK && !v.git.baseTrack.track.poll.busy
	})
	s := v.git.baseTrack
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if s.track.baseErr != "" {
		t.Fatal(s.track.baseErr)
	}
	return s
}

func TestTargetGitDivergenceProduction(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "viewer", true: "controlled"}[controlled], func(t *testing.T) {
			a, _, v, d, tt := targetDivergenceFixture(t)
			if controlled {
				takeRequestTestControl(t, a)
			}
			d.ed.Buffer.Set("unsaved base draft")
			buffer := d.ed.Buffer
			v.ch.message = "unsent commit message"
			s := waitTargetBase(t, v, tt)
			m := locales.In("zh-Hans")
			if s.track.ahead != 2 || s.track.behind != 1 || s.track.base.Ahead != 1 || s.track.base.Behind != 2 {
				t.Fatal("upstream and base counts mixed")
			}
			for _, want := range []string{m.BranchRelativeTo("base-main"), m.BranchConflictCount(1), m.BranchSync(), m.GitMerge()} {
				if !tt.HasText(want) {
					t.Fatal("missing base row", want, tt.Texts())
				}
			}
			for _, label := range []string{m.BranchSync(), m.GitMerge()} {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if a.confirmDo != nil || a.confirming || v.git.busy != "" {
					t.Fatal("read-only button prepared write")
				}
			}
			for _, run := range []func(){func() { a.syncBase(appL(), &a.workspaces[0], false) }, func() { a.syncBase(appL(), &a.workspaces[0], true) }, func() { a.askMergeWorkspace(appL(), v.wsID) }, func() { a.askMergeRepos(appL(), v.wsID) }} {
				run()
				assertRemoteFileUnsupported(t, a.err, "git.write")
				if a.confirmDo != nil || a.confirming || v.git.busy != "" {
					t.Fatal("remote write entry prepared callback")
				}
			}
			if d.ed.Buffer != buffer || buffer.String() != "unsaved base draft" || v.ch.message != "unsent commit message" {
				t.Fatal("base comparison replaced drafts")
			}
		})
	}
}

func TestTargetGitDivergencePredictionStates(t *testing.T) {
	states := []string{gitx.DivergenceAttributes, gitx.DivergenceConfig, gitx.DivergenceSubmodule, gitx.DivergenceShallow, gitx.DivergenceMultipleBases, gitx.DivergenceUnrelated, gitx.DivergenceLimit, gitx.DivergenceGit, "clear", "not-needed"}
	for _, reason := range states {
		t.Run(reason, func(t *testing.T) {
			_, f, v, _, tt := targetDivergenceFixture(t)
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				out, err := normal(ctx, p)
				if err != nil || p.Method != "git.divergence" {
					return out, err
				}
				d := out.(rpc.GitDivergenceResult)
				d.Conflicts = []string{}
				d.Prediction, d.Reason = gitx.DivergenceUnavailable, reason
				if reason == "clear" {
					d.Prediction, d.Reason = gitx.DivergenceCompleted, ""
				}
				if reason == "not-needed" {
					d.Prediction, d.Reason, d.Ahead = gitx.DivergenceNotNeeded, "", 0
				}
				return d, nil
			}
			f.mu.Unlock()
			s := waitTargetBase(t, v, tt)
			m := locales.In("zh-Hans")
			switch reason {
			case "clear":
				if !tt.HasText(m.BranchPredictionClear()) {
					t.Fatal("confirmed result missing")
				}
			case "not-needed":
				if tt.HasText(m.BranchPredictionClear()) {
					t.Fatal("unneeded prediction described as completed")
				}
			default:
				if !tt.HasText(m.BranchPredictionUnavailable(reason)) || tt.HasText(m.BranchPredictionClear()) || tt.HasText(m.CompareNoDifference()) {
					t.Fatal("unavailable prediction presented as clear", tt.Texts())
				}
				if !strings.Contains(baseTip(m, "base-main", &s.track), m.BranchPredictionUnavailable(reason)) {
					t.Fatal("branch tooltip omitted unavailable reason")
				}
			}
		})
	}
}

func TestTargetGitDivergenceUnchangedAndRetry(t *testing.T) {
	a, f, v, _, tt := targetDivergenceFixture(t)
	s := waitTargetBase(t, v, tt)
	var calls atomic.Int32
	var fail, badOID atomic.Bool
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.divergence" {
			calls.Add(1)
			var q rpc.GitDivergenceParams
			json.Unmarshal(p.Params, &q)
			if q.PreviousBaseline != s.result.Baseline {
				t.Errorf("poll lost previous baseline %q", q.PreviousBaseline)
			}
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "references changed")
			}
			out, err := normal(ctx, p)
			if err != nil {
				return nil, err
			}
			d := out.(rpc.GitDivergenceResult)
			if badOID.Load() {
				d.Head = targetLogCommit(42).Hash
			}
			return d, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	s.track.poll.at = time.Time{}
	until(t, tt, "unchanged poll", func() bool { return calls.Load() == 1 && !s.track.poll.busy })
	if s.track.base.Ahead != 1 || s.track.base.Behind != 2 || len(s.track.base.Conflicts) != 1 || s.track.basePrediction != gitx.DivergenceCompleted {
		t.Fatal("unchanged zeroed cached prediction")
	}
	for range 5 {
		tt.Frame()
	}
	if calls.Load() != 1 {
		t.Fatal("base comparison polled every frame")
	}
	fail.Store(true)
	a.loadTargetBase(&a.workspaces[0], v, v.git)
	until(t, tt, "base error", func() bool { return !s.track.poll.busy && s.track.baseErr != "" })
	s.track.poll.at = time.Time{}
	for range 5 {
		tt.Frame()
	}
	if calls.Load() != 2 || !s.track.baseOK || !tt.HasText(locales.In("zh-Hans").BranchConflictCount(1)) {
		t.Fatal("base failure lost data or retried")
	}
	fail.Store(false)
	badOID.Store(true)
	if err := tt.Click(locales.In("zh-Hans").BranchRefreshBase()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "invalid unchanged rejected", func() bool { return calls.Load() == 3 && !s.track.poll.busy })
	if s.track.baseErr == "" || s.result.Head != targetLogCommit(0).Hash {
		t.Fatal("unchanged with wrong OID accepted")
	}
	badOID.Store(false)
	if err := tt.Click(locales.In("zh-Hans").BranchRefreshBase()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "base retry", func() bool { return calls.Load() == 4 && !s.track.poll.busy && s.track.baseErr == "" })
}

func TestTargetGitDivergenceRootOwnership(t *testing.T) {
	for _, kind := range []string{"symlink-root", "subdirectory", "nested"} {
		t.Run(kind, func(t *testing.T) {
			a, f, v, _, tt := targetDivergenceFixture(t)
			var calls atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.divergence" {
					calls.Add(1)
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			root := v.root
			if kind == "symlink-root" {
				v.root = filepath.Join(t.TempDir(), "target-only-alias")
				a.workspaces[0].Path = v.root
			} else if kind == "subdirectory" {
				v.realRoot = filepath.Join(root, "subdir")
			} else {
				v.realRoot = filepath.Dir(root)
			}
			if kind == "symlink-root" {
				waitTargetBase(t, v, tt)
				if !tt.HasText(locales.In("zh-Hans").BranchRelativeTo("base-main")) {
					t.Fatal("valid symlink root hidden")
				}
			} else {
				for range 5 {
					tt.Frame()
				}
				if calls.Load() != 0 || tt.HasText(locales.In("zh-Hans").BranchRelativeTo("base-main")) {
					t.Fatal("non-root repository borrowed workspace base")
				}
			}
		})
	}
}

func TestTargetGitDivergenceLateAndResume(t *testing.T) {
	for _, action := range []string{"hide", "reconnect", "base-ref", "identity"} {
		t.Run(action, func(t *testing.T) {
			a, f, v, _, tt := targetDivergenceFixture(t)
			s := waitTargetBase(t, v, tt)
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var calls atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.divergence" {
					n := calls.Add(1)
					if n == 1 {
						out, err := normal(ctx, p)
						close(entered)
						<-release
						close(returned)
						return out, err
					}
					var q rpc.GitDivergenceParams
					json.Unmarshal(p.Params, &q)
					if q.PreviousBaseline != "" {
						t.Errorf("resumed request reused old baseline %q", q.PreviousBaseline)
					}
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.loadTargetBase(&a.workspaces[0], v, v.git)
			<-entered
			old, seq := a.request(), s.read.seq
			switch action {
			case "hide":
				a.selected = settingsBoardID
				tt.Frame()
			case "reconnect":
				a.host.detach()
				tt.Frame()
			case "base-ref":
				a.workspaces[0].BaseRef = "next-base"
				tt.Frame()
			case "identity":
				id := v.git.identity
				id.RootInode++
				a.targetGitIdentity(v.git, id)
			}
			if s.read.seq == seq {
				t.Fatal("old base request not canceled")
			}
			if action == "hide" || action == "reconnect" {
				if !s.track.baseOK || s.baseline != "" {
					t.Fatal("cancel lost result or retained reuse baseline")
				}
				if action == "hide" {
					a.selected = v.wsID
				} else if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			}
			if action != "identity" {
				until(t, tt, "resumed base query", func() bool {
					return calls.Load() >= 2 && v.git.baseTrack.track.baseOK && !v.git.baseTrack.track.poll.busy
				})
			}
			once.Do(func() { close(release) })
			<-returned
			a.runPending()
			tt.Frame()
			if action == "base-ref" && v.git.baseTrack.ref != "next-base" {
				t.Fatal("late result changed base ref")
			}
		})
	}
}

func TestTargetGitDivergenceDiscoveryRetry(t *testing.T) {
	a, f, v, _, tt := targetDivergenceFixture(t)
	var fail atomic.Bool
	fail.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.discover" && fail.Load() {
			return nil, rpc.Errorf(rpc.CodeInternal, "temporary discovery failure")
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	v.repos.scanned = false
	v.repos.scanAt = time.Time{}
	until(t, tt, "discovery error shown", func() bool { return v.repos.err != "" && tt.HasText(v.repos.err) })
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	fail.Store(false)
	if err := tt.Click(locales.In("zh-Hans").CommonRefresh()); err != nil {
		t.Fatal(err)
	}
	waitTargetBase(t, v, tt)
	if v.repos.err != "" {
		t.Fatal("discovery error retained after refresh")
	}
	if a.confirmDo != nil {
		t.Fatal("retry prepared write")
	}
}

func TestPreviewTargetGitDivergence(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, _, tt := targetDivergenceFixture(t)
		s := waitTargetBase(t, v, tt)
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-base-conflict-"+name+".png")
		s.track.base.Conflicts = nil
		s.track.basePrediction, s.track.baseReason = gitx.DivergenceUnavailable, gitx.DivergenceAttributes
		savePNG(t, newPreviewTester(a, dark), "remote-git-base-unavailable-"+name+".png")
	}
}
