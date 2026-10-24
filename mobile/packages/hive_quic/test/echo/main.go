// echo 是 hive_quic 的 Dart 测试用的服务：原样回发收到的消息，收到文本 "close" 时发关闭帧。
// 启动后在标准输出打印监听的端口。
package main

import (
	"context"
	"fmt"
	"net"
	"os"

	"github.com/quic-go/quic-go"

	"hive/internal/mquic"
)

func main() {
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cfg, err := mquic.ServerTLS()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ln, err := (&quic.Transport{Conn: pc}).Listen(cfg, mquic.Config())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(pc.LocalAddr().(*net.UDPAddr).Port)
	for {
		qc, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		go func() {
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
		}()
	}
}
