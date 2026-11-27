package app

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/files"
	"posse/internal/rpc"
	"posse/locales"
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

func TestTargetDirectoryFallbackSearchAndCreateFolder(t *testing.T) {
	a, f, _ := targetDirectoryFixture(t, nil)
	var createdPath string
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.readDirs":
			var args rpc.FilePathsParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			dir := args.Paths[0]
			switch dir {
			case "/Users/pur/Desktop":
				return []rpc.FileDirectoryResult{{Path: dir, Error: rpc.Errorf(rpc.CodeNotFound, "not found")}}, nil
			case "/Users/pur":
				return []rpc.FileDirectoryResult{{
					Path: dir,
					Entries: []files.Entry{
						{Name: "my-flutter", Dir: true},
						{Name: "flutter", Dir: true},
						{Name: "ohio", Dir: true},
					},
					Stamp: files.Stamp{Mode: os.ModeDir},
				}}, nil
			default:
				return []rpc.FileDirectoryResult{{Path: dir, Stamp: files.Stamp{Mode: os.ModeDir}}}, nil
			}
		case "files.mkdir":
			var args rpc.FileMkdirParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			createdPath = args.Path
			return map[string]any{"path": args.Path}, nil
		default:
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
		}
	}
	f.mu.Unlock()

	// 1. Desktop 不可读时自动回退到 /Users/pur
	a.request().chooseTargetDirectoryWithFallbacks(appL().AppAddProjectTitle(), appL().AppAddProjectButton(), "/Users/pur/Desktop", []string{"/Users/pur", "/"}, nil)
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
	d := a.targetDir
	if d == nil || d.path != "/Users/pur" || !d.loaded {
		t.Fatalf("fallback did not load /Users/pur: %+v", d)
	}

	// 2. 路径末尾输入过滤词（或纯关键词）时实时匹配，前缀匹配排在子串匹配前
	d.input = "/Users/pur/fl"
	vis := d.visibleEntries()
	if len(vis) != 2 || vis[0].Name != "flutter" || vis[1].Name != "my-flutter" {
		t.Fatalf("filtered entries = %+v", vis)
	}
	d.input = "oh"
	vis = d.visibleEntries()
	if len(vis) != 1 || vis[0].Name != "ohio" {
		t.Fatalf("keyword filtered entries = %+v", vis)
	}
	// 回车自动进入唯一匹配目录
	d.submitInput()
	waitFileUpdate(t, a)
	a.runPending()
	if d.path != "/Users/pur/ohio" {
		t.Fatalf("submitInput did not enter matched folder: path=%q", d.path)
	}

	// 3. 明确绝对路径回车必须按原路径加载，绝不误替换为模糊匹配的另一目录
	d.load("/Users/pur")
	waitFileUpdate(t, a)
	a.runPending()
	d.input = "/Users/pur/flu"
	d.submitInput()
	waitFileUpdate(t, a)
	a.runPending()
	if d.path != "/Users/pur/flu" {
		t.Fatalf("absolute submit was replaced by fuzzy match: path=%q", d.path)
	}

	// 4. 编辑路径切换到另一父目录时，防抖自动加载新父目录并联动过滤；同值重输时旧防抖令牌失效
	d.load("/Users/pur/ohio")
	waitFileUpdate(t, a)
	a.runPending()
	d.input = "/Users/p"
	d.onInputChanged()
	oldSeq := d.browseSeq
	d.stopBrowseTimer()
	if d.browseSeq == oldSeq {
		t.Fatal("stopBrowseTimer did not invalidate browseSeq")
	}
	d.input = "/Users/p"
	d.onInputChanged()
	waitFileUpdate(t, a)
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
	if d.path != "/Users" || d.input != "/Users/p" || d.filterQuery() != "p" {
		t.Fatalf("debounced parent browse failed: path=%q input=%q filter=%q", d.path, d.input, d.filterQuery())
	}
	d.load("/Users/pur/ohio")
	waitFileUpdate(t, a)
	a.runPending()

	// 5. 创建文件夹期间导航被阻止，且创建完成后正确进入新目录、解除 creating
	d.newOpen = true
	d.newName = "new-app"
	d.createFolder(appL())
	if !d.creating {
		t.Fatal("createFolder did not set creating")
	}
	d.load("/should-be-ignored-while-creating")
	if d.path != "/Users/pur/ohio" {
		t.Fatalf("navigation succeeded during folder creation: %q", d.path)
	}
	waitFileUpdate(t, a)
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
	if createdPath != "/Users/pur/ohio/new-app" || d.path != "/Users/pur/ohio/new-app" || d.newOpen || d.creating {
		t.Fatalf("createFolder: created=%q path=%q newOpen=%v creating=%v", createdPath, d.path, d.newOpen, d.creating)
	}

	// 6. 传输超时错误不得触发回退链
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		return nil, context.DeadlineExceeded
	}
	f.mu.Unlock()
	a.request().chooseTargetDirectoryWithFallbacks(appL().AppAddProjectTitle(), appL().AppAddProjectButton(), "/Users/pur/Desktop", []string{"/Users/pur", "/"}, nil)
	a.runPending()
	waitFileUpdate(t, a)
	a.runPending()
	if a.targetDir.path != "/Users/pur/Desktop" || a.targetDir.err == nil {
		t.Fatalf("transport timeout triggered fallback: path=%q err=%v", a.targetDir.path, a.targetDir.err)
	}
}

func TestTargetDirectoryNewFolderNoLayoutShift(t *testing.T) {
	a, _, _ := targetDirectoryFixture(t, nil)
	d := openTargetDirectory(t, a, "/", nil)
	m := locales.In("zh-Hans")
	tt := newTester(a.targetDirectoryDialog, 800, 600)
	settle(tt)
	before, ok := tt.Find(d.title)
	if !ok {
		t.Fatal("dialog title not found before opening new-folder row")
	}
	if err := tt.Click(m.TargetDirectoryNewFolder()); err != nil {
		t.Fatalf("click New Folder: %v", err)
	}
	settle(tt)
	if !d.newOpen {
		t.Fatal("clicking New Folder did not enter inline create mode")
	}
	after, ok := tt.Find(d.title)
	if !ok {
		t.Fatal("dialog title not found after opening new-folder row")
	}
	if before.Y != after.Y {
		t.Fatalf("modal jumped vertically when opening New Folder: y %.1f -> %.1f", before.Y, after.Y)
	}
	// 底栏原地替换：主提交按钮应切为「创建」，不再同时显示「添加」
	if !tt.HasText(m.TargetDirectoryCreate()) || slices.Contains(tt.Texts(), d.verb) {
		t.Fatalf("footer did not switch in-place during inline create: %v", tt.Texts())
	}
	if err := tt.Click(m.CommonCancel()); err != nil {
		t.Fatalf("click inline Cancel: %v", err)
	}
	settle(tt)
	if d.newOpen || a.targetDir != d {
		t.Fatalf("inline Cancel should exit create mode without closing modal: newOpen=%v targetDir=%v", d.newOpen, a.targetDir != nil)
	}
}
