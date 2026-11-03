package app

import (
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
)

// 窗口终端的 16 色改从 term 取（常驻进程和手机照同一份），值要和 mygo 插件原来的默认一样
func TestTerminalPaletteUnchanged(t *testing.T) {
	light, dark := terminalThemes()
	if light.Palette != terminal.LightTheme().Palette || dark.Palette != terminal.DarkTheme().Palette {
		t.Fatalf("palette differs from mygo's:\n%v\n%v", light.Palette, dark.Palette)
	}
}

// 报给常驻进程的配色是窗口终端在用的前景、底色和 16 色；还没画过一帧时不报
func TestWindowColors(t *testing.T) {
	light, dark := terminalThemes()
	for _, th := range []*terminal.Theme{light, dark} {
		c := windowColors(th == dark)
		if c.Background.R != th.Background.R || c.Foreground.G != th.Foreground.G || c.Palette[1].B != th.Palette[1].B {
			t.Fatalf("windowColors(%v) = %+v", th == dark, c)
		}
	}
	a := &app{}
	if a.statusParams() != nil {
		t.Fatal("reported colors before the first frame")
	}
	a.shade.Store(2)
	if p, _ := a.statusParams().(map[string]any); p["colors"] != windowColors(true) {
		t.Fatalf("statusParams = %v", p)
	}
}
