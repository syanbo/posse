package client

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"posse/internal/rpc"
)

// fakeAttach 起一个 socket，每条连接交给 serve。
func fakeAttach(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ca")
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); os.RemoveAll(dir) })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return sock
}

// 常驻进程收了连接却一直不回：过了期限 Read 报错，窗口才会重新接入。
func TestAttachNoReplyTimesOut(t *testing.T) {
	old := attachTimeout
	attachTimeout = 200 * time.Millisecond
	defer func() { attachTimeout = old }()
	hold := make(chan struct{})
	defer close(hold)
	sock := fakeAttach(t, func(c net.Conn) { <-hold; c.Close() })
	k := (&Client{Socket: sock}).Attach("t1")
	defer k.Close()
	done := make(chan error, 1)
	go func() {
		k.Resize(80, 24)
		_, err := k.Read(make([]byte, 16))
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("read succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("attach never timed out")
	}
}

// 期限只管等第一行回复：之后的输出隔多久来都照收。
func TestAttachStreamOutlivesTimeout(t *testing.T) {
	old := attachTimeout
	attachTimeout = 200 * time.Millisecond
	defer func() { attachTimeout = old }()
	sock := fakeAttach(t, func(c net.Conn) {
		defer c.Close()
		if _, err := bufio.NewReader(c).ReadBytes('\n'); err != nil {
			return
		}
		rpc.WriteResult(c, map[string]any{"cols": 80, "rows": 24})
		time.Sleep(3 * attachTimeout)
		rpc.WriteFrame(c, rpc.FrameOutput, []byte("late"))
		time.Sleep(time.Second)
	})
	k := (&Client{Socket: sock}).Attach("t1")
	defer k.Close()
	k.Resize(80, 24)
	buf := make([]byte, 16)
	n, err := k.Read(buf)
	if err != nil || string(buf[:n]) != "late" {
		t.Fatalf("read %q, %v", buf[:n], err)
	}
}
