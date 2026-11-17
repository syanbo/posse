package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"posse/internal/files"
	"posse/internal/rpc"
)

func pumpSaves(t *testing.T, apps []*app, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		for _, a := range apps {
			a.runPending()
		}
		if ready() {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("file save did not settle")
		default:
			runtime.Gosched()
		}
	}
}

func waitSave(t *testing.T, apps []*app, start func(func(bool))) bool {
	t.Helper()
	finished, saved := false, false
	start(func(ok bool) { finished, saved = true, ok })
	pumpSaves(t, apps, func() bool { return finished })
	return saved
}

func fileSaveVersion(path, text string) files.Version {
	sum := sha256.Sum256([]byte(text))
	return files.Version{Path: path, Stamp: files.Stamp{Ino: 42, Size: int64(len(text)), Mode: 0600}, Hash: hex.EncodeToString(sum[:])}
}

func saveFixture(t *testing.T) (*app, *hostControlFixture, *fileDoc) {
	t.Helper()
	a, f, path, _ := remoteFileFixture(t, "base")
	takeRequestTestControl(t, a)
	base := fileSaveVersion("/target/real.txt", "base")
	d := &fileDoc{path: path, kind: docText, base: base.Stamp, baseVersion: base, ed: editor.State{Buffer: editor.NewBuffer("base")}}
	d.ed.Buffer.Set("submitted")
	a.files = map[string]*filesView{"w": {docs: []*fileDoc{d}}}
	return a, f, d
}

func TestAsyncFileSaveSnapshot(t *testing.T) {
	for _, mode := range []string{"normal", "edited", "identity", "moved"} {
		t.Run(mode, func(t *testing.T) {
			a, f, d := saveFixture(t)
			base := d.baseVersion
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.version":
					return base, nil
				case "files.save":
					calls.Add(1)
					var args rpc.FileSaveParams
					if err := json.Unmarshal(p.Params, &args); err != nil {
						return nil, err
					}
					if args.Path != d.path || !args.Expected.Same(base) || string(args.Data) != "submitted" || args.Hash != fileSaveVersion("", "submitted").Hash {
						t.Error("submitted data or baseline changed")
					}
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return rpc.FileSaveResult{Version: fileSaveVersion(base.Path, string(args.Data)), Revision: args.Revision}, nil
				}
				return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
			}
			f.mu.Unlock()
			finished, saved := false, false
			a.saveDoc(appL(), d, func(ok bool) { finished, saved = true, ok })
			pumpSaves(t, []*app{a}, func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			})
			if finished || !d.needsSave() {
				t.Fatal("save completed before reply")
			}
			// 同一文档的第二次保存不能排入另一份正文；重载也不能清掉提交状态。
			a.saveDoc(appL(), d, func(ok bool) {
				if ok {
					t.Error("concurrent save succeeded")
				}
			})
			a.reloadDoc(d)
			switch mode {
			case "edited":
				d.ed.Buffer.Set("newer")
			case "identity":
				takeRequestTestControl(t, a)
			case "moved":
				d.path += ".moved"
				d.loadSeq++
			}
			close(release)
			pumpSaves(t, []*app{a}, func() bool { return finished })
			if calls.Load() != 1 || saved != (mode == "normal") {
				t.Fatalf("calls=%d saved=%v", calls.Load(), saved)
			}
			if mode == "normal" || mode == "edited" {
				if d.save != nil || d.baseVersion.Hash != fileSaveVersion("", "submitted").Hash {
					t.Fatal("saved baseline missing")
				}
				if mode == "edited" {
					if !d.isModified() || d.ed.Buffer.String() != "newer" {
						t.Fatal("newer edit lost")
					}
					d.ed.Buffer.Undo()
					if d.isModified() {
						t.Fatal("undo to saved snapshot is dirty")
					}
				}
			} else if d.save == nil || !d.isModified() {
				t.Fatal("stale save discarded unconfirmed draft")
			}
			if text, err := os.ReadFile(filepath.Join(filepath.Dir(d.path), "same.txt")); err != nil || string(text) != "local contents" {
				t.Fatal("remote save touched local file")
			}
		})
	}
}

func TestAsyncFileSaveConflict(t *testing.T) {
	for _, mode := range []string{"confirmed", "changed-again", "identity"} {
		t.Run(mode, func(t *testing.T) {
			a := &app{}
			path := filepath.Join(t.TempDir(), "doc.txt")
			if err := os.WriteFile(path, []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			base, err := files.ReadVersion(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			d := &fileDoc{path: path, kind: docText, base: base.Stamp, baseVersion: base, ed: editor.State{Buffer: editor.NewBuffer("base")}}
			d.ed.Buffer.Set("mine")
			if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
				t.Fatal(err)
			}
			finished, saved := false, false
			a.saveDoc(appL(), d, func(ok bool) { finished, saved = true, ok })
			pumpSaves(t, []*app{a}, func() bool { return a.confirming })
			if finished {
				t.Fatal("save finished before overwrite decision")
			}
			if mode == "changed-again" {
				if err := os.WriteFile(path, []byte("latest"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "identity" {
				a.viewGeneration.Add(1)
			}
			a.confirmDo()
			pumpSaves(t, []*app{a}, func() bool { return finished })
			if saved != (mode == "confirmed") {
				t.Fatal("incorrect save callback")
			}
			want := map[string]string{"confirmed": "mine", "changed-again": "latest", "identity": "other"}[mode]
			if text, err := os.ReadFile(path); err != nil || string(text) != want {
				t.Fatalf("disk=%q err=%v", text, err)
			}
			if d.isModified() != (mode != "confirmed") {
				t.Fatal("incorrect dirty state")
			}
		})
	}
}

func TestAsyncFileSaveUnknown(t *testing.T) {
	a, f, d := saveFixture(t)
	base := d.baseVersion
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method == "files.version" {
			return base, nil
		}
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "lost response")
	}
	f.mu.Unlock()
	if waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) || d.save == nil || d.save.ref == nil || !d.isModified() {
		t.Fatal("unknown save was forgotten")
	}
	if calls.Load() != 1 {
		t.Fatal("save replayed")
	}
	ref := *d.save.ref
	f.mu.Lock()
	f.operation = func(got rpc.DesktopOperationRef) (any, error) {
		if got != ref {
			t.Error("reconciliation changed original operation reference")
		}
		return rpc.DesktopOperation{DesktopOperationRef: got, State: "running", Method: "files.save"}, nil
	}
	f.mu.Unlock()
	if waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) || d.save == nil {
		t.Fatal("running operation was discarded")
	}
	f.mu.Lock()
	f.operation = func(got rpc.DesktopOperationRef) (any, error) {
		return rpc.DesktopOperation{DesktopOperationRef: got, State: "unknown"}, nil
	}
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "files.version" {
			t.Error("reconciliation wrote a file")
		}
		return fileSaveVersion(base.Path, "submitted"), nil
	}
	f.mu.Unlock()
	if !waitSave(t, []*app{a}, func(done func(bool)) { a.saveDoc(appL(), d, done) }) || d.isModified() || d.save != nil {
		t.Fatal("matching current content did not confirm snapshot")
	}
}

func TestAsyncSaveUnsavedWaits(t *testing.T) {
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
	a.askUnsaved([]*fileDoc{d}, func() {})
	u := a.unsaved
	u.windowClose = true
	finished, saved := false, false
	a.saveUnsaved(appL(), u, func(ok bool) { finished, saved = true, ok })
	pumpSaves(t, []*app{a}, func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	if finished {
		t.Fatal("close finished while save was pending")
	}
	d.ed.Buffer.Set("new draft")
	close(release)
	pumpSaves(t, []*app{a}, func() bool { return finished })
	if saved || !d.isModified() {
		t.Fatal("close discarded editing during save")
	}
}
