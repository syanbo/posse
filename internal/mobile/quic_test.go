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
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/box"

	"posse/internal/deskrelay"
	"posse/internal/mquic"
	"posse/internal/term"
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
	m := p.next(p.call("posse.quic", map[string]any{}))
	if m["ok"] != true {
		t.Fatalf("posse.quic failed: %v", m)
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
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return deskrelay.Config{Relay: relay} }})
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

// 中转开了鉴权：Mac 用邀请码里的密钥登记开会话，手机不带密钥照常加入。
func TestQUICViaAuthRelay(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	master := deskrelay.NewMaster()
	rs := deskrelay.NewServer(pc)
	rs.SetMaster(master)
	go rs.Serve()
	admin, err := deskrelay.ParseCode(deskrelay.AdminCode(pc.LocalAddr().String(), master))
	if err != nil {
		t.Fatal(err)
	}
	code, err := admin.Invite("friend")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := deskrelay.ParseCode(code)
	if err != nil {
		t.Fatal(err)
	}

	srv := newQUICServer(t)
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return cfg }})
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	q := quicInfoOf(t, ws)
	p := handshakePhone(t, dialQUIC(t, q.Relay, q.RelayToken), parseTestOffer(t, info))
	defer p.ws.CloseNow()
	if r := result(t, p.next(p.call("runtime.ping", map[string]any{}))); len(r) != 0 {
		t.Fatalf("ping result %v", r)
	}
}

// 改了中转设置后 Mac 立刻用新设置给拿过令牌的手机登记：手机不在局域网，等不到它先调 posse.quic。
func TestQUICRelayReopensOnChange(t *testing.T) {
	listenRelay := func() string {
		pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { pc.Close() })
		go deskrelay.NewServer(pc).Serve()
		return pc.LocalAddr().String()
	}
	var mu sync.Mutex
	relay := listenRelay()
	srv := newQUICServer(t)
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config {
		mu.Lock()
		defer mu.Unlock()
		return deskrelay.Config{Relay: relay}
	}})
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	q := quicInfoOf(t, ws)
	ws.ws.CloseNow()

	mu.Lock()
	relay = listenRelay()
	mu.Unlock()
	srv.RelayChanged()
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

// 两台手机同时经中转连着，各自的请求都有回复：每台手机一个令牌，中转不会把一台的包转给另一台。
func TestQUICRelayTwoPhones(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go deskrelay.NewServer(pc).Serve()
	relay := pc.LocalAddr().String()

	srv := newQUICServer(t)
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return deskrelay.Config{Relay: relay} }})
	var phones []*phone
	var tokens [][]byte
	for range 2 {
		info, err := srv.Pair(false, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		ws := dialPhone(t, srv, info)
		defer ws.ws.CloseNow()
		q := quicInfoOf(t, ws)
		if again := quicInfoOf(t, ws); string(again.RelayToken) != string(q.RelayToken) {
			t.Fatal("token changed for the same phone")
		}
		tokens = append(tokens, q.RelayToken)
		p := handshakePhone(t, dialQUIC(t, relay, q.RelayToken), parseTestOffer(t, info))
		defer p.ws.CloseNow()
		phones = append(phones, p)
	}
	if string(tokens[0]) == string(tokens[1]) {
		t.Fatal("two phones share a relay token")
	}
	for range 4 {
		for i, p := range phones {
			m, ok := p.nextWithin(p.call("runtime.ping", map[string]any{}), 3*time.Second)
			if !ok {
				t.Fatalf("phone %d got no reply via relay", i)
			}
			result(t, m)
		}
	}
}

// 中转第一次开不了（地址解析失败）不记下来，下次 posse.quic 再试。
func TestQUICRelayRetriesAfterFailure(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go deskrelay.NewServer(pc).Serve()
	relay := "bad host:1"
	srv := newQUICServer(t)
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return deskrelay.Config{Relay: relay} }})
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	if q := quicInfoOf(t, ws); q.Relay != "" {
		t.Fatalf("relay %q from a bad address", q.Relay)
	}
	relay = pc.LocalAddr().String()
	if q := quicInfoOf(t, ws); q.Relay != relay {
		t.Fatalf("relay %q after fixing the address", q.Relay)
	}
}

// 中转令牌存在 mobile.json 里：常驻进程重启后同一台手机拿到的还是它，手机记着的地址照样能用。
func TestQUICRelayTokenSurvivesRestart(t *testing.T) {
	home := t.TempDir()
	relay := "127.0.0.1:9"
	port := 0
	start := func() *Server {
		srv, err := New(home, &fakeHost{terms: map[string]*term.Terminal{}}, -1)
		if err != nil {
			t.Fatal(err)
		}
		srv.port = port // 重启后用同一个端口，配对码里的地址才对得上
		srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return deskrelay.Config{Relay: relay} }})
		return srv
	}
	srv := start()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	first := quicInfoOf(t, ws).RelayToken
	port = srv.Port()
	ws.ws.CloseNow()
	for _, d := range srv.Devices() {
		if d.RelayToken != "" {
			t.Fatal("device list leaks the relay token")
		}
	}
	srv.Close()

	srv = start()
	defer srv.Close()
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	ws = dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	if again := quicInfoOf(t, ws).RelayToken; len(first) != deskrelay.TokenLen || string(again) != string(first) {
		t.Fatalf("relay token %x after restart, was %x", again, first)
	}
}

// 打洞：手机经中转连着，新开一个 socket 问出公网地址交给 Mac，Mac 回它中转出口的公网地址，
// 手机向那里拨 QUIC，不经中转直接连上 Mac。本机没有 NAT，走的是同一套流程。
func TestQUICPunch(t *testing.T) {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go deskrelay.NewServer(pc).Serve()
	relay := pc.LocalAddr().String()

	srv := newQUICServer(t)
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return deskrelay.Config{Relay: relay} }})
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	q := quicInfoOf(t, ws)
	viaRelay := handshakePhone(t, dialQUIC(t, relay, q.RelayToken), parseTestOffer(t, info))
	defer viaRelay.ws.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	prep, err := mquic.Prepare(ctx, relay)
	if err != nil {
		t.Fatal(err)
	}
	m := viaRelay.next(viaRelay.call("posse.punch", map[string]any{"addr": prep.Public.String()}))
	mac, _ := result(t, m)["addr"].(string)
	if mac == "" || mac == relay {
		t.Fatalf("mac address %q", mac)
	}
	c, err := prep.Dial(ctx, mac)
	if err != nil {
		t.Fatal(err)
	}
	direct := handshakePhone(t, &testQUICWire{quicWire{c: c}}, parseTestOffer(t, info))
	defer direct.ws.CloseNow()
	if c.RemoteAddr().String() != mac {
		t.Fatalf("connected to %v, want %s", c.RemoteAddr(), mac)
	}
	result(t, direct.next(direct.call("runtime.ping", map[string]any{})))
	// 地址不对时报错，不去打洞
	bad := viaRelay.next(viaRelay.call("posse.punch", map[string]any{"addr": "nope"}))
	if bad["ok"] == true {
		t.Fatal("punch accepted a bad address")
	}
}

// 常驻进程退出（重启、交接）时手机要马上知道断了：新进程紧接着听同一个端口，手机收不到端口不可达，
// 没人通知的话要等测延迟连续超时才发现。
func TestQUICCloseTellsPhone(t *testing.T) {
	srv := newQUICServer(t)
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	ws := dialPhone(t, srv, info)
	defer ws.ws.CloseNow()
	q := quicInfoOf(t, ws)
	p := handshakePhone(t, dialQUIC(t, "127.0.0.1:"+strconv.Itoa(q.Port), nil), parseTestOffer(t, info))
	defer p.ws.CloseNow()
	result(t, p.next(p.call("runtime.ping", map[string]any{})))

	start := time.Now()
	srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		_, _, err := p.ws.Read(ctx)
		if err == nil {
			continue
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("phone told after %v: %v", d, err)
		}
		return
	}
}
