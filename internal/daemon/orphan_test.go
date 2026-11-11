package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"posse/internal/store"
)

// rmCall 调一个删除的处理函数，返回它的报错。
func rmCall(h func(context.Context, json.RawMessage) (any, error), params map[string]any) error {
	raw, _ := json.Marshal(params)
	_, err := h(context.Background(), raw)
	return err
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

// rmOut 调一个删除的处理函数，返回它的回复（转成 JSON 再读回来，和 CLI、窗口看到的一样）。
func rmOut(t *testing.T, h func(context.Context, json.RawMessage) (any, error), params map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(params)
	res, err := h(context.Background(), raw)
	if err != nil {
		t.Fatalf("%v: %v", params, err)
	}
	b, _ := json.Marshal(res)
	var out map[string]any
	json.Unmarshal(b, &out)
	return out
}

// orphanProject 建一个 git 项目和几个 worktree 工作区，每个工作区里放一个没提交的文件，再删掉项目目录。
func orphanProject(t *testing.T, d *Daemon, name string, wts ...string) (store.Project, map[string]store.Workspace) {
	t.Helper()
	ctx := context.Background()
	repo := filepath.Join(d.cfg.Home, "src", name)
	gitInit(t, repo)
	proj := store.Project{ID: store.NewID("p_"), Name: name, Path: repo, Kind: "git", CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	ws := map[string]store.Workspace{}
	for _, n := range wts {
		w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: n, Mode: "worktree", NoSetup: true})
		if err != nil || w.Kind != "worktree" {
			t.Fatalf("create %s: %+v %v", n, w, err)
		}
		os.WriteFile(filepath.Join(w.Path, "draft.txt"), []byte("not committed\n"), 0o644)
		ws[n] = w
	}
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	return proj, ws
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// 项目目录不在了（被删、挪走，或磁盘没挂载）：worktree 工作区带 force 只删记录、留下目录并告诉调用方，
// 再带 discardFiles 才连目录一起删；目录本来就不在或是空的直接算清理掉了；$POSSE_HOME/worktrees 外面的不碰。
func TestRemoveWhenProjectDirGone(t *testing.T) {
	d, _ := newTestDaemon(t)
	_, ws := orphanProject(t, d, "repo", "keep", "discard", "empty", "other")
	keep, discard, empty, other := ws["keep"], ws["discard"], ws["empty"], ws["other"]

	err := rmCall(d.workspaceRm, map[string]any{"workspace": keep.ID})
	if err == nil || !strings.Contains(err.Error(), "pass force") {
		t.Fatalf("rm without force: %v", err)
	}
	out := rmOut(t, d.workspaceRm, map[string]any{"workspace": keep.ID, "force": true, "deleteBranch": true})
	if _, err := d.store.Workspace(keep.ID); err == nil {
		t.Fatal("workspace record kept")
	}
	if !exists(filepath.Join(keep.Path, "draft.txt")) || out["left"] != keep.Path || out["repoGone"] != true {
		t.Fatalf("force without discardFiles: reply %v, directory there %v", out, exists(keep.Path))
	}

	out = rmOut(t, d.workspaceRm, map[string]any{"workspace": discard.ID, "force": true, "discardFiles": true})
	if exists(discard.Path) || out["left"] != nil {
		t.Fatalf("discardFiles: reply %v, directory there %v", out, exists(discard.Path))
	}

	os.RemoveAll(empty.Path)
	os.MkdirAll(empty.Path, 0o755)
	out = rmOut(t, d.workspaceRm, map[string]any{"workspace": empty.ID, "force": true})
	if exists(empty.Path) || out["left"] != nil {
		t.Fatalf("empty leftover: reply %v, directory there %v", out, exists(empty.Path))
	}

	// 记录指到 home 外面：带 discardFiles 也不碰，回复里照样说目录还在
	outside := filepath.Join(t.TempDir(), "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o644)
	if err := d.store.SetWorkspacePath(other.ID, outside, other.Branch); err != nil {
		t.Fatal(err)
	}
	out = rmOut(t, d.workspaceRm, map[string]any{"workspace": other.ID, "force": true, "discardFiles": true})
	if !exists(filepath.Join(outside, "keep")) || out["left"] != outside {
		t.Fatalf("outside home: reply %v, directory there %v", out, exists(outside))
	}
}

// 项目目录不在了：project rm 不带 force 提示可以加；带 force 连同 worktree 工作区一起移除，
// 目录留着、回复里列出来；再带 discardFiles 才删目录。
func TestRemoveProjectWhenDirGone(t *testing.T) {
	d, _ := newTestDaemon(t)
	proj, ws := orphanProject(t, d, "kept", "a", "b")
	err := rmCall(d.projectRm, map[string]any{"project": proj.ID})
	if err == nil || !strings.Contains(err.Error(), "pass force") {
		t.Fatalf("project rm without force: %v", err)
	}
	out := rmOut(t, d.projectRm, map[string]any{"project": proj.ID, "force": true})
	if _, err := d.store.FindProject(proj.ID); err == nil {
		t.Fatal("project kept")
	}
	left, _ := out["left"].([]any)
	if len(left) != 2 || out["repoGone"] != true || !exists(ws["a"].Path) || !exists(ws["b"].Path) {
		t.Fatalf("force without discardFiles: reply %v", out)
	}

	proj, ws = orphanProject(t, d, "gone", "a", "b")
	out = rmOut(t, d.projectRm, map[string]any{"project": proj.ID, "force": true, "discardFiles": true})
	if exists(ws["a"].Path) || exists(ws["b"].Path) || out["left"] != nil {
		t.Fatalf("discardFiles: reply %v", out)
	}
}

// 仓库还在：discardFiles 不起作用，带不带结果都和以前一样（git 删掉 worktree，回复只有 removed）；
// project rm --force 照旧要求先删 worktree 工作区。
func TestRemoveWithRepoPresentUnchanged(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	repo := filepath.Join(d.cfg.Home, "src", "repo")
	gitInit(t, repo)
	proj := store.Project{ID: store.NewID("p_"), Name: "repo", Path: repo, Kind: "git", CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	for _, discard := range []bool{false, true} {
		w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "w" + strconv.FormatBool(discard), Mode: "worktree", NoSetup: true})
		if err != nil {
			t.Fatal(err)
		}
		out := rmOut(t, d.workspaceRm, map[string]any{"workspace": w.ID, "force": true, "discardFiles": discard})
		if exists(w.Path) || len(out) != 1 || out["removed"] != w.ID {
			t.Fatalf("discardFiles %v: reply %v, directory there %v", discard, out, exists(w.Path))
		}
	}
	if _, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "x", Mode: "worktree", NoSetup: true}); err != nil {
		t.Fatal(err)
	}
	for _, discard := range []bool{false, true} {
		if err := rmCall(d.projectRm, map[string]any{"project": proj.ID, "force": true, "discardFiles": discard}); err == nil || !strings.Contains(err.Error(), "remove it first") {
			t.Fatalf("project rm with the directory present: %v", err)
		}
	}
}

// 多仓库 worktree 工作区：仓库所在的项目目录没了，带 force 只删记录、留目录，带 discardFiles 连目录删。
func TestRemoveMultiRepoWhenSourcesGone(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	dir := filepath.Join(d.cfg.Home, "src", "works")
	gitInit(t, filepath.Join(dir, "a"))
	gitInit(t, filepath.Join(dir, "b"))
	proj := store.Project{ID: store.NewID("p_"), Name: "works", Path: dir, Kind: "folder", CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	var ws []store.Workspace
	for _, n := range []string{"keep", "discard"} {
		w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: n, Repos: []string{"a", "b"}, NoSetup: true})
		if err != nil {
			t.Fatal(err)
		}
		ws = append(ws, w)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := rmCall(d.workspaceRm, map[string]any{"workspace": ws[0].ID}); err == nil || !strings.Contains(err.Error(), "pass force") {
		t.Fatalf("rm without force: %v", err)
	}
	out := rmOut(t, d.workspaceRm, map[string]any{"workspace": ws[0].ID, "force": true, "deleteBranch": true})
	if !exists(ws[0].Path) || out["left"] != ws[0].Path || out["repoGone"] != true {
		t.Fatalf("force without discardFiles: reply %v", out)
	}
	out = rmOut(t, d.workspaceRm, map[string]any{"workspace": ws[1].ID, "force": true, "discardFiles": true})
	if exists(ws[1].Path) || out["left"] != nil {
		t.Fatalf("discardFiles: reply %v", out)
	}
	if err := rmCall(d.projectRm, map[string]any{"project": proj.ID}); err != nil {
		t.Fatalf("project rm after its worktrees are gone: %v", err)
	}
}

// worktrees 里中间一级是指到 home 外面的符号链接：路径字面上在里面，带 discard 也不删外面的目录；
// 最后一级是链接时只删链接。
func TestRemoveLeftoverBehindSymlink(t *testing.T) {
	d, _ := newTestDaemon(t)
	root := filepath.Join(d.cfg.Home, "worktrees")
	outside := t.TempDir()
	victim := filepath.Join(outside, "victim")
	os.MkdirAll(victim, 0o755)
	os.WriteFile(filepath.Join(victim, "keep"), []byte("x"), 0o644)
	os.MkdirAll(root, 0o755)
	if err := os.Symlink(outside, filepath.Join(root, "link-out")); err != nil {
		t.Fatal(err)
	}
	through := filepath.Join(root, "link-out", "victim")
	if left := d.removeLeftover(through, true); left != through || !exists(filepath.Join(victim, "keep")) {
		t.Fatalf("through a link: left %q, outside directory there %v", left, exists(victim))
	}

	os.MkdirAll(filepath.Join(root, "proj"), 0o755)
	last := filepath.Join(root, "proj", "ws")
	if err := os.Symlink(victim, last); err != nil {
		t.Fatal(err)
	}
	if left := d.removeLeftover(last, true); left != "" || !exists(filepath.Join(victim, "keep")) {
		t.Fatalf("link as the last part: left %q, outside directory there %v", left, exists(victim))
	}
}
