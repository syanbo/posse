package mquic

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

func TestTSConfigParams(t *testing.T) {
	cfg := TSConfig()
	if cfg.InitialPacketSize != 1200 {
		t.Fatalf("InitialPacketSize = %d, want 1200", cfg.InitialPacketSize)
	}
	if !cfg.DisablePathMTUDiscovery {
		t.Fatal("DisablePathMTUDiscovery should be true")
	}
	if cfg.KeepAlivePeriod != Config().KeepAlivePeriod {
		t.Fatalf("KeepAlivePeriod = %v, want %v", cfg.KeepAlivePeriod, Config().KeepAlivePeriod)
	}
}

func TestDialOverCustomPacketConn(t *testing.T) {
	tlsCfg, err := ServerTLS()
	if err != nil {
		t.Fatalf("ServerTLS: %v", err)
	}
	srvPC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP server: %v", err)
	}
	defer srvPC.Close()
	tr := &quic.Transport{Conn: srvPC}
	defer tr.Close()
	ln, err := tr.Listen(tlsCfg, TSConfig())
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	srvErr := make(chan error, 1)
	go func() {
		qc, err := ln.Accept(context.Background())
		if err != nil {
			srvErr <- err
			return
		}
		defer qc.CloseWithError(0, "")
		c, err := Accept(context.Background(), qc)
		if err != nil {
			srvErr <- err
			return
		}
		typ, msg, err := c.Read()
		if err != nil {
			srvErr <- err
			return
		}
		if err := c.Write(typ, append([]byte("echo:"), msg...)); err != nil {
			srvErr <- err
			return
		}
		srvErr <- nil
		<-qc.Context().Done()
	}()

	cliPC, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("ListenUDP client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := DialOver(ctx, cliPC, srvPC.LocalAddr(), TSConfig())
	if err != nil {
		t.Fatalf("DialOver: %v", err)
	}
	defer c.CloseNow()

	if err := c.Write(Text, []byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	typ, got, err := c.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if typ != Text || string(got) != "echo:hello" {
		t.Fatalf("got (%d, %q), want (%d, %q)", typ, got, Text, "echo:hello")
	}
	if err := <-srvErr; err != nil {
		t.Fatalf("server err: %v", err)
	}
}
