package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/rpc"
	"hive/internal/usage"
)

// planStatus 是常驻进程记下的各家套餐用量（internal/daemon/usage.go），随 daemon.status 来；
// 旧常驻进程没有这个字段时为空。
type planStatus struct {
	Usages    []usage.Usage `json:"usages"`
	CursorOff bool          `json:"cursorOff"`
	CodexOff  bool          `json:"codexOff"`
}

// 用量到这里用红色：快要被限流了。
const planUrgent = 80

// windowLabel 按窗口长度写名字：Claude、Codex 的 5 小时和每周，Grok 的每周，Cursor 的计费周期（约一个月）。
func windowLabel(minutes int) string {
	const day = 24 * 60
	switch {
	case minutes == 0:
		return "本期"
	case minutes >= 295 && minutes <= 305:
		return "5 小时"
	case minutes >= 7*day-10 && minutes <= 7*day+10:
		return "每周"
	case minutes >= 27*day && minutes <= 32*day:
		return "每月"
	case minutes >= day:
		return fmt.Sprintf("%d 天", (minutes+day/2)/day)
	}
	return fmt.Sprintf("%d 小时", (minutes+30)/60)
}

// resetText 是"2 小时 15 分钟后重置"；超过一天写日期。
func resetText(at int64, now time.Time) string {
	if at == 0 {
		return ""
	}
	t := time.UnixMilli(at)
	d := t.Sub(now)
	switch {
	case d <= 0:
		return "已重置"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟后重置", int(d.Minutes())+1)
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return fmt.Sprintf("%d 小时后重置", h)
		}
		return fmt.Sprintf("%d 小时 %d 分钟后重置", h, m)
	}
	return t.Local().Format("1月2日 15:04") + " 重置"
}

func percentText(p float64) string {
	return fmt.Sprintf("%.0f%%", p)
}

// shown 是界面上写的数：已用，或者切到"剩余"时的 100 减已用。快用完的判断不随切换变。
func (a *app) shown(w usage.Window) float64 {
	if a.state.UsageRemaining {
		return 100 - w.Percent
	}
	return w.Percent
}

func (a *app) shownWord() string {
	if a.state.UsageRemaining {
		return "剩余"
	}
	return "已用"
}

// statusBar 是主区域最下面的状态栏，和侧栏的底栏同高同色，并排看是贯穿窗口的一条。
// 左边是套餐用量，每家只占一小段：图标和一个窗口（有 5 小时就是它，否则是第一个）的百分比，合起来是一个按钮，
// 点开的浮窗里是各家所有窗口、重置时间这些详情和"已用 / 剩余"切换；最右边是终端和内存、防休眠。
func (a *app) statusBar(c *ui.Context) {
	t := c.Theme()
	p := colors(t)
	st := a.daemon.Usage
	ui.Row(c).Height(footHeight).Padding(0, 8, 0, 6).Shrink(0).AlignItems(ui.Center).Clip().
		Background(footShade(t)).BorderWidth(1, 0, 0, 0).BorderColor(p.line).Children(func() {
		// 窗口窄时裁掉用量，右边的按钮不被挤出去
		ui.Row(c).Grow(1).Shrink(1).MinWidth(0).Clip().AlignItems(ui.Center).Children(func() { a.planButton(c, st) })
		if a.loaded {
			// 终端数和内存放在一个按钮里：点开的浮窗按工作区列的就是这些终端占的内存
			a.memoryButton(c)
			a.awakeButton(c)
		}
	})
}

// planButton 是状态栏左边的套餐用量按钮，没有读数时是一行说明。
func (a *app) planButton(c *ui.Context, st *planStatus) {
	t := c.Theme()
	p := colors(t)
	if st == nil {
		return
	}
	var shown []usage.Usage
	for _, u := range st.Usages {
		if len(u.Windows) > 0 {
			shown = append(shown, u)
		}
	}
	if len(shown) == 0 {
		ui.Text(c, "agent 运行后在这里显示套餐用量").FontSize(11).TextColor(t.TextMuted).Padding(0, 4).SingleLine()
		return
	}
	b := ui.Row(c.Key("plan")).Height(26).Padding(0, 8).Gap(12).Radius(6).AlignItems(ui.Center).Shrink(0).
		Role(ui.RoleButton).Label("套餐用量")
	bg := ui.Transparent
	if a.planOpen || b.Hovered() {
		bg = p.wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		for _, u := range shown {
			w := headWindow(u)
			color := t.Text
			if w.Percent >= planUrgent {
				color = t.Danger
			}
			ui.Row(c.Key(u.Agent)).Gap(4).AlignItems(ui.Center).Shrink(0).
				Tooltip(fmt.Sprintf("%s %s %s %s", agentTitle(u.Agent), windowLabel(w.Minutes), a.shownWord(), percentText(a.shown(w)))).
				Children(func() {
					agentTile(c, u.Agent, 14)
					ui.Text(c, windowShort(w.Minutes)).FontSize(11).TextColor(t.TextMuted).SingleLine()
					ui.Text(c, percentText(a.shown(w))).FontSize(11).FontFeatures("tnum").TextColor(color).SingleLine()
				})
		}
	})
	if b.Clicked() {
		a.planOpen = !a.planOpen
	}
	ui.Popover(c, b, &a.planOpen, func() { a.planPanel(c, shown) })
}

// headWindow 是状态栏里代表这一家的窗口：总额度里有 5 小时就是它，否则是第一个。
func headWindow(u usage.Usage) usage.Window {
	for _, w := range u.Windows {
		if w.Name == "" && windowLabel(w.Minutes) == "5 小时" {
			return w
		}
	}
	return u.Windows[0]
}

// windowName 是详情里窗口的名字：分池带上池名，"Fable 每周"、"Auto 每月"。
func windowName(w usage.Window) string {
	if w.Name == "" {
		return windowLabel(w.Minutes)
	}
	return w.Name + " " + windowLabel(w.Minutes)
}

// money 把美分写成 $12.34。
func money(cents float64) string {
	return fmt.Sprintf("$%.2f", cents/100)
}

// planFacts 是详情里窗口下面的几行：套餐内金额、按需用量、本周用量用在哪、重置卡。
// 和整家的读数不是同一份的，后面写上是多久前的。
func planFacts(u usage.Usage) []string {
	stale := func(at int64) string {
		if at > 0 && u.UpdatedAt-at > int64(10*time.Minute/time.Millisecond) {
			return "（" + ago(time.UnixMilli(at)) + "的读数）"
		}
		return ""
	}
	var out []string
	if sp := u.Spend; sp != nil && sp.Limit > 0 {
		out = append(out, fmt.Sprintf("套餐内已用 %s，额度 %s", money(sp.Used), money(sp.Limit)))
	}
	if od := u.OnDemand; od != nil {
		line := "按需用量 " + money(od.Used)
		if od.Limit > 0 {
			line += "，上限 " + money(od.Limit)
		}
		out = append(out, line+stale(od.At))
	}
	if sh := u.Shares; sh != nil && len(sh.Items) > 0 {
		var parts []string
		for _, it := range sh.Items {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", it.Name, it.Percent))
		}
		out = append(out, "本周用量用在 "+strings.Join(parts, "，")+stale(sh.At))
	}
	if rs := u.Resets; rs != nil {
		line := fmt.Sprintf("重置卡 %d 张", rs.Count)
		if rs.Count > 0 && rs.ExpiresAt > 0 {
			line += "，最早一张 " + time.UnixMilli(rs.ExpiresAt).Local().Format("1月2日") + " 过期"
		}
		out = append(out, line+stale(rs.At))
	}
	return out
}

// windowShort 是状态栏里的窗口名，比 windowLabel 短。
func windowShort(minutes int) string {
	switch l := windowLabel(minutes); l {
	case "5 小时":
		return "5h"
	case "每周":
		return "周"
	case "每月":
		return "月"
	default:
		return l
	}
}

// planBar 是进度条：画已用或剩余的那一段，快用完时是红色。
func (a *app) planBar(c *ui.Context, w usage.Window, width, height float32) {
	t := c.Theme()
	fill := t.Accent
	if w.Percent >= planUrgent {
		fill = t.Danger
	}
	v := a.shown(w)
	bar := ui.Box(c).Height(height).Radius(height / 2).Background(colors(t).fill).Shrink(0)
	if width > 0 {
		bar.Width(width)
	} else {
		bar.Grow(1)
	}
	bar.Children(func() {
		if v > 0 {
			ui.Box(c).Height(height).Radius(height / 2).WidthPercent(float32(max(v, 3))).Background(fill)
		}
	})
}

// planPanel 是点开状态栏的详情：各家的套餐、所有窗口的进度和重置时间、读数时间，顶上切换已用还是剩余。
func (a *app) planPanel(c *ui.Context, us []usage.Usage) {
	t := c.Theme()
	now := time.Now()
	// 倒计时和"几分钟前"按分钟走
	c.After(time.Minute)
	// 浮窗至少和底下整排一样宽，面板跟着撑满
	ui.Column(c).MinWidth(420).Grow(1).AlignSelf(ui.Stretch).Gap(12).Padding(8, 8, 6, 8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			titleText(c, "套餐用量", fsCardTitle).Grow(1)
			// 查询进行中按钮换成转圈
			if a.planBusy {
				ui.Row(c.Key("plan-refreshing")).Size(28, 28).Shrink(0).Justify(ui.Center).AlignItems(ui.Center).
					Tooltip("正在查询").Children(func() { ui.Spinner(c).Label("正在查询") })
			} else if pressable(iconButtonSized(c.Key("plan-refresh"), iconRefresh, "立即查询各家用量", 28, 15)).Clicked() {
				a.refreshPlan()
			}
			sel := 0
			if a.state.UsageRemaining {
				sel = 1
			}
			if segmented(c, &sel, "套餐用量显示", "已用", "剩余") {
				a.state.UsageRemaining = sel == 1
				a.saveState()
			}
		})
		for i, u := range us {
			if i > 0 {
				ui.Box(c).Height(1).Background(colors(t).line)
			}
			ui.Column(c.Key(u.Agent)).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					agentTile(c, u.Agent, 16)
					ui.Text(c, agentTitle(u.Agent)).FontSize(13).FontWeight(500).Shrink(0)
					if u.Plan != "" {
						ui.Text(c, u.Plan).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(1)
					}
					ui.Spacer(c)
					ui.Text(c, ago(time.UnixMilli(u.UpdatedAt))).FontSize(11).TextColor(t.TextMuted).Shrink(0).
						Tooltip("读数来自 agent 最近一次运行：" + time.UnixMilli(u.UpdatedAt).Local().Format("1月2日 15:04"))
				})
				for j, w := range u.Windows {
					color := t.Text
					if w.Percent >= planUrgent {
						color = t.Danger
					}
					// 一个窗口一行：名称、进度条、百分比、重置时间
					ui.Row(c.Key(fmt.Sprint(j))).Gap(10).AlignItems(ui.Center).Children(func() {
						ui.Text(c, windowName(w)).FontSize(12).Width(84).Shrink(0).SingleLine()
						a.planBar(c, w, 0, 5)
						ui.Text(c, a.shownWord()+" "+percentText(a.shown(w))).FontSize(12).FontFeatures("tnum").TextColor(color).
							Width(64).Shrink(0).TextAlign(ui.End)
						ui.Text(c, resetText(w.ResetsAt, now)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).
							Width(138).Shrink(0).TextAlign(ui.End).SingleLine()
					})
					// 分池和总额度不是同一份读数（Claude 的 Fable 只在用户看 /usage 时更新）
					if w.At > 0 && u.UpdatedAt-w.At > int64(10*time.Minute/time.Millisecond) {
						ui.Text(c.Key(fmt.Sprint(j, "-at")), ago(time.UnixMilli(w.At))+"的读数").
							FontSize(11).TextColor(t.TextMuted).Margin(-4, 0, 0, 94)
					}
				}
				for _, f := range planFacts(u) {
					ui.Text(c, f).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted)
				}
			})
		}
		ui.Text(c, "agent 运行时顺带更新，点右上角的刷新马上查一次；Claude 没有可查的接口，只读它自己的缓存。").
			FontSize(11).TextColor(t.TextMuted).LineHeight(1.5).Padding(2, 0, 0, 0)
	})
}

// refreshPlan 让常驻进程马上向各家取一次用量，做完才回（Codex、Cursor 要联网，最长半分钟）。
func (a *app) refreshPlan() {
	a.planBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var st planStatus
		err := a.c.Call(ctx, "usage.refresh", nil, &st)
		a.update(func() {
			a.planBusy = false
			var re *rpc.Error
			switch {
			case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
				a.err = "常驻进程版本较旧，不支持刷新套餐用量：到设置页更新常驻进程后再试。"
			case err != nil:
				a.err = "刷新套餐用量失败：" + err.Error()
			default:
				a.daemon.Usage = &st
			}
		})
	}()
}

// planSettings 是设置页里两项查询的开关：Cursor 用量和 Codex 重置卡本地没有，要在它们用过之后查一次。
func (a *app) planSettings(c *ui.Context) {
	st := a.daemon.Usage
	settingsCard(c, "套餐用量", "底栏显示各 agent 套餐用了多少，在 agent 运行时顺带更新：Claude、Codex、Grok 读它们自己留在本地的记录。下面两项本地没有，要向它们查（至少隔 5 分钟）：Cursor 在常驻进程启动和它用过之后，用 cursor-agent 存在钥匙串里的登录信息去查；Codex 的重置卡在它用过之后，由 Codex 用自己的登录去查。", func() {
		if st == nil {
			ui.Text(c, "更新常驻进程后可用。").FontSize(13).TextColor(c.Theme().TextMuted)
			return
		}
		cursor, codex := !st.CursorOff, !st.CodexOff
		settingsRow(c, "查询 Cursor 用量", func() {
			if ui.Switch(c, &cursor).Label("查询 Cursor 用量").Changed() {
				a.setUsageQuery("cursorOff", cursor)
			}
		})
		settingsRow(c, "查询 Codex 重置卡", func() {
			if ui.Switch(c, &codex).Label("查询 Codex 重置卡").Changed() {
				a.setUsageQuery("codexOff", codex)
			}
		})
	})
}

func (a *app) setUsageQuery(key string, on bool) {
	go func() {
		if a.call("usage.set", map[string]any{key: !on}, nil, 5*time.Second) == nil {
			a.refresh()
		}
	}()
}
