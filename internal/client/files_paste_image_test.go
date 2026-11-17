package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestPasteImageRPC(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		err  *rpc.Error
		lost bool
	}{
		{name: "success", path: "/target/tmp/posse-paste-one.png"},
		{name: "empty-path"},
		{name: "relative-path", path: "tmp/image.png"},
		{name: "nul-path", path: "/tmp/image\x00.png"},
		{name: "unknown", err: rpc.Errorf(rpc.CodeOutcomeUnknown, "original unknown outcome")},
		{name: "unknown-method", err: rpc.Errorf(rpc.CodeUnknown, "original unknown method")},
		{name: "lost", lost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("image bytes")
			sum := sha256.Sum256(data)
			id := rpc.DesktopIdentity{ClientID: "window", InstanceID: "daemon", Epoch: 4}
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
				var p rpc.DesktopCall
				if err := json.Unmarshal(req.Params, &p); err != nil {
					t.Error(err)
				}
				var body rpc.FilePasteImageParams
				if err := json.Unmarshal(p.Params, &body); err != nil {
					t.Error(err)
				}
				if req.Method != "desktop.call" || p.Method != "files.pasteImage" || p.Desktop != id || p.Sequence == 0 || !bytes.Equal(body.Data, data) || body.Hash != hex.EncodeToString(sum[:]) {
					t.Errorf("paste request: %+v %+v", p, body)
				}
				request <- p
				if !tc.lost {
					json.NewEncoder(conn).Encode(map[string]any{"result": rpc.FilePasteImageResult{Path: tc.path}, "error": tc.err})
				}
			})
			c := (&Client{Socket: socket, Remote: true}).WithDesktop(id)
			path, err := c.PasteImage(t.Context(), data)
			p := <-request
			switch {
			case tc.err != nil || tc.lost:
				var re *rpc.Error
				wantCode := rpc.CodeOutcomeUnknown
				if tc.err != nil {
					wantCode = tc.err.Code
				}
				if path != "" || !errors.As(err, &re) || re.Code != wantCode || rpc.Retryable(err) {
					t.Fatalf("unknown: %q %v", path, err)
				}
				if tc.err != nil && re.Message != tc.err.Message {
					t.Fatalf("changed original error: %v", re)
				}
				if re.Code == rpc.CodeOutcomeUnknown {
					var ref rpc.DesktopOperationRef
					if err := json.Unmarshal(re.Data, &ref); err != nil || ref.Desktop != id || ref.Sequence != p.Sequence {
						t.Fatalf("unknown reference: %+v %v", ref, err)
					}
				}
			case tc.name == "success":
				if err != nil || path != tc.path {
					t.Fatalf("paste: %q %v", path, err)
				}
			default:
				if path != "" || err == nil || err.Error() != "invalid image paste response" {
					t.Fatalf("invalid result accepted: %q %v", path, err)
				}
			}
			if calls.Load() != 1 {
				t.Fatal("paste replayed automatically")
			}
		})
	}
}

func TestPasteImageBeforeRPC(t *testing.T) {
	c := &Client{Socket: "/missing"}
	var re *rpc.Error
	if _, err := c.PasteImage(t.Context(), nil); !errors.As(err, &re) || re.Code != rpc.CodeReadOnly {
		t.Fatalf("missing identity: %v", err)
	}
	c = c.WithDesktop(rpc.DesktopIdentity{ClientID: "window"})
	if _, err := c.PasteImage(t.Context(), make([]byte, files.MaxSize+1)); !errors.As(err, &re) || re.Code != rpc.CodeBadRequest {
		t.Fatalf("oversized image: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := c.PasteImage(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled image: %v", err)
	}
}
