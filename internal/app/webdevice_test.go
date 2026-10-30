package app

import (
	"context"
	"math"
	"net"
	"path/filepath"
	"testing"

	"posse/internal/rpc"
)

func TestDeviceZoom(t *testing.T) {
	cases := []struct {
		w, h, aw, ah float32
		want         float64
	}{
		{393, 852, 1000, 1000, 1},   // 放得下不放大
		{393, 852, 1000, 426, 0.5},  // 矮窗口按高度缩
		{768, 1024, 384, 2000, 0.5}, // 窄窗口按宽度缩
		{393, 852, 1000, 700, 0.82}, // 取到 1%，往下取
		{393, 852, 0, 0, 1},         // 第一帧还不知道可用空间
	}
	for _, c := range cases {
		if got := deviceZoom(c.w, c.h, c.aw, c.ah); got != c.want {
			t.Errorf("deviceZoom(%v×%v in %v×%v) = %v, want %v", c.w, c.h, c.aw, c.ah, got, c.want)
		}
	}
	// 缩放后视图取整数像素，页面宽度（视图宽度 ÷ 缩放）正好是机型宽度
	for _, d := range webDevices {
		for _, z := range []float64{1, 0.99, 0.87, 0.5, 0.33} {
			vw, _, zoom := deviceView(d.w, d.h, z)
			if vw != float32(math.Round(float64(vw))) || math.Abs(float64(vw)/zoom-float64(d.w)) > 1e-9 {
				t.Errorf("%s at %v: view %v zoom %v gives %v css px", d.id, z, vw, zoom, float64(vw)/zoom)
			}
		}
	}
	if w, h := webDevices[0].size(true); w != 852 || h != 393 {
		t.Fatalf("landscape size = %v×%v", w, h)
	}
}

// 没有页面（离屏）时只改状态：开、换机型记住上次的、⇧⌘M 开关、关掉时横屏也清掉。
func TestSetWebDevice(t *testing.T) {
	a := &app{}
	wd := &webDoc{}
	a.toggleWebDevice(wd)
	if wd.device != "iphone" {
		t.Fatalf("toggle on = %q, want the first device", wd.device)
	}
	a.setWebDevice(wd, "pixel", true)
	if wd.device != "pixel" || !wd.landscape || a.state.WebDevice != "pixel" {
		t.Fatalf("set pixel: %+v, remembered %q", wd, a.state.WebDevice)
	}
	a.toggleWebDevice(wd)
	if wd.device != "" || wd.landscape {
		t.Fatalf("toggle off: %+v", wd)
	}
	// 再开用上次选的机型
	a.toggleWebDevice(wd)
	if wd.device != "pixel" {
		t.Fatalf("toggle on again = %q, want pixel", wd.device)
	}
	// 不认识的机型不动
	a.setWebDevice(wd, "nokia", false)
	if wd.device != "pixel" {
		t.Fatalf("unknown device changed it to %q", wd.device)
	}
}

func TestWebDeviceRequest(t *testing.T) {
	path := filepath.Join(shortDir(t), "app.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	a := &app{mainUpdate: func(fn func()) { fn() }}
	a.workspaces = []workspace{{ID: "w1", Label: "posse"}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.ctlServer().Serve(ctx, ln)
	if err := rpc.Call(ctx, path, "web.device", map[string]any{"device": "nokia", "workspace": "w1"}, nil); rpcCode(err) != rpc.CodeBadRequest {
		t.Fatalf("unknown device = %v, want bad_request", err)
	}
	if err := rpc.Call(ctx, path, "web.device", map[string]any{"device": "iphone", "workspace": "w1"}, nil); rpcCode(err) != rpc.CodeNotFound {
		t.Fatalf("no web tab = %v, want not_found", err)
	}
}
