package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func fileTestClient(t *testing.T, handler func(rpc.DesktopCall) (any, error)) *Client {
	t.Helper()
	id := rpc.DesktopIdentity{InstanceID: "target", ClientID: "viewer"}
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
		if req.Method != "desktop.call" || p.Desktop != id || p.Sequence != 0 {
			t.Errorf("unbound read: %s %+v", req.Method, p)
		}
		result, err := handler(p)
		var re *rpc.Error
		if err != nil {
			re, _ = err.(*rpc.Error)
			if re == nil {
				re = rpc.Errorf(rpc.CodeInternal, "%v", err)
			}
		}
		json.NewEncoder(conn).Encode(map[string]any{"result": result, "error": re})
	})
	return (&Client{Socket: socket, Remote: true}).WithDesktop(id)
}

func TestReadRemoteFileChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "same-path.txt")
	if err := os.WriteFile(path, []byte("local contents"), 0600); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte{'a'}, files.RangeLimit+31)
	data[len(data)-1] = 0xff
	st := files.Stamp{Size: int64(len(data)), Ino: 123, Mode: 0600}
	var ranges atomic.Int32
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		switch p.Method {
		case "files.stat":
			return []rpc.FileStatResult{{Path: path, Stamp: st}}, nil
		case "files.readRange":
			var q rpc.FileRangeParams
			if err := rpc.Decode(p.Params, &q); err != nil {
				t.Error(err)
			}
			if q.Path != path || q.Expected == nil || !q.Expected.Same(st) {
				t.Error("range changed target or version")
			}
			ranges.Add(1)
			end := min(int(q.Offset)+q.Limit, len(data))
			return rpc.FileRangeResult{Data: data[int(q.Offset):end], Stamp: st, EOF: end == len(data)}, nil
		default:
			t.Errorf("unexpected %s", p.Method)
			return nil, nil
		}
	})
	content, digest, err := c.ReadFile(context.Background(), path)
	sum := sha256.Sum256(data)
	if err != nil || content.Text != string(data) || digest != hex.EncodeToString(sum[:]) || ranges.Load() != 2 {
		t.Fatalf("read: len=%d hash=%s ranges=%d err=%v", len(content.Text), digest, ranges.Load(), err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "local contents" {
		t.Fatal("client touched local file")
	}
}

func TestReadRemoteFileFailureKeepsErrors(t *testing.T) {
	for _, mode := range []string{"missing", "permission", "offline", "changed", "empty-progress"} {
		t.Run(mode, func(t *testing.T) {
			const path = "/target/file"
			st := files.Stamp{Size: 5, Ino: 123, Mode: 0600}
			var ranges atomic.Int32
			c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
				if p.Method == "files.stat" {
					switch mode {
					case "missing":
						return []rpc.FileStatResult{{Path: path, Stamp: files.Stamp{Missing: true}}}, nil
					case "permission":
						return []rpc.FileStatResult{{Path: path, Error: rpc.Errorf(rpc.CodePermissionDenied, "denied")}}, nil
					case "offline":
						return nil, rpc.Errorf(rpc.CodeInternal, "target storage unavailable")
					}
					return []rpc.FileStatResult{{Path: path, Stamp: st}}, nil
				}
				ranges.Add(1)
				if mode == "changed" {
					next := st
					next.Ino++
					return rpc.FileRangeResult{Stamp: next, Data: []byte("other"), EOF: true}, nil
				}
				return rpc.FileRangeResult{Stamp: st}, nil
			})
			content, hash, err := c.ReadFile(context.Background(), path)
			if err == nil || content.Text != "" || hash != "" {
				t.Fatalf("partial content accepted: %+v %s %v", content, hash, err)
			}
			if mode == "missing" && !os.IsNotExist(err) {
				t.Fatalf("missing: %v", err)
			}
			if mode == "permission" && (!os.IsPermission(err) || os.IsNotExist(err)) {
				t.Fatalf("permission: %v", err)
			}
			if mode != "missing" && os.IsNotExist(err) {
				t.Fatal("error treated as deletion")
			}
			if ranges.Load() > 1 {
				t.Fatal("failed read replayed")
			}
		})
	}
}
