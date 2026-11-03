// Command deskrelay 是手机看桌面和手机 QUIC 连接的 UDP 中转服务器，部署在一台有公网地址的机器上。
// 协议和行为见 internal/deskrelay。
//
//	GOOS=linux GOARCH=amd64 go build -o deskrelay ./tools/deskrelay
//	deskrelay --listen :443 --secret /etc/deskrelay/secret [--revoked /etc/deskrelay/revoked] \
//	          [--apns-key AuthKey_XXXX.p8 --apns-key-id XXXX]
//	deskrelay admin-code --secret /etc/deskrelay/secret --public 1.2.3.4:443
//
// --secret 指向的主密钥文件不存在时自动生成。admin-code 打印管理员码，贴进管理员自己的 Mac
// （Posse 手机页的「外网中转」），之后在那台 Mac 上给别人生成邀请码。--revoked 是停用的名字，一行一个，
// 改了 10 秒内生效，这些人开的会话立刻关掉。不给 --secret 时不鉴权，谁都能用。
// --apns-key 是 Apple 开发者后台生成的 APNs 密钥（.p8），给了才替 Mac 发手机推送。
package main

import (
	"bytes"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/deskrelay"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "admin-code" {
		adminCode(os.Args[2:])
		return
	}
	listen := flag.String("listen", ":443", "UDP address to listen on")
	secret := flag.String("secret", "", "master key file (created if missing); empty: anyone can use the relay")
	revoked := flag.String("revoked", "", "file of revoked names, one per line")
	apnsKey := flag.String("apns-key", "", "APNs auth key (.p8); empty: no phone push")
	apnsKeyID := flag.String("apns-key-id", "", "key ID of the APNs auth key")
	apnsTeam := flag.String("apns-team", "QKSM9Z4U85", "Apple developer team ID")
	apnsTopic := flag.String("apns-topic", "com.neolabsapp.posse.mobile", "bundle ID of the phone app")
	flag.Parse()
	addr, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		log.Fatal(err)
	}
	srv := deskrelay.NewServer(conn)
	if *secret != "" {
		master, err := loadMaster(*secret, true)
		if err != nil {
			log.Fatal(err)
		}
		srv.SetMaster(master)
		log.Printf("deskrelay: only Macs with a key from %s can open sessions", *secret)
	} else {
		log.Printf("deskrelay: no --secret, anyone can use this relay")
	}
	if *revoked != "" {
		go watchRevoked(srv, *revoked)
	}
	if *apnsKey != "" {
		p8, err := os.ReadFile(*apnsKey)
		if err != nil {
			log.Fatal(err)
		}
		a, err := deskrelay.NewAPNs(p8, *apnsKeyID, *apnsTeam, *apnsTopic)
		if err != nil {
			log.Fatal(err)
		}
		srv.SetPusher(a)
		log.Printf("deskrelay: phone push on for %s", *apnsTopic)
	}
	log.Printf("deskrelay: listening on %s", conn.LocalAddr())
	log.Fatal(srv.Serve())
}

func adminCode(args []string) {
	fs := flag.NewFlagSet("admin-code", flag.ExitOnError)
	secret := fs.String("secret", "", "master key file")
	public := fs.String("public", "", "address Macs reach this relay at, host:port")
	fs.Parse(args)
	if *secret == "" || *public == "" {
		fmt.Fprintln(os.Stderr, "usage: deskrelay admin-code --secret FILE --public HOST:PORT")
		os.Exit(2)
	}
	master, err := loadMaster(*secret, false)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(deskrelay.AdminCode(*public, master))
}

// loadMaster 读主密钥文件（base64）；create 时没有就生成一个，只给自己读。
func loadMaster(path string, create bool) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		master := deskrelay.NewMaster()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(master)+"\n"), 0o600); err != nil {
			return nil, err
		}
		log.Printf("deskrelay: created master key %s; run `deskrelay admin-code` for the admin code", path)
		return master, nil
	}
	if err != nil {
		return nil, err
	}
	master, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(master) != deskrelay.KeyLen {
		return nil, fmt.Errorf("%s: not a deskrelay master key", path)
	}
	return master, nil
}

// watchRevoked 每 10 秒看一次停用名单，变了就换上。
func watchRevoked(srv *deskrelay.Server, path string) {
	var last []byte
	for first := true; ; first = false {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("deskrelay: %v", err)
		} else if first || !bytes.Equal(data, last) {
			last = data
			var names []string
			for _, l := range strings.Split(string(data), "\n") {
				if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
					names = append(names, l)
				}
			}
			srv.SetRevoked(names)
			log.Printf("deskrelay: %d revoked names", len(names))
		}
		time.Sleep(10 * time.Second)
	}
}
