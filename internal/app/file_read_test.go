package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func waitFileUpdate(t *testing.T, a *app) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.pendingMu.Lock()
		ready := len(a.pending) > 0
		a.pendingMu.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("file update was not queued")
}

func remoteFileFixture(t *testing.T, text string) (*app, *hostControlFixture, string, files.Stamp) {
	t.Helper()
	a, f := requestTestApp(t, "remote")
	path := filepath.Join(t.TempDir(), "same.txt")
	if err := os.WriteFile(path, []byte("local contents"), 0600); err != nil {
		t.Fatal(err)
	}
	st := files.Stamp{Size: int64(len(text)), Ino: 42, ModTime: time.Unix(123, 0), Mode: 0600}
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.stat":
			var args rpc.FilePathsParams
			if err := json.Unmarshal(p.Params, &args); err != nil {
				return nil, err
			}
			out := make([]rpc.FileStatResult, len(args.Paths))
			for i, path := range args.Paths {
				out[i] = rpc.FileStatResult{Path: path, Stamp: st}
			}
			return out, nil
		case "files.readDirs":
			return []rpc.FileDirectoryResult{{Path: filepath.Dir(path), Entries: []files.Entry{{Name: "target-only.txt"}}, Stamp: st}}, nil
		case "files.readRange":
			return rpc.FileRangeResult{Data: []byte(text), Stamp: st, EOF: true}, nil
		case "files.version":
			sum := sha256.Sum256([]byte(text))
			return files.Version{Path: "/target/real.txt", Stamp: st, Hash: hex.EncodeToString(sum[:])}, nil
		}
		return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
	}
	f.mu.Unlock()
	return a, f, path, st
}

func TestTargetFileWindowReads(t *testing.T) {
	a, _, path, st := remoteFileFixture(t, "target contents")
	v := &filesView{root: filepath.Dir(path), tree: files.NewTree(filepath.Dir(path))}
	a.loadDirs(v, []string{v.root})
	waitFileUpdate(t, a)
	a.runPending()
	if rows := v.tree.Rows(); len(rows) != 1 || rows[0].Name != "target-only.txt" {
		t.Fatalf("target tree: %+v", rows)
	}
	d := &fileDoc{path: path, kind: docLoading, started: true}
	a.loadDoc(d)
	waitFileUpdate(t, a)
	a.runPending()
	if d.kind != docText || d.ed.Buffer.String() != "target contents" || !d.base.Same(st) || !d.baseVersion.Stamp.Same(st) || d.baseVersion.Path != "/target/real.txt" || d.baseVersion.Hash == "" {
		t.Fatalf("target document: kind=%v stamp=%+v hash=%q", d.kind, d.base, d.baseVersion.Hash)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "local contents" {
		t.Fatalf("local file changed: %q %v", b, err)
	}
}

func TestTargetFileLoadDiscardsStaleResults(t *testing.T) {
	for _, mode := range []string{"edit", "new-load", "path", "identity"} {
		t.Run(mode, func(t *testing.T) {
			a, _, path, _ := remoteFileFixture(t, "target contents")
			d := &fileDoc{path: path, kind: docText, started: true, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			a.loadDoc(d)
			waitFileUpdate(t, a)
			switch mode {
			case "edit":
				d.ed.Insert("draft")
			case "new-load":
				d.loadSeq++
			case "path":
				d.path += ".moved"
			case "identity":
				takeRequestTestControl(t, a)
			}
			expected := d.ed.Buffer.String()
			a.runPending()
			if d.ed.Buffer.String() != expected || d.baseVersion.Hash != "" {
				t.Fatal("stale read replaced document")
			}
		})
	}
}

func TestTargetFilePollPreservesOnErrors(t *testing.T) {
	for _, mode := range []string{"offline", "permission", "range"} {
		t.Run(mode, func(t *testing.T) {
			a, f, path, st := remoteFileFixture(t, "target contents")
			d := &fileDoc{path: path, kind: docText, base: files.Stamp{Ino: 1}, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			if mode != "range" {
				d.ed.Insert("draft")
			}
			v := &filesView{root: filepath.Dir(path), tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}}
			v.tree.Set(v.root, []files.Entry{{Name: "keep"}}, files.Stamp{Ino: 1}, nil)
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if mode == "offline" {
					return nil, rpc.Errorf(rpc.CodeInternal, "offline")
				}
				if mode == "range" && p.Method == "files.readRange" {
					return nil, rpc.Errorf(rpc.CodeConflict, "changed")
				}
				if p.Method == "files.stat" && mode == "permission" {
					var args rpc.FilePathsParams
					if err := json.Unmarshal(p.Params, &args); err != nil {
						return nil, err
					}
					out := make([]rpc.FileStatResult, len(args.Paths))
					for i, path := range args.Paths {
						out[i] = rpc.FileStatResult{Path: path, Error: rpc.Errorf(rpc.CodePermissionDenied, "denied")}
					}
					return out, nil
				}
				if p.Method == "files.readDirs" {
					return []rpc.FileDirectoryResult{{Path: v.root, Stamp: st, Error: rpc.Errorf(rpc.CodePermissionDenied, "denied")}}, nil
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			before := d.ed.Buffer.String()
			a.pollFiles(v)
			waitFileUpdate(t, a)
			a.runPending()
			if d.ed.Buffer.String() != before || d.deleted || d.conflict || a.err == "" {
				t.Fatalf("read error damaged document or vanished: deleted=%v conflict=%v err=%q", d.deleted, d.conflict, a.err)
			}
			if rows := v.tree.Rows(); len(rows) != 1 || rows[0].Name != "keep" {
				t.Fatalf("read error cleared tree: %+v", rows)
			}
		})
	}
}

func TestTargetFilePollRejectsChangedBaseline(t *testing.T) {
	for _, mode := range []string{"save", "reload", "edit", "identity"} {
		t.Run(mode, func(t *testing.T) {
			a, _, path, _ := remoteFileFixture(t, "target contents")
			d := &fileDoc{path: path, kind: docText, base: files.Stamp{Ino: 1}, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			v := &filesView{tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}}
			a.pollFiles(v)
			waitFileUpdate(t, a)
			switch mode {
			case "save":
				d.base = files.Stamp{Ino: 5}
			case "reload":
				d.loadSeq++
			case "edit":
				d.ed.Insert("draft")
			case "identity":
				takeRequestTestControl(t, a)
			}
			before := d.ed.Buffer.String()
			a.runPending()
			if d.ed.Buffer.String() != before || d.baseVersion.Hash != "" {
				t.Fatal("stale poll replaced document")
			}
			if mode == "edit" && !d.conflict {
				t.Fatal("external change did not mark dirty document conflicting")
			}
		})
	}
}

func TestTargetFileEditorControl(t *testing.T) {
	a, _, path, _ := remoteFileFixture(t, "{}")
	d := &fileDoc{path: path + ".json", kind: docText, ed: editor.State{Buffer: editor.NewBuffer(`{"x":1}`)}}
	if a.canEditDoc(d) {
		t.Fatal("viewer may edit")
	}
	a.formatJSON(appL(), d)
	if d.ed.Buffer.String() != `{"x":1}` {
		t.Fatal("viewer formatted document")
	}
	takeRequestTestControl(t, a)
	if !a.canEditDoc(d) {
		t.Fatal("controller cannot edit")
	}
	d.readOnly = true
	if a.canEditDoc(d) {
		t.Fatal("large file became editable")
	}
}

func TestTargetFileViewerFrame(t *testing.T) {
	a := previewApp(t)
	v := previewFiles(t, a, "main.go")
	d := v.cur
	h, _ := requestTestApp(t, "remote")
	a.host = h.host
	a.viewID, v.remote = "remote", true
	tt := newPreviewTester(a, false)
	before := d.ed.Buffer.String()
	tt.Type("blocked")
	tt.Frame()
	if !d.ed.ReadOnly || d.ed.Buffer.String() != before {
		t.Fatal("viewer editor accepted input")
	}
	takeRequestTestControl(t, a)
	tt.Frame()
	if d.ed.ReadOnly {
		t.Fatal("controller editor remained read-only")
	}
}

func TestPreviewTargetFiles(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		v := previewFiles(t, a, "main.go")
		h, _ := requestTestApp(t, "remote")
		a.host = h.host
		a.viewID, v.remote = "remote", true
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "target-files-"+suffix+".png")
	}
}

func TestTargetWikiDoesNotUseLocalPath(t *testing.T) {
	a, f, path, _ := remoteFileFixture(t, "target contents")
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		var args rpc.FilePathsParams
		if err := json.Unmarshal(p.Params, &args); err != nil {
			return nil, err
		}
		out := make([]rpc.FileStatResult, len(args.Paths))
		for i, path := range args.Paths {
			out[i] = rpc.FileStatResult{Path: path, Stamp: files.Stamp{Missing: true}}
		}
		return out, nil
	}
	f.mu.Unlock()
	a.openWiki(appL(), &workspace{Path: filepath.Dir(path)}, path, "same.txt")
	waitFileUpdate(t, a)
	a.runPending()
	if a.err == "" || len(a.files) > 0 {
		t.Fatal("wiki opened local path absent on target")
	}
}
