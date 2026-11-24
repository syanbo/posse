package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"posse/internal/mquic"
	"posse/internal/term"
)

type fakeTSProvider struct {
	mu           sync.Mutex
	ip           string
	gen          uint64
	failUDP      bool
	failTCP      bool
	udpAddr      net.Addr
	tcpAddr      net.Addr
	udpPC        net.PacketConn
	tcpLN        net.Listener
	udpListenCnt int
	tcpListenCnt int
}

func (f *fakeTSProvider) IP() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ip
}

func (f *fakeTSProvider) Generation() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ip == "" {
		return 0
	}
	if f.gen == 0 {
		return 1
	}
	return f.gen
}

func (f *fakeTSProvider) SetIP(ip string) {
	f.mu.Lock()
	f.ip = ip
	f.mu.Unlock()
}

func (f *fakeTSProvider) BumpGen() {
	f.mu.Lock()
	if f.gen == 0 {
		f.gen = 1
	}
	f.gen++
	f.mu.Unlock()
}

func (f *fakeTSProvider) ListenPacket(port int) (net.PacketConn, error) {
	f.mu.Lock()
	fail := f.failUDP
	f.mu.Unlock()
	if fail {
		return nil, errors.New("udp listen failed")
	}
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.udpAddr = pc.LocalAddr()
	f.udpPC = pc
	f.udpListenCnt++
	f.mu.Unlock()
	return pc, nil
}

func (f *fakeTSProvider) ListenTCP(port int) (net.Listener, error) {
	f.mu.Lock()
	fail := f.failTCP
	f.mu.Unlock()
	if fail {
		return nil, errors.New("tcp listen failed")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.tcpAddr = ln.Addr()
	f.tcpLN = ln
	f.tcpListenCnt++
	f.mu.Unlock()
	return ln, nil
}

func TestTailscaleListenerPairingQUICAndDeskReady(t *testing.T) {
	h := &fakeHost{terms: map[string]*term.Terminal{}}
	h.desk = func(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error) {
		return DeskReady{Port: 4321, Codec: "hevc", TSHost: "100.64.0.42", TSPort: 4321}, nil
	}
	srv, err := New(t.TempDir(), h, -1)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.port = 0
	ts := &fakeTSProvider{ip: "100.64.0.42", gen: 1}
	srv.SetQUIC(QUICOptions{Tailscale: ts})
	defer srv.Close()

	// 1. Pair 应把内置 Tailscale IP 放在首选 endpoint，并同时拉起 ts QUIC + ts TCP 监听
	pair, err := srv.Pair(false, "")
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if !strings.Contains(pair.Endpoint, "100.64.0.42:") {
		t.Fatalf("primary endpoint = %q, want 100.64.0.42", pair.Endpoint)
	}
	ts.mu.Lock()
	udpAddr, tcpAddr, udpCnt, tcpCnt := ts.udpAddr, ts.tcpAddr, ts.udpListenCnt, ts.tcpListenCnt
	ts.mu.Unlock()
	if udpCnt != 1 || tcpCnt != 1 || udpAddr == nil || tcpAddr == nil {
		t.Fatalf("expected 1 udp and 1 tcp ts listener, got udp=%d tcp=%d", udpCnt, tcpCnt)
	}

	// 2. 通过 ts TCP 监听建立 WebSocket 并完成 E2EE 握手，验证官方 Tailscale 手机首次扫码路径可用
	offer := parseTestOffer(t, pair)
	ws, _, err := websocket.Dial(context.Background(), "ws://"+tcpAddr.String(), nil)
	if err != nil {
		t.Fatalf("websocket.Dial ts tcp: %v", err)
	}
	ws.SetReadLimit(8 << 20)
	p := handshakePhone(t, ws, offer)
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.desk.v1"},
	})))

	// 3. 查询 posse.quic 应包含 tsHost 与 100.64.0.42
	res := result(t, p.next(p.call("posse.quic", map[string]any{})))
	var qi QUICInfo
	b, _ := json.Marshal(res)
	if err := json.Unmarshal(b, &qi); err != nil {
		t.Fatalf("unmarshal QUICInfo: %v", err)
	}
	if qi.TSHost != "100.64.0.42" || len(qi.Hosts) == 0 || qi.Hosts[0] != "100.64.0.42" {
		t.Fatalf("unexpected QUICInfo: %+v", qi)
	}

	// 4. 通过 ts QUIC 监听使用 mquic.DialOver + TSConfig 拨号并完成握手
	cliPC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	qc, err := mquic.DialOver(ctx, cliPC, udpAddr, mquic.TSConfig())
	if err != nil {
		t.Fatalf("DialOver ts QUIC: %v", err)
	}
	qc.CloseNow()

	// 5. desk.subscribe 的 ready 帧应携带 tsHost 与 tsPort
	sub := map[string]any{"width": 1200, "height": 800, "codecs": []string{"hevc"}}
	subID := p.call("desk.subscribe", sub)
	readyFrame := result(t, p.next(subID))
	if readyFrame["tsHost"] != "100.64.0.42" || readyFrame["tsPort"] != float64(4321) {
		t.Fatalf("desk ready missing tsHost/tsPort: %#v", readyFrame)
	}

	// 6. 同 IP 下节点重建（Generation 递增）必须重建监听
	ts.BumpGen()
	srv.TailscaleChanged()
	ts.mu.Lock()
	udpCnt2, tcpCnt2 := ts.udpListenCnt, ts.tcpListenCnt
	ts.mu.Unlock()
	if udpCnt2 != 2 || tcpCnt2 != 2 {
		t.Fatalf("same-IP gen bump did not rebuild listeners: udp=%d tcp=%d", udpCnt2, tcpCnt2)
	}

	// 7. Close 后再 TailscaleChanged 不得重建监听
	srv.Close()
	ts.BumpGen()
	srv.TailscaleChanged()
	ts.mu.Lock()
	afterCnt := ts.udpListenCnt
	ts.mu.Unlock()
	if afterCnt != 2 {
		t.Fatalf("TailscaleChanged after Close reopened listener: udpListenCnt=%d", afterCnt)
	}
}

func TestTailscalePartialFailureRecovery(t *testing.T) {
	h := &fakeHost{terms: map[string]*term.Terminal{}}
	srv, err := New(t.TempDir(), h, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	// UDP 失败、TCP 成功：TCP 必须照常打开且 Pair 宣传 TS，quicInfo 不宣传 TS
	ts := &fakeTSProvider{ip: "100.64.0.55", gen: 1, failUDP: true}
	srv.SetQUIC(QUICOptions{Tailscale: ts})
	defer srv.Close()

	pair, err := srv.Pair(false, "")
	if err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if !strings.Contains(pair.Endpoint, "100.64.0.55:") {
		t.Fatalf("expected TS endpoint when TCP succeeded, got %q", pair.Endpoint)
	}
	qi, err := srv.quicInfo("dev1")
	if err != nil {
		t.Fatalf("quicInfo: %v", err)
	}
	if qi.TSHost != "" {
		t.Fatalf("expected empty TSHost when TS UDP failed, got %q", qi.TSHost)
	}

	// 恢复 UDP 后不手动调 TailscaleChanged，直接查 quicInfo 也应自动补开 UDP（且不重建 TCP）
	ts.mu.Lock()
	ts.failUDP = false
	ts.mu.Unlock()
	qi, err = srv.quicInfo("dev1")
	if err != nil || qi.TSHost != "100.64.0.55" {
		t.Fatalf("expected quicInfo auto-recovery of TSHost, got %+v err=%v", qi, err)
	}
	ts.mu.Lock()
	udpCnt, tcpCnt := ts.udpListenCnt, ts.tcpListenCnt
	ts.mu.Unlock()
	if udpCnt != 1 || tcpCnt != 1 {
		t.Fatalf("expected independent UDP recovery (udp=1, tcp=1), got udp=%d tcp=%d", udpCnt, tcpCnt)
	}

	// 同理：首次 TCP 失败后，在条件恢复后直接再次 Pair() 也应自动补开 TCP 并宣传 TS 地址
	srv2, err := New(t.TempDir(), h, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv2.port = 0
	ts2 := &fakeTSProvider{ip: "100.64.0.56", gen: 1, failTCP: true}
	srv2.SetQUIC(QUICOptions{Tailscale: ts2})
	defer srv2.Close()
	pair1, err := srv2.Pair(false, "")
	if err != nil {
		t.Fatalf("Pair1: %v", err)
	}
	if strings.Contains(pair1.Endpoint, "100.64.0.56:") {
		t.Fatalf("expected no TS endpoint when TCP failed, got %q", pair1.Endpoint)
	}
	ts2.mu.Lock()
	ts2.failTCP = false
	ts2.mu.Unlock()
	pair2, err := srv2.Pair(false, "")
	if err != nil {
		t.Fatalf("Pair2: %v", err)
	}
	if !strings.Contains(pair2.Endpoint, "100.64.0.56:") {
		t.Fatalf("expected Pair2 to auto-recover TS TCP endpoint, got %q", pair2.Endpoint)
	}
}
