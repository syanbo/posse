package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClassifyTrace(t *testing.T) {
	old := traceSlack
	traceSlack = 0
	defer func() { traceSlack = old }()
	home := resolvePath(t.TempDir())
	mk := func(rel string) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(rel string) string {
		p := filepath.Join(home, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ws := mk("Desktop/Works/proj")
	mk("Downloads")
	mk(".ssh")
	mk("Library/Caches/x")
	existing := write("Desktop/notes.md")
	time.Sleep(30 * time.Millisecond)
	sc := traceScope{home: home, inside: []string{ws}, since: time.Now()}
	time.Sleep(30 * time.Millisecond)
	apk := write("Downloads/ddt-apk/a/b.apk")
	sib := write("Desktop/Works/proj-wt/x.go")
	inWs := write("Desktop/Works/proj/out.log")
	cfg := write(".ssh/config.bak")
	cache := write("Library/Caches/x/y")
	os.WriteFile(existing, []byte("y"), 0o644)

	cases := []struct {
		path, want, kind string
	}{
		{apk, filepath.Join(home, "Downloads/ddt-apk"), "created"},
		{sib, filepath.Join(home, "Desktop/Works/proj-wt"), "created"},
		{existing, existing, "modified"},
		{cfg, cfg, "config"},
		{inWs, "", ""},
		{cache, "", ""},
		{filepath.Join(home, "Downloads/missing"), "", ""},
		{"/private/tmp", "", ""},
	}
	for _, c := range cases {
		got, kind, ok := classifyTrace(c.path, sc)
		if c.want == "" {
			if ok {
				t.Errorf("%s: recorded as %s %s", c.path, kind, got)
			}
			continue
		}
		if got != c.want || kind != c.kind {
			t.Errorf("%s: got %s %s, want %s %s", c.path, kind, got, c.kind, c.want)
		}
	}
}

func TestExpandHome(t *testing.T) {
	for in, want := range map[string]string{
		"~/a":        "/h/a",
		"$HOME/b":    "/h/b",
		"${HOME}/c":  "/h/c",
		"rel/x":      "/cwd/rel/x",
		"$OUT/x":     "",
		"/abs/*.png": "",
	} {
		if got := expandHome(in, "/h", "/cwd"); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
}
