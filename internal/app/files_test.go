package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/locales"
)

const sampleGo = `package main

import (
	"fmt"
	"os"
)

// main 读配置，启动常驻进程，再打开窗口。
func main() {
	cfg, err := loadConfig(os.Getenv("POSSE_HOME"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "posse:", err)
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
	put("README.md", "# Posse\n\n多 agent 终端管理器。\n\n## 构建\n\n```sh\ngo build ./cmd/posse\n```\n\n- 常驻进程托管终端\n- 界面可以随时关闭\n\n## 入口\n\n| # | 入口 | 位置 | 写法 | 结果处理 |\n|---|---|---|---|---|\n| 1 | 私聊 拍照 | `IMChatBaseActivity:1506` | `openCamera(this, single().compress(true), REQUEST_CAMERA)` | 改为 `parse(data).getPath()`，裁剪优先，其次压缩图，最后原图 |\n| 2 | 私聊 相册 | `IMChatBaseActivity:1523` | `openAlbum(this, multiple(9).compress(true).allowGif(true).cameraInAlbum(false), CHOOSE_REQUEST)` | 循环取 `getPath()` |\n\n| 项目 | 数据 |\n|---|---|\n| 版本 | 1.4 |\n")
	put("internal/files/tree.go", "package files\n\n// Tree 是工作区目录的树。\ntype Tree struct{}\n")
	put("internal/files/search.go", "package files\n\nfunc Search() {}\n")
	put("internal/app/view.go", "package app\n\nfunc (a *app) main() {}\n")
	put("docs/plan.md", "# 计划\n")
	put(".gitignore", "build/\n")
	put("build/posse.bin", "\x00\x01")
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
		pass(tt, 5*time.Millisecond)
	}
}

// pass 让真实时间和 Tester 的时钟一起走 d：动画和 c.After 只认 Tester 的时钟，后台协程按真实时间。
func pass(tt *ui.Tester, d time.Duration) {
	time.Sleep(d)
	tt.Advance(d)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyE)
	v := a.filesFor(w)
	if !a.filesPanelOpen() || v.side != sideFiles {
		t.Fatal("⇧⌘E should open the files panel on the tree")
	}
	until(t, tt, "tree", func() bool { return v.tree.Loaded(root) && tt.HasText("main.go") && a.filesPanelShown == 1 })
	// 选中文件标签：不被换回第一个终端，终端断开接入
	if a.activeTerm[w.ID] != filesTabID || a.panes[0] != nil {
		t.Fatalf("activeTerm=%q view=%v", a.activeTerm[w.ID], a.panes[0])
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
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	if !tt.HasText("切换终端") || tt.HasText("文件") && a.filesActive(a.workspace("w2")) {
		t.Fatal("expected the empty workspace page first")
	}
	tt.Key(ui.Cmd, ui.KeyL)
	until(t, tt, "tree", func() bool { return a.filesPanelOpen() && tt.HasText("main.go") && a.filesPanelShown == 1 })
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
	tt := newTester(a.build, 1200, 760)
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
	if v.side != sideFiles || !a.filesPanelOpen() {
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

	// 检索和文件树在同一页：有字时树让给结果，点 × 清空回到树，结果跟着清掉
	tt.Frame()
	if tt.HasText("README.md") || !tt.HasText("Aa") {
		t.Fatalf("searching should replace the tree: %v", tt.Texts())
	}
	if err := tt.Click("清空"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.search.active() || len(v.search.hits) != 0 || !tt.HasText("README.md") || tt.HasText("Aa") {
		t.Fatalf("clear should return to the tree: %q %v", v.search.query.Text, tt.Texts())
	}
	// ⇧⌘E 在检索时先回到树，不关文件栏
	v.search.query.Text = "func"
	a.startSearch(v, 0)
	tt.Key(ui.Cmd|ui.Shift, ui.KeyE)
	tt.Frame()
	if v.search.active() || !a.filesPanelOpen() {
		t.Fatal("⇧⌘E while searching should show the tree")
	}
}

// 打开的文件和展开的目录记进 ui.json，交给写文件的协程的是深拷贝。
func TestFilesStateSaved(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.home = t.TempDir()
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
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
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
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

// Markdown 预览里的 [[名字]] 是链接：点了打开同一目录下的 名字.md（工作区外的文件也行）。
func TestWikiLinkOpens(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	w := &a.workspaces[0]
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "status.md"), []byte("---\nname: status\n---\n\n偏好见 [[ui-taste]]。\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "ui-taste.md"), []byte("# 偏好\n"), 0o644)
	tt := newTester(a.build, 1200, 760)
	a.openFile(w, filepath.Join(dir, "status.md"), 0, [2]int{})
	v := a.files[w.ID]
	until(t, tt, "preview", func() bool { return v.cur != nil && v.cur.mdPreview() && tt.HasText("ui-taste") })
	if err := tt.Click("ui-taste"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "opened", func() bool { return v.cur != nil && v.cur.path == filepath.Join(dir, "ui-taste.md") })
}

// 预览标签（照 Orca）：树里单击打开的文件被下一次单击换掉；双击、跳转或改了内容就常驻。
func TestPreviewTabs(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFilesPanel(w, sideFiles)
	until(t, tt, "tree", func() bool { return tt.HasText("main.go") && a.filesPanelShown == 1 })
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
	tt := newTester(a.build, 1200, 760)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")} // 面板会去查常驻进程，连不上就报错
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
	if s := loadUIState(home); s.RightPanel != "" || s.FilesPanelTab != tabHistory {
		t.Fatalf("%+v", s)
	}
}

// 文件栏顶上切"历史""文件"；⇧⌘E 这类打开文件栏某一页的操作回到"文件"。
func TestFilesPanelHistoryTab(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")} // 历史会去查会话，连不上就报错
	// 侧栏里也有"历史"，收起它，点到的才是文件栏的页签
	a.state.SidebarHidden = true
	tt := newTester(a.build, 1200, 760)
	a.openFilesPanel(w, sideChanges)
	// 等文件栏滑进来
	for range 20 {
		tt.Frame()
		pass(tt, 20*time.Millisecond)
	}
	if !tt.HasText("改动") {
		t.Fatal("files tabs missing")
	}
	if err := tt.Click("历史"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if a.state.FilesPanelTab != tabHistory || tt.HasText("改动") {
		t.Fatalf("history tab: %q", a.state.FilesPanelTab)
	}
	a.toggleFilesPanel(w, sideFiles)
	tt.Frame()
	if a.state.FilesPanelTab != "" || !a.filesPanelOpen() || a.filesFor(w).side != sideFiles {
		t.Fatal("⇧⌘E should show the files tab")
	}
}

// 点标签上的 × 连着关文件：关闭发生在画标签的遍历里，不能遍历到被清空的格子而崩溃。
func TestCloseDocTabsByButton(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
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
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
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
	a.c = &client.Client{Socket: filepath.Join(t.TempDir(), "possed.sock")}
	tt := newTester(a.build, 1200, 760)
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
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	path := filepath.Join(root, "internal", "files", "tree.go")
	a.revealInFiles(w, path)
	v := a.filesFor(w)
	until(t, tt, "revealed", func() bool { return tt.HasText("tree.go") && !v.revealScroll && a.filesPanelShown == 1 })
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

// 文件栏顶栏的"全部折叠"收起所有目录，⌥F1 再把当前文件定位出来
func TestCollapseAndRevealCurrent(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	path := filepath.Join(root, "internal", "files", "tree.go")
	a.openFile(w, path, 0, [2]int{})
	a.openFilesPanel(w, sideFiles)
	v := a.filesFor(w)
	dir := filepath.Join(root, "internal", "files")
	until(t, tt, "opened", func() bool { return v.tree.Expanded(dir) && tt.HasText("tree.go") && a.filesPanelShown == 1 })
	if err := tt.Click("全部折叠"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.tree.Expanded(dir) || v.tree.Expanded(filepath.Join(root, "internal")) || slices.Contains(a.state.Files[w.ID].Expanded, dir) {
		t.Fatal("collapse all left directories open")
	}
	tt.Key(ui.Alt, ui.KeyF1)
	until(t, tt, "revealed", func() bool { return v.tree.Expanded(dir) && !v.revealScroll && tt.HasText("tree.go") })
	if v.reveal != path {
		t.Fatalf("reveal = %q", v.reveal)
	}

	// 文件顶栏的准星同样定位
	v.tree.CollapseAll()
	tt.Frame()
	if err := tt.Click("在文件树中定位 " + a.keyText("revealFile")); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "revealed from doc bar", func() bool { return v.tree.Expanded(dir) })

	// Markdown 的预览、源码切换是图标按钮
	readme := filepath.Join(root, "README.md")
	a.openFile(w, readme, 0, [2]int{})
	tt.Frame()
	if err := tt.Click("查看源码"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.doc(readme).preview {
		t.Fatal("source button left the preview on")
	}
	if err := tt.Click("预览"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !v.doc(readme).preview {
		t.Fatal("preview button did not switch back")
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
	tt := newTester(a.build, 1200, 760)
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
	a.state.FilesPanelTab = tabHistory
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

// 跳到定义：⌘点击 `Helper.open` 跳到 Helper.java 里的声明并选中名字，⌃- 回到出发的位置；
// 几处同名的声明弹出列表，回车跳到选中的那处
func TestGoToDefinition(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	put := func(rel, text string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("app/Chat.java", "class Chat {\n  void init() {\n    Helper.open(this, 3);\n  }\n}\n")
	put("lib/Helper.java", "public class Helper {\n  public static void open(Object a, int code) {\n  }\n}\n")
	w := &a.workspaces[0]
	a.selected = w.ID
	tt := newTester(a.build, 1200, 760)
	chat := filepath.Join(root, "app", "Chat.java")
	a.openFile(w, chat, 0, [2]int{})
	v := a.filesFor(w)
	until(t, tt, "chat loaded", func() bool { return v.cur.kind == docText })
	d := v.cur
	off := strings.Index(d.ed.Buffer.String(), "open(") + 1
	a.goToDefinition(locales.In("zh-Hans"), w, v, d, off)
	helper := filepath.Join(root, "lib", "Helper.java")
	until(t, tt, "jump", func() bool { return v.cur.path == helper && v.cur.kind == docText && v.cur.ed.SelectedText() == "open" })
	if v.cur.ed.CursorPos().Line != 1 {
		t.Fatalf("line = %d", v.cur.ed.CursorPos().Line)
	}
	a.jumpBack(w)
	until(t, tt, "back", func() bool { return v.cur == d && d.ed.Cursor() == off })
	if len(v.jumps) != 0 || len(v.forward) != 1 {
		t.Fatalf("jumps = %d forward = %d", len(v.jumps), len(v.forward))
	}
	a.jumpForward(w)
	until(t, tt, "forward", func() bool { return v.cur.path == helper && v.cur.ed.CursorPos().Line == 1 })
	a.jumpBack(w)
	until(t, tt, "back again", func() bool { return v.cur == d })

	// ⌘P 这类打开也记历史：从 Chat.java 打开 Helper.java，⌃- 回到 Chat.java
	a.openFile(w, helper, 0, [2]int{})
	until(t, tt, "opened", func() bool { return v.cur.path == helper })
	a.jumpBack(w)
	until(t, tt, "back from open", func() bool { return v.cur == d })

	// 变量的类型推断：p 是 Helper 的实例，p.open 去 Helper.java 找
	put("lib/Use.java", "class Use {\n  void f() {\n    Helper p = new Helper();\n    p.open(this, 1);\n  }\n}\n")
	a.refreshList(v, true, nil)
	until(t, tt, "list", func() bool { return !v.listing })
	use := filepath.Join(root, "lib", "Use.java")
	a.openFile(w, use, 0, [2]int{})
	until(t, tt, "use loaded", func() bool { return v.cur.path == use && v.cur.kind == docText })
	ud := v.cur
	a.goToDefinition(locales.In("zh-Hans"), w, v, ud, strings.Index(ud.ed.Buffer.String(), "p.open(")+3)
	until(t, tt, "typed receiver", func() bool { return v.cur.path == helper && v.cur.ed.SelectedText() == "open" })

	// 光标在 Other.java 自己的 open 上、别处还有两处同分的声明：排除自己，弹出列表，按下键再回车跳到第二处
	put("lib/Other.java", "class Other {\n  void open() {}\n}\n")
	put("lib/Another.java", "class Another {\n  void open() {}\n}\n")
	a.refreshList(v, true, nil)
	until(t, tt, "list", func() bool { return !v.listing })
	other := filepath.Join(root, "lib", "Other.java")
	a.openFile(w, other, 0, [2]int{})
	until(t, tt, "other loaded", func() bool { return v.cur.path == other && v.cur.kind == docText })
	od := v.cur
	a.goToDefinition(locales.In("zh-Hans"), w, v, od, strings.Index(od.ed.Buffer.String(), "open(")+1)
	until(t, tt, "pick", func() bool { return a.defPick.open })
	if n := len(a.defPick.defs); n != 2 || a.defPick.defs[0].Path != filepath.Join("lib", "Another.java") {
		t.Fatalf("defs = %+v", a.defPick.defs)
	}
	if !tt.HasText("有 2 处定义") {
		t.Fatal("dialog not shown")
	}
	tt.Key(0, ui.KeyDown)
	tt.Frame()
	tt.Key(0, ui.KeyEnter)
	until(t, tt, "jump to helper", func() bool { return !a.defPick.open && v.cur.path == helper && v.cur.ed.SelectedText() == "open" })

	// 光标就在定义上、别处没有同名的：退化为在检索页按整词找用法
	v.cur = d
	a.goToDefinition(locales.In("zh-Hans"), w, v, d, strings.Index(d.ed.Buffer.String(), "init(")+1)
	until(t, tt, "usages", func() bool { return v.side == sideFiles && v.search.query.Regex })
	if v.search.query.Text != `\binit\b` || !a.filesPanelOpen() || v.cur != d {
		t.Fatalf("query = %+v", v.search.query)
	}

	// ⌘点在 import 的路径上：按 import 规则解析后打开那个文件；点 import 进来的名字优先去那个文件里找
	put("web/app.js", "import { helper } from './lib/util';\nhelper();\n")
	put("web/lib/util.js", "export const helper = 1;\n")
	put("web/other.js", "const helper = 2;\n")
	a.refreshList(v, true, nil)
	until(t, tt, "list", func() bool { return !v.listing })
	appJS := filepath.Join(root, "web", "app.js")
	a.openFile(w, appJS, 0, [2]int{})
	until(t, tt, "app.js", func() bool { return v.cur.path == appJS && v.cur.kind == docText })
	js := v.cur
	a.goToDefinition(locales.In("zh-Hans"), w, v, js, strings.Index(js.ed.Buffer.String(), "lib/util")+1)
	util := filepath.Join(root, "web", "lib", "util.js")
	until(t, tt, "import path", func() bool { return v.cur.path == util })
	v.cur = js
	a.goToDefinition(locales.In("zh-Hans"), w, v, js, strings.Index(js.ed.Buffer.String(), "helper")+1)
	until(t, tt, "imported name", func() bool { return v.cur.path == util && v.cur.ed.SelectedText() == "helper" })

	// 找不到：提示一句，不跳
	v.cur = d
	a.goToDefinition(locales.In("zh-Hans"), w, v, d, strings.Index(d.ed.Buffer.String(), "this")+1)
	until(t, tt, "no definition", func() bool { return d.hint != "" })
	if d.hint != "没找到 this 的定义" || v.cur != d {
		t.Fatalf("hint = %q", d.hint)
	}
}

// 设置里改成所有项目共用后，切项目时文件栏开关不变；改回按项目时当前项目接着开着。
func TestFilesPanelShared(t *testing.T) {
	a := previewApp(t)
	a.selected = "w2"
	a.openFilesPanel(a.workspace("w2"), sideFiles)
	a.setFilesPanelShared(true)
	if !a.filesPanelOpen() {
		t.Fatal("switching to shared should keep the panel open")
	}
	a.selected = "w4"
	if !a.filesPanelOpen() {
		t.Fatal("shared panel should stay open on another project")
	}
	a.closeFilesPanel()
	a.selected = "w2"
	if a.filesPanelOpen() {
		t.Fatal("closing in one project should close it everywhere")
	}
	a.openFilesPanel(a.workspace("w2"), sideFiles)
	a.setFilesPanelShared(false)
	if !a.filesPanelOpen() || !a.state.FilesPanels[a.panelProject()] {
		t.Fatal("back to per project should keep the current project open")
	}
	a.selected = "w4"
	if a.filesPanelOpen() {
		t.Fatal("per project: another project's panel should follow its own state")
	}
}

// 目录不在了的工作区：侧栏写明，空工作区页换成重新指定目录、移除，文件栏不再去读。
func TestMissingWorkspace(t *testing.T) {
	a := previewApp(t)
	i := slices.IndexFunc(a.workspaces, func(w workspace) bool { return w.ID == "w9" })
	a.workspaces[i].Missing, a.workspaces[i].Branch = true, ""
	a.indexWorkspaces()
	a.selected = "w9"
	a.state.FilesPanels = map[string]bool{"p5": true}
	tt := newPreviewTester(a, true)
	for _, want := range []string{"目录不存在", "目录已经不在了", "重新指定目录…", "从 Posse 移除…", "工作区的目录已经不在了"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q", want)
		}
	}
	if tt.HasText("切换终端") {
		t.Error("launch buttons still shown")
	}
	if v := a.files["w9"]; v != nil && (v.listing || v.list != nil) {
		t.Error("listed files of a missing directory")
	}
	if dir := os.Getenv("POSSE_PREVIEW_DIR"); dir != "" {
		savePNG(t, tt, "missing-workspace.png")
	}
}

// 从别处加进来的 worktree 显示当前分支，游离时用原名；侧栏和标题都跟着换。
func TestAttachedShowsBranch(t *testing.T) {
	dir := t.TempDir()
	repo := filepath.Join(dir, "godfrey")
	os.MkdirAll(repo, 0o755)
	gitRun(t, repo, "init", "-q", "-b", "main")
	gitRun(t, repo, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(dir, "tz-wt", "godfrey")
	gitRun(t, repo, "worktree", "add", "-q", "-b", "feat_tz", wt)
	ws := []workspace{
		{ID: "m", ProjectID: "p", Name: "main", Kind: "main", Project: "godfrey", Label: "godfrey/main", Path: repo},
		{ID: "a", ProjectID: "p", Name: "godfrey", Kind: "folder", Project: "godfrey", Label: "godfrey/godfrey", Path: wt},
	}
	showBranches(ws)
	if ws[0].Shown != "" || ws[1].Shown != "feat_tz" || displayName(&ws[1]) != "godfrey/feat_tz" {
		t.Fatalf("shown %q %q, title %q", ws[0].Shown, ws[1].Shown, displayName(&ws[1]))
	}
	gitRun(t, wt, "checkout", "-q", "--detach")
	ws[1].Shown = ""
	showBranches(ws)
	if ws[1].Shown != "" || displayName(&ws[1]) != "godfrey/godfrey" {
		t.Fatalf("detached shown %q", ws[1].Shown)
	}
}

// Markdown 预览切到别的标签再切回来，接着上次滚到的位置；每个文档的位置各记各的。
func TestMarkdownPreviewKeepsScroll(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 360)
	readme := filepath.Join(root, "README.md")
	a.openFile(w, readme, 0, [2]int{})
	d := a.files[w.ID].doc(readme)
	until(t, tt, "loaded", func() bool { return d.kind == docText && d.md != nil })
	tt.Frame()
	if d.mdScroll.MaxY <= 0 {
		t.Fatal("README should overflow the preview")
	}
	tt.Scroll(800, 250, 0, 120)
	tt.Frame()
	y := d.mdScroll.Y
	if y <= 0 {
		t.Fatalf("scroll did not move the preview: y = %v", y)
	}
	// 切到另一个 Markdown，它从头显示
	plan := filepath.Join(root, "docs", "plan.md")
	a.openFile(w, plan, 0, [2]int{})
	p := a.files[w.ID].doc(plan)
	until(t, tt, "plan loaded", func() bool { return p.kind == docText && p.md != nil })
	tt.Frame()
	if p.mdScroll.Y != 0 {
		t.Fatalf("another document took the scroll: y = %v", p.mdScroll.Y)
	}
	// 再切到源码文件，隔几帧，预览的元素状态已被回收
	a.openFile(w, filepath.Join(root, "main.go"), 0, [2]int{})
	for range 3 {
		tt.Frame()
	}
	a.openFile(w, readme, 0, [2]int{})
	tt.Frame()
	tt.Frame()
	if d.mdScroll.Y != y {
		t.Fatalf("scroll after switching back = %v, want %v", d.mdScroll.Y, y)
	}
}

// Markdown 预览里 ⌘F 打开查找栏，⌘G、⇧⌘G 在命中间跳（⇧⌘G 不去开关改动页），Esc 关闭。
func TestMarkdownPreviewFind(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	readme := filepath.Join(root, "README.md")
	a.openFile(w, readme, 0, [2]int{})
	v := a.files[w.ID]
	d := v.doc(readme)
	until(t, tt, "loaded", func() bool { return d.kind == docText && d.md != nil })
	tt.Key(ui.Cmd, ui.KeyF)
	tt.Frame()
	if !d.find.open {
		t.Fatal("⌘F should open the find bar in the markdown preview")
	}
	tt.Type("posse")
	tt.Frame()
	tt.Frame()
	// 标题里的 Posse 和代码块里的 posse
	if d.find.n != 2 || !tt.HasText("1/2") {
		t.Fatalf("matches = %d", d.find.n)
	}
	tt.Key(ui.Cmd, ui.KeyG)
	tt.Frame()
	if d.find.cur != 1 {
		t.Fatalf("⌘G: cur = %d", d.find.cur)
	}
	side, open := v.side, a.filesPanelOpen()
	tt.Key(ui.Cmd|ui.Shift, ui.KeyG)
	tt.Frame()
	if d.find.cur != 0 || v.side != side || a.filesPanelOpen() != open {
		t.Fatalf("⇧⌘G should step back in the find bar: cur = %d", d.find.cur)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if d.find.open {
		t.Fatal("Esc should close the find bar")
	}
}

// 工作区目录当时不在、后来又出现时，轮询重读根目录，文件树不会一直停在"正在读取…"。
func TestTreeRootReappears(t *testing.T) {
	a := previewApp(t)
	root := filepath.Join(t.TempDir(), "later")
	a.workspaces[0].Path = root
	a.indexWorkspaces()
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	v := a.filesFor(w)
	a.pollFiles(v)
	pass(tt, 200*time.Millisecond)
	tt.Frame()
	if v.tree.Loaded(root) {
		t.Fatal("a missing root loaded")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "a.txt"), nil, 0o644)
	a.pollFiles(v)
	until(t, tt, "root read", func() bool { return v.tree.Loaded(root) && len(v.tree.Rows()) == 1 })
}

// 文件树右键"删除…"先确认；移走后关掉其中没改过的文件，改过的留着显示已删除。
func TestTrashFromTree(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	tt := newTester(a.build, 1200, 760)
	a.openFilesPanel(w, sideFiles)
	until(t, tt, "tree", func() bool { return tt.HasText("main.go") && a.filesPanelShown == 1 })
	if err := tt.RightClick("main.go"); err != nil {
		t.Fatal(err)
	}
	chooseMenu(t, tt, "删除…")
	if !a.confirming || a.confirmTitle != "把 main.go 移到废纸篓？" || a.confirmBody != "文件会移到废纸篓，可以从那里找回。" {
		t.Fatalf("confirm %v %q %q", a.confirming, a.confirmTitle, a.confirmBody)
	}
	a.confirming = false

	v := a.files[w.ID]
	tree, search := filepath.Join(root, "internal/files/tree.go"), filepath.Join(root, "internal/files/search.go")
	a.openFile(w, tree, 0, [2]int{})
	a.openFile(w, search, 0, [2]int{})
	until(t, tt, "opened", func() bool { return v.doc(search) != nil && v.doc(search).kind == docText })
	v.doc(search).ed.Insert("x")
	a.forgetTrashed(v, filepath.Join(root, "internal/files"), true)
	if v.doc(tree) != nil || v.doc(search) == nil {
		t.Fatalf("docs left: %d", len(v.docs))
	}
}

// 照 WebStorm：文件树里选中目录后按 ⇧⌘F 只搜这个目录；点掉范围回到整个工作区，焦点在别处时按下也是整个工作区
func TestSearchInSelectedDir(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	v := a.openFilesPanel(w, sideFiles)
	tt := newTester(a.build, 1200, 760)
	for range 20 {
		tt.Frame()
		pass(tt, 20*time.Millisecond)
	}
	until(t, tt, "tree", func() bool { return tt.HasText("internal") })
	if err := tt.Click("internal"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.treeSelected() != filepath.Join(root, "internal") {
		t.Fatalf("selection %q", v.treeSel)
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	tt.Frame()
	tt.Frame()
	tt.Type("package")
	until(t, tt, "scoped", func() bool { return !v.search.running && len(v.search.hits) == 3 })
	if v.search.query.Dir != "internal" || !tt.HasText("在 internal 中") {
		t.Fatalf("scope %q", v.search.query.Dir)
	}
	if err := tt.Click("搜索整个工作区"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "whole", func() bool { return v.search.query.Dir == "" && !v.search.running && len(v.search.hits) == 4 })

	// 焦点不在文件树上时按 ⇧⌘F 搜整个工作区
	v.search.query.Dir = "internal"
	tt.Key(ui.Cmd|ui.Shift, ui.KeyF)
	until(t, tt, "unscoped", func() bool { return v.search.query.Dir == "" && !v.search.running && len(v.search.hits) == 4 })

	// 右键目录「在此目录中查找…」
	a.clearSearch(v)
	tt.Frame()
	until(t, tt, "files row", func() bool { return tt.HasText("files") }) // 上面点 internal 时展开了
	if err := tt.RightClick("files"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("在此目录中查找…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	tt.Type("package")
	until(t, tt, "menu scope", func() bool {
		return v.search.query.Dir == filepath.Join("internal", "files") && !v.search.running && len(v.search.hits) == 2
	})
}

// 随处搜索：⇧⌘A 在"操作"页按名字找到操作，回车直接做；⇥ 换页；查询为空时文件页先列最近打开的
func TestSearchEverywhere(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	sidebar := a.state.SidebarHidden
	tt.Key(ui.Cmd|ui.Shift, ui.KeyA)
	tt.Frame()
	if !a.quick.open || a.quick.tab != quickActions {
		t.Fatalf("⇧⌘A: open %v tab %d", a.quick.open, a.quick.tab)
	}
	tt.Frame()
	tt.Type("侧栏")
	tt.Frame()
	if !tt.HasText("收起 / 展开侧栏") || tt.HasText("文件树") {
		t.Fatalf("actions: %v", tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if a.quick.open || a.state.SidebarHidden == sidebar {
		t.Fatal("enter should run the action and close")
	}

	// 打开过的文件在空查询时排最前
	v := a.filesFor(w)
	a.openFile(w, filepath.Join(root, "docs", "plan.md"), 0, [2]int{})
	tt.Frame()
	a.openQuick(w, quickAll)
	until(t, tt, "list", func() bool { return v.list != nil && len(a.quick.matches) > 0 })
	if a.quick.matches[0].Path != filepath.Join("docs", "plan.md") {
		t.Fatalf("recent first: %v", a.quick.matches[0].Path)
	}
	tt.Frame()
	tt.Key(0, ui.KeyTab)
	tt.Frame()
	if a.quick.tab != quickFiles {
		t.Fatalf("tab: %d", a.quick.tab)
	}
	tt.Type("search")
	until(t, tt, "match", func() bool {
		return len(a.quick.matches) > 0 && a.quick.matches[0].Path == filepath.Join("internal", "files", "search.go")
	})
	tt.Key(0, ui.KeyEnter)
	until(t, tt, "opened", func() bool {
		return v.cur != nil && v.cur.path == filepath.Join(root, "internal", "files", "search.go")
	})
}

// 随处搜索的文本页：后台全文检索，回车跳到命中的那一行；"在文件栏中查看全部"交给文件栏的检索框
func TestSearchEverywhereText(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	v := a.filesFor(w)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	a.openQuick(w, quickAll)
	tt.Frame()
	tt.Type("func")
	until(t, tt, "all page", func() bool { return !a.quick.text.running && len(a.quick.text.hits) == 4 && tt.HasText("文本") })
	if !tt.HasText("search.go:3") {
		t.Fatalf("hit row: %v", tt.Texts())
	}
	if err := tt.Click("文本"); err != nil { // 页签
		t.Fatal(err)
	}
	tt.Frame()
	if a.quick.tab != quickText {
		t.Fatalf("tab %d", a.quick.tab)
	}
	if err := tt.Click("func Search() {}"); err != nil {
		t.Fatal(err)
	}
	sp := filepath.Join(root, "internal", "files", "search.go")
	until(t, tt, "jump", func() bool {
		d := v.doc(sp)
		return !a.quick.open && d != nil && d.gotoLine == 0 && d.ed.SelectedText() == "func"
	})

	a.openQuick(w, quickText)
	tt.Frame()
	tt.Type("package")
	until(t, tt, "hits", func() bool { return !a.quick.text.running && len(a.quick.text.hits) > 0 })
	if err := tt.Click("在文件栏中查看全部"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "panel", func() bool {
		return !a.quick.open && a.filesPanelOpen() && v.search.query.Text == "package" && !v.search.running && len(v.search.hits) == 4
	})
}

// 随处搜索的符号页：按名字找声明，回车跳到声明并选中名字；「只看类型」只留类型
func TestSearchEverywhereSymbols(t *testing.T) {
	a := previewApp(t)
	root := filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	v := a.filesFor(w)
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	a.openQuick(w, quickSymbols)
	tt.Frame()
	tt.Type("search")
	until(t, tt, "symbols", func() bool { return !a.quick.sym.running && len(a.quick.sym.syms) == 1 })
	if s := a.quick.sym.syms[0]; s.Name != "Search" || !tt.HasText("函数") || !tt.HasText("search.go:3") {
		t.Fatalf("symbol %+v %v", s, tt.Texts())
	}
	tt.Key(0, ui.KeyEnter)
	sp := filepath.Join(root, "internal", "files", "search.go")
	until(t, tt, "jump", func() bool {
		d := v.doc(sp)
		return !a.quick.open && d != nil && d.gotoLine == 0 && d.ed.SelectedText() == "Search"
	})

	a.openQuick(w, quickSymbols)
	tt.Frame()
	tt.Type("tree")
	until(t, tt, "tree", func() bool { return !a.quick.sym.running && len(a.quick.sym.syms) > 0 })
	if err := tt.Click("只列类、结构体、接口、枚举这类类型"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "types", func() bool {
		s := a.quick.sym.syms
		return a.quick.typesOnly && !a.quick.sym.running && len(s) == 1 && s[0].Name == "Tree" && s[0].Kind == "type"
	})
}

// 双击 ⇧（设置里打开后）：单独连按两下 ⇧ 打开随处搜索；中间按了别的键不算，没打开设置也不算
func TestDoubleShift(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	w := &a.workspaces[0]
	a.selected = w.ID
	tt := newTester(a.build, 1200, 760)
	tt.Frame()
	tap := func() {
		tt.HoldModifiers(ui.Shift)
		tt.HoldModifiers(0)
	}
	tap()
	tap()
	tt.Frame()
	if a.quick.open {
		t.Fatal("double shift should be off by default")
	}
	a.state.DoubleShift = true
	tap()
	tt.Key(ui.Shift, ui.KeyA) // 中间打了一个大写字母
	tap()
	tt.Frame()
	if a.quick.open {
		t.Fatal("a key between the taps should not count")
	}
	tap()
	tap()
	tt.Frame()
	if !a.quick.open || a.quick.tab != quickAll {
		t.Fatalf("double shift: open %v tab %d", a.quick.open, a.quick.tab)
	}
}
