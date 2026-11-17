package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureDaemonCancelledBeforeStart(t *testing.T) {
	home := filepath.Join(t.TempDir(), "unused-home")
	c := &Client{Home: home, Socket: filepath.Join(home, "s")}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.EnsureDaemonContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ensure cancelled = %v", err)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("cancelled startup created the daemon home")
	}
}
