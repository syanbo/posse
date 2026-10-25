// echo 是 posse_quic 的 Dart 测试用的服务：原样回发收到的消息，收到文本 "close" 时发关闭帧。
// 启动后在标准输出打印一行：直连端口、本地中转的端口。打洞时测试往标准输入写 "punch 手机的地址"，
// 它回一行出口在中转看来的地址，并从出口向手机打洞；出口上也跑着同一个回显服务。
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/quic-go/quic-go"

	"posse/internal/deskrelay"
	"posse/internal/mquic"
)

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loopback() *net.UDPConn {
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	must(err)
	return pc
}

func serve(pc net.PacketConn, cfg *tls.Config) {
	ln, err := (&quic.Transport{Conn: pc}).Listen(cfg, mquic.Config())
	must(err)
	go func() {
		for {
			qc, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go echo(qc)
		}
	}()
}

func echo(qc *quic.Conn) {
	c, err := mquic.Accept(context.Background(), qc)
	if err != nil {
		return
	}
	for {
		typ, p, err := c.Read()
		if err != nil {
			return
		}
		if typ == mquic.Text && string(p) == "close" {
			c.Write(mquic.Text, []byte("bye"))
			c.Close(4001, "test")
			return
		}
		c.Write(typ, p)
	}
}

func main() {
	cfg, err := mquic.ServerTLS()
	must(err)
	direct := loopback()
	serve(direct, cfg)
	relay := loopback()
	go deskrelay.NewServer(relay).Serve()
	out := mquic.NewProbeConn(loopback(), relay.LocalAddr().(*net.UDPAddr))
	serve(out, cfg)
	fmt.Println(direct.LocalAddr().(*net.UDPAddr).Port, relay.LocalAddr().(*net.UDPAddr).Port)

	in := bufio.NewScanner(os.Stdin)
	for in.Scan() {
		addr, ok := strings.CutPrefix(in.Text(), "punch ")
		if !ok {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		pub, err := out.Public(ctx, true)
		cancel()
		must(err)
		peer, err := net.ResolveUDPAddr("udp4", addr)
		must(err)
		go out.PunchTo(peer, mquic.PunchFor)
		fmt.Println(pub)
	}
}
