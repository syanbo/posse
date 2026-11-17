package remote

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestConnectCancel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			var b [4096]byte
			for {
				if _, err := c.Read(b[:]); err != nil {
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = Connect(ctx, Host{Name: "Stalled", Target: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port}, Options{config: "/dev/null"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel: %v", err)
	}
	ln.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSH socket still open after cancellation")
	}
}
