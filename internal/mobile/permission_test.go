package mobile

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hive/internal/term"
)

func TestPermissionHoldAndGit(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	tm.SetAgent(term.AgentState{Status: "waiting", SessionID: "sid"})
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)

	if srv.HoldPermission(context.Background(), "t_1", json.RawMessage(`{"tool_name":"Bash"}`), tm.LastInput) != "" {
		t.Fatal("held without a subscribed phone")
	}

	sub := p.call("nativeChat.subscribe", map[string]any{
		"agent": "claude", "sessionId": "sid", "subscriptionId": "agent:sid:live", "limit": 5,
		"capabilities": map[string]any{"transcriptPending": 1},
	})
	if result(t, p.next(sub))["type"] != "snapshot" {
		t.Fatal("missing snapshot")
	}
	if srv.HoldPermission(context.Background(), "t_1", json.RawMessage(`{"tool_name":"Bash"}`), tm.LastInput) != "" {
		t.Fatal("held a phone that did not declare hive.permission.v1")
	}

	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "hive-flutter", "version": 1, "capabilities": []string{"hive.permission.v1", "hive.git.v1"},
	})))
	denied := p.next(p.call("git.status", map[string]any{}))
	if denied["ok"] != false {
		t.Fatalf("empty worktree should fail: %v", denied)
	}
	unknown := p.next(p.call("agent.permission.decide", map[string]any{"requestId": "missing", "behavior": "allow"}))
	if unknown["ok"] != false {
		t.Fatalf("late decide should fail: %v", unknown)
	}
}

func TestPermissionDecide(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	tm.SetAgent(term.AgentState{Status: "waiting", SessionID: "sid"})
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "hive-flutter", "version": 1, "capabilities": []string{"hive.permission.v1"},
	})))
	sub := p.call("nativeChat.subscribe", map[string]any{
		"agent": "claude", "sessionId": "sid", "subscriptionId": "agent:sid:live", "limit": 5,
		"capabilities": map[string]any{"transcriptPending": 1},
	})
	if result(t, p.next(sub))["type"] != "snapshot" {
		t.Fatal("missing snapshot")
	}

	got := make(chan string, 1)
	go func() {
		got <- srv.HoldPermission(context.Background(), "t_1", json.RawMessage(`{"tool_name":"Bash","tool_input":{"command":"go test"}}`), tm.LastInput)
	}()
	frame := result(t, p.next(sub))
	if frame["type"] != "permission" || frame["tool"] != "Bash" {
		t.Fatalf("frame %v", frame)
	}
	req := frame["requestId"].(string)
	if result(t, p.next(p.call("agent.permission.decide", map[string]any{"requestId": req, "behavior": "allow"})))["behavior"] != "allow" {
		t.Fatal("decide was not accepted")
	}
	select {
	case behavior := <-got:
		if behavior != "allow" {
			t.Fatalf("behavior %q", behavior)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hold did not return")
	}

	released := make(chan string, 1)
	go func() {
		released <- srv.HoldPermission(context.Background(), "t_1", json.RawMessage(`{"tool_name":"Bash"}`), tm.LastInput)
	}()
	if result(t, p.next(sub))["type"] != "permission" {
		t.Fatal("second request was not pushed")
	}
	if err := tm.Send([]byte("x")); err != nil {
		t.Fatal(err)
	}
	select {
	case behavior := <-released:
		if behavior != "" {
			t.Fatalf("keystroke should release, got %q", behavior)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("keystroke did not release the hold")
	}
}

func TestGitStatusAndDiff(t *testing.T) {
	dir := t.TempDir()
	cmd := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	cmd("init", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd("add", "a.txt")
	cmd("commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tm, err := term.Start(term.Options{ID: "t_1", Dir: dir, Command: []string{"/bin/sh"}, Cols: 40, Rows: 10})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}, dir: dir}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	missing := p.next(p.call("git.status", map[string]any{"worktree": "id:p1::w1"}))
	if missing["ok"] != false {
		t.Fatalf("orca connection saw git.status: %v", missing)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "hive-flutter", "version": 1, "capabilities": []string{"hive.git.v1"},
	})))
	st := result(t, p.next(p.call("git.status", map[string]any{"worktree": "id:p1::w1"})))
	if st["branch"] != "main" {
		t.Fatalf("status %v", st)
	}
	files, _ := st["files"].([]any)
	if len(files) != 1 || files[0].(map[string]any)["path"] != "a.txt" || files[0].(map[string]any)["added"] != float64(1) {
		t.Fatalf("files %v", files)
	}
	diff := result(t, p.next(p.call("git.diff", map[string]any{"worktree": "id:p1::w1", "path": "a.txt"})))
	if !strings.Contains(diff["diff"].(string), "+two") {
		t.Fatalf("diff %v", diff["diff"])
	}
	bad := p.next(p.call("git.diff", map[string]any{"worktree": "id:p1::w1", "path": "../a.txt"}))
	if bad["ok"] != false {
		t.Fatalf("path escaped: %v", bad)
	}
}
