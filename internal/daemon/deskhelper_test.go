package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"posse/internal/desk"
	"posse/internal/mobile"
)

// scriptDeskProc 先写 first 里的帧，再每 20ms 写一帧视频（video 为假时什么都不写、也不退出），
// 写了 live 这么久就自己退出（0 是一直写；clean 为真时算正常退出，否则算出错退出）；控制管道读到 EOF 或被杀就退出。
// 读到的控制消息放进 controls。
type scriptDeskProc struct {
	exitErr  error
	fr, fw   *os.File
	cr, cw   *os.File
	done     chan struct{}
	stop     chan struct{}
	stopOnce sync.Once
	controls chan desk.Control
}

func newScriptDeskProc(t *testing.T, first []desk.Frame, video bool, live time.Duration, clean bool) *scriptDeskProc {
	t.Helper()
	fr, fw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cr, cw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p := &scriptDeskProc{fr: fr, fw: fw, cr: cr, cw: cw, done: make(chan struct{}), stop: make(chan struct{}), controls: make(chan desk.Control, 64)}
	go func() {
		for {
			c, err := desk.ReadControl(cr)
			if err != nil {
				p.halt()
				return
			}
			select {
			case p.controls <- c:
			default:
			}
		}
	}()
	go func() {
		defer func() {
			fw.Close()
			close(p.done)
		}()
		for _, f := range first {
			if desk.WriteFrame(fw, f) != nil {
				return
			}
		}
		var end <-chan time.Time
		if live > 0 {
			end = time.After(live)
		}
		for n := uint64(1); ; n++ {
			select {
			case <-p.stop:
				return
			case <-end:
				if !clean {
					p.exitErr = errors.New("desk: helper exited 1")
				}
				return
			case <-time.After(20 * time.Millisecond):
			}
			if !video {
				continue
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

func (p *scriptDeskProc) halt()                   { p.stopOnce.Do(func() { close(p.stop) }) }
func (p *scriptDeskProc) Frames() io.ReadCloser   { return p.fr }
func (p *scriptDeskProc) Control() io.WriteCloser { return p.cw }
func (p *scriptDeskProc) Wait() error             { <-p.done; return p.exitErr }
func (p *scriptDeskProc) Kill() error             { p.halt(); return nil }

func displaysFrame(current uint32) desk.Frame {
	b, _ := json.Marshal(desk.Displays{Current: current, List: []desk.Display{{ID: current}}})
	return desk.Frame{Kind: desk.KindDisplays, Payload: b}
}

func withDeskRestart(t *testing.T, limit int, quiet time.Duration) {
	oldLimit, oldDelay, oldQuiet := deskRestartLimit, deskRestartDelay, deskHelperQuiet
	deskRestartLimit, deskRestartDelay, deskHelperQuiet = limit, time.Millisecond, quiet
	t.Cleanup(func() { deskRestartLimit, deskRestartDelay, deskHelperQuiet = oldLimit, oldDelay, oldQuiet })
}

// 子进程出错退出：重新拉起，接着看它退出前看的那块屏，码率补发给新的子进程，观看不断
func TestDeskHelperRestartsAfterExit(t *testing.T) {
	withDeskRestart(t, 3, 10*time.Second)
	var mu sync.Mutex
	var spawned [][]string
	var procs []*scriptDeskProc
	withDeskSpawn(t, func(args []string) (deskProc, error) {
		mu.Lock()
		defer mu.Unlock()
		spawned = append(spawned, args)
		var p *scriptDeskProc
		if len(spawned) == 1 {
			p = newScriptDeskProc(t, []desk.Frame{configFrame(), displaysFrame(7)}, true, 100*time.Millisecond, false)
		} else {
			p = newScriptDeskProc(t, []desk.Frame{configFrame()}, true, 0, false)
		}
		procs = append(procs, p)
		return p, nil
	})
	d := &Daemon{}
	events := make(chan mobile.DeskEvent, 8)
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := d.deskStart(ctx, deskRequest(), func(e mobile.DeskEvent) { events <- e }); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(procs)
		mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case e := <-events:
			if e.Type == "end" {
				t.Fatalf("ended instead of restarting: %+v", e)
			}
		case <-deadline:
			t.Fatal("helper was not restarted")
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	args, second := strings.Join(spawned[1], " "), procs[1]
	mu.Unlock()
	if !strings.HasSuffix(args, "--display 7") {
		t.Fatalf("restart args %q", args)
	}
	select {
	case c := <-second.controls:
		if c.Kind != desk.CtrlBitrate {
			t.Fatalf("first control to the new helper %+v", c)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the new helper got no bitrate")
	}
	cancel()
	select {
	case e := <-events:
		for e.Type != "end" {
			e = <-events
		}
		if e.Reason != "unsubscribed" {
			t.Fatalf("end %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no end after unsubscribe")
	}
}

// 子进程一直出错：一分钟内重建 deskRestartLimit 次以后不再试，这次观看以 capture_failed 结束
func TestDeskHelperGivesUp(t *testing.T) {
	withDeskRestart(t, 2, 10*time.Second)
	var mu sync.Mutex
	n := 0
	withDeskSpawn(t, func([]string) (deskProc, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return newScriptDeskProc(t, []desk.Frame{configFrame()}, true, 30*time.Millisecond, false), nil
	})
	d := &Daemon{}
	events := make(chan mobile.DeskEvent, 8)
	if _, err := d.deskStart(context.Background(), deskRequest(), func(e mobile.DeskEvent) { events <- e }); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-events:
		for e.Type != "end" {
			e = <-events
		}
		if e.Reason != "capture_failed" {
			t.Fatalf("end %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not give up")
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 3 {
		t.Fatalf("spawned %d times, want the first and 2 restarts", n)
	}
	if viewer, _ := d.deskStatus(); viewer != "" {
		t.Fatalf("viewer left behind %q", viewer)
	}
}

// 子进程不退出也不再写任何东西（连心跳都没有）：当作卡死，停掉它重新拉起
func TestDeskHelperRestartsWhenSilent(t *testing.T) {
	withDeskRestart(t, 3, 200*time.Millisecond)
	var mu sync.Mutex
	var procs []*scriptDeskProc
	withDeskSpawn(t, func([]string) (deskProc, error) {
		mu.Lock()
		defer mu.Unlock()
		p := newScriptDeskProc(t, []desk.Frame{configFrame()}, len(procs) > 0, 0, false)
		procs = append(procs, p)
		return p, nil
	})
	d := &Daemon{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := d.deskStart(ctx, deskRequest(), func(mobile.DeskEvent) {}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(procs)
		var first *scriptDeskProc
		if n > 0 {
			first = procs[0]
		}
		mu.Unlock()
		if n >= 2 {
			select {
			case <-first.done:
			default:
				t.Fatal("the silent helper is still running")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("silent helper was not restarted")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// endReason 等到 end 事件，返回它的理由。
func endReason(t *testing.T, events <-chan mobile.DeskEvent) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Type == "end" {
				return e.Reason
			}
		case <-deadline:
			t.Fatal("no end event")
		}
	}
}

// 子进程正常退出（用户在菜单栏停了录屏）：不重建，这次观看结束
func TestDeskHelperCleanExitEnds(t *testing.T) {
	withDeskRestart(t, 3, 10*time.Second)
	var mu sync.Mutex
	n := 0
	withDeskSpawn(t, func([]string) (deskProc, error) {
		mu.Lock()
		n++
		mu.Unlock()
		return newScriptDeskProc(t, []desk.Frame{configFrame()}, true, 50*time.Millisecond, true), nil
	})
	d := &Daemon{}
	events := make(chan mobile.DeskEvent, 8)
	if _, err := d.deskStart(context.Background(), deskRequest(), func(e mobile.DeskEvent) { events <- e }); err != nil {
		t.Fatal(err)
	}
	if r := endReason(t, events); r != "capture_failed" {
		t.Fatalf("end %q", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 1 {
		t.Fatalf("spawned %d times after a clean exit", n)
	}
}

// 重建时已经锁屏：新子进程报了锁屏就退出，这次观看照锁屏结束，不当成采集失败
func TestDeskHelperRestartFindsLocked(t *testing.T) {
	withDeskRestart(t, 3, 10*time.Second)
	var mu sync.Mutex
	n := 0
	withDeskSpawn(t, func([]string) (deskProc, error) {
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return newScriptDeskProc(t, []desk.Frame{configFrame()}, true, 50*time.Millisecond, false), nil
		}
		return newScriptDeskProc(t, []desk.Frame{{Kind: desk.KindStatus, Payload: []byte{desk.StatusLocked}}}, false, time.Millisecond, true), nil
	})
	d := &Daemon{}
	events := make(chan mobile.DeskEvent, 8)
	if _, err := d.deskStart(context.Background(), deskRequest(), func(e mobile.DeskEvent) { events <- e }); err != nil {
		t.Fatal(err)
	}
	if r := endReason(t, events); r != "locked" {
		t.Fatalf("end %q", r)
	}
	mu.Lock()
	defer mu.Unlock()
	if n != 2 {
		t.Fatalf("spawned %d times", n)
	}
}
