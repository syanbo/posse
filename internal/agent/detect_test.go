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
