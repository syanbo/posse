package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHostsConcurrentAndIdentity(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Put(home, Host{Name: fmt.Sprint(i), Target: "test-mac"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	hosts, err := Load(home)
	if err != nil || len(hosts) != 12 {
		t.Fatalf("hosts=%v err=%v", hosts, err)
	}
	h := hosts[0]
	id := h.ID
	h.Target = "other-mac"
	if _, err := Put(home, h); err != nil {
		t.Fatal(err)
	}
	got, err := Find(home, id)
	if err != nil || got.Target != "other-mac" {
		t.Fatalf("host=%+v err=%v", got, err)
	}
	if err := Remove(home, id); err != nil {
		t.Fatal(err)
	}
	if _, err := Find(home, id); err == nil {
		t.Fatal("deleted host found")
	}
	st, err := os.Stat(filepath.Join(home, "hosts.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v err=%v", st, err)
	}
}

func TestHostValidation(t *testing.T) {
	for _, target := range []string{"-oProxyCommand=evil", "host;id", "host name", "user@-host", "@host", "host@", "a@b@c", "$(id)", "host\nother"} {
		if err := (Host{Name: "Mac", Target: target}).Validate(); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
	for _, target := range []string{"mac", "user@mac", "192.0.2.1", "[::1]", "user@[::1]"} {
		if err := (Host{Name: "Mac", Target: target, Home: "~/Library/Application Support/Posse"}).Validate(); err != nil {
			t.Errorf("%q: %v", target, err)
		}
	}
	h := Host{Name: "Mac", Target: "mac", Home: "relative/path"}
	if h.Validate() == nil {
		t.Fatal("relative path accepted")
	}
	h.Home = "/tmp/home"
	h.Port = 65536
	if h.Validate() == nil {
		t.Fatal("invalid port accepted")
	}
}
