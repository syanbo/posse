package app

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func targetPushFixture(t *testing.T, total int) (*app, *hostControlFixture, *filesView, *fileDoc, *ui.Tester) {
	t.Helper()
	a, f, v, draft, tt := targetBlameFixture(t)
	id := v.git.identity
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.pushPreview" {
			return normal(ctx, p)
		}
		var q rpc.GitPushPreviewParams
		if err := json.Unmarshal(p.Params, &q); err != nil {
			return nil, err
		}
		if q.Expected != id || q.Path != id.Root || q.Limit != 500 {
			return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
		}
		out := rpc.GitPushPreviewResult{Identity: id, PushPreviewRead: gitx.PushPreviewRead{
			Plan:      gitx.PushPlan{Branch: q.Branch, Remote: "origin", RemoteBranch: q.Branch, Commits: []gitx.CommitInfo{}},
			BranchOID: targetLogCommit(0).Hash, TrackingRef: "refs/remotes/origin/" + q.Branch, TrackingOID: targetLogCommit(2000).Hash,
			Baseline: strings.Repeat("b", 64), More: total > q.Limit,
		}}
		for i := range min(total, q.Limit) {
			out.Plan.Commits = append(out.Plan.Commits, targetLogCommit(i))
		}
		return out, nil
	}
	f.mu.Unlock()
	return a, f, v, draft, tt
}

func openTargetPushTest(t *testing.T, a *app, tt *ui.Tester) *pushState {
	t.Helper()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
	until(t, tt, "target push preview", func() bool { return a.push != nil && a.push.loaded && !a.push.loading })
	if a.push.err != "" {
		t.Fatal(a.push.err)
	}
	return a.push
}

func selectPushTest(t *testing.T, a *app, tt *ui.Tester, ps *pushState) {
	t.Helper()
	if err := tt.Click("Target commit 0"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "push commit files", func() bool { return ps.commit != nil && len(ps.commit.files) == 2 })
	if err := tt.Click("src/detail.txt"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "push fixed diff", func() bool { return ps.detail != nil && ps.detail.kind == docDiff })
}

func TestTargetGitPushProduction(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "viewer", true: "controlled"}[controlled], func(t *testing.T) {
			a, _, v, draft, tt := targetPushFixture(t, 501)
			if controlled {
				takeRequestTestControl(t, a)
			}
			draft.ed.Buffer.Set("unsaved push draft")
			buffer := draft.ed.Buffer
			a.openBranches(&a.workspaces[0])
			m := locales.In("zh-Hans")
			until(t, tt, "push branch menu", func() bool { return tt.HasText(m.PushPreviewTitle()) })
			if err := tt.Click(m.PushPreviewTitle()); err != nil {
				t.Fatal(err)
			}
			until(t, tt, "push plan", func() bool { return a.push != nil && a.push.loaded })
			ps := a.push
			if ps.plan.Branch != "target-main" || !tt.HasText(m.PushPreviewNotice()) || !tt.HasText(m.LogMoreCommitCount(500)) || !tt.HasText(m.PushPreviewLimit(500)) {
				t.Fatal("preview scope missing", tt.Texts())
			}
			selectPushTest(t, a, tt, ps)
			if ps.detail.diff.oldRev != targetLogCommit(0).Parents[0] || ps.detail.diff.newRev != targetLogCommit(0).Hash || ps.detail.diff.state.New.String() != "target revision\n" {
				t.Fatal("push diff not fixed to target commit")
			}
			for _, label := range []string{m.ComparePush(), m.CompareForcePushEllipsis()} {
				if err := tt.Click(label); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				if ps.pushing || a.confirmDo != nil {
					t.Fatal("preview prepared push")
				}
			}
			for _, force := range []bool{false, true} {
				a.doPush(appL(), v, ps, force)
				assertRemoteFileUnsupported(t, a.err, "git.write")
				if ps.pushing {
					t.Fatal("write guard left pushing")
				}
			}
			if buffer != draft.ed.Buffer || buffer.String() != "unsaved push draft" {
				t.Fatal("push preview changed draft")
			}
			if err := tt.Click(m.CommonCancel()); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if a.push != nil || v.push != nil {
				t.Fatal("closed dialog still owns target reads")
			}
			a.openBranches(&a.workspaces[0])
			until(t, tt, "branch selection", func() bool { return tt.HasText("feature-target") })
			if err := tt.Click("feature-target"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			tt.Key(0, ui.KeyUp)
			tt.Key(0, ui.KeyEnter)
			until(t, tt, "selected branch preview", func() bool { return a.push != nil && a.push.loaded })
			if a.push.plan.Branch != "feature-target" {
				t.Fatal("preview ignored selected branch")
			}
		})
	}
}

func TestTargetGitPushFailureAndRefresh(t *testing.T) {
	a, f, v, _, tt := targetPushFixture(t, 3)
	ps := openTargetPushTest(t, a, tt)
	selectPushTest(t, a, tt, ps)
	oldDetail := ps.detail
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.pushPreview" {
			calls.Add(1)
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "references changed")
			}
			out, err := normal(ctx, p)
			if err != nil {
				return nil, err
			}
			preview := out.(rpc.GitPushPreviewResult)
			preview.Plan.Remote = "new-origin"
			preview.TrackingRef = "refs/remotes/new-origin/" + preview.Plan.RemoteBranch
			preview.Baseline = strings.Repeat("c", 64)
			return preview, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.loadTargetPush(ps)
	until(t, tt, "failed push refresh", func() bool { return !ps.loading && ps.err != "" })
	for range 5 {
		tt.Frame()
	}
	if calls.Load() != 1 || !ps.loaded || ps.detail != oldDetail || !tt.HasText("Target commit 0") {
		t.Fatal("failed refresh lost plan or retried")
	}
	fail.Store(false)
	if err := tt.Click(locales.In("zh-Hans").CommonRefresh()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "push preview refreshed", func() bool { return !ps.loading && ps.err == "" && ps.plan.Remote == "new-origin" })
	if ps.commit != nil || ps.detail != nil || v.ownsGitContent(oldDetail) {
		t.Fatal("new destination retained old selection")
	}
}

func TestTargetGitPushDetachedAndCapturedBranch(t *testing.T) {
	a, f, _, _, tt := targetPushFixture(t, 3)
	var detached atomic.Bool
	var moved atomic.Bool
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		out, err := normal(ctx, p)
		if err != nil {
			return nil, err
		}
		if p.Method == "git.status" {
			status := out.(rpc.GitStatusResult)
			if detached.Load() {
				status.Status.Head = ""
			} else if moved.Load() {
				status.Status.Head = "next-branch"
			}
			return status, nil
		}
		if p.Method == "git.pushPreview" {
			moved.Store(true)
		}
		return out, nil
	}
	f.mu.Unlock()
	ps := openTargetPushTest(t, a, tt)
	if ps.plan.Branch != "target-main" {
		t.Fatal("plan borrowed later HEAD")
	}
	a.loadTargetPush(ps)
	until(t, tt, "new branch", func() bool { return !ps.loading && ps.plan.Branch == "next-branch" })
	detached.Store(true)
	a.loadTargetPush(ps)
	until(t, tt, "detached preview", func() bool { return !ps.loading && ps.err != "" })
	if ps.loaded || ps.preview != nil || ps.commit != nil || ps.err != appL().CompareDetachedPush() || !tt.HasText(ps.err) {
		t.Fatalf("detached preview: loaded=%v preview=%v commit=%v err=%q texts=%v", ps.loaded, ps.preview, ps.commit, ps.err, tt.Texts())
	}
}

func TestTargetGitPushLateAndResume(t *testing.T) {
	for _, tc := range []struct{ method, action string }{
		{"git.pushPreview", "reconnect"}, {"git.pushPreview", "replace"}, {"git.pushPreview", "close"}, {"git.pushPreview", "identity"}, {"git.pushPreview", "hide"},
		{"git.commitFiles", "reconnect"}, {"git.commitFiles", "replace"}, {"git.commitFiles", "selection"},
		{"git.file", "reconnect"}, {"git.file", "close"}, {"git.file", "file"},
	} {
		t.Run(tc.method+"/"+tc.action, func(t *testing.T) {
			a, f, v, _, tt := targetPushFixture(t, 3)
			ps := openTargetPushTest(t, a, tt)
			if tc.method == "git.file" {
				a.selectCommit(ps.root, &ps.commit, ps.plan.Commits[0])
				until(t, tt, "detail before diff", func() bool { return ps.commit != nil && ps.commit.files != nil })
			}
			entered, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var calls atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == tc.method && calls.Add(1) == 1 {
					out, err := normal(ctx, p)
					close(entered)
					<-release
					close(returned)
					return out, err
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			switch tc.method {
			case "git.pushPreview":
				a.loadTargetPush(ps)
			case "git.commitFiles":
				a.selectCommit(ps.root, &ps.commit, ps.plan.Commits[0])
			case "git.file":
				a.pushShow(v, ps, 1, true)
			}
			<-entered
			old := a.request()
			cv, d, seq := ps.commit, ps.detail, ps.read.seq
			switch tc.action {
			case "reconnect":
				a.host.detach()
				tt.Frame()
				if !ps.loaded || !tt.HasText("Target commit 0") {
					t.Fatal("disconnect cleared loaded plan")
				}
				if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			case "replace":
				a.openTargetPush(&a.workspaces[0], "feature-target")
				if tc.method == "git.commitFiles" {
					until(t, tt, "replacement plan", func() bool { return a.push.loaded })
					a.selectCommit(a.push.root, &a.push.commit, a.push.plan.Commits[1])
				}
			case "close":
				a.closePush(ps)
				tt.Frame()
			case "identity":
				id := v.git.identity
				id.RootInode++
				a.targetGitIdentity(v.git, id)
				if ps.loaded || ps.preview != nil || ps.commit != nil || ps.detail != nil {
					t.Fatal("identity retained old plan")
				}
			case "selection":
				a.selectCommit(ps.root, &ps.commit, ps.plan.Commits[1])
			case "file":
				a.pushShow(v, ps, 0, true)
			case "hide":
				views := newHostViews(a)
				views.add("other", &app{})
				views.activate("other")
				if !ps.loaded {
					t.Fatal("hide cleared loaded plan")
				}
				views.activate("local")
				if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			}
			if tc.method == "git.pushPreview" && ps.read.seq == seq {
				t.Fatal("old preview read not canceled")
			}
			if tc.action != "close" && tc.action != "identity" {
				until(t, tt, "replacement target read", func() bool {
					if calls.Load() < 2 {
						return false
					}
					now := a.push
					if now == nil || now.loading || !now.loaded {
						return false
					}
					switch tc.method {
					case "git.commitFiles":
						return now.commit != nil && now.commit.files != nil
					case "git.file":
						return now.detail != nil && now.detail.kind == docDiff
					}
					return true
				})
			}
			once.Do(func() { close(release) })
			<-returned
			a.runPending()
			tt.Frame()
			if tc.action == "close" && (a.push != nil || v.push != nil) {
				t.Fatal("late read reopened dialog")
			}
			if tc.action == "replace" && a.push.plan.Branch != "feature-target" {
				t.Fatal("old dialog replaced new plan")
			}
			if tc.action == "selection" && a.push.commit.info.Hash != targetLogCommit(1).Hash {
				t.Fatal("old selection applied")
			}
			if tc.action == "file" && a.push.detail.diff.rel != "same.txt" {
				t.Fatal("old file applied")
			}
			if tc.action == "replace" || tc.action == "close" || tc.action == "selection" {
				if cv != nil && tc.method == "git.commitFiles" && cv.files != nil {
					t.Fatal("canceled detail accepted late reply")
				}
			}
			if tc.action == "close" && d != nil && d.diff.loaded {
				t.Fatal("canceled diff accepted late reply")
			}
		})
	}
}

func TestTargetGitPushDetailRetryAndShallow(t *testing.T) {
	a, f, _, _, tt := targetPushFixture(t, 3)
	ps := openTargetPushTest(t, a, tt)
	var failDetail, failFile atomic.Bool
	var details, reads atomic.Int32
	failDetail.Store(true)
	failFile.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.commitFiles" {
			details.Add(1)
			if failDetail.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "temporarily unavailable")
			}
			out, err := normal(ctx, p)
			if err != nil {
				return nil, err
			}
			detail := out.(rpc.GitCommitFilesResult)
			detail.Shallow, detail.DiffBase = true, ""
			return detail, nil
		}
		if p.Method == "git.file" {
			var q rpc.GitFileParams
			json.Unmarshal(p.Params, &q)
			if q.File != "src/detail.txt" {
				return normal(ctx, p)
			}
			reads.Add(1)
			if q.Revision != targetLogCommit(0).Hash {
				return nil, rpc.Errorf(rpc.CodeConflict, "missing parent object")
			}
			if failFile.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "temporarily unavailable")
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	if err := tt.Click("Target commit 0"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "failed commit detail", func() bool { return ps.commit != nil && ps.commit.err != "" })
	for range 5 {
		tt.Frame()
	}
	if details.Load() != 1 || !ps.loaded {
		t.Fatal("detail error retried or discarded plan")
	}
	failDetail.Store(false)
	if err := tt.Click(locales.In("zh-Hans").CommonRefresh()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "commit detail retry", func() bool { return ps.commit.files != nil && ps.commit.err == "" })
	if ps.commit.diffBase == nil || *ps.commit.diffBase != "" || len(ps.commit.info.Parents) != 1 {
		t.Fatal("shallow metadata lost")
	}
	if err := tt.Click("src/detail.txt"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "failed inline diff", func() bool { return ps.detail != nil && ps.detail.kind == docError })
	for range 5 {
		tt.Frame()
	}
	if reads.Load() != 1 || ps.detail.diff.oldRev != "" {
		t.Fatal("shallow diff read parent or retried")
	}
	failFile.Store(false)
	if err := tt.Click(locales.In("zh-Hans").CommonRefresh()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "inline diff retry", func() bool { return ps.detail.kind == docDiff })
	if reads.Load() != 2 || ps.detail.diff.state.Old.String() != "" || ps.detail.diff.state.New.String() != "target revision\n" {
		t.Fatal("inline retry ignored shallow DiffBase")
	}
}

func TestTargetGitPushDiscoveryRetry(t *testing.T) {
	a, f, v, _, tt := targetPushFixture(t, 3)
	var fail atomic.Bool
	var discoveries atomic.Int32
	fail.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.discover" {
			discoveries.Add(1)
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeInternal, "temporary discovery failure")
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	v.repos.scanned = false
	a.openTargetPush(&a.workspaces[0], "")
	ps := a.push
	until(t, tt, "initial discovery failed", func() bool { return !ps.waiting && ps.err != "" })
	for range 5 {
		tt.Frame()
	}
	if ps.root != "" || discoveries.Load() != 1 {
		t.Fatal("failed discovery bound a root or retried")
	}
	fail.Store(false)
	a.scanTargetRepos(v)
	until(t, tt, "discovery recovered", func() bool { return v.repos.scanned && !v.repos.scanning })
	if ps.loaded {
		t.Fatal("discovery recovery silently retried failed preview")
	}
	if err := tt.Click(locales.In("zh-Hans").CommonRefresh()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "preview recovered by refresh", func() bool { return ps.loaded && ps.err == "" })
	if ps.root != v.git.repo || ps.plan.Branch != "target-main" {
		t.Fatal("refresh did not bind discovered repository")
	}
}

func TestPreviewTargetGitPush(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, f, v, _, tt := targetPushFixture(t, 501)
		ps := openTargetPushTest(t, a, tt)
		contents := [][]byte{[]byte("connect to target\nshow stored commits\n"), []byte("connect to target\npreview pending commits\nkeep push disabled\n")}
		blobs := map[string][]byte{}
		oids := make([]string, len(contents))
		for i, data := range contents {
			oids[i] = fmt.Sprintf("%x", sha1.Sum(append(fmt.Appendf(nil, "blob %d\x00", len(data)), data...)))
			blobs[oids[i]] = data
		}
		id := v.git.identity
		f.mu.Lock()
		normal := f.desktopCall
		f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
			switch p.Method {
			case "git.file":
				var q rpc.GitFileParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				i := 0
				if q.Revision == targetLogCommit(0).Hash {
					i = 1
				}
				return rpc.GitFileResult{Identity: id, File: gitx.FileVersion{Source: q.Source, Revision: q.Revision, Commit: q.Revision, Path: q.File, OID: oids[i], Size: int64(len(contents[i]))}}, nil
			case "git.blobRange":
				var q rpc.GitBlobRangeParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				data := blobs[q.OID]
				end := min(int64(len(data)), q.Offset+int64(q.Limit))
				return rpc.GitBlobRangeResult{Identity: id, OID: q.OID, Size: int64(len(data)), Offset: q.Offset, Data: data[q.Offset:end], EOF: end == int64(len(data))}, nil
			}
			return normal(ctx, p)
		}
		f.mu.Unlock()
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-push-"+name+".png")
		selectPushTest(t, a, tt, ps)
		savePNG(t, newPreviewTester(a, dark), "remote-git-push-diff-"+name+".png")
	}
}
