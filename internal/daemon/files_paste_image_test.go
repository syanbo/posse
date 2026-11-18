package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func pasteImageParams(t *testing.T, data []byte) rpc.FilePasteImageParams {
	t.Helper()
	if data == nil {
		var b bytes.Buffer
		if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
			t.Fatal(err)
		}
		data = b.Bytes()
	}
	sum := sha256.Sum256(data)
	return rpc.FilePasteImageParams{Data: data, Hash: hex.EncodeToString(sum[:])}
}

func pasteImageCall(id rpc.DesktopIdentity, seq uint64, p rpc.FilePasteImageParams) rpc.DesktopCall {
	raw, _ := json.Marshal(p)
	return rpc.DesktopCall{Desktop: id, Sequence: seq, Method: "files.pasteImage", Params: raw}
}

func TestDesktopPasteImageRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	p := pasteImageParams(t, nil)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "files.pasteImage", p, nil), rpc.CodeReadOnly)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pasteImageCall(viewer, 1, p), nil), rpc.CodeReadOnly)
	owner := desktopTakeTest(t, d, "owner")
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pasteImageCall(owner, 0, p), nil), rpc.CodeBadRequest)
	request := pasteImageCall(owner, 1, p)
	var got rpc.FilePasteImageResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", request, &got); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(got.Path) || filepath.Dir(got.Path) != dir {
		t.Fatalf("target path = %q", got.Path)
	}
	data, err := os.ReadFile(got.Path)
	if err != nil || !bytes.Equal(data, p.Data) {
		t.Fatalf("saved content: %x %v", data, err)
	}
	fi, err := os.Stat(got.Path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode: %v %v", fi, err)
	}
	if err := os.Remove(got.Path); err != nil {
		t.Fatal(err)
	}
	desktopTakeTest(t, d, "replacement")
	var again rpc.FilePasteImageResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", request, &again); err != nil || again != got {
		t.Fatalf("duplicate: %+v %v", again, err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "posse-paste-*.png"))
	if len(entries) != 0 {
		t.Fatalf("duplicate recreated image: %v", entries)
	}
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pasteImageCall(owner, 2, p), nil), rpc.CodeReadOnly)
	stored := desktopOperationGet(t, d, request)
	if bytes.Contains(stored.Result, []byte(`"data"`)) || len(stored.Result) > 4096 {
		t.Fatal("image bytes retained in operation result")
	}
}

func TestDesktopPasteImageValidation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	owner := desktopTakeTest(t, d, "owner")
	badHash := pasteImageParams(t, nil)
	badHash.Hash = "bad"
	for i, p := range []rpc.FilePasteImageParams{badHash, pasteImageParams(t, []byte("not PNG")), pasteImageParams(t, []byte("\x89PNG\r\n\x1a\n")), pasteImageParams(t, make([]byte, files.MaxSize+1))} {
		desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pasteImageCall(owner, uint64(i+1), p), nil), rpc.CodeBadRequest)
	}
	ctx := context.WithValue(t.Context(), desktopCallContext{}, desktopRequestContext{identity: owner, readOnly: true})
	raw, _ := json.Marshal(pasteImageParams(t, nil))
	_, err := d.filesPasteImage(ctx, raw)
	desktopErrorCode(t, err, rpc.CodeReadOnly)
	ctx = context.WithValue(t.Context(), desktopCallContext{}, desktopRequestContext{identity: owner})
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := d.filesPasteImage(ctx, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled handler = %v", err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "posse-paste-*.png"))
	if len(entries) != 0 {
		t.Fatalf("failed validation left images: %v", entries)
	}
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", pasteImageCall(owner, 10, pasteImageParams(t, nil)), nil), rpc.CodeNotFound)
}

func TestDesktopPasteImageSharedBudget(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	d, _ := newTestDaemon(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	srv := rpc.NewServer()
	srv.Handle("files.pasteImage", func(ctx context.Context, raw json.RawMessage) (any, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return d.filesPasteImage(ctx, raw)
	})
	srv.Handle("files.save", d.filesSave)
	owner := desktopTakeTest(t, d, "owner")
	payload, err := json.Marshal(struct {
		rpc.FilePasteImageParams
		Padding string `json:"padding"`
	}{pasteImageParams(t, nil), strings.Repeat("x", rpc.FileSaveRequestLimit/2+1)})
	if err != nil {
		t.Fatal(err)
	}
	request := rpc.DesktopCall{Desktop: owner, Sequence: 1, Method: "files.pasteImage", Params: payload}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := desktopRequestCall(d, srv, ctx, request); done <- err }()
	<-entered
	cancel()
	desktopErrorCode(t, <-done, rpc.CodeOutcomeUnknown)
	d.desktop.mu.Lock()
	used := d.desktop.saveInputBytes
	d.desktop.mu.Unlock()
	if used != len(payload) {
		t.Fatalf("accepted image budget = %d, want %d", used, len(payload))
	}
	for _, method := range []string{"files.save", "files.pasteImage"} {
		next := rpc.DesktopCall{Desktop: owner, Sequence: 2, Method: method, Params: payload}
		_, err := desktopRequestCall(d, srv, t.Context(), next)
		desktopErrorCode(t, err, rpc.CodeBusy)
		if desktopOperationGet(t, d, next).State != "unknown" {
			t.Fatal("budget rejection consumed sequence")
		}
	}
	// 执行器取消已接受操作后才释放输入预算，不随调用方断线释放。
	d.desktop.mu.Lock()
	d.desktop.operations[rpc.DesktopOperationRef{Desktop: owner, Sequence: 1}].cancel()
	d.desktop.mu.Unlock()
	waitFor(t, "cancelled image finished", func() bool { return desktopOperationGet(t, d, request).State == "done" })
	d.desktop.mu.Lock()
	used = d.desktop.saveInputBytes
	d.desktop.mu.Unlock()
	if used != 0 {
		t.Fatalf("cancelled image retained budget: %d", used)
	}
	path := filepath.Join(dir, "saved")
	base, err := files.ReadVersion(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := desktopRequestCall(d, srv, t.Context(), saveCall(owner, 2, saveParams(path, base, []byte("saved")))); err != nil {
		t.Fatalf("budget not released for save: %v", err)
	}
}

func TestDesktopPasteFileAndMkdir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	d, _ := newTestDaemon(t)
	socket := desktopServer(t, d)
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	owner := desktopTakeTest(t, d, "owner")

	body := []byte("dropped payload")
	sum := sha256.Sum256(body)
	fp := rpc.FilePasteFileParams{Name: "notes.md", Data: body, Hash: hex.EncodeToString(sum[:])}
	rawFP, _ := json.Marshal(fp)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "files.pasteFile", fp, nil), rpc.CodeReadOnly)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Sequence: 1, Method: "files.pasteFile", Params: rawFP}, nil), rpc.CodeReadOnly)

	var pasted rpc.FilePasteImageResult
	if err := rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: owner, Sequence: 1, Method: "files.pasteFile", Params: rawFP}, &pasted); err != nil {
		t.Fatal(err)
	}
	if filepath.Base(pasted.Path) != "notes.md" {
		t.Fatalf("pasted path = %q", pasted.Path)
	}
	if got, err := os.ReadFile(pasted.Path); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("pasted content = %q, %v", got, err)
	}

	badName := fp
	badName.Name = "../escape.md"
	rawBad, _ := json.Marshal(badName)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: owner, Sequence: 2, Method: "files.pasteFile", Params: rawBad}, nil), rpc.CodeBadRequest)

	targetDir := filepath.Join(t.TempDir(), "sub", "nested")
	rawMkdir, _ := json.Marshal(rpc.FileMkdirParams{Path: targetDir})
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "files.mkdir", rpc.FileMkdirParams{Path: targetDir}, nil), rpc.CodeReadOnly)
	desktopErrorCode(t, rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Sequence: 1, Method: "files.mkdir", Params: rawMkdir}, nil), rpc.CodeReadOnly)
	if err := rpc.Call(t.Context(), socket, "desktop.call", rpc.DesktopCall{Desktop: owner, Sequence: 3, Method: "files.mkdir", Params: rawMkdir}, nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(targetDir); err != nil || !info.IsDir() {
		t.Fatalf("mkdir target = %v, %v", info, err)
	}
}
