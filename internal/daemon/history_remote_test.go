package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/history"
	"posse/internal/rpc"
)

func TestDesktopHistoryRead(t *testing.T) {
	d, w := newTestDaemon(t)
	roots := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: roots}
	t.Cleanup(d.closeHistory)
	sid := "55555555-5555-4555-8555-555555555555"
	path := filepath.Join(roots, ".claude", "projects", "-target", sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","cwd":"` + w.Path + `","sessionId":"` + sid + `","timestamp":"2026-11-14T10:00:00Z","message":{"role":"user","content":"target history phrase"}}
{"type":"assistant","sessionId":"` + sid + `","timestamp":"2026-11-14T10:00:01Z","message":{"role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"target answer"}],"usage":{"input_tokens":50000,"output_tokens":5}}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	socket := desktopServer(t, d)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	viewer := rpc.DesktopIdentity{InstanceID: d.instanceID, ClientID: "viewer"}
	query := func(method string, params any, out any) {
		t.Helper()
		raw, _ := json.Marshal(params)
		if err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: method, Params: raw}, out); err != nil {
			t.Fatal(err)
		}
	}
	var list historyListResult
	query("history.list", map[string]any{"limit": 50}, &list)
	if len(list.Sessions) != 1 || list.Sessions[0].Path != path {
		t.Fatalf("list = %+v", list)
	}
	var search historySearchResult
	query("history.search", map[string]any{"query": "phrase"}, &search)
	if len(search.Hits) != 1 {
		t.Fatalf("search = %+v", search)
	}
	var detail historyShowResult
	query("history.show", map[string]any{"session": sid, "path": path, "context": true}, &detail)
	if detail.Session.Path != path || len(detail.Messages) != 2 || detail.Context == nil || detail.Context.Used != 50000 {
		t.Fatalf("detail = %+v", detail)
	}
	var plain historyShowResult
	query("history.show", map[string]any{"session": sid, "path": path}, &plain)
	if plain.Context != nil {
		t.Fatal("unrequested context read")
	}
	if len(d.terms) != 0 {
		t.Fatal("history query launched a terminal")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != body {
		t.Fatal("history source changed")
	}
	if state := desktopTestCall(t, d.desktopStatus, nil); state.Owner != "" {
		t.Fatal("history query took control")
	}
	for _, method := range []string{"history.index", "history.resume"} {
		_, allowed := rpc.DesktopMethod(method)
		if method == "history.index" && allowed {
			t.Fatal("forced indexing was exposed as a read")
		}
		if method == "history.resume" {
			err := rpc.Call(ctx, socket, "desktop.call", rpc.DesktopCall{Desktop: viewer, Method: method, Sequence: 1, Params: json.RawMessage(`{"session":"` + sid + `"}`)}, nil)
			desktopErrorCode(t, err, rpc.CodeReadOnly)
		}
	}
}
