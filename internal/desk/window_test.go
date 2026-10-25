package desk

import (
	"reflect"
	"testing"
)

func TestPickWindowsKeepsOrderAndFilters(t *testing.T) {
	displays := []Display{
		{ID: 1, X: 0, Y: 0, Width: 1512, Height: 982, PixelWidth: 3024},
		{ID: 2, X: 1512, Y: 0, Width: 1920, Height: 1080, PixelWidth: 1920},
	}
	raw := []rawWindow{
		{ID: 10, PID: 100, Alpha: 1, App: "Safari", Title: "Posse", X: 100, Y: 100, W: 800, H: 600},
		{ID: 11, PID: 101, Layer: 25, Alpha: 1, App: "Control Center", W: 400, H: 300}, // 不是普通层
		{ID: 12, PID: 102, Alpha: 0, App: "Ghost", W: 400, H: 300},                     // 透明
		{ID: 13, PID: 103, Alpha: 1, App: "Tool", W: 100, H: 300},                      // 太窄
		{ID: 14, PID: 7, Alpha: 1, App: "Posse", W: 400, H: 300},                       // 自己
		{ID: 15, PID: 104, Alpha: 1, App: "Simulator", Bundle: "com.apple.iphonesimulator", W: 400, H: 300},
		{ID: 16, PID: 105, Alpha: 1, App: "Terminal", X: 2000, Y: 200, W: 600, H: 400},
		{ID: 18, PID: 107, Alpha: 1, App: "\x00\x00\x00", Title: "\x00\x00", W: 1512, H: 865}, // 名字全是 NUL
		{ID: 17, PID: 106, Alpha: 1, App: "Notes", X: -900, Y: 100, W: 600, H: 400},           // 在所有屏外面，算最近的
	}
	got := pickWindows(raw, 7, []string{"com.apple.iphonesimulator"}, displays)
	want := []Window{
		{ID: 10, App: "Safari", Title: "Posse", Width: 800, Height: 600, Display: 1},
		{ID: 16, App: "Terminal", Width: 600, Height: 400, Display: 2},
		{ID: 17, App: "Notes", Width: 600, Height: 400, Display: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pickWindows = %+v\nwant %+v", got, want)
	}
}

func TestWindowPixelsFollowTheDisplayScale(t *testing.T) {
	retina := Display{Width: 1512, PixelWidth: 3024}
	if w, h := windowPixels(800, 600, retina); w != 1600 || h != 1200 {
		t.Fatalf("retina %dx%d", w, h)
	}
	plain := Display{Width: 1920, PixelWidth: 1920}
	if w, h := windowPixels(801, 601, plain); w != 801 || h != 601 {
		t.Fatalf("plain %dx%d", w, h)
	}
	if w, h := windowPixels(800, 600, Display{}); w != 800 || h != 600 {
		t.Fatalf("unknown display %dx%d", w, h)
	}
}
