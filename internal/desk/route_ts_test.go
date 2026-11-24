package desk

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type eofPacketConn struct {
	reads atomic.Int32
}

func (c *eofPacketConn) ReadFrom([]byte) (int, net.Addr, error) {
	c.reads.Add(1)
	return 0, nil, io.EOF
}
func (c *eofPacketConn) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (c *eofPacketConn) Close() error                              { return nil }
func (c *eofPacketConn) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}
}
func (c *eofPacketConn) SetDeadline(time.Time) error      { return nil }
func (c *eofPacketConn) SetReadDeadline(time.Time) error  { return nil }
func (c *eofPacketConn) SetWriteDeadline(time.Time) error { return nil }

func TestRoutesTailscalePathAndDERPDetection(t *testing.T) {
	tsSock, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP ts: %v", err)
	}
	var derp atomic.Bool
	derp.Store(true)

	r, err := ListenRoutesWithOptions(RouteOptions{
		TS:       tsSock,
		TSIsDERP: func(net.Addr) bool { return derp.Load() },
	})
	if err != nil {
		tsSock.Close()
		t.Fatalf("ListenRoutesWithOptions: %v", err)
	}
	defer r.Close()

	wantPort := tsSock.LocalAddr().(*net.UDPAddr).Port
	if got := r.TSPort(); got != wantPort {
		t.Fatalf("TSPort() = %d, want %d", got, wantPort)
	}

	// 模拟手机经内置 Tailscale 发包到 tsSock
	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP phone: %v", err)
	}
	defer phone.Close()

	if _, err := phone.WriteToUDP([]byte("hi-ts"), tsSock.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("phone write: %v", err)
	}

	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, from, err := r.ReadFrom(buf)
	if err != nil {
		t.Fatalf("Routes.ReadFrom: %v", err)
	}
	if string(buf[:n]) != "hi-ts" {
		t.Fatalf("got %q, want hi-ts", buf[:n])
	}

	// 当前 tsIsDERP=true，应被判为中转
	if !r.IsRelay(from) {
		t.Fatal("expected IsRelay(from) == true when peer is on DERP")
	}

	// 回包必须原路经 tsSock 发回 phone
	if _, err := r.WriteTo([]byte("reply-ts"), from); err != nil {
		t.Fatalf("Routes.WriteTo: %v", err)
	}
	phone.SetReadDeadline(time.Now().Add(2 * time.Second))
	rn, rfrom, err := phone.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("phone read reply: %v", err)
	}
	if string(buf[:rn]) != "reply-ts" {
		t.Fatalf("phone got %q, want reply-ts", buf[:rn])
	}
	if rfrom.Port != wantPort {
		t.Fatalf("reply came from port %d, want tsSock port %d", rfrom.Port, wantPort)
	}

	// 切到 LAN 再切回 TS（此时已转为直连）：切走时应失效旧 DERP 缓存，切回后立即识别为直连
	lanAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: r.Port()}
	if _, err := phone.WriteToUDP([]byte("hi-lan"), lanAddr); err != nil {
		t.Fatalf("phone write lan: %v", err)
	}
	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from2, err := r.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "hi-lan" {
		t.Fatalf("ReadFrom lan: n=%d err=%v", n, err)
	}
	if r.IsRelay(from2) {
		t.Fatal("expected LAN packet not to be treated as TS DERP")
	}

	derp.Store(false)
	if _, err := phone.WriteToUDP([]byte("hi-ts-direct"), tsSock.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatalf("phone write ts direct: %v", err)
	}
	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from3, err := r.ReadFrom(buf)
	if err != nil || string(buf[:n]) != "hi-ts-direct" {
		t.Fatalf("ReadFrom ts direct: n=%d err=%v", n, err)
	}
	if r.IsRelay(from3) {
		t.Fatal("expected stale DERP cache cleared after LAN -> TS switch")
	}
}

func TestRoutesTSListenSharesLANPortAndStopsOnEOF(t *testing.T) {
	var requestedPort int
	eofPC := &eofPacketConn{}
	r, err := ListenRoutesWithOptions(RouteOptions{
		TSListen: func(port int) (net.PacketConn, error) {
			requestedPort = port
			return eofPC, nil
		},
	})
	if err != nil {
		t.Fatalf("ListenRoutesWithOptions: %v", err)
	}
	defer r.Close()

	if requestedPort != r.Port() {
		t.Fatalf("TSListen requested port %d, want LAN port %d", requestedPort, r.Port())
	}
	// readTS 遇到 io.EOF 应立即退出，不得空转
	time.Sleep(20 * time.Millisecond)
	if got := eofPC.reads.Load(); got > 2 {
		t.Fatalf("readTS spun on io.EOF: reads=%d", got)
	}
}

func TestCasterNotePeerUpdatesBitrateOnSameAddrRouteSwitch(t *testing.T) {
	var relay atomic.Bool
	c := &caster{
		control:    io.Discard,
		isRelay:    func(net.Addr) bool { return relay.Load() },
		haveConfig: true,
		fullW:      1920,
		fullH:      1080,
		fullBit:    PictureBitrate(1920, 1080),
		bitrate:    PictureBitrate(1920, 1080),
	}
	c.rate.reset(time.Now(), c.bitrate, relayCeil)
	peer := &net.UDPAddr{IP: net.IPv4(100, 64, 0, 9), Port: 50000}

	// 初始为直连 -> 上限应为 rateCeil (40 Mbps)
	relay.Store(false)
	c.notePeer(peer)
	c.mu.Lock()
	direct1, ceil1 := c.direct, c.rate.maxBit
	c.mu.Unlock()
	if !direct1 || ceil1 != rateCeil {
		t.Fatalf("initial direct: direct=%v ceil=%d, want true/%d", direct1, ceil1, rateCeil)
	}

	// 同一地址底层切到 DERP -> 上限应立即降至 relayCeil (20 Mbps)
	relay.Store(true)
	c.notePeer(peer)
	c.mu.Lock()
	direct2, ceil2 := c.direct, c.rate.maxBit
	c.mu.Unlock()
	if direct2 || ceil2 != relayCeil {
		t.Fatalf("after switch to DERP: direct=%v ceil=%d, want false/%d", direct2, ceil2, relayCeil)
	}

	// 同一地址切回直连 -> 上限恢复 rateCeil
	relay.Store(false)
	c.notePeer(peer)
	c.mu.Lock()
	direct3, ceil3 := c.direct, c.rate.maxBit
	c.mu.Unlock()
	if !direct3 || ceil3 != rateCeil {
		t.Fatalf("after switch back to direct: direct=%v ceil=%d, want true/%d", direct3, ceil3, rateCeil)
	}
}
