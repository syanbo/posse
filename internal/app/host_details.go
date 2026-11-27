package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/locales"
)

const (
	hostTabOverview = 0
	hostTabSettings = 1
)

const linuxLingerCommand = "loginctl enable-linger $USER"

type hostDetailState struct {
	ctx                  context.Context
	cancel               context.CancelFunc
	memCancel            context.CancelFunc
	memSeq               uint64
	liveKey              string
	fetchedGen           uint64
	loadingInfo          bool
	loadingAutostart     bool
	loadingMem           bool
	info                 *rpc.RemoteInfo
	infoErr              string
	autostart            *rpc.AutostartStatus
	autostartErr         string
	autostartUnsupported bool
	mem                  *rpc.DaemonMemoryResult
	memErr               string
	memUnsupported       bool
	memExpanded          map[string]bool
	copiedKey            string
	copiedAt             time.Time
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

	// 概览打开期间若完成连接、切换到当前主机或更换实例/连接代次，自动拉取最新详情；离开或断开时立即取消在途请求。
	if isActive && connected && target != nil {
		if key := detailLiveKey(h.ID, target); key != "" && key != d.detail.liveKey {
			a.refreshHostDetails(owner, d)
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

	// 二、资源占用（仅活动且已连接时拉取实时终端明细；否则展示缓存摘要）
	if isActive && connected && target != nil {
		hline(c)
		a.hostOverviewResourcesSection(c, owner, d, target)
		hline(c)
		a.hostOverviewAutostartSection(c, owner, d, info)
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
		if isActive && connected {
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

func (a *app) hostOverviewAutostartSection(c *ui.Context, owner *app, d *hostDialogState, info *rpc.RemoteInfo) {
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
	if d.detail.autostartErr != "" {
		ui.Text(c, d.detail.autostartErr).FontSize(fsSmall).TextColor(t.Danger).Wrap()
		return
	}
	st := d.detail.autostart
	if st == nil {
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
	if st.Installed {
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

	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, kindLabel).FontSize(fsBody).TextColor(t.Text).Grow(1).Shrink(1).SingleLine().Ellipsis("…")
			ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Box(c).Size(hostStatusDotInner(), hostStatusDotInner()).Radius(full(hostStatusDotInner())).Background(statusColor).Shrink(0)
				ui.Text(c, statusText).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
			})
		})
		if st.Path != "" {
			a.hostDetailCopyRow(c, d, "autostart-path", m.HostAutostartFieldPath(), st.Path)
		}
		if st.ExpectedExecutable != "" {
			hostDetailKV(c, m.HostAutostartFieldExpectedExe(), st.ExpectedExecutable, true)
		}
		if !st.Managed && info != nil && info.Edition != "headless" {
			ui.Text(c, m.HostAutostartFullNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Wrap()
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
