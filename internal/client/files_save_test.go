package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestRemoteFileVersion(t *testing.T) {
	want := files.Version{Path: "/target/real", Stamp: files.Stamp{Missing: true}}
	c := fileTestClient(t, func(p rpc.DesktopCall) (any, error) {
		var q rpc.FileVersionParams
		if err := rpc.Decode(p.Params, &q); err != nil || q.Path != "/target/link" || p.Method != "files.version" {
			t.Errorf("version request: %+v %v", p, err)
		}
		return want, nil
	})
	got, err := c.FileVersion(t.Context(), "/target/link")
	if err != nil || !got.Same(want) {
		t.Fatalf("version: %+v %v", got, err)
	}
}

func TestRemoteFileSave(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "lost-response"}[lost], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "same-path")
			if err := os.WriteFile(path, []byte("local"), 0o600); err != nil {
				t.Fatal(err)
			}
			data := []byte{'r', '\r', '\n', 0xff, 0}
			sum := sha256.Sum256(data)
			p := rpc.FileSaveParams{Path: path, Expected: files.Version{Path: "/target/real", Stamp: files.Stamp{Missing: true}}, Data: data, Hash: hex.EncodeToString(sum[:]), Revision: 27}
			want := rpc.FileSaveResult{Version: files.Version{Path: p.Expected.Path, Stamp: files.Stamp{Size: int64(len(data)), Ino: 123, Mode: 0o644}, Hash: p.Hash}, Revision: p.Revision}
			id := rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 3}
			var calls atomic.Int32
			request := make(chan rpc.DesktopCall, 1)
			socket := fakeAttach(t, func(conn net.Conn) {
				defer conn.Close()
				calls.Add(1)
				var req rpc.Request
				if err := json.NewDecoder(conn).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				var got rpc.DesktopCall
				if err := json.Unmarshal(req.Params, &got); err != nil {
					t.Error(err)
				}
				var q rpc.FileSaveParams
				if err := json.Unmarshal(got.Params, &q); err != nil {
					t.Error(err)
				}
				if req.Method != "desktop.call" || got.Method != "files.save" || got.Desktop != id || got.Sequence == 0 || q.Path != p.Path || !q.Expected.Same(p.Expected) || !bytes.Equal(q.Data, p.Data) || q.Hash != p.Hash || q.Revision != p.Revision {
					t.Errorf("save changed request: %+v %+v", got, q)
				}
				request <- got
				if !lost {
					rpc.WriteResult(conn, want)
				}
			})
			c := (&Client{Socket: socket, Remote: true}).WithDesktop(id)
			got, err := c.SaveFile(t.Context(), p)
			req := <-request
			if lost {
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != rpc.CodeOutcomeUnknown || rpc.Retryable(err) {
					t.Fatalf("lost response: %v", err)
				}
				var ref rpc.DesktopOperationRef
				if err := json.Unmarshal(re.Data, &ref); err != nil || ref.Desktop != id || ref.Sequence != req.Sequence {
					t.Fatalf("lost operation identity: %+v %v", ref, err)
				}
			} else if err != nil || got.Revision != want.Revision || !got.Version.Same(want.Version) {
				t.Fatalf("save: %+v %v", got, err)
			}
			if calls.Load() != 1 {
				t.Fatal("save automatically replayed")
			}
			if local, err := os.ReadFile(path); err != nil || string(local) != "local" {
				t.Fatalf("remote save touched local file: %q %v", local, err)
			}
		})
	}
}

func TestFileSaveRequiresDesktop(t *testing.T) {
	c := &Client{Socket: "/missing"}
	_, err := c.SaveFile(context.Background(), rpc.FileSaveParams{})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
		t.Fatalf("unbound save reached socket: %v", err)
	}
}
