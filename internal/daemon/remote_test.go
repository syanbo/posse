package daemon

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestRemoteInfo(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "posse-info-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	home := filepath.Join(dir, "home")
	d := &Daemon{cfg: Config{Home: home}}
	ctx, cancel := context.WithCancel(context.Background())
	srv := rpc.NewServer()
	d.register(srv, cancel)
	ln, err := net.Listen("unix", filepath.Join(dir, "rpc.sock"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	var first rpc.RemoteInfo
	if err := rpc.Call(callCtx, ln.Addr().String(), "daemon.info", nil, &first); err != nil {
		t.Fatal(err)
	}
	want := rpc.RemoteInfo{
		Protocol: rpc.RemoteProtocol, InstanceID: d.instanceID,
		Platform: runtime.GOOS, Arch: runtime.GOARCH, Home: home,
		Version: Version, Build: Build,
		Capabilities: []string{"rpc.v1", "terminal.stream.v1"},
	}
	if first.InstanceID == "" || !reflect.DeepEqual(first, want) {
		t.Fatalf("daemon.info = %+v, want %+v with nonempty instance ID", first, want)
	}
	var second rpc.RemoteInfo
	if err := rpc.Call(callCtx, ln.Addr().String(), "daemon.info", nil, &second); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated query changed info: %+v -> %+v", first, second)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("read-only query created home: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := rpc.Call(callCtx, ln.Addr().String(), "daemon.info", nil, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"protocol", "instanceId", "platform", "arch", "home", "version", "build", "capabilities"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing JSON key %q", key)
		}
	}
}

func TestRemoteInstanceID(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	first := &Daemon{cfg: Config{Home: home}}
	first.register(rpc.NewServer(), func() {})
	id := first.instanceID
	first.register(rpc.NewServer(), func() {})
	if id == "" || first.instanceID != id {
		t.Fatal("one daemon must retain its instance ID across registrations")
	}
	// 重启和交接都构造新的 Daemon，即便 home 没变也必须有新的实例标识。
	second := &Daemon{cfg: first.cfg}
	second.register(rpc.NewServer(), func() {})
	if second.instanceID == "" || second.instanceID == id {
		t.Fatal("new daemon must receive a different instance ID")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("instance ID must not create persistent state: %v", err)
	}
}
