package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"posse/internal/agent"
	"posse/internal/rpc"
)

func TestChatEnsureDesktop(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := filepath.Join(d.cfg.Home, "target-chat")
	if err := agent.SetChatDir(d.cfg.Home, dir); err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	var status statusResult
	if err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: "daemon.status"}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Chat.Path != dir || status.Chat.WorkspaceID != "" {
		t.Fatalf("location = %+v", status.Chat)
	}
	params, _ := json.Marshal(rpc.ChatEnsureParams{ExpectedPath: dir, Name: "Chat"})
	p := rpc.DesktopCall{Desktop: viewer, Method: "chat.ensure", Sequence: 1, Params: params}
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", p, nil), rpc.CodeReadOnly)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("viewer created directory: %v", err)
	}
	p.Desktop = desktopTakeTest(t, d, "controller")
	var first, again rpc.ChatLocation
	if err := rpc.Call(ctx, socket, "desktop.call", p, &first); err != nil {
		t.Fatal(err)
	}
	if first.Path != dir || first.WorkspaceID == "" {
		t.Fatalf("created = %+v", first)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("directory missing: %v", err)
	}
	desktopTakeTest(t, d, "other")
	if err := rpc.Call(ctx, socket, "desktop.call", p, &again); err != nil || again != first {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	p.Sequence++
	desktopErrorCode(t, rpc.Call(ctx, socket, "desktop.call", p, nil), rpc.CodeReadOnly)
	ps, err := d.store.Projects()
	if err != nil || len(ps) != 2 {
		t.Fatalf("projects = %v, %v", ps, err)
	}
}

func TestChatEnsureExistingAndConcurrent(t *testing.T) {
	d, _ := newTestDaemon(t)
	dir := filepath.Join(d.cfg.Home, "target-chat")
	if err := agent.SetChatDir(d.cfg.Home, dir); err != nil {
		t.Fatal(err)
	}
	// 同名项目占用了界面标题，目标端直接选目录名，不靠失败后重发请求。
	call(t, d.projectAdd, map[string]any{"path": t.TempDir(), "name": "Chat"})
	params, _ := json.Marshal(rpc.ChatEnsureParams{ExpectedPath: dir, Name: "Chat"})
	var wg sync.WaitGroup
	results := make(chan rpc.ChatLocation, 3)
	for range 3 {
		wg.Go(func() {
			v, err := d.chatEnsure(context.Background(), params)
			if err != nil {
				t.Error(err)
				return
			}
			results <- v.(rpc.ChatLocation)
		})
	}
	wg.Wait()
	close(results)
	var first rpc.ChatLocation
	for v := range results {
		if first.WorkspaceID == "" {
			first = v
		}
		if first != v {
			t.Fatalf("duplicate workspaces: %+v / %+v", first, v)
		}
	}
	if first.WorkspaceID == "" {
		t.Fatal("no chat workspace")
	}
	ps, err := d.store.Projects()
	if err != nil || len(ps) != 3 {
		t.Fatalf("projects = %v, %v", ps, err)
	}
	p, err := d.store.FindProject(dir)
	if err != nil || p.Name != "target-chat" {
		t.Fatalf("fallback name = %+v, %v", p, err)
	}
	alias := filepath.Join(d.cfg.Home, "chat-link")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	if err := agent.SetChatDir(d.cfg.Home, alias); err != nil {
		t.Fatal(err)
	}
	location, err := d.chatLocation()
	if err != nil || location != first {
		t.Fatalf("alias = %+v, %v", location, err)
	}
	params, _ = json.Marshal(rpc.ChatEnsureParams{ExpectedPath: alias, Name: "Renamed"})
	if v, err := d.chatEnsure(context.Background(), params); err != nil || v.(rpc.ChatLocation) != first {
		t.Fatalf("existing = %+v, %v", v, err)
	}
}

func TestChatEnsureChangedAndFailure(t *testing.T) {
	d, _ := newTestDaemon(t)
	old, next := filepath.Join(d.cfg.Home, "old"), filepath.Join(d.cfg.Home, "next")
	if err := agent.SetChatDir(d.cfg.Home, next); err != nil {
		t.Fatal(err)
	}
	params, _ := json.Marshal(rpc.ChatEnsureParams{ExpectedPath: old, Name: "Chat"})
	_, err := d.chatEnsure(context.Background(), params)
	desktopErrorCode(t, err, rpc.CodeConflict)
	for _, path := range []string{old, next} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("changed config created %s: %v", path, err)
		}
	}
	if err := os.WriteFile(next, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	params, _ = json.Marshal(rpc.ChatEnsureParams{ExpectedPath: next, Name: "Chat"})
	if _, err := d.chatEnsure(context.Background(), params); err == nil {
		t.Fatal("file accepted as directory")
	}
	if data, err := os.ReadFile(next); err != nil || string(data) != "keep" {
		t.Fatal("existing file changed")
	}
	ps, err := d.store.Projects()
	if err != nil || len(ps) != 1 {
		t.Fatalf("failed creation registered a project: %v, %v", ps, err)
	}
}
