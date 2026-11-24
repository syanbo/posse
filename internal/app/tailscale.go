package app

import (
	"context"
	"errors"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/internal/tslink"
	"posse/locales"
)

// tailscaleView 是手机页「内置 Tailscale」卡片的状态：
// 独立管理开关、登录链接、高级设置（节点名、ControlURL、一次性 AuthKey、代理兼容开关）和重置身份。
type tailscaleView struct {
	st       tslink.Status
	loaded   bool
	loading  bool
	loadedAt time.Time
	old      bool   // 常驻进程不认 tailscale.status（旧版）：不显示卡片，也不再问
	busy     bool   // 正在执行 set / login / reset
	opSeq    uint64 // 每次变更操作加一，防止旧轮询结果覆盖最新操作状态
	err      string

	advanced    bool   // 高级设置展开状态
	draftInit   bool   // 是否已从常驻进程同步过一次草稿
	draftDirty  bool   // 用户是否手动改过草稿（未保存前不被后台轮询覆盖）
	draftSeq    uint64 // 每次用户修改高级草稿加一，避免在途保存/重置回包覆盖新编辑
	hostname    string // 节点名草稿
	controlURL  string // Headscale / 自定义控制服务器地址草稿
	authKey     string // 一次性认证密钥，提交后立即清空
	disableBind bool   // 关闭物理网卡绑定，允许走系统代理/VPN
}

const (
	// tailscalePollFast 在启动中或等待登录/审批时快速轮询，让登录完成立刻反映到界面和二维码。
	tailscalePollFast = 2 * time.Second
	// tailscalePollSlow 在已连接时慢速轮询，同步 IP 变更或命令行操作。
	tailscalePollSlow = 15 * time.Second
	// tailscalePollIdle 在未开启时低频轮询，同步命令行开启操作。
	tailscalePollIdle = 30 * time.Second
	// tailscaleRetry 在手机页刚打开、初次查询失败（如常驻进程正在交接）时的重试间隔。
	tailscaleRetry = 5 * time.Second
)

// openExternalURL 在默认浏览器中打开外链；测试可替换以拦截系统命令。
// 在独立协程中运行 Run() 既可回收子进程，也能把非零退出状态反馈给界面。
var openExternalURL = func(raw string) error {
	return exec.Command("open", raw).Run()
}

func (v *tailscaleView) runningIP() string {
	if !v.loaded || v.old || v.st.State != tslink.StateRunning {
		return ""
	}
	return strings.TrimSpace(v.st.IP)
}

func (v *tailscaleView) pollInterval() time.Duration {
	switch v.st.State {
	case tslink.StateRunning:
		return tailscalePollSlow
	case tslink.StateDisabled:
		if v.st.Enabled {
			return tailscalePollFast
		}
		return tailscalePollIdle
	default:
		if v.st.Enabled {
			return tailscalePollFast
		}
		return tailscalePollIdle
	}
}

func (a *app) applyTailscaleStatus(st tslink.Status, syncDraft bool) {
	ts := &a.mobile.tailscale
	prevRunning := ts.st.State == tslink.StateRunning
	prevIP := strings.TrimSpace(ts.st.IP)
	ts.st = st
	ts.loaded = true
	ts.loadedAt = time.Now()
	ts.old = false
	if syncDraft || !ts.draftInit || !ts.draftDirty {
		ts.hostname = st.Hostname
		ts.controlURL = st.ControlURL
		ts.disableBind = st.DisableBindInterface
		ts.draftInit = true
		if syncDraft {
			ts.draftDirty = false
		}
	}
	nowRunning := st.State == tslink.StateRunning
	nowIP := strings.TrimSpace(st.IP)

	// 仅对明确选中内置 Tailscale 来源的地址（MobileAddressBuiltIn）做自动跟随或回退；
	// 手工输入的自定义地址（builtIn=false）即使碰巧与内置 IP 相同也绝不改写。
	addrUpdated := false
	builtInInvalidated := false
	if a.state.MobileAddressBuiltIn {
		switch {
		case nowRunning && nowIP != "":
			if a.state.MobileAddress != nowIP {
				a.state.MobileAddress = nowIP
				a.saveState()
				addrUpdated = true
				builtInInvalidated = true
			}
		default:
			if a.state.MobileAddress != "" || a.state.MobileAddressBuiltIn {
				a.state.MobileAddress = ""
				a.state.MobileAddressBuiltIn = false
				a.saveState()
				addrUpdated = true
				builtInInvalidated = true
			}
		}
	}

	if builtInInvalidated {
		// 原配对码仅包含已失效的内置 IP：立刻清掉旧码，并按非静默方式刷新（无可用地址时能报错提示）
		a.mobile.url, a.mobile.code, a.mobile.endpoint, a.mobile.qr = "", "", "", nil
	}

	if prevRunning != nowRunning || prevIP != nowIP || addrUpdated {
		a.requestMobilePairRefresh(builtInInvalidated)
	}
}

// markTailscaleOld 在任一 RPC 返回 CodeUnknown 时统一降级：清空缓存状态、隐藏卡片、停止轮询，
// 若选中的是内置 Tailscale 地址则清掉旧码、回退到自动并按非静默方式刷新配对码。
func (a *app) markTailscaleOld() {
	ts := &a.mobile.tailscale
	ts.old = true
	ts.loaded = false
	ts.st = tslink.Status{State: tslink.StateDisabled}
	if a.state.MobileAddressBuiltIn {
		a.state.MobileAddress = ""
		a.state.MobileAddressBuiltIn = false
		a.saveState()
		a.mobile.url, a.mobile.code, a.mobile.endpoint, a.mobile.qr = "", "", "", nil
		a.requestMobilePairRefresh(true)
	}
}

func (a *app) loadTailscale() {
	ts := &a.mobile.tailscale
	if ts.loading || ts.busy || a.c == nil {
		return
	}
	ts.loading = true
	seq := ts.opSeq
	go func() {
		var st tslink.Status
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "tailscale.status", nil, &st)
		a.update(func() {
			ts := &a.mobile.tailscale
			ts.loading = false
			if ts.opSeq != seq {
				// 期间有新的变更操作，丢弃本次过期的状态轮询
				return
			}
			ts.loadedAt = time.Now()
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					a.markTailscaleOld()
				} else {
					// 瞬态失败（如常驻进程正在交接升级）解除 old 标记，允许页内重试发现新版
					ts.old = false
				}
				return
			}
			a.applyTailscaleStatus(st, false)
		})
	}()
}

func (a *app) pollTailscale(c *ui.Context) {
	ts := &a.mobile.tailscale
	if ts.loading || ts.busy || ts.old || a.c == nil {
		return
	}
	if wait := ts.pollInterval() - time.Since(ts.loadedAt); wait > 0 {
		c.After(wait)
		return
	}
	a.loadTailscale()
}

func (a *app) toggleTailscale(m locales.Messages, on bool) {
	ts := &a.mobile.tailscale
	if ts.busy || a.c == nil {
		return
	}
	ts.busy = true
	ts.err = ""
	ts.opSeq++
	go func() {
		var st tslink.Status
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "tailscale.set", map[string]any{"enabled": on}, &st)
		a.update(func() {
			ts := &a.mobile.tailscale
			ts.busy = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					a.markTailscaleOld()
				}
				ts.err = tailscaleErrText(m, err)
				return
			}
			a.applyTailscaleStatus(st, false)
		})
	}()
}

func (a *app) saveTailscaleAdvanced(m locales.Messages) {
	ts := &a.mobile.tailscale
	if ts.busy || a.c == nil {
		return
	}
	hostname := strings.TrimSpace(ts.hostname)
	controlURL := strings.TrimSpace(ts.controlURL)
	rawAuthKey := ts.authKey
	authKey := strings.TrimSpace(rawAuthKey)
	disableBind := ts.disableBind
	saveDraftSeq := ts.draftSeq

	params := map[string]any{
		"hostname":             hostname,
		"controlUrl":           controlURL,
		"disableBindInterface": disableBind,
	}
	if authKey != "" {
		params["authKey"] = authKey
		// 填了 AuthKey 但当前未开启时，一并开启让一次性密钥立即生效
		if !ts.st.Enabled {
			params["enabled"] = true
		}
	}

	ts.busy = true
	ts.err = ""
	ts.opSeq++
	go func() {
		var st tslink.Status
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "tailscale.set", params, &st)
		a.update(func() {
			ts := &a.mobile.tailscale
			ts.busy = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					a.markTailscaleOld()
				}
				ts.err = tailscaleErrText(m, err)
				return
			}
			unchanged := ts.draftSeq == saveDraftSeq
			if unchanged || ts.authKey == rawAuthKey {
				ts.authKey = ""
			}
			a.applyTailscaleStatus(st, unchanged)
		})
	}()
}

func (a *app) loginTailscale(m locales.Messages) {
	ts := &a.mobile.tailscale
	if ts.busy || a.c == nil {
		return
	}
	ts.busy = true
	ts.err = ""
	ts.opSeq++
	go func() {
		var st tslink.Status
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "tailscale.login", nil, &st)
		a.update(func() {
			ts := &a.mobile.tailscale
			ts.busy = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					a.markTailscaleOld()
				}
				ts.err = tailscaleErrText(m, err)
				return
			}
			a.applyTailscaleStatus(st, false)
		})
	}()
}

func (a *app) resetTailscale(m locales.Messages) {
	ts := &a.mobile.tailscale
	if ts.busy || a.c == nil {
		return
	}
	a.confirm(m.TailscaleResetConfirm(), m.TailscaleResetBody(), m.TailscaleReset(), func() {
		type resetPermit struct {
			ok       bool
			draftSeq uint64
			authKey  string
		}
		permitCh := make(chan resetPermit, 1)
		a.update(func() {
			ts := &a.mobile.tailscale
			if ts.busy || a.c == nil {
				permitCh <- resetPermit{}
				return
			}
			ts.busy = true
			ts.err = ""
			ts.opSeq++
			permitCh <- resetPermit{ok: true, draftSeq: ts.draftSeq, authKey: ts.authKey}
		})
		permit := <-permitCh
		if !permit.ok {
			return
		}
		var res struct {
			Status       tslink.Status `json:"status"`
			LoggedOut    bool          `json:"loggedOut"`
			LogoutError  string        `json:"logoutError"`
			LocalCleared bool          `json:"localCleared"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "tailscale.reset", nil, &res)
		a.update(func() {
			ts := &a.mobile.tailscale
			ts.busy = false
			if err != nil {
				var re *rpc.Error
				if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
					a.markTailscaleOld()
				}
				ts.err = tailscaleErrText(m, err)
				return
			}
			unchanged := ts.draftSeq == permit.draftSeq
			if unchanged || ts.authKey == permit.authKey {
				ts.authKey = ""
			}
			a.applyTailscaleStatus(res.Status, unchanged)
			if res.LogoutError != "" {
				ts.err = m.TailscaleResetLocalOnly(errtext.Message(res.LogoutError))
			}
		})
	})
}

func (a *app) openTailscaleAuthURL(m locales.Messages, raw string) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		a.mobile.tailscale.err = m.TailscaleInvalidAuthURL()
		return
	}
	a.mobile.tailscale.err = ""
	target := u.String()
	go func() {
		if err := openExternalURL(target); err != nil {
			a.update(func() {
				a.mobile.tailscale.err = errtext.Text(err)
			})
		}
	}()
}

var tailscaleStates = map[tslink.State]struct{ label, note func(locales.Messages) string }{
	tslink.StateStarting:         {locales.Messages.TailscaleStarting, locales.Messages.TailscaleStartingNote},
	tslink.StateNeedsLogin:       {locales.Messages.TailscaleNeedsLogin, locales.Messages.TailscaleNeedsLoginNote},
	tslink.StateNeedsMachineAuth: {locales.Messages.TailscaleNeedsApproval, locales.Messages.TailscaleNeedsApprovalNote},
	tslink.StateRunning:          {locales.Messages.TailscaleRunning, locales.Messages.TailscaleRunningNote},
	tslink.StateStopped:          {locales.Messages.TailscaleStopped, locales.Messages.TailscaleStoppedNote},
	tslink.StateError:            {locales.Messages.TailscaleError, locales.Messages.TailscaleStoppedNote},
}

// mobileTailscaleCard 是「内置 Tailscale」卡片：一键开关内置节点、引导浏览器登录、展示 100.x IP 与高级设置。
// 常驻进程是旧版、问不到状态时不显示。
func (a *app) mobileTailscaleCard(c *ui.Context) {
	ts := &a.mobile.tailscale
	if !ts.loaded {
		if !ts.old && !ts.loading && a.c != nil {
			if wait := tailscaleRetry - time.Since(ts.loadedAt); wait > 0 {
				c.After(wait)
			} else {
				a.loadTailscale()
			}
		}
		return
	}
	a.pollTailscale(c)
	t := c.Theme()
	m := L(c)
	st := ts.st
	stateInfo, known := tailscaleStates[st.State]

	// 与手机页同栏卡片（mobile.go、relay.go）保持 20px 内边距，保证标题与输入边缘对齐
	card(c).Padding(20).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			// 标题行 + 总开关
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				title := m.TailscaleTitle()
				if (st.Enabled || st.State != tslink.StateDisabled) && known {
					title = m.TailscaleTitleState(stateInfo.label(m))
				}
				cardTitle(c, title)
				ui.Spacer(c)
				on := st.Enabled
				if ui.Switch(c.Key("tailscale-enable"), &on).Label(m.TailscaleTitle()).Disabled(ts.busy).Changed() {
					a.toggleTailscale(m, on)
				}
			})

			// 主体状态
			switch st.State {
			case tslink.StateRunning:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(7, 7).Radius(full(7)).Background(colors(t).sage)
					if st.IP != "" {
						ui.Text(c, st.IP).FontSize(fsBody).Font(monoFont).Shrink(0)
					}
					nodeName := st.DNSName
					if nodeName == "" {
						nodeName = st.Hostname
					}
					if nodeName != "" {
						ui.Text(c, nodeName).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Grow(1).Shrink(1)
					} else {
						ui.Spacer(c)
					}
					if st.IP != "" && pressable(textButton(c.Key("ts-copy-ip"), m.CommonCopy())).Clicked() {
						c.WriteClipboard(st.IP)
					}
				})
				ui.Text(c, m.TailscaleRunningNote()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)

			case tslink.StateNeedsLogin:
				ui.Text(c, m.TailscaleNeedsLoginNote()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					if st.AuthURL != "" {
						if pressable(accentButton(c.Key("ts-open-login"), m.TailscaleOpenLogin())).Tooltip(m.TailscaleOpenLoginTip()).Clicked() {
							a.openTailscaleAuthURL(m, st.AuthURL)
						}
						if pressable(textButton(c.Key("ts-copy-login"), m.TailscaleCopyLoginURL())).Clicked() {
							c.WriteClipboard(st.AuthURL)
						}
					}
					if pressable(textButton(c.Key("ts-refresh-login"), m.TailscaleRefreshLogin())).Disabled(ts.busy).Clicked() {
						a.loginTailscale(m)
					}
				})

			case tslink.StateStarting, tslink.StateNeedsMachineAuth:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(7, 7).Radius(full(7)).Background(colors(t).honey)
					ui.Text(c, stateInfo.note(m)).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5).Grow(1).Shrink(1)
				})

			case tslink.StateDisabled:
				if st.Enabled {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(7, 7).Radius(full(7)).Background(colors(t).honey)
						ui.Text(c, m.TailscaleStartingNote()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5).Grow(1).Shrink(1)
					})
				} else {
					ui.Text(c, m.TailscaleNote()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				}

			default:
				if known {
					ui.Text(c, stateInfo.note(m)).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				} else {
					ui.Text(c, m.TailscaleNote()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				}
			}

			// 错误提示（操作报错优先于后台状态 Error）
			errMsg := ts.err
			if errMsg == "" && st.Error != "" {
				errMsg = errtext.Message(st.Error)
			}
			if errMsg != "" {
				ui.Text(c, errMsg).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45)
			}

			// 高级设置折叠区
			a.tailscaleAdvancedSection(c)
		})
	})
}

func (a *app) tailscaleAdvancedSection(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	ts := &a.mobile.tailscale

	hline(c)
	advRow := ui.Row(c.Key("ts-advanced-toggle")).Padding(4, 0).Gap(8).AlignItems(ui.Center)
	if advRow.Clicked() {
		ts.advanced = !ts.advanced
	}
	advRow.Children(func() {
		ui.Text(c, m.TailscaleAdvanced()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Spacer(c)
		label := m.MobileExpand()
		if ts.advanced {
			label = m.MobileCollapse()
		}
		ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted)
	})

	if !ts.advanced {
		return
	}

	ui.Column(c).Gap(12).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, m.TailscaleHostname()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			in := fieldInput(c.Key("ts-hostname"), &ts.hostname, m.TailscaleHostnamePlaceholder()).Disabled(ts.busy)
			if in.Changed() {
				ts.draftDirty = true
				ts.draftSeq++
			}
			if in.Submitted() {
				a.saveTailscaleAdvanced(m)
			}
		})

		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, m.TailscaleControlURL()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			in := fieldInput(c.Key("ts-control-url"), &ts.controlURL, m.TailscaleControlURLPlaceholder()).Disabled(ts.busy)
			if in.Changed() {
				ts.draftDirty = true
				ts.draftSeq++
			}
			if in.Submitted() {
				a.saveTailscaleAdvanced(m)
			}
		})

		ui.Column(c).Gap(4).Children(func() {
			ui.Text(c, m.TailscaleAuthKey()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			in := fieldInput(c.Key("ts-auth-key"), &ts.authKey, m.TailscaleAuthKeyPlaceholder()).Password().Disabled(ts.busy)
			if in.Changed() {
				ts.draftSeq++
			}
			if in.Submitted() {
				a.saveTailscaleAdvanced(m)
			}
			ui.Text(c, m.TailscaleAuthKeyNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
		})

		ui.Column(c).Gap(4).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, m.TailscaleDisableBind()).FontSize(fsBody).Grow(1).Shrink(1)
				if ui.Switch(c.Key("ts-disable-bind"), &ts.disableBind).Label(m.TailscaleDisableBind()).Disabled(ts.busy).Changed() {
					ts.draftDirty = true
					ts.draftSeq++
				}
			})
			ui.Text(c, m.TailscaleDisableBindNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
		})

		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if pressable(textButton(c.Key("ts-save"), m.CommonSave())).Disabled(ts.busy).Clicked() {
				a.saveTailscaleAdvanced(m)
			}
			ui.Spacer(c)
			if pressable(textButton(c.Key("ts-reset"), m.TailscaleReset())).Tooltip(m.TailscaleResetTip()).Disabled(ts.busy).Clicked() {
				a.resetTailscale(m)
			}
		})
	})
}

func tailscaleErrText(m locales.Messages, err error) string {
	var re *rpc.Error
	switch {
	case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
		return m.TailscaleOldDaemon()
	case errors.As(err, &re):
		return errtext.Message(re.Message)
	}
	return errtext.Text(err)
}
