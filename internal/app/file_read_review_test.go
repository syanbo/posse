package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func TestFileReloadDoesNotUndoSave(t *testing.T) {
	a := &app{}
	path := filepath.Join(t.TempDir(), "document.txt")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	d := &fileDoc{path: path, kind: docText, base: files.StatFile(path), ed: editor.State{Buffer: editor.NewBuffer("old")}}
	d.ed.Insert("draft")
	a.reloadDoc(d)
	waitFileUpdate(t, a)
	expected := d.ed.Buffer.String()
	if !waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) {
		t.Fatalf("save: %s", a.err)
	}
	a.runPending()
	if d.ed.Buffer.String() != expected || !d.ed.Buffer.CanUndo() {
		t.Fatal("queued reload replaced saved content or undo history")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != expected {
		t.Fatalf("disk content=%q err=%v", b, err)
	}
}

func TestFileTrashConfirmationExpires(t *testing.T) {
	a, f := requestTestApp(t, localHostID)
	takeRequestTestControl(t, a)
	path := filepath.Join(t.TempDir(), "keep.txt")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) { return files.ReadEntryVersion(ctx, path) }
	f.mu.Unlock()
	a.askTrashPath(appL(), &filesView{}, path, false)
	pumpSaves(t, []*app{a}, func() bool { return a.confirmDo != nil })
	if a.confirmDo == nil {
		t.Fatal("confirmation missing")
	}
	takeRequestTestControl(t, a)
	a.confirmDo()
	a.runPending()
	if b, err := os.ReadFile(path); err != nil || string(b) != "keep" {
		t.Fatalf("expired confirmation deleted file: %q %v", b, err)
	}
}

func TestTargetDirectoryReadOrdering(t *testing.T) {
	for _, pollFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "load-first", true: "poll-first"}[pollFirst], func(t *testing.T) {
			a, f, path, _ := remoteFileFixture(t, "target")
			root := filepath.Dir(path)
			v := &filesView{root: root, tree: files.NewTree(root)}
			v.tree.Set(root, []files.Entry{{Name: "before"}}, files.Stamp{Ino: 1}, nil)
			var reads atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.stat":
					return []rpc.FileStatResult{{Path: root, Stamp: files.Stamp{Ino: 2}}}, nil
				case "files.readDirs":
					if reads.Add(1) == 1 {
						return []rpc.FileDirectoryResult{{Path: root, Error: rpc.Errorf(rpc.CodeNotFound, "gone")}}, nil
					}
					return []rpc.FileDirectoryResult{{Path: root, Stamp: files.Stamp{Ino: 2}, Entries: []files.Entry{{Name: "new"}}}}, nil
				}
				return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
			}
			f.mu.Unlock()
			if pollFirst {
				a.pollFiles(v)
			} else {
				a.loadDirs(v, []string{root})
			}
			waitFileUpdate(t, a)
			a.pendingMu.Lock()
			old := a.pending
			a.pending = nil
			a.pendingMu.Unlock()
			if pollFirst {
				a.loadDirs(v, []string{root})
			} else {
				a.pollFiles(v)
			}
			waitFileUpdate(t, a)
			a.runPending()
			for _, apply := range old {
				apply()
			}
			if rows := v.tree.Rows(); len(rows) != 1 || rows[0].Name != "new" {
				t.Fatalf("old missing result erased new tree: %+v", rows)
			}
		})
	}
}

func TestTargetDirtyPollSkipsContent(t *testing.T) {
	for _, undo := range []bool{false, true} {
		t.Run(map[bool]string{false: "dirty", true: "undo-while-reading"}[undo], func(t *testing.T) {
			a, f, path, st := remoteFileFixture(t, "target")
			d := &fileDoc{path: path, kind: docText, base: files.Stamp{Ino: 1}, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			d.ed.Insert("draft")
			v := &filesView{tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}}
			var ranges atomic.Int32
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "files.readRange" {
					ranges.Add(1)
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			a.pollFiles(v)
			waitFileUpdate(t, a)
			if undo {
				d.ed.Buffer.Undo()
			}
			expected := d.ed.Buffer.String()
			a.runPending()
			if ranges.Load() != 0 || d.ed.Buffer.String() != expected {
				t.Fatal("dirty poll downloaded content or erased buffer")
			}
			if undo {
				if d.seen.Same(st) {
					t.Fatal("skipped content was marked current after undo")
				}
				a.pollFiles(v)
				waitFileUpdate(t, a)
				a.runPending()
				if d.ed.Buffer.String() != "target" {
					t.Fatal("clean document failed to reload next poll")
				}
			} else if !d.conflict || !d.seen.Same(st) {
				t.Fatal("dirty document did not track external change")
			}
		})
	}
}
