package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"posse/internal/desk"
	"posse/internal/mobile"
)

func TestDeskResolutionRestoresLatestDuringRestart(t *testing.T) {
	withDeskRestart(t, 3, 10*time.Second)
	restarting := make(chan []string, 1)
	release := make(chan struct{})
	second := make(chan *scriptDeskProc, 1)
	n := 0
	var initial []string
	withDeskSpawn(t, func(args []string) (deskProc, error) {
		n++
		if n == 1 {
			initial = args
			ds, _ := json.Marshal(desk.Displays{Current: 7, Window: &desk.Window{ID: 77, App: "Safari"}})
			return newScriptDeskProc(t, []desk.Frame{configFrame(), {Kind: desk.KindDisplays, Payload: ds}}, true, 100*time.Millisecond, false), nil
		}
		restarting <- args
		<-release
		p := newScriptDeskProc(t, []desk.Frame{configFrame()}, true, 0, false)
		second <- p
		return p, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d := &Daemon{}
	events := make(chan mobile.DeskEvent, 16)
	req := deskRequest()
	req.LongEdge = 800
	req.Resolution = 720
	ready, err := d.deskStart(ctx, req, func(e mobile.DeskEvent) { events <- e })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(initial, " "), "--long-edge 800 --resolution 720") {
		t.Fatalf("initial args %v", initial)
	}
	if err := ready.Resolution(context.Background(), 1080); err != nil {
		t.Fatal(err)
	}
	select {
	case args := <-restarting:
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--resolution 1080") || !strings.Contains(joined, "--display 7") {
			t.Fatalf("restart args %v", args)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no restart")
	}
	// 新 helper 还没装上，写旧管道失败必须报错，但最新选择不能丢。
	if err := ready.Resolution(context.Background(), 1440); err == nil {
		t.Error("restart-gap write failure was hidden")
	}
	close(release)
	var p *scriptDeskProc
	select {
	case p = <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("new helper missing")
	}
	var window, resolution bool
	timeout := time.After(3 * time.Second)
	for !window || !resolution {
		select {
		case c := <-p.controls:
			if c.Kind == desk.CtrlWindow && c.Window == 77 {
				window = true
			}
			if c.Kind == desk.CtrlResolution && c.Resolution == 1440 && c.Scale == 1 {
				resolution = true
			}
		case <-timeout:
			t.Fatalf("restored window=%v resolution=%v", window, resolution)
		}
	}
	cancel()
	if r := endReason(t, events); r != "unsubscribed" {
		t.Fatalf("end %s", r)
	}
	if err := ready.Resolution(context.Background(), 720); err == nil {
		t.Fatal("choice accepted after subscription ended")
	}
}
