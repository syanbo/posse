package app

import (
	"github.com/egoist/mygo/ui"
)

// popMenu 是 Hive 自己画的弹出菜单，代替系统菜单（NSMenu）：系统菜单的悬停高亮是系统强调色、底板是毛玻璃，都改不了。
// 样式照 floating 的浮层，悬停是中性透明底（wash），和列表一致；↑↓ 换项、回车选中，Esc 或点外面关掉。
type popMenu struct {
	st    *menuState
	n     int // 这一帧画到第几项
	enter bool
	hover int // 这一帧指针停在哪一项，-1 是没有
}

type menuState struct {
	open bool
	hot  int // 高亮的项：指针停着的，或方向键移到的；-1 是没有
	ptr  int // 上一帧指针停着的项，指针换了项才改 hot，免得盖掉方向键
	n    int // 上一帧的项数，方向键循环用
}

// menuButton 让按钮点一下弹出菜单，build 往里加项。
func menuButton(c *ui.Context, b ui.Element, build func(m *popMenu)) {
	st := ui.Local(b, "menu", func() menuState { return menuState{hot: -1, ptr: -1} })
	if b.Clicked() {
		st.open = !st.open
		st.hot, st.ptr = -1, -1
	}
	ui.PopoverBase(c, b, &st.open, func(panel ui.Element) {
		floating(c, panel).Padding(4).MinWidth(180).Role(ui.RoleMenu).Focusable().AutoFocus()
		switch {
		case st.n == 0:
		case panel.Shortcut(0, ui.KeyDown):
			st.hot = (st.hot + 1) % st.n
		case panel.Shortcut(0, ui.KeyUp):
			st.hot = (st.hot - 1 + st.n) % st.n
		}
		m := &popMenu{st: st, enter: panel.Shortcut(0, ui.KeyEnter) || panel.Shortcut(0, ui.KeySpace), hover: -1}
		panel.Children(func() { build(m) })
		st.n = m.n
		if m.hover != st.ptr {
			st.ptr = m.hover
			if m.hover >= 0 {
				st.hot = m.hover
			}
		}
	})
}

// Item 加一项（c 是建菜单时的 Context）：lead 画最左边的图标（可以为 nil），keys 是右边的快捷键提示。选中时关掉菜单并返回 true。
func (m *popMenu) Item(c *ui.Context, lead func(), label, keys string) bool {
	t, i := c.Theme(), m.n
	m.n++
	row := ui.Row(c.Key("menu-item-"+label)).Height(30).Padding(0, 10, 0, 6).Gap(8).Radius(rButton).AlignItems(ui.Center).
		Role(ui.RoleMenuItem).Label(label)
	if row.Hovered() {
		m.hover = i
	}
	bg := ui.Transparent
	if m.st.hot == i {
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		if lead != nil {
			lead()
		}
		ui.Text(c, label).FontSize(13).Grow(1).SingleLine()
		if keys != "" {
			ui.Text(c, keys).FontSize(fsCaption).TextColor(colors(t).faint).Shrink(0)
		}
	})
	if row.Clicked() || m.enter && m.st.hot == i {
		m.st.open = false
		return true
	}
	return false
}
