package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"posse/internal/app"
	"posse/internal/rpc"
)

func webEnv(t *testing.T) *env {
	// macOS 的 TempDir 太长，放不下 unix socket 的路径
	home, err := os.MkdirTemp("/tmp", "pw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	return &env{home: home, out: io.Discard, errOut: io.Discard}
}

func errCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func TestWebNoWindow(t *testing.T) {
	e := webEnv(t)
	if err := e.callApp(time.Second, "web.list", nil, nil); errCode(err) != codeNoWindow {
		t.Fatalf("no socket: %v", err)
	}
	// 窗口进程崩溃留下的 socket 文件，没人应答
	ln, err := net.Listen("unix", app.CtlSocket(e.home))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Stat(app.CtlSocket(e.home)); err != nil {
		t.Fatal(err)
	}
	if err := e.callApp(time.Second, "web.list", nil, nil); errCode(err) != codeNoWindow {
		t.Fatalf("stale socket: %v", err)
	}
}

func TestWebTimeout(t *testing.T) {
	e := webEnv(t)
	ln, err := net.Listen("unix", app.CtlSocket(e.home))
	if err != nil {
		t.Fatal(err)
	}
	s := rpc.NewServer()
	s.Handle("web.eval", func(ctx context.Context, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Serve(ctx, ln)
	if err := e.callApp(100*time.Millisecond, "web.eval", nil, nil); errCode(err) != rpc.CodeTimeout {
		t.Fatalf("hung page: %v", err)
	}
	// wait 最后一轮只剩极短的时间，拨号时就到期：报超时，不报窗口没开
	if err := e.callApp(time.Nanosecond, "web.eval", nil, nil); errCode(err) != rpc.CodeTimeout {
		t.Fatalf("expired at dial: %v", err)
	}
}

func TestWebParams(t *testing.T) {
	t.Setenv("POSSE_WORKSPACE_ID", "w_env")
	f := newWebFlags("web list", time.Second)
	if _, err := parse(f.fs, []string{"--tab", "3"}); err != nil {
		t.Fatal(err)
	}
	if p := f.params(nil); p["workspace"] != "w_env" || p["tab"] != 3 {
		t.Fatalf("params = %v", p)
	}
	f = newWebFlags("web list", time.Second)
	if _, err := parse(f.fs, []string{"--workspace", "w_flag"}); err != nil {
		t.Fatal(err)
	}
	if p := f.params(nil); p["workspace"] != "w_flag" || p["tab"] != nil {
		t.Fatalf("params = %v", p)
	}
}

func TestWebArg(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/r.html", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	abs, _ := os.Getwd()
	for in, want := range map[string]string{
		"r.html":                abs + "/r.html",
		"missing.html":          "missing.html",
		"localhost:3000":        "localhost:3000",
		"http://localhost/a.md": "http://localhost/a.md",
	} {
		if got := webArg(in); got != want {
			t.Errorf("webArg(%q) = %q, want %q", in, got, want)
		}
	}
}
