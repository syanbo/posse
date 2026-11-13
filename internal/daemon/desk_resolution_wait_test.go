package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"posse/internal/desk"
	"posse/internal/mobile"
)

type blockedResolutionControl struct {
	io.WriteCloser
	entered, closed chan struct{}
	once            sync.Once
}

func (c *blockedResolutionControl) Write(p []byte) (int, error) {
	if len(p) > 4 && p[4] == desk.CtrlResolution {
		close(c.entered)
		<-c.closed
		return 0, io.ErrClosedPipe
	}
	return c.WriteCloser.Write(p)
}
func (c *blockedResolutionControl) Close() error {
	c.once.Do(func() { close(c.closed) })
	return c.WriteCloser.Close()
}

type blockedResolutionProc struct {
	deskProc
	control *blockedResolutionControl
}

func (p blockedResolutionProc) Control() io.WriteCloser { return p.control }

func TestDeskResolutionCancelWithBackpressure(t *testing.T) {
	var blocked *blockedResolutionControl
	withDeskSpawn(t, func([]string) (deskProc, error) {
		p := newScriptDeskProc(t, []desk.Frame{configFrame()}, true, 0, false)
		blocked = &blockedResolutionControl{WriteCloser: p.Control(), entered: make(chan struct{}), closed: make(chan struct{})}
		return blockedResolutionProc{p, blocked}, nil
	})
	watch, cancelWatch := context.WithCancel(context.Background())
	defer cancelWatch()
	events := make(chan mobile.DeskEvent, 16)
	d := &Daemon{}
	ready, err := d.deskStart(watch, deskRequest(), func(e mobile.DeskEvent) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	request, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ready.Resolution(request, 1440) }()
	select {
	case <-blocked.entered:
	case <-time.After(time.Second):
		t.Fatal("control write did not block")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request result %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled request still waits on the pipe")
	}
	// 上一个写仍然堵着，新的等待也必须有界；不额外起一个写管道的协程。
	next, cancelNext := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelNext()
	if err := ready.Resolution(next, 720); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("next wait %v", err)
	}
	cancelWatch()
	if reason := endReason(t, events); reason != "unsubscribed" {
		t.Fatalf("end %s", reason)
	}
}

func TestDeskHelperRestoresWindowIntent(t *testing.T) {
	for _, tt := range []struct {
		name string
		last desk.Displays
		want uint32
	}{
		{"temporary lock screen", desk.Displays{Current: 1, RestoreWindow: 77}, 77},
		{"explicit display while locked", desk.Displays{Current: 1}, 0},
		{"window gone", desk.Displays{Current: 1, Gone: true}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := newFakeDeskProc(t, nil, false)
			ctl := &teeControl{WriteCloser: p.Control()}
			proc := teeDeskProc{p, ctl}
			defer stopDeskProc(proc)
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			go io.Copy(io.Discard, reader)
			h := newDeskHelper(func(uint32) (deskProc, []byte, byte, error) { return proc, nil, 0, nil }, proc, 1, writer)
			h.delay = 0
			note := func(ds desk.Displays) {
				b, _ := json.Marshal(ds)
				h.note(desk.Frame{Kind: desk.KindDisplays, Payload: b})
			}
			note(desk.Displays{Current: 1, Window: &desk.Window{ID: 77}})
			note(tt.last)
			if err := h.restart(errors.New("capture stopped")); err != nil {
				t.Fatal(err)
			}
			ctl.mu.Lock()
			raw := bytes.Clone(ctl.log)
			ctl.mu.Unlock()
			if tt.want == 0 {
				if len(raw) != 0 {
					t.Fatalf("restored canceled window: %x", raw)
				}
				return
			}
			got, err := desk.ReadControl(bytes.NewReader(raw))
			if err != nil || got.Kind != desk.CtrlWindow || got.Window != tt.want {
				t.Fatalf("restore %+v %v", got, err)
			}
		})
	}
}
