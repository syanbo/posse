package cli

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/daemon"
)

func TestRemoteProbeResolvesHomeAlias(t *testing.T) {
	e, out := remoteProbeEnv(t)
	if err := os.MkdirAll(e.home, 0700); err != nil {
		t.Fatal(err)
	}
	real, err := daemon.RemoteHome(e.home)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(e.home), "alias")
	if err := os.Symlink(e.home, alias); err != nil {
		t.Fatal(err)
	}
	e.home = alias
	if err := cmdRemoteProbe(e, nil); err != nil {
		t.Fatal(err)
	}
	got := readRemoteProbe(t, out)
	if got.Home != real {
		t.Fatalf("probe home=%q want %q", got.Home, real)
	}
	other, err := daemon.RemoteHome(real)
	if err != nil || other != got.Home {
		t.Fatalf("daemon home=%q err=%v", other, err)
	}
}
