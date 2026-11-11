package app

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/skills"
	"posse/locales"
)

// posseSkillsView 是 Posse 自带技能的安装情况（internal/skills 的 posse.go）：侧栏的安装提示和技能页的状态行用它。
// 打开窗口时读一次，装、删、重扫技能后再读（测试里不开窗口，不读本机的技能目录，也就不出提示）。
type posseSkillsView struct {
	st      skills.PosseState
	read    bool
	reading bool
	again   bool // 读的时候又要读（刚装完）：这次读完再读一遍，免得留下装之前的结果
}

// readPosseSkills 在后台重读安装情况。
func (a *app) readPosseSkills() {
	p := &a.posseSkills
	if p.reading {
		p.again = true
		return
	}
	p.reading = true
	skillsWork.Add(1)
	go func() {
		defer skillsWork.Done()
		st := skills.ReadPosse(homeDir)
		a.update(func() {
			p.st, p.read, p.reading = st, true, false
			if p.again {
				p.again = false
				a.readPosseSkills()
			}
		})
	}()
}

// installPosseSkills 装上没装的、更新旧了的 Posse 技能。之前是直接复制装的、或者没有工作区建不了跑命令的终端时直接复制；
// 别的经 npx skills 在对话框里跑，失败了对话框里可以改成直接复制。
func (a *app) installPosseSkills(m locales.Messages) {
	// 按磁盘上现在的样子算：提示出来之后用户可能自己放了同名技能，npx 会直接覆盖它
	st := skills.ReadPosse(homeDir)
	a.posseSkills.st, a.posseSkills.read = st, true
	names := st.Pending()
	if len(names) == 0 {
		return
	}
	claude := st.LinkClaude()
	src, err := skills.WritePosseSource()
	if err != nil {
		a.err = errtext.Text(err)
		return
	}
	w := a.skillsWorkspace()
	if st.Installed() && !st.NPX || w == nil {
		a.copyPosseSkills(names, claude)
		return
	}
	title := m.SkillsPosseRunTitle()
	if st.Installed() {
		title = m.SkillsPosseUpdateRunTitle()
	}
	line := skills.ShellLine(skills.PosseInstall(src, names, claude).Args())
	a.askSkillCommand(m, title, m.SkillsPosseRunBody(strings.Join(names, m.SkillsListSeparator())), line, w, true)
	a.skillRun.posse, a.skillRun.posseNames, a.skillRun.posseClaude = true, names, claude
}

// copyPosseSkills 不经 npx 直接放好技能文件，完了重读、重扫。
func (a *app) copyPosseSkills(names []string, claude bool) {
	if err := skills.CopyPosse(homeDir, names, claude); err != nil {
		a.err = errtext.Text(err)
	}
	a.posseSkillsChanged()
}

// posseSkillsChanged 在装、删技能后重读安装情况，技能页开着时一起重扫。
func (a *app) posseSkillsChanged() {
	a.readPosseSkills()
	if a.skills.visible {
		a.scanSkills()
	}
}

// posseSkillsBar 是侧栏底部的安装提示：Posse 技能没装或旧了，且这一版的技能还没提示过时出现，关掉后同一版不再出现，
// 技能页里照样能装。
func (a *app) posseSkillsBar(c *ui.Context) {
	p := &a.posseSkills
	if !a.loaded || !p.read {
		return
	}
	version := skills.PosseVersion()
	if len(p.st.Pending()) == 0 || a.state.PosseSkillsPrompted == version {
		return
	}
	m := L(c)
	title, body, verb := m.SidebarPosseSkillsTitle(), m.SidebarPosseSkillsBody(), m.SkillsPosseInstall()
	if p.st.Installed() {
		title, body, verb = m.SidebarPosseSkillsUpdateTitle(), m.SidebarPosseSkillsUpdateBody(), m.SkillsUpdate()
	}
	dismiss := func() {
		a.state.PosseSkillsPrompted = version
		a.saveState()
	}
	sideNotice(c, false, title, body, dismiss, func() {
		ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
			b := pressable(accentButton(c, verb))
			if a.skillRun.open {
				b.Disabled(true)
			}
			if b.Clicked() {
				a.installPosseSkills(m)
			}
			if pressable(textButton(c, m.SidebarPosseSkillsView())).Clicked() {
				a.selected = skillsBoardID
			}
		})
	})
}

// posseSkillsLine 是技能页列表顶上 Posse 技能的状态：没装、有更新时给按钮，有同名占着的写出来。
func (a *app) posseSkillsLine(c *ui.Context) {
	t := c.Theme()
	m := L(c)
	st := a.posseSkills.st
	if !a.posseSkills.read {
		return
	}
	pending := len(st.Pending()) > 0
	if !pending && len(st.Taken) == 0 {
		return
	}
	ui.Column(c).Margin(0, 8, 4, 8).Padding(8, 12).Gap(6).Radius(rControl).Background(colors(t).tint).Children(func() {
		if pending {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconSkill).FontSize(iconSize).TextColor(t.Accent).Shrink(0)
				text, verb := m.SkillsPosseMissing(), m.SkillsPosseInstall()
				if st.Installed() {
					text, verb = m.SkillsPosseOutdated(), m.SkillsUpdate()
				}
				ui.Text(c, text).FontSize(fsBody).Grow(1).Shrink(1)
				if pressable(accentButton(c.Key("posse-skills-install"), verb)).Clicked() {
					a.installPosseSkills(m)
				}
			})
		}
		if len(st.Taken) > 0 {
			ui.Text(c, m.SkillsPosseTaken(strings.Join(st.Taken, m.SkillsListSeparator()))).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
		}
	})
}
