package desk

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"posse/internal/deskrelay"
)

// relayKeepalive 是向中转重发登记的间隔。家用路由器的 UDP 映射常见 30 秒就回收。
const relayKeepalive = 10 * time.Second

// derpCacheTTL 是缓存内置 Tailscale 对端是否走 DERP 的时长，避免每个回执包都查 LocalAPI。
const derpCacheTTL = 2 * time.Second

// listenOut 开中转用的 socket。测试里换成普通 socket：绑在物理网卡上发不到 127.0.0.1
var listenOut = ListenPhysical

type routedPacket struct {
	data []byte
	addr *net.UDPAddr
	sock net.PacketConn
}

type derpCacheEntry struct {
	isDERP bool
	at     time.Time
}

// RouteOptions 配置 ListenRoutesWithOptions 的多路 UDP 监听。
type RouteOptions struct {
	Relay    string
	Cred     *deskrelay.Credential
	TS       net.PacketConn                         // 可选：已打开的内置 Tailscale UDP PacketConn
	TSListen func(port int) (net.PacketConn, error) // 可选：优先尝试绑定与 LAN 同端口，失败再回退端口 0
	TSIsDERP func(net.Addr) bool                    // 可选：查询该 ts 对端当前是否走 DERP 中继
}

// Routes 是 Serve 用的 UDP 连接，底下最多三路 socket：
// lan 照系统路由走，给局域网和系统 Tailscale；out 绑在物理网卡上，绕开 Surge 这类接管全部流量的代理，
// 给中转和打洞用；ts 走内置 Tailscale 的用户态 netstack。回包走收到那个地址时用的 socket。
type Routes struct {
	lan, out *net.UDPConn
	ts       net.PacketConn
	tsIsDERP func(net.Addr) bool
	relay    *net.UDPAddr
	token    []byte
	cred     *deskrelay.Credential

	in   chan routedPacket
	done chan struct{}
	once sync.Once

	mu        sync.Mutex
	via       map[string]net.PacketConn
	tsPeers   map[string]bool
	derpCache map[string]derpCacheEntry
	deadline  time.Time
	acked     bool
	denied    bool

	public    atomic.Pointer[netip.AddrPort] // 最近一次探测回来的公网地址
	lastPunch atomic.Int64
}

// ListenRoutes 开好 lan 与可选的 relay 两个 socket。relay 为空时只有 lan，和以前一样。
func ListenRoutes(relay string, cred *deskrelay.Credential) (*Routes, error) {
	return ListenRoutesWithOptions(RouteOptions{Relay: relay, Cred: cred})
}

// ListenRoutesWithOptions 开好局域网、可选中转与可选内置 Tailscale 路线。
// 若创建失败，不会关闭调用方传入的 opts.TS（由调用方决定是否关闭）。
func ListenRoutesWithOptions(opts RouteOptions) (*Routes, error) {
	lan, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	lanPort := lan.LocalAddr().(*net.UDPAddr).Port
	tsConn := opts.TS
	tsOwned := false
	if tsConn == nil && opts.TSListen != nil {
		// 优先共享 LAN 端口，让旧版官方 Tailscale 手机按 ready.port 直发内置 IP 也能连通
		if pc, tsErr := opts.TSListen(lanPort); tsErr == nil {
			tsConn, tsOwned = pc, true
		} else if pc, tsErr2 := opts.TSListen(0); tsErr2 == nil {
			tsConn, tsOwned = pc, true
		} else {
			log.Printf("desk: tailscale listen: %v (fallback: %v)", tsErr, tsErr2)
		}
	}
	r := &Routes{
		lan:       lan,
		ts:        tsConn,
		tsIsDERP:  opts.TSIsDERP,
		in:        make(chan routedPacket, 256),
		done:      make(chan struct{}),
		via:       map[string]net.PacketConn{},
		tsPeers:   map[string]bool{},
		derpCache: map[string]derpCacheEntry{},
	}
	if opts.Relay != "" {
		addr, err := net.ResolveUDPAddr("udp4", opts.Relay)
		if err != nil {
			lan.Close()
			if tsOwned && tsConn != nil {
				tsConn.Close()
			}
			return nil, err
		}
		r.relay, r.cred = addr, opts.Cred
		r.token = make([]byte, deskrelay.TokenLen)
		rand.Read(r.token)
		if out, err := listenOut(); err == nil {
			r.out = out
		} else {
			log.Printf("desk: no physical interface for relay, using system route: %v", err)
		}
	}
	go r.readUDP(r.lan)
	if r.out != nil {
		go r.readUDP(r.out)
	}
	if r.ts != nil {
		go r.readTS(r.ts)
	}
	if r.relay != nil {
		go r.keepalive()
	}
	return r, nil
}

// Port 是告诉手机直连用的端口。
func (r *Routes) Port() int { return r.lan.LocalAddr().(*net.UDPAddr).Port }

// TSPort 返回内置 Tailscale UDP 监听的本地端口，未启用时返回 0。
func (r *Routes) TSPort() int {
	if r.ts == nil {
		return 0
	}
	return udpPortOf(r.ts.LocalAddr())
}

func udpPortOf(a net.Addr) int {
	if a == nil {
		return 0
	}
	if u, ok := a.(*net.UDPAddr); ok && u != nil {
		return u.Port
	}
	if ap, err := netip.ParseAddrPort(a.String()); err == nil {
		return int(ap.Port())
	}
	return 0
}

// Relay 是中转地址和这次观看的令牌，没配中转时为空。
func (r *Routes) Relay() (addr string, token []byte) {
	if r.relay == nil {
		return "", nil
	}
	return r.relay.String(), r.token
}

// IsRelay 判断这个地址是不是中转路径：包括 deskrelay 中转服务器，以及经内置 Tailscale DERP 中继的对端。
func (r *Routes) IsRelay(a net.Addr) bool {
	if r.isDeskRelay(a) {
		return true
	}
	if r.ts == nil || r.tsIsDERP == nil || a == nil {
		return false
	}
	key := a.String()
	r.mu.Lock()
	fromTS := r.tsPeers[key]
	entry, cached := r.derpCache[key]
	r.mu.Unlock()
	if !fromTS {
		return false
	}
	if cached && time.Since(entry.at) < derpCacheTTL {
		return entry.isDERP
	}
	derp := r.tsIsDERP(a)
	r.mu.Lock()
	// 查询期间若来源已切回 LAN/中转，不写入过期缓存
	if !r.tsPeers[key] {
		delete(r.derpCache, key)
		r.mu.Unlock()
		return false
	}
	if len(r.derpCache) >= viaMax {
		clear(r.derpCache)
	}
	r.derpCache[key] = derpCacheEntry{isDERP: derp, at: time.Now()}
	r.mu.Unlock()
	return derp
}

func (r *Routes) isDeskRelay(a net.Addr) bool {
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
	t := time.NewTicker(relayKeepalive)
	defer t.Stop()
	for {
		reg := deskrelay.MacRegister(r.token, r.cred, time.Now())
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

func (r *Routes) readUDP(sock *net.UDPConn) {
	buf := make([]byte, PacketLimit+1)
	for {
		n, addr, err := sock.ReadFromUDP(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		if deskrelay.IsControl(buf[:n]) {
			if !r.isDeskRelay(addr) {
				// 手机的打洞包：一次连发几十个，只记第一个
				if now := time.Now().UnixNano(); now-r.lastPunch.Swap(now) > int64(10*time.Second) {
					log.Printf("desk: punch: packet from phone %s reached the mac", addr)
				}
				continue
			}
			if a, ok := deskrelay.ParseProbeReply(buf[:n]); ok {
				r.public.Store(&a)
			}
			if deskrelay.IsAck(buf[:n], deskrelay.RoleMac) {
				r.mu.Lock()
				first := !r.acked
				r.acked = true
				r.mu.Unlock()
				if first {
					log.Printf("desk: relay %s ready", addr)
				}
			}
			if deskrelay.IsDenied(buf[:n], deskrelay.RoleMac) {
				r.mu.Lock()
				first := !r.denied
				r.denied = true
				r.mu.Unlock()
				if first {
					log.Printf("desk: relay %s denied the registration: key invalid or revoked", addr)
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

func (r *Routes) readTS(sock net.PacketConn) {
	buf := make([]byte, PacketLimit+1)
	for {
		n, rawAddr, err := sock.ReadFrom(buf)
		if err != nil {
			// gVisor gonet.UDPConn 在端点/netstack 关闭时返回 io.EOF，必须退出避免空转
			if errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF) {
				return
			}
			select {
			case <-r.done:
				return
			default:
				continue
			}
		}
		addr := toUDPAddr(rawAddr)
		if addr == nil {
			continue
		}
		p := routedPacket{data: append([]byte(nil), buf[:n]...), addr: addr, sock: sock}
		select {
		case r.in <- p:
		case <-r.done:
			return
		default:
		}
	}
}

func toUDPAddr(a net.Addr) *net.UDPAddr {
	if a == nil {
		return nil
	}
	if u, ok := a.(*net.UDPAddr); ok && u != nil {
		return cloneAddr(u).(*net.UDPAddr)
	}
	if ap, err := netip.ParseAddrPort(a.String()); err == nil {
		return net.UDPAddrFromAddrPort(ap)
	}
	return nil
}

// Public 经中转问出出口 socket 在公网上的地址，手机向它打洞。
func (r *Routes) Public(ctx context.Context) (netip.AddrPort, error) {
	if r.relay == nil {
		return netip.AddrPort{}, errors.New("desk: no relay")
	}
	r.public.Store(nil)
	probe := deskrelay.Probe()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := r.relaySock().WriteToUDP(probe, r.relay); err != nil {
			return netip.AddrPort{}, err
		}
		select {
		case <-ctx.Done():
			return netip.AddrPort{}, fmt.Errorf("desk: relay %s did not answer the probe", r.relay)
		case <-r.done:
			return netip.AddrPort{}, net.ErrClosed
		case <-tick.C:
		}
		if a := r.public.Load(); a != nil {
			return *a, nil
		}
	}
}

// PunchTo 在 d 之内每 50ms 从出口 socket 向 peer 发一个打洞包，让这边的 NAT 放进手机的包。
func (r *Routes) PunchTo(peer *net.UDPAddr, d time.Duration) {
	punch := deskrelay.Punch()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if _, err := r.relaySock().WriteToUDP(punch, peer); errors.Is(err, net.ErrClosed) {
			return
		}
		select {
		case <-r.done:
			return
		case <-time.After(50 * time.Millisecond):
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
		r.noteVia(p)
		return copy(b, p.data), p.addr, nil
	case <-timeout:
		return 0, nil, os.ErrDeadlineExceeded
	case <-r.done:
		return 0, nil, net.ErrClosed
	}
}

// viaMax 是记着回包 socket 的地址数上限。正常观看只有手机的一两个地址（直连、打洞后的公网地址）。
const viaMax = 64

// noteVia 记下回包用哪个 socket。包还没解密，来源地址谁都能伪造：只记和默认不同的（局域网地址从中转用的 socket 进来，
// 或从内置 Tailscale socket 进来），超过上限就清空重记，不让乱发包的人把表撑大；被清掉的手机地址下一个包到了又记上。
func (r *Routes) noteVia(p routedPacket) {
	u := p.addr
	key := u.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ts != nil && p.sock == r.ts {
		if _, ok := r.tsPeers[key]; !ok {
			if len(r.tsPeers) >= viaMax {
				clear(r.tsPeers)
				clear(r.derpCache)
			}
			delete(r.derpCache, key)
		}
		r.tsPeers[key] = true
	} else {
		if r.tsPeers[key] {
			delete(r.tsPeers, key)
			delete(r.derpCache, key)
		}
	}
	if p.sock == r.defaultSock(u) {
		delete(r.via, key)
		return
	}
	if _, ok := r.via[key]; !ok && len(r.via) >= viaMax {
		clear(r.via)
	}
	r.via[key] = p.sock
}

// defaultSock 是没记着时发往 u 用的 socket：中转走 relaySock，其余走 lan。
func (r *Routes) defaultSock(u *net.UDPAddr) net.PacketConn {
	if r.isDeskRelay(u) {
		return r.relaySock()
	}
	return r.lan
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
		sock = r.defaultSock(u)
	}
	if uc, ok := sock.(*net.UDPConn); ok {
		return uc.WriteToUDP(b, u)
	}
	return sock.WriteTo(b, u)
}

func (r *Routes) Close() error {
	r.once.Do(func() { close(r.done) })
	if r.out != nil {
		r.out.Close()
	}
	if r.ts != nil {
		r.ts.Close()
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
