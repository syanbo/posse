package deskrelay

import (
	"bytes"
	"net"
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
