package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/rpc"
	"posse/locales"
)

// 项目目录找不到时删 worktree 工作区、删项目：确认框写明原因和要删的目录，两个动作各带对的参数，
// 回车按的是只移除（不删目录）的那个。
func TestRemoveOrphanAsksAboutFiles(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "hv")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var calls []string
	srv := rpc.NewServer()
	for _, method := range []string{"workspace.rm", "project.rm"} {
		srv.Handle(method, func(_ context.Context, raw json.RawMessage) (any, error) {
			var p struct {
				Force, DiscardFiles bool
			}
			json.Unmarshal(raw, &p)
			mu.Lock()
			calls = append(calls, method+" force="+map[bool]string{true: "1", false: "0"}[p.Force]+" discard="+map[bool]string{true: "1", false: "0"}[p.DiscardFiles])
			mu.Unlock()
			return map[string]string{}, nil
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	a := previewApp(t)
	a.c = &client.Client{Socket: sock}
	wt := filepath.Join(dir, "worktrees", "Gone", "feat")
	os.MkdirAll(wt, 0o755)
	a.projects = append(a.projects, project{ID: "pg", Name: "Gone", Kind: "git"})
	mainW := workspace{ID: "wg", ProjectID: "pg", Name: "main", Kind: "main", Project: "Gone", Label: "Gone/main", Path: filepath.Join(dir, "gone"), Missing: true}
	feat := workspace{ID: "wf", ProjectID: "pg", Name: "feat", Kind: "worktree", Project: "Gone", Label: "Gone/feat", Path: wt}
	a.workspaces = append(a.workspaces, mainW, feat)
	a.indexWorkspaces()
	m := locales.In("zh-Hans")
	tt := newPreviewTester(a, false)
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(calls) == 0 {
			return ""
		}
		return calls[len(calls)-1]
	}
	expect := func(what, want string) {
		t.Helper()
		until(t, tt, what, func() bool { return last() == want })
		mu.Lock()
		calls = nil
		mu.Unlock()
	}
	open := func(ask func()) {
		t.Helper()
		ask()
		until(t, tt, "dialog", func() bool { return a.confirming && tt.HasText("只移除") && tt.HasText("连同目录删除") })
		if !strings.Contains(a.confirmBody, "找不到项目目录") || !strings.Contains(a.confirmBody, wt) {
			t.Fatalf("body %q", a.confirmBody)
		}
	}

	open(func() { a.askRemoveWorkspace(m, feat) })
	tt.Key(0, ui.KeyEnter)
	expect("enter keeps the folder", "workspace.rm force=1 discard=0")
	open(func() { a.askRemoveWorkspace(m, feat) })
	if err := tt.Click("连同目录删除"); err != nil {
		t.Fatal(err)
	}
	expect("delete folder", "workspace.rm force=1 discard=1")

	open(func() { a.askRemoveProject(m, mainW) })
	if err := tt.Click("只移除"); err != nil {
		t.Fatal(err)
	}
	expect("project keeps folders", "project.rm force=1 discard=0")
	open(func() { a.askRemoveProject(m, mainW) })
	if err := tt.Click("连同目录删除"); err != nil {
		t.Fatal(err)
	}
	expect("project deletes folders", "project.rm force=1 discard=1")
}
