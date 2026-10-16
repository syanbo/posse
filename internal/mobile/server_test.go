package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"hive/internal/term"
)

// fakeHost 是一个项目、一个工作区和一个 shell 终端。
type fakeHost struct {
	mu    sync.Mutex
	terms map[string]*term.Terminal
	order []string
	dir   string // 工作区目录，空是 /tmp
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
		w.Terminals = append(w.Terminals, Terminal{ID: id, Agent: "claude", Status: "waiting", Running: t.Running(), CreatedAt: t.CreatedAt, LastOutput: t.LastOutput()})
	}
	return State{Projects: []Project{{ID: "p1", Name: "demo", Path: "/tmp"}}, Workspaces: []Workspace{w}}
}

func (h *fakeHost) Terminal(id string) *term.Terminal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.terms[id]
}

func (h *fakeHost) CreateTerminal(wid, agent, prompt, command string) (string, error) {
	return "", errors.New("not supported")
}

func (h *fakeHost) CloseTerminal(id string) error { return nil }
func (h *fakeHost) Agents() []string              { return []string{"claude"} }

// phone 扮演 Orca 手机 App 的连接。
type phone struct {
	t      *testing.T
	ws     *websocket.Conn
	sess   *session
	token  string
	n      int
	binary chan []byte
	resp   chan map[string]any
}

func dialPhone(t *testing.T, srv *Server, info PairInfo) *phone {
	raw, _ := base64.RawURLEncoding.DecodeString(info.Code)
	var offer struct {
		V            int    `json:"v"`
		Endpoint     string `json:"endpoint"`
		DeviceToken  string `json:"deviceToken"`
		PublicKeyB64 string `json:"publicKeyB64"`
	}
	if err := json.Unmarshal(raw, &offer); err != nil || offer.V != 2 {
		t.Fatalf("bad offer %s", raw)
	}
	ctx := context.Background()
	ws, _, err := websocket.Dial(ctx, offer.Endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	ws.SetReadLimit(8 << 20)
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

// next 等下一条 id 匹配的回复。
func (p *phone) next(id string) map[string]any {
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-p.resp:
			if !ok {
				p.t.Fatal("connection closed")
			}
			if m["id"] == id {
				return m
			}
		case <-timeout:
			p.t.Fatalf("no reply for %s", id)
		}
	}
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
