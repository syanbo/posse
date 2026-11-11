package collaboration

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func identityIn(t *testing.T) Identity {
	t.Helper()
	id, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// serve 在本机端口上接受连接，握手完写回一个字节。
func serve(t *testing.T, cfg *tls.Config) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				if err := c.(*tls.Conn).Handshake(); err == nil {
					c.Write([]byte{1})
					buf := make([]byte, 1)
					c.Read(buf)
				}
			}()
		}
	}()
	return ln.Addr().String()
}

// serverWith 是房主的配置，另外把每次握手的客户端指纹交给 check（测试用来看到指纹、模拟撤销）。
func serverWith(t *testing.T, id Identity, check func(fp string) error) *tls.Config {
	t.Helper()
	cfg, err := ServerConfig(id)
	if err != nil {
		t.Fatal(err)
	}
	inner := cfg.VerifyConnection
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if err := inner(cs); err != nil {
			return err
		}
		fp, _ := PeerFingerprint(cs)
		return check(fp)
	}
	return cfg
}

func dial(addr string, cfg *tls.Config) (tls.ConnectionState, error) {
	c, err := tls.Dial("tcp", addr, cfg)
	if err != nil {
		return tls.ConnectionState{}, err
	}
	defer c.Close()
	buf := make([]byte, 1)
	if _, err := c.Read(buf); err != nil {
		return c.ConnectionState(), err
	}
	return c.ConnectionState(), nil
}

func TestTLSPinsBothSides(t *testing.T) {
	host, member, stranger := identityIn(t), identityIn(t), identityIn(t)
	var mu sync.Mutex
	var seen []string
	srv := serverWith(t, host, func(fp string) error {
		mu.Lock()
		seen = append(seen, fp)
		mu.Unlock()
		return nil
	})
	addr := serve(t, srv)
	cli, _ := ClientConfig(member, host.ID, nil)
	if _, err := dial(addr, cli); err != nil {
		t.Fatalf("pinned dial: %v", err)
	}
	mu.Lock()
	if len(seen) != 1 || seen[0] != member.ID {
		t.Fatalf("server saw %v, want the member's fingerprint", seen)
	}
	mu.Unlock()
	// 邀请里的指纹不是这台房主：拒绝
	wrong, _ := ClientConfig(member, stranger.ID, nil)
	if _, err := dial(addr, wrong); err == nil {
		t.Fatal("dialed a host with the wrong fingerprint")
	}
	// 不出示证书的客户端：服务端拒绝
	bare := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}
	if _, err := dial(addr, bare); err == nil {
		t.Fatal("client without a certificate was accepted")
	}
}

// 会话恢复时两边的 VerifyConnection 都还会被调用：撤销后的成员不能靠恢复的会话连上。
func TestTLSResumptionStillChecks(t *testing.T) {
	host, member := identityIn(t), identityIn(t)
	var revoked atomic.Bool
	var checks atomic.Int32
	srv := serverWith(t, host, func(fp string) error {
		checks.Add(1)
		if revoked.Load() {
			return errors.New("revoked")
		}
		return nil
	})
	addr := serve(t, srv)
	cache := tls.NewLRUClientSessionCache(4)
	cli, _ := ClientConfig(member, host.ID, cache)
	first, err := dial(addr, cli)
	if err != nil {
		t.Fatal(err)
	}
	if first.DidResume {
		t.Fatal("first connection resumed")
	}
	second, err := dial(addr, cli)
	if err != nil {
		t.Fatal(err)
	}
	if !second.DidResume {
		t.Skip("session was not resumed; cannot check resumption here")
	}
	if checks.Load() != 2 {
		t.Fatalf("server checked %d times over 2 handshakes (one resumed)", checks.Load())
	}
	revoked.Store(true)
	if _, err := dial(addr, cli); err == nil {
		t.Fatal("revoked member reconnected with a resumed session")
	}
}

// 不是安装身份的证书（这里用 ECDSA 自签，和手机配对那类别的密钥一样不是 Ed25519 安装身份）连不上；
// 未知但合法的 Ed25519 身份能完成握手，指纹交给应用层，由它只开放加入申请。
func TestTLSRejectsForeignKeys(t *testing.T) {
	host, stranger := identityIn(t), identityIn(t)
	var got []string
	srv := serverWith(t, host, func(fp string) error { got = append(got, fp); return nil })
	addr := serve(t, srv)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	foreign := &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	if _, err := dial(addr, foreign); err == nil {
		t.Fatal("non-Ed25519 client certificate accepted")
	}
	cli, _ := ClientConfig(stranger, host.ID, nil)
	if _, err := dial(addr, cli); err != nil {
		t.Fatalf("unknown Ed25519 identity could not reach the join path: %v", err)
	}
	if len(got) != 1 || got[0] != stranger.ID {
		t.Fatalf("server saw %v", got)
	}
}
