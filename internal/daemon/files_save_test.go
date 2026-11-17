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
	"posse/internal/mobile"
	"posse/internal/rpc"
)

func saveParams(path string, version files.Version, data []byte) rpc.FileSaveParams {
	sum := sha256.Sum256(data)
	return rpc.FileSaveParams{Path: path, Expected: version, Data: data, Hash: hex.EncodeToString(sum[:]), Revision: 42}
}

func saveCall(id rpc.DesktopIdentity, sequence uint64, p rpc.FileSaveParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: id, Sequence: sequence, Method: "files.save", Params: raw}
}

func TestDesktopFileSaveRoundTrip(t *testing.T) {
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	ctx := t.Context()
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	root := t.TempDir()
	path, link := filepath.Join(root, "file"), filepath.Join(root, "link")
	if err := os.WriteFile(path, []byte("before\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("file", link); err != nil {
		t.Fatal(err)
	}
	version := func(path string) files.Version {
		t.Helper()
		p, _ := json.Marshal(rpc.FileVersionParams{Path: path})
		var v files.Version
		if err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: "files.version", Params: p}, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	base := version(link)
	real, _ := filepath.EvalSymlinks(path)
	if base.Path != real || base.Hash == "" || base.Stamp.Mode.Perm() != 0o640 {
		t.Fatalf("baseline: %+v", base)
	}
	p := saveParams(link, base, []byte{'a', '\r', '\n', 0xff, 0})
	desktopErrorCode(t, rpc.Call(ctx, socket, "files.save", p, nil), rpc.CodeReadOnly)
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", saveCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" {
		t.Fatal("version read acquired control")
	}
	owner := desktopTakeTest(t, d, "owner")
	request := saveCall(owner, 1, p)
	var result rpc.FileSaveResult
	if err := rpc.Call(ctx, socket, "desktop.call", request, &result); err != nil {
		t.Fatal(err)
	}
	if result.Revision != p.Revision || !result.Version.Same(version(link)) || result.Version.Hash != p.Hash || result.Version.Stamp.Mode.Perm() != 0o640 {
		t.Fatalf("saved version: %+v", result)
	}
	if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, p.Data) {
		t.Fatalf("raw bytes: %q %v", data, err)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("save replaced symlink")
	}
	// 缓存只保留结果；相同请求再次到来不能覆盖随后外部修改的内容。
	if err := os.WriteFile(path, []byte("external"), 0o640); err != nil {
		t.Fatal(err)
	}
	var duplicate rpc.FileSaveResult
	if err := rpc.Call(ctx, socket, "desktop.call", request, &duplicate); err != nil || !duplicate.Version.Same(result.Version) {
		t.Fatalf("duplicate: %+v %v", duplicate, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "external" {
		t.Fatalf("save replayed: %q", data)
	}
	stored := desktopOperationGet(t, d, request)
	if bytes.Contains(stored.Result, []byte(`"data"`)) || len(stored.Result) > 4096 {
		t.Fatal("save retained file contents")
	}
	missing := filepath.Join(root, "new")
	missingBase := version(missing)
	if !missingBase.Stamp.Missing || missingBase.Hash != "" {
		t.Fatalf("missing baseline: %+v", missingBase)
	}
	if err := rpc.Call(ctx, socket, "desktop.call", saveCall(owner, 2, saveParams(missing, missingBase, nil)), &result); err != nil || result.Version.Stamp.Missing || result.Version.Stamp.Size != 0 {
		t.Fatalf("create empty: %+v %v", result, err)
	}
}

func TestDesktopFileSaveConflicts(t *testing.T) {
	for _, mode := range []string{"same-stamp", "retarget", "permissions", "missing-created"} {
		t.Run(mode, func(t *testing.T) {
			d, _ := newTestDaemon(t)
			srv := rpc.NewServer()
			srv.Handle("files.save", d.filesSave)
			owner := desktopTakeTest(t, d, "owner")
			root := t.TempDir()
			path := filepath.Join(root, "file")
			if mode != "missing-created" {
				if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			queryPath := path
			if mode == "retarget" {
				queryPath = filepath.Join(root, "link")
				if err := os.Symlink(path, queryPath); err != nil {
					t.Fatal(err)
				}
			}
			base, err := files.ReadVersion(t.Context(), queryPath)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "same-stamp":
				if err := os.WriteFile(path, []byte("edited"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(path, base.Stamp.ModTime, base.Stamp.ModTime); err != nil {
					t.Fatal(err)
				}
			case "permissions":
				if err := os.Chmod(path, 0o640); err != nil {
					t.Fatal(err)
				}
			case "retarget":
				other := filepath.Join(root, "other")
				if err := os.Link(path, other); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(queryPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(other, queryPath); err != nil {
					t.Fatal(err)
				}
			case "missing-created":
				if err := os.WriteFile(path, []byte("created"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			_, err = desktopRequestCall(d, srv, t.Context(), saveCall(owner, 1, saveParams(queryPath, base, []byte("save"))))
			desktopErrorCode(t, err, rpc.CodeConflict)
			if data, _ := os.ReadFile(path); !bytes.Equal(data, before) {
				t.Fatal("conflict overwrote file")
			}
		})
	}
}

func TestDesktopFileSaveWaitAndDisconnect(t *testing.T) {
	for _, mode := range []string{"disconnect", "changed", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			d, w := newTestDaemon(t)
			socket := desktopServer(t, d)
			owner := desktopTakeTest(t, d, "owner")
			path := filepath.Join(w.Path, "file")
			if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			base, err := files.ReadVersion(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			_, release, err := (mobileHost{d}).BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: w.ID, WorkspacePath: w.Path, Root: w.Path})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			request := saveCall(owner, 1, saveParams(path, base, []byte("saved")))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- rpc.Call(ctx, socket, "desktop.call", request, nil) }()
			waitFor(t, "save accepted", func() bool { return desktopOperationGet(t, d, request).State == "running" })
			desktopErrorCode(t, d.desktop.blockHandoff(), rpc.CodeBusy)
			if data, _ := os.ReadFile(path); string(data) != "before" {
				t.Fatal("save bypassed checkout lock")
			}
			switch mode {
			case "disconnect":
				cancel()
				if err := <-done; err == nil {
					t.Fatal("disconnected caller succeeded")
				}
				desktopTakeTest(t, d, "new-owner")
				late := request
				late.Sequence++
				desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", late, nil), rpc.CodeReadOnly)
				release()
			case "changed":
				if err := os.WriteFile(path, []byte("checkout"), 0o600); err != nil {
					t.Fatal(err)
				}
				release()
				desktopErrorCode(t, <-done, rpc.CodeConflict)
			case "shutdown":
				stopped := make(chan struct{})
				go func() { d.desktop.shutdown(); close(stopped) }()
				if err := <-done; err == nil {
					t.Fatal("shutdown save succeeded")
				}
				release()
				<-stopped
			}
			waitFor(t, "save finished", func() bool { return desktopOperationGet(t, d, request).State == "done" })
			operation := desktopOperationGet(t, d, request)
			d.desktop.mu.Lock()
			inputBytes := d.desktop.saveInputBytes
			d.desktop.mu.Unlock()
			if inputBytes != 0 {
				t.Fatalf("finished save retained %d request bytes", inputBytes)
			}
			want := map[string]string{"disconnect": "saved", "changed": "checkout", "shutdown": "before"}[mode]
			if data, _ := os.ReadFile(path); string(data) != want {
				t.Fatalf("file = %q, want %q", data, want)
			}
			if mode == "disconnect" {
				if operation.Error != nil {
					t.Fatal(operation.Error)
				}
				var saved rpc.FileSaveResult
				if err := json.Unmarshal(operation.Result, &saved); err != nil || saved.Revision != 42 {
					t.Fatalf("lost result: %+v %v", saved, err)
				}
				// 更换实例后旧操作结果只能是未知，不能推断从未执行。
				ref := request.Desktop
				ref.InstanceID = "another-instance"
				raw, _ := json.Marshal(rpc.DesktopOperationRef{Desktop: ref, Sequence: request.Sequence})
				result, err := d.desktopOperationStatus(t.Context(), raw)
				if err != nil || result.(rpc.DesktopOperation).State != "unknown" {
					t.Fatalf("changed instance: %+v %v", result, err)
				}
			}
		})
	}
}

func TestDesktopFileSaveValidation(t *testing.T) {
	d, _ := newTestDaemon(t)
	srv := rpc.NewServer()
	srv.Handle("files.save", d.filesSave)
	owner := desktopTakeTest(t, d, "owner")
	path := filepath.Join(t.TempDir(), "new")
	base, err := files.ReadVersion(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	sequence := uint64(0)
	for _, mutate := range []func(*rpc.FileSaveParams){
		func(p *rpc.FileSaveParams) { p.Path = "relative" },
		func(p *rpc.FileSaveParams) { p.Expected.Path = "" },
		func(p *rpc.FileSaveParams) { p.Hash = "bad" },
		func(p *rpc.FileSaveParams) { p.Expected.Hash = p.Hash },
		func(p *rpc.FileSaveParams) { p.Expected.Stamp.Missing = false },
	} {
		p := saveParams(path, base, []byte("saved"))
		mutate(&p)
		sequence++
		_, err := desktopRequestCall(d, srv, t.Context(), saveCall(owner, sequence, p))
		desktopErrorCode(t, err, rpc.CodeBadRequest)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid request created file: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := d.desktop.blockHandoff(); err != nil {
		t.Fatal(err)
	}
	defer d.desktop.unblock()
	_, err = desktopRequestCall(d, srv, ctx, saveCall(owner, sequence+1, saveParams(path, base, []byte("saved"))))
	desktopErrorCode(t, err, rpc.CodeBusy)
}
