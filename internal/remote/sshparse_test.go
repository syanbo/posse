package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseSSHCommand(t *testing.T) {
	tests := []struct {
		in      string
		wantOK  bool
		target  string
		port    int
		ignored []string
	}{
		{"office-mac", false, "", 0, nil},
		{"user@host.local", false, "", 0, nil},
		{"ssh -p 2222 user@mac.local", true, "user@mac.local", 2222, nil},
		{"ssh -vvp 2222 example.test", true, "example.test", 2222, nil},
		{"ssh example.test -p 2222", true, "example.test", 2222, nil},
		{"ssh -p notaport example.test", false, "", 0, nil},
		{"ssh://alice@10.0.0.5:99999", false, "", 0, nil},
		{"ssh -p2222 -l alice mac.local", true, "alice@mac.local", 2222, nil},
		{"ssh -o Port=2200 -o User=bob -i ~/.ssh/id_ed25519 -J jump mac.local", true, "bob@mac.local", 2200, []string{"-i", "-J"}},
		{"ssh -t user@host ls -la", true, "user@host", 0, []string{"command"}},
		{"ssh://alice@10.0.0.5:2222", true, "alice@10.0.0.5", 2222, nil},
		{"ssh://[fe80::1]:2200", true, "[fe80::1]", 2200, nil},
	}
	for _, tc := range tests {
		h, ignored, ok := ParseSSHCommand(tc.in)
		if ok != tc.wantOK {
			t.Errorf("ParseSSHCommand(%q) ok = %v, want %v", tc.in, ok, tc.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if h.Target != tc.target || h.Port != tc.port || !reflect.DeepEqual(ignored, tc.ignored) {
			t.Errorf("ParseSSHCommand(%q) = (%+v, %v), want target=%q port=%d ignored=%v", tc.in, h, ignored, tc.target, tc.port, tc.ignored)
		}
	}
}

func TestDeriveName(t *testing.T) {
	for in, want := range map[string]string{
		"office-mac":           "office-mac",
		"alice@mac-mini.local": "mac-mini",
		"192.168.1.10":         "192.168.1.10",
		"root@10.0.0.8":        "10.0.0.8",
		"[fe80::1]":            "[fe80::1]",
		"":                     "",
	} {
		if got := DeriveName(in); got != want {
			t.Errorf("DeriveName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigAliasesStaticAndSafe(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(home, "should-not-exist")
	mainCfg := `
# comment
Include extra.conf
Include conf.d/*.conf

Match exec "touch ` + marker + `"
  User evil

Host *
  ServerAliveInterval 30

Host office-mac *.internal
  HostName mac-office.local
  User alice
  Port 2222

Host builder
  HostName 10.0.0.8
  User ci
  Include conditional.conf
`
	extraCfg := `
Host studio
  HostName studio.local
`
	condCfg := `
Host hidden-cond
  HostName cond.internal
`
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(mainCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "extra.conf"), []byte(extraCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "conditional.conf"), []byte(condCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	got := ConfigAliases(home)
	want := []Alias{
		{Name: "studio", HostName: "studio.local"},
		{Name: "office-mac", HostName: "mac-office.local", User: "alice", Port: 2222},
		{Name: "builder", HostName: "10.0.0.8", User: "ci"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ConfigAliases = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Match exec was executed")
	}
	if h := got[1].Hint(); h != "alice@mac-office.local:2222" {
		t.Fatalf("Hint = %q", h)
	}
}
