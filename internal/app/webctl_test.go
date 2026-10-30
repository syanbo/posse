package app

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/rpc"
)

// shortDir 给 unix socket 一个够短的目录：macOS 的 TempDir 太长，超过 sun_path 的 104 字节。
func shortDir(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func rpcCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestListenCtl(t *testing.T) {
	path := filepath.Join(shortDir(t), "app.sock")
	// 上次窗口进程崩溃留下的文件没人应答：删掉重建
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ln, err := listenCtl(path)
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	defer ln.Close()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket stat = %v, %v; want mode 0600", fi, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// 已有窗口进程在接：不接管
	if _, err := listenCtl(path); !errors.Is(err, errCtlBusy) {
		t.Fatalf("second listen: %v, want errCtlBusy", err)
	}
}

func TestFindWeb(t *testing.T) {
	a := &app{mainUpdate: func(fn func()) { fn() }}
	a.workspaces = []workspace{{ID: "w1", Label: "posse"}, {ID: "w2", Label: "other"}}
	a.selected = "w2"
	ctx := context.Background()
	if _, err := a.target(ctx, webTarget{Workspace: "w1"}); rpcCode(err) != rpc.CodeNotFound {
		t.Fatalf("no tabs: %v", err)
	}
	if _, err := a.target(ctx, webTarget{Workspace: "nope"}); rpcCode(err) != rpc.CodeNotFound {
		t.Fatalf("unknown workspace: %v", err)
	}
	// 不带工作区时用侧栏选中的
	if w, err := a.webWorkspace(""); err != nil || w.ID != "w2" {
		t.Fatalf("default workspace = %v, %v; want w2", w, err)
	}
	a.selected = ""
	if _, err := a.webWorkspace(""); rpcCode(err) != rpc.CodeNotFound {
		t.Fatalf("nothing selected: %v", err)
	}
	// 没有网页标签时 list 回空列表而不是 null
	got, err := a.webList(ctx, []byte(`{"workspace":"w1"}`))
	if tabs, ok := got.([]webTab); err != nil || !ok || tabs == nil || len(tabs) != 0 {
		t.Fatalf("webList = %#v, %v", got, err)
	}
}

func TestCurrentWeb(t *testing.T) {
	now := time.Now()
	a := &webDoc{id: 1, used: now.Add(-time.Minute)}
	b := &webDoc{id: 2, used: now}
	c := &webDoc{id: 3}
	if got := currentWeb([]*webDoc{a, b, c}); got != b {
		t.Fatalf("current = %d, want 2 (shown most recently)", got.id)
	}
	// 都没显示过时是最后开的
	d := &webDoc{id: 4}
	if got := currentWeb([]*webDoc{d, c}); got != d {
		t.Fatalf("current = %d, want 4", got.id)
	}
}

// 请求先结束（CLI 断开、超时）时 onMain 不等主线程，主线程后来也不跑它。
func TestOnMainCanceled(t *testing.T) {
	held := make(chan func(), 1)
	a := &app{mainUpdate: func(fn func()) { held <- fn }}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	ran := false
	if err := a.onMain(ctx, func() { ran = true }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("onMain = %v, want deadline", err)
	}
	// 主线程后来才轮到它：CLI 已经报了失败，不再做
	(<-held)()
	if ran {
		t.Fatal("fn ran after the request ended")
	}
}

func TestCtlServerBadRequest(t *testing.T) {
	path := filepath.Join(shortDir(t), "app.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{mainUpdate: func(fn func()) { fn() }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.ctlServer().Serve(ctx, ln)
	if err := rpc.Call(ctx, path, "web.run", map[string]any{"fn": "alert"}, nil); rpcCode(err) != rpc.CodeBadRequest {
		t.Fatalf("web.run alert = %v, want bad_request", err)
	}
	if err := rpc.Call(ctx, path, "web.open", map[string]any{"url": "not a url"}, nil); rpcCode(err) != rpc.CodeBadRequest {
		t.Fatalf("web.open bad url = %v, want bad_request", err)
	}
}
