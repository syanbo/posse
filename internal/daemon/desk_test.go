package daemon

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"hive/internal/desk"
	"hive/internal/mobile"
)

// fakeDeskProc 照子进程的样子：先写 first 里的帧，再按 video 为真每 20ms 写一帧视频；控制管道读到 EOF 就退出。
type fakeDeskProc struct {
	fr, fw   *os.File
	cr, cw   *os.File
	done     chan struct{}
	closeOne sync.Once
}

func newFakeDeskProc(t *testing.T, first []desk.Frame, video bool) *fakeDeskProc {
	t.Helper()
	fr, fw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cr, cw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeDeskProc{fr: fr, fw: fw, cr: cr, cw: cw, done: make(chan struct{})}
	stop := make(chan struct{})
	go func() {
		io.Copy(io.Discard, cr)
		close(stop)
	}()
	go func() {
		defer func() {
			fw.Close()
			<-stop
			close(p.done)
		}()
		for _, f := range first {
			if desk.WriteFrame(fw, f) != nil {
				return
			}
		}
		if !video {
			return
		}
		for n := uint64(1); ; n++ {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			f := desk.Frame{Kind: desk.KindVideo, Keyframe: n == 1, Number: n, CaptureUnixNano: time.Now().UnixNano(),
				Payload: []byte{0, 0, 0, 1, 0x40, 0x01, 1, 2, 3}}
			if desk.WriteFrame(fw, f) != nil {
				return
			}
		}
	}()
	return p
}

func (p *fakeDeskProc) Frames() io.ReadCloser   { return p.fr }
func (p *fakeDeskProc) Control() io.WriteCloser { return p.cw }
func (p *fakeDeskProc) Wait() error {
	<-p.done
	return nil
}

func configFrame() desk.Frame {
	return desk.Frame{Kind: desk.KindConfig, Payload: desk.MarshalConfig(desk.Config{Codec: desk.CodecHEVC, Width: 1920, Height: 1246})}
}

func deskRequest() mobile.DeskRequest {
	secret := make([]byte, 64)
	nonce := make([]byte, 16)
	rand.Read(secret)
	rand.Read(nonce)
	return mobile.DeskRequest{Device: "iPhone", Secret: secret, Nonce: nonce, LongEdge: 2556, Codecs: []string{"hevc"}}
}

func withDeskSpawn(t *testing.T, fn func(args []string) (deskProc, error)) {
	old := deskSpawn
	deskSpawn = fn
	t.Cleanup(func() { deskSpawn = old })
}

func TestDeskWatchAndStop(t *testing.T) {
	var gotArgs []string
	withDeskSpawn(t, func(args []string) (deskProc, error) {
		gotArgs = args
		return newFakeDeskProc(t, []desk.Frame{configFrame()}, true), nil
	})
	d := &Daemon{}
	events := make(chan mobile.DeskEvent, 4)
	req := deskRequest()
	ready, err := d.deskStart(context.Background(), req, func(e mobile.DeskEvent) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	if ready.Codec != "hevc" || ready.Port == 0 {
		t.Fatalf("ready %+v", ready)
	}
	if strings.Join(gotArgs, " ") != "--quiet --long-edge 1920" {
		t.Fatalf("helper args %q", gotArgs)
	}
	if viewer, _ := d.deskStatus(); viewer != "iPhone" {
		t.Fatalf("viewer %q", viewer)
	}
	if _, err := d.deskStart(context.Background(), deskRequest(), func(mobile.DeskEvent) {}); err == nil || !strings.HasPrefix(err.Error(), "busy:") {
		t.Fatalf("second viewer: %v", err)
	}

	// 手机：hello 之后收到加密的视频包
	toMobile, toMac, err := desk.DeriveKeys(req.Secret, req.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := desk.NewAEAD(toMac)
	opener, _ := desk.NewAEAD(toMobile)
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: ready.Port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	hello, _ := desk.Seal(sealer, desk.DirToMac, desk.PacketHello, 1, nil)
	conn.Write(hello)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, desk.PacketLimit)
	gotVideo := false
	for !gotVideo {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("no video: %v", err)
		}
		kind, _, _, err := desk.Open(opener, desk.DirToMobile, buf[:n])
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		gotVideo = kind == desk.PacketVideo
	}

	if _, err := d.deskStop(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		if e.Type != "end" || e.Reason != "stopped" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end after desk.stop")
	}
	if viewer, _ := d.deskStatus(); viewer != "" {
		t.Fatalf("viewer after stop %q", viewer)
	}
}

func TestDeskUnsubscribe(t *testing.T) {
	withDeskSpawn(t, func([]string) (deskProc, error) {
		return newFakeDeskProc(t, []desk.Frame{configFrame()}, true), nil
	})
	d := &Daemon{}
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan mobile.DeskEvent, 4)
	if _, err := d.deskStart(ctx, deskRequest(), func(e mobile.DeskEvent) { events <- e }); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case e := <-events:
		if e.Reason != "unsubscribed" {
			t.Fatalf("event %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end after unsubscribe")
	}
}

func TestDeskRefusals(t *testing.T) {
	status := func(code byte) desk.Frame {
		return desk.Frame{Kind: desk.KindStatus, Payload: []byte{code}}
	}
	for _, tc := range []struct {
		first []desk.Frame
		code  string
	}{
		{[]desk.Frame{status(desk.StatusNoScreen)}, "permission_screen:"},
		{[]desk.Frame{status(desk.StatusLocked)}, "locked:"},
		{nil, "capture_failed:"},
	} {
		withDeskSpawn(t, func([]string) (deskProc, error) {
			return newFakeDeskProc(t, tc.first, false), nil
		})
		d := &Daemon{}
		_, err := d.deskStart(context.Background(), deskRequest(), func(mobile.DeskEvent) {})
		if err == nil || !strings.HasPrefix(err.Error(), tc.code) {
			t.Fatalf("want %s got %v", tc.code, err)
		}
		viewer, missing := d.deskStatus()
		if viewer != "" {
			t.Fatalf("viewer left behind after %s", tc.code)
		}
		if tc.code == "permission_screen:" && (len(missing) != 1 || missing[0] != "screen") {
			t.Fatalf("missing %v", missing)
		}
	}
	if _, err := deskCodec([]string{"av1"}); err == nil {
		t.Fatal("accepted a codec the Mac cannot send")
	}
}
