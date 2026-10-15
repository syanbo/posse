package app

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// 设计约定（质感参照 AndroidMirror）：
//   - 三层底色：顶栏 band、画布 canvas、卡片 card。成块的内容放进卡片：12px 圆角，不加描边，靠底色和画布区分。
//   - 选中项用强调色淡底，左边加一道 3px 的强调色竖条；悬停只铺一层很淡的底。
//   - 只有"待处理"用琥珀色并配文字，是界面上唯一醒目的东西；本轮完成用绿色提示可以看结果，
//     工作中用中性色，最安静。状态靠形状区分而不只靠颜色：实心蜂巢格是正在进行，空心是停下来了。
//   - 每个 agent 有自己的色块图标，像 AndroidMirror 里的应用图标，扫一眼就知道是谁。
//   - 路径、分支、命令用等宽字体和次要色。一行里的几项信息靠间距和图标分开，不用"A · B"中点串。
//   - 悬停和按下即时反馈（≤120ms 缓出，按下缩放 0.96）。自动播放的动画只有一处：工作中显示 Claude Code 那样来回变换的星号。

var hover = ui.Animation{Duration: 120 * time.Millisecond}

// palette 是在系统主题之外 Hive 自己的颜色。
type palette struct {
	honey  ui.Color // 等你处理
	sage   ui.Color // 本轮完成
	canvas ui.Color // 主区域的底，卡片放在上面
	card   ui.Color // 卡片，和主题的 Background、终端底色一致
	band   ui.Color // 顶栏
	line   ui.Color // 分隔线
	tint   ui.Color // 选中项的底
	wash   ui.Color // 悬停的底
}

func colors(t *ui.Theme) palette {
	if t.Dark {
		return palette{
			honey: ui.Hex("#F2AA2E"), sage: ui.Hex("#8DBB92"),
			canvas: ui.Hex("#262628"), card: t.Background, band: ui.Hex("#2E2E30"),
			line: ui.RGBA(255, 255, 255, 0.08), tint: t.Accent.Alpha(0.2), wash: ui.RGBA(255, 255, 255, 0.05),
		}
	}
	return palette{
		honey: ui.Hex("#B5700E"), sage: ui.Hex("#3E8A4C"),
		canvas: ui.Hex("#F1F1F4"), card: t.Background, band: ui.Hex("#F8F8FA"),
		line: ui.RGBA(0, 0, 0, 0.08), tint: t.Accent.Alpha(0.12), wash: ui.RGBA(0, 0, 0, 0.04),
	}
}

var hiveThemes [2]*ui.Theme

// hiveTheme 在系统主题上换成 Claude 橙和 8px 圆角；暗色底和卡片、终端一个颜色。
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
		t.Background = ui.Hex("#1C1C1E")
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#2C2C2E"), ui.Hex("#363638"), ui.Hex("#404043")
		t.Border = ui.Hex("#3A3A3C")
		t.TextMuted = ui.Hex("#98989F")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#D97757"), ui.Hex("#E38C70"), ui.Hex("#C4643F")
		t.Selection, t.Focus = ui.RGBA(217, 119, 87, 0.35), ui.RGBA(217, 119, 87, 0.6)
	} else {
		t = ui.LightTheme()
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#F2F2F5"), ui.Hex("#E8E8EC"), ui.Hex("#DDDDE2")
		t.Border = ui.Hex("#D8D8DD")
		t.TextMuted = ui.Hex("#6E6E73")
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
func floating(c *ui.Context, e ui.Element) ui.Element {
	t := c.Theme()
	p := colors(t)
	shadow := ui.RGBA(0, 0, 0, 0.18)
	if t.Dark {
		shadow = ui.RGBA(0, 0, 0, 0.45)
	}
	return e.Radius(12).Background(p.band).Border(1, p.line).Shadow(0, 8, 24, 0, shadow)
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
	return ui.Column(c).Background(p.card).Radius(12)
}

// cardTitle 是卡片里一节的标题。
func cardTitle(c *ui.Context, title string) ui.Element {
	return ui.Text(c, title).FontSize(13).FontWeight(600)
}

// band 是主区域顶上的一条栏：页面标题和操作，可以拖动窗口。
func band(c *ui.Context, fn func()) {
	p := colors(c.Theme())
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 14, 0, 20).Gap(10).Background(p.band).
		BorderWidth(0, 0, 1, 0).BorderColor(p.line).DragWindow().Children(fn)
}

// selectable 给列表里的一行加上选中和悬停的底；选中时左边有强调色竖条。
func selectable(c *ui.Context, row ui.Element, selected bool) ui.Element {
	t := c.Theme()
	p := colors(t)
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.tint
	case row.Hovered():
		bg = p.wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	if selected {
		accent := t.Accent
		row.Draw(func(pt *ui.Painter, r ui.Rect) {
			h := min(r.H-12, 18)
			pt.Fill(ui.Rect{X: r.X + 3, Y: r.Y + (r.H-h)/2, W: 3, H: h}, accent, 1.5)
		})
	}
	return row
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

// textButton 和 accentButton 是 mygo 的 Button、PrimaryButton，上内边距少 cjkLift、下内边距多 cjkLift。
func textButton(c *ui.Context, label string) ui.Element {
	return liftButton(c, ui.Button(c, label))
}

func accentButton(c *ui.Context, label string) ui.Element {
	return liftButton(c, ui.PrimaryButton(c, label))
}

func liftButton(c *ui.Context, b ui.Element) ui.Element {
	t := c.Theme()
	v, h := t.Space(1.5), t.Space(3.5) // mygo 按钮的默认内边距
	return b.Padding(v-cjkLift, h, v+cjkLift, h)
}

// segmented 是分段选择，选中的一段填强调色；返回这一帧是否换了选择。
func segmented(c *ui.Context, sel *int, label string, options ...string) bool {
	t := c.Theme()
	changed := false
	ui.Row(c).Padding(2).Gap(2).Radius(8).Background(t.Surface).Shrink(0).Label(label).Role(ui.RoleRadioGroup).Children(func() {
		for i, o := range options {
			// 被拉宽时（对话框里）各段平分宽度
			seg := ui.Box(c.Key(fmt.Sprint("seg-", i))).Height(24).Padding(0, 12).Grow(1).Radius(6).Center().Label(o).Role(ui.RoleRadio).Checked(i == *sel)
			bg, fg := ui.Transparent, t.TextMuted
			switch {
			case i == *sel:
				bg, fg = t.Accent, t.AccentText
			case seg.Hovered():
				bg, fg = t.SurfaceHover, t.Text
			}
			seg.Background(seg.AnimateColor("bg", bg, hover))
			seg.Children(func() {
				text := ui.Text(c, o).FontSize(12).TextColor(fg).Margin(0, 0, 2*cjkLift, 0)
				if i == *sel {
					text.FontWeight(600)
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
