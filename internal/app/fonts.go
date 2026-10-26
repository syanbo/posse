package app

import (
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
)

// 终端和编辑器各有字体、字号，存在 ui.json。编辑器指文件标签里的代码视图：编辑器、diff、逐行追溯、
// Git 日志里的 diff、改动卡片。字体为空表示默认（termFontFamily）。

// defaultEditorFontSize 比终端默认小一号：同样的字号下编辑器显得比终端挤。
const defaultEditorFontSize = defaultFontSize - 1

// fontStack 把用户选的字体接上默认字体栈当回退：提示符里的分支等图标只有 Nerd Font 有，
// 中文回退到苹方的理由见 termFontFamily。
func fontStack(family string) string {
	if family == "" {
		return termFontFamily
	}
	return family + ", " + termFontFamily
}

func (a *app) termFont() terminal.Font {
	return terminal.Font{Family: fontStack(a.termFamily), Size: a.fontSize}
}

func (a *app) editorFont() ui.Font {
	return ui.Font{Family: fontStack(a.editorFamily), Size: a.editorSize}
}

// editorSizeOf 是 ui.json 里的编辑器字号；旧版没有这一项，编辑器跟着终端小一号，升级后字号不变。
func editorSizeOf(s uiState) float32 {
	switch {
	case s.EditorFontSize > 0:
		return s.EditorFontSize
	case s.FontSize > 0:
		return max(s.FontSize-1, minFontSize)
	}
	return defaultEditorFontSize
}

func (a *app) setTermFamily(family string) {
	a.termFamily = family
	if a.view != nil {
		a.view.term.SetFont(a.termFont())
	}
	a.state.TermFont = family
	a.saveState()
}

func (a *app) setEditorFamily(family string) {
	a.editorFamily = family
	a.state.EditorFont = family
	a.saveState()
}

func (a *app) setEditorFontSize(size float32) {
	a.editorSize = min(max(size, minFontSize), maxFontSize)
	if a.state.EditorFontSize != a.editorSize {
		a.state.EditorFontSize = a.editorSize
		a.saveState()
	}
}

// systemFonts 是系统里的字体，第一次打开字体选择时在后台列出（字体多时要几十毫秒），之后不再重列：
// 运行中新装的字体重开窗口后才有。
var systemFonts struct {
	once   sync.Once
	mu     sync.Mutex
	list   []ui.FontFamily
	labels map[string]string // 字体名 → 用户语言下的名字（"PingFang SC" 是"苹方-简"），和原名一样的不记
	done   bool
}

// loadFonts 返回系统字体和是否已经列完；没列完时在后台开始列，列完重建界面。
func (a *app) loadFonts() ([]ui.FontFamily, map[string]string, bool) {
	systemFonts.once.Do(func() {
		go func() {
			list := ui.FontFamilies()
			labels := map[string]string{}
			for _, f := range list {
				if l := localizedFamily(f.Name); l != f.Name {
					labels[f.Name] = l
				}
			}
			systemFonts.mu.Lock()
			systemFonts.list, systemFonts.labels, systemFonts.done = list, labels, true
			systemFonts.mu.Unlock()
			if a.win != nil {
				a.win.Update(func() {})
			}
		}()
	})
	systemFonts.mu.Lock()
	defer systemFonts.mu.Unlock()
	return systemFonts.list, systemFonts.labels, systemFonts.done
}

// fontLabel 是字体在列表和按钮上显示的名字：有本地化的名字就用它，空是默认。
func fontLabel(family string, labels map[string]string) string {
	if family == "" {
		return "默认（JetBrains Mono）"
	}
	if l := labels[family]; l != "" {
		return l
	}
	return family
}

// fontPick 是一个字体选择按钮弹出的列表的状态。
type fontPick struct {
	open  bool
	query string
	hl    int // 方向键高亮的一行（fontRows 的下标），-1 是打开时还没定位到当前字体
	list  ui.ListState
}

// fontRow 是字体列表的一行：分组标题（header 不为空）或一个字体，family 为空是"默认"；label 是显示的名字。
type fontRow struct {
	header, family, label string
}

// fontRows 照 Orca 列出系统的全部字体，不只是等宽的：等宽的在前面一组，其余在"其他字体"下。
// 中文字体显示中文名，按显示的名字排序，原名和中文名都能搜。
// 搜索时不列"默认"，第一个匹配的字体就是高亮的一行，回车直接选它。
func fontRows(all []ui.FontFamily, labels map[string]string, query string) []fontRow {
	q := strings.ToLower(strings.TrimSpace(query))
	var mono, other []fontRow
	for _, f := range all {
		label := fontLabel(f.Name, labels)
		if q != "" && !strings.Contains(strings.ToLower(f.Name), q) && !strings.Contains(strings.ToLower(label), q) {
			continue
		}
		r := fontRow{family: f.Name, label: label}
		if f.Monospaced {
			mono = append(mono, r)
		} else {
			other = append(other, r)
		}
	}
	byLabel := func(a, b fontRow) int { return strings.Compare(strings.ToLower(a.label), strings.ToLower(b.label)) }
	slices.SortStableFunc(mono, byLabel)
	slices.SortStableFunc(other, byLabel)
	var rows []fontRow
	if q == "" {
		rows = append(rows, fontRow{label: fontLabel("", nil)})
	}
	if len(mono) > 0 {
		rows = append(append(rows, fontRow{header: "等宽字体"}), mono...)
	}
	if len(other) > 0 {
		rows = append(append(rows, fontRow{header: "其他字体"}), other...)
	}
	return rows
}

// nextFont 是 rows 里从 i 起往 d 方向（含 i）的第一个字体行，没有时原样返回 i。
func nextFont(rows []fontRow, i, d int) int {
	for k := i; k >= 0 && k < len(rows); k += d {
		if rows[k].header == "" {
			return k
		}
	}
	return i
}

// fontPicker 是设置页里选字体的按钮：显示当前字体，点开是可搜索的字体列表，每项用它自己的字体写名字。
func (a *app) fontPicker(c *ui.Context, p *fontPick, current, label string, pick func(family string)) {
	t := c.Theme()
	_, labels, _ := a.loadFonts()
	name := fontLabel(current, labels)
	b := ui.ButtonBase(c).Height(28).Width(240).Padding(0, 8, 0, 10).Gap(6).Radius(rButton).Label(label).AlignItems(ui.Center)
	bg := colors(t).fill
	if b.Hovered() {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Text(c, name).FontSize(13).Font(fontStack(current)).SingleLine().Grow(1).Shrink(1)
		ui.Icon(c, iconDown).FontSize(11).TextColor(t.TextMuted).Shrink(0)
	})
	if pressable(b).Clicked() {
		p.open = !p.open
		p.query, p.hl = "", -1
	}
	if !p.open {
		return
	}
	ui.PopoverBase(c, b, &p.open, func(panel ui.Element) {
		floating(c, panel).Width(300).Padding(6).Gap(4)
		panel.Children(func() { a.fontList(c, p, current, pick) })
	})
}

const fontRowHeight = 28

func (a *app) fontList(c *ui.Context, p *fontPick, current string, pick func(family string)) {
	t := c.Theme()
	all, labels, done := a.loadFonts()
	// 先画搜索框：它在这一帧就改写 p.query，下面按新的搜索词列出来。最后一个字和回车常落在同一帧，
	// 按旧词列的话回车会落在分组标题上
	in := fieldInput(c, &p.query, "搜索字体").AutoFocus()
	rows := fontRows(all, labels, p.query)
	if !done {
		rows = rows[:min(len(rows), 1)]
	}
	switch {
	case in.Changed():
		p.hl = 0
		p.list.ScrollTo(0, ui.Start)
	case p.hl < 0 && done:
		// 打开时高亮当前的字体，滚到列表中间
		p.hl = 0
		for i, r := range rows {
			if r.header == "" && r.family == current {
				p.hl = i
			}
		}
		p.list.ScrollTo(p.hl, ui.Center)
	}
	if done {
		p.hl = nextFont(rows, min(max(p.hl, 0), max(len(rows)-1, 0)), 1)
	}
	choose := func(f string) {
		p.open = false
		if f != current {
			pick(f)
		}
	}
	move := func(d int) {
		if i := nextFont(rows, p.hl+d, d); i >= 0 && i < len(rows) && rows[i].header == "" {
			p.hl = i
			p.list.ScrollIntoView(i)
		}
	}
	switch {
	case in.Shortcut(0, ui.KeyDown):
		move(1)
	case in.Shortcut(0, ui.KeyUp):
		move(-1)
	case in.Shortcut(0, ui.KeyEscape):
		p.open = false
	case in.Submitted() && p.hl >= 0 && p.hl < len(rows) && rows[p.hl].header == "":
		choose(rows[p.hl].family)
	}
	switch {
	case !done:
		ui.Text(c, "正在读取字体…").FontSize(12).TextColor(t.TextMuted).Padding(6, 10)
	case len(rows) == 0:
		ui.Text(c, "没有名字里含“"+strings.TrimSpace(p.query)+"”的字体").FontSize(12).TextColor(t.TextMuted).Padding(6, 10)
	}
	if len(rows) == 0 {
		return
	}
	// 两百多个字体每个都要加载，只画看得见的几行（ui.List）；列表要有确定的高度才能只画看得见的
	p.list.Key = func(i int) any { return rows[i] }
	ui.List(c, &p.list, len(rows), func(i int) {
		r := rows[i]
		if r.header != "" {
			ui.Row(c).Height(fontRowHeight).Padding(6, 10, 0, 10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, r.header).FontSize(fsCaption).FontWeight(500).TextColor(t.TextMuted)
			})
			return
		}
		label := r.label
		row := ui.Row(c).Height(fontRowHeight).Padding(0, 10, 0, 8).Gap(8).Radius(rButton).AlignItems(ui.Center).
			Role(ui.RoleMenuItem).Label(label)
		bg := ui.Transparent
		if row.Hovered() || i == p.hl {
			bg = colors(t).wash
		}
		row.Background(row.AnimateColor("bg", bg, hover))
		row.Children(func() {
			box := ui.Box(c).Width(14).Shrink(0).Center()
			if r.family == current {
				box.Children(func() { ui.Icon(c, iconCheck).FontSize(14).TextColor(t.Accent) })
			}
			ui.Text(c, label).FontSize(13).Font(fontStack(r.family)).SingleLine().Grow(1).Shrink(1)
		})
		if row.Clicked() {
			choose(r.family)
		}
	}).Height(min(float32(len(rows))*fontRowHeight, 320)).Shrink(0)
}

// fontSizeStepper 是字号的"− 13 + 默认"一组按钮。
func fontSizeStepper(c *ui.Context, size, def float32, set func(float32)) {
	if pressable(textButton(c, "−")).Label("缩小字号").Clicked() {
		set(size - 1)
	}
	ui.Text(c, fmt.Sprintf("%g", size)).FontSize(13).FontFeatures("tnum").Width(32).Center()
	if pressable(textButton(c, "+")).Label("放大字号").Clicked() {
		set(size + 1)
	}
	if pressable(textButton(c, "默认")).Clicked() {
		set(def)
	}
}

// previewBlock 是设置卡片底部的预览：上面一条细线和"预览"二字，内容直接放在卡片底色上，
// 卡片和终端、编辑器同一个底色，看到的就是实际的样子。
func previewBlock(c *ui.Context, body func()) {
	t := c.Theme()
	ui.Column(c).Gap(10).Padding(14, 0, 0, 0).Margin(4, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(colors(t).line).Children(func() {
		ui.Text(c, "预览").FontSize(fsCaption).FontWeight(500).TextColor(t.TextMuted)
		body()
	})
}

// termPreview 照终端的配色画一段提示符、ls 和中英混排：分支图标来自 Nerd Font，看得出选的字体缺不缺图标。
func (a *app) termPreview(c *ui.Context) {
	t := c.Theme()
	light, dark := terminalThemes()
	th := light
	if t.Dark {
		th = dark
	}
	font, size := fontStack(a.termFamily), a.fontSize
	span := func(text string, color ui.Color) ui.Span {
		return ui.Span{Text: text, Font: font, Size: size, Color: color}
	}
	fg, pal := th.Foreground, th.Palette
	previewBlock(c, func() {
		ui.RichText(c,
			span("~/posse", pal[6]), span(" on ", fg), span(" main", pal[5]), span(" [!]\n", pal[1]),
			span("❯ ", pal[2]), span("ls\n", fg),
			span("cmd  docs  internal  ", pal[4]), span("go.mod  README.md\n", fg),
			span("❯ ", pal[2]), span("echo \"中文 English 123\"\n", fg),
			span("中文 English 123\n", fg),
			span("0O 1lI {}[] => != ≥ ", fg), span("█", fg),
		).LineHeight(1.25).Selectable()
	})
}

const editorPreviewText = `// greet 返回问候语：预览编辑器的字体和字号
func greet(name string) string {
	if name == "" {
		return "你好，世界"
	}
	return fmt.Sprintf("hi %s => %d", name, len(name))
}`

// editorPreview 是一个只读的真编辑器，高亮、行号、行高都和文件标签里一样。
func (a *app) editorPreview(c *ui.Context) {
	if a.editorDemo == nil {
		a.editorDemo = &editor.State{Buffer: editor.NewBuffer(editorPreviewText), ReadOnly: true, FitContent: true,
			NoActiveLine: true, NoFolding: true, NoScrollPastEnd: true, Label: "编辑器预览"}
		a.editorDemo.Buffer.SetLanguage(editor.LanguageFor("preview.go"))
	}
	a.editorDemo.Font = a.editorFont()
	previewBlock(c, func() {
		editor.View(c.Key("editor-preview"), a.editorDemo)
	})
}
