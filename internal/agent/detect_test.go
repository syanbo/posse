package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func writeExe(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// 登录 shell 的 alias、函数和 PATH 里的程序都算装了；启动文件往 stdout 打的字不干扰结果；
// 转发脚本的目录不算。
func TestDetectInShell(t *testing.T) {
	dir := t.TempDir()
	bin, shim := filepath.Join(dir, "bin"), filepath.Join(dir, "shim")
	writeExe(t, filepath.Join(bin, "cursor-agent"))
	writeExe(t, filepath.Join(shim, "grok"))
	rc := "echo hello from rc\nexport PATH=" + bin + ":$PATH\nalias claude=/nowhere/claude\n"
	rc += "export ZCODE_SESSION_DB_PATH=" + Quote("~/ZCode Data/session.sqlite") + "\nZCODE_SESSION_DB=unexported.sqlite\n"
	if err := os.WriteFile(filepath.Join(dir, ".zshrc"), []byte(rc), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=" + dir, "ZDOTDIR=" + dir, "PATH=/usr/bin:/bin", "TERM=dumb"}
	det, err := DetectInShell(context.Background(), "/bin/zsh", env)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(det.Found, []string{"claude", "cursor"}) {
		t.Fatalf("found %v", det.Found)
	}
	if got := det.ZCode.Database(dir); got != filepath.Join(dir, "ZCode Data", "session.sqlite") || det.ZCode.LegacyDB != "" {
		t.Fatalf("login shell ZCode database: %q, legacy override %q", got, det.ZCode.LegacyDB)
	}
	if got := Installed(det.Found, det.Path+":"+shim, shim); !slices.Equal(got, []string{"claude", "cursor"}) {
		t.Fatalf("installed %v", got)
	}
	// 之后新装进 PATH 的，只 stat 也认得出
	writeExe(t, filepath.Join(bin, "codex"))
	if got := Installed(det.Found, det.Path, shim); !slices.Equal(got, []string{"claude", "codex", "cursor"}) {
		t.Fatalf("after install %v", got)
	}
}

func TestWithoutDir(t *testing.T) {
	if got := WithoutDir("/a:/b/:/c::/b", "/b"); got != "/a:/c" {
		t.Fatalf("got %q", got)
	}
}

func TestDetectZCodeRequiresTUI(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "zcode")
	env := []string{"HOME=" + dir, "ZDOTDIR=" + dir, "PATH=" + dir + ":/usr/bin:/bin"}
	for _, tc := range []struct {
		output string
		found  bool
	}{
		{"Cannot find package '@zcode/tui'", false},
		{"TUI requires an interactive terminal.", true},
	} {
		if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' "+Quote(tc.output)+" >&2\nexit 1\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		det, err := DetectInShell(context.Background(), "/bin/zsh", env)
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(Installed(det.Found, det.Path, ""), "zcode"); got != tc.found {
			t.Fatalf("probe %q: detected = %v", tc.output, got)
		}
		if got := ProbeZCode(t.Context(), bin); got != tc.found {
			t.Fatalf("direct probe %q: detected = %v", tc.output, got)
		}
	}
}

func TestDetectZCodeThroughFish(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "zcode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' 'TUI requires an interactive terminal.' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// 模拟 fish 从配置加载 PATH、再按 argv 把 POSIX 探测交给 sh。
	fish := filepath.Join(dir, "fish")
	script := "#!/bin/sh\n[ \"$4\" = 'exec /bin/sh -c \"$argv[1]\"' ] || exit 1\nexport PATH=" + Quote(dir) + ":$PATH\nexec /bin/sh -c \"$5\"\n"
	if err := os.WriteFile(fish, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	det, err := DetectInShell(t.Context(), fish, []string{"PATH=/usr/bin:/bin"})
	if err != nil || !slices.Contains(det.Found, "zcode") {
		t.Fatalf("fish detection = %v, %v", det, err)
	}
}
