package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintTailscale(t *testing.T) {
	var out bytes.Buffer
	e := &env{out: &out, errOut: &out}
	st := &tailscaleStatus{
		Enabled:    true,
		State:      "needs_login",
		Hostname:   "posse-mac",
		ControlURL: "https://hs.example.com",
		AuthURL:    "https://login.tailscale.com/a/abc",
	}
	printTailscale(e, st)()
	got := out.String()
	for _, want := range []string{"tailscale: needs_login", "hostname: posse-mac", "control: https://hs.example.com", "login: https://login.tailscale.com/a/abc"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestMobileTailscaleOnRejectsMissingFlagValue(t *testing.T) {
	var out bytes.Buffer
	e := &env{out: &out, errOut: &out}
	if err := cmdMobileTailscale(e, []string{"on", "--hostname", "--disable-bind-interface"}); err == nil {
		t.Fatal("expected error when --hostname is followed by another flag")
	}
	if err := cmdMobileTailscale(e, []string{"on", "--control-url", "--auth-key", "k"}); err == nil {
		t.Fatal("expected error when --control-url is followed by another flag")
	}
	if err := cmdMobileTailscale(e, []string{"on", "--hostname", "--disable-bind-interface", "--hostname=valid-node"}); err == nil {
		t.Fatal("expected error even when a duplicate --hostname follows a missing value")
	}
}
