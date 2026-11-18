package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
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

func pathsResultFixture(p rpc.GitPathsParams, status string) rpc.GitPathsResult {
	out := rpc.GitPathsResult{Target: p.Metadata(), Status: status, Started: status != rpc.GitFetchConflict, Logs: []gitx.Run{}}
	if status != rpc.GitFetchSucceeded {
		code := rpc.CodeInternal
		if status == rpc.GitFetchConflict {
			code = rpc.CodeConflict
		}
		if status == rpc.GitFetchCanceled {
			code = "canceled"
		}
		if status == rpc.GitFetchTimedOut {
			code = rpc.CodeTimeout
		}
		out.Error = rpc.Errorf(code, "test result")
	}
	if out.Started {
		for i, args := range rpc.GitPathsCommands(p) {
			out.Logs = append(out.Logs, gitx.Run{ID: int64(i + 1), OpID: 1, Op: "paths", Dir: p.Expected.Root, Args: args, Start: time.Now(), Done: true})
		}
	}
	if p.Action == rpc.GitPathsWrite && status == rpc.GitFetchSucceeded {
		v := *p.Paths[0].Version
		v.Hash = p.Hash
		v.Stamp.Ino++
		out.Version = &v
	}
	return out
}
func pathsDoneFixture(ref rpc.DesktopOperationRef, out rpc.GitPathsResult) rpc.DesktopOperation {
	raw, _ := json.Marshal(out)
	return rpc.DesktopOperation{DesktopOperationRef: ref, Method: "git.paths", State: "done", StartedAt: 1, FinishedAt: 2, Result: raw}
}
func pathsAppFixture(t *testing.T, controlled bool, handler func(rpc.GitPathsParams) (any, error)) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f, v, _ := remoteGitFixture(t)
	preview := previewApp(t)
	preview.host, preview.viewID = a.host, "remote"
	preview.workspaces, preview.projects, preview.selected, preview.files = a.workspaces, a.projects, a.selected, a.files
	preview.indexWorkspaces()
	preview.activeTerm = map[string]string{}
	a = preview

	if controlled {
		takeRequestTestControl(t, a)
	}
	g := discoverTargetGit(t, a, v)
	g.status = &gitx.Status{Commit: strings.Repeat("a", 40), Entries: []gitx.Entry{{Path: "same.txt", X: '.', Y: 'M'}}}
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "git.paths" {
			var q rpc.GitPathsParams
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
func TestTargetGitPathsActions(t *testing.T) {
	for _, controlled := range []bool{false, true} {
		for _, action := range []string{rpc.GitPathsStage, rpc.GitPathsResolve, rpc.GitPathsTake, rpc.GitPathsUnstage, rpc.GitPathsRollback} {
			t.Run(action+map[bool]string{true: "-control", false: "-viewer"}[controlled], func(t *testing.T) {
				var calls atomic.Int32
				a, _, v := pathsAppFixture(t, controlled, func(p rpc.GitPathsParams) (any, error) {
					calls.Add(1)
					if p.Action != action {
						t.Errorf("action %s", p.Action)
					}
					return pathsResultFixture(p, rpc.GitFetchSucceeded), nil
				})
				g := v.git
				e := gitx.Entry{Path: "same.txt", X: '.', Y: 'M'}
				switch action {
				case rpc.GitPathsStage:
					e.Untracked = true
					e.X = '?'
					e.Y = '?'
				case rpc.GitPathsResolve, rpc.GitPathsTake:
					e.Conflict = true
					e.X = 'U'
					e.Y = 'U'
				case rpc.GitPathsUnstage:
					e.X = 'M'
				}
				g.status.Entries = []gitx.Entry{e}
				before, _ := os.ReadFile(filepath.Join(v.root, "same.txt"))
				if action == rpc.GitPathsRollback {
					a.openRollback(v)
					if a.rollback.open != controlled || calls.Load() != 0 || a.err != "" {
						t.Fatal("rollback confirmation guard", a.err)
					}
					if controlled {
						tt := newTester(func(c *ui.Context) { a.runPending(); a.rollbackDialog(c) }, 800, 600)
						tt.Frame()
						if err := tt.Click(locales.In("zh-Hans").RollbackFilesButton(1)); err != nil {
							t.Fatal(err)
						}
					}
				} else {
					a.pathAction(appL(), g, action, action, e, true && action == rpc.GitPathsTake)
				}
				if controlled {
					pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) == 1 && a.targetPaths[0].done })
					if calls.Load() != 1 {
						t.Fatal("write call count", calls.Load(), a.err)
					}
				} else {
					a.runPending()
					if calls.Load() != 0 || len(a.targetPaths) != 0 || a.err != "" {
						t.Fatal("viewer wrote", a.err)
					}
				}
				after, _ := os.ReadFile(filepath.Join(v.root, "same.txt"))
				if string(before) != string(after) {
					t.Fatal("remote path touched local disk")
				}
			})
		}
	}
}
func TestTargetGitPathsHunk(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "rpc-reject", "stamp", "viewer", "disconnect"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v, g, d := targetContentDiffFixture(t)
			if mode != "viewer" {
				takeRequestTestControl(t, a)
			}
			g.status.Entries = []gitx.Entry{{Path: "same.txt", X: '.', Y: 'M'}}
			df := d.diff
			old := df.state.New.String()
			stamp := df.stamp
			version := fileSaveVersion("/target/same.txt", old)
			version.Stamp = stamp
			var writes, versions atomic.Int32
			var written atomic.Bool
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.stat":
					if written.Load() {
						var q rpc.FilePathsParams
						json.Unmarshal(p.Params, &q)
						newStamp := stamp
						newStamp.Ino++
						return []rpc.FileStatResult{{Path: q.Paths[0], Stamp: newStamp}}, nil
					}
				case "files.version":
					versions.Add(1)
					v := version
					if mode == "stamp" {
						v.Stamp.Ino++
					}
					return v, nil
				case "git.paths":
					writes.Add(1)
					if mode == "rpc-reject" {
						return nil, rpc.Errorf(rpc.CodeConflict, "rejected request")
					}
					var q rpc.GitPathsParams
					json.Unmarshal(p.Params, &q)
					if q.Action != rpc.GitPathsWrite || string(q.Data) != "before\n" || q.Paths[0].Version == nil || !q.Paths[0].Version.Same(version) {
						t.Errorf("write %+v", q)
					}
					if mode == "disconnect" {
						close(entered)
						<-release
					}
					status := rpc.GitFetchSucceeded
					if mode == "conflict" {
						status = rpc.GitFetchConflict
					}
					if status == rpc.GitFetchSucceeded {
						written.Store(true)
					}
					return pathsResultFixture(q, status), nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			// 编辑器缓冲区与逐段回滚各自保存版本，不能顺便把旧缓冲区标成已保存。
			ed := &fileDoc{path: df.abs(), kind: docText, baseVersion: version, base: stamp, ed: editor.State{Buffer: editor.NewBuffer(old)}}
			v.docs = append(v.docs, ed)
			a.revertHunk(appL(), v, d, 0)
			switch mode {
			case "viewer":
				a.runPending()
				if writes.Load() != 0 || versions.Load() != 0 || a.err != "" {
					t.Fatal("viewer sent hunk operation")
				}
			case "stamp":
				pumpSaves(t, []*app{a}, func() bool { return a.err != "" })
				if writes.Load() != 0 {
					t.Fatal("changed stamp sent")
				}
			case "disconnect":
				pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) > 0 })
				waitConnectionEvent(t, entered)
				a.host.detach()
				a.runPending()
				release <- struct{}{}
				pumpSaves(t, []*app{a}, func() bool { return a.targetPaths[0].done })
				if df.state.New.String() != old {
					t.Fatal("disconnected response changed diff")
				}
			default:
				pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) > 0 && a.targetPaths[0].done })
				if writes.Load() != 1 || a.targetPaths[0].unknown {
					t.Fatal("hunk count", a.err)
				}
				if mode == "success" {
					if df.state.New.String() != "before\n" || df.stamp.Ino != stamp.Ino+1 {
						t.Fatalf("successful hunk text=%q stamp=%+v want=%+v kind=%v seq=%d result=%+v err=%s", df.state.New.String(), df.stamp, stamp, d.kind, df.contentSeq, a.targetPaths[0].result, a.err)
					}
				} else {
					if df.state.New.String() != old {
						t.Fatal("rejected hunk retained")
					}
					pumpSaves(t, []*app{a}, func() bool { return !df.loading })
				}
			}
			if !ed.baseVersion.Same(version) || ed.ed.Buffer.String() != old {
				t.Fatal("editor marked saved by hunk")
			}
		})
	}
}
func TestTargetGitPathsUnknownQueryAndCancel(t *testing.T) {
	var calls, cancels, completed atomic.Int32
	a, f, v := pathsAppFixture(t, true, func(p rpc.GitPathsParams) (any, error) {
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	})
	g := v.git
	e := gitx.Entry{Path: "same.txt", Untracked: true, X: '?', Y: '?'}
	g.status.Entries = []gitx.Entry{e}
	target := a.gitTarget(&a.workspaces[0], v, g)
	p := rpc.GitPathsParams{GitFetchParams: target.params, Action: rpc.GitPathsStage, Head: g.status.Commit, Paths: []rpc.GitPath{{Entry: e}}}
	a.startTargetPaths(appL(), target, "Stage", p, nil, func(rpc.GitPathsResult) { completed.Add(1) })
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) > 0 && a.targetPaths[0].unknown })
	s := a.targetPaths[0]
	if g.busyPaths != s {
		t.Fatal("unknown released repo")
	}
	tt := newTester(func(c *ui.Context) { a.gitConsole(c, &a.workspaces[0], v) }, 800, 600)
	tt.Frame()
	if v.consoleLast != s.key() || v.consoleDone {
		t.Fatal("unknown operation missing from console")
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
			t.Error("cancel changed identity/ref")
		}
		return rpc.DesktopOperation{DesktopOperationRef: s.ref, Method: "git.paths", State: "running", StartedAt: 1}, nil
	}
	f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
		if ref != s.ref {
			t.Error("query changed ref")
		}
		return pathsDoneFixture(ref, pathsResultFixture(s.params, rpc.GitFetchSucceeded)), nil
	}
	f.mu.Unlock()
	a.cancelPaths(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return !s.canceling })
	if s.done || g.busyPaths != s || cancels.Load() != 1 {
		t.Fatal("cancel receipt treated as completion")
	}
	a.queryPaths(appL(), s)
	pumpSaves(t, []*app{a}, func() bool { return s.done })
	if calls.Load() != 1 || g.busy != "" || completed.Load() != 0 || !reflect.DeepEqual(s.params, p) {
		t.Fatal("query resent or navigated disconnected request")
	}
}
func TestPreviewTargetGitPaths(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("set POSSE_PREVIEW_DIR to render previews")
	}
	for _, dark := range []bool{false, true} {
		for _, mode := range []string{"viewer", "rollback", "unknown"} {
			t.Run(mode+map[bool]string{true: "-dark", false: "-light"}[dark], func(t *testing.T) {
				a, _, v := pathsAppFixture(t, mode != "viewer", func(p rpc.GitPathsParams) (any, error) {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
				})
				a.openFilesPanel(&a.workspaces[0], sideChanges)
				v.side = sideChanges
				switch mode {
				case "rollback":
					a.openRollback(v)
				case "unknown":
					e := gitx.Entry{Path: "same.txt", Untracked: true, X: '?', Y: '?'}
					v.git.status.Entries = []gitx.Entry{e}
					a.pathAction(appL(), v.git, locales.In("zh-Hans").GitAdd(), rpc.GitPathsStage, e, false)
					pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) > 0 && a.targetPaths[0].unknown })
					a.openLog(&a.workspaces[0], "", "")
					v.console = true
				}
				savePNG(t, newPreviewTester(a, dark), "git-paths-"+mode+map[bool]string{true: "-dark", false: "-light"}[dark]+".png")
			})
		}
	}
}

func TestLocalDesktopPathsQueue(t *testing.T) {
	for _, state := range []string{rpc.GitFetchSucceeded, rpc.GitFetchFailed, rpc.GitFetchCanceled, rpc.GitFetchTimedOut, rpc.GitFetchConflict, "unknown"} {
		t.Run(state, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			gitRun(t, v.root, "config", "user.name", "Paths test")
			gitRun(t, v.root, "config", "user.email", "paths@example.invalid")
			gitRun(t, v.root, "add", ".")
			gitRun(t, v.root, "commit", "-qm", "initial")
			os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
			g := v.git
			g.status, _ = gitx.ReadStatus(t.Context(), v.root)
			e := gitx.Entry{Path: "new", X: '?', Y: '?', Untracked: true}
			entered, release := make(chan struct{}), make(chan struct{})
			defer close(release)
			var calls, commits atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "git.paths" {
					return normal(ctx, p)
				}
				var q rpc.GitPathsParams
				json.Unmarshal(p.Params, &q)
				calls.Add(1)
				close(entered)
				<-release
				if state == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
				}
				return pathsResultFixture(q, state), nil
			}
			f.mu.Unlock()
			a.pathAction(appL(), g, "Stage", rpc.GitPathsStage, e, false)
			waitConnectionEvent(t, entered)
			s := a.targetPaths[0]
			a.gitDo(appL(), g, "Commit", func(ctx context.Context, root string) error {
				commits.Add(1)
				return gitx.Commit(ctx, root, "queued", false, true)
			})
			if g.busyPaths != s || len(g.queue) != 1 || commits.Load() != 0 {
				t.Fatal("paths did not hold queue")
			}
			release <- struct{}{}
			pumpSaves(t, []*app{a}, func() bool { return s.done || s.unknown })
			if state == "unknown" {
				if commits.Load() != 0 || g.busyPaths != s {
					t.Fatal("unknown released queue")
				}
				f.mu.Lock()
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					return pathsDoneFixture(ref, pathsResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.queryPaths(appL(), s)
			}
			pumpSaves(t, []*app{a}, func() bool { return g.busy == "" && commits.Load() == 1 })
			if calls.Load() != 1 || len(g.queue) != 0 {
				t.Fatal("queue replayed")
			}
		})
	}
}

func TestLocalDesktopPathsUnknownConfirmationQueue(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, _, _ := localDesktopRepo(t, a, f)
	gitRun(t, v.root, "config", "user.name", "Paths test")
	gitRun(t, v.root, "config", "user.email", "paths@example.invalid")
	gitRun(t, v.root, "add", ".")
	gitRun(t, v.root, "commit", "-qm", "initial")
	os.WriteFile(filepath.Join(v.root, "new"), []byte("new"), 0600)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return v.repos.scanned && !v.repos.scanning })
	g := v.git
	g.status, _ = gitx.ReadStatus(t.Context(), v.root)
	e := gitx.Entry{Path: "new", X: '?', Y: '?', Untracked: true}
	var calls atomic.Int32
	f.mu.Lock()
	normal := f.desktopCall
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.paths" {
			return normal(ctx, p)
		}
		var q rpc.GitPathsParams
		json.Unmarshal(p.Params, &q)
		if calls.Add(1) == 1 {
			return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost")
		}
		return pathsResultFixture(q, rpc.GitFetchSucceeded), nil
	}
	f.mu.Unlock()
	a.pathAction(appL(), g, "Stage", rpc.GitPathsStage, e, false)
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) == 1 && a.targetPaths[0].unknown })
	old := a.targetPaths[0]
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	a.gitDo(appL(), g, "Queued Git", func(ctx context.Context, root string) error {
		close(entered)
		<-release
		_, err := gitx.ReadStatus(ctx, root)
		return err
	})
	a.pathAction(appL(), g, "Stage", rpc.GitPathsStage, e, false)
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
	pumpSaves(t, []*app{a}, func() bool { return len(a.targetPaths) == 2 && a.targetPaths[1].done })
	if calls.Load() != 2 || old.ref == a.targetPaths[1].ref {
		t.Fatal("new operation reused original")
	}
}
