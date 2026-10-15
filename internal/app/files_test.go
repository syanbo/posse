package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
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
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	v := a.showFiles(w)
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
	if err := tt.Click("不保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if v.doc(path) != nil {
		t.Fatal("doc still open")
	}
	// 没有文件时回到之前的终端（再画一帧就要真的接入终端，测试里没有常驻进程，只看选中项）
	a.leaveFiles(w)
	if a.activeTerm[w.ID] != "t1" {
		t.Fatalf("activeTerm = %q, want back to t1", a.activeTerm[w.ID])
	}
}

// 没有终端的工作区也能打开文件标签，而不是落到空工作区的页面。
func TestFilesTabWithoutTerminals(t *testing.T) {
	a := previewApp(t)
	filesWorkspace(t, a)
	a.selected = "w2"
	tt := ui.NewTester(a.build, 1200, 760)
	tt.Frame()
	if !tt.HasText("切换终端") {
		t.Fatal("expected the empty workspace page first")
	}
	if err := tt.Click("文件"); err != nil {
		t.Fatal(err)
	}
	until(t, tt, "files page", func() bool { return tt.HasText("在左边的文件树里选一个文件") })
	if tt.HasText("切换终端") {
		t.Fatal("empty workspace page still shown")
	}
	tt.Key(ui.Cmd|ui.Shift, ui.KeyE)
	tt.Frame()
	if !a.filesActive(a.workspace("w2")) {
		t.Fatal("⇧⌘E should stay on files without terminals")
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
	if !a.quick.open || !a.filesActive(w) {
		t.Fatal("⌘P should open quick open on the files tab")
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
	if v.side != 1 {
		t.Fatal("⇧⌘F should switch to search")
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
