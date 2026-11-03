package term

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"posse/internal/vt"
)

// 终端的默认前景、背景色，窗口里的 Posse 主题（正文色、底色）也用这两组。
// 常驻进程的内核设上它们，没有界面接入时也能应答 OSC 10/11：
// Codex 只在启动时查一次背景色，查不到就把用户消息画成 ANSI 深灰底。
const (
	LightForeground, LightBackground = "#1F1E1D", "#FFFFFF"
	DarkForeground, DarkBackground   = "#ECEBE8", "#20201E"
)

// LightPalette、DarkPalette 是终端的 16 色（照 Visual Studio Code，和 mygo 终端插件的默认一样），
// 窗口的终端和常驻进程的内核都用它们。
var (
	LightPalette = [16]string{
		"#000000", "#cd3131", "#00bc00", "#949800", "#0451a5", "#bc05bc", "#0598bc", "#555555",
		"#666666", "#cd3131", "#14ce14", "#b5ba00", "#0451a5", "#bc05bc", "#0598bc", "#a5a5a5",
	}
	DarkPalette = [16]string{
		"#000000", "#cd3131", "#0dbc79", "#e5e510", "#2472c8", "#bc3fbc", "#11a8cd", "#e5e5e5",
		"#666666", "#f14c4c", "#23d18b", "#f5f543", "#3b8eea", "#d670d6", "#29b8db", "#ffffff",
	}
)

// Colors 是终端的默认配色：程序查到的前景色、底色和 16 色。窗口开着时是窗口终端在用的那套
// （窗口随轮询报给常驻进程），窗口关掉后内核照它应答；手机照它画终端，和程序按它配出来的颜色一致。
type Colors struct {
	Foreground, Background vt.RGB
	Palette                [16]vt.RGB
}

// DefaultColors 是深浅外观下 Posse 的终端配色。
func DefaultColors(dark bool) Colors {
	fg, bg, pal := LightForeground, LightBackground, LightPalette
	if dark {
		fg, bg, pal = DarkForeground, DarkBackground, DarkPalette
	}
	c := Colors{Foreground: hexRGB(fg), Background: hexRGB(bg)}
	for i, s := range pal {
		c.Palette[i] = hexRGB(s)
	}
	return c
}

// Dark 按底色的亮度判断深浅，算法和 mygo 终端插件一致。
func (c Colors) Dark() bool {
	b := c.Background
	return 299*int(b.R)+587*int(b.G)+114*int(b.B) < 128*1000
}

// palette256 是内核要的 256 色：16 色之后是 xterm 的 6×6×6 色块和 24 级灰。
func (c Colors) palette256() *[256]vt.RGB {
	var p [256]vt.RGB
	copy(p[:], c.Palette[:])
	levels := [6]uint8{0, 95, 135, 175, 215, 255}
	for i := range 216 {
		p[16+i] = vt.RGB{R: levels[i/36], G: levels[i/6%6], B: levels[i%6]}
	}
	for i := range 24 {
		v := uint8(8 + 10*i)
		p[232+i] = vt.RGB{R: v, G: v, B: v}
	}
	return &p
}

type colorsJSON struct {
	Foreground string     `json:"foreground"`
	Background string     `json:"background"`
	Palette    [16]string `json:"palette"`
}

// MarshalJSON 写成 #rrggbb：交接记录和手机都读这个格式。
func (c Colors) MarshalJSON() ([]byte, error) {
	j := colorsJSON{Foreground: rgbHex(c.Foreground), Background: rgbHex(c.Background)}
	for i, v := range c.Palette {
		j.Palette[i] = rgbHex(v)
	}
	return json.Marshal(j)
}

func (c *Colors) UnmarshalJSON(b []byte) error {
	var j colorsJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	c.Foreground, c.Background = hexRGB(j.Foreground), hexRGB(j.Background)
	for i, s := range j.Palette {
		c.Palette[i] = hexRGB(s)
	}
	return nil
}

var appearance struct {
	sync.Mutex
	dark bool
	at   time.Time
}

// systemDark 读 macOS 的外观设置：窗口的主题跟着它，常驻进程据此猜窗口会用哪一套颜色。
// 读一次约 10ms，一秒内复用，交接时接过一批终端不必每个都读。
func systemDark() bool {
	appearance.Lock()
	defer appearance.Unlock()
	if time.Since(appearance.at) > time.Second {
		out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
		appearance.dark = err == nil && strings.TrimSpace(string(out)) == "Dark"
		appearance.at = time.Now()
	}
	return appearance.dark
}

func hexRGB(s string) vt.RGB {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return vt.RGB{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}
}

func rgbHex(c vt.RGB) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }
