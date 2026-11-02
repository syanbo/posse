package mobile

import (
	"bytes"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"
)

// 这里实现 Orca 手机 App 的 E2EE v2（stablyai/orca 的 src/shared/mobile-e2ee-v2-*.ts）：
// X25519 + HSalsa20 得到共享密钥，按握手记录做 HKDF 派生两个方向的密钥和会话 ID，
// 帧是 XSalsa20-Poly1305 secretbox，nonce 由会话 ID、方向、类型和计数器构成。

const transcriptDomain = "orca-mobile-e2ee/v2/transcript"

type helloContext struct {
	Protocol    string  `json:"protocol"`
	Initiator   string  `json:"initiator"`
	Responder   string  `json:"responder"`
	Transport   string  `json:"transport"`
	RelayHostID *string `json:"relayHostId,omitempty"`
}

type helloCaps struct {
	Framing      []int    `json:"framing"`
	PayloadKinds []string `json:"payloadKinds"`
}

type hello struct {
	Type               string       `json:"type"`
	V                  int          `json:"v"`
	ClientPublicKeyB64 string       `json:"clientPublicKeyB64"`
	ClientNonceB64     string       `json:"clientNonceB64"`
	Capabilities       helloCaps    `json:"capabilities"`
	Context            helloContext `json:"context"`
}

type readySelection struct {
	Framing      int      `json:"framing"`
	PayloadKinds []string `json:"payloadKinds"`
}

type ready struct {
	Type                string         `json:"type"`
	V                   int            `json:"v"`
	DesktopPublicKeyB64 string         `json:"desktopPublicKeyB64"`
	ClientNonceB64      string         `json:"clientNonceB64"`
	DesktopNonceB64     string         `json:"desktopNonceB64"`
	Selection           readySelection `json:"selection"`
	Context             helloContext   `json:"context"`
}

var payloadKinds = []string{"text", "binary"}

// session 是一条连接上握手完成后的加密状态。两个方向各一个计数器，text 和 binary 共用。
type session struct {
	recvKey, sendKey [32]byte
	id               [32]byte
	recvN, sendN     uint64
	transcriptHash   [32]byte
	framing          int // 握手选定的分帧，见 framingText、framingBinary
}

// 分帧：Orca App 只会 2，帧是 nonce、密文（明文前有 42 字节的帧头），JSON 帧再整帧 base64 放进文本帧。
// 3 是 Posse 手机客户端的：所有帧都走二进制帧，内容是 u8 kind 加 secretbox 的密文（16 字节标签在前），
// 每帧固定开销 17 字节（2 是 82 字节）。nonce 不上线、明文不带帧头：两端按计数器各自算 nonce，
// 它含会话 ID、方向、kind 和计数器，改了 kind、重放、乱序都解不开，帧头里的这些信息是多余的。
const (
	framingText   = 2
	framingBinary = 3
)

const (
	dirMobileToDesktop = 0
	dirDesktopToMobile = 1
	kindText           = 0
	kindBinary         = 1
)

// exactKeys 检查 JSON 对象的键恰好是 keys：手机对 ready 做同样严格的检查，这里对 hello 也照办。
func exactKeys(raw json.RawMessage, keys ...string) bool {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || len(m) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			return false
		}
	}
	return true
}

// decode32 解出规范的标准 base64（带补位）表示的 32 字节。
func decode32(s string) ([32]byte, bool) {
	var out [32]byte
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != 32 || base64.StdEncoding.EncodeToString(b) != s {
		return out, false
	}
	copy(out[:], b)
	return out, true
}

// parseHello 校验手机发来的明文 hello；只接受直连。
func parseHello(raw []byte) (hello, bool) {
	var h hello
	if !exactKeys(raw, "type", "v", "clientPublicKeyB64", "clientNonceB64", "capabilities", "context") ||
		json.Unmarshal(raw, &h) != nil {
		return h, false
	}
	var obj struct {
		Capabilities json.RawMessage `json:"capabilities"`
		Context      json.RawMessage `json:"context"`
	}
	json.Unmarshal(raw, &obj)
	c := h.Context
	if h.Type != "e2ee_hello" || h.V != 2 ||
		!exactKeys(obj.Capabilities, "framing", "payloadKinds") ||
		len(h.Capabilities.Framing) != 1 ||
		(h.Capabilities.Framing[0] != framingText && h.Capabilities.Framing[0] != framingBinary) ||
		!equalStrings(h.Capabilities.PayloadKinds, payloadKinds) ||
		!exactKeys(obj.Context, "protocol", "initiator", "responder", "transport") ||
		c.Protocol != "orca-mobile-e2ee" || c.Initiator != "mobile" || c.Responder != "desktop" || c.Transport != "direct" {
		return h, false
	}
	if _, ok := decode32(h.ClientPublicKeyB64); !ok {
		return h, false
	}
	if _, ok := decode32(h.ClientNonceB64); !ok {
		return h, false
	}
	return h, true
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// accept 用主机的长期密钥回应 hello，返回要发回的明文 ready 和之后用的加密状态。
func accept(h hello, pub, priv *[32]byte) (ready, *session) {
	var nonce [32]byte
	rand.Read(nonce[:])
	r := ready{
		Type: "e2ee_ready", V: 2,
		DesktopPublicKeyB64: base64.StdEncoding.EncodeToString(pub[:]),
		ClientNonceB64:      h.ClientNonceB64,
		DesktopNonceB64:     base64.StdEncoding.EncodeToString(nonce[:]),
		Selection:           readySelection{Framing: h.Capabilities.Framing[0], PayloadKinds: payloadKinds},
		Context:             h.Context,
	}
	clientPub, _ := decode32(h.ClientPublicKeyB64)
	var shared [32]byte
	box.Precompute(&shared, &clientPub, priv)
	cn, _ := decode32(h.ClientNonceB64)
	s := newSession(shared, transcript(h, r), cn, nonce)
	s.framing = r.Selection.Framing
	return r, s
}

func newSession(shared [32]byte, t []byte, cn, dn [32]byte) *session {
	th := sha256.Sum256(t)
	salt := sha256.Sum256(append(append([]byte("orca-mobile-e2ee/v2/salt\x00"), cn[:]...), dn[:]...))
	info := append([]byte("orca-mobile-e2ee/v2/session\x00"), th[:]...)
	k, _ := hkdf.Key(sha256.New, shared[:], salt[:], string(info), 96)
	s := &session{transcriptHash: th}
	copy(s.recvKey[:], k[:32])
	copy(s.sendKey[:], k[32:64])
	copy(s.id[:], k[64:])
	return s
}

func u32(v int) []byte { return binary.BigEndian.AppendUint32(nil, uint32(v)) }

func strList(ss []string) []byte {
	out := u32(len(ss))
	for _, s := range ss {
		out = append(out, u32(len(s))...)
		out = append(out, s...)
	}
	return out
}

func relayID(c helloContext) string {
	if c.RelayHostID != nil {
		return *c.RelayHostID
	}
	return ""
}

// transcript 按 encodeMobileE2EEV2Transcript 的字段顺序串起握手内容。
func transcript(h hello, r ready) []byte {
	cpub, _ := decode32(h.ClientPublicKeyB64)
	cn, _ := decode32(h.ClientNonceB64)
	dpub, _ := decode32(r.DesktopPublicKeyB64)
	dn, _ := decode32(r.DesktopNonceB64)
	var framing []byte
	framing = append(framing, u32(len(h.Capabilities.Framing))...)
	for _, f := range h.Capabilities.Framing {
		framing = append(framing, u32(f)...)
	}
	ctx := func(p string, c helloContext) [][2][]byte {
		return [][2][]byte{
			{[]byte(p + ".context.protocol"), []byte(c.Protocol)},
			{[]byte(p + ".context.initiator"), []byte(c.Initiator)},
			{[]byte(p + ".context.responder"), []byte(c.Responder)},
			{[]byte(p + ".context.transport"), []byte(c.Transport)},
			{[]byte(p + ".context.relay-host-id"), []byte(relayID(c))},
		}
	}
	fields := [][2][]byte{
		{[]byte("domain"), []byte(transcriptDomain)},
		{[]byte("mobile-to-desktop.type"), []byte(h.Type)},
		{[]byte("mobile-to-desktop.version"), u32(h.V)},
		{[]byte("mobile-to-desktop.client-public-key"), cpub[:]},
		{[]byte("mobile-to-desktop.client-nonce"), cn[:]},
		{[]byte("mobile-to-desktop.capabilities.framing"), framing},
		{[]byte("mobile-to-desktop.capabilities.payload-kinds"), strList(h.Capabilities.PayloadKinds)},
	}
	fields = append(fields, ctx("mobile-to-desktop", h.Context)...)
	fields = append(fields, [][2][]byte{
		{[]byte("desktop-to-mobile.type"), []byte(r.Type)},
		{[]byte("desktop-to-mobile.version"), u32(r.V)},
		{[]byte("desktop-to-mobile.desktop-public-key"), dpub[:]},
		{[]byte("desktop-to-mobile.client-nonce-echo"), cn[:]},
		{[]byte("desktop-to-mobile.desktop-nonce"), dn[:]},
		{[]byte("desktop-to-mobile.selection.framing"), u32(r.Selection.Framing)},
		{[]byte("desktop-to-mobile.selection.payload-kinds"), strList(r.Selection.PayloadKinds)},
	}...)
	fields = append(fields, ctx("desktop-to-mobile", r.Context)...)
	var out []byte
	for _, f := range fields {
		out = append(out, u32(len(f[0]))...)
		out = append(out, f[0]...)
		out = append(out, u32(len(f[1]))...)
		out = append(out, f[1]...)
	}
	return out
}

const headerLen = 32 + 1 + 1 + 8

func (s *session) nonce(dir, kind byte, n uint64) [24]byte {
	var out [24]byte
	copy(out[:12], s.id[:12])
	out[12] = 2
	out[13] = dir
	out[14] = kind
	binary.BigEndian.PutUint64(out[16:], n)
	return out
}

func (s *session) header(dir, kind byte, n uint64) []byte {
	h := make([]byte, 0, headerLen)
	h = append(h, s.id[:]...)
	h = append(h, dir, kind)
	return binary.BigEndian.AppendUint64(h, n)
}

// seal 按握手选的分帧加密一帧发给手机。
func (s *session) seal(payload []byte, kind byte) []byte {
	n := s.sendN
	s.sendN++
	nonce := s.nonce(dirDesktopToMobile, kind, n)
	if s.framing == framingBinary {
		return secretbox.Seal([]byte{kind}, payload, &nonce, &s.sendKey)
	}
	plain := append(s.header(dirDesktopToMobile, kind, n), payload...)
	return secretbox.Seal(nonce[:], plain, &nonce, &s.sendKey)
}

var errFrame = errors.New("bad frame")

// open 解密手机发来的一帧；帧必须按顺序到达。
func (s *session) open(frame []byte, kind byte) ([]byte, error) {
	nonce := s.nonce(dirMobileToDesktop, kind, s.recvN)
	if len(frame) < 24+secretbox.Overhead+headerLen || !bytes.Equal(frame[:24], nonce[:]) {
		return nil, errFrame
	}
	plain, ok := secretbox.Open(nil, frame[24:], &nonce, &s.recvKey)
	if !ok || !bytes.Equal(plain[:headerLen], s.header(dirMobileToDesktop, kind, s.recvN)) {
		return nil, errFrame
	}
	s.recvN++
	return plain[headerLen:], nil
}

// sealMessage 加密一条 JSON 消息，返回该用的帧类型和帧内容。
func (s *session) sealMessage(p []byte) (websocket.MessageType, []byte) {
	if s.framing == framingBinary {
		return websocket.MessageBinary, s.seal(p, kindText)
	}
	return websocket.MessageText, s.sealText(p)
}

// openMessage 解开手机发来的一帧，返回它的 kind（kindText 是 JSON）。
func (s *session) openMessage(typ websocket.MessageType, raw []byte) (byte, []byte, error) {
	if s.framing == framingBinary {
		if typ != websocket.MessageBinary || len(raw) < 1+secretbox.Overhead {
			return 0, nil, errFrame
		}
		kind := raw[0]
		if kind != kindText && kind != kindBinary {
			return 0, nil, errFrame
		}
		nonce := s.nonce(dirMobileToDesktop, kind, s.recvN)
		p, ok := secretbox.Open(nil, raw[1:], &nonce, &s.recvKey)
		if !ok {
			return kind, nil, errFrame
		}
		s.recvN++
		return kind, p, nil
	}
	if typ == websocket.MessageText {
		p, err := s.openText(raw)
		return kindText, p, err
	}
	p, err := s.open(raw, kindBinary)
	return kindBinary, p, err
}

// sealText 加密 JSON 文本，结果是放进 WebSocket 文本帧的 base64。
func (s *session) sealText(p []byte) []byte {
	f := s.seal(p, kindText)
	out := make([]byte, base64.StdEncoding.EncodedLen(len(f)))
	base64.StdEncoding.Encode(out, f)
	return out
}

func (s *session) openText(b64 []byte) ([]byte, error) {
	f, err := base64.StdEncoding.Strict().DecodeString(string(b64))
	if err != nil {
		return nil, errFrame
	}
	return s.open(f, kindText)
}
