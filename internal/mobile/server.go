// Package mobile 让 Orca 的手机 App 连到 hive：实现它的配对、E2EE v2 和用到的那部分 RPC，
// 手机上看到的"主机"就是这台电脑上的 hive 常驻进程。协议整理自 stablyai/orca 的 mobile/ 和 src/shared/。
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// DefaultPort 避开 Orca 的 6768：两边可以同时开着。
const DefaultPort = 6788

// Server 是给手机的 WebSocket 服务，挂在常驻进程里。
type Server struct {
	host  Host
	pair  *pairing
	port  int
	epoch string // 本次启动的发布纪元：手机按它判断标签快照的新旧

	mu      sync.Mutex
	ln      net.Listener
	srv     *http.Server
	conns   map[*conn]struct{}
	tabs    map[string]*tabState // 工作区 ID → 标签快照的版本
	nextSID uint32

	permMu sync.Mutex
	perms  map[string]*permWait

	media mediaCache
}

// PairInfo 是给界面和 CLI 显示的配对信息。
type PairInfo struct {
	URL       string   `json:"url"` // orca://pair?code=…，二维码内容
	Code      string   `json:"code"`
	Endpoint  string   `json:"endpoint"`
	Endpoints []string `json:"endpoints,omitempty"`
}

func New(home string, host Host, port int) (*Server, error) {
	p, err := openPairing(home)
	if err != nil {
		return nil, err
	}
	if port == 0 {
		port = DefaultPort
	}
	return &Server{host: host, pair: p, port: port, epoch: "hive:" + newUUID(),
		conns: map[*conn]struct{}{}, tabs: map[string]*tabState{}}, nil
}

// Start 在已有配对过的手机时开始监听；还没有手机时等到生成配对码再监听，免得平白对局域网开端口。
func (s *Server) Start() error {
	if !s.pair.paired() {
		return nil
	}
	return s.listen()
}

func (s *Server) listen() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return nil
	}
	ln, err := net.Listen("tcp", ":"+strconv.Itoa(s.port))
	if err != nil {
		return fmt.Errorf("mobile: listen on port %d: %w", s.port, err)
	}
	s.ln = ln
	s.srv = &http.Server{Handler: http.HandlerFunc(s.serveWS), ReadHeaderTimeout: 10 * time.Second}
	go s.srv.Serve(ln)
	log.Printf("mobile: listening on %s", ln.Addr())
	return nil
}

// Port 是实际监听的端口（测试里用 0 让系统分配）。
func (s *Server) Port() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return s.port
	}
	return s.ln.Addr().(*net.TCPAddr).Port
}

func (s *Server) Close() {
	s.mu.Lock()
	srv := s.srv
	conns := make([]*conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if srv != nil {
		srv.Close()
	}
	for _, c := range conns {
		c.ws.Close(websocket.StatusGoingAway, "host shutting down")
	}
}

// Pair 返回配对码（必要时开始监听）。rotate 时作废还没用过的旧码。
// addr 为空时自动选 Tailscale 或局域网地址，写法见 ResolveEndpoint。
// 主 endpoint 保持这一条（Orca App 只读它）；endpoints 再加上本机其余可达地址，给 Hive 客户端并发握手。
func (s *Server) Pair(rotate bool, addr string) (PairInfo, error) {
	if err := s.listen(); err != nil {
		return PairInfo{}, err
	}
	port := s.Port()
	var primary string
	if addr != "" {
		ep, err := ResolveEndpoint(addr, port)
		if err != nil {
			return PairInfo{}, err
		}
		primary = ep
	}
	// 选了地址就只写这一个：手机会同时连配对码里的所有地址、谁先连上用谁，
	// 再带上 Tailscale、局域网的话，选的局域网或自定义地址总被抢先的那个挤掉
	var extras []string
	for _, host := range LocalEndpoints() {
		if primary != "" {
			break
		}
		ep, err := ResolveEndpoint(host, port)
		if err != nil {
			continue
		}
		extras = append(extras, ep)
	}
	if primary == "" {
		if len(extras) == 0 {
			return PairInfo{}, errors.New("no LAN or Tailscale address found; connect to a network first")
		}
		primary = extras[0]
	}
	eps := uniqueEndpoints(primary, extras)
	d, err := s.pair.pending(rotate)
	if err != nil {
		return PairInfo{}, err
	}
	code := offerCode(primary, eps, d.Token, s.pair.pub, d.ID)
	return PairInfo{URL: "orca://pair?code=" + code, Code: code, Endpoint: primary, Endpoints: eps}, nil
}

// Devices 列出配对过的手机，标出现在连着的。
func (s *Server) Devices() []Device {
	out := s.pair.devices()
	s.mu.Lock()
	online := map[string]bool{}
	for c := range s.conns {
		online[c.device.ID] = true
		if c.device.Phone != "" {
			online[c.device.Phone] = true
		}
	}
	s.mu.Unlock()
	for i, d := range out {
		out[i].Online = online[d.ID] || d.Phone != "" && online[d.Phone]
	}
	return out
}

// RemoveDevice 移除配对，并断开这台手机现有的连接。
func (s *Server) RemoveDevice(id string) bool {
	removed := s.pair.remove(id)
	if len(removed) == 0 {
		return false
	}
	s.dropDevices(removed)
	return true
}

// dropDevices 断开用这些设备 token 连着的连接。
func (s *Server) dropDevices(ids []string) {
	if len(ids) == 0 {
		return
	}
	s.mu.Lock()
	var drop []*conn
	for c := range s.conns {
		if slices.Contains(ids, c.device.ID) {
			drop = append(drop, c)
		}
	}
	s.mu.Unlock()
	for _, c := range drop {
		c.ws.Close(4001, "Unauthorized")
	}
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	// 手机 App 的 WebSocket 不带 Origin；安全靠 E2EE 和设备 token
	// 不开 permessage-deflate：消息都是密文的 base64，只有 HuffmanOnly 压得动（约少四分之一），
	// websocket 库写死用 BestSpeed，Go 1.27 的 BestSpeed 遇到这种数据原样存储，开了只白费 CPU
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	// 对端没发关闭帧就断开时，websocket 库只返回读错误、不关底层连接，要自己关
	defer ws.CloseNow()
	// 图片上传按块发，每块不超过几百 KB
	ws.SetReadLimit(4 << 20)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	c := &conn{srv: s, ws: ws, ctx: ctx, streams: map[string]context.CancelFunc{}, streamGen: map[string]uint64{}, uploads: map[string]*upload{}}
	if err := c.handshake(); err != nil {
		return
	}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.stopStreams()
		c.dropUploads()
	}()
	c.serve()
}

// streamID 给终端二进制流编号，整个服务内唯一。
func (s *Server) streamID() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextSID++
	return s.nextSID
}

// conn 是一条已认证的手机连接。
type conn struct {
	srv    *Server
	ws     *websocket.Conn
	ctx    context.Context
	sess   *session
	device Device

	wmu sync.Mutex // 加密计数器和发送顺序必须一致，发送整体串行

	smu       sync.Mutex
	streams   map[string]context.CancelFunc // 订阅键 → 停止这个流
	streamGen map[string]uint64
	nextGen   uint64
	uploads   map[string]*upload

	// 握手之后 runtime.clientCapabilities.update 记在这条连接上。没声明的（Orca App）能力为空。
	clientName    string
	clientVersion int
	caps          map[string]struct{}

	// 这条连接上还开着的对话订阅。键是流的 key，值是它看着的终端。
	chatWatch map[string]chatWatch
}

func (c *conn) setClientCaps(client string, version int, names []string) {
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			set[name] = struct{}{}
		}
	}
	c.smu.Lock()
	c.clientName = client
	c.clientVersion = version
	c.caps = set
	c.smu.Unlock()
}

func (c *conn) hasCapability(name string) bool {
	c.smu.Lock()
	defer c.smu.Unlock()
	_, ok := c.caps[name]
	return ok
}

func (c *conn) handshake() error {
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	typ, raw, err := c.ws.Read(ctx)
	if err != nil {
		return err
	}
	h, ok := parseHello(raw)
	if typ != websocket.MessageText || !ok {
		c.ws.Close(4001, "Unauthorized")
		return errors.New("bad hello")
	}
	rd, sess := accept(h, &c.srv.pair.pub, &c.srv.pair.priv)
	c.sess = sess
	b, _ := json.Marshal(rd)
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	typ, raw, err = c.ws.Read(ctx)
	if err != nil {
		return err
	}
	var auth struct {
		Type              string `json:"type"`
		V                 int    `json:"v"`
		TranscriptHashB64 string `json:"transcriptHashB64"`
		DeviceToken       string `json:"deviceToken"`
	}
	var plain []byte
	if typ == websocket.MessageText {
		plain, err = sess.openText(raw)
	}
	th := b64(sess.transcriptHash)
	if typ != websocket.MessageText || err != nil || json.Unmarshal(plain, &auth) != nil ||
		auth.Type != "e2ee_auth" || auth.V != 2 || auth.TranscriptHashB64 != th {
		c.ws.Close(4001, "Unauthorized")
		return errors.New("bad auth")
	}
	d, ok := c.srv.pair.auth(auth.DeviceToken)
	if !ok {
		c.sendJSON(map[string]any{"type": "e2ee_error", "error": map[string]string{"code": "unauthorized"}})
		c.ws.Close(4001, "Unauthorized")
		return errors.New("unknown device")
	}
	c.device = d
	return c.sendJSON(map[string]any{"type": "e2ee_authenticated", "v": 2, "transcriptHashB64": th})
}

// sendJSON 加密并发送一个 JSON 文本帧。
func (c *conn) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	frame := c.sess.sealText(b)
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout(len(frame)))
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageText, frame)
}

func (c *conn) sendBinary(p []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	frame := c.sess.seal(p, kindBinary)
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout(len(frame)))
	defer cancel()
	return c.ws.Write(ctx, websocket.MessageBinary, frame)
}

// writeTimeout 是发一条消息最多等多久：至少 15 秒，大消息再按每秒 64 KB 的最低速度加。
// 写超时时 websocket 库会关掉整条连接（手机上所有终端跟着掉线），一张大图、一段视频在
// 慢的线路上（跨境隧道晚高峰）发得久一点不该把连接拖断。
func writeTimeout(n int) time.Duration {
	return 15*time.Second + time.Duration(n/(64<<10))*time.Second
}

type request struct {
	ID          string          `json:"id"`
	DeviceToken string          `json:"deviceToken"`
	Method      string          `json:"method"`
	Params      json.RawMessage `json:"params"`
}

func (c *conn) reply(id string, result any) error {
	return c.sendJSON(map[string]any{"id": id, "ok": true, "result": result})
}

// replyOrFail 回结果或错误：rpcError 带上它的错误码，文件不存在报 not_found。
func (c *conn) replyOrFail(id string, res any, err error) {
	if err == nil {
		c.reply(id, res)
		return
	}
	var re *rpcError
	switch {
	case errors.As(err, &re):
		c.fail(id, re.code, re.msg)
	case errors.Is(err, fs.ErrNotExist):
		c.fail(id, "not_found", err.Error())
	default:
		c.fail(id, "internal_error", err.Error())
	}
}

func (c *conn) stream(id string, result any) error {
	return c.sendJSON(map[string]any{"id": id, "ok": true, "streaming": true, "result": result})
}

func (c *conn) fail(id, code, msg string) error {
	return c.sendJSON(map[string]any{"id": id, "ok": false, "error": map[string]string{"code": code, "message": msg}})
}

func (c *conn) serve() {
	bad := 0
	for {
		typ, raw, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			// 手机不发终端二进制帧；解开只为让计数器前进
			if _, err := c.sess.open(raw, kindBinary); err != nil {
				return
			}
			continue
		}
		plain, err := c.sess.openText(raw)
		if err != nil {
			// 计数器对不上之后的帧都解不开，连续失败就断开让手机重连
			if bad++; bad >= 5 {
				c.ws.Close(4003, "Decrypt failed")
				return
			}
			continue
		}
		bad = 0
		var req request
		if json.Unmarshal(plain, &req) != nil || req.ID == "" {
			continue
		}
		if !c.srv.pair.valid(req.DeviceToken) {
			c.fail(req.ID, "unauthorized", "device is no longer paired")
			c.ws.Close(4001, "Unauthorized")
			return
		}
		if err := c.dispatch(req); err != nil {
			var re *rpcError
			if errors.As(err, &re) {
				c.fail(req.ID, re.code, re.msg)
			} else {
				c.fail(req.ID, "internal_error", err.Error())
			}
		}
	}
}

type rpcError struct{ code, msg string }

func (e *rpcError) Error() string { return e.code + ": " + e.msg }

func errorf(code, format string, a ...any) error {
	return &rpcError{code, fmt.Sprintf(format, a...)}
}

// startStream 登记一个流，返回它的 context；同 ID 的旧流先停掉。
func (c *conn) startStream(id string) context.Context {
	ctx, _ := c.startKeyedStream(id)
	return ctx
}

// startKeyedStream 用 key 登记取消。同 key 的新订阅替换旧订阅。
// gen 用来让退出的旧流不要删掉已经换上的新流。
func (c *conn) startKeyedStream(key string) (context.Context, uint64) {
	ctx, cancel := context.WithCancel(c.ctx)
	c.smu.Lock()
	if old := c.streams[key]; old != nil {
		old()
	}
	c.nextGen++
	gen := c.nextGen
	c.streams[key] = cancel
	c.streamGen[key] = gen
	c.smu.Unlock()
	return ctx, gen
}

func (c *conn) stopStream(id string) bool {
	c.smu.Lock()
	cancel := c.streams[id]
	delete(c.streams, id)
	delete(c.streamGen, id)
	c.smu.Unlock()
	if cancel != nil {
		cancel()
	}
	return cancel != nil
}

// stopStreamsPrefix 停掉某个前缀下的全部流。prefix 为空则全部停掉。
func (c *conn) stopStreamsPrefix(prefix string) {
	c.smu.Lock()
	var cancels []context.CancelFunc
	for id, cancel := range c.streams {
		if prefix == "" || strings.HasPrefix(id, prefix) {
			cancels = append(cancels, cancel)
			delete(c.streams, id)
			delete(c.streamGen, id)
		}
	}
	c.smu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (c *conn) endStream(id string) {
	c.smu.Lock()
	delete(c.streams, id)
	delete(c.streamGen, id)
	c.smu.Unlock()
}

func (c *conn) endStreamGen(id string, gen uint64) {
	c.smu.Lock()
	if c.streamGen[id] == gen {
		delete(c.streams, id)
		delete(c.streamGen, id)
	}
	c.smu.Unlock()
}

func (c *conn) stopStreams() {
	c.smu.Lock()
	for id, cancel := range c.streams {
		cancel()
		delete(c.streams, id)
		delete(c.streamGen, id)
	}
	c.smu.Unlock()
}
