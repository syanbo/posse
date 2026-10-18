package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// 设计约定（参照 Claude 桌面端的克制，见 docs/plans/ui-claude-polish.html）：
//   - 暖灰底色，和陶土色同一色温。顶栏 band、画布 canvas、卡片 card 只差一点明度，浮层再高一层 raised；
//     深色下侧栏 side 比画布明显浅一档。分层靠明度差和 8% 透明的细线，不靠粗线。
//   - 单闭合边框：从画布往里看，任何一条视线最多穿过一层闭合的框。有框的容器里，按钮和输入不再加框，
//     用填充（fill）或幽灵样式；容器内部分区用单向细线。
//   - 陶土色用在主按钮、焦点、工作中的星号、链接、图标，以及选中项：强调色淡底加左边 3px 竖条。
//     悬停是中性的透明底（wash）。
//   - 只有"待处理"用琥珀色并配文字，是界面上唯一醒目的东西；本轮完成用绿色提示可以看结果，
//     工作中用中性色，最安静。状态靠形状区分而不只靠颜色：实心蜂巢格是正在进行，空心是停下来了。
//   - 每个 agent 有自己的色块图标，扫一眼就知道是谁。
//   - 路径、分支、命令用等宽字体和次要色。一行里的几项信息靠间距和图标分开，不用"A · B"中点串。
//   - 字号和圆角用下面的常量；字重三档：400 正文、500 列表标题和按钮、600 只给页面标题和卡片标题。
//   - 悬停和按下即时反馈（≤120ms 缓出，按下缩放 0.98）。自动播放的动画只有一处：工作中显示 Claude Code 那样来回变换的星号。

var hover = ui.Animation{Duration: 120 * time.Millisecond}

// 字号阶梯（pt）。
const (
	fsCaption   = 11   // 说明、计数、快捷键
	fsMeta      = 12   // 元信息：路径、时间、作者
	fsBody      = 14   // 正文、列表
	fsRead      = 14.5 // 阅读：Markdown 回复
	fsCardTitle = 15
	fsPageTitle = 18
)

// 圆角按角色分级：外圆角 = 内圆角 + 内边距。
const (
	rButton   = 7
	rInput    = 8
	rCode     = 10
	rCard     = 12
	rFloat    = 12
	rBubble   = 14
	rComposer = 16
)

// palette 是在系统主题之外 Hive 自己的颜色。
type palette struct {
	honey  ui.Color // 等你处理
	sage   ui.Color // 本轮完成
	side   ui.Color // 侧栏、窗口底
	canvas ui.Color // 主区域的底，卡片放在上面
	card   ui.Color // 卡片，和主题的 Background、终端底色一致
	band   ui.Color // 顶栏
	raised ui.Color // 浮层：比卡片再高一层
	line   ui.Color // 分隔线
	tint   ui.Color // 选中项的底：强调色淡底
	wash   ui.Color // 悬停的底
	fill   ui.Color // 框里的次级按钮的底
	faint  ui.Color // 最弱的文字：占位、不重要的计数
	code   ui.Color // 代码块的底
}

func colors(t *ui.Theme) palette {
	if t.Dark {
		return palette{
			honey: ui.Hex("#F2AA2E"), sage: ui.Hex("#8DBB92"),
			side: ui.Hex("#2A2A27"), canvas: ui.Hex("#1A1A19"), card: t.Background, band: ui.Hex("#1C1C1B"), raised: ui.Hex("#2A2A27"),
			line: ui.RGBA(255, 255, 255, 0.08), tint: t.Accent.Alpha(0.2), wash: ui.RGBA(255, 255, 255, 0.05),
			fill: ui.RGBA(255, 255, 255, 0.09), faint: ui.Hex("#7D7B76"), code: ui.Hex("#141413"),
		}
	}
	return palette{
		honey: ui.Hex("#B5700E"), sage: ui.Hex("#3E8A4C"),
		side: ui.Hex("#F3F2EF"), canvas: ui.Hex("#F8F8F6"), card: t.Background, band: ui.Hex("#F8F7F5"), raised: ui.Hex("#FFFFFF"),
		line: ui.RGBA(0, 0, 0, 0.08), tint: t.Accent.Alpha(0.12), wash: ui.RGBA(0, 0, 0, 0.04),
		fill: ui.RGBA(0, 0, 0, 0.06), faint: ui.Hex("#999793"), code: ui.Hex("#F5F4F1"),
	}
}

var hiveThemes [2]*ui.Theme

// hiveTheme 在系统主题上换成暖灰、Claude 橙和 8px 圆角；卡片和终端一个颜色（Background）。
func hiveTheme(dark bool) *ui.Theme {
	i := 0
	if dark {
		i = 1
	}
	if hiveThemes[i] != nil {
		return hiveThemes[i]
	}
	var t *ui.Theme
	if dark {
		t = ui.DarkTheme()
		t.Background = ui.Hex("#20201E")
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#2A2A27"), ui.Hex("#31312E"), ui.Hex("#383835")
		t.Border = ui.Hex("#3A3936")
		t.Text, t.TextMuted = ui.Hex("#ECEBE8"), ui.Hex("#B4B2AC")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#D97757"), ui.Hex("#E38C70"), ui.Hex("#C4643F")
		t.Selection, t.Focus = ui.RGBA(217, 119, 87, 0.35), ui.RGBA(217, 119, 87, 0.6)
	} else {
		t = ui.LightTheme()
		t.Background = ui.Hex("#FFFFFF")
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#F3F2EF"), ui.Hex("#ECEBE7"), ui.Hex("#E4E2DD")
		t.Border = ui.Hex("#DCDAD5")
		t.Text, t.TextMuted = ui.Hex("#1F1E1D"), ui.Hex("#6B6966")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#C15F3C"), ui.Hex("#AD5333"), ui.Hex("#98482B")
		t.Selection, t.Focus = ui.RGBA(193, 95, 60, 0.22), ui.RGBA(193, 95, 60, 0.5)
	}
	t.Radius = 8
	hiveThemes[i] = t
	return t
}

// terminalThemes 让终端底色跟着 Hive 的主题，而不是 mygo 默认的底色。
func terminalThemes() (light, dark *terminal.Theme) {
	light, dark = terminal.LightTheme(), terminal.DarkTheme()
	light.Background, light.Foreground = hiveTheme(false).Background, hiveTheme(false).Text
	dark.Background, dark.Foreground = hiveTheme(true).Background, hiveTheme(true).Text
	return light, dark
}

// floating 是浮在内容上面的卡片（右侧面板、链接卡片、图片预览）：顶栏底色、描边和投影，位置由调用方定。
// 深色下不画白框，用 1px 内高光（像边缘接到顶光）配深而散的投影；浅色下是细外环加投影。
func floating(c *ui.Context, e ui.Element) ui.Element {
	t := c.Theme()
	p := colors(t)
	e.Radius(rFloat).Background(p.raised)
	if t.Dark {
		return e.InsetShadow(0, 0, 0, 1, ui.RGBA(255, 255, 255, 0.1)).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.5))
	}
	return e.Border(1, p.line).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, 0.12))
}

// fieldInput 是放在框里（浮层、对话框、卡片）的单行输入：填充底、不加框，聚焦时描焦点色。
// 返回里面的输入框，调用方照常问 Submitted、Changed、Shortcut。
func fieldInput(c *ui.Context, value *string, placeholder string) ui.Element {
	t := c.Theme()
	box := ui.Row(c).Height(30).Padding(0, 10).Radius(rInput).Background(colors(t).fill).AlignItems(ui.Center)
	var in ui.Element
	box.Children(func() {
		in = ui.TextInputBase(c, value).Placeholder(placeholder).Label(placeholder).FontSize(13).Grow(1)
	})
	if in.Focused() {
		box.Border(1, t.Focus)
	}
	return in
}

// fadeEdges 在可滚动列表的上下边各叠一条 24pt 的渐隐，提示那边还有内容；滚到头的那边不画。
// 在绘制时判断，滚动时不必重建界面也跟得上。bg 是列表所在的底色。
func fadeEdges(list ui.Element, s *ui.ListState, bg ui.Color) ui.Element {
	const h = 24
	return list.DrawOver(func(p *ui.Painter, r ui.Rect) {
		if first, last := s.Visible(); last >= 0 && first > 0 {
			p.FillGradient(ui.Rect{X: r.X, Y: r.Y, W: r.W, H: h}, ui.LinearGradient{From: bg, To: bg.Alpha(0), Angle: 180}, 0)
		}
		if first, last := s.Visible(); last >= first && !s.AtEnd() {
			p.FillGradient(ui.Rect{X: r.X, Y: r.Y + r.H - h, W: r.W, H: h}, ui.LinearGradient{From: bg.Alpha(0), To: bg, Angle: 180}, 0)
		}
	})
}

// edge 给卡片一样的容器描边：深色是 1px 内高光，浅色是很淡的外环加一点投影；卡片本身是唯一的那层框。
func edge(c *ui.Context, e ui.Element) ui.Element {
	if c.Theme().Dark {
		return e.InsetShadow(0, 0, 0, 1, ui.RGBA(255, 255, 255, 0.07))
	}
	return e.Border(1, ui.RGBA(0, 0, 0, 0.06)).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.04))
}

// composer 是一体的输入区：圆角框里是输入，右下角是发送按钮，⌘↩ 也能发送；聚焦时描边换成焦点色。
// 它自己就是框，外面不再套卡片。返回这一帧是否要发送（内容不为空）。
func composer(c *ui.Context, value *string, placeholder, verb string, multiline bool) bool {
	t := c.Theme()
	box := ui.Column(c).Radius(rComposer).Background(colors(t).card).Padding(10, 10, 10, 14).Gap(6)
	sent := false
	var in ui.Element
	box.Children(func() {
		if multiline {
			in = ui.TextAreaBase(c, value).Placeholder(placeholder).Label(placeholder).Height(64).FontSize(fsBody)
		} else {
			in = ui.TextInputBase(c, value).Placeholder(placeholder).Label(placeholder).FontSize(fsBody).Padding(4, 0)
			sent = in.Submitted()
		}
		// 中文输入法拼字时的回车是确认拼写，不是发送
		sent = (sent || in.Shortcut(ui.Cmd, ui.KeyEnter)) && !in.Composing()
		ui.Row(c).Justify(ui.End).Children(func() {
			btn := pressable(accentButton(c, verb)).Tooltip(verb + " ⌘↩")
			if strings.TrimSpace(*value) == "" {
				btn.Disabled(true).Opacity(0.5)
			} else if btn.Clicked() {
				sent = true
			}
		})
	})
	if in.Focused() {
		box.Border(1, t.Focus)
	} else {
		edge(c, box)
	}
	return sent && strings.TrimSpace(*value) != ""
}

// pendingLabel 是"待处理"三个字：界面上唯一用琥珀色的文字。
func pendingLabel(c *ui.Context) ui.Element {
	return ui.Text(c, "待处理").FontSize(11).FontWeight(600).TextColor(colors(c.Theme()).honey)
}

// workspaceIcon 是工作区的图标：worktree 是分支，其余是文件夹。
func workspaceIcon(kind string) *ui.SVG {
	if kind == "worktree" {
		return iconBranch
	}
	return iconFolder
}

// markSpans 把 text 按 marks（markRanges 的结果）切成几段，命中的段铺上 mark 底色，其余沿用 base 的样式。
func markSpans(text string, marks [][2]int, base ui.Span, mark ui.Color) []ui.Span {
	out := make([]ui.Span, 0, 2*len(marks)+1)
	last := 0
	for _, m := range marks {
		s := base
		s.Text = text[last:m[0]]
		out = append(out, s)
		s.Text, s.Background = text[m[0]:m[1]], mark
		out = append(out, s)
		last = m[1]
	}
	base.Text = text[last:]
	return append(out, base)
}

// homeDir 是用户主目录，显示路径时每帧都要用，只取一次。
var homeDir, _ = os.UserHomeDir()

func shortPath(p string) string {
	if homeDir != "" && strings.HasPrefix(p, homeDir) {
		return "~" + p[len(homeDir):]
	}
	return p
}

// card 是放在画布上的一块内容。
func card(c *ui.Context) ui.Element {
	p := colors(c.Theme())
	return edge(c, ui.Column(c).Background(p.card).Radius(rCard))
}

// titleText 是页面、对话框和卡片的标题：界面上只有标题用 600 的字重。
func titleText(c *ui.Context, text string, size float32) ui.Element {
	return ui.Text(c, text).FontSize(size).FontWeight(600)
}

// cardTitle 是卡片里一节的标题。
func cardTitle(c *ui.Context, title string) ui.Element {
	return titleText(c, title, 13)
}

// band 是主区域顶上的一条栏：页面标题和操作，可以拖动窗口。
func (a *app) band(c *ui.Context, fn func()) {
	p := colors(c.Theme())
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 14, 0, 20+a.barInset(c)).Gap(10).Background(p.band).
		BorderWidth(0, 0, 1, 0).BorderColor(p.line).DragWindow().Children(fn)
}

// selectable 给列表里的一行加上选中和悬停的底；选中时左边有强调色竖条。
func selectable(c *ui.Context, row ui.Element, selected bool) ui.Element {
	t := c.Theme()
	selectedBg(c, row, selected)
	if selected {
		accent := t.Accent
		row.Draw(func(pt *ui.Painter, r ui.Rect) {
			h := min(r.H-12, 18)
			pt.Fill(ui.Rect{X: r.X + 3, Y: r.Y + (r.H-h)/2, W: 3, H: h}, accent, 1.5)
		})
	}
	return row
}

// selectedBg 只给一行加选中和悬停的底，不画竖条（侧栏的行）。
func selectedBg(c *ui.Context, row ui.Element, selected bool) ui.Element {
	p := colors(c.Theme())
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.tint
	case row.Hovered():
		bg = p.wash
	}
	return row.Background(row.AnimateColor("bg", bg, hover))
}

// tile 是带底色的方形图标，用在卡片标题旁边。
func tile(c *ui.Context, icon *ui.SVG, size float32, bg, fg ui.Color) ui.Element {
	return ui.Box(c).Size(size, size).Radius(size * 0.26).Background(bg).Center().Shrink(0).Children(func() {
		ui.Icon(c, icon).FontSize(size * 0.52).TextColor(fg)
	})
}

// agentTile 是 agent 的色块图标。
func agentTile(c *ui.Context, agent string, size float32) ui.Element {
	t := c.Theme()
	icon, bg, fg := iconTerminal, t.SurfacePressed, t.Text
	switch agent {
	case "claude":
		icon, bg, fg = iconClaude, ui.Hex("#D97757"), ui.Hex("#FFFFFF")
	case "codex":
		icon, bg, fg = iconCodex, ui.Hex("#4F5DF5"), ui.Hex("#FFFFFF") // Codex 应用图标的蓝紫
	case "grok":
		icon, bg, fg = iconGrok, t.Text, t.Background
	case "cursor":
		icon, bg, fg = iconCursor, ui.Hex("#6E56CF"), ui.Hex("#FFFFFF")
	}
	return tile(c, icon, size, bg, fg).Tooltip(agentTitle(agent))
}

// cjkLift 把按钮里的文字往上提：中文字形来自苹方回退，在系统字体的行框里偏低，按行框居中看起来下沉约 1pt。
const cjkLift = 1

// textButton 是次级按钮：无边框的填充底。它常放在卡片、对话框这些有框的容器里，再加框就是框里套框。
func textButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Height(28).Padding(0, 12).Radius(rButton).Label(label).Shrink(0)
	bg := colors(t).fill
	if b.Hovered() {
		bg = ui.RGBA(0, 0, 0, 0.1)
		if t.Dark {
			bg = ui.RGBA(255, 255, 255, 0.14)
		}
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Text(c, label).FontSize(13).FontWeight(500).Margin(0, 0, 2*cjkLift, 0).SingleLine()
	})
	return b
}

// accentButton 是主按钮（mygo 的 PrimaryButton），上内边距少 cjkLift、下内边距多 cjkLift；一屏里只放一个。
func accentButton(c *ui.Context, label string) ui.Element {
	return liftButton(c, ui.PrimaryButton(c, label)).Radius(rButton)
}

func liftButton(c *ui.Context, b ui.Element) ui.Element {
	t := c.Theme()
	v, h := t.Space(1.5), t.Space(3.5) // mygo 按钮的默认内边距
	return b.Padding(v-cjkLift, h, v+cjkLift, h)
}

// segmented 是分段选择：轨道是一层淡底，选中的一段填强调色；返回这一帧是否换了选择。
func segmented(c *ui.Context, sel *int, label string, options ...string) bool {
	t := c.Theme()
	p := colors(t)
	changed := false
	ui.Row(c).Padding(2).Gap(2).Radius(rInput).Background(p.fill).Shrink(0).Label(label).Role(ui.RoleRadioGroup).Children(func() {
		for i, o := range options {
			// 被拉宽时（对话框里）各段平分宽度
			seg := ui.Box(c.Key(fmt.Sprint("seg-", i))).Height(24).Padding(0, 12).Grow(1).Shrink(0).Radius(6).Center().Label(o).Role(ui.RoleRadio).Checked(i == *sel)
			bg, fg := ui.Transparent, t.TextMuted
			switch {
			case i == *sel:
				// 和列表选中项同一种强调色淡底，实心橙只留给主按钮
				bg, fg = p.tint, t.Text
			case seg.Hovered():
				fg = t.Text
			}
			seg.Background(seg.AnimateColor("bg", bg, hover))
			seg.Children(func() {
				text := ui.Text(c, o).FontSize(12).TextColor(fg).Margin(0, 0, 2*cjkLift, 0).Shrink(0)
				if i == *sel {
					text.FontWeight(500)
				}
			})
			if seg.Clicked() && i != *sel {
				*sel, changed = i, true
			}
		}
	})
	return changed
}

// metaRow 是标题下面的次要信息，靠间距和图标分开，不用中点串起来；放不下时整项折到下一行。
func metaRow(c *ui.Context, items ...func()) {
	ui.Row(c).Gap(14).Wrap().Children(func() {
		for _, it := range items {
			if it != nil {
				it()
			}
		}
	})
}

// meta 是 metaRow 里的一段文字。
func meta(c *ui.Context, text string) func() {
	return func() {
		ui.Text(c, text).FontSize(12).FontFeatures("tnum").TextColor(c.Theme().TextMuted).MaxLines(1).Shrink(0)
	}
}

// metaMono 是 metaRow 里的路径、分支这类标识符。
func metaMono(c *ui.Context, icon *ui.SVG, text, tip string) func() {
	return func() {
		t := c.Theme()
		ui.Row(c).Gap(4).Shrink(1).Children(func() {
			if icon != nil {
				ui.Icon(c, icon).FontSize(13).TextColor(t.TextMuted)
			}
			e := ui.Text(c, text).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			if tip != "" {
				e.Tooltip(tip)
			}
		})
	}
}
