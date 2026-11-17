package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"posse/internal/agent"
	"posse/internal/rpc"
)

func TestHostChatViewerAndDiscovery(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	a.home, a.loaded = t.TempDir(), true
	local := filepath.Join(a.home, "local-chat")
	if err := agent.SetChatDir(a.home, local); err != nil {
		t.Fatal(err)
	}
	if a.chatPath() != "" || a.chatWorkspace() != nil {
		t.Fatal("remote chat fell back to local settings")
	}
	target := filepath.Join(t.TempDir(), "not-on-client")
	location := rpc.ChatLocation{Path: target, WorkspaceID: "target-w"}
	var writes atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if write, _ := rpc.DesktopMethod(p.Method); write {
			writes.Add(1)
		}
		switch p.Method {
		case "daemon.status":
			return daemonStatus{Chat: &location}, nil
		case "workspace.list":
			return []workspace{{ID: location.WorkspaceID, Kind: "main", Path: "/target/alias"}}, nil
		default:
			return nil, nil
		}
	}
	f.mu.Unlock()
	a.openChat(appL(), false)
	a.runPending()
	if writes.Load() != 0 || a.chatAdding != nil {
		t.Fatal("viewer created chat")
	}
	a.refresh()
	a.runPending()
	if a.chatPath() != target || a.chatWorkspace() == nil {
		t.Fatal("target chat metadata was not applied")
	}
	a.openChat(appL(), false)
	if a.selected != location.WorkspaceID || writes.Load() != 0 {
		t.Fatal("viewer could not select existing target chat")
	}
	for _, path := range []string{local, target} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("client touched %s: %v", path, err)
		}
	}
}

func TestHostChatCreateResult(t *testing.T) {
	for _, mode := range []string{"success", "unknown", "late"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			takeRequestTestControl(t, a)
			a.loaded = true
			a.selected = historyBoardID
			a.chatDir = filepath.Join(t.TempDir(), "not-on-client")
			location := rpc.ChatLocation{Path: a.chatDir, WorkspaceID: "created-w"}
			identity := *a.request().client.Desktop
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var writes atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				switch p.Method {
				case "chat.ensure":
					writes.Add(1)
					var body rpc.ChatEnsureParams
					if err := rpc.Decode(p.Params, &body); err != nil || body.ExpectedPath != location.Path || p.Desktop != identity {
						t.Error("chat target or identity changed")
					}
					close(entered)
					<-finish
					if mode == "unknown" {
						return nil, rpc.Errorf(rpc.CodeOutcomeUnknown, "desktop operation response was lost; inspect its status before trying again")
					}
					return location, nil
				case "daemon.status":
					return daemonStatus{Chat: &location}, nil
				case "workspace.list":
					return []workspace{{ID: location.WorkspaceID, Path: location.Path, Kind: "main"}}, nil
				default:
					return nil, nil
				}
			}
			f.mu.Unlock()
			// a.c 故意为空，调用必须使用捕获的 host Client。
			a.openChat(appL(), false)
			waitConnectionEvent(t, entered)
			a.openChat(appL(), false)
			if mode == "late" {
				takeRequestTestControl(t, a)
			}
			finish <- struct{}{}
			for a.chatAdding != nil {
				awaitHostUpdate(t, a)
			}
			if writes.Load() != 1 {
				t.Fatal("chat creation was replayed")
			}
			if mode == "success" && a.selected != location.WorkspaceID {
				t.Fatal("created chat was not selected")
			}
			if mode != "success" && a.selected != historyBoardID {
				t.Fatal("failed or stale result selected chat")
			}
			if mode == "unknown" && a.err == "" {
				t.Fatal("unknown outcome was hidden")
			}
			if mode == "late" && a.err != "" {
				t.Fatal("stale result reported an error")
			}
			if _, err := os.Stat(location.Path); !os.IsNotExist(err) {
				t.Fatalf("client created target directory: %v", err)
			}
		})
	}
}

func TestHostChatFolderMenu(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	a.chatDir = "/target/chat"
	term := terminalInfo{Dir: "/target/chat/1114-1234"}
	var writes atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) { writes.Add(1); return nil, nil }
	f.mu.Unlock()
	seq := 0
	menu := newPopMenu(&seq, -1)
	a.chatFolderMenu(appL(), menu, term)
	var convert int
	for _, item := range menu.items {
		if item.label == appL().ChatOpenFolder() {
			t.Fatal("remote path offered a local Finder action")
		}
		if item.label == appL().ChatConvertProject() {
			convert = item.seq
		}
	}
	if convert == 0 {
		t.Fatal("convert action missing")
	}
	seq = 0
	a.chatFolderMenu(appL(), newPopMenu(&seq, convert), term)
	a.runPending()
	if writes.Load() != 0 || a.err == "" {
		t.Fatal("viewer converted chat to project")
	}
}
