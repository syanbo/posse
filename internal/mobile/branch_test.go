package mobile

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"posse/internal/gitx"
	"posse/internal/term"
)

// 手机切换分支：列出本地和远端分支，远端分支检出成本地跟踪分支，别的 worktree 检出着的不切，
// agent 正在工作时先要确认，切换记进操作记录。
func TestGitCheckout(t *testing.T) {
	base := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	origin := filepath.Join(base, "origin")
	os.MkdirAll(origin, 0o755)
	git(origin, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(origin, "README"), []byte("one\n"), 0o644)
	git(origin, "add", ".")
	git(origin, "commit", "-q", "-m", "init")
	git(origin, "branch", "r1")
	ws := filepath.Join(base, "ws")
	git(base, "clone", "-q", origin, ws)
	git(ws, "branch", "wt")
	git(ws, "worktree", "add", "-q", filepath.Join(base, "other"), "wt")

	h := &fakeHost{terms: map[string]*term.Terminal{}, dir: ws}
	srv, err := New(t.TempDir(), h, -1)
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
	errCode := func(resp map[string]any) string {
		t.Helper()
		if resp["ok"] != false {
			t.Fatalf("want error, got %v", resp)
		}
		return resp["error"].(map[string]any)["code"].(string)
	}
	wt := map[string]any{"worktree": "id:p1::w1", "repo": "."}

	caps("posse.git.v1", "posse.git.v2")
	if code := errCode(p.next(p.call("git.branches", wt))); code != "method_not_found" {
		t.Fatalf("without v3: %s", code)
	}

	caps("posse.git.v1", "posse.git.v2", "posse.git.v3")
	res := result(t, p.next(p.call("git.branches", wt)))
	var names []string
	for _, b := range res["branches"].([]any) {
		b := b.(map[string]any)
		n := b["name"].(string)
		if b["current"] == true {
			n += "*"
		}
		if b["elsewhere"] != nil {
			n += "@"
		}
		names = append(names, n)
	}
	if want := []string{"main*", "wt@", "origin/main", "origin/r1"}; !slices.Equal(names, want) || res["head"] != "main" {
		t.Fatalf("branches %v head %v, want %v", names, res["head"], want)
	}

	checkout := func(name string, remote, force bool) map[string]any {
		return p.next(p.call("git.checkout", map[string]any{"worktree": "id:p1::w1", "repo": ".", "name": name, "remote": remote, "force": force}))
	}
	if code := errCode(checkout("wt", false, false)); code != "checked_out" {
		t.Fatalf("checked out elsewhere: %s", code)
	}

	h.mu.Lock()
	h.working = "codex"
	h.mu.Unlock()
	if r := result(t, checkout("origin/r1", true, false)); r["switched"] != false || r["confirm"] != "codex" {
		t.Fatalf("agent working: %v", r)
	}
	if head, _ := gitx.HeadRef(ws); head != "main" {
		t.Fatalf("switched without confirm: %s", head)
	}
	if r := result(t, checkout("origin/r1", true, true)); r["switched"] != true || r["head"] != "r1" {
		t.Fatalf("forced: %v", r)
	}
	h.mu.Lock()
	recs := slices.Clone(h.gitRecs)
	h.mu.Unlock()
	if len(recs) != 1 || !slices.Equal(recs[0].Args, []string{"switch", "--track", "origin/r1"}) || recs[0].Err != "" || recs[0].WorkspaceID != "w1" {
		t.Fatalf("records %+v", recs)
	}

	// 当前分支什么也不做，不记
	if r := result(t, checkout("r1", false, true)); r["switched"] != false {
		t.Fatalf("current: %v", r)
	}
	// 切回 main 时 git 拒绝（未提交的改动会被覆盖）：报 git 的错，也记下来
	git(ws, "checkout", "-q", "-b", "side")
	os.WriteFile(filepath.Join(ws, "README"), []byte("side\n"), 0o644)
	git(ws, "commit", "-q", "-am", "side")
	os.WriteFile(filepath.Join(ws, "README"), []byte("dirty\n"), 0o644)
	if code := errCode(checkout("main", false, true)); code != "git" {
		t.Fatalf("dirty: %s", code)
	}
	h.mu.Lock()
	recs = slices.Clone(h.gitRecs)
	h.mu.Unlock()
	if len(recs) != 2 || recs[1].Err == "" {
		t.Fatalf("failed record %+v", recs)
	}
}

// 多仓库的项目目录（主工作区）里检出着的分支：从多仓库 worktree 工作区看，能认出是主工作区，
// 项目目录里别的工具建的 worktree 不算主工作区。
func TestGitBranchesMultiRepoWorkspace(t *testing.T) {
	base := t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	proj := filepath.Join(base, "proj")
	api := filepath.Join(proj, "api")
	os.MkdirAll(api, 0o755)
	git(api, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(api, "README"), []byte("one\n"), 0o644)
	git(api, "add", ".")
	git(api, "commit", "-q", "-m", "init")
	git(api, "branch", "tool")
	// 项目目录里别的工具建的 worktree：git 认得出它，Posse 不把它当主工作区
	git(api, "worktree", "add", "-q", filepath.Join(proj, ".tool", "x"), "tool")
	wt := filepath.Join(base, "wt")
	os.MkdirAll(wt, 0o755)
	git(api, "worktree", "add", "-q", "-b", "feat", filepath.Join(wt, "api"))

	h := &fakeHost{terms: map[string]*term.Terminal{}, dir: wt,
		extra: []Workspace{{ID: "w0", ProjectID: "p1", Name: "main", Path: proj, Kind: "main"}}}
	srv, err := New(t.TempDir(), h, -1)
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
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1,
		"capabilities": []string{"posse.git.v1", "posse.git.v2", "posse.git.v3"}})))
	res := result(t, p.next(p.call("git.branches", map[string]any{"worktree": "id:p1::w1", "repo": "api"})))
	got := map[string]string{}
	for _, b := range res["branches"].([]any) {
		b := b.(map[string]any)
		if b["elsewhere"] != nil {
			ws, _ := b["workspace"].(string)
			got[b["name"].(string)] = ws
		}
	}
	if got["main"] != "p1::w0" || got["tool"] != "" || len(got) != 2 {
		t.Fatalf("elsewhere %v", got)
	}
}
