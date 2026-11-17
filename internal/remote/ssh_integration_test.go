package remote

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 4 && os.Args[1] == "remote" && os.Args[2] == "askpass" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		answer, err := AnswerPrompt(ctx, os.Args[3])
		if err != nil {
			os.Exit(1)
		}
		fmt.Println(answer)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// 使用临时密钥和高位回环端口，不读写用户的 known_hosts 或系统 sshd 配置。
func TestSSHIntegration(t *testing.T) {
	if os.Getenv("POSSE_SSH_TEST") != "1" {
		t.Skip("set POSSE_SSH_TEST=1 to run an isolated loopback sshd")
	}
	dir := t.TempDir()
	short, err := os.MkdirTemp("/tmp", "ps-sshtest-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	for _, name := range []string{"host", "client"} {
		out, err := exec.Command("/usr/bin/ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", filepath.Join(dir, name)).CombinedOutput()
		if err != nil {
			t.Fatalf("keygen: %v %s", err, out)
		}
	}
	pub, err := os.ReadFile(filepath.Join(dir, "client.pub"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0600); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	cfg := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey %s\nPidFile %s\nAuthorizedKeysFile %s\nStrictModes no\nUsePAM no\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nAllowStreamLocalForwarding yes\nLogLevel ERROR\n", port, filepath.Join(dir, "host"), filepath.Join(dir, "pid"), filepath.Join(dir, "authorized_keys"))
	config := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(config, []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	serverCtx, stopServer := context.WithCancel(context.Background())
	defer stopServer()
	sshd := exec.CommandContext(serverCtx, "/usr/sbin/sshd", "-D", "-e", "-f", config)
	var log boundedOutput
	sshd.Stderr = &log
	if err := sshd.Start(); err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- sshd.Wait() }()
	defer func() { stopServer(); <-serverDone }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
ready:
	for {
		select {
		case <-deadline.C:
			t.Fatalf("sshd not ready: %s", log.String())
		case <-tick.C:
			c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 50*time.Millisecond)
			if err == nil {
				c.Close()
				break ready
			}
		}
	}
	who, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	clientConfig := filepath.Join(dir, "ssh_config")
	clientText := fmt.Sprintf("Host isolated-mac\n HostName 127.0.0.1\n Port %d\n User %s\n IdentityFile %s\n IdentitiesOnly yes\n UserKnownHostsFile %s\n GlobalKnownHostsFile /dev/null\n", port, who.Username, filepath.Join(dir, "client"), filepath.Join(dir, "known_hosts"))
	if err := os.WriteFile(clientConfig, []byte(clientText), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(short, "daemon ' é.sock")
	rpcLn, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	rpcCtx, stopRPC := context.WithCancel(context.Background())
	defer stopRPC()
	srv := rpc.NewServer()
	info := rpc.RemoteInfo{Protocol: rpc.RemoteProtocol, Platform: "darwin", Arch: "arm64", Home: dir, InstanceID: "isolated-daemon", Capabilities: []string{"rpc.v1"}}
	var infoMu sync.Mutex
	liveInfo := info
	srv.Handle("daemon.info", func(context.Context, json.RawMessage) (any, error) {
		infoMu.Lock()
		defer infoMu.Unlock()
		return liveInfo, nil
	})
	srv.HandleStream("echo", func(_ context.Context, _ json.RawMessage, c net.Conn, r *bufio.Reader) error {
		rpc.WriteResult(c, nil)
		_, err := io.Copy(c, r)
		return err
	})
	go srv.Serve(rpcCtx, rpcLn)
	p := rpc.RemoteProbe{Protocol: rpc.RemoteProtocol, Platform: "darwin", Home: dir, Socket: socket, Running: true}
	body, _ := json.Marshal(p)
	helper := filepath.Join(dir, "probe ' é")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' 'startup noise' "+shellQuote(probeMarker+string(body))+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h := Host{ID: "isolated", Name: "Isolated", Target: "isolated-mac", Executable: helper, Home: dir}
	prompts := 0
	opts := Options{config: clientConfig, Required: []string{"rpc.v1"}, Prompt: func(_ context.Context, p Prompt) (string, error) {
		prompts++
		if !strings.Contains(p.Text, "SHA256:") {
			return "", fmt.Errorf("unexpected prompt: %s", p.Text)
		}
		return "yes", nil
	}}
	connections := NewConnections()
	defer connections.Close()
	s, err := connections.Open(ctx, h, opts)
	if err != nil {
		t.Fatalf("connect: %v; sshd: %s", err, log.String())
	}
	connections.Park()
	info.InstanceID, info.Version, info.Build = "replacement-daemon", "next", "next-build"
	info.Capabilities = []string{"rpc.v1", "terminal.stream.v1"}
	infoMu.Lock()
	liveInfo = info
	infoMu.Unlock()
	reused, err := connections.Open(ctx, h, opts)
	if err != nil || reused != s {
		t.Fatalf("reuse master: same=%v err=%v", reused == s, err)
	}
	if s.Info.InstanceID != info.InstanceID || s.Info.Version != info.Version || s.Info.Build != info.Build || !slices.Equal(s.Info.Capabilities, info.Capabilities) {
		t.Fatalf("reuse returned stale daemon information: %+v", s.Info)
	}
	if prompts != 1 {
		t.Fatalf("host key prompts=%d", prompts)
	}
	for range 2 {
		var got rpc.RemoteInfo
		if err := rpc.Call(ctx, s.Socket, "daemon.info", nil, &got); err != nil || got.InstanceID != info.InstanceID {
			t.Fatalf("handshake=%+v err=%v", got, err)
		}
	}
	conn, reader, err := rpc.Open(ctx, s.Socket, "echo", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte{0, 1, 27, 255}, 2048)
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := conn.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	_, err = io.ReadFull(reader, got)
	conn.Close()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("binary stream: %v", err)
	}
	control := s.control
	localSocket := s.Socket
	connections.Park()
	missingCapability := opts
	missingCapability.Required = []string{"rpc.v1", "files.v1"}
	if _, err := connections.Open(ctx, h, missingCapability); err == nil || !strings.Contains(err.Error(), "files.v1") {
		t.Fatalf("cached capability check: %v", err)
	}
	connections.Disconnect(h.ID)
	connections.Close()
	for _, p := range []string{control, localSocket} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("SSH path remains: %s", p)
		}
	}
	var still rpc.RemoteInfo
	if err := rpc.Call(ctx, socket, "daemon.info", nil, &still); err != nil {
		t.Fatalf("disconnect stopped daemon: %v", err)
	}
	stoppedProbe := p
	stoppedProbe.Running = false
	stoppedJSON, _ := json.Marshal(stoppedProbe)
	startScript := "#!/bin/sh\nif test \"$3\" = --start; then printf '%s\\n' " + shellQuote(probeMarker+string(body)) + "; else printf '%s\\n' " + shellQuote(probeMarker+string(stoppedJSON)) + "; fi\n"
	if err := os.WriteFile(helper, []byte(startScript), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(ctx, h, opts); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("implicit start: %v", err)
	}
	opts.Start = true
	started, err := Connect(ctx, h, opts)
	if err != nil {
		t.Fatalf("explicit start: %v", err)
	}
	started.Close()
	stoppedProbe.Platform = "linux"
	unsupportedJSON, _ := json.Marshal(stoppedProbe)
	startMarker := filepath.Join(dir, "unexpected-start")
	unsupportedScript := "#!/bin/sh\nif test \"$3\" = --start; then touch " + shellQuote(startMarker) + "; fi\nprintf '%s\\n' " + shellQuote(probeMarker+string(unsupportedJSON)) + "\n"
	if err := os.WriteFile(helper, []byte(unsupportedScript), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(ctx, h, opts); err == nil {
		t.Fatal("unsupported platform accepted")
	}
	if _, err := os.Stat(startMarker); !os.IsNotExist(err) {
		t.Fatal("started daemon on unsupported platform")
	}
	opts.Start = false
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nprintf '%s\\n' "+shellQuote(probeMarker+string(body))+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	// 已确认的 key 不再提示；不兼容 daemon 关闭自己的 master。
	opts.Required = []string{"files.v1"}
	if _, err := Connect(ctx, h, opts); err == nil || !strings.Contains(err.Error(), "files.v1") {
		t.Fatalf("capability check: %v", err)
	}
	if prompts != 1 {
		t.Fatal("existing host key prompted again")
	}
	opts.Required = []string{"rpc.v1"}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), append([]byte("restrict "), pub...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(ctx, h, opts); err == nil || !strings.Contains(err.Error(), "forwarding") {
		t.Fatalf("forwarding denial: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), pub, 0600); err != nil {
		t.Fatal(err)
	}
	// 用另一把临时公钥替换信任记录，模拟相同地址的主机密钥发生变化。
	wrongKey := fmt.Sprintf("[127.0.0.1]:%d %s", port, pub)
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(wrongKey), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Connect(ctx, h, opts); err == nil || !strings.Contains(err.Error(), "HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("changed host key: %v", err)
	}
	if prompts != 1 {
		t.Fatal("changed host key allowed confirmation")
	}
	t.Log("OpenSSH Unix forwarding, binary stream, master reuse, host-key confirmation/change, forwarding denial and disconnect verified on port " + strconv.Itoa(port))
}
