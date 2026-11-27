package spawn

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestServerVersionDirRetainAndCollect(t *testing.T) {
	root := t.TempDir()
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	home := filepath.Join(root, "home")
	versions := filepath.Join(home, "server", "versions")
	v1 := filepath.Join(versions, "0.1.54-old")
	v2 := filepath.Join(versions, "0.1.54-new")
	v3 := filepath.Join(versions, "0.1.54-current")
	stranger := filepath.Join(versions, "unmanaged-dir")
	for _, dir := range []string{v1, v2, v3, stranger} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "posse"), []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{v1, v2, v3} {
		if err := os.WriteFile(filepath.Join(dir, ServerManagedMarker), []byte("ok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("versions/0.1.54-current", filepath.Join(home, "server", "current")); err != nil {
		t.Fatal(err)
	}

	exe2 := filepath.Join(v2, "posse")
	if got := RuntimeDirFor(exe2, home, "headless"); got != v2 {
		t.Fatalf("RuntimeDirFor(%q) = %q, want %q", exe2, got, v2)
	}
	// 非 headless 形态、跨 home、错误文件名、无受管标记都不能被识别为受管无头版本目录
	if got := RuntimeDirFor(exe2, home, "full"); got != "" {
		t.Fatalf("full edition falsely matched server dir: %q", got)
	}
	if got := RuntimeDirFor(exe2, filepath.Join(root, "other-home"), "headless"); got != "" {
		t.Fatalf("cross-home falsely matched server dir: %q", got)
	}
	if got := RuntimeDirFor(filepath.Join(v2, "other-bin"), home, "headless"); got != "" {
		t.Fatalf("unrelated executable falsely matched server dir: %q", got)
	}
	if got := RuntimeDirFor(filepath.Join(stranger, "posse"), home, "headless"); got != "" {
		t.Fatalf("unmanaged dir falsely matched server dir: %q", got)
	}

	if err := Retain(filepath.Join(v1, "posse"), home); err != nil {
		t.Fatal(err)
	}
	if err := Retain(exe2, home); err != nil {
		t.Fatal(err)
	}
	// 推进时间超过 refGrace：v1 被回收；v2（own）、v3（current）与 stranger（无受管标记）都必须保留
	removed, err := collect(versions, home, []string{v2}, func([]string) map[string]bool { return nil }, time.Now().Add(2*refGrace))
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != v1 {
		t.Fatalf("removed = %v, want [%s]", removed, v1)
	}
	for _, keepDir := range []string{v2, v3, stranger} {
		if _, err := os.Stat(keepDir); err != nil {
			t.Fatalf("directory %s should not be deleted: %v", keepDir, err)
		}
	}
}

func TestServerCommitAndActivateAtomicityAndGuards(t *testing.T) {
	home := t.TempDir()
	posseBytes := []byte("#!/bin/sh\necho ok\n")
	libBytes := []byte("fake-dylib")
	makeStage := func(name string) string {
		s := filepath.Join(home, "server", "staging", name)
		if err := os.MkdirAll(s, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s, "posse"), posseBytes, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(s, "libghostty-vt.dylib"), libBytes, 0o644); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 并发提交相同 tag：均应成功且保持不可变
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			st := makeStage("s-" + string(rune('a'+idx)))
			_, err := CommitServerVersion(home, st, "0.1.54-abc123456789", "libghostty-vt.dylib", shaHex(posseBytes), shaHex(libBytes))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent CommitServerVersion failed: %v", err)
		}
	}

	// 若 server/current 是实体目录，Activate 必须报错而非在目录里创建符号链接
	curPath := filepath.Join(home, "server", "current")
	if err := os.MkdirAll(curPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ActivateServerVersion(home, "0.1.54-abc123456789"); err == nil {
		t.Fatal("ActivateServerVersion succeeded when current is a real directory")
	}
	if err := os.Remove(curPath); err != nil {
		t.Fatal(err)
	}

	// 正常激活与切换
	exe, err := ActivateServerVersion(home, "0.1.54-abc123456789")
	if err != nil {
		t.Fatalf("ActivateServerVersion failed: %v", err)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("activated exe missing: %v", err)
	}
	target, err := os.Readlink(curPath)
	if err != nil || target != filepath.Join("versions", "0.1.54-abc123456789") {
		t.Fatalf("current symlink = %q (%v)", target, err)
	}
}
