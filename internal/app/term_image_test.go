package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/rpc"
)

func clipboardTestPNG(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestTargetPasteImage(t *testing.T) {
	for _, mode := range []string{"success", "failure", "unknown", "control-changed", "replaced", "stale-error"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			takeRequestTestControl(t, a)
			v, conn := recTerminal(t, a)
			v.request = a.request()
			data := clipboardTestPNG(t)
			original := bytes.Clone(data)
			target := filepath.Join(t.TempDir(), "target.png")
			entered, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(ctx context.Context, p rpc.DesktopCall) (any, error) {
				calls.Add(1)
				if p.Method != "files.pasteImage" || p.Desktop != *v.request.client.Desktop {
					t.Error("upload changed method or target identity")
				}
				var args rpc.FilePasteImageParams
				if err := json.Unmarshal(p.Params, &args); err != nil {
					return nil, err
				}
				sum := sha256.Sum256(original)
				if !bytes.Equal(args.Data, original) || args.Hash != hex.EncodeToString(sum[:]) {
					t.Error("upload did not retain clipboard snapshot")
				}
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				switch mode {
				case "failure", "stale-error":
					return nil, rpc.Errorf(rpc.CodeInternal, "upload failed")
				case "unknown":
					return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
				}
				return rpc.FilePasteImageResult{Path: target}, nil
			}
			f.mu.Unlock()
			a.pasteImage(v, data)
			data[0] = 0
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("upload did not start")
			}
			select {
			case <-conn.in:
				t.Fatal("pasted before upload finished")
			default:
			}
			if mode == "control-changed" || mode == "stale-error" {
				a.host.detach()
			}
			if mode == "replaced" {
				a.panes[0] = &termView{id: v.id, request: v.request}
			}
			close(release)
			waitFileUpdate(t, a)
			a.runPending()
			if mode == "success" {
				if got := sent(t, conn); got != target {
					t.Fatalf("pasted %q instead of target path %q", got, target)
				}
			} else {
				select {
				case b := <-conn.in:
					t.Fatalf("failed or stale upload pasted %q", b)
				default:
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("upload replayed %d times", calls.Load())
			}
			if (mode == "failure" || mode == "unknown") != (a.err != "") {
				t.Fatalf("unexpected error state: %q", a.err)
			}
			if _, err := os.Stat(target); !os.IsNotExist(err) {
				t.Fatal("remote upload wrote a local file")
			}
		})
	}
}

func TestTargetPasteImageViewer(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	v, conn := recTerminal(t, a)
	v.request = a.request()
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(context.Context, rpc.DesktopCall) (any, error) {
		calls.Add(1)
		return nil, nil
	}
	f.mu.Unlock()
	a.pasteImage(v, clipboardTestPNG(t))
	a.runPending()
	if calls.Load() != 0 || a.err == "" {
		t.Fatal("viewer uploaded an image or failed to explain control requirement")
	}
	select {
	case <-conn.in:
		t.Fatal("viewer pasted an image")
	default:
	}
}

func TestLocalPasteImage(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	a := &app{}
	v, conn := recTerminal(t, a)
	data := clipboardTestPNG(t)
	a.pasteImage(v, data)
	waitFileUpdate(t, a)
	a.runPending()
	path := sent(t, conn)
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("local image: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("local image permissions: %v %v", st, err)
	}
}

func TestTargetPasteFiles(t *testing.T) {
	for _, mode := range []string{"success", "failure", "viewer"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			if mode != "viewer" {
				takeRequestTestControl(t, a)
			}
			v, conn := recTerminal(t, a)
			v.request = a.request()
			localFile := filepath.Join(t.TempDir(), "spec doc.md")
			if err := os.WriteFile(localFile, []byte("# spec\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			remoteFile := "/tmp/posse-drop-remote/spec doc.md"
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				calls.Add(1)
				if p.Method != "files.pasteFile" {
					t.Errorf("unexpected method %s", p.Method)
				}
				var q rpc.FilePasteFileParams
				if err := json.Unmarshal(p.Params, &q); err != nil {
					return nil, err
				}
				if q.Name != "spec doc.md" || string(q.Data) != "# spec\n" {
					t.Errorf("unexpected payload %+v", q)
				}
				if mode == "failure" {
					return nil, rpc.Errorf(rpc.CodeInternal, "disk full")
				}
				return rpc.FilePasteImageResult{Path: remoteFile}, nil
			}
			f.mu.Unlock()

			a.pasteFiles(v, []string{localFile}, "claude")
			if mode != "viewer" {
				waitFileUpdate(t, a)
			}
			a.runPending()
			switch mode {
			case "success":
				got := sent(t, conn)
				if got != "'/tmp/posse-drop-remote/spec doc.md' " {
					t.Fatalf("pasted %q, want remote quoted path", got)
				}
				if a.err != "" || calls.Load() != 1 {
					t.Fatalf("err=%q calls=%d", a.err, calls.Load())
				}
			case "failure":
				select {
				case b := <-conn.in:
					t.Fatalf("failure leaked path %q", b)
				default:
				}
				if a.err == "" || calls.Load() != 1 {
					t.Fatalf("missing error or wrong calls=%d", calls.Load())
				}
			case "viewer":
				select {
				case b := <-conn.in:
					t.Fatalf("viewer pasted %q", b)
				default:
				}
				if calls.Load() != 0 {
					t.Fatalf("viewer called upload %d times", calls.Load())
				}
			}
		})
	}
}
