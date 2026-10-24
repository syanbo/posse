//go:build !darwin

package desk

import (
	"errors"
	"net"
)

func ListenPhysical() (*net.UDPConn, error) {
	return nil, errors.New("binding to a physical interface is macOS only")
}
