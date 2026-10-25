package mobile

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stuckWire 的写一直卡到 ctx 结束，像手机被挂起后发不出去的连接。
type stuckWire struct{}

func (stuckWire) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	<-ctx.Done()
	return 0, nil, ctx.Err()
}

func (stuckWire) Write(ctx context.Context, _ websocket.MessageType, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func (stuckWire) Close(websocket.StatusCode, string) error { return nil }

func TestStalledWriteDropsOnlyASilentPhone(t *testing.T) {
	oldWrite, oldQuiet := stallWrite, stallQuiet
	stallWrite, stallQuiet = 50*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { stallWrite, stallQuiet = oldWrite, oldQuiet })
	seal := func() []byte { return []byte("x") }

	// 手机一直有帧来：写卡着也不断，直到手机不说话了
	c := &conn{ctx: context.Background(), ws: stuckWire{}}
	c.lastRead.Store(time.Now().UnixNano())
	alive := make(chan struct{})
	go func() {
		for {
			select {
			case <-alive:
				return
			case <-time.After(20 * time.Millisecond):
				c.lastRead.Store(time.Now().UnixNano())
			}
		}
	}()
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- c.write(websocket.MessageText, seal) }()
	select {
	case err := <-done:
		t.Fatalf("dropped a phone that is still talking: %v", err)
	case <-time.After(600 * time.Millisecond):
	}
	close(alive)
	select {
	case err := <-done:
		if err == nil || time.Since(start) > 2*time.Second {
			t.Fatalf("err %v after %v", err, time.Since(start))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("silent phone not dropped")
	}

	// 手机早就不说话了：卡住 stallWrite 就断
	c = &conn{ctx: context.Background(), ws: stuckWire{}}
	c.lastRead.Store(time.Now().Add(-time.Minute).UnixNano())
	start = time.Now()
	if err := c.write(websocket.MessageText, seal); err == nil || time.Since(start) > time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}
