package mobile

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"posse/internal/term"
)

func TestDeskResolutionDoesNotBlockConnection(t *testing.T) {
	oldTimeout := deskResolutionTimeout
	deskResolutionTimeout = 100 * time.Millisecond
	defer func() { deskResolutionTimeout = oldTimeout }()
	entered := make(chan context.Context, 4)
	var calls atomic.Int32
	h := &fakeHost{terms: map[string]*term.Terminal{}}
	h.desk = func(ctx context.Context, _ DeskRequest, notify func(DeskEvent)) (DeskReady, error) {
		go func() { <-ctx.Done(); notify(DeskEvent{Type: "end", Reason: "unsubscribed"}) }()
		return DeskReady{Port: 4321, Codec: "hevc", FPS: func(uint32) error { return nil }, Resolution: func(wait context.Context, _ uint32) error {
			calls.Add(1)
			entered <- wait
			<-wait.Done()
			// 即使后端迟到地说成功，也不能把超时或退订报成已应用。
			return nil
		}}, nil
	}
	srv, err := New(t.TempDir(), h, -1)
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
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.desk.v1"}})))
	subscribe := func() { result(t, p.next(p.call("desk.subscribe", map[string]any{"width": 800, "height": 600}))) }
	checkError := func(id, code string) {
		t.Helper()
		m, ok := p.nextWithin(id, time.Second)
		if !ok {
			t.Fatalf("no response for %s", id)
		}
		e, _ := m["error"].(map[string]any)
		if m["ok"] != false || e["code"] != code {
			t.Fatalf("response %v, want %s", m, code)
		}
	}
	subscribe()
	pending := p.call("desk.resolution", map[string]any{"resolution": 720})
	<-entered
	// 同一连接上普通请求和重复切档都不被卡住，也不为重复切档起更多等待协程。
	result(t, p.next(p.call("desk.fps", map[string]any{"fps": 30})))
	for range 3 {
		checkError(p.call("desk.resolution", map[string]any{"resolution": 1440}), "busy")
	}
	checkError(pending, "timeout")
	if calls.Load() != 1 {
		t.Fatalf("spawned %d waits", calls.Load())
	}
	pending = p.call("desk.resolution", map[string]any{"resolution": 1080})
	wait := <-entered
	result(t, p.next(p.call("desk.unsubscribe", map[string]any{})))
	select {
	case <-wait.Done():
	case <-time.After(time.Second):
		t.Fatal("unsubscribe did not cancel the resolution wait")
	}
	checkError(pending, "unavailable")
	subscribe()
	// 新观看有自己的 context，旧等待不会改到它上面。
	pending = p.call("desk.resolution", map[string]any{"resolution": 1440})
	wait = <-entered
	if wait.Err() != nil {
		t.Fatal("new watching inherited canceled context")
	}
	result(t, p.next(p.call("desk.unsubscribe", map[string]any{})))
	checkError(pending, "unavailable")
}
