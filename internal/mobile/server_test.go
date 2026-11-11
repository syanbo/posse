package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	"posse/internal/projicon"
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
	gitRecs     []GitRecord
	// working 不为空时工作区里多一个正在工作的这个 agent
	working string
	// extra 是 w1 之外的工作区
	extra []Workspace
}

func (h *fakeHost) Desk(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error) {
	if h.desk == nil {
		return DeskReady{}, DeskError("capture_failed", "no desk in tests")
	}
	return h.desk(ctx, req, notify)
}

func (h *fakeHost) SessionTerminal(agentName, sessionID string) host.Terminal {
	for _, w := range h.State().Workspaces {
		for _, t := range w.Terminals {
			if t.SessionID == sessionID && (agentName == "" || t.Agent == agentName) {
				return h.Terminal(t.ID)
			}
		}
	}
	return nil
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
	if h.working != "" {
		w.Terminals = append(w.Terminals, Terminal{ID: "busy", Agent: h.working, Status: "working", Running: true})
	}
	return State{Projects: []Project{{ID: "p1", Name: "demo", Path: "/tmp"}}, Workspaces: append([]Workspace{w}, h.extra...)}
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

func (h *fakeHost) CloseTerminal(id string) error { return nil }
func (h *fakeHost) RecordGit(r GitRecord) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.gitRecs = append(h.gitRecs, r)
}
func (h *fakeHost) Restore(id string) host.Terminal           { return nil }
func (h *fakeHost) View(id string) func()                     { return func() {} }
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

// phone 扮演 Posse 手机客户端的连接。
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
		"capabilities": map[string]any{"framing": []int{framingBinary}, "payloadKinds": []string{"text", "binary"}},
		"context":      map[string]any{"protocol": "orca-mobile-e2ee", "initiator": "mobile", "responder": "desktop", "transport": "direct"},
	})
	ws.Write(ctx, websocket.MessageText, hb)
	_, rb, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var r ready
	json.Unmarshal(rb, &r)
	if r.Selection.Framing != framingBinary {
		t.Fatalf("ready selected framing %d", r.Selection.Framing)
	}
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
	at, ab, err := ws.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if at != websocket.MessageBinary {
		t.Fatalf("auth reply came as %v", at)
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
	p.ws.Write(context.Background(), websocket.MessageBinary, p.seal(kindText, b))
}

// seal 加密手机发出的一帧。借用 recvN 当手机的发送计数，密钥用主机的 recvKey。
func (p *phone) seal(kind byte, b []byte) []byte {
	s := p.sess
	n := s.recvN
	s.recvN++
	nonce := s.nonce(dirMobileToDesktop, kind, n)
	return secretbox.Seal([]byte{kind}, b, &nonce, &s.recvKey)
}

func (p *phone) open(raw []byte, kind byte) []byte {
	s := p.sess
	nonce := s.nonce(dirDesktopToMobile, kind, s.sendN)
	if len(raw) < 1 || raw[0] != kind {
		p.t.Fatalf("host frame %d: kind byte %v, want %d", s.sendN, raw[:min(1, len(raw))], kind)
	}
	plain, ok := secretbox.Open(nil, raw[1:], &nonce, &s.sendKey)
	if !ok {
		p.t.Fatalf("cannot open host frame %d", s.sendN)
	}
	s.sendN++
	return plain
}

func (p *phone) readLoop() {
	for {
		typ, raw, err := p.ws.Read(context.Background())
		if err != nil {
			close(p.resp)
			return
		}
		if typ != websocket.MessageBinary || len(raw) < 1 {
			p.t.Errorf("got a %v frame of %d bytes", typ, len(raw))
			continue
		}
		if raw[0] == kindBinary {
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
	home := t.TempDir()
	srv, err := New(home, host, -1)
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
	if row["worktreeId"] != "p1::w1" || row["status"] != "permission" || row["displayName"] != "demo" || row["repoIcon"] != nil {
		t.Fatalf("row %v", row)
	}
	// 桌面设了项目图标：每次列工作区时读，不用重启；图片按文件名取，只认图标目录里的 PNG
	if err := projicon.Save(home, map[string]projicon.Icon{"p1": {Image: "p1-1.png"}}); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(projicon.Dir(home), "p1-1.png"), []byte("png"), 0o644)
	ps = result(t, p.next(p.call("worktree.ps", map[string]any{"limit": 10000})))
	if icon := ps["worktrees"].([]any)[0].(map[string]any)["repoIcon"]; fmt.Sprint(icon) != "map[image:p1-1.png]" {
		t.Fatalf("repoIcon %v", icon)
	}
	if got := result(t, p.next(p.call("posse.projectIcon", map[string]any{"name": "p1-1.png"}))); got["data"] != "cG5n" {
		t.Fatalf("icon data %v", got)
	}
	if m := p.next(p.call("posse.projectIcon", map[string]any{"name": "../mobile.json"})); m["ok"] == true {
		t.Fatalf("read outside icon dir: %v", m)
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
	firstStream := uint32(sub["streamId"].(float64))
	// 没声明 posse.termcolors.v1 的（Orca App）不发配色；声明了再订阅一次
	if _, ok := sub["colors"]; ok {
		t.Fatalf("colors sent to a client without %s: %v", termColorsCap, sub)
	}
	for f := range p.binary {
		if f[2] == opSnapshotEnd {
			break
		}
	}
	p.next(p.call("terminal.unsubscribe", map[string]any{"requestId": tid}))
	p.next(p.call("runtime.clientCapabilities.update", map[string]any{"client": "posse-flutter", "version": 1, "capabilities": []string{termColorsCap}}))
	tid = p.call("terminal.subscribe", map[string]any{
		"terminal": "t_1", "client": map[string]any{"id": p.token, "type": "mobile"},
		"viewport": map[string]any{"cols": 46, "rows": 30}, "capabilities": map[string]any{"terminalBinaryStream": 1},
	})
	sub = result(t, p.next(tid))
	if sub["type"] != "subscribed" || sub["cols"] != float64(46) || sub["rows"] != float64(30) {
		t.Fatalf("subscribed %v", sub)
	}
	// 订阅时带上程序按它配色的那套，手机照它画终端
	if colors, _ := sub["colors"].(map[string]any); colors["background"] != fmt.Sprintf("#%02x%02x%02x", tm.Colors().Background.R, tm.Colors().Background.G, tm.Colors().Background.B) {
		t.Fatalf("subscribed colors %v", sub["colors"])
	}
	if c, r := tm.Size(); c != 46 || r != 30 {
		t.Fatalf("terminal not phone-sized: %dx%d", c, r)
	}
	// 快照：start → chunk* → end。第一次订阅的流（已经退订）可能还有排在后面的输出帧（shell 晚到的提示符），
	// 帧带着流 ID，客户端按它丢掉旧流的；新流的第一帧必须是快照开始
	frame := <-p.binary
	for binary.LittleEndian.Uint32(frame[4:]) != uint32(sub["streamId"].(float64)) && binary.LittleEndian.Uint32(frame[4:]) == firstStream {
		frame = <-p.binary
	}
	if frame[2] != opSnapshotStart || binary.LittleEndian.Uint32(frame[4:]) != uint32(sub["streamId"].(float64)) {
		t.Fatalf("first frame op %d", frame[2])
	}
	for f := range p.binary {
		if f[2] == opSnapshotEnd {
			break
		}
	}

	// 窗口换了配色，手机收到一条 colors
	tm.SetColors(term.DefaultColors(!tm.Colors().Dark()))
	if m := result(t, p.next(tid)); m["type"] != "colors" || m["colors"].(map[string]any)["background"] == sub["colors"].(map[string]any)["background"] {
		t.Fatalf("colors update %v", m)
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

// rawKeys 一个一个写：Cursor Agent 这类程序一次读到几个按键就当成一段输入。读的一方每读一次打一行。
func TestTerminalSendRawKeysSeparately(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("needs python3")
	}
	script := `import os, sys, tty
tty.setraw(0)
while True:
    b = os.read(0, 64)
    sys.stdout.write("read:" + b.hex() + "\r\n")
    sys.stdout.flush()
`
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"python3", "-c", script}, Cols: 80, Rows: 24})
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
	// 等 python 进了 raw 模式再发，不然按键被行规程吃掉
	time.Sleep(500 * time.Millisecond)
	enc := base64.StdEncoding.EncodeToString
	send := result(t, p.next(p.call("terminal.send", map[string]any{
		"terminal": "t_1",
		"rawKeys":  []string{enc([]byte{0x05}), enc([]byte{0x15}), enc([]byte{0x7f})},
		"raw":      enc([]byte{0x05, 0x15, 0x7f}),
	})))
	if s := send["send"].(map[string]any); s["accepted"] != true || s["bytesWritten"] != float64(3) {
		t.Fatalf("rawKeys send %v", send)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !(tm.ContainsText("read:05") && tm.ContainsText("read:15") && tm.ContainsText("read:7f")) {
		if time.Now().After(deadline) {
			t.Fatalf("keys not read one by one\n%s", strings.Join(tm.Lines(), "\n"))
		}
		time.Sleep(20 * time.Millisecond)
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
