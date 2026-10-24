package desk

import (
	"crypto/rand"
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"hive/internal/deskrelay"
)

// relayKeepalive 是向中转重发登记的间隔。家用路由器的 UDP 映射常见 30 秒就回收。
const relayKeepalive = 10 * time.Second

// listenOut 开中转用的 socket。测试里换成普通 socket：绑在物理网卡上发不到 127.0.0.1
var listenOut = listenPhysical

type routedPacket struct {
	data []byte
	addr *net.UDPAddr
	sock *net.UDPConn
}

// Routes 是 Serve 用的 UDP 连接，底下两个 socket：
// lan 照系统路由走，给局域网和 Tailscale；out 绑在物理网卡上，绕开 Surge 这类接管全部流量的代理，
// 给中转（和以后的打洞）用。回包走收到那个地址时用的 socket。
type Routes struct {
	lan, out *net.UDPConn
	relay    *net.UDPAddr
	token    []byte

	in   chan routedPacket
	done chan struct{}
	once sync.Once

	mu       sync.Mutex
	via      map[string]*net.UDPConn
	deadline time.Time
	acked    bool
}

// ListenRoutes 开好两个 socket。relay 为空时只有 lan，和以前一样。
func ListenRoutes(relay string) (*Routes, error) {
	lan, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	r := &Routes{lan: lan, in: make(chan routedPacket, 256), done: make(chan struct{}), via: map[string]*net.UDPConn{}}
	if relay != "" {
		addr, err := net.ResolveUDPAddr("udp4", relay)
		if err != nil {
			lan.Close()
			return nil, err
		}
		r.relay = addr
		r.token = make([]byte, deskrelay.TokenLen)
		rand.Read(r.token)
		if out, err := listenOut(); err == nil {
			r.out = out
		} else {
			log.Printf("desk: no physical interface for relay, using system route: %v", err)
		}
	}
	go r.read(r.lan)
	if r.out != nil {
		go r.read(r.out)
	}
	if r.relay != nil {
		go r.keepalive()
	}
	return r, nil
}

// Port 是告诉手机直连用的端口。
func (r *Routes) Port() int { return r.lan.LocalAddr().(*net.UDPAddr).Port }

// Relay 是中转地址和这次观看的令牌，没配中转时为空。
func (r *Routes) Relay() (addr string, token []byte) {
	if r.relay == nil {
		return "", nil
	}
	return r.relay.String(), r.token
}

// IsRelay 判断这个地址是不是中转服务器。
func (r *Routes) IsRelay(a net.Addr) bool {
	u, ok := a.(*net.UDPAddr)
	return ok && r.relay != nil && u.IP.Equal(r.relay.IP) && u.Port == r.relay.Port
}

func (r *Routes) relaySock() *net.UDPConn {
	if r.out != nil {
		return r.out
	}
	return r.lan
}

func (r *Routes) keepalive() {
	reg := deskrelay.Register(deskrelay.RoleMac, r.token)
	t := time.NewTicker(relayKeepalive)
	defer t.Stop()
	for {
		if _, err := r.relaySock().WriteToUDP(reg, r.relay); err != nil && errors.Is(err, net.ErrClosed) {
			return
		}
		select {
		case <-r.done:
			return
		case <-t.C:
		}
	}
}

func (r *Routes) read(sock *net.UDPConn) {
	buf := make([]byte, PacketLimit+1)
	for {
		n, addr, err := sock.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if r.IsRelay(addr) && deskrelay.IsControl(buf[:n]) {
			if deskrelay.IsAck(buf[:n], deskrelay.RoleMac) {
				r.mu.Lock()
				first := !r.acked
				r.acked = true
				r.mu.Unlock()
				if first {
					log.Printf("desk: relay %s ready", addr)
				}
			}
			continue
		}
		p := routedPacket{data: append([]byte(nil), buf[:n]...), addr: cloneAddr(addr).(*net.UDPAddr), sock: sock}
		select {
		case r.in <- p:
		case <-r.done:
			return
		default:
			// Serve 跟不上时丢新包，和内核缓冲满了一样
		}
	}
}

func (r *Routes) ReadFrom(b []byte) (int, net.Addr, error) {
	r.mu.Lock()
	deadline := r.deadline
	r.mu.Unlock()
	var timeout <-chan time.Time
	if !deadline.IsZero() {
		d := time.Until(deadline)
		if d <= 0 {
			return 0, nil, os.ErrDeadlineExceeded
		}
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case p := <-r.in:
		r.mu.Lock()
		r.via[p.addr.String()] = p.sock
		r.mu.Unlock()
		return copy(b, p.data), p.addr, nil
	case <-timeout:
		return 0, nil, os.ErrDeadlineExceeded
	case <-r.done:
		return 0, nil, net.ErrClosed
	}
}

func (r *Routes) WriteTo(b []byte, addr net.Addr) (int, error) {
	u, ok := addr.(*net.UDPAddr)
	if !ok {
		return 0, errors.New("desk: not a UDP address")
	}
	r.mu.Lock()
	sock := r.via[u.String()]
	r.mu.Unlock()
	if sock == nil {
		sock = r.lan
		if r.IsRelay(u) {
			sock = r.relaySock()
		}
	}
	return sock.WriteToUDP(b, u)
}

func (r *Routes) Close() error {
	r.once.Do(func() { close(r.done) })
	if r.out != nil {
		r.out.Close()
	}
	return r.lan.Close()
}

func (r *Routes) LocalAddr() net.Addr { return r.lan.LocalAddr() }

func (r *Routes) SetDeadline(t time.Time) error { return r.SetReadDeadline(t) }

func (r *Routes) SetReadDeadline(t time.Time) error {
	r.mu.Lock()
	r.deadline = t
	r.mu.Unlock()
	return nil
}

func (r *Routes) SetWriteDeadline(time.Time) error { return nil }
