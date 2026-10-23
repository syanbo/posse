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
	put("README.md", "# Hive\n\n多 agent 终端管理器。\n\n## 构建\n\n```sh\ngo build ./cmd/hive\n```\n\n- 常驻进程托管终端\n- 界面可以随时关闭\n\n## 入口\n\n| # | 入口 | 位置 | 写法 | 结果处理 |\n|---|---|---|---|---|\n| 1 | 私聊 拍照 | `IMChatBaseActivity:1506` | `openCamera(this, single().compress(true), REQUEST_CAMERA)` | 改为 `parse(data).getPath()`，裁剪优先，其次压缩图，最后原图 |\n| 2 | 私聊 相册 | `IMChatBaseActivity:1523` | `openAlbum(this, multiple(9).compress(true).allowGif(true).cameraInAlbum(false), CHOOSE_REQUEST)` | 循环取 `getPath()` |\n\n| 项目 | 数据 |\n|---|---|\n| 版本 | 1.4 |\n")
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
	// 编辑器有焦点时 ⇧⌘G 是它的"查找上一个"，不去开关改动页
	side := v.side
	tt.Key(ui.Cmd|ui.Shift, ui.KeyG)
	tt.Frame()
	if !a.filesPanelOpen() || v.side != side {
		t.Fatal("⇧⌘G in the editor should stay with the editor")
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
	// 改回原样（像保存或撤销之后那样没有未保存的改动）也还是常驻
	orig, _ := os.ReadFile(main)
	d.ed.Select(0, len(d.ed.Buffer.String()))
	d.ed.Insert(string(orig))
	if d.isModified() || d.isPreview() {
		t.Fatalf("modified=%v preview=%v after reverting the edit", d.isModified(), d.isPreview())
	}
	a.previewFile(w, readme)
	if v.doc(main) == nil {
		t.Fatal("a once-edited tab should not be replaced")
	}
}

// 浮层面板和分支弹窗互斥：点分支按钮收起技能与工具面板。
// 文件栏停靠在右边，不受分支弹窗影响。
func TestBranchPopupClosesPanel(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	w := &a.workspaces[0]
	w.Branch = "main"
	tt := ui.NewTester(a.build, 1200, 760)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")} // 面板会去查常驻进程，连不上就报错
	a.openFilesPanel(w, sideFiles)
	a.state.RightPanel = panelUsage
	tt.Frame()
	if err := tt.Click("分支 main"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.state.RightPanel != "" || a.branchPop == nil || !a.filesPanelOpen() {
		t.Fatalf("overlay=%q popup=%v files=%v", a.state.RightPanel, a.branchPop != nil, a.filesPanelOpen())
	}
}

// 旧版 ui.json 里文件栏记成浮层面板，读进来改成停靠的文件栏。
func TestFilesPanelStateMigrates(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(uiStatePath(home), []byte(`{"rightPanel":"files"}`), 0o600)
	if s := loadUIState(home); s.RightPanel != "" || !s.FilesPanel {
		t.Fatalf("%+v", s)
	}
	// 历史以前也是浮层面板，现在是文件栏的"历史"页
	os.WriteFile(uiStatePath(home), []byte(`{"rightPanel":"history"}`), 0o600)
	if s := loadUIState(home); s.RightPanel != "" || !s.FilesPanelHistory {
		t.Fatalf("%+v", s)
	}
}

// 文件栏顶上切"历史""文件"；⇧⌘E 这类打开文件栏某一页的操作回到"文件"。
func TestFilesPanelHistoryTab(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")} // 历史会去查会话，连不上就报错
	// 侧栏里也有"历史"，收起它，点到的才是文件栏的页签
	a.state.SidebarHidden = true
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFilesPanel(w, sideChanges)
	// 等文件栏滑进来
	for range 20 {
		tt.Frame()
		time.Sleep(20 * time.Millisecond)
	}
	if !tt.HasText("检索") {
		t.Fatal("files tabs missing")
	}
	if err := tt.Click("历史"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !a.state.FilesPanelHistory || tt.HasText("检索") {
		t.Fatalf("history tab: %v", a.state.FilesPanelHistory)
	}
	a.toggleFilesPanel(w, sideFiles)
	tt.Frame()
	if a.state.FilesPanelHistory || !a.filesPanelOpen() || a.filesFor(w).side != sideFiles {
		t.Fatal("⇧⌘E should show the files tab")
	}
}

// 点标签上的 × 连着关文件：关闭发生在画标签的遍历里，不能遍历到被清空的格子而崩溃。
func TestCloseDocTabsByButton(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "README.md"), 0, [2]int{})
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "opened", func() bool { return v.cur != nil && v.cur.kind == docText })
	// 不是当前的标签，指针先移上去 × 才出来，所以每个点到关掉为止
	for want := 1; want >= 0; want-- {
		for range 3 {
			if len(v.docs) == want {
				break
			}
			if err := tt.Click("关闭文件"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
		}
		if len(v.docs) != want {
			t.Fatalf("docs = %d, want %d", len(v.docs), want)
		}
	}
	if a.activeTerm[w.ID] != "t1" {
		t.Fatalf("should go back to the terminal, got %q", a.activeTerm[w.ID])
	}
}

// 菜单的 ⌘W 和快捷键一样：有对话框时不关后面的标签。
func TestMenuCloseTabBehindDialog(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "opened", func() bool { return v.cur != nil && v.cur.kind == docText })
	a.confirm("确认？", "", "好", func() {})
	tt.Frame()
	tt.Frame()
	a.menuCloseTab = true
	tt.Frame()
	if len(v.docs) != 1 {
		t.Fatal("⌘W from the menu closed a tab behind the dialog")
	}
	a.confirming = false
	tt.Frame()
	tt.Frame()
	a.menuCloseTab = true
	tt.Frame()
	if len(v.docs) != 0 {
		t.Fatal("⌘W from the menu should close the tab")
	}
}

// 文件栏的开关照 Orca 固定在右上角：打开后挪到文件栏顶栏的右端，位置不变。
func TestFilesPanelToggleStays(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	before, ok := tt.Find("文件栏 ⌘L")
	if !ok {
		t.Fatal("toggle missing")
	}
	a.openFilesPanel(w, sideFiles)
	tt.Frame()
	tt.Frame()
	if after, _ := tt.Find("文件栏 ⌘L"); after != before || countText(tt, "文件栏 ⌘L") > 1 {
		t.Fatalf("toggle moved: %v → %v", before, after)
	}
	if err := tt.Click("文件栏 ⌘L"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.filesPanelOpen() {
		t.Fatal("the toggle in the panel should close it")
	}
}

// 图片、技能与工具跟着会话走：按钮常驻在 agent 终端右上角，普通终端没有。
func TestSessionButtons(t *testing.T) {
	a := previewApp(t)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "hived.sock")}
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	r, ok := tt.Find("这个会话用到的技能与工具")
	if _, img := tt.Find("这个会话里生成和粘贴的图片"); !ok || !img {
		t.Fatal("buttons should show on a claude terminal")
	}
	if r.X < 900 || r.Y > 120 {
		t.Fatalf("buttons should sit at the terminal's top right: %v", r)
	}
	if err := tt.Click("这个会话用到的技能与工具"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.state.RightPanel != panelUsage {
		t.Fatal("click should open the panel")
	}
}

// 链接卡片的"在文件栏中显示"：打开文件树、展开上级目录、选中这一项，点树上别的行就不再选中它
func TestRevealInFiles(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	path := filepath.Join(root, "internal", "files", "tree.go")
	a.revealInFiles(w, path)
	v := a.filesFor(w)
	until(t, tt, "revealed", func() bool { return tt.HasText("tree.go") && !v.revealScroll })
	if !a.filesPanelOpen() || v.side != sideFiles || !v.tree.Expanded(filepath.Join(root, "internal", "files")) {
		t.Fatal("files panel not opened to the revealed path")
	}
	if v.reveal != path {
		t.Fatalf("reveal = %q", v.reveal)
	}
	if err := tt.Click("README.md"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.reveal != "" {
		t.Fatal("clicking another row should drop the reveal")
	}
}

// Markdown 默认打开成预览，跳到某一行时切回源码
func TestMarkdownOpensInPreview(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	readme := filepath.Join(root, "README.md")
	a.openFile(w, readme, 0, [2]int{})
	v := a.filesFor(w)
	if d := v.doc(readme); d == nil || !d.preview {
		t.Fatal("markdown should open in preview")
	}
	if d := v.doc(filepath.Join(root, "main.go")); d != nil {
		t.Fatal("unexpected doc")
	}
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	if d := v.doc(filepath.Join(root, "main.go")); d == nil || d.preview {
		t.Fatal("go file should not preview")
	}
	plan := filepath.Join(root, "docs", "plan.md")
	a.openFile(w, plan, 1, [2]int{})
	if d := v.doc(plan); d == nil || d.preview {
		t.Fatal("jumping to a line should show the source")
	}
}

// 文件栏和日志浮层按项目开关：在一个项目打开，切到别的项目是关着的，切回来还开着；
// 同一项目的别的工作区跟着开。旧版不分项目的开关记到当前项目上。
func TestPanelsFollowProject(t *testing.T) {
	a := previewApp(t)
	a.selected = "w1"
	a.openFilesPanel(a.workspace("w1"), sideFiles)
	a.setLogPanel(true)
	a.selected = "w2"
	if a.filesPanelOpen() || a.logPanelOpen() {
		t.Fatal("another project's panels should stay closed")
	}
	a.selected = "w1"
	if !a.filesPanelOpen() || !a.logPanelOpen() {
		t.Fatal("panels should still be open when switching back")
	}
	a.selected = "w6" // Skill 的 worktree
	a.openFilesPanel(a.workspace("w6"), sideFiles)
	a.selected = "w5"
	if !a.filesPanelOpen() {
		t.Fatal("same project's main workspace should share the files panel")
	}
	a.closeFilesPanel()
	a.selected = "w1"
	if !a.filesPanelOpen() {
		t.Fatal("closing in one project closed another")
	}

	b := previewApp(t)
	b.selected = "w2"
	b.state.FilesPanel = true
	b.state.FilesPanels = map[string]bool{"gone": true}
	b.migratePanels()
	if !b.filesPanelOpen() || b.state.FilesPanel || b.state.FilesPanels["gone"] {
		t.Fatalf("migrate %+v", b.state)
	}
}

// 切到开着文件栏的项目时直接是展开的，不再滑进来；在当前项目点开关照常播动画。
func TestPanelsSnapOnProjectSwitch(t *testing.T) {
	a := previewApp(t)
	a.selected = "w2"
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	a.openFilesPanel(a.workspace("w2"), sideFiles)
	tt.Frame()
	if a.filesPanelShown >= 1 {
		t.Fatalf("toggling should animate, shown %v on the first frame", a.filesPanelShown)
	}
	settle(tt)
	a.selected = "w4"
	tt.Frame()
	if a.filesPanelShown != 0 {
		t.Fatalf("switching to a closed project should hide at once, shown %v", a.filesPanelShown)
	}
	a.selected = "w2"
	tt.Frame()
	if a.filesPanelShown != 1 {
		t.Fatalf("switching back should show at once, shown %v", a.filesPanelShown)
	}
}

// 文件栏历史页的会话卡片：悬停时右下角有"移到废纸篓"。
func TestRecentTrashButton(t *testing.T) {
	a := previewApp(t)
	previewHistory(a, "")
	a.selected = "w1"
	a.state.FilesPanels = map[string]bool{"p1": true}
	a.state.FilesPanelHistory = true
	a.recent = recentView{key: "p1", loaded: true, poll: panelPoll{at: time.Now()}, sessions: a.history.sessions}
	tt := newPreviewTester(a, true)
	if tt.HasText("移到废纸篓") {
		t.Fatal("trash button shown without hover")
	}
	r, ok := tt.Find(oneLine(a.history.sessions[0].Title))
	if !ok {
		t.Fatal("no card")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Frame()
	tt.Frame()
	if _, ok := tt.Find("移到废纸篓"); !ok {
		t.Fatal("no trash button on hover")
	}
}

// 侧栏「手机」：有手机连着时行尾有绿点，悬停写是哪几台；没连着时没有。
func TestMobileOnlineMark(t *testing.T) {
	a := previewApp(t)
	tt := newPreviewTester(a, true)
	if tt.HasText("正连着") {
		t.Fatal("mark without phones")
	}
	a.daemon.MobileOnline = []string{"iPhone 12 Pro", "Pixel"}
	tt.Frame()
	if _, ok := tt.Find("iPhone 12 Pro、Pixel 正连着"); !ok || !tt.HasText("2") {
		t.Fatal("no online mark")
	}
}
