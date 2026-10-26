package mquic

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"posse/internal/deskrelay"
)

// RelayConn 是交给 quic-go 的 UDP socket：定时向中转登记（不登记的只用来探测和打洞），
// 收包时滤掉所有控制包：中转的回执、探测的回包、对方的打洞包。
// 不嵌入 *net.UDPConn：quic-go 见到 ReadMsgUDP 会绕过 ReadFrom 直接读，回执就漏给它了。
type RelayConn struct {
	conn  *net.UDPConn
	relay *net.UDPAddr
	role  byte
	reg   func() []byte // 登记包，Mac 的带签名，每次重新生成

	done   chan struct{}
	once   sync.Once
	acked  atomic.Bool
	denied atomic.Bool
	public atomic.Pointer[netip.AddrPort] // 最近一次探测回来的公网地址

	// OnPeerPunch 在收到对方的打洞包时调用（排查用：收得到说明这边的 NAT 放进了对方的包）。
	// 在开始收包前设好
	OnPeerPunch func(from *net.UDPAddr)
}

// NewRelayConn 开始向 relay 登记，每隔 every 重发一次。cred 只有 Mac 用：服务器开了鉴权时登记要带签名。
func NewRelayConn(conn *net.UDPConn, relay *net.UDPAddr, role byte, token []byte, cred *deskrelay.Credential, every time.Duration) *RelayConn {
	reg := func() []byte { return deskrelay.Register(role, token) }
	if role == deskrelay.RoleMac {
		reg = func() []byte { return deskrelay.MacRegister(token, cred, time.Now()) }
	}
	r := &RelayConn{
		conn:  conn,
		relay: relay,
		role:  role,
		reg:   reg,
		done:  make(chan struct{}),
	}
	go r.keepalive(every)
	return r
}

// NewProbeConn 不登记，只用来向 relay 探测公网地址、向对方打洞。
func NewProbeConn(conn *net.UDPConn, relay *net.UDPAddr) *RelayConn {
	return &RelayConn{conn: conn, relay: relay, done: make(chan struct{})}
}

func (r *RelayConn) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := r.conn.WriteToUDP(r.reg(), r.relay); errors.Is(err, net.ErrClosed) {
			return
		}
		select {
		case <-r.done:
			return
		case <-t.C:
		}
	}
}

// Acked 是中转有没有回过执。
func (r *RelayConn) Acked() bool { return r.acked.Load() }

// Relay 是中转的地址。
func (r *RelayConn) Relay() *net.UDPAddr { return r.relay }

func (r *RelayConn) ReadFrom(b []byte) (int, net.Addr, error) {
	for {
		n, addr, err := r.conn.ReadFromUDP(b)
		if err != nil {
			return n, addr, err
		}
		if deskrelay.IsControl(b[:n]) {
			r.control(b[:n], addr)
			continue
		}
		return n, addr, nil
	}
}

func (r *RelayConn) control(b []byte, from *net.UDPAddr) {
	if from.Port != r.relay.Port || !from.IP.Equal(r.relay.IP) {
		if r.OnPeerPunch != nil {
			r.OnPeerPunch(from)
		}
		return
	}
	if r.reg != nil && deskrelay.IsAck(b, r.role) {
		r.acked.Store(true)
		r.denied.Store(false)
	}
	if r.reg != nil && deskrelay.IsDenied(b, r.role) && !r.denied.Swap(true) {
		log.Printf("mquic: relay %s denied the registration: key invalid or revoked", r.relay)
	}
	if a, ok := deskrelay.ParseProbeReply(b); ok {
		r.public.Store(&a)
	}
}

// Public 向中转探测这个 socket 在公网上的地址。socket 要有人在读（交给了 quic-go，或者 reading 为 false
// 时这里自己读）。
func (r *RelayConn) Public(ctx context.Context, reading bool) (netip.AddrPort, error) {
	r.public.Store(nil)
	probe := deskrelay.Probe()
	buf := make([]byte, 64)
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := r.conn.WriteToUDP(probe, r.relay); err != nil {
			return netip.AddrPort{}, err
		}
		next := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(next) {
			if a := r.public.Load(); a != nil {
				return *a, nil
			}
			if ctx.Err() != nil {
				return netip.AddrPort{}, fmt.Errorf("mquic: relay %s did not answer the probe", r.relay)
			}
			if reading {
				time.Sleep(20 * time.Millisecond)
				continue
			}
			r.conn.SetReadDeadline(next)
			if n, from, err := r.conn.ReadFromUDP(buf); err == nil && deskrelay.IsControl(buf[:n]) {
				r.control(buf[:n], from)
			}
			r.conn.SetReadDeadline(time.Time{})
		}
	}
}

// PunchTo 在 d 之内每 50ms 向 peer 发一个打洞包，让这边的 NAT 放进对方的包。
func (r *RelayConn) PunchTo(peer *net.UDPAddr, d time.Duration) {
	punch := deskrelay.Punch()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if _, err := r.conn.WriteToUDP(punch, peer); errors.Is(err, net.ErrClosed) {
			return
		}
		select {
		case <-r.done:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (r *RelayConn) WriteTo(b []byte, addr net.Addr) (int, error) { return r.conn.WriteTo(b, addr) }

func (r *RelayConn) Close() error {
	r.once.Do(func() { close(r.done) })
	return r.conn.Close()
}

func (r *RelayConn) LocalAddr() net.Addr                { return r.conn.LocalAddr() }
func (r *RelayConn) SetDeadline(t time.Time) error      { return r.conn.SetDeadline(t) }
func (r *RelayConn) SetReadDeadline(t time.Time) error  { return r.conn.SetReadDeadline(t) }
func (r *RelayConn) SetWriteDeadline(t time.Time) error { return r.conn.SetWriteDeadline(t) }
func (r *RelayConn) SetReadBuffer(n int) error          { return r.conn.SetReadBuffer(n) }
func (r *RelayConn) SetWriteBuffer(n int) error         { return r.conn.SetWriteBuffer(n) }
