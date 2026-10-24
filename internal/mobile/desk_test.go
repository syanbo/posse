package mobile

import (
	"context"
	"encoding/base64"
	"testing"
	"time"

	"hive/internal/term"
)

func TestDeskSubscribe(t *testing.T) {
	reqs := make(chan DeskRequest, 2)
	h := &fakeHost{terms: map[string]*term.Terminal{}}
	busy := false
	punched := make(chan string, 1)
	h.desk = func(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error) {
		if busy {
			return DeskReady{}, DeskError("busy", "someone else")
		}
		reqs <- req
		go func() {
			<-ctx.Done()
			notify(DeskEvent{Type: "end", Reason: "unsubscribed"})
		}()
		return DeskReady{Port: 4321, Codec: "hevc", Relay: "203.0.113.1:443", RelayToken: make([]byte, 16),
			Punch: func(ctx context.Context, phone string) (string, error) {
				punched <- phone
				return "198.51.100.2:5000", nil
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
	sub := map[string]any{"width": 2556, "height": 1179, "codecs": []string{"hevc", "h264"}}
	if m := p.next(p.call("desk.subscribe", sub)); m["ok"] != false {
		t.Fatalf("subscribed without hive.desk.v1: %v", m)
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "hive-flutter", "version": 1, "capabilities": []string{"hive.desk.v1"},
	})))

	// 没在看桌面时不能打洞
	if m := p.next(p.call("desk.punch", map[string]any{"addr": "192.0.2.7:1234"})); m["ok"] != false {
		t.Fatalf("punched without watching: %v", m)
	}

	id := p.call("desk.subscribe", sub)
	ready := result(t, p.next(id))
	if ready["type"] != "ready" || ready["port"] != float64(4321) || ready["codec"] != "hevc" {
		t.Fatalf("ready %v", ready)
	}
	req := <-reqs
	nonce, _ := base64.StdEncoding.DecodeString(ready["nonceB64"].(string))
	if string(nonce) != string(req.Nonce) || len(nonce) != 16 {
		t.Fatalf("nonce %x vs %x", nonce, req.Nonce)
	}
	// 主机发出的会话密钥在前：手机的收密钥就是主机的发密钥
	c := onlyConn(t, srv)
	if len(req.Secret) != 64 || string(req.Secret[:32]) != string(c.sess.sendKey[:]) || string(req.Secret[32:]) != string(c.sess.recvKey[:]) {
		t.Fatal("secret is not sendKey||recvKey")
	}
	if req.LongEdge != 2556 {
		t.Fatalf("long edge %d", req.LongEdge)
	}
	if ready["relay"] != "203.0.113.1:443" {
		t.Fatalf("relay %v", ready["relay"])
	}
	if r := result(t, p.next(p.call("desk.punch", map[string]any{"addr": "192.0.2.7:1234"}))); r["addr"] != "198.51.100.2:5000" {
		t.Fatalf("punch %v", r)
	}
	if got := <-punched; got != "192.0.2.7:1234" {
		t.Fatalf("phone address %q", got)
	}

	result(t, p.next(p.call("desk.unsubscribe", map[string]any{})))
	end, ok := p.nextWithin(id, 5*time.Second)
	if !ok {
		t.Fatal("no end after unsubscribe")
	}
	if r := result(t, end); r["type"] != "end" || r["reason"] != "unsubscribed" {
		t.Fatalf("end %v", r)
	}

	busy = true
	m := p.next(p.call("desk.subscribe", sub))
	if e, _ := m["error"].(map[string]any); m["ok"] != false || e["code"] != "busy" {
		t.Fatalf("busy %v", m)
	}
}
