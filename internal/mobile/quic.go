package mobile

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"

	"hive/internal/deskrelay"
	"hive/internal/mquic"
)

// relayKeepalive 是向中转重发登记的间隔。家用路由器的 UDP 映射常见 30 秒就回收。
const relayKeepalive = 10 * time.Second

// QUICOptions 由常驻进程给。Relay 每次用到时调用（读 desk.json，改了不用重启）；
// ListenOut 开绑在物理网卡上的 socket，绕开接管默认路由的代理，为空或失败时照系统路由走。
type QUICOptions struct {
	Relay     func() string
	ListenOut func() (*net.UDPConn, error)
}

// quicState 是手机服务的 QUIC 这一侧：局域网上和 WebSocket 同一个端口号的 UDP，
// 加上登记在中转上的出口 socket。两个各一个 quic.Transport，收进来的连接走同一套处理。
type quicState struct {
	mu   sync.Mutex
	opts QUICOptions
	tls  *tls.Config
	lan  *quic.Transport

	relayAt string
	relay   *mquic.RelayConn
	relayTr *quic.Transport
	// 这次启动的中转令牌，手机经 hive.quic 拿到后用它登记。换了中转地址也不变
	token []byte
}

// SetQUIC 在 Start 之前调用。不调用时只有局域网 QUIC。
func (s *Server) SetQUIC(o QUICOptions) {
	s.q.mu.Lock()
	s.q.opts = o
	s.q.mu.Unlock()
}

// startQUIC 在 WebSocket 监听上之后调用。开不了只记日志：手机还能走 WebSocket。
func (s *Server) startQUIC(port int) {
	q := &s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lan != nil {
		return
	}
	cfg, err := mquic.ServerTLS()
	if err != nil {
		log.Printf("mobile: quic: %v", err)
		return
	}
	q.tls = cfg
	q.token = make([]byte, deskrelay.TokenLen)
	rand.Read(q.token)
	pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		log.Printf("mobile: quic: listen on udp %d: %v", port, err)
		return
	}
	tr := &quic.Transport{Conn: pc}
	ln, err := tr.Listen(q.tls, mquic.Config())
	if err != nil {
		pc.Close()
		log.Printf("mobile: quic: %v", err)
		return
	}
	q.lan = tr
	go s.acceptQUIC(ln)
	log.Printf("mobile: quic listening on %s", pc.LocalAddr())
	s.ensureRelayLocked()
}

// ensureRelayLocked 按当前配置开、换或关中转那一路。调用时持有 q.mu。
func (s *Server) ensureRelayLocked() {
	q := &s.q
	if q.tls == nil {
		return
	}
	addr := ""
	if q.opts.Relay != nil {
		addr = q.opts.Relay()
	}
	if addr == q.relayAt {
		return
	}
	if q.relayTr != nil {
		q.relayTr.Close()
		q.relay.Close()
		q.relayTr, q.relay = nil, nil
	}
	q.relayAt = addr
	if addr == "" {
		return
	}
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		log.Printf("mobile: quic relay %s: %v", addr, err)
		return
	}
	var out *net.UDPConn
	if q.opts.ListenOut != nil {
		if out, err = q.opts.ListenOut(); err != nil {
			log.Printf("mobile: quic: no physical interface for relay, using system route: %v", err)
		}
	}
	if out == nil {
		if out, err = net.ListenUDP("udp4", nil); err != nil {
			log.Printf("mobile: quic relay: %v", err)
			return
		}
	}
	rc := mquic.NewRelayConn(out, raddr, deskrelay.RoleMac, q.token, relayKeepalive)
	tr := &quic.Transport{Conn: rc}
	ln, err := tr.Listen(q.tls, mquic.Config())
	if err != nil {
		rc.Close()
		log.Printf("mobile: quic relay: %v", err)
		return
	}
	q.relay, q.relayTr = rc, tr
	go s.acceptQUIC(ln)
	log.Printf("mobile: quic registered at relay %s", addr)
}

func (s *Server) closeQUIC() {
	q := &s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lan != nil {
		q.lan.Close()
		q.lan.Conn.Close()
		q.lan = nil
	}
	if q.relayTr != nil {
		q.relayTr.Close()
		q.relay.Close()
		q.relayTr, q.relay = nil, nil
	}
	q.relayAt = ""
}

func (s *Server) acceptQUIC(ln *quic.Listener) {
	for {
		qc, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		go s.serveQUIC(qc)
	}
}

func (s *Server) serveQUIC(qc *quic.Conn) {
	ctx, cancel := context.WithTimeout(qc.Context(), 10*time.Second)
	c, err := mquic.Accept(ctx, qc)
	cancel()
	if err != nil {
		qc.CloseWithError(0, "")
		return
	}
	w := &quicWire{c: c}
	s.serveWire(qc.Context(), w)
	if !w.closing.Load() {
		c.CloseNow()
	}
}

// QUICInfo 是 hive.quic 的回复：手机按它拨 QUIC。
type QUICInfo struct {
	Port  int      `json:"port"`
	Hosts []string `json:"hosts"` // 本机的 Tailscale 和局域网地址
	Relay string   `json:"relay,omitempty"`
	// 和 relay 一起给：手机向中转登记用，同一台 Mac 这次启动内不变
	RelayToken []byte `json:"relayToken,omitempty"`
}

func (s *Server) quicInfo() (QUICInfo, error) {
	q := &s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.lan == nil {
		return QUICInfo{}, errorf("unavailable", "quic is not running")
	}
	s.ensureRelayLocked()
	info := QUICInfo{Port: q.lan.Conn.LocalAddr().(*net.UDPAddr).Port, Hosts: LocalEndpoints()}
	if q.relay != nil {
		info.Relay = q.relayAt
		info.RelayToken = q.token
	}
	return info, nil
}

// quicWire 让 QUIC 的流满足 wire：ctx 换成读写期限。
type quicWire struct {
	c       *mquic.Conn
	closing atomic.Bool
}

func deadlineOf(ctx context.Context) time.Time {
	d, _ := ctx.Deadline()
	return d
}

func (w *quicWire) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	w.c.SetReadDeadline(deadlineOf(ctx))
	stop := context.AfterFunc(ctx, func() { w.c.SetReadDeadline(time.Now()) })
	defer stop()
	typ, p, err := w.c.Read()
	if err != nil {
		return 0, nil, err
	}
	switch typ {
	case mquic.Text:
		return websocket.MessageText, p, nil
	case mquic.Binary:
		return websocket.MessageBinary, p, nil
	}
	return 0, nil, errors.New("mobile: unknown quic message type " + strconv.Itoa(int(typ)))
}

func (w *quicWire) Write(ctx context.Context, typ websocket.MessageType, p []byte) error {
	t := mquic.Text
	if typ == websocket.MessageBinary {
		t = mquic.Binary
	}
	w.c.SetWriteDeadline(deadlineOf(ctx))
	stop := context.AfterFunc(ctx, func() { w.c.SetWriteDeadline(time.Now()) })
	defer stop()
	err := w.c.Write(t, p)
	if err != nil {
		// 和 WebSocket 一样，写失败（超时）整条连接作废，免得后面的帧和加密计数器错开
		w.c.CloseNow()
	}
	return err
}

func (w *quicWire) Close(code websocket.StatusCode, reason string) error {
	w.closing.Store(true)
	w.c.Close(uint16(code), reason)
	return nil
}
