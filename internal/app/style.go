package app

import (
	"cmp"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"posse/internal/mermaid/diagram"
	"posse/internal/term"
	"posse/internal/vt"
	"posse/locales"
)

// 界面的取向和字号、字重、图标、间距、圆角、颜色、层级的规则见 docs/design.md；这里只定义取值，调用处只用名字。

var hover = ui.Animation{Duration: 120 * time.Millisecond}

// iconSize 是图标按钮和侧栏图标的字号，设置页可以换档（iconSizes，记在 ui.json）；
// 标签页里和侧栏行尾的状态小一号（smallIcon），按钮框比图标大 12（buttonBox）。
// 只在界面线程读写：控件函数大多拿不到 app，所以是包级变量。
var iconSize float32 = defaultIconSize

// uiSmooth 是界面文字用不用系统的字体平滑（ui.json 的 uiSmooth）。默认不用：平滑让深色底上的浅色字重一档，
// 参照的 Claude 桌面端是网页，也不平滑。
var uiSmooth bool

const defaultIconSize = 16

var iconSizes = []struct {
	size  float32
	label func(locales.Messages) string
}{{14, locales.Messages.SettingsIconSmall}, {16, locales.Messages.SettingsIconNormal}, {18, locales.Messages.SettingsIconLarge}}

// 图标只用这几档：文字旁的图标和那行字一样大（fsSmall、fsBody）；工具按钮、列表行首是 iconSize，
// 按钮框 buttonBox；列表行内、卡片和浮层里的紧凑按钮是 smallIcon 配 compactBox，标签和文件行里的小按钮是 miniBox；
// agent 色块另有多行列表用的 tileMedium 和大图 tileLarge。
// 文字旁的跟着界面字号，其余跟着设置里的图标大小，不写数字（视频上的播放键这种单独的大图标除外）。
func smallIcon() float32  { return iconSize - 2 }
func buttonBox() float32  { return iconSize + 12 }
func compactBox() float32 { return iconSize + 8 }

// miniBox 是标签、文件行里的小按钮（关闭、继续），放 fsSmall 的图标。
func miniBox() float32 { return iconSize + 4 }

// tileMedium 是多行列表项（历史会话）行首的 agent 色块，tileLarge 是空状态、会话和任务详情页头上的大色块。
func tileMedium() float32 { return iconSize + 12 }
func tileLarge() float32  { return iconSize * 2.75 }

// 界面的字号和字重（设置 › 外观，ui.json 的 uiFontSize、uiWeight、uiBoldWeight），由 setUIType 从正文字号和
// 正文、粗体字重推出：字号相邻两档差 2，差得太少看不出层级；fwMedium 取正文和粗体中间。
// 和 iconSize 一样只在界面线程读写。
var (
	fsSmall float32 // 说明、计数、快捷键、路径、时间；正文里的等宽字也用它，等宽字自身显大
	fsBody  float32 // 正文、列表、按钮、输入框
	fsTitle float32 // 卡片、对话框标题
	fsPage  float32 // 页面标题、空状态
	fsCode  float32 // 作为一行主要内容的等宽字（分支名、版本号）：比正文小一号看起来才一样大

	fwBody, fwMedium, fwBold int
)

const (
	defaultUIFontSize = 14
	minUIFontSize     = 12
	maxUIFontSize     = 18
	// rowBase 是行高、按钮高这些尺寸当初对着调的正文字号，sz 照它按比例放大缩小。
	rowBase = 13
)

func init() { setUIType(defaultUIFontSize, 0, 0) }

// setUIType 换界面的字号和字重；weight、bold 为 0 时用默认（同 weightsOf）。
func setUIType(size float32, weight, bold int) {
	fsBody = min(max(cmp.Or(size, defaultUIFontSize), minUIFontSize), maxUIFontSize)
	fsSmall, fsCode, fsTitle, fsPage = typeScale(fsBody)
	fwBody, fwBold = weightsOf(weight, bold)
	fwMedium = int(math.Round(float64(fwBody+fwBold)/200)) * 100
}

// typeScale 由正文字号推出说明文字、等宽字、卡片标题和页面标题的字号；界面和 Markdown 共用这套差值。
func typeScale(body float32) (small, code, title, page float32) {
	return body - 2, body - 1, body + 2, body + 4
}

// sz 是跟着界面字号缩放的尺寸：行、按钮、输入框的高度和文字旁的小图标写成 sz(26)，默认字号下比原来大一点，配得上大一号的字。
func sz(v float32) float32 { return float32(math.Round(float64(v * fsBody / rowBase))) }

// 间距只用 2、4、6、8、12、16、20、24、32 这几档（spaceScale），不跟着字号变。常用的组合：
// 上下叠放的两行字隔 2，一行里几项隔 8；列表行外边距 6、左右内边距 8；卡片内边距 cardPad，浮层菜单 6；
// 树每层缩进 treeIndent。
var spaceScale = []float32{0, 2, 4, 6, 8, 12, 16, 20, 24, 32}

const (
	cardPad    = 16
	pageGap    = 12 // 卡片式页面（任务、历史、技能、MCP）四周、两栏之间、卡片之间都是它，三处一样宽才整齐
	treeIndent = 16
	hairline   = 0.5 // 分隔线的粗细：2 倍屏上一个物理像素，1pt 显粗；对齐时要让出它
	focusRing  = 1   // 聚焦框的粗细：要一眼看得见，不跟着分隔线变细
	guideWidth = 1   // 侧栏 worker 连线的粗细
	guideRoom  = 3   // 连线拐过来停在状态图形、agent 图标前留的空
	shadowDim  = 0.6 // 侧栏影子行（派到别的工作区的 worker）的不透明度
)

// 圆角按角色分五档，嵌套时外圆角 = 内圆角 + 内边距（规则见 docs/design.md）。
const (
	rSmall    = 4  // 小标签、小芯片、勾选框、小按钮
	rRow      = 6  // 列表行、菜单行，放在内边距 6 的容器里的行
	rControl  = 8  // 按钮、输入框、文件标签、侧栏行、面板里的内嵌块
	rCard     = 12 // 卡片、浮层、磁贴、代码块
	rComposer = 16 // 发送输入区
)

// full 是高（或宽）为 h 的圆点、胶囊、圆形按钮的圆角。
func full(h float32) float32 { return h / 2 }

// palette 是在系统主题之外 Posse 自己的颜色。颜色只在这里和 posseTheme、agentIcon 里定义，别处用名字（docs/design.md）。
type palette struct {
	honey  ui.Color   // 等你处理
	sage   ui.Color   // 本轮完成、新增
	sky    ui.Color   // 信息：传入的提交、MCP
	side   ui.Color   // 侧栏、窗口底
	canvas ui.Color   // 主区域的底，卡片放在上面
	card   ui.Color   // 卡片，和主题的 Background、终端底色一致
	band   ui.Color   // 顶栏
	raised ui.Color   // 浮层：比卡片再高一层
	shade  ui.Color   // 底栏：压暗一层
	code   ui.Color   // 代码块的底
	line   ui.Color   // 分隔线；深色下也是卡片、浮层的内高光
	guide  ui.Color   // 侧栏 worker 连线：分隔线叠在侧栏底上的不透明色，相邻两行的线接头处不叠深
	tint   ui.Color   // 选中项的底：强调色淡底（soft）
	mark   ui.Color   // 搜索命中的底
	markOn ui.Color   // 查找栏当前所在的那个命中
	wash   ui.Color   // 悬停的底
	fill   ui.Color   // 框里的次级按钮的底
	fillHi ui.Color   // 次级按钮悬停
	faint  ui.Color   // 最弱的文字：占位、禁用、不重要的计数
	shadow ui.Color   // 浮层、提示的投影
	scrim  ui.Color   // 压在图片、视频上的深色底
	data   []ui.Color // 数据色：提交图的线、分支比较的两边、用量的分类；依次取
}

func colors(t *ui.Theme) palette {
	if t.Dark {
		p := palette{
			honey: ui.Hex("#F2AA2E"), sage: ui.Hex("#8DBB92"),
			side: ui.Hex("#2A2A27"), canvas: ui.Hex("#171716"), card: t.Background, band: ui.Hex("#1C1C1B"), raised: ui.Hex("#2A2A27"),
			shade: ui.RGBA(0, 0, 0, 0.18), code: ui.Hex("#141413"),
			line: ui.RGBA(255, 255, 255, 0.08), wash: ui.RGBA(255, 255, 255, 0.05),
			fill: ui.RGBA(255, 255, 255, 0.09), fillHi: ui.RGBA(255, 255, 255, 0.14), faint: ui.Hex("#7D7B76"),
			shadow: ui.RGBA(0, 0, 0, 0.35), scrim: ui.RGBA(0, 0, 0, 0.55),
			data: darkData,
		}
		return p.derive(t)
	}
	p := palette{
		honey: ui.Hex("#B5700E"), sage: ui.Hex("#3E8A4C"),
		side: ui.Hex("#F3F2EF"), canvas: ui.Hex("#F4F3F0"), card: t.Background, band: ui.Hex("#F8F7F5"), raised: ui.Hex("#FFFFFF"),
		shade: ui.RGBA(0, 0, 0, 0.03), code: ui.Hex("#F5F4F1"),
		line: ui.RGBA(0, 0, 0, 0.08), wash: ui.RGBA(0, 0, 0, 0.04),
		fill: ui.RGBA(0, 0, 0, 0.06), fillHi: ui.RGBA(0, 0, 0, 0.1), faint: ui.Hex("#999793"),
		shadow: ui.RGBA(0, 0, 0, 0.08), scrim: ui.RGBA(0, 0, 0, 0.55),
		data: lightData,
	}
	return p.derive(t)
}

// 数据色依次是橙、蓝、绿、紫、金、青；浅色底上要深一档才看得清。colors 每帧调用很多次，切片只建一次。
var (
	darkData  = []ui.Color{ui.Hex("#D97757"), ui.Hex("#7AA2F7"), ui.Hex("#5FBFA0"), ui.Hex("#B36AD9"), ui.Hex("#D9A441"), ui.Hex("#4BB3B3")}
	lightData = []ui.Color{ui.Hex("#C15F3C"), ui.Hex("#3B6FD9"), ui.Hex("#21866A"), ui.Hex("#8E4BB8"), ui.Hex("#A9771A"), ui.Hex("#2B8A8A")}
)

// derive 补上由别的颜色推出来的几项。
func (p palette) derive(t *ui.Theme) palette {
	p.sky = p.data[1]
	p.guide = p.line.Over(p.side)
	p.tint = soft(t, t.Accent)
	p.mark = p.honey.Alpha(0.28)
	p.markOn = p.honey.Alpha(0.65)
	return p
}

// soft 是某个语义色的淡底（选中、命中、提醒、出错的底），深色下浓一点才看得出。
func soft(t *ui.Theme, c ui.Color) ui.Color {
	if t.Dark {
		return c.Alpha(0.2)
	}
	return c.Alpha(0.12)
}

// dataColor 是第 i 个数据色，超出时循环。
func dataColor(t *ui.Theme, i int) ui.Color {
	d := lightData
	if t.Dark {
		d = darkData
	}
	return d[i%len(d)]
}

// mermaidStyle 是 Markdown 里 Mermaid 图的配色和字体：画在代码块的底上，节点是中性的次级填充，
// 强调色只给 Pele 拿来标重点的地方（关键路径、高亮）。Pele 要不透明的颜色，半透明的都先压到底色上。
func mermaidStyle(t *ui.Theme) diagram.Style {
	p := colors(t)
	flat := func(c ui.Color) ui.Color { return c.Over(p.code) }
	return diagram.Style{
		Bg: p.code, Fg: flat(t.Text), Muted: flat(t.TextMuted), Line: flat(t.TextMuted),
		Surface: flat(p.fill), SurfaceAlt: flat(p.fillHi), Border: flat(p.faint), Accent: flat(t.Accent), Danger: flat(t.Danger),
		Mono: monoFont, Weight: fwBody, BoldWeight: fwBold, MonoWeight: fwBody,
	}
}

// white 和 black 是不随主题变的颜色：品牌色块、勾选框里的对勾、二维码的白底这类本来就固定的地方。
var (
	white = ui.Hex("#FFFFFF")
	black = ui.Hex("#000000")
)

var posseThemes [2]*ui.Theme

// posseTheme 在系统主题上换成暖灰、Claude 橙和 8px 圆角；卡片和终端一个颜色（Background）。
func posseTheme(dark bool) *ui.Theme {
	i := 0
	if dark {
		i = 1
	}
	if t := posseThemes[i]; t != nil {
		t.FlatText, t.FontSize, t.FontWeight = !uiSmooth, fsBody, fwBody
		return t
	}
	var t *ui.Theme
	if dark {
		t = ui.DarkTheme()
		t.Background = ui.Hex(term.DarkBackground)
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#2A2A27"), ui.Hex("#31312E"), ui.Hex("#383835")
		t.Border = ui.Hex("#3A3936")
		t.Text, t.TextMuted = ui.Hex(term.DarkForeground), ui.Hex("#B4B2AC")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#D97757"), ui.Hex("#E38C70"), ui.Hex("#C4643F")
		t.Selection, t.Focus = ui.RGBA(217, 119, 87, 0.35), ui.RGBA(217, 119, 87, 0.6)
	} else {
		t = ui.LightTheme()
		t.Background = ui.Hex(term.LightBackground)
		t.Surface, t.SurfaceHover, t.SurfacePressed = ui.Hex("#F3F2EF"), ui.Hex("#ECEBE7"), ui.Hex("#E4E2DD")
		t.Border = ui.Hex("#DCDAD5")
		t.Text, t.TextMuted = ui.Hex(term.LightForeground), ui.Hex("#6B6966")
		t.Accent, t.AccentHover, t.AccentPressed = ui.Hex("#C15F3C"), ui.Hex("#AD5333"), ui.Hex("#98482B")
		t.Selection, t.Focus = ui.RGBA(193, 95, 60, 0.22), ui.RGBA(193, 95, 60, 0.5)
	}
	t.Radius = rControl
	// 照 Orca：能点的地方是手形指针，拖动中是抓握的手
	t.PointerCursor = true
	t.FlatText, t.FontSize, t.FontWeight = !uiSmooth, fsBody, fwBody
	posseThemes[i] = t
	return t
}

// terminalThemes 让终端底色跟着 Posse 的主题，而不是 mygo 默认的底色。
func terminalThemes() (light, dark *terminal.Theme) {
	light, dark = terminal.LightTheme(), terminal.DarkTheme()
	light.Background, light.Foreground = posseTheme(false).Background, posseTheme(false).Text
	dark.Background, dark.Foreground = posseTheme(true).Background, posseTheme(true).Text
	// 16 色取 term 里的那份：常驻进程的内核和手机都照它
	for i := range 16 {
		light.Palette[i], dark.Palette[i] = ui.Hex(term.LightPalette[i]), ui.Hex(term.DarkPalette[i])
	}
	// 查找的命中和预览里的查找同一种蜂蜜色淡底，文字用正文色（插件默认是黄底黑字）
	for _, th := range []*terminal.Theme{light, dark} {
		pt := posseTheme(th == dark)
		p := colors(pt)
		th.Search, th.SearchSelected = p.mark, p.markOn
		th.SearchText, th.SearchSelectedText = pt.Text, pt.Text
	}
	return light, dark
}

// windowColors 是窗口终端在用的配色（前景、底色、16 色），报给常驻进程。
func windowColors(dark bool) term.Colors {
	light, darkTheme := terminalThemes()
	th := light
	if dark {
		th = darkTheme
	}
	rgb := func(c ui.Color) vt.RGB { return vt.RGB{R: c.R, G: c.G, B: c.B} }
	c := term.Colors{Foreground: rgb(th.Foreground), Background: rgb(th.Background)}
	for i, p := range th.Palette {
		c.Palette[i] = rgb(p)
	}
	return c
}

// floating 是浮在内容上面的卡片（右侧面板、链接卡片、图片预览）：顶栏底色、描边和投影，位置由调用方定。
// 深色下不画白框，用 1px 内高光（像边缘接到顶光）配深而散的投影；浅色下是细外环加投影。
// 出现时淡入并从 97% 放大到原大（160ms，只播一次，之后不再请求帧）；自己带动画的调用方（右侧面板）
// 之后再设 Opacity、Transform 会盖掉这里的。
func floating(c *ui.Context, e ui.Element) ui.Element {
	k := e.Keyframes("pop-in", popInFrames)
	e.Radius(rCard).Background(colors(c.Theme()).raised).Opacity(k).Scale(0.97+0.03*k, 0.97+0.03*k)
	return elevFloat(c, e)
}

var popInFrames = []ui.Keyframe{{At: 0, Value: 0}, {At: 160 * time.Millisecond, Value: 1, Ease: ui.EaseOut}}

// fieldInput 是放在框里（浮层、对话框、卡片）的单行输入：填充底、不加框，聚焦时描焦点色。
// 返回里面的输入框，调用方照常问 Submitted、Changed、Shortcut。
func fieldInput(c *ui.Context, value *string, placeholder string) ui.Element {
	return fieldInputWidth(c, value, placeholder, 0)
}

// fieldInputWidth 是定宽的 fieldInput，放在一行里用；width 为 0 时随父容器。
// 所有输入框都是这一种样子（填充底、不画框，聚焦时一圈强调色），和标签改名的输入框一致，
// 不用 mygo 自带的 TextInput、TextArea（白底灰框、聚焦时整条强调色框）。
func fieldInputWidth(c *ui.Context, value *string, placeholder string, width float32) ui.Element {
	return fieldInputSized(c, value, placeholder, width, width)
}

// fieldInputSized 平时 width 宽，一行放不下时最窄缩到 minWidth，让同一行里的标题不被挤得折行。
func fieldInputSized(c *ui.Context, value *string, placeholder string, width, minWidth float32) ui.Element {
	in, _ := fieldBox(c, value, placeholder, width, minWidth, false, nil)
	return in
}

// clearableField 是有内容时尾部带清空按钮的 fieldInput（检索框）；cleared 报告这一帧点了清空，
// 值已经清掉、焦点放回输入框，调用方照按 Esc 清空那样处理。
func clearableField(c *ui.Context, value *string, placeholder string) (in ui.Element, cleared bool) {
	return fieldBox(c, value, placeholder, 0, 0, true, nil)
}

// searchField 是 clearableField 在 × 后面再放几个常驻的小按钮（检索框的大小写、正则、筛选），trailing 画它们。
func searchField(c *ui.Context, value *string, placeholder string, trailing func()) (in ui.Element, cleared bool) {
	return fieldBox(c, value, placeholder, 0, 0, true, trailing)
}

func fieldBox(c *ui.Context, value *string, placeholder string, width, minWidth float32, clearable bool, trailing func()) (ui.Element, bool) {
	t := c.Theme()
	box := ui.Row(c).Height(sz(30)).Padding(0, 12).Radius(rControl).Background(colors(t).fill).AlignItems(ui.Center)
	if clearable {
		// 清空按钮自带留白，右边距收窄，× 和左边的文字离边一样远
		box.Padding(0, 4, 0, 12).Gap(2)
	}
	if width > 0 {
		box.Width(width)
		if minWidth < width {
			box.Shrink(1).MinWidth(minWidth)
		} else {
			box.Shrink(0)
		}
	}
	var in ui.Element
	cleared := false
	box.Children(func() {
		in = ui.TextInputBase(c, value).Placeholder(placeholder).Label(placeholder).FontSize(fsBody).Grow(1).Shrink(1).MinWidth(0)
		if clearable && *value != "" && iconButtonSized(c.Key("field-clear"), iconClose, L(c).FieldClear(), compactBox(), smallIcon()).Shrink(0).Clicked() {
			*value = ""
			cleared = true
			in.Focus()
		}
		if trailing != nil {
			trailing()
		}
	})
	// 边框算在高度里。聚焦才加上时框会高 2px，包着它的对话框跟着居中，整块跳一下。
	fieldBorder(box, in.Focused(), colors(t).fill, t.Focus)
	return in, cleared
}

// fieldArea 是多行的 fieldInput，height 是文字区的高度。
func fieldArea(c *ui.Context, value *string, placeholder string, height float32) ui.Element {
	t := c.Theme()
	box := ui.Column(c).Padding(8, 12).Radius(rControl).Background(colors(t).fill)
	var in ui.Element
	box.Children(func() {
		in = ui.TextAreaBase(c, value).Placeholder(placeholder).Label(placeholder).FontSize(fsBody).Height(height)
	})
	fieldBorder(box, in.Focused(), colors(t).fill, t.Focus)
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

// fadeIn 从左到右由透明渐变到 bgs 依次叠起来的底色（半透明的底要先垫下面那层），给浮在文字上的按钮做渐隐。
// 先合成一个不透明色再画一层渐变：逐层各画一遍渐变时，中段底层已压暗、上层还没盖回来，会凹出一块暗色。
func fadeIn(p *ui.Painter, r ui.Rect, bgs ...ui.Color) {
	if len(bgs) == 0 {
		return
	}
	bg := bgs[0]
	for _, c := range bgs[1:] {
		bg = c.Over(bg)
	}
	p.FillGradient(r, ui.LinearGradient{From: bg.Alpha(0), To: bg, Angle: 90}, 0)
}

// 层级只有三档（docs/design.md），投影和描边都经这几个函数，别处不写 Shadow、InsetShadow。
// 深色下不画白框，用 1px 内高光（像边缘接到顶光）；浅色下是细外环加投影。

// elevCard 是卡片：不描边、不投影，靠和画布（canvas）的明度差分开。留着这个函数，卡片的层级规则只在一处。
func elevCard(c *ui.Context, e ui.Element) ui.Element { return e }

// elevFloat 是弹出来、盖在内容上面的东西：浮层和提示。只有它带投影，小而淡，够把它从底下分开就行。
func elevFloat(c *ui.Context, e ui.Element) ui.Element {
	p := colors(c.Theme())
	if c.Theme().Dark {
		return e.InsetShadow(0, 0, 0, hairline, p.line).Shadow(0, 6, 20, 0, p.shadow)
	}
	return e.Border(hairline, p.line).Shadow(0, 4, 16, 0, p.shadow)
}

// elevSheet 是从窗口底边升起的面板（Git 日志）：顶边一条线，不投影。
func elevSheet(c *ui.Context, e ui.Element) ui.Element {
	return e.BorderWidth(hairline, 0, 0, 0).BorderColor(colors(c.Theme()).line)
}

// hline、vline 是兄弟元素之间的分隔线；容器自己的上下左右边界用 BorderWidth(hairline, …)。
// 列表、菜单里两组之间的线左右缩 6、上下留 4（Margin(4, 6)），和列表行的外边距对齐。
func hline(c *ui.Context) ui.Element {
	return ui.Box(c).Height(hairline).Shrink(0).Background(colors(c.Theme()).line)
}

func vline(c *ui.Context) ui.Element {
	return ui.Box(c).Width(hairline).Shrink(0).Background(colors(c.Theme()).line)
}

// composer 是一体的输入区：圆角框里是输入，右下角是发送按钮，⌘↩ 也能发送；聚焦时描边换成焦点色。
// 它自己就是框，外面不再套卡片。返回这一帧是否要发送（内容不为空）。
func composer(c *ui.Context, value *string, placeholder, verb string, multiline bool) bool {
	t := c.Theme()
	box := ui.Column(c).Radius(rComposer).Background(colors(t).card).Padding(12, 12, 12, 16).Gap(6)
	sent := false
	var in ui.Element
	box.Children(func() {
		if multiline {
			in = ui.TextAreaBase(c, value).Placeholder(placeholder).Label(placeholder).Height(sz(64)).FontSize(fsBody)
		} else {
			in = ui.TextInputBase(c, value).Placeholder(placeholder).Label(placeholder).FontSize(fsBody).Padding(4, 0)
			sent = in.Submitted()
			// 单行输入框自己不用 ↑↓，没人接时会落到页面上（任务页拿它换任务）：在框里打字时按到不该换走页面
			in.Shortcut(0, ui.KeyUp)
			in.Shortcut(0, ui.KeyDown)
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
	composerFocus(c, box, in.Focused())
	return sent && strings.TrimSpace(*value) != ""
}

// composerFocus 画发送输入区的聚焦框；讨论页自己排输入区时也用它。
func composerFocus(c *ui.Context, box ui.Element, focused bool) {
	t := c.Theme()
	if focused {
		// 外框和平时一样宽（不挤动内容），再往里描一圈，合起来是 focusRing
		box.Border(hairline, t.Focus).InsetShadow(0, 0, 0, focusRing-hairline, t.Focus)
	} else {
		// 平时画一圈和底同色、看不见的框，和聚焦时一样大
		elevCard(c, box).Border(hairline, colors(t).card)
	}
}

// fieldBorder 画聚焦框。没聚焦时用和底一样的颜色，看着没有框，尺寸却和聚焦时相同。
func fieldBorder(box ui.Element, focused bool, fill, focus ui.Color) {
	color := fill
	if focused {
		color = focus
	}
	box.Border(focusRing, color)
}

// pendingLabel 是"待处理"三个字：界面上唯一用琥珀色的文字。
func pendingLabel(c *ui.Context) ui.Element {
	return ui.Text(c, L(c).TermPending()).FontSize(fsSmall).FontWeight(fwBold).TextColor(colors(c.Theme()).honey)
}

// doneLabel 是"已完成"三个字，和"待处理"一样是要你看一眼的状态，用强调色。
func doneLabel(c *ui.Context) ui.Element {
	return ui.Text(c, L(c).TermDone()).FontSize(fsSmall).FontWeight(fwBold).TextColor(c.Theme().Accent)
}

// isWorktree 报告工作区是不是 git worktree：posse 建的（worktree），或者从别处加进来的（attach 记成 folder，
// 但目录不是项目目录）。
func (a *app) isWorktree(w *workspace) bool {
	if w.Kind == "worktree" {
		return true
	}
	if w.Kind != "folder" {
		return false
	}
	ws := a.byProject[w.ProjectID]
	return len(ws) > 0 && ws[0].Kind == "main" && filepath.Clean(ws[0].Path) != filepath.Clean(w.Path)
}

// wsIcon 是侧栏、空工作区页里工作区的图标：worktree（含加进来的）是分支，其余是文件夹。
func (a *app) wsIcon(w *workspace) *ui.SVG {
	if a.isWorktree(w) {
		return iconBranch
	}
	return iconFolder
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
	if homeDir != "" && (p == homeDir || strings.HasPrefix(p, homeDir+"/")) {
		return "~" + p[len(homeDir):]
	}
	return p
}

// card 是放在画布上的一块内容。
func card(c *ui.Context) ui.Element {
	p := colors(c.Theme())
	return elevCard(c, ui.Column(c).Background(p.card).Radius(rCard))
}

// emptyCard 是页面没有内容时居中的说明卡片：只靠底色和画布分开，不描边。
func emptyCard(c *ui.Context) ui.Element {
	return ui.Column(c).Background(colors(c.Theme()).card).Radius(rCard)
}

// titleText 是页面、对话框和卡片的标题，用 600 的字重。
func titleText(c *ui.Context, text string, size float32) ui.Element {
	return ui.Text(c, text).FontSize(size).FontWeight(fwBold)
}

// cardTitle 是卡片里一节的标题。
func cardTitle(c *ui.Context, title string) ui.Element {
	return titleText(c, title, fsBody)
}

// band 是主区域顶上的一条栏：页面标题和操作，可以拖动窗口。
func (a *app) band(c *ui.Context, fn func()) {
	p := colors(c.Theme())
	ui.Row(c).Height(titleHeight(c)).Shrink(0).Padding(0, 16, 0, 20+a.barInset(c)).Gap(8).Background(p.band).
		BorderWidth(0, 0, hairline, 0).BorderColor(p.line).DragWindow().Children(fn)
}

// selectedBg 给列表里的一行加选中和悬停的底：选中是强调色淡底，不画竖条。
func selectedBg(c *ui.Context, row ui.Element, selected bool) ui.Element {
	return row.Background(selectedColor(c, row, selected))
}

// selectedColor 是 selectedBg 这一帧的底色（含动画），给要自己画底色的行用。
func selectedColor(c *ui.Context, row ui.Element, selected bool) ui.Color {
	p := colors(c.Theme())
	bg := ui.Transparent
	switch {
	case selected:
		bg = p.tint
	case row.Hovered():
		bg = p.wash
	}
	return row.AnimateColor("bg", bg, hover)
}

// tile 是带底色的方形图标，用在卡片标题旁边。
func tile(c *ui.Context, icon *ui.SVG, size float32, bg, fg ui.Color) ui.Element {
	return ui.Box(c).Size(size, size).Radius(size * 0.26).Background(bg).Center().Shrink(0).Children(func() {
		ui.Icon(c, icon).FontSize(size * 0.52).TextColor(fg)
	})
}

// agentTile 是 agent 的色块图标。
func agentTile(c *ui.Context, agent string, size float32) ui.Element {
	return agentIcon(c, agent, size).Tooltip(agentTitle(agent))
}

// agentIcon 是不带悬停提示的 agentTile，用在旁边已经写着名字或用不着提示的地方。
func agentIcon(c *ui.Context, agent string, size float32) ui.Element {
	t := c.Theme()
	icon, bg, fg := iconTerminal, t.SurfacePressed, t.Text
	switch agent {
	case "claude":
		icon, bg, fg = iconClaude, ui.Hex("#D97757"), white
	case "codex":
		icon, bg, fg = iconCodex, white, black // ChatGPT 应用图标的白底黑标
	case "grok":
		icon, bg, fg = iconGrok, t.Text, t.Background
	case "cursor":
		icon, bg, fg = iconCursor, ui.Hex("#6E56CF"), white
	}
	e := tile(c, icon, size, bg, fg)
	if agent == "codex" {
		// 白底在浅色卡片上会融掉，描一圈细边
		e.InsetShadow(0, 0, 0, hairline, black.Alpha(0.12))
	}
	return e
}

// cjkLift 把按钮里的文字往上提：中文字形来自苹方回退，在系统字体的行框里偏低，按行框居中看起来下沉约 1pt。
const cjkLift = 1

// textButton 是次级按钮：无边框的填充底。它常放在卡片、对话框这些有框的容器里，再加框就是框里套框。
func textButton(c *ui.Context, label string) ui.Element {
	t := c.Theme()
	b := ui.ButtonBase(c).Height(sz(28)).Padding(0, 12).Radius(rControl).Label(label).Shrink(0)
	bg := colors(t).fill
	if b.Hovered() {
		bg = colors(t).fillHi
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Text(c, label).FontSize(fsBody).FontWeight(fwMedium).Margin(0, 0, 2*cjkLift, 0).SingleLine()
	})
	return b
}

// accentButton 是主按钮（mygo 的 PrimaryButton），上内边距少 cjkLift、下内边距多 cjkLift；一屏里只放一个。
func accentButton(c *ui.Context, label string) ui.Element {
	return liftButton(c, ui.PrimaryButton(c, label)).Radius(rControl)
}

func liftButton(c *ui.Context, b ui.Element) ui.Element {
	t := c.Theme()
	v, h := t.Space(1.5), t.Space(3.5) // mygo 按钮的默认内边距
	return b.Padding(v-cjkLift, h, v+cjkLift, h)
}

// segmented 是分段选择：轨道是一层淡底，选中的一段填强调色；返回这一帧是否换了选择。
func segmented(c *ui.Context, sel *int, label string, options ...string) bool {
	return segmentedPad(c, sel, label, nil, 12, options...)
}

// segmentedTight 同 segmented，每段两侧的留白窄一些：面板宽度里要放下四个页签（命令速查）。
func segmentedTight(c *ui.Context, sel *int, label string, options ...string) bool {
	return segmentedPad(c, sel, label, nil, 6, options...)
}

// segmentedBadges 是带计数的分段：badges[i] 大于 0 时在第 i 段文字后面画强调色的数（文件栏"改动"的改动文件数）。
func segmentedBadges(c *ui.Context, sel *int, label string, badges []int, options ...string) bool {
	return segmentedPad(c, sel, label, badges, 12, options...)
}

// segmentedPad 是分段的实现，pad 是每段两侧的留白。
func segmentedPad(c *ui.Context, sel *int, label string, badges []int, pad float32, options ...string) bool {
	t := c.Theme()
	p := colors(t)
	changed := false
	ui.Row(c).Padding(2).Gap(2).Radius(rControl).Background(p.fill).Shrink(0).Label(label).Role(ui.RoleRadioGroup).Children(func() {
		for i, o := range options {
			// 被拉宽时（对话框里）各段平分多出来的宽度；Grow 会把起始宽度设成 0，
			// 字长的一段（"只建工作区"）要按字宽给够，不然被截成省略号
			tw, _ := c.MeasureText(0, ui.Span{Text: o, Size: fsSmall})
			n := 0
			if i < len(badges) {
				n = badges[i]
			}
			name := o
			if n > 0 {
				bw, _ := c.MeasureText(0, ui.Span{Text: fmt.Sprint(n), Size: fsSmall})
				tw += max(bw+10, 16) + 5
				name = fmt.Sprintf("%s %d", o, n)
			}
			seg := ui.Box(c.Key(fmt.Sprint("seg-", i))).Height(sz(24)).Padding(0, pad).Grow(1).Shrink(0).MinWidth(tw + 2*pad + 2).Radius(rRow).Center().Label(name).Role(ui.RoleRadio).Checked(i == *sel)
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
				ui.Row(c).AlignItems(ui.Center).Children(func() {
					text := ui.Text(c, o).FontSize(fsSmall).TextColor(fg).Margin(0, 0, 2*cjkLift, 0).Shrink(0).SingleLine()
					if i == *sel {
						text.FontWeight(fwMedium)
					}
					if n > 0 {
						ui.Box(c).Height(16).MinWidth(16).Padding(0, 4).Margin(0, 0, 0, 4).Radius(full(16)).Center().Shrink(0).
							Background(colors(t).tint).Children(func() {
							ui.Text(c, fmt.Sprint(n)).FontSize(fsSmall).FontWeight(fwMedium).FontFeatures("tnum").TextColor(t.Accent).SingleLine()
						})
					}
				})
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
	ui.Row(c).Gap(12).Wrap().Children(func() {
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
		ui.Text(c, text).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted).MaxLines(1).Shrink(0)
	}
}

// metaMono 是 metaRow 里的路径、分支这类标识符。
func metaMono(c *ui.Context, icon *ui.SVG, text, tip string) func() {
	return func() {
		t := c.Theme()
		ui.Row(c).Gap(4).Shrink(1).Children(func() {
			if icon != nil {
				ui.Icon(c, icon).FontSize(fsBody).TextColor(t.TextMuted)
			}
			e := ui.Text(c, text).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			if tip != "" {
				e.Tooltip(tip)
			}
		})
	}
}
