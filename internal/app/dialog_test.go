package app

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo"
)

// fakeOpenDialog 换掉系统的打开对话框：调用一次记一次，等 answer 给出选的文件才返回。
func fakeOpenDialog(t *testing.T) (calls *atomic.Int32, answer chan []string) {
	calls, answer = new(atomic.Int32), make(chan []string)
	old := openDialog
	openDialog = func(mygo.OpenDialogOptions) ([]string, error) {
		calls.Add(1)
		return <-answer, nil
	}
	t.Cleanup(func() { openDialog = old })
	return
}

// 选附件的对话框开着时：再点不开第二个；选完时已经换了讨论，选的文件不进新讨论的草稿。
func TestAttachDialogOnceAndStale(t *testing.T) {
	calls, answer := fakeOpenDialog(t)
	a := previewApp(t)
	a.refreshQueued.Store(true) // 不去连常驻进程
	a.disc.sel, a.disc.d = "d1", &discDetail{}
	tt := newPreviewTester(a, false)
	m := L(nil)

	a.pickAttachments(m)
	until(t, tt, "dialog shown", func() bool { return calls.Load() == 1 })
	a.pickAttachments(m)
	tt.Frame()
	a.selectDiscussion("d2")
	a.disc.d = &discDetail{}
	answer <- []string{"/tmp/a.png"}
	until(t, tt, "dialog closed", func() bool { return !a.dialogOpen })
	if calls.Load() != 1 {
		t.Fatalf("%d dialogs opened", calls.Load())
	}
	if len(a.disc.atts) != 0 {
		t.Fatalf("picked files went into another discussion: %v", a.disc.atts)
	}

	// 同一个讨论里选完照常放进草稿
	a.pickAttachments(m)
	until(t, tt, "dialog shown again", func() bool { return calls.Load() == 2 })
	answer <- []string{"/tmp/b.png"}
	until(t, tt, "attached", func() bool { return len(a.disc.atts) == 1 })
}

// 存储为：对话框开着时讨论被切走，回来不再存。
func TestSaveImageDialogStale(t *testing.T) {
	src := filepath.Join(t.TempDir(), "img.png")
	os.WriteFile(src, []byte("png"), 0o644)
	dst := filepath.Join(t.TempDir(), "out.png")
	answer := make(chan string)
	old := saveDialog
	saveDialog = func(mygo.SaveDialogOptions) (string, error) { return <-answer, nil }
	t.Cleanup(func() { saveDialog = old })
	a := previewApp(t)
	a.refreshQueued.Store(true)
	a.disc.sel, a.disc.d = "d1", &discDetail{}
	tt := newPreviewTester(a, false)

	a.saveImageAs(L(nil), src)
	a.selectDiscussion("d2")
	answer <- dst
	until(t, tt, "dialog closed", func() bool { return !a.dialogOpen })
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("saved after the discussion was left")
	}

	// 还在同一个讨论里：照常存
	a.disc.d = &discDetail{}
	a.saveImageAs(L(nil), src)
	answer <- dst
	until(t, tt, "saved", func() bool { _, err := os.Stat(dst); return err == nil })
}
