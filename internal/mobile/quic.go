package mobile

import (
	"context"
	"crypto/tls"
	"errors"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"

	"posse/internal/deskrelay"
	"posse/internal/mquic"
)

// relayKeepalive 是向中转重发登记的间隔。家用路由器的 UDP 映射常见 30 秒就回收。
const relayKeepalive = 10 * time.Second

// TSProvider 向手机服务提供可选的内置 Tailscale（tsnet）节点监听能力。
type TSProvider interface {
	IP() string
	Generation() uint64
	ListenPacket(port int) (net.PacketConn, error)
	ListenTCP(port int) (net.Listener, error)
}

// QUICOptions 由常驻进程给。Relay 每次用到时调用（读 desk.json，改了不用重启），返回中转地址和登记用的密钥；
// ListenOut 开绑在物理网卡上的 socket，绕开接管默认路由的代理，为空或失败时照系统路由走；
// Tailscale 提供可选的内置 Tailscale 节点，在同端口同时开放 QUIC 与首次扫码用的 WebSocket。
type QUICOptions struct {
	Relay     func() deskrelay.Config
	ListenOut func() (*net.UDPConn, error)
	Tailscale TSProvider
}

// quicState 是手机服务的 QUIC 这一侧：局域网上和 WebSocket 同一个端口号的 UDP，
// 加上可选的内置 Tailscale 监听（QUIC + TCP WS），以及每台手机一个登记在中转上的出口 socket。
type quicState struct {
	mu     sync.Mutex
	opts   QUICOptions
	tls    *tls.Config
	port   int
	closed bool
	lan    *quic.Transport

	tsTr      *quic.Transport
	tsLN      net.Listener
	tsIP      string
	tsGen     uint64
	tsRetry   *time.Timer
	tsCloseWg sync.WaitGroup

	// 中转按令牌配对两端、一个令牌只记一台手机的地址，所以每台手机（设备 ID）各用一个令牌和 socket，
	// 共用一个时两台手机会互相抢走对方的包。第一次调 posse.quic 时开，解除配对、换了中转地址或密钥时关
	relayAt deskrelay.Config
	relays  map[string]*relayPath
}

type relayPath struct {
	token []byte
	rc    *mquic.RelayConn
	tr    *quic.Transport
}

func (p *relayPath) close() {
	p.tr.Close()
	p.rc.Close()
}

// SetQUIC 在 Start 之前调用。不调用时只有局域网 QUIC。
func (s *Server) SetQUIC(o QUICOptions) {
	s.q.mu.Lock()
	s.q.opts = o
	s.q.mu.Unlock()
}

// startQUIC 在 WebSocket 监听上之后调用（调用方持有 s.mu）。开不了只记日志：手机还能走 WebSocket。
// 宿主 UDP 监听失败时不跳过内置 Tailscale 的独立 UDP/TCP 监听。
func (s *Server) startQUIC(port int, httpSrv *http.Server) {
	q := &s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.port != 0 {
		return
	}
	q.port = port
	cfg, err := mquic.ServerTLS()
	if err != nil {
		log.Printf("mobile: quic: %v", err)
	} else {
		q.tls = cfg
		pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
		if err != nil {
			log.Printf("mobile: quic: listen on udp %d: %v", port, err)
		} else {
			tr := &quic.Transport{Conn: pc}
			ln, err := tr.Listen(q.tls, mquic.Config())
			if err != nil {
				tr.Close()
				pc.Close()
				log.Printf("mobile: quic: %v", err)
			} else {
				q.lan = tr
				go s.acceptQUIC(ln, nil)
				log.Printf("mobile: quic listening on %s", pc.LocalAddr())
			}
		}
		s.openRelaysLocked()
	}
	s.syncTailscaleLocked(httpSrv)
}

// markClosing 在 Server.Close 开始时立即置上关闭标记，阻止关停期间的回调再打开新的 TS 监听。
func (s *Server) markClosing() {
	s.q.mu.Lock()
	s.q.closed = true
	s.q.mu.Unlock()
}

// TailscaleChanged 在内置 Tailscale 状态变为运行/拿到新 IP 或停止后调用，同步同端口监听。
func (s *Server) TailscaleChanged() {
	s.mu.Lock()
	httpSrv := s.srv
	s.mu.Unlock()
	s.q.mu.Lock()
	defer s.q.mu.Unlock()
	if s.q.closed || s.q.port <= 0 {
		return
	}
	s.syncTailscaleLocked(httpSrv)
}

func (s *Server) closeTailscaleLocked() {
	q := &s.q
	if q.tsRetry != nil {
		q.tsRetry.Stop()
		q.tsRetry = nil
	}
	if q.tsTr != nil {
		q.tsTr.Close()
		if q.tsTr.Conn != nil {
			q.tsTr.Conn.Close()
		}
		q.tsTr = nil
	}
	if q.tsLN != nil {
		q.tsLN.Close()
		q.tsLN = nil
	}
	q.tsIP = ""
	q.tsGen = 0
}

// tsRetryDelay 是 TS 监听局部失败或异常退出后的补开延迟，测试可调小。
var tsRetryDelay = 3 * time.Second

func (s *Server) scheduleTailscaleRetryLocked() {
	q := &s.q
	if q.closed || q.tsRetry != nil {
		return
	}
	q.tsRetry = time.AfterFunc(tsRetryDelay, func() {
		s.q.mu.Lock()
		q.tsRetry = nil
		closed := q.closed
		s.q.mu.Unlock()
		if !closed {
			s.TailscaleChanged()
		}
	})
}

// syncTailscaleLocked 在持有 q.mu 时按当前内置 Tailscale 的 (IP, Generation) 独立管理 UDP QUIC 和 TCP WS 监听。
// 一路失败不影响另一路，且在后续 TailscaleChanged、Pair/quicInfo 调用或退避定时器触发时自动补开；
// 监听异常退出时主动释放底层 Transport/PacketConn/Listener，防止端口或 netstack 登记泄漏。
func (s *Server) syncTailscaleLocked(httpSrv *http.Server) {
	q := &s.q
	if q.closed {
		return
	}
	ts := q.opts.Tailscale
	ip := ""
	var gen uint64
	if ts != nil {
		ip = strings.TrimSpace(ts.IP())
		gen = ts.Generation()
	}
	if ip == "" || q.port <= 0 {
		s.closeTailscaleLocked()
		return
	}
	if q.tsIP != ip || q.tsGen != gen {
		s.closeTailscaleLocked()
		q.tsIP = ip
		q.tsGen = gen
	}

	needRetry := false

	// 1. 独立启动/恢复 TS UDP QUIC 监听
	if q.tsTr == nil && q.tls != nil {
		pc, err := ts.ListenPacket(q.port)
		if err != nil {
			log.Printf("mobile: tailscale quic listen on %s:%d: %v", ip, q.port, err)
			needRetry = true
		} else {
			tr := &quic.Transport{Conn: pc}
			ln, err := tr.Listen(q.tls, mquic.TSConfig())
			if err != nil {
				tr.Close()
				pc.Close()
				log.Printf("mobile: tailscale quic serve: %v", err)
				needRetry = true
			} else {
				q.tsTr = tr
				q.tsCloseWg.Add(1)
				go func(tr *quic.Transport, pc net.PacketConn, ln *quic.Listener) {
					defer s.q.tsCloseWg.Done()
					s.acceptQUIC(ln, nil)
					s.q.mu.Lock()
					mine := s.q.tsTr == tr
					if mine {
						s.q.tsTr = nil
					}
					s.q.mu.Unlock()
					// quic.Transport 遇到不可恢复错误时不会关闭外部 PacketConn，先释放完毕再安排重试与结束关闭等待
					tr.Close()
					pc.Close()
					if mine {
						s.q.mu.Lock()
						if !s.q.closed && s.q.tsIP != "" && s.q.tsTr == nil {
							s.scheduleTailscaleRetryLocked()
						}
						s.q.mu.Unlock()
					}
				}(tr, pc, ln)
				log.Printf("mobile: tailscale quic listening on %s:%d", ip, q.port)
			}
		}
	}

	// 2. 独立启动/恢复 TS TCP WebSocket 监听（供官方 Tailscale 手机首次扫码）
	if q.tsLN == nil && httpSrv != nil {
		tcpLN, err := ts.ListenTCP(q.port)
		if err != nil {
			log.Printf("mobile: tailscale tcp listen on %s:%d: %v", ip, q.port, err)
			needRetry = true
		} else {
			q.tsLN = tcpLN
			q.tsCloseWg.Add(1)
			go func(ln net.Listener) {
				defer s.q.tsCloseWg.Done()
				_ = httpSrv.Serve(ln)
				s.q.mu.Lock()
				mine := s.q.tsLN == ln
				if mine {
					s.q.tsLN = nil
				}
				s.q.mu.Unlock()
				ln.Close()
				if mine {
					s.q.mu.Lock()
					if !s.q.closed && s.q.tsIP != "" && s.q.tsLN == nil {
						s.scheduleTailscaleRetryLocked()
					}
					s.q.mu.Unlock()
				}
			}(tcpLN)
			log.Printf("mobile: tailscale ws listening on %s:%d", ip, q.port)
		}
	}

	if needRetry {
		s.scheduleTailscaleRetryLocked()
	} else if q.tsRetry != nil && q.tsTr != nil && q.tsLN != nil {
		// 只有两路都已恢复时才取消共享重试定时器，避免仅补 UDP 的调用误取消待重试的 TCP
		q.tsRetry.Stop()
		q.tsRetry = nil
	}
}

func prependUniqueHost(ip string, locals []string) []string {
	if ip == "" || !IsTailscale(ip) {
		return locals
	}
	out := make([]string, 0, 1+len(locals))
	out = append(out, ip)
	for _, h := range locals {
		if h != ip {
			out = append(out, h)
		}
	}
	return out
}

// pairEndpoints 仅在 TS TCP WS 确实正在监听时才把内置 Tailscale IP 放入配对码首选列表。
func (s *Server) pairEndpoints() []string {
	s.q.mu.Lock()
	tsIP := ""
	if s.q.tsLN != nil {
		tsIP = s.q.tsIP
	}
	s.q.mu.Unlock()
	return prependUniqueHost(tsIP, LocalEndpoints())
}

// openRelaysLocked 给拿过令牌的手机都开好中转出口：常驻进程重启、交接后，或改了中转设置，
// 不在局域网的手机要靠它们连回来，等不到手机先调 posse.quic。调用时持有 q.mu。
func (s *Server) openRelaysLocked() {
	if s.q.closed {
		return
	}
	s.syncRelayLocked()
	for _, id := range s.pair.relayDevices() {
		s.relayLocked(id)
	}
}

// RelayChanged 在改了 desk.json 的中转设置后调用，按新设置重开中转出口。
func (s *Server) RelayChanged() {
	s.q.mu.Lock()
	defer s.q.mu.Unlock()
	if s.q.tls != nil {
		s.openRelaysLocked()
	}
}

// syncRelayLocked 重读中转设置，变了就关掉所有中转出口。调用时持有 q.mu。
func (s *Server) syncRelayLocked() deskrelay.Config {
	q := &s.q
	var cfg deskrelay.Config
	if q.opts.Relay != nil {
		cfg = q.opts.Relay()
	}
	if cfg != q.relayAt {
		for _, p := range q.relays {
			p.close()
		}
		q.relays, q.relayAt = nil, cfg
	}
	return cfg
}

// relayLocked 返回这台手机的中转出口，没有就开一个。没配中转或开不了时为 nil，开不了的下次再试。
// 调用时持有 q.mu。
func (s *Server) relayLocked(device string) *relayPath {
	q := &s.q
	if q.closed {
		return nil
	}
	cfg := s.syncRelayLocked()
	addr := cfg.Relay
	if addr == "" || q.tls == nil {
		return nil
	}
	if p := q.relays[device]; p != nil {
		return p
	}
	token := s.pair.relayToken(device)
	if token == nil {
		return nil
	}
	raddr, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		log.Printf("mobile: quic relay %s: %v", addr, err)
		return nil
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
			return nil
		}
	}
	rc := mquic.NewRelayConn(out, raddr, deskrelay.RoleMac, token, cfg.Credential(), relayKeepalive)
	var lastPunch atomic.Int64
	rc.OnPeerPunch = func(from *net.UDPAddr) {
		// 手机连发几十个，只记每次打洞的第一个
		if now := time.Now().UnixNano(); now-lastPunch.Swap(now) > int64(10*time.Second) {
			log.Printf("mobile: punch: packet from phone %s reached the mac", from)
		}
	}
	tr := &quic.Transport{Conn: rc}
	ln, err := tr.Listen(q.tls, mquic.Config())
	if err != nil {
		rc.Close()
		log.Printf("mobile: quic relay: %v", err)
		return nil
	}
	p := &relayPath{token: token, rc: rc, tr: tr}
	if q.relays == nil {
		q.relays = map[string]*relayPath{}
	}
	q.relays[device] = p
	go s.acceptQUIC(ln, raddr)
	log.Printf("mobile: quic registered at relay %s (%d phones)", addr, len(q.relays))
	return p
}

// dropRelays 关掉这些设备的中转出口（解除配对、并进同一台手机的旧 token）。
func (s *Server) dropRelays(ids []string) {
	q := &s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range ids {
		if p := q.relays[id]; p != nil {
			p.close()
			delete(q.relays, id)
		}
	}
}

func (s *Server) closeQUIC() {
	q := &s.q
	q.mu.Lock()
	q.closed = true
	s.closeTailscaleLocked()
	if q.lan != nil {
		q.lan.Close()
		q.lan.Conn.Close()
		q.lan = nil
	}
	for _, p := range q.relays {
		p.close()
	}
	q.relays, q.relayAt = nil, deskrelay.Config{}
	q.mu.Unlock()
	// 等待异常退出协程把外部 TS PacketConn / Listener 释放完毕，保证 Close 返回后端口已释放
	q.tsCloseWg.Wait()
}

// acceptQUIC 接受连接。relay 不为空时这是中转出口：从中转来的是经中转，别的地址是打洞直连。
func (s *Server) acceptQUIC(ln *quic.Listener, relay *net.UDPAddr) {
	for {
		qc, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		via := "lan"
		if relay != nil {
			via = "direct (punched)"
			if a, ok := qc.RemoteAddr().(*net.UDPAddr); ok && a.IP.Equal(relay.IP) && a.Port == relay.Port {
				via = "relay"
			}
		}
		log.Printf("mobile: quic connection from %s via %s", qc.RemoteAddr(), via)
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
	// 和 WebSocket 的读上限一致：手机发来的只有请求和分块上传的图片
	c.ReadLimit = 4 << 20
	w := &quicWire{c: c}
	s.serveWire(qc.Context(), w, nil)
	if !c.Closing() {
		c.CloseNow()
	}
}

// PunchInfo 是 posse.punch 的回复：这台手机的中转出口在公网上的地址，手机向它打洞并拨号。
type PunchInfo struct {
	Addr string `json:"addr"`
}

// punch 是打洞的 Mac 一侧：手机经中转连着时，新开一个 socket 问出自己的公网地址 phone 发来；
// 这边问出这台手机中转出口的公网地址回给它，同时从这个出口向 phone 连发打洞包。出口上本来就有
// QUIC 在听，手机打通后直接拨进来，常驻进程照常接受。
func (s *Server) punch(device, phone string) (PunchInfo, error) {
	peer, err := net.ResolveUDPAddr("udp4", phone)
	if err != nil || peer.IP == nil || peer.Port == 0 {
		return PunchInfo{}, errorf("bad_params", "phone address %q", phone)
	}
	s.q.mu.Lock()
	p := s.relayLocked(device)
	s.q.mu.Unlock()
	if p == nil {
		return PunchInfo{}, errorf("unavailable", "no relay configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pub, err := p.rc.Public(ctx, true)
	if err != nil {
		log.Printf("mobile: punch: %v", err)
		return PunchInfo{}, errorf("unavailable", "%v", err)
	}
	log.Printf("mobile: punch: phone at %s, mac at %s", peer, pub)
	go p.rc.PunchTo(peer, mquic.PunchFor)
	return PunchInfo{Addr: pub.String()}, nil
}

// QUICInfo 是 posse.quic 的回复：手机按它拨 QUIC。
type QUICInfo struct {
	Port   int      `json:"port"`
	Hosts  []string `json:"hosts"`            // 本机的内置/系统 Tailscale 和局域网地址
	TSHost string   `json:"tsHost,omitempty"` // Mac 内置 Tailscale 节点的 IPv4（运行中时非空）
	Relay  string   `json:"relay,omitempty"`
	// 和 relay 一起给：这台手机向中转登记用，存在 mobile.json 里，重启、交接都不变
	RelayToken []byte `json:"relayToken,omitempty"`
}

func (s *Server) quicInfo(device string) (QUICInfo, error) {
	q := &s.q
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return QUICInfo{}, errorf("unavailable", "quic is not running")
	}
	// 若 TS UDP 之前局部失败，趁查询时主动补开一次（不触碰 s.mu）
	if q.tsTr == nil && q.opts.Tailscale != nil && q.port > 0 {
		s.syncTailscaleLocked(nil)
	}
	relayPath := s.relayLocked(device)
	if q.lan == nil && q.tsTr == nil && relayPath == nil {
		return QUICInfo{}, errorf("unavailable", "quic is not running")
	}
	port := 0
	if q.lan != nil {
		port = q.lan.Conn.LocalAddr().(*net.UDPAddr).Port
	} else if q.tsTr != nil {
		port = q.port
	}
	// 仅在 TS UDP QUIC 确实监听中时宣传 tsHost 与内置 TS 地址
	tsIP := ""
	if q.tsTr != nil && IsTailscale(q.tsIP) {
		tsIP = q.tsIP
	}
	var hosts []string
	if q.lan != nil {
		hosts = prependUniqueHost(tsIP, LocalEndpoints())
	} else if tsIP != "" {
		hosts = []string{tsIP}
	}
	info := QUICInfo{Port: port, Hosts: hosts, TSHost: tsIP}
	if relayPath != nil {
		info.Relay = q.relayAt.Relay
		info.RelayToken = relayPath.token
	}
	return info, nil
}

// quicWire 让 QUIC 的流满足 wire：ctx 换成读写期限。
type quicWire struct{ c *mquic.Conn }

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
	if err != nil && !w.c.Closing() {
		// 和 WebSocket 一样，写失败（超时）整条连接作废，免得后面的帧和加密计数器错开。
		// 已经在关的不动：关闭帧还排着，直接断开会把它和前面那条报错一起丢掉
		w.c.CloseNow()
	}
	return err
}

func (w *quicWire) Close(code websocket.StatusCode, reason string) error {
	w.c.Close(uint16(code), reason)
	return nil
}
