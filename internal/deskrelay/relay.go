// Package deskrelay 是手机看桌面和手机 QUIC 连接共用的 UDP 中转：手机和 Mac 打不通洞时，两边都连到一台有公网地址的服务器，
// 服务器按同一个令牌把两边的包原样互转。包本身已经端到端加密，服务器只看到密文。
//
// 控制包都以 'H' 'R' 'Y' '1' 开头，其余一律当数据转发：
//
//	登记：'H' 'R' 'Y' '1'、u8 身份（1 Mac、2 手机）、16 字节令牌，共 21 字节。
//	      服务器回 'H' 'R' 'Y' '1'、0x80|身份，表示收到。两边每隔几秒重发一次当保活，换了网络也靠它更新地址。
//	探测：'H' 'R' 'Y' '1'、0x03，补零到 24 字节（回包不比请求大，不能被拿来放大流量）。
//	      服务器回 'H' 'R' 'Y' '1'、0x83、它看到的来源地址（IPv4 4 字节或 IPv6 16 字节）、u16 端口。
//	      打洞前两端各自用它问出自己在公网上的地址，和会话无关。
//	打洞：'H' 'R' 'Y' '1'、0x05。两端直接互发、不经服务器，只为在各自的 NAT 上开口子，收到就丢。
//
// 服务器配了主密钥时 Mac 的登记要带签名，见 auth.go；Mac 经中转发手机推送见 push.go。
//
// 数据包是看桌面的 desk 包或手机连接的 QUIC 包。desk 包的第一个字节是类型（internal/desk 的 Packet*，都小于 'H'），
// 撞不上控制包；QUIC 的短包头第一个字节可能是 'H'，但要连着后面的连接 ID 凑出 "RY1" 才会被误认，概率可以忽略。
package deskrelay

import (
	"bytes"
	"encoding/binary"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	RoleMac   byte = 1
	RolePhone byte = 2

	TokenLen    = 16
	RegisterLen = 4 + 1 + TokenLen
	ProbeLen    = 24

	kindProbe      byte = 0x03
	kindProbeReply byte = 0x83
	kindPunch      byte = 0x05

	// 两边都超过这么久没登记、没发包，会话作废
	sessionIdle = 2 * time.Minute
	maxSessions = 64
	// maxPerOwner 是开了鉴权时一个名字最多开几个会话，免得一个人占满名额。
	// 一台 Mac 每台手机一个，看桌面时再多一个
	maxPerOwner = 16
	packetLimit = 1500
)

var magic = []byte("HRY1")

// Register 是一个登记包。
func Register(role byte, token []byte) []byte {
	out := make([]byte, 0, RegisterLen)
	out = append(out, magic...)
	out = append(out, role)
	return append(out, token...)
}

// Probe 是一个探测包。
func Probe() []byte {
	out := make([]byte, ProbeLen)
	copy(out, magic)
	out[4] = kindProbe
	return out
}

// ParseProbeReply 解开探测的回包，得到自己在公网上的地址。
func ParseProbeReply(b []byte) (netip.AddrPort, bool) {
	if len(b) != 5+4+2 && len(b) != 5+16+2 || !bytes.Equal(b[:4], magic) || b[4] != kindProbeReply {
		return netip.AddrPort{}, false
	}
	ip, _ := netip.AddrFromSlice(b[5 : len(b)-2])
	return netip.AddrPortFrom(ip, binary.BigEndian.Uint16(b[len(b)-2:])), true
}

func probeReply(from netip.AddrPort) []byte {
	ip := from.Addr().AsSlice()
	out := append(append([]byte{}, magic...), kindProbeReply)
	out = append(out, ip...)
	return binary.BigEndian.AppendUint16(out, from.Port())
}

// Punch 是一个打洞包。
func Punch() []byte { return append(append([]byte{}, magic...), kindPunch) }

// IsControl 判断是不是中转的控制包（登记或回执），这种包不交给 desk 解密。
func IsControl(b []byte) bool {
	return len(b) >= 5 && bytes.Equal(b[:4], magic)
}

// IsAck 判断是不是服务器对这个身份登记的回执。
func IsAck(b []byte, role byte) bool {
	return len(b) == 5 && bytes.Equal(b[:4], magic) && b[4] == 0x80|role
}

type session struct {
	addr  [3]netip.AddrPort // 下标是身份
	seen  time.Time
	owner string // 开会话的 Mac 登记用的名字，没开鉴权时为空
}

// Server 是中转服务器。所有状态都在内存里，重启后两边几秒内重新登记。
type Server struct {
	conn     *net.UDPConn
	mu       sync.Mutex
	sessions map[[TokenLen]byte]*session
	byAddr   map[netip.AddrPort][TokenLen]byte
	now      func() time.Time

	master  []byte          // 主密钥，空的就是不鉴权
	revoked map[string]bool // 停用的名字

	push pushState // 推送（push.go）
}

func NewServer(conn *net.UDPConn) *Server {
	return &Server{
		conn:     conn,
		sessions: map[[TokenLen]byte]*session{},
		byAddr:   map[netip.AddrPort][TokenLen]byte{},
		now:      time.Now,
	}
}

// SetMaster 开启鉴权，在 Serve 之前调用。
func (s *Server) SetMaster(master []byte) { s.master = master }

// SetRevoked 换掉停用名单，并关掉这些人开的会话（会话里一直有包也不留）。
func (s *Server) SetRevoked(names []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = map[string]bool{}
	for _, n := range names {
		s.revoked[n] = true
	}
	for token, sess := range s.sessions {
		if sess.owner != "" && s.revoked[sess.owner] {
			s.dropLocked(token, sess)
			log.Printf("deskrelay: session of %s closed: revoked (%d open)", sess.owner, len(s.sessions))
		}
	}
}

func (s *Server) isRevoked(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revoked[name]
}

// Serve 一直收包，直到连接被关掉。
func (s *Server) Serve() error {
	buf := make([]byte, packetLimit+1)
	lastSweep := s.now()
	for {
		n, from, err := s.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return err
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		if now := s.now(); now.Sub(lastSweep) > 10*time.Second {
			s.sweep(now)
			lastSweep = now
		}
		if n > packetLimit {
			continue
		}
		if to, ok := s.handle(buf[:n], from); ok {
			_, _ = s.conn.WriteToUDPAddrPort(buf[:n], to)
		}
	}
}

// handle 处理一个包：登记就记下地址并回执，数据就返回该转给谁。
func (s *Server) handle(b []byte, from netip.AddrPort) (to netip.AddrPort, forward bool) {
	if IsControl(b) {
		s.control(b, from)
		return to, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.byAddr[from]
	if !ok {
		return to, false
	}
	sess := s.sessions[token]
	if sess == nil {
		return to, false
	}
	sess.seen = s.now()
	for role := RoleMac; role <= RolePhone; role++ {
		if sess.addr[role] == from {
			other := sess.addr[RoleMac+RolePhone-role]
			return other, other.IsValid()
		}
	}
	return to, false
}

func (s *Server) control(b []byte, from netip.AddrPort) {
	reply := func(p ...byte) { _, _ = s.conn.WriteToUDPAddrPort(append(append([]byte{}, magic...), p...), from) }
	switch {
	case len(b) == ProbeLen && b[4] == kindProbe:
		_, _ = s.conn.WriteToUDPAddrPort(probeReply(from), from)
		return
	case b[4] == kindCheck:
		sg, ok := parseSigned(b, 5)
		if !ok {
			return
		}
		result := CheckOK
		if s.master != nil {
			result = verify(s.master, sg, s.isRevoked, s.now())
		}
		reply(kindCheckReply, result)
		return
	case b[4] == kindPush:
		s.handlePush(b, from)
		return
	case len(b) < RegisterLen || (b[4] != RoleMac && b[4] != RolePhone):
		return
	}
	role := b[4]
	var token [TokenLen]byte
	copy(token[:], b[5:RegisterLen])
	owner, open := "", true
	switch {
	case len(b) == RegisterLen:
		// 开了鉴权时，不带签名的只有手机能登记，而且只能加入 Mac 开好的会话
		if s.master != nil {
			if role == RoleMac {
				reply(deniedBit | role)
				return
			}
			open = false
		}
	case role == RoleMac:
		sg, ok := parseSigned(b, RegisterLen)
		if !ok {
			return
		}
		if s.master != nil {
			if r := verify(s.master, sg, s.isRevoked, s.now()); r != CheckOK {
				reply(deniedBit | role)
				return
			}
			owner = sg.name
		}
	default:
		return
	}
	if s.register(token, role, from, owner, open) {
		reply(0x80 | role)
	}
}

// register 记下 from 是这个会话的这个身份。open 为 false 时只加入已有的会话。
func (s *Server) register(token [TokenLen]byte, role byte, from netip.AddrPort, owner string, open bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[token]
	if sess == nil {
		if !open || len(s.sessions) >= maxSessions {
			return false
		}
		if owner != "" && s.ownedLocked(owner) >= maxPerOwner {
			log.Printf("deskrelay: %s already has %d sessions, refused another", owner, maxPerOwner)
			return false
		}
		sess = &session{owner: owner}
		s.sessions[token] = sess
		log.Printf("deskrelay: session opened by %q (%d open)", owner, len(s.sessions))
	}
	if owner != "" {
		sess.owner = owner
	}
	// 同一个地址只能属于一个会话、一个身份：换了令牌或身份就从旧的里摘掉
	if old, ok := s.byAddr[from]; ok && old != token {
		if o := s.sessions[old]; o != nil {
			for r := range o.addr {
				if o.addr[r] == from {
					o.addr[r] = netip.AddrPort{}
				}
			}
		}
	}
	if prev := sess.addr[role]; prev.IsValid() && prev != from {
		delete(s.byAddr, prev)
	}
	sess.addr[role] = from
	sess.seen = s.now()
	s.byAddr[from] = token
	return true
}

func (s *Server) ownedLocked(owner string) int {
	n := 0
	for _, sess := range s.sessions {
		if sess.owner == owner {
			n++
		}
	}
	return n
}

func (s *Server) sweep(now time.Time) {
	s.sweepPush(now)
	s.mu.Lock()
	defer s.mu.Unlock()
	for token, sess := range s.sessions {
		if now.Sub(sess.seen) < sessionIdle {
			continue
		}
		s.dropLocked(token, sess)
		log.Printf("deskrelay: session closed (%d open)", len(s.sessions))
	}
}

func (s *Server) dropLocked(token [TokenLen]byte, sess *session) {
	for _, a := range sess.addr {
		if a.IsValid() && s.byAddr[a] == token {
			delete(s.byAddr, a)
		}
	}
	delete(s.sessions, token)
}
