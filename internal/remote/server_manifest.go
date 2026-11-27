package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// ServerManifest 描述 R2 / 镜像站点上发布的无头服务端版本清单（由更新私钥 Ed25519 签发）。
type ServerManifest struct {
	Version   string `json:"version"`
	Commit    string `json:"commit,omitempty"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// ValidServerVersion 检查服务端版本号格式，与远端 versions/<tag> 目录命名及长度限制保持一致。
func ValidServerVersion(v string) bool {
	if len(v) == 0 || len(v) > 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 && !alnum {
			return false
		}
		if !alnum && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// ParseUpdatePublicKey 解析与 mygo.json 一致的 base64 编码 Ed25519 公钥。
func ParseUpdatePublicKey(raw string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid server verification public key")
	}
	return ed25519.PublicKey(b), nil
}

// ParseUpdatePrivateKey 解析与 mygo-update.key 一致的 base64 编码 Ed25519 私钥。
func ParseUpdatePrivateKey(raw string) (ed25519.PrivateKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil || len(b) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid server signing private key")
	}
	return ed25519.PrivateKey(b), nil
}

// DecodeServerManifest 严格读取单个 JSON 对象，拒绝超长响应与尾随垃圾数据。
func DecodeServerManifest(r io.Reader) (ServerManifest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, maxServerManifestBytes+1))
	if err != nil {
		return ServerManifest{}, fmt.Errorf("invalid server manifest: %w", err)
	}
	if len(raw) > maxServerManifestBytes {
		return ServerManifest{}, errors.New("server manifest is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	var m ServerManifest
	if err := dec.Decode(&m); err != nil {
		return ServerManifest{}, fmt.Errorf("invalid server manifest: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ServerManifest{}, errors.New("invalid server manifest: unexpected trailing data")
	}
	return m, nil
}

// VerifyServerManifest 校验无头服务端清单的字段完整性、版本格式、下载地址域约束与 Ed25519 签名。
func VerifyServerManifest(m ServerManifest, goos, goarch, baseURL string, pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return errors.New("invalid server verification public key")
	}
	if m.OS != goos || m.Arch != goarch || !ValidServerVersion(m.Version) || m.URL == "" || len(m.SHA256) != 64 || m.Signature == "" {
		return errors.New("incomplete server manifest")
	}
	if _, err := hex.DecodeString(m.SHA256); err != nil {
		return errors.New("incomplete server manifest")
	}
	if err := validDownloadURL(m.URL, baseURL); err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || !ed25519.Verify(pub, serverManifestSignPayload(m), sig) {
		return errors.New("server manifest signature verification failed")
	}
	return nil
}

// SignServerManifest 先按线上客户端同等标准校验压缩包完整性与目标平台，再生成并签发 ServerManifest。
func SignServerManifest(ctx context.Context, archivePath, goos, goarch, version, commit, downloadURL string, priv ed25519.PrivateKey) (ServerManifest, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return ServerManifest{}, errors.New("invalid server signing private key")
	}
	normOS, err := NormalizeOS(goos)
	if err != nil {
		return ServerManifest{}, err
	}
	normArch, err := NormalizeArch(goarch)
	if err != nil {
		return ServerManifest{}, err
	}
	if !ValidServerVersion(version) {
		return ServerManifest{}, fmt.Errorf("invalid server version %q", version)
	}
	u, err := url.Parse(downloadURL)
	if err != nil || u.Host == "" {
		return ServerManifest{}, errors.New("invalid server download URL")
	}
	baseURL := u.Scheme + "://" + u.Host
	if err := validDownloadURL(downloadURL, baseURL); err != nil {
		return ServerManifest{}, err
	}

	pkg, err := inspectAndSnapshot(ctx, archivePath, version, normOS, normArch, "")
	if err != nil {
		return ServerManifest{}, err
	}
	defer pkg.Close()
	if pkg.Version != version {
		return ServerManifest{}, fmt.Errorf("server archive version %q does not match requested %q", pkg.Version, version)
	}

	m := ServerManifest{
		Version: pkg.Version,
		Commit:  strings.TrimSpace(commit),
		OS:      normOS,
		Arch:    normArch,
		URL:     downloadURL,
		SHA256:  strings.ToLower(pkg.SHA256),
	}
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, serverManifestSignPayload(m)))
	pub := priv.Public().(ed25519.PublicKey)
	if err := VerifyServerManifest(m, normOS, normArch, baseURL, pub); err != nil {
		return ServerManifest{}, err
	}
	return m, nil
}
