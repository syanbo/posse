package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestDesktopFileReads(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := t.TempDir()
	path := filepath.Join(root, "target.bin")
	data := []byte{'a', 0xff, 0, 'z'}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("folder", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	query := func(method string, p, out any) error {
		raw, _ := json.Marshal(p)
		return rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: method, Params: raw}, out)
	}
	var stats []rpc.FileStatResult
	if err := query("files.stat", rpc.FilePathsParams{Paths: []string{path, filepath.Join(root, "missing"), filepath.Join(path, "child")}}, &stats); err != nil {
		t.Fatal(err)
	}
	if len(stats) != 3 || stats[0].Stamp.Size != int64(len(data)) || stats[0].Error != nil || !stats[1].Stamp.Missing || stats[1].Error != nil || stats[2].Error == nil || stats[2].Stamp.Missing {
		t.Fatalf("stats: %+v", stats)
	}
	var dirs []rpc.FileDirectoryResult
	if err := query("files.readDirs", rpc.FilePathsParams{Paths: []string{root, path}}, &dirs); err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 2 || dirs[0].Error != nil || len(dirs[0].Entries) != 3 || dirs[1].Error == nil {
		t.Fatalf("dirs: %+v", dirs)
	}
	if dirs[0].Entries[0].Name != "folder" || dirs[0].Entries[1].Link != "folder" || !dirs[0].Entries[1].Dir {
		t.Fatalf("symlink ordering: %+v", dirs[0].Entries)
	}
	var read rpc.FileRangeResult
	p := rpc.FileRangeParams{Path: path, Limit: files.RangeLimit, Expected: &stats[0].Stamp}
	if err := query("files.readRange", p, &read); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(read.Data, data) || !read.EOF {
		t.Fatalf("bytes changed: %+v", read)
	}
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	desktopErrorCode(t, query("files.readRange", p, nil), rpc.CodeConflict)
	for _, p := range []rpc.FileRangeParams{{Path: "relative", Limit: 1}, {Path: path, Limit: files.RangeLimit + 1}, {Path: path, Limit: 1, Offset: -1}} {
		desktopErrorCode(t, query("files.readRange", p, nil), rpc.CodeBadRequest)
	}
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" {
		t.Fatal("read acquired control")
	}
	if len(d.terms) != 0 {
		t.Fatal("read launched terminal")
	}
}

func TestFileBatchValidation(t *testing.T) {
	d, _ := newTestDaemon(t)
	for _, method := range []rpc.Handler{d.filesStat, d.filesReadDirs} {
		for _, paths := range [][]string{nil, {"relative"}, {"/nul\x00path"}, make([]string, rpc.FileBatchLimit+1)} {
			raw, _ := json.Marshal(rpc.FilePathsParams{Paths: paths})
			_, err := method(context.Background(), raw)
			desktopErrorCode(t, err, rpc.CodeBadRequest)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	raw, _ := json.Marshal(rpc.FilePathsParams{Paths: []string{t.TempDir()}})
	if _, err := d.filesStat(ctx, raw); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}
