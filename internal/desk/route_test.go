package desk

import (
	"bytes"
	"net"
	"testing"
	"time"

	"hive/internal/deskrelay"
)

func TestRoutesThroughRelay(t *testing.T) {
	old := listenOut
	listenOut = func() (*net.UDPConn, error) { return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}) }
	t.Cleanup(func() { listenOut = old })

	srvConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	go deskrelay.NewServer(srvConn).Serve()
	srv := srvConn.LocalAddr().(*net.UDPAddr)

	r, err := ListenRoutes(srv.String())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	relay, token := r.Relay()
	if relay != srv.String() || len(token) != deskrelay.TokenLen {
		t.Fatalf("relay %q token %d", relay, len(token))
	}

	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	phone.WriteToUDP(deskrelay.Register(deskrelay.RolePhone, token), srv)
	phone.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	if n, _ := phone.Read(buf); !deskrelay.IsAck(buf[:n], deskrelay.RolePhone) {
		t.Fatal("phone not registered")
	}

	// 手机 → 中转 → Mac：Mac 看到的来源是中转，登记回执不会交上来
	hello := []byte{PacketHello, 1, 2, 3}
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	var from net.Addr
	for got == nil && time.Now().Before(deadline) {
		phone.WriteToUDP(hello, srv)
		r.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, a, err := r.ReadFrom(buf)
		if err == nil {
			got, from = append([]byte(nil), buf[:n]...), a
		}
	}
	if !bytes.Equal(got, hello) || !r.IsRelay(from) {
		t.Fatalf("mac got %v from %v", got, from)
	}
	// Mac → 中转 → 手机
	if _, err := r.WriteTo([]byte{PacketVideo, 9}, from); err != nil {
		t.Fatal(err)
	}
	phone.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := phone.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte{PacketVideo, 9}) {
		t.Fatalf("phone got %v %v", buf[:n], err)
	}
}
