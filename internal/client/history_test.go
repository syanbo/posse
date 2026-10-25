package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/daemon"
	"posse/internal/history"
)

func startHistoryDaemon(t *testing.T, roots string) *Client {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "hv")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := daemon.Run(ctx, daemon.Config{Home: home, HistoryRoots: &history.Roots{Home: roots}}); err != nil {
			t.Errorf("daemon: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		os.RemoveAll(home)
	})
	c := &Client{Home: home, Socket: daemon.SocketPath(home)}
	for i := 0; i < 100 && !c.Up(); i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return c
}

func TestHistoryRPC(t *testing.T) {
	roots := t.TempDir()
	project := t.TempDir()
	sid := "44444444-4444-4444-8444-444444444444"
	path := filepath.Join(roots, ".claude", "projects", "-proj", sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"type":"user","cwd":"`+project+`","sessionId":"`+sid+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"给登录页加上验证码"}}
{"type":"assistant","timestamp":"2026-10-01T10:00:02Z","message":{"role":"assistant","content":[{"type":"text","text":"captcha 已加上"}]}}
`), 0o644)

	c := startHistoryDaemon(t, roots)
	ctx := context.Background()

	// 第一次查询触发建索引，等它完成后能搜到
	var res struct {
		Hits   []history.Hit
		Status struct{ Sessions int }
	}
	if err := c.Call(ctx, "history.search", map[string]any{"query": "验证码"}, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].ID != sid || res.Status.Sessions != 1 {
		t.Fatalf("search = %+v", res)
	}
	if !strings.Contains(res.Hits[0].Snippet, "验证码") || len(res.Hits[0].Spans) != 1 {
		t.Errorf("snippet = %q %v", res.Hits[0].Snippet, res.Hits[0].Spans)
	}

	var list struct{ Sessions []history.Session }
	if err := c.Call(ctx, "history.list", map[string]any{"agent": "claude"}, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Title != "给登录页加上验证码" {
		t.Fatalf("list = %+v", list)
	}
	if err := c.Call(ctx, "history.list", map[string]any{"agent": "nope"}, nil); rpcCode(err) != "bad_request" {
		t.Errorf("unknown agent: %v", err)
	}

	var show struct {
		Session  history.Session
		Messages []history.Message
	}
	if err := c.Call(ctx, "history.show", map[string]any{"session": sid[:8]}, &show); err != nil {
		t.Fatal(err)
	}
	if len(show.Messages) != 2 || show.Messages[1].Text != "captcha 已加上" {
		t.Fatalf("show = %+v", show)
	}

	// 新写入的会话：过期前的查询看不到，强制更新后能看到
	sid2 := "55555555-5555-4555-8555-555555555555"
	os.WriteFile(filepath.Join(filepath.Dir(path), sid2+".jsonl"), []byte(`{"type":"user","cwd":"/gone","sessionId":"`+sid2+`","timestamp":"2026-10-02T10:00:00Z","message":{"role":"user","content":"newer session"}}
`), 0o644)
	if err := c.Call(ctx, "history.index", map[string]any{"wait": true}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.Call(ctx, "history.search", map[string]any{"query": "newer"}, &res); err != nil || len(res.Hits) != 1 {
		t.Fatalf("after reindex: %+v %v", res, err)
	}

	// 恢复会话：目录没有对应的工作区、目录已不存在、会话不存在
	if err := c.Call(ctx, "history.resume", map[string]any{"session": sid}, nil); rpcCode(err) != "not_found" || !strings.Contains(err.Error(), project) {
		t.Errorf("resume without workspace: %v", err)
	}
	if err := c.Call(ctx, "history.resume", map[string]any{"session": sid2}, nil); rpcCode(err) != "conflict" {
		t.Errorf("resume with missing dir: %v", err)
	}
	if err := c.Call(ctx, "history.resume", map[string]any{"session": "nope"}, nil); rpcCode(err) != "not_found" {
		t.Errorf("resume unknown: %v", err)
	}
}
