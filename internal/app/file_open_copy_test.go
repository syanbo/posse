package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"posse/internal/files"
	"posse/internal/rpc"
	"posse/locales"
)

func TestTargetFileOpenCopy(t *testing.T) {
	for _, mode := range []string{"success", "download-failure", "bad-hash", "local-conflict", "changed-host", "changed-view", "open-failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			a, f, v := transferFixture(t)
			messages := appL()
			if mode == "success" {
				messages = locales.In("zh-Hans")
			}
			data := []byte{0, 0xff, '\r', '\n'}
			target := filepath.Join(v.root, "remote.bin")
			destination := filepath.Join(t.TempDir(), "saved.bin")
			stamp := files.Stamp{Ino: 1, Size: int64(len(data)), Mode: 0600}
			sum := sha256.Sum256(data)
			entered := atomic.Bool{}
			release := make(chan struct{})
			unblock := sync.OnceFunc(func() { close(release) })
			defer unblock()
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.resourceRange":
					entered.Store(true)
					select {
					case <-release:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					if mode == "download-failure" {
						return nil, rpc.Errorf(rpc.CodePermissionDenied, "read denied")
					}
					return rpc.FileRangeResult{Data: data, Stamp: stamp, EOF: true}, nil
				case "files.resourceVersion":
					hash := hex.EncodeToString(sum[:])
					if mode == "bad-hash" {
						hash = strings.Repeat("0", 64)
					}
					return files.Version{Path: target, Stamp: stamp, Hash: hash}, nil
				}
				return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected method")
			}
			f.mu.Unlock()
			oldSave, oldOpen := saveDialog, openDownloadedCopy
			t.Cleanup(func() { saveDialog, openDownloadedCopy = oldSave, oldOpen })
			var dialogs atomic.Int32
			saveDialog = func(o mygo.SaveDialogOptions) (string, error) {
				dialogs.Add(1)
				if o.Title != messages.FilesSaveCopyAndOpen() || o.DefaultPath != "remote.bin" {
					t.Errorf("copy dialog = %+v", o)
				}
				if mode == "cancel" {
					return "", nil
				}
				return destination, nil
			}
			opened := 0
			openDownloadedCopy = func(path string) error {
				opened++
				got, err := os.ReadFile(path)
				if path != destination || err != nil || !bytes.Equal(got, data) {
					t.Errorf("opened before verified copy was saved: %q %v %v", path, got, err)
				}
				if mode == "open-failure" {
					return errors.New("no application")
				}
				return nil
			}
			if mode == "success" {
				d := &fileDoc{path: target, kind: docBinary}
				tt := newTester(func(c *ui.Context) { a.docMessage(c, v, d, L(c).FilesBinaryNoContent()) }, 600, 400)
				settle(tt)
				if err := tt.Click(messages.FilesOpenCopy()); err != nil {
					t.Fatal(err)
				}
			} else {
				a.chooseDownload(appL(), v, target, true)
			}
			if mode == "cancel" {
				pumpSaves(t, []*app{a}, func() bool { return !a.dialogOpen })
				if entered.Load() || opened != 0 || v.transfer != nil || dialogs.Load() != 1 {
					t.Fatal("cancel started a download or opened a file")
				}
				return
			}
			pumpSaves(t, []*app{a}, entered.Load)
			if opened != 0 {
				t.Fatal("opened while download was pending")
			}
			switch mode {
			case "local-conflict":
				if err := os.WriteFile(destination, []byte("new local content"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-host":
				a.host.detach()
			case "changed-view":
				a.viewGeneration.Add(1)
			}
			unblock()
			pumpSaves(t, []*app{a}, func() bool { return v.transfer == nil })
			wantOpen := 0
			if mode == "success" || mode == "open-failure" {
				wantOpen = 1
			}
			if opened != wantOpen || dialogs.Load() != 1 || a.host.snapshot().Controlled {
				t.Fatalf("opened=%d dialogs=%d control=%v", opened, dialogs.Load(), a.host.snapshot().Controlled)
			}
			wantError := mode == "download-failure" || mode == "bad-hash" || mode == "local-conflict" || mode == "open-failure"
			if (a.err != "") != wantError {
				t.Fatalf("UI error = %q", a.err)
			}
			if mode == "local-conflict" {
				got, err := os.ReadFile(destination)
				if err != nil || string(got) != "new local content" {
					t.Fatalf("overwrote local change: %q %v", got, err)
				}
			} else if mode == "download-failure" || mode == "bad-hash" {
				if _, err := os.Stat(destination); !os.IsNotExist(err) {
					t.Fatal("failed download left a copy")
				}
			} else if mode == "open-failure" {
				got, err := os.ReadFile(destination)
				if err != nil || !bytes.Equal(got, data) || a.err != appL().FilesOpenCopyFailed("remote.bin", "no application") {
					t.Fatalf("opening failure lost saved copy: %v %v %q", got, err, a.err)
				}
			}
		})
	}
}
