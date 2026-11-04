package app

import (
	"context"
	"image"
	"image/color"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"rsc.io/qr"

	"posse/internal/errtext"
	"posse/internal/mobile"
	"posse/locales"
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
	// othersOpen 展开没报过身份的配对，不记进 ui.json
	othersOpen bool
	loading    bool
	seq        int       // 每次取码加一，只认最后一次的结果
	loadedAt   time.Time // 上次取码完成的时间，到点了再取一次
	err        string
	errAddr    string   // err 是用哪个地址取码时出的错
	custom     string   // 正在输入的自定义地址
	locals     []string // 本机检测到的 Tailscale、局域网地址，取码时顺便刷新
	relay      relayView
}

// mobilePoll 是手机页开着时重取配对码和设备列表的间隔：手机扫码连上后旧码就作废了，
// 要换上新码，列表里也要出现这台手机。
const mobilePoll = 3 * time.Second

type mobileDevice struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Phone    string    `json:"phone"`
	PairedAt time.Time `json:"pairedAt"`
	LastSeen time.Time `json:"lastSeen"`
	Links    int       `json:"links"`
	Online   bool      `json:"online"`
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
	a.loadRelay()
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
				m.err, m.errAddr = errtext.Text(err), addr
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

func (a *app) removeDevice(m locales.Messages, d mobileDevice) {
	a.confirm(m.MobileRemoveConfirm(d.Name), m.MobileRemoveBody(), m.CommonRemove(), func() {
		a.dropDevices([]mobileDevice{d})
	})
}

func (a *app) removeDevices(m locales.Messages, ds []mobileDevice) {
	a.confirm(m.MobileRemoveOthersConfirm(len(ds)), m.MobileRemoveOthersBody(), m.MobileRemoveAll(), func() {
		a.dropDevices(ds)
	})
}

// dropDevices 在后台逐个移除，再重读列表。
func (a *app) dropDevices(ds []mobileDevice) {
	for _, d := range ds {
		if a.call("mobile.rm", map[string]any{"device": d.ID}, nil, 10*time.Second) != nil {
			break
		}
	}
	var devices []mobileDevice
	a.call("mobile.devices", nil, &devices, 10*time.Second)
	a.win.Update(func() { a.mobile.devices = devices })
}

// mobileSidebarItem 是侧栏顶上的「手机」磁贴。
func (a *app) mobileSidebarItem(c *ui.Context) ui.Element {
	// 有手机连着时右上角一个绿点（几台时带数字），悬停写是哪几台
	var badge func()
	if on := a.daemon.MobileOnline; len(on) > 0 {
		badge = func() { mobileOnlineMark(c, on) }
	}
	item := a.navTile(c, mobileBoardID, iconPhone, L(c).MobileTitle(), badge)
	if a.revealSel && a.selected == mobileBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
	return item
}

// mobileOnlineMark 是侧栏「手机」磁贴角上的连接标识：绿点，连着不止一台时前面写台数。
func mobileOnlineMark(c *ui.Context, names []string) {
	t := c.Theme()
	green := colors(t).sage
	tip := L(c).MobileOnlineTip(strings.Join(names, L(c).MobileListSeparator()))
	ui.Row(c).Gap(4).AlignItems(ui.Center).Label(tip).Tooltip(tip).Children(func() {
		if len(names) > 1 {
			ui.Text(c, strconv.Itoa(len(names))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
		}
		ui.Box(c).Size(7, 7).Radius(full(7)).Background(green)
	})
}

// mobileBoard 是主区域的手机页，顶栏和任务、历史一样。内容是居中的一栏：上面配对新手机
// （二维码和地址），下面已配对的手机；窗口再宽也不拉开，免得名字和按钮隔着半个屏幕。
func (a *app) mobileBoard(c *ui.Context) {
	a.pollMobile(c)
	a.band(c, func() {
		titleText(c, L(c).MobileTitle(), fsTitle)
		ui.Spacer(c)
		a.closeBoardButton(c)
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(24).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(720).Gap(16).Children(func() {
				a.mobileDeskCard(c)
				a.mobilePairCard(c)
				a.mobileRelayCard(c)
				a.mobileDevices(c)
			})
		})
	})
}

// deskPrivacy 是看桌面要的两项系统授权在系统设置里的页面。
// note 是还没授权时的说明。
var deskPrivacy = []struct {
	key  string
	note func(locales.Messages) string
	pane string
}{
	{"screen", locales.Messages.MobileNoScreenRecording, "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"},
	{"accessibility", locales.Messages.MobileNoAccessibility, "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"},
}

// mobileDeskCard 是手机看桌面的情况：谁在看（可以停掉），以及缺哪项系统授权。都没有时不显示。
func (a *app) mobileDeskCard(c *ui.Context) {
	viewer, missing := a.daemon.DeskViewer, a.daemon.DeskMissing
	if viewer == "" && len(missing) == 0 {
		return
	}
	t := c.Theme()
	card(c).Padding(20).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			cardTitle(c, L(c).MobileDeskTitle())
			if viewer != "" {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(7, 7).Radius(full(7)).Background(colors(t).sage)
					ui.Text(c, L(c).MobileDeskViewing(viewer)).FontSize(fsBody).Grow(1)
					if pressable(textButton(c, L(c).MobileDeskStop())).Tooltip(L(c).MobileDeskStopTip()).Clicked() {
						go a.call("desk.stop", nil, nil, 10*time.Second)
					}
				})
			}
			for _, p := range deskPrivacy {
				if !slices.Contains(missing, p.key) {
					continue
				}
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, p.note(L(c))).FontSize(fsBody).TextColor(t.TextMuted).Grow(1)
					if pressable(textButton(c, L(c).MobileOpenSystemSettings())).Tooltip(L(c).MobileOpenSystemSettingsTip()).Clicked() {
						go exec.Command("open", p.pane).Run()
					}
				})
			}
		})
	})
}

// mobilePairCard 左边二维码，右边说明、按钮和地址。
func (a *app) mobilePairCard(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	card(c).Padding(20).Children(func() {
		ui.Row(c).Gap(24).AlignItems(ui.Start).Children(func() {
			box := ui.Box(c).Size(208, 208).Shrink(0).Radius(rCard).Background(white).Center()
			box.Children(func() {
				switch {
				case m.qr != nil:
					ui.Image(c, m.qr).Size(196, 196)
				case m.loading:
					ui.Text(c, L(c).MobileGenerating()).FontSize(fsSmall).TextColor(posseTheme(false).TextMuted) // 二维码总是白底
				}
			})
			ui.Column(c).Grow(1).Shrink(1).Gap(12).Children(func() {
				ui.Column(c).Gap(4).Children(func() {
					cardTitle(c, L(c).MobilePairTitle())
					ui.Text(c, L(c).MobilePairNote()).
						FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				})
				if m.err != "" {
					ui.Text(c, m.err).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45)
				}
				if m.endpoint != "" {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if pressable(textButton(c, L(c).MobileCopyCode())).Tooltip(L(c).MobileCopyCodeTip()).Clicked() {
							c.WriteClipboard(m.code)
						}
						if pressable(textButton(c, L(c).MobileNewCode())).Tooltip(L(c).MobileNewCodeTip()).Clicked() {
							m.qr = nil
							a.startMobile(true, a.state.MobileAddress, false)
						}
					})
				}
				hline(c)
				a.mobileAddressPicker(c)
			})
		})
	})
}

// mobileDevices 列出配对过的手机。Posse Mobile 报过身份的一台一行；没报过的（旧版 App，
// 或重新扫码后手机已经不用的码）收成一行"其他配对"，展开才逐个列出。
func (a *app) mobileDevices(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	var phones, others []mobileDevice
	for _, d := range m.devices {
		if d.Phone != "" {
			phones = append(phones, d)
		} else {
			others = append(others, d)
		}
	}
	byRecent := func(x, y mobileDevice) int { return y.LastSeen.Compare(x.LastSeen) }
	slices.SortStableFunc(phones, byRecent)
	slices.SortStableFunc(others, byRecent)
	card(c).Padding(20, 12, 12, 12).Gap(4).Children(func() {
		ui.Row(c).Padding(0, 8, 6, 8).AlignItems(ui.Center).Children(func() {
			cardTitle(c, L(c).MobilePairedTitle()).Grow(1)
			if len(phones) > 0 {
				ui.Text(c, strconv.Itoa(len(phones))).FontSize(fsSmall).FontFeatures("tnum").TextColor(colors(t).faint)
			}
		})
		if len(m.devices) == 0 {
			ui.Text(c, L(c).MobileNoDevices()).FontSize(fsBody).TextColor(t.TextMuted).Padding(4, 8, 8, 8)
			return
		}
		for _, d := range phones {
			a.mobileDeviceRow(c, d)
		}
		if len(others) == 0 {
			return
		}
		if len(phones) > 0 {
			hline(c).Margin(4, 6)
		}
		online := 0
		for _, d := range others {
			if d.Online {
				online++
			}
		}
		row := ui.Row(c).Padding(8, 8).Gap(12).Radius(rControl).AlignItems(ui.Center)
		selectedBg(c, row, false)
		if row.Clicked() {
			m.othersOpen = !m.othersOpen
		}
		row.Children(func() {
			tile(c, iconPhone, 32, colors(t).fill, t.TextMuted)
			ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
				ui.Text(c, L(c).MobileOthers(len(others))).FontSize(fsBody)
				note := L(c).MobileOthersNote()
				if online > 0 {
					note = L(c).MobileOthersNoteOnline(online)
				}
				ui.Text(c, note).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			})
			label := L(c).MobileExpand()
			if m.othersOpen {
				label = L(c).MobileCollapse()
			}
			ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			if pressable(textButton(c, L(c).MobileRemoveAll())).Clicked() {
				a.removeDevices(L(c), others)
			}
		})
		if m.othersOpen {
			for _, d := range others {
				a.mobileDeviceRow(c, d)
			}
		}
	})
}

// mobileDeviceRow 是一台手机：名字，下面是配对时间和存了几个配对码；右边在线或上次连接、移除。
func (a *app) mobileDeviceRow(c *ui.Context, d mobileDevice) {
	t := c.Theme()
	row := ui.Row(c).Padding(8, 8).Gap(12).Radius(rControl).AlignItems(ui.Center)
	selectedBg(c, row, false)
	row.Children(func() {
		fg := t.TextMuted
		if d.Online {
			fg = t.Accent
		}
		tile(c, iconPhone, 32, colors(t).fill, fg)
		ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
			ui.Text(c, d.Name).FontSize(fsBody).SingleLine()
			date := L(c).AppDate(d.PairedAt.Local())
			sub := L(c).MobilePairedOn(date)
			if d.Links > 1 {
				sub = L(c).MobilePairedOnLinks(date, d.Links)
			}
			ui.Text(c, sub).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
		})
		if d.Online {
			ui.Text(c, L(c).MobileConnected()).FontSize(fsSmall).TextColor(t.Accent).Shrink(0)
		} else {
			ui.Text(c, L(c).MobileLastSeen(agoInline(L(c), d.LastSeen))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		}
		if pressable(textButton(c, L(c).CommonRemove())).Clicked() {
			a.removeDevice(L(c), d)
		}
	})
}

// mobileAddressPicker 选配对码里写的地址：自动，或自定义的主机、主机:端口、完整 URL
// （如隧道的 https://example.com/路径），手机要能连到它。
func (a *app) mobileAddressPicker(c *ui.Context) {
	t := c.Theme()
	m := &a.mobile
	st := &a.state
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, L(c).MobileAddressTitle()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			// 下拉：自动、检测到的 Tailscale 和局域网地址、用过的自定义地址；选了具体地址，配对码里只有它
			labels := []string{L(c).MobileAddressAuto()}
			addrs := []string{""}
			for _, ip := range m.locals {
				label := L(c).MobileAddressLAN(ip)
				if mobile.IsTailscale(ip) {
					label = L(c).MobileAddressTailscale(ip)
				}
				labels, addrs = append(labels, label), append(addrs, ip)
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
			if slices.Contains(st.MobileAddresses, st.MobileAddress) && pressable(textButton(c, L(c).CommonRemove())).Tooltip(L(c).MobileAddressRemoveTip()).Clicked() {
				a.removeMobileAddress(st.MobileAddress)
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).Shrink(1).Children(func() {
				in := fieldInput(c, &m.custom, L(c).MobileAddressCustom())
				if in.Submitted() && m.custom != "" {
					a.useMobileAddress(m.custom)
				}
			})
			if pressable(textButton(c, L(c).MobileUse())).Clicked() && m.custom != "" {
				a.useMobileAddress(m.custom)
			}
		})
		ui.Text(c, L(c).MobileAddressNote()).
			FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
	})
}
