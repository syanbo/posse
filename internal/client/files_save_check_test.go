package client

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestCheckFileSave(t *testing.T) {
	ref := rpc.DesktopOperationRef{
		Desktop:  rpc.DesktopIdentity{ClientID: "window", InstanceID: "original-daemon", Epoch: 7},
		Sequence: 42,
	}
	sum := sha256.Sum256([]byte("submitted\r\n"))
	hash := hex.EncodeToString(sum[:])
	version := files.Version{Path: "/target/resolved-file", Stamp: files.Stamp{Size: 11, ModTime: time.Unix(123, 0).UTC(), Ino: 456, Mode: 0o640}, Hash: hash}
	saveErr := rpc.Errorf(rpc.CodeConflict, "save failed")
	statusErr := rpc.Errorf(rpc.CodeInternal, "status failed")
	readErr := rpc.Errorf(rpc.CodePermissionDenied, "version denied")
	tests := []struct {
		name       string
		state      string
		method     string
		changeRef  func(*rpc.DesktopOperationRef)
		version    files.Version
		saveErr    *rpc.Error
		statusErr  *rpc.Error
		readErr    *rpc.Error
		loseStatus bool
		loseRead   bool
		want       FileSaveCheck
		wantReads  int32
		wantErr    error
	}{
		{name: "running", state: "running", method: "files.save", want: FileSaveCheck{Pending: true}},
		{name: "done-match", state: "done", method: "files.save", version: version, want: FileSaveCheck{Version: version, Matches: true}, wantReads: 1},
		{name: "unknown-match", state: "unknown", version: version, want: FileSaveCheck{Version: version, Matches: true}, wantReads: 1},
		{name: "done-different", state: "done", method: "files.save", version: differentSaveVersion(version), want: FileSaveCheck{Version: differentSaveVersion(version)}, wantReads: 1},
		{name: "unknown-different", state: "unknown", version: differentSaveVersion(version), want: FileSaveCheck{Version: differentSaveVersion(version)}, wantReads: 1},
		{name: "done-error-match", state: "done", method: "files.save", saveErr: saveErr, version: version, want: FileSaveCheck{Version: version, Matches: true}, wantReads: 1},
		{name: "done-error-different", state: "done", method: "files.save", saveErr: saveErr, version: differentSaveVersion(version), want: FileSaveCheck{Version: differentSaveVersion(version)}, wantReads: 1, wantErr: saveErr},
		{name: "done-missing", state: "done", method: "files.save", version: missingSaveVersion(version), want: FileSaveCheck{Version: missingSaveVersion(version)}, wantReads: 1},
		{name: "unknown-missing", state: "unknown", version: missingSaveVersion(version), want: FileSaveCheck{Version: missingSaveVersion(version)}, wantReads: 1},
		{name: "status-error", statusErr: statusErr, wantErr: statusErr},
		{name: "status-disconnected", loseStatus: true, wantErr: rpc.ErrNoResponse},
		{name: "done-read-error", state: "done", method: "files.save", readErr: readErr, wantReads: 1, wantErr: os.ErrPermission},
		{name: "unknown-read-error", state: "unknown", readErr: readErr, wantReads: 1, wantErr: os.ErrPermission},
		{name: "done-error-read-error", state: "done", method: "files.save", saveErr: saveErr, readErr: readErr, wantReads: 1, wantErr: os.ErrPermission},
		{name: "version-disconnected", state: "done", method: "files.save", loseRead: true, wantReads: 1, wantErr: rpc.ErrNoResponse},
		{name: "wrong-client", state: "done", method: "files.save", changeRef: func(r *rpc.DesktopOperationRef) { r.Desktop.ClientID = "other" }, wantErr: errors.New("invalid file save operation response")},
		{name: "wrong-instance", state: "unknown", changeRef: func(r *rpc.DesktopOperationRef) { r.Desktop.InstanceID = "other" }, wantErr: errors.New("invalid file save operation response")},
		{name: "wrong-epoch", state: "running", method: "files.save", changeRef: func(r *rpc.DesktopOperationRef) { r.Desktop.Epoch++ }, wantErr: errors.New("invalid file save operation response")},
		{name: "wrong-sequence", state: "done", method: "files.save", changeRef: func(r *rpc.DesktopOperationRef) { r.Sequence++ }, wantErr: errors.New("invalid file save operation response")},
		{name: "invalid-state", state: "queued", method: "files.save", wantErr: errors.New("invalid file save operation response")},
		{name: "empty-state", method: "files.save", wantErr: errors.New("invalid file save operation response")},
		{name: "wrong-done-method", state: "done", method: "files.version", wantErr: errors.New("invalid file save operation response")},
		{name: "wrong-running-method", state: "running", method: "files.version", wantErr: errors.New("invalid file save operation response")},
		{name: "wrong-unknown-method", state: "unknown", method: "files.version", wantErr: errors.New("invalid file save operation response")},
		{name: "empty-done-method", state: "done", wantErr: errors.New("invalid file save operation response")},
		{name: "empty-running-method", state: "running", wantErr: errors.New("invalid file save operation response")},
	}
	for _, remote := range []bool{false, true} {
		t.Run(map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), "same-path")
					if err := os.WriteFile(path, []byte("local content differs"), 0o600); err != nil {
						t.Fatal(err)
					}
					id := ref.Desktop
					if tt.state == "unknown" {
						id.InstanceID, id.Epoch = "new-daemon", 1
					}
					operation := rpc.DesktopOperation{DesktopOperationRef: ref, State: tt.state, Method: tt.method, Error: tt.saveErr,
						// 旧结果既可能过时，也未必能解码成 FileSaveResult。
						Result: json.RawMessage(`"not a file save result"`),
					}
					if tt.changeRef != nil {
						tt.changeRef(&operation.DesktopOperationRef)
					}
					var calls, reads atomic.Int32
					socket := fakeAttach(t, func(conn net.Conn) {
						defer conn.Close()
						call := calls.Add(1)
						var req rpc.Request
						if err := json.NewDecoder(conn).Decode(&req); err != nil {
							t.Error(err)
							return
						}
						switch req.Method {
						case "desktop.operation":
							var got rpc.DesktopOperationRef
							if err := json.Unmarshal(req.Params, &got); err != nil || got != ref || call != 1 {
								t.Errorf("status changed original reference or order: %+v call=%d err=%v", got, call, err)
							}
							if tt.loseStatus {
								return
							}
							if tt.statusErr != nil {
								json.NewEncoder(conn).Encode(rpc.Response{Error: tt.statusErr})
							} else {
								rpc.WriteResult(conn, operation)
							}
						case "desktop.call":
							reads.Add(1)
							var p rpc.DesktopCall
							if err := json.Unmarshal(req.Params, &p); err != nil {
								t.Error(err)
								return
							}
							var q rpc.FileVersionParams
							if err := json.Unmarshal(p.Params, &q); err != nil || q.Path != path || p.Desktop != id || p.Method != "files.version" || p.Sequence != 0 || call != 2 {
								t.Errorf("unexpected request (only a bound version read is allowed): %+v %+v call=%d err=%v", p, q, call, err)
							}
							if tt.loseRead {
								return
							}
							if tt.readErr != nil {
								json.NewEncoder(conn).Encode(rpc.Response{Error: tt.readErr})
							} else {
								rpc.WriteResult(conn, tt.version)
							}
						default:
							t.Errorf("unexpected RPC: %s", req.Method)
							json.NewEncoder(conn).Encode(rpc.Response{Error: rpc.Errorf(rpc.CodeUnknown, "unexpected method")})
						}
					})
					c := (&Client{Socket: socket, Remote: remote}).WithDesktop(id)
					got, err := c.CheckFileSave(t.Context(), ref, path, hash)
					if !reflect.DeepEqual(got, tt.want) {
						t.Errorf("check: got %+v, want %+v", got, tt.want)
					}
					if tt.wantErr == nil {
						if err != nil {
							t.Errorf("unexpected error: %v", err)
						}
					} else if err == nil || (!errors.Is(err, tt.wantErr) && err.Error() != tt.wantErr.Error()) {
						t.Errorf("error: got %v, want %v", err, tt.wantErr)
					}
					if calls.Load() != 1+tt.wantReads || reads.Load() != tt.wantReads {
						t.Errorf("unexpected requests: calls=%d reads=%d", calls.Load(), reads.Load())
					}
					if data, err := os.ReadFile(path); err != nil || string(data) != "local content differs" {
						t.Errorf("check touched local file: %q %v", data, err)
					}
				})
			}
		})
	}
}

func differentSaveVersion(v files.Version) files.Version {
	v.Hash = "different-content-hash"
	return v
}

func missingSaveVersion(v files.Version) files.Version {
	// 即使服务端带回同一指纹，Missing 仍不能算匹配。
	v.Stamp.Missing = true
	return v
}
