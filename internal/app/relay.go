package app

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/locales"
)

// relayView 是手机页「外网中转」一栏：手机不在同一个 Wi-Fi 时经中转连回这台电脑。
// 界面不显示中转地址，只显示能不能用；地址和密钥都在邀请码里。
type relayView struct {
	st       relayStatus
	loaded   bool
	loading  bool
	loadedAt time.Time
	busy     bool   // 正在使用邀请码或生成邀请码
	code     string // 正在粘贴的邀请码
	err      string
	name     string // 管理员正在输入的被邀请人名字
	invite   string // 刚生成的邀请码
}

type relayStatus struct {
	Configured bool   `json:"configured"`
	Admin      bool   `json:"admin"`
	Name       string `json:"name"`
	State      string `json:"state"`
}

// relayPoll 是手机页开着时重新检查中转的间隔。
const relayPoll = 30 * time.Second

func (a *app) loadRelay() {
	r := &a.mobile.relay
	if r.loading {
		return
	}
	r.loading = true
	go func() {
		var st relayStatus
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "relay.status", nil, &st)
		a.update(func() {
			r := &a.mobile.relay
			r.loading, r.loadedAt = false, time.Now()
			if err != nil {
				return
			}
			r.st, r.loaded = st, true
		})
	}()
}

func (a *app) pollRelay(c *ui.Context) {
	r := &a.mobile.relay
	if r.loading || r.busy {
		return
	}
	if wait := relayPoll - time.Since(r.loadedAt); wait > 0 {
		c.After(wait)
		return
	}
	a.loadRelay()
}

// useRelayCode 用邀请码（或服务器给的管理员码），出错显示在这一栏里。
func (a *app) useRelayCode(m locales.Messages, code string) {
	r := &a.mobile.relay
	if code == "" || r.busy {
		return
	}
	r.busy, r.err = true, ""
	go func() {
		var st relayStatus
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "relay.set", map[string]any{"code": code}, &st)
		a.update(func() {
			r := &a.mobile.relay
			r.busy = false
			if err != nil {
				r.err = relayErrText(m, err)
				return
			}
			r.st, r.loaded, r.loadedAt, r.code = st, true, time.Now(), ""
		})
	}()
}

func (a *app) removeRelay(m locales.Messages) {
	body := m.RelayRemoveBody()
	if a.mobile.relay.st.Admin {
		body = m.RelayRemoveBodyAdmin()
	}
	a.confirm(m.RelayRemoveConfirm(), body, m.CommonRemove(), func() {
		var st relayStatus
		if a.call("relay.clear", nil, &st, 10*time.Second) != nil {
			return
		}
		a.update(func() {
			r := &a.mobile.relay
			r.st, r.loadedAt, r.invite, r.err = st, time.Now(), "", ""
		})
	})
}

func (a *app) makeInvite(m locales.Messages, name string) {
	r := &a.mobile.relay
	if name == "" || r.busy {
		return
	}
	r.busy, r.err, r.invite = true, "", ""
	go func() {
		var out struct {
			Code string `json:"code"`
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := a.c.Call(ctx, "relay.invite", map[string]any{"name": name}, &out)
		a.update(func() {
			r := &a.mobile.relay
			r.busy = false
			if err != nil {
				r.err = relayErrText(m, err)
				return
			}
			r.invite, r.name = out.Code, ""
		})
	}()
}

// relayStates 是每种状态的标题后缀和说明。
var relayStates = map[string]struct{ label, note func(locales.Messages) string }{
	"ready":       {locales.Messages.RelayReady, locales.Messages.RelayReadyNote},
	"denied":      {locales.Messages.RelayDenied, locales.Messages.RelayDeniedNote},
	"revoked":     {locales.Messages.RelayRevoked, locales.Messages.RelayRevokedNote},
	"clock":       {locales.Messages.RelayClock, locales.Messages.RelayClockNote},
	"unreachable": {locales.Messages.RelayUnreachable, locales.Messages.RelayUnreachableNote},
}

// mobileRelayCard 是「外网中转」：没配时贴邀请码，配了显示能不能用；管理员还能给别人生成邀请码。
// 常驻进程是旧版、问不到状态时不显示。
func (a *app) mobileRelayCard(c *ui.Context) {
	r := &a.mobile.relay
	if !r.loaded {
		return
	}
	a.pollRelay(c)
	t := c.Theme()
	st := r.st
	state, known := relayStates[st.State]
	card(c).Padding(20).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				title := L(c).RelayTitle()
				if st.Configured && known {
					title = L(c).RelayTitleState(state.label(L(c)))
				}
				cardTitle(c, title)
				ui.Spacer(c)
				if st.Configured && pressable(textButton(c, L(c).CommonRemove())).Tooltip(L(c).RelayRemoveTip()).Clicked() {
					a.removeRelay(L(c))
				}
			})
			if !st.Configured {
				ui.Text(c, L(c).RelayNote()).
					FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Grow(1).Shrink(1).Children(func() {
						if fieldInput(c, &r.code, L(c).RelayCodePlaceholder()).Submitted() {
							a.useRelayCode(L(c), r.code)
						}
					})
					if pressable(textButton(c, L(c).MobileUse())).Clicked() {
						a.useRelayCode(L(c), r.code)
					}
				})
			} else if known {
				ui.Text(c, state.note(L(c))).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			}
			if r.err != "" {
				ui.Text(c, r.err).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45)
			}
			if st.Admin {
				a.relayInviteRow(c)
			}
		})
	})
}

// relayInviteRow 是管理员给别人生成邀请码：填名字（在服务器上停用某个人时用），生成后复制发给对方。
func (a *app) relayInviteRow(c *ui.Context) {
	t := c.Theme()
	r := &a.mobile.relay
	hline(c)
	ui.Column(c).Gap(6).Children(func() {
		ui.Text(c, L(c).RelayInviteTitle()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).Shrink(1).Children(func() {
				if fieldInput(c, &r.name, L(c).RelayInviteNamePlaceholder()).Submitted() {
					a.makeInvite(L(c), r.name)
				}
			})
			if pressable(textButton(c, L(c).RelayInviteCreate())).Clicked() {
				a.makeInvite(L(c), r.name)
			}
		})
		if r.invite != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, r.invite).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Grow(1).Shrink(1)
				if pressable(textButton(c, L(c).CommonCopy())).Clicked() {
					c.WriteClipboard(r.invite)
				}
			})
		}
		ui.Text(c, L(c).RelayInviteNote()).
			FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
	})
}

// relayErrText 是显示在这一栏里的报错：常驻进程的报错只取说明，不带错误码。
func relayErrText(m locales.Messages, err error) string {
	var re *rpc.Error
	switch {
	case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
		return m.RelayOldDaemon()
	case errors.As(err, &re):
		return errtext.Message(re.Message)
	}
	return errtext.Text(err)
}
