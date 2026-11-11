package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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

// 项目目录被删掉以后：worktree 工作区带 force 能删（只删记录和 $POSSE_HOME/worktrees 里的残留目录），
// 项目带 force 连同 worktree 工作区一起移除；不带 force 照旧报错，但提示可以加 force。
func TestRemoveWhenProjectDirGone(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx := context.Background()
	repo := filepath.Join(d.cfg.Home, "src", "repo")
	gitInit(t, repo)
	proj := store.Project{ID: store.NewID("p_"), Name: "repo", Path: repo, Kind: "git", CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	mk := func(name string) store.Workspace {
		w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: name, Mode: "worktree", NoSetup: true})
		if err != nil || w.Kind != "worktree" {
			t.Fatalf("create %s: %+v %v", name, w, err)
		}
		return w
	}
	feat, other, third := mk("feat"), mk("other"), mk("third")
	// other 的记录指到 home 外面：删记录时不碰那个目录
	outside := filepath.Join(t.TempDir(), "outside")
	os.MkdirAll(outside, 0o755)
	os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o644)
	if err := d.store.SetWorkspacePath(other.ID, outside, other.Branch); err != nil {
		t.Fatal(err)
	}

	// 项目目录还在：project rm --force 照旧要求先删 worktree 工作区
	if err := rmCall(d.projectRm, map[string]any{"project": proj.ID, "force": true}); err == nil || !strings.Contains(err.Error(), "remove it first") {
		t.Fatalf("project rm with the directory present: %v", err)
	}

	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}
	err := rmCall(d.workspaceRm, map[string]any{"workspace": feat.ID})
	if err == nil || !strings.Contains(err.Error(), "pass force") {
		t.Fatalf("rm without force: %v", err)
	}
	if err := rmCall(d.workspaceRm, map[string]any{"workspace": feat.ID, "force": true, "deleteBranch": true}); err != nil {
		t.Fatalf("rm --force: %v", err)
	}
	if _, err := d.store.Workspace(feat.ID); err == nil {
		t.Fatal("workspace record kept")
	}
	if _, err := os.Stat(feat.Path); !os.IsNotExist(err) {
		t.Fatalf("leftover directory kept: %v", err)
	}
	if err := rmCall(d.workspaceRm, map[string]any{"workspace": other.ID, "force": true}); err != nil {
		t.Fatalf("rm --force of a workspace pointing outside home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatalf("directory outside $POSSE_HOME/worktrees touched: %v", err)
	}

	err = rmCall(d.projectRm, map[string]any{"project": proj.ID})
	if err == nil || !strings.Contains(err.Error(), "pass force") {
		t.Fatalf("project rm without force: %v", err)
	}
	if err := rmCall(d.projectRm, map[string]any{"project": proj.ID, "force": true}); err != nil {
		t.Fatalf("project rm --force: %v", err)
	}
	if _, err := d.store.FindProject(proj.ID); err == nil {
		t.Fatal("project kept")
	}
	if _, err := os.Stat(third.Path); !os.IsNotExist(err) {
		t.Fatalf("leftover of %s kept: %v", third.Name, err)
	}
}

// 多仓库 worktree 工作区：仓库所在的项目目录没了，带 force 一样能删。
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
	w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "feat", Repos: []string{"a", "b"}, NoSetup: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := rmCall(d.workspaceRm, map[string]any{"workspace": w.ID}); err == nil || !strings.Contains(err.Error(), "pass force") {
		t.Fatalf("rm without force: %v", err)
	}
	if err := rmCall(d.workspaceRm, map[string]any{"workspace": w.ID, "force": true, "deleteBranch": true}); err != nil {
		t.Fatalf("rm --force: %v", err)
	}
	if _, err := os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("leftover directory kept: %v", err)
	}
	if err := rmCall(d.projectRm, map[string]any{"project": proj.ID}); err != nil {
		t.Fatalf("project rm after its worktrees are gone: %v", err)
	}
}
