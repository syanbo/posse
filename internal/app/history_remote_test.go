package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"posse/internal/history"
	"posse/internal/rpc"
)

func TestPreviewHostHistory(t *testing.T) {
	if os.Getenv("POSSE_PREVIEW_DIR") == "" {
		t.Skip("preview directory required")
	}
	for _, dark := range []bool{false, true} {
		a := previewApp(t)
		a.viewID = "remote"
		previewHistory(a, "磁盘")
		suffix := map[bool]string{false: "light", true: "dark"}[dark]
		savePNG(t, newPreviewTester(a, dark), "host-history-"+suffix+".png")
	}
}

func TestHostHistoryLocalPathGuards(t *testing.T) {
	a, f := requestTestApp(t, "remote")
	takeRequestTestControl(t, a)
	path := filepath.Join(t.TempDir(), "same-path.jsonl")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity := *a.request().client.Desktop
	var calls atomic.Int32
	f.mu.Lock()
	f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
		if p.Method != "history.trash" {
			return map[string]any{}, nil
		}
		var body struct{ Session, Path string }
		if err := rpc.Decode(p.Params, &body); err != nil || body.Path != path || body.Session != "same-id" || p.Desktop != identity {
			t.Error("trash changed target")
		}
		calls.Add(1)
		return nil, rpc.Errorf(rpc.CodeReadOnly, "desktop control is no longer held")
	}
	f.mu.Unlock()
	a.askTrashSession(appL(), &historyDetail{Session: history.Session{ID: "same-id", Agent: "claude", Path: path}})
	if a.confirmDo == nil {
		t.Fatal("missing trash confirmation")
	}
	a.confirmDo()
	awaitHostUpdate(t, a)
	if calls.Load() != 1 || a.err == "" {
		t.Fatal("target failure not reported")
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "keep" {
		t.Fatal("remote trash changed a local file")
	}
	tt := newTester(func(c *ui.Context) { a.cwdLink(c, filepath.Dir(path))() }, 600, 200)
	settle(tt)
	if _, ok := tt.Find(appL().CommonOpenInFinder()); ok {
		t.Fatal("remote directory offered local Finder")
	}
}

func TestHostHistoryQueryIdentity(t *testing.T) {
	for _, query := range []string{"", "target phrase"} {
		t.Run(query, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			identity := *a.request().client.Desktop
			a.history.wQuery = query
			a.history.req = 1
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				calls.Add(1)
				method := "history.list"
				if query != "" {
					method = "history.search"
				}
				var body struct{ Query string }
				if err := rpc.Decode(p.Params, &body); err != nil || body.Query != query || p.Method != method || p.Desktop != identity {
					t.Error("history query changed target")
				}
				return map[string]any{"sessions": []history.Session{{ID: "remote-session"}}, "hits": []history.Hit{{}}}, nil
			}
			f.mu.Unlock()
			a.request().fetchHistory(1)
			a.runPending()
			if calls.Load() != 1 || !a.history.loaded || a.history.failed != "" {
				t.Fatal("viewer query failed")
			}
			if query == "" && (len(a.history.sessions) != 1 || a.history.sessions[0].ID != "remote-session") {
				t.Fatal("wrong list")
			}
			if query != "" && len(a.history.hits) != 1 {
				t.Fatal("wrong search")
			}
		})
	}
}

func TestHostHistoryQueuedQueryExpires(t *testing.T) {
	for _, mode := range []string{"new-query", "new-query-error", "new-identity", "before-send"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			a.history.req = 1
			a.history.sessions = []history.Session{{ID: "keep"}}
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				calls.Add(1)
				if mode == "new-query-error" {
					return nil, rpc.Errorf(rpc.CodeConflict, "old failure")
				}
				return map[string]any{"sessions": []history.Session{{ID: "stale"}}}, nil
			}
			f.mu.Unlock()
			r := a.request()
			if mode == "before-send" {
				takeRequestTestControl(t, a)
			}
			r.fetchHistory(1)
			if mode == "new-identity" {
				takeRequestTestControl(t, a)
			}
			if mode == "new-query" || mode == "new-query-error" {
				a.history.mu.Lock()
				a.history.req++
				a.history.mu.Unlock()
			}
			a.runPending()
			if len(a.history.sessions) != 1 || a.history.sessions[0].ID != "keep" || a.history.failed != "" {
				t.Fatal("stale history result applied")
			}
			if mode == "before-send" && calls.Load() != 0 {
				t.Fatal("delayed query borrowed new identity")
			}
		})
	}
}

func TestHostHistoryContextFromTarget(t *testing.T) {
	for _, included := range []bool{false, true} {
		t.Run(map[bool]string{false: "old-server", true: "target-context"}[included], func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			path := filepath.Join(t.TempDir(), "same-path.jsonl")
			if err := os.WriteFile(path, []byte(`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"total_tokens":999},"model_context_window":1000}}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			session := history.Session{ID: "same-id", Agent: "codex", Path: path}
			identity := *a.request().client.Desktop
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				var body struct {
					Path    string
					Context bool
				}
				if err := rpc.Decode(p.Params, &body); err != nil || body.Path != path || !body.Context || p.Desktop != identity || p.Method != "history.show" {
					t.Error("detail request changed target")
				}
				detail := historyDetail{Session: session}
				if included {
					detail.Context = &history.Context{Used: 20, Window: 100}
				}
				return detail, nil
			}
			f.mu.Unlock()
			a.selectSession(session, -1)
			awaitHostUpdate(t, a)
			if a.history.detail == nil {
				t.Fatal("detail missing")
			}
			value := a.history.detail.Context
			if included && (value == nil || value.Used != 20) || !included && value != nil {
				t.Fatalf("local file substituted target context: %+v", value)
			}
		})
	}
}

func TestHostHistoryDetailLateResult(t *testing.T) {
	for _, mode := range []string{"new-identity", "same-session", "closed", "stale-error"} {
		t.Run(mode, func(t *testing.T) {
			a, f := requestTestApp(t, "remote")
			session := history.Session{ID: "same-id", Path: "/target/session"}
			entered, finish := make(chan struct{}), make(chan struct{})
			defer close(finish)
			var calls atomic.Int32
			f.mu.Lock()
			f.desktopCall = func(_ context.Context, p rpc.DesktopCall) (any, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-finish
					if mode == "stale-error" {
						return nil, rpc.Errorf(rpc.CodeConflict, "stale detail failure")
					}
					return historyDetail{Session: session, Context: &history.Context{Used: 1, Window: 100}}, nil
				}
				return historyDetail{Session: session, Context: &history.Context{Used: 2, Window: 100}}, nil
			}
			f.mu.Unlock()
			a.selectSession(session, -1)
			waitConnectionEvent(t, entered)
			switch mode {
			case "new-identity":
				takeRequestTestControl(t, a)
			case "closed":
				a.releaseHistory()
			default:
				a.selectSession(session, -1)
				awaitHostUpdate(t, a)
			}
			finish <- struct{}{}
			awaitHostUpdate(t, a)
			if a.err != "" {
				t.Fatal("stale detail error applied")
			}
			if mode == "same-session" || mode == "stale-error" {
				if a.history.detail == nil || a.history.detail.Context.Used != 2 {
					t.Fatal("older request replaced newer detail")
				}
			} else if a.history.detail != nil {
				t.Fatal("stale detail applied after invalidation")
			}
		})
	}
}
