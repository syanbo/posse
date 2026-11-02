package mobile

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"
)

func rep32(b byte) [32]byte { return [32]byte(bytes.Repeat([]byte{b}, 32)) }

// TestTranscriptVector 对照 Orca 的 src/shared/mobile-e2ee-v2-fixtures.ts。
func TestTranscriptVector(t *testing.T) {
	relay := "AbCdEf0123_-xyZ9"
	ctx := helloContext{Protocol: "orca-mobile-e2ee", Initiator: "mobile", Responder: "desktop", Transport: "relay", RelayHostID: &relay}
	h := hello{Type: "e2ee_hello", V: 2, ClientPublicKeyB64: b64(rep32(1)), ClientNonceB64: b64(rep32(2)),
		Capabilities: helloCaps{Framing: []int{2}, PayloadKinds: payloadKinds}, Context: ctx}
	r := ready{Type: "e2ee_ready", V: 2, DesktopPublicKeyB64: b64(rep32(3)), ClientNonceB64: b64(rep32(2)), DesktopNonceB64: b64(rep32(4)),
		Selection: readySelection{Framing: 2, PayloadKinds: payloadKinds}, Context: ctx}
	tr := transcript(h, r)
	if len(tr) != 1347 {
		t.Fatalf("transcript length %d", len(tr))
	}
	th := sha256.Sum256(tr)
	s := newSession(rep32(5), tr, rep32(2), rep32(4))
	for name, got := range map[string][]byte{
		"ca6385f8bbf64a223fdd59587bfb67e2373891ce9e6d85ab41df8b7a20a168e3": th[:],
		"df17ff534df77fd3a30999f4e6200c8fcedefbb15d369301ca62c3cdfea9559a": s.recvKey[:],
		"71365fcf8212a6d63caf909ee28de3c8f689682ef298a374136055e0ab1cde4a": s.sendKey[:],
		"339ae1f2bdff63481857d2813c2f19dd1f5aa4824705d5e5daeb25dae7b9196e": s.id[:],
	} {
		if hex.EncodeToString(got) != name {
			t.Errorf("got %x want %s", got, name)
		}
	}
	// 主机发出的第一帧文本。Dart 侧用同一把 sendKey 解开，两边不能各算各的。
	frame := s.seal([]byte("hi"), kindText)
	const wantFrame = "339ae1f2bdff63481857d2810201000000000000000000002f21aaab2d6989389cd450263b93b3c5bf64fd318b70ca4c2844bc702ffd9811d0dfa9fea3f633ad6d946eaba8e61619d0ebdf9658edc1d2f8ded559"
	if got := hex.EncodeToString(frame); got != wantFrame {
		t.Fatalf("seal frame %s", got)
	}
}

// TestHandshakeRoundTrip 扮演手机：发 hello，按 ready 自己派生密钥，双向收发几帧。
func TestHandshakeRoundTrip(t *testing.T) {
	hostPub, hostPriv, _ := box.GenerateKey(rand.Reader)
	cliPub, cliPriv, _ := box.GenerateKey(rand.Reader)
	var cn [32]byte
	rand.Read(cn[:])
	raw, _ := json.Marshal(map[string]any{
		"type": "e2ee_hello", "v": 2, "clientPublicKeyB64": b64(*cliPub), "clientNonceB64": b64(cn),
		"capabilities": map[string]any{"framing": []int{2}, "payloadKinds": []string{"text", "binary"}},
		"context":      map[string]any{"protocol": "orca-mobile-e2ee", "initiator": "mobile", "responder": "desktop", "transport": "direct"},
	})
	h, ok := parseHello(raw)
	if !ok {
		t.Fatal("hello rejected")
	}
	r, host := accept(h, hostPub, hostPriv)
	if r.DesktopPublicKeyB64 != b64(*hostPub) || r.ClientNonceB64 != h.ClientNonceB64 {
		t.Fatalf("bad ready %+v", r)
	}

	// 手机这一侧：同样的握手记录，方向反过来
	var shared [32]byte
	box.Precompute(&shared, hostPub, cliPriv)
	dn, _ := decode32(r.DesktopNonceB64)
	phone := newSession(shared, transcript(h, r), cn, dn)
	phone.recvKey, phone.sendKey = phone.sendKey, phone.recvKey

	// 手机 → 主机：手机的 seal 用的是 desktop→mobile 的方向字节，这里手工按 mobile→desktop 封装
	for i := range 3 {
		msg := []byte(`{"n":` + string(rune('0'+i)) + `}`)
		nonce := phone.nonce(dirMobileToDesktop, kindText, uint64(i))
		plain := append(phone.header(dirMobileToDesktop, kindText, uint64(i)), msg...)
		frame := sealWith(nonce, plain, &phone.sendKey)
		got, err := host.openText([]byte(base64.StdEncoding.EncodeToString(frame)))
		if err != nil || !bytes.Equal(got, msg) {
			t.Fatalf("frame %d: %q %v", i, got, err)
		}
	}
	// 重放旧帧要被拒绝
	nonce := phone.nonce(dirMobileToDesktop, kindText, 0)
	old := sealWith(nonce, append(phone.header(dirMobileToDesktop, kindText, 0), 'x'), &phone.sendKey)
	if _, err := host.open(old, kindText); err == nil {
		t.Fatal("replayed frame accepted")
	}

	// 主机 → 手机：text 和 binary 共用计数器
	for i, kind := range []byte{kindText, kindBinary, kindText} {
		f := host.seal([]byte("hi"), kind)
		n := phone.nonce(dirDesktopToMobile, kind, uint64(i))
		if !bytes.Equal(f[:24], n[:]) {
			t.Fatalf("nonce %d mismatch", i)
		}
		plain, ok := openWith(f, &phone.recvKey)
		if !ok || !bytes.Equal(plain[headerLen:], []byte("hi")) || !bytes.Equal(plain[:headerLen], phone.header(dirDesktopToMobile, kind, uint64(i))) {
			t.Fatalf("host frame %d undecodable", i)
		}
	}
}

func TestHelloRejectsRelayAndExtraKeys(t *testing.T) {
	base := `{"type":"e2ee_hello","v":2,"clientPublicKeyB64":"` + b64(rep32(1)) + `","clientNonceB64":"` + b64(rep32(2)) +
		`","capabilities":{"framing":[2],"payloadKinds":["text","binary"]},"context":{"protocol":"orca-mobile-e2ee","initiator":"mobile","responder":"desktop","transport":"%s"}%s}`
	for _, c := range [][2]string{{"relay", ""}, {"direct", `,"x":1`}} {
		raw := []byte(sprintf(base, c[0], c[1]))
		if _, ok := parseHello(raw); ok {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, ok := parseHello([]byte(sprintf(base, "direct", ""))); !ok {
		t.Error("rejected valid hello")
	}
}

// 分帧只认 2（Orca）和 3（Posse），一次只能报一种
func TestHelloFraming(t *testing.T) {
	base := `{"type":"e2ee_hello","v":2,"clientPublicKeyB64":"` + b64(rep32(1)) + `","clientNonceB64":"` + b64(rep32(2)) +
		`","capabilities":{"framing":%s,"payloadKinds":["text","binary"]},"context":{"protocol":"orca-mobile-e2ee","initiator":"mobile","responder":"desktop","transport":"direct"}}`
	for f, want := range map[string]bool{"[2]": true, "[3]": true, "[1]": false, "[4]": false, "[2,3]": false, "[]": false} {
		h, ok := parseHello([]byte(sprintf(base, f)))
		if ok != want {
			t.Errorf("framing %s: accepted=%v", f, ok)
			continue
		}
		if ok {
			pub, priv, _ := box.GenerateKey(rand.Reader)
			r, s := accept(h, pub, priv)
			if r.Selection.Framing != h.Capabilities.Framing[0] || s.framing != r.Selection.Framing {
				t.Errorf("framing %s: selected %d, session %d", f, r.Selection.Framing, s.framing)
			}
		}
	}
}

func sealWith(nonce [24]byte, plain []byte, key *[32]byte) []byte {
	return secretbox.Seal(nonce[:], plain, &nonce, key)
}

func openWith(frame []byte, key *[32]byte) ([]byte, bool) {
	nonce := [24]byte(frame[:24])
	return secretbox.Open(nil, frame[24:], &nonce, key)
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

// 分帧 3 下 openMessage 的拒绝路径：失败都不推进计数器
func TestOpenMessageBinaryFraming(t *testing.T) {
	s := &session{framing: framingBinary}
	rand.Read(s.recvKey[:])
	rand.Read(s.id[:])
	frame := func(kind byte, n uint64, payload string) []byte {
		nonce := s.nonce(dirMobileToDesktop, kind, n)
		return secretbox.Seal(nonce[:], append(s.header(dirMobileToDesktop, kind, n), payload...), &nonce, &s.recvKey)
	}
	flipped := frame(kindText, 0, "x")
	flipped[14] = kindBinary
	badKind := frame(kindText, 0, "x")
	badKind[14] = 2
	for name, c := range map[string]struct {
		typ websocket.MessageType
		raw []byte
	}{
		"text frame":   {websocket.MessageText, frame(kindText, 0, "x")},
		"short":        {websocket.MessageBinary, make([]byte, 20)},
		"unknown kind": {websocket.MessageBinary, badKind},
		"flipped kind": {websocket.MessageBinary, flipped},
		"replayed":     {websocket.MessageBinary, frame(kindText, 1, "x")},
	} {
		if _, _, err := s.openMessage(c.typ, c.raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if s.recvN != 0 {
			t.Fatalf("%s: counter moved to %d", name, s.recvN)
		}
	}
	for i, want := range []byte{kindText, kindBinary, kindText} {
		kind, p, err := s.openMessage(websocket.MessageBinary, frame(want, uint64(i), "ok"))
		if err != nil || kind != want || string(p) != "ok" {
			t.Fatalf("frame %d: kind %d, %q, %v", i, kind, p, err)
		}
	}
}
