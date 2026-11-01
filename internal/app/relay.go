package app

import (
	"context"
	"errors"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
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
func (a *app) useRelayCode(code string) {
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
				r.err = relayErrText(err)
				return
			}
			r.st, r.loaded, r.loadedAt, r.code = st, true, time.Now(), ""
		})
	}()
}

func (a *app) removeRelay() {
	body := "手机在外面就连不回这台电脑了，要再用得重新贴邀请码。"
	if a.mobile.relay.st.Admin {
		body = "这台电脑存着中转的主密钥，移除后不能再邀请别人，要从服务器重新取管理员码。已发出的邀请码照常能用。"
	}
	a.confirm("不再使用外网中转？", body, "移除", func() {
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

func (a *app) makeInvite(name string) {
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
				r.err = relayErrText(err)
				return
			}
			r.invite, r.name = out.Code, ""
		})
	}()
}

// relayStates 是每种状态的标题后缀和说明。
var relayStates = map[string]struct{ label, note string }{
	"ready":       {"已就绪", "手机在 Wi-Fi 下连过这台电脑一次后，在外面也能连上。"},
	"denied":      {"邀请码无效", "中转不认这个邀请码，向发码的人要一个新的。"},
	"revoked":     {"已停用", "这个邀请码已被中转的管理员停用。"},
	"clock":       {"时间不准", "这台电脑的时间和中转差了 5 分钟以上，在系统设置里打开「自动设置日期与时间」。"},
	"unreachable": {"中转不应答", "中转服务器没开，或者当前网络封了 UDP。"},
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
				title := "外网中转"
				if st.Configured && known {
					title += " · " + state.label
				}
				cardTitle(c, title)
				ui.Spacer(c)
				if st.Configured && pressable(textButton(c, "移除")).Tooltip("这台电脑不再使用中转").Clicked() {
					a.removeRelay()
				}
			})
			if !st.Configured {
				ui.Text(c, "手机不在同一个 Wi-Fi 时，经中转连回这台电脑。贴上收到的中转邀请码。").
					FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Grow(1).Shrink(1).Children(func() {
						if fieldInput(c, &r.code, "粘贴中转邀请码").Submitted() {
							a.useRelayCode(r.code)
						}
					})
					if pressable(textButton(c, "使用")).Clicked() {
						a.useRelayCode(r.code)
					}
				})
			} else if known {
				ui.Text(c, state.note).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
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
		ui.Text(c, "邀请别人").FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Grow(1).Shrink(1).Children(func() {
				if fieldInput(c, &r.name, "对方的名字，如 lvxiaohai").Submitted() {
					a.makeInvite(r.name)
				}
			})
			if pressable(textButton(c, "生成邀请码")).Clicked() {
				a.makeInvite(r.name)
			}
		})
		if r.invite != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, r.invite).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Grow(1).Shrink(1)
				if pressable(textButton(c, "复制")).Clicked() {
					c.WriteClipboard(r.invite)
				}
			})
		}
		ui.Text(c, "对方在自己电脑的 Posse 手机页贴上邀请码。名字只能用小写字母、数字、点、横线，在服务器上停用某个人时用它。").
			FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
	})
}

// relayErrText 是显示在这一栏里的报错：常驻进程的报错只取说明，不带错误码。
func relayErrText(err error) string {
	var re *rpc.Error
	switch {
	case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
		return "常驻进程是旧版，在设置页更新常驻进程后再试。"
	case errors.As(err, &re):
		return errtext.Message(re.Message)
	}
	return errtext.Text(err)
}
