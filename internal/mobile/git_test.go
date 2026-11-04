package mobile

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/term"
)

// 工作区目录下有几个仓库：新版 App 拿到按仓库分开的列表，旧版拿第一个仓库。
func TestGitStatusMultiRepo(t *testing.T) {
	base := t.TempDir()
	ws := filepath.Join(base, "ws")
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo := func(dir string) {
		t.Helper()
		write(filepath.Join(dir, "README"), "one\n")
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"commit", "-q", "-m", "init"}} {
			c := exec.Command("git", args...)
			c.Dir = dir
			c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
			if out, err := c.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, out)
			}
		}
	}
	repo(filepath.Join(ws, "a"))
	repo(filepath.Join(ws, "a", "inner"))
	repo(filepath.Join(ws, "b"))
	repo(filepath.Join(base, "c"))
	if err := os.Symlink(filepath.Join(base, "c"), filepath.Join(ws, "link")); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(ws, "a", "README"), "one\ntwo\n")
	write(filepath.Join(ws, "a", "inner", "README"), "one\ninner\n")
	write(filepath.Join(base, "c", "new.txt"), "c\n")

	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}, dir: ws}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	caps := func(c ...string) {
		result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": c})))
	}
	paths := func(r map[string]any) []string {
		var out []string
		for _, f := range r["files"].([]any) {
			out = append(out, f.(map[string]any)["path"].(string))
		}
		return out
	}
	wt := map[string]any{"worktree": "id:p1::w1"}

	// 旧版：第一个仓库 a，嵌套的 inner/ 不当成未跟踪文件
	caps("posse.git.v1")
	st := result(t, p.next(p.call("git.status", wt)))
	if st["branch"] != "main" || !slices.Equal(paths(st), []string{"README"}) {
		t.Fatalf("v1 status %v", st)
	}
	diff := result(t, p.next(p.call("git.diff", map[string]any{"worktree": "id:p1::w1", "path": "README"})))
	if !strings.Contains(diff["diff"].(string), "+two") {
		t.Fatalf("v1 diff %v", diff["diff"])
	}

	caps("posse.git.v1", "posse.git.v2")
	st = result(t, p.next(p.call("git.status", wt)))
	var got []string
	for _, r := range st["repos"].([]any) {
		r := r.(map[string]any)
		got = append(got, r["repo"].(string)+"="+r["name"].(string)+":"+strings.Join(paths(r), ","))
		if r["dir"] != r["repo"] {
			t.Fatalf("dir %v", r)
		}
	}
	if want := []string{"a=a:README", "a/inner=a/inner:README", "b=b:", "link=link:new.txt"}; !slices.Equal(got, want) {
		t.Fatalf("repos = %v, want %v", got, want)
	}
	diff = result(t, p.next(p.call("git.diff", map[string]any{"worktree": "id:p1::w1", "repo": "a/inner", "path": "README"})))
	if !strings.Contains(diff["diff"].(string), "+inner") {
		t.Fatalf("inner diff %v", diff["diff"])
	}
	if bad := p.next(p.call("git.diff", map[string]any{"worktree": "id:p1::w1", "repo": "gone", "path": "README"})); bad["ok"] != false {
		t.Fatalf("unknown repo %v", bad)
	}
}

// 工作区是仓库的子目录：仓库键是 ".."，手机按 sub 把仓库里的路径换回工作区里的路径。
func TestGitStatusSubdir(t *testing.T) {
	root := t.TempDir()
	c := exec.Command("git", "init", "-q", "-b", "main")
	c.Dir = root
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	ws := filepath.Join(root, "src")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := readRepo(t.Context(), ws, []gitx.Repo{{Root: root, Real: root}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r["repo"] != ".." || r["sub"] != "src" || r["dir"] != nil || r["name"] != filepath.Base(root) {
		t.Fatalf("subdir repo %v", r)
	}
}

// 请求中途取消的扫描不记进缓存：下一次照常扫到全部仓库。
func TestRepoCacheCanceled(t *testing.T) {
	ws := t.TempDir()
	for _, n := range []string{"a", "b"} {
		c := exec.Command("git", "init", "-q", filepath.Join(ws, n))
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	var rc repoCache
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rc.get(ctx, ws, true)
	if got := rc.get(t.Context(), ws, true); len(got) != 2 {
		t.Fatalf("after canceled scan: %d repos", len(got))
	}
	// 已有结果时，取消的扫描不盖掉它
	rc.dirs[ws].at = time.Now().Add(-2 * repoRescan)
	if got := rc.get(ctx, ws, true); len(got) != 2 {
		t.Fatalf("canceled rescan: %d repos", len(got))
	}
	if got := rc.get(t.Context(), ws, true); len(got) != 2 {
		t.Fatalf("after canceled rescan: %d repos", len(got))
	}
}

// 以 .. 开头的目录名不是上级目录：照样给 dir。
func TestGitStatusDotDotName(t *testing.T) {
	ws := t.TempDir()
	root := filepath.Join(ws, "..foo")
	c := exec.Command("git", "init", "-q", root)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	r, err := readRepo(t.Context(), ws, []gitx.Repo{{Root: root, Real: root}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r["dir"] != "..foo" || r["name"] != "..foo" {
		t.Fatalf("..foo repo %v", r)
	}
}

// 图片 diff 不顺着软链读仓库外面的文件。
func TestGitDiffImageSymlinkOutside(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	c := exec.Command("git", "init", "-q", repo)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.WriteFile(filepath.Join(base, "secret.txt"), []byte("PRIVATE"), 0o644)
	os.Symlink(filepath.Join(base, "secret.txt"), filepath.Join(repo, "leak.png"))
	os.WriteFile(filepath.Join(repo, "ok.png"), []byte("\x89PNG"), 0o644)
	if b, err := readInside(repo, "leak.png"); err == nil {
		t.Fatalf("read outside the repository: %q", b)
	}
	if b, err := readInside(repo, "ok.png"); err != nil || string(b) != "\x89PNG" {
		t.Fatalf("ok.png %q %v", b, err)
	}
}

// 工作区是指向仓库子目录的软链：按真实路径算出 sub，手机才能把仓库里的路径换回工作区里的。
func TestGitStatusSymlinkSubdir(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	c := exec.Command("git", "init", "-q", repo)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	linked := filepath.Join(base, "linked-src")
	os.Symlink(filepath.Join(repo, "src"), linked)
	real, _ := filepath.EvalSymlinks(repo)
	r, err := readRepo(t.Context(), linked, []gitx.Repo{{Root: real, Real: real}}, 0)
	if err != nil || r["sub"] != "src" || r["dir"] != nil {
		t.Fatalf("linked subdir repo %v %v", r, err)
	}
}
