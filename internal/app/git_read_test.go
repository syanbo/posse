package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

func remoteGitFixture(t *testing.T) (*app, *hostControlFixture, *filesView, rpc.GitDiscoverResult) {
	t.Helper()
	a, f, path, stamp := remoteFileFixture(t, "target contents")
	root := filepath.Dir(path)
	id := gitx.RepositoryIdentity{Root: root, GitDir: filepath.Join(root, ".git"), CommonDir: filepath.Join(root, ".git"), RootInode: 1, GitInode: 2, CommonInode: 2}
	discovery := rpc.GitDiscoverResult{RealPath: root, Repos: []rpc.GitRepository{{Root: root, Real: root, Identity: id}}}
	a.workspaces = []workspace{{ID: "w", ProjectID: "p", Name: "main", Path: root}}
	a.projects = []project{{ID: "p", Name: "remote", Path: root}}
	a.selected = "w"
	a.activeTerm = map[string]string{}
	a.indexWorkspaces()
	v := &filesView{wsID: "w", root: root, remote: true, tree: files.NewTree(root)}
	newGitStates(v)
	a.files = map[string]*filesView{"w": v}
	f.mu.Lock()
	fileCall := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.discover":
			return discovery, nil
		case "git.status":
			var args rpc.GitStatusParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			out := rpc.GitStatusResult{Identity: id, Status: &gitx.Status{Head: "target-main", Commit: strings.Repeat("a", 40), Ahead: 2, Behind: 1, Entries: []gitx.Entry{{Path: "same.txt", Y: 'M'}}}}
			if args.Full {
				out.Head = map[string]gitx.LineStats{"same.txt": {Added: 3, Deleted: 1}}
				out.Stashes = []gitx.Stash{{Ref: "stash@{0}", Hash: strings.Repeat("b", 40), Message: "On target-main: target stash", Branch: "target-main", When: time.Now()}}
			}
			return out, nil
		case "git.branches":
			return rpc.GitBranchesResult{Identity: id, Branches: []gitx.Branch{{Name: "target-main", Current: true}, {Name: "feature-target", Worktree: filepath.Join(root, "other-worktree")}}, Tags: []string{"v-target"}, Recent: []string{"target-main"}}, nil
		case "files.readDirs":
			return []rpc.FileDirectoryResult{{Path: root, Entries: []files.Entry{{Name: "same.txt"}}, Stamp: stamp}}, nil
		case "files.stat":
			var args rpc.FilePathsParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			var out []rpc.FileStatResult
			for _, path := range args.Paths {
				out = append(out, rpc.FileStatResult{Path: path, Stamp: stamp})
			}
			return out, nil
		case "files.list":
			return files.List{Root: root, Files: []string{"same.txt"}}, nil
		}
		return fileCall(ctx, p)
	}
	f.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref: refs/heads/local-poison\n"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	marker := filepath.Join(bin, "called")
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$POSSE_TEST_GIT_POISON\"\nexit 97\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POSSE_TEST_GIT_POISON", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Cleanup(func() {
		v.cancelGitReads()
		if got, err := os.ReadFile(marker); err == nil {
			t.Errorf("target Git reached local executable: %s", got)
		} else if !os.IsNotExist(err) {
			t.Error(err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "local contents" {
			t.Errorf("local poison changed: %q %v", got, err)
		}
	})
	return a, f, v, discovery
}

func settleTargetGit(t *testing.T, a *app, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.runPending()
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("target Git did not settle: %s", a.err)
}

func discoverTargetGit(t *testing.T, a *app, v *filesView) *gitState {
	t.Helper()
	a.scanRepos(v)
	settleTargetGit(t, a, func() bool { return v.repos.scanned && !v.repos.scanning })
	return v.git
}

func TestTargetGitReadsAndRealPath(t *testing.T) {
	a, f, v, discovery := remoteGitFixture(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(v.root, alias); err != nil {
		t.Fatal(err)
	}
	v.root = alias
	a.workspaces[0].Path = alias
	discovery.RealPath = filepath.Join(discovery.Repos[0].Root, "remote-sub")
	discovery.Repos[0].Aliases = []string{filepath.Join(t.TempDir(), "repo-alias")}
	f.mu.Lock()
	normal := f.desktopCall
	var full, light atomic.Int32
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.discover" {
			var args rpc.GitDiscoverParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			if args.Path != alias {
				t.Errorf("discovery path = %q", args.Path)
			}
			return discovery, nil
		}
		if p.Method == "git.status" {
			var args rpc.GitStatusParams
			json.Unmarshal(p.Params, &args)
			if args.Full {
				full.Add(1)
			} else {
				light.Add(1)
			}
		}
		return normal(ctx, p)
	}
	f.mu.Unlock()
	g := discoverTargetGit(t, a, v)
	if v.realRoot != discovery.RealPath || g != v.repoFor(filepath.Join(alias, "same.txt")) || g != v.repoFor(filepath.Join(g.aliases[0], "same.txt")) {
		t.Fatalf("remote alias mapping: real=%s g=%+v", v.realRoot, g)
	}
	if got := g.relPath(filepath.Join(alias, "same.txt")); got != "remote-sub/same.txt" {
		t.Fatalf("relative subdirectory path = %q", got)
	}
	if a.workspaceAt(g.repo) != nil || a.workspaceAt(v.realRoot) != &a.workspaces[0] {
		t.Fatal("workspace matching followed the local symlink instead of target RealPath")
	}
	a.pollRepo(g, true)
	settleTargetGit(t, a, func() bool { return !g.poll.busy })
	if got := a.gitBranch(&a.workspaces[0]); got != "target-main" {
		t.Fatalf("branch = %q", got)
	}
	if v.repoBranch(g.repo) != "target-main" || g.head["same.txt"].Added != 3 || len(g.stashes) != 1 || len(v.treeMarks()) == 0 {
		t.Fatalf("incomplete target state: %+v", g.status)
	}
	a.pollRepo(g, false)
	settleTargetGit(t, a, func() bool { return !g.poll.busy })
	if full.Load() != 1 || light.Load() != 1 || len(g.stashes) != 1 || len(g.head) != 1 {
		t.Fatal("non-full poll discarded full data")
	}
	a.loadBranches(g)
	settleTargetGit(t, a, func() bool { return !g.branches.loading })
	if len(g.branches.list) != 2 || !slices.Equal(g.branches.tags, []string{"v-target"}) || !slices.Equal(g.branches.recent, []string{"target-main"}) {
		t.Fatalf("branches = %+v", g.branches)
	}
}

func TestTargetGitProductionViews(t *testing.T) {
	a, f, v, discovery := remoteGitFixture(t)
	targetGitContentHandler(t, f, discovery.Repos[0].Identity, []byte("before\n"), []byte("after\n"))
	targetGitLogHandler(t, f, discovery.Repos[0].Identity, 3)
	p := previewApp(t)
	p.host, p.viewID = a.host, "remote"
	p.workspaces, p.projects, p.selected, p.files = a.workspaces, a.projects, a.selected, a.files
	p.indexWorkspaces()
	p.activeTerm = map[string]string{}
	p.state.LogPanels = map[string]bool{"p": true}
	a = p
	tt := newTester(a.build, 1200, 760)
	until(t, tt, "remote branch button", func() bool { _, ok := tt.Find("分支 target-main"); return ok })
	a.err = ""
	for range 3 {
		tt.Frame()
	}
	until(t, tt, "saved target log preference", func() bool { return tt.HasText("Target commit 0") })
	a.setLogPanel(false)
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if err := tt.Click("分支 target-main"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "remote branch popup", func() bool { return tt.HasText("feature-target") && tt.HasText("v-target") })
	if !tt.HasText(locales.In("zh-Hans").FetchManual()) || a.fetchWritable(v) {
		t.Fatal("viewer fetch entry missing or writable")
	}
	for _, unwanted := range []string{"更新项目", "Push…", "新建分支…"} {
		if tt.HasText(unwanted) {
			t.Fatalf("remote popup exposes %s", unwanted)
		}
	}
	if err := tt.Click("feature-target"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("在访达中显示 worktree") || tt.HasText("检出") {
		t.Fatal("remote branch menu exposed local actions")
	}
	a.branchPop = nil
	a.openFilesPanel(&a.workspaces[0], sideChanges)
	until(t, tt, "remote changes", func() bool { return tt.HasText("same.txt") && a.filesPanelShown == 1 })
	if tt.HasText("提交信息") || tt.HasText("修改上一次提交") {
		t.Fatal("remote changes exposed commit editor")
	}
	if err := tt.Click("same.txt"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "target diff", func() bool { return v.cur != nil && v.cur.kind == docDiff })
	if v.cur.diff.state.Old.String() != "before\n" || v.cur.diff.state.New.String() != "after\n" {
		t.Fatal("diff did not show target contents")
	}
	if tt.HasText(appL().GitRollbackFile()) || v.cur.diff.state.HunkActions || v.cur.diff.state.RenderHunkMark != nil || v.cur.diff.state.RenderHunkActions != nil {
		t.Fatal("remote diff exposed write actions")
	}
	v.ch.tab = 1
	tt.Frame()
	if !tt.HasText("target stash") {
		t.Fatalf("stash data missing: %v", tt.Texts())
	}
	if err := tt.Click("On target-main: target stash"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if tt.HasText("弹出") || tt.HasText("应用") || a.confirmDo != nil {
		t.Fatal("read-only stash exposed write confirmation")
	}
}

func TestTargetGitWriteAndReadGuards(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		t.Run(map[bool]string{false: "viewer", true: "controlled"}[controlled], func(t *testing.T) {
			a, _, v, _ := remoteGitFixture(t)
			if controlled {
				takeRequestTestControl(t, a)
			}
			g := discoverTargetGit(t, a, v)
			a.pollRepo(g, true)
			settleTargetGit(t, a, func() bool { return !g.poll.busy })
			var ran atomic.Bool
			op := gitOp{m: appL(), what: "poison", fn: func(context.Context, string) error { ran.Store(true); return nil }}
			a.gitStart(g, op)
			a.gitRun(g, op)
			assertRemoteFileUnsupported(t, a.err, "git.write")
			if ran.Load() || g.busy != "" || len(g.queue) != 0 {
				t.Fatal("remote Git closure executed")
			}
			a.autoFetch(nil, &a.workspaces[0], g.repo, &trackInfo{})
			if len(a.fetchAt) != 0 {
				t.Fatal("remote auto fetch changed state")
			}
			d := &fileDoc{path: filepath.Join(v.root, "same.txt"), kind: docText, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			a.loadHead(v, d)
			a.gitChanged(g, nil)
			if d.headFor != "" || d.headBuf != nil {
				t.Fatal("read local HEAD contents")
			}
			for _, item := range []struct {
				method string
				run    func()
			}{
				{"git.write", func() { a.doPush(appL(), v, &pushState{remote: true}, false) }},
				{"git.write", func() { a.doPush(appL(), v, &pushState{remote: true}, true) }},
				{"git.write", func() { a.askNewBranch(appL(), &a.workspaces[0], v, "") }},
				{"git.write", func() { a.checkoutBranch(appL(), &a.workspaces[0], v, gitx.Branch{Name: "other"}) }},
				{"git.write", func() { a.openRollback(v) }},
				{"git.show", func() { a.setAmend(v, true) }},
				{"git.write", func() { a.syncBase(appL(), &a.workspaces[0], false) }},
			} {
				a.err = ""
				item.run()
				assertRemoteFileUnsupported(t, a.err, item.method)
			}
			if a.confirmDo != nil || a.promptDlg != nil || len(v.docs) != 0 || len(v.logs) != 0 {
				t.Fatal("unsupported operation prepared a local callback")
			}
		})
	}
}

func TestTargetGitFailuresPreserveAndRetry(t *testing.T) {
	a, f, v, _ := remoteGitFixture(t)
	g := discoverTargetGit(t, a, v)
	a.pollRepo(g, true)
	settleTargetGit(t, a, func() bool { return !g.poll.busy })
	a.loadBranches(g)
	settleTargetGit(t, a, func() bool { return !g.branches.loading })
	status := g.status
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		return nil, rpc.Errorf(rpc.CodePermissionDenied, "temporarily unavailable")
	}
	f.mu.Unlock()
	a.scanRepos(v)
	settleTargetGit(t, a, func() bool { return !v.repos.scanning })
	a.pollRepo(g, true)
	settleTargetGit(t, a, func() bool { return !g.poll.busy })
	a.loadBranches(g)
	settleTargetGit(t, a, func() bool { return !g.branches.loading })
	if v.git != g || g.status != status || len(g.stashes) != 1 || len(g.branches.list) != 2 || a.err == "" {
		t.Fatal("failed read discarded valid state or error")
	}
	f.mu.Lock()
	f.desktopCall = normal
	f.mu.Unlock()
	a.scanRepos(v)
	settleTargetGit(t, a, func() bool { return !v.repos.scanning })
	a.pollRepo(g, true)
	settleTargetGit(t, a, func() bool { return !g.poll.busy })
	a.loadBranches(g)
	settleTargetGit(t, a, func() bool { return !g.branches.loading })
	if g.status == status || g.err != "" || len(g.branches.list) != 2 {
		t.Fatal("retry did not replace state")
	}
}

func TestTargetGitLateResults(t *testing.T) {
	for _, method := range []string{"git.discover", "git.status", "git.branches"} {
		for _, mode := range []string{"identity", "switch", "close", "replace", "reverse"} {
			t.Run(method+"/"+mode, func(t *testing.T) {
				a, f, v, _ := remoteGitFixture(t)
				g := discoverTargetGit(t, a, v)
				f.mu.Lock()
				normal := f.desktopCall
				entered, release := make(chan struct{}, 1), make(chan struct{})
				var calls atomic.Int32
				f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
					out, err := normal(ctx, p)
					if p.Method == method && calls.Add(1) == 1 {
						entered <- struct{}{}
						<-release
					}
					return out, err
				}
				f.mu.Unlock()
				t.Cleanup(func() {
					select {
					case <-release:
					default:
						close(release)
					}
				})
				start := func() {
					switch method {
					case "git.discover":
						a.scanRepos(v)
					case "git.status":
						a.pollRepo(g, true)
					case "git.branches":
						a.loadBranches(g)
					}
				}
				start()
				select {
				case <-entered:
				case <-time.After(5 * time.Second):
					t.Fatal("read did not enter")
				}
				switch mode {
				case "identity":
					takeRequestTestControl(t, a)
				case "switch":
					views := newHostViews(a)
					views.add("other", &app{})
					if !views.activate("other") {
						t.Fatal("host switch failed")
					}
				case "close":
					a.closeFilesPanel()
				case "replace":
					delete(a.files, v.wsID)
					v.cancelGitReads()
				case "reverse":
					close(release)
					waitFileUpdate(t, a)
					a.pendingMu.Lock()
					older := a.pending
					a.pending = nil
					a.pendingMu.Unlock()
					if method == "git.discover" {
						v.repos.read.stop()
						v.repos.scanning = false
					}
					start()
					settleTargetGit(t, a, func() bool {
						switch method {
						case "git.discover":
							return !v.repos.scanning
						case "git.status":
							return !g.poll.busy
						default:
							return !g.branches.loading
						}
					})
					g.status = &gitx.Status{Head: "newest"}
					g.branches.list = []gitx.Branch{{Name: "newest"}}
					if method == "git.discover" {
						v.realRoot = "/newest"
					}
					a.pendingMu.Lock()
					a.pending = append(a.pending, older...)
					a.pendingMu.Unlock()
				}
				if mode != "reverse" {
					// 服务尚未回复时客户端就应因身份或视图失效而结束读取。
					waitFileUpdate(t, a)
					close(release)
				}
				a.runPending()
				if mode == "reverse" {
					if g.status.Head != "newest" || g.branches.list[0].Name != "newest" || method == "git.discover" && v.realRoot != "/newest" {
						t.Fatal("old response overwrote new state")
					}
				} else if g.status != nil || len(g.branches.list) != 0 {
					t.Fatal("stale response populated state")
				}
			})
		}
	}
}

func TestTargetGitRepositoryIdentityReplacement(t *testing.T) {
	for _, method := range []string{"git.discover", "git.status", "git.branches"} {
		t.Run(method, func(t *testing.T) {
			a, f, v, discovery := remoteGitFixture(t)
			g := discoverTargetGit(t, a, v)
			g.status = &gitx.Status{Head: "old"}
			g.picks = map[string]bool{"same.txt": true}
			g.autostash = "old"
			discovery.Repos[0].Identity.RootInode++
			id := discovery.Repos[0].Identity
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "git.discover":
					return discovery, nil
				case "git.status":
					return rpc.GitStatusResult{Identity: id, Status: &gitx.Status{Head: "new"}}, nil
				case "git.branches":
					return rpc.GitBranchesResult{Identity: id, Branches: []gitx.Branch{{Name: "new"}}}, nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			switch method {
			case "git.discover":
				a.scanRepos(v)
			case "git.status":
				a.pollRepo(g, true)
			case "git.branches":
				a.loadBranches(g)
			}
			settleTargetGit(t, a, func() bool { return v.git != g && v.repos.scanned && !v.repos.scanning })
			if v.git.identity != id || v.git.status != nil || len(v.git.picks) != 0 || v.git.autostash != "" {
				t.Fatal("same-root replacement reused old state")
			}
			var ran bool
			a.gitStart(g, gitOp{m: appL(), what: "old confirmation", fn: func(context.Context, string) error { ran = true; return nil }})
			if ran {
				t.Fatal("old confirmation used replacement repository")
			}
		})
	}
}

func TestTargetGitReconnectRetry(t *testing.T) {
	a, _, v, _ := remoteGitFixture(t)
	g := discoverTargetGit(t, a, v)
	old := a.request()
	a.host.detach()
	if err := a.host.connect(context.Background(), old.client); err != nil {
		t.Fatal(err)
	}
	v.repos.scanning = true
	g.poll.busy = true
	g.branches.loading = true
	a.openBranches(&a.workspaces[0])
	settleTargetGit(t, a, func() bool {
		return v.repos.scanned && !v.repos.scanning && !v.git.branches.loading && len(v.git.branches.list) == 2
	})
	a.refreshRepo(v.git)
	settleTargetGit(t, a, func() bool { return !v.git.poll.busy && v.git.status != nil })
	if old.current() || v.git.status.Head != "target-main" {
		t.Fatal("reconnected read retained stale request")
	}
}

func TestPreviewTargetGit(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a, _, v, _ := remoteGitFixture(t)
		p := previewApp(t)
		p.host, p.viewID = a.host, "remote"
		p.workspaces, p.projects, p.selected, p.files = a.workspaces, a.projects, a.selected, a.files
		p.indexWorkspaces()
		p.activeTerm = map[string]string{}
		a = p
		tt := newTester(a.build, 1200, 760)
		a.openFilesPanel(&a.workspaces[0], sideChanges)
		until(t, tt, "remote changes", func() bool { return v.git.status != nil })
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "remote-git-changes-"+suffix+".png")
		v.ch.tab = 1
		savePNG(t, newPreviewTester(a, dark), "remote-git-stashes-"+suffix+".png")
		a.openBranches(&a.workspaces[0])
		until(t, tt, "remote branches", func() bool { return len(v.git.branches.list) > 0 })
		savePNG(t, newPreviewTester(a, dark), "remote-git-branches-"+suffix+".png")
	}
}

func TestTargetGitNewIdentityInvalidatesOlderDiscovery(t *testing.T) {
	a, f, v, old := remoteGitFixture(t)
	g := discoverTargetGit(t, a, v)
	newID := g.identity
	newID.GitInode++
	newDiscovery := rpc.GitDiscoverResult{RealPath: old.RealPath, Repos: []rpc.GitRepository{{Root: g.repo, Real: g.real, Identity: newID}}}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var discovers atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "git.discover":
			if discovers.Add(1) == 1 {
				entered <- struct{}{}
				<-release
				return old, nil
			}
			return newDiscovery, nil
		case "git.status":
			return rpc.GitStatusResult{Identity: newID, Status: &gitx.Status{Head: "replacement"}}, nil
		}
		return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
	}
	f.mu.Unlock()
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	a.scanRepos(v)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery did not start")
	}
	a.pollRepo(g, true)
	settleTargetGit(t, a, func() bool { return v.git != g && v.repos.scanned && !v.repos.scanning })
	if discovers.Load() != 2 || v.git.identity != newID {
		t.Fatal("identity change did not replace the pending discovery")
	}
	close(release)
	a.runPending()
	if v.git.identity != newID {
		t.Fatal("old discovery revived old repository")
	}
}

func TestTargetGitOldResultCannotClearNewBusy(t *testing.T) {
	for _, method := range []string{"git.discover", "git.status", "git.branches"} {
		t.Run(method, func(t *testing.T) {
			a, f, v, _ := remoteGitFixture(t)
			g := discoverTargetGit(t, a, v)
			start := func() {
				switch method {
				case "git.discover":
					a.scanRepos(v)
				case "git.status":
					a.pollRepo(g, true)
				case "git.branches":
					a.loadBranches(g)
				}
			}
			start()
			waitFileUpdate(t, a)
			a.pendingMu.Lock()
			older := a.pending
			a.pending = nil
			a.pendingMu.Unlock()
			entered, release := make(chan struct{}, 1), make(chan struct{})
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == method {
					entered <- struct{}{}
					<-release
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			t.Cleanup(func() {
				select {
				case <-release:
				default:
					close(release)
				}
			})
			if method == "git.discover" {
				v.repos.read.stop()
				v.repos.scanning = false
			}
			start()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("new read did not start")
			}
			for _, fn := range older {
				fn()
			}
			var busy bool
			switch method {
			case "git.discover":
				busy = v.repos.scanning
			case "git.status":
				busy = g.poll.busy
			case "git.branches":
				busy = g.branches.loading
			}
			if !busy {
				t.Fatal("old response cleared new read's busy flag")
			}
			close(release)
			waitFileUpdate(t, a)
			a.runPending()
		})
	}
}

func TestTargetGitBranchWorktreeIgnoresLocalSymlink(t *testing.T) {
	a, _, v, _ := remoteGitFixture(t)
	g := discoverTargetGit(t, a, v)
	localOther := t.TempDir()
	alias := filepath.Join(t.TempDir(), "repository-link")
	if err := os.Symlink(localOther, alias); err != nil {
		t.Fatal(err)
	}
	g.repo, g.real = alias, "/target/repository"
	g.branches.list = []gitx.Branch{{Name: "target-main", Current: true}, {Name: "other", Worktree: localOther}}
	a.branchPop = &branchPopup{ws: v.wsID, hl: -1}
	tt := newTester(func(c *ui.Context) { a.branchPopupView(c, &a.workspaces[0]) }, 800, 600)
	g.branches.at = time.Now()
	tt.Frame()
	if !tt.HasText("worktree · " + filepath.Base(localOther)) {
		t.Fatalf("branch resolved a target path through the local symlink: %v", tt.Texts())
	}
}
