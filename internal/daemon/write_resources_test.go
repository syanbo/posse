package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"

	"posse/internal/agent"
	"posse/internal/files"
	"posse/internal/mobile"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/writecoord"
)

func resourceGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TRACE2_EVENT=0", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.invalid", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.invalid")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestWriteResourcesCommonDirAndPaths(t *testing.T) {
	dir := t.TempDir()
	repo, wt := filepath.Join(dir, "repo"), filepath.Join(dir, "worktree")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	resourceGit(t, repo, "init", "-q", "-b", "main")
	resourceGit(t, repo, "commit", "-q", "--allow-empty", "-m", "initial")
	resourceGit(t, repo, "worktree", "add", "-q", "-b", "side", wt)
	a, err := writeScope(t.Context(), []string{filepath.Join(repo, "new", "file")}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := writeScope(t.Context(), []string{filepath.Join(wt, "new")}, nil, false)
	if err != nil || len(a.Keys) != 1 || !slices.Equal(a.Keys, b.Keys) {
		t.Fatalf("common-dir identities: %+v %+v %v", a, b, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	alias, err := writeScope(t.Context(), []string{filepath.Join(link, "new", "file")}, nil, false)
	if err != nil || !slices.Equal(a.Paths, alias.Paths) || !slices.Equal(a.Keys, alias.Keys) {
		t.Fatalf("alias identity: %+v %v", alias, err)
	}
	if err := os.Mkdir(filepath.Join(repo, "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(dir, "deep-link")
	if err := os.Symlink(filepath.Join(repo, "deep"), deep); err != nil {
		t.Fatal(err)
	}
	got, err := resourcePath(deep + "/../new/file")
	realRepo, _ := filepath.EvalSymlinks(repo)
	if err != nil || got != filepath.Join(realRepo, "new", "file") {
		t.Fatalf("link/.. identity: %q %v", got, err)
	}
}

func TestWorkspaceWriteLifecycle(t *testing.T) {
	d, _ := newTestDaemon(t)
	ctx, finish, err := d.beginWorkspaceOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	nested, nestedFinish, err := d.beginWorkspaceOperation(ctx)
	if err != nil || nested != ctx {
		t.Fatalf("nested registration: %v", err)
	}
	nestedFinish()
	if d.desktop.active != 1 {
		t.Fatalf("active = %d", d.desktop.active)
	}
	desktopErrorCode(t, d.desktop.blockHandoff(), rpc.CodeBusy)
	stopped := make(chan struct{})
	go func() { d.desktop.shutdown(); close(stopped) }()
	<-ctx.Done()
	d.desktop.mu.Lock()
	active := d.desktop.active
	d.desktop.mu.Unlock()
	if active != 1 {
		t.Fatalf("shutdown did not retain operation: %d", active)
	}
	finish()
	finish()
	<-stopped
	if d.desktop.active != 0 || len(d.desktop.workspaceOperations) != 0 {
		t.Fatal("finished operation retained")
	}
	_, _, err = d.beginWorkspaceOperation(context.Background())
	desktopErrorCode(t, err, rpc.CodeBusy)
}

func TestWorkspaceWriteHandoffBlocksIndependentEntries(t *testing.T) {
	d, w := newTestDaemon(t)
	if err := d.desktop.blockHandoff(); err != nil {
		t.Fatal(err)
	}
	defer d.desktop.unblock()
	for name, handler := range map[string]rpc.Handler{
		"project.add": d.projectAdd, "project.set": d.projectSet, "project.rm": d.projectRm,
		"workspace.create": d.workspaceCreate, "workspace.rm": d.workspaceRm,
		"workspace.relocate": d.workspaceRelocate, "workspace.merge": d.workspaceMerge, "task.start": d.taskStart,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := handler(t.Context(), json.RawMessage(`{}`))
			desktopErrorCode(t, err, rpc.CodeBusy)
		})
	}
	_, _, err := (mobileHost{d}).BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: w.ID, WorkspacePath: w.Path, Root: w.Path})
	desktopErrorCode(t, err, rpc.CodeBusy)
}

func TestWorkspaceWriteWaitCancellation(t *testing.T) {
	d, main := newTestDaemon(t)
	proj, err := d.store.FindProject(main.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	w, _, err := d.createWorkspace(t.Context(), proj, workspaceCreateParams{Name: "extra", Mode: "folder"})
	if err != nil {
		t.Fatal(err)
	}
	real, err := resourcePath(w.Path)
	if err != nil {
		t.Fatal(err)
	}
	release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Paths: []string{resourceKey(real)}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		p, _ := json.Marshal(workspaceRmParams{Workspace: w.ID})
		_, err := d.workspaceRm(ctx, p)
		done <- err
	}()
	waitFor(t, "accepted independent workspace write", func() bool {
		d.desktop.mu.Lock()
		defer d.desktop.mu.Unlock()
		return d.desktop.active == 1
	})
	desktopErrorCode(t, d.desktop.blockHandoff(), rpc.CodeBusy)
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("wait cancellation: %v", err)
	}
	if _, err := d.store.Workspace(w.ID); err != nil {
		t.Fatalf("canceled removal changed workspace: %v", err)
	}
	release()
	p, _ := json.Marshal(workspaceRmParams{Workspace: w.ID})
	if _, err := d.workspaceRm(t.Context(), p); err != nil {
		t.Fatal(err)
	}
}

type resourceWaitContext struct {
	context.Context
	queued chan struct{}
	once   sync.Once
}

func (c *resourceWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.queued) })
	return c.Context.Done()
}

func TestMobileWriteUsesSharedResources(t *testing.T) {
	d, w := newTestDaemon(t)
	ctx, release, err := (mobileHost{d}).BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: w.ID, WorkspacePath: w.Path, Root: w.Path})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	desktopErrorCode(t, d.desktop.blockHandoff(), rpc.CodeBusy)
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	// 手机持锁时，文件保存的同路径申请必须等到取消，不能先获得锁。
	scope, err := writeScope(t.Context(), []string{filepath.Join(w.Path, "new")}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	waiting, cancel := context.WithCancel(t.Context())
	defer cancel()
	observed := &resourceWaitContext{Context: waiting, queued: make(chan struct{})}
	canceled := make(chan error, 1)
	go func() {
		unlock, err := d.writes.Acquire(observed, scope)
		if err == nil {
			unlock()
			canceled <- errors.New("file write passed mobile checkout")
			return
		}
		canceled <- err
	}()
	<-observed.queued
	cancel()
	if err := <-canceled; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	unlock, err := d.lockWritePaths(t.Context(), []string{filepath.Join(w.Path, "new")})
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	path := filepath.Join(w.Path, "new")
	version, err := files.ReadVersion(t.Context(), path)
	if err != nil || !version.Stamp.Missing {
		t.Fatalf("file baseline: %+v %v", version, err)
	}
	if _, err := files.WriteFileChecked(t.Context(), path, version, func(out io.Writer) error {
		_, err := io.WriteString(out, "saved after checkout")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = (mobileHost{d}).BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: "missing", WorkspacePath: w.Path, Root: w.Path})
	if err == nil {
		t.Fatal("missing workspace accepted")
	}
}

func TestWorkspaceSetupReleasesResourcesAndStops(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	d, w := newTestDaemon(t)
	resourceGit(t, w.Path, "init", "-q", "-b", "main")
	resourceGit(t, w.Path, "commit", "-q", "--allow-empty", "-m", "initial")
	barrier := t.TempDir()
	ready, fifo := filepath.Join(barrier, "ready"), filepath.Join(barrier, "wait")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	proj, err := d.store.FindProject(w.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	proj.Kind = "git"
	proj.SetupScript = "printf ready > " + agent.Quote(ready) + "; read value < " + agent.Quote(fifo)
	if err := d.store.UpdateProject(proj); err != nil {
		t.Fatal(err)
	}
	done := make(chan *setupResult, 1)
	failure := make(chan error, 1)
	go func() {
		_, setup, err := d.createWorkspace(t.Context(), proj, workspaceCreateParams{Name: "setup", Mode: "worktree"})
		failure <- err
		done <- setup
	}()
	waitFor(t, "independent setup started", func() bool { _, err := os.Stat(ready); return err == nil })
	_, release, err := d.lockProjectWrite(t.Context(), proj.ID, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	release()
	desktopErrorCode(t, d.desktop.blockHandoff(), rpc.CodeBusy)
	d.desktop.shutdown()
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	if setup := <-done; setup == nil || setup.OK {
		t.Fatalf("shutdown did not cancel setup: %+v", setup)
	}
}

func TestProjectResourceStateIncludesAllWorktrees(t *testing.T) {
	s := projectWriteState{project: store.Project{Path: "/project"}, workspaces: []store.Workspace{{Path: "/worktree", Repos: []store.WorkspaceRepo{{Path: "api", Source: "/source"}}}}}
	paths := s.paths([]string{"/destination"})
	for _, want := range []string{"/project", "/destination", "/worktree", "/worktree/api", "/source"} {
		if !slices.Contains(paths, want) {
			t.Fatalf("missing resource %q: %v", want, paths)
		}
	}
}
