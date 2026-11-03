package deskrelay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"
)

// APNs 经 HTTP/2 和 token 鉴权（.p8 密钥签 ES256 的 JWT）发通知，只用标准库。
type APNs struct {
	KeyID  string
	TeamID string
	Topic  string // App 的包 ID
	key    *ecdsa.PrivateKey

	// 测试时换成假服务器
	Production, Sandbox string
	Client              *http.Client

	mu    sync.Mutex
	jwt   string
	jwtAt time.Time
}

// NewAPNs 读 .p8 密钥（PEM 里的 PKCS#8 EC 私钥）。
func NewAPNs(p8 []byte, keyID, teamID, topic string) (*APNs, error) {
	block, _ := pem.Decode(p8)
	if block == nil {
		return nil, errors.New("apns key: not a PEM file")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("apns key: %w", err)
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("apns key: not an EC key")
	}
	if keyID == "" || teamID == "" || topic == "" {
		return nil, errors.New("apns: key ID, team ID and topic are required")
	}
	return &APNs{
		KeyID: keyID, TeamID: teamID, Topic: topic, key: ec,
		Production: "https://api.push.apple.com",
		Sandbox:    "https://api.sandbox.push.apple.com",
		Client:     &http.Client{Timeout: 10 * time.Second},
	}, nil
}

// token 是鉴权用的 JWT。苹果要求 20 到 60 分钟换一次，太频繁换会被拒。
func (a *APNs) token(now time.Time) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jwt != "" && now.Sub(a.jwtAt) < 40*time.Minute {
		return a.jwt, nil
	}
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": a.TeamID, "iat": now.Unix()})
	unsigned := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.jwt, a.jwtAt = unsigned+"."+enc.EncodeToString(sig), now
	return a.jwt, nil
}

// Payload 是交给 APNs 的 JSON。明文只有兜底文字（App 里的本地化键，通知扩展没解开时显示）和角标，
// 真正的标题和正文在 e 里，由手机上的通知扩展解开。
func Payload(p Push) []byte {
	b, _ := json.Marshal(map[string]any{
		"aps": map[string]any{
			"alert": map[string]any{
				"title-loc-key": "PUSH_FALLBACK_TITLE",
				"loc-key":       "PUSH_FALLBACK_BODY",
			},
			"mutable-content": 1,
			"sound":           "default",
			"badge":           p.Badge,
		},
		"e": base64.StdEncoding.EncodeToString(p.Cipher),
	})
	return b
}

func (a *APNs) Send(p Push) byte {
	jwt, err := a.token(time.Now())
	if err != nil {
		log.Printf("apns: sign: %v", err)
		return PushFailed
	}
	host := a.Production
	if p.Sandbox {
		host = a.Sandbox
	}
	req, err := http.NewRequest(http.MethodPost, host+"/3/device/"+hex.EncodeToString(p.Token), bytes.NewReader(Payload(p)))
	if err != nil {
		return PushFailed
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-priority", "10")
	req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	req.Header.Set("apns-collapse-id", hex.EncodeToString(p.Collapse[:]))
	resp, err := a.Client.Do(req)
	if err != nil {
		log.Printf("apns: %v", err)
		return PushFailed
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode == http.StatusOK {
		return PushOK
	}
	var reason struct {
		Reason string `json:"reason"`
	}
	json.Unmarshal(body, &reason)
	log.Printf("apns: %d %s", resp.StatusCode, reason.Reason)
	switch {
	case resp.StatusCode == http.StatusGone,
		reason.Reason == "BadDeviceToken", reason.Reason == "DeviceTokenNotForTopic", reason.Reason == "Unregistered":
		return PushBadToken
	case reason.Reason == "ExpiredProviderToken":
		a.mu.Lock()
		a.jwt = ""
		a.mu.Unlock()
	}
	return PushFailed
}
