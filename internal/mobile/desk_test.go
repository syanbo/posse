package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"hive/internal/term"
)

func TestDeskSubscribe(t *testing.T) {
	reqs := make(chan DeskRequest, 2)
	h := &fakeHost{terms: map[string]*term.Terminal{}}
	busy := false
	punched := make(chan string, 1)
	switched := make(chan uint32, 1)
	windowed := make(chan uint32, 1)
	h.desk = func(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error) {
		if busy {
			return DeskReady{}, DeskError("busy", "someone else")
		}
		reqs <- req
		go func() {
			// 屏列表可能比 ready 先到，手机要照收
			notify(DeskEvent{Type: "displays", Current: 1, Displays: []map[string]any{{"id": 1, "main": true}, {"id": 2}}})
			<-ctx.Done()
			notify(DeskEvent{Type: "end", Reason: "unsubscribed"})
		}()
		return DeskReady{Port: 4321, Codec: "hevc", Relay: "203.0.113.1:443", RelayToken: make([]byte, 16),
			Punch: func(ctx context.Context, phone string) (string, error) {
				punched <- phone
				return "198.51.100.2:5000", nil
			},
			Display: func(id uint32) error {
				switched <- id
				return nil
			},
			Window: func(id uint32) error {
				windowed <- id
				return nil
			},
			Windows: func(ctx context.Context) (json.RawMessage, error) {
				return json.RawMessage(`{"windows":[{"id":77,"app":"Safari","title":"Hive"}]}`), nil
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

	if m := p.next(p.call("desk.display", map[string]any{"id": 2})); m["ok"] != false {
		t.Fatalf("switched without watching: %v", m)
	}
	if m := p.next(p.call("desk.windows", map[string]any{})); m["ok"] != false {
		t.Fatalf("listed windows without watching: %v", m)
	}

	sub["display"] = 2
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
	if req.LongEdge != 2556 || req.Display != 2 {
		t.Fatalf("long edge %d, display %d", req.LongEdge, req.Display)
	}
	// 订阅流里有屏列表
	for {
		m, ok := p.nextWithin(id, 5*time.Second)
		if !ok {
			t.Fatal("no displays event")
		}
		if r := result(t, m); r["type"] == "displays" {
			if r["current"] != float64(1) || len(r["displays"].([]any)) != 2 {
				t.Fatalf("displays %v", r)
			}
			break
		}
	}
	result(t, p.next(p.call("desk.display", map[string]any{"id": 2})))
	if got := <-switched; got != 2 {
		t.Fatalf("switched to %d", got)
	}
	ws := result(t, p.next(p.call("desk.windows", map[string]any{})))
	if list, _ := ws["windows"].([]any); len(list) != 1 || list[0].(map[string]any)["app"] != "Safari" {
		t.Fatalf("windows %v", ws)
	}
	if m := p.next(p.call("desk.window", map[string]any{})); m["ok"] != false {
		t.Fatalf("window without id: %v", m)
	}
	result(t, p.next(p.call("desk.window", map[string]any{"id": 77})))
	if got := <-windowed; got != 77 {
		t.Fatalf("window %d", got)
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
