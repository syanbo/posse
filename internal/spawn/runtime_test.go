//go:build darwin

package spawn

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testPlist = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleExecutable</key>
	<string>Posse</string>
	<key>CFBundleIdentifier</key>
	<string>com.example.posse</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<key>CFBundleShortVersionString</key>
	<string>1.2.3</string>
</dict>
</plist>
`

// fakeApp 在 dir 下造一个 Posse.app，返回里面的可执行文件。
func fakeApp(t *testing.T, dir, content string) string {
	t.Helper()
	app := filepath.Join(dir, "Posse.app")
	os.RemoveAll(app)
	for path, body := range map[string]string{
		"Contents/Info.plist":           testPlist,
		"Contents/MacOS/Posse":          content,
		"Contents/Resources/lib.dylib":  "lib",
		"Contents/_CodeSignature/x.txt": "sig",
	} {
		p := filepath.Join(app, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(app, "Contents", "MacOS", "Posse")
}

func tempDir(t *testing.T) string {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func inode(t *testing.T, path string) uint64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

func TestPrepareRuntime(t *testing.T) {
	src, root := tempDir(t), tempDir(t)
	exe := fakeApp(t, src, "v1")
	old := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	os.Chtimes(exe, old, old)

	got, err := prepare(root, exe, "/home/a")
	if err != nil {
		t.Fatal(err)
	}
	dir := runtimeDir(root, got)
	if dir == "" || filepath.Dir(dir) != filepath.Join(root, "com.example.posse") || !strings.HasPrefix(filepath.Base(dir), "1.2.3-20261001T080000Z-") {
		t.Fatalf("copy at %s (dir %q)", got, dir)
	}
	if got != filepath.Join(dir, "Posse.app", "Contents", "MacOS", "Posse") {
		t.Fatalf("exe %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "Posse.app", "Contents", "Resources", "lib.dylib")); string(b) != "lib" {
		t.Fatalf("resources not copied: %q", b)
	}
	// 克隆保留修改时间：常驻进程的构建标识（daemon.Build）和窗口的对得上
	if fi, _ := os.Stat(got); !fi.ModTime().Equal(old) {
		t.Fatalf("mtime %v, want %v", fi.ModTime(), old)
	}
	if _, err := os.Stat(filepath.Join(dir, "refs", refName("/home/a"))); err != nil {
		t.Fatalf("no ref for home: %v", err)
	}

	// 同一构建复用，不重新克隆
	ino := inode(t, got)
	again, err := prepare(root, exe, "/home/b")
	if err != nil || again != got || inode(t, again) != ino {
		t.Fatalf("reuse: %s %v", again, err)
	}
	// 已经是副本：原样返回，只登记
	self, err := prepare(root, got, "/home/c")
	if err != nil || self != got {
		t.Fatalf("copy of a copy: %s %v", self, err)
	}
	refs, _ := os.ReadDir(filepath.Join(dir, "refs"))
	if len(refs) != 3 {
		t.Fatalf("refs %d, want 3", len(refs))
	}

	// 换了包（新的可执行文件）是另一份副本，旧的不动
	exe = fakeApp(t, src, "v2")
	newer, err := prepare(root, exe, "/home/a")
	if err != nil || runtimeDir(root, newer) == dir {
		t.Fatalf("new build reused %s: %v", newer, err)
	}
	if b, _ := os.ReadFile(newer); string(b) != "v2" {
		t.Fatalf("new copy has %q", b)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("old copy gone: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, "com.example.posse")); len(entries) != 3 { // .lock 和两份副本
		t.Fatalf("leftovers: %v", entries)
	}
}

func TestRuntimeOutsideBundle(t *testing.T) {
	exe := filepath.Join(tempDir(t), "posse")
	os.WriteFile(exe, []byte("x"), 0o755)
	got, err := Runtime(exe, "/home/a")
	if err != nil || got != exe {
		t.Fatalf("got %s %v", got, err)
	}
	if RuntimeDir(exe) != "" {
		t.Fatal("bin/posse is not a runtime copy")
	}
}

func TestCollectRuntimes(t *testing.T) {
	src, root := tempDir(t), tempDir(t)
	now := time.Now()
	past := now.Add(-2 * refGrace)
	base := filepath.Join(root, "com.example.posse")
	copyOf := func(content, home string) string {
		exe, err := prepare(root, fakeApp(t, src, content), home)
		if err != nil {
			t.Fatal(err)
		}
		dir := runtimeDir(root, exe)
		refs, _ := os.ReadDir(filepath.Join(dir, "refs"))
		for _, r := range refs {
			os.Chtimes(filepath.Join(dir, "refs", r.Name()), past, past)
		}
		return dir
	}
	own := copyOf("own", "/home/a")
	idle := copyOf("idle", "/home/a")   // 这个 home 不再用，回收
	other := copyOf("other", "/home/a") // 另一个 home 也在用，只去掉自己的登记
	addRef(other, "/home/b")            // 刚登记的也不动
	term := copyOf("term", "/home/a")   // 还有终端，留着
	fresh := copyOf("fresh", "/home/a") // 刚准备好、常驻进程还没起来
	busy := copyOf("busy", "/home/a")   // 还有进程从里面运行
	addRef(fresh, "/home/a")
	os.MkdirAll(filepath.Join(base, ".tmp-1-x"), 0o700)
	os.MkdirAll(filepath.Join(base, ".trash-1-y"), 0o700)

	running := func(dirs []string) map[string]bool { return map[string]bool{busy: true} }
	removed, err := collect(base, "/home/a", []string{own, term}, running, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != idle {
		t.Fatalf("removed %v, want only %s", removed, idle)
	}
	for _, dir := range []string{own, other, term, fresh, busy} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("%s removed: %v", filepath.Base(dir), err)
		}
	}
	if _, err := os.Stat(filepath.Join(other, "refs", refName("/home/a"))); err == nil {
		t.Fatal("home a still holds the copy only home b uses")
	}
	if _, err := os.Stat(filepath.Join(busy, "refs", refName("/home/a"))); err == nil {
		t.Fatal("unused ref kept")
	}
	for _, name := range []string{".tmp-1-x", ".trash-1-y"} {
		if _, err := os.Stat(filepath.Join(base, name)); err == nil {
			t.Fatalf("%s left behind", name)
		}
	}

	// 还要用的副本登记丢了也不删，并补回登记
	os.RemoveAll(filepath.Join(term, "refs"))
	removed, err = collect(base, "/home/a", []string{own, term}, func([]string) map[string]bool { return nil }, now)
	if err != nil || len(removed) != 1 || removed[0] != busy {
		t.Fatalf("kept copy without refs: removed %v %v", removed, err)
	}
	if _, err := os.Stat(filepath.Join(term, "refs", refName("/home/a"))); err != nil {
		t.Fatalf("ref of kept copy not restored: %v", err)
	}

	// 进程退出、终端退完以后都回收
	removed, err = collect(base, "/home/a", []string{own}, func([]string) map[string]bool { return nil }, now.Add(2*refGrace))
	if err != nil || len(removed) != 2 { // term 和登记过了宽限期的 fresh
		t.Fatalf("second pass removed %v %v", removed, err)
	}
}

func TestRunningIn(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	mine, elsewhere := filepath.Dir(exe), tempDir(t)
	busy := runningIn([]string{mine, elsewhere})
	if !busy[mine] || busy[elsewhere] {
		t.Fatalf("busy %v", busy)
	}
}

// 进程比第一次的容量多时，不能只看到前一截：launchd（pid 1）排在列表最后。
func TestRunningInBeyondFirstBuffer(t *testing.T) {
	defer func(c int) { pidCap = c }(pidCap)
	pidCap = 16
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe, _ = filepath.EvalSymlinks(exe)
	mine := filepath.Dir(exe)
	busy := runningIn([]string{mine, "/sbin"})
	if !busy[mine] || !busy["/sbin"] {
		t.Fatalf("busy %v", busy)
	}
}
