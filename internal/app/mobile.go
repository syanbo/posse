package app

import (
	"context"
	"image"
	"image/color"
	"time"

	"github.com/egoist/mygo/ui"
	"rsc.io/qr"
)

// mobileView 是"连接手机"对话框：Orca 手机 App 扫码配对，下面列出配对过的手机。
type mobileView struct {
	open     bool
	url      string
	code     string
	endpoint string
	qr       *ui.Bitmap
	devices  []mobileDevice
	loading  bool
	err      string
}

type mobileDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen"`
}

func (a *app) openMobile() {
	a.mobile = mobileView{open: true, loading: true}
	go a.loadMobile(false)
}

// loadMobile 取配对码并画成二维码；rotate 时换一个新码，旧码作废。
func (a *app) loadMobile(rotate bool) {
	var info struct {
		URL      string `json:"url"`
		Code     string `json:"code"`
		Endpoint string `json:"endpoint"`
	}
	var devices []mobileDevice
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 出错显示在对话框里，不走错误条
	err := a.c.Call(ctx, "mobile.pair", map[string]any{"rotate": rotate}, &info)
	if err == nil {
		err = a.c.Call(ctx, "mobile.devices", nil, &devices)
	}
	var img *image.RGBA
	if err == nil {
		img, err = qrImage(info.URL)
	}
	a.win.Update(func() {
		m := &a.mobile
		m.loading = false
		if err != nil {
			m.err = err.Error()
			return
		}
		m.err, m.url, m.code, m.endpoint, m.devices = "", info.URL, info.Code, info.Endpoint, devices
		m.qr = ui.NewBitmap(img)
	})
}

// qrImage 把内容画成黑白二维码位图，四周留白；每个模块 8 像素，放大显示也清楚。
func qrImage(text string) (*image.RGBA, error) {
	code, err := qr.Encode(text, qr.L)
	if err != nil {
		return nil, err
	}
	const scale, quiet = 8, 3
	n := (code.Size + 2*quiet) * scale
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			mx, my := x/scale-quiet, y/scale-quiet
			c := color.RGBA{255, 255, 255, 255}
			if mx >= 0 && my >= 0 && mx < code.Size && my < code.Size && code.Black(mx, my) {
				c = color.RGBA{0, 0, 0, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	return img, nil
}

func (a *app) removeDevice(d mobileDevice) {
	a.confirm("移除「"+d.Name+"」？", "这台手机会立刻断开，要再用得重新扫码配对。", "移除", func() {
		if a.call("mobile.rm", map[string]any{"device": d.ID}, nil, 10*time.Second) != nil {
			return
		}
		var devices []mobileDevice
		a.call("mobile.devices", nil, &devices, 10*time.Second)
		a.win.Update(func() { a.mobile.devices = devices })
	})
}

func (a *app) mobileDialog(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	ui.Modal(c, &m.open, func() {
		ui.Column(c).Width(400).Gap(12).Children(func() {
			titleText(c, "连接手机", fsCardTitle)
			ui.Text(c, "用 Orca 手机 App 扫码（Pair Desktop → Continue）。手机要和这台电脑在同一个 Wi-Fi 或 Tailscale 里，之后在手机上就能看终端、打字和新开 agent。").
				FontSize(12.5).TextColor(t.TextMuted).LineHeight(1.5)
			ui.Row(c).Justify(ui.Center).Children(func() {
				box := ui.Box(c).Size(232, 232).Radius(12).Background(ui.RGBA(255, 255, 255, 1)).Center()
				box.Children(func() {
					switch {
					case m.qr != nil:
						ui.Image(c, m.qr).Size(220, 220)
					case m.loading:
						ui.Text(c, "正在生成…").FontSize(12).TextColor(ui.RGBA(0, 0, 0, 0.5))
					}
				})
			})
			if m.err != "" {
				ui.Text(c, m.err).FontSize(12).TextColor(t.Danger).LineHeight(1.45)
			}
			if m.endpoint != "" {
				ui.Text(c, "手机将连接 "+m.endpoint).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).Center()
				ui.Row(c).Gap(8).Justify(ui.Center).Children(func() {
					if pressable(textButton(c, "复制配对码")).Tooltip("扫不了码时，在 App 的配对页粘贴").Clicked() {
						c.WriteClipboard(m.code)
					}
					if pressable(textButton(c, "换一个码")).Tooltip("作废还没用过的旧码").Clicked() {
						m.loading, m.qr = true, nil
						go a.loadMobile(true)
					}
				})
			}
			if len(m.devices) > 0 {
				ui.Box(c).Height(1).Background(colors(t).line)
				ui.Text(c, "已配对").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
				for _, d := range m.devices {
					ui.Row(c.Key(d.ID)).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, d.Name).FontSize(13).Grow(1).Shrink(1).SingleLine()
						ui.Text(c, "上次连接 "+ago(d.LastSeen)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
						if pressable(textButton(c, "移除")).Clicked() {
							a.removeDevice(d)
						}
					})
				}
			}
			ui.Row(c).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
				if pressable(accentButton(c, "完成")).Clicked() {
					m.open = false
				}
			})
		})
	})
	// 关掉后释放二维码位图
	if !m.open && m.qr != nil {
		m.qr = nil
	}
}
