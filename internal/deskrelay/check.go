package deskrelay

import (
	"context"
	"fmt"
	"net"
	"time"
)

// ErrUnreachable 是中转在时限内没有应答。
var ErrUnreachable = fmt.Errorf("deskrelay: relay did not answer")

// CheckRelay 经 conn 问中转这份密钥能不能用，返回 Check* 之一；ctx 结束还没应答时返回 ErrUnreachable。
// c 为 nil（旧的只有地址的设置）时用一份不存在的密钥问：没开鉴权的服务器回可以，开了的回密钥不对。
func CheckRelay(ctx context.Context, conn *net.UDPConn, relay string, c *Credential) (byte, error) {
	raddr, err := net.ResolveUDPAddr("udp4", relay)
	if err != nil {
		return 0, err
	}
	if c == nil {
		c = &Credential{Name: "anonymous", Key: make([]byte, KeyLen)}
	}
	buf := make([]byte, 64)
	for {
		if _, err := conn.WriteToUDP(Check(*c, time.Now()), raddr); err != nil {
			return 0, err
		}
		next := time.Now().Add(300 * time.Millisecond)
		for time.Now().Before(next) {
			if ctx.Err() != nil {
				return 0, ErrUnreachable
			}
			conn.SetReadDeadline(next)
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}
			if from.Port != raddr.Port || !from.IP.Equal(raddr.IP) {
				continue
			}
			if r, ok := ParseCheckReply(buf[:n]); ok {
				return r, nil
			}
		}
	}
}
