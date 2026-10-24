package mquic

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"hive/internal/deskrelay"
)

// RelayConn 是交给 quic-go 的 UDP socket：定时向中转登记，收包时滤掉中转的回执。
// 不嵌入 *net.UDPConn：quic-go 见到 ReadMsgUDP 会绕过 ReadFrom 直接读，回执就漏给它了。
type RelayConn struct {
	conn  *net.UDPConn
	relay *net.UDPAddr
	reg   []byte

	done  chan struct{}
	once  sync.Once
	kick  chan struct{}
	acked atomic.Bool
}

// NewRelayConn 开始向 relay 登记，每隔 every 重发一次。
func NewRelayConn(conn *net.UDPConn, relay *net.UDPAddr, role byte, token []byte, every time.Duration) *RelayConn {
	r := &RelayConn{
		conn:  conn,
		relay: relay,
		reg:   deskrelay.Register(role, token),
		done:  make(chan struct{}),
		kick:  make(chan struct{}, 1),
	}
	go r.keepalive(every)
	return r
}

func (r *RelayConn) keepalive(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := r.conn.WriteToUDP(r.reg, r.relay); errors.Is(err, net.ErrClosed) {
			return
		}
		select {
		case <-r.done:
			return
		case <-t.C:
		case <-r.kick:
		}
	}
}

// Kick 马上重发一次登记：换了网络后中转要尽快知道新地址。
func (r *RelayConn) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
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
		if deskrelay.IsControl(b[:n]) && addr.Port == r.relay.Port && addr.IP.Equal(r.relay.IP) {
			if deskrelay.IsAck(b[:n], r.reg[4]) {
				r.acked.Store(true)
			}
			continue
		}
		return n, addr, nil
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
