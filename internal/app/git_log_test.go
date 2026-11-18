package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func targetLogCommit(i int) gitx.CommitInfo {
	return gitx.CommitInfo{Hash: fmt.Sprintf("%040x", i+1), Parents: []string{fmt.Sprintf("%040x", i+2)}, Author: "Target", Email: "target@example.invalid", Subject: fmt.Sprintf("Target commit %d", i), Time: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)}
}

func targetGitLogHandler(t *testing.T, f *hostControlFixture, id gitx.RepositoryIdentity, total int) {
	t.Helper()
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.log":
			var q rpc.GitLogParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			if q.Expected != id {
				return nil, rpc.Errorf(rpc.CodeConflict, "repository changed")
			}
			n := total
			if q.Filter.Text != "" {
				n = 1
			}
			out := rpc.GitLogResult{Identity: id, Baseline: strings.Repeat("b", 64), Commits: []gitx.CommitInfo{}}
			for i := q.Skip; i < min(n, q.Skip+q.Limit); i++ {
				out.Commits = append(out.Commits, targetLogCommit(i))
			}
			out.NextSkip = q.Skip + len(out.Commits)
			out.HasMore = out.NextSkip < n
			return out, nil
		case "git.commitFiles":
			var q rpc.GitCommitFilesParams
			if err := json.Unmarshal(p.Params, &q); err != nil {
				return nil, err
			}
			ci := targetLogCommit(0)
			ci.Hash = q.Hash
			return rpc.GitCommitFilesResult{Identity: id, Commit: ci, DiffBase: ci.Parents[0], Files: []gitx.FileChange{{Path: "same.txt", Status: 'M'}}}, nil
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
}

func remoteLogFixture(t *testing.T, total int) (*app, *hostControlFixture, *filesView, *fileDoc) {
	t.Helper()
	a, f, v, out := remoteGitFixture(t)
	targetGitLogHandler(t, f, out.Repos[0].Identity, total)
	targetGitContentHandler(t, f, out.Repos[0].Identity, []byte("target revision\n"), []byte("target worktree\n"))
	discoverTargetGit(t, a, v)
	a.openLog(&a.workspaces[0], "", "")
	d := v.logCur
	settleTargetGit(t, a, func() bool { return !d.log.loading })
	if d.log.err != "" {
		t.Fatal(d.log.err)
	}
	return a, f, v, d
}

func TestTargetGitLogPagingFiltersAndRetry(t *testing.T) {
	a, f, v, d := remoteLogFixture(t, 1205)
	ld := d.log
	if len(ld.commits) != 300 || !ld.more || ld.filter.Branches[0] != "HEAD" {
		t.Fatalf("first page: %+v", ld)
	}
	for range 3 {
		a.loadLog(v, d, true)
		settleTargetGit(t, a, func() bool { return !ld.loading })
	}
	if len(ld.commits) != 1200 {
		t.Fatal(len(ld.commits), ld.err)
	}
	var pages atomic.Int32
	var fail atomic.Bool
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.log" {
			var q rpc.GitLogParams
			json.Unmarshal(p.Params, &q)
			pages.Add(1)
			if q.Limit > 1000 || q.Skip > 0 && q.Baseline != strings.Repeat("b", 64) {
				t.Errorf("invalid page %+v", q)
			}
			if fail.Load() {
				return nil, rpc.Errorf(rpc.CodeConflict, "references changed")
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.reloadLog(v, d)
	settleTargetGit(t, a, func() bool { return !ld.loading })
	if pages.Load() != 2 || len(ld.commits) != 1200 {
		t.Fatalf("refresh pages=%d rows=%d err=%s", pages.Load(), len(ld.commits), ld.err)
	}
	fail.Store(true)
	a.loadLog(v, d, true)
	settleTargetGit(t, a, func() bool { return !ld.loading })
	if len(ld.commits) != 1200 || ld.err == "" {
		t.Fatal("failure discarded list")
	}
	fail.Store(false)
	a.loadLog(v, d, false)
	settleTargetGit(t, a, func() bool { return !ld.loading })
	if len(ld.commits) != 300 || ld.err != "" {
		t.Fatal("refresh did not reset baseline")
	}
	ld.text, ld.author = "needle", "Target"
	a.applyLogFilter(v, d)
	settleTargetGit(t, a, func() bool { return ld.filter.Text == "needle" && !ld.loading })
	if len(ld.commits) != 1 || ld.loadedFilter.Author != "Target" {
		t.Fatal("filter not applied", ld.err)
	}
}

func TestTargetGitLogCommitAndHistory(t *testing.T) {
	a, _, v, d := remoteLogFixture(t, 3)
	a.selectCommit(d.log.root, &d.log.commit, d.log.commits[0])
	settleTargetGit(t, a, func() bool { return d.log.commit != nil && !d.log.commit.loading })
	cv := d.log.commit
	if len(cv.files) != 1 || len(cv.info.Parents) != 1 {
		t.Fatal("commit detail missing", cv.err)
	}
	_, open := a.tabDiffs(&a.workspaces[0], v, commitSpecs(cv.root, cv.info, cv.files))
	open(0)
	settleTargetGit(t, a, func() bool { return v.cur != nil && !v.cur.diff.loading })
	if v.cur.kind != docDiff || v.cur.diff.state.New.String() != "target revision\n" {
		t.Fatal("commit diff did not use target", v.cur.err)
	}
	a.openLogIn(&a.workspaces[0], v.git, "", "same.txt")
	h := v.logCur
	settleTargetGit(t, a, func() bool { return !h.log.loading })
	h.log.hist = &fileHistory{layout: editor.DiffUnified}
	a.selectHistory(v, h, h.log.commits[0])
	settleTargetGit(t, a, func() bool { return !h.log.hist.loading && h.log.hist.diff != nil })
	hd := h.log.hist.diff
	settleTargetGit(t, a, func() bool { return !hd.diff.loading })
	if hd.kind != docDiff || hd.diff.state.New.String() != "target revision\n" {
		t.Fatal("embedded history diff did not load", hd.err)
	}
	a.closeLog(v, h)
	if v.ownsGitContent(hd) {
		t.Fatal("closed history owns diff")
	}
	a.guardPushed(appL(), v.root, "HEAD", func() { t.Error("write confirmation opened") })
	a.askMessage(appL(), v.root, "poison", "HEAD", "", func(string) { t.Error("write prompt opened") })
	if a.err != "" || a.confirmDo != nil || a.promptDlg != nil {
		t.Fatal("legacy history helpers were not silent for a desktop target", a.err)
	}
}

func TestTargetGitLogLateResults(t *testing.T) {
	for _, method := range []string{"git.log", "git.commitFiles"} {
		for _, change := range []string{"close", "switch", "identity", "selection", "hide"} {
			t.Run(method+"/"+change, func(t *testing.T) {
				a, f, v, d := remoteLogFixture(t, 3)
				entered, release := make(chan struct{}), make(chan struct{})
				defer close(release)
				var calls atomic.Int32
				f.mu.Lock()
				normal := f.desktopCall
				f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
					if p.Method == method && calls.Add(1) == 1 {
						close(entered)
						<-release
					}
					return normal(ctx, p)
				}
				f.mu.Unlock()
				if method == "git.log" {
					a.reloadLog(v, d)
				} else {
					a.selectCommit(d.log.root, &d.log.commit, d.log.commits[0])
				}
				<-entered
				oldCV := d.log.commit
				switch change {
				case "close":
					a.closeLog(v, d)
				case "switch":
					views := newHostViews(a)
					views.add("other", &app{})
					views.activate("other")
				case "identity":
					id := v.git.identity
					id.RootInode++
					a.targetGitIdentity(v.git, id)
				case "selection":
					if method == "git.log" {
						d.log.filter.Text = "new"
						a.loadLog(v, d, false)
					} else {
						a.selectCommit(d.log.root, &d.log.commit, d.log.commits[1])
					}
				case "hide":
					a.setLogPanel(false)
				}
				// 服务仍阻塞时客户端必须已经因生命周期换代而取消。
				waitFileUpdate(t, a)
				a.runPending()
				if method == "git.commitFiles" && oldCV.files != nil {
					t.Fatal("stale commit applied")
				}
				if change == "selection" {
					settleTargetGit(t, a, func() bool {
						if method == "git.log" {
							return !d.log.loading
						}
						return !d.log.commit.loading
					})
					if method == "git.log" && len(d.log.commits) != 1 {
						t.Fatal("old log replaced new filter")
					}
					if method == "git.commitFiles" && d.log.commit.info.Hash != targetLogCommit(1).Hash {
						t.Fatal("old selection returned")
					}
				}
			})
		}
	}
}

func TestTargetGitLogProductionView(t *testing.T) {
	a, f, v, out := remoteGitFixture(t)
	targetGitLogHandler(t, f, out.Repos[0].Identity, 3)
	targetGitContentHandler(t, f, out.Repos[0].Identity, []byte("target revision\n"), []byte("target worktree\n"))
	p := previewApp(t)
	p.host, p.viewID = a.host, "remote"
	p.workspaces, p.projects, p.selected, p.files = a.workspaces, a.projects, a.selected, a.files
	p.indexWorkspaces()
	p.activeTerm = map[string]string{}
	p.state.LogPanels = map[string]bool{"p": true}
	a = p
	tt := newTester(a.build, 1200, 760)
	until(t, tt, "restored target log", func() bool { return tt.HasText("Target commit 0") })
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("Target commit 0"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "target commit files", func() bool { return v.logCur.log.commit != nil && len(v.logCur.log.commit.files) == 1 })
	if err := tt.Click("same.txt"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "target commit diff", func() bool { return v.cur != nil && v.cur.kind == docDiff })
	if v.cur.diff.state.New.String() != "target revision\n" {
		t.Fatal("production diff source")
	}
	m := locales.In("zh-Hans")
	if err := tt.Click(m.GitConsoleTitle()); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "target console", func() bool { return v.console && tt.HasText(m.FetchConsoleScope()) })
	if len(a.gitRuns) != 0 || a.consoleLines != 0 {
		t.Fatal("target console loaded local persistent history")
	}
	a.openFile(&a.workspaces[0], v.git.absPath("same.txt"), 0, [2]int{})
	until(t, tt, "target editor", func() bool { return v.cur != nil && v.cur.kind == docText })
	if err := tt.Click("文件历史"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "production file history", func() bool {
		return v.logCur.log.hist != nil && v.logCur.log.hist.diff != nil && v.logCur.log.hist.diff.kind == docDiff
	})
}

func TestPreviewTargetGitLog(t *testing.T) {
	dir := os.Getenv("POSSE_PREVIEW_DIR")
	if dir == "" {
		t.Skip("POSSE_PREVIEW_DIR not set")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, d := remoteLogFixture(t, 3)
		a.pollRepo(v.git, true)
		a.loadBranches(v.git)
		settleTargetGit(t, a, func() bool { return !v.git.poll.busy && !v.git.branches.loading })
		a.selectCommit(d.log.root, &d.log.commit, d.log.commits[0])
		settleTargetGit(t, a, func() bool { return !d.log.commit.loading })
		name := "light"
		if dark {
			name = "dark"
		}
		p := previewApp(t)
		p.host, p.viewID = a.host, "remote"
		p.workspaces, p.projects, p.selected, p.files = a.workspaces, a.projects, a.selected, a.files
		p.indexWorkspaces()
		p.activeTerm = map[string]string{}
		p.state.LogPanels = map[string]bool{"p": true}
		v.repos.request = p.request()
		savePNG(t, newPreviewTester(p, dark), "remote-git-log-"+name+".png")
		a.openLogIn(&a.workspaces[0], v.git, "", "same.txt")
		h := v.logCur
		settleTargetGit(t, a, func() bool { return !h.log.loading })
		h.log.hist = &fileHistory{layout: editor.DiffUnified, ignoreWS: true}
		a.selectHistory(v, h, h.log.commits[0])
		settleTargetGit(t, a, func() bool { return !h.log.hist.loading && h.log.hist.diff != nil && !h.log.hist.diff.diff.loading })
		v.repos.request = p.request()
		savePNG(t, newPreviewTester(p, dark), "remote-git-history-"+name+".png")
	}
}

func TestTargetGitLogDiffBases(t *testing.T) {
	for _, mode := range []string{"normal", "root", "shallow"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v, d := remoteLogFixture(t, 3)
			ci := targetLogCommit(0)
			base := ci.Parents[0]
			if mode != "normal" {
				base = ""
			}
			if mode == "root" {
				ci.Parents = nil
			}
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "git.commitFiles" {
					return rpc.GitCommitFilesResult{Identity: v.git.identity, Commit: ci, DiffBase: base, Shallow: mode == "shallow", Files: []gitx.FileChange{{Path: "same.txt", Status: 'M'}}}, nil
				}
				if p.Method == "git.file" {
					var q rpc.GitFileParams
					json.Unmarshal(p.Params, &q)
					if mode != "normal" && q.Revision == targetLogCommit(0).Parents[0] {
						t.Error("read unavailable parent")
					}
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.selectCommit(d.log.root, &d.log.commit, ci)
			settleTargetGit(t, a, func() bool { return !d.log.commit.loading })
			cv := d.log.commit
			if cv.err != "" || cv.diffSpecs()[0].oldRev != base || mode == "shallow" && len(cv.info.Parents) != 1 {
				t.Fatal("incorrect target diff base", cv.err)
			}
			_, open := a.tabDiffs(&a.workspaces[0], v, cv.diffSpecs())
			open(0)
			settleTargetGit(t, a, func() bool { return !v.cur.diff.loading })
			if v.cur.kind != docDiff {
				t.Fatal(v.cur.err)
			}
			a.openLogIn(&a.workspaces[0], v.git, "", "same.txt")
			h := v.logCur
			settleTargetGit(t, a, func() bool { return !h.log.loading })
			h.log.hist = &fileHistory{layout: editor.DiffUnified}
			a.selectHistory(v, h, ci)
			settleTargetGit(t, a, func() bool { return !h.log.hist.loading && h.log.hist.diff != nil && !h.log.hist.diff.diff.loading })
			if h.log.hist.diff.kind != docDiff || h.log.hist.diff.diff.oldRev != base {
				t.Fatal("history did not use target diff base", h.log.hist.err)
			}
		})
	}
}

func TestTargetGitLogHistoryCancellation(t *testing.T) {
	for _, method := range []string{"git.commitFiles", "git.blobRange"} {
		for _, action := range []string{"select", "close"} {
			t.Run(method+"/"+action, func(t *testing.T) {
				a, f, v, _ := remoteLogFixture(t, 3)
				a.openLogIn(&a.workspaces[0], v.git, "", "same.txt")
				d := v.logCur
				settleTargetGit(t, a, func() bool { return !d.log.loading })
				d.log.hist = &fileHistory{layout: editor.DiffUnified}
				h := d.log.hist
				entered, release := make(chan struct{}), make(chan struct{})
				defer close(release)
				var calls atomic.Int32
				f.mu.Lock()
				normal := f.desktopCall
				f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
					if p.Method == method && calls.Add(1) == 1 {
						close(entered)
						<-release
					}
					return normal(ctx, p)
				}
				f.mu.Unlock()
				a.selectHistory(v, d, d.log.commits[0])
				if method == "git.blobRange" {
					settleTargetGit(t, a, func() bool { return !h.loading && h.diff != nil })
				}
				<-entered
				old := h.diff
				if action == "select" {
					a.selectHistory(v, d, d.log.commits[1])
					settleTargetGit(t, a, func() bool { return !h.loading && h.diff != nil && h.diff.kind == docDiff })
					if h.diff.diff.newRev != targetLogCommit(1).Hash {
						t.Fatal("old history selection won")
					}
				} else {
					a.closeLog(v, d)
					waitFileUpdate(t, a)
					a.runPending()
				}
				if old != nil && v.ownsGitContent(old) {
					t.Fatal("old embedded diff retained ownership")
				}
			})
		}
	}
}

func TestTargetGitLogHiddenWhileDiscovering(t *testing.T) {
	a, f, v, out := remoteGitFixture(t)
	targetGitLogHandler(t, f, out.Repos[0].Identity, 3)
	entered, release := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.discover" {
			close(entered)
			<-release
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.openLog(&a.workspaces[0], "", "")
	<-entered
	a.setLogPanel(false)
	close(release)
	settleTargetGit(t, a, func() bool { return !v.repos.scanning })
	if v.logCur != nil || a.logPanelOpen() {
		t.Fatal("hidden log reopened after discovery")
	}
}

func TestTargetGitLogHistoryFailureRetainsDisplay(t *testing.T) {
	a, f, v, _ := remoteLogFixture(t, 3)
	a.openLogIn(&a.workspaces[0], v.git, "", "same.txt")
	d := v.logCur
	settleTargetGit(t, a, func() bool { return !d.log.loading })
	d.log.hist = &fileHistory{layout: editor.DiffUnified}
	a.selectHistory(v, d, d.log.commits[0])
	h := d.log.hist
	settleTargetGit(t, a, func() bool { return !h.loading && h.diff != nil && h.diff.kind == docDiff })
	old := h.diff
	var fail atomic.Bool
	fail.Store(true)
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.commitFiles" && fail.Load() {
			return nil, rpc.Errorf(rpc.CodeUnknown, "temporary failure")
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	a.selectHistory(v, d, d.log.commits[1])
	settleTargetGit(t, a, func() bool { return !h.loading })
	if h.err == "" || h.diff != old || old.diff.state.New.String() != "target revision\n" {
		t.Fatal("failed history cleared displayed content")
	}
	fail.Store(false)
	a.retryTargetHistory(v, d)
	settleTargetGit(t, a, func() bool { return !h.loading && h.diff != old && h.diff.kind == docDiff })
	if h.err != "" || h.diff.diff.newRev != targetLogCommit(1).Hash {
		t.Fatal("history retry failed")
	}
}

// 走实际浮层显示入口；只驱动读取状态的测试不能覆盖隐藏后的恢复。
func targetLogPanelTester(a *app, v *filesView) *ui.Tester {
	v.git.status = &gitx.Status{Head: "main"}
	v.git.poll.at = time.Now()
	v.git.branches.list = []gitx.Branch{}
	return newTester(func(c *ui.Context) {
		a.runPending()
		ui.Column(c).Grow(1).Children(func() {
			body := ui.Box(c).Grow(1)
			a.logPanel(c, &a.workspaces[0], body)
		})
	}, 1200, 760)
}

func TestTargetGitLogPanelResume(t *testing.T) {
	for _, tc := range []struct {
		name, method, action string
		history              bool
	}{
		{"log-hidden", "git.log", "hide", false},
		{"detail-hidden", "git.commitFiles", "hide", false},
		{"history-switched", "git.commitFiles", "switch", true},
		{"history-reconnected", "git.blobRange", "reconnect", true},
		{"history-closed", "git.commitFiles", "close", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, f, v, d := remoteLogFixture(t, 3)
			if tc.history {
				a.openLogIn(&a.workspaces[0], v.git, "", "same.txt")
				d = v.logCur
				settleTargetGit(t, a, func() bool { return !d.log.loading })
				d.log.hist = &fileHistory{layout: editor.DiffUnified}
			}
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
			switch {
			case tc.history:
				a.selectHistory(v, d, d.log.commits[0])
			case tc.method == "git.log":
				a.reloadLog(v, d)
			default:
				a.selectCommit(d.log.root, &d.log.commit, d.log.commits[0])
			}
			if tc.method == "git.blobRange" {
				settleTargetGit(t, a, func() bool { return !d.log.hist.loading && d.log.hist.diff != nil })
			}
			<-entered
			tt := targetLogPanelTester(a, v)
			tt.Frame()
			tt.Advance(300 * time.Millisecond)
			tt.Frame()
			switch tc.action {
			case "hide":
				a.setLogPanel(false)
				tt.Frame()
				a.toggleLogPanel(&a.workspaces[0])
			case "switch":
				a.openLogIn(&a.workspaces[0], v.git, "", "")
				tt.Frame()
				if err := tt.Click(locales.In("zh-Hans").LogHistoryTab("same.txt")); err != nil {
					t.Fatal(err)
				}
			case "reconnect":
				old := a.request()
				a.host.detach()
				tt.Frame()
				if err := a.host.connect(context.Background(), old.client); err != nil {
					t.Fatal(err)
				}
			case "close":
				a.closeLog(v, d)
				waitFileUpdate(t, a)
				for range 3 {
					tt.Frame()
				}
				if calls.Load() != 1 || v.logCur == d {
					t.Fatal("closed history restarted")
				}
				return
			}
			until(t, tt, "resumed visible target log", func() bool {
				if calls.Load() < 2 {
					return false
				}
				if tc.history {
					h := d.log.hist
					return h != nil && !h.loading && h.diff != nil && h.diff.kind == docDiff && !h.diff.diff.loading
				}
				if tc.method == "git.log" {
					return !d.log.loading && d.log.err == ""
				}
				return d.log.commit != nil && !d.log.commit.loading && len(d.log.commit.files) == 1
			})
		})
	}
}

func TestTargetGitLogPanelResumesPendingFilter(t *testing.T) {
	a, _, v, d := remoteLogFixture(t, 3)
	tt := targetLogPanelTester(a, v)
	tt.Frame()
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	ld := d.log
	ld.text, ld.author = "needle", "Target"
	a.applyLogFilter(v, d)
	// 正常绘帧仍保留防抖，不提前应用。
	tt.Frame()
	if ld.filter.Text != "" {
		t.Fatal("visible panel bypassed filter debounce")
	}
	a.setLogPanel(false)
	tt.Frame()
	a.toggleLogPanel(&a.workspaces[0])
	until(t, tt, "resumed pending filter", func() bool {
		return !ld.loading && ld.filter.Text == "needle" && ld.filter.Author == "Target" && len(ld.commits) == 1
	})
	if ld.filterPending {
		t.Fatal("filter pending flag remained")
	}
}
