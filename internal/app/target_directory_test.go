package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/files"
	"posse/internal/rpc"
)

func targetDirectoryFixture(t *testing.T, entries []files.Entry) (*app, *hostControlFixture, <-chan string) {
	t.Helper()
	a, f := requestTestApp(t, "Office Mac")
	takeRequestTestControl(t, a)
	paths := make(chan string, 32)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "files.readDirs" {
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
		}
		var args rpc.FilePathsParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		if len(args.Paths) != 1 {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "expected one path")
		}
		path := args.Paths[0]
		paths <- path
		return []rpc.FileDirectoryResult{{Path: path, Entries: entries, Stamp: files.Stamp{Mode: os.ModeDir}}}, nil
	}
	f.mu.Unlock()
	t.Cleanup(a.closeTargetDirectory)
	return a, f, paths
}

func openTargetDirectory(t *testing.T, a *app, start string, done func(string)) *targetDirectory {
	t.Helper()
	a.request().chooseTargetDirectory(appL().AppAddProjectTitle(), appL().AppAddProjectButton(), start, done)
	a.runPending()
	if a.targetDir == nil {
		t.Fatal("picker did not open")
	}
	waitFileUpdate(t, a)
	a.runPending()
	return a.targetDir
}

func targetDirectoryPath(t *testing.T, paths <-chan string) string {
	t.Helper()
	select {
	case path := <-paths:
		return path
	case <-time.After(3 * time.Second):
		t.Fatal("directory request or selection did not arrive")
		return ""
	}
}

func TestTargetDirectoryTargetOnlyAndLinks(t *testing.T) {
	entries := []files.Entry{
		{Name: "remote-only", Dir: true}, {Name: "link", Dir: true, Link: "../actual"},
		{Name: "file.txt"}, {Name: "..", Dir: true}, {Name: "bad/name", Dir: true},
		{Name: "bad\x00name", Dir: true}, {Name: ".", Dir: true}, {Name: "", Dir: true},
	}
	a, _, paths := targetDirectoryFixture(t, entries)
	selected := make(chan string, 1)
	// 此路径在本机不存在；只有目标 RPC 能提供目录。
	d := openTargetDirectory(t, a, "/target/link/../projects", func(path string) { selected <- path })
	if got := targetDirectoryPath(t, paths); got != d.path || !d.canSelect() {
		t.Fatalf("path=%q state=%+v", got, d)
	}
	if !reflect.DeepEqual(d.entries, entries[:2]) {
		t.Fatalf("entries = %+v", d.entries)
	}
	d.enter("link")
	if d.canSelect() {
		t.Fatal("loading directory was selectable")
	}
	waitFileUpdate(t, a)
	a.runPending()
	want := "/target/link/../projects/link"
	if got := targetDirectoryPath(t, paths); got != want || d.path != want {
		t.Fatalf("link path normalized: %q, %q", got, d.path)
	}
	d.selectDirectory()
	if a.targetDir != nil || targetDirectoryPath(t, selected) != want {
		t.Fatal("selection did not close and return the original path")
	}
}

func TestTargetDirectoryMacNames(t *testing.T) {
	name := "space \\ folder 目录"
	a, _, paths := targetDirectoryFixture(t, []files.Entry{{Name: name, Dir: true}})
	d := openTargetDirectory(t, a, "/target", nil)
	targetDirectoryPath(t, paths)
	d.enter(name)
	waitFileUpdate(t, a)
	a.runPending()
	if got := targetDirectoryPath(t, paths); got != "/target/"+name || !d.canSelect() {
		t.Fatalf("legal Mac directory name changed: %q", got)
	}
}

func TestTargetDirectoryEmptyAndChangedInput(t *testing.T) {
	a, _, paths := targetDirectoryFixture(t, nil)
	selected := make(chan string, 1)
	d := openTargetDirectory(t, a, "relative", func(path string) { selected <- path })
	if targetDirectoryPath(t, paths) != "/" || !d.canSelect() || len(d.entries) != 0 {
		t.Fatal("default or empty directory could not be selected")
	}
	d.input = "/not-loaded"
	d.selectDirectory()
	if d.canSelect() || a.targetDir != d {
		t.Fatal("edited path selected the previous directory")
	}
	d.input = "/"
	d.selectDirectory()
	if targetDirectoryPath(t, selected) != "/" {
		t.Fatal("empty directory was not selected")
	}
}

func TestTargetDirectoryFailures(t *testing.T) {
	for _, mode := range []string{"permission", "missing", "missing-stamp", "not-directory", "wrong-path", "empty-response", "transport"} {
		t.Run(mode, func(t *testing.T) {
			a, f, _ := targetDirectoryFixture(t, nil)
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				item := rpc.FileDirectoryResult{Path: "/target", Stamp: files.Stamp{Mode: os.ModeDir}}
				switch mode {
				case "permission":
					item.Error = rpc.Errorf(rpc.CodePermissionDenied, "permission denied")
				case "missing":
					item.Error = rpc.Errorf(rpc.CodeNotFound, "not found")
				case "missing-stamp":
					item.Stamp.Missing = true
				case "not-directory":
					item.Stamp.Mode = 0600
				case "wrong-path":
					item.Path = "/another"
				case "empty-response":
					return []rpc.FileDirectoryResult{}, nil
				case "transport":
					return nil, context.DeadlineExceeded
				}
				return []rpc.FileDirectoryResult{item}, nil
			}
			f.mu.Unlock()
			d := openTargetDirectory(t, a, "/target", func(string) { calls.Add(1) })
			d.selectDirectory()
			if d.err == nil || d.loaded || d.canSelect() || calls.Load() != 0 || a.targetDir != d {
				t.Fatalf("failed directory selected: %+v", d)
			}
		})
	}
}

func TestTargetDirectoryInvalidInput(t *testing.T) {
	for _, input := range []string{"relative", "/bad\x00path"} {
		a, _, paths := targetDirectoryFixture(t, nil)
		d := openTargetDirectory(t, a, "/", nil)
		targetDirectoryPath(t, paths)
		d.load(input)
		if d.err == nil || d.loading || d.canSelect() {
			t.Fatalf("invalid path accepted: %q", input)
		}
		select {
		case got := <-paths:
			t.Fatalf("invalid input reached RPC: %q", got)
		default:
		}
		d.load(targetDirectoryParent(d.path))
		waitFileUpdate(t, a)
		a.runPending()
		if !d.canSelect() {
			t.Fatal("could not return to parent after invalid path")
		}
	}
	if got := targetDirectoryParent("/a/link/../missing"); got != "/a/link/.." {
		t.Fatalf("parent normalized symlink path: %q", got)
	}
}

func TestTargetDirectoryBackgroundOpenAndCancel(t *testing.T) {
	a, _, _ := targetDirectoryFixture(t, nil)
	r := a.request()
	queued := make(chan struct{})
	var calls atomic.Int32
	go func() {
		r.chooseTargetDirectory("title", "choose", "/", func(string) { calls.Add(1) })
		close(queued)
	}()
	<-queued
	if a.targetDir != nil {
		t.Fatal("background caller changed UI state directly")
	}
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
	tt := newTester(a.targetDirectoryDialog, 800, 600)
	settle(tt)
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	if a.targetDir != nil || calls.Load() != 0 {
		t.Fatal("cancel invoked completion")
	}
}

func TestTargetDirectoryRejectsViewerAndStaleOpen(t *testing.T) {
	for _, mode := range []string{"viewer", "identity", "detached", "local", "no-desktop"} {
		t.Run(mode, func(t *testing.T) {
			a, _ := requestTestApp(t, "remote")
			if mode != "viewer" {
				takeRequestTestControl(t, a)
			}
			r := a.request()
			if mode == "local" || mode == "no-desktop" {
				copy := *r.client
				if mode == "local" {
					copy.Remote = false
				} else {
					copy.Desktop = nil
				}
				r.client = &copy
			}
			r.chooseTargetDirectory("title", "choose", "/", nil)
			if mode == "identity" {
				takeRequestTestControl(t, a)
			} else if mode == "detached" {
				a.host.detach()
			}
			a.runPending()
			if a.targetDir != nil {
				t.Fatal("invalid request opened picker")
			}
		})
	}
}

func TestTargetDirectoryDiscardsQueuedResults(t *testing.T) {
	for _, mode := range []string{"new-path", "close", "identity", "detached", "view-generation"} {
		t.Run(mode, func(t *testing.T) {
			a, _, _ := targetDirectoryFixture(t, nil)
			var calls atomic.Int32
			a.request().chooseTargetDirectory("title", "choose", "/old", func(string) { calls.Add(1) })
			a.runPending()
			d := a.targetDir
			waitFileUpdate(t, a)
			switch mode {
			case "new-path":
				d.load("/new")
			case "close":
				a.closeTargetDirectory()
			case "identity":
				takeRequestTestControl(t, a)
			case "detached":
				a.host.detach()
			case "view-generation":
				a.viewGeneration.Add(1)
			}
			a.runPending()
			if mode == "new-path" {
				if d.loaded && d.path == "/old" {
					t.Fatal("old response replaced new path")
				}
				if d.loading {
					waitFileUpdate(t, a)
					a.runPending()
				}
				if !d.canSelect() || d.path != "/new" {
					t.Fatal("new response failed")
				}
			} else {
				d.selectDirectory()
				if d.loaded || d.canSelect() || calls.Load() != 0 {
					t.Fatal("stale response or selection was applied")
				}
			}
		})
	}
}

func TestTargetDirectoryCloseCancelsLoad(t *testing.T) {
	a, f, _ := targetDirectoryFixture(t, nil)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		close(started)
		<-release
		return []rpc.FileDirectoryResult{{Path: "/", Stamp: files.Stamp{Mode: os.ModeDir}}}, nil
	}
	f.mu.Unlock()
	var calls atomic.Int32
	a.request().chooseTargetDirectory("title", "choose", "/", func(string) { calls.Add(1) })
	a.runPending()
	d := a.targetDir
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	a.closeTargetDirectory()
	// 服务端仍阻塞；客户端只有取消上下文才会排回结果。
	waitFileUpdate(t, a)
	a.runPending()
	d.selectDirectory()
	if a.targetDir != nil || d.cancel != nil || calls.Load() != 0 || d.loaded {
		t.Fatal("closing failed to cancel pending load")
	}
}

func TestTargetDirectoryDialogNavigation(t *testing.T) {
	a, _, paths := targetDirectoryFixture(t, []files.Entry{{Name: "first", Dir: true}, {Name: "second", Dir: true}, {Name: "third", Dir: true}})
	var calls atomic.Int32
	d := openTargetDirectory(t, a, "/target", func(string) { calls.Add(1) })
	targetDirectoryPath(t, paths)
	tt := newTester(a.targetDirectoryDialog, 800, 600)
	settle(tt)
	if err := tt.Click("first"); err != nil {
		t.Fatal(err)
	}
	waitFileUpdate(t, a)
	a.runPending()
	if got := targetDirectoryPath(t, paths); got != "/target/first" || !d.canSelect() {
		t.Fatalf("directory click = %q", got)
	}
	tt.Frame()
	if err := tt.Click("目录绝对路径"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("/target/link/../typed")
	if d.canSelect() {
		t.Fatal("typing left selection enabled")
	}
	tt.Key(0, ui.KeyEnter)
	waitFileUpdate(t, a)
	a.runPending()
	if got := targetDirectoryPath(t, paths); got != "/target/link/../typed" {
		t.Fatalf("typed path = %q", got)
	}
	tt.Frame()
	if err := tt.Click("上一级"); err != nil {
		t.Fatal(err)
	}
	waitFileUpdate(t, a)
	a.runPending()
	if got := targetDirectoryPath(t, paths); got != "/target/link/.." {
		t.Fatalf("parent path = %q", got)
	}
	tt.Frame()
	tt.Key(0, ui.KeyEscape)
	if a.targetDir != nil || calls.Load() != 0 {
		t.Fatal("escape did not cancel")
	}
}

func TestTargetDirectoryRealActions(t *testing.T) {
	for _, action := range []string{"add", "relocate"} {
		for _, stale := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "-success", true: "-stale"}[stale], func(t *testing.T) {
				a, f, _ := targetDirectoryFixture(t, nil)
				writes := make(chan rpc.DesktopCall, 2)
				f.mu.Lock()
				read := f.desktopCall
				f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
					if p.Method == "project.add" || p.Method == "workspace.relocate" {
						writes <- p
						return nil, nil
					}
					return read(ctx, p)
				}
				f.mu.Unlock()
				identity := *a.request().client.Desktop
				if action == "add" {
					a.addProject(appL())
				} else {
					a.relocateWorkspace(appL(), workspace{ID: "work", Path: "/target/old"})
				}
				a.runPending()
				waitFileUpdate(t, a)
				a.runPending()
				d := a.targetDir
				d.load("/target/new")
				waitFileUpdate(t, a)
				a.runPending()
				if stale {
					takeRequestTestControl(t, a)
				}
				d.selectDirectory()
				if stale {
					select {
					case <-writes:
						t.Fatal("expired picker wrote")
					default:
					}
					return
				}
				select {
				case p := <-writes:
					var args struct{ Path, Workspace string }
					if err := json.Unmarshal(p.Params, &args); err != nil {
						t.Fatal(err)
					}
					if args.Path != "/target/new" || p.Desktop != identity || action == "relocate" && args.Workspace != "work" || a.targetDir != nil {
						t.Fatalf("unexpected write: %+v %+v", p, args)
					}
				case <-time.After(3 * time.Second):
					t.Fatal("real action did not write")
				}
			})
		}
	}
}
