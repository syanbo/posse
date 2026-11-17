package remote

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestDiscoveryQuotesAndNoise(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "odd ' é $(touch injected)")
	// 以真实 shell 执行，确保路径与 home 中的元字符不会被二次解释。
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$POSSE_HOME\" \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h := Host{Executable: exe, Home: "/tmp/a ' é $(touch injected)"}
	cmd := exec.Command("/bin/sh", "-c", discoveryCommand(h, true))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if string(out) != h.Home+"\nremote\nprobe\n--start\n" {
		t.Fatalf("output=%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
		t.Fatal("shell injection")
	}
	p := rpc.RemoteProbe{Protocol: rpc.RemoteProtocol, Platform: "darwin", Home: "/tmp/home", Socket: "/tmp/home/possed.sock", Running: true}
	b, _ := json.Marshal(p)
	got, err := parseProbe(append([]byte("shell welcome\n"+probeMarker), append(b, []byte("\nlogout noise\n")...)...))
	if err != nil || got.Socket != p.Socket {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := parseProbe([]byte(probeMarker + string(b) + "\n" + probeMarker + string(b))); err == nil {
		t.Fatal("duplicate response accepted")
	}
	p.Socket = "/tmp/colon:socket"
	b, _ = json.Marshal(p)
	if _, err := parseProbe(append([]byte(probeMarker), b...)); err == nil {
		t.Fatal("ambiguous forwarding path accepted")
	}
}

func TestCapabilityUsesDaemon(t *testing.T) {
	p := rpc.RemoteProbe{Home: "/tmp/home", Version: "999"}
	info := rpc.RemoteInfo{Protocol: rpc.RemoteProtocol, Platform: "darwin", Home: p.Home, InstanceID: "one", Capabilities: []string{"rpc.v1"}}
	if err := validateInfo(p, info, []string{"rpc.v1"}); err != nil {
		t.Fatal(err)
	}
	if err := validateInfo(p, info, []string{"files.v1"}); err == nil {
		t.Fatal("CLI version substituted for daemon capability")
	}
	info.Home = "/tmp/other"
	if err := validateInfo(p, info, nil); err == nil {
		t.Fatal("wrong daemon accepted")
	}
}

func TestAskpassPrivateChannel(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ps-auth-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	a, err := newAskpass(ctx, dir, "/bin/false", func(_ context.Context, p Prompt) (string, error) {
		if p.Text != "Password:" {
			t.Error("prompt lost")
		}
		return "sensitive-answer", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	t.Setenv("POSSE_ASKPASS_SOCKET", a.socket)
	t.Setenv("POSSE_ASKPASS_TOKEN", a.token)
	answer, err := AnswerPrompt(ctx, "Password:")
	if err != nil || answer != "sensitive-answer" {
		t.Fatalf("auth failed: %v", err)
	}
	b, err := os.ReadFile(a.script)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), a.token) || strings.Contains(string(b), answer) {
		t.Fatal("credential written to wrapper")
	}
	t.Setenv("POSSE_ASKPASS_TOKEN", "invalid")
	if _, err := AnswerPrompt(ctx, "Password:"); err == nil {
		t.Fatal("unauthenticated prompt accepted")
	}
}

func TestAskpassCancel(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ps-auth-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	a, err := newAskpass(ctx, dir, "/bin/false", func(ctx context.Context, _ Prompt) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	t.Setenv("POSSE_ASKPASS_SOCKET", a.socket)
	t.Setenv("POSSE_ASKPASS_TOKEN", a.token)
	done := make(chan error, 1)
	go func() { _, err := AnswerPrompt(ctx, "Password:"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("prompt not delivered")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel blocked")
	}
}
