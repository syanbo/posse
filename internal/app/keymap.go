package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/locales"
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

type keyGroup string

const (
	keyGroupWindow       keyGroup = "window"
	keyGroupWeb          keyGroup = "web"
	keyGroupFiles        keyGroup = "files"
	keyGroupGit          keyGroup = "git"
	keyGroupHistory      keyGroup = "history"
	keyGroupTerminal     keyGroup = "terminal"
	keyGroupOther        keyGroup = "other"
	keyGroupEditorMove   keyGroup = "editorMove"
	keyGroupEditorSelect keyGroup = "editorSelect"
	keyGroupEditorEdit   keyGroup = "editorEdit"
	keyGroupEditorFold   keyGroup = "editorFold"
	keyGroupEditorFind   keyGroup = "editorFind"
)

type keyAction struct {
	id         string
	group      keyGroup
	groupLabel func(locales.Messages) string
	label      func(locales.Messages) string
	scope      keyScope
	def        []keyCombo
	fixed      bool                          // 只列出来，不能改
	cmd        editor.Command                // scopeEditor 的命令
	note       func(locales.Messages) string // 悬停说明
	text       string                        // 设置页上代替按键写法显示的文字（⌘1…9）
}

func k(mods ui.Modifiers, key ui.Key) keyCombo { return keyCombo{mods: mods, key: key} }

// Posse 自己的快捷键。和编辑器的查找共用 editor.find 等几项，预览、Annotate 的查找栏跟着编辑器的改。
var posseKeys = []keyAction{
	{id: "newTerminal", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysNewTerminalLabel, def: []keyCombo{k(ui.Cmd, ui.KeyT)}},
	{id: "newWorkspace", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysNewWorkspaceLabel, def: []keyCombo{k(ui.Cmd, ui.KeyN)}},
	{id: "newChat", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysNewChatLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyN)}, note: locales.Messages.KeysNewChatNote},
	{id: "closeTab", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysCloseTabLabel, def: []keyCombo{k(ui.Cmd, ui.KeyW)}, note: locales.Messages.KeysCloseTabNote},
	{id: "closeWindow", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysCloseWindowLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyW)}, note: locales.Messages.KeysCloseWindowNote},
	{id: "settings", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysSettingsLabel, def: []keyCombo{k(ui.Cmd, ui.KeyComma)}, note: locales.Messages.KeysSettingsNote},
	{id: "nextTab", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysNextTabLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyBracketRight), k(ui.Ctrl, ui.KeyTab)}},
	{id: "prevTab", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysPrevTabLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyBracketLeft), k(ui.Ctrl|ui.Shift, ui.KeyTab)}},
	{id: "selectTab", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysSelectTabLabel, def: []keyCombo{k(ui.Cmd, ui.Key1)}, fixed: true, text: "⌘1…9"},
	{id: "toggleSidebar", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysToggleSidebarLabel, def: []keyCombo{k(ui.Cmd, ui.KeyB)}},
	{id: "zoomIn", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysZoomInLabel, def: []keyCombo{k(ui.Cmd, ui.KeyEqual)}, note: locales.Messages.KeysZoomInNote},
	{id: "zoomOut", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysZoomOutLabel, def: []keyCombo{k(ui.Cmd, ui.KeyMinus)}, note: locales.Messages.KeysZoomOutNote},
	{id: "zoomReset", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysZoomResetLabel, def: []keyCombo{k(ui.Cmd, ui.Key0)}, note: locales.Messages.KeysZoomResetNote},
	{id: "jumpBack", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysJumpBackLabel, def: []keyCombo{k(ui.Ctrl, ui.KeyMinus)}},
	{id: "jumpForward", group: keyGroupWindow, groupLabel: locales.Messages.KeysGroupWindow, label: locales.Messages.KeysJumpForwardLabel, def: []keyCombo{k(ui.Ctrl|ui.Shift, ui.KeyMinus)}},

	{id: "webReload", group: keyGroupWeb, groupLabel: locales.Messages.KeysGroupWeb, label: locales.Messages.KeysWebReloadLabel, def: []keyCombo{k(ui.Cmd, ui.KeyR)}, note: locales.Messages.KeysWebReloadNote},
	{id: "webHardReload", group: keyGroupWeb, groupLabel: locales.Messages.KeysGroupWeb, label: locales.Messages.KeysWebHardReloadLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyR)}, note: locales.Messages.KeysWebHardReloadNote},
	{id: "webMobile", group: keyGroupWeb, groupLabel: locales.Messages.KeysGroupWeb, label: locales.Messages.KeysWebMobileLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyM)}, note: locales.Messages.KeysWebMobileNote},

	{id: "quickOpen", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysQuickOpenLabel, def: []keyCombo{k(ui.Cmd, ui.KeyP)}},
	{id: "searchEverywhere", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysSearchEverywhereLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyP)}},
	{id: "findAction", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysFindActionLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyA)}},
	{id: "searchFiles", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysSearchFilesLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyF)}},
	{id: "filesPanel", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysFilesPanelLabel, def: []keyCombo{k(ui.Cmd, ui.KeyL)}},
	{id: "fileTree", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysFileTreeLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyE)}},
	{id: "revealFile", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysRevealFileLabel, def: []keyCombo{k(ui.Alt, ui.KeyF1)}},
	{id: "save", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.CommonSave, scope: scopeFiles, def: []keyCombo{k(ui.Cmd, ui.KeyS)}},
	{id: "formatJSON", group: keyGroupFiles, groupLabel: locales.Messages.KeysGroupFiles, label: locales.Messages.KeysFormatJSONLabel, scope: scopeFiles, def: []keyCombo{k(ui.Alt|ui.Shift, ui.KeyF)}},

	{id: "changes", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysChangesLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyG)}, note: locales.Messages.KeysChangesNote},
	{id: "commit", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysCommitLabel, scope: scopeFiles, def: []keyCombo{k(ui.Cmd, ui.KeyK)}, note: locales.Messages.KeysCommitNote},
	{id: "push", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysPushLabel, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyK)}},
	{id: "newBranch", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysNewBranchLabel, def: []keyCombo{k(ui.Cmd|ui.Alt, ui.KeyN)}},
	{id: "gitLog", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysGitLogLabel, def: []keyCombo{k(ui.Cmd, ui.Key9)}},
	{id: "nextChange", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysNextChangeLabel, scope: scopeFiles, def: []keyCombo{k(0, ui.KeyF7)}, note: locales.Messages.KeysNextChangeNote},
	{id: "prevChange", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysPrevChangeLabel, scope: scopeFiles, def: []keyCombo{k(ui.Shift, ui.KeyF7)}, note: locales.Messages.KeysPrevChangeNote},
	{id: "diffOpenFile", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysDiffOpenFileLabel, scope: scopeFiles, def: []keyCombo{k(0, ui.KeyF4)}},
	{id: "rollback", group: keyGroupGit, groupLabel: locales.Messages.KeysGroupGit, label: locales.Messages.KeysRollbackLabel, scope: scopeFiles, def: []keyCombo{k(ui.Cmd|ui.Alt, ui.KeyZ)}, note: locales.Messages.KeysRollbackNote},

	{id: "historySearch", group: keyGroupHistory, groupLabel: locales.Messages.KeysGroupHistory, label: locales.Messages.KeysHistorySearchLabel, scope: scopeHistory, def: []keyCombo{k(ui.Cmd, ui.KeyF)}},

	{id: "termFind", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermFindLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyF)}, note: locales.Messages.KeysTermFindNote},
	{id: "termClear", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermClearLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyK)}},
	{id: "termTop", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermTopLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyUp)}},
	{id: "termBottom", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermBottomLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyDown)}},
	{id: "termLineStart", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermLineStartLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyLeft)}},
	{id: "termLineEnd", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermLineEndLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyRight)}},
	{id: "termKillLine", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermKillLineLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyBackspace)}},
	{id: "termWordLeft", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermWordLeftLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Alt, ui.KeyLeft)}},
	{id: "termWordRight", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermWordRightLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Alt, ui.KeyRight)}},
	{id: "termKillWord", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermKillWordLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Alt, ui.KeyBackspace)}},
	{id: "splitRight", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysSplitRightLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyD)}, note: locales.Messages.KeysSplitRightNote},
	{id: "splitDown", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysSplitDownLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd|ui.Shift, ui.KeyD)}, note: locales.Messages.KeysSplitDownNote},
	{id: "closeSplit", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysCloseSplitLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd|ui.Ctrl, ui.KeyD)}, note: locales.Messages.KeysCloseSplitNote},
	{id: "switchPane", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysSwitchPaneLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Cmd, ui.KeyBracketRight), k(ui.Cmd, ui.KeyBracketLeft)}, note: locales.Messages.KeysSwitchPaneNote},
	{id: "termNewline", group: keyGroupTerminal, groupLabel: locales.Messages.KeysGroupTerminal, label: locales.Messages.KeysTermNewlineLabel, scope: scopeTerminal, def: []keyCombo{k(ui.Shift, ui.KeyEnter)}},

	{id: "sendMessage", group: keyGroupOther, groupLabel: locales.Messages.CommonOther, label: locales.Messages.KeysSendMessageLabel, def: []keyCombo{k(ui.Cmd, ui.KeyEnter)}, fixed: true},
	{id: "sysQuit", group: keyGroupOther, groupLabel: locales.Messages.CommonOther, label: locales.Messages.KeysSysQuitLabel, scope: scopeSystem, def: []keyCombo{k(ui.Cmd, ui.KeyQ)}, fixed: true},
	{id: "sysHide", group: keyGroupOther, groupLabel: locales.Messages.CommonOther, label: locales.Messages.KeysSysHideLabel, scope: scopeSystem, def: []keyCombo{k(ui.Cmd, ui.KeyH)}, fixed: true},
	{id: "sysHideOthers", group: keyGroupOther, groupLabel: locales.Messages.CommonOther, label: locales.Messages.KeysSysHideOthersLabel, scope: scopeSystem, def: []keyCombo{k(ui.Cmd|ui.Alt, ui.KeyH)}, fixed: true},
	{id: "sysMinimize", group: keyGroupOther, groupLabel: locales.Messages.CommonOther, label: locales.Messages.KeysSysMinimizeLabel, scope: scopeSystem, def: []keyCombo{k(ui.Cmd, ui.KeyM)}, fixed: true},
	{id: "sysFullScreen", group: keyGroupOther, groupLabel: locales.Messages.CommonOther, label: locales.Messages.KeysSysFullScreenLabel, scope: scopeSystem, def: []keyCombo{k(ui.Cmd|ui.Ctrl, ui.KeyF)}, fixed: true},
}

// editorKeyLabels 是编辑器里列进设置页的命令；方向键移动、打字、退格这类基本操作不列，也不能改。
var editorKeyLabels = []struct {
	group      keyGroup
	groupLabel func(locales.Messages) string
	cmds       []editor.Command
	names      []func(locales.Messages) string
}{
	{keyGroupEditorMove, locales.Messages.KeysGroupEditorMove, []editor.Command{
		editor.MoveWordLeft, editor.MoveWordRight, editor.MoveSubwordLeft, editor.MoveSubwordRight,
		editor.MoveLineStart, editor.MoveLineEnd, editor.MoveDocStart, editor.MoveDocEnd,
		editor.MoveParagraphUp, editor.MoveParagraphDown, editor.MovePageUp, editor.MovePageDown, editor.MoveToBracket,
	}, []func(locales.Messages) string{
		locales.Messages.KeysEditorMoveWordLeft, locales.Messages.KeysEditorMoveWordRight, locales.Messages.KeysEditorMoveSubwordLeft, locales.Messages.KeysEditorMoveSubwordRight,
		locales.Messages.KeysEditorMoveLineStart, locales.Messages.KeysEditorMoveLineEnd, locales.Messages.KeysEditorMoveDocStart, locales.Messages.KeysEditorMoveDocEnd,
		locales.Messages.KeysEditorMoveParagraphUp, locales.Messages.KeysEditorMoveParagraphDown, locales.Messages.KeysEditorMovePageUp, locales.Messages.KeysEditorMovePageDown, locales.Messages.KeysEditorMoveToBracket,
	}},
	{keyGroupEditorSelect, locales.Messages.KeysGroupEditorSelect, []editor.Command{
		editor.SelectWordLeft, editor.SelectWordRight, editor.SelectSubwordLeft, editor.SelectSubwordRight,
		editor.SelectLineStart, editor.SelectLineEnd, editor.SelectDocStart, editor.SelectDocEnd,
		editor.SelectParagraphUp, editor.SelectParagraphDown, editor.SelectPageUp, editor.SelectPageDown,
		editor.SelectAll, editor.SelectLine, editor.SelectNext, editor.SelectPrevious, editor.SelectAllMatches,
		editor.AddCursorAbove, editor.AddCursorBelow, editor.SelectLarger, editor.SelectSmaller,
		editor.UndoSelection, editor.RedoSelection,
	}, []func(locales.Messages) string{
		locales.Messages.KeysEditorSelectWordLeft, locales.Messages.KeysEditorSelectWordRight, locales.Messages.KeysEditorSelectSubwordLeft, locales.Messages.KeysEditorSelectSubwordRight,
		locales.Messages.KeysEditorSelectLineStart, locales.Messages.KeysEditorSelectLineEnd, locales.Messages.KeysEditorSelectDocStart, locales.Messages.KeysEditorSelectDocEnd,
		locales.Messages.KeysEditorSelectParagraphUp, locales.Messages.KeysEditorSelectParagraphDown, locales.Messages.KeysEditorSelectPageUp, locales.Messages.KeysEditorSelectPageDown,
		locales.Messages.CommonSelectAll, locales.Messages.KeysEditorSelectLine, locales.Messages.KeysEditorSelectNext, locales.Messages.KeysEditorSelectPrevious, locales.Messages.KeysEditorSelectAllMatches,
		locales.Messages.KeysEditorAddCursorAbove, locales.Messages.KeysEditorAddCursorBelow, locales.Messages.KeysEditorSelectLarger, locales.Messages.KeysEditorSelectSmaller,
		locales.Messages.KeysEditorUndoSelection, locales.Messages.KeysEditorRedoSelection,
	}},
	{keyGroupEditorEdit, locales.Messages.KeysGroupEditorEdit, []editor.Command{
		editor.Undo, editor.Redo, editor.Cut, editor.Copy, editor.Paste,
		editor.DeleteWordLeft, editor.DeleteWordRight, editor.DeleteSubwordLeft, editor.DeleteSubwordRight,
		editor.DeleteToLineStart, editor.DeleteToLineEnd, editor.CutToLineEnd, editor.DeleteLine,
		editor.NewlineBelow, editor.NewlineAbove, editor.MoveLinesUp, editor.MoveLinesDown,
		editor.DuplicateLinesUp, editor.DuplicateLinesDown, editor.JoinLines, editor.Transpose,
		editor.Indent, editor.Outdent, editor.ToggleComment, editor.ToggleBlockComment,
	}, []func(locales.Messages) string{
		locales.Messages.KeysEditorUndo, locales.Messages.KeysEditorRedo, locales.Messages.KeysEditorCut, locales.Messages.CommonCopy, locales.Messages.CommonPaste,
		locales.Messages.KeysEditorDeleteWordLeft, locales.Messages.KeysEditorDeleteWordRight, locales.Messages.KeysEditorDeleteSubwordLeft, locales.Messages.KeysEditorDeleteSubwordRight,
		locales.Messages.KeysEditorDeleteToLineStart, locales.Messages.KeysEditorDeleteToLineEnd, locales.Messages.KeysEditorCutToLineEnd, locales.Messages.KeysEditorDeleteLine,
		locales.Messages.KeysEditorNewlineBelow, locales.Messages.KeysEditorNewlineAbove, locales.Messages.KeysEditorMoveLinesUp, locales.Messages.KeysEditorMoveLinesDown,
		locales.Messages.KeysEditorDuplicateLinesUp, locales.Messages.KeysEditorDuplicateLinesDown, locales.Messages.KeysEditorJoinLines, locales.Messages.KeysEditorTranspose,
		locales.Messages.KeysEditorIndent, locales.Messages.KeysEditorOutdent, locales.Messages.KeysEditorToggleComment, locales.Messages.KeysEditorToggleBlockComment,
	}},
	{keyGroupEditorFold, locales.Messages.KeysGroupEditorFold, []editor.Command{
		editor.Fold, editor.Unfold, editor.ToggleFold, editor.FoldAll, editor.UnfoldAll, editor.FoldRecursive, editor.UnfoldRecursive,
	}, []func(locales.Messages) string{
		locales.Messages.KeysEditorFold, locales.Messages.KeysEditorUnfold, locales.Messages.KeysEditorToggleFold, locales.Messages.KeysEditorFoldAll, locales.Messages.KeysEditorUnfoldAll, locales.Messages.KeysEditorFoldRecursive, locales.Messages.KeysEditorUnfoldRecursive,
	}},
	{keyGroupEditorFind, locales.Messages.KeysGroupEditorFind, []editor.Command{
		editor.Find, editor.FindReplace, editor.FindNext, editor.FindPrevious, editor.UseSelectionForFind, editor.FindInSelection,
		editor.GoToLine, editor.ScrollCursorCenter, editor.ScrollLineUp, editor.ScrollLineDown,
		editor.GoToDefinition, editor.ShowCompletions, editor.ShowHover,
		editor.NextHunk, editor.PreviousHunk, editor.RevertHunk, editor.ToggleSoftWrap, editor.ToggleLineNumbers,
	}, []func(locales.Messages) string{
		locales.Messages.KeysEditorFind, locales.Messages.KeysEditorFindReplace, locales.Messages.KeysEditorFindNext, locales.Messages.KeysEditorFindPrevious, locales.Messages.KeysEditorUseSelectionForFind, locales.Messages.KeysEditorFindInSelection,
		locales.Messages.KeysEditorGoToLine, locales.Messages.KeysEditorScrollCursorCenter, locales.Messages.KeysEditorScrollLineUp, locales.Messages.KeysEditorScrollLineDown,
		locales.Messages.KeysEditorGoToDefinition, locales.Messages.KeysEditorShowCompletions, locales.Messages.KeysEditorShowHover,
		locales.Messages.KeysEditorNextHunk, locales.Messages.KeysEditorPreviousHunk, locales.Messages.KeysEditorRevertHunk, locales.Messages.KeysEditorToggleSoftWrap, locales.Messages.KeysEditorToggleLineNumbers,
	}},
}

// editorNotes 是编辑器命令的补充说明。
var editorNotes = map[editor.Command]func(locales.Messages) string{
	editor.Find:         locales.Messages.KeysEditorFindNote,
	editor.FindNext:     locales.Messages.KeysEditorFindNote,
	editor.FindPrevious: locales.Messages.KeysEditorFindNote,
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
			out = append(out, keyAction{id: "editor." + cmd.String(), group: g.group, groupLabel: g.groupLabel, label: g.names[i], scope: scopeEditor,
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
