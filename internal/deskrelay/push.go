package deskrelay

import (
	"bytes"
	"encoding/binary"
	"errors"
	"log"
	"net/netip"
	"sync"
	"time"
)

// 推送：Mac 让中转替它向 APNs 发一条通知（中转上有 APNs 密钥，Mac 没有）。
//
//	推送：'H' 'R' 'Y' '1'、0x07、u8 标志（PushSandbox）、16 字节 nonce、u8 token 长度、APNs token、
//	      16 字节折叠 ID、u16 角标、u16 密文长度、密文，接签名尾（同 auth.go：u64 时间、u8 名字长度、名字、16 字节签名）。
//	回执：'H' 'R' 'Y' '1'、0x87、16 字节 nonce、u8 结果（Push*）。
//
// 密文是 Mac 用手机的推送密钥加密的通知内容，中转原样放进 APNs 负载，只看得到 token、角标和折叠 ID。
// 同一个 nonce 5 分钟内只发一次，Mac 没收到回执重发时回上次的结果。开了鉴权时只认带签名的 Mac。

const (
	kindPush      byte = 0x07
	kindPushReply byte = 0x87
	// 新类型让旧中转直接忽略，不能把只更新角标的包误发成带声音的通知。
	kindPushOptions byte = 0x08
	pushNoAlert     byte = 2
	pushProbe       byte = 4

	NonceLen    = 16
	CollapseLen = 16
	// MaxPushCipher 是密文的上限：整包要放进一个 UDP 包（packetLimit），APNs 负载上限 4 KB 也够
	MaxPushCipher = 1100
	maxAPNsToken  = 100

	// PushSandbox 是标志位：手机是开发版（Xcode 装的），走 APNs 的沙盒环境
	PushSandbox byte = 1

	// 签名时间前后各 clockSkew 都收，去重记录要留够整个范围，否则过期后能原样重放
	pushDedupe = 2*clockSkew + time.Minute
	// pushPerHour 是一个名字一小时最多推几条，免得出错的 Mac 刷爆
	pushPerHour = 300
)

// 推送的结果。
const (
	PushOK         byte = 0
	PushBadKey     byte = 1 // 同 CheckBadKey
	PushRevoked    byte = 2
	PushClock      byte = 3
	PushBadToken   byte = 4 // APNs 说这个 token 不能用了（卸载、换环境），Mac 该删掉这条注册
	PushFailed     byte = 5 // APNs 暂时失败，可以稍后再试
	PushUnavail    byte = 6 // 这台中转没配 APNs
	PushRateLimit  byte = 7
	pushInProgress byte = 0xFF // 内部：还在发，不回
)

// Push 是一条要发的推送。
type Push struct {
	Nonce    [NonceLen]byte
	Sandbox  bool
	NoAlert  bool
	Probe    bool
	Token    []byte // APNs 设备 token（二进制）
	Collapse [CollapseLen]byte
	Badge    uint16
	Cipher   []byte
}

// EncodePush 是 Mac 发的推送包。每次重发都要重新生成：里面有时间。
func EncodePush(p Push, c *Credential, now time.Time) ([]byte, error) {
	if len(p.Token) == 0 || len(p.Token) > maxAPNsToken || len(p.Cipher) > MaxPushCipher {
		return nil, errors.New("push too large")
	}
	kind := kindPush
	if p.NoAlert || p.Probe {
		kind = kindPushOptions
	}
	out := append(append([]byte{}, magic...), kind)
	var flags byte
	if p.Sandbox {
		flags |= PushSandbox
	}
	if p.NoAlert {
		flags |= pushNoAlert
	}
	if p.Probe {
		flags |= pushProbe
	}
	out = append(out, flags)
	out = append(out, p.Nonce[:]...)
	out = append(out, byte(len(p.Token)))
	out = append(out, p.Token...)
	out = append(out, p.Collapse[:]...)
	out = binary.BigEndian.AppendUint16(out, p.Badge)
	out = binary.BigEndian.AppendUint16(out, uint16(len(p.Cipher)))
	out = append(out, p.Cipher...)
	if c == nil {
		// 没开鉴权的中转：签名尾照样要有，名字随便写、签名不核对
		c = &Credential{Name: "anonymous", Key: make([]byte, KeyLen)}
	}
	return appendSigned(out, *c, now), nil
}

// parsePush 解开推送包，返回推送和签名部分。
func parsePush(b []byte) (Push, signed, bool) {
	var p Push
	off := 5
	if len(b) < off+1+NonceLen+1 {
		return p, signed{}, false
	}
	p.Sandbox = b[off]&PushSandbox != 0
	if b[4] == kindPushOptions {
		p.NoAlert = b[off]&pushNoAlert != 0
		p.Probe = b[off]&pushProbe != 0
	}
	off++
	copy(p.Nonce[:], b[off:])
	off += NonceLen
	n := int(b[off])
	off++
	if n == 0 || n > maxAPNsToken || len(b) < off+n+CollapseLen+4 {
		return p, signed{}, false
	}
	p.Token = append([]byte{}, b[off:off+n]...)
	off += n
	copy(p.Collapse[:], b[off:])
	off += CollapseLen
	p.Badge = binary.BigEndian.Uint16(b[off:])
	m := int(binary.BigEndian.Uint16(b[off+2:]))
	off += 4
	if m > MaxPushCipher || len(b) < off+m {
		return p, signed{}, false
	}
	p.Cipher = append([]byte{}, b[off:off+m]...)
	off += m
	sg, ok := parseSigned(b, off)
	return p, sg, ok
}

// ParsePushReply 解开推送的回执。
func ParsePushReply(b []byte) (nonce [NonceLen]byte, result byte, ok bool) {
	if len(b) != 5+NonceLen+1 || !bytes.Equal(b[:4], magic) || b[4] != kindPushReply {
		return nonce, 0, false
	}
	copy(nonce[:], b[5:])
	return nonce, b[5+NonceLen], true
}

func pushReply(nonce [NonceLen]byte, result byte) []byte {
	out := append(append([]byte{}, magic...), kindPushReply)
	out = append(out, nonce[:]...)
	return append(out, result)
}

// Pusher 把推送交给 APNs，返回 Push* 结果。会阻塞，中转在单独的 goroutine 里调。
type Pusher interface {
	Send(p Push) byte
}

type pushSeen struct {
	result byte
	at     time.Time
}

// pushState 是中转上推送的去重和限流。
type pushState struct {
	mu     sync.Mutex
	pusher Pusher
	seen   map[[NonceLen]byte]pushSeen
	counts map[string][]time.Time // 名字 → 最近一小时的推送时间
}

// SetPusher 打开推送，在 Serve 之前调用。没调时推送一律回 PushUnavail。
func (s *Server) SetPusher(p Pusher) {
	s.push.pusher = p
}

func (s *Server) handlePush(b []byte, from netip.AddrPort) {
	p, sg, ok := parsePush(b)
	if !ok {
		return
	}
	reply := func(r byte) { _, _ = s.conn.WriteToUDPAddrPort(pushReply(p.Nonce, r), from) }
	owner := ""
	if s.master != nil {
		if r := verify(s.master, sg, s.isRevoked, s.now()); r != CheckOK {
			reply(r)
			return
		}
		owner = sg.name
	}
	ps := &s.push
	if ps.pusher == nil {
		reply(PushUnavail)
		return
	}
	now := s.now()
	if p.Probe {
		reply(PushOK)
		return
	}
	ps.mu.Lock()
	if ps.seen == nil {
		ps.seen = map[[NonceLen]byte]pushSeen{}
		ps.counts = map[string][]time.Time{}
	}
	if old, ok := ps.seen[p.Nonce]; ok && now.Sub(old.at) < pushDedupe {
		ps.mu.Unlock()
		if old.result != pushInProgress {
			reply(old.result)
		}
		return
	}
	recent := ps.counts[owner][:0]
	for _, t := range ps.counts[owner] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= pushPerHour {
		ps.counts[owner] = recent
		ps.mu.Unlock()
		reply(PushRateLimit)
		return
	}
	ps.counts[owner] = append(recent, now)
	ps.seen[p.Nonce] = pushSeen{result: pushInProgress, at: now}
	ps.mu.Unlock()
	go func() {
		r := ps.pusher.Send(p)
		ps.mu.Lock()
		ps.seen[p.Nonce] = pushSeen{result: r, at: s.now()}
		ps.mu.Unlock()
		if r != PushOK {
			log.Printf("deskrelay: push from %q: result %d", owner, r)
		}
		reply(r)
	}()
}

// sweepPush 清掉过期的去重记录，sweep 里调。
func (s *Server) sweepPush(now time.Time) {
	ps := &s.push
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for k, v := range ps.seen {
		if now.Sub(v.at) >= pushDedupe {
			delete(ps.seen, k)
		}
	}
	for k, ts := range ps.counts {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) >= time.Hour {
			delete(ps.counts, k)
		}
	}
}
