package deskrelay

import (
	"bytes"
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func listen(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func read(t *testing.T, c *net.UDPConn) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n]
}

func TestRelayForwardsBothWays(t *testing.T) {
	srvConn := listen(t)
	go NewServer(srvConn).Serve()
	srv := srvConn.LocalAddr().(*net.UDPAddr)
	mac, phone, stranger := listen(t), listen(t), listen(t)
	token := bytes.Repeat([]byte{7}, TokenLen)

	mac.WriteToUDP(Register(RoleMac, token), srv)
	if !IsAck(read(t, mac), RoleMac) {
		t.Fatal("no ack for mac")
	}
	phone.WriteToUDP(Register(RolePhone, token), srv)
	if !IsAck(read(t, phone), RolePhone) {
		t.Fatal("no ack for phone")
	}
	phone.WriteToUDP([]byte{1, 2, 3}, srv)
	if got := read(t, mac); !bytes.Equal(got, []byte{1, 2, 3}) {
		t.Fatalf("mac got %v", got)
	}
	mac.WriteToUDP([]byte{2, 9}, srv)
	if got := read(t, phone); !bytes.Equal(got, []byte{2, 9}) {
		t.Fatalf("phone got %v", got)
	}
	// 没登记的地址发来的包不转
	stranger.WriteToUDP([]byte{1, 1}, srv)
	mac.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := mac.Read(make([]byte, 64)); err == nil {
		t.Fatal("forwarded a packet from an unregistered address")
	}
	// 手机换了网络：新地址登记后转到新地址
	phone2 := listen(t)
	phone2.WriteToUDP(Register(RolePhone, token), srv)
	read(t, phone2)
	mac.WriteToUDP([]byte{3}, srv)
	if got := read(t, phone2); !bytes.Equal(got, []byte{3}) {
		t.Fatalf("new phone address got %v", got)
	}
}

func TestDataBytesNeverLookLikeControl(t *testing.T) {
	for kind := byte(1); kind <= 6; kind++ {
		if IsControl(append([]byte{kind}, bytes.Repeat([]byte{'H'}, 30)...)) {
			t.Fatalf("desk packet kind %d taken for control", kind)
		}
	}
}

// 探测回的是服务器看到的来源地址，回包不比请求大；打洞包不登记、不转发。
func TestProbeReportsSourceAddress(t *testing.T) {
	srvConn := listen(t)
	go NewServer(srvConn).Serve()
	srv := srvConn.LocalAddr().(*net.UDPAddr)
	c := listen(t)
	c.WriteToUDP(Probe(), srv)
	reply := read(t, c)
	if len(reply) > ProbeLen {
		t.Fatalf("reply %d bytes, larger than the probe", len(reply))
	}
	got, ok := ParseProbeReply(reply)
	if want := c.LocalAddr().(*net.UDPAddr).AddrPort(); !ok || got != want {
		t.Fatalf("probe says %v (%v), want %v", got, ok, want)
	}
	if !IsControl(Punch()) {
		t.Fatal("punch is not a control packet")
	}
	c.WriteToUDP(Punch(), srv)
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if _, err := c.Read(make([]byte, 64)); err == nil {
		t.Fatal("server answered a punch packet")
	}
}

func expectNothing(t *testing.T, c *net.UDPConn) {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, err := c.Read(make([]byte, 64)); err == nil {
		t.Fatalf("unexpected reply %v", n)
	}
}

// 开了鉴权：不带签名、签名不对、停用的名字开不了会话；手机只能加入 Mac 开好的会话。
func TestRelayAuth(t *testing.T) {
	master := NewMaster()
	srvConn := listen(t)
	srv := NewServer(srvConn)
	srv.SetMaster(master)
	go srv.Serve()
	addr := srvConn.LocalAddr().(*net.UDPAddr)
	mac, phone := listen(t), listen(t)
	token := bytes.Repeat([]byte{9}, TokenLen)
	now := time.Now()
	friend := Credential{Name: "friend", Key: UserKey(master, "friend")}

	mac.WriteToUDP(Register(RoleMac, token), addr)
	if !IsDenied(read(t, mac), RoleMac) {
		t.Fatal("unsigned mac register not denied")
	}
	mac.WriteToUDP(SignedRegister(token, Credential{Name: "friend", Key: NewMaster()}, now), addr)
	if !IsDenied(read(t, mac), RoleMac) {
		t.Fatal("wrong key not denied")
	}
	mac.WriteToUDP(SignedRegister(token, friend, now.Add(-10*time.Minute)), addr)
	if !IsDenied(read(t, mac), RoleMac) {
		t.Fatal("stale timestamp not denied")
	}
	// 手机不能自己开会话
	phone.WriteToUDP(Register(RolePhone, token), addr)
	expectNothing(t, phone)

	mac.WriteToUDP(SignedRegister(token, friend, now), addr)
	if !IsAck(read(t, mac), RoleMac) {
		t.Fatal("signed register not acked")
	}
	phone.WriteToUDP(Register(RolePhone, token), addr)
	if !IsAck(read(t, phone), RolePhone) {
		t.Fatal("phone could not join")
	}
	phone.WriteToUDP([]byte{1, 2}, addr)
	if got := read(t, mac); !bytes.Equal(got, []byte{1, 2}) {
		t.Fatalf("mac got %v", got)
	}

	// 停用后会话立刻关掉，再登记也不认
	srv.SetRevoked([]string{"friend"})
	phone.WriteToUDP([]byte{3}, addr)
	expectNothing(t, mac)
	mac.WriteToUDP(SignedRegister(token, friend, time.Now()), addr)
	if !IsDenied(read(t, mac), RoleMac) {
		t.Fatal("revoked name not denied")
	}
}

func TestRelayCheck(t *testing.T) {
	master := NewMaster()
	srvConn := listen(t)
	srv := NewServer(srvConn)
	srv.SetMaster(master)
	srv.SetRevoked([]string{"gone"})
	go srv.Serve()
	addr := srvConn.LocalAddr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, tc := range []struct {
		c    *Credential
		want byte
	}{
		{&Credential{Name: AdminName, Key: UserKey(master, AdminName)}, CheckOK},
		{&Credential{Name: "gone", Key: UserKey(master, "gone")}, CheckRevoked},
		{&Credential{Name: "x", Key: NewMaster()}, CheckBadKey},
		{nil, CheckBadKey},
	} {
		got, err := CheckRelay(ctx, listen(t), addr, tc.c)
		if err != nil || got != tc.want {
			t.Fatalf("%v: got %d %v, want %d", tc.c, got, err, tc.want)
		}
	}
	// 没开鉴权的服务器：旧设置也能用
	open := listen(t)
	go NewServer(open).Serve()
	if got, err := CheckRelay(ctx, listen(t), open.LocalAddr().String(), nil); err != nil || got != CheckOK {
		t.Fatalf("open server: %d %v", got, err)
	}
	// 没人应答
	short, cancel2 := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel2()
	if _, err := CheckRelay(short, listen(t), listen(t).LocalAddr().String(), nil); err != ErrUnreachable {
		t.Fatalf("silent relay: %v", err)
	}
}

func TestCodes(t *testing.T) {
	master := NewMaster()
	admin, err := ParseCode(AdminCode("1.2.3.4:443", master))
	if err != nil || !admin.Admin() || admin.Relay != "1.2.3.4:443" {
		t.Fatalf("admin code: %+v %v", admin, err)
	}
	if c := admin.Credential(); c == nil || c.Name != AdminName {
		t.Fatalf("admin credential %+v", c)
	}
	code, err := admin.Invite("alice")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(code, "1.2.3.4") {
		t.Fatal("address readable in the code")
	}
	user, err := ParseCode(code)
	if err != nil || user.Admin() || user.Relay != "1.2.3.4:443" || user.Name != "alice" {
		t.Fatalf("invite: %+v %v", user, err)
	}
	if c := user.Credential(); c == nil || !bytes.Equal(c.Key, UserKey(master, "alice")) {
		t.Fatal("invite key does not match the master")
	}
	if _, err := user.Invite("x"); err == nil {
		t.Fatal("non-admin could invite")
	}
	for _, bad := range []string{"", "Bob", "admin", "a b"} {
		if _, err := admin.Invite(bad); err == nil {
			t.Fatalf("invited %q", bad)
		}
	}
	for _, bad := range []string{"", "posse-relay:", "posse-relay:!!", code[:len(code)-3], "hello"} {
		if _, err := ParseCode(bad); err == nil {
			t.Fatalf("parsed %q", bad)
		}
	}
}

// 一个名字最多开 maxPerOwner 个会话，别人不受影响。
func TestRelayPerOwnerLimit(t *testing.T) {
	master := NewMaster()
	srvConn := listen(t)
	srv := NewServer(srvConn)
	srv.SetMaster(master)
	go srv.Serve()
	addr := srvConn.LocalAddr().(*net.UDPAddr)
	greedy := Credential{Name: "greedy", Key: UserKey(master, "greedy")}
	mac := listen(t)
	register := func(c Credential, i byte) bool {
		mac.WriteToUDP(SignedRegister(bytes.Repeat([]byte{i}, TokenLen), c, time.Now()), addr)
		mac.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 64)
		n, err := mac.Read(buf)
		return err == nil && IsAck(buf[:n], RoleMac)
	}
	for i := range maxPerOwner {
		if !register(greedy, byte(i)) {
			t.Fatalf("session %d refused", i)
		}
	}
	if register(greedy, maxPerOwner) {
		t.Fatal("went past the per-owner limit")
	}
	if !register(Credential{Name: "other", Key: UserKey(master, "other")}, 200) {
		t.Fatal("another name was blocked")
	}
}

// 留下的临时文件权限再宽，写出的 desk.json 也只给自己读。
func TestSaveConfigPrivate(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(ConfigFile(home)+".tmp", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig(home, Config{Relay: "1.2.3.4:443", Master: "k"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(ConfigFile(home))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("desk.json mode %v %v", fi.Mode(), err)
	}
}

// 同一个地址在同一个会话里换了身份：不留在旧身份上，它发的包不转回给它自己，
// 对端重新登记后照常互转。
func TestRelaySwitchRoleSameAddress(t *testing.T) {
	srvConn := listen(t)
	go NewServer(srvConn).Serve()
	srv := srvConn.LocalAddr().(*net.UDPAddr)
	a, b := listen(t), listen(t)
	token := bytes.Repeat([]byte{5}, TokenLen)

	a.WriteToUDP(Register(RoleMac, token), srv)
	read(t, a)
	a.WriteToUDP(Register(RolePhone, token), srv)
	if !IsAck(read(t, a), RolePhone) {
		t.Fatal("no ack after switching role")
	}
	a.WriteToUDP([]byte{1, 2}, srv)
	a.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if n, err := a.Read(make([]byte, 64)); err == nil {
		t.Fatalf("got %d bytes of its own packet back", n)
	}

	b.WriteToUDP(Register(RoleMac, token), srv)
	read(t, b)
	a.WriteToUDP([]byte{3}, srv)
	if got := read(t, b); !bytes.Equal(got, []byte{3}) {
		t.Fatalf("mac got %v", got)
	}
	b.WriteToUDP([]byte{4}, srv)
	if got := read(t, a); !bytes.Equal(got, []byte{4}) {
		t.Fatalf("phone got %v", got)
	}
}
