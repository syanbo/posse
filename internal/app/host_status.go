package app

import (
	"math"

	"github.com/egoist/mygo/ui"

	"posse/internal/remote"
	"posse/locales"
)

func (a *app) connectStageText(m locales.Messages) string {
	if s := a.windowOwner().sshAuth; s != nil && s.current() != nil {
		return m.HostStageAuth()
	}
	switch a.connectStage {
	case remote.StageAuth:
		return m.HostStageAuth()
	case remote.StageProbe:
		return m.HostStageProbe()
	case remote.StagePackage:
		return m.HostStagePackage()
	case remote.StageUpload:
		pct := int(math.Round(a.connectPct * 100))
		if pct < 0 {
			pct = 0
		}
		if pct > 100 {
			pct = 100
		}
		return m.HostStageUpload(pct)
	case remote.StageStart:
		return m.HostStageStart()
	case remote.StageUpgrade:
		return m.HostStageUpgrade()
	case remote.StageHandshake:
		return m.HostStageHandshake()
	default:
		return m.HostConnecting()
	}
}

// hostChip 画在状态栏内存按钮左边、a.loaded 判断之外，仅在需要引导或处理异常状态时出现：
// 1. 尚无任何远程主机且在本机：轻量的「添加 SSH」入口；
// 2. 远端连接/部署中：转圈 + 阶段文字，点击可取消；
// 3. 远端查看中：眼睛图标 + 查看状态，点击可接管；
// 4. 远端已断开：断线图标 + 已断开，点击可重连；
// 5. 已有主机且在本机，或远端已正常控制：隐藏不占位（归属由导航栏选中磁贴和侧栏主机行表达）。
func (a *app) hostChip(c *ui.Context) {
	if a.views == nil {
		return
	}
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	owner := a.windowOwner()

	var s hostSnapshot
	if a.host != nil {
		s = a.host.snapshot()
	}

	if !a.remoteHost() {
		// 本机处于只读查看态（例如被其他端持有控制）时照常显示查看芯片，不因已配远程主机而藏起异常状态。
		if !s.Connected || s.Controlled {
			if len(owner.hostMenu.hosts) > 0 {
				return
			}
			b := ui.Row(c.Key("host-chip")).Height(sz(24)).Padding(0, chipPad).Gap(6).Radius(rRow).AlignItems(ui.Center).Shrink(1).
				MinWidth(fsBody + 2*chipPad).Role(ui.RoleButton).Label(m.HostAddItem()).Tooltip(a.withKey(m.HostAddItem(), "switchHost"))
			bg := ui.Transparent
			if b.Hovered() {
				bg = p.wash
			}
			b.Background(b.AnimateColor("bg", bg, hover)).Children(func() {
				ui.Icon(c, iconHost).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
				ui.Text(c, m.HostAddShort()).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
			})
			if b.Clicked() {
				a.openHostDialog(remote.Host{})
			}
			return
		}
	}

	if !a.connecting && s.Connected && s.Controlled {
		return
	}

	b := ui.Row(c.Key("host-chip")).Height(sz(24)).Padding(0, chipPad).Gap(6).Radius(rRow).AlignItems(ui.Center).Shrink(1).
		MinWidth(fsBody + 2*chipPad).Role(ui.RoleButton)
	bg := ui.Transparent
	if b.Hovered() {
		bg = p.wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))

	switch {
	case a.connecting:
		stage := a.connectStageText(m)
		b.Label(stage).Tooltip(a.withKey(stage, "switchHost")).Children(func() {
			if c.Preferences().ReduceMotion {
				ui.Icon(c, iconHexOutline).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			} else {
				ui.Spinner(c).Size(fsSmall, fsSmall).TextColor(t.TextMuted).Shrink(0)
			}
			ui.Text(c, stage).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(hostChipNameMax()).Shrink(1).MinWidth(0)
		})
		menuButton(c, b, func(menu *popMenu) {
			if menu.Item(m.HostCancelConnect()).Chosen() {
				owner.hosts.disconnect(a.viewID)
			}
		})
	case !s.Connected:
		iconColor := p.faint
		if a.err != "" {
			iconColor = t.Danger
		}
		label := m.HostDisconnected()
		b.Label(label).Tooltip(a.withKey(label, "switchHost")).Children(func() {
			ui.Icon(c, iconHost).FontSize(fsBody).TextColor(iconColor).Shrink(0)
			ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(hostChipNameMax()).Shrink(1).MinWidth(0)
		})
		if b.Clicked() {
			if h, ok := a.findSavedHost(a.viewID); ok {
				a.selectRemoteHost(h)
			}
		}
	default:
		label := m.HostViewing()
		if s.Desktop.Owner != "" && s.Desktop.Owner != a.host.controllerID {
			label = m.HostViewingOwner(s.Desktop.Label)
		}
		b.Label(label).Tooltip(a.withKey(label, "switchHost")).Children(func() {
			ui.Icon(c, iconEye).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Ellipsis("…").MaxWidth(hostChipNameMax()).Shrink(1).MinWidth(0)
		})
		if b.Clicked() {
			a.takeHost(m, s)
		}
	}
}

// hostHeaderBadge 画在工作区、聊天、任务、历史模块侧栏标题（如「项目」）右侧，
// 仅远端主机显示，与标题垂直居中对齐；点击展开当前主机的操作菜单（不重复列主机切换列表）。
func (a *app) hostHeaderBadge(c *ui.Context) {
	if !a.remoteHost() {
		return
	}
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	name := a.hostName(m)
	saved, ok := a.findSavedHost(a.viewID)
	if !ok {
		saved = remote.Host{ID: a.viewID, Name: name}
	}
	chip := ui.Row(c.Key("host-context-badge")).Height(sz(22)).Padding(0, 6).Gap(4).Radius(rRow).
		AlignItems(ui.Center).Shrink(1).MinWidth(0).Role(ui.RoleButton).Label(name).
		BorderWidth(hairline).BorderColor(p.line)
	bg := p.fill
	if chip.Hovered() {
		bg = p.wash
	}
	chip.Background(chip.AnimateColor("bg", bg, hover)).Children(func() {
		a.hostMiniGlyph(c, saved)
		ui.Text(c, name).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Text).SingleLine().Ellipsis("…").Shrink(1).MinWidth(0)
	})
	var build func(*popMenu)
	menuButton(c, chip, func(menu *popMenu) {
		if build == nil {
			build = a.hostActionsBuild(m, saved, true)
		}
		build(menu)
	})
}

// remoteLandingCard 在首次连接远端（尚无缓存数据）期间或失败时占据主区域中央，避免整块白屏。
func (a *app) remoteLandingCard(c *ui.Context) bool {
	if !a.remoteHost() || a.loaded {
		return false
	}
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	name := a.hostName(m)
	var s hostSnapshot
	if a.host != nil {
		s = a.host.snapshot()
	}
	if s.Connected && !a.connecting {
		return false
	}
	ui.Column(c.Key("remote-landing")).Grow(1).Center().Padding(24).Children(func() {
		card := ui.Column(c).MaxWidth(hostDialogWidth()).Padding(24).Gap(12).Radius(rCard).Background(p.card).AlignItems(ui.Center)
		elevCard(c, card)
		card.Children(func() {
			if a.connecting {
				posseLoader(c, tileLarge(), t.Accent, c.Preferences().ReduceMotion)
				titleText(c, m.HostConnectingTitle(name), fsTitle)
				ui.Text(c, a.connectStageText(m)).FontSize(fsBody).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine()
				ui.Row(c).Gap(8).Padding(8, 0, 0, 0).Children(func() {
					if confirmButton(c, pressable(textButton(c, m.CommonCancel()))).Clicked() {
						a.windowOwner().hosts.disconnect(a.viewID)
					}
				})
				return
			}
			ui.Icon(c, iconHost).FontSize(tileLarge()).TextColor(t.Danger)
			titleText(c, m.HostFailedTitle(name), fsTitle)
			if a.err != "" {
				ui.Text(c, a.err).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
			}
			ui.Row(c).Gap(8).Padding(8, 0, 0, 0).Children(func() {
				if confirmButton(c, pressable(textButton(c, m.HostBackLocal()))).Clicked() {
					a.selectLocalHost()
				}
				if h, ok := a.findSavedHost(a.viewID); ok {
					if confirmButton(c, pressable(textButton(c, m.HostEditItem()))).Clicked() {
						a.openHostDialog(h)
					}
					if confirmButton(c, pressable(accentButton(c, m.CommonRetry()))).Clicked() {
						a.selectRemoteHost(h)
					}
				}
			})
		})
	})
	return true
}
