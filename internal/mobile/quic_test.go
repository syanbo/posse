package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/box"

	"hive/internal/deskrelay"
	"hive/internal/mquic"
	"hive/internal/term"
)

type testQUICWire struct{ quicWire }

func (w *testQUICWire) CloseNow() error {
	w.c.CloseNow()
	return nil
}

func dialQUIC(t *testing.T, addr string, token []byte) *testQUICWire {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := mquic.Dial(ctx, addr, token)
	if err != nil {
		t.Fatal(err)
	}
	return &testQUICWire{quicWire{c: c}}
}

func quicInfoOf(t *testing.T, p *phone) QUICInfo {
	t.Helper()
	m := p.next(p.call("hive.quic", map[string]any{}))
	if m["ok"] != true {
		t.Fatalf("hive.quic failed: %v", m)
	}
	b, _ := json.Marshal(m["result"])
	var info QUICInfo
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func newQUICServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	t.Cleanup(srv.Close)
	return srv
}

func TestQUICDirect(t *testing.T) {
	srv := newQUICServer(t)
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	q := quicInfoOf(t, ws)
	if q.Port != srv.Port() || q.Relay != "" {
		t.Fatalf("quic info %+v, tcp port %d", q, srv.Port())
	}

	p := handshakePhone(t, dialQUIC(t, "127.0.0.1:"+strconv.Itoa(q.Port), nil), parseTestOffer(t, info))
	defer p.ws.CloseNow()
	if r := result(t, p.next(p.call("runtime.ping", map[string]any{}))); len(r) != 0 {
		t.Fatalf("ping result %v", r)
	}
}

// 拒绝认证时那条报错要先送到再断：直接关 QUIC 连接会丢掉还没发出去的数据。
func TestQUICCloseDeliversLastMessage(t *testing.T) {
	srv := newQUICServer(t)
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	q := quicInfoOf(t, ws)

	offer := parseTestOffer(t, info)
	offer.DeviceToken = "not-a-device"
	w := dialQUIC(t, "127.0.0.1:"+strconv.Itoa(q.Port), nil)
	defer w.CloseNow()
	ctx := context.Background()
	cpub, cpriv, _ := box.GenerateKey(rand.Reader)
	var cn [32]byte
	hb, _ := json.Marshal(map[string]any{
		"type": "e2ee_hello", "v": 2, "clientPublicKeyB64": b64(*cpub), "clientNonceB64": b64(cn),
		"capabilities": map[string]any{"framing": []int{2}, "payloadKinds": []string{"text", "binary"}},
		"context":      map[string]any{"protocol": "orca-mobile-e2ee", "initiator": "mobile", "responder": "desktop", "transport": "direct"},
	})
	w.Write(ctx, websocket.MessageText, hb)
	_, rb, err := w.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var r ready
	json.Unmarshal(rb, &r)
	h, _ := parseHello(hb)
	hostPub, _ := decode32(r.DesktopPublicKeyB64)
	dn, _ := decode32(r.DesktopNonceB64)
	var shared [32]byte
	box.Precompute(&shared, &hostPub, cpriv)
	p := &phone{t: t, ws: w, sess: newSession(shared, transcript(h, r), cn, dn)}
	p.sendRaw(map[string]any{"type": "e2ee_auth", "v": 2, "transcriptHashB64": b64(p.sess.transcriptHash), "deviceToken": offer.DeviceToken})
	_, ab, err := w.Read(ctx)
	if err != nil {
		t.Fatalf("error reply lost: %v", err)
	}
	if plain := p.open(ab, kindText); !strings.Contains(string(plain), "unauthorized") {
		t.Fatalf("reply %s", plain)
	}
	_, _, err = w.Read(ctx)
	var ce *mquic.CloseError
	if !errors.As(err, &ce) || ce.Code != 4001 {
		t.Fatalf("after error: %v", err)
	}
}

func TestQUICViaRelay(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go deskrelay.NewServer(pc).Serve()
	relay := pc.LocalAddr().String()

	srv := newQUICServer(t)
	srv.SetQUIC(QUICOptions{Relay: func() string { return relay }})
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	q := quicInfoOf(t, ws)
	if q.Relay != relay || len(q.RelayToken) != deskrelay.TokenLen {
		t.Fatalf("quic info %+v", q)
	}
	if got, _ := json.Marshal(q); !strings.Contains(string(got), base64.StdEncoding.EncodeToString(q.RelayToken)) {
		t.Fatalf("relay token should be base64 in json: %s", got)
	}

	// Mac 的登记每 10 秒一次，第一次在开中转时就发了；手机登记后等回执再握手
	p := handshakePhone(t, dialQUIC(t, relay, q.RelayToken), parseTestOffer(t, info))
	defer p.ws.CloseNow()
	if r := result(t, p.next(p.call("runtime.ping", map[string]any{}))); len(r) != 0 {
		t.Fatalf("ping result %v", r)
	}
}

func TestQUICRelayUnreachableFailsFast(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	pc.Close() // 没人应答
	start := time.Now()
	_, err = mquic.Dial(context.Background(), addr, make([]byte, deskrelay.TokenLen))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
}
