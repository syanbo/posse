package app

import (
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// popMenu 是 Posse 自己画的菜单，代替系统菜单（NSMenu）：系统菜单的悬停高亮是系统强调色、底板是毛玻璃，都改不了。
// 用法照 ui.Menu：build 里 Item(...).Shortcut().Disabled().Checked().Chosen()、Separator、Submenu。
// build 每帧先跑一遍收集菜单项（Chosen 都是 false），画出来；点了某一项后同一帧再跑一遍，那一项的 Chosen 报告 true。
// 所以 build 前后两遍加的项要一样，动作只写在 Chosen 里。
type popMenu struct {
	items  []*menuItem
	seq    *int // 所有层共用的编号，认出选中的是哪一项
	chosen int  // 这一遍要报告选中的项，-1 是收集
}

type menuItem struct {
	m        *popMenu
	seq      int
	label    string
	keys     string
	icon     *ui.SVG
	lead     func() // 自己画最左边（agent 图标），优先于 icon
	sep      bool
	disabled bool
	checked  *bool // 不为 nil 时是可勾选的项
	sub      *popMenu
}

func newPopMenu(seq *int, chosen int) *popMenu { return &popMenu{seq: seq, chosen: chosen} }

func (m *popMenu) add(it *menuItem) *menuItem {
	*m.seq++
	it.m, it.seq = m, *m.seq
	m.items = append(m.items, it)
	return it
}

// Item 加一项。
func (m *popMenu) Item(label string) *menuItem { return m.add(&menuItem{label: label}) }

// Separator 加一条分隔线；开头、结尾和连着的分隔线不画。
func (m *popMenu) Separator() { m.add(&menuItem{sep: true}) }

// Submenu 加一项，指针停上去时在右边展开 build 加的项。
func (m *popMenu) Submenu(label string, build func(m *popMenu)) *menuItem {
	it := m.add(&menuItem{label: label})
	it.sub = newPopMenu(m.seq, m.chosen)
	build(it.sub)
	return it
}

func (it *menuItem) Icon(icon *ui.SVG) *menuItem { it.icon = icon; return it }
func (it *menuItem) Lead(draw func()) *menuItem  { it.lead = draw; return it }
func (it *menuItem) Disabled(d bool) *menuItem   { it.disabled = d; return it }
func (it *menuItem) Checked(on bool) *menuItem   { it.checked = &on; return it }

// Shortcut 只在右边写出快捷键，不绑定：快捷键本身由界面别处处理。
func (it *menuItem) Shortcut(mods ui.Modifiers, key ui.Key) *menuItem {
	it.keys = shortcutText(mods, key)
	return it
}

// Chosen 报告用户选了这一项（在点下去的那一帧的第二遍）。
func (it *menuItem) Chosen() bool { return it.seq == it.m.chosen && !it.disabled }

// shortcutText 照 macOS 菜单的写法：⌃⌥⇧⌘ 加键名。
func shortcutText(mods ui.Modifiers, key ui.Key) string {
	var b strings.Builder
	for _, m := range []struct {
		mod  ui.Modifiers
		name string
	}{{ui.Ctrl, "⌃"}, {ui.Alt, "⌥"}, {ui.Shift, "⇧"}, {ui.Super, "⌘"}} {
		if mods&m.mod != 0 {
			b.WriteString(m.name)
		}
	}
	switch {
	case key >= ui.KeyA && key <= ui.KeyZ:
		b.WriteRune(rune('A' + key - ui.KeyA))
	case key >= ui.Key0 && key <= ui.Key9:
		b.WriteRune(rune('0' + key - ui.Key0))
	case key >= ui.KeyF1 && key <= ui.KeyF12:
		b.WriteString("F" + strconv.Itoa(int(key-ui.KeyF1)+1))
	default:
		b.WriteString(map[ui.Key]string{
			ui.KeyEnter: "↩", ui.KeyEscape: "⎋", ui.KeyBackspace: "⌫", ui.KeyDelete: "⌦", ui.KeyTab: "⇥",
			ui.KeyEqual: "=", ui.KeyMinus: "-",
			ui.KeyUp: "↑", ui.KeyDown: "↓", ui.KeyLeft: "←", ui.KeyRight: "→",
		}[key])
	}
	return b.String()
}

// menuState 是一个菜单开着时的状态，挂在打开它的元素上（ui.Local）。
type menuState struct {
	open    bool
	focused bool    // 已经把焦点交给菜单：终端开着时方向键要给菜单
	x, y    float32 // 右键菜单弹出的位置（窗口坐标）
	hot     int     // 第一层高亮的项（编号），0 是没有
	ptr     int     // 上一帧指针停着的第一层项，指针换了项才改 hot，免得盖掉方向键
	sub     int     // 展开着子菜单的项
	subHot  int     // 子菜单里高亮的项
}

func menuStateOf(e ui.Element) *menuState {
	return ui.Local(e, "posse-menu", func() menuState { return menuState{} })
}

// menuButton 让按钮点一下在下面弹出菜单（代替 Element.Menu）。
func menuButton(c *ui.Context, b ui.Element, build func(m *popMenu)) {
	st := menuStateOf(b)
	if b.Clicked() {
		st.open = !st.open
		st.reset()
	}
	if !st.open {
		return
	}
	ui.PopoverBase(c, b, &st.open, func(panel ui.Element) {
		showMenu(c, panel, st, build)
	})
}

// contextMenu 让元素右键时在指针处弹出菜单（代替 Element.ContextMenu）。
func contextMenu(c *ui.Context, e ui.Element, build func(m *popMenu)) {
	st := menuStateOf(e)
	if x, y, ok := e.ContextMenuAsked(); ok {
		st.open, st.x, st.y = true, x, y
		st.reset()
	}
	contextMenuAt(c, st, build)
}

// contextMenuAt 在 st 记下的位置画开着的右键菜单：没有元素可挂的地方（终端插件交来的位置）用它。
func contextMenuAt(c *ui.Context, st *menuState, build func(m *popMenu)) {
	if !st.open {
		return
	}
	ui.Overlay(c, func() {
		// 零大小的锚点放在指针处，菜单贴着它；放不下时 AttachTo 会翻到另一边
		anchor := ui.Box(c.Key("posse-menu-at")).Absolute().Left(st.x).Top(st.y).Size(0, 0)
		panel := ui.Box(c.Key("posse-menu")).Role(ui.RolePopup).AttachTo(anchor, ui.AnchorTopLeft, ui.AnchorTopLeft)
		// 弹出菜单的那一下右键也算"在菜单外面按下"，弹出的这一帧不能据此关掉它
		fresh := !st.focused
		showMenu(c, panel, st, build)
		if !fresh && panel.PressedOutside() || panel.Shortcut(0, ui.KeyEscape) || panel.OverlayShortcut(0, ui.KeyEscape) {
			st.open = false
		}
	})
}

func (st *menuState) reset() { st.focused, st.hot, st.ptr, st.sub, st.subHot = false, 0, 0, 0, 0 }

// showMenu 收集菜单项、画在 panel 里、处理键盘，点了哪一项就在这一帧执行它。
func showMenu(c *ui.Context, panel ui.Element, st *menuState, build func(m *popMenu)) {
	seq := 0
	m := newPopMenu(&seq, -1)
	build(m)
	floating(c, panel).Padding(4).MinWidth(200).MaxWidth(360).Role(ui.RoleMenu).Focusable()
	if !st.focused {
		panel.Focus()
		st.focused = true
	}
	picked := menuKeys(panel, st, m)
	hover := 0
	panel.Children(func() {
		if p := drawMenuItems(c, m, st, 0, &hover); p != 0 {
			picked = p
		}
	})
	if hover != st.ptr {
		st.ptr = hover
		if hover != 0 {
			st.hot = hover
		}
	}
	if picked != 0 {
		st.open = false
		seq = 0
		build(newPopMenu(&seq, picked))
	}
}

// menuKeys 处理方向键和回车：↑↓ 在第一层或展开的子菜单里移动，→ 展开子菜单、← 收起，回车选中。返回选中的项。
func menuKeys(panel ui.Element, st *menuState, m *popMenu) int {
	items, hot := m.items, &st.hot
	if st.sub != 0 {
		if it := findItem(m.items, st.sub); it != nil && it.sub != nil {
			items, hot = it.sub.items, &st.subHot
		}
	}
	move := func(d int) {
		var on []int
		for _, it := range items {
			if !it.sep && !it.disabled {
				on = append(on, it.seq)
			}
		}
		if len(on) == 0 {
			return
		}
		i := -1
		for k, s := range on {
			if s == *hot {
				i = k
			}
		}
		switch {
		case i < 0 && d > 0:
			i = 0
		case i < 0:
			i = len(on) - 1
		default:
			i = (i + d + len(on)) % len(on)
		}
		*hot = on[i]
	}
	switch {
	case panel.Shortcut(0, ui.KeyDown):
		move(1)
	case panel.Shortcut(0, ui.KeyUp):
		move(-1)
	case panel.Shortcut(0, ui.KeyLeft):
		st.sub, st.subHot = 0, 0
	case panel.Shortcut(0, ui.KeyRight), panel.Shortcut(0, ui.KeyEnter), panel.Shortcut(0, ui.KeySpace):
		it := findItem(items, *hot)
		switch {
		case it == nil || it.disabled:
		case it.sub != nil:
			st.sub, st.subHot = it.seq, 0
		case panel.Shortcut(0, ui.KeyRight):
		default:
			return it.seq
		}
	}
	return 0
}

func findItem(items []*menuItem, seq int) *menuItem {
	for _, it := range items {
		if it.seq == seq {
			return it
		}
	}
	return nil
}

// drawMenuItems 画一层菜单项，返回这一帧点了的项。level 0 是第一层，1 是子菜单。
func drawMenuItems(c *ui.Context, m *popMenu, st *menuState, level int, pointed *int) int {
	t := c.Theme()
	p := colors(t)
	// 有一项可勾选就给勾留出位置，有一项带图标就给图标留出位置，各项的文字才对齐
	var checks, icons bool
	for _, it := range m.items {
		checks = checks || it.checked != nil
		icons = icons || it.icon != nil || it.lead != nil
	}
	hot := st.hot
	if level > 0 {
		hot = st.subHot
	}
	picked := 0
	prevSep := true
	for i, it := range m.items {
		if it.sep {
			// 开头、结尾和连着的分隔线不画
			if !prevSep && i < len(m.items)-1 {
				ui.Box(c.Key(it.seq)).Height(1).Margin(4, 6).Background(p.line)
				prevSep = true
			}
			continue
		}
		prevSep = false
		row := ui.Row(c.Key(it.seq)).Height(sz(28)).Padding(0, 12, 0, 8).Gap(8).Radius(rRow).AlignItems(ui.Center).
			Role(ui.RoleMenuItem).Label(it.label)
		if it.disabled {
			row.Cursor(ui.CursorDefault)
		} else if row.Hovered() {
			if level == 0 {
				*pointed = it.seq
				if it.sub != nil {
					st.sub = it.seq
				} else if st.sub != 0 {
					st.sub, st.subHot = 0, 0
				}
			} else {
				st.subHot = it.seq
			}
		}
		bg := ui.Transparent
		if !it.disabled && (it.seq == hot || it.sub != nil && st.sub == it.seq) {
			bg = p.wash
		}
		row.Background(row.AnimateColor("bg", bg, hover))
		fg, muted := t.Text, p.faint
		if it.disabled {
			fg, muted = p.faint, p.faint
		}
		row.Children(func() {
			if checks {
				box := ui.Box(c).Width(14).Shrink(0).Center()
				if it.checked != nil && *it.checked {
					box.Children(func() { ui.Icon(c, iconCheck).FontSize(fsBody).TextColor(t.Accent) })
				}
			}
			switch {
			case it.lead != nil:
				it.lead()
			case it.icon != nil:
				ui.Icon(c, it.icon).FontSize(iconSize).TextColor(t.TextMuted).Shrink(0)
			case icons:
				ui.Box(c).Width(15).Shrink(0)
			}
			ui.Text(c, it.label).FontSize(fsBody).TextColor(fg).Grow(1).Shrink(1).SingleLine()
			if it.keys != "" {
				ui.Text(c, it.keys).FontSize(fsSmall).TextColor(muted).Shrink(0)
			}
			if it.sub != nil {
				ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(muted).Shrink(0)
			}
		})
		if it.sub != nil && st.sub == it.seq {
			ui.Overlay(c, func() {
				panel := ui.Column(c.Key("posse-submenu")).Role(ui.RoleMenu).AttachTo(row, ui.AnchorTopRight, ui.AnchorTopLeft).Margin(-4, 0, 0, 2)
				floating(c, panel).Padding(4).MinWidth(180).MaxWidth(360)
				panel.Children(func() {
					if p := drawMenuItems(c, it.sub, st, 1, pointed); p != 0 {
						picked = p
					}
				})
			})
		}
		if !it.disabled && it.sub == nil && row.Clicked() {
			picked = it.seq
		}
	}
	return picked
}

// sysMenu 是 mygo 自己的菜单（文字输入框、可选文字、编辑器的编辑菜单）：经 PresentMenus 交给 Posse，照样自绘。
type sysMenu struct {
	items  []ui.MenuEntry
	choose func(id int)
	st     menuState
}

// presentMenu 是交给 mygo 的菜单呈现器，在主线程、帧外调用。
func (a *app) presentMenu(items []ui.MenuEntry, x, y float32, choose func(id int)) {
	a.sysMenu = sysMenu{items: items, choose: choose, st: menuState{open: true, x: x, y: y}}
}

// sysMenuView 画 mygo 交来的菜单；最后画，压在对话框上面。
func (a *app) sysMenuView(c *ui.Context) {
	sm := &a.sysMenu
	if !sm.st.open {
		return
	}
	choose := sm.choose
	contextMenuAt(c, &sm.st, func(m *popMenu) {
		addEntries(m, sm.items, func(id int) {
			// 交回 mygo 时不能在建界面的过程中
			a.update(func() { choose(id) })
		})
	})
}

func addEntries(m *popMenu, items []ui.MenuEntry, chosen func(id int)) {
	for _, e := range items {
		switch {
		case e.Separator:
			m.Separator()
		case e.Submenu != nil:
			m.Submenu(menuLabel(e.Label), func(sm *popMenu) { addEntries(sm, e.Submenu, chosen) })
		default:
			it := m.Item(menuLabel(e.Label)).Disabled(!e.Enabled)
			it.keys = acceleratorText(e.Shortcut)
			if e.Checked {
				it.Checked(true)
			}
			if it.Chosen() {
				chosen(e.ID)
			}
		}
	}
}

// menuLabels 把 mygo 菜单里的英文换成中文，叫法照 macOS 中文版。
var menuLabels = map[string]string{
	"Undo": "撤销", "Redo": "重做", "Cut": "剪切", "Copy": "复制", "Paste": "粘贴", "Delete": "删除", "Select All": "全选",
	"Go to Definition": "跳到定义", "Toggle Comment": "切换注释",
	"Selection": "选择", "Select Next Occurrence": "选择下一个相同项", "Select All Occurrences": "选择所有相同项",
	"Expand Selection": "扩大选择范围", "Split into Lines": "拆分到每一行",
	"Transform": "转换", "Upper Case": "大写", "Lower Case": "小写", "Title Case": "首字母大写",
	"Snake Case": "snake_case", "Kebab Case": "kebab-case", "Camel Case": "camelCase", "Pascal Case": "PascalCase",
	"Sort Lines": "按行排序", "Reverse Lines": "倒转行序", "Unique Lines": "删除重复行", "Join Lines": "合并行",
	"Trim Trailing Whitespace": "删除行尾空白",
	"Folding":                  "折叠", "Fold": "折叠", "Unfold": "展开", "Fold All": "全部折叠", "Unfold All": "全部展开",
	"Clear Scrollback": "清除回滚",
}

func menuLabel(s string) string {
	if t, ok := menuLabels[s]; ok {
		return t
	}
	return s
}

// acceleratorText 把 mygo 的快捷键写法（"Super+Shift+Z"）换成 macOS 菜单的写法（⇧⌘Z）。
func acceleratorText(acc string) string {
	if acc == "" {
		return ""
	}
	parts := strings.Split(acc, "+")
	if strings.HasSuffix(acc, "++") {
		parts = append(parts[:len(parts)-2], "+")
	}
	key := parts[len(parts)-1]
	mods := map[string]bool{}
	for _, p := range parts[:len(parts)-1] {
		mods[p] = true
	}
	var b strings.Builder
	for _, m := range []struct{ name, sym string }{{"Ctrl", "⌃"}, {"Alt", "⌥"}, {"Shift", "⇧"}, {"Super", "⌘"}} {
		if mods[m.name] {
			b.WriteString(m.sym)
		}
	}
	if sym, ok := map[string]string{
		"Enter": "↩", "Escape": "⎋", "Backspace": "⌫", "Delete": "⌦", "Tab": "⇥", "Space": "空格",
		"Up": "↑", "Down": "↓", "Left": "←", "Right": "→", "PageUp": "⇞", "PageDown": "⇟", "Home": "↖", "End": "↘",
	}[key]; ok {
		key = sym
	}
	b.WriteString(strings.ToUpper(key))
	return b.String()
}
