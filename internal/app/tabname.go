package app

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// startRename 让终端标签原地变成输入框，填上现在显示的名字。
func (a *app) startRename(term terminalInfo) {
	name, detail := tabLabel(term)
	if title, ok := a.workerTitle[term.ID]; ok && term.Name == "" {
		detail = title
	}
	switch {
	case term.Name != "":
		name = term.Name
	case term.Agent != "" && detail != "" && detail != "已退出":
		name = detail
	}
	a.renaming, a.renameText = term.ID, name
}

// renameInput 是改名时标签里的输入框：回车或点别处保存，Esc 取消；清空等于恢复默认名称。
func (a *app) renameInput(c *ui.Context, term terminalInfo) {
	in := ui.TextInputBase(c, &a.renameText).Label("标签名").FontSize(12).MinWidth(120).Grow(1)
	// 终端视图出现时会自动聚焦（切回这个标签、重新接入），没点别处就把焦点拿回来
	if !in.Focused() {
		in.Focus()
	}
	switch {
	case in.Shortcut(0, ui.KeyEscape):
		a.renaming = ""
	case in.Submitted(), in.PressedOutside():
		// 点了别处也保存，和访达改名一样
		a.finishRename(term)
	}
}

func (a *app) finishRename(term terminalInfo) {
	a.renaming = ""
	name := strings.TrimSpace(a.renameText)
	// 和默认显示的一样就不记，之后会话标题变了还能跟着变
	if def, detail := tabLabel(terminalInfo{ID: term.ID, Title: term.Title, Agent: term.Agent, AgentState: term.AgentState, Running: true}); term.Name == "" && (name == def || name == detail) {
		return
	}
	a.renameTerminal(term.ID, name)
}

// renameTerminal 记下终端标签的名字，name 为空时恢复默认（会话标题或终端标题）。
func (a *app) renameTerminal(id, name string) {
	if name == "" {
		delete(a.state.TermNames, id)
	} else {
		if a.state.TermNames == nil {
			a.state.TermNames = map[string]string{}
		}
		a.state.TermNames[id] = name
	}
	if _, t := a.findTerminal(id); t != nil {
		t.Name = name
	}
	a.saveState()
}
