package app

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/rpc"
)

// 手机页打开时问中转状态失败一次（常驻进程正在交接）：过一会儿再问，卡片照样出来；不认这个方法的旧常驻进程才不再问。
func TestRelayCardRetriesAfterFailure(t *testing.T) {
	var calls atomic.Int32
	_, c := startFakeDaemon(t, map[string]rpc.Handler{"relay.status": func(ctx context.Context, raw json.RawMessage) (any, error) {
		if calls.Add(1) == 1 {
			return nil, rpc.Errorf(rpc.CodeInternal, "restarting")
		}
		return map[string]any{"configured": false}, nil
	}})
	a := &app{c: c}
	tt := newTester(func(c *ui.Context) { a.mobileRelayCard(c) }, 600, 400)
	a.loadRelay()
	r := &a.mobile.relay
	deadline := time.Now().Add(5 * time.Second)
	for r.loading && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		a.runPending()
	}
	if r.loaded || r.old {
		t.Fatalf("after a failure loaded=%v old=%v", r.loaded, r.old)
	}
	r.loadedAt = time.Now().Add(-relayRetry)
	tt.Frame()
	for !r.loaded && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		a.runPending()
	}
	if !r.loaded {
		t.Fatal("relay status not asked again")
	}

	// 旧常驻进程：不认这个方法
	_, old := startFakeDaemon(t)
	b := &app{c: old}
	b.loadRelay()
	for b.mobile.relay.loading && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		b.runPending()
	}
	if !b.mobile.relay.old {
		t.Fatal("unknown method not taken as an old daemon")
	}
}
