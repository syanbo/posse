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

// 真实的 WebSocket 连接，服务端照 serveWS 留住底层连接：
//   - idle：手机断了网（连接在、不回关闭帧），服务端读循环在 Read 里；
//   - busy：手机发了一条请求后断网，服务端正在处理、没在 Read；
//   - normal：连着的手机照常收到 GoingAway；
//   - revoke：撤销设备时同样不等断网的手机 5 秒。
func TestCloseRealWebsocket(t *testing.T) {
	defer func(v time.Duration) { closeWait = v }(closeWait)
	closeWait = 300 * time.Millisecond
	for _, mode := range []string{"idle", "busy", "normal", "revoke"} {
		t.Run(mode, func(t *testing.T) {
			s := &Server{conns: map[*conn]struct{}{}}
			var kept *hijackKeeper
			ready := make(chan struct{})
			release := make(chan struct{})
			defer close(release)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				keep := &hijackKeeper{ResponseWriter: w}
				kept = keep
				ws, err := websocket.Accept(keep, r, nil)
				if err != nil {
					return
				}
				defer ws.CloseNow()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				c := &conn{ws: ws, ctx: ctx, cancel: cancel, abort: keep.abort, device: Device{ID: "phone"}}
				s.mu.Lock()
				s.conns[c] = struct{}{}
				s.mu.Unlock()
				if mode == "busy" {
					if _, _, err := ws.Read(ctx); err != nil {
						return
					}
					close(ready)
					<-release // 正在处理这条请求
					return
				}
				close(ready)
				ws.Read(ctx)
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			peer, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer peer.CloseNow()
			if mode == "busy" {
				if err := peer.Write(ctx, websocket.MessageText, []byte("request")); err != nil {
					t.Fatal(err)
				}
			}
			<-ready
			peerErr := make(chan error, 1)
			if mode == "normal" {
				go func() {
					_, _, err := peer.Read(ctx)
					peerErr <- err
				}()
			}
			start := time.Now()
			if mode == "revoke" {
				s.dropDevices([]string{"phone"})
			} else {
				s.Close()
			}
			took := time.Since(start)
			if limit := closeWait + closeGrace + 200*time.Millisecond; took > limit {
				t.Fatalf("took %s, want under %s", took, limit)
			}
			if mode == "normal" {
				if err := <-peerErr; websocket.CloseStatus(err) != websocket.StatusGoingAway {
					t.Fatalf("peer got %v", err)
				}
				return
			}
			// 断网的那条也真的断开了：服务端的底层连接已经关掉，不是留着等进程退出
			if err := kept.conn.SetReadDeadline(time.Now()); err == nil {
				t.Fatal("the underlying connection is still open after Close")
			}
		})
	}
}
