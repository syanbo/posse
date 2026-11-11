package app

import (
	"os"
	"path/filepath"
	"testing"

	"posse/internal/files"

	"posse/locales"

	"github.com/egoist/mygo/ui"
)

// updateFlowApp 是有一份没保存的文件、新版本已就绪的窗口，只画设置里的更新卡片和未保存对话框。
func updateFlowApp(t *testing.T) (*app, *ui.Tester, *int) {
	relaunches := new(int)
	old := relaunchNow
	relaunchNow = func(*app, locales.Messages) { *relaunches++ }
	t.Cleanup(func() { relaunchNow = old })
	a := previewApp(t)
	// 不建 git 仓库：文件视图在后台跑的 git 会和临时目录的清理撞上
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("# Posse\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := &fileDoc{path: path, started: true}
	c, err := files.ReadFile(path)
	a.applyContent(d, c, err)
	a.files = map[string]*filesView{"w1": {wsID: "w1", root: root, tree: files.NewTree(root), docs: []*fileDoc{d}, cur: d}}
	d.ed.Insert("<!-- draft -->\n")
	if !d.isModified() {
		t.Fatal("doc not modified")
	}
	a.upd.phase, a.upd.version = updateReady, "0.9.0"
	tt := newTester(func(c *ui.Context) {
		a.updateCard(c, "", a.upd.view())
		a.unsavedDialog(c)
	}, 900, 700)
	tt.Frame()
	return a, tt, relaunches
}

func click(t *testing.T, tt *ui.Tester, label string) {
	t.Helper()
	if err := tt.Click(label); err != nil {
		t.Fatalf("click %s: %v", label, err)
	}
	tt.Frame()
	tt.Frame()
}

// 换完包、重开前问未保存文件时点了取消：再点「重启更新」、存好文件后要接着重开，不能没反应。
func TestRestartAfterInstallCancelled(t *testing.T) {
	a, tt, relaunches := updateFlowApp(t)
	a.upd.installed = true // takeStaged 取走、installUpdate 换好包之后的状态
	a.relaunch(L(nil))
	tt.Frame()
	click(t, tt, "取消")
	if a.unsaved != nil || *relaunches != 0 {
		t.Fatal("cancel should stay")
	}
	click(t, tt, "重启更新")
	click(t, tt, "保存")
	if *relaunches != 1 {
		t.Fatalf("restart after the install relaunched %d times", *relaunches)
	}
}

// 未保存对话框里选「不保存」就往下走，不再问一遍。
func TestRestartDontSaveGoesOn(t *testing.T) {
	a, tt, _ := updateFlowApp(t)
	click(t, tt, "重启更新")
	if a.unsaved == nil {
		t.Fatal("no unsaved dialog")
	}
	click(t, tt, "不保存")
	if a.unsaved != nil {
		t.Fatal("asked again after choosing not to save")
	}
}
