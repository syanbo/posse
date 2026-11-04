package app

import (
	"os"
	"path/filepath"
	"testing"
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
	a.confirming = false

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
