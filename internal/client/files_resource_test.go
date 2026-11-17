package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestReadResourceBinaryAndEmpty(t *testing.T) {
	for _, size := range []int{0, files.RangeLimit + 31} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "same.bin"), []byte("local"), 0600); err != nil {
				t.Fatal(err)
			}
			data := bytes.Repeat([]byte{0xff}, size)
			if size > 0 {
				copy(data, []byte{0, '\r', '\n', 0xfe})
			}
			st := files.Stamp{Size: int64(size), Ino: 123, Mode: 0600}
			var calls atomic.Int32
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				if p.Method == "files.resourceVersion" {
					var q rpc.FileResourceVersionParams
					if err := rpc.Decode(p.Params, &q); err != nil {
						t.Error(err)
					}
					if q.Root != root || q.Path != "same.bin" {
						t.Errorf("version request: %+v", q)
					}
					sum := sha256.Sum256(data)
					return files.Version{Path: filepath.Join(root, q.Path), Stamp: st, Hash: hex.EncodeToString(sum[:])}, nil
				}
				var q rpc.FileResourceRangeParams
				if err := rpc.Decode(p.Params, &q); err != nil {
					t.Error(err)
				}
				n := calls.Add(1)
				if p.Method != "files.resourceRange" || q.Root != root || q.Path != "same.bin" || q.Limit != files.RangeLimit || q.Offset != int64(n-1)*files.RangeLimit {
					t.Errorf("request: %s %+v", p.Method, q)
				}
				if n == 1 && q.Expected != nil || n > 1 && (q.Expected == nil || !q.Expected.Same(st) || q.Expected.Mode != st.Mode) {
					t.Error("incorrect expected stamp")
				}
				end := min(q.Offset+int64(q.Limit), int64(len(data)))
				return rpc.FileRangeResult{Data: data[q.Offset:end], Stamp: st, EOF: end == int64(len(data))}, nil
			})
			got, stamp, err := c.ReadResource(context.Background(), root, "same.bin", int64(size))
			if err != nil || !bytes.Equal(got, data) || !stamp.Same(st) || stamp.Mode != st.Mode {
				t.Fatalf("bytes=%d stamp=%+v err=%v", len(got), stamp, err)
			}
			if got, err := os.ReadFile(filepath.Join(root, "same.bin")); err != nil || string(got) != "local" {
				t.Fatal("local file changed")
			}
		})
	}
}

func TestReadResourceRejectsSameStampMixedContent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	old := bytes.Repeat([]byte{'a'}, files.RangeLimit+17)
	changed := bytes.Repeat([]byte{'b'}, len(old))
	if err := os.WriteFile(path, old, 0600); err != nil {
		t.Fatal(err)
	}
	before := files.StatFile(path)
	var ranges, versions atomic.Int32
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.resourceRange":
			var q rpc.FileResourceRangeParams
			if err := rpc.Decode(p.Params, &q); err != nil {
				return nil, err
			}
			if ranges.Add(1) == 2 {
				if err := os.WriteFile(path, changed, 0600); err != nil {
					return nil, err
				}
				if err := os.Chtimes(path, before.ModTime, before.ModTime); err != nil {
					return nil, err
				}
				if !before.Same(files.StatFile(path)) {
					t.Error("test did not retain stamp")
				}
			}
			r, err := files.ReadResourceRange(context.Background(), q.Root, q.Path, q.Offset, q.Limit, q.Expected)
			return rpc.FileRangeResult{Data: r.Data, Stamp: r.Stamp, EOF: r.EOF}, err
		case "files.resourceVersion":
			versions.Add(1)
			var q rpc.FileResourceVersionParams
			if err := rpc.Decode(p.Params, &q); err != nil {
				return nil, err
			}
			return files.ReadResourceVersion(context.Background(), q.Root, q.Path)
		default:
			t.Errorf("unexpected method %s", p.Method)
			return nil, errors.New("unexpected method")
		}
	})
	data, stamp, err := c.ReadResource(context.Background(), root, "file", int64(len(old)))
	if !errors.Is(err, files.ErrChanged) || data != nil || stamp != (files.Stamp{}) || ranges.Load() != 2 || versions.Load() != 1 {
		t.Fatalf("mixed version accepted/retried: bytes=%d stamp=%+v ranges=%d versions=%d err=%v", len(data), stamp, ranges.Load(), versions.Load(), err)
	}
}

func TestReadResourceVersionFailure(t *testing.T) {
	for _, mode := range []string{"hash", "stamp", "mode", "path", "outside", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := files.Stamp{Size: 4, Ino: 123, Mode: 0600}
			body := []byte{0xff, 0, '\r', '\n'}
			sum := sha256.Sum256(body)
			var versions atomic.Int32
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				if p.Method == "files.resourceRange" {
					return rpc.FileRangeResult{Data: body, Stamp: st, EOF: true}, nil
				}
				if p.Method != "files.resourceVersion" {
					t.Errorf("unrestricted version request: %s", p.Method)
				}
				versions.Add(1)
				v := files.Version{Path: "/target/file", Stamp: st, Hash: hex.EncodeToString(sum[:])}
				switch mode {
				case "hash":
					v.Hash = "bad hash"
				case "stamp":
					v.Stamp.Ino++
				case "mode":
					v.Stamp.Mode ^= 0100
				case "path":
					v.Path = "relative"
				case "outside":
					return nil, rpc.Errorf(rpc.CodeBadRequest, "%s", files.ErrResourcePath)
				case "cancel":
					cancel()
				}
				return v, nil
			})
			data, stamp, err := c.ReadResource(ctx, "/target", "file", 4)
			if err == nil || data != nil || stamp != (files.Stamp{}) || versions.Load() != 1 {
				t.Fatalf("version failure: bytes=%d stamp=%+v calls=%d err=%v", len(data), stamp, versions.Load(), err)
			}
			if mode == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
		})
	}
}

func TestReadResourceRejectsPartialResponses(t *testing.T) {
	for _, mode := range []string{"stamp", "permissions", "size", "missing", "directory", "negative-size", "oversized-chunk", "early-eof", "missing-eof", "empty-progress", "permission-error", "not-found", "offline"} {
		t.Run(mode, func(t *testing.T) {
			st := files.Stamp{Size: 4, Ino: 123, Mode: 0600}
			var calls atomic.Int32
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				if calls.Add(1) == 1 {
					return rpc.FileRangeResult{Data: []byte{0, 0xff}, Stamp: st}, nil
				}
				r := rpc.FileRangeResult{Data: []byte{0xfe, 0}, Stamp: st, EOF: true}
				switch mode {
				case "stamp":
					r.Stamp.Ino++
				case "permissions":
					r.Stamp.Mode = 0400
				case "size":
					r.Stamp.Size++
				case "missing":
					r.Stamp.Missing = true
				case "directory":
					r.Stamp.Mode = os.ModeDir | 0700
				case "negative-size":
					r.Stamp.Size = -1
				case "oversized-chunk":
					r.Data = make([]byte, files.RangeLimit+1)
				case "early-eof":
					r.Data = []byte{1}
				case "missing-eof":
					r.EOF = false
				case "empty-progress":
					r.Data, r.EOF = nil, false
				case "permission-error":
					return nil, rpc.Errorf(rpc.CodePermissionDenied, "denied")
				case "not-found":
					return nil, rpc.Errorf(rpc.CodeNotFound, "missing")
				case "offline":
					return nil, rpc.Errorf(rpc.CodeInternal, "offline")
				}
				return r, nil
			})
			data, stamp, err := c.ReadResource(context.Background(), "/target", "file", 10)
			if err == nil || data != nil || stamp != (files.Stamp{}) || calls.Load() != 2 {
				t.Fatalf("partial/retried: data=%d stamp=%+v calls=%d err=%v", len(data), stamp, calls.Load(), err)
			}
			if mode == "permission-error" && !os.IsPermission(err) {
				t.Fatalf("permission: %v", err)
			}
			if mode == "not-found" && !os.IsNotExist(err) {
				t.Fatalf("missing: %v", err)
			}
		})
	}
}

func TestReadResourceLimitAndCancel(t *testing.T) {
	for _, mode := range []string{"too-large", "negative-limit", "cancel-before", "cancel-after"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var calls atomic.Int32
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				calls.Add(1)
				if mode == "cancel-after" {
					cancel()
				}
				return rpc.FileRangeResult{Data: []byte{0}, Stamp: files.Stamp{Size: 5, Mode: 0600}}, nil
			})
			limit := int64(4)
			if mode == "negative-limit" {
				limit = -1
			}
			if mode == "cancel-before" {
				cancel()
			}
			data, stamp, err := c.ReadResource(ctx, "/target", "file", limit)
			if err == nil || data != nil || stamp != (files.Stamp{}) {
				t.Fatalf("accepted: %d %+v %v", len(data), stamp, err)
			}
			want := int32(1)
			if mode == "negative-limit" || mode == "cancel-before" {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("calls=%d want %d", calls.Load(), want)
			}
			if (mode == "cancel-before" || mode == "cancel-after") && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
		})
	}
	c := &Client{}
	data, stamp, err := c.ReadResource(context.Background(), "/target", "file", 10)
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly || data != nil || stamp != (files.Stamp{}) {
		t.Fatalf("identity: %d %+v %v", len(data), stamp, err)
	}
}
