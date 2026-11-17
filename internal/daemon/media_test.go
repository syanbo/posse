package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"posse/internal/files"
	"posse/internal/media"
	"posse/internal/rpc"
)

func mediaTranscript(t *testing.T, n int) (rpc.MediaSource, []byte) {
	t.Helper()
	data := pasteImageParams(t, nil).Data
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "{\"type\":\"user\",\"message\":{\"content\":[{\"type\":\"tool_result\",\"content\":[{\"type\":\"image\",\"source\":{\"type\":\"base64\",\"media_type\":\"image/png\",\"data\":\"%s\"}}]}]}}\n", base64.StdEncoding.EncodeToString(data))
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return rpc.MediaSource{Agent: "claude", TranscriptPath: path}, data
}

func mediaTestCall(ctx context.Context, socket string, id rpc.DesktopIdentity, method string, p, out any) error {
	raw, _ := json.Marshal(p)
	return rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: id, Method: method, Params: raw}, out)
}

func TestDesktopMediaListAndPath(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	source, data := mediaTranscript(t, 42)
	var list rpc.MediaListResult
	if err := mediaTestCall(t.Context(), socket, viewer, "media.list", source, &list); err != nil {
		t.Fatal(err)
	}
	if !list.Found || len(list.Made) != 40 || list.MadeAll != 42 {
		t.Fatalf("list = %+v", list)
	}
	scanner := d.media.scanners[source].scanner
	if err := mediaTestCall(t.Context(), socket, viewer, "media.list", source, &list); err != nil || d.media.scanners[source].scanner != scanner {
		t.Fatalf("scanner not reused: %v", err)
	}
	it := list.Made[0]
	it.Label = "../../untrusted-label"
	var first, second rpc.MediaPathResult
	params := rpc.MediaPathParams{Item: it}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", params, &first); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(first.Path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("embedded bytes = %x %v", got, err)
	}
	fi, err := os.Stat(first.Path)
	if err != nil || fi.Mode().Perm() != 0o600 || !strings.HasPrefix(filepath.Base(first.Path), "resource-") {
		t.Fatalf("private file = %v %v", fi, err)
	}
	root, err := os.Stat(d.media.root)
	if err != nil || root.Mode().Perm() != 0o700 {
		t.Fatalf("private root = %v %v", root, err)
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", params, &second); err != nil || first != second {
		t.Fatalf("cache miss = %+v %v", second, err)
	}
	// 查看不需要控制代次；旧实例身份仍必须拒绝。
	viewer.Epoch = 999
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", params, &second); err != nil {
		t.Fatal(err)
	}
	stale := viewer
	stale.InstanceID = "old-instance"
	desktopErrorCode(t, mediaTestCall(t.Context(), socket, stale, "media.list", source, nil), rpc.CodeConflict)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "media.list", source, nil), rpc.CodeReadOnly)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "media.path", params, nil), rpc.CodeReadOnly)
	missingHash := it
	copyTool := *it.Tool
	copyTool.LineHash = [32]byte{}
	missingHash.Tool = &copyTool
	desktopErrorCode(t, mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: missingHash}, nil), rpc.CodeBadRequest)
	content, _ := os.ReadFile(source.TranscriptPath)
	content[it.Tool.Offset+2] = 'X'
	if err := os.WriteFile(source.TranscriptPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", params, nil); err == nil {
		t.Fatal("rewritten line reused cached image")
	}
	cacheDir := d.media.root
	d.media.close()
	if _, err := os.Stat(cacheDir); !os.IsNotExist(err) {
		t.Fatalf("cache survived close: %v", err)
	}
}

func TestDesktopMediaValidation(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	for _, source := range []rpc.MediaSource{{Agent: "unknown"}, {Agent: "claude", SessionID: "../x"}, {Agent: "claude", TranscriptPath: "relative"}} {
		desktopErrorCode(t, mediaTestCall(t.Context(), socket, viewer, "media.list", source, nil), rpc.CodeBadRequest)
	}
	for _, path := range []string{"", "relative", "/tmp/nul\x00"} {
		desktopErrorCode(t, mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: media.Item{Path: path}}, nil), rpc.CodeBadRequest)
	}
	root := t.TempDir()
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: media.Item{Path: root}}, nil); err == nil {
		t.Fatal("directory accepted")
	}
	path := filepath.Join(root, "image.png")
	data := pasteImageParams(t, nil).Data
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out rpc.MediaPathResult
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: media.Item{Path: path}}, &out); err != nil || out.Path != path {
		t.Fatalf("disk image path: %+v %v", out, err)
	}
	if err := os.Truncate(path, files.MaxSize+1); err != nil {
		t.Fatal(err)
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", rpc.MediaPathParams{Item: media.Item{Path: path}}, nil); err == nil {
		t.Fatal("oversized original accepted")
	}
}

func TestDesktopMediaVideoCache(t *testing.T) {
	d, _ := newTestDaemon(t)
	d.media.init()
	var calls atomic.Int32
	data := pasteImageParams(t, nil).Data
	d.media.thumbnail = func(ctx context.Context, source, dir string) (string, error) {
		calls.Add(1)
		path := filepath.Join(dir, "thumb.png")
		return path, os.WriteFile(path, data, 0o600)
	}
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	path := filepath.Join(t.TempDir(), "large.mp4")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 80<<20); err != nil {
		t.Fatal(err)
	}
	p := rpc.MediaPathParams{Item: media.Item{Path: path, Video: true, Key: "video"}}
	var first, next rpc.MediaPathResult
	for i := 0; i < 2; i++ {
		if err := mediaTestCall(t.Context(), socket, viewer, "media.path", p, &next); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = next
		}
	}
	if calls.Load() != 1 || first != next {
		t.Fatal("unchanged video did not reuse thumbnail")
	}
	if got, err := os.ReadFile(first.Path); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("thumbnail content: %x %v", got, err)
	}
	if err := os.Truncate(path, (80<<20)+1); err != nil {
		t.Fatal(err)
	}
	if err := mediaTestCall(t.Context(), socket, viewer, "media.path", p, &next); err != nil || next == first || calls.Load() != 2 {
		t.Fatalf("changed video reused thumbnail: %+v %v calls=%d", next, err, calls.Load())
	}
}

func TestDesktopMediaCancellationAndFailures(t *testing.T) {
	for _, mode := range []string{"cancel", "close", "failure", "too-large", "source-changed"} {
		t.Run(mode, func(t *testing.T) {
			d, _ := newTestDaemon(t)
			d.media.init()
			entered := make(chan struct{})
			path := filepath.Join(t.TempDir(), "source.mp4")
			os.WriteFile(path, []byte("video"), 0o600)
			d.media.thumbnail = func(ctx context.Context, source, dir string) (string, error) {
				thumb := filepath.Join(dir, "thumb.png")
				os.WriteFile(thumb, []byte("partial"), 0o600)
				close(entered)
				switch mode {
				case "cancel", "close":
					<-ctx.Done()
					return "", ctx.Err()
				case "failure":
					return "", errors.New("injected thumbnail failure")
				case "too-large":
					os.Truncate(thumb, files.MaxSize+1)
				case "source-changed":
					os.Truncate(source, 100)
				}
				return thumb, nil
			}
			ctx := context.WithValue(t.Context(), desktopCallContext{}, desktopRequestContext{readOnly: true})
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			raw, _ := json.Marshal(rpc.MediaPathParams{Item: media.Item{Path: path, Video: true}})
			done := make(chan error, 1)
			go func() { _, err := d.mediaPath(ctx, raw); done <- err }()
			<-entered
			if mode == "cancel" || mode == "close" {
				queued, stop := context.WithCancel(t.Context())
				stop()
				if _, _, err := d.media.enter(queued); !errors.Is(err, context.Canceled) {
					t.Fatalf("queued cancellation: %v", err)
				}
				if mode == "close" {
					d.media.close()
				} else {
					cancel()
				}
			}
			if err := <-done; err == nil {
				t.Fatal("failed generation succeeded")
			}
			if len(d.media.artifacts) != 0 || d.media.bytes != 0 {
				t.Fatal("failed generation retained artifact")
			}
			if d.media.root != "" {
				entries, err := os.ReadDir(d.media.root)
				if err != nil || len(entries) != 0 {
					t.Fatalf("work files survived failure: %v %v", entries, err)
				}
			}
		})
	}
}

func TestDesktopMediaCacheBounds(t *testing.T) {
	var s mediaState
	s.init()
	defer s.close()
	s.root = t.TempDir()
	s.maxFiles, s.maxBytes = 2, 8
	first, err := s.keep(t.Context(), "first", []byte("1111"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.keep(t.Context(), "second", []byte("2222"))
	if err != nil {
		t.Fatal(err)
	}
	if s.cached("first") != first {
		t.Fatal("cache lookup failed")
	}
	if _, err := s.keep(t.Context(), "third", []byte("3333")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); !os.IsNotExist(err) {
		t.Fatal("LRU artifact not removed")
	}
	if _, err := s.keep(t.Context(), "bytes", []byte("1234567")); err != nil {
		t.Fatal(err)
	}
	if len(s.artifacts) != 1 || s.bytes != 7 {
		t.Fatalf("cache budget: %d %d", len(s.artifacts), s.bytes)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatal("byte eviction did not remove file")
	}
}

func TestDesktopMediaScannerBoundsAndListLimit(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	for i := 0; i < mediaScannerLimit+1; i++ {
		source, _ := mediaTranscript(t, 1)
		if err := mediaTestCall(t.Context(), socket, viewer, "media.list", source, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(d.media.scanners) != mediaScannerLimit {
		t.Fatalf("scanner count = %d", len(d.media.scanners))
	}
	path := filepath.Join(t.TempDir(), "many.jsonl")
	line := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,eA=="}]}}` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, 12000)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	err := mediaTestCall(t.Context(), socket, viewer, "media.list", rpc.MediaSource{Agent: "codex", TranscriptPath: path}, nil)
	if err == nil || !strings.Contains(err.Error(), "session media list exceeds the response limit") {
		t.Fatalf("list budget = %v", err)
	}
}
