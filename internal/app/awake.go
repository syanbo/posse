package app

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// awakeStatus 是常驻进程的防休眠状态（internal/daemon/awake.go）：档位 off、auto、display、on，
// 此刻是否托着不让睡，以及正在工作的 agent 数（不决定托不托住）。
type awakeStatus struct {
	Mode    string `json:"mode"`
	Active  bool   `json:"active"`
	Working int    `json:"working"`
}

// short 是底栏按钮上的字，item 是菜单项，seg 是设置页分段按钮上的字（菜单项太长放不下）。
var awakeModes = []struct{ mode, short, item, seg string }{
	{"off", "关闭", "关闭", "关闭"},
	{"auto", "运行", "Posse 运行时", "Posse 运行时"},
	{"display", "亮屏", "Posse 运行时，屏幕也亮", "运行时亮屏"},
}

// awakeTip 是按钮的悬停说明。
func awakeTip(st *awakeStatus) string {
	switch {
	case st == nil:
		return "防休眠：重启常驻进程后可用"
	case st.Mode == "off":
		return "防休眠已关闭，电脑按系统设置休眠"
	case st.Mode == "display" && st.Active:
		return "防休眠：Posse 运行中，电脑不休眠，屏幕也不熄"
	case st.Mode == "display":
		return "防休眠：Posse 运行时电脑不休眠、屏幕不熄"
	case st.Active:
		return "防休眠：Posse 运行中，电脑不会休眠，屏幕照常熄灭"
	}
	return "防休眠：Posse 运行时电脑不休眠"
}

// awakeButton 在主区域状态栏最右边：咖啡杯图标加档位，托着不让睡时图标是强调色。点开选档位。
func (a *app) awakeButton(c *ui.Context) {
	t := c.Theme()
	st := a.daemon.Awake
	short := "不可用"
	if st != nil {
		for _, m := range awakeModes {
			if m.mode == st.Mode {
				short = m.short
			}
		}
	}
	tip := awakeTip(st)
	b := ui.Row(c.Key("awake")).Height(sz(24)).Padding(0, 8).Gap(6).Radius(6).AlignItems(ui.Center).
		Role(ui.RoleButton).Label("防休眠：" + short).Tooltip(tip)
	bg := ui.Transparent
	if b.Hovered() && st != nil {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	iconColor, textColor := t.TextMuted, t.TextMuted
	if st != nil && st.Active {
		iconColor = t.Accent
	}
	if st == nil {
		iconColor, textColor = colors(t).faint, colors(t).faint
	}
	b.Children(func() {
		ui.Icon(c, iconCoffee).FontSize(fsBody).TextColor(iconColor).Shrink(0)
		ui.Text(c, short).FontSize(fsSmall).TextColor(textColor)
	})
	if st == nil {
		return
	}
	menuButton(c, b, func(m *popMenu) {
		for _, o := range awakeModes {
			if m.Item(o.item).Checked(o.mode == st.Mode).Chosen() {
				a.setAwake(o.mode)
			}
		}
	})
}

func (a *app) setAwake(mode string) {
	go func() {
		if a.call("awake.set", map[string]any{"mode": mode}, nil, 5*time.Second) == nil {
			a.refresh()
		}
	}()
}
