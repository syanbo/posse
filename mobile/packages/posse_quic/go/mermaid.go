package main

/*
#include <stdlib.h>
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unicode"
	"unsafe"

	"posse/internal/mermaid"
)

// mermaidRequest 是手机要画的一张图。手机的字体只有 Dart 能量，Go 里量不了，也不能在 Pele 排版中途同步回调 Dart，
// 所以 Dart 先按 Base 字号量好可能用到的字（源码里的字和 ASCII）的宽度，按样式放在 Advances 里：
// "r" 正文、"b" 粗体、"i" 斜体、"bi" 粗斜体、"m" 等宽。没量到的字按全角一个字号、半角 0.6 个字号估。
type mermaidRequest struct {
	Source   string                        `json:"source"`
	Style    map[string]string             `json:"style"` // bg、fg、muted、line、surface、surfaceAlt、border、accent、danger，"#rrggbb"
	Key      string                        `json:"key"`   // 量法（字体、字号缩放）的标识
	Base     float32                       `json:"base"`
	Advances map[string]map[string]float32 `json:"advances"`
}

type mermaidLabel struct {
	Text  string  `json:"t"`
	Size  float32 `json:"s"`
	Style int     `json:"st"`
	X     float32 `json:"x"`
	Y     float32 `json:"y"` // 基线
	Angle float32 `json:"a"`
	Color string  `json:"c"` // "#rrggbbaa"
}

type mermaidResult struct {
	SVG    string         `json:"svg,omitempty"`
	W      float32        `json:"w,omitempty"`
	H      float32        `json:"h,omitempty"`
	Labels []mermaidLabel `json:"labels,omitempty"`
	Err    string         `json:"err,omitempty"`
}

// renderMermaid 画一张图，请求和结果都是 JSON。
func renderMermaid(req []byte) []byte {
	var r mermaidRequest
	var out mermaidResult
	if err := json.Unmarshal(req, &r); err != nil {
		out.Err = err.Error()
	} else if l, err := mermaid.Render(r.Source, r.style(), r.measurer()); err != nil {
		out.Err = err.Error()
	} else {
		out.SVG, out.W, out.H = string(l.Shapes), l.W, l.H
		for _, lb := range l.Labels {
			c := lb.Color
			out.Labels = append(out.Labels, mermaidLabel{Text: lb.Text, Size: lb.Size, Style: lb.Style, X: lb.X, Y: lb.Y,
				Angle: lb.Angle, Color: fmt.Sprintf("#%02x%02x%02x%02x", c.R, c.G, c.B, c.A)})
		}
	}
	b, _ := json.Marshal(out)
	return b
}

func (r mermaidRequest) style() mermaid.Style {
	c := func(name string) mermaid.Color {
		v, _ := mermaid.ParseHex(r.Style[name])
		return v
	}
	return mermaid.Style{
		Bg: c("bg"), Fg: c("fg"), Muted: c("muted"), Line: c("line"), Surface: c("surface"),
		SurfaceAlt: c("surfaceAlt"), Border: c("border"), Accent: c("accent"), Danger: c("danger"),
	}
}

func (r mermaidRequest) measurer() mermaid.Measurer {
	base := r.Base
	if base <= 0 {
		base = 16
	}
	return mermaid.Measurer{Key: r.Key, Width: func(text string, size float32, style int) float32 {
		name := "r"
		switch bold, italic := style&mermaid.Bold != 0, style&mermaid.Italic != 0; {
		case style&mermaid.Mono != 0:
			name = "m"
		case bold && italic:
			name = "bi"
		case bold:
			name = "b"
		case italic:
			name = "i"
		}
		table := r.Advances[name]
		if style&(mermaid.Sub|mermaid.Sup) != 0 {
			size *= mermaid.Smaller
		}
		w := float32(0)
		for _, ch := range text {
			if a, ok := table[string(ch)]; ok {
				w += a * size / base
			} else if wide(ch) {
				w += size
			} else {
				w += size * 0.6
			}
		}
		return w
	}}
}

func wide(ch rune) bool {
	return unicode.Is(unicode.Han, ch) || unicode.Is(unicode.Hiragana, ch) || unicode.Is(unicode.Katakana, ch) ||
		unicode.Is(unicode.Hangul, ch) || ch >= 0x3000 && ch <= 0x303F || ch >= 0xFF00 && ch <= 0xFF60
}

// hq_mermaid 画一张图（见 mermaidRequest）。同步执行，第一次约 100 ms（启动 JS 运行时），之后一张约 1 ms；
// Dart 在后台 isolate 里调。返回的 JSON 由 Go 用 malloc 分配，Dart 读完调 hq_free。
//
//export hq_mermaid
func hq_mermaid(req *C.char, n C.int32_t) *C.char {
	out := renderMermaid(C.GoBytes(unsafe.Pointer(req), C.int(n)))
	return C.CString(string(out))
}
