package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func TestFileWindowRawVersion(t *testing.T) {
	a := &app{}
	path := filepath.Join(t.TempDir(), "file.txt")
	text := "raw\r\ntext"
	if err := os.WriteFile(path, []byte(text), 0o640); err != nil {
		t.Fatal(err)
	}
	d := &fileDoc{path: path, kind: docLoading}
	a.loadDoc(d)
	waitFileUpdate(t, a)
	a.runPending()
	sum := sha256.Sum256([]byte(text))
	real, _ := filepath.EvalSymlinks(path)
	if d.kind != docText || d.ed.Buffer.String() != "raw\ntext" || d.baseVersion.Path != real || d.baseVersion.Hash != hex.EncodeToString(sum[:]) || !d.baseVersion.Stamp.Same(d.base) {
		t.Fatalf("raw bytes lost during editor normalization: kind=%v version=%+v", d.kind, d.baseVersion)
	}
}

func TestFileWindowVersionFailurePreservesDocument(t *testing.T) {
	for _, poll := range []bool{false, true} {
		t.Run(map[bool]string{false: "reload", true: "poll"}[poll], func(t *testing.T) {
			a, f, path, _ := remoteFileFixture(t, "target contents")
			d := &fileDoc{path: path, kind: docText, base: files.Stamp{Ino: 1}, baseVersion: files.Version{Path: "/before", Hash: "before"}, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			before := d.baseVersion
			f.mu.Lock()
			normal := f.desktopCall
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				if p.Method == "files.version" {
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
				}
				return normal(ctx, p)
			}
			f.mu.Unlock()
			if poll {
				a.pollFiles(&filesView{tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}})
			} else {
				a.loadDoc(d)
			}
			waitFileUpdate(t, a)
			a.runPending()
			if d.kind != docText || d.ed.Buffer.String() != "keep" || !d.baseVersion.Same(before) || d.deleted || a.err == "" {
				t.Fatalf("failed baseline damaged document: kind=%v base=%+v err=%s", d.kind, d.baseVersion, a.err)
			}
		})
	}
}

func TestFileWindowVersionRejectsStaleRead(t *testing.T) {
	for _, poll := range []bool{false, true} {
		t.Run(map[bool]string{false: "reload", true: "poll"}[poll], func(t *testing.T) {
			a, _, path, st := remoteFileFixture(t, "target contents")
			d := &fileDoc{path: path, kind: docText, base: st, ed: editor.State{Buffer: editor.NewBuffer("keep")}}
			if poll {
				a.pollFiles(&filesView{tree: files.NewTree(filepath.Dir(path)), docs: []*fileDoc{d}})
			} else {
				a.loadDoc(d)
			}
			waitFileUpdate(t, a)
			// 完整版本已被更新，即使磁盘戳相同，也不能应用先前排队的读取。
			d.baseVersion = files.Version{Path: "/new-baseline", Stamp: st, Hash: "new"}
			a.runPending()
			if d.ed.Buffer.String() != "keep" || d.baseVersion.Path != "/new-baseline" {
				t.Fatal("stale read replaced newer baseline")
			}
		})
	}
}
