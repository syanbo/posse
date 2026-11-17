package daemon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/internal/writecoord"
)

func TestWriteResourceLinkClaims(t *testing.T) {
	d, _ := newTestDaemon(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"data/child", "links", "chain"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for path, target := range map[string]string{"links/project": "../chain/target", "chain/target": "../data/child"} {
		if err := os.Symlink(target, filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	path := root + "/links/project/../first"
	scope, err := writeScope(t.Context(), []string{path}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(scope.Paths, []string{resourceKey(filepath.Join(root, "data/first"))}) {
		t.Fatalf("link/.. changed meaning: %+v", scope)
	}
	for _, link := range []string{"links/project", "chain/target"} {
		if !slices.Contains(scope.SharedPaths, resourceKey(filepath.Join(root, link))) {
			t.Fatalf("link %s missing: %+v", link, scope)
		}
	}
	release, err := d.lockWritePaths(t.Context(), []string{path})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// 共用相同链接链但修改不同文件，不应被链接本身串行化。
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	otherRelease, err := d.lockWritePaths(ctx, []string{root + "/links/project/../second"})
	if err != nil {
		t.Fatal("independent write blocked by shared aliases:", err)
	}
	otherRelease()
	for _, dir := range []string{"links", "chain"} {
		ctx, cancel := context.WithCancel(t.Context())
		observed := &resourceWaitContext{Context: ctx, queued: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			r, err := d.writes.Acquire(observed, writecoord.Scope{Paths: []string{resourceKey(filepath.Join(root, dir))}})
			if err == nil {
				r()
			}
			done <- err
		}()
		<-observed.queued
		cancel()
		if err := <-done; err != context.Canceled {
			t.Fatalf("removing %s did not wait for alias use: %v", dir, err)
		}
	}
}

func TestWriteResourceLinkChainChangesWhileWaiting(t *testing.T) {
	d, _ := newTestDaemon(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, target := range map[string]string{"first": "data", "second": "data", "link": "first"} {
		if err := os.Symlink(target, filepath.Join(root, path)); err != nil {
			t.Fatal(err)
		}
	}
	release, err := d.writes.Acquire(t.Context(), writecoord.Scope{Paths: []string{resourceKey(filepath.Join(root, "data"))}})
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	queried := make(chan struct{})
	var once sync.Once
	previous := gitx.Trace
	gitx.Trace = func(r gitx.Run) {
		if r.Done && r.Dir == filepath.Join(root, "data") {
			once.Do(func() { close(queried) })
		}
	}
	defer func() { gitx.Trace = previous }()
	done := make(chan error, 1)
	go func() {
		r, err := d.lockWritePaths(t.Context(), []string{filepath.Join(root, "link/file")})
		if err == nil {
			r()
		}
		done <- err
	}()
	<-queried
	if err := os.Remove(filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("second", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	release()
	// 最终文件路径未变，但排队时占用的旧链接链不能用于新链。
	desktopErrorCode(t, <-done, rpc.CodeConflict)
}
