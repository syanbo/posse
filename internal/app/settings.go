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
)

// settingsBoardID 是设置页的 ID，不在侧栏里，从 ⌘, 和侧栏顶栏的齿轮进来。
const settingsBoardID = "settings"

func (a *app) openSettings() {
	a.selected = settingsBoardID
}

// settingsTabs 是设置页左边的分类，右边只放选中的一类。
var settingsTabs = []struct {
	id, label string
	icon      *ui.SVG
}{
	{"general", "通用", iconSettings},
	{"agent", "Agent", iconSkill},
	{"appearance", "外观", iconAppearance},
	{"keys", "快捷键", iconKeyboard},
	{"about", "关于", iconInfo},
}

// settingsBoard 是设置页：左边分类，右边是这一类的卡片。通用（防休眠、恢复终端、终端选区、通知）、Agent（权限、套餐用量）、
// 外观（图标大小，终端和编辑器的字体、字号、预览）、快捷键（keysettings.go）、关于（版本、更新和重启常驻进程、日志和数据目录）。手机配对在「手机」页。
func (a *app) settingsBoard(c *ui.Context) {
	t := c.Theme()
	a.band(c, func() {
		titleText(c, "设置", fsTitle)
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
				if a.navRowAt(c, "settings:"+s.id, s.id == tab, icon, s.label, "", "", nil).Clicked() {
					a.settingsTab = s.id
				}
			}
		})
		ui.Scroll(c.Key("settings:" + tab)).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
			ui.Column(c).Padding(24).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Width(640).MaxWidth(640).Gap(16).Children(func() {
					switch tab {
					case "general":
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
	settingsCard(c, "防休眠", "Posse 运行时不让电脑休眠，屏幕照常熄灭；底栏的咖啡杯也能切换。「运行时亮屏」让屏幕也不熄：屏幕熄了会跟着锁屏。", func() {
		if st == nil {
			ui.Text(c, "更新常驻进程后可用。").FontSize(fsBody).TextColor(c.Theme().TextMuted)
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
		ui.Text(c, awakeTip(st)).FontSize(fsSmall).TextColor(c.Theme().TextMuted)
	})
}

// restoreSettings 是重启电脑或常驻进程之后怎么恢复上次的终端，见 internal/daemon/restore.go。
func (a *app) restoreSettings(c *ui.Context) {
	settingsCard(c, "恢复终端", "重启电脑或常驻进程后，上次的终端照常列在侧栏和标签栏（手机上不列），标着「未恢复」，切到它时才拉起：agent 续接原来的会话，普通终端在原来的目录重开，之前的输出不保留。打开下面的开关，常驻进程一启动就全部恢复（agent 多时会一起占内存和用量）。", func() {
		on := a.restoreAll
		settingsRow(c, "启动时恢复全部终端", func() {
			if ui.Switch(c.Key("restoreAll"), &on).Label("启动时恢复全部终端").Changed() {
				a.setRestoreAll(on)
			}
		})
	})
}

func (a *app) linkSettings(c *ui.Context) {
	settingsCard(c, "终端链接", "默认单击链接弹出卡片，⌘ 点击直接打开：网址在网页标签里打开，文件在文件标签里打开并跳到行号，目录在文件栏中显示。⇧⌘ 点击交给系统：网址用默认浏览器，文件用默认程序，目录在访达中打开。「单击直接打开」让单击和 ⌘ 点击反过来；「互换」让直接打开和交给系统的按法对调，⌘ 点击（开了单击直接打开时是单击）交给系统，⇧⌘ 点击在 Posse 里打开。", func() {
		on := a.state.LinkClickOpens
		settingsRow(c, "单击直接打开", func() {
			if ui.Switch(c.Key("linkClickOpens"), &on).Label("单击直接打开").Changed() {
				a.state.LinkClickOpens = on
				a.saveState()
			}
		})
		swap := a.state.LinkSwapSystem
		settingsRow(c, "⌘ 点击和 ⇧⌘ 点击互换", func() {
			if ui.Switch(c.Key("linkSwapSystem"), &swap).Label("⌘ 点击和 ⇧⌘ 点击互换").Changed() {
				a.state.LinkSwapSystem = swap
				a.saveState()
			}
		})
	})
}

func (a *app) selectSettings(c *ui.Context) {
	settingsCard(c, "终端选区", "打开后，在终端里用鼠标选中文字、松手就复制到剪贴板，全选也会复制；关掉时照常用 ⌘C 或右键菜单复制。", func() {
		on := a.state.TermCopyOnSelect
		settingsRow(c, "选中即复制", func() {
			if ui.Switch(c.Key("termCopyOnSelect"), &on).Label("选中即复制").Changed() {
				a.setCopyOnSelect(on)
			}
		})
	})
}

// notifySettings 是系统通知和 Dock 角标。通知由窗口发，关掉窗口后就不再有。
func (a *app) notifySettings(c *ui.Context) {
	settingsCard(c, "通知", "agent 等你处理、完成了而你没在看，以及任务有问题、完成或失败时发系统通知，点通知跳到那个会话或任务。窗口开着才会通知，正看着的会话不通知。Dock 角标是等你处理和完成了还没看的会话数。", func() {
		notify, badge := !a.state.NotifyOff, !a.state.BadgeOff
		settingsRow(c, "系统通知", func() {
			if ui.Switch(c.Key("notify"), &notify).Label("系统通知").Changed() {
				a.state.NotifyOff = !notify
				a.saveState()
			}
		})
		settingsRow(c, "Dock 角标", func() {
			if ui.Switch(c.Key("badge"), &badge).Label("Dock 角标").Changed() {
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
			a.err = err.Error()
			return
		}
	}
	a.restoreAll = on
}

var permissionModes = []struct{ mode, seg, tip string }{
	{agent.PermissionSkip, "跳过审批", "启动时带上各家跳过确认的参数：Claude --dangerously-skip-permissions，Codex --dangerously-bypass-approvals-and-sandbox（同时不用沙盒），Grok --permission-mode bypassPermissions，Cursor --yolo。"},
	{agent.PermissionAsk, "逐项确认", "照各家自己的设置，读写文件、执行命令前停下来问；Claude 的确认也会推到配对的手机上。"},
}

func (a *app) permissionSettings(c *ui.Context) {
	settingsCard(c, "Agent 权限", "在 Posse 里启动的 agent（包括在终端里手动敲的 claude、codex、cursor-agent）用哪种权限。改了之后新开的 agent 生效，已经在运行的不变。", func() {
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
		ui.Text(c, permissionModes[sel].tip).FontSize(fsSmall).TextColor(c.Theme().TextMuted).LineHeight(1.5)
	})
}

// agentSettings 是各家 agent 的开关：关掉的不出现在新建会话、派发任务、新建工作区和手机里，常驻进程也拒绝启动它。
func (a *app) agentSettings(c *ui.Context) {
	settingsCard(c, "Agent", "关掉的 agent 不出现在新建会话、派发任务、新建工作区和手机里，常驻进程也不再启动它；已经在运行的不受影响，在终端里自己敲命令照样能用。只列本机装了的。", func() {
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
			a.err = err.Error()
			return
		}
		a.disabled = agent.Disabled(a.home)
	}
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

// interfaceSettings 是界面的字号、字重、字体平滑和图标大小。
func (a *app) interfaceSettings(c *ui.Context) {
	settingsCard(c, "界面", "字号是正文的大小：说明文字小 2 号，卡片标题大 2 号，页面标题大 4 号，行高跟着变。粗体字重用在标题上，列表标题和按钮取正文和粗体中间的字重。", func() {
		setType := func(f func(s *uiState)) {
			f(&a.state)
			setUIType(a.state.UIFontSize, a.state.UIWeight, a.state.UIBoldWeight)
			a.saveState()
		}
		settingsRow(c, "字号", func() {
			fontSizeStepper(c, fsBody, defaultUIFontSize, func(size float32) {
				setType(func(s *uiState) {
					s.UIFontSize = 0
					if size = min(max(size, minUIFontSize), maxUIFontSize); size != defaultUIFontSize {
						s.UIFontSize = size
					}
				})
			})
		})
		settingsRow(c, "字重", func() {
			stepper(c, "字重", fmt.Sprint(fwBody),
				func(d int) { setType(func(s *uiState) { s.UIWeight = stepWeight(fwBody, d) }) },
				func() { setType(func(s *uiState) { s.UIWeight = 0 }) })
		})
		settingsRow(c, "粗体字重", func() {
			stepper(c, "粗体字重", fmt.Sprint(fwBold),
				func(d int) { setType(func(s *uiState) { s.UIBoldWeight = stepWeight(fwBold, d) }) },
				func() { setType(func(s *uiState) { s.UIBoldWeight = 0 }) })
		})
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
		settingsRow(c, "侧栏项目显示分支和路径", func() {
			sel := 1
			if a.state.ProjectMetaHidden {
				sel = 0
			}
			if segmented(c, &sel, "侧栏项目显示分支和路径", "关", "开") {
				a.state.ProjectMetaHidden = sel == 0
				a.saveState()
			}
		})
		settingsRow(c, "侧栏工作区显示分支", func() {
			sel := 1
			if a.state.WorkspaceMetaHidden {
				sel = 0
			}
			if segmented(c, &sel, "侧栏工作区显示分支", "关", "开") {
				a.state.WorkspaceMetaHidden = sel == 0
				a.saveState()
			}
		})
		settingsRow(c, "右侧文件栏开关", func() {
			sel := 0
			if a.state.FilesPanelShared {
				sel = 1
			}
			if segmented(c, &sel, "右侧文件栏开关", "跟着项目", "所有项目共用") {
				a.setFilesPanelShared(sel == 1)
			}
		})
		settingsRow(c, "字体平滑", func() {
			sel := 0
			if uiSmooth {
				sel = 1
			}
			if segmented(c, &sel, "界面字体平滑", "关", "开") {
				uiSmooth = sel == 1
				a.state.UISmooth = uiSmooth
				a.saveState()
			}
		})
	})
}

func (a *app) termFontSettings(c *ui.Context) {
	settingsCard(c, "终端", "终端里也可以用 ⌘= 放大、⌘- 缩小、⌘0 恢复默认。字体平滑是 macOS 原生 App 画字的方式，深色底上的浅色字会显得重一档；关掉时和 Ghostty 默认一样细。", func() {
		settingsRow(c, "字体", func() {
			a.fontPicker(c, &a.termPick, a.termFamily, "终端字体", a.setTermFamily)
		})
		settingsRow(c, "字号", func() {
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
	settingsRow(c, "字重", func() {
		stepper(c, "字重", fmt.Sprint(weight),
			func(d int) { change(func(w, _ *int, _ *float32, _ *bool) { *w = stepWeight(weight, d) }) },
			func() { change(func(w, _ *int, _ *float32, _ *bool) { *w = 0 }) })
	})
	settingsRow(c, "粗体字重", func() {
		stepper(c, "粗体字重", fmt.Sprint(bold),
			func(d int) { change(func(_, b *int, _ *float32, _ *bool) { *b = stepWeight(bold, d) }) },
			func() { change(func(_, b *int, _ *float32, _ *bool) { *b = 0 }) })
	})
	settingsRow(c, "行高", func() {
		stepper(c, "行高", fmt.Sprintf("%.1f", lh),
			func(d int) {
				change(func(_, _ *int, l *float32, _ *bool) { *l = stepLineHeight(lh, d, maxLineHeight) })
			},
			func() { change(func(_, _ *int, l *float32, _ *bool) { *l = 0 }) })
	})
	settingsRow(c, "字体平滑", func() {
		sel := 0
		if *sm {
			sel = 1
		}
		if segmented(c, &sel, "字体平滑", "关", "开") {
			change(func(_, _ *int, _ *float32, sm *bool) { *sm = sel == 1 })
		}
	})
}

func (a *app) editorFontSettings(c *ui.Context) {
	settingsCard(c, "编辑器", "文件、diff、逐行追溯和 Git 日志里的代码用这里的字体；文件标签在前台时 ⌘= ⌘- ⌘0 调编辑器的字号。粗体字重用在 Markdown 的标题和加粗上。", func() {
		settingsRow(c, "字体", func() {
			a.fontPicker(c, &a.editorPick, a.editorFamily, "编辑器字体", a.setEditorFamily)
		})
		settingsRow(c, "字号", func() {
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
			ui.Text(c, versionLabel(daemon.Version, daemon.Build)).FontSize(fsCode).Font(monoFont).TextColor(t.TextMuted)
		})
		settingsRow(c, "常驻进程", func() {
			switch {
			case d.Pid == 0:
				ui.Text(c, "没有连上").FontSize(fsBody).TextColor(t.TextMuted)
			case d.Build == "":
				ui.Text(c, d.Version+"（旧版本）").FontSize(fsCode).Font(monoFont).TextColor(t.TextMuted)
			default:
				ui.Text(c, versionLabel(d.Version, d.Build)).FontSize(fsCode).Font(monoFont).TextColor(t.TextMuted)
			}
		})
		if d.Pid != 0 {
			c.After(time.Minute)
			settingsRow(c, "", func() {
				ui.Text(c, fmt.Sprintf("pid %d，%s启动", d.Pid, ago(d.StartedAt))).FontSize(fsSmall).TextColor(t.TextMuted)
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
			note := "常驻进程的版本比窗口旧。更新常驻进程，终端不会断开。"
			switch {
			case updating:
				note = "正在更新常驻进程…"
			case !canUpdate:
				note = "常驻进程的版本比窗口旧，而且不支持不断开终端的更新，需要重启一次。重启会关闭所有终端。"
			case windowOlder:
				note = "窗口的版本比常驻进程旧，可能打开的是旧的 Posse.app。更新会把常驻进程换成窗口的版本。"
			}
			ui.Text(c, note).FontSize(fsBody).TextColor(t.Text).LineHeight(1.5)
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
	body := "所有终端都会断开，重启后标为「未恢复」，切到时 agent 续接原来的会话，之前的输出不保留。"
	if n := a.daemon.Running; n > 0 {
		body = fmt.Sprintf("正在运行的 %d 个终端都会断开，重启后标为「未恢复」，切到时 agent 续接原来的会话，之前的输出不保留。", n)
	}
	a.confirm("重启常驻进程？", body, "重启", func() {
		go func() {
			a.call("daemon.stop", nil, nil, 5*time.Second)
			a.refresh()
		}()
	})
}
