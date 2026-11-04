package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

// 从访达把文件夹拖到侧栏：新文件夹先问要不要加成项目，已经是项目的直接切过去，拖来的文件报错。
func TestProjectDrop(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	dir := t.TempDir()
	fresh := filepath.Join(dir, "fresh")
	known := filepath.Join(dir, "known")
	file := filepath.Join(dir, "a.txt")
	os.Mkdir(fresh, 0o755)
	os.Mkdir(known, 0o755)
	os.WriteFile(file, nil, 0o644)
	a.projects[3].Path = known // Server，主工作区 w4

	// 侧栏项目列表里的一点
	const x, y = 120, 560
	if !tt.DropFiles(x, y, fresh) {
		t.Fatal("sidebar did not take the folder")
	}
	tt.Frame()
	if !a.confirming || a.confirmTitle != "把 "+shortPath(fresh)+" 加入项目？" || !tt.HasText("加入") {
		t.Fatalf("confirm %v %q", a.confirming, a.confirmTitle)
	}
	// 点遮罩不关，Esc 和取消关
	settle(tt)
	tt.ClickAt(20, 20)
	settle(tt)
	if !a.confirming {
		t.Fatal("clicking the backdrop closed the confirmation")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.confirming {
		t.Fatal("Escape did not close the confirmation")
	}
	a.confirm("t", "", "v", func() {})
	tt.Frame()
	if err := tt.Click("取消"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.confirming {
		t.Fatal("Cancel did not close the confirmation")
	}

	// 已经是项目的、不存在的不算进去
	tt.DropFiles(x, y, fresh, filepath.Join(dir, "gone"), known)
	tt.Frame()
	if !a.confirming || a.confirmTitle != "把 "+shortPath(fresh)+" 加入项目？" {
		t.Fatalf("only the new folder should be asked: %q", a.confirmTitle)
	}
	a.confirming = false
	tt.DropFiles(x, y, fresh, dir)
	tt.Frame()
	if !a.confirming || a.confirmTitle != "把 2 个文件夹加入项目？" {
		t.Fatalf("two folders: %q", a.confirmTitle)
	}
	a.confirming = false

	a.selected = "w1"
	tt.DropFiles(x, y, known)
	tt.Frame()
	if a.confirming || a.selected != "w4" {
		t.Fatalf("known project: confirming %v selected %q", a.confirming, a.selected)
	}

	tt.DropFiles(x, y, file)
	tt.Frame()
	if a.confirming || a.err != "只有文件夹能加为项目" {
		t.Fatalf("file: confirming %v err %q", a.confirming, a.err)
	}
}

// 确认框的键盘操作：打开时选中取消，回车取消；→ 换到确认、回车执行；← 换回取消；Esc 关。
func TestConfirmKeys(t *testing.T) {
	a := previewApp(t)
	offlineClient(t, a)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	done := make(chan struct{}, 1)
	open := func() {
		a.confirm("t", "", "执行", func() { done <- struct{}{} })
		tt.Frame()
		tt.Frame()
	}
	key := func(k ui.Key) {
		tt.Key(0, k)
		tt.Frame()
		tt.Frame()
	}
	ran := func() bool {
		select {
		case <-done:
			return true
		case <-time.After(200 * time.Millisecond):
			return false
		}
	}

	open()
	key(ui.KeyEnter)
	if a.confirming || ran() {
		t.Fatal("Enter on the default button should cancel")
	}
	open()
	key(ui.KeyRight)
	key(ui.KeyEnter)
	if a.confirming || !ran() {
		t.Fatal("→ then Enter should confirm")
	}
	open()
	key(ui.KeyRight)
	key(ui.KeyLeft)
	key(ui.KeyEnter)
	if a.confirming || ran() {
		t.Fatal("→ ← then Enter should cancel")
	}
	open()
	key(ui.KeyTab)
	key(ui.KeySpace)
	if a.confirming || !ran() {
		t.Fatal("Tab then Space should confirm")
	}
	open()
	key(ui.KeyEscape)
	if a.confirming || ran() {
		t.Fatal("Escape should close without confirming")
	}
}
