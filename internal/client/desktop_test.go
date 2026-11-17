package client

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestRemoteClientNeverStartsLocalDaemon(t *testing.T) {
	home := filepath.Join(t.TempDir(), "remote-home")
	c := &Client{Home: home, Socket: filepath.Join(home, "missing.sock"), Remote: true}
	if err := c.EnsureDaemon(); err == nil {
		t.Fatal("disconnected remote client started locally")
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("remote client created a local home")
	}
	old := c.WithDesktop(rpc.DesktopIdentity{ClientID: "one", Epoch: 1, InstanceID: "daemon"})
	next := old.WithDesktop(rpc.DesktopIdentity{ClientID: "one", Epoch: 2, InstanceID: "daemon"})
	if old.Desktop.Epoch != 1 || next.Desktop.Epoch != 2 || !next.Remote {
		t.Fatal("control identity was mutated in place")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := next.Open(ctx, "terminal.attach", nil, nil); err == nil {
		t.Fatal("cancelled open succeeded")
	}
}
