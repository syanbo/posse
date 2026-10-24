// Package mquic 是手机和 Mac 之间的 QUIC 传输，两端共用：Mac 的常驻进程用它收手机的连接，
// 手机 App 里编进去的库（mobile/packages/hive_quic）用它拨号。
//
// 一条连接只开一条双向流，上面跑和 WebSocket 一样的消息：文本、二进制两种，各带长度。
// 端到端加密的计数器要求消息按发送顺序到达，所以不按终端拆流。
// TLS 只当 QUIC 的传输加密用，不验证书：身份和内容保密由上层的 E2EE 负责，和局域网里的 ws:// 一样。
//
// 帧：u8 类型、u32 长度（大端）、内容。类型 3 是关闭：内容是 u16 关闭码加原因，
// 发完就关流，对方读到后关连接。直接关连接会丢掉还没发出去的数据（比如拒绝认证前的那条报错）。
package mquic

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"time"

	"github.com/quic-go/quic-go"
)

const ALPN = "hive-mobile/1"

const (
	Text   byte = 1
	Binary byte = 2
	Close  byte = 3
)

// MaxMessage 和 WebSocket 那边的读上限一致，再留点余量给加密和 base64 的膨胀。
const MaxMessage = 8 << 20

// WriteMessage 把一条消息一次写进流里。
func WriteMessage(w io.Writer, typ byte, p []byte) error {
	if len(p) > MaxMessage {
		return fmt.Errorf("mquic: message too large (%d bytes)", len(p))
	}
	buf := make([]byte, 5+len(p))
	buf[0] = typ
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(p)))
	copy(buf[5:], p)
	_, err := w.Write(buf)
	return err
}

// ReadMessage 读一条消息。
func ReadMessage(r *bufio.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > MaxMessage {
		return 0, nil, fmt.Errorf("mquic: message too large (%d bytes)", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		return 0, nil, err
	}
	return head[0], p, nil
}

// CloseFrame 是关闭帧的内容。
func CloseFrame(code uint16, reason string) []byte {
	if len(reason) > 120 {
		reason = reason[:120]
	}
	out := make([]byte, 2+len(reason))
	binary.BigEndian.PutUint16(out, code)
	copy(out[2:], reason)
	return out
}

// ParseClose 解开关闭帧。
func ParseClose(p []byte) (uint16, string, error) {
	if len(p) < 2 {
		return 0, "", errors.New("mquic: short close frame")
	}
	return binary.BigEndian.Uint16(p), string(p[2:]), nil
}

// Config 是两端共用的 QUIC 参数。保活短于常见家用路由器回收 UDP 映射的 30 秒，也给中转续上会话。
func Config() *quic.Config {
	return &quic.Config{
		HandshakeIdleTimeout: 8 * time.Second,
		MaxIdleTimeout:       30 * time.Second,
		KeepAlivePeriod:      10 * time.Second,
	}
}

// ServerTLS 每次启动生成一张自签名证书。
func ServerTLS() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour),
		DNSNames:     []string{"hive"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
		NextProtos:   []string{ALPN},
	}, nil
}

// ClientTLS 不验证书，见包说明。
func ClientTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true, NextProtos: []string{ALPN}, ServerName: "hive"}
}
