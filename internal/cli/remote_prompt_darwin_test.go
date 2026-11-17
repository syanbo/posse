package cli

import (
	"context"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/creack/pty"
	"posse/internal/remote"
)

func TestRemotePromptNotification(t *testing.T) {
	if os.Getenv("POSSE_TEST_AUTH_NOTICE") == "1" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := remoteTerminalPrompt(ctx, remote.Prompt{Text: "Touch security key", Notify: true}); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRemotePromptNotification$")
	cmd.Env = append(os.Environ(), "POSSE_TEST_AUTH_NOTICE=1")
	terminal, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.Close()
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, terminal); close(done) }()
	if err := cmd.Wait(); err != nil {
		t.Fatalf("notification waited for input: %v", err)
	}
	terminal.Close()
	<-done
}
