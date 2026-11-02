package app

import (
	"fmt"
	"math"

	"github.com/egoist/mygo/ui"

	"posse/locales"
)

// 网页标签的手机预览：Mac 上的 WebKit 没有设备模拟，也不认 <meta viewport>，排版宽度就是视图宽度。
// 所以把网页放进一块手机尺寸的视图里居中，换上手机浏览器的 UA；窗口放不下时连视图带页面一起缩小
// （页面缩放让 CSS 宽度保持机型的宽度），排版不变。触摸事件、安全区、设备像素比模拟不了。

// webDevice 是手机预览模拟的机型，尺寸是竖屏的 CSS 像素。
type webDevice struct {
	id   string
	name string
	w, h float32
	ua   string
}

const (
	uaIPhone  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	uaIPad    = "Mozilla/5.0 (iPad; CPU OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"
	uaAndroid = "Mozilla/5.0 (Linux; Android 15; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36"
)

var webDevices = []webDevice{
	{"iphone", "iPhone 15/16", 393, 852, uaIPhone},
	{"se", "iPhone SE", 375, 667, uaIPhone},
	{"pixel", "Pixel 8", 412, 915, uaAndroid},
	{"ipad", "iPad mini", 768, 1024, uaIPad},
}

func deviceByID(id string) (webDevice, bool) {
	for _, d := range webDevices {
		if d.id == id {
			return d, true
		}
	}
	return webDevice{}, false
}

// size 是这台机型按 landscape 摆放时的宽高。
func (d webDevice) size(landscape bool) (float32, float32) {
	if landscape {
		return d.h, d.w
	}
	return d.w, d.h
}

// deviceZoom 是机型在 availW×availH 里放得下的缩放，不放大；还不知道可用空间时是 1。
// 缩放取到 1% 免得窗口每变一点就重设一次页面缩放、页面跟着重排。
func deviceZoom(w, h, availW, availH float32) float64 {
	if availW <= 0 || availH <= 0 {
		return 1
	}
	z := min(1, availW/w, availH/h)
	return math.Floor(float64(z)*100) / 100
}

// setWebDevice 开关手机预览、换机型或横竖屏：换 UA 后重新载入一次，按 UA 判断设备的网站也认成手机。
// id 为空是关掉，回到电脑的 UA 和原来的大小。
func (a *app) setWebDevice(wd *webDoc, id string, landscape bool) {
	if id != "" {
		if _, ok := deviceByID(id); !ok {
			return
		}
	}
	uaChanged := devUA(wd.device) != devUA(id)
	wd.device, wd.landscape = id, landscape && id != ""
	if id != "" && a.state.WebDevice != id {
		a.state.WebDevice = id
		a.saveState()
	}
	if wd.view == nil || wd.view.IsDestroyed() {
		return
	}
	p := wd.view.Page()
	if id == "" && wd.zoom != 0 && wd.zoom != 1 {
		p.SetZoomFactor(1)
	}
	if id == "" {
		wd.zoom = 0
	}
	if uaChanged {
		p.SetUserAgent(devUA(id))
		a.reloadWeb(wd)
	}
}

// devUA 是机型的 UA，关掉手机预览时为空（用回 WebKit 默认的）。
func devUA(id string) string {
	d, _ := deviceByID(id)
	return d.ua
}

// toggleWebDevice 是 ⇧⌘M：开着就关，关着就用上次选的机型打开。
func (a *app) toggleWebDevice(wd *webDoc) {
	if wd.device != "" {
		a.setWebDevice(wd, "", false)
		return
	}
	id := a.state.WebDevice
	if _, ok := deviceByID(id); !ok {
		id = webDevices[0].id
	}
	a.setWebDevice(wd, id, false)
}

// deviceMenu 是工具栏上手机按钮的菜单：电脑、各机型和横屏。
func (a *app) deviceMenu(msg locales.Messages, m *popMenu, wd *webDoc) {
	if m.Item(msg.WebDesktop()).Checked(wd.device == "").Chosen() {
		a.setWebDevice(wd, "", false)
	}
	for _, d := range webDevices {
		label := fmt.Sprintf("%s  %.0f×%.0f", d.name, d.w, d.h)
		if m.Item(label).Checked(wd.device == d.id).Chosen() {
			a.setWebDevice(wd, d.id, wd.landscape)
		}
	}
	m.Separator()
	if m.Item(msg.WebLandscape()).Checked(wd.landscape).Disabled(wd.device == "").Chosen() {
		a.setWebDevice(wd, wd.device, !wd.landscape)
	}
}

// deviceStage 画手机预览：一块机型大小的网页居中放在底色上，上面一行写机型、尺寸和缩放。
func (a *app) deviceStage(c *ui.Context, d *fileDoc) {
	wd := d.web
	t := c.Theme()
	dev, ok := deviceByID(wd.device)
	if !ok {
		return
	}
	w, h := dev.size(wd.landscape)
	const pad, caption = 16, 28
	stage := ui.Column(c.Key("web-stage")).Grow(1).AlignItems(ui.Center).Padding(pad).Gap(8).
		Background(colors(t).band).BorderWidth(hairline, 0, 0, 0).BorderColor(colors(t).line)
	b := stage.Bounds()
	vw, vh, zoom := deviceView(w, h, deviceZoom(w, h, b.W-2*pad, b.H-2*pad-caption))
	if zoom != wd.zoom {
		wd.zoom = zoom
		wd.view.Page().SetZoomFactor(zoom)
	}
	stage.Children(func() {
		info := fmt.Sprintf("%s · %.0f×%.0f", dev.name, w, h)
		if zoom < 1 {
			info += fmt.Sprintf(" · %.0f%%", zoom*100)
		}
		ui.Text(c, info).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		// 边框画在外面一层：画在网页视图上会占掉里面的宽度，页面就比机型窄两三个像素
		ui.Box(c).Shrink(0).Padding(hairline).Background(colors(t).line).Children(func() {
			ui.WebView(c.Key("web:"+d.path), wd.view).Width(vw).Height(vh)
		})
	})
}

// currentWebDoc 是选中的工作区当前显示的网页标签，没有时为 nil。View 菜单的网页命令作用于它。
func (a *app) currentWebDoc() *webDoc {
	w := a.workspace(a.selected)
	if w == nil || !a.filesActive(w) {
		return nil
	}
	v := a.files[w.ID]
	if v == nil || v.cur == nil || v.cur.web == nil || v.cur.web.view == nil || v.cur.web.view.IsDestroyed() {
		return nil
	}
	return v.cur.web
}

// menuWeb 是 View 菜单的网页命令：当前不是网页标签时什么也不做。
func (a *app) menuWeb(action string) {
	wd := a.currentWebDoc()
	if wd == nil {
		return
	}
	switch action {
	case "reload":
		a.reloadWeb(wd)
	case "hardReload":
		if wd.failed != "" {
			a.reloadWeb(wd)
			return
		}
		wd.view.Page().ReloadIgnoringCache()
	case "mobile":
		a.toggleWebDevice(wd)
	}
}

// deviceView 是缩放 z 时网页视图的整像素宽高，和设给页面的缩放：视图宽度要取整，
// 缩放跟着改成"整数宽度 ÷ 机型宽度"，页面的 CSS 宽度才正好是机型的宽度，不多不少 1 像素。
func deviceView(w, h float32, z float64) (vw, vh float32, zoom float64) {
	vw = float32(math.Round(float64(w) * z))
	zoom = float64(vw) / float64(w)
	vh = float32(math.Round(float64(h) * zoom))
	return vw, vh, zoom
}
