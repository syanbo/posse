package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
)

// skillRunView 是技能的安装、更新、删除对话框：写明要跑的命令，确认后在对话框里嵌一个终端跑（照 Orca 的技能设置面板），
// 跑完按退出码给结果，成功了重扫。终端是常驻进程里的普通终端，只是窗口不把它放进标签栏和侧栏（hiddenTerms）；
// 跑着时关掉对话框不杀命令，转成普通终端标签接着跑。
type skillRunView struct {
	open  bool
	title string
	about string
	line  string // 要跑的 shell 命令
	ws    string

	id       string             // 跑起来后的终端
	term     *terminal.Terminal // 接入它的视图
	starting bool
	gen      int    // 每次开跑加一：等终端建好时认得出是不是这一次
	err      string // 建终端失败的原因：错误条被对话框挡着，在框里也写一遍
	exit     *int   // 命令退出码，还在跑时为空
}

// askSkillCommand 弹框写明命令，确认后在框里的终端跑；start 为真时直接跑（安装对话框里已经确认过命令）。
func (a *app) askSkillCommand(title, about, line string, w *workspace, start bool) {
	if w == nil {
		a.err = "先添加一个项目：命令要在工作区的终端里跑。"
		return
	}
	a.closeSkillRun()
	gen := a.skillRun.gen
	a.skillRun = skillRunView{open: true, title: title, about: about, line: line, ws: w.ID, gen: gen}
	if start {
		a.startSkillRun()
	}
}

// startSkillRun 建终端跑命令。命令跑完 shell 就退出，终端的退出码就是命令的。
func (a *app) startSkillRun() {
	r := &a.skillRun
	r.gen++
	r.starting, r.err = true, ""
	sh, ws, line, gen := userShell(), r.ws, r.line, r.gen
	go func() {
		var t terminalInfo
		err := a.call("terminal.create", map[string]any{"workspace": ws, "command": []string{sh, "-l", "-i", "-c", line}}, &t, 10*time.Second)
		a.update(func() {
			if r.gen != gen {
				// 等终端建好时对话框已经关了或换了一次：命令照跑，留在标签栏里
				return
			}
			r.starting = false
			if err != nil {
				r.err = "打不开终端：" + err.Error()
				return
			}
			if a.hiddenTerms == nil {
				a.hiddenTerms = map[string]bool{}
			}
			a.hiddenTerms[t.ID] = true
			light, dark := terminalThemes()
			term, err := terminal.New(terminal.Options{Conn: a.c.Attach(t.ID), Font: a.termFont(), Theme: light, DarkTheme: dark, NoWheelZoom: true})
			if err != nil {
				a.err = err.Error()
				return
			}
			r.id, r.term, r.exit = t.ID, term, nil
		})
	}()
}

// skillRunExited 由轮询交来藏起来的终端的状态：命令跑完了记下退出码，成功了重扫技能。
func (a *app) skillRunExited(t terminalInfo) {
	r := &a.skillRun
	if t.ID != r.id || r.exit != nil || t.Running || t.ExitCode == nil {
		return
	}
	code := *t.ExitCode
	r.exit = &code
	if code == 0 {
		if a.skills.visible {
			a.scanSkills()
		}
		a.skillsTab.poll.at = time.Time{}
	}
}

// closeSkillRun 收起对话框：命令跑完了就关掉它的终端；还在跑的交回标签栏接着跑。
func (a *app) closeSkillRun() {
	r := &a.skillRun
	if r.term != nil {
		r.term.Close()
	}
	if r.id != "" {
		delete(a.hiddenTerms, r.id)
		if r.exit != nil {
			id := r.id
			go a.closeTerminal(id)
		} else {
			a.pollForce.Store(true)
			go a.refresh()
		}
	}
	*r = skillRunView{gen: r.gen + 1}
}

// showSkillRun 把还在跑或失败了的命令转到终端标签里看。
func (a *app) showSkillRun() {
	r := &a.skillRun
	ws, id := r.ws, r.id
	r.exit = nil // 留着终端，不随对话框关掉
	a.closeSkillRun()
	// 先刷新让它回到列表再选中：列表里还没有它时 currentTerminal 会把选中换回第一个
	go func() {
		a.refresh()
		a.update(func() { a.openTerminal(ws, id) })
	}()
}

func (a *app) skillRunDialog(c *ui.Context) {
	t := c.Theme()
	r := &a.skillRun
	if !r.open && r.title == "" {
		return
	}
	ui.Modal(c, &r.open, func() {
		ui.Column(c).Width(560).Gap(12).Children(func() {
			titleText(c, r.title, fsTitle)
			if r.about != "" {
				ui.Text(c, r.about).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45).Selectable()
			}
			ui.Text(c, r.line).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Padding(8, 12).Radius(rCard).Background(colors(t).code).Selectable()
			if r.term != nil {
				// 留一点内边距，文字不贴边；底色和终端一致
				ui.Box(c).Height(sz(280)).Radius(rCard).Clip().Background(t.Background).Padding(6, 4, 2, 12).Children(func() {
					terminal.View(c.Key(r.id), r.term).Fill().AutoFocus()
				})
			}
			switch {
			case r.err != "":
				ui.Text(c, r.err).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.45).Selectable()
			case r.starting:
				ui.Text(c, "正在打开终端…").FontSize(fsSmall).TextColor(t.TextMuted)
			case r.exit != nil && *r.exit == 0:
				ui.Text(c, "已完成，技能列表已重新扫描。").FontSize(fsSmall).TextColor(colors(t).sage)
			case r.exit != nil:
				ui.Text(c, fmt.Sprintf("命令退出，代码 %d。", *r.exit)).FontSize(fsSmall).TextColor(t.Danger)
			case r.term != nil:
				ui.Text(c, "正在运行，可以在上面的终端里回答它的提问。关掉对话框不会停止，命令会转到终端标签里接着跑。").FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				switch {
				case r.term == nil:
					if pressable(textButton(c, "取消")).Clicked() {
						r.open = false
					}
					run := ui.Box(c).Shrink(0)
					if r.starting {
						run.Disabled(true).Opacity(0.5)
					}
					run.Children(func() {
						if pressable(accentButton(c, "运行")).Clicked() {
							a.startSkillRun()
						}
					})
				case r.exit == nil:
					if pressable(textButton(c, "转到终端标签")).Clicked() {
						a.showSkillRun()
					}
				case *r.exit == 0:
					if pressable(accentButton(c, "完成")).Clicked() {
						r.open = false
					}
				default:
					if pressable(textButton(c, "在终端标签中打开")).Clicked() {
						a.showSkillRun()
					}
					if pressable(textButton(c, "关闭")).Clicked() {
						r.open = false
					}
					if pressable(accentButton(c, "重试")).Clicked() {
						title, about, line, ws := r.title, r.about, r.line, a.workspace(r.ws)
						a.askSkillCommand(title, about, line, ws, true)
					}
				}
			})
		})
	})
	// 点了框外、按了 Esc 或按钮关掉
	if !r.open && r.title != "" {
		a.closeSkillRun()
	}
}

// installNodeLine 是用 Homebrew 装 Node.js 的命令。
func installNodeLine(brew string) string { return agent.Quote(brew) + " install node" }
