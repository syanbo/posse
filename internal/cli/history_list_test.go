package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/rpc"
)

// 文本列表把子会话缩进打在父会话下面，时间是它自己的。
func TestHistoryListPrintsChildren(t *testing.T) {
	dir, err := os.MkdirTemp("", "hl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	now := time.Now()
	srv := rpc.NewServer()
	srv.Handle("history.list", func(context.Context, json.RawMessage) (any, error) {
		return map[string]any{"sessions": []history.Session{{
			Agent: "claude", ID: "parent-id", Title: "parent", UpdatedAt: now.Add(-48 * time.Hour), Messages: 3,
			Children: []history.Session{{Agent: "claude", ID: "child-id", Title: "child", UpdatedAt: now, Messages: 1}},
		}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)
	var out, errOut bytes.Buffer
	if err := cmdHistoryList(&env{home: dir, socket: sock, out: &out, errOut: &errOut}, nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "parent-id") || !strings.HasPrefix(lines[2], "  child-id") {
		t.Fatalf("output:\n%s", out.String())
	}
	if !strings.Contains(lines[1], "2d") || strings.Contains(lines[2], "2d") {
		t.Fatalf("times should be each session's own:\n%s", out.String())
	}
}
