package gitx

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// initRepo 在 dir 建一个带一次提交的仓库。
func initRepo(t *testing.T, dir string) {
	t.Helper()
	write(t, filepath.Join(dir, "README"), "x\n")
	gitIn(t, dir, "init", "-q", "-b", "main")
	gitIn(t, dir, "config", "user.email", "t@example.com")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "base")
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// roots 是找到的仓库相对 ws 的路径，排好序。
func roots(ws string, d Discovery) []string {
	var out []string
	for _, r := range d.Repos {
		rel, _ := filepath.Rel(ws, r.Root)
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

func TestDiscoverFolder(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	outside := filepath.Join(base, "outside")

	initRepo(t, filepath.Join(ws, "a"))
	initRepo(t, filepath.Join(ws, "group", "deep", "b"))
	// 软链过去的仓库（本机的 Works 就是这样）；两条软链指向同一个仓库只记一个，另一条是别名
	initRepo(t, filepath.Join(outside, "c"))
	symlink(t, filepath.Join(outside, "c"), filepath.Join(ws, "c"))
	symlink(t, filepath.Join(outside, "c"), filepath.Join(ws, "z-again"))
	// 依赖、点目录里的仓库不算
	initRepo(t, filepath.Join(ws, "node_modules", "dep"))
	initRepo(t, filepath.Join(ws, ".cache", "hidden"))
	// 指向上级目录、自己的软链不跟（否则扫遍整个上级），也不会绕成环
	symlink(t, base, filepath.Join(ws, "up"))
	symlink(t, ws, filepath.Join(ws, "group", "self"))
	// 裸仓库没有工作区，不算，也不往里走
	gitIn(t, filepath.Join(ws, "a"), "clone", "-q", "--bare", ".", filepath.Join(ws, "bare.git"))
	// 坏掉的软链
	symlink(t, filepath.Join(base, "missing"), filepath.Join(ws, "broken"))

	d := Discover(context.Background(), ws, true)
	if got, want := roots(ws, d), []string{"a", "c", "group/deep/b"}; !slices.Equal(got, want) {
		t.Fatalf("repos = %v, want %v", got, want)
	}
	if d.Partial {
		t.Fatal("unexpected partial")
	}
	c := d.Repos[1]
	if !slices.Equal(c.Aliases, []string{filepath.Join(ws, "z-again")}) {
		t.Fatalf("aliases = %v", c.Aliases)
	}
	if root, ok := c.Contains(filepath.Join(ws, "z-again", "README")); !ok || root != filepath.Join(ws, "z-again") {
		t.Fatalf("Contains via alias = %q %v", root, ok)
	}
	if _, ok := c.Contains(filepath.Join(ws, "cc")); ok {
		t.Fatal("prefix without separator matched")
	}
}

func TestDiscoverInsideRepo(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	initRepo(t, outer)
	write(t, filepath.Join(outer, "src", "a.txt"), "a\n")
	write(t, filepath.Join(outer, ".gitignore"), "ignored/\n")
	gitIn(t, outer, "add", ".")
	gitIn(t, outer, "commit", "-q", "-m", "src")
	// 已跟踪目录里的嵌套仓库：git 报成未跟踪的 src/inner/
	initRepo(t, filepath.Join(outer, "src", "inner"))
	// 嵌套仓库里再嵌一个
	initRepo(t, filepath.Join(outer, "src", "inner", "innermost"))
	// 未跟踪的普通目录里更深处的仓库：git 只报 tools/
	initRepo(t, filepath.Join(outer, "tools", "lib", "x"))
	// 同一个未跟踪目录里被外层忽略的（.gitignore 的 ignored/）不算
	initRepo(t, filepath.Join(outer, "tools", "ignored", "secret"))
	write(t, filepath.Join(outer, "tools", "keep.txt"), "k\n")
	// 同一个未跟踪目录里指向外面仓库的软链
	initRepo(t, filepath.Join(base, "ext3"))
	symlink(t, filepath.Join(base, "ext3"), filepath.Join(outer, "tools", "link"))
	// 被外层忽略的嵌套仓库不算
	initRepo(t, filepath.Join(outer, "ignored", "x"))
	// 没有 .gitmodules 的 gitlink
	initRepo(t, filepath.Join(outer, "gl"))
	gitIn(t, outer, "add", "gl")
	// 仓库里指向外面仓库的软链：一条已跟踪，一条未跟踪
	initRepo(t, filepath.Join(base, "ext1"))
	initRepo(t, filepath.Join(base, "ext2"))
	symlink(t, filepath.Join(base, "ext1"), filepath.Join(outer, "link1"))
	gitIn(t, outer, "add", "link1")
	symlink(t, filepath.Join(base, "ext2"), filepath.Join(outer, "link2"))

	d := Discover(context.Background(), outer, true)
	want := []string{".", "gl", "link1", "link2", "src/inner", "src/inner/innermost", "tools/lib/x", "tools/link"}
	if got := roots(outer, d); !slices.Equal(got, want) {
		t.Fatalf("repos = %v, want %v", got, want)
	}

	// 工作区是仓库的子目录：外层仓库也算，嵌套仓库只在这个子目录里找
	sub := filepath.Join(outer, "src")
	d = Discover(context.Background(), sub, true)
	var got []string
	for _, r := range d.Repos {
		got = append(got, r.Root)
	}
	if want := []string{outer, filepath.Join(sub, "inner"), filepath.Join(sub, "inner", "innermost")}; !slices.Equal(got, want) {
		t.Fatalf("subdir repos = %v, want %v", got, want)
	}
}

// worktree 的 .git 是文件，照样认成仓库。
func TestDiscoverWorktree(t *testing.T) {
	base := t.TempDir()
	main := filepath.Join(base, "main")
	initRepo(t, main)
	ws := filepath.Join(base, "ws")
	gitIn(t, main, "worktree", "add", "-q", "-b", "feat", filepath.Join(ws, "feat"))
	d := Discover(context.Background(), ws, true)
	if got := roots(ws, d); !slices.Equal(got, []string{"feat"}) {
		t.Fatalf("repos = %v", got)
	}
}

// 扫描只用查询命令，也不对不是仓库的目录调 git：不在操作记录里留下任何东西。
func TestDiscoverLeavesNoTrace(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	initRepo(t, filepath.Join(ws, "a"))
	initRepo(t, filepath.Join(ws, "a", "nested"))
	if err := os.MkdirAll(filepath.Join(ws, "plain", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var runs []string
	old := Trace
	Trace = func(r Run) {
		mu.Lock()
		defer mu.Unlock()
		runs = append(runs, strings.Join(r.Args, " ")+" "+r.Err)
	}
	t.Cleanup(func() { Trace = old })
	Discover(context.Background(), ws, true)
	Discover(context.Background(), filepath.Join(ws, "plain"), true)
	Discover(context.Background(), filepath.Join(ws, "a"), true)
	if len(runs) > 0 {
		t.Fatalf("traced: %v", runs)
	}
}

func TestDiscoverDirLimit(t *testing.T) {
	ws := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d"} {
		if err := os.MkdirAll(filepath.Join(ws, n, "x", "y"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	initRepo(t, filepath.Join(ws, "a", "x", "y", "deep", "deeper", "repo"))
	if d := Discover(context.Background(), ws, true); len(d.Repos) != 1 || d.Partial {
		t.Fatalf("repos = %v partial = %v", roots(ws, d), d.Partial)
	}
	old := discoverMaxDirs
	discoverMaxDirs = 5
	t.Cleanup(func() { discoverMaxDirs = old })
	if d := Discover(context.Background(), ws, true); !d.Partial {
		t.Fatal("dir limit not reported")
	}
}

// 关掉多仓库时只认所在的那一个仓库。
func TestDiscoverSingle(t *testing.T) {
	base := t.TempDir()
	initRepo(t, filepath.Join(base, "ws", "a"))
	if d := Discover(context.Background(), filepath.Join(base, "ws"), false); len(d.Repos) != 0 {
		t.Fatalf("repos = %v", d.Repos)
	}
	initRepo(t, filepath.Join(base, "ws", "a", "nested"))
	if got := roots(filepath.Join(base, "ws", "a"), Discover(context.Background(), filepath.Join(base, "ws", "a"), false)); !slices.Equal(got, []string{"."}) {
		t.Fatalf("repos = %v", got)
	}
}

// 工作区是指向仓库子目录的软链：按真实路径找到外围仓库（关掉多仓库时也是）。
func TestDiscoverSymlinkSubdir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	write(t, filepath.Join(repo, "src", "a.txt"), "a\n")
	symlink(t, filepath.Join(repo, "src"), filepath.Join(base, "linked-src"))
	real, _ := filepath.EvalSymlinks(repo)
	for _, multi := range []bool{false, true} {
		d := Discover(context.Background(), filepath.Join(base, "linked-src"), multi)
		if len(d.Repos) != 1 || d.Repos[0].Root != real {
			t.Fatalf("multi=%v repos = %+v", multi, d.Repos)
		}
	}
}

// 外层仓库经两条软链进来：里面的仓库也有另一条的写法。
func TestDiscoverNestedAliases(t *testing.T) {
	base := t.TempDir()
	outer := filepath.Join(base, "outer")
	initRepo(t, outer)
	initRepo(t, filepath.Join(outer, "inner"))
	ws := filepath.Join(base, "ws")
	symlink(t, outer, filepath.Join(ws, "a"))
	symlink(t, outer, filepath.Join(ws, "z"))
	d := Discover(context.Background(), ws, true)
	if got := roots(ws, d); !slices.Equal(got, []string{"a", "a/inner"}) {
		t.Fatalf("repos = %v", got)
	}
	if root, ok := d.Repos[1].Contains(filepath.Join(ws, "z", "inner", "README")); !ok || root != filepath.Join(ws, "z", "inner") {
		t.Fatalf("inner via z: %q %v (aliases %v)", root, ok, d.Repos[1].Aliases)
	}
}
