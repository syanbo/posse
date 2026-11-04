package mobile

import (
	"crypto/rand"
	"strconv"
	"testing"

	"github.com/coder/websocket"
	"golang.org/x/crypto/nacl/secretbox"
)

// 电脑端收发一条 JSON 消息的加解密开销；手机端的同一组数在 mobile/benchmark。
// go test ./internal/mobile -run '^$' -bench E2ee
var benchSizes = []int{300, 9 << 10, 64 << 10, 1 << 20}

func benchSession() *session {
	s := &session{}
	rand.Read(s.sendKey[:])
	rand.Read(s.recvKey[:])
	rand.Read(s.id[:])
	return s
}

// phoneFrame 是手机发来的第 0 帧 JSON 消息。
func phoneFrame(s *session, p []byte) []byte {
	nonce := s.nonce(dirMobileToDesktop, kindText, 0)
	return secretbox.Seal([]byte{kindText}, p, &nonce, &s.recvKey)
}

func BenchmarkE2eeSealMessage(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := benchSession()
			p := make([]byte, n)
			rand.Read(p)
			b.SetBytes(int64(n))
			for b.Loop() {
				s.seal(p, kindText)
			}
		})
	}
}

func BenchmarkE2eeOpenMessage(b *testing.B) {
	for _, n := range benchSizes {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			s := benchSession()
			p := make([]byte, n)
			rand.Read(p)
			raw := phoneFrame(s, p)
			b.SetBytes(int64(n))
			for b.Loop() {
				s.recvN = 0
				if _, _, err := s.openMessage(websocket.MessageBinary, raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
