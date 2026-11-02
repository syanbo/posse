package app

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/locales"
)

// 设置页「快捷键」：按快捷键表（keymap.go）分组列出，可以搜索；点一组键录新的，行尾的 + 再加一组，
// 改过的行有"恢复默认"。录的时候窗口快捷键一律不认（pressed），按键都交给录制框。

// keyRecording 是正在录的一项：idx 是换掉第几组键，-1 是新加一组。
type keyRecording struct {
	id    string
	idx   int
	focus bool   // 下一帧把焦点给录制框
	hint  string // 按了不能用的键时的说明
}

func (a *app) keySettings(c *ui.Context) {
	t := c.Theme()
	q := strings.ToLower(strings.TrimSpace(a.keyQuery))
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		ui.Column(c).Grow(1).Children(func() { fieldInput(c, &a.keyQuery, L(c).KeysSearch()) })
		if len(a.state.Keys) > 0 && pressable(textButton(c, L(c).KeysResetAll())).Clicked() {
			a.state.Keys = nil
			a.keysVer++
			a.keyRec = nil
			a.saveState()
			a.refreshMenu()
		}
	})
	ui.Text(c, L(c).KeysNote()).
		FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
	var groups []keyGroup
	byGroup := map[keyGroup][]*keyAction{}
	for i := range allKeyActions {
		ka := &allKeyActions[i]
		if q != "" && !keyMatches(L(c), a, ka, q) {
			continue
		}
		if byGroup[ka.group] == nil {
			groups = append(groups, ka.group)
		}
		byGroup[ka.group] = append(byGroup[ka.group], ka)
	}
	if len(groups) == 0 {
		ui.Text(c, L(c).KeysEmpty()).FontSize(fsBody).TextColor(t.TextMuted).Padding(12, 0)
	}
	for _, g := range groups {
		settingsCard(c, byGroup[g][0].groupLabel(L(c)), "", func() {
			ui.Column(c).Gap(2).Children(func() {
				for _, ka := range byGroup[g] {
					a.keyRow(c, ka)
				}
			})
		})
	}
}

// keyMatches 报告 q 出现在名称、分组或按键的写法里。
func keyMatches(m locales.Messages, a *app, ka *keyAction, q string) bool {
	if strings.Contains(strings.ToLower(ka.label(m)), q) || strings.Contains(strings.ToLower(ka.groupLabel(m)), q) {
		return true
	}
	for _, kc := range a.keysFor(ka.id) {
		if strings.Contains(strings.ToLower(kc.String()), q) {
			return true
		}
	}
	return false
}

func (a *app) keyRow(c *ui.Context, ka *keyAction) {
	t := c.Theme()
	keys := a.keysFor(ka.id)
	_, changed := a.state.Keys[ka.id]
	row := ui.Row(c.Key("key:"+ka.id)).MinHeight(sz(32)).Padding(2, 4, 2, 8).Gap(6).Radius(rControl).AlignItems(ui.Center)
	if row.Hovered() {
		row.Background(colors(t).wash)
	}
	row.Children(func() {
		label := ui.Text(c, ka.label(L(c))).FontSize(fsBody).Grow(1).Shrink(1)
		if ka.note != nil {
			label.Tooltip(ka.note(L(c)))
		}
		if changed && !ka.fixed && pressable(textButton(c.Key("reset"), L(c).CommonResetToDefault())).Clicked() {
			a.setKeys(ka.id, ka.def)
			a.keyRec = nil
		}
		rec := a.keyRec
		for i, kc := range keys {
			if rec != nil && rec.id == ka.id && rec.idx == i {
				a.keyRecorder(c, ka)
				continue
			}
			text := kc.String()
			if ka.text != "" {
				text = ka.text
			}
			chip := keyChip(c.Key(i), text, ka.fixed)
			if ka.fixed {
				chip.Tooltip(keyFixedNote(L(c), ka.note))
			} else if chip.Clicked() {
				a.keyRec = &keyRecording{id: ka.id, idx: i, focus: true}
			}
		}
		if len(keys) == 0 && !(rec != nil && rec.id == ka.id) {
			ui.Text(c, L(c).KeysNone()).FontSize(fsSmall).TextColor(colors(t).faint).Padding(2, 6)
		}
		if rec != nil && rec.id == ka.id && rec.idx < 0 {
			a.keyRecorder(c, ka)
		}
		if ka.fixed {
			// 和能改的行的 + 占一样宽，按键对齐
			ui.Box(c).Size(compactBox(), compactBox()).Shrink(0)
			return
		}
		add := quietIconButton(c.Key("add"), iconPlus, L(c).KeysAdd(), compactBox(), smallIcon()).Tooltip(L(c).KeysAdd())
		if !row.Hovered() {
			add.Opacity(0)
		}
		if add.Clicked() {
			a.keyRec = &keyRecording{id: ka.id, idx: -1, focus: true}
		}
	})
}

func keyFixedNote(m locales.Messages, note func(locales.Messages) string) string {
	if note != nil {
		return m.KeysFixedNote(note(m))
	}
	return m.KeysFixedTip()
}

// keyChip 是一组键的小块：能改的可以点，固定的淡一些。
func keyChip(c *ui.Context, text string, fixed bool) ui.Element {
	t := c.Theme()
	chip := ui.Box(c).Padding(2, 8).Radius(rSmall).Background(t.Surface).Shrink(0)
	color := t.Text
	if fixed {
		color = t.TextMuted
	} else {
		chip.Role(ui.RoleButton).Label(L(c).KeysEditShortcut(text))
		if chip.Hovered() {
			chip.Background(colors(t).fillHi)
		}
	}
	chip.Children(func() { ui.Text(c, text).FontSize(fsSmall).FontFeatures("tnum").TextColor(color) })
	return chip
}

// keyRecorder 是录制中的那一块：拿着焦点收按键，点别处或 Esc 取消。
func (a *app) keyRecorder(c *ui.Context, ka *keyAction) {
	t := c.Theme()
	rec := a.keyRec
	box := ui.Box(c.Key("recorder")).Padding(2, 8).Radius(rSmall).Background(soft(t, t.Accent)).Shrink(0).
		Focusable().FocusRing(false).Label(L(c).KeysRecordLabel())
	fieldBorder(box, true, soft(t, t.Accent), t.Focus)
	if rec.focus {
		box.Focus()
		rec.focus = false
	}
	box.HandleInput(func(ev ui.InputEvent) bool {
		if ev.Kind != ui.InputKeyDown {
			return ev.Kind == ui.InputText
		}
		a.recordKey(L(c), ka, ev.Mods, ev.Key)
		return true
	})
	text := L(c).KeysRecording()
	if rec.hint != "" {
		text = rec.hint
	}
	box.Children(func() { ui.Text(c, text).FontSize(fsSmall).TextColor(t.Accent) })
	if box.PressedOutside() {
		a.keyRec = nil
	}
}

// recordKey 处理录制框收到的一个键。
func (a *app) recordKey(m locales.Messages, ka *keyAction, mods ui.Modifiers, key ui.Key) {
	rec := a.keyRec
	if rec == nil {
		return
	}
	keys := append([]keyCombo(nil), a.keysFor(ka.id)...)
	switch {
	case mods == 0 && key == ui.KeyEscape:
		a.keyRec = nil
		return
	case mods == 0 && (key == ui.KeyBackspace || key == ui.KeyDelete):
		if rec.idx >= 0 && rec.idx < len(keys) {
			keys = append(keys[:rec.idx], keys[rec.idx+1:]...)
			a.setKeys(ka.id, keys)
		}
		a.keyRec = nil
		return
	}
	if _, ok := keyNames[key]; !ok {
		// 只按了修饰键，或键盘上没有名字的键：接着等
		return
	}
	fkey := key >= ui.KeyF1 && key <= ui.KeyF12
	if mods == 0 && !fkey || printable(key) && mods&^ui.Shift == 0 {
		rec.hint = m.KeysModifierRequired()
		return
	}
	kc := keyCombo{mods: mods, key: key}
	if other := a.keyConflict(ka.id, kc); other != nil {
		rest := a.keysFor(other.id)
		var kept []keyCombo
		for _, o := range rest {
			if o != kc {
				kept = append(kept, o)
			}
		}
		a.setKeys(other.id, kept)
		a.notify(m.KeysConflictRemoved(other.label(m), kc.String()), "", nil)
	}
	switch {
	case rec.idx >= 0 && rec.idx < len(keys):
		keys[rec.idx] = kc
	default:
		keys = append(keys, kc)
	}
	// 同一项里不留两组一样的
	var out []keyCombo
	for _, o := range keys {
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	a.setKeys(ka.id, out)
	a.keyRec = nil
}

// printable 是会打出字的键：不带 ⌘ ⌃ ⌥ 时当快捷键会挡住打字。
func printable(key ui.Key) bool {
	switch {
	case key >= ui.KeyA && key <= ui.KeyZ, key >= ui.Key0 && key <= ui.Key9:
		return true
	}
	switch key {
	case ui.KeySpace, ui.KeyMinus, ui.KeyEqual, ui.KeyComma, ui.KeyPeriod, ui.KeySlash, ui.KeySemicolon,
		ui.KeyQuote, ui.KeyBracketLeft, ui.KeyBracketRight, ui.KeyBackslash, ui.KeyBackquote:
		return true
	}
	return false
}
