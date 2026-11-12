package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/rpc"
	"posse/internal/usage"
	"posse/locales"
)

// planStatus 是常驻进程记下的各家套餐用量（internal/daemon/usage.go），随 daemon.status 来；
// 旧常驻进程没有这个字段时为空。
type planStatus struct {
	Usages       []usage.Usage `json:"usages"`
	Off          bool          `json:"off"` // 设置里整个关掉了：状态栏不显示，常驻进程也不读
	CursorOff    bool          `json:"cursorOff"`
	CodexOff     bool          `json:"codexOff"`
	CodexError   string        `json:"codexError,omitempty"`
	CodexReading bool          `json:"codexReading,omitempty"`
}

// 用量到这里用红色：快要被限流了。
const planUrgent = 80

// windowLabel 按窗口长度写名字：Claude、Codex 的 5 小时和每周，Grok 的每周，Cursor 的计费周期（约一个月）。
func windowLabel(m locales.Messages, minutes int) string {
	const day = 24 * 60
	switch windowKindOf(minutes) {
	case windowPeriod:
		return m.UsageWindowPeriod()
	case window5h:
		return m.UsageWindowFiveHours()
	case windowWeek:
		return m.UsageWindowWeek()
	case windowMonth:
		return m.UsageWindowMonth()
	}
	if minutes >= day {
		return m.UsageWindowDays((minutes + day/2) / day)
	}
	return m.UsageWindowHours((minutes + 30) / 60)
}

// windowKind 是套餐窗口的类别，逻辑按它判断，不比较显示的文字。
type windowKind int

const (
	windowOther  windowKind = iota
	windowPeriod            // 不知道长度：本期
	window5h
	windowWeek
	windowMonth
)

// windowKindOf 按窗口的分钟数认出类别，各家报的长度有几分钟的出入。
func windowKindOf(minutes int) windowKind {
	const day = 24 * 60
	switch {
	case minutes == 0:
		return windowPeriod
	case minutes >= 295 && minutes <= 305:
		return window5h
	case minutes >= 7*day-10 && minutes <= 7*day+10:
		return windowWeek
	case minutes >= 27*day && minutes <= 32*day:
		return windowMonth
	}
	return windowOther
}

// resetText 是"2 小时 15 分钟后重置"；超过一天写日期。
func resetText(l locales.Messages, at int64, now time.Time) string {
	if at == 0 {
		return ""
	}
	t := time.UnixMilli(at)
	d := t.Sub(now)
	switch {
	case d <= 0:
		return l.UsageResetDone()
	case d < time.Hour:
		return l.UsageResetInMinutes(int(d.Minutes()) + 1)
	case d < 24*time.Hour:
		h, m := int(d.Hours()), int(d.Minutes())%60
		if m == 0 {
			return l.UsageResetInHours(h)
		}
		return l.UsageResetInHoursMinutes(h, m)
	}
	return l.UsageResetAt(t.Local())
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

func (a *app) shownWord(m locales.Messages) string {
	if a.state.UsageRemaining {
		return m.UsageRemaining()
	}
	return m.UsageUsed()
}

// chipPad 是状态栏里文字按钮（用量、内存、防休眠）的左右内边距。
const chipPad = 8

// statusBar 是主区域最下面的状态栏，和侧栏的底栏同高同色，并排看是贯穿窗口的一条。
// 左边是当前工作区的分支和 Git 日志开关（w 为 nil 或设置里放在标签栏时不画，见 branchButton）。右边是套餐用量（分支不在这里时在左边），每家只占一小段：图标和一个窗口（有 5 小时就是它，否则是第一个）的百分比，合起来是一个按钮，
// 点开的浮窗里是各家所有窗口、重置时间这些详情和"已用 / 剩余"切换；最右边是终端和内存、防休眠。
func (a *app) statusBar(c *ui.Context, w *workspace) {
	t := c.Theme()
	p := colors(t)
	st := a.daemon.Usage
	// 两头的按钮自带 chipPad 的左右内边距，文字离边 trafficLightX，和上面顶栏的图标对齐
	ui.Row(c).Height(footHeight).Padding(0, trafficLightX-chipPad).Shrink(0).AlignItems(ui.Center).Clip().
		Background(footShade(t)).BorderWidth(hairline, 0, 0, 0).BorderColor(p.line).Children(func() {
		a.overBranchButton = false
		inBar := !a.state.GitInTabBar
		switch {
		case w == nil || a.gitBranch(w) == "":
			// 切到别的页面时弹窗画不出来、也收不到点外面，不收掉的话回来又自己弹出
			a.branchPop = nil
		case inBar:
			a.branchButton(c, w)
			// Git 日志浮层从终端底边滑上来，开关挨着分支
			if toggleIconButtonSized(c, "git-log", iconGitLog, a.withKey(L(c).GitLogTitle(), "gitLog"), a.logPanelOpen(), sz(24), fsBody, rRow) {
				a.toggleLogPanel(w)
			}
		}
		// 分支在这里时用量靠右、挨着内存，否则照旧在左端；窗口窄时裁掉用量，右边的按钮不被挤出去
		plan := ui.Row(c).Grow(1).Shrink(1).MinWidth(0).Clip().AlignItems(ui.Center)
		if inBar {
			plan.Justify(ui.End)
		}
		plan.Children(func() { a.planButton(c, st) })
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
	if st == nil || st.Off {
		a.planOpen = false
		return
	}
	var shown []usage.Usage
	for _, u := range st.Usages {
		if len(u.Windows) > 0 {
			shown = append(shown, u)
		}
	}
	if (st.CodexError != "" || st.CodexReading) && !slices.ContainsFunc(shown, func(u usage.Usage) bool { return u.Agent == "codex" }) {
		shown = append(shown, usage.Usage{Agent: "codex"})
	}
	if len(shown) == 0 {
		ui.Text(c, L(c).UsageEmpty()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, chipPad).SingleLine()
		return
	}
	b := ui.Row(c.Key("plan")).Height(sz(26)).Padding(0, chipPad).Gap(12).Radius(rRow).AlignItems(ui.Center).Shrink(0).
		Role(ui.RoleButton).Label(L(c).UsageTitle())
	bg := ui.Transparent
	if a.planOpen || b.Hovered() {
		bg = p.wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		for _, u := range shown {
			w := headWindow(u)
			color := t.Text
			if !w.Unknown && w.Percent >= planUrgent {
				color = t.Danger
			}
			ui.Row(c.Key(u.Agent)).Gap(4).AlignItems(ui.Center).Shrink(0).
				Children(func() {
					agentIcon(c, u.Agent, smallIcon())
					ui.Text(c, windowShort(L(c), w.Minutes)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
					ui.Text(c, planPercent(L(c), a, w)).FontSize(fsSmall).FontFeatures("tnum").TextColor(color).SingleLine()
				})
		}
	})
	if b.Clicked() {
		a.planOpen = !a.planOpen
	}
	ui.Popover(c, b, &a.planOpen, func() { a.planPanel(c, shown) })
}

// planWindows 是这一家要显示的窗口。Cursor 有分池时照它的控制台和 Magpie 只列两池：
// 总的 totalPercentUsed 按 Auto 池折算，远低于两池，和它们并排看着像算错了。
func planWindows(u usage.Usage) []usage.Window {
	if u.Agent == "codex" {
		ws := slices.Clone(u.Windows)
		for i, w := range ws {
			if w.ResetsAt > 0 && w.ResetsAt <= time.Now().UnixMilli() {
				ws[i].Unknown = true
			}
		}
		return ws
	}
	if u.Agent != "cursor" {
		return u.Windows
	}
	var pools []usage.Window
	for _, w := range u.Windows {
		if w.Name != "" {
			pools = append(pools, w)
		}
	}
	if len(pools) == 0 {
		return u.Windows
	}
	return pools
}

// headWindow 是状态栏里代表这一家的窗口：总额度里有 5 小时就是它；只有分池时取用得最多的一池，否则是第一个。
// Claude 的套餐都有 5 小时窗口，读数里没有就是这个窗口里还没用过，照样写 5 小时，不换成每周。
func headWindow(u usage.Usage) usage.Window {
	ws := planWindows(u)
	for _, w := range ws {
		if w.Name == "" && windowKindOf(w.Minutes) == window5h {
			return w
		}
	}
	if u.Agent == "claude" {
		return usage.Window{Minutes: 300}
	}
	if len(ws) == 0 {
		return usage.Window{Unknown: true}
	}
	head := ws[0]
	if head.Name != "" {
		for _, w := range ws {
			if w.Percent > head.Percent {
				head = w
			}
		}
	}
	return head
}

// cursorPools 是 Cursor 两池在它控制台上的叫法：Auto 池含 Composer 和 Cursor Grok，API 池是其余模型。
var cursorPools = map[string]func(locales.Messages) string{"Auto": locales.Messages.UsageCursorModels, "API": locales.Messages.UsageOtherModels}

// windowName 是详情里窗口的名字：分池带上池名，"Fable 每周"；Cursor 的两池只写池名，重置时间在同一行。
func windowName(m locales.Messages, w usage.Window) string {
	if w.Name == "" {
		return windowLabel(m, w.Minutes)
	}
	if n, ok := cursorPools[w.Name]; ok {
		return n(m)
	}
	return m.UsageNamedWindow(w.Name, windowLabel(m, w.Minutes))
}

// money 把美分写成 $12.34。
func money(cents float64) string {
	return fmt.Sprintf("$%.2f", cents/100)
}

// planFacts 是详情里窗口下面的几行：套餐内用量的金额、按需用量、本周用量用在哪、重置卡。
// 和整家的读数不是同一份的，后面写上是多久前的。
func planFacts(m locales.Messages, u usage.Usage) []string {
	stale := func(at int64) string {
		if at > 0 && u.UpdatedAt-at > int64(10*time.Minute/time.Millisecond) {
			return m.UsageStaleReading(agoInline(m, time.UnixMilli(at)))
		}
		return ""
	}
	var out []string
	if sp := u.Spend; sp != nil && sp.Limit > 0 {
		// Cursor 的套餐内用量按 API 价折成金额（Ultra 月费 $200，含 $400），和两池的百分比分母不同
		out = append(out, m.UsageIncludedSpend(money(sp.Used), money(sp.Limit), percentText(sp.Used/sp.Limit*100)))
	}
	if od := u.OnDemand; od != nil {
		line := m.UsageOnDemand(money(od.Used), stale(od.At))
		if od.Limit > 0 {
			line = m.UsageOnDemandLimited(money(od.Used), money(od.Limit), stale(od.At))
		}
		out = append(out, line)
	}
	if sh := u.Shares; sh != nil && len(sh.Items) > 0 {
		var parts []string
		for _, it := range sh.Items {
			parts = append(parts, fmt.Sprintf("%s %.0f%%", it.Name, it.Percent))
		}
		out = append(out, m.UsageShares(strings.Join(parts, m.UsageListSeparator()), stale(sh.At)))
	}
	if rs := u.Resets; rs != nil {
		line := m.UsageResetCards(rs.Count, stale(rs.At))
		if rs.Count > 0 && rs.ExpiresAt > 0 {
			line = m.UsageResetCardsExpiring(rs.Count, m.AppDate(time.UnixMilli(rs.ExpiresAt).Local()), stale(rs.At))
		}
		out = append(out, line)
	}
	return out
}

// windowShort 是状态栏里的窗口名，比 windowLabel 短。
func windowShort(m locales.Messages, minutes int) string {
	switch windowKindOf(minutes) {
	case window5h:
		return "5h"
	case windowWeek:
		return m.UsageWindowWeekShort()
	case windowMonth:
		return m.UsageWindowMonthShort()
	}
	return windowLabel(m, minutes)
}

// planBar 是进度条：画已用或剩余的那一段，快用完时是红色。
func (a *app) planBar(c *ui.Context, w usage.Window) {
	const height = 5
	t := c.Theme()
	fill := t.Accent
	if !w.Unknown && w.Percent >= planUrgent {
		fill = t.Danger
	}
	v := a.shown(w)
	if w.Unknown {
		v = 0
	}
	bar := ui.Box(c).Height(height).Radius(full(height)).Background(colors(t).fill).Shrink(0).Grow(1)
	bar.Children(func() {
		if v > 0 {
			ui.Box(c).Height(height).Radius(full(height)).WidthPercent(float32(max(v, 3))).Background(fill)
		}
	})
}

// planPanel 是点开状态栏的详情：各家的套餐、所有窗口的进度和重置时间、读数时间，顶上切换已用还是剩余。
func (a *app) planPanel(c *ui.Context, us []usage.Usage) {
	t := c.Theme()
	now := time.Now()
	// 打开的浮层也补查，不能靠后台更新恰好触发重建；失败时同样等一分钟再试。
	c.After(time.Minute)
	if st := a.daemon.Usage; a.c != nil && st != nil && !st.CodexOff && !slices.Contains(a.disabled, "codex") && !a.planBusy && time.Since(a.planAutoAt) >= time.Minute {
		a.planAutoAt = now
		a.refreshPlanQuery(L(c), map[string]any{"agent": "codex", "force": false})
	}
	// 浮窗至少和底下整排一样宽，面板跟着撑满
	ui.Column(c).MinWidth(420).Grow(1).AlignSelf(ui.Stretch).Gap(12).Padding(8, 8, 6, 8).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			titleText(c, L(c).UsageTitle(), fsTitle).Grow(1)
			// 查询进行中按钮换成转圈
			if a.planBusy {
				ui.Row(c.Key("plan-refreshing")).Size(buttonBox(), buttonBox()).Shrink(0).Justify(ui.Center).AlignItems(ui.Center).AlignItems(ui.Center).
					Tooltip(L(c).UsageRefreshing()).Children(func() { ui.Spinner(c).Label(L(c).UsageRefreshing()) })
			} else if pressable(iconButtonSized(c.Key("plan-refresh"), iconRefresh, L(c).UsageRefreshTip(), buttonBox(), iconSize)).Clicked() {
				a.refreshPlan(L(c))
			}
			sel := 0
			if a.state.UsageRemaining {
				sel = 1
			}
			if segmented(c, &sel, L(c).UsageDisplayLabel(), L(c).UsageUsed(), L(c).UsageRemaining()) {
				a.state.UsageRemaining = sel == 1
				a.saveState()
			}
		})
		// 百分比一列按最长的写法定宽（英文的 Remaining 比中文长），各行的进度条一样长
		pctText := func(pct float64) string { return L(c).UsageShownPercent(a.shownWord(L(c)), percentText(pct)) }
		pctW, _ := c.MeasureText(0, ui.Span{Text: pctText(100), Size: fsSmall})
		pctW = max(sz(64), pctW+2)
		for i, u := range us {
			if i > 0 {
				hline(c)
			}
			ui.Column(c.Key(u.Agent)).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					agentTile(c, u.Agent, smallIcon())
					ui.Text(c, agentTitle(u.Agent)).FontSize(fsBody).FontWeight(fwMedium).Shrink(0)
					if u.Plan != "" {
						ui.Text(c, strings.Replace(u.Plan, "/mo", L(c).UsagePerMonth(), 1)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
					}
					ui.Spacer(c)
					// 刷新状态占用读数时间的位置，避免临时加行使底部浮层上下跳动。
					if u.Agent == "codex" && a.daemon.Usage != nil && a.daemon.Usage.CodexReading {
						ui.Text(c, L(c).UsageRefreshing()).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
					} else if u.UpdatedAt > 0 {
						ui.Text(c, planReadingAge(L(c), u, now)).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0).
							Tooltip(L(c).UsageReadAt(L(c).AppDateTime(time.UnixMilli(u.UpdatedAt).Local())))
					}
				})
				if u.Agent == "codex" {
					if st := a.daemon.Usage; st != nil {
						switch {
						case st.CodexError != "":
							ui.Text(c, L(c).UsageQueryFailed(errtext.Message(st.CodexError))).FontSize(fsSmall).TextColor(t.Danger)
						case st.CodexOff:
							ui.Text(c, L(c).UsageQueryDisabled()).FontSize(fsSmall).TextColor(t.TextMuted)
						}
					}
				}
				for j, w := range planWindows(u) {
					color := t.Text
					if !w.Unknown && w.Percent >= planUrgent {
						color = t.Danger
					}
					// 一个窗口一行：名称、进度条、百分比、重置时间
					ui.Row(c.Key(fmt.Sprint(j))).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, windowName(L(c), w)).FontSize(fsSmall).Width(planNameWidth()).Shrink(0).SingleLine()
						a.planBar(c, w)
						ui.Text(c, planWindowPercent(L(c), a, w)).FontSize(fsSmall).FontFeatures("tnum").TextColor(color).
							Width(pctW).Shrink(0).TextAlign(ui.End).SingleLine()
						ui.Text(c, planResetText(L(c), w, now)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).
							Width(sz(138)).Shrink(0).TextAlign(ui.End).SingleLine()
					})
					// 分池和总额度不是同一份读数（Claude 的 Fable 只在用户看 /usage 时更新）
					if w.At > 0 && u.UpdatedAt-w.At > int64(10*time.Minute/time.Millisecond) {
						ui.Text(c.Key(fmt.Sprint(j, "-at")), L(c).UsageReadingAge(agoInline(L(c), time.UnixMilli(w.At)))).
							FontSize(fsSmall).TextColor(t.TextMuted).Margin(-4, 0, 0, planNameWidth()+8) // 和上一行的名称列之后对齐
					}
				}
				for _, f := range planFacts(L(c), u) {
					ui.Text(c, f).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
				}
			})
		}
		ui.Text(c, L(c).UsageRefreshNote()).
			FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Padding(2, 0, 0, 0)
	})
}

// refreshPlan 让常驻进程马上向各家取一次用量，做完才回（Codex、Cursor 要联网，最长半分钟）。
func (a *app) refreshPlan(m locales.Messages) {
	a.planAutoAt = time.Now()
	a.refreshPlanQuery(m, nil)
}

func (a *app) refreshPlanQuery(m locales.Messages, params map[string]any) {
	if a.planBusy {
		return
	}
	a.planBusy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		var st planStatus
		err := a.c.Call(ctx, "usage.refresh", params, &st)
		a.update(func() {
			a.planBusy = false
			var re *rpc.Error
			switch {
			case errors.As(err, &re) && re.Code == rpc.CodeUnknown:
				a.err = m.UsageOldDaemonError()
			case err != nil:
				a.err = m.UsageRefreshError(errtext.Text(err))
			default:
				a.daemon.Usage = &st
			}
		})
	}()
}

// planSettings 控制 Cursor 用量和 Codex 在线额度查询，本地会话读数仍可更新。
func (a *app) planSettings(c *ui.Context) {
	st := a.daemon.Usage
	settingsCard(c, L(c).UsageTitle(), L(c).UsageSettingsNote(), func() {
		if st == nil {
			ui.Text(c, L(c).UsageDaemonRequired()).FontSize(fsBody).TextColor(c.Theme().TextMuted)
			return
		}
		on := !st.Off
		settingsRow(c, L(c).UsageShowLabel(), func() {
			if ui.Switch(c, &on).Label(L(c).UsageShowLabel()).Changed() {
				a.setUsageQuery("off", on)
			}
		})
		if st.Off {
			return
		}
		// 上面关掉的 agent 常驻进程不查，它的查询开关也不列
		cursor, codex := !st.CursorOff, !st.CodexOff
		if !slices.Contains(a.disabled, "cursor") {
			settingsRow(c, L(c).UsageCursorQuery(), func() {
				if ui.Switch(c, &cursor).Label(L(c).UsageCursorQuery()).Changed() {
					a.setUsageQuery("cursorOff", cursor)
				}
			})
		}
		if !slices.Contains(a.disabled, "codex") {
			settingsRow(c, L(c).UsageCodexQuery(), func() {
				if ui.Switch(c, &codex).Label(L(c).UsageCodexQuery()).Changed() {
					a.setUsageQuery("codexOff", codex)
				}
			})
		}
	})
}

func (a *app) setUsageQuery(key string, on bool) {
	go func() {
		if a.call("usage.set", map[string]any{key: !on}, nil, 5*time.Second) == nil {
			a.refresh()
		}
	}()
}

// planNameWidth 是用量窗口一行里名称列的宽度。
func planNameWidth() float32 { return sz(84) }

// planPercent 在过期窗口不再暗示剩余或已用百分比，状态栏用紧凑的占位。
func planPercent(m locales.Messages, a *app, w usage.Window) string {
	if w.Unknown {
		return m.UsageUnknownShort()
	}
	return percentText(a.shown(w))
}

func planWindowPercent(m locales.Messages, a *app, w usage.Window) string {
	if w.Unknown {
		return m.UsageAwaitingUpdate()
	}
	return m.UsageShownPercent(a.shownWord(m), percentText(a.shown(w)))
}

func planResetText(m locales.Messages, w usage.Window, now time.Time) string {
	if w.Unknown {
		return ""
	}
	return resetText(m, w.ResetsAt, now)
}

func planReadingAge(m locales.Messages, u usage.Usage, now time.Time) string {
	if u.UpdatedAt == 0 {
		return ""
	}
	age := ago(m, time.UnixMilli(u.UpdatedAt))
	if u.Agent == "codex" && now.Sub(time.UnixMilli(u.UpdatedAt)) > 5*time.Minute {
		return m.UsageOutdated(age)
	}
	return age
}
