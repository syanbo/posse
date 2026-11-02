package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/client"
	"posse/internal/rpc"
)

// 新窗口连上旧常驻进程、某个列表接口对不上时：照样发现版本差去交接，提示说清楚怎么办，带上常驻进程的信息给设置页。
func TestStaleDaemonStillUpgrades(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "hv")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	handoff := make(chan string, 1)
	srv := rpc.NewServer()
	srv.Handle("project.list", func(context.Context, json.RawMessage) (any, error) { return []project{}, nil })
	srv.Handle("workspace.list", func(context.Context, json.RawMessage) (any, error) { return []workspace{}, nil })
	// 旧常驻进程没有 task.list
	srv.Handle("daemon.status", func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"version": "0.0.9", "build": "abc1234 2020-01-01T00:00:00Z", "pid": 4242}, nil
	})
	srv.Handle("daemon.handoff", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct{ Exe string }
		json.Unmarshal(raw, &p)
		handoff <- p.Exe
		return map[string]any{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	a := &app{c: &client.Client{Socket: sock}}
	a.refresh()
	select {
	case exe := <-handoff:
		if exe == "" {
			t.Fatal("handoff without the window's executable")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no handoff to the stale daemon")
	}
	// 正在更新和读不到项目两句开头一样（"版本和窗口不一致"），按 App 的语言取
	m := appL()
	running, mismatch := []rune(m.UpgradeRunning()), []rune(m.UpgradeMismatch(""))
	n := 0
	for n < len(running) && n < len(mismatch) && running[n] == mismatch[n] {
		n++
	}
	if a.lastPoll == nil || !strings.HasPrefix(a.lastPoll.err, string(running[:n])) {
		t.Fatalf("poll error = %q", a.lastPoll.err)
	}
	if a.lastPoll.daemonInfo.Pid != 4242 {
		t.Fatalf("daemon info = %+v", a.lastPoll.daemonInfo)
	}
}
