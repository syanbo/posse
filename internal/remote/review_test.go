package remote

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"testing"
	"time"

	"posse/internal/rpc"
)

func TestAskpassNoticeAndHelperExit(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ps-auth-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	entered, exited := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	a, err := newAskpass(ctx, dir, "/bin/false", func(ctx context.Context, p Prompt) (string, error) {
		if p.Notify {
			return "", nil
		}
		close(entered)
		<-ctx.Done()
		close(exited)
		return "", ctx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.close()
	t.Setenv("POSSE_ASKPASS_SOCKET", a.socket)
	t.Setenv("POSSE_ASKPASS_TOKEN", a.token)
	t.Setenv("SSH_ASKPASS_PROMPT", "none")
	if _, err := AnswerPrompt(ctx, "Touch security key"); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("unix", a.socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(conn).Encode(authRequest{Token: a.token, Prompt: Prompt{Text: "Password:"}}); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("prompt not delivered")
	}
	conn.Close()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("helper exit did not cancel prompt")
	}
}

func TestForwardPathExpansionRejected(t *testing.T) {
	for _, socket := range []string{`/tmp/posse\test/possed.sock`, `/tmp/${HOME}/possed.sock`} {
		p := rpc.RemoteProbe{Protocol: rpc.RemoteProtocol, Platform: "darwin", Home: "/tmp/home", Socket: socket, Running: true}
		b, _ := json.Marshal(p)
		if _, err := parseProbe(append([]byte(probeMarker), b...)); err == nil {
			t.Errorf("accepted forwarding path %q", socket)
		}
	}
}
