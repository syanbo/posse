package app

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func targetCompareFixture(t *testing.T, total int) (*app, *hostControlFixture, *filesView, *fileDoc, *ui.Tester) {
	t.Helper()
	a, f, v, draft, tt := targetBlameFixture(t)
	id := v.git.identity
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.compare" {
			return normal(ctx, p)
		}
		var q rpc.GitCompareParams
		if err := json.Unmarshal(p.Params, &q); err != nil {
			return nil, err
		}
		if q.Expected != id || q.Path != id.Root || q.Limit != 1000 {
			return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
		}
		out := rpc.GitCompareResult{Identity: id, Current: q.Current, Other: q.Other, CompareRead: gitx.CompareRead{
			CurrentOID: targetLogCommit(2000).Hash, OtherOID: targetLogCommit(0).Hash,
			OnlyCurrent: []gitx.CommitInfo{targetLogCommit(2000)}, OnlyOther: []gitx.CommitInfo{}, Baseline: strings.Repeat("b", 64),
		}}
		for i := range min(total, q.Limit) {
			out.OnlyOther = append(out.OnlyOther, targetLogCommit(i))
		}
		out.MoreOther = total > q.Limit
		if q.Current == "feature-target" {
			out.CurrentOID, out.OtherOID = out.OtherOID, out.CurrentOID
			out.OnlyCurrent, out.OnlyOther = out.OnlyOther, out.OnlyCurrent
			out.MoreCurrent, out.MoreOther = out.MoreOther, out.MoreCurrent
		}
		return out, nil
	}
	f.mu.Unlock()
	return a, f, v, draft, tt
}

func openTargetCompare(t *testing.T, a *app, v *filesView, tt *ui.Tester) *fileDoc {
	t.Helper()
	a.openCompare(&a.workspaces[0], "feature-target", "target-main", false)
	d := v.cur
	until(t, tt, "target compare", func() bool { return d.comp.loaded && !d.comp.loading })
	if d.comp.err != "" {
		t.Fatal(d.comp.err)
	}
	return d
}

func TestTargetGitCompareProduction(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "viewer", true: "controlled"}[controlled], func(t *testing.T) {
			a, _, v, draft, tt := targetCompareFixture(t, 1001)
			if controlled {
				takeRequestTestControl(t, a)
			}
			draft.ed.Buffer.Set("unsaved compare draft")
			buffer := draft.ed.Buffer
			a.openBranches(&a.workspaces[0])
			until(t, tt, "target branch menu", func() bool { return tt.HasText("feature-target") })
			if err := tt.Click("feature-target"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			m := locales.In("zh-Hans")
			for _, label := range []string{m.BranchCheckout(), m.BranchRevealWorktree(), m.BranchMergeInto("feature-target", "target-main"), m.BranchRebaseOnto("target-main", "feature-target")} {
				if tt.HasText(label) {
					t.Fatalf("remote menu exposes %s", label)
				}
			}
			if err := tt.Click(m.BranchCompareWith("target-main")); err != nil {
				t.Fatal(err)
			}
			until(t, tt, "compare opened from branch menu", func() bool { return v.cur.comp != nil && v.cur.comp.loaded })
			d, cd := v.cur, v.cur.comp
			if !tt.HasText("Target commit 0") || !tt.HasText("Target commit 2000") || cd.otherOID != targetLogCommit(0).Hash || cd.curOID != targetLogCommit(2000).Hash {
				t.Fatal("target comparison missing", tt.Texts())
			}
			if err := tt.Click("Target commit 0"); err != nil {
				t.Fatal(err)
			}
			until(t, tt, "comparison detail", func() bool { return cd.commit != nil && len(cd.commit.files) == 2 })
			if _, ok := tt.Find(m.GitCherryPick()); ok {
				t.Fatal("remote comparison exposes cherry-pick")
			}
			tt.Key(ui.Cmd|ui.Shift, ui.KeyK)
			tt.Frame()
			if a.push == nil || !a.push.remote || a.confirmDo != nil || a.promptDlg != nil {
				t.Fatal("push shortcut did not open a read-only preview")
			}
			a.closePush(a.push)
			tt.Frame()
			a.err = ""
			if err := tt.Click("src/detail.txt"); err != nil {
				t.Fatal(err)
			}
			until(t, tt, "comparison fixed commit diff", func() bool { return v.cur.diff != nil && v.cur.kind == docDiff })
			if v.cur.diff.oldRev != targetLogCommit(0).Parents[0] || v.cur.diff.newRev != targetLogCommit(0).Hash || v.cur.diff.state.New.String() != "target revision\n" {
				t.Fatal("comparison diff did not use fixed target commit and DiffBase")
			}
			v.cur = d
			tt = newTester(a.build, 1000, 760)
			tt.Frame()
			if !tt.HasText(m.LogMoreCommitCount(1000)) {
				t.Fatal("narrow comparison lost truncated count")
			}
			if err := tt.Click(m.CompareSwap()); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if cd.onlyOther[0].Hash != targetLogCommit(2000).Hash || cd.curOID != targetLogCommit(0).Hash {
				t.Fatalf("swap did not swap fixed comparison: other=%s cur=%s first=%s curOID=%s loading=%v err=%s docs=%d current=%s", cd.other, cd.cur, cd.onlyOther[0].Hash, cd.curOID, cd.loading, cd.err, len(v.docs), v.cur.path)
			}
			if err := tt.Click(m.CommonRefresh()); err != nil {
				t.Fatal(err)
			}
			until(t, tt, "narrow comparison refreshed", func() bool { return !cd.loading && cd.err == "" })
			if draft.ed.Buffer != buffer || buffer.String() != "unsaved compare draft" {
				t.Fatal("comparison replaced dirty editor buffer")
			}
		})
	}
}

func TestTargetGitCompareLimitAndRetry(t *testing.T) {
	a, f, v, _, tt := targetCompareFixture(t, 1001)
	d := openTargetCompare(t, a, v, tt)
	cd := d.comp
	cd.queryOther = "not in loaded commits"
	tt.Frame()
	m := locales.In("zh-Hans")
	if !tt.HasText(m.LogMoreCommitCount(1000)) || !tt.HasText(m.CompareLoadedLimit(1000)) || !tt.HasText(m.CompareNoMatch()) {
		t.Fatal("truncated search missing loaded scope", tt.Texts())
	}
	cd.queryOther = ""
	var calls atomic.Int32
	var fail atomic.Bool
	fail.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.compare" {
			calls.Add(1)
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "references changed")
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.loadCompare(v, d)
	until(t, tt, "compare refresh failed", func() bool { return !cd.loading && cd.err != "" })
	for range 5 {
		tt.Frame()
	}
	if calls.Load() != 1 || len(cd.onlyOther) != 1000 || !tt.HasText("Target commit 0") {
		t.Fatal("failed comparison discarded data or auto-retried")
	}
	fail.Store(false)
	if err := tt.Click(m.CommonRefresh()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "compare retry", func() bool { return !cd.loading && cd.err == "" && calls.Load() == 2 })
}

func TestTargetGitCompareLateAndResume(t *testing.T) {
	for _, tc := range []struct{ method, action string }{
		{"git.compare", "tab"}, {"git.commitFiles", "tab"}, {"git.compare", "reconnect"},
		{"git.compare", "close"}, {"git.commitFiles", "close"}, {"git.compare", "identity"},
		{"git.compare", "swap"}, {"git.compare", "refresh"}, {"git.commitFiles", "selection"},
	} {
		t.Run(tc.method+"/"+tc.action, func(t *testing.T) {
			a, f, v, draft, tt := targetCompareFixture(t, 3)
			d := openTargetCompare(t, a, v, tt)
			cd := d.comp
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var calls atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == tc.method && calls.Add(1) == 1 {
					close(entered)
					<-release
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			if tc.method == "git.compare" {
				a.loadCompare(v, d)
			} else {
				a.selectCommit(cd.root, &cd.commit, targetLogCommit(0))
			}
			<-entered
			seq := cd.read.seq
			cv := cd.commit
			old := a.request()
			switch tc.action {
			case "tab":
				v.cur = draft
				tt.Frame()
			case "reconnect":
				a.host.detach()
				tt.Frame()
			case "close":
				a.dropDoc(v, d)
				tt.Frame()
			case "identity":
				id := v.git.identity
				id.RootInode++
				a.targetGitIdentity(v.git, id)
			case "swap":
				a.swapTargetCompare(v, d)
			case "refresh":
				a.loadCompare(v, d)
			case "selection":
				a.selectCommit(cd.root, &cd.commit, targetLogCommit(1))
			}
			if tc.method == "git.compare" && cd.read.seq == seq {
				t.Fatal("old comparison request not invalidated")
			}
			if cv != nil && cv.files != nil {
				t.Fatal("old detail applied")
			}
			switch tc.action {
			case "tab":
				v.cur = d
			case "reconnect":
				if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			case "close", "identity":
				return
			}
			until(t, tt, "new comparison request completed", func() bool {
				if calls.Load() < 2 {
					return false
				}
				if tc.method == "git.compare" {
					return !cd.loading && cd.err == ""
				}
				return cd.commit != nil && !cd.commit.loading && len(cd.commit.files) == 2
			})
			if tc.action == "swap" && (cd.onlyOther[0].Hash != targetLogCommit(2000).Hash || cd.otherOID != targetLogCommit(2000).Hash) {
				t.Fatal("swap mixed old sides")
			}
			if tc.action == "selection" && cd.commit.info.Hash != targetLogCommit(1).Hash {
				t.Fatal("old selected commit replaced new selection")
			}
		})
	}
}

func TestPreviewTargetGitCompare(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, _, tt := targetCompareFixture(t, 1001)
		d := openTargetCompare(t, a, v, tt)
		a.selectCommit(d.comp.root, &d.comp.commit, targetLogCommit(0))
		until(t, tt, "comparison detail", func() bool { return d.comp.commit != nil && !d.comp.commit.loading })
		name := "light"
		if dark {
			name = "dark"
		}
		savePNG(t, newPreviewTester(a, dark), "remote-git-compare-"+name+".png")
		savePNG(t, newPreviewTesterSize(a, dark, 1000, 760), "remote-git-compare-narrow-"+name+".png")
	}
}
