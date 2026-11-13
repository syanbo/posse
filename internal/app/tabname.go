package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/rpc"
	"posse/locales"
)

type termNameBase struct {
	Agent     string `json:"agent,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
	Title     string `json:"title,omitempty"`
}

func termNameOrigin(t terminalInfo) termNameBase {
	base := termNameBase{Agent: t.Agent}
	if t.AgentState != nil {
		base.SessionID, base.Title = t.AgentState.SessionID, t.AgentState.Title
	}
	return base
}

// defaultTaskTitle 只在终端尚未被显式命名时用任务标题兜底。
func (a *app) defaultTaskTitle(term terminalInfo) string {
	if term.Name != "" || term.AgentState != nil && term.AgentState.Title != "" {
		return ""
	}
	return a.workerTitle[term.ID]
}

// startRename 让终端标签原地变成输入框，填上现在显示的名字。
func (a *app) startRename(m locales.Messages, term terminalInfo) {
	name, detail := fullLabel(m, term)
	// 退出的终端 detail 是"已退出"，不是标题，不拿来当名字
	exited := !term.Running && !term.Lost
	if title := a.defaultTaskTitle(term); title != "" {
		detail, exited = title, false
	}
	switch {
	case term.Name != "":
		name = term.Name
	case term.Agent != "" && detail != "" && !exited:
		name = detail
	}
	a.renaming, a.renameText = term.ID, name
}

// renameInput 是改名时标签里的输入框：回车或点别处保存，Esc 取消；清空等于恢复默认名称。
func (a *app) renameInput(c *ui.Context, term terminalInfo) {
	in := ui.TextInputBase(c, &a.renameText).Label(L(c).TabRenameLabel()).FontSize(fsSmall).MinWidth(120).Grow(1)
	// 终端视图出现时会自动聚焦（切回这个标签、重新接入），没点别处就把焦点拿回来
	if !in.Focused() {
		in.Focus()
	}
	switch {
	case in.Shortcut(0, ui.KeyEscape):
		a.renaming = ""
	case in.Submitted(), in.PressedOutside():
		// 点了别处也保存，和访达改名一样
		a.finishRename(L(c), term)
	}
}

func (a *app) finishRename(m locales.Messages, term terminalInfo) {
	a.renaming = ""
	name := strings.TrimSpace(a.renameText)
	// 和默认显示的一样就不记，之后会话标题变了还能跟着变
	if def, detail := fullLabel(m, terminalInfo{ID: term.ID, Title: term.Title, Agent: term.Agent, AgentState: term.AgentState, Running: true}); term.Name == "" && (name == def || name == detail || name == a.defaultTaskTitle(term)) {
		return
	}
	a.renameTerminal(term.ID, name)
}

// renameTerminal 记下终端标签的名字，name 为空时恢复默认（会话标题或终端标题）。
// 名字由常驻进程记着，手机上也显示；交过去之前先记在 ui.json 里，界面马上换名字。
func (a *app) renameTerminal(id, name string) {
	if a.state.TermNames == nil {
		a.state.TermNames = map[string]string{}
	}
	a.state.TermNames[id] = name
	if a.state.TermNameBases == nil {
		a.state.TermNameBases = map[string]termNameBase{}
	}
	a.state.TermNameBases[id] = termNameBase{}
	if _, t := a.findTerminal(id); t != nil {
		a.state.TermNameBases[id] = termNameOrigin(*t)
		t.Name = name
	}
	a.saveState()
	a.sendTermName(id, name)
}

// syncTermName 在每次轮询时调用：还没交给常驻进程的名字先显示本地的，常驻进程报上来的一样了就不再记着。
func (a *app) syncTermName(t *terminalInfo) {
	name, ok := a.state.TermNames[t.ID]
	if !ok {
		return
	}
	if t.Name == name {
		a.ackTermName(t.ID, name)
		return
	}
	// 待交的名字可能因为断线留到下一轮；后来明确设置的会话标题优先，不能重发旧名字盖回去。
	if base, ok := a.state.TermNameBases[t.ID]; ok && t.Name == "" && t.AgentState != nil && t.AgentState.Title != "" && base != termNameOrigin(*t) {
		a.ackTermName(t.ID, name)
		return
	}
	t.Name = name
	if sent, ok := a.nameSent[t.ID]; !ok || sent != name {
		a.sendTermName(t.ID, name)
	}
}

// ackTermName 收到成功回复就撤下本地覆盖：下一次轮询前会话可能又被命令改名，
// 不能一直等常驻进程报回旧名字。较早请求的回复不能清掉后来输入的新名字。
func (a *app) ackTermName(id, name string) {
	if pending, ok := a.state.TermNames[id]; ok && pending == name {
		delete(a.state.TermNames, id)
		delete(a.state.TermNameBases, id)
		delete(a.nameSent, id)
		a.saveState()
	}
}

// confirmTermName 等在途轮询先提交界面更新，再撤掉本地覆盖。
// refreshMu 同时保证后续轮询在成功回复之后取数，不会用旧结果把名字改回去。
func (a *app) confirmTermName(id, name string) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	a.update(func() { a.ackTermName(id, name) })
}

// sendTermName 把名字交给常驻进程。同一个名字只发一次；发失败（常驻进程在交接、超时）就忘掉，
// 下次轮询再发。旧常驻进程不认 terminal.rename 时不再发，名字一直留在 ui.json 里。
func (a *app) sendTermName(id, name string) {
	if a.nameSent == nil {
		a.nameSent = map[string]string{}
	}
	a.nameSent[id] = name
	if a.c == nil {
		return
	}
	a.renameOnce.Do(func() {
		a.renameQ = make(chan [2]string, 64)
		go a.renameWorker(a.renameQ)
	})
	select {
	case a.renameQ <- [2]string{id, name}:
	default:
		delete(a.nameSent, id)
	}
}

// renameWorker 按顺序发改名：连着改两次时，常驻进程最后记的是后一个。
func (a *app) renameWorker(q <-chan [2]string) {
	for r := range q {
		id, name := r[0], r[1]
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := a.c.Call(ctx, "terminal.rename", map[string]any{"terminal": id, "name": name}, nil)
		cancel()
		if err == nil {
			a.confirmTermName(id, name)
			a.refresh()
			continue
		}
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == rpc.CodeUnknown {
			continue
		}
		a.update(func() {
			if a.nameSent[id] == name {
				delete(a.nameSent, id)
			}
		})
	}
}
