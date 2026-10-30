package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 快捷键表：设置页「快捷键」列出这里的每一项，改过的存在 ui.json 的 keys（只存和默认不同的）。
// Posse 自己处理的键经 a.pressed 按表查；编辑器的键交给 mygo 编辑器的 Bindings（editorBindings）；
// 窗口菜单上的（关闭标签、关闭窗口、设置）改了重新装菜单（installMenu）。

// keyCombo 是一组按键：修饰键加一个键；then 非零时是和弦的第二下（编辑器的 ⌘K ⌘/ 这类）。
type keyCombo struct {
	mods     ui.Modifiers
	key      ui.Key
	thenMods ui.Modifiers
	thenKey  ui.Key
}

// keyScope 是快捷键起作用的地方，同一处的两项不能用同一组键；不同处的可以（⌘K 在终端里清屏、在文件里提交）。
type keyScope uint8

const (
	scopeWindow   keyScope = iota // 窗口上任何地方
	scopeFiles                    // 文件标签（编辑器以外的：保存、Git、预览查找）
	scopeHistory                  // 历史页
	scopeTerminal                 // 终端里
	scopeEditor                   // 编辑器里，交给 mygo 编辑器
	scopeSystem                   // 系统菜单，只列出来
)

type keyAction struct {
	id    string
	group string
	label string
	scope keyScope
	def   []keyCombo
	fixed bool           // 只列出来，不能改
	cmd   editor.Command // scopeEditor 的命令
	note  string         // 悬停说明
	text  string         // 设置页上代替按键写法显示的文字（⌘1…9）
}

func k(mods ui.Modifiers, key ui.Key) keyCombo { return keyCombo{mods: mods, key: key} }

// Posse 自己的快捷键。和编辑器的查找共用 editor.find 等几项，预览、Annotate 的查找栏跟着编辑器的改。
var posseKeys = []keyAction{
	{id: "newTerminal", group: "窗口与标签", label: "新建终端", def: []keyCombo{k(ui.Cmd, ui.KeyT)}},
	{id: "newWorkspace", group: "窗口与标签", label: "新建工作区", def: []keyCombo{k(ui.Cmd, ui.KeyN)}},
	{id: "closeTab", group: "窗口与标签", label: "关闭标签", def: []keyCombo{k(ui.Cmd, ui.KeyW)}, note: "在菜单栏的 File 里，改了菜单跟着改"},
	{id: "closeWindow", group: "窗口与标签", label: "关闭窗口", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyW)}, note: "在菜单栏的 File 里，改了菜单跟着改"},
	{id: "settings", group: "窗口与标签", label: "打开设置", def: []keyCombo{k(ui.Cmd, ui.KeyComma)}, note: "在菜单栏的 Posse 里，改了菜单跟着改"},
	{id: "nextTab", group: "窗口与标签", label: "下一个标签", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyBracketRight), k(ui.Ctrl, ui.KeyTab)}},
	{id: "prevTab", group: "窗口与标签", label: "上一个标签", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyBracketLeft), k(ui.Ctrl|ui.Shift, ui.KeyTab)}},
	{id: "selectTab", group: "窗口与标签", label: "切到第 1…9 个终端", def: []keyCombo{k(ui.Cmd, ui.Key1)}, fixed: true, text: "⌘1…9"},
	{id: "toggleSidebar", group: "窗口与标签", label: "收起 / 展开侧栏", def: []keyCombo{k(ui.Cmd, ui.KeyB)}},
	{id: "zoomIn", group: "窗口与标签", label: "放大字号", def: []keyCombo{k(ui.Cmd, ui.KeyEqual)}, note: "终端里调终端字号，文件标签里调编辑器字号，Markdown 预览和历史页调 Markdown 字号，看图片时放大图片"},
	{id: "zoomOut", group: "窗口与标签", label: "缩小字号", def: []keyCombo{k(ui.Cmd, ui.KeyMinus)}, note: "终端里调终端字号，文件标签里调编辑器字号，Markdown 预览和历史页调 Markdown 字号，看图片时缩小图片"},
	{id: "zoomReset", group: "窗口与标签", label: "恢复默认字号", def: []keyCombo{k(ui.Cmd, ui.Key0)}, note: "看图片时是适应窗口"},
	{id: "jumpBack", group: "窗口与标签", label: "回到跳走之前的位置", def: []keyCombo{k(ui.Ctrl, ui.KeyMinus)}},
	{id: "jumpForward", group: "窗口与标签", label: "再往前跳", def: []keyCombo{k(ui.Ctrl|ui.Shift, ui.KeyMinus)}},

	{id: "webReload", group: "网页", label: "重新载入网页", def: []keyCombo{k(ui.Cmd, ui.KeyR)}, note: "在菜单栏的 View 里，网页有焦点时也认；当前不是网页标签时不起作用"},
	{id: "webHardReload", group: "网页", label: "强制重新载入网页（不用缓存）", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyR)}, note: "在菜单栏的 View 里"},
	{id: "webMobile", group: "网页", label: "开关手机预览", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyM)}, note: "在菜单栏的 View 里，用上次选的机型"},

	{id: "quickOpen", group: "文件", label: "快速打开文件", def: []keyCombo{k(ui.Cmd, ui.KeyP)}},
	{id: "searchFiles", group: "文件", label: "在文件里检索", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyF)}},
	{id: "filesPanel", group: "文件", label: "开关文件栏", def: []keyCombo{k(ui.Cmd, ui.KeyL)}},
	{id: "fileTree", group: "文件", label: "文件树", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyE)}},
	{id: "revealFile", group: "文件", label: "在文件树中定位当前文件", def: []keyCombo{k(ui.Alt, ui.KeyF1)}},
	{id: "save", group: "文件", label: "保存", scope: scopeFiles, def: []keyCombo{k(ui.Cmd, ui.KeyS)}},
	{id: "formatJSON", group: "文件", label: "格式化 JSON", scope: scopeFiles, def: []keyCombo{k(ui.Alt|ui.Shift, ui.KeyF)}},

	{id: "changes", group: "Git", label: "改动页", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyG)}, note: "编辑器有焦点、预览开着查找栏时让给\"查找上一个\""},
	{id: "commit", group: "Git", label: "提交", scope: scopeFiles, def: []keyCombo{k(ui.Cmd, ui.KeyK)}, note: "焦点在文件或文件栏上时"},
	{id: "push", group: "Git", label: "Push", def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyK)}},
	{id: "newBranch", group: "Git", label: "新建分支", def: []keyCombo{k(ui.Cmd|ui.Alt, ui.KeyN)}},
	{id: "gitLog", group: "Git", label: "Git 日志", def: []keyCombo{k(ui.Cmd, ui.Key9)}},
	{id: "nextChange", group: "Git", label: "下一处改动", scope: scopeFiles, def: []keyCombo{k(0, ui.KeyF7)}, note: "diff、分支比较和回滚对话框里"},
	{id: "prevChange", group: "Git", label: "上一处改动", scope: scopeFiles, def: []keyCombo{k(ui.Shift, ui.KeyF7)}, note: "diff、分支比较和回滚对话框里"},
	{id: "diffOpenFile", group: "Git", label: "从 diff 打开文件", scope: scopeFiles, def: []keyCombo{k(0, ui.KeyF4)}},
	{id: "rollback", group: "Git", label: "回滚", scope: scopeFiles, def: []keyCombo{k(ui.Cmd|ui.Alt, ui.KeyZ)}, note: "改动页里批量回滚，diff 里回滚当前这一处"},

	{id: "historySearch", group: "历史", label: "搜索会话", scope: scopeHistory, def: []keyCombo{k(ui.Cmd, ui.KeyF)}},

	{id: "termFind", group: "终端", label: "在终端里查找", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyF)}, note: "查找栏开着时，下一个、上一个跟着编辑器的查找"},
	{id: "termClear", group: "终端", label: "清屏", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyK)}},
	{id: "termTop", group: "终端", label: "滚到顶", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyUp)}},
	{id: "termBottom", group: "终端", label: "滚到底", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyDown)}},
	{id: "termLineStart", group: "终端", label: "光标到行首", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyLeft)}},
	{id: "termLineEnd", group: "终端", label: "光标到行尾", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyRight)}},
	{id: "termKillLine", group: "终端", label: "删到行首", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyBackspace)}},
	{id: "termWordLeft", group: "终端", label: "上一个词", scope: scopeTerminal, def: []keyCombo{k(ui.Alt, ui.KeyLeft)}},
	{id: "termWordRight", group: "终端", label: "下一个词", scope: scopeTerminal, def: []keyCombo{k(ui.Alt, ui.KeyRight)}},
	{id: "termKillWord", group: "终端", label: "删前一个词", scope: scopeTerminal, def: []keyCombo{k(ui.Alt, ui.KeyBackspace)}},
	{id: "splitRight", group: "终端", label: "向右分屏", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyD)}, note: "当前终端放到右栏，左栏换成它前面的标签；只有一个终端时新开一个"},
	{id: "closeSplit", group: "终端", label: "关闭分屏", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyD)}, note: "两个终端都留着，留下焦点所在的那个"},
	{id: "switchPane", group: "终端", label: "切换分屏的栏", scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyBracketRight), k(ui.Cmd, ui.KeyBracketLeft)}},
	{id: "termNewline", group: "终端", label: "Claude、Codex 输入框里换行", scope: scopeTerminal, def: []keyCombo{k(ui.Shift, ui.KeyEnter)}},

	{id: "sendMessage", group: "其他", label: "多行输入框里发送、提交", def: []keyCombo{k(ui.Cmd, ui.KeyEnter)}, fixed: true},
	{id: "sysQuit", group: "其他", label: "退出 Posse", scope: scopeSystem, def: []keyCombo{k(ui.Cmd, ui.KeyQ)}, fixed: true},
	{id: "sysHide", group: "其他", label: "隐藏 Posse", scope: scopeSystem, def: []keyCombo{k(ui.Cmd, ui.KeyH)}, fixed: true},
	{id: "sysHideOthers", group: "其他", label: "隐藏其他", scope: scopeSystem, def: []keyCombo{k(ui.Cmd|ui.Alt, ui.KeyH)}, fixed: true},
	{id: "sysMinimize", group: "其他", label: "最小化窗口", scope: scopeSystem, def: []keyCombo{k(ui.Cmd, ui.KeyM)}, fixed: true},
	{id: "sysFullScreen", group: "其他", label: "全屏", scope: scopeSystem, def: []keyCombo{k(ui.Cmd|ui.Ctrl, ui.KeyF)}, fixed: true},
}

// editorKeyLabels 是编辑器里列进设置页的命令；方向键移动、打字、退格这类基本操作不列，也不能改。
var editorKeyLabels = []struct {
	group string
	cmds  []editor.Command
	names []string
}{
	{"编辑器 · 移动", []editor.Command{
		editor.MoveWordLeft, editor.MoveWordRight, editor.MoveSubwordLeft, editor.MoveSubwordRight,
		editor.MoveLineStart, editor.MoveLineEnd, editor.MoveDocStart, editor.MoveDocEnd,
		editor.MoveParagraphUp, editor.MoveParagraphDown, editor.MovePageUp, editor.MovePageDown, editor.MoveToBracket,
	}, []string{
		"上一个词", "下一个词", "上一个子词", "下一个子词",
		"行首", "行尾", "文件开头", "文件末尾",
		"上一段", "下一段", "上一页", "下一页", "跳到匹配的括号",
	}},
	{"编辑器 · 选择", []editor.Command{
		editor.SelectWordLeft, editor.SelectWordRight, editor.SelectSubwordLeft, editor.SelectSubwordRight,
		editor.SelectLineStart, editor.SelectLineEnd, editor.SelectDocStart, editor.SelectDocEnd,
		editor.SelectParagraphUp, editor.SelectParagraphDown, editor.SelectPageUp, editor.SelectPageDown,
		editor.SelectAll, editor.SelectLine, editor.SelectNext, editor.SelectPrevious, editor.SelectAllMatches,
		editor.AddCursorAbove, editor.AddCursorBelow, editor.SelectLarger, editor.SelectSmaller,
		editor.UndoSelection, editor.RedoSelection,
	}, []string{
		"选到上一个词", "选到下一个词", "选到上一个子词", "选到下一个子词",
		"选到行首", "选到行尾", "选到文件开头", "选到文件末尾",
		"选到上一段", "选到下一段", "向上选一页", "向下选一页",
		"全选", "选中整行", "选中下一个相同的", "选中上一个相同的", "选中所有相同的",
		"在上面加光标", "在下面加光标", "扩大选区", "缩小选区",
		"撤销选择", "重做选择",
	}},
	{"编辑器 · 编辑", []editor.Command{
		editor.Undo, editor.Redo, editor.Cut, editor.Copy, editor.Paste,
		editor.DeleteWordLeft, editor.DeleteWordRight, editor.DeleteSubwordLeft, editor.DeleteSubwordRight,
		editor.DeleteToLineStart, editor.DeleteToLineEnd, editor.CutToLineEnd, editor.DeleteLine,
		editor.NewlineBelow, editor.NewlineAbove, editor.MoveLinesUp, editor.MoveLinesDown,
		editor.DuplicateLinesUp, editor.DuplicateLinesDown, editor.JoinLines, editor.Transpose,
		editor.Indent, editor.Outdent, editor.ToggleComment, editor.ToggleBlockComment,
	}, []string{
		"撤销", "重做", "剪切", "复制", "粘贴",
		"删前一个词", "删后一个词", "删前一个子词", "删后一个子词",
		"删到行首", "删到行尾", "剪切到行尾", "删除行",
		"在下面插入新行", "在上面插入新行", "上移行", "下移行",
		"向上复制行", "向下复制行", "合并行", "交换前后字符",
		"增加缩进", "减少缩进", "切换行注释", "切换块注释",
	}},
	{"编辑器 · 折叠", []editor.Command{
		editor.Fold, editor.Unfold, editor.ToggleFold, editor.FoldAll, editor.UnfoldAll, editor.FoldRecursive, editor.UnfoldRecursive,
	}, []string{
		"折叠", "展开", "切换折叠", "全部折叠", "全部展开", "递归折叠", "递归展开",
	}},
	{"编辑器 · 查找与跳转", []editor.Command{
		editor.Find, editor.FindReplace, editor.FindNext, editor.FindPrevious, editor.UseSelectionForFind, editor.FindInSelection,
		editor.GoToLine, editor.ScrollCursorCenter, editor.ScrollLineUp, editor.ScrollLineDown,
		editor.GoToDefinition, editor.ShowCompletions, editor.ShowHover,
		editor.NextHunk, editor.PreviousHunk, editor.RevertHunk, editor.ToggleSoftWrap, editor.ToggleLineNumbers,
	}, []string{
		"查找", "替换", "查找下一个", "查找上一个", "用选中的文字查找", "在选区里查找",
		"跳到行", "把光标所在行滚到中间", "向上滚一行", "向下滚一行",
		"跳到定义", "补全", "显示悬停信息",
		"下一处改动", "上一处改动", "回滚这一处改动", "切换自动换行", "切换行号",
	}},
}

// editorNotes 是编辑器命令的补充说明。
var editorNotes = map[editor.Command]string{
	editor.Find:         "Markdown 预览、Annotate 的查找栏也用它",
	editor.FindNext:     "Markdown 预览、Annotate 的查找栏也用它",
	editor.FindPrevious: "Markdown 预览、Annotate 的查找栏也用它",
}

var allKeyActions = buildKeyActions()

func buildKeyActions() []keyAction {
	out := slices.Clone(posseKeys)
	defs := map[editor.Command][]keyCombo{}
	for _, b := range editor.DefaultBindings() {
		defs[b.Command] = append(defs[b.Command], keyCombo{b.Mods, b.Key, b.ThenMods, b.ThenKey})
	}
	for _, g := range editorKeyLabels {
		for i, cmd := range g.cmds {
			out = append(out, keyAction{id: "editor." + cmd.String(), group: g.group, label: g.names[i], scope: scopeEditor,
				def: defs[cmd], cmd: cmd, note: editorNotes[cmd]})
		}
	}
	return out
}

func keyActionByID(id string) *keyAction {
	for i := range allKeyActions {
		if allKeyActions[i].id == id {
			return &allKeyActions[i]
		}
	}
	return nil
}

// keysFor 是一项现在的按键：改过的用 ui.json 里的，否则默认。
func (a *app) keysFor(id string) []keyCombo {
	if s, ok := a.state.Keys[id]; ok {
		var out []keyCombo
		for _, t := range s {
			if kc, ok := parseCombo(t); ok {
				out = append(out, kc)
			}
		}
		return out
	}
	if ka := keyActionByID(id); ka != nil {
		return ka.def
	}
	return nil
}

// pressed 报告这一帧按下了 id 的某组键（窗口级，c.Shortcut）。录新快捷键时一律不认，键交给录制框。
func (a *app) pressed(c *ui.Context, id string) bool {
	if a.keyRec != nil {
		return false
	}
	for _, kc := range a.keysFor(id) {
		if kc.thenKey == 0 && c.Shortcut(kc.mods, kc.key) {
			return true
		}
	}
	return false
}

// pressedIn 和 pressed 一样，问的是某个元素（焦点在它里面时）。
func (a *app) pressedIn(e ui.Element, id string) bool {
	if a.keyRec != nil {
		return false
	}
	for _, kc := range a.keysFor(id) {
		if kc.thenKey == 0 && e.Shortcut(kc.mods, kc.key) {
			return true
		}
	}
	return false
}

// keyIs 报告 mods+key 是不是 id 的某组键：终端的 OnKey 收到的是单个按键，在这里比。
func (a *app) keyIs(id string, mods ui.Modifiers, key ui.Key) bool {
	return slices.Contains(a.keysFor(id), keyCombo{mods: mods, key: key})
}

// keyText 是 id 的第一组键的写法（⌘B），放在提示里；没有键时为空。
func (a *app) keyText(id string) string {
	if ks := a.keysFor(id); len(ks) > 0 {
		return ks[0].String()
	}
	return ""
}

// withKey 在提示文字后面接上快捷键。
func (a *app) withKey(label, id string) string {
	if t := a.keyText(id); t != "" {
		return label + " " + t
	}
	return label
}

// menuKey 是 id 的第一组键，给自绘菜单的 Shortcut 用；没有键时 ok 为假。
func (a *app) menuKey(id string) (ui.Modifiers, ui.Key, bool) {
	for _, kc := range a.keysFor(id) {
		if kc.thenKey == 0 {
			return kc.mods, kc.key, true
		}
	}
	return 0, 0, false
}

// editorBindings 是交给编辑器的按键：改过的命令先把默认的键让出来（NoCommand），再绑上新键。
// 没改过时为 nil，编辑器用自己的默认。改过的表按 ui.json 缓存，ui.json 的 keys 变了才重算。
func (a *app) editorBindings() []editor.Binding {
	// edBindKeys 记的是 keysVer+1，零值表示还没算过
	if a.edBindKeys == a.keysVer+1 {
		return a.edBind
	}
	a.edBindKeys = a.keysVer + 1
	var out []editor.Binding
	taken := map[keyCombo]bool{}
	for _, ka := range allKeyActions {
		if ka.scope != scopeEditor {
			continue
		}
		if _, ok := a.state.Keys[ka.id]; !ok {
			continue
		}
		for _, kc := range a.keysFor(ka.id) {
			out = append(out, editor.Binding{Mods: kc.mods, Key: kc.key, ThenMods: kc.thenMods, ThenKey: kc.thenKey, Command: ka.cmd})
			taken[kc] = true
		}
	}
	for _, ka := range allKeyActions {
		if ka.scope != scopeEditor {
			continue
		}
		if _, ok := a.state.Keys[ka.id]; !ok {
			continue
		}
		for _, kc := range ka.def {
			if !taken[kc] {
				out = append(out, editor.Binding{Mods: kc.mods, Key: kc.key, ThenMods: kc.thenMods, ThenKey: kc.thenKey, Command: editor.NoCommand})
				taken[kc] = true
			}
		}
	}
	a.edBind = out
	return out
}

// setKeys 改 id 的按键：和默认一样时删掉记录。改了菜单上的项重新装菜单。
func (a *app) setKeys(id string, ks []keyCombo) {
	ka := keyActionByID(id)
	if ka == nil || ka.fixed {
		return
	}
	if a.state.Keys == nil {
		a.state.Keys = map[string][]string{}
	}
	if slices.Equal(ks, ka.def) {
		delete(a.state.Keys, id)
	} else {
		s := make([]string, 0, len(ks))
		for _, kc := range ks {
			s = append(s, kc.encode())
		}
		a.state.Keys[id] = s
	}
	a.keysVer++
	a.saveState()
	if menuBarKeys[id] {
		a.refreshMenu()
	}
}

// menuBarKeys 是装在菜单栏上的快捷键：改了要重装菜单。
var menuBarKeys = map[string]bool{"closeTab": true, "closeWindow": true, "settings": true, "webReload": true, "webHardReload": true, "webMobile": true}

// refreshMenu 让菜单栏上的快捷键跟着快捷键表；窗口还没建（测试里）时不装。
func (a *app) refreshMenu() {
	if a.win != nil {
		a.installMenu()
	}
}

// keyConflict 返回和 id 在同一处、也用了 kc 的另一项。
func (a *app) keyConflict(id string, kc keyCombo) *keyAction {
	self := keyActionByID(id)
	for i := range allKeyActions {
		ka := &allKeyActions[i]
		if ka.id == id || ka.fixed || ka.scope != self.scope {
			continue
		}
		if slices.Contains(a.keysFor(ka.id), kc) {
			return ka
		}
	}
	return nil
}

// --- 写法

func (kc keyCombo) String() string {
	s := shortcutText(kc.mods, kc.key)
	if kc.thenKey != 0 {
		s += " " + shortcutText(kc.thenMods, kc.thenKey)
	}
	return s
}

// accelerator 是 mygo 菜单的写法（Super+Shift+w），和弦和没有名字的键为空。
func (kc keyCombo) accelerator() string {
	if kc.thenKey != 0 {
		return ""
	}
	return encodeOne(kc.mods, kc.key)
}

// encode 是存进 ui.json 的写法：Super+Shift+k，和弦用空格隔开两下。
func (kc keyCombo) encode() string {
	s := encodeOne(kc.mods, kc.key)
	if kc.thenKey != 0 {
		s += " " + encodeOne(kc.thenMods, kc.thenKey)
	}
	return s
}

func encodeOne(mods ui.Modifiers, key ui.Key) string {
	var b strings.Builder
	for _, m := range keyMods {
		if mods&m.mod != 0 {
			b.WriteString(m.name + "+")
		}
	}
	b.WriteString(keyNames[key])
	return b.String()
}

func parseCombo(s string) (keyCombo, bool) {
	first, then, chord := strings.Cut(s, " ")
	m, kk, ok := parseOne(first)
	if !ok {
		return keyCombo{}, false
	}
	kc := keyCombo{mods: m, key: kk}
	if chord {
		if kc.thenMods, kc.thenKey, ok = parseOne(then); !ok {
			return keyCombo{}, false
		}
	}
	return kc, true
}

func parseOne(s string) (ui.Modifiers, ui.Key, bool) {
	var mods ui.Modifiers
	for {
		found := false
		for _, m := range keyMods {
			if rest, ok := strings.CutPrefix(s, m.name+"+"); ok {
				mods |= m.mod
				s, found = rest, true
			}
		}
		if !found {
			break
		}
	}
	for key, name := range keyNames {
		if name == s {
			return mods, key, true
		}
	}
	return 0, 0, false
}

var keyMods = []struct {
	mod  ui.Modifiers
	name string
}{{ui.Super, "Super"}, {ui.Ctrl, "Ctrl"}, {ui.Alt, "Alt"}, {ui.Shift, "Shift"}}

// keyNames 是能录进快捷键的键和它们在 ui.json、菜单里的名字。
var keyNames = func() map[ui.Key]string {
	m := map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyEscape: "Escape", ui.KeyBackspace: "Backspace", ui.KeyTab: "Tab", ui.KeySpace: "Space",
		ui.KeyDelete: "Delete", ui.KeyHome: "Home", ui.KeyEnd: "End", ui.KeyPageUp: "PageUp", ui.KeyPageDown: "PageDown",
		ui.KeyLeft: "Left", ui.KeyRight: "Right", ui.KeyUp: "Up", ui.KeyDown: "Down",
		ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyComma: ",", ui.KeyPeriod: ".", ui.KeySlash: "/", ui.KeySemicolon: ";",
		ui.KeyQuote: "'", ui.KeyBracketLeft: "[", ui.KeyBracketRight: "]", ui.KeyBackslash: "\\", ui.KeyBackquote: "`",
	}
	for k := ui.KeyA; k <= ui.KeyZ; k++ {
		m[k] = string(rune('a' + k - ui.KeyA))
	}
	for k := ui.Key0; k <= ui.Key9; k++ {
		m[k] = string(rune('0' + k - ui.Key0))
	}
	for k := ui.KeyF1; k <= ui.KeyF12; k++ {
		m[k] = "F" + strconv.Itoa(int(k-ui.KeyF1)+1)
	}
	return m
}()
