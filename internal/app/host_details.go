package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/remote"
	"posse/internal/rpc"
	"posse/locales"
)

const (
	hostTabOverview = 0
	hostTabSettings = 1
)

const linuxLingerCommand = "loginctl enable-linger $USER"

type hostAutostartOp struct {
	instanceID string
	busy       bool
	status     *rpc.AutostartStatus
	notice     string
	err        string
}

type hostDetailState struct {
	ctx                       context.Context
	cancel                    context.CancelFunc
	memCancel                 context.CancelFunc
	memSeq                    uint64
	liveKey                   string
	fetchedGen                uint64
	loadingInfo               bool
	loadingAutostart          bool
	loadingMem                bool
	info                      *rpc.RemoteInfo
	infoErr                   string
	autostart                 *rpc.AutostartStatus
	autostartErr              string
	autostartUnsupported      bool
	autostartWriteUnsupported bool
	mem                       *rpc.DaemonMemoryResult
	memErr                    string
	memUnsupported            bool
	memExpanded               map[string]bool
	copiedKey                 string
	copiedAt                  time.Time
}

func (a *app) cancelHostDetails() {
	owner := a.windowOwner()
	if owner.hostDlg.detail.memCancel != nil {
		owner.hostDlg.detail.memCancel()
		owner.hostDlg.detail.memCancel = nil
	}
	if owner.hostDlg.detail.cancel != nil {
		owner.hostDlg.detail.cancel()
		owner.hostDlg.detail.cancel = nil
	}
	owner.hostDlg.detail.ctx = nil
	owner.hostDlg.detail.memSeq++
}

func (a *app) hostDialogDirty(d *hostDialogState) bool {
	if d.editing.ID == "" {
		return false
	}
	portStr := ""
	if d.editing.Port != 0 {
		portStr = strconv.Itoa(d.editing.Port)
	}
	return strings.TrimSpace(d.target) != d.editing.Target ||
		strings.TrimSpace(d.name) != d.editing.Name ||
		strings.TrimSpace(d.icon) != d.editing.Icon ||
		d.iconImage != d.editing.IconImage ||
		strings.TrimSpace(d.user) != d.editing.User ||
		strings.TrimSpace(d.port) != portStr ||
		strings.TrimSpace(d.home) != d.editing.Home ||
		strings.TrimSpace(d.exe) != d.editing.Executable
}

func (a *app) detailTargetView(owner *app, hostID string) (target *app, isActive bool, connected bool) {
	if owner.views == nil || hostID == "" {
		return nil, false, false
	}
	target = owner.views.all[hostID]
	if target == nil {
		return nil, false, false
	}
	isActive = owner.views.active.Load() == target
	if target.host != nil {
		connected = target.host.snapshot().Connected && !target.connecting
	}
	return target, isActive, connected
}

func detailLiveKey(hostID string, target *app) string {
	if target == nil || target.host == nil {
		return ""
	}
	s := target.host.snapshot()
	if !s.Connected || target.connecting {
		return ""
	}
	return fmt.Sprintf("%s|%s|%d|%d", hostID, s.Desktop.InstanceID, s.Generation, target.viewGeneration.Load())
}

// refreshHostDetails 仅对当前显示且已连接的远端主机拉取实时 daemon.info、开机自启状态与终端内存明细。
func (a *app) refreshHostDetails(owner *app, d *hostDialogState) {
	owner.cancelHostDetails()
	target, isActive, connected := a.detailTargetView(owner, d.editing.ID)
	if !isActive || !connected || target == nil {
		d.detail.liveKey = ""
		d.detail.loadingInfo = false
		d.detail.loadingAutostart = false
		d.detail.loadingMem = false
		return
	}
	r := target.request()
	if r.client == nil {
		return
	}
	gen := d.gen
	liveKey := detailLiveKey(d.editing.ID, target)
	prevLiveKey := d.detail.liveKey
	ctx, cancel := context.WithCancel(owner.windowContext())
	d.detail.ctx = ctx
	d.detail.cancel = cancel
	d.detail.liveKey = liveKey
	d.detail.fetchedGen = gen
	d.detail.loadingInfo = true
	d.detail.loadingAutostart = true
	d.detail.loadingMem = true
	d.detail.infoErr = ""
	d.detail.autostartErr = ""
	d.detail.memErr = ""
	if prevLiveKey != liveKey {
		// 切换主机、重连或更换 daemon 实例时清掉上一实例的自启缓存，避免拿旧实例状态开放新实例开关。
		d.detail.autostart = nil
		d.detail.autostartUnsupported = false
		d.detail.autostartWriteUnsupported = false
		if target.autostartOp != nil && target.autostartOp.instanceID != r.snapshot.Desktop.InstanceID {
			target.autostartOp = nil
		}
	}
	target.autostartGen++
	autostartReadGen := target.autostartGen
	d.detail.memSeq++
	memSeq := d.detail.memSeq
	memCtx, memCancel := context.WithCancel(ctx)
	d.detail.memCancel = memCancel

	var expanded []string
	for id, open := range d.detail.memExpanded {
		if open {
			expanded = append(expanded, id)
		}
	}

	go func() {
		qctx, qcancel := context.WithTimeout(ctx, 6*time.Second)
		defer qcancel()
		info, err := r.client.RemoteInfo(qctx)
		owner.update(func() {
			if !owner.hostDlg.open || owner.hostDlg.gen != gen || owner.hostDlg.detail.liveKey != liveKey || ctx.Err() != nil || !r.current() {
				return
			}
			owner.hostDlg.detail.loadingInfo = false
			if err != nil {
				owner.hostDlg.detail.infoErr = errtext.Text(err)
				return
			}
			cp := info
			owner.hostDlg.detail.info = &cp
			target.remoteInfo = &cp
			if info.Platform != "" {
				target.platform = info.Platform
			}
		})
	}()

	go func() {
		qctx, qcancel := context.WithTimeout(ctx, 10*time.Second)
		defer qcancel()
		st, err := r.client.AutostartStatus(qctx)
		owner.update(func() {
			if !owner.hostDlg.open || owner.hostDlg.gen != gen || owner.hostDlg.detail.liveKey != liveKey || ctx.Err() != nil || !r.current() {
				return
			}
			// 读请求期间若已发起写入或更晚的读写操作，丢弃迟到的状态回包，绝不覆盖更新后的写入结果。
			if target.autostartGen != autostartReadGen {
				owner.hostDlg.detail.loadingAutostart = false
				return
			}
			owner.hostDlg.detail.loadingAutostart = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					owner.hostDlg.detail.autostartUnsupported = true
					owner.hostDlg.detail.autostart = nil
					return
				}
				owner.hostDlg.detail.autostartErr = errtext.Text(err)
				return
			}
			cp := st
			owner.hostDlg.detail.autostartUnsupported = false
			owner.hostDlg.detail.autostart = &cp
		})
	}()

	go func() {
		qctx, qcancel := context.WithTimeout(memCtx, 5*time.Second)
		defer qcancel()
		mem, err := r.client.DaemonMemory(qctx, expanded)
		owner.update(func() {
			if !owner.hostDlg.open || owner.hostDlg.gen != gen || owner.hostDlg.detail.liveKey != liveKey || owner.hostDlg.detail.memSeq != memSeq || memCtx.Err() != nil || !r.current() {
				return
			}
			owner.hostDlg.detail.loadingMem = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					owner.hostDlg.detail.memUnsupported = true
					owner.hostDlg.detail.mem = nil
					return
				}
				owner.hostDlg.detail.memErr = errtext.Text(err)
				return
			}
			cp := mem
			owner.hostDlg.detail.memUnsupported = false
			owner.hostDlg.detail.mem = &cp
		})
	}()
}

func (a *app) reloadHostDetailMemory(owner *app, d *hostDialogState) {
	target, isActive, connected := a.detailTargetView(owner, d.editing.ID)
	if !isActive || !connected || target == nil {
		return
	}
	r := target.request()
	if r.client == nil {
		return
	}
	if d.detail.memCancel != nil {
		d.detail.memCancel()
		d.detail.memCancel = nil
	}
	parent := d.detail.ctx
	if parent == nil {
		parent = owner.windowContext()
	}
	memCtx, memCancel := context.WithCancel(parent)
	d.detail.memCancel = memCancel
	d.detail.memSeq++
	seq := d.detail.memSeq
	gen := d.gen
	liveKey := d.detail.liveKey
	d.detail.loadingMem = true
	d.detail.memErr = ""
	var expanded []string
	for id, open := range d.detail.memExpanded {
		if open {
			expanded = append(expanded, id)
		}
	}
	go func() {
		qctx, qcancel := context.WithTimeout(memCtx, 5*time.Second)
		defer qcancel()
		mem, err := r.client.DaemonMemory(qctx, expanded)
		owner.update(func() {
			if !owner.hostDlg.open || owner.hostDlg.gen != gen || owner.hostDlg.detail.liveKey != liveKey || owner.hostDlg.detail.memSeq != seq || memCtx.Err() != nil || !r.current() {
				return
			}
			owner.hostDlg.detail.loadingMem = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					owner.hostDlg.detail.memUnsupported = true
					owner.hostDlg.detail.mem = nil
					return
				}
				owner.hostDlg.detail.memErr = errtext.Text(err)
				return
			}
			cp := mem
			owner.hostDlg.detail.memUnsupported = false
			owner.hostDlg.detail.mem = &cp
		})
	}()
}

func (a *app) hostOverviewTab(c *ui.Context, owner *app, d *hostDialogState) {
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	h := d.editing
	target, isActive, connected := a.detailTargetView(owner, h.ID)
	connState := owner.remoteHostConnState(h.ID)

	// 概览打开期间若完成连接、切换到当前主机或更换实例/连接代次，自动拉取最新详情；热升级进行中暂停拉取；离开或断开时立即取消在途请求。
	if isActive && connected && target != nil {
		if !target.upgrading.Load() {
			if key := detailLiveKey(h.ID, target); key != "" && key != d.detail.liveKey {
				a.refreshHostDetails(owner, d)
			}
		}
	} else if d.detail.liveKey != "" {
		owner.cancelHostDetails()
		d.detail.liveKey = ""
		d.detail.loadingInfo = false
		d.detail.loadingAutostart = false
		d.detail.loadingMem = false
	}

	// 顶部状态头：主机图标 + 名称/目标 + 连接状态点与单一步骤主动作（Modal 内遵循单闭合边框，使用底色填充不画框）。
	mono := "SH"
	if cands := monogramCandidates(h.Name); len(cands) > 0 {
		mono = cands[0]
	}
	ui.Row(c.Key("host-overview-head")).Padding(12).Gap(12).Radius(rControl).Background(p.fill).AlignItems(ui.Center).Children(func() {
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
		ui.Text(c, m.HostOverviewInactiveNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
	} else if !connected && (target == nil || !target.connecting) {
		ui.Text(c, m.HostOverviewDisconnectedNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
	}

	// 合并实时 info 与内存快照（非活动/断开时展示最近一次已知信息）。
	info := d.detail.info
	if info == nil && target != nil {
		info = target.remoteInfo
	}

	// 一、服务端身份与版本
	hline(c)
	a.hostOverviewServerSection(c, owner, d, target, isActive, connected, info)

	// 二、服务端检查更新与热升级、资源占用与开机自启（仅活动且已连接时展示实时面板）
	if isActive && connected && target != nil {
		hline(c)
		a.hostOverviewUpgradeSection(c, owner, d, target, info)
		hline(c)
		a.hostOverviewResourcesSection(c, owner, d, target)
		hline(c)
		a.hostOverviewAutostartSection(c, owner, d, target, info)
	}
}

func (a *app) hostDetailStatusDisplay(c *ui.Context, m locales.Messages, target *app, isActive bool, st hostConnState) (ui.Color, string) {
	t, p := c.Theme(), colors(c.Theme())
	switch st {
	case hostConnOnline:
		if isActive {
			return p.sage, m.HostStatusControlled()
		}
		return p.sage, m.HostStatusPooled()
	case hostConnConnecting:
		if target != nil {
			return p.honey, target.connectStageText(m)
		}
		return p.honey, m.HostConnecting()
	case hostConnUpgrading:
		if target != nil {
			return p.honey, target.upgradeStageText(m)
		}
		return p.honey, m.HostUpgrading()
	case hostConnViewing:
		if target != nil && target.host != nil {
			s := target.host.snapshot()
			if s.Desktop.Owner != "" && s.Desktop.Owner != target.host.controllerID {
				return p.honey, m.HostViewingOwner(s.Desktop.Label)
			}
		}
		return p.honey, m.HostViewing()
	case hostConnError:
		return t.Danger, m.HostStatusError()
	default:
		return p.faint, m.HostDisconnected()
	}
}

func (a *app) hostOverviewPrimaryAction(c *ui.Context, owner *app, d *hostDialogState, target *app, isActive, connected bool, st hostConnState) {
	m := L(c)
	h := d.editing
	switch {
	case !isActive:
		if pressable(textButton(c.Key("host-detail-switch"), m.HostSwitchHere())).Clicked() {
			a.openHost(h)
			d.detail.info, d.detail.autostart, d.detail.mem = nil, nil, nil
			a.refreshHostDetails(owner, d)
		}
	case st == hostConnUpgrading:
		if target != nil && target.upgradeCancel != nil {
			if pressable(textButton(c.Key("host-detail-upgrade-cancel"), m.HostUpgradeCancelBtn())).Clicked() {
				if owner.hosts != nil {
					owner.hosts.cancelHostUpgrade(h.ID)
				}
			}
		}
	case st == hostConnConnecting:
		if pressable(textButton(c.Key("host-detail-cancel"), m.HostCancelConnect())).Clicked() {
			if owner.hosts != nil {
				owner.hosts.disconnect(h.ID)
			}
		}
	case !connected:
		if pressable(accentButton(c.Key("host-detail-connect"), m.HostConnect())).Clicked() {
			a.selectRemoteHost(h)
		}
	case target != nil && target.host != nil && !target.host.snapshot().Controlled:
		snap := target.host.snapshot()
		if pressable(accentButton(c.Key("host-detail-take"), m.HostTake())).Clicked() {
			target.takeHost(m, snap)
		}
	default:
		if pressable(textButton(c.Key("host-detail-disconnect"), m.HostDisconnect())).Clicked() {
			if owner.hosts != nil {
				owner.hosts.disconnect(h.ID)
			}
		}
	}
}

func (a *app) hostOverviewServerSection(c *ui.Context, owner *app, d *hostDialogState, target *app, isActive, connected bool, info *rpc.RemoteInfo) {
	t, m := c.Theme(), L(c)
	ui.Row(c).AlignItems(ui.Center).Children(func() {
		ui.Text(c, m.HostOverviewSectionServer()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Grow(1)
		if isActive && connected && (target == nil || !target.upgrading.Load()) {
			if pressable(iconButtonSized(c.Key("host-detail-refresh"), iconRefresh, m.CommonRefresh(), compactBox(), smallIcon())).Clicked() {
				a.refreshHostDetails(owner, d)
			}
		}
	})
	if d.detail.loadingInfo && info == nil && (target == nil || target.daemon.Version == "") {
		ui.Text(c, m.HostDetailLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
		return
	}
	if d.detail.infoErr != "" {
		ui.Text(c, d.detail.infoErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
	}

	version, build, edition := "", "", ""
	platform, arch, home := "", "", ""
	pid := 0
	var startedAt time.Time
	if target != nil {
		version, build, edition = target.daemon.Version, target.daemon.Build, target.daemon.Edition
		home, pid, startedAt = target.daemon.Home, target.daemon.Pid, target.daemon.StartedAt
		platform = target.targetPlatform()
	}
	managed := false
	if info != nil {
		if info.Version != "" {
			version = info.Version
		}
		if info.Build != "" {
			build = info.Build
		}
		if info.Edition != "" {
			edition = info.Edition
		}
		if info.Platform != "" {
			platform = info.Platform
		}
		if info.Arch != "" {
			arch = info.Arch
		}
		if info.Home != "" {
			home = info.Home
		}
		managed = info.Managed
	}

	ui.Column(c).Gap(6).Children(func() {
		if edition != "" {
			edLabel := m.HostEditionFull()
			if edition == "headless" {
				if managed {
					edLabel = m.HostEditionHeadlessManaged()
				} else {
					edLabel = m.HostEditionHeadlessCustom()
				}
			}
			hostDetailKV(c, m.HostFieldEdition(), edLabel, false)
		}
		if version != "" {
			verText := version
			if commit, _, _ := strings.Cut(strings.TrimSpace(build), " "); commit != "" && commit != "dev" && !strings.Contains(commit, "T") {
				if len(commit) > 7 {
					commit = commit[:7]
				}
				verText = version + " (" + commit + ")"
			}
			hostDetailKV(c, m.HostFieldVersion(), verText, true)
		}
		if platform != "" {
			sys := hostPlatformLabel(platform, arch)
			hostDetailKV(c, m.HostFieldPlatform(), sys, false)
		}
		if isActive && connected && pid > 0 {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.HostFieldProcess()).FontSize(fsSmall).TextColor(t.TextMuted).Width(sz(88)).Shrink(0)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).MinWidth(0).Children(func() {
					ui.Text(c, m.HostProcessPid(strconv.Itoa(pid))).FontSize(fsSmall).Font(monoFont).FontFeatures("tnum").TextColor(t.Text).SingleLine()
					if !startedAt.IsZero() {
						ui.Text(c, m.HostProcessUp(formatHostUptime(m, time.Since(startedAt)))).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine()
					}
				})
			})
		}
		if home != "" {
			a.hostDetailCopyRow(c, d, "home", m.HostFieldDataDir(), home)
		}
	})
}

func (a *app) hostOverviewUpgradeSection(c *ui.Context, owner *app, d *hostDialogState, target *app, info *rpc.RemoteInfo) {
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	ui.Text(c, m.HostOverviewSectionUpgrade()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
	if info == nil && d.detail.loadingInfo {
		ui.Text(c, m.HostDetailLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
		return
	}
	edition := target.daemon.Edition
	managed := false
	if info != nil {
		if info.Edition != "" {
			edition = info.Edition
		}
		managed = info.Managed
	}
	if edition != "" && edition != "headless" {
		ui.Text(c, m.HostUpgradeFullAppNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}
	if !managed || d.editing.Executable != "" {
		ui.Text(c, m.HostUpgradeUnmanagedNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}

	controlled := target.host != nil && target.host.snapshot().Controlled
	autostartBusy := target.autostartOp != nil && target.host != nil &&
		target.autostartOp.instanceID == target.host.snapshot().Desktop.InstanceID && target.autostartOp.busy

	ui.Column(c).Gap(8).Children(func() {
		if target.upgrading.Load() {
			ui.Column(c).Padding(8, 12).Gap(8).Radius(rRow).Background(p.fill).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if c.Preferences().ReduceMotion {
						ui.Icon(c, iconHexOutline).FontSize(fsSmall).TextColor(p.honey).Shrink(0)
					} else {
						ui.Spinner(c).Size(fsSmall, fsSmall).TextColor(p.honey).Shrink(0)
					}
					ui.Text(c, target.upgradeStageText(m)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.Text).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
					if target.upgradeCancel != nil {
						if pressable(textButton(c.Key("host-upgrade-cancel"), m.HostUpgradeCancelBtn())).Clicked() {
							if owner.hosts != nil {
								owner.hosts.cancelHostUpgrade(d.editing.ID)
							}
						}
					}
				})
				if target.upgradeStage == remote.StageUpload {
					const barH = 4
					pct := int(math.Round(target.upgradePct * 100))
					if pct < 0 {
						pct = 0
					}
					if pct > 100 {
						pct = 100
					}
					ui.Box(c).Height(barH).Radius(full(barH)).Background(p.wash).Shrink(0).Children(func() {
						if pct > 0 {
							ui.Box(c).Height(barH).Radius(full(barH)).WidthPercent(float32(max(pct, 3))).Background(t.Accent)
						}
					})
				}
			})
			return
		}

		// 检查结果或默认说明（缓存仅在当前实例/版本/构建/tag 仍一致时有效，避免陈旧结果误导）
		var chk *remote.ServerUpgradeCheck
		if target.upgradeCheck != nil && (info == nil || target.upgradeCheckKey == "" || target.upgradeCheckKey == upgradeCheckIdentity(*info)) {
			chk = target.upgradeCheck
		}
		switch {
		case target.upgradeChecking:
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if !c.Preferences().ReduceMotion {
					ui.Spinner(c).Size(fsSmall, fsSmall).TextColor(t.TextMuted).Shrink(0)
				}
				ui.Text(c, m.HostUpgradeChecking()).FontSize(fsSmall).TextColor(t.TextMuted)
			})
		case chk != nil:
			ver := formatUpgradeVersion(chk.TargetVersion, chk.TargetCommit)
			switch {
			case chk.Available:
				msg := m.HostUpgradeAvailableRemote(ver)
				if chk.Source == "local" {
					msg = m.HostUpgradeAvailableLocal(ver)
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(hostStatusDotInner(), hostStatusDotInner()).Radius(full(hostStatusDotInner())).Background(p.sage).Shrink(0)
					ui.Text(c, msg).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.Text).Grow(1).Shrink(1).Wrap()
				})
			case chk.DesktopTooOld:
				ui.Text(c, m.HostUpgradeDesktopTooOld(ver, chk.DesktopVersion)).FontSize(fsSmall).FontFeatures("tnum").TextColor(p.honey).LineHeight(1.5).Wrap()
			default:
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(hostStatusDotInner(), hostStatusDotInner()).Radius(full(hostStatusDotInner())).Background(p.sage).Shrink(0)
					ui.Text(c, m.HostUpgradeUpToDate()).FontSize(fsSmall).TextColor(t.TextMuted)
				})
			}
		default:
			ui.Text(c, m.HostUpgradeHint()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		}

		if target.upgradeNotice != "" {
			ui.Text(c, target.upgradeNotice).FontSize(fsSmall).TextColor(p.sage).LineHeight(1.5).Wrap()
		}
		if target.upgradeCheckErr != "" {
			ui.Text(c, target.upgradeCheckErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
		}
		if target.upgradeErr != "" {
			ui.Text(c, target.upgradeErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
		}

		if !controlled {
			ui.Text(c, m.HostUpgradeViewOnlyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
			return
		}

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			checkDisabled := target.upgradeChecking || info == nil
			if pressable(textButton(c.Key("host-upgrade-check"), m.HostUpgradeCheckBtn()).Disabled(checkDisabled)).Clicked() && !checkDisabled {
				if owner.hosts != nil {
					owner.hosts.checkHostUpgrade(d.editing.ID, *info)
				}
			}
			if chk != nil && chk.Available {
				runDisabled := target.upgradeChecking || autostartBusy
				if pressable(accentButton(c.Key("host-upgrade-run"), m.HostUpgradeRunBtn()).Disabled(runDisabled)).Clicked() && !runDisabled {
					if owner.hosts != nil {
						owner.hosts.upgradeRemote(d.editing.ID)
					}
				}
			}
		})
	})
}

func formatUpgradeVersion(version, commit string) string {
	c := strings.TrimSpace(commit)
	if c != "" && c != "dev" {
		if len(c) > 7 {
			c = c[:7]
		}
		return version + " (" + c + ")"
	}
	return version
}

func (a *app) hostOverviewResourcesSection(c *ui.Context, owner *app, d *hostDialogState, target *app) {
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	ui.Text(c, m.HostOverviewSectionResources()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
	ui.Column(c).Gap(6).Children(func() {
		hostDetailKV(c, m.HostResDaemonMem(), mb(target.daemon.FootprintKB), true)
		total := max(target.daemon.Terminals, target.daemon.Running)
		hostDetailKV(c, m.HostResTerminals(), m.HostResTerminalsCount(target.daemon.Running, total), true)
		switch {
		case d.detail.memUnsupported:
			ui.Text(c, m.HostResLegacyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		case d.detail.loadingMem && d.detail.mem == nil:
			hostDetailKV(c, m.HostResTermsMem(), m.HostDetailLoading(), false)
		case d.detail.memErr != "":
			ui.Text(c, d.detail.memErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
		case d.detail.mem != nil:
			hostDetailKV(c, m.HostResTermsMem(), mb(d.detail.mem.TotalKB), true)
			if len(d.detail.mem.Workspaces) > 0 {
				ui.Column(c).Padding(6, 8).Gap(4).Radius(rRow).Background(p.fill).Children(func() {
					for _, ws := range d.detail.mem.Workspaces {
						ui.Column(c.Key("dm-ws-" + ws.ID)).Gap(2).Children(func() {
							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Icon(c, iconFolder).FontSize(fsSmall).TextColor(t.Accent).Shrink(0)
								ui.Text(c, ws.Label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Text).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
								ui.Text(c, mb(ws.KB)).FontSize(fsSmall).FontWeight(fwMedium).FontFeatures("tnum").TextColor(t.Text).Shrink(0)
							})
							for _, term := range ws.Terminals {
								open := d.detail.memExpanded[term.ID]
								row := ui.Row(c.Key("dm-t-"+term.ID)).Height(sz(24)).Padding(0, 6).Gap(6).Radius(rSmall).AlignItems(ui.Center).Role(ui.RoleButton).Label(term.Title)
								if row.Hovered() {
									row.Background(p.wash)
								}
								row.Children(func() {
									chev := iconChevron
									if open {
										chev = iconDown
									}
									ui.Icon(c, chev).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
									if term.Agent != "" {
										agentTile(c, term.Agent, smallIcon())
									} else {
										ui.Icon(c, iconTerminal).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
									}
									label := term.Title
									if label == "" {
										label = m.TermName()
									}
									ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
									ui.Text(c, mb(term.KB)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
								})
								if row.Clicked() {
									if d.detail.memExpanded == nil {
										d.detail.memExpanded = map[string]bool{}
									}
									d.detail.memExpanded[term.ID] = !open
									if !open && len(term.Procs) == 0 {
										a.reloadHostDetailMemory(owner, d)
									}
								}
								if open {
									if len(term.Procs) == 0 && d.detail.loadingMem {
										ui.Text(c, m.HostDetailLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, 0, 0, 24)
									}
									for i, pr := range term.Procs {
										name := pr.Name
										if pr.Count > 1 {
											name = fmt.Sprintf("%s ×%d", pr.Name, pr.Count)
										}
										ui.Row(c.Key(fmt.Sprintf("dm-p-%s-%d", term.ID, i))).Padding(0, 6, 0, 24).Gap(8).AlignItems(ui.Center).Children(func() {
											ui.Text(c, name).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
											ui.Text(c, mb(pr.KB)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
										})
									}
								}
							}
						})
					}
				})
			}
			if d.detail.mem.Truncated {
				ui.Text(c, m.HostResTruncated()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
			}
		}
	})
}

func (a *app) hostOverviewAutostartSection(c *ui.Context, owner *app, d *hostDialogState, target *app, info *rpc.RemoteInfo) {
	t, p, m := c.Theme(), colors(c.Theme()), L(c)
	ui.Text(c, m.HostOverviewSectionAutostart()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
	if info != nil && len(info.Features) > 0 && !slices.Contains(info.Features, "autostart.v1") {
		ui.Text(c, m.HostAutostartLegacyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}
	if d.detail.autostartUnsupported {
		ui.Text(c, m.HostAutostartLegacyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}
	if d.detail.loadingAutostart && d.detail.autostart == nil {
		ui.Text(c, m.HostDetailLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
		return
	}
	st := d.detail.autostart
	var opState *hostAutostartOp
	if target != nil && target.host != nil && target.autostartOp != nil && target.autostartOp.instanceID == target.host.snapshot().Desktop.InstanceID {
		opState = target.autostartOp
		if opState.status != nil {
			st = opState.status
		}
	}
	autostartBusy := opState != nil && opState.busy
	autostartErr := d.detail.autostartErr
	autostartNotice := ""
	if opState != nil {
		if opState.err != "" {
			autostartErr = opState.err
		}
		autostartNotice = opState.notice
	}

	if st == nil {
		if autostartErr != "" {
			ui.Text(c, autostartErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
		}
		return
	}
	if !st.Supported {
		ui.Text(c, m.HostAutostartUnsupported()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		return
	}

	kindLabel := m.HostAutostartLaunchd()
	if st.Kind == "systemd" {
		kindLabel = m.HostAutostartSystemd()
	}
	statusText := m.HostAutostartOff()
	statusColor := p.faint
	if autostartBusy {
		statusText, statusColor = m.HostAutostartUpdating(), p.honey
	} else if st.Installed {
		switch {
		case st.Kind == "systemd" && st.Active:
			statusText, statusColor = m.HostAutostartTimerActive(), p.sage
		case st.Kind == "launchd" && st.Active:
			statusText, statusColor = m.HostAutostartLoaded(), p.sage
		case st.Enabled:
			statusText, statusColor = m.HostAutostartOn(), p.sage
		default:
			statusText, statusColor = m.HostAutostartInstalledPending(), p.honey
		}
	}

	controlled := target != nil && target.host != nil && target.host.snapshot().Controlled
	upgrading := target != nil && target.upgrading.Load()
	// 仅远端明确宣告写能力（autostart.write.v1 + st.Writable）时开放开关，兼容只读的一期旧无头节点。
	writable := st.Writable && st.Managed && !d.detail.autostartWriteUnsupported && (info == nil || len(info.Features) == 0 || slices.Contains(info.Features, "autostart.write.v1"))
	canToggle := writable && controlled && !upgrading && !autostartBusy && !d.detail.loadingAutostart && d.detail.autostartErr == ""
	// 无活跃 bus 或 Aqua 会话下已写入 unit/plist 文件时仍视为待启用态，允许用户切回关闭以清理文件。
	on := st.Installed && (st.Enabled || st.NoteCode == rpc.AutostartNoteSystemdBusInactive || st.NoteCode == rpc.AutostartNoteAquaInactive)

	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, kindLabel).FontSize(fsBody).TextColor(t.Text).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
			ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Box(c).Size(hostStatusDotInner(), hostStatusDotInner()).Radius(full(hostStatusDotInner())).Background(statusColor).Shrink(0)
				ui.Text(c, statusText).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
			})
			if writable {
				sw := ui.Switch(c.Key("host-autostart-switch"), &on).Label(kindLabel).Disabled(!canToggle)
				if sw.Changed() && canToggle {
					a.toggleHostAutostart(owner, d, target, on)
				}
			}
		})
		if autostartErr != "" {
			ui.Text(c, autostartErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
		}
		if autostartNotice != "" {
			ui.Text(c, autostartNotice).FontSize(fsSmall).TextColor(p.honey).LineHeight(1.5).Wrap()
		}
		if st.Path != "" {
			a.hostDetailCopyRow(c, d, "autostart-path", m.HostAutostartFieldPath(), st.Path)
		}
		if st.ExpectedExecutable != "" {
			hostDetailKV(c, m.HostAutostartFieldExpectedExe(), st.ExpectedExecutable, true)
		}
		switch {
		case writable && !controlled:
			ui.Text(c, m.HostAutostartReadOnlyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		case writable:
			ui.Text(c, m.HostAutostartToggleNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		case st.Managed:
			ui.Text(c, m.HostAutostartLegacyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		case info != nil && info.Edition != "headless":
			ui.Text(c, m.HostAutostartFullNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		default:
			ui.Text(c, m.HostAutostartCustomNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
		}
		switch st.NoteCode {
		case rpc.AutostartNoteAquaInactive:
			ui.Text(c, m.HostAutostartNoteAqua()).FontSize(fsSmall).TextColor(p.honey).LineHeight(1.5).Wrap()
		case rpc.AutostartNoteSystemdBusInactive:
			ui.Text(c, m.HostAutostartNoteSystemdBus()).FontSize(fsSmall).TextColor(p.honey).LineHeight(1.5).Wrap()
		case rpc.AutostartNoteSystemdLingerDisabled:
			ui.Column(c).Padding(8, 12).Gap(6).Radius(rRow).Background(soft(t, p.honey)).Children(func() {
				ui.Text(c, m.HostAutostartNoteLinger()).FontSize(fsSmall).TextColor(t.Text).LineHeight(1.5).Wrap()
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, linuxLingerCommand).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Grow(1).Shrink(1).Wrap()
					btnLabel := m.HostAutostartCopyLinger()
					if d.detail.copiedKey == "linger" && time.Since(d.detail.copiedAt) < 2*time.Second {
						btnLabel = m.HostDetailCopied()
						c.After(2*time.Second - time.Since(d.detail.copiedAt))
					}
					if pressable(textButton(c.Key("copy-linger"), btnLabel)).Clicked() {
						c.WriteClipboard(linuxLingerCommand)
						d.detail.copiedKey, d.detail.copiedAt = "linger", time.Now()
					}
				})
			})
		}
	})
}

func hostDetailKV(c *ui.Context, label, value string, mono bool) {
	t := c.Theme()
	ui.Row(c).Gap(8).AlignItems(ui.Start).Children(func() {
		ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).Width(sz(88)).Shrink(0)
		v := ui.Text(c, value).FontSize(fsSmall).TextColor(t.Text).Grow(1).Shrink(1).Wrap()
		if mono {
			v.Font(monoFont).FontFeatures("tnum")
		}
	})
}

func (a *app) hostDetailCopyRow(c *ui.Context, d *hostDialogState, key, label, value string) {
	t, m := c.Theme(), L(c)
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).Width(sz(88)).Shrink(0)
		ui.Text(c, value).FontSize(fsSmall).Font(monoFont).TextColor(t.Text).Grow(1).Shrink(1).Wrap()
		tip := m.CommonCopyPath()
		if d.detail.copiedKey == key && time.Since(d.detail.copiedAt) < 2*time.Second {
			tip = m.HostDetailCopied()
			c.After(2*time.Second - time.Since(d.detail.copiedAt))
		}
		if pressable(iconButtonSized(c.Key("copy-"+key), iconCopy, tip, compactBox(), smallIcon())).Clicked() {
			c.WriteClipboard(value)
			d.detail.copiedKey, d.detail.copiedAt = key, time.Now()
		}
	})
}

func hostPlatformLabel(goos, arch string) string {
	osName := goos
	switch goos {
	case "darwin":
		osName = "macOS"
	case "linux":
		osName = "Linux"
	}
	if arch != "" {
		return osName + " (" + arch + ")"
	}
	return osName
}

func (a *app) toggleHostAutostart(owner *app, d *hostDialogState, target *app, enabled bool) {
	if target == nil || target.upgrading.Load() {
		return
	}
	r := target.request()
	if !r.canWrite() || r.client == nil {
		return
	}
	inst := r.snapshot.Desktop.InstanceID
	if target.autostartOp != nil && target.autostartOp.instanceID == inst && target.autostartOp.busy {
		return
	}
	op, err := r.client.PrepareSetAutostart(enabled)
	if err != nil {
		d.detail.autostartErr = errtext.Text(err)
		return
	}
	// 写操作立刻推进共享代次，使所有更早发出的只读状态查询回包失效；操作状态挂在 target 上，关弹窗/切页不遗忘。
	target.autostartGen++
	writeGen := target.autostartGen
	state := &hostAutostartOp{
		instanceID: inst,
		busy:       true,
	}
	target.autostartOp = state
	d.detail.autostartErr = ""
	winCtx := owner.windowContext()
	m := appL()

	go func() {
		execCtx, execCancel := context.WithTimeout(winCtx, 35*time.Second)
		var out rpc.AutostartStatus
		err := op.Execute(execCtx, &out)
		execCancel()

		var notice string
		var finalSt *rpc.AutostartStatus
		var writeUnsupported bool
		if err == nil {
			cp := out
			finalSt = &cp
		} else {
			var re *rpc.Error
			switch {
			case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
				writeUnsupported = true
				err = nil
			case errors.As(err, &re) && re.Code == rpc.CodeOutcomeUnknown:
				// 结果未知：始终使用 Prepare 固定的原 ref，在共用 30s 预算内重试临时查询错误并最终以 status 对账，绝不自动重发。
				ref := op.Ref()
				resolved := false
				pollCtx, pollCancel := context.WithTimeout(winCtx, 30*time.Second)
				for pollCtx.Err() == nil {
					qctx, qcancel := context.WithTimeout(pollCtx, 4*time.Second)
					opSt, qerr := r.client.DesktopOperationStatus(qctx, ref)
					qcancel()
					if qerr != nil {
						var qre *rpc.Error
						if errors.As(qerr, &qre) && (qre.Code == rpc.CodeUnknown || qre.Code == rpc.CodeBadRequest || qre.Code == rpc.CodeReadOnly) {
							break
						}
						if pollCtx.Err() != nil {
							break
						}
						select {
						case <-pollCtx.Done():
						case <-time.After(time.Second):
						}
						continue
					}
					if opSt.Method != "" && opSt.Method != "server.autostart.set" {
						break
					}
					if opSt.State == "running" {
						select {
						case <-pollCtx.Done():
						case <-time.After(time.Second):
						}
						continue
					}
					if opSt.State == "done" {
						if opSt.Error != nil {
							err = opSt.Error
						} else if len(opSt.Result) > 0 && json.Unmarshal(opSt.Result, &out) == nil {
							cp := out
							finalSt = &cp
							err = nil
						}
						resolved = true
					}
					break
				}
				pollCancel()
				sctx, scancel := context.WithTimeout(winCtx, 8*time.Second)
				if fresh, ferr := r.client.AutostartStatus(sctx); ferr == nil {
					cp := fresh
					finalSt = &cp
					if !resolved {
						err = nil
						notice = m.HostAutostartOutcomeUnknown()
					}
				}
				scancel()
			}
		}

		owner.update(func() {
			if target.autostartOp != state {
				return
			}
			state.busy = false
			if target.autostartGen != writeGen {
				return
			}
			if finalSt != nil {
				state.status = finalSt
			}
			state.notice = notice
			if err != nil {
				state.err = errtext.Text(err)
			} else {
				state.err = ""
			}
			if owner.hostDlg.open && owner.hostDlg.editing.ID == target.viewID {
				if writeUnsupported {
					owner.hostDlg.detail.autostartWriteUnsupported = true
				}
				if finalSt != nil {
					owner.hostDlg.detail.autostart = finalSt
				}
				owner.hostDlg.detail.autostartErr = state.err
			}
		})
	}()
}

func formatHostUptime(m locales.Messages, d time.Duration) string {
	if d < time.Minute {
		return m.HostUptimeSeconds()
	}
	totalMins := int(d / time.Minute)
	days := totalMins / (24 * 60)
	hours := (totalMins % (24 * 60)) / 60
	mins := totalMins % 60
	if days > 0 {
		return m.HostUptimeDays(days, hours)
	}
	if hours > 0 {
		return m.HostUptimeHours(hours, mins)
	}
	return m.HostUptimeMins(mins)
}
