package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"posse/internal/daemon"
	"posse/internal/rpc"
)

func remoteProbeEnv(t *testing.T) (*env, *bytes.Buffer) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "posse-probe-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	out := new(bytes.Buffer)
	return &env{home: filepath.Join(dir, "home"), socket: filepath.Join(dir, "custom.sock"), out: out, errOut: io.Discard}, out
}

func readRemoteProbe(t *testing.T, out *bytes.Buffer) rpc.RemoteProbe {
	t.Helper()
	text := out.String()
	if strings.Count(text, "\n") != 1 || !strings.HasSuffix(text, "\n") || !strings.HasPrefix(text, "POSSE_REMOTE_V1:") {
		t.Fatalf("expected one prefixed JSON line, got %q", text)
	}
	data := strings.TrimPrefix(text, "POSSE_REMOTE_V1:")
	var result rpc.RemoteProbe
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(data), &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"protocol", "platform", "arch", "home", "socket", "version", "build", "running"} {
		if _, ok := fields[key]; !ok {
			t.Errorf("missing JSON key %q", key)
		}
	}
	return result
}

func TestRemoteProbeStopped(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		e, out := remoteProbeEnv(t)
		e.json = jsonOutput
		if err := cmdRemoteProbe(e, nil); err != nil {
			t.Fatal(err)
		}
		got := readRemoteProbe(t, out)
		want := rpc.RemoteProbe{
			Protocol: rpc.RemoteProtocol, Platform: runtime.GOOS, Arch: runtime.GOARCH,
			Home: e.home, Socket: e.socket, Version: daemon.Version, Build: daemon.Build,
			Edition: daemon.Edition,
			Running: false,
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("probe = %+v, want %+v", got, want)
		}
		if _, err := os.Stat(e.home); !os.IsNotExist(err) {
			t.Fatalf("discovery must not create home: %v", err)
		}
	}
}

func TestRemoteProbeLegacyDaemon(t *testing.T) {
	e, out := remoteProbeEnv(t)
	ln, err := net.Listen("unix", e.socket)
	if err != nil {
		t.Fatal(err)
	}
	// 旧 daemon 无需理解任何 RPC；发现连接必须直接关闭，不发送 daemon.info。
	done := make(chan error, 1)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				done <- nil
				return
			}
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			data, err := io.ReadAll(conn)
			conn.Close()
			if err != nil || len(data) != 0 {
				done <- fmt.Errorf("probe sent RPC data %q, read error: %v", data, err)
				return
			}
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	for _, args := range [][]string{nil, {"--start"}} {
		out.Reset()
		if err := cmdRemoteProbe(e, args); err != nil {
			t.Fatal(err)
		}
		got := readRemoteProbe(t, out)
		if !got.Running || got.Home != e.home || got.Socket != e.socket || got.Version != daemon.Version || got.Build != daemon.Build {
			t.Fatalf("unexpected probe: %+v", got)
		}
	}
	if _, err := os.Stat(e.home); !os.IsNotExist(err) {
		t.Fatalf("running daemon must not create home: %v", err)
	}
}

func TestRemoteProbeStartFailure(t *testing.T) {
	e, out := remoteProbeEnv(t)
	// EnsureDaemon 在打开日志前就失败，验证 --start 路径且不拉起任何子进程。
	if err := os.WriteFile(e.home, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmdRemoteProbe(e, []string{"--start"}); err == nil {
		t.Fatal("expected EnsureDaemon failure")
	}
	if out.Len() != 0 {
		t.Fatalf("failed start emitted discovery result: %q", out.String())
	}
}

func TestRemoteProbeInvalidArgs(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"unexpected"}, {"--start", "unexpected"}} {
		e, out := remoteProbeEnv(t)
		if err := cmdRemoteProbe(e, args); err == nil {
			t.Fatalf("accepted invalid args %v", args)
		}
		if out.Len() != 0 {
			t.Fatalf("invalid arguments emitted output: %q", out.String())
		}
		if _, err := os.Stat(e.home); !os.IsNotExist(err) {
			t.Fatalf("invalid arguments created home: %v", err)
		}
	}
}
