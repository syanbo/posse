package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"hive/internal/agent"
	"hive/internal/daemon"
)

// settingsBoardID 是设置页的 ID，不在侧栏里，从 ⌘, 和侧栏顶栏的齿轮进来。
const settingsBoardID = "settings"

func (a *app) openSettings() {
	a.selected = settingsBoardID
}

// settingsBoard 是设置页：防休眠、agent 权限、套餐用量、图标大小、终端字号、关于（版本、重启常驻进程、日志和数据目录）。
// 手机配对仍在「手机」页。
func (a *app) settingsBoard(c *ui.Context) {
	a.band(c, func() {
		titleText(c, "设置", fsCardTitle)
		ui.Spacer(c)
		a.closeBoardButton(c)
	})
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(24).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Width(640).Gap(16).Children(func() {
				a.awakeSettings(c)
				a.permissionSettings(c)
				a.planSettings(c)
				a.interfaceSettings(c)
				a.fontSettings(c)
				a.aboutSettings(c)
			})
		})
	})
}

func settingsCard(c *ui.Context, title, note string, body func()) {
	t := c.Theme()
	card(c).Padding(20).Gap(12).Children(func() {
		ui.Column(c).Gap(4).Children(func() {
			cardTitle(c, title)
			if note != "" {
				ui.Text(c, note).FontSize(12.5).TextColor(t.TextMuted).LineHeight(1.5)
			}
		})
		body()
	})
}

// settingsRow 是左边名称、右边控件的一行。
func settingsRow(c *ui.Context, label string, right func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(13).Grow(1).Shrink(1)
		right()
	})
}

func (a *app) awakeSettings(c *ui.Context) {
	st := a.daemon.Awake
	settingsCard(c, "防休眠", "Hive 运行时不让电脑休眠，屏幕照常熄灭；底栏的咖啡杯也能切换。「运行时亮屏」让屏幕也不熄：屏幕熄了会跟着锁屏。", func() {
		if st == nil {
			ui.Text(c, "更新常驻进程后可用。").FontSize(13).TextColor(c.Theme().TextMuted)
			return
		}
		sel := 0
		var labels []string
		for i, m := range awakeModes {
			labels = append(labels, m.seg)
			if m.mode == st.Mode {
				sel = i
			}
		}
		if segmented(c, &sel, "防休眠", labels...) {
			a.setAwake(awakeModes[sel].mode)
		}
		ui.Text(c, awakeTip(st)).FontSize(12).TextColor(c.Theme().TextMuted)
	})
}

var permissionModes = []struct{ mode, seg, tip string }{
	{agent.PermissionSkip, "跳过审批", "启动时带上各家跳过确认的参数：Claude --dangerously-skip-permissions，Codex --dangerously-bypass-approvals-and-sandbox（同时不用沙盒），Grok --permission-mode bypassPermissions，Cursor --yolo。"},
	{agent.PermissionAsk, "逐项确认", "照各家自己的设置，读写文件、执行命令前停下来问；Claude 的确认也会推到配对的手机上。"},
}

func (a *app) permissionSettings(c *ui.Context) {
	settingsCard(c, "Agent 权限", "在 Hive 里启动的 agent（包括在终端里手动敲的 claude、codex、cursor-agent）用哪种权限。改了之后新开的 agent 生效，已经在运行的不变。", func() {
		sel := 0
		var labels []string
		for i, m := range permissionModes {
			labels = append(labels, m.seg)
			if m.mode == a.permission {
				sel = i
			}
		}
		if segmented(c, &sel, "Agent 权限", labels...) {
			a.setPermission(permissionModes[sel].mode)
		}
		ui.Text(c, permissionModes[sel].tip).FontSize(12).TextColor(c.Theme().TextMuted).LineHeight(1.5)
	})
}

func (a *app) setPermission(mode string) {
	if a.home != "" {
		if err := agent.SetPermissionMode(a.home, mode); err != nil {
			a.err = err.Error()
			return
		}
	}
	a.permission = mode
}

// interfaceSettings 是界面的图标大小：图标按钮、侧栏和标签页里的图标一起换档。
func (a *app) interfaceSettings(c *ui.Context) {
	settingsCard(c, "界面", "", func() {
		settingsRow(c, "图标大小", func() {
			sel := 0
			var labels []string
			for i, s := range iconSizes {
				labels = append(labels, s.label)
				if s.size == iconSize {
					sel = i
				}
			}
			if segmented(c, &sel, "图标大小", labels...) {
				iconSize = iconSizes[sel].size
				a.state.IconSize = 0
				if iconSize != defaultIconSize {
					a.state.IconSize = iconSize
				}
				a.saveState()
			}
		})
	})
}

func (a *app) fontSettings(c *ui.Context) {
	t := c.Theme()
	settingsCard(c, "终端", "", func() {
		settingsRow(c, "字号", func() {
			if pressable(textButton(c, "−")).Label("缩小字号").Clicked() {
				a.setFontSize(a.fontSize - 1)
			}
			ui.Text(c, fmt.Sprintf("%g", a.fontSize)).FontSize(13).FontFeatures("tnum").Width(32).Center()
			if pressable(textButton(c, "+")).Label("放大字号").Clicked() {
				a.setFontSize(a.fontSize + 1)
			}
			if pressable(textButton(c, "默认")).Clicked() {
				a.setFontSize(defaultFontSize)
			}
		})
		ui.Text(c, "终端里也可以用 ⌘= 放大、⌘- 缩小、⌘0 恢复默认。").FontSize(12).TextColor(t.TextMuted)
	})
}

// versionLabel 把版本和构建标识（提交加可执行文件时间）写成"0.1.0（提交，有未提交改动，10-18 15:15 构建）"的样子。
// 带上构建时间：同一个提交打两次包时，只看提交分不出窗口和常驻进程哪个旧。两样都没有时只写版本。
func versionLabel(version, build string) string {
	rev, _, _ := strings.Cut(build, " ")
	var parts []string
	if rev != "" {
		parts = append(parts, strings.TrimSuffix(rev, "+"))
	}
	if strings.HasSuffix(rev, "+") {
		parts = append(parts, "有未提交改动")
	}
	if t, ok := buildTime(build); ok {
		parts = append(parts, t.Local().Format("01-02 15:04")+" 构建")
	}
	if len(parts) == 0 {
		return version
	}
	return version + "（" + strings.Join(parts, "，") + "）"
}

func (a *app) aboutSettings(c *ui.Context) {
	t := c.Theme()
	d := a.daemon
	stale := d.Build != daemon.Build
	settingsCard(c, "关于", "", func() {
		settingsRow(c, "窗口", func() {
			ui.Text(c, versionLabel(daemon.Version, daemon.Build)).FontSize(12.5).Font(monoFont).TextColor(t.TextMuted)
		})
		settingsRow(c, "常驻进程", func() {
			switch {
			case d.Pid == 0:
				ui.Text(c, "没有连上").FontSize(12.5).TextColor(t.TextMuted)
			case d.Build == "":
				ui.Text(c, d.Version+"（旧版本）").FontSize(12.5).Font(monoFont).TextColor(t.TextMuted)
			default:
				ui.Text(c, versionLabel(d.Version, d.Build)).FontSize(12.5).Font(monoFont).TextColor(t.TextMuted)
			}
		})
		if d.Pid != 0 {
			c.After(time.Minute)
			settingsRow(c, "", func() {
				ui.Text(c, fmt.Sprintf("pid %d，%s启动", d.Pid, ago(d.StartedAt))).FontSize(12).TextColor(t.TextMuted)
			})
		}
		updating, unsupported := a.upgrade.status(d.Pid)
		canUpdate := d.Pid != 0 && stale && !unsupported && d.Build != ""
		// 窗口比常驻进程旧（打开了旧的 Hive.app）时不催更新：更新会把常驻进程换成窗口的旧版本
		windowOlder := false
		if mine, ok := buildTime(daemon.Build); ok {
			if theirs, ok := buildTime(d.Build); ok {
				windowOlder = mine.Before(theirs)
			}
		}
		if d.Pid != 0 && stale {
			note := "常驻进程的版本比窗口旧。更新常驻进程，终端不会断开。"
			switch {
			case updating:
				note = "正在更新常驻进程…"
			case !canUpdate:
				note = "常驻进程的版本比窗口旧，而且不支持不断开终端的更新，需要重启一次。重启会关闭所有终端。"
			case windowOlder:
				note = "窗口的版本比常驻进程旧，可能打开的是旧的 Hive.app。更新会把常驻进程换成窗口的版本。"
			}
			ui.Text(c, note).FontSize(12.5).TextColor(t.Text).LineHeight(1.5)
		}
		ui.Row(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
			if canUpdate {
				update := accentButton
				if windowOlder {
					update = textButton
				}
				b := pressable(update(c, "更新常驻进程"))
				if updating {
					b.Disabled(true)
				}
				if b.Clicked() {
					a.upgradeDaemon(d.Pid, false)
				}
			}
			button := textButton
			if d.Pid != 0 && stale && !canUpdate {
				button = accentButton
			}
			if pressable(button(c, "重启常驻进程")).Clicked() {
				a.askRestartDaemon()
			}
			if pressable(textButton(c, "打开日志")).Clicked() {
				mygo.Shell.OpenPath(filepath.Join(a.home, "daemon.log"))
			}
			if pressable(textButton(c, "打开数据目录")).Clicked() {
				mygo.Shell.OpenPath(a.home)
			}
		})
	})
}

// askRestartDaemon 先确认再停掉常驻进程；下一次轮询连不上 socket 时，窗口用自己的包把它拉起来。
func (a *app) askRestartDaemon() {
	body := "所有终端都会断开，里面的 agent 要从「历史」里接着聊。"
	if n := a.daemon.Running; n > 0 {
		body = fmt.Sprintf("正在运行的 %d 个终端都会断开，里面的 agent 要从「历史」里接着聊。", n)
	}
	a.confirm("重启常驻进程？", body, "重启", func() {
		go func() {
			a.call("daemon.stop", nil, nil, 5*time.Second)
			a.refresh()
		}()
	})
}
