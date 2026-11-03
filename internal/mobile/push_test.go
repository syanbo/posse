package mobile

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"posse/internal/deskrelay"
	"posse/internal/term"
)

type relayPusher struct {
	result byte
	got    chan deskrelay.Push
}

func (r *relayPusher) Send(p deskrelay.Push) byte {
	r.got <- p
	return r.result
}

func openPush(t *testing.T, key, sealed []byte) pushBody {
	t.Helper()
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, sealed[:12], sealed[12:], nil)
	if err != nil {
		t.Fatal(err)
	}
	var b pushBody
	if err := json.Unmarshal(plain, &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPushNotify(t *testing.T) {
	tm, err := term.Start(term.Options{ID: "t_1", Dir: t.TempDir(), Command: []string{"/bin/sh"}, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close(time.Second)
	host := &fakeHost{terms: map[string]*term.Terminal{"t_1": tm}, order: []string{"t_1"}}
	srv, err := New(t.TempDir(), host, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.port = 0
	defer srv.Close()

	// 本机起一个中转，推送交给假的 APNs
	rc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	relay := deskrelay.NewServer(rc)
	pusher := &relayPusher{result: deskrelay.PushOK, got: make(chan deskrelay.Push, 4)}
	relay.SetPusher(pusher)
	go relay.Serve()
	srv.SetQUIC(QUICOptions{Relay: func() deskrelay.Config { return deskrelay.Config{Relay: rc.LocalAddr().String()} }})

	info, err := srv.Pair(false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	p := dialPhone(t, srv, info)
	defer p.ws.CloseNow()
	// 没报安装 ID 的连接注册不了
	key := make([]byte, 32)
	rand.Read(key)
	tok := make([]byte, 32)
	rand.Read(tok)
	reg := map[string]any{"token": hex.EncodeToString(tok), "key": base64.StdEncoding.EncodeToString(key), "waiting": true, "done": false}
	if m := p.next(p.call("posse.push.register", reg)); m["ok"] == true {
		t.Fatal("registered without a phone ID")
	}
	result(t, p.next(p.call("runtime.clientCapabilities.update", map[string]any{
		"client": "posse-flutter", "version": 1, "capabilities": []string{"posse.push.v1"},
		"phone": map[string]any{"id": "install-1", "name": "iPhone"},
	})))
	if r := result(t, p.next(p.call("posse.push.register", reg))); r["relay"] != true {
		t.Fatalf("register result %v", r)
	}

	// App 开着时不推
	srv.Notify("t_1", PushWaiting)
	select {
	case got := <-pusher.got:
		t.Fatalf("pushed while the phone is in front: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}
	result(t, p.next(p.call("posse.push.presence", map[string]any{"foreground": false})))
	srv.Notify("t_1", PushWaiting)
	var got deskrelay.Push
	select {
	case got = <-pusher.got:
	case <-time.After(3 * time.Second):
		t.Fatal("no push reached the relay")
	}
	if hex.EncodeToString(got.Token) != hex.EncodeToString(tok) || got.Badge != 1 {
		t.Fatalf("push %+v", got)
	}
	body := openPush(t, key, got.Cipher)
	if body.Kind != PushWaiting || body.Agent != "claude" || body.Project != "demo" || body.Terminal != "t_1" ||
		body.Worktree != "p1::w1" || body.Computer != b64(srv.pair.pub) {
		t.Fatalf("body %+v", body)
	}
	// 关了完成这一类
	srv.Notify("t_1", PushDone)
	select {
	case got := <-pusher.got:
		t.Fatalf("pushed a disabled kind: %+v", got)
	case <-time.After(300 * time.Millisecond):
	}

	// APNs 说 token 失效：删掉这条注册
	pusher.result = deskrelay.PushBadToken
	srv.Notify("t_1", PushWaiting)
	<-pusher.got
	deadline := time.Now().Add(2 * time.Second)
	for len(srv.pair.pushTargets()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("bad token was not dropped")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestPushRemovedWithPhone(t *testing.T) {
	srv, err := New(t.TempDir(), &fakeHost{terms: map[string]*term.Terminal{}}, -1)
	if err != nil {
		t.Fatal(err)
	}
	srv.pair.st.Devices = []Device{{ID: "d1", Phone: "install-1"}, {ID: "d2", Phone: "install-2"}}
	srv.pair.setPush(PushTarget{Phone: "install-1", Token: "aa"})
	srv.pair.setPush(PushTarget{Phone: "install-2", Token: "bb"})
	// 同一台手机再注册换掉旧的
	srv.pair.setPush(PushTarget{Phone: "install-2", Token: "cc"})
	srv.pair.remove("d1")
	got := srv.pair.pushTargets()
	if len(got) != 1 || got[0].Phone != "install-2" || got[0].Token != "cc" {
		t.Fatalf("targets %+v", got)
	}
	// 关掉推送：token 为空就删掉
	srv.pair.setPush(PushTarget{Phone: "install-2"})
	if n := len(srv.pair.pushTargets()); n != 0 {
		t.Fatalf("%d targets after unregister", n)
	}
}

// 长中文的消息和标签名加密后超过中转的上限时缩短，不整条丢掉
func TestPushPlainFits(t *testing.T) {
	long := strings.Repeat("长", 240)
	b := pushBody{V: 1, Kind: PushWaiting, Message: long, Tab: strings.Repeat("签", 80),
		Project: strings.Repeat("项", 60), Space: strings.Repeat("区", 60), Computer: strings.Repeat("k", 44)}
	plain := pushPlain(b)
	if n := len(plain) + pushSealOverhead; n > deskrelay.MaxPushCipher {
		t.Fatalf("sealed %d bytes, over %d", n, deskrelay.MaxPushCipher)
	}
	var got pushBody
	if err := json.Unmarshal(plain, &got); err != nil {
		t.Fatal(err)
	}
	if got.Message == "" || !strings.HasSuffix(got.Message, "…") || got.Project != b.Project {
		t.Fatalf("shortened wrong: message %q project kept %v", got.Message, got.Project == b.Project)
	}
	key := make([]byte, 32)
	if _, err := sealPush(key, plain); err != nil {
		t.Fatal(err)
	}
	short := pushBody{V: 1, Message: "hi"}
	if p, _ := json.Marshal(short); string(pushPlain(short)) != string(p) {
		t.Fatal("short body changed")
	}
}
