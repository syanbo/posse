package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posse/internal/term"
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
		t.Fatal("held a phone that did not declare posse.permission.v1")
	}

	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.permission.v1", "posse.git.v1"},
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
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.permission.v1"},
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
	if err := os.WriteFile(filepath.Join(dir, "old.png"), []byte("\x89PNG-old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd("add", "a.txt", "old.png")
	cmd("commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd("mv", "old.png", "new.png")
	if err := os.WriteFile(filepath.Join(dir, "new.png"), []byte("\x89PNG-new"), 0o644); err != nil {
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
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.git.v1"},
	})))
	st := result(t, p.next(p.call("git.status", map[string]any{"worktree": "id:p1::w1"})))
	if st["branch"] != "main" {
		t.Fatalf("status %v", st)
	}
	files, _ := st["files"].([]any)
	if len(files) != 2 || files[0].(map[string]any)["path"] != "a.txt" || files[0].(map[string]any)["added"] != float64(1) {
		t.Fatalf("files %v", files)
	}
	// 图片带上改之前和现在两张，改名的按原来的路径读旧图
	img := result(t, p.next(p.call("git.diff", map[string]any{"worktree": "id:p1::w1", "path": "new.png", "orig": "old.png"})))
	sides, _ := img["images"].(map[string]any)
	side := func(k string) string {
		m, _ := sides[k].(map[string]any)
		b, _ := base64.StdEncoding.DecodeString(fmt.Sprint(m["content"]))
		return string(b)
	}
	if side("old") != "\x89PNG-old" || side("new") != "\x89PNG-new" {
		t.Fatalf("image sides %v", img["images"])
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

// 手机端历史：没声明 posse.history.v1 的连接（Orca App）看不到；声明后转给常驻进程，报错换成看得懂的话。
func TestHistoryMethods(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
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
	if hidden := p.next(p.call("history.list", map[string]any{})); hidden["ok"] != false {
		t.Fatalf("orca connection saw history.list: %v", hidden)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.history.v1"},
	})))
	list := result(t, p.next(p.call("history.search", map[string]any{"query": "bug", "limit": 50})))
	ss, _ := list["sessions"].([]any)
	params, _ := list["params"].(map[string]any)
	if len(ss) != 1 || params["query"] != "bug" {
		t.Fatalf("history.search %v", list)
	}
	res := p.next(p.call("history.resume", map[string]any{"session": "s1"}))
	e, _ := res["error"].(map[string]any)
	if res["ok"] != false || e["code"] != "not_found" || !strings.Contains(e["message"].(string), "加成项目") {
		t.Fatalf("history.resume %v", res)
	}
	// 声明了英文界面的连接拿英文源文本
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.history.v1"}, "locale": "en",
	})))
	res = p.next(p.call("history.resume", map[string]any{"session": "s1"}))
	e, _ = res["error"].(map[string]any)
	if res["ok"] != false || e["code"] != "not_found" || !strings.Contains(e["message"].(string), "add the folder as a project") {
		t.Fatalf("history.resume in English %v", res)
	}
}

// 套餐用量：没声明 posse.usage.v1 的连接看不到；声明后原样拿到常驻进程的读数。
func TestUsageMethod(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
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
	if hidden := p.next(p.call("usage.get", nil)); hidden["ok"] != false {
		t.Fatalf("orca connection saw usage.get: %v", hidden)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.usage.v1"},
	})))
	got := result(t, p.next(p.call("usage.get", nil)))
	us, _ := got["usages"].([]any)
	if len(us) != 1 || us[0].(map[string]any)["agent"] != "codex" {
		t.Fatalf("usage.get %v", got)
	}
	got = result(t, p.next(p.call("usage.refresh", nil)))
	if us, _ := got["usages"].([]any); len(us) != 1 {
		t.Fatalf("usage.refresh %v", got)
	}
}

// 会话上下文：没声明 posse.context.v1 的连接看不到；声明后按会话文件最后一条回复的用量算。
func TestSessionContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	line := `{"type":"assistant","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":10,"cache_read_input_tokens":49990,"output_tokens":0}}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
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
	args := map[string]any{"agent": "claude", "sessionId": "s1", "transcriptPath": path}
	if hidden := p.next(p.call("session.context", args)); hidden["ok"] != false {
		t.Fatalf("orca connection saw session.context: %v", hidden)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.context.v1"},
	})))
	got := result(t, p.next(p.call("session.context", args)))
	if got["known"] != true || got["percent"] != float64(25) || got["window"] != float64(200000) {
		t.Fatalf("session.context %v", got)
	}
	none := result(t, p.next(p.call("session.context", map[string]any{"agent": "grok", "sessionId": "x"})))
	if none["known"] != false {
		t.Fatalf("grok context %v", none)
	}
}

// 慢的历史查询不占着读循环：它没回之前，ping 照样回
func TestSlowHistoryDoesNotBlockPing(t *testing.T) {
	gate := make(chan struct{})
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}, historyGate: gate}, -1)
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
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.history.v1"},
	})))
	slow := p.call("history.list", map[string]any{})
	if _, ok := p.nextWithin(p.call("runtime.ping", map[string]any{}), 2*time.Second); !ok {
		t.Fatal("ping waited for history.list")
	}
	close(gate)
	result(t, p.next(slow))
}
