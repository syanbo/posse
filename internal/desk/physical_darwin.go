//go:build darwin

package desk

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
)

// IP_BOUND_IF：把 socket 钉在一块网卡上，发包不再查路由表，Surge 等代理的 utun 默认路由就管不到它
const ipBoundIf = 25

// listenPhysical 开一个绑在物理网卡上的 UDP socket。
func listenPhysical() (*net.UDPConn, error) {
	ifc, err := physicalInterface()
	if err != nil {
		return nil, err
	}
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, ipBoundIf, ifc.Index)
		})
		if err != nil {
			return err
		}
		return serr
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		return nil, err
	}
	return pc.(*net.UDPConn), nil
}

// physicalInterface 挑第一块开着、有 IPv4 地址的 en 网卡（Wi-Fi 或有线）。
func physicalInterface() (*net.Interface, error) {
	ifcs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifcs {
		ifc := &ifcs[i]
		if !strings.HasPrefix(ifc.Name, "en") || ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagRunning == 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ip, ok := a.(*net.IPNet); ok && ip.IP.To4() != nil && !ip.IP.IsLinkLocalUnicast() {
				return ifc, nil
			}
		}
	}
	return nil, errors.New("no active en interface with IPv4")
}
