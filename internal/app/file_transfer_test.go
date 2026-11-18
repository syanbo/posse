package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui/editor"
	"posse/internal/files"
	"posse/internal/rpc"
)

func transferFixture(t *testing.T) (*app, *hostControlFixture, *filesView) {
	t.Helper()
	a, f := requestTestApp(t, "remote")
	root := filepath.Join(t.TempDir(), "remote")
	v := &filesView{wsID: "w", root: root, remote: true, tree: files.NewTree(root)}
	a.files = map[string]*filesView{"w": v}
	return a, f, v
}

func TestTargetFileUpload(t *testing.T) {
	for _, mode := range []string{"missing", "replace", "cancel", "changed-control", "new-confirmation", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v := transferFixture(t)
			takeRequestTestControl(t, a)
			source := filepath.Join(t.TempDir(), "file.bin")
			data := []byte{'a', '\r', '\n', 0xff, 0}
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(v.root, "file.bin")
			base := files.Version{Path: target, Stamp: files.Stamp{Missing: mode == "missing"}}
			doc := &fileDoc{path: target, kind: docText, ed: editor.State{Buffer: editor.NewBuffer("base")}}
			doc.ed.Buffer.Set("unsaved")
			v.docs = []*fileDoc{doc}
			var uploads atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.version":
					close(entered)
					if mode == "new-confirmation" {
						select {
						case <-release:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					}
					return base, nil
				case "files.save":
					uploads.Add(1)
					var args rpc.FileSaveParams
					if err := json.Unmarshal(p.Params, &args); err != nil {
						return nil, err
					}
					if args.Path != target || !args.Expected.Same(base) || !bytes.Equal(args.Data, data) {
						t.Errorf("wrong upload: %+v", args)
					}
					if mode == "unknown" {
						return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
					}
					return rpc.FileSaveResult{Version: base}, nil
				case "files.stat":
					return []rpc.FileStatResult{}, nil
				}
				return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
			}
			f.mu.Unlock()
			r := a.request()
			r.prepareUpload(appL(), v, source, target)
			if mode == "new-confirmation" {
				<-entered
				a.confirm("new confirmation", "", "OK", func() {})
				close(release)
				pumpSaves(t, []*app{a}, func() bool { return v.transfer == nil })
				if a.confirmTitle != "new confirmation" || uploads.Load() != 0 {
					t.Fatal("late upload replaced a newer confirmation")
				}
				return
			}
			if mode != "missing" {
				pumpSaves(t, []*app{a}, func() bool { return a.confirming })
				if uploads.Load() != 0 {
					t.Fatal("existing file uploaded without confirmation")
				}
				switch mode {
				case "cancel":
					a.cancelConfirmation()
				case "changed-control":
					a.host.detach()
					go a.confirmDo()
				default:
					go a.confirmDo()
				}
			}
			pumpSaves(t, []*app{a}, func() bool { return v.transfer == nil })
			want := int32(1)
			if mode == "cancel" || mode == "changed-control" {
				want = 0
			}
			if uploads.Load() != want || (a.err != "") != (mode == "unknown") {
				t.Fatalf("uploads=%d error=%q", uploads.Load(), a.err)
			}
			if doc.ed.Buffer.String() != "unsaved" || !doc.isModified() {
				t.Fatal("upload discarded the editor draft")
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("upload wrote a local target path")
			}
		})
	}
}

func TestTargetFileDownloadViewer(t *testing.T) {
	a, f, v := transferFixture(t)
	data := []byte{0, 0xff, '\r', '\n'}
	target := filepath.Join(v.root, "file.bin")
	destination := filepath.Join(t.TempDir(), "copy.bin")
	stamp := files.Stamp{Ino: 1, Size: int64(len(data)), Mode: 0600}
	sum := sha256.Sum256(data)
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.resourceRange":
			return rpc.FileRangeResult{Data: data, Stamp: stamp, EOF: true}, nil
		case "files.resourceVersion":
			return files.Version{Path: target, Stamp: stamp, Hash: hex.EncodeToString(sum[:])}, nil
		}
		return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
	}
	f.mu.Unlock()
	a.request().downloadFile(appL(), v, target, destination, false)
	pumpSaves(t, []*app{a}, func() bool { return v.transfer == nil })
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, data) || a.err != "" || len(a.toasts) != 1 {
		t.Fatalf("download: data=%v err=%v UI=%q", got, err, a.err)
	}
	if a.host.snapshot().Controlled {
		t.Fatal("download took desktop control")
	}
}

func TestTargetTransferDialogIdentity(t *testing.T) {
	for _, upload := range []bool{true, false} {
		t.Run(map[bool]string{true: "upload", false: "download"}[upload], func(t *testing.T) {
			a, f, v := transferFixture(t)
			takeRequestTestControl(t, a)
			answer := make(chan string)
			oldOpen, oldSave := openDialog, saveDialog
			t.Cleanup(func() { openDialog, saveDialog = oldOpen, oldSave })
			openDialog = func(mygo.OpenDialogOptions) ([]string, error) { return []string{<-answer}, nil }
			saveDialog = func(mygo.SaveDialogOptions) (string, error) { return <-answer, nil }
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) { calls.Add(1); return nil, nil }
			f.mu.Unlock()
			if upload {
				a.chooseUpload(appL(), v, v.root)
			} else {
				a.chooseDownload(appL(), v, filepath.Join(v.root, "file.bin"), false)
			}
			a.host.detach()
			answer <- filepath.Join(t.TempDir(), "file.bin")
			pumpSaves(t, []*app{a}, func() bool { return !a.dialogOpen })
			if calls.Load() != 0 || v.transfer != nil || a.err != "" {
				t.Fatal("dialog completion used a changed host identity")
			}
		})
	}
}

func TestTargetFolderUploadAndDownload(t *testing.T) {
	a, f, v := transferFixture(t)
	takeRequestTestControl(t, a)
	localSrc := filepath.Join(t.TempDir(), "bundle")
	if err := os.MkdirAll(filepath.Join(localSrc, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localSrc, "nested", "hello.txt"), []byte("folder body"), 0o600); err != nil {
		t.Fatal(err)
	}
	remoteDir := filepath.Join(t.TempDir(), "remote-root", "bundle")
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.stat":
			var q rpc.FilePathsParams
			json.Unmarshal(p.Params, &q)
			st, _ := files.Stat(q.Paths[0])
			return []rpc.FileStatResult{{Path: q.Paths[0], Stamp: st}}, nil
		case "files.mkdir":
			var q rpc.FileMkdirParams
			json.Unmarshal(p.Params, &q)
			return map[string]any{"path": q.Path}, files.MakeDir(t.Context(), q.Path)
		case "files.version":
			var q rpc.FileVersionParams
			json.Unmarshal(p.Params, &q)
			return files.ReadVersion(t.Context(), q.Path)
		case "files.save":
			var q rpc.FileSaveParams
			json.Unmarshal(p.Params, &q)
			if err := os.WriteFile(q.Path, q.Data, 0o600); err != nil {
				return nil, err
			}
			ver, _ := files.ReadVersion(t.Context(), q.Path)
			return rpc.FileSaveResult{Version: ver}, nil
		case "files.readDirs":
			var q rpc.FilePathsParams
			json.Unmarshal(p.Params, &q)
			entries, st, _ := files.ReadDir(q.Paths[0])
			return []rpc.FileDirectoryResult{{Path: q.Paths[0], Entries: entries, Stamp: st}}, nil
		case "files.resourceRange":
			var q rpc.FileResourceRangeParams
			json.Unmarshal(p.Params, &q)
			r, err := files.ReadResourceRange(t.Context(), q.Root, q.Path, q.Offset, q.Limit, q.Expected)
			return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
		case "files.resourceVersion":
			var q rpc.FileResourceVersionParams
			json.Unmarshal(p.Params, &q)
			return files.ReadResourceVersion(t.Context(), q.Root, q.Path)
		}
		return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected %s", p.Method)
	}
	f.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(remoteDir), 0o755); err != nil {
		t.Fatal(err)
	}
	a.request().prepareUploadFolder(appL(), v, localSrc, remoteDir)
	pumpSaves(t, []*app{a}, func() bool { return v.transfer == nil })
	if a.err != "" {
		t.Fatalf("folder upload error: %s", a.err)
	}
	if got, err := os.ReadFile(filepath.Join(remoteDir, "nested", "hello.txt")); err != nil || string(got) != "folder body" {
		t.Fatalf("remote folder content: %q %v", got, err)
	}

	// 查看端也可下载文件夹
	a.host.yield(a.host.snapshot())
	localCopy := filepath.Join(t.TempDir(), "downloaded-bundle")
	a.request().prepareDownloadFolder(appL(), v, remoteDir, localCopy)
	pumpSaves(t, []*app{a}, func() bool { return v.transfer == nil })
	if a.err != "" {
		t.Fatalf("folder download error: %s", a.err)
	}
	if got, err := os.ReadFile(filepath.Join(localCopy, "nested", "hello.txt")); err != nil || string(got) != "folder body" {
		t.Fatalf("downloaded folder content: %q %v", got, err)
	}
}
