package app

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/client"
	"posse/internal/rpc"
)

// 终端在别处关掉后列表就不再变：轮询结果一直相同、不走 apply，过了 paneGoneAfter 那一栏也要收掉。
func TestPanePruneWithStablePoll(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ppoll")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := rpc.NewServer()
	srv.Handle("project.list", func(context.Context, json.RawMessage) (any, error) { return []project{{ID: "p", Name: "p"}}, nil })
	srv.Handle("workspace.list", func(context.Context, json.RawMessage) (any, error) {
		return []workspace{{ID: "w1", ProjectID: "p", Kind: "main", Path: dir, Terminals: []terminalInfo{{ID: "t1"}, {ID: "t3"}}}}, nil
	})
	srv.Handle("daemon.status", func(context.Context, json.RawMessage) (any, error) { return daemonStatus{}, nil })
	srv.Handle("task.list", func(context.Context, json.RawMessage) (any, error) { return []taskInfo{}, nil })
	go srv.Serve(ctx, ln)
	a := &app{sectionsOpen: map[string]*bool{}, lastStatus: map[string]string{}, selected: historyBoardID, c: &client.Client{Socket: sock}}
	a.state.Panes = map[string]layoutState{"w1": twoCols([]string{"t1", "t3"}, []string{"t2"}, 0)}
	a.refresh()
	a.runPending()
	if a.lastPoll == nil || a.lastPoll.err != "" || a.paneGone["t2"].IsZero() {
		t.Fatalf("first poll %+v, gone %+v", a.lastPoll, a.paneGone)
	}
	for range 100 {
		before := a.lastPoll
		a.paneGone["t2"] = time.Now().Add(-paneGoneAfter - time.Second)
		a.refresh()
		if before == a.lastPoll {
			// 结果和上次一样
			a.runPending()
			if _, kept := a.state.Panes["w1"]; kept {
				t.Fatal("an unchanged poll skipped the overdue pane cleanup")
			}
			if a.paneDue.Load() {
				t.Fatal("still marked due after the cleanup")
			}
			return
		}
		// 结果变了（第一次的统计之类）：丢掉这次 apply，等到连续两次相同
		a.pendingMu.Lock()
		a.pending = nil
		a.pendingMu.Unlock()
	}
	t.Fatal("never got two identical polls")
}
