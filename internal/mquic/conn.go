package mquic

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"

	"hive/internal/deskrelay"
)

// relayEvery 是手机向中转重发登记的间隔：换网络后最多这么久中转就认得新地址。
const relayEvery = 5 * time.Second

// CloseError 是对方发来关闭帧后 Read 返回的错误。
type CloseError struct {
	Code   uint16
	Reason string
}

func (e *CloseError) Error() string {
	return fmt.Sprintf("mquic: closed by peer (%d %s)", e.Code, e.Reason)
}

// Conn 是一条连接上的那条流，按消息读写。读只能有一个调用方；写可以并发，按到达顺序整条写出。
type Conn struct {
	qc *quic.Conn
	st *quic.Stream
	r  *bufio.Reader

	// ReadLimit 是读一条消息的上限，默认 MaxMessage
	ReadLimit int

	wmu     sync.Mutex
	closing atomic.Bool

	// 拨号的一方自己开的 socket，连接结束时关掉
	tr *quic.Transport
	pc net.PacketConn

	once sync.Once
}

func newConn(qc *quic.Conn, st *quic.Stream) *Conn {
	return &Conn{qc: qc, st: st, r: bufio.NewReaderSize(st, 64<<10), ReadLimit: MaxMessage}
}

// Accept 等手机开流。
func Accept(ctx context.Context, qc *quic.Conn) (*Conn, error) {
	st, err := qc.AcceptStream(ctx)
	if err != nil {
		return nil, err
	}
	return newConn(qc, st), nil
}

// Dial 连到 addr。relayToken 不为空时 addr 是中转：先登记、等到回执再握手，中转不通时很快失败。
func Dial(ctx context.Context, addr string, relayToken []byte) (*Conn, error) {
	raddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	udp, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	var pc net.PacketConn = udp
	var relay *RelayConn
	if len(relayToken) > 0 {
		relay = NewRelayConn(udp, raddr, deskrelay.RolePhone, relayToken, relayEvery)
		pc = relay
	}
	tr := &quic.Transport{Conn: pc}
	fail := func(err error) (*Conn, error) {
		tr.Close()
		pc.Close()
		return nil, err
	}
	if relay != nil {
		if err := waitAck(ctx, relay); err != nil {
			return fail(err)
		}
	}
	qc, err := tr.Dial(ctx, raddr, ClientTLS(), Config())
	if err != nil {
		return fail(err)
	}
	st, err := qc.OpenStreamSync(ctx)
	if err != nil {
		qc.CloseWithError(0, "")
		return fail(err)
	}
	c := newConn(qc, st)
	c.tr, c.pc = tr, pc
	go func() {
		<-qc.Context().Done()
		c.release()
	}()
	return c, nil
}

// waitAck 等中转回执。Transport 还没开始读 socket，自己读。
func waitAck(ctx context.Context, r *RelayConn) error {
	deadline := time.Now().Add(1500 * time.Millisecond)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	r.conn.SetReadDeadline(deadline)
	defer r.conn.SetReadDeadline(time.Time{})
	buf := make([]byte, 64)
	for {
		n, addr, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return fmt.Errorf("mquic: relay %s did not answer", r.relay)
		}
		if addr.Port == r.relay.Port && addr.IP.Equal(r.relay.IP) && deskrelay.IsAck(buf[:n], deskrelay.RolePhone) {
			r.acked.Store(true)
			return nil
		}
	}
}

// Read 读一条消息。对方发来关闭帧时关掉连接，返回 *CloseError。
func (c *Conn) Read() (byte, []byte, error) {
	typ, p, err := ReadMessage(c.r, c.ReadLimit)
	if err != nil {
		return 0, nil, err
	}
	if typ == Close {
		code, reason, _ := ParseClose(p)
		c.qc.CloseWithError(quic.ApplicationErrorCode(code), reason)
		return 0, nil, &CloseError{Code: code, Reason: reason}
	}
	return typ, p, nil
}

// ErrClosing 是 Close 之后再写。
var ErrClosing = errors.New("mquic: closing")

func (c *Conn) Write(typ byte, p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if c.closing.Load() {
		return ErrClosing
	}
	return WriteMessage(c.st, typ, p)
}

// Closing 是已经调过 Close。
func (c *Conn) Closing() bool { return c.closing.Load() }

func (c *Conn) SetReadDeadline(t time.Time) error  { return c.st.SetReadDeadline(t) }
func (c *Conn) SetWriteDeadline(t time.Time) error { return c.st.SetWriteDeadline(t) }

// Context 在连接断开时结束。
func (c *Conn) Context() context.Context { return c.qc.Context() }

func (c *Conn) RemoteAddr() net.Addr { return c.qc.RemoteAddr() }

// Close 排在正在写的那条后面发关闭帧、关流，等对方关连接（最多 2 秒）再自己关。不阻塞；
// 之后的 Write 返回 ErrClosing。
func (c *Conn) Close(code uint16, reason string) {
	if c.closing.Swap(true) {
		return
	}
	go func() {
		c.wmu.Lock()
		c.st.SetWriteDeadline(time.Now().Add(2 * time.Second))
		WriteMessage(c.st, Close, CloseFrame(code, reason))
		c.st.Close()
		c.wmu.Unlock()
		select {
		case <-c.qc.Context().Done():
		case <-time.After(2 * time.Second):
		}
		c.qc.CloseWithError(quic.ApplicationErrorCode(code), reason)
	}()
}

// CloseNow 直接断开，不等还没发出去的数据。
func (c *Conn) CloseNow() {
	c.qc.CloseWithError(0, "")
}

func (c *Conn) release() {
	c.once.Do(func() {
		if c.tr != nil {
			c.tr.Close()
			c.pc.Close()
		}
	})
}
