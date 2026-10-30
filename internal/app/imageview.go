package app

import (
	"fmt"
	"image"
	"image/color"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

// imageInfo 是文件标签里图片的原始信息，顶栏显示。
type imageInfo struct {
	w, h   int
	format string
	bytes  int64
	alpha  bool // 有透明像素：铺棋盘格才看得出
}

// imageView 是图片预览的缩放和位置。zoom 按原图算：1 是原图一个像素对屏幕一个物理像素，0 是适应窗口。
// cx、cy 是视口中心对着的位图像素，适应窗口时不用。vw、vh、dpr 记着上一次画的视口，输入按它换算。
type imageView struct {
	zoom         float32
	cx, cy       float32
	vw, vh, dpr  float32
	drag         bool
	lastX, lastY float32
	checkerW     int // checker 是按视口大小做的棋盘格位图，一格一个像素，拉伸成 checkerTile 见方的格子
	checkerH     int
	checkerDark  bool
	checker      *ui.Bitmap
}

const (
	imageMaxPixels = 24 << 20 // 再大的图等比缩到这么多像素：一张图最多占 96 MB
	imagePad       = 16       // 适应窗口时四周留的空
	checkerTile    = 8
	zoomMin        = 0.02
	zoomMax        = 16
)

// imageScale 是位图一个像素画成多少 DIP；k 是原图像素和位图像素之比（太大的图解码时缩过）。
func (d *fileDoc) imageScale(z float32) float32 {
	bw, _ := d.img.Size()
	k := float32(d.imgInfo.w) / float32(bw)
	return z * k / max(d.iv.dpr, 1)
}

// fitZoom 是适应窗口的缩放：整张图放得下，比窗口小的图不放大。
func (d *fileDoc) fitZoom() float32 {
	bw, bh := d.img.Size()
	v := &d.iv
	if v.vw <= 0 || v.vh <= 0 {
		return 1
	}
	s1 := d.imageScale(1)
	s := min((v.vw-2*imagePad)/float32(bw), (v.vh-2*imagePad)/float32(bh))
	return max(min(s/s1, 1), zoomMin)
}

// curZoom 是现在实际的缩放。
func (d *fileDoc) curZoom() float32 {
	if d.iv.zoom == 0 {
		return d.fitZoom()
	}
	return d.iv.zoom
}

// setZoom 改缩放，视口里 (mx, my) 处的那一点留在原处；z 为 0 回到适应窗口。
func (d *fileDoc) setZoom(z, mx, my float32) {
	v := &d.iv
	if z == 0 {
		v.zoom = 0
		return
	}
	bw, bh := d.img.Size()
	old := d.imageScale(d.curZoom())
	if v.zoom == 0 {
		v.cx, v.cy = float32(bw)/2, float32(bh)/2
	}
	ix := v.cx + (mx-v.vw/2)/old
	iy := v.cy + (my-v.vh/2)/old
	v.zoom = min(max(z, zoomMin), zoomMax)
	s := d.imageScale(v.zoom)
	v.cx, v.cy = ix-(mx-v.vw/2)/s, iy-(my-v.vh/2)/s
	d.clampImage()
}

// clampImage 让图不被拖出视口：比视口小的方向居中，大的方向边缘不进到视口里面。
func (d *fileDoc) clampImage() {
	v := &d.iv
	bw, bh := d.img.Size()
	s := d.imageScale(d.curZoom())
	clamp := func(c, size, view float32) float32 {
		if size*s <= view {
			return size / 2
		}
		half := view / 2 / s
		return min(max(c, half), size-half)
	}
	v.cx = clamp(v.cx, float32(bw), v.vw)
	v.cy = clamp(v.cy, float32(bh), v.vh)
}

// pannable 报告图比视口大、可以拖动。
func (d *fileDoc) pannable() bool {
	bw, bh := d.img.Size()
	s := d.imageScale(d.curZoom())
	return float32(bw)*s > d.iv.vw+0.5 || float32(bh)*s > d.iv.vh+0.5
}

// zoomStep 是 ⌘= ⌘- 和菜单的放大缩小，以视口中心为准。
func (d *fileDoc) zoomStep(in bool) {
	f := float32(1.25)
	if !in {
		f = 1 / f
	}
	d.setZoom(d.curZoom()*f, d.iv.vw/2, d.iv.vh/2)
}

func (a *app) imageDocView(c *ui.Context, d *fileDoc) {
	v := &d.iv
	if v.vw == 0 {
		// 第一帧还不知道视口多大，顶栏的缩放比例要等画过一次再算
		c.AnimationFrame()
	}
	box := ui.Box(c.Key("img:" + d.path)).Grow(1).ClipX().ClipY()
	switch {
	case v.drag:
		box.Cursor(ui.CursorGrabbing)
	case d.pannable():
		box.Cursor(ui.CursorGrab)
	default:
		box.Cursor(ui.CursorDefault)
	}
	box.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputScroll:
			if ev.Mods&ui.Cmd != 0 {
				// 触控板一次滑动来很多小步，鼠标滚轮一格 40：按滑过的距离连续缩放
				d.setZoom(d.curZoom()*float32(math.Exp(float64(-ev.DY)*0.005)), ev.X, ev.Y)
				return true
			}
			if !d.pannable() {
				return false
			}
			if v.zoom == 0 {
				d.setZoom(d.curZoom(), v.vw/2, v.vh/2)
			}
			s := d.imageScale(v.zoom)
			v.cx += ev.DX / s
			v.cy += ev.DY / s
			d.clampImage()
			return true
		case ui.InputPointerDown:
			if ev.Button != 0 {
				return false
			}
			if ev.Clicks == 2 {
				fit := d.fitZoom()
				switch {
				case v.zoom == 0 && fit < 0.999:
					d.setZoom(1, ev.X, ev.Y)
				case v.zoom == 0:
					d.setZoom(2, ev.X, ev.Y)
				default:
					d.setZoom(0, 0, 0)
				}
				return true
			}
			if d.pannable() {
				if v.zoom == 0 {
					d.setZoom(d.curZoom(), v.vw/2, v.vh/2)
				}
				v.drag, v.lastX, v.lastY = true, ev.X, ev.Y
			}
			return true
		case ui.InputPointerMove:
			if !v.drag {
				return false
			}
			s := d.imageScale(d.curZoom())
			v.cx -= (ev.X - v.lastX) / s
			v.cy -= (ev.Y - v.lastY) / s
			v.lastX, v.lastY = ev.X, ev.Y
			d.clampImage()
			return true
		case ui.InputPointerUp:
			if !v.drag {
				return false
			}
			v.drag = false
			return true
		}
		return false
	})
	checker := d.imgInfo.alpha && !a.state.ImageCheckerOff
	dark := c.Theme().Dark
	box.Draw(func(p *ui.Painter, r ui.Rect) {
		resized := r.W != v.vw || r.H != v.vh
		v.vw, v.vh, v.dpr = r.W, r.H, p.Scale()
		if resized && v.zoom != 0 {
			d.clampImage()
		}
		bw, bh := d.img.Size()
		s := d.imageScale(d.curZoom())
		cx, cy := float32(bw)/2, float32(bh)/2
		if v.zoom != 0 {
			cx, cy = v.cx, v.cy
		}
		dst := ui.Rect{X: r.X + r.W/2 - cx*s, Y: r.Y + r.H/2 - cy*s, W: float32(bw) * s, H: float32(bh) * s}
		// Draw 画的东西不受元素的 ClipX、ClipY 约束，放大后要自己裁在视口里
		p.Clip(r, 0, func() {
			if checker {
				cols, rows := int(r.W/checkerTile)+1, int(r.H/checkerTile)+1
				if v.checker == nil || cols != v.checkerW || rows != v.checkerH || dark != v.checkerDark {
					v.checker = checkerBitmap(cols, rows, dark)
					v.checkerW, v.checkerH, v.checkerDark = cols, rows, dark
				}
				p.Clip(intersect(dst, r), 0, func() {
					p.ImageRegion(v.checker, ui.Rect{}, ui.Rect{X: r.X, Y: r.Y, W: float32(cols * checkerTile), H: float32(rows * checkerTile)}, ui.ImageOptions{})
				})
			}
			p.ImageRegion(d.img, ui.Rect{}, dst, ui.ImageOptions{})
		})
	})
}

func intersect(a, b ui.Rect) ui.Rect {
	x0, y0 := max(a.X, b.X), max(a.Y, b.Y)
	x1, y1 := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	return ui.Rect{X: x0, Y: y0, W: max(x1-x0, 0), H: max(y1-y0, 0)}
}

// checkerBitmap 做 cols×rows 个像素的棋盘格，每个像素是一格，画的时候按像素块拉大。
func checkerBitmap(cols, rows int, dark bool) *ui.Bitmap {
	a, b := color.RGBA{0xFF, 0xFF, 0xFF, 0xFF}, color.RGBA{0xE4, 0xE4, 0xE2, 0xFF}
	if dark {
		a, b = color.RGBA{0x2E, 0x2E, 0x2B, 0xFF}, color.RGBA{0x3A, 0x3A, 0x37, 0xFF}
	}
	img := image.NewRGBA(image.Rect(0, 0, cols, rows))
	for y := range rows {
		for x := range cols {
			if (x+y)%2 == 0 {
				img.SetRGBA(x, y, a)
			} else {
				img.SetRGBA(x, y, b)
			}
		}
	}
	return ui.NewBitmap(img).Pixelated()
}

// imageBar 是图片在文档顶栏右边的部分：尺寸、格式、大小，棋盘格开关，缩放比例（点开选适应窗口、100%、200%）。
func (a *app) imageBar(c *ui.Context, d *fileDoc) {
	t := c.Theme()
	info := d.imgInfo
	ui.Text(c, fmt.Sprintf("%d × %d  %s  %s", info.w, info.h, strings.ToUpper(info.format), byteSize(int(info.bytes)))).
		FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(0)
	if info.alpha {
		on := !a.state.ImageCheckerOff
		if toggleIconButton(c, "img-checker", iconChecker, "透明处显示棋盘格", on) {
			a.state.ImageCheckerOff = on
			a.saveState()
		}
	}
	z := ui.Box(c.Key("img-zoom")).Height(sz(22)).MinWidth(48).Padding(0, 8).Radius(rSmall).Center().Shrink(0).
		Role(ui.RoleButton).Label("缩放").Tooltip("缩放：⌘ 加滚动、" + a.keyText("zoomIn") + " " + a.keyText("zoomOut") + "，双击切换适应窗口和原大")
	bg := ui.Transparent
	if z.Hovered() {
		bg = t.SurfaceHover
	}
	z.Background(z.AnimateColor("bg", bg, hover))
	z.Children(func() {
		ui.Text(c, fmt.Sprintf("%d%%", int(math.Round(float64(d.curZoom()*100))))).FontSize(fsSmall).TextColor(t.TextMuted).
			FontFeatures("tnum")
	})
	menuButton(c, z, func(m *popMenu) {
		if m.Item("适应窗口").Keys(a.keyText("zoomReset")).Checked(d.iv.zoom == 0).Chosen() {
			d.setZoom(0, 0, 0)
		}
		for _, p := range []float32{1, 2} {
			if m.Item(fmt.Sprintf("%d%%", int(p*100))).Checked(d.iv.zoom == p).Chosen() {
				d.setZoom(p, d.iv.vw/2, d.iv.vh/2)
			}
		}
		m.Separator()
		if m.Item("放大").Keys(a.keyText("zoomIn")).Chosen() {
			d.zoomStep(true)
		}
		if m.Item("缩小").Keys(a.keyText("zoomOut")).Chosen() {
			d.zoomStep(false)
		}
	})
}

// frontImage 返回前台的图片文档：工作区选中文件标签、当前文件是图片；没有就是 nil。
func (a *app) frontImage(w *workspace) *fileDoc {
	if !a.filesActive(w) {
		return nil
	}
	if d := a.filesFor(w).cur; d != nil && d.kind == docImage && d.diff == nil && d.comp == nil {
		return d
	}
	return nil
}
