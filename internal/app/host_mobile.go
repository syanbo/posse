package app

import (
	"context"
	"errors"
	"image"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/client"
	"posse/internal/errtext"
	"posse/internal/mobile"
	"posse/internal/rpc"
	"posse/internal/tslink"
	"posse/locales"
)

const (
	hostMobilePoll          = 5 * time.Second
	hostMobileTailscalePoll = 15 * time.Second
	hostMobileRelayPoll     = 30 * time.Second
)

type hostMobileState struct {
	ctx          context.Context
	cancel       context.CancelFunc
	seq          uint64
	liveKey      string
	loading      bool
	pairLoading  bool
	loadedAt     time.Time
	unsupported  bool
	pairFetched  bool // 当前 liveKey 是否已完成过首次取码（例行轮询绝不触发写操作 mobile.pair）
	pairErr      string
	devErr       string
	removeNotice string
	url          string
	code         string
	endpoint     string
	pairAddr     string // 当前二维码对应的地址草稿，地址切换失败时不混显旧码
	qr           *ui.Bitmap
	locals       []string
	addr         string // 当前选中的目标配对地址（空为自动）
	custom       string // 自定义地址输入框草稿
	devices      []client.MobileDevice
	removingID   string
	copiedAt     time.Time
	tsLoaded     bool
	tsAt         time.Time
	ts           tslink.Status
	relayLoaded  bool
	relayAt      time.Time
	relay        client.RelayStatusResult
}

func (a *app) cancelHostMobile() {
	owner := a.windowOwner()
	m := &owner.hostDlg.mobile
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.ctx = nil
	m.seq++
	m.loading = false
	m.pairLoading = false
	m.pairFetched = false
	// 关闭或切离时连同二维码和配对凭据一起清理，避免凭据滞留对话框内存状态。
	m.url = ""
	m.code = ""
	m.endpoint = ""
	m.pairAddr = ""
	m.qr = nil
}

func hostMobileLiveKey(hostID string, target *app) string {
	base := detailLiveKey(hostID, target)
	if base == "" {
		return ""
	}
	controlled := target.host != nil && target.host.snapshot().Controlled
	if controlled {
		return base + "|ctrl"
	}
	return base + "|view"
}

// refreshHostMobile 拉取目标远端主机的手机配对信息。
// 例行轮询（poll=true）严格只读（mobile.devices + 定时刷新 tailscale/relay），绝不发写操作 mobile.pair；
// 仅在显式请求（首次进入控制态、换一个码、切换地址、重试）时由控制端调用 mobile.pair。
func (a *app) refreshHostMobile(owner *app, d *hostDialogState, rotate bool, poll bool) {
	target, isActive, connected := a.detailTargetView(owner, d.editing.ID)
	if !isActive || !connected || target == nil || target.upgrading.Load() {
		owner.cancelHostMobile()
		d.mobile.liveKey = ""
		return
	}
	r := target.request()
	if r.client == nil {
		return
	}
	controlled := r.snapshot.Controlled
	liveKey := hostMobileLiveKey(d.editing.ID, target)
	prevLiveKey := d.mobile.liveKey

	if d.mobile.cancel != nil {
		d.mobile.cancel()
		d.mobile.cancel = nil
	}
	ctx, cancel := context.WithCancel(owner.windowContext())
	d.mobile.ctx = ctx
	d.mobile.cancel = cancel
	d.mobile.seq++
	seq := d.mobile.seq
	gen := d.gen
	hostID := d.editing.ID
	d.mobile.liveKey = liveKey
	d.mobile.loading = true

	if prevLiveKey != liveKey {
		d.mobile.unsupported = false
		d.mobile.pairFetched = false
		d.mobile.pairErr = ""
		d.mobile.devErr = ""
		d.mobile.removeNotice = ""
		d.mobile.url = ""
		d.mobile.code = ""
		d.mobile.endpoint = ""
		d.mobile.pairAddr = ""
		d.mobile.qr = nil
		d.mobile.locals = nil
		d.mobile.devices = nil
		d.mobile.tsLoaded = false
		d.mobile.tsAt = time.Time{}
		d.mobile.relayLoaded = false
		d.mobile.relayAt = time.Time{}
	} else if !poll {
		d.mobile.pairErr = ""
		d.mobile.devErr = ""
		d.mobile.removeNotice = ""
	}

	// 在 UI 线程一次性快照全部输入，后台 goroutine 绝不触碰任何可变对话框字段（避免数据竞争）。
	fetchPair := controlled && (!poll || !d.mobile.pairFetched)
	addr := strings.TrimSpace(d.mobile.addr)
	now := time.Now()
	needTS := !poll || !d.mobile.tsLoaded || now.Sub(d.mobile.tsAt) >= hostMobileTailscalePoll
	needRelay := !poll || !d.mobile.relayLoaded || now.Sub(d.mobile.relayAt) >= hostMobileRelayPoll
	if fetchPair {
		d.mobile.pairLoading = true
		// 显式轮换或切换目标地址时立即清空旧二维码与旧凭据，避免与报错并存误导用户。
		if rotate || d.mobile.pairAddr != addr {
			d.mobile.url = ""
			d.mobile.code = ""
			d.mobile.endpoint = ""
			d.mobile.pairAddr = ""
			d.mobile.qr = nil
		}
	}

	go func() {
		qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
		defer qcancel()

		var pairInfo client.MobilePairResult
		var pairErr error
		var img *image.RGBA
		if fetchPair {
			pairInfo, pairErr = r.client.MobilePair(qctx, rotate, addr)
			if pairErr == nil && pairInfo.URL != "" {
				img, pairErr = qrImage(pairInfo.URL)
			}
		}

		devices, devErr := r.client.MobileDevices(qctx)

		var tsSt tslink.Status
		var tsOK bool
		if needTS {
			tctx, tcancel := context.WithTimeout(qctx, 4*time.Second)
			if st, err := r.client.TailscaleStatus(tctx); err == nil {
				tsSt, tsOK = st, true
			}
			tcancel()
		}
		var relaySt client.RelayStatusResult
		var relayOK bool
		if needRelay {
			rctx, rcancel := context.WithTimeout(qctx, 4*time.Second)
			if st, err := r.client.RelayStatus(rctx); err == nil {
				relaySt, relayOK = st, true
			}
			rcancel()
		}

		doneAt := time.Now()
		owner.update(func() {
			if !owner.hostDlg.open || owner.hostDlg.gen != gen || owner.hostDlg.editing.ID != hostID ||
				owner.hostDlg.mobile.seq != seq || owner.hostDlg.mobile.liveKey != liveKey ||
				ctx.Err() != nil || !r.current() || target.upgrading.Load() {
				return
			}
			ms := &owner.hostDlg.mobile
			ms.loading = false
			ms.pairLoading = false
			ms.loadedAt = doneAt
			if tsOK {
				ms.ts = tsSt
				ms.tsLoaded = true
				ms.tsAt = doneAt
			}
			if relayOK {
				ms.relay = relaySt
				ms.relayLoaded = true
				ms.relayAt = doneAt
			}

			var pairRPC, devRPC *rpc.Error
			if (pairErr != nil && errors.As(pairErr, &pairRPC) && pairRPC.Code == rpc.CodeUnknown) ||
				(devErr != nil && errors.As(devErr, &devRPC) && devRPC.Code == rpc.CodeUnknown) {
				ms.unsupported = true
				ms.pairErr = ""
				ms.devErr = ""
				ms.url, ms.code, ms.endpoint, ms.pairAddr, ms.qr = "", "", "", "", nil
				return
			}

			ms.unsupported = false
			if devErr != nil {
				if !poll {
					ms.devErr = errtext.Text(devErr)
				}
			} else {
				ms.devErr = ""
				ms.devices = devices
			}

			if fetchPair {
				ms.pairFetched = true
				if pairErr != nil {
					ms.pairErr = errtext.Text(pairErr)
					ms.url, ms.code, ms.endpoint, ms.pairAddr, ms.qr = "", "", "", "", nil
					return
				}
				ms.pairErr = ""
				if ms.url != pairInfo.URL || ms.qr == nil {
					ms.qr = nil
					if img != nil && owner.hostDlg.tab == hostTabMobile {
						ms.qr = ui.NewBitmap(img)
					}
				}
				ms.url = pairInfo.URL
				ms.code = pairInfo.Code
				ms.endpoint = pairInfo.Endpoint
				ms.pairAddr = addr
				ms.locals = pairInfo.Locals
				if !poll && addr != "" && addr == strings.TrimSpace(ms.custom) {
					ms.custom = ""
				}
			}
		})
	}()
}

// removeHostMobileDevice 在打开确认框前冻结主机、实例、控制代次与绑定原身份的请求快照，
// 防止确认期间发生切主机、重连或控制权交接后用新身份误提交旧解绑操作。
func (a *app) removeHostMobileDevice(owner *app, d *hostDialogState, target *app, dev client.MobileDevice) {
	if target == nil || target.upgrading.Load() {
		return
	}
	req := target.request()
	if req.client == nil || !req.snapshot.Controlled {
		return
	}
	hostID := d.editing.ID
	dlgGen := d.gen
	devID := dev.ID
	instID := req.snapshot.Desktop.InstanceID
	epoch := req.snapshot.Desktop.Epoch
	m := appL()

	a.confirm(m.MobileRemoveConfirm(dev.Name), m.MobileRemoveBody(), m.CommonRemove(), func() {
		owner.update(func() {
			if !owner.hostDlg.open || owner.hostDlg.gen != dlgGen ||
				owner.hostDlg.tab != hostTabMobile || owner.hostDlg.editing.ID != hostID {
				return
			}
			if target.upgrading.Load() || !req.current() {
				return
			}
			cur := target.host.snapshot()
			if !cur.Connected || !cur.Controlled || cur.Desktop.InstanceID != instID || cur.Desktop.Epoch != epoch {
				return
			}
			op, err := req.client.PrepareMobileRemoveDevice(devID)
			if err != nil {
				owner.hostDlg.mobile.devErr = errtext.Text(err)
				return
			}
			owner.hostDlg.mobile.removingID = devID
			owner.hostDlg.mobile.devErr = ""
			owner.hostDlg.mobile.removeNotice = ""

			go func() {
				winCtx := owner.windowContext()
				ctx, cancel := context.WithTimeout(winCtx, 10*time.Second)
				err := op.Execute(ctx, nil)
				cancel()

				var notice string
				if err != nil {
					var re *rpc.Error
					if errors.As(err, &re) && re.Code == rpc.CodeOutcomeUnknown {
						// 结果未知时按原 ref 查最终状态并刷新设备列表，绝不自动重发新序号。
						pollCtx, pollCancel := context.WithTimeout(winCtx, 6*time.Second)
						opSt, qerr := req.client.DesktopOperationStatus(pollCtx, op.Ref())
						pollCancel()
						if qerr == nil && opSt.State == "done" {
							if opSt.Error != nil {
								err = opSt.Error
							} else {
								err = nil
							}
						} else {
							err = nil
							notice = m.HostMobileRemoveOutcomeUnknown()
						}
					}
				}

				listCtx, listCancel := context.WithTimeout(winCtx, 8*time.Second)
				devices, devErr := req.client.MobileDevices(listCtx)
				listCancel()

				owner.update(func() {
					if !owner.hostDlg.open || owner.hostDlg.gen != dlgGen ||
						owner.hostDlg.editing.ID != hostID || !req.current() {
						return
					}
					ms := &owner.hostDlg.mobile
					if ms.removingID == devID {
						ms.removingID = ""
					}
					ms.removeNotice = notice
					if err != nil {
						ms.devErr = errtext.Text(err)
					} else if devErr != nil {
						ms.devErr = errtext.Text(devErr)
					} else {
						ms.devErr = ""
					}
					if devErr == nil {
						ms.devices = devices
					}
				})
			}()
		})
	})
}

func (a *app) hostMobileTab(c *ui.Context, owner *app, d *hostDialogState) {
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	h := d.editing
	target, isActive, connected := a.detailTargetView(owner, h.ID)
	connState := owner.remoteHostConnState(h.ID)
	ms := &d.mobile

	// 热升级期间立即取消在途读取并冻结配对凭据；连接/控制权/实例变更时自动刷新；常规轮询只读。
	if isActive && connected && target != nil {
		if target.upgrading.Load() {
			if ms.liveKey != "" || ms.loading || ms.qr != nil || ms.code != "" {
				owner.cancelHostMobile()
				ms.liveKey = ""
			}
		} else {
			if key := hostMobileLiveKey(h.ID, target); key != "" && key != ms.liveKey {
				a.refreshHostMobile(owner, d, false, false)
			} else if !ms.loading && !ms.unsupported {
				if wait := hostMobilePoll - time.Since(ms.loadedAt); wait > 0 {
					c.After(wait)
				} else {
					a.refreshHostMobile(owner, d, false, true)
				}
			}
		}
	} else if ms.liveKey != "" {
		owner.cancelHostMobile()
		ms.liveKey = ""
	}

	// 顶部主机状态头（与概览一致）
	mono := "SH"
	if cands := monogramCandidates(h.Name); len(cands) > 0 {
		mono = cands[0]
	}
	ui.Row(c.Key("host-mobile-head")).Padding(12).Gap(12).Radius(rControl).Background(p.fill).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(navigationBox(), navigationBox()).Shrink(0).Radius(rControl).Center().Background(p.tint).Children(func() {
			a.hostTileGlyph(c, h, mono, t.Accent)
		})
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, h.Name).FontSize(fsBody).FontWeight(fwMedium).TextColor(t.Text).SingleLine().Ellipsis("…")
			ui.Text(c, h.Target).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
			ui.Row(c).Gap(6).AlignItems(ui.Center).Padding(2, 0, 0, 0).Children(func() {
				dotColor, statusLabel := a.hostDetailStatusDisplay(c, m, target, isActive, connState)
				ui.Box(c).Size(hostStatusDotInner(), hostStatusDotInner()).Radius(full(hostStatusDotInner())).Background(dotColor).Shrink(0)
				ui.Text(c, statusLabel).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			})
		})
		a.hostOverviewPrimaryAction(c, owner, d, target, isActive, connected, connState)
	})

	if !isActive {
		ui.Text(c, m.HostMobileInactiveNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}
	if !connected || target == nil {
		if target == nil || !target.connecting {
			ui.Text(c, m.HostMobileDisconnectedNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		}
		return
	}
	if ms.unsupported {
		hline(c)
		ui.Text(c, m.HostMobileLegacyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}

	upgrading := target.upgrading.Load()
	controlled := target.host != nil && target.host.snapshot().Controlled
	if upgrading {
		ui.Text(c, m.HostUpgradeLockNote()).FontSize(fsSmall).TextColor(p.honey).LineHeight(1.5).Wrap()
	}

	// 一、扫码配对区（仅控制端可见写操作；查看端静默隐藏写入口不发写请求）
	hline(c)
	ui.Text(c, m.MobilePairTitle()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
	if !controlled {
		ui.Text(c, m.HostMobileReadOnlyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
	} else {
		ui.Row(c).Gap(16).AlignItems(ui.Start).Children(func() {
			qrSize := sz(168)
			imgSize := sz(156)
			ui.Box(c.Key("host-mobile-qr")).Size(qrSize, qrSize).Shrink(0).Radius(rCard).Background(white).Center().Children(func() {
				switch {
				case ms.qr != nil:
					ui.Image(c, ms.qr).Size(imgSize, imgSize)
				case ms.pairLoading:
					ui.Text(c, m.MobileGenerating()).FontSize(fsSmall).TextColor(posseTheme(false).TextMuted)
				}
			})
			ui.Column(c).Grow(1).MinWidth(0).Gap(8).Children(func() {
				ui.Text(c, m.HostMobilePairNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
				if ms.pairErr != "" {
					ui.Text(c, ms.pairErr).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45).Wrap()
				}
				if ms.endpoint != "" && ms.code != "" {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						copyLabel := m.MobileCopyCode()
						if time.Since(ms.copiedAt) < 1500*time.Millisecond {
							copyLabel = m.HostDetailCopied()
							c.After(1500*time.Millisecond - time.Since(ms.copiedAt))
						}
						if pressable(textButton(c.Key("host-mobile-copy"), copyLabel)).Clicked() {
							c.WriteClipboard(ms.code)
							ms.copiedAt = time.Now()
						}
						rotateDisabled := ms.pairLoading || upgrading
						if pressable(textButton(c.Key("host-mobile-rotate"), m.MobileNewCode()).Disabled(rotateDisabled)).Clicked() && !rotateDisabled {
							a.refreshHostMobile(owner, d, true, false)
						}
					})
				} else if !ms.pairLoading && !upgrading {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if pressable(textButton(c.Key("host-mobile-retry"), m.HostMobileRetryPair())).Clicked() {
							a.refreshHostMobile(owner, d, false, false)
						}
					})
				}
				// 目标主机地址选择（只列目标主机检测到的 IP 与运行中的内置 TS IP，不混入本机 Mac IP）
				a.hostMobileAddressPicker(c, owner, d, upgrading)
			})
		})
	}

	// 二、外网连接状态摘要（内置 Tailscale / 外网中转只读概览）
	if ms.tsLoaded || ms.relayLoaded {
		hline(c)
		ui.Text(c, m.HostMobileReachabilityTitle()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Column(c).Gap(6).Children(func() {
			if ms.tsLoaded {
				tsText := hostMobileTailscaleSummary(m, ms.ts)
				hostDetailKV(c, m.TailscaleTitle(), tsText, ms.ts.IP != "")
			}
			if ms.relayLoaded {
				relayText := hostMobileRelaySummary(m, ms.relay)
				hostDetailKV(c, m.RelayTitle(), relayText, false)
			}
		})
	}

	// 三、已配对手机列表
	hline(c)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.MobilePairedTitle()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Grow(1)
		if len(ms.devices) > 0 {
			ui.Text(c, strconv.Itoa(len(ms.devices))).FontSize(fsSmall).FontFeatures("tnum").TextColor(p.faint)
		}
	})
	if ms.devErr != "" {
		ui.Text(c, ms.devErr).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45).Wrap()
	}
	if ms.removeNotice != "" {
		ui.Text(c, ms.removeNotice).FontSize(fsSmall).TextColor(p.honey).LineHeight(1.45).Wrap()
	}
	if len(ms.devices) == 0 {
		if ms.loading && ms.loadedAt.IsZero() {
			ui.Text(c, m.HostDetailLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
		} else if ms.devErr == "" {
			ui.Text(c, m.MobileNoDevices()).FontSize(fsSmall).TextColor(t.TextMuted)
		}
		return
	}
	devs := slices.Clone(ms.devices)
	slices.SortStableFunc(devs, func(x, y client.MobileDevice) int { return y.LastSeen.Compare(x.LastSeen) })
	ui.Column(c).Gap(4).Children(func() {
		for _, dev := range devs {
			dev := dev
			ui.Row(c.Key("host-mobile-dev-"+dev.ID)).Padding(8, 12).Gap(8).Radius(rRow).Background(p.fill).AlignItems(ui.Center).Children(func() {
				dotColor := p.faint
				if dev.Online {
					dotColor = p.sage
				}
				ui.Box(c).Size(hostStatusDotInner(), hostStatusDotInner()).Radius(full(hostStatusDotInner())).Background(dotColor).Shrink(0)
				ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
					ui.Text(c, dev.Name).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Text).SingleLine().Ellipsis("…")
					date := m.AppDate(dev.PairedAt.Local())
					sub := m.MobilePairedOn(date)
					if dev.Links > 1 {
						sub = m.MobilePairedOnLinks(date, dev.Links)
					}
					ui.Text(c, sub).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…")
				})
				if dev.Online {
					ui.Text(c, m.MobileConnected()).FontSize(fsSmall).TextColor(t.Accent).Shrink(0)
				} else if !dev.LastSeen.IsZero() {
					ui.Text(c, m.MobileLastSeen(agoInline(m, dev.LastSeen))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
				}
				if controlled {
					rmDisabled := upgrading || ms.removingID == dev.ID
					if pressable(textButton(c.Key("host-mobile-rm-"+dev.ID), m.CommonRemove()).Disabled(rmDisabled)).Clicked() && !rmDisabled {
						a.removeHostMobileDevice(owner, d, target, dev)
					}
				}
			})
		}
	})
}

func (a *app) hostMobileAddressPicker(c *ui.Context, owner *app, d *hostDialogState, upgrading bool) {
	t, m := c.Theme(), L(c)
	ms := &d.mobile
	labels := []string{m.MobileAddressAuto()}
	addrs := []string{""}
	for _, ip := range ms.locals {
		label := m.MobileAddressLAN(ip)
		if mobile.IsTailscale(ip) {
			label = m.MobileAddressTailscale(ip)
		}
		labels, addrs = append(labels, label), append(addrs, ip)
	}
	if ms.tsLoaded && ms.ts.State == tslink.StateRunning {
		if ip := strings.TrimSpace(ms.ts.IP); ip != "" && !slices.Contains(addrs, ip) {
			labels, addrs = append(labels, m.MobileAddressTailscale(ip)), append(addrs, ip)
		}
	}
	if ms.addr != "" && !slices.Contains(addrs, ms.addr) {
		labels, addrs = append(labels, ms.addr), append(addrs, ms.addr)
	}
	curIdx := slices.Index(addrs, ms.addr)
	if curIdx < 0 {
		curIdx = 0
	}
	cur := labels[curIdx]
	sel := cur

	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, m.HostMobileAddressTitle()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Box(c).Children(func() {
			if ui.Select(c.Key("host-mobile-addr-select"), &sel, labels).Disabled(upgrading).Changed() && sel != cur && !upgrading {
				idx := slices.Index(labels, sel)
				if idx >= 0 {
					ms.addr = addrs[idx]
					a.refreshHostMobile(owner, d, false, false)
				}
			}
		})
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).MinWidth(0).Children(func() {
				inp := fieldInput(c.Key("host-mobile-addr-custom"), &ms.custom, m.HostMobileAddressCustom())
				if inp.Submitted() && strings.TrimSpace(ms.custom) != "" && !upgrading {
					ms.addr = strings.TrimSpace(ms.custom)
					a.refreshHostMobile(owner, d, false, false)
				}
			})
			useDisabled := upgrading || strings.TrimSpace(ms.custom) == ""
			if pressable(textButton(c.Key("host-mobile-addr-use"), m.MobileUse()).Disabled(useDisabled)).Clicked() && !useDisabled {
				ms.addr = strings.TrimSpace(ms.custom)
				a.refreshHostMobile(owner, d, false, false)
			}
		})
	})
}

func hostMobileTailscaleSummary(m locales.Messages, st tslink.Status) string {
	if st.State == tslink.StateRunning {
		if ip := strings.TrimSpace(st.IP); ip != "" {
			return ip
		}
	}
	if info, ok := tailscaleStates[st.State]; ok {
		return info.label(m)
	}
	return m.HostAutostartOff()
}

func hostMobileRelaySummary(m locales.Messages, st client.RelayStatusResult) string {
	if !st.Configured {
		return m.HostAutostartOff()
	}
	if info, ok := relayStates[st.State]; ok {
		return info.label(m)
	}
	return m.RelayUnreachable()
}
