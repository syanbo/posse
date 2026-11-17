package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/rpc"
)

func localDesktopFixture(t *testing.T) (*app, *hostControlFixture) {
	t.Helper()
	f, c := newHostControlFixture(t)
	c.Home = t.TempDir()
	a := newWindowApp(c)
	t.Cleanup(a.hosts.close)
	return a, f
}

func connectLocalDesktop(t *testing.T, a *app) {
	t.Helper()
	a.connectLocal(context.Background())
	a.runPending()
	if !a.host.snapshot().Controlled {
		t.Fatalf("local window did not take control: %s", a.err)
	}
}

func TestLocalDesktopStartupAndClose(t *testing.T) {
	a, f := localDesktopFixture(t)
	ctx, cancel := context.WithCancel(a.hosts.ctx)
	defer cancel()
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "project.list" || p.Desktop.ClientID != a.host.controllerID {
			t.Errorf("startup polling lost identity: %+v", p)
		}
		cancel()
		return nil, rpc.Errorf(rpc.CodeUnknown, "test stopped")
	}
	f.mu.Unlock()
	a.poll(ctx)
	if a.host.id != localHostID || !a.host.snapshot().Controlled || a.c.Desktop != nil {
		t.Fatal("startup did not keep a unique local session and a separate base client")
	}
	f.mu.Lock()
	takes, heartbeats := f.takes, f.heartbeats
	f.mu.Unlock()
	if takes != 1 || heartbeats != 1 {
		t.Fatalf("startup takes=%d heartbeats=%d", takes, heartbeats)
	}
	a.hosts.close()
	f.mu.Lock()
	owner := f.state.Owner
	f.mu.Unlock()
	if owner != "" || a.host.snapshot().Connected || !a.c.Up() {
		t.Fatal("window close did not release only the lease")
	}
}

func TestLocalDesktopHeartbeatAndReconnect(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	pool := &observedHostCleanup{a.hosts.pool, make(chan struct{}, 4), make(chan struct{}, 4)}
	a.hosts.pool = pool
	f.mu.Lock()
	f.heartbeatFail = true
	f.statusError = rpc.Errorf(rpc.CodeBusy, "test unavailable")
	f.mu.Unlock()
	if a.hosts.heartbeat(a) {
		t.Fatal("unavailable local service remained connected")
	}
	a.runPending()
	select {
	case <-pool.disconnected:
		t.Fatal("local failure used SSH cleanup")
	default:
	}
	f.mu.Lock()
	f.heartbeatFail, f.statusError = false, nil
	f.mu.Unlock()
	a.connectLocal(context.Background())
	if !a.host.snapshot().Connected || a.host.snapshot().Controlled {
		t.Fatal("same-instance reconnect regained control")
	}
	if !a.hosts.heartbeat(a) || a.host.snapshot().Controlled {
		t.Fatal("status query restored uncertain control")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.takes != 1 {
		t.Fatalf("automatic takes = %d", f.takes)
	}
}

func localDesktopDocument(t *testing.T, a *app) (*filesView, *fileDoc) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "document.txt")
	if err := os.WriteFile(path, []byte("base"), 0600); err != nil {
		t.Fatal(err)
	}
	base, err := files.ReadVersion(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	d := &fileDoc{path: path, kind: docText, base: base.Stamp, baseVersion: base, ed: editor.State{Buffer: editor.NewBuffer("base")}}
	d.ed.Buffer.Set("submitted")
	v := &filesView{wsID: "w", root: root, tree: files.NewTree(root), docs: []*fileDoc{d}, listAt: time.Now()}
	newGitStates(v)
	a.workspaces = []workspace{{ID: "w", ProjectID: "p", Name: "main", Path: root}}
	a.projects = []project{{ID: "p", Name: "local", Path: root}}
	a.indexWorkspaces()
	a.files = map[string]*filesView{"w": v}
	return v, d
}

func assertLocalDocumentUnchanged(t *testing.T, d *fileDoc) {
	t.Helper()
	if data, err := os.ReadFile(d.path); err != nil || string(data) != "base" {
		t.Fatalf("desktop write fell back to local disk: %q, %v", data, err)
	}
}

func TestLocalDesktopSave(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "unknown", "late"} {
		t.Run(mode, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			_, d := localDesktopDocument(t, a)
			base, identity := d.baseVersion, *a.request().client.Desktop
			var saves atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Desktop != identity {
					t.Error("save changed desktop identity")
				}
				if p.Method == "files.version" {
					return base, nil
				}
				if p.Method != "files.save" || p.Sequence == 0 {
					t.Errorf("save did not use sequenced desktop.call: %+v", p)
				}
				saves.Add(1)
				var args rpc.FileSaveParams
				if err := json.Unmarshal(p.Params, &args); err != nil {
					return nil, err
				}
				if !args.Expected.Same(base) || string(args.Data) != "submitted" {
					t.Error("save lost its version or draft")
				}
				if mode == "conflict" {
					return nil, rpc.Errorf(rpc.CodeConflict, "changed after version check")
				}
				if mode == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
				}
				if mode == "late" {
					close(entered)
					<-release
				}
				return rpc.FileSaveResult{Version: fileSaveVersion(base.Path, "submitted"), Revision: args.Revision}, nil
			}
			f.mu.Unlock()
			finished, saved := false, false
			a.saveDoc(appL(), d, func(ok bool) { finished, saved = true, ok })
			if mode == "late" {
				pumpSaves(t, []*app{a}, func() bool {
					select {
					case <-entered:
						return true
					default:
						return false
					}
				})
				if err := a.host.detach().release(context.Background()); err != nil {
					t.Fatal(err)
				}
				once.Do(func() { close(release) })
			}
			pumpSaves(t, []*app{a}, func() bool { return finished })
			if saved != (mode == "success") || saves.Load() != 1 || d.isModified() == saved {
				t.Fatalf("save mode=%s saved=%v calls=%d", mode, saved, saves.Load())
			}
			if mode == "conflict" && !d.conflict {
				t.Fatal("save conflict was lost")
			}
			if mode == "unknown" {
				if d.save == nil || d.save.ref == nil {
					t.Fatal("unknown save lost operation reference")
				}
				ref := *d.save.ref
				f.mu.Lock()
				f.operation = func(got rpc.DesktopOperationRef) (any, error) {
					if got != ref {
						t.Error("query did not use original save operation")
					}
					return rpc.DesktopOperation{DesktopOperationRef: got, State: "unknown"}, nil
				}
				f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
					if p.Method != "files.version" {
						t.Error("save reconciliation resent a write")
					}
					return fileSaveVersion(base.Path, "submitted"), nil
				}
				f.mu.Unlock()
				if !waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) {
					t.Fatal("original save could not be confirmed")
				}
			}
			assertLocalDocumentUnchanged(t, d)
		})
	}
}

func TestLocalDesktopTrash(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "unknown", "late"} {
		t.Run(mode, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, d := localDesktopDocument(t, a)
			entry, err := files.ReadEntryVersion(context.Background(), d.path)
			if err != nil {
				t.Fatal(err)
			}
			identity := *a.request().client.Desktop
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Desktop != identity {
					t.Error("trash identity changed")
				}
				if p.Method == "files.entry" {
					return entry, nil
				}
				var args rpc.FileTrashParams
				if err := json.Unmarshal(p.Params, &args); err != nil {
					return nil, err
				}
				if p.Method != "files.trash" || p.Sequence == 0 || !args.Expected.Same(entry) {
					t.Error("trash lost version or write identity")
				}
				calls.Add(1)
				if mode == "conflict" {
					return nil, rpc.Errorf(rpc.CodeConflict, "entry changed")
				}
				if mode == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
				}
				if mode == "late" {
					close(entered)
					<-release
				}
				return rpc.FileTrashResult{Destination: "/fake/.Trash/item"}, nil
			}
			f.mu.Unlock()
			a.askTrashPath(appL(), v, d.path, false)
			pumpSaves(t, []*app{a}, func() bool { return a.confirming })
			if mode == "late" {
				done := make(chan struct{})
				go func() { a.confirmDo(); close(done) }()
				waitConnectionEvent(t, entered)
				if err := a.host.detach().release(context.Background()); err != nil {
					t.Fatal(err)
				}
				once.Do(func() { close(release) })
				waitConnectionEvent(t, done)
			} else {
				a.confirmDo()
			}
			a.runPending()
			if calls.Load() != 1 || v.listAt.IsZero() != (mode == "success") {
				t.Fatal("trash was replayed or stale outcome changed the view")
			}
			if mode != "success" && mode != "late" && a.err == "" {
				t.Fatal("trash failure not reported")
			}
			assertLocalDocumentUnchanged(t, d)
		})
	}
}

func TestLocalDesktopWritesRequireControl(t *testing.T) {
	for _, mode := range []string{"not-connected", "viewer", "released"} {
		t.Run(mode, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			if mode == "viewer" {
				f.mu.Lock()
				f.state.Owner = "other"
				f.mu.Unlock()
				a.connectLocal(context.Background())
			} else if mode == "released" {
				connectLocalDesktop(t, a)
				if err := a.host.detach().release(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			v, d := localDesktopDocument(t, a)
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { calls.Add(1); return nil, nil }
			f.mu.Unlock()
			if waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) {
				t.Fatal("viewer saved")
			}
			a.request().trashEntry(appL(), v, d.path, false, files.EntryVersion{})
			a.startFetch(appL(), &a.workspaces[0], v)
			a.runPending()
			if calls.Load() != 0 || !a.typedFetch() || len(a.targetFetches) != 0 || v.git.busy != "" {
				t.Fatal("write without control reached an executor")
			}
			assertLocalDocumentUnchanged(t, d)
		})
	}
}

func localDesktopRepo(t *testing.T, a *app, f *hostControlFixture) (*filesView, rpc.GitDiscoverResult, *atomic.Int32) {
	t.Helper()
	v, _ := localDesktopDocument(t, a)
	gitRun(t, v.root, "init", "-q", "-b", "main")
	id := gitx.RepositoryIdentity{Root: v.root, GitDir: filepath.Join(v.root, ".git"), CommonDir: filepath.Join(v.root, ".git"), RootInode: 91, GitInode: 92, CommonInode: 92}
	discovery := rpc.GitDiscoverResult{RealPath: v.root, Repos: []rpc.GitRepository{{Root: v.root, Real: v.root, Identity: id}}}
	calls := &atomic.Int32{}
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "git.discover" {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
		}
		calls.Add(1)
		return discovery, nil
	}
	f.mu.Unlock()
	return v, discovery, calls
}

func TestLocalDesktopRepositoryIdentity(t *testing.T) {
	a, f := localDesktopFixture(t)
	v, discovery, calls := localDesktopRepo(t, a, f)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	old := v.git
	v.ch.message = "keep local draft"
	old.picks = map[string]bool{"document.txt": false}
	if old.repo != v.root || old.identity.Root != "" || calls.Load() != 0 {
		t.Fatal("initial local discovery was not retained")
	}
	connectLocalDesktop(t, a)
	a.pollRepos(nil, v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning && !v.git.poll.busy })
	if v.remote || v.git != old || v.ch.message != "keep local draft" || len(v.git.picks) != 1 || v.git.identity != discovery.Repos[0].Identity || !a.fetchWritable(v) {
		t.Fatal("desktop discovery lost local state or did not enable Fetch")
	}
	for i := 0; i < 3; i++ {
		a.pollRepos(nil, v)
	}
	if calls.Load() != 1 {
		t.Fatalf("identity discovery repeated %d times", calls.Load())
	}
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	if v.git != old {
		t.Fatal("unchanged identity replaced Git state")
	}
	oldTarget := a.gitTarget(&a.workspaces[0], v, v.git)
	replacement := discovery
	replacement.Repos = append([]rpc.GitRepository(nil), discovery.Repos...)
	replacement.Repos[0].Identity.RootInode++
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { return replacement, nil }
	f.mu.Unlock()
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	if v.git == old || oldTarget.current() || v.git.identity != replacement.Repos[0].Identity {
		t.Fatal("replacement repository retained old Fetch target")
	}
}

func TestLocalDesktopRepositoryLateResult(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, discovery, _ := localDesktopRepo(t, a, f)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { close(entered); <-release; return discovery, nil }
	f.mu.Unlock()
	a.scanRepos(v)
	waitConnectionEvent(t, entered)
	if err := a.host.detach().release(context.Background()); err != nil {
		t.Fatal(err)
	}
	once.Do(func() { close(release) })
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	if v.git.identity.Root != "" || v.repos.scanned {
		t.Fatal("late discovery restored stale identity")
	}
}

func TestLocalDesktopFetch(t *testing.T) {
	for _, mode := range []string{"success", "conflict", "unknown", "late"} {
		t.Run(mode, func(t *testing.T) {
			a, f := localDesktopFixture(t)
			connectLocalDesktop(t, a)
			v, _, _ := localDesktopRepo(t, a, f)
			a.scanRepos(v)
			pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
			identity := *a.request().client.Desktop
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method != "git.fetch" || p.Desktop != identity || p.Sequence == 0 {
					t.Errorf("Fetch lost desktop identity: %+v", p)
				}
				calls.Add(1)
				var params rpc.GitFetchParams
				if err := json.Unmarshal(p.Params, &params); err != nil {
					return nil, err
				}
				if params.Expected != v.git.identity || params.WorkspaceID != "w" || params.WorkspacePath != v.root {
					t.Error("Fetch target changed")
				}
				if mode == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
				}
				if mode == "late" {
					close(entered)
					<-release
				}
				status := rpc.GitFetchSucceeded
				if mode == "conflict" {
					status = rpc.GitFetchConflict
				}
				return fetchResultFixture(params, status), nil
			}
			f.mu.Unlock()
			tt := newTester(func(c *ui.Context) { a.runPending(); a.fetchButton(c, &a.workspaces[0], v) }, 180, 80)
			tt.Frame()
			clickFetch(t, tt)
			if mode == "late" {
				waitConnectionEvent(t, entered)
				if err := a.host.detach().release(context.Background()); err != nil {
					t.Fatal(err)
				}
				once.Do(func() { close(release) })
			}
			until(t, tt, "local fetch outcome", func() bool {
				return len(a.targetFetches) == 1 && (a.targetFetches[0].done || a.targetFetches[0].unknown)
			})
			s := a.targetFetches[0]
			if calls.Load() != 1 || s.ref.Desktop != identity {
				t.Fatal("Fetch was replayed or changed identity")
			}
			if mode == "conflict" && (s.result == nil || s.result.Status != rpc.GitFetchConflict) {
				t.Fatal("Fetch version conflict lost")
			}
			if mode == "late" && v.git.pollAgain {
				t.Fatal("late Fetch result refreshed current view")
			}
			if mode == "unknown" {
				f.mu.Lock()
				f.operation = func(ref rpc.DesktopOperationRef) (any, error) {
					if ref != s.ref {
						t.Error("Fetch query lost original operation")
					}
					return fetchDoneFixture(ref, fetchResultFixture(s.params, rpc.GitFetchSucceeded)), nil
				}
				f.mu.Unlock()
				a.queryFetch(appL(), s)
				until(t, tt, "local fetch query", func() bool { return s.done && !s.checking })
				if calls.Load() != 1 {
					t.Fatal("query resent Fetch")
				}
			}
			pumpSaves(t, []*app{a}, func() bool { return !v.git.poll.busy })
		})
	}
}

func TestLocalDesktopDiscussionRouting(t *testing.T) {
	for _, selected := range []string{localHostID, "remote"} {
		t.Run(selected, func(t *testing.T) {
			var mu sync.Mutex
			state := rpc.DesktopState{InstanceID: "local", Epoch: 1}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			v, a, b, local, remote := localDiscussionFixture(t, map[string]rpc.Handler{
				"desktop.status": func(context.Context, json.RawMessage) (any, error) { mu.Lock(); defer mu.Unlock(); return state, nil },
				"desktop.take": func(_ context.Context, raw json.RawMessage) (any, error) {
					var p rpc.DesktopRequest
					if err := rpc.Decode(raw, &p); err != nil {
						return nil, err
					}
					mu.Lock()
					defer mu.Unlock()
					state.Owner = p.ClientID
					state.Epoch++
					return state, nil
				},
				"desktop.release": func(context.Context, json.RawMessage) (any, error) { return nil, nil },
				"desktop.call": func(_ context.Context, raw json.RawMessage) (any, error) {
					var p rpc.DesktopCall
					if err := rpc.Decode(raw, &p); err != nil {
						return nil, err
					}
					if p.Method != "project.list" {
						t.Errorf("unexpected desktop method: %s", p.Method)
					}
					return nil, rpc.Errorf(rpc.CodeUnknown, "test does not serve polling")
				},
				"discussion.list": func(context.Context, json.RawMessage) (any, error) {
					cancel()
					return discList{Seq: 1, Items: []discSummary{{Key: "local-room"}}}, nil
				},
			})
			v.activate(localHostID)
			a.host = newHostSession(localHostID, "window")
			connectLocalDesktop(t, a)
			if selected == "remote" {
				v.activate("remote")
				if err := b.host.connect(context.Background(), b.c); err != nil {
					t.Fatal(err)
				}
			}
			a.displayed().discussionRequest().discCall("discussion.post", map[string]any{"key": "local-room", "body": "hello"})
			a.runPending()
			// local 模式下上面的讨论后刷新也必须给桌面轮询重新绑定身份。
			if selected == localHostID {
				if _, ok := local.find("desktop.call"); !ok {
					t.Fatal("discussion refresh bypassed desktop identity")
				}
			}
			ctx, cancel = context.WithCancel(context.Background())
			a.poll(ctx)
			a.runPending()
			if a.c.Desktop != nil {
				t.Fatal("base client acquired desktop identity")
			}
			for _, method := range []string{"discussion.post", "discussion.list"} {
				if params, ok := local.find(method); !ok || params["desktop"] != nil {
					t.Fatalf("discussion did not use local base client: %s %+v", method, params)
				}
			}
			assertNoRemoteDiscussion(t, remote, "")
			if selected == localHostID {
				_ = a.host.detach().release(context.Background())
			}
		})
	}
}

func TestLocalDesktopRepositoryFailureRetry(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, discovery, _ := localDesktopRepo(t, a, f)
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		if calls.Add(1) == 2 {
			close(entered)
			<-release
		}
		return nil, rpc.Errorf(rpc.CodeBusy, "discovery failed")
	}
	f.mu.Unlock()
	waiting := 0
	v.repos.waiting = append(v.repos.waiting, func() { waiting++ })
	a.pollRepos(nil, v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	failedAt := v.repos.scanAt
	if v.repos.err == "" || failedAt.IsZero() || v.repos.request.app != nil || a.fetchWritable(v) {
		t.Fatal("failed first discovery published a successful identity")
	}
	a.pollRepos(nil, v)
	a.pollRepos(nil, v)
	if v.repos.scanning || calls.Load() != 1 || v.repos.scanAt != failedAt || waiting != 0 || len(v.repos.waiting) != 1 {
		t.Fatal("failed discovery immediately retried or consumed waiting callbacks")
	}
	v.repos.scanAt = time.Now().Add(-gitPollEvery)
	a.pollRepos(nil, v)
	waitConnectionEvent(t, entered)
	a.pollRepos(nil, v)
	a.pollRepos(nil, v)
	if calls.Load() != 2 || !v.repos.scanning {
		t.Fatal("discovery retry was not a single in-flight request")
	}
	once.Do(func() { close(release) })
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	if waiting != 0 || len(v.repos.waiting) != 1 {
		t.Fatal("failed retry consumed first-result callbacks")
	}
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { return discovery, nil }
	f.mu.Unlock()
	v.repos.scanAt = time.Now().Add(-gitPollEvery)
	a.pollRepos(nil, v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	if waiting != 1 || len(v.repos.waiting) != 0 || !a.fetchWritable(v) {
		t.Fatal("successful retry did not publish identity and run waiting callbacks")
	}
}

func TestLocalDesktopRepositoryRescanKeepsFetch(t *testing.T) {
	a, f := localDesktopFixture(t)
	connectLocalDesktop(t, a)
	v, discovery, _ := localDesktopRepo(t, a, f)
	a.scanRepos(v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	old, identity, request := v.git, v.git.identity, v.repos.request
	if !a.fetchWritable(v) {
		t.Fatal("successful discovery did not enable Fetch")
	}
	var calls atomic.Int32
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil, rpc.Errorf(rpc.CodeBusy, "rescan failed")
	}
	f.mu.Unlock()
	a.scanRepos(v)
	waitConnectionEvent(t, entered)
	if !a.fetchWritable(v) || v.git != old || v.git.identity != identity {
		t.Fatal("in-flight rescan disabled Fetch or changed identity")
	}
	once.Do(func() { close(release) })
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning })
	if !a.fetchWritable(v) || v.git != old || v.git.identity != identity || v.repos.request != request || v.repos.err == "" {
		t.Fatal("failed rescan discarded a current successful identity")
	}
	a.pollRepos(nil, v)
	a.pollRepos(nil, v)
	if v.repos.scanning || calls.Load() != 1 {
		t.Fatal("failed rescan immediately retried")
	}
	pumpSaves(t, []*app{a}, func() bool { return !v.git.poll.busy })
	takeRequestTestControl(t, a)
	if a.fetchWritable(v) {
		t.Fatal("new control identity used an old discovery result")
	}
	v.repos.scanAt = time.Now().Add(-gitPollEvery)
	a.pollRepos(nil, v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning && !v.git.poll.busy })
	if a.fetchWritable(v) || v.repos.request != request || v.git.identity != identity {
		t.Fatal("failed discovery authorized stale repository identity for new control")
	}
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { return discovery, nil }
	f.mu.Unlock()
	v.repos.scanAt = time.Now().Add(-gitPollEvery)
	a.pollRepos(nil, v)
	pumpSaves(t, []*app{a}, func() bool { return !v.repos.scanning && !v.git.poll.busy })
	if !a.fetchWritable(v) || !v.repos.request.current() || v.git != old || v.git.identity != identity {
		t.Fatal("new successful discovery did not restore the same repository")
	}
}
