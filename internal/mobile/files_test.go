package mobile

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/term"
)

func TestPhoneFiles(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src", "deep"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# 你好"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "deep", "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "logo.png"), []byte("\x89PNG\x00"), 0o644)
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("中", fileReadMax)), 0o644)

	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}, dir: dir}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	wt := "id:p1::w1"

	names := func(rel string) []string {
		m := p.next(p.call("files.readDir", map[string]any{"worktree": wt, "relativePath": rel}))
		var out []string
		for _, e := range m["result"].([]any) {
			e := e.(map[string]any)
			n := e["name"].(string)
			if e["isDirectory"] == true {
				n += "/"
			}
			out = append(out, n)
		}
		return out
	}
	if got := strings.Join(names(""), " "); got != "src/ big.txt logo.png README.md" {
		t.Fatalf("root entries %q", got)
	}
	if got := strings.Join(names("src"), " "); got != "deep/ main.go" {
		t.Fatalf("src entries %q", got)
	}

	list := result(t, p.next(p.call("files.list", map[string]any{"worktree": wt})))
	if list["truncated"] != false || len(list["files"].([]any)) != 5 {
		t.Fatalf("list %v", list)
	}

	rd := result(t, p.next(p.call("files.read", map[string]any{"worktree": wt, "relativePath": "README.md"})))
	if rd["content"] != "# 你好" || rd["truncated"] != false {
		t.Fatalf("read %v", rd)
	}
	big := result(t, p.next(p.call("files.read", map[string]any{"worktree": wt, "relativePath": "big.txt"})))
	if big["truncated"] != true || !strings.HasSuffix(big["content"].(string), "中") {
		t.Fatalf("big read not cut on a rune boundary")
	}
	img := result(t, p.next(p.call("files.readPreview", map[string]any{"worktree": wt, "relativePath": "logo.png"})))
	if b, _ := base64.StdEncoding.DecodeString(img["content"].(string)); string(b) != "\x89PNG\x00" || img["mimeType"] != "image/png" {
		t.Fatalf("preview %v", img)
	}
	// 按段读：从中间读到末尾，size 是整个文件
	part := result(t, p.next(p.call("files.readRange", map[string]any{"worktree": wt, "relativePath": "logo.png", "offset": 2, "limit": 10})))
	if b, _ := base64.StdEncoding.DecodeString(part["content"].(string)); string(b) != "NG\x00" || part["size"] != float64(5) {
		t.Fatalf("range %v", part)
	}

	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "src/../../x"} {
		m := p.next(p.call("files.read", map[string]any{"worktree": wt, "relativePath": bad}))
		if m["ok"] != false || m["error"].(map[string]any)["code"] != "invalid_relative_path" {
			t.Fatalf("%s accepted: %v", bad, m)
		}
	}
	if m := p.next(p.call("files.read", map[string]any{"worktree": wt, "relativePath": "logo.png"})); m["ok"] != false {
		t.Fatalf("binary read accepted: %v", m)
	}
}

func TestWriteTimeoutGrowsWithSize(t *testing.T) {
	if got := writeTimeout(100); got != 15*time.Second {
		t.Fatalf("small message %v", got)
	}
	// 10 MB 的图片预览线上约 18 MB：按每秒 64 KB 也要给到 5 分钟上下，而不是一刀切的 15 秒
	if got := writeTimeout(18 << 20); got < 4*time.Minute {
		t.Fatalf("18 MB message %v", got)
	}
}
