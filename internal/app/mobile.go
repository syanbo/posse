package app

import (
	"context"
	"image"
	"image/color"
	"slices"
	"strconv"
	"time"

	"github.com/egoist/mygo/ui"
	"rsc.io/qr"

	"hive/internal/mobile"
)

// mobileBoardID 是侧栏里「手机」一项的 ID。工作区 ID 都以 w_ 开头，不会冲突。
const mobileBoardID = "mobile"

// mobileView 是「手机」页：扫码配对，右边是地址和已配对的手机。
type mobileView struct {
	shown    bool
	url      string
	code     string
	endpoint string
	qr       *ui.Bitmap
	devices  []mobileDevice
	devList  ui.ListState
	loading  bool
	seq      int       // 每次取码加一，只认最后一次的结果
	loadedAt time.Time // 上次取码完成的时间，到点了再取一次
	err      string
	errAddr  string   // err 是用哪个地址取码时出的错
	custom   string   // 正在输入的自定义地址
	locals   []string // 本机检测到的 Tailscale、局域网地址，取码时顺便刷新
}

// mobilePoll 是手机页开着时重取配对码和设备列表的间隔：手机扫码连上后旧码就作废了，
// 要换上新码，列表里也要出现这台手机。
const mobilePoll = 3 * time.Second

// mobileAuto 是地址下拉里"自动"一项的文字。
const mobileAuto = "自动（手机同时试本机所有地址）"

type mobileDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen"`
}

func (a *app) openMobile() {
	a.selected, a.revealSel = mobileBoardID, true
}

// setMobileVisible 每次进入这一页都重新取配对码和设备列表，之前的码可能已经被手机用掉。
// 离开时放掉二维码位图；再进来先用上次的内容重画，新的取回来再换。
func (a *app) setMobileVisible(on bool) {
	m := &a.mobile
	if m.shown == on {
		return
	}
	m.shown = on
	if !on {
		m.qr = nil
		return
	}
	if m.url != "" && m.qr == nil {
		if img, err := qrImage(m.url); err == nil {
			m.qr = ui.NewBitmap(img)
		}
	}
	a.startMobile(false, a.state.MobileAddress, false)
}

// startMobile 在界面线程里发起一次取码。poll 是手机页的定时刷新：出错不显示，也不改选中的地址。
func (a *app) startMobile(rotate bool, addr string, poll bool) {
	m := &a.mobile
	m.seq++
	m.loading = true
	if !poll {
		m.err = ""
	}
	go a.loadMobile(m.seq, rotate, addr, poll)
}

// pollMobile 在手机页开着、没有正在取码时，每隔 mobilePoll 取一次。
func (a *app) pollMobile(c *ui.Context) {
	m := &a.mobile
	if m.loading {
		return
	}
	if wait := mobilePoll - time.Since(m.loadedAt); wait > 0 {
		c.After(wait)
		return
	}
	a.startMobile(false, a.state.MobileAddress, true)
}

// loadMobile 按 addr 取配对码并画成二维码；rotate 时换一个新码，旧码作废。
// 成功后 addr 才成为选中的地址，自定义的记进 ui.json：填错的地址不留下。
func (a *app) loadMobile(seq int, rotate bool, addr string, poll bool) {
	var info struct {
		URL      string `json:"url"`
		Code     string `json:"code"`
		Endpoint string `json:"endpoint"`
	}
	var devices []mobileDevice
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 出错显示在这一页上，不走错误条
	err := a.c.Call(ctx, "mobile.pair", map[string]any{"rotate": rotate, "address": addr}, &info)
	if err == nil {
		err = a.c.Call(ctx, "mobile.devices", nil, &devices)
	}
	var img *image.RGBA
	if err == nil {
		img, err = qrImage(info.URL)
	}
	locals := mobile.LocalEndpoints()
	a.update(func() {
		m := &a.mobile
		if seq != m.seq {
			return
		}
		m.locals = locals
		m.loading, m.loadedAt = false, time.Now()
		if err != nil {
			if !poll {
				m.err, m.errAddr = err.Error(), addr
			}
			return
		}
		if m.url != info.URL || m.qr == nil {
			m.qr = nil
			if m.shown {
				m.qr = ui.NewBitmap(img)
			}
		}
		m.url, m.code, m.endpoint, m.devices = info.URL, info.Code, info.Endpoint, devices
		if poll {
			// 定时刷新用的是选中的地址：之前用它出的错（比如断网）这时已经好了，填错的自定义地址的报错还留着
			if m.errAddr == addr {
				m.err = ""
			}
			return
		}
		m.err = ""
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
	// 检测到的本机地址每次都列在下拉里，不记进自定义的列表
	if addr != "" && !slices.Contains(st.MobileAddresses, addr) && !slices.Contains(a.mobile.locals, addr) {
		st.MobileAddresses = append(st.MobileAddresses, addr)
	}
	a.saveState()
}

// useMobileAddress 换配对码里的地址。地址不对时报错，二维码和下拉都还是原来的。
func (a *app) useMobileAddress(addr string) {
	a.startMobile(false, addr, false)
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

// mobileSidebarItem 是侧栏里「历史」下面的「手机」，和任务、历史同一行样式。
func (a *app) mobileSidebarItem(c *ui.Context) {
	item := a.navRow(c, mobileBoardID, iconPhone, "手机", "", "", nil)
	if a.revealSel && a.selected == mobileBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
}

// mobileBoard 是主区域的手机页，顶栏和任务、历史一样。
func (a *app) mobileBoard(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	a.pollMobile(c)
	a.band(c, func() {
		titleText(c, "手机", fsCardTitle)
	})
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(14).Gap(14).Children(func() {
		card(c).Width(360).Shrink(0).Padding(20).Gap(14).Children(func() {
			ui.Text(c, "用 Hive Mobile 扫这个码。扫不了就复制配对码，贴进 App 的配对页。").
				FontSize(13).TextColor(t.TextMuted).LineHeight(1.5)
			ui.Row(c).Justify(ui.Center).Children(func() {
				box := ui.Box(c).Size(208, 208).Radius(12).Background(ui.RGBA(255, 255, 255, 1)).Center()
				box.Children(func() {
					switch {
					case m.qr != nil:
						ui.Image(c, m.qr).Size(196, 196)
					case m.loading:
						ui.Text(c, "正在生成…").FontSize(12).TextColor(ui.RGBA(0, 0, 0, 0.5))
					}
				})
			})
			if m.err != "" {
				ui.Text(c, m.err).FontSize(12).TextColor(t.Danger).LineHeight(1.45)
			}
			if m.endpoint != "" {
				ui.Text(c, "手机将连接 "+m.endpoint).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).Center().SingleLine()
				ui.Row(c).Gap(8).Justify(ui.Center).Children(func() {
					if pressable(textButton(c, "复制配对码")).Tooltip("扫不了码时，在 App 的配对页粘贴").Clicked() {
						c.WriteClipboard(m.code)
					}
					if pressable(textButton(c, "换一个码")).Tooltip("作废还没用过的旧码").Clicked() {
						m.qr = nil
						a.startMobile(true, a.state.MobileAddress, false)
					}
				})
			}
		})
		card(c).Grow(1).Shrink(1).MinHeight(0).Clip().Children(func() {
			ui.Column(c).Padding(16, 16, 12, 16).Gap(10).Shrink(0).Children(func() {
				a.mobileAddressPicker(c)
			})
			ui.Box(c).Height(1).Shrink(0).Background(colors(t).line)
			a.mobileDevices(c)
		})
	})
}

// mobileDevices 列出配对过的手机。列表占卡片剩下的高度。
func (a *app) mobileDevices(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	n := len(m.devices)
	ui.Row(c).Padding(12, 20, 4, 20).Shrink(0).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "已配对").FontSize(11).FontWeight(500).TextColor(t.TextMuted).Grow(1)
		ui.Text(c, strconv.Itoa(n)).FontSize(11).FontFeatures("tnum").TextColor(colors(t).faint)
	})
	if n == 0 {
		ui.Text(c, "还没有配对过的手机").FontSize(13).TextColor(t.TextMuted).Padding(8, 20)
		return
	}
	m.devList.Key = func(i int) any {
		if i < 0 || i >= len(m.devices) {
			return i
		}
		return m.devices[i].ID
	}
	list := ui.List(c, &m.devList, n, func(i int) {
		d := m.devices[i]
		row := ui.Row(c).Margin(0, 8).Padding(8, 12).Gap(8).Radius(8).AlignItems(ui.Center)
		selectedBg(c, row, false)
		row.Children(func() {
			ui.Text(c, d.Name).FontSize(13).Grow(1).Shrink(1).SingleLine()
			ui.Text(c, "上次连接 "+ago(d.LastSeen)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			if pressable(textButton(c, "移除")).Clicked() {
				a.removeDevice(d)
			}
		})
	}).Grow(1).MinHeight(0).Padding(0, 0, 8, 0)
	fadeEdges(list, &m.devList, colors(t).card)
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
			// 下拉：自动、检测到的 Tailscale 和局域网地址、用过的自定义地址；选了具体地址，配对码里只有它
			labels := []string{mobileAuto}
			addrs := []string{""}
			for _, ip := range m.locals {
				kind := "局域网 "
				if mobile.IsTailscale(ip) {
					kind = "Tailscale "
				}
				labels, addrs = append(labels, kind+ip), append(addrs, ip)
			}
			for _, s := range st.MobileAddresses {
				if !slices.Contains(addrs, s) {
					labels, addrs = append(labels, s), append(addrs, s)
				}
			}
			if i := slices.Index(addrs, st.MobileAddress); i < 0 {
				labels, addrs = append(labels, st.MobileAddress), append(addrs, st.MobileAddress)
			}
			cur := labels[slices.Index(addrs, st.MobileAddress)]
			sel := cur
			ui.Box(c).Grow(1).Shrink(1).Children(func() {
				if ui.Select(c, &sel, labels).Changed() && sel != cur {
					a.useMobileAddress(addrs[slices.Index(labels, sel)])
				}
			})
			if slices.Contains(st.MobileAddresses, st.MobileAddress) && pressable(textButton(c, "移除")).Tooltip("从列表里删掉这个地址，改回自动").Clicked() {
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
		ui.Text(c, "手机要能连到它：同一 Wi-Fi、Tailscale，或转到本机的隧道。选了具体地址，手机就只连这一个。").
			FontSize(11.5).TextColor(t.TextMuted).LineHeight(1.45)
	})
}
