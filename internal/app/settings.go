package app

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/daemon"
	"posse/internal/errtext"
	"posse/locales"
)

// settingsBoardID 是设置页的 ID，不在侧栏里，从 ⌘, 和侧栏顶栏的齿轮进来。
const settingsBoardID = "settings"

func (a *app) openSettings() {
	a.selected = settingsBoardID
}

// settingsTabs 是设置页左边的分类，右边只放选中的一类。
var settingsTabs = []struct {
	id    string
	label func(locales.Messages) string
	icon  *ui.SVG
}{
	{"general", locales.Messages.SettingsTabGeneral, iconSettings},
	{"agent", locales.Messages.SettingsTabAgent, iconSkill},
	{"appearance", locales.Messages.SettingsTabAppearance, iconAppearance},
	{"keys", locales.Messages.SettingsTabKeys, iconKeyboard},
	{"about", locales.Messages.SettingsTabAbout, iconInfo},
}

// settingsBoard 是设置页：左边分类，右边是这一类的卡片。通用（语言、防休眠、恢复终端、终端选区、通知）、Agent（权限、套餐用量）、
// 外观（图标大小，终端和编辑器的字体、字号、预览）、快捷键（keysettings.go）、关于（版本、更新和重启常驻进程、日志和数据目录）。手机配对在「手机」页。
func (a *app) settingsBoard(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	a.band(c, func() {
		titleText(c, m.SettingsTitle(), fsTitle)
		ui.Spacer(c)
		a.closeBoardButton(c)
	})
	tab := a.settingsTab
	if tab == "" {
		tab = settingsTabs[0].id
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(184).Shrink(0).Padding(16, 12).Gap(2).BorderWidth(0, hairline, 0, 0).BorderColor(colors(t).line).Children(func() {
			for _, s := range settingsTabs {
				icon := func() { ui.Icon(c, s.icon).FontSize(iconSize).TextColor(t.Accent).Shrink(0) }
				if a.navRowAt(c, "settings:"+s.id, s.id == tab, icon, s.label(m), "", "", nil).Clicked() {
					a.settingsTab = s.id
				}
			}
		})
		ui.Scroll(c.Key("settings:" + tab)).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
			ui.Column(c).Padding(24).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Width(640).MaxWidth(640).Gap(16).Children(func() {
					switch tab {
					case "general":
						a.languageSettings(c)
						a.chatSettings(c)
						a.awakeSettings(c)
						a.restoreSettings(c)
						a.selectSettings(c)
						a.linkSettings(c)
						a.notifySettings(c)
					case "agent":
						a.agentSettings(c)
						a.permissionSettings(c)
						a.planSettings(c)
					case "appearance":
						a.interfaceSettings(c)
						a.termFontSettings(c)
						a.editorFontSettings(c)
					case "keys":
						a.keySettings(c)
					case "about":
						a.updateSettings(c)
						a.aboutSettings(c)
					}
				})
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
				ui.Text(c, note).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			}
		})
		body()
	})
}

// settingsRow 是左边名称、右边控件的一行。
func settingsRow(c *ui.Context, label string, right func()) {
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(fsBody).Grow(1).Shrink(1)
		right()
	})
}

func (a *app) awakeSettings(c *ui.Context) {
	st := a.daemon.Awake
	m := L(c)
	settingsCard(c, m.SettingsAwakeTitle(), m.SettingsAwakeNote(), func() {
		if st == nil {
			ui.Text(c, m.SettingsUpdateDaemonFirst()).FontSize(fsBody).TextColor(c.Theme().TextMuted)
			return
		}
		sel := 0
		var labels []string
		for i, m := range awakeModes {
			labels = append(labels, m.seg(L(c)))
			if m.mode == st.Mode {
				sel = i
			}
		}
		if segmented(c, &sel, m.SettingsAwakeTitle(), labels...) {
			a.setAwake(awakeModes[sel].mode)
		}
		ui.Text(c, awakeTip(m, st)).FontSize(fsSmall).TextColor(c.Theme().TextMuted)
	})
}

// restoreSettings 是重启电脑或常驻进程之后怎么恢复上次的终端，见 internal/daemon/restore.go。
func (a *app) restoreSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsRestoreTitle(), m.SettingsRestoreNote(), func() {
		on := a.restoreAll
		settingsRow(c, m.SettingsRestoreAll(), func() {
			if ui.Switch(c.Key("restoreAll"), &on).Label(m.SettingsRestoreAll()).Changed() {
				a.setRestoreAll(on)
			}
		})
	})
}

func (a *app) linkSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsLinkTitle(), m.SettingsLinkNote(), func() {
		on := a.state.LinkClickOpens
		settingsRow(c, m.SettingsLinkClickOpens(), func() {
			if ui.Switch(c.Key("linkClickOpens"), &on).Label(m.SettingsLinkClickOpens()).Changed() {
				a.state.LinkClickOpens = on
				a.saveState()
			}
		})
		swap := a.state.LinkSwapSystem
		settingsRow(c, m.SettingsLinkSwapSystem(), func() {
			if ui.Switch(c.Key("linkSwapSystem"), &swap).Label(m.SettingsLinkSwapSystem()).Changed() {
				a.state.LinkSwapSystem = swap
				a.saveState()
			}
		})
	})
}

func (a *app) selectSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsSelectTitle(), m.SettingsSelectNote(), func() {
		on := a.state.TermCopyOnSelect
		settingsRow(c, m.SettingsCopyOnSelect(), func() {
			if ui.Switch(c.Key("termCopyOnSelect"), &on).Label(m.SettingsCopyOnSelect()).Changed() {
				a.setCopyOnSelect(on)
			}
		})
	})
}

// notifySettings 是系统通知和 Dock 角标。通知由窗口发，关掉窗口后就不再有。
func (a *app) notifySettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsNotifyTitle(), m.SettingsNotifyNote(), func() {
		notify, badge := !a.state.NotifyOff, !a.state.BadgeOff
		settingsRow(c, m.SettingsNotifySystem(), func() {
			if ui.Switch(c.Key("notify"), &notify).Label(m.SettingsNotifySystem()).Changed() {
				a.state.NotifyOff = !notify
				a.saveState()
			}
		})
		settingsRow(c, m.SettingsNotifyBadge(), func() {
			if ui.Switch(c.Key("badge"), &badge).Label(m.SettingsNotifyBadge()).Changed() {
				a.state.BadgeOff = !badge
				a.saveState()
				a.updateBadge()
			}
		})
	})
}

func (a *app) setRestoreAll(on bool) {
	if a.home != "" {
		if err := agent.SetRestoreAll(a.home, on); err != nil {
			a.err = errtext.Text(err)
			return
		}
	}
	a.restoreAll = on
}

var permissionModes = []struct {
	mode     string
	seg, tip func(locales.Messages) string
}{
	{agent.PermissionSkip, locales.Messages.SettingsPermissionSkip, locales.Messages.SettingsPermissionSkipTip},
	{agent.PermissionAsk, locales.Messages.SettingsPermissionAsk, locales.Messages.SettingsPermissionAskTip},
}

func (a *app) permissionSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsPermissionTitle(), m.SettingsPermissionNote(), func() {
		sel := 0
		var labels []string
		for i, pm := range permissionModes {
			labels = append(labels, pm.seg(m))
			if pm.mode == a.permission {
				sel = i
			}
		}
		if segmented(c, &sel, m.SettingsPermissionTitle(), labels...) {
			a.setPermission(permissionModes[sel].mode)
		}
		ui.Text(c, permissionModes[sel].tip(m)).FontSize(fsSmall).TextColor(c.Theme().TextMuted).LineHeight(1.5)
	})
}

// agentSettings 是各家 agent 的开关：关掉的不出现在新建会话、派发任务、新建工作区和手机里，常驻进程也拒绝启动它。
func (a *app) agentSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsAgentsTitle(), m.SettingsAgentsNote(), func() {
		for _, name := range a.installedAgents() {
			on := !slices.Contains(a.disabled, name)
			settingsRow(c, agentTitle(name), func() {
				if ui.Switch(c.Key("agent:"+name), &on).Label(agentTitle(name)).Changed() {
					a.setAgentEnabled(name, on)
				}
			})
		}
	})
}

func (a *app) setAgentEnabled(name string, on bool) {
	if a.home != "" {
		if err := agent.SetEnabled(a.home, name, on); err != nil {
			a.err = errtext.Text(err)
			return
		}
		a.disabled = agent.Disabled(a.home)
	}
}

func (a *app) setPermission(mode string) {
	if a.home != "" {
		if err := agent.SetPermissionMode(a.home, mode); err != nil {
			a.err = errtext.Text(err)
			return
		}
	}
	a.permission = mode
}

// interfaceSettings 是界面的字号、字重、字体平滑和图标大小。
func (a *app) interfaceSettings(c *ui.Context) {
	zoom := a.keyText("zoomIn") + " " + a.keyText("zoomOut") + " " + a.keyText("zoomReset")
	m := L(c)
	settingsCard(c, m.SettingsInterfaceTitle(), m.SettingsInterfaceNote(zoom), func() {
		setType := func(f func(s *uiState)) {
			f(&a.state)
			setUIType(a.state.UIFontSize, a.state.UIWeight, a.state.UIBoldWeight)
			a.saveState()
		}
		settingsRow(c, m.SettingsFontSize(), func() {
			fontSizeStepper(c, fsBody, defaultUIFontSize, func(size float32) {
				setType(func(s *uiState) {
					s.UIFontSize = 0
					if size = min(max(size, minUIFontSize), maxUIFontSize); size != defaultUIFontSize {
						s.UIFontSize = size
					}
				})
			})
		})
		settingsRow(c, m.SettingsMarkdownFontSize(), func() {
			fontSizeStepper(c, a.mdSize(), 0, a.setMarkdownFontSize)
		})
		settingsRow(c, m.SettingsFontWeight(), func() {
			stepper(c, m.SettingsFontWeight(), fmt.Sprint(fwBody),
				func(d int) { setType(func(s *uiState) { s.UIWeight = stepWeight(fwBody, d) }) },
				func() { setType(func(s *uiState) { s.UIWeight = 0 }) })
		})
		settingsRow(c, m.SettingsBoldWeight(), func() {
			stepper(c, m.SettingsBoldWeight(), fmt.Sprint(fwBold),
				func(d int) { setType(func(s *uiState) { s.UIBoldWeight = stepWeight(fwBold, d) }) },
				func() { setType(func(s *uiState) { s.UIBoldWeight = 0 }) })
		})
		settingsRow(c, m.SettingsIconSize(), func() {
			sel := 0
			var labels []string
			for i, s := range iconSizes {
				labels = append(labels, s.label(m))
				if s.size == iconSize {
					sel = i
				}
			}
			if segmented(c, &sel, m.SettingsIconSize(), labels...) {
				iconSize = iconSizes[sel].size
				a.state.IconSize = 0
				if iconSize != defaultIconSize {
					a.state.IconSize = iconSize
				}
				a.saveState()
			}
		})
		settingsRow(c, m.SettingsProjectMeta(), func() {
			sel := 1
			if a.state.ProjectMetaHidden {
				sel = 0
			}
			if segmented(c, &sel, m.SettingsProjectMeta(), m.CommonOff(), m.CommonOn()) {
				a.state.ProjectMetaHidden = sel == 0
				a.saveState()
			}
		})
		settingsRow(c, m.SettingsWorkspaceMeta(), func() {
			sel := 1
			if a.state.WorkspaceMetaHidden {
				sel = 0
			}
			if segmented(c, &sel, m.SettingsWorkspaceMeta(), m.CommonOff(), m.CommonOn()) {
				a.state.WorkspaceMetaHidden = sel == 0
				a.saveState()
			}
		})
		settingsRow(c, m.SettingsFilesPanel(), func() {
			sel := 0
			if a.state.FilesPanelShared {
				sel = 1
			}
			if segmented(c, &sel, m.SettingsFilesPanel(), m.SettingsFilesPanelPerProject(), m.SettingsFilesPanelShared()) {
				a.setFilesPanelShared(sel == 1)
			}
		})
		settingsRow(c, m.SettingsFontSmoothing(), func() {
			sel := 0
			if uiSmooth {
				sel = 1
			}
			if segmented(c, &sel, m.SettingsInterfaceFontSmoothing(), m.CommonOff(), m.CommonOn()) {
				uiSmooth = sel == 1
				a.state.UISmooth = uiSmooth
				a.saveState()
			}
		})
	})
}

func (a *app) termFontSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsTerminalTitle(), m.SettingsTerminalNote(), func() {
		settingsRow(c, m.SettingsFont(), func() {
			a.fontPicker(c, &a.termPick, a.termFamily, m.SettingsTerminalFont(), a.setTermFamily)
		})
		settingsRow(c, m.SettingsFontSize(), func() {
			fontSizeStepper(c, a.fontSize, defaultFontSize, a.setFontSize)
		})
		a.fontStyleRows(c, func(s *uiState) (*int, *int, *float32, *bool) {
			return &s.TermWeight, &s.TermBoldWeight, &s.TermLineHeight, &s.TermSmooth
		}, defaultTermLineHeight, maxTermLineHeight, a.setTermStyle)
		a.termPreview(c)
	})
}

// fontStyleRows 是终端、编辑器卡片里共用的字重、粗体字重、行高、字体平滑四行。fields 取出 ui.json 里
// 对应的四项（0 和 false 是默认），set 改完后让它生效并保存。
func (a *app) fontStyleRows(c *ui.Context, fields func(s *uiState) (weight, bold *int, lineHeight *float32, smooth *bool),
	defLineHeight, maxLineHeight float32, set func(f func(s *uiState))) {
	w, b, l, sm := fields(&a.state)
	weight, bold := weightsOf(*w, *b)
	lh := cmp.Or(*l, defLineHeight)
	change := func(f func(w, b *int, l *float32, sm *bool)) {
		set(func(s *uiState) { f(fields(s)) })
	}
	m := L(c)
	settingsRow(c, m.SettingsFontWeight(), func() {
		stepper(c, m.SettingsFontWeight(), fmt.Sprint(weight),
			func(d int) { change(func(w, _ *int, _ *float32, _ *bool) { *w = stepWeight(weight, d) }) },
			func() { change(func(w, _ *int, _ *float32, _ *bool) { *w = 0 }) })
	})
	settingsRow(c, m.SettingsBoldWeight(), func() {
		stepper(c, m.SettingsBoldWeight(), fmt.Sprint(bold),
			func(d int) { change(func(_, b *int, _ *float32, _ *bool) { *b = stepWeight(bold, d) }) },
			func() { change(func(_, b *int, _ *float32, _ *bool) { *b = 0 }) })
	})
	settingsRow(c, m.SettingsLineHeight(), func() {
		stepper(c, m.SettingsLineHeight(), fmt.Sprintf("%.1f", lh),
			func(d int) {
				change(func(_, _ *int, l *float32, _ *bool) { *l = stepLineHeight(lh, d, maxLineHeight) })
			},
			func() { change(func(_, _ *int, l *float32, _ *bool) { *l = 0 }) })
	})
	settingsRow(c, m.SettingsFontSmoothing(), func() {
		sel := 0
		if *sm {
			sel = 1
		}
		if segmented(c, &sel, m.SettingsFontSmoothing(), m.CommonOff(), m.CommonOn()) {
			change(func(_, _ *int, _ *float32, sm *bool) { *sm = sel == 1 })
		}
	})
}

func (a *app) editorFontSettings(c *ui.Context) {
	m := L(c)
	settingsCard(c, m.SettingsEditorTitle(), m.SettingsEditorNote(), func() {
		settingsRow(c, m.SettingsFont(), func() {
			a.fontPicker(c, &a.editorPick, a.editorFamily, m.SettingsEditorFont(), a.setEditorFamily)
		})
		settingsRow(c, m.SettingsFontSize(), func() {
			fontSizeStepper(c, a.editorSize, defaultEditorFontSize, a.setEditorFontSize)
		})
		a.fontStyleRows(c, func(s *uiState) (*int, *int, *float32, *bool) {
			return &s.EditorWeight, &s.EditorBoldWeight, &s.EditorLineHeight, &s.EditorSmooth
		}, defaultEditorLineHeight, maxEditorLineHeight, a.setEditorStyle)
		a.editorPreview(c)
	})
}

// versionLabel 把版本和构建标识（提交加可执行文件时间）写成"0.1.0（提交，有未提交改动，10-18 15:15 构建）"的样子。
// 带上构建时间：同一个提交打两次包时，只看提交分不出窗口和常驻进程哪个旧。两样都没有时只写版本。
func versionLabel(m locales.Messages, version, build string) string {
	rev, _, _ := strings.Cut(build, " ")
	var parts []string
	if rev != "" {
		parts = append(parts, strings.TrimSuffix(rev, "+"))
	}
	if strings.HasSuffix(rev, "+") {
		parts = append(parts, m.SettingsVersionDirty())
	}
	if t, ok := buildTime(build); ok {
		parts = append(parts, m.SettingsVersionBuilt(m.AppDigitsDateTime(t.Local().Format("01-02"), t.Local())))
	}
	if len(parts) == 0 {
		return version
	}
	return m.SettingsVersionDetails(version, strings.Join(parts, m.SettingsVersionSeparator()))
}

func (a *app) aboutSettings(c *ui.Context) {
	t := c.Theme()
	d := a.daemon
	stale := d.Build != daemon.Build
	m := L(c)
	settingsCard(c, m.SettingsAboutTitle(), "", func() {
		settingsRow(c, m.SettingsAboutWindow(), func() {
			ui.Text(c, versionLabel(m, daemon.Version, daemon.Build)).FontSize(fsCode).Font(monoFont).TextColor(t.TextMuted)
		})
		settingsRow(c, m.SettingsAboutDaemon(), func() {
			switch {
			case d.Pid == 0:
				ui.Text(c, m.SettingsDaemonNotConnected()).FontSize(fsBody).TextColor(t.TextMuted)
			case d.Build == "":
				ui.Text(c, m.SettingsDaemonOldVersion(d.Version)).FontSize(fsCode).Font(monoFont).TextColor(t.TextMuted)
			default:
				ui.Text(c, versionLabel(m, d.Version, d.Build)).FontSize(fsCode).Font(monoFont).TextColor(t.TextMuted)
			}
		})
		if d.Pid != 0 {
			c.After(time.Minute)
			settingsRow(c, "", func() {
				ui.Text(c, m.SettingsDaemonStarted(fmt.Sprint(d.Pid), agoInline(m, d.StartedAt))).FontSize(fsSmall).TextColor(t.TextMuted)
			})
		}
		updating, unsupported := a.upgrade.status(d.Pid)
		canUpdate := d.Pid != 0 && stale && !unsupported && d.Build != ""
		// 窗口比常驻进程旧（打开了旧的 Posse.app）时不催更新：更新会把常驻进程换成窗口的旧版本
		windowOlder := false
		if mine, ok := buildTime(daemon.Build); ok {
			if theirs, ok := buildTime(d.Build); ok {
				windowOlder = mine.Before(theirs)
			}
		}
		if d.Pid != 0 && stale {
			note := m.SettingsDaemonStale()
			switch {
			case updating:
				note = m.SettingsDaemonUpdating()
			case !canUpdate:
				note = m.SettingsDaemonNeedsRestart()
			case windowOlder:
				note = m.SettingsDaemonWindowOlder()
			}
			ui.Text(c, note).FontSize(fsBody).TextColor(t.Text).LineHeight(1.5)
		}
		ui.Row(c).Gap(8).Padding(4, 0, 0, 0).Children(func() {
			if canUpdate {
				update := accentButton
				if windowOlder {
					update = textButton
				}
				b := pressable(update(c, m.SettingsUpdateDaemon()))
				if updating {
					b.Disabled(true)
				}
				if b.Clicked() {
					a.upgradeDaemon(m, d.Pid, false)
				}
			}
			button := textButton
			if d.Pid != 0 && stale && !canUpdate {
				button = accentButton
			}
			if pressable(button(c, m.SettingsRestartDaemon())).Clicked() {
				a.askRestartDaemon(m)
			}
			if pressable(textButton(c, m.SettingsOpenLog())).Clicked() {
				mygo.Shell.OpenPath(filepath.Join(a.home, "daemon.log"))
			}
			if pressable(textButton(c, m.SettingsOpenDataDir())).Clicked() {
				mygo.Shell.OpenPath(a.home)
			}
		})
	})
}

// askRestartDaemon 先确认再停掉常驻进程；下一次轮询连不上 socket 时，窗口用自己的包把它拉起来。
func (a *app) askRestartDaemon(m locales.Messages) {
	a.confirm(m.SettingsRestartDaemonConfirm(), m.SettingsRestartDaemonBody(max(a.daemon.Running, 0)), m.SettingsRestart(), func() {
		go func() {
			a.call("daemon.stop", nil, nil, 5*time.Second)
			a.refresh()
		}()
	})
}
