// Command deskrelay 是手机看桌面的 UDP 中转服务器，部署在一台有公网地址的机器上。
// 协议和行为见 internal/deskrelay。
//
//	GOOS=linux GOARCH=amd64 go build -o deskrelay ./tools/deskrelay
//	deskrelay --listen :443
package main

import (
	"flag"
	"log"
	"net"

	"posse/internal/deskrelay"
)

func main() {
	listen := flag.String("listen", ":443", "UDP address to listen on")
	flag.Parse()
	addr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("deskrelay: listening on %s", conn.LocalAddr())
	log.Fatal(deskrelay.NewServer(conn).Serve())
}
