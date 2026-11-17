package app

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
)

func targetContentDiffFixture(t *testing.T) (*app, *hostControlFixture, *filesView, *gitState, *fileDoc) {
	t.Helper()
	a, f, v, discovery := remoteGitFixture(t)
	targetGitContentHandler(t, f, discovery.Repos[0].Identity, []byte("before\n"), []byte("after\n"))
	g := discoverTargetGit(t, a, v)
	g.status = &gitx.Status{Commit: strings.Repeat("a", 40)}
	a.openDiff(&a.workspaces[0], diffSpec{root: g.repo, rel: "same.txt", oldRev: "HEAD"}, nil, true)
	d := v.cur
	settleTargetGit(t, a, func() bool { return !d.diff.loading })
	if !d.diff.loaded {
		t.Fatal(a.err)
	}
	return a, f, v, g, d
}

func TestTargetGitContentHeadRefreshRetry(t *testing.T) {
	a, f, _, g, d := targetContentDiffFixture(t)
	var reads atomic.Int32
	var failed atomic.Bool
	failed.Store(true)
	stamp := d.diff.stamp
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.file" {
			reads.Add(1)
			if failed.Load() {
				return nil, rpc.Errorf(rpc.CodePermissionDenied, "temporary failure")
			}
		}
		if p.Method == "files.stat" {
			var q rpc.FilePathsParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			return []rpc.FileStatResult{{Path: q.Paths[0], Stamp: stamp}}, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	old := g.status
	g.status = &gitx.Status{Commit: strings.Repeat("b", 40)}
	a.targetGitChanged(g, old)
	settleTargetGit(t, a, func() bool { return !d.diff.loading })
	if a.err == "" || !d.diff.loaded || d.diff.state.Old.String() != "before\n" {
		t.Fatal("failed refresh did not retain existing comparison")
	}
	failed.Store(false)
	before := reads.Load()
	a.targetGitChanged(g, g.status)
	settleTargetGit(t, a, func() bool { return !d.diff.loading })
	if reads.Load() == before || d.diff.retry {
		t.Fatal("unchanged worktree prevented retry after HEAD refresh failure")
	}
}

func TestTargetGitContentComparisonSurvivesStat(t *testing.T) {
	a, f, v, _, d := targetContentDiffFixture(t)
	df := d.diff
	text := strings.Repeat("x\n", diffAheadSize/2)
	df.state.Old.Reset(text)
	df.state.New.Reset(text)
	stamp := df.stamp
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "files.stat" {
			var q rpc.FilePathsParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			return []rpc.FileStatResult{{Path: q.Paths[0], Stamp: stamp}}, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.toggleIgnoreSpace(d)
	deadline := time.Now().Add(5 * time.Second)
	queued := false
	for time.Now().Before(deadline) {
		a.pendingMu.Lock()
		queued = len(a.pending) > 0
		a.pendingMu.Unlock()
		if queued {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !queued {
		t.Fatal("comparison did not finish")
	}
	a.reloadTargetDiff(v, d)
	settleTargetGit(t, a, func() bool { return !df.loading })
	if df.comparing || df.state.IgnoreWhitespace {
		t.Fatal("unchanged stat discarded comparison or left it busy")
	}
}
