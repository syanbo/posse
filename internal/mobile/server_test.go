package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"posse/internal/host"
	"posse/internal/rpc"
	"posse/internal/term"
)

// fakeHost 是一个项目、一个工作区和一个 shell 终端。
type fakeHost struct {
	mu    sync.Mutex
	terms map[string]*term.Terminal
	order []string
	dir   string // 工作区目录，空是 /tmp
	desk  func(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error)
	// historyGate 不为空时历史查询等它关上才返回，模拟等索引
	historyGate chan struct{}
}

func (h *fakeHost) Desk(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error) {
	if h.desk == nil {
		return DeskReady{}, DeskError("capture_failed", "no desk in tests")
	}
	return h.desk(ctx, req, notify)
}

func (h *fakeHost) State() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	dir := h.dir
	if dir == "" {
		dir = "/tmp"
	}
	w := Workspace{ID: "w1", ProjectID: "p1", Name: "main", Path: dir, Kind: "main", CreatedAt: time.Now()}
	for _, id := range h.order {
		t := h.terms[id]
		// 没有 hook 上报时保持 claude/waiting：已有的手机会话测试靠这个变成 permission。
		// 终端上一旦有真实 agent 状态，就原样带上会话和审批字段。
		row := Terminal{ID: id, Agent: "claude", Status: "waiting", Running: t.Running(), CreatedAt: t.CreatedAt, LastOutput: t.LastOutput()}
		if a := t.Agent(); a.Status != "" || a.SessionID != "" || a.TranscriptPath != "" || a.ToolName != "" || a.ToolInput != "" || a.Prompt != "" || a.Message != "" {
			if a.Status != "" {
				row.Status = a.Status
			}
			row.StatusAt = a.At
			row.Message = a.Message
			row.SessionID = a.SessionID
			row.TranscriptPath = a.TranscriptPath
			row.ToolName = a.ToolName
			row.ToolInput = a.ToolInput
			row.Prompt = a.Prompt
		}
		w.Terminals = append(w.Terminals, row)
	}
	return State{Projects: []Project{{ID: "p1", Name: "demo", Path: "/tmp"}}, Workspaces: []Workspace{w}}
}

func (h *fakeHost) Terminal(id string) host.Terminal {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.terms[id]; ok {
		return host.Wrap(t)
	}
	return nil
}

func (h *fakeHost) CreateTerminal(wid, agent, prompt, command string) (string, error) {
	return "", errors.New("not supported")
}

func (h *fakeHost) CloseTerminal(id string) error             { return nil }
func (h *fakeHost) Agents() []string                          { return []string{"claude"} }
func (h *fakeHost) RefreshUsage(context.Context) (any, error) { return h.Usage(), nil }
func (h *fakeHost) Usage() any {
	return map[string]any{"usages": []map[string]any{{"agent": "codex", "windows": []map[string]any{{"percent": 7, "minutes": 300}}}}}
}

// History 假装常驻进程：list 回一个会话，resume 报找不到工作区。
func (h *fakeHost) History(_ context.Context, method string, params json.RawMessage) (any, error) {
	if h.historyGate != nil {
		<-h.historyGate
	}
	switch method {
	case "history.list", "history.search":
		return map[string]any{"sessions": []map[string]any{{"agent": "claude", "id": "s1", "title": "修 bug", "path": "/tmp/s1.jsonl"}}, "params": json.RawMessage(params)}, nil
	}
	return nil, &rpc.Error{Code: rpc.CodeNotFound, Message: "no workspace contains /x; pass a workspace"}
}

// phoneWire 是测试手机用的传输：WebSocket 或 QUIC。
type phoneWire interface {
	Read(ctx context.Context) (websocket.MessageType, []byte, error)
	Write(ctx context.Context, typ websocket.MessageType, p []byte) error
	CloseNow() error
}

// phone 扮演 Orca 手机 App 的连接。
type phone struct {
	t      *testing.T
	ws     phoneWire
	sess   *session
	token  string
	n      int
	binary chan []byte
	resp   chan map[string]any
	held   []map[string]any
}

type testOffer struct {
	V            int    `json:"v"`
	Endpoint     string `json:"endpoint"`
	DeviceToken  string `json:"deviceToken"`
	PublicKeyB64 string `json:"publicKeyB64"`
}

func parseTestOffer(t *testing.T, info PairInfo) testOffer {
	raw, _ := base64.RawURLEncoding.DecodeString(info.Code)
	var offer testOffer
	if err := json.Unmarshal(raw, &offer); err != nil || offer.V != 2 {
		t.Fatalf("bad offer %s", raw)
	}
	return offer
}

func dialPhone(t *testing.T, srv *Server, info PairInfo) *phone {
	offer := parseTestOffer(t, info)
	ws, _, err := websocket.Dial(context.Background(), offer.Endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(8 << 20)
	return handshakePhone(t, ws, offer)
}

// handshakePhone 在连好的传输上走 hello、ready、auth。
func handshakePhone(t *testing.T, ws phoneWire, offer testOffer) *phone {
	ctx := context.Background()
	cpub, cpriv, _ := box.GenerateKey(rand.Reader)
	var cn [32]byte
	rand.Read(cn[:])
	hb, _ := json.Marshal(map[string]any{
		"type": "e2ee_hello", "v": 2, "clientPublicKeyB64": b64(*cpub), "clientNonceB64": b64(cn),
		"capabilities": map[string]any{"framing": []int{2}, "payloadKinds": []string{"text", "binary"}},
		"context":      map[string]any{"protocol": "orca-mobile-e2ee", "initiator": "mobile", "responder": "desktop", "transport": "direct"},
	})
	ws.Write(ctx, websocket.MessageText, hb)
	_, rb, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var r ready
	json.Unmarshal(rb, &r)
	if r.DesktopPublicKeyB64 != offer.PublicKeyB64 {
		t.Fatalf("ready key %s does not match offer", r.DesktopPublicKeyB64)
	}
	h, _ := parseHello(hb)
	hostPub, _ := decode32(r.DesktopPublicKeyB64)
	dn, _ := decode32(r.DesktopNonceB64)
	var shared [32]byte
	box.Precompute(&shared, &hostPub, cpriv)
	s := newSession(shared, transcript(h, r), cn, dn)
	p := &phone{t: t, ws: ws, sess: s, token: offer.DeviceToken, binary: make(chan []byte, 256), resp: make(chan map[string]any, 256)}
	p.sendRaw(map[string]any{"type": "e2ee_auth", "v": 2, "transcriptHashB64": b64(s.transcriptHash), "deviceToken": offer.DeviceToken})
	_, ab, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	plain := p.open(ab, kindText)
	if !strings.Contains(string(plain), "e2ee_authenticated") {
		t.Fatalf("auth reply %s", plain)
	}
	go p.readLoop()
	return p
}

// 手机方向：发用 mobile→desktop、收用 desktop→mobile；secretbox 密钥同样对调。
func (p *phone) sendRaw(v any) {
	b, _ := json.Marshal(v)
	s := p.sess
	n := s.recvN // 借用计数器字段：手机的发送计数
	s.recvN++
	nonce := s.nonce(dirMobileToDesktop, kindText, n)
	frame := secretbox.Seal(nonce[:], append(s.header(dirMobileToDesktop, kindText, n), b...), &nonce, &s.recvKey)
	p.ws.Write(context.Background(), websocket.MessageText, []byte(base64.StdEncoding.EncodeToString(frame)))
}

func (p *phone) open(raw []byte, kind byte) []byte {
	s := p.sess
	if kind == kindText {
		raw, _ = base64.StdEncoding.DecodeString(string(raw))
	}
	nonce := s.nonce(dirDesktopToMobile, kind, s.sendN)
	plain, ok := secretbox.Open(nil, raw[24:], &nonce, &s.sendKey)
	if !ok {
		p.t.Fatalf("cannot open host frame %d", s.sendN)
	}
	s.sendN++
	return plain[headerLen:]
}

func (p *phone) readLoop() {
	for {
		typ, raw, err := p.ws.Read(context.Background())
		if err != nil {
			close(p.resp)
			return
		}
		if typ == websocket.MessageBinary {
			p.binary <- p.open(raw, kindBinary)
			continue
		}
		var m map[string]any
		json.Unmarshal(p.open(raw, kindText), &m)
		p.resp <- m
	}
}

func (p *phone) call(method string, params any) string {
	p.n++
	id := "rpc-" + strconv.Itoa(p.n)
	p.sendRaw(map[string]any{"id": id, "deviceToken": p.token, "method": method, "params": params})
	return id
}

// next 等下一条 id 匹配的回复。别的 id 先留着，免得两条流交错时把其中一条丢掉。
func (p *phone) next(id string) map[string]any {
	m, ok := p.nextWithin(id, 10*time.Second)
	if !ok {
		p.t.Fatalf("no reply for %s", id)
	}
	return m
}

func (p *phone) nextWithin(id string, d time.Duration) (map[string]any, bool) {
	if m, ok := p.pull(id); ok {
		return m, true
	}
	timeout := time.After(d)
	for {
		select {
		case m, ok := <-p.resp:
			if !ok {
				p.t.Fatal("connection closed")
			}
			if m["id"] == id {
				return m, true
			}
			p.held = append(p.held, m)
		case <-timeout:
			return nil, false
		}
	}
}

func (p *phone) nextAny(d time.Duration) (map[string]any, bool) {
	if len(p.held) > 0 {
		m := p.held[0]
		p.held = p.held[1:]
		return m, true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case m, ok := <-p.resp:
		if !ok {
			p.t.Fatal("connection closed")
		}
		return m, true
	case <-timer.C:
		return nil, false
	}
}

func (p *phone) pull(id string) (map[string]any, bool) {
	for i, m := range p.held {
		if m["id"] == id {
			p.held = append(p.held[:i], p.held[i+1:]...)
			return m, true
		}
	}
	return nil, false
}

func result(t *testing.T, m map[string]any) map[string]any {
	if m["ok"] != true {
		t.Fatalf("rpc failed: %v", m)
	}
	r, _ := m["result"].(map[string]any)
	return r
}

func TestPhoneSession(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.URL, "orca://pair?code=") {
		t.Fatalf("url %s", info.URL)
	}
	p := dialPhone(t, srv, info)
	if len(srv.Devices()) != 1 {
		t.Fatal("device not recorded as paired")
	}

	st := result(t, p.next(p.call("status.get", nil)))
	if st["protocolVersion"] != float64(3) {
		t.Fatalf("status %v", st)
	}
	ps := result(t, p.next(p.call("worktree.ps", map[string]any{"limit": 10000})))
	rows := ps["worktrees"].([]any)
	row := rows[0].(map[string]any)
	if row["worktreeId"] != "p1::w1" || row["status"] != "permission" || row["displayName"] != "demo" {
		t.Fatalf("row %v", row)
	}

	sid := p.call("session.tabs.subscribe", map[string]any{"worktree": "id:p1::w1"})
	snap := p.next(sid)
	r := snap["result"].(map[string]any)
	if snap["streaming"] != true || r["type"] != "snapshot" || r["activeTabId"] != surfaceID("t_1") {
		t.Fatalf("tabs snapshot %v", snap)
	}

	tid := p.call("terminal.subscribe", map[string]any{
		"terminal": "t_1", "client": map[string]any{"id": p.token, "type": "mobile"},
		"viewport": map[string]any{"cols": 46, "rows": 30}, "capabilities": map[string]any{"terminalBinaryStream": 1},
	})
	sub := result(t, p.next(tid))
	if sub["type"] != "subscribed" || sub["cols"] != float64(46) || sub["rows"] != float64(30) {
		t.Fatalf("subscribed %v", sub)
	}
	if c, r := tm.Size(); c != 46 || r != 30 {
		t.Fatalf("terminal not phone-sized: %dx%d", c, r)
	}
	// 快照：start → chunk* → end
	frame := <-p.binary
	if frame[2] != opSnapshotStart || binary.LittleEndian.Uint32(frame[4:]) != uint32(sub["streamId"].(float64)) {
		t.Fatalf("first frame op %d", frame[2])
	}
	for f := range p.binary {
		if f[2] == opSnapshotEnd {
			break
		}
	}

	// 界面这时调整尺寸只记下来，不打断手机
	tm.Resize(100, 30)
	if c, _ := tm.Size(); c != 46 {
		t.Fatal("desktop resize applied while phone is watching")
	}

	sendID := p.call("terminal.send", map[string]any{"terminal": "t_1", "text": "echo hi-$((40+2))", "enter": true})
	send := result(t, p.next(sendID))
	if send["send"].(map[string]any)["accepted"] != true {
		t.Fatalf("send %v", send)
	}
	var out strings.Builder
	deadline := time.After(10 * time.Second)
	for !strings.Contains(out.String(), "hi-42") {
		select {
		case f := <-p.binary:
			if f[2] == opOutput {
				out.Write(f[16:])
			}
		case <-deadline:
			t.Fatalf("no echo output, got %q", out.String())
		}
	}

	p.next(p.call("terminal.unsubscribe", map[string]any{"subscriptionId": "t_1:" + p.token, "requestId": tid}))
	deadline = time.After(5 * time.Second)
	for {
		if c, r := tm.Size(); c == 100 && r == 30 {
			break
		}
		select {
		case <-deadline:
			c, r := tm.Size()
			t.Fatalf("size not restored to desktop: %dx%d", c, r)
		case <-time.After(50 * time.Millisecond):
		}
	}

	// 移除设备后旧 token 不再能用
	dev := srv.Devices()[0]
	srv.RemoveDevice(dev.ID)
	if srv.pair.valid(p.token) {
		t.Fatal("token still valid after removal")
	}
}

func TestTerminalSendRawKeepsUTF8(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	const text = "看下今天"
	raw := base64.StdEncoding.EncodeToString([]byte(text))
	send := result(t, p.next(p.call("terminal.send", map[string]any{
		"terminal": "t_1", "raw": raw, "enter": true,
	})))
	if send["send"].(map[string]any)["accepted"] != true {
		t.Fatalf("raw send %v", send)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !tm.ContainsText(text) {
		if time.Now().After(deadline) {
			t.Fatalf("pty did not echo raw utf-8 %q\n%s", text, strings.Join(tm.Lines(), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
	}
	bad := p.next(p.call("terminal.send", map[string]any{"terminal": "t_1", "raw": "!!!!"}))
	if bad["ok"] != false {
		t.Fatalf("bad raw accepted %v", bad)
	}
}

func TestUTF8Complete(t *testing.T) {
	b := []byte("ab你")
	for i := range len(b) + 1 {
		n := utf8Complete(b[:i])
		if want := min(i, 2); i == len(b) {
			if n != len(b) {
				t.Errorf("full: %d", n)
			}
		} else if n != want {
			t.Errorf("prefix %d: got %d want %d", i, n, want)
		}
	}
}

func TestResolveEndpoint(t *testing.T) {
	ok := map[string]string{
		"100.64.1.2":                      "ws://100.64.1.2:6788",
		"home.example.com:8443":           "ws://home.example.com:8443",
		"fd00::1":                         "ws://[fd00::1]:6788",
		"[fd00::1]:9000":                  "ws://[fd00::1]:9000",
		"https://orca.example.uk/v11v7w0": "wss://orca.example.uk/v11v7w0",
		"HTTPS://example.com/":            "wss://example.com",
		"http://10.0.0.2:8080":            "ws://10.0.0.2:8080",
		"wss://example.com/a?b=1":         "wss://example.com/a?b=1",
		"  ws://example.com:6788  ":       "ws://example.com:6788",
	}
	for in, want := range ok {
		got, err := ResolveEndpoint(in, 6788)
		if err != nil || got != want {
			t.Errorf("ResolveEndpoint(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "0.0.0.0", "host:0", "host:65536", "host/path", "ftp://example.com",
		"https://user@example.com", "https://example.com/#x", "ws://0.0.0.0:1", "a b", "host:"} {
		if got, err := ResolveEndpoint(in, 6788); err == nil {
			t.Errorf("ResolveEndpoint(%q) = %q, want error", in, got)
		}
	}
}

// 手机不发关闭帧直接断开（断网、App 被杀）时，服务端也要关掉自己这一端的连接。
func TestAbruptDisconnectReleasesConn(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	base := openFDs(t)
	for range 3 {
		p := dialPhone(t, srv, info)
		result(t, p.next(p.call("status.get", nil)))
		p.ws.CloseNow()
	}
	deadline := time.Now().Add(5 * time.Second)
	for openFDs(t) > base {
		if time.Now().After(deadline) {
			t.Fatalf("server kept %d connections open after the phones dropped", openFDs(t)-base)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func openFDs(t *testing.T) int {
	es, err := os.ReadDir("/dev/fd")
	if err != nil {
		t.Skip(err)
	}
	return len(es)
}

func TestOfferEndpoints(t *testing.T) {
	code := offerCode("ws://127.0.0.1:6788", []string{
		"ws://127.0.0.1:6788", "ws://100.1.2.3:6788", "wss://tun.example/posse", "",
	}, "tok", rep32(9), "dev")
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil {
		t.Fatal(err)
	}
	var offer struct {
		V            int      `json:"v"`
		Endpoint     string   `json:"endpoint"`
		Endpoints    []string `json:"endpoints"`
		DeviceToken  string   `json:"deviceToken"`
		PublicKeyB64 string   `json:"publicKeyB64"`
		PairedDevice string   `json:"pairedDeviceId"`
		Scope        string   `json:"scope"`
	}
	if err := json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	if offer.V != 2 || offer.Endpoint != "ws://127.0.0.1:6788" || offer.DeviceToken != "tok" ||
		offer.Scope != "mobile" || offer.PairedDevice != "dev" || offer.PublicKeyB64 != b64(rep32(9)) {
		t.Fatalf("offer %+v", offer)
	}
	want := []string{"ws://127.0.0.1:6788", "ws://100.1.2.3:6788", "wss://tun.example/posse"}
	if !reflect.DeepEqual(offer.Endpoints, want) {
		t.Fatalf("endpoints %v", offer.Endpoints)
	}
}

func TestPairKeepsPrimaryEndpoint(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	want := "ws://127.0.0.1:" + strconv.Itoa(srv.Port())
	// 选了地址就只有它：再带上本机其他地址，手机并发连接时会被先连上的挤掉
	if info.Endpoint != want || len(info.Endpoints) != 1 || info.Endpoints[0] != want {
		t.Fatalf("pair %+v", info)
	}
	seen := map[string]bool{}
	for _, ep := range info.Endpoints {
		if seen[ep] {
			t.Fatalf("duplicate %s in %v", ep, info.Endpoints)
		}
		seen[ep] = true
		if !strings.HasPrefix(ep, "ws://") && !strings.HasPrefix(ep, "wss://") {
			t.Fatalf("endpoint %s", ep)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(info.Code)
	if err != nil {
		t.Fatal(err)
	}
	var offer struct {
		Endpoint  string   `json:"endpoint"`
		Endpoints []string `json:"endpoints"`
	}
	if err := json.Unmarshal(raw, &offer); err != nil {
		t.Fatal(err)
	}
	if offer.Endpoint != want || !reflect.DeepEqual(offer.Endpoints, info.Endpoints) {
		t.Fatalf("code endpoint %s list %v", offer.Endpoint, offer.Endpoints)
	}
}

func TestClientCapabilities(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	defer p.ws.CloseNow()
	id := p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1,
		"capabilities": []string{"posse.permission.v1", "posse.git.v1"},
	})
	if result(t, p.next(id)) == nil {
		t.Fatal("empty result")
	}
	c := onlyConn(t, srv)
	if c.clientName != "posse-flutter" || c.clientVersion != 1 ||
		!c.hasCapability("posse.permission.v1") || !c.hasCapability("posse.git.v1") || c.hasCapability("nope") {
		t.Fatalf("caps %s %d %v", c.clientName, c.clientVersion, c.caps)
	}
	id = p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "other", "version": 2, "capabilities": []string{},
	})
	result(t, p.next(id))
	if c.hasCapability("posse.permission.v1") || c.clientName != "other" || c.clientVersion != 2 {
		t.Fatal("capabilities were not replaced")
	}
}

func TestRuntimePing(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	defer p.ws.CloseNow()
	if r := result(t, p.next(p.call("runtime.ping", map[string]any{}))); len(r) != 0 {
		t.Fatalf("ping result %v", r)
	}
}

func onlyConn(t *testing.T, srv *Server) *conn {
	t.Helper()
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if len(srv.conns) != 1 {
		t.Fatalf("conns %d", len(srv.conns))
	}
	for c := range srv.conns {
		return c
	}
	return nil
}

func TestPhoneIdentityMergesTokens(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()
	pair := func() *phone {
		info, err := srv.Pair(false, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return dialPhone(t, srv, info)
	}
	identify := func(p *phone, tokens ...string) {
		result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
			"client": "posse-flutter", "version": 1, "capabilities": []string{},
			"phone": map[string]any{"id": "install-1", "name": "iPhone 16 Pro", "tokens": tokens},
		})))
	}
	p1 := pair()
	defer p1.ws.CloseNow()
	identify(p1, p1.token)
	// 重新扫码：手机只存着新 token，旧的作废
	p2 := pair()
	defer p2.ws.CloseNow()
	identify(p2, p2.token)
	ds := srv.Devices()
	if len(ds) != 1 || ds[0].Name != "iPhone 16 Pro" || ds[0].Links != 1 || !ds[0].Online {
		t.Fatalf("after rescan %+v", ds)
	}
	if srv.pair.valid(p1.token) {
		t.Fatal("replaced token still valid")
	}
	// 旧版 App 连上的 token 先单独算一台，手机报上它还存着时归过来
	p3 := pair()
	defer p3.ws.CloseNow()
	if n := len(srv.Devices()); n != 2 {
		t.Fatalf("legacy device not listed separately: %d", n)
	}
	identify(p2, p2.token, p3.token)
	ds = srv.Devices()
	if len(ds) != 1 || ds[0].Links != 2 {
		t.Fatalf("after claim %+v", ds)
	}
	if !srv.RemoveDevice(ds[0].ID) || len(srv.Devices()) != 0 || srv.pair.valid(p2.token) || srv.pair.valid(p3.token) {
		t.Fatal("remove did not drop every token of the phone")
	}
}
