package term

import (
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

// defaultColors 是深浅外观下终端的前景、背景色。
func defaultColors(dark bool) (fg, bg vt.RGB) {
	if dark {
		return hexRGB(DarkForeground), hexRGB(DarkBackground)
	}
	return hexRGB(LightForeground), hexRGB(LightBackground)
}

func hexRGB(s string) vt.RGB {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return vt.RGB{R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}
}
