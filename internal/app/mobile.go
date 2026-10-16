package app

import (
	"context"
	"image"
	"image/color"
	"slices"
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
	custom   string // 正在输入的自定义地址
}

// mobileAuto 是地址下拉里"自动"一项的文字。
const mobileAuto = "自动（Tailscale 或局域网）"

type mobileDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen"`
}

func (a *app) openMobile() {
	a.mobile = mobileView{open: true, loading: true}
	go a.loadMobile(false, a.state.MobileAddress)
}

// loadMobile 按 addr 取配对码并画成二维码；rotate 时换一个新码，旧码作废。
// 成功后 addr 才成为选中的地址，自定义的记进 ui.json：填错的地址不留下。
func (a *app) loadMobile(rotate bool, addr string) {
	var info struct {
		URL      string `json:"url"`
		Code     string `json:"code"`
		Endpoint string `json:"endpoint"`
	}
	var devices []mobileDevice
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 出错显示在对话框里，不走错误条
	err := a.c.Call(ctx, "mobile.pair", map[string]any{"rotate": rotate, "address": addr}, &info)
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
		if addr == m.custom {
			m.custom = ""
		}
		a.setMobileAddress(addr)
	})
}

func (a *app) setMobileAddress(addr string) {
	st := &a.state
	if addr == st.MobileAddress && (addr == "" || slices.Contains(st.MobileAddresses, addr)) {
		return
	}
	st.MobileAddress = addr
	if addr != "" && !slices.Contains(st.MobileAddresses, addr) {
		st.MobileAddresses = append(st.MobileAddresses, addr)
	}
	a.saveState()
}

// useMobileAddress 换配对码里的地址。地址不对时报错，二维码和下拉都还是原来的。
func (a *app) useMobileAddress(addr string) {
	m := &a.mobile
	m.loading, m.err = true, ""
	go a.loadMobile(false, addr)
}

func (a *app) removeMobileAddress(addr string) {
	st := &a.state
	st.MobileAddresses = slices.DeleteFunc(st.MobileAddresses, func(s string) bool { return s == addr })
	if st.MobileAddress == addr {
		st.MobileAddress = ""
		a.useMobileAddress("")
	}
	a.saveState()
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
			ui.Text(c, "用 Orca 手机 App 扫码（Pair Desktop → Continue），之后在手机上就能看终端、打字和新开 agent。").
				FontSize(12.5).TextColor(t.TextMuted).LineHeight(1.5)
			a.mobileAddressPicker(c)
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
						go a.loadMobile(true, a.state.MobileAddress)
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

// mobileAddressPicker 选配对码里写的地址：自动，或自定义的主机、主机:端口、完整 URL
// （如隧道的 https://example.com/路径），手机要能连到它。
func (a *app) mobileAddressPicker(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	st := &a.state
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, "此电脑的地址").FontSize(11).FontWeight(500).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			cur := mobileAuto
			if st.MobileAddress != "" {
				cur = st.MobileAddress
			}
			sel := cur
			ui.Box(c).Grow(1).Shrink(1).Children(func() {
				if ui.Select(c, &sel, append([]string{mobileAuto}, st.MobileAddresses...)).Changed() && sel != cur {
					if sel == mobileAuto {
						sel = ""
					}
					a.useMobileAddress(sel)
				}
			})
			if st.MobileAddress != "" && pressable(textButton(c, "移除")).Tooltip("从列表里删掉这个地址，改回自动").Clicked() {
				a.removeMobileAddress(st.MobileAddress)
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).Shrink(1).Children(func() {
				in := fieldInput(c, &m.custom, "自定义，如 host:8443 或 https://example.com/hive")
				if in.Submitted() && m.custom != "" {
					a.useMobileAddress(m.custom)
				}
			})
			if pressable(textButton(c, "使用")).Clicked() && m.custom != "" {
				a.useMobileAddress(m.custom)
			}
		})
		ui.Text(c, "手机要能连到这个地址：同一个 Wi-Fi、Tailscale，或转发到本机的隧道、端口映射。").
			FontSize(11.5).TextColor(t.TextMuted).LineHeight(1.45)
	})
}
