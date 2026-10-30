package app

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// termFind 是终端的查找栏，搜索交给终端插件（libghostty 的 find）：窗口这份内核的屏幕和回滚区，
// 和常驻进程的一样大。只认原文，ASCII 字母不分大小写；程序还在输出时命中跟着变。
// 状态跟着终端视图走，切到别的终端就没了；被常驻进程断开、重新接入时由 ensureView 带到新视图上。
type termFind struct {
	open  bool
	query string
	// focus：查找栏搭出来时把焦点给输入框。⌘F 和紧跟着按的键常在同一批事件里、在搭界面之前就派给了终端，
	// 这期间的字由 termFindText、回车退格这些键由 termFindKey 接住，不发给程序
	focus bool
	// healed：这次搜索已经替插件选回过一次命中（见 termFindBar），之后选中的命中再没了就不管，免得把视口拽走
	healed bool
}

// search 从头搜 query。
func (f *termFind) search(v *termView) {
	f.healed = false
	v.term.Search(f.query)
}

// openTermFind 是终端里的 ⌘F：打开查找栏；有选中的文字就拿它当搜索词（只取单行的）。
func (a *app) openTermFind(v *termView) {
	f := &v.find
	if sel := v.term.Selection(); sel != "" && !strings.Contains(sel, "\n") {
		f.query = sel
	}
	// 关掉再打开时接着搜上次的词
	if f.query != "" {
		f.search(v)
	}
	f.open, f.focus = true, true
}

// termFindText 是终端的 OnText：查找栏刚打开、输入框还没拿到焦点时，打的字接到搜索词后面，不发给程序。
func (a *app) termFindText(v *termView, text string) bool {
	f := &v.find
	if !a.live(v) || !f.open || !f.focus {
		return false
	}
	f.query += text
	f.search(v)
	return true
}

// termFindKey 和 termFindText 管同一段时间里不出字的键：照输入框的意思处理回车、⇧回车、退格和 Esc，
// 方向键、Tab 和带 Control 的键吞掉，都不发给程序（回车会提交 Claude 输入框里的内容，⌃C 会打断它）。
// 带 Command 的照常交给快捷键；出字的键返回假，字随后经 OnText 进来。
func (a *app) termFindKey(v *termView, mods ui.Modifiers, key ui.Key) bool {
	f := &v.find
	if !a.live(v) || !f.open || !f.focus || mods&ui.Super != 0 {
		return false
	}
	switch key {
	case ui.KeyEnter:
		if mods&ui.Shift != 0 {
			v.term.SearchPrevious()
		} else {
			v.term.SearchNext()
		}
	case ui.KeyBackspace:
		if _, n := utf8.DecodeLastRuneInString(f.query); n > 0 {
			f.query = f.query[:len(f.query)-n]
			f.search(v)
		}
	case ui.KeyEscape:
		a.closeTermFind(v)
	case ui.KeyTab, ui.KeyDelete, ui.KeyUp, ui.KeyDown, ui.KeyLeft, ui.KeyRight,
		ui.KeyHome, ui.KeyEnd, ui.KeyPageUp, ui.KeyPageDown:
	default:
		return mods&ui.Ctrl != 0
	}
	return true
}

func (a *app) closeTermFind(v *termView) {
	v.find = termFind{query: v.find.query}
	v.term.EndSearch()
	a.focusTerm = true
}

// termFinding 报告终端的查找栏开着、焦点在终端区域里（终端或查找栏），这时 ⌘G、⇧⌘G 归它。
func (a *app) termFinding(c *ui.Context) bool {
	v := a.focusView()
	return v != nil && v.find.open && c.FocusWithin(a.termAreaRef[a.pane])
}

// termFindKeys 在查找栏开着时处理跳转：⌘G 往上（更早的输出），⇧⌘G 往下。按键跟着编辑器的查找下一个、上一个。
// 窗口的快捷键先于焦点拿到按键，焦点在终端里或输入框里都认。
func (a *app) termFindKeys(c *ui.Context) {
	if !a.termFinding(c) {
		return
	}
	switch v := a.focusView(); {
	case a.pressed(c, "editor.findNext"):
		v.term.SearchNext()
	case a.pressed(c, "editor.findPrevious"):
		v.term.SearchPrevious()
	}
}

// termFindBar 画浮在终端右上角的查找栏（findBar）。
// agent 终端多一个"在历史里搜"：Claude 重画时连回滚区一起清掉、Codex 在备用屏幕上，终端里只搜得到眼前这一屏。
func (a *app) termFindBar(c *ui.Context, v *termView, cur *terminalInfo) {
	f := &v.find
	if !f.open {
		return
	}
	switch st := v.term.SearchStatus(); {
	case f.query != "" && st.Text != f.query:
		// 重新接入后的新终端还没在搜
		f.search(v)
	case !f.healed && st.Total > 0 && st.Selected == 0 && !st.Searching:
		// 搜索刚开始时终端改了尺寸，插件就丢了选中的命中、也不再选：选回最新的那个，每次搜索只补一次
		f.healed = true
		v.term.SearchNext()
	}
	right := float32(findBarRight)
	if (cur.Agent != "" || a.taskByWorker(cur.ID) != nil) && !a.state.SessionButtonsHidden {
		right += sessionColWidth() // 让开右上角那一列会话按钮
	}
	// 终端视图先搭，这一帧推进过的搜索在这里已经算进去了；输入框改了词之后要再取
	st := v.term.SearchStatus()
	agent := cur.Agent != ""
	a.findBar(c, findBarSpec{
		key: "term-find", label: "在终端里查找", right: right,
		query: &f.query, focus: &f.focus,
		changed: func() { f.search(v); st = v.term.SearchStatus() },
		submit:  v.term.SearchNext, submitBack: v.term.SearchPrevious,
		up: v.term.SearchNext, down: v.term.SearchPrevious,
		upTip:   a.withKey("往上（更早的输出）", "editor.findNext"),
		downTip: a.withKey("往下（更新的输出）", "editor.findPrevious"),
		close:   func() { a.closeTermFind(v) },
		status: func() string {
			switch {
			case f.query == "":
				return ""
			case st.Total == 0 && st.Searching:
				return "搜索中"
			case st.Total == 0:
				return "无结果"
			case st.Selected == 0:
				return fmt.Sprint(st.Total)
			}
			return fmt.Sprintf("%d/%d", st.Selected, st.Total)
		},
		extra: func() {
			if agent && iconButtonSized(c, iconHistory, "在历史里搜", compactBox(), smallIcon()).Clicked() {
				a.searchInHistory(f.query, cur.Agent)
			}
		},
		below: func() {
			// 搜不到时把话说明白，免得以为查找坏了
			if !agent || f.query == "" || st.Total > 0 || st.Searching {
				return
			}
			ui.Row(c).Padding(4, 8, 2, 8).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "agent 的对话不一定留在终端里").FontSize(fsSmall).TextColor(c.Theme().TextMuted).Shrink(1)
				ui.Spacer(c)
				if pressable(textButton(c, "在历史里搜")).Clicked() {
					a.searchInHistory(f.query, cur.Agent)
				}
			})
		},
	})
}

// searchInHistory 跳到历史页，按 agent 筛选、搜 query。
func (a *app) searchInHistory(query, agent string) {
	h := &a.history
	h.query = query
	if i := slices.Index(historyAgents, agent); i >= 0 {
		h.agent = i
	}
	// 历史页出现时（setHistoryVisible）按这个词查
	a.selected, a.revealSel = historyBoardID, true
}
