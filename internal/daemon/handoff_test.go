package daemon

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// 前面的大帧对方还没读完就发描述符：macOS 上 sendmsg 直接报 EMSGSIZE，不会等，sendFile 要自己等对方读走再发。
func TestSendFileAfterLargeFrame(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) *net.UnixConn {
		f := os.NewFile(uintptr(fd), "sock")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c.(*net.UnixConn)
	}
	snd, rcv := conn(fds[0]), conn(fds[1])
	defer snd.Close()
	defer rcv.Close()
	frame := make([]byte, 4<<20)
	got := make(chan error, 1)
	go func() {
		// 对方慢一点才开始读，和刚拉起的新进程一样
		time.Sleep(200 * time.Millisecond)
		if _, err := io.ReadFull(rcv, make([]byte, len(frame))); err != nil {
			got <- err
			return
		}
		f, err := recvFile(rcv, "pty")
		if err == nil {
			f.Close()
		}
		got <- err
	}()
	go func() {
		if _, err := snd.Write(frame); err != nil {
			got <- err
		}
	}()
	time.Sleep(50 * time.Millisecond)
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	if err := sendFile(snd, r); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}
