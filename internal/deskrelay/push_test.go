package deskrelay

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakePusher struct {
	n      atomic.Int32
	result byte
	got    chan Push
}

func (f *fakePusher) Send(p Push) byte {
	f.n.Add(1)
	if f.got != nil {
		f.got <- p
	}
	return f.result
}

func samplePush() Push {
	p := Push{Token: bytes.Repeat([]byte{0xAB}, 32), Badge: 3, Cipher: []byte("secret"), Sandbox: true}
	rand.Read(p.Nonce[:])
	p.Collapse[0] = 9
	return p
}

func TestPushRoundTrip(t *testing.T) {
	master := NewMaster()
	cred := Credential{Name: "lu", Key: UserKey(master, "lu")}
	p := samplePush()
	b, err := EncodePush(p, &cred, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > packetLimit {
		t.Fatalf("push packet %d bytes", len(b))
	}
	got, sg, ok := parsePush(b)
	if !ok || !bytes.Equal(got.Token, p.Token) || got.Badge != 3 || !got.Sandbox || got.Collapse != p.Collapse ||
		string(got.Cipher) != "secret" || got.Nonce != p.Nonce || sg.name != "lu" {
		t.Fatalf("parsed %+v %v", got, ok)
	}
	if verify(master, sg, nil, time.Now()) != CheckOK {
		t.Fatal("signature does not verify")
	}
	// 最大的密文也要放进一个包
	p.Cipher = make([]byte, MaxPushCipher)
	p.Token = make([]byte, maxAPNsToken)
	b, _ = EncodePush(p, &Credential{Name: strings.Repeat("a", maxName), Key: cred.Key}, time.Now())
	if len(b) > packetLimit {
		t.Fatalf("largest push packet %d bytes > %d", len(b), packetLimit)
	}
	if _, _, ok := parsePush(b[:len(b)-1]); ok {
		t.Fatal("truncated packet parsed")
	}
}

func TestRelayPush(t *testing.T) {
	srvConn := listen(t)
	srv := NewServer(srvConn)
	master := NewMaster()
	srv.SetMaster(master)
	fake := &fakePusher{result: PushOK, got: make(chan Push, 4)}
	srv.SetPusher(fake)
	go srv.Serve()
	addr := srvConn.LocalAddr().(*net.UDPAddr)
	mac := listen(t)
	cred := Credential{Name: "lu", Key: UserKey(master, "lu")}

	p := samplePush()
	b, _ := EncodePush(p, &cred, time.Now())
	mac.WriteToUDP(b, addr)
	nonce, r, ok := ParsePushReply(read(t, mac))
	if !ok || nonce != p.Nonce || r != PushOK {
		t.Fatalf("reply %v %d %v", nonce, r, ok)
	}
	if got := <-fake.got; string(got.Cipher) != "secret" {
		t.Fatalf("pusher got %+v", got)
	}
	// 同一个 nonce 重发：回上次的结果，不再发一次
	b, _ = EncodePush(p, &cred, time.Now())
	mac.WriteToUDP(b, addr)
	if _, r, _ := ParsePushReply(read(t, mac)); r != PushOK {
		t.Fatalf("resend result %d", r)
	}
	if fake.n.Load() != 1 {
		t.Fatalf("sent %d times", fake.n.Load())
	}
	// 密钥不对、停用的名字
	p2 := samplePush()
	b, _ = EncodePush(p2, &Credential{Name: "lu", Key: make([]byte, KeyLen)}, time.Now())
	mac.WriteToUDP(b, addr)
	if _, r, _ := ParsePushReply(read(t, mac)); r != PushBadKey {
		t.Fatalf("bad key result %d", r)
	}
	b, _ = EncodePush(p2, nil, time.Now())
	mac.WriteToUDP(b, addr)
	if _, r, _ := ParsePushReply(read(t, mac)); r != PushBadKey {
		t.Fatalf("unsigned result %d", r)
	}
	srv.SetRevoked([]string{"lu"})
	b, _ = EncodePush(p2, &cred, time.Now())
	mac.WriteToUDP(b, addr)
	if _, r, _ := ParsePushReply(read(t, mac)); r != PushRevoked {
		t.Fatalf("revoked result %d", r)
	}
	if fake.n.Load() != 1 {
		t.Fatalf("sent %d times", fake.n.Load())
	}
}

func TestRelayPushUnavailableAndLimit(t *testing.T) {
	srvConn := listen(t)
	srv := NewServer(srvConn)
	go srv.Serve()
	addr := srvConn.LocalAddr().(*net.UDPAddr)
	mac := listen(t)
	b, _ := EncodePush(samplePush(), nil, time.Now())
	mac.WriteToUDP(b, addr)
	if _, r, _ := ParsePushReply(read(t, mac)); r != PushUnavail {
		t.Fatalf("no pusher result %d", r)
	}

	srv2 := NewServer(listen(t))
	fake := &fakePusher{result: PushOK}
	srv2.SetPusher(fake)
	srv2.push.mu.Lock()
	srv2.push.seen = map[[NonceLen]byte]pushSeen{}
	srv2.push.counts = map[string][]time.Time{"": make([]time.Time, pushPerHour)}
	for i := range srv2.push.counts[""] {
		srv2.push.counts[""][i] = time.Now()
	}
	srv2.push.mu.Unlock()
	go srv2.Serve()
	addr2 := srv2.conn.LocalAddr().(*net.UDPAddr)
	b, _ = EncodePush(samplePush(), nil, time.Now())
	mac.WriteToUDP(b, addr2)
	if _, r, _ := ParsePushReply(read(t, mac)); r != PushRateLimit {
		t.Fatalf("over limit result %d", r)
	}
}

func testKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestAPNsSend(t *testing.T) {
	k, p8 := testKey(t)
	var mu sync.Mutex
	var paths []string
	var last *http.Request
	var lastBody []byte
	status, reason := http.StatusOK, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		paths = append(paths, r.Host+r.URL.Path)
		last = r
		lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
		if reason != "" {
			json.NewEncoder(w).Encode(map[string]string{"reason": reason})
		}
	}))
	defer srv.Close()
	a, err := NewAPNs(p8, "KEY123", "TEAM45", "com.example.app")
	if err != nil {
		t.Fatal(err)
	}
	a.Production, a.Sandbox, a.Client = srv.URL, srv.URL, srv.Client()

	p := samplePush()
	if r := a.Send(p); r != PushOK {
		t.Fatalf("send = %d", r)
	}
	mu.Lock()
	if !strings.HasSuffix(paths[0], "/3/device/"+hex.EncodeToString(p.Token)) ||
		last.Header.Get("apns-topic") != "com.example.app" || last.Header.Get("apns-push-type") != "alert" ||
		last.Header.Get("apns-collapse-id") != hex.EncodeToString(p.Collapse[:]) {
		t.Fatalf("request %v %v", paths, last.Header)
	}
	// JWT：头里有 kid，签名用 .p8 的公钥核得过
	jwt := strings.TrimPrefix(last.Header.Get("authorization"), "bearer ")
	parts := strings.Split(jwt, ".")
	head, _ := base64.RawURLEncoding.DecodeString(parts[0])
	if !strings.Contains(string(head), `"kid":"KEY123"`) {
		t.Fatalf("jwt head %s", head)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256Sum(parts[0] + "." + parts[1])
	if !ecdsaVerify(&k.PublicKey, sum, sig) {
		t.Fatal("jwt signature does not verify")
	}
	var payload struct {
		APS map[string]any `json:"aps"`
		E   string         `json:"e"`
	}
	json.Unmarshal(lastBody, &payload)
	if payload.APS["mutable-content"] != float64(1) || payload.APS["badge"] != float64(3) || payload.E != base64.StdEncoding.EncodeToString([]byte("secret")) {
		t.Fatalf("payload %s", lastBody)
	}
	status, reason = http.StatusGone, "Unregistered"
	mu.Unlock()
	if r := a.Send(p); r != PushBadToken {
		t.Fatalf("410 = %d", r)
	}
	mu.Lock()
	status, reason = http.StatusBadRequest, "BadDeviceToken"
	mu.Unlock()
	if r := a.Send(p); r != PushBadToken {
		t.Fatalf("BadDeviceToken = %d", r)
	}
	mu.Lock()
	status, reason = http.StatusServiceUnavailable, ""
	mu.Unlock()
	if r := a.Send(p); r != PushFailed {
		t.Fatalf("503 = %d", r)
	}
}

func sha256Sum(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

func ecdsaVerify(pub *ecdsa.PublicKey, sum, sig []byte) bool {
	if len(sig) != 64 {
		return false
	}
	return ecdsa.Verify(pub, sum, new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func TestPushIndependentPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noAlert bool
		badge   uint16
	}{
		{"both", false, 3}, {"notification only", false, 0}, {"badge only", true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := samplePush()
			p.NoAlert, p.Badge = tc.noAlert, tc.badge
			packet, err := EncodePush(p, nil, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if (packet[4] == kindPushOptions) != tc.noAlert {
				t.Fatalf("packet kind %x", packet[4])
			}
			got, _, ok := parsePush(packet)
			if !ok || got.NoAlert != p.NoAlert || got.Badge != p.Badge {
				t.Fatalf("roundtrip %+v", got)
			}
			var payload map[string]any
			if err := json.Unmarshal(Payload(got), &payload); err != nil {
				t.Fatal(err)
			}
			aps := payload["aps"].(map[string]any)
			if aps["badge"] != float64(tc.badge) {
				t.Fatalf("badge %v", aps)
			}
			for _, k := range []string{"alert", "sound", "mutable-content"} {
				if _, present := aps[k]; present == tc.noAlert {
					t.Fatalf("%s: %v", k, aps)
				}
			}
			if _, present := payload["e"]; present == tc.noAlert {
				t.Fatalf("cipher: %v", payload)
			}
		})
	}
}
