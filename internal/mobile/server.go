// Package mobile 是常驻进程里给 Posse 手机客户端的服务：配对、E2EE v2、RPC 和终端输出流，
// 手机上看到的"电脑"就是这台电脑上的 posse 常驻进程。握手、方法名和帧格式沿用 Orca 手机协议
// （整理自 stablyai/orca 的 mobile/ 和 src/shared/），Orca 自己的手机 App 已不再兼容。
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"posse/internal/errtext"
	"posse/internal/projicon"
	"posse/internal/rpc"
)

// DefaultPort 避开 Orca 的 6768：两边可以同时开着。
const DefaultPort = 6788

// Server 是给手机的 WebSocket 服务，挂在常驻进程里。
type Server struct {
	host  Host
	home  string
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

	q quicState

	media mediaCache
	tools toolsCache
	repos repoCache
	icons projicon.Cache
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
	return &Server{host: host, home: home, pair: p, port: port, epoch: "posse:" + newUUID(),
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
	s.startQUIC(ln.Addr().(*net.TCPAddr).Port)
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
	var wg sync.WaitGroup
	var slow []wire
	for _, c := range conns {
		// QUIC 连接当场关：后面关 Transport 只在本地销毁连接、不通知手机；交接、重启时新进程紧接着
		// 听同一个端口，手机也收不到端口不可达，要等测延迟连续超时（十几秒）才发现断了
		if q, ok := c.ws.(*quicWire); ok {
			q.c.CloseNow()
			continue
		}
		// WebSocket 的关闭握手要等手机回一帧，断了网的手机不回，一条就要等 5 秒：一起关，限时等
		slow = append(slow, c.ws)
		wg.Go(func() { c.ws.Close(websocket.StatusGoingAway, "host shutting down") })
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(closeWait):
		for _, w := range slow {
			if n, ok := w.(interface{ CloseNow() error }); ok {
				n.CloseNow()
			}
		}
	}
	s.closeQUIC()
}

// closeWait 是退出时等手机回关闭握手的时间：连着的手机（局域网、经中转）几十到几百毫秒就回，
// 过了还没回的是断了网的，不再等（常驻进程退出、交接都在等这一步）
var closeWait = time.Second

// Pair 返回配对码（必要时开始监听）。rotate 时作废还没用过的旧码。
// addr 为空时自动选 Tailscale 或局域网地址，写法见 ResolveEndpoint。
// 主 endpoint 是选中的这条地址，手机按公钥加主地址认电脑；endpoints 再加上本机其余可达地址，给手机并发握手。
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
	s.dropRelays(ids)
}

func (s *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	// 手机 App 的 WebSocket 不带 Origin；安全靠 E2EE 和设备 token
	// 不开 permessage-deflate：消息都是密文（分帧 2 是密文的 base64，只有 HuffmanOnly 压得动、约少四分之一；
	// 分帧 3 是原始密文，更压不动），websocket 库写死用 BestSpeed，Go 1.27 的 BestSpeed 遇到这种数据原样存储，开了只白费 CPU
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	// 对端没发关闭帧就断开时，websocket 库只返回读错误、不关底层连接，要自己关
	defer ws.CloseNow()
	// 图片上传按块发，每块不超过几百 KB
	ws.SetReadLimit(4 << 20)
	s.serveWire(r.Context(), ws)
}

// serveWire 在一条已经连上的传输上握手，然后处理请求直到断开。
func (s *Server) serveWire(parent context.Context, w wire) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	c := &conn{srv: s, ws: w, ctx: ctx, streams: map[string]context.CancelFunc{}, streamGen: map[string]uint64{}, uploads: map[string]*upload{}}
	c.lastRead.Store(time.Now().UnixNano())
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

// wire 是手机连接底下的传输：WebSocket，或 QUIC 上的一条流（quic.go）。
type wire interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	Close(code websocket.StatusCode, reason string) error
}

// conn 是一条已认证的手机连接。
type conn struct {
	srv    *Server
	ws     wire
	ctx    context.Context
	sess   *session
	device Device

	wmu sync.Mutex // 加密计数器和发送顺序必须一致，发送整体串行

	smu       sync.Mutex
	streams   map[string]context.CancelFunc // 订阅键 → 停止这个流
	streamGen map[string]uint64
	nextGen   uint64
	uploads   map[string]*upload
	// 待发的上传确认（bulk.go 的 ackUpload）：blob → 已收字节数；acking 时有协程在发
	acks   map[uint32]int
	acking bool

	// 握手之后 runtime.clientCapabilities.update 记在这条连接上。没声明的（旧版客户端）能力为空。
	clientName    string
	clientVersion int
	caps          map[string]struct{}
	// lang 是手机界面的语言，报错和说明按它翻译；没报 locale 的手机是中文
	lang errtext.Lang

	// 这条连接上还开着的对话订阅。键是流的 key，值是它看着的终端。
	chatWatch map[string]chatWatch

	// 正在看的桌面（打洞、切屏的入口），这次观看结束时清掉；gen 认出是哪次订阅设的
	desk    *DeskReady
	deskGen uint64

	nextBlob atomic.Uint32 // 大块字段的编号（bulk.go）
	lastRead atomic.Int64  // 最近一次收到手机的帧（UnixNano），判断写卡住时连接还活没活着
	// background 是手机报过 App 切到了后台（posse.push.presence），这时照常推送
	background atomic.Bool
}

func (c *conn) setClientCaps(client string, version int, names []string, locale string) {
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
	c.lang = errtext.LangOf(locale)
	c.smu.Unlock()
}

// language 是这条连接上给人看的文字用的语言：握手之后、手机声明能力之前是中文。
func (c *conn) language() errtext.Lang {
	c.smu.Lock()
	defer c.smu.Unlock()
	if c.lang == "" {
		return errtext.ZH
	}
	return c.lang
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
	kind, plain, err := sess.openMessage(typ, raw)
	th := b64(sess.transcriptHash)
	if kind != kindText || err != nil || json.Unmarshal(plain, &auth) != nil ||
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

// sendJSON 加密一条 JSON 消息发给手机。
func (c *conn) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.write(func() (websocket.MessageType, []byte) {
		return websocket.MessageBinary, c.sess.seal(b, kindText)
	})
}

func (c *conn) sendBinary(p []byte) error {
	return c.write(func() (websocket.MessageType, []byte) {
		return websocket.MessageBinary, c.sess.seal(p, kindBinary)
	})
}

// write 在发送锁里加密（计数器和发送顺序必须一致）并写出。等锁或写得久的记下来：
// 分得清是排在别的大消息后面，还是线路本身慢。
func (c *conn) write(seal func() (websocket.MessageType, []byte)) error {
	asked := time.Now()
	c.wmu.Lock()
	defer c.wmu.Unlock()
	got := time.Now()
	typ, frame := seal()
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout(len(frame)))
	defer cancel()
	// 先建好再启动：回调里要用 watch
	var watch *time.Timer
	watch = time.AfterFunc(time.Duration(math.MaxInt64), func() {
		// 写完了（ctx 随函数返回取消）就不再看：Stop 和这里同时进行时，下面的 Reset 会让它一直醒下去
		if ctx.Err() != nil {
			return
		}
		if quiet := time.Since(time.Unix(0, c.lastRead.Load())); quiet >= stallQuiet {
			log.Printf("mobile: write blocked for %v and nothing from the phone for %v, dropping the connection",
				time.Since(got).Round(time.Millisecond), quiet.Round(time.Second))
			cancel()
			return
		}
		watch.Reset(time.Second)
	})
	watch.Reset(stallWrite)
	err := c.ws.Write(ctx, typ, frame)
	watch.Stop()
	wait, took := got.Sub(asked), time.Since(got)
	if wait > time.Second || took > time.Second {
		log.Printf("mobile: send %d bytes waited %v for the lock, wrote in %v", len(frame), wait.Round(time.Millisecond), took.Round(time.Millisecond))
	}
	return err
}

// writeTimeout 是发一条消息最多等多久：至少 15 秒，大消息再按每秒 64 KB 的最低速度加。
// 写超时时 websocket 库会关掉整条连接（手机上所有终端跟着掉线），一张大图、一段视频在
// 慢的线路上（跨境隧道晚高峰）发得久一点不该把连接拖断。
func writeTimeout(n int) time.Duration {
	return 15*time.Second + time.Duration(n/(64<<10))*time.Second
}

// 一条消息写了 stallWrite 还没写完、手机也有 stallQuiet 没发任何东西来，就当连接已经死了（手机被挂起、
// 换了网络），马上断开让它重连，不等到 writeTimeout：发送是串行的，卡住的这条后面所有消息都在等。
// 只看写得久会误伤慢线路：经隧道的 TCP 积着大块数据时，小消息也要写几秒。活着的手机不会安静这么久：
// 手机每 5 秒 ping 一次
var (
	stallWrite = 3 * time.Second
	stallQuiet = 10 * time.Second
)

type request struct {
	ID          string          `json:"id"`
	DeviceToken string          `json:"deviceToken"`
	Method      string          `json:"method"`
	Params      json.RawMessage `json:"params"`
}

func (c *conn) reply(id string, result any) error {
	return c.sendJSON(map[string]any{"id": id, "ok": true, "result": result})
}

// async 在自己的 goroutine 里处理一个请求，返回的错误照 serve 的规矩回给手机。
func (c *conn) async(id string, run func() error) error {
	go func() {
		start := time.Now()
		if err := run(); err != nil {
			c.replyOrFail(id, nil, err)
		}
		if d := time.Since(start); d > 2*time.Second {
			log.Printf("mobile: request %s took %v (off the read loop)", id, d.Round(time.Millisecond))
		}
	}()
	return nil
}

// replyOrFail 回结果或错误：rpcError 带上它的错误码，文件不存在报 not_found。
func (c *conn) replyOrFail(id string, res any, err error) {
	if err == nil {
		c.reply(id, res)
		return
	}
	var re *rpcError
	var de *rpc.Error
	switch {
	case errors.As(err, &de):
		if len(de.Data) > 0 {
			c.failData(id, de.Code, de.Message, de.Data)
		} else {
			c.fail(id, de.Code, de.Message)
		}
	case errors.As(err, &re):
		c.fail(id, re.code, re.msg)
	case errors.Is(err, fs.ErrNotExist):
		c.fail(id, "not_found", errtext.TextIn(c.language(), err))
	default:
		c.fail(id, "internal_error", errtext.TextIn(c.language(), err))
	}
}

func (c *conn) stream(id string, result any) error {
	return c.sendJSON(map[string]any{"id": id, "ok": true, "streaming": true, "result": result})
}

// fail 回一个失败的应答：错误码照旧给程序判断，说明按这条连接的语言给人看。
func (c *conn) fail(id, code, msg string) error {
	return c.failData(id, code, msg, nil)
}

func (c *conn) failData(id, code, msg string, data any) error {
	msg = errtext.MessageIn(c.language(), msg)
	detail := map[string]any{"code": code, "message": msg}
	if data != nil {
		detail["data"] = data
	}
	return c.sendJSON(map[string]any{"id": id, "ok": false, "error": detail})
}

func (c *conn) serve() {
	bad := 0
	for {
		typ, raw, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		c.lastRead.Store(time.Now().UnixNano())
		kind, plain, err := c.sess.openMessage(typ, raw)
		if kind == kindBinary {
			// 手机的二进制帧只有上传的分段（bulk.go），别的解开只为让计数器前进
			if err != nil {
				return
			}
			if op, blob, payload, ok := parseBulk(plain); ok && op == opUploadChunk {
				c.uploadChunk(blob, payload)
			}
			continue
		}
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
		start := time.Now()
		err = c.dispatch(req)
		// 同一连接的请求在这里排队处理：一个卡住，后面的全部超时（手机上一直"电脑没有回应"）
		if d := time.Since(start); d > 2*time.Second {
			log.Printf("mobile: %s took %v", req.Method, d.Round(time.Millisecond))
		}
		if err != nil {
			var re *rpcError
			if errors.As(err, &re) {
				c.fail(req.ID, re.code, re.msg)
			} else {
				c.fail(req.ID, "internal_error", errtext.TextIn(c.language(), err))
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
