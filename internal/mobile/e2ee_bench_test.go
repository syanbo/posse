package mobile

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/secretbox"
)

// 电脑端收发一条 JSON 消息的加解密开销，两种分帧对照；手机端的同一组数在 mobile/benchmark。
// go test ./internal/mobile -run '^$' -bench E2ee
var benchSizes = []int{300, 9 << 10, 64 << 10, 1 << 20}

func benchSession(framing int) *session {
	s := &session{framing: framing}
	rand.Read(s.sendKey[:])
	rand.Read(s.recvKey[:])
	rand.Read(s.id[:])
	return s
}

// phoneFrame 是手机发来的第 0 帧 JSON 消息。
func phoneFrame(s *session, p []byte) (websocket.MessageType, []byte) {
	nonce := s.nonce(dirMobileToDesktop, kindText, 0)
	if s.framing == framingBinary {
		return websocket.MessageBinary, secretbox.Seal([]byte{kindText}, p, &nonce, &s.recvKey)
	}
	plain := append(s.header(dirMobileToDesktop, kindText, 0), p...)
	f := secretbox.Seal(nonce[:], plain, &nonce, &s.recvKey)
	return websocket.MessageText, []byte(base64.StdEncoding.EncodeToString(f))
}

func BenchmarkE2eeSealMessage(b *testing.B) {
	for _, framing := range []int{framingText, framingBinary} {
		for _, n := range benchSizes {
			b.Run(fmt.Sprintf("framing%d/%d", framing, n), func(b *testing.B) {
				s := benchSession(framing)
				p := make([]byte, n)
				rand.Read(p)
				b.SetBytes(int64(n))
				for b.Loop() {
					s.sealMessage(p)
				}
			})
		}
	}
}

func BenchmarkE2eeOpenMessage(b *testing.B) {
	for _, framing := range []int{framingText, framingBinary} {
		for _, n := range benchSizes {
			b.Run(fmt.Sprintf("framing%d/%d", framing, n), func(b *testing.B) {
				s := benchSession(framing)
				p := make([]byte, n)
				rand.Read(p)
				typ, raw := phoneFrame(s, p)
				b.SetBytes(int64(n))
				for b.Loop() {
					s.recvN = 0
					if _, _, err := s.openMessage(typ, raw); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
