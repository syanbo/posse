package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"posse/internal/gitx"
	"posse/internal/mobile"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/writecoord"
)

func TestWriteResourceDirectoryDisappears(t *testing.T) {
	for _, relocate := range []bool{false, true} {
		t.Run(map[bool]string{false: "add", true: "relocate"}[relocate], func(t *testing.T) {
			d, w := newTestDaemon(t)
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			real, err := resourcePath(target)
			if err != nil {
				t.Fatal(err)
			}
			release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Paths: []string{resourceKey(real)}})
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			queried := make(chan struct{})
			var once sync.Once
			previous := gitx.Trace
			gitx.Trace = func(r gitx.Run) {
				if r.Done && r.Dir == real {
					once.Do(func() { close(queried) })
				}
			}
			defer func() { gitx.Trace = previous }()
			done := make(chan error, 1)
			go func() {
				if relocate {
					p, _ := json.Marshal(workspaceRelocateParams{Workspace: w.ID, Path: target})
					_, err := d.workspaceRelocate(t.Context(), p)
					done <- err
				} else {
					p, _ := json.Marshal(projectAddParams{Path: target, Name: "new"})
					_, err := d.projectAdd(t.Context(), p)
					done <- err
				}
			}()
			// 目标目录已经通过入口检查并完成第一次身份查询，随后等资源锁。
			<-queried
			if err := os.Remove(target); err != nil {
				t.Fatal(err)
			}
			release()
			desktopErrorCode(t, <-done, rpc.CodeBadRequest)
			if current, err := d.store.Workspace(w.ID); err != nil || current.Path != w.Path {
				t.Fatalf("registered removed destination: %+v %v", current, err)
			}
			if ps, err := d.store.Projects(); err != nil || len(ps) != 1 {
				t.Fatalf("created project for removed directory: %v %v", ps, err)
			}
		})
	}
}

func TestWriteResourceBrokenLinkRecovery(t *testing.T) {
	for _, relocate := range []bool{false, true} {
		t.Run(map[bool]string{false: "remove", true: "relocate"}[relocate], func(t *testing.T) {
			d, _ := newTestDaemon(t)
			dir := t.TempDir()
			target, moved, link := filepath.Join(dir, "target"), filepath.Join(dir, "moved"), filepath.Join(dir, "link")
			if err := os.Mkdir(target, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("target", link); err != nil {
				t.Fatal(err)
			}
			p, _ := json.Marshal(projectAddParams{Path: link, Name: "linked"})
			out, err := d.projectAdd(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			proj := out.(projectInfo).Project
			ws, err := d.store.Workspaces(proj.ID)
			if err != nil || len(ws) != 1 {
				t.Fatalf("workspace: %v %v", ws, err)
			}
			if err := os.Rename(target, moved); err != nil {
				t.Fatal(err)
			}
			if relocate {
				p, _ = json.Marshal(workspaceRelocateParams{Workspace: ws[0].ID, Path: moved})
				if _, err := d.workspaceRelocate(t.Context(), p); err != nil {
					t.Fatal(err)
				}
			} else {
				p, _ = json.Marshal(projectRmParams{Project: proj.ID, Force: true})
				if _, err := d.projectRm(t.Context(), p); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(moved); err != nil {
				t.Fatalf("recovery removed actual directory: %v", err)
			}
		})
	}
}

func TestMobileIndependentRepositories(t *testing.T) {
	d, w := newTestDaemon(t)
	a, b := filepath.Join(w.Path, "a"), filepath.Join(w.Path, "b")
	for _, dir := range []string{a, b} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		resourceGit(t, dir, "init", "-q", "-b", "main")
	}
	broken := filepath.Join(w.Path, "broken")
	if err := os.Mkdir(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("gitdir: /missing/posse-test-gitdir\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := d.store.AddWorkspace(store.Workspace{ID: "broken", Name: "broken", ProjectID: w.ProjectID, Path: broken, Kind: "folder"}); err != nil {
		t.Fatal(err)
	}
	h := mobileHost{d}
	_, releaseA, err := h.BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: w.ID, WorkspacePath: w.Path, Root: a})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	_, releaseB, err := h.BeginGitWrite(t.Context(), mobile.GitWriteTarget{WorkspaceID: w.ID, WorkspacePath: w.Path, Root: b})
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()
	// 两个 checkout 都持共享登记键时，项目修改仍必须等待。
	ctx, cancel := context.WithCancel(t.Context())
	observed := &resourceWaitContext{Context: ctx, queued: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, release, err := d.lockProjectSettings(observed, w.ProjectID)
		if err == nil {
			release()
		}
		done <- err
	}()
	<-observed.queued
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("project mutation bypassed shared claims: %v", err)
	}
}
