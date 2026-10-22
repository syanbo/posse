package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"hive/internal/client"
	"hive/internal/rpc"
)

// 目录被删掉的 worktree 工作区（hive 建的、加进来的）在轮询时移除；还有终端在跑的、上一级目录也不在的不动。
func TestDropGoneWorktrees(t *testing.T) {
	dir, _ := os.MkdirTemp("/tmp", "hv")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var removed []string
	srv := rpc.NewServer()
	srv.Handle("workspace.rm", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p struct{ Workspace string }
		json.Unmarshal(raw, &p)
		mu.Lock()
		removed = append(removed, p.Workspace)
		mu.Unlock()
		return map[string]string{"removed": p.Workspace}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	repo := filepath.Join(dir, "repo")
	os.MkdirAll(repo, 0o755)
	a := &app{c: &client.Client{Socket: sock}}
	ws := []workspace{
		{ID: "m", ProjectID: "p", Kind: "main", Path: repo},
		{ID: "f", ProjectID: "p", Kind: "folder", Path: repo},
		{ID: "hive", ProjectID: "p", Kind: "worktree", Path: filepath.Join(dir, "gone-a")},
		{ID: "attached", ProjectID: "p", Kind: "folder", Path: filepath.Join(dir, "gone-b")},
		{ID: "busy", ProjectID: "p", Kind: "worktree", Path: filepath.Join(dir, "gone-c"), Terminals: []terminalInfo{{Running: true}}},
		{ID: "unmounted", ProjectID: "p", Kind: "worktree", Path: filepath.Join(dir, "no-volume", "wt")},
	}
	left := a.dropGoneWorktrees(ctx, ws)
	var ids []string
	for _, w := range left {
		ids = append(ids, w.ID)
	}
	if got, want := ids, []string{"m", "f", "busy", "unmounted"}; !slices.Equal(got, want) {
		t.Fatalf("kept %v, want %v", got, want)
	}
	if !slices.Equal(removed, []string{"hive", "attached"}) {
		t.Fatalf("removed %v", removed)
	}
}
