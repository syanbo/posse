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

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func TestTargetFileTrashConfirmationOrder(t *testing.T) {
	for _, mode := range []string{"new-trash", "other-confirmation", "stale-error"} {
		t.Run(mode, func(t *testing.T) {
			a, f, path, _ := remoteFileFixture(t, "target")
			takeRequestTestControl(t, a)
			v := &filesView{root: filepath.Dir(path), tree: files.NewTree(filepath.Dir(path))}
			entered, unblock := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(unblock) })
			var trashed string
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.entry":
					var args rpc.FileVersionParams
					if err := json.Unmarshal(p.Params, &args); err != nil {
						return nil, err
					}
					if args.Path == path {
						close(entered)
						select {
						case <-unblock:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						if mode == "stale-error" {
							return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
						}
					}
					return files.EntryVersion{Path: args.Path, Stamp: files.Stamp{Mode: 0600, Ino: 42}}, nil
				case "files.trash":
					var args rpc.FileTrashParams
					if err := json.Unmarshal(p.Params, &args); err != nil {
						return nil, err
					}
					f.mu.Lock()
					trashed = args.Path
					f.mu.Unlock()
					return rpc.FileTrashResult{Destination: "/target/.Trash/item"}, nil
				}
				return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
			}
			f.mu.Unlock()
			a.askTrashPath(appL(), v, path, false)
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("first entry request did not start")
			}
			otherChosen := false
			if mode == "new-trash" {
				a.askTrashPath(appL(), v, path+"-new", false)
				pumpSaves(t, []*app{a}, func() bool { return a.confirming })
			} else {
				a.confirm("new confirmation", "body", "confirm", func() { otherChosen = true })
			}
			seq := a.confirmSeq
			once.Do(func() { close(unblock) })
			waitFileUpdate(t, a)
			a.runPending()
			if a.confirmSeq != seq || a.err != "" {
				t.Fatal("late entry response changed the current confirmation or error")
			}
			a.confirmDo()
			a.runPending()
			f.mu.Lock()
			got := trashed
			f.mu.Unlock()
			if mode == "new-trash" && got != path+"-new" || mode != "new-trash" && (!otherChosen || got != "") {
				t.Fatalf("late entry response replaced confirmation: trashed=%q other=%v", got, otherChosen)
			}
		})
	}
}

func TestTargetFileTrash(t *testing.T) {
	for _, mode := range []string{"success", "failure", "unknown", "identity"} {
		t.Run(mode, func(t *testing.T) {
			a, f, path, _ := remoteFileFixture(t, "target")
			takeRequestTestControl(t, a)
			entry := files.EntryVersion{Path: "/target/item", Device: 12, Stamp: files.Stamp{Ino: 42, Size: 6, Mode: 0600}}
			dirty := &fileDoc{path: path, kind: docText, ed: editor.State{Buffer: editor.NewBuffer("base")}}
			dirty.ed.Buffer.Set("draft")
			pending := &fileDoc{path: path, kind: docText, ed: editor.State{Buffer: editor.NewBuffer("clean")}, save: &fileSave{busy: true}}
			v := &filesView{root: filepath.Dir(path), tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{dirty, pending}, listAt: time.Now()}
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "files.entry" {
					return entry, nil
				}
				if p.Method != "files.trash" {
					t.Error("unexpected method")
				}
				calls.Add(1)
				var args rpc.FileTrashParams
				if err := json.Unmarshal(p.Params, &args); err != nil {
					return nil, err
				}
				if args.Path != path || !args.Expected.Same(entry) || p.Sequence == 0 {
					t.Error("trash lost confirmed identity")
				}
				if mode == "failure" {
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
				}
				if mode == "unknown" {
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "unknown")
				}
				return rpc.FileTrashResult{Destination: "/target/.Trash/item"}, nil
			}
			f.mu.Unlock()
			a.askTrashPath(appL(), v, path, false)
			pumpSaves(t, []*app{a}, func() bool { return a.confirming })
			if mode == "identity" {
				takeRequestTestControl(t, a)
			}
			a.confirmDo()
			a.runPending()
			if mode == "identity" {
				if calls.Load() != 0 {
					t.Fatal("stale confirmation submitted trash")
				}
			} else if calls.Load() != 1 {
				t.Fatal("trash was not submitted exactly once")
			}
			if mode == "success" && !v.listAt.IsZero() {
				t.Fatal("successful trash did not invalidate list")
			}
			if mode == "failure" || mode == "unknown" {
				if a.err == "" || v.listAt.IsZero() {
					t.Fatal("unconfirmed trash cleaned up the view")
				}
			}
			if len(v.docs) != 2 || !dirty.isModified() || pending.save == nil {
				t.Fatal("trash discarded a draft or pending save")
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "local contents" {
				t.Fatal("remote trash touched local file")
			}
		})
	}
}
