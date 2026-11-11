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

	"posse/internal/agent"
	"posse/internal/rpc"
)

func TestPermissionDecision(t *testing.T) {
	allow := permissionDecision("allow")
	var doc map[string]any
	if err := json.Unmarshal([]byte(allow), &doc); err != nil {
		t.Fatal(err)
	}
	out := doc["hookSpecificOutput"].(map[string]any)
	if out["hookEventName"] != "PermissionRequest" {
		t.Fatalf("event %v", out["hookEventName"])
	}
	if out["decision"].(map[string]any)["behavior"] != "allow" {
		t.Fatalf("decision %v", out["decision"])
	}
	if permissionDecision("deny") == "" || permissionDecision("") != "" || permissionDecision("ask") != "" {
		t.Fatal("only allow and deny are written")
	}
}

// hook 参数不足也不能让 agent 看到失败：退出码 0，Cursor 照样拿到 {}。
func TestHookMissingArgs(t *testing.T) {
	for _, args := range [][]string{nil, {"claude"}, {"cursor"}} {
		var out, errOut bytes.Buffer
		e := &env{out: &out, errOut: &errOut}
		if err := cmdHook(e, args); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		want := ""
		if len(args) == 1 && args[0] == "cursor" {
			want = "{}\n"
		}
		if out.String() != want {
			t.Fatalf("%v: stdout %q, want %q", args, out.String(), want)
		}
	}
}

// 子 agent 交来的大事件（工具输出超过 1 MB）不能丢掉 agent_id，不然常驻进程把它记成主 agent 在工作。
func TestHookLargePayloadKeepsAttribution(t *testing.T) {
	dir, err := os.MkdirTemp("", "hk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan hookParams, 1)
	srv := rpc.NewServer()
	srv.Handle("hook.event", func(_ context.Context, raw json.RawMessage) (any, error) {
		var p hookParams
		json.Unmarshal(raw, &p)
		got <- p
		return map[string]any{}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.Serve(ctx, ln)

	big, _ := json.Marshal(map[string]any{"session_id": "s1", "agent_id": "sub1", "hook_event_name": "PostToolUse",
		"tool_name": "Read", "tool_response": strings.Repeat("x", 2<<20)})
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() { w.Write(big); w.Close() }()
	defer func(f *os.File) { os.Stdin = f }(os.Stdin)
	os.Stdin = r
	t.Setenv("POSSE_TERMINAL_ID", "t_1")
	t.Setenv("POSSE_SOCKET", sock)
	var out, errOut bytes.Buffer
	if err := cmdHook(&env{out: &out, errOut: &errOut}, []string{"claude", "PostToolUse"}); err != nil {
		t.Fatal(err)
	}
	p := <-got
	if len(p.Payload) == 0 || len(p.Payload) > maxHookPayload {
		t.Fatalf("payload %d bytes; stderr %s", len(p.Payload), errOut.String())
	}
	if _, ok := agent.MapEvent("claude", "PostToolUse", p.Payload); ok {
		t.Fatalf("subagent event counted for the main agent: %.200s", p.Payload)
	}
	if agent.SessionOf(p.Payload) != "s1" {
		t.Fatalf("session lost: %.200s", p.Payload)
	}
}

type hookParams struct {
	Payload json.RawMessage `json:"payload"`
}
