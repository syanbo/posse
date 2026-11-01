package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestChatFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "chat")
	now := time.Date(2026, 11, 1, 15, 30, 0, 0, time.Local)
	a, err := NewChatFolder(root, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewChatFolder(root, now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(a) != "1101-1530" || filepath.Base(b) != "1101-1530-2" {
		t.Fatalf("names: %s %s", a, b)
	}
	if !IsChatFolder(root, a) || !IsChatFolder(root, b) {
		t.Fatal("not recognized as chat folders")
	}
	own := filepath.Join(root, "notes")
	os.Mkdir(own, 0o755)
	if IsChatFolder(root, own) || IsChatFolder(root, root) || IsChatFolder(filepath.Dir(root), a) {
		t.Fatal("recognized a folder that is not a chat folder")
	}
	// 有文件的留着，空的删掉，用户自己建的空目录不碰
	os.WriteFile(filepath.Join(b, "out.md"), []byte("x"), 0o644)
	RemoveChatFolder(root, a)
	RemoveChatFolder(root, b)
	RemoveChatFolder(root, own)
	for dir, want := range map[string]bool{a: false, b: true, own: true} {
		if _, err := os.Stat(dir); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", dir, err == nil, want)
		}
	}
}

func TestChatDirSetting(t *testing.T) {
	home := t.TempDir()
	if ChatDir(home) != DefaultChatDir() {
		t.Fatalf("default: %s", ChatDir(home))
	}
	if err := SetChatDir(home, "/tmp/elsewhere"); err != nil {
		t.Fatal(err)
	}
	if ChatDir(home) != "/tmp/elsewhere" {
		t.Fatalf("custom: %s", ChatDir(home))
	}
	// 选回默认目录等于恢复默认，不记死路径
	SetChatDir(home, DefaultChatDir())
	if loadSettings(home).ChatDir != "" {
		t.Fatal("default dir stored explicitly")
	}
}
