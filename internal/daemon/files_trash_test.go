package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func waitDesktopActive(t *testing.T, d *Daemon, count int) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		d.desktop.mu.Lock()
		active := d.desktop.active
		d.desktop.mu.Unlock()
		if active == count {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("active=%d, want %d", active, count)
		default:
			runtime.Gosched()
		}
	}
}

func trashCall(id rpc.DesktopIdentity, seq uint64, path string, entry files.EntryVersion) rpc.DesktopCall {
	data, _ := json.Marshal(rpc.FileTrashParams{Path: path, Expected: entry})
	return rpc.DesktopCall{Desktop: id, Sequence: seq, Method: "files.trash", Params: data}
}

func TestDesktopFileTrash(t *testing.T) {
	for _, kind := range []string{"file", "directory", "symlink", "broken", "loop", "failure", "replaced"} {
		t.Run(kind, func(t *testing.T) {
			d, _ := newTestDaemon(t)
			root := t.TempDir()
			path, target, destination := filepath.Join(root, "item"), filepath.Join(root, "target"), filepath.Join(t.TempDir(), "moved")
			if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "broken", "loop":
				link := map[string]string{"symlink": "target", "broken": "missing", "loop": "item"}[kind]
				if err := os.Symlink(link, path); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, []byte("selected"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var moves atomic.Int32
			socket := desktopServer(t, d, func(srv *rpc.Server) {
				srv.Handle("files.trash", func(ctx context.Context, raw json.RawMessage) (any, error) {
					return d.trashFileEntry(ctx, raw, func(path string) (string, error) {
						moves.Add(1)
						if kind == "failure" {
							return "", errors.New("trash unavailable")
						}
						return destination, os.Rename(path, destination)
					})
				})
			})
			viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
			params, _ := json.Marshal(rpc.FileVersionParams{Path: path})
			var entry files.EntryVersion
			if err := rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: "files.entry", Params: params}, &entry); err != nil {
				t.Fatal(err)
			}
			request := trashCall(viewer, 1, path, entry)
			desktopErrorCode(t, rpc.Call(t.Context(), socket, "files.trash", rpc.FileTrashParams{Path: path, Expected: entry}, nil), rpc.CodeReadOnly)
			desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", request, nil), rpc.CodeReadOnly)
			request.Desktop = desktopTakeTest(t, d, "owner")
			if kind == "replaced" {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out rpc.FileTrashResult
			err := rpc.Call(t.Context(), socket, "desktop.call", request, &out)
			if kind == "failure" || kind == "replaced" {
				if err == nil {
					t.Fatal("failed trash reported success")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatal("failed trash removed source")
				}
				if kind == "replaced" {
					desktopErrorCode(t, err, rpc.CodeConflict)
					if moves.Load() != 0 {
						t.Fatal("moved replacement")
					}
				}
				return
			}
			if err != nil || out.Destination != destination || moves.Load() != 1 {
				t.Fatalf("trash=%+v err=%v", out, err)
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatal("source remained")
			}
			if b, err := os.ReadFile(target); err != nil || string(b) != "target" {
				t.Fatal("trash followed final symlink")
			}
			if err := os.WriteFile(path, []byte("new file"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := rpc.Call(t.Context(), socket, "desktop.call", request, &out); err != nil || moves.Load() != 1 {
				t.Fatal("duplicate trash moved replacement")
			}
		})
	}
}

func TestDesktopTrashAndSaveCoordinateAlias(t *testing.T) {
	for _, directory := range []bool{false, true} {
		t.Run(map[bool]string{false: "file", true: "directory"}[directory], func(t *testing.T) {
			testDesktopTrashAndSaveCoordinateAlias(t, directory)
		})
	}
}

func testDesktopTrashAndSaveCoordinateAlias(t *testing.T, directory bool) {
	d, _ := newTestDaemon(t)
	root, outside := t.TempDir(), t.TempDir()
	target, link := filepath.Join(outside, "target"), filepath.Join(root, "link")
	linkTarget, savePath := target, link
	if directory {
		if err := os.Mkdir(filepath.Join(outside, "src"), 0700); err != nil {
			t.Fatal(err)
		}
		target = filepath.Join(outside, "src/main.go")
		linkTarget, savePath = outside, filepath.Join(link, "src/main.go")
	}
	if err := os.WriteFile(target, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linkTarget, link); err != nil {
		t.Fatal(err)
	}
	entry, err := files.ReadEntryVersion(t.Context(), link)
	if err != nil {
		t.Fatal(err)
	}
	version, err := files.ReadVersion(t.Context(), savePath)
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	socket := desktopServer(t, d, func(srv *rpc.Server) {
		srv.Handle("files.trash", func(ctx context.Context, raw json.RawMessage) (any, error) {
			return d.trashFileEntry(ctx, raw, func(path string) (string, error) {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return "", ctx.Err()
				}
				return filepath.Join(root, "trashed"), os.Rename(path, filepath.Join(root, "trashed"))
			})
		})
	})
	owner := desktopTakeTest(t, d, "owner")
	trashDone := make(chan error, 1)
	go func() {
		trashDone <- rpc.Call(t.Context(), socket, "desktop.call", trashCall(owner, 1, link, entry), nil)
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	saveDone := make(chan error, 1)
	go func() {
		saveDone <- rpc.Call(ctx, socket, "desktop.call", saveCall(owner, 2, saveParams(savePath, version, []byte("after"))), nil)
	}()
	// 等执行器接受保存：不能靠延时判断它是否被资源锁挡住。
	waitDesktopActive(t, d, 2)
	if b, err := os.ReadFile(target); err != nil || string(b) != "before" {
		t.Fatal("save bypassed link parent lock")
	}
	cancel()
	<-saveDone
	close(release)
	if err := <-trashDone; err != nil {
		t.Fatal(err)
	}
	waitDesktopActive(t, d, 0)
	if b, err := os.ReadFile(target); err != nil || string(b) != "before" {
		t.Fatal("save rewrote target after alias was trashed")
	}
}
