package collaboration

// 第二阶段局域网协作的传输身份（试验）：TLS 1.3，双方用安装身份的 Ed25519 自签证书，
// 按公钥 SPKI 指纹固定对端。TLS 只证明持钥；谁能读哪个房间由应用层的成员表和凭据决定。
// 两台真实 Mac 上的本地网络权限试验还没做，这里只是单机可验证的那部分。

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Certificate 用安装身份生成自签证书：公钥不变，证书可以随时重签（续期不换身份）。
func Certificate(id Identity, now time.Time) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "posse-" + id.ID[:16]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, id.Public, id.Private)
	if err != nil {
		return tls.Certificate{}, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: id.Private}, nil
}

// PeerFingerprint 是对端证书公钥的指纹；只认 Ed25519。
func PeerFingerprint(cs tls.ConnectionState) (string, error) {
	if len(cs.PeerCertificates) == 0 {
		return "", errors.New("peer sent no certificate")
	}
	pub, ok := cs.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return "", errors.New("peer key is not Ed25519")
	}
	return Fingerprint(pub)
}

// ServerConfig 是房主一侧：要求客户端出示证书（证明持钥），只接受 Ed25519 安装身份。
// 每次握手（包括会话恢复）都核对；未知身份是否放行（只开放加入申请）由应用层按指纹决定。
func ServerConfig(id Identity) (*tls.Config, error) {
	cert, err := Certificate(id, time.Now())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyConnection: func(cs tls.ConnectionState) error {
			_, err := PeerFingerprint(cs)
			return err
		},
	}, nil
}

// ClientConfig 是加入者一侧：不走 CA 校验，改为把房主证书公钥的指纹固定成邀请里的那个。
func ClientConfig(id Identity, hostFingerprint string, cache tls.ClientSessionCache) (*tls.Config, error) {
	cert, err := Certificate(id, time.Now())
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{cert},
		ClientSessionCache: cache,
		// 自签证书没有 CA 链；身份由下面的 VerifyConnection 按指纹核对，恢复会话时它照样被调用
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			fp, err := PeerFingerprint(cs)
			if err != nil {
				return err
			}
			if fp != hostFingerprint {
				return fmt.Errorf("host fingerprint %s does not match the invitation", fp[:16])
			}
			return nil
		},
	}, nil
}
