package app

import (
	"errors"
	"testing"
	"time"

	"posse/internal/media"
)

// 用预览打开图片不在界面线程上等 open 退出；打不开时照样把报错写进错误条。
func TestOpenInPreviewDoesNotBlock(t *testing.T) {
	release := make(chan error)
	got := make(chan string, 1)
	old := openPreview
	t.Cleanup(func() { openPreview = old })
	openPreview = func(path string) error {
		got <- path
		return <-release
	}
	a := &app{}
	done := make(chan struct{})
	go func() {
		a.openInPreview(imageItem{Item: media.Item{Path: "/tmp/x.png"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("openInPreview waited for open to exit")
	}
	if p := <-got; p != "/tmp/x.png" {
		t.Fatalf("opened %q", p)
	}
	release <- errors.New("LSOpenURLsWithRole() failed")
	for range 200 {
		a.runPending()
		if a.err != "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("error from open not shown")
}
