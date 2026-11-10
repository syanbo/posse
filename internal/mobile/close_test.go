package mobile

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// 真实的 WebSocket：手机断了网（连接还在、不回关闭帧），服务端读循环照常在 Read 里。Close 到 closeWait 就断开，
// 不等关闭握手的 5 秒（websocket 库的 CloseNow 在关闭中会去等那次握手，不能靠它）。
func TestCloseRealSilentWebsocket(t *testing.T) {
	defer func(v time.Duration) { closeWait = v }(closeWait)
	closeWait = 300 * time.Millisecond
	s := &Server{conns: map[*conn]struct{}{}}
	served := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		// 照 serveWire：连接自己的 ctx，读循环一直 Read(ctx)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		c := &conn{ws: ws, ctx: ctx, cancel: cancel}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		close(served)
		for {
			if _, _, err := ws.Read(ctx); err != nil {
				return
			}
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 手机连上后什么都不读：收不到关闭帧，也不回
	peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.CloseNow()
	<-served
	start := time.Now()
	s.Close()
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("Close took %s", took)
	}
}
