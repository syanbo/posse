package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func transferVersion(t *testing.T, path string) files.Version {
	t.Helper()
	v, err := files.ReadVersion(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func transferWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func transferContents(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("contents of %s: %d bytes, want %d; %v", path, len(got), len(want), err)
	}
}

func TestUploadFileBytesAndDestination(t *testing.T) {
	for _, mode := range []string{"existing", "missing", "conflict", "lost-response", "empty", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			local, target := t.TempDir(), t.TempDir()
			source := filepath.Join(local, "source.bin")
			// 远端和本机可以存在同名绝对路径；RPC 服务映射到独立目录验证不误写本机。
			decoy := filepath.Join(local, "destination.bin")
			destination := filepath.Join(target, "destination.bin")
			data := bytes.Repeat([]byte{0, 0xff, '\r', '\n', 0xfe}, files.RangeLimit/5+13)
			if mode == "empty" {
				data = nil
			}
			transferWrite(t, source, data)
			if mode == "symlink" {
				link := filepath.Join(local, "source-link")
				if err := os.Symlink(source, link); err != nil {
					t.Fatal(err)
				}
				source = link
			}
			transferWrite(t, decoy, []byte("local decoy"))
			if mode != "missing" {
				transferWrite(t, destination, []byte("target old"))
			}
			var saves atomic.Int32
			sent := make(chan rpc.DesktopCall, 1)
			id := rpc.DesktopIdentity{InstanceID: "target", ClientID: "controller", Epoch: 7}
			socket := fakeAttach(t, func(conn net.Conn) {
				defer conn.Close()
				var req rpc.Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				var p rpc.DesktopCall
				if err := rpc.Decode(req.Params, &p); err != nil {
					t.Error(err)
					return
				}
				if req.Method != "desktop.call" || p.Desktop != id {
					t.Errorf("identity lost: %+v", p)
				}
				if p.Method == "files.version" {
					var q rpc.FileVersionParams
					if err := rpc.Decode(p.Params, &q); err != nil || q.Path != decoy || p.Sequence != 0 {
						t.Errorf("version request: %+v %v", q, err)
					}
					v, err := files.ReadVersion(t.Context(), destination)
					if err != nil {
						t.Error(err)
					}
					rpc.WriteResult(conn, v)
					return
				}
				saves.Add(1)
				sent <- p
				var q rpc.FileSaveParams
				if err := rpc.Decode(p.Params, &q); err != nil {
					t.Error(err)
					return
				}
				sum := sha256.Sum256(data)
				if p.Method != "files.save" || p.Sequence == 0 || q.Path != decoy || q.Hash != hex.EncodeToString(sum[:]) || q.Revision != 0 || !bytes.Equal(q.Data, data) {
					t.Errorf("save request: method=%s sequence=%d path=%s hash=%s revision=%d bytes=%d", p.Method, p.Sequence, q.Path, q.Hash, q.Revision, len(q.Data))
				}
				v, err := files.WriteFileChecked(t.Context(), destination, q.Expected, func(w io.Writer) error {
					_, err := w.Write(q.Data)
					return err
				})
				if mode == "lost-response" {
					if err != nil {
						t.Error(err)
					}
					return
				}
				var re *rpc.Error
				if err != nil {
					if !errors.Is(err, files.ErrSaveConflict) {
						t.Error(err)
					}
					re = rpc.Errorf(rpc.CodeConflict, "%v", err)
				}
				json.NewEncoder(conn).Encode(map[string]any{"result": rpc.FileSaveResult{Version: v}, "error": re})
			})
			c := (&Client{Socket: socket, Remote: true}).WithDesktop(id)
			expected, err := c.FileVersion(t.Context(), decoy)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "conflict" {
				transferWrite(t, destination, []byte("target edited"))
			}
			got, err := c.UploadFile(t.Context(), source, decoy, expected)
			switch mode {
			case "conflict":
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != rpc.CodeConflict {
					t.Fatalf("conflict: %v", err)
				}
				transferContents(t, destination, []byte("target edited"))
			case "lost-response":
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown || rpc.Retryable(err) {
					t.Fatalf("unknown: %v", err)
				}
				var ref rpc.DesktopOperationRef
				request := <-sent
				if err := json.Unmarshal(re.Data, &ref); err != nil || ref.Desktop != id || ref.Sequence != request.Sequence {
					t.Fatalf("operation identity: %+v %v", ref, err)
				}
				transferContents(t, destination, data)
			default:
				if err != nil || !got.Version.Same(transferVersion(t, destination)) {
					t.Fatalf("upload: %+v %v", got, err)
				}
				transferContents(t, destination, data)
			}
			if saves.Load() != 1 {
				t.Fatalf("save automatically replayed: %d", saves.Load())
			}
			transferContents(t, decoy, []byte("local decoy"))
			transferContents(t, source, data)
		})
	}
}

func TestUploadFileSourceFailures(t *testing.T) {
	for _, mode := range []string{"missing", "directory", "too-large", "canceled", "no-identity"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			if mode == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if mode != "missing" {
				transferWrite(t, path, []byte("source"))
			}
			if mode == "too-large" {
				if err := os.Truncate(path, files.MaxSize+1); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			c := fileTestClient(t, func(rpc.DesktopCall) (any, error) {
				calls.Add(1)
				return nil, errors.New("unexpected RPC")
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if mode == "no-identity" {
				c.Desktop = nil
			}
			_, err := c.UploadFile(ctx, path, "/target/file", files.Version{})
			if err == nil || calls.Load() != 0 {
				t.Fatalf("invalid source reached target: calls=%d err=%v", calls.Load(), err)
			}
			if mode == "missing" && !os.IsNotExist(err) || mode == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("wrong error: %v", err)
			}
			if mode == "no-identity" {
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
					t.Fatalf("identity: %v", err)
				}
			}
		})
	}
}

func TestUploadFileSourceVersionChanges(t *testing.T) {
	for _, mode := range []string{"same-stamp", "retarget"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			transferWrite(t, path, []byte("old\r\n"))
			before := transferVersion(t, path)
			if mode == "same-stamp" {
				transferWrite(t, path, []byte("new\r\n"))
				if err := os.Chtimes(path, before.Stamp.ModTime, before.Stamp.ModTime); err != nil {
					t.Fatal(err)
				}
				if !before.Stamp.Same(files.StatFile(path)) {
					t.Fatal("test did not retain source stamp")
				}
			} else {
				other := filepath.Join(filepath.Dir(path), "other")
				transferWrite(t, other, []byte("old\r\n"))
				link := filepath.Join(filepath.Dir(path), "link")
				if err := os.Symlink(other, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			data, err := readUploadSource(t.Context(), path, before)
			if !errors.Is(err, files.ErrChanged) || data != nil {
				t.Fatalf("source changed after baseline: data=%q err=%v", data, err)
			}
		})
	}
}

func TestDownloadFileBytesAndDestination(t *testing.T) {
	for _, mode := range []string{"existing", "missing", "empty", "symlink", "conflict", "created-during-read"} {
		t.Run(mode, func(t *testing.T) {
			local, remote := t.TempDir(), t.TempDir()
			path, source := filepath.Join(local, "download"), filepath.Join(remote, "source")
			data := bytes.Repeat([]byte{0xff, 0, '\r', '\n', 0xfe}, files.RangeLimit/5+19)
			if mode == "empty" {
				data = nil
			}
			transferWrite(t, source, data)
			if mode != "missing" && mode != "created-during-read" {
				transferWrite(t, path, []byte("local original"))
			}
			if mode == "symlink" {
				link := filepath.Join(local, "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			expected := transferVersion(t, path)
			var ranges atomic.Int32
			// Epoch 为零的查看身份只发读取请求，不申请目标控制权。
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "files.resourceRange":
					var q rpc.FileResourceRangeParams
					if err := rpc.Decode(p.Params, &q); err != nil {
						return nil, err
					}
					if q.Root != "/" || q.Path != strings.TrimPrefix(source, "/") {
						t.Errorf("wrong target: %+v", q)
					}
					if ranges.Add(1) == 1 && (mode == "conflict" || mode == "created-during-read") {
						if err := os.WriteFile(path, []byte("local edited"), 0600); err != nil {
							return nil, err
						}
					}
					r, err := files.ReadResourceRange(t.Context(), q.Root, q.Path, q.Offset, q.Limit, q.Expected)
					return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
				case "files.resourceVersion":
					var q rpc.FileResourceVersionParams
					if err := rpc.Decode(p.Params, &q); err != nil {
						return nil, err
					}
					return files.ReadResourceVersion(t.Context(), q.Root, q.Path)
				default:
					t.Errorf("download requested write/control: %s", p.Method)
					return nil, errors.New("unexpected method")
				}
			})
			err := c.DownloadFile(t.Context(), source, path, expected)
			if mode == "conflict" || mode == "created-during-read" {
				if !errors.Is(err, files.ErrSaveConflict) {
					t.Fatalf("conflict: %v", err)
				}
				transferContents(t, path, []byte("local edited"))
			} else {
				if err != nil {
					t.Fatal(err)
				}
				transferContents(t, path, data)
			}
			transferContents(t, source, data)
			entries, err := os.ReadDir(local)
			want := 1
			if mode == "symlink" {
				want = 2
			}
			if err != nil || len(entries) != want {
				t.Fatalf("partial files left behind: %v %v", entries, err)
			}
		})
	}
}

func TestDownloadFileConcurrentClients(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "existing"}[existing], func(t *testing.T) {
			local := filepath.Join(t.TempDir(), "download")
			if existing {
				transferWrite(t, local, []byte("original"))
			}
			expected := transferVersion(t, local)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			ready, unblock := make(chan struct{}, 2), make(chan struct{})
			release := sync.OnceFunc(func() { close(unblock) })
			defer release()
			type result struct {
				data []byte
				err  error
			}
			results := make(chan result, 2)
			for _, data := range [][]byte{[]byte("first\r\n\x00\xff"), []byte("second\r\n\x00\xfe")} {
				source := filepath.Join(t.TempDir(), "source")
				transferWrite(t, source, data)
				c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
					switch p.Method {
					case "files.resourceRange":
						var q rpc.FileResourceRangeParams
						if err := rpc.Decode(p.Params, &q); err != nil {
							return nil, err
						}
						r, err := files.ReadResourceRange(ctx, q.Root, q.Path, q.Offset, q.Limit, q.Expected)
						return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
					case "files.resourceVersion":
						v, err := files.ReadVersion(ctx, source)
						ready <- struct{}{}
						select {
						case <-unblock:
						case <-ctx.Done():
							return nil, ctx.Err()
						}
						return v, err
					default:
						return nil, errors.New("unexpected method")
					}
				})
				go func() {
					results <- result{data, c.DownloadFile(ctx, source, local, expected)}
				}()
			}
			// 两个独立 Client 都完成读取后，同时放行提交同一目的、同一基线。
			for range 2 {
				select {
				case <-ready:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			release()
			var winner []byte
			success, conflict := 0, 0
			for range 2 {
				r := <-results
				if r.err == nil {
					success++
					winner = r.data
				} else if errors.Is(r.err, files.ErrSaveConflict) {
					conflict++
				} else {
					t.Fatalf("unexpected failure: %v", r.err)
				}
			}
			if success != 1 || conflict != 1 {
				t.Fatalf("same baseline committed twice: successes=%d conflicts=%d", success, conflict)
			}
			transferContents(t, local, winner)
		})
	}
}

func TestDownloadFileSharesLocalSaveLock(t *testing.T) {
	local, source := filepath.Join(t.TempDir(), "download"), filepath.Join(t.TempDir(), "source")
	transferWrite(t, local, []byte("original"))
	transferWrite(t, source, []byte("downloaded"))
	expected := transferVersion(t, local)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	entered, unblock := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(unblock) })
	defer release()
	saved := make(chan error, 1)
	go func() {
		_, err := files.WriteFileCoordinated(ctx, local, expected, func(w io.Writer) error {
			close(entered)
			select {
			case <-unblock:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := io.WriteString(w, "local save")
			return err
		})
		saved <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	read := make(chan struct{}, 3)
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.resourceRange":
			var q rpc.FileResourceRangeParams
			if err := rpc.Decode(p.Params, &q); err != nil {
				return nil, err
			}
			r, err := files.ReadResourceRange(ctx, q.Root, q.Path, q.Offset, q.Limit, q.Expected)
			return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
		case "files.resourceVersion":
			v, err := files.ReadVersion(ctx, source)
			read <- struct{}{}
			return v, err
		default:
			return nil, errors.New("unexpected method")
		}
	})
	waiting, stop := context.WithCancel(ctx)
	defer stop()
	downloaded := make(chan error, 1)
	go func() { downloaded <- c.DownloadFile(waiting, source, local, expected) }()
	select {
	case <-read:
	case <-ctx.Done():
		t.Fatal("destination lock blocked resource reading")
	}
	other := filepath.Join(filepath.Dir(local), "other")
	if err := c.DownloadFile(ctx, source, other, transferVersion(t, other)); err != nil {
		t.Fatalf("different download destination blocked: %v", err)
	}
	stop()
	if err := <-downloaded; !errors.Is(err, context.Canceled) {
		t.Fatalf("download bypassed local save lock or could not cancel: %v", err)
	}
	transferContents(t, local, []byte("original"))
	transferContents(t, other, []byte("downloaded"))
	release()
	if err := <-saved; err != nil {
		t.Fatalf("local save: %v", err)
	}
	if err := c.DownloadFile(ctx, source, local, transferVersion(t, local)); err != nil {
		t.Fatalf("canceled download leaked lock: %v", err)
	}
	transferContents(t, local, []byte("downloaded"))
}

func TestDownloadFileFailuresLeaveNoPartial(t *testing.T) {
	for _, mode := range []string{"changed", "read-failed", "cancel", "version-failed", "too-large", "no-identity", "missing-target"} {
		for _, existing := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/missing", true: "/existing"}[existing], func(t *testing.T) {
				local, remote := t.TempDir(), t.TempDir()
				path, source := filepath.Join(local, "download"), filepath.Join(remote, "source")
				decoy := filepath.Join(local, "source")
				transferWrite(t, decoy, []byte("wrong host contents"))
				data := bytes.Repeat([]byte{'a'}, files.RangeLimit+13)
				transferWrite(t, source, data)
				before := files.StatFile(source)
				if mode == "too-large" {
					if err := os.Truncate(source, files.MaxSize+1); err != nil {
						t.Fatal(err)
					}
				}
				if existing {
					transferWrite(t, path, []byte("local original"))
				}
				expected := transferVersion(t, path)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var ranges atomic.Int32
				c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
					if p.Method == "files.resourceVersion" {
						if mode == "version-failed" {
							return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
						}
						return files.ReadResourceVersion(t.Context(), remote, "source")
					}
					var q rpc.FileResourceRangeParams
					if err := rpc.Decode(p.Params, &q); err != nil {
						return nil, err
					}
					if p.Method != "files.resourceRange" || q.Root != "/" || q.Path != strings.TrimPrefix(decoy, "/") {
						t.Errorf("wrong request: %s %+v", p.Method, q)
					}
					if mode == "missing-target" {
						return nil, rpc.Errorf(rpc.CodeNotFound, "missing")
					}
					if ranges.Add(1) == 2 {
						switch mode {
						case "changed":
							if err := os.WriteFile(source, bytes.Repeat([]byte{'b'}, len(data)), 0600); err != nil {
								return nil, err
							}
							if err := os.Chtimes(source, before.ModTime, before.ModTime); err != nil {
								return nil, err
							}
						case "read-failed":
							return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
						case "cancel":
							cancel()
						}
					}
					r, err := files.ReadResourceRange(t.Context(), remote, "source", q.Offset, q.Limit, q.Expected)
					return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
				})
				if mode == "no-identity" {
					c.Desktop = nil
				}
				err := c.DownloadFile(ctx, decoy, path, expected)
				if err == nil {
					t.Fatal("failed download succeeded")
				}
				if mode == "changed" && !errors.Is(err, files.ErrChanged) || mode == "cancel" && !errors.Is(err, context.Canceled) || mode == "missing-target" && !os.IsNotExist(err) {
					t.Fatalf("wrong error: %v", err)
				}
				if existing {
					transferContents(t, path, []byte("local original"))
				} else if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("partial destination exists: %v", err)
				}
				transferContents(t, decoy, []byte("wrong host contents"))
				entries, err := os.ReadDir(local)
				want := 1
				if existing {
					want++
				}
				if err != nil || len(entries) != want {
					t.Fatalf("partial files left behind: %v %v", entries, err)
				}
			})
		}
	}
}

func TestUploadAndDownloadDirectory(t *testing.T) {
	srcRoot := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(srcRoot, "sub", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(srcRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, ".git", "HEAD"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, ".DS_Store"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "root.txt"), []byte("root\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcRoot, "sub", "data.bin"), []byte{0, 1, 2, 0xff}, 0o600); err != nil {
		t.Fatal(err)
	}

	remoteRoot := filepath.Join(t.TempDir(), "remote-target")
	id := rpc.DesktopIdentity{InstanceID: "target", ClientID: "controller", Epoch: 3}
	socket := fakeAttach(t, func(conn net.Conn) {
		defer conn.Close()
		var req rpc.Request
		if err := json.NewDecoder(conn).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		var p rpc.DesktopCall
		if err := json.Unmarshal(req.Params, &p); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "desktop.call" || p.Desktop != id {
			t.Errorf("wrong desktop call: %s %+v", req.Method, p)
		}
		write, _ := rpc.DesktopMethod(p.Method)
		if (p.Sequence != 0) != write {
			t.Errorf("sequence mismatch for %s: seq=%d", p.Method, p.Sequence)
		}
		result, err := func(p rpc.DesktopCall) (any, error) {
			switch p.Method {
			case "files.stat":
				var q rpc.FilePathsParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				out := make([]rpc.FileStatResult, len(q.Paths))
				for i, path := range q.Paths {
					st, err := files.Stat(path)
					var re *rpc.Error
					if err != nil {
						re = rpc.Errorf(rpc.CodeInternal, "%v", err)
					}
					out[i] = rpc.FileStatResult{Path: path, Stamp: st, Error: re}
				}
				return out, nil
			case "files.mkdir":
				var q rpc.FileMkdirParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				return map[string]any{"path": q.Path}, files.MakeDir(t.Context(), q.Path)
			case "files.version":
				var q rpc.FileVersionParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				return files.ReadVersion(t.Context(), q.Path)
			case "files.save":
				var q rpc.FileSaveParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				ver, err := files.WriteFileChecked(t.Context(), q.Path, q.Expected, func(w io.Writer) error {
					_, err := w.Write(q.Data)
					return err
				})
				if err != nil {
					return nil, err
				}
				return rpc.FileSaveResult{Version: ver}, nil
			case "files.readDirs":
				var q rpc.FilePathsParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				out := make([]rpc.FileDirectoryResult, len(q.Paths))
				for i, path := range q.Paths {
					entries, st, err := files.ReadDir(path)
					var re *rpc.Error
					if err != nil {
						re = rpc.Errorf(rpc.CodeInternal, "%v", err)
					}
					out[i] = rpc.FileDirectoryResult{Path: path, Entries: entries, Stamp: st, Error: re}
				}
				return out, nil
			case "files.resourceRange":
				var q rpc.FileResourceRangeParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				r, err := files.ReadResourceRange(t.Context(), q.Root, q.Path, q.Offset, q.Limit, q.Expected)
				return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
			case "files.resourceVersion":
				var q rpc.FileResourceVersionParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				return files.ReadResourceVersion(t.Context(), q.Root, q.Path)
			case "files.pasteFile":
				var q rpc.FilePasteFileParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				pasted, err := files.PasteFile(t.Context(), q.Name, q.Data)
				if err != nil {
					return nil, err
				}
				return rpc.FilePasteImageResult{Path: pasted}, nil
			}
			return nil, rpc.Errorf(rpc.CodeUnknown, "unexpected %s", p.Method)
		}(p)
		var re *rpc.Error
		if err != nil {
			if !errors.As(err, &re) {
				re = rpc.Errorf(rpc.CodeInternal, "%v", err)
			}
		}
		_ = json.NewEncoder(conn).Encode(map[string]any{"result": result, "error": re})
	})
	c := (&Client{Socket: socket, Remote: true}).WithDesktop(id)

	if err := c.UploadDirectory(t.Context(), srcRoot, remoteRoot, false); err != nil {
		t.Fatalf("UploadDirectory failed: %v", err)
	}
	if err := c.UploadDirectory(t.Context(), srcRoot, remoteRoot, false); !errors.Is(err, files.ErrSaveConflict) {
		t.Fatalf("UploadDirectory without replace = %v, want conflict", err)
	}
	if err := c.UploadDirectory(t.Context(), srcRoot, remoteRoot, true); err != nil {
		t.Fatalf("UploadDirectory replace failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(remoteRoot, ".git")); !os.IsNotExist(err) {
		t.Fatal("uploaded .git directory")
	}
	transferContents(t, filepath.Join(remoteRoot, "root.txt"), []byte("root\r\n"))
	transferContents(t, filepath.Join(remoteRoot, "sub", "data.bin"), []byte{0, 1, 2, 0xff})
	if info, err := os.Stat(filepath.Join(remoteRoot, "sub", "empty")); err != nil || !info.IsDir() {
		t.Fatal("missing empty subdirectory on remote")
	}

	dstRoot := filepath.Join(t.TempDir(), "downloaded")
	if err := c.DownloadDirectory(t.Context(), remoteRoot, dstRoot, false); err != nil {
		t.Fatalf("DownloadDirectory failed: %v", err)
	}
	if err := c.DownloadDirectory(t.Context(), remoteRoot, dstRoot, false); !errors.Is(err, files.ErrSaveConflict) {
		t.Fatalf("DownloadDirectory without replace = %v, want conflict", err)
	}
	transferContents(t, filepath.Join(dstRoot, "root.txt"), []byte("root\r\n"))
	transferContents(t, filepath.Join(dstRoot, "sub", "data.bin"), []byte{0, 1, 2, 0xff})
	if info, err := os.Stat(filepath.Join(dstRoot, "sub", "empty")); err != nil || !info.IsDir() {
		t.Fatal("missing empty subdirectory on local download")
	}

	// 拒绝目录内符号链接
	withLink := filepath.Join(t.TempDir(), "with-link")
	if err := os.MkdirAll(withLink, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(srcRoot, "root.txt"), filepath.Join(withLink, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := c.UploadDirectory(t.Context(), withLink, filepath.Join(t.TempDir(), "never"), false); err == nil {
		t.Fatal("UploadDirectory accepted symlink")
	}

	// PasteFile 客户端往返
	t.Setenv("TMPDIR", t.TempDir())
	pastedPath, err := c.PasteFile(t.Context(), filepath.Join(srcRoot, "root.txt"))
	if err != nil {
		t.Fatalf("PasteFile failed: %v", err)
	}
	transferContents(t, pastedPath, []byte("root\r\n"))
}
