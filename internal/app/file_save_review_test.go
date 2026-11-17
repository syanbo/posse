package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func TestAsyncCloseGroupChecksInitiallyCleanDoc(t *testing.T) {
	a, f, d := saveFixture(t)
	v := a.files["w"]
	clean := &fileDoc{path: d.path + ".clean", kind: docText, ed: editor.State{Buffer: editor.NewBuffer("clean")}}
	v.docs = append(v.docs, clean)
	base := d.baseVersion
	entered, release := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "files.version" {
			return base, nil
		}
		var args rpc.FileSaveParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return rpc.FileSaveResult{Version: fileSaveVersion(base.Path, string(args.Data)), Revision: args.Revision}, nil
	}
	f.mu.Unlock()
	a.closeDocs(v, v.docs)
	u := a.unsaved
	finished, saved := false, false
	a.saveUnsaved(appL(), u, func(ok bool) {
		finished, saved = true, ok
		if ok {
			u.then()
		}
	})
	pumpSaves(t, []*app{a}, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	clean.ed.Buffer.Set("new draft")
	close(release)
	pumpSaves(t, []*app{a}, func() bool { return finished })
	if saved || len(v.docs) != 2 || !clean.isModified() {
		t.Fatal("batch close discarded a new draft")
	}
}

func TestAsyncOldCloseKeepsNewQuit(t *testing.T) {
	a, f, d := saveFixture(t)
	base := d.baseVersion
	entered, release := make(chan struct{}), make(chan struct{})
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "files.version" {
			return base, nil
		}
		var args rpc.FileSaveParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return rpc.FileSaveResult{Version: fileSaveVersion(base.Path, string(args.Data)), Revision: args.Revision}, nil
	}
	f.mu.Unlock()
	a.askUnsaved([]*fileDoc{d}, func() { t.Error("old close completed") })
	tt := newTester(a.unsavedDialog, 800, 600)
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	pumpSaves(t, []*app{a}, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	a.quitting.Store(true)
	a.askUnsaved([]*fileDoc{d}, func() {})
	close(release)
	pumpSaves(t, []*app{a}, func() bool { return d.save == nil })
	if !a.quitting.Load() {
		t.Fatal("old save callback cancelled the new quit request")
	}
}

func TestAsyncSaveMovedPathCanRecover(t *testing.T) {
	a := &app{}
	root := t.TempDir()
	old, moved := filepath.Join(root, "old"), filepath.Join(root, "moved")
	if err := os.Mkdir(old, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(old, "file.txt")
	if err := os.WriteFile(path, []byte("submitted"), 0600); err != nil {
		t.Fatal(err)
	}
	version, err := files.ReadVersion(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := editor.NewBuffer("base")
	buffer.Set("submitted")
	s := &fileSave{path: path, buffer: buffer, snapshot: buffer.Snapshot(), hash: version.Hash}
	buffer.Set("new draft")
	if err := os.Rename(old, moved); err != nil {
		t.Fatal(err)
	}
	d := &fileDoc{path: filepath.Join(moved, "file.txt"), kind: docText, loadSeq: 1, base: version.Stamp, baseVersion: version, ed: editor.State{Buffer: buffer}, save: s}
	d.baseVersion.Path = ""
	if waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) || d.save != nil || !d.isModified() || !d.conflict {
		t.Fatal("old path left the document stuck")
	}
	if !waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) {
		t.Fatalf("save at moved path: %s", a.err)
	}
	if data, err := os.ReadFile(d.path); err != nil || string(data) != "new draft" {
		t.Fatal("moved draft not saved")
	}
}

func TestAsyncSaveConflictsAreSequential(t *testing.T) {
	for _, cancelSecond := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "cancel"}[cancelSecond], func(t *testing.T) {
			a := &app{files: make(map[string]*filesView)}
			v := &filesView{}
			a.files["w"] = v
			for _, name := range []string{"first.txt", "second.txt"} {
				path := filepath.Join(t.TempDir(), name)
				if err := os.WriteFile(path, []byte("base"), 0600); err != nil {
					t.Fatal(err)
				}
				version, err := files.ReadVersion(context.Background(), path)
				if err != nil {
					t.Fatal(err)
				}
				d := &fileDoc{path: path, kind: docText, base: version.Stamp, baseVersion: version, ed: editor.State{Buffer: editor.NewBuffer("base")}}
				d.ed.Buffer.Set("mine")
				if err := os.WriteFile(path, []byte("outside"), 0600); err != nil {
					t.Fatal(err)
				}
				v.docs = append(v.docs, d)
			}
			a.askUnsaved(v.docs, func() {})
			finished, saved := false, false
			a.saveUnsaved(appL(), a.unsaved, func(ok bool) { finished, saved = true, ok })
			pumpSaves(t, []*app{a}, func() bool { return a.confirming })
			if finished || !v.docs[0].isModified() {
				t.Fatal("first confirmation was skipped")
			}
			a.confirming = false
			a.confirmDo()
			pumpSaves(t, []*app{a}, func() bool { return a.confirming })
			if finished || v.docs[0].isModified() || !v.docs[1].isModified() {
				t.Fatal("confirmations overlapped")
			}
			if cancelSecond {
				a.cancelConfirmation()
			} else {
				a.confirmDo()
			}
			pumpSaves(t, []*app{a}, func() bool { return finished })
			if saved == cancelSecond || v.docs[1].isModified() != cancelSecond {
				t.Fatal("confirmation did not complete the batch correctly")
			}
		})
	}
}

func TestLocalSearchResumesAfterHostSwitch(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "after-list", true: "before-list"}[early], func(t *testing.T) { testLocalSearchSwitch(t, early) })
	}
}

func testLocalSearchSwitch(t *testing.T, early bool) {
	hosts, a, _ := hostViewsFixture(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "text.txt"), []byte("needle"), 0600); err != nil {
		t.Fatal(err)
	}
	w := workspace{ID: "w", Path: root}
	a.workspaces = []workspace{w}
	v := a.filesFor(&a.workspaces[0])
	v.search.query.Text = "needle"
	entered, release := make(chan struct{}), make(chan struct{})
	original := listRepos
	var calls atomic.Int32
	listRepos = func(ctx context.Context, root string, repos []string) (*files.List, error) {
		if calls.Add(1) != 1 {
			return original(ctx, root, repos)
		}
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return original(ctx, root, repos)
	}
	t.Cleanup(func() { listRepos = original })
	a.startSearch(v, 0)
	pumpSaves(t, []*app{a}, func() bool {
		select {
		case <-entered:
			return len(v.listWait) > 0
		default:
			return false
		}
	})
	hosts.activate("remote")
	if !early {
		close(release)
		pumpSaves(t, []*app{a}, func() bool { return !v.listing })
		if v.search.running {
			t.Fatal("hidden search remained running after its list callback was dropped")
		}
	}
	hosts.activate(localHostID)
	tt := newTester(func(c *ui.Context) { a.filesTick(c, &a.workspaces[0]) }, 800, 600)
	tt.Frame()
	if early {
		pumpSaves(t, []*app{a}, func() bool { return calls.Load() >= 2 })
		close(release)
	}
	pumpSaves(t, []*app{a}, func() bool { return !v.search.running })
	if len(v.search.hits) != 1 || !v.search.request.current() {
		t.Fatalf("local search was not restarted: %+v", v.search)
	}
}
