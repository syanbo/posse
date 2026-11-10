package desk

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"posse/internal/deskrelay"
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

	r, err := ListenRoutes(srv.String(), nil)
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

func TestRoutesPunch(t *testing.T) {
	old := listenOut
	listenOut = func() (*net.UDPConn, error) { return net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)}) }
	t.Cleanup(func() { listenOut = old })

	srvConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srvConn.Close()
	go deskrelay.NewServer(srvConn).Serve()

	r, err := ListenRoutes(srvConn.LocalAddr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pub, err := r.Public(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 中转看到的就是出口 socket
	if pub.String() != r.out.LocalAddr().String() {
		t.Fatalf("public %s, out %s", pub, r.out.LocalAddr())
	}

	phone, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer phone.Close()
	go r.PunchTo(phone.LocalAddr().(*net.UDPAddr), 300*time.Millisecond)
	buf := make([]byte, 2048)
	phone.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := phone.ReadFromUDP(buf)
	if err != nil || !bytes.Equal(buf[:n], deskrelay.Punch()) || from.String() != pub.String() {
		t.Fatalf("phone got %v from %v: %v", buf[:n], from, err)
	}

	// 手机的打洞包不交上来，打通后的数据包照常交上来、回包走出口 socket
	phone.WriteToUDP(deskrelay.Punch(), from)
	phone.WriteToUDP([]byte{PacketHello, 7}, from)
	r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, a, err := r.ReadFrom(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte{PacketHello, 7}) || r.IsRelay(a) {
		t.Fatalf("mac got %v from %v: %v", buf[:n], a, err)
	}
	r.WriteTo([]byte{PacketVideo, 1}, a)
	for {
		phone.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, src, err := phone.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		if deskrelay.IsControl(buf[:n]) {
			continue // 还在打洞
		}
		if !bytes.Equal(buf[:n], []byte{PacketVideo, 1}) || src.String() != pub.String() {
			t.Fatalf("phone got %v from %v", buf[:n], src)
		}
		break
	}
}

// 解密前就按来源地址记回包 socket：乱发包的人换着来源地址发，表不能一直长。
func TestRoutesViaBounded(t *testing.T) {
	r, err := ListenRoutes("", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	other, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	buf := make([]byte, 16)
	for i := range 1000 {
		addr := &net.UDPAddr{IP: net.IPv4(10, byte(i>>8), byte(i), 1), Port: 4000 + i}
		sock := r.lan
		if i%2 == 0 {
			sock = other // 从另一个 socket 进来的才需要记
		}
		r.in <- routedPacket{data: []byte("x"), addr: addr, sock: sock}
		if _, _, err := r.ReadFrom(buf); err != nil {
			t.Fatal(err)
		}
	}
	r.mu.Lock()
	n := len(r.via)
	r.mu.Unlock()
	if n > viaMax {
		t.Fatalf("via grew to %d", n)
	}
	// 刚进来的地址还记着，回包走它进来的 socket
	last := &net.UDPAddr{IP: net.IPv4(10, 3, 230, 1), Port: 4998}
	r.mu.Lock()
	got := r.via[last.String()]
	r.mu.Unlock()
	if got != other {
		t.Fatal("latest address lost its socket")
	}
}
