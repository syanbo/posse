package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posse/internal/files"
	"posse/internal/rpc"
)

func TestDesktopFileSaveInputBudget(t *testing.T) {
	d, _ := newTestDaemon(t)
	srv := rpc.NewServer()
	srv.Handle("files.save", d.filesSave)
	srv.Handle("files.pasteImage", d.filesPasteImage)
	owner := desktopTakeTest(t, d, "owner")
	path := filepath.Join(t.TempDir(), "new")
	base, err := files.ReadVersion(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	// 未知字段仍占请求内存；用真实大 JSON 验证入场预算，不分配同样大小的解码正文。
	payload, err := json.Marshal(struct {
		rpc.FileSaveParams
		Padding string `json:"padding"`
	}{saveParams(path, base, nil), strings.Repeat("x", rpc.FileSaveRequestLimit/2+1)})
	if err != nil {
		t.Fatal(err)
	}
	request := rpc.DesktopCall{Desktop: owner, Sequence: 1, Method: "files.save", Params: payload}
	release, err := d.lockWritePaths(t.Context(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := desktopRequestCall(d, srv, ctx, request); done <- err }()
	waitFor(t, "large save accepted", func() bool { return desktopOperationGet(t, d, request).State == "running" })
	cancel()
	desktopErrorCode(t, <-done, rpc.CodeOutcomeUnknown)
	used := func() int {
		d.desktop.mu.Lock()
		defer d.desktop.mu.Unlock()
		return d.desktop.saveInputBytes
	}
	if got := used(); got != len(payload) {
		t.Fatalf("pending input budget = %d, want %d", got, len(payload))
	}
	next := request
	next.Desktop = desktopTakeTest(t, d, "new-owner")
	_, err = desktopRequestCall(d, srv, t.Context(), next)
	desktopErrorCode(t, err, rpc.CodeBusy)
	if desktopOperationGet(t, d, next).State != "unknown" || used() != len(payload) {
		t.Fatal("rejected save was accepted or changed budget")
	}
	paste := next
	paste.Method = "files.pasteImage"
	_, err = desktopRequestCall(d, srv, t.Context(), paste)
	desktopErrorCode(t, err, rpc.CodeBusy)
	if desktopOperationGet(t, d, paste).State != "unknown" || used() != len(payload) {
		t.Fatal("image paste bypassed save budget")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("save bypassed resource lock: %v", err)
	}
	release()
	waitFor(t, "large save finished", func() bool { return desktopOperationGet(t, d, request).State == "done" })
	if got := used(); got != 0 {
		t.Fatalf("completed save retained %d request bytes", got)
	}
	// 忙碌在接受前返回，原序号未消费；完成后同一新请求仍可正常执行。
	if _, err := desktopRequestCall(d, srv, t.Context(), next); err == nil {
		t.Fatal("stale missing baseline was accepted")
	} else {
		desktopErrorCode(t, err, rpc.CodeConflict)
	}
	if desktopOperationGet(t, d, next).State != "done" || used() != 0 {
		t.Fatal("failed save retained budget or busy consumed sequence")
	}
}
