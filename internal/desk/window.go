package desk

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// rawWindow 是 CGWindowListCopyWindowInfo 的一项。位置和大小是全局坐标里的点。
type rawWindow struct {
	ID         uint32
	PID        int32
	Layer      int
	Alpha      float64
	OnScreen   bool
	App        string
	Title      string
	Bundle     string
	X, Y, W, H float64
}

// 太小的多半是工具条、浮层，看了也没用
const (
	windowMinW = 120
	windowMinH = 80
)

// pickWindows 从系统的窗口列表（最前的在前面）里挑出能单独看的：普通层、看得见、不太小、
// 不是自己（采集子进程）也不是被排除的 App。Display 填窗口中心所在的屏，都不在时填离得最近的。
func pickWindows(raw []rawWindow, self int32, exclude []string, displays []Display) []Window {
	var out []Window
	for _, w := range raw {
		w.App, w.Title = cleanName(w.App), cleanName(w.Title)
		if w.Layer != 0 || w.Alpha <= 0 || w.W < windowMinW || w.H < windowMinH || w.App == "" {
			continue
		}
		if w.PID == self || (w.Bundle != "" && slices.Contains(exclude, w.Bundle)) {
			continue
		}
		out = append(out, Window{
			ID: w.ID, App: w.App, Title: w.Title, Width: w.W, Height: w.H,
			Display: displayAt(displays, w.X+w.W/2, w.Y+w.H/2), Icon: iconKey(w),
		})
	}
	return out
}

// iconKey 是窗口所属 App 的图标键：有包 ID 用包 ID，没有用进程号。
func iconKey(w rawWindow) string {
	if w.Bundle != "" {
		return w.Bundle
	}
	return "pid:" + strconv.Itoa(int(w.PID))
}

// windowIconPIDs 是 list 里各个图标键对应的进程，取图标用。
func windowIconPIDs(raw []rawWindow, list []Window) map[string]int32 {
	want := map[string]bool{}
	for _, w := range list {
		want[w.Icon] = true
	}
	pids := map[string]int32{}
	for _, w := range raw {
		if k := iconKey(w); want[k] && pids[k] == 0 {
			pids[k] = w.PID
		}
	}
	return pids
}

// displayAt 是点 (x, y) 所在的屏；不在任何一块屏上时是离得最近的那块。
func displayAt(displays []Display, x, y float64) uint32 {
	var best uint32
	bestDist := -1.0
	for _, d := range displays {
		dx := max(d.X-x, 0, x-(d.X+d.Width))
		dy := max(d.Y-y, 0, y-(d.Y+d.Height))
		dist := dx*dx + dy*dy
		if bestDist < 0 || dist < bestDist {
			best, bestDist = d.ID, dist
		}
	}
	return best
}

// windowPixels 是窗口在它所在的屏上占的像素：点数乘这块屏的缩放（像素宽除以点宽）。
func windowPixels(w, h float64, d Display) (int, int) {
	scale := 1.0
	if d.Width > 0 && d.PixelWidth > 0 {
		scale = float64(d.PixelWidth) / d.Width
	}
	return int(w*scale + 0.5), int(h*scale + 0.5)
}

// cleanName 去掉名字里的控制字符和首尾空白：有的系统窗口的名字是一串 NUL。
func cleanName(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}
