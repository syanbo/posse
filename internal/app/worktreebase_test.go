package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// worktreeWorkspace 把 w1 换成从项目目录分出去的 worktree，两边改了 main.go 的同一行：
// 合并会冲突。项目目录（w2，挪进同一项目）在基准分支上存了一条储藏。返回项目目录和基准分支。
func worktreeWorkspace(t *testing.T, a *app) (root, base string) {
	t.Helper()
	root = gitWorkspace(t, a)
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-q", "-m", "work")
	base = gitRun(t, root, "rev-parse", "--abbrev-ref", "HEAD")
	wt := filepath.Join(t.TempDir(), "feat-log")
	if err := gitx.AddWorktree(context.Background(), root, wt, "feat-log", base); err != nil {
		t.Fatal(err)
	}
	edit := func(dir, from, to, msg string) {
		p := filepath.Join(dir, "main.go")
		b, _ := os.ReadFile(p)
		os.WriteFile(p, []byte(strings.Replace(string(b), from, to, 1)), 0o644)
		gitRun(t, dir, "commit", "-qam", msg)
	}
	edit(wt, "log(cfg)", "log(cfg, \"feat\")", "feat: 带上来源")
	edit(root, "log(cfg)", "log(cfg, \"main\")", "main: 带上来源")
	edit(root, "package main", "// Package main 是入口。\npackage main", "main: 包注释")
	os.WriteFile(filepath.Join(root, "README.md"), []byte("改了一半\n"), 0o644)
	gitRun(t, root, "stash", "push", "-q", "-m", "项目目录里改了一半")

	a.workspaces[0].Path, a.workspaces[0].Kind, a.workspaces[0].Name = wt, "worktree", "feat-log"
	a.workspaces[0].Branch, a.workspaces[0].BaseRef = "feat-log", base
	a.workspaces[1].Path, a.workspaces[1].ProjectID, a.workspaces[1].Branch = root, "p1", base
	a.indexWorkspaces()
	return root, base
}

func TestWorktreeBaseConflictToSync(t *testing.T) {
	a := previewApp(t)
	_, base := worktreeWorkspace(t, a)
	tt := ui.NewTester(a.build, 1200, 760)
	v := openChanges(t, a, tt)
	until(t, tt, "base", func() bool { return a.track["w1"] != nil && a.track["w1"].baseOK })
	d := a.track["w1"].base
	if d.Ahead != 1 || d.Behind != 2 || len(d.Conflicts) != 1 || d.Conflicts[0] != "main.go" {
		t.Fatalf("divergence %+v", d)
	}
	tt.Frame()
	for _, want := range []string{"相对 " + base, "会冲突 1", "同步", "合并"} {
		if !tt.HasText(want) {
			t.Fatalf("base row lacks %q: %v", want, tt.Texts())
		}
	}

	// 储藏是仓库共用的：项目目录里存的这里也看得到，标出分支，弹出前先问
	v.git.tab = 1
	tt.Frame()
	if !tt.HasText("项目目录里改了一半") || !tt.HasText(base) {
		t.Fatalf("stash from %s not labeled: %v", base, tt.Texts())
	}
	if owner := a.stashOwner(v, base); owner == nil || owner.ID != "w2" {
		t.Fatalf("stash owner %+v", owner)
	}
	v.git.tab = 0

	// 预测到冲突：合并按钮不去试，改问要不要先把基准分支合并进来
	if err := tt.Click("合并"); err != nil {
		t.Fatal(err)
	}
	// 点的时候现算一次会不会冲突，算完才问
	until(t, tt, "conflict ask", func() bool { return a.confirming })
	if a.confirmTitle != "合并会冲突" || !strings.Contains(a.confirmBody, "main.go") {
		t.Fatalf("confirm %q %q", a.confirmTitle, a.confirmBody)
	}
	a.confirming = false
	a.confirmDo()
	// 预览里 w1 有 agent 在工作：改动工作目录前再确认一次
	until(t, tt, "agent guard", func() bool { return a.confirming && strings.Contains(a.confirmTitle, "正在这个工作区里工作") })
	a.confirming = false
	a.confirmDo()
	// 在 worktree 里合并，停在冲突上，改动页是操作横幅
	until(t, tt, "merge stops on conflict", func() bool {
		return v.git.status != nil && v.git.status.Operation == "merge" && v.git.busy == ""
	})
	tt.Frame()
	if tt.HasText("相对 " + base) {
		t.Fatal("base row should give way to the operation banner")
	}
	if !tt.HasText("中止") {
		t.Fatalf("no operation banner: %v", tt.Texts())
	}
	// 停在冲突上是预期的：横幅说明怎么继续，错误条和"已合并"提示都不出
	if a.err != "" || len(a.toasts) != 0 {
		t.Fatalf("conflict stop reported as error or success: err=%q toasts=%v", a.err, a.toasts)
	}
}

func TestMergedBody(t *testing.T) {
	if got := mergedBody("feat", nil); strings.Contains(got, "没提交") {
		t.Fatalf("clean worktree: %q", got)
	}
	got := mergedBody("feat", []string{".env", "tmp/a.log", "tmp/b.log", "out.bin"})
	if !strings.Contains(got, "没提交的文件一起删掉：.env、tmp/a.log、tmp/b.log 等 4 个文件。") {
		t.Fatalf("untracked listed: %q", got)
	}
}
