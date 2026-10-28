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

func TestValidName(t *testing.T) {
	for _, ok := range []string{"feat-a", "v1.2", "坠崖十年"} {
		if err := validName(ok); err != nil {
			t.Errorf("validName(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "..", "-x", "a/b", "a b", "a:b"} {
		if validName(bad) == nil {
			t.Errorf("validName(%q) accepted", bad)
		}
	}
}

func TestWithin(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{"/tmp", "/private/tmp", true},
		{"/a/b/c", "/a/b", true},
		{"/a/bc", "/a/b", false},
		{"/a", "/a/b", false},
	}
	for _, c := range cases {
		if got := within(c.path, c.dir); got != c.want {
			t.Errorf("within(%q, %q) = %v", c.path, c.dir, got)
		}
	}
}

// 别的工具建的 worktree 加成工作区：记成 folder（删工作区不碰目录），不是本项目的、重复加的都拒绝。
func TestAttachWorktree(t *testing.T) {
	d, _ := newTestDaemon(t)
	repo := filepath.Join(d.cfg.Home, "repo")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.MkdirAll(repo, 0o755)
	git(repo, "init", "-q", "-b", "main")
	git(repo, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(d.cfg.Home, "other-wt")
	git(repo, "worktree", "add", "-q", "-b", "feat", wt)
	proj := store.Project{ID: store.NewID("p_"), Name: "repo", Path: repo, Kind: "git", CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "other-wt", Mode: "attach", Path: wt})
	if err != nil {
		t.Fatal(err)
	}
	if w.Kind != "folder" || !samePath(w.Path, wt) || w.Branch != "feat" {
		t.Fatalf("attached %+v", w)
	}
	if _, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "again", Mode: "attach", Path: wt}); err == nil {
		t.Fatal("attached the same worktree twice")
	}
	if _, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "home", Mode: "attach", Path: d.cfg.Home}); err == nil {
		t.Fatal("attached a directory that is not a worktree of the project")
	}
	call(t, d.workspaceRm, map[string]any{"workspace": w.ID})
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("removing the workspace deleted the worktree: %v", err)
	}
}

// 加进来的 worktree 在外面被挪走后重新指定目录：只认这个仓库的 worktree，改完 git 的记录也指向新目录。
func TestRelocateWorkspace(t *testing.T) {
	d, _ := newTestDaemon(t)
	repo := filepath.Join(d.cfg.Home, "repo")
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	os.MkdirAll(repo, 0o755)
	git(repo, "init", "-q", "-b", "main")
	git(repo, "-c", "user.email=a@b", "-c", "user.name=a", "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(d.cfg.Home, "old", "wt")
	git(repo, "worktree", "add", "-q", "-b", "feat", wt)
	proj := store.Project{ID: store.NewID("p_"), Name: "repo", Path: repo, Kind: "git", CreatedAt: time.Now()}
	if err := d.store.AddProject(proj); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	w, _, err := d.createWorkspace(ctx, proj, workspaceCreateParams{Name: "wt", Mode: "attach", Path: wt})
	if err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(d.cfg.Home, "new-wt")
	if err := os.Rename(wt, moved); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"workspace": w.ID, "path": d.cfg.Home})
	if _, err := d.workspaceRelocate(ctx, raw); err == nil {
		t.Fatal("relocated to a directory that is not a worktree of the project")
	}
	call(t, d.workspaceRelocate, map[string]any{"workspace": w.ID, "path": moved})
	got, err := d.store.Workspace(w.ID)
	if err != nil || !samePath(got.Path, moved) || got.Branch != "feat" {
		t.Fatalf("relocated %+v %v", got, err)
	}
	real, _ := filepath.EvalSymlinks(moved)
	if out := git(repo, "worktree", "list", "--porcelain"); !strings.Contains(out, "worktree "+real+"\n") {
		t.Fatalf("git still points elsewhere:\n%s", out)
	}
}
