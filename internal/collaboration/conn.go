package collaboration

// 协作连接：TLS 1.3 上的 WebSocket（路径 /collaboration/v1、子协议 posse-collab.v1），和手机服务完全分开：
// 独立端口、独立密钥域，不叠加手机的 secretbox，也没有手机 RPC。帧是一行 JSON：请求带 id 和 method，
// 应答带同一个 id，房主主动推的只有 push（讨论有变化）。控制帧最多 1 MiB，附件按 64 KiB 分块走单独的方法。
// 加入者只往外连，不需要监听端口。

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const (
	Path            = "/collaboration/v1"
	Subprotocol     = "posse-collab.v1"
	ProtocolVersion = 1
	MaxFrame        = 1 << 20
	ChunkSize       = 64 << 10
	// MaxBody 是一条消息正文的上限
	MaxBody = 256 << 10
	// 还没绑定房间（只能提交、查询加入申请）的连接最多这么多
	maxUnbound = 16
	// 一分钟里同一地址最多试这么多次加入申请（限流，防猜邀请）
	joinPerMinute = 10
	// 心跳：加入者 15 秒一次，房主 60 秒没收到当连接中断（只用于显示，不回收执行权）
	HeartbeatEvery = 15 * time.Second
	HeartbeatLost  = 60 * time.Second
	idleTimeout    = 90 * time.Second
)

// Frame 是线上的一帧。
type Frame struct {
	ID     int64           `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
	Push   string          `json:"push,omitempty"`
	Rev    int64           `json:"rev,omitempty"`
}

// Error 是对端返回的错误。Code 是稳定的代号，Message 是英文原文（界面经 errtext 翻译）。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// 错误代号
const (
	CodeUnauthorized = "unauthorized" // 不是这个房间的成员，或凭据不对
	CodeRevoked      = "revoked"      // 成员资格被撤销或代际过期：终态
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
	CodeBadRequest   = "bad_request"
	CodeRateLimited  = "rate_limited"
	CodeUpgrade      = "upgrade" // 协议版本不兼容
	CodeQuota        = "quota"
	CodeOffline      = "offline" // 房主关了共享
	CodeInternal     = "internal"
)

// Errorf 生成一个带代号的错误。
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsCode 判断错误是不是某个代号。
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// --- 房主一侧

// Session 是一条已建立的协作连接。FP 来自 TLS（对端持有这把私钥），Room、Gen 在 hello 通过凭据核对后才有。
type Session struct {
	FP   string
	Addr string
	ws   *websocket.Conn
	wmu  sync.Mutex
	srv  *Server

	mu       sync.Mutex
	room     string
	gen      int64
	lastSeen time.Time
	closed   bool
}

// Bind 把连接绑定到一个房间（hello 核对凭据之后）。
func (s *Session) Bind(room string, gen int64) {
	s.mu.Lock()
	was := s.room
	s.room, s.gen = room, gen
	s.mu.Unlock()
	if was == "" {
		s.srv.mu.Lock()
		s.srv.unbound--
		s.srv.mu.Unlock()
	}
}

// Room 是绑定的房间和代际，没绑定时为空。
func (s *Session) Room() (string, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.room, s.gen
}

// Touch 记下收到心跳的时间（房主的时钟）。
func (s *Session) Touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

func (s *Session) send(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.ws.Write(ctx, websocket.MessageText, b)
}

// Close 断开连接。
func (s *Session) Close(reason string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()
	s.ws.Close(websocket.StatusPolicyViolation, reason)
}

// Handler 处理一条请求。返回的错误不是 *Error 时按 internal 回给对端（不带细节）。
type Handler interface {
	Serve(ctx context.Context, s *Session, method string, params json.RawMessage) (any, error)
	Closed(s *Session)
}

// Server 是房主的协作服务。
type Server struct {
	id  Identity
	h   Handler
	ln  net.Listener
	srv *http.Server

	mu       sync.Mutex
	sessions map[*Session]struct{}
	unbound  int
	joins    map[string][]time.Time // 地址 → 最近的加入申请时间（限流）
	stopped  atomic.Bool
}

// NewServer 用安装身份建服务；Listen 之后才接受连接。
func NewServer(id Identity, h Handler) *Server {
	return &Server{id: id, h: h, sessions: map[*Session]struct{}{}, joins: map[string][]time.Time{}}
}

// Listen 在 addr（如 ":6791"）上监听。端口被占用时返回明确的错误。
func (s *Server) Listen(addr string) error {
	cfg, err := ServerConfig(s.id, nil)
	if err != nil {
		return err
	}
	cfg.NextProtos = []string{"http/1.1"}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("collaboration port %s is in use by another program; set POSSE_COLLAB_PORT to another port", addr)
		}
		return err
	}
	s.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc(Path, s.accept)
	s.srv = &http.Server{Handler: mux, TLSConfig: cfg, ReadHeaderTimeout: 10 * time.Second, ErrorLog: log.New(discard{}, "", 0)}
	go s.srv.ServeTLS(ln, "", "")
	return nil
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Addr 是实际监听的地址（端口为 0 时拿真实端口）。
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Close 停止接受连接并断开所有连接。
func (s *Server) Close() {
	if !s.stopped.CompareAndSwap(false, true) {
		return
	}
	if s.srv != nil {
		s.srv.Close()
	}
	s.mu.Lock()
	ss := make([]*Session, 0, len(s.sessions))
	for x := range s.sessions {
		ss = append(ss, x)
	}
	s.mu.Unlock()
	for _, x := range ss {
		x.Close("host stopped sharing")
	}
}

// AllowJoin 是加入申请的限流：同一地址一分钟最多 joinPerMinute 次。
func (s *Server) AllowJoin(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var keep []time.Time
	for _, t := range s.joins[host] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= joinPerMinute {
		s.joins[host] = keep
		return false
	}
	s.joins[host] = append(keep, now)
	return true
}

func (s *Server) accept(w http.ResponseWriter, r *http.Request) {
	if s.stopped.Load() || r.TLS == nil {
		http.Error(w, "not available", http.StatusServiceUnavailable)
		return
	}
	fp, err := PeerFingerprint(*r.TLS)
	if err != nil {
		http.Error(w, "client certificate required", http.StatusUnauthorized)
		return
	}
	s.mu.Lock()
	if s.unbound >= maxUnbound {
		s.mu.Unlock()
		http.Error(w, "too many connections", http.StatusTooManyRequests)
		return
	}
	s.unbound++
	s.mu.Unlock()
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{Subprotocol}})
	if err != nil {
		s.mu.Lock()
		s.unbound--
		s.mu.Unlock()
		return
	}
	if ws.Subprotocol() != Subprotocol {
		ws.Close(websocket.StatusPolicyViolation, "subprotocol "+Subprotocol+" required; update Posse")
		s.mu.Lock()
		s.unbound--
		s.mu.Unlock()
		return
	}
	ws.SetReadLimit(MaxFrame)
	sess := &Session{FP: fp, Addr: r.RemoteAddr, ws: ws, srv: s, lastSeen: time.Now()}
	s.mu.Lock()
	s.sessions[sess] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.sessions, sess)
		if room, _ := sess.Room(); room == "" {
			s.unbound--
		}
		s.mu.Unlock()
		sess.Close("")
		s.h.Closed(sess)
	}()
	ctx := r.Context()
	for {
		rctx, cancel := context.WithTimeout(ctx, idleTimeout)
		_, b, err := ws.Read(rctx)
		cancel()
		if err != nil {
			return
		}
		var f Frame
		if json.Unmarshal(b, &f) != nil || f.ID == 0 || f.Method == "" {
			sess.Close("bad frame")
			return
		}
		res, herr := s.h.Serve(ctx, sess, f.Method, f.Params)
		out := Frame{ID: f.ID}
		if herr != nil {
			var e *Error
			if !errors.As(herr, &e) {
				log.Printf("collaboration %s from %s: %v", f.Method, fp[:12], herr)
				e = Errorf(CodeInternal, "internal error")
			}
			out.Error = e
		} else if out.Result, err = json.Marshal(res); err != nil {
			out.Error = Errorf(CodeInternal, "internal error")
		}
		if err := sess.send(out); err != nil {
			return
		}
	}
}

// Push 告诉绑定在这个房间的连接：讨论变了，到 rev 了。
func (s *Server) Push(room string, rev int64) {
	for _, x := range s.Sessions(room, "") {
		go x.send(Frame{Push: "changed", Rev: rev})
	}
}

// Sessions 是绑定在房间上的连接（fp 不为空时只要这个人的）。
func (s *Server) Sessions(room, fp string) []*Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*Session
	for x := range s.sessions {
		r, _ := x.Room()
		if r == room && (fp == "" || x.FP == fp) {
			out = append(out, x)
		}
	}
	return out
}

// Kick 断开一个人在房间上的连接（移除成员、关闭共享时）；fp 为空断开整个房间。
func (s *Server) Kick(room, fp, reason string) {
	for _, x := range s.Sessions(room, fp) {
		x.Close(reason)
	}
}

// LastSeen 是房间里每个人最近一次心跳（房主的时钟）。
func (s *Server) LastSeen(room string) map[string]time.Time {
	out := map[string]time.Time{}
	for _, x := range s.Sessions(room, "") {
		x.mu.Lock()
		if t := x.lastSeen; t.After(out[x.FP]) {
			out[x.FP] = t
		}
		x.mu.Unlock()
	}
	return out
}

// --- 加入者一侧

// Client 是加入者到房主的一条连接。
type Client struct {
	ws      *websocket.Conn
	Addr    string
	wmu     sync.Mutex
	mu      sync.Mutex
	next    int64
	pending map[int64]chan Frame
	pushes  chan int64
	done    chan struct{}
	err     error
}

// DialError 是一个地址都连不上：列出每个地址的结果，界面给合并的排查提示（系统不一定能区分没授权和网络不通）。
type DialError struct {
	Attempts []string
}

func (e *DialError) Error() string {
	return "could not reach the host: " + strings.Join(e.Attempts, "; ")
}

// Dial 依次试邀请里的地址，固定房主指纹；每个地址最多等 4 秒。
func Dial(ctx context.Context, id Identity, hostFP string, addrs []string, cache tls.ClientSessionCache) (*Client, error) {
	cfg, err := ClientConfig(id, hostFP, cache)
	if err != nil {
		return nil, err
	}
	cfg.NextProtos = []string{"http/1.1"}
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: false, TLSHandshakeTimeout: 4 * time.Second,
		DialContext: (&net.Dialer{Timeout: 4 * time.Second}).DialContext}
	defer tr.CloseIdleConnections()
	de := &DialError{}
	for _, a := range addrs {
		actx, cancel := context.WithTimeout(ctx, 4*time.Second)
		ws, resp, err := websocket.Dial(actx, "wss://"+a+Path, &websocket.DialOptions{
			HTTPClient:   &http.Client{Transport: tr},
			Subprotocols: []string{Subprotocol},
		})
		cancel()
		if err != nil {
			msg := err.Error()
			if resp != nil && resp.StatusCode == http.StatusNotFound {
				msg = "the host's Posse does not support collaboration; update it"
			}
			de.Attempts = append(de.Attempts, a+": "+msg)
			continue
		}
		if ws.Subprotocol() != Subprotocol {
			ws.Close(websocket.StatusPolicyViolation, "")
			de.Attempts = append(de.Attempts, a+": incompatible protocol; update Posse")
			continue
		}
		ws.SetReadLimit(MaxFrame)
		c := &Client{ws: ws, Addr: a, pending: map[int64]chan Frame{}, pushes: make(chan int64, 1), done: make(chan struct{})}
		go c.readLoop()
		return c, nil
	}
	return nil, de
}

func (c *Client) readLoop() {
	defer close(c.done)
	for {
		_, b, err := c.ws.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			c.err = err
			for id, ch := range c.pending {
				close(ch)
				delete(c.pending, id)
			}
			c.mu.Unlock()
			return
		}
		var f Frame
		if json.Unmarshal(b, &f) != nil {
			continue
		}
		if f.Push != "" {
			select {
			case c.pushes <- f.Rev:
			default:
			}
			continue
		}
		c.mu.Lock()
		ch := c.pending[f.ID]
		delete(c.pending, f.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- f
		}
	}
}

// Pushes 在房主说讨论变了时收到一个值（多次合并成一个）。
func (c *Client) Pushes() <-chan int64 { return c.pushes }

// Done 在连接断开时关闭。
func (c *Client) Done() <-chan struct{} { return c.done }

// ErrClosed 是连接已经断了。
var ErrClosed = errors.New("connection to the host closed")

// Call 发一个请求等应答；对端的错误是 *Error。
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	b, err := json.Marshal(params)
	if err != nil {
		return err
	}
	ch := make(chan Frame, 1)
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return ErrClosed
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	f, _ := json.Marshal(Frame{ID: id, Method: method, Params: b})
	if len(f) > MaxFrame {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return Errorf(CodeBadRequest, "request is larger than %d KiB", MaxFrame>>10)
	}
	c.wmu.Lock()
	err = c.ws.Write(ctx, websocket.MessageText, f)
	c.wmu.Unlock()
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return ErrClosed
		}
		if r.Error != nil {
			return r.Error
		}
		if out != nil && len(r.Result) > 0 {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Close 断开。
func (c *Client) Close() {
	c.ws.Close(websocket.StatusNormalClosure, "")
}
