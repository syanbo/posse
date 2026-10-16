package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/client"
)

const sampleGo = `package main

import (
	"fmt"
	"os"
)

// main 读配置，启动常驻进程，再打开窗口。
func main() {
	cfg, err := loadConfig(os.Getenv("HIVE_HOME"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "hive:", err)
		os.Exit(1)
	}
	run(cfg)
}

func loadConfig(home string) (*config, error) {
	if home == "" {
		home = defaultHome()
	}
	return &config{home: home}, nil
}
`

// filesWorkspace 造一个 git 仓库作为 previewApp 里 w1（以及 w2）的工作区目录，返回它的路径。
func filesWorkspace(t *testing.T, a *app) string {
	t.Helper()
	root := t.TempDir()
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("main.go", sampleGo)
	put("README.md", "# Hive\n\n多 agent 终端管理器。\n\n## 构建\n\n```sh\ngo build ./cmd/hive\n```\n\n- 常驻进程托管终端\n- 界面可以随时关闭\n")
	put("internal/files/tree.go", "package files\n\n// Tree 是工作区目录的树。\ntype Tree struct{}\n")
	put("internal/files/search.go", "package files\n\nfunc Search() {}\n")
	put("internal/app/view.go", "package app\n\nfunc (a *app) main() {}\n")
	put("docs/plan.md", "# 计划\n")
	put(".gitignore", "build/\n")
	put("build/hive.bin", "\x00\x01")
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	a.workspaces[0].Path = root
	a.workspaces[1].Path = root
	a.indexWorkspaces()
	return root
}

// until 一帧一帧地画，直到 cond 成立；后台读文件要几帧才回来。
func until(t *testing.T, tt *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tt.Frame()
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (v *filesView) doc(path string) *fileDoc {
	for _, d := range v.docs {
		if d.path == path {
			return d
		}
	}
	return nil
}

func TestFilesTabOpenEditSave(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.showFiles(w)
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyE)
	v := a.filesFor(w)
	if !a.filesPanelOpen() || v.side != sideFiles {
		t.Fatal("⇧⌘E should open the files panel on the tree")
	}
	until(t, tt, "tree", func() bool { return v.tree.Loaded(root) && tt.HasText("main.go") })
	// 选中文件标签：不被换回第一个终端，终端断开接入
	if a.activeTerm[w.ID] != filesTabID || a.view != nil {
		t.Fatalf("activeTerm=%q view=%v", a.activeTerm[w.ID], a.view)
	}
	// build/ 被 .gitignore 忽略，照样列出（变暗）
	until(t, tt, "list", func() bool { return v.list != nil })
	if !tt.HasText("build") || !v.list.Ignored(filepath.Join(root, "build"), true) {
		t.Fatal("ignored dir should be listed and marked ignored")
	}

	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "main.go")
	until(t, tt, "doc", func() bool { d := v.doc(path); return d != nil && d.kind == docText })
	d := v.doc(path)
	if d.ed.Buffer.String() != sampleGo || d.isModified() {
		t.Fatal("doc content or modified state wrong after open")
	}
	// 文件栏浮在编辑区上，后面要点编辑区右边的横幅按钮，先收起来
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	if a.filesPanelOpen() {
		t.Fatal("⌘L should close the files panel")
	}

	// 编辑后 ⌘S 保存到磁盘，未保存标记消失
	d.ed.Select(0, 0)
	d.ed.Insert("// edited\n")
	tt.Frame()
	if !d.isModified() || a.dirtyCount.Load() != 1 {
		t.Fatalf("modified=%v dirty=%d", d.isModified(), a.dirtyCount.Load())
	}
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if b, _ := os.ReadFile(path); string(b) != "// edited\n"+sampleGo {
		t.Fatalf("saved %q", b)
	}
	if d.isModified() {
		t.Fatal("still modified after save")
	}
	// 撤销回保存前的内容算有改动，再撤销回来又不算
	d.ed.Buffer.Undo()
	if !d.isModified() {
		t.Fatal("undo past save should be modified")
	}
	d.ed.Buffer.Redo()
	if d.isModified() {
		t.Fatal("redo back to saved text should not be modified")
	}

	// agent 写临时文件再改名：没有未保存的改动时自动重载
	replace := func(text string) {
		tmp := path + ".tmp"
		os.WriteFile(tmp, []byte(text), 0o644)
		if err := os.Rename(tmp, path); err != nil {
			t.Fatal(err)
		}
		v.poll.at = time.Time{} // 不等一秒
	}
	replace("package main\n\nfunc main() {}\n")
	until(t, tt, "reload", func() bool { return d.ed.Buffer.String() == "package main\n\nfunc main() {}\n" })
	if d.isModified() || d.conflict {
		t.Fatal("reload should leave the doc clean")
	}

	// 有未保存的改动时不重载，标出冲突；保存前先问
	d.ed.Insert("x")
	replace("package main // agent\n")
	until(t, tt, "conflict", func() bool { return d.conflict && tt.HasText("保留我的") })
	tt.Key(ui.Cmd, ui.KeyS)
	tt.Frame()
	if !a.confirming {
		t.Fatal("saving over an outside change should ask first")
	}
	a.confirming = false
	tt.Frame()
	// 用磁盘上的：放弃自己的改动
	if err := tt.Click("用磁盘上的"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "take disk", func() bool { return !d.conflict && d.ed.Buffer.String() == "package main // agent\n" })

	// 文件被删掉：标出已删除
	os.Remove(path)
	v.poll.at = time.Time{}
	until(t, tt, "deleted", func() bool { return d.deleted && tt.HasText("文件已经被删除。保存会重新创建它。") })

	// ⌘W 关闭有改动的文件先问；不保存就关掉
	d.ed.Insert("y")
	tt.Key(ui.Cmd, ui.KeyW)
	tt.Frame()
	if a.unsaved == nil {
		t.Fatal("closing a modified doc should ask")
	}
	// 关掉最后一个文件会切回终端并接入它：给一个连不上的 socket，接入失败而不是空指针
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	if err := tt.Click("不保存"); err != nil {
		t.Fatal(err)
	}
	if v.doc(path) != nil {
		t.Fatal("doc still open")
	}
	// 关掉最后一个文件就回到之前的终端
	if len(v.docs) != 0 {
		t.Fatalf("docs left open: %d", len(v.docs))
	}
	if a.activeTerm[w.ID] != "t1" {
		t.Fatalf("activeTerm = %q, want back to t1", a.activeTerm[w.ID])
	}
}

// 没有终端的工作区也能开文件栏、打开文件，而不是一直停在空工作区的页面。
func TestFilesTabWithoutTerminals(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	a.selected = "w2"
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	if !tt.HasText("切换终端") || tt.HasText("文件") && a.filesActive(a.workspace("w2")) {
		t.Fatal("expected the empty workspace page first")
	}
	tt.Key(ui.Cmd, ui.KeyL)
	until(t, tt, "tree", func() bool { return a.filesPanelOpen() && tt.HasText("main.go") })
	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	w := a.workspace("w2")
	until(t, tt, "opened", func() bool { return a.filesActive(w) && !tt.HasText("切换终端") })
	// 没有终端可回：关掉文件后留在文件标签
	tt.Key(ui.Cmd, ui.KeyW)
	until(t, tt, "empty files", func() bool { return tt.HasText("在右边的文件栏里选一个文件") })
	if !a.filesActive(w) {
		t.Fatal("should stay on files without terminals")
	}
	tt.Key(ui.Cmd, ui.KeyL)
	tt.Frame()
	if a.filesPanelOpen() {
		t.Fatal("⌘L should close the files panel")
	}
}

func TestQuickOpenAndSearch(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	tt.Key(ui.Cmd, ui.KeyP)
	tt.Frame()
	if !a.quick.open || a.filesActive(w) {
		t.Fatal("⌘P should open quick open without leaving the terminal")
	}
	v := a.files[w.ID]
	until(t, tt, "list", func() bool { return v.list != nil && len(a.quick.matches) > 0 })
	tt.Type("tree:3")
	until(t, tt, "match", func() bool {
		return len(a.quick.matches) > 0 && a.quick.matches[0].Path == filepath.Join("internal", "files", "tree.go")
	})
	tt.Key(0, ui.KeyEnter)
	path := filepath.Join(root, "internal", "files", "tree.go")
	until(t, tt, "opened at line", func() bool {
		d := v.doc(path)
		return d != nil && d.kind == docText && d.gotoLine == 0 && d.ed.CursorPos().Line == 2
	})
	// 打开的文件在树上展开到它
	if !v.tree.Expanded(filepath.Join(root, "internal", "files")) {
		t.Fatal("tree not revealed to the opened file")
	}

	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	tt.Frame()
	if v.side != sideSearch || !a.filesPanelOpen() {
		t.Fatal("⇧⌘F should open the files panel on search")
	}
	tt.Frame()
	tt.Type("func")
	until(t, tt, "search", func() bool { return !v.search.running && len(v.search.hits) == 4 })
	if !tt.HasText("3 个文件里有 4 行") {
		t.Fatalf("status: %v", tt.Texts())
	}
	// 点命中跳到那一行并选中命中的文字
	if err := tt.Click("func Search() {}"); err != nil {
		t.Fatal(err)
	}
	sp := filepath.Join(root, "internal", "files", "search.go")
	until(t, tt, "jump", func() bool {
		d := v.doc(sp)
		return d != nil && d.kind == docText && d.gotoLine == 0 && d.ed.SelectedText() == "func"
	})
}

// 打开的文件和展开的目录记进 ui.json，交给写文件的协程的是深拷贝。
func TestFilesStateSaved(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.home = t.TempDir()
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	a.openFile(w, filepath.Join(root, "docs", "plan.md"), 0, [2]int{})
	until(t, tt, "doc", func() bool { return a.files[w.ID].cur.kind == docText })
	st := a.state.Files[w.ID]
	if len(st.Open) != 1 || st.Current != filepath.Join(root, "docs", "plan.md") || len(st.Expanded) != 1 {
		t.Fatalf("saved state %+v", st)
	}
	// 改原来的不影响已经交出去的拷贝（go test -race 查得出共享）
	a.saveState()
	a.state.Files[w.ID].Open[0] = "changed"
	time.Sleep(50 * time.Millisecond)
	saved := loadUIState(a.home)
	if got := saved.Files[w.ID].Open[0]; got == "changed" {
		t.Fatal("ui.json got the later change")
	}
	// 重新打开窗口：从 ui.json 恢复
	b := previewApp(t)
	b.workspaces = a.workspaces
	b.indexWorkspaces()
	b.state = saved
	bv := b.filesFor(&b.workspaces[0])
	if len(bv.docs) != 1 || bv.cur == nil || !bv.tree.Expanded(filepath.Join(root, "docs")) {
		t.Fatalf("restored %d docs, cur=%v", len(bv.docs), bv.cur)
	}
	if !strings.HasSuffix(bv.cur.path, "plan.md") {
		t.Fatal(bv.cur.path)
	}
}

// 打开的文件是标签栏里的标签：在终端上点它切到那个文件，再点终端标签切回去。
func TestDocTabsInTabBar(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "README.md"), 0, [2]int{})
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "opened", func() bool { return v.cur != nil && v.cur.kind == docText && tt.HasText("README.md") })
	a.activeTerm[w.ID] = "t3"
	tt.Frame()
	if a.filesActive(w) {
		t.Fatal("should be on the terminal")
	}
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.filesActive(w) || v.cur != v.doc(filepath.Join(root, "README.md")) {
		t.Fatalf("clicking the doc tab should show it: active=%v cur=%v", a.filesActive(w), v.cur.path)
	}
}

// 预览标签（照 Orca）：树里单击打开的文件被下一次单击换掉；双击、跳转或改了内容就常驻。
func TestPreviewTabs(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFilesPanel(w, sideFiles)
	until(t, tt, "tree", func() bool { return tt.HasText("main.go") })
	if err := tt.Click("main.go"); err != nil {
		t.Fatal(err)
	}
	v := a.files[w.ID]
	main, readme := filepath.Join(root, "main.go"), filepath.Join(root, "README.md")
	if len(v.docs) != 1 || !v.docs[0].isPreview() {
		t.Fatalf("single click should open a preview tab: %d docs", len(v.docs))
	}
	a.previewFile(w, readme)
	if len(v.docs) != 1 || v.docs[0].path != readme {
		t.Fatal("the next single click should replace the preview tab")
	}
	// 再次正常打开（双击）就常驻，之后的单击另开一个预览标签
	a.openFile(w, readme, 0, [2]int{})
	a.previewFile(w, main)
	if len(v.docs) != 2 || v.doc(readme).isPreview() || !v.doc(main).isPreview() {
		t.Fatal("double click should pin the tab")
	}
	// 改了内容的预览标签不会被换掉
	d := v.doc(main)
	until(t, tt, "loaded", func() bool { return d.kind == docText })
	d.ed.Insert("x")
	a.previewFile(w, filepath.Join(root, "docs", "plan.md"))
	if len(v.docs) != 3 || v.doc(main) == nil {
		t.Fatalf("an edited preview tab should stay: %d docs", len(v.docs))
	}
}
