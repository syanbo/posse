package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestDesktopResourceRange(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	data := bytes.Repeat([]byte{0, 0xff, 0xfe}, files.RangeLimit/3+10)
	path := filepath.Join(root, "resource")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	call := func(id rpc.DesktopIdentity, p rpc.FileResourceRangeParams, out any) error {
		raw, _ := json.Marshal(p)
		return rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: id, Method: "files.resourceRange", Params: raw}, out)
	}
	p := rpc.FileResourceRangeParams{Root: root, Path: "linked", Limit: files.RangeLimit}
	var first, last rpc.FileRangeResult
	if err := call(viewer, p, &first); err != nil {
		t.Fatal(err)
	}
	p.Offset, p.Expected = int64(len(first.Data)), &first.Stamp
	if err := call(viewer, p, &last); err != nil {
		t.Fatal(err)
	}
	got := append(first.Data, last.Data...)
	if !bytes.Equal(got, data) || first.EOF || !last.EOF || !first.Stamp.Same(last.Stamp) {
		t.Fatalf("read: bytes=%d first=%+v last=%+v", len(got), first.Stamp, last.Stamp)
	}
	query := func(p rpc.FileResourceRangeParams) error { return call(viewer, p, nil) }
	if err := os.WriteFile(path, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	desktopErrorCode(t, query(p), rpc.CodeConflict)
	for _, p := range []rpc.FileResourceRangeParams{
		{Root: root, Path: "../escape", Limit: 1},
		{Root: root, Path: "/absolute", Limit: 1},
		{Root: root, Path: "nul\x00path", Limit: 1},
		{Root: "relative", Path: "resource", Limit: 1},
		{Root: root, Path: "resource", Limit: files.RangeLimit + 1},
		{Root: root, Path: "resource", Limit: 1, Offset: -1},
	} {
		desktopErrorCode(t, query(p), rpc.CodeBadRequest)
	}
	desktopErrorCode(t, rpc.Call(ctx, socket, "files.resourceRange", p, nil), rpc.CodeReadOnly)
	stale := rpc.DesktopIdentity{InstanceID: "stale", ClientID: "viewer"}
	desktopErrorCode(t, call(stale, p, nil), rpc.CodeConflict)
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" {
		t.Fatal("read acquired control")
	}
	if len(d.terms) != 0 {
		t.Fatal("read launched terminal")
	}
}

func TestDesktopResourceVersion(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	data := []byte{0xff, 0, '\r', '\n', 'x'}
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := rpc.FileResourceVersionParams{Root: root, Path: "link"}
	raw, _ := json.Marshal(p)
	call := rpc.DesktopCall{Desktop: rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}, Method: "files.resourceVersion", Params: raw}
	var version files.Version
	if err := rpc.Call(ctx, socket, "desktop.call", call, &version); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	real, err := filepath.EvalSymlinks(path)
	if err != nil || version.Path != real || !version.Stamp.Same(files.StatFile(path)) || version.Hash != hex.EncodeToString(sum[:]) {
		t.Fatalf("version: %+v err=%v", version, err)
	}
	desktopErrorCode(t, rpc.Call(ctx, socket, "files.resourceVersion", p, nil), rpc.CodeReadOnly)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", call, nil), rpc.CodeBadRequest)
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" {
		t.Fatal("version acquired control")
	}
}

func TestResourceRangeCanceled(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx = context.WithValue(ctx, desktopCallContext{}, desktopRequestContext{readOnly: true})
	raw, _ := json.Marshal(rpc.FileResourceRangeParams{Root: t.TempDir(), Path: "file", Limit: 1})
	for _, method := range []rpc.Handler{d.filesResourceRange, d.filesResourceVersion} {
		result, err := method(ctx, raw)
		if err != context.Canceled || result != nil {
			t.Fatalf("canceled: %v %v", result, err)
		}
	}
}
