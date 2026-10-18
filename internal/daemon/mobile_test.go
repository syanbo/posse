package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"hive/internal/history"
	"hive/internal/rpc"
)

// 手机端历史转给常驻进程的 history.* 接口：列表、搜索和原来一样；继续一个不存在的会话报 not_found。
func TestMobileHistory(t *testing.T) {
	d, w := newTestDaemon(t)
	roots := t.TempDir()
	d.cfg.HistoryRoots = &history.Roots{Home: roots}
	t.Cleanup(d.closeHistory)
	sid := "55555555-5555-4555-8555-555555555555"
	path := filepath.Join(roots, ".claude", "projects", "-proj", sid+".jsonl")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"type":"user","cwd":"`+w.Path+`","sessionId":"`+sid+`","timestamp":"2026-10-01T10:00:00Z","message":{"role":"user","content":"手机上看历史"}}
`), 0o644)
	h := mobileHost{d}
	ctx := context.Background()
	d.refreshHistory(ctx, true, 0)
	list, err := h.History(ctx, "history.list", json.RawMessage(`{"limit":50}`))
	if err != nil {
		t.Fatal(err)
	}
	if ss := list.(historyListResult).Sessions; len(ss) != 1 || ss[0].ID != sid || ss[0].Path != path {
		t.Fatalf("list %+v", ss)
	}
	found, err := h.History(ctx, "history.search", json.RawMessage(`{"query":"历史"}`))
	if err != nil || len(found.(historySearchResult).Hits) != 1 {
		t.Fatalf("search %+v %v", found, err)
	}
	_, err = h.History(ctx, "history.resume", json.RawMessage(`{"session":"nope"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeNotFound {
		t.Fatalf("resume unknown %v", err)
	}
}
