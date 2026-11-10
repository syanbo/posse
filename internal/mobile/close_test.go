package mobile

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// silentWire 是断了网的手机：关闭握手等不到回应，CloseNow 才放开。
type silentWire struct{ now chan struct{} }

func (w *silentWire) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (w *silentWire) Write(context.Context, websocket.MessageType, []byte) error { return nil }

func (w *silentWire) Close(websocket.StatusCode, string) error {
	select {
	case <-w.now:
	case <-time.After(5 * time.Second): // 和 websocket 库的关闭握手时限一样
	}
	return nil
}

func (w *silentWire) CloseNow() error {
	close(w.now)
	return nil
}

// 几台断了网的手机不让常驻进程退出、交接一台一台地各等 5 秒。
func TestCloseDoesNotWaitForSilentPhones(t *testing.T) {
	defer func(v time.Duration) { closeWait = v }(closeWait)
	closeWait = 200 * time.Millisecond
	s := &Server{conns: map[*conn]struct{}{}}
	for range 3 {
		s.conns[&conn{ws: &silentWire{now: make(chan struct{})}}] = struct{}{}
	}
	start := time.Now()
	s.Close()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Close took %s", took)
	}
}
