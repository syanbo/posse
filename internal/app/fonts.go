package app

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/locales"
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

// 字重、行高的默认值和范围。终端的行高是字体自身行高的倍数（Ghostty 的 adjust-cell-height），
// 编辑器的是字号的倍数，两者默认值不同。
const (
	defaultWeight           = 400
	minWeight, maxWeight    = 100, 900
	defaultTermLineHeight   = 1
	maxTermLineHeight       = 2
	defaultEditorLineHeight = 1.5
	maxEditorLineHeight     = 2.5
)

func (a *app) termFont() terminal.Font {
	s := a.state
	weight, bold := weightsOf(s.TermWeight, s.TermBoldWeight)
	return terminal.Font{Family: fontStack(a.termFamily), Size: a.fontSize, Weight: weight, BoldWeight: bold,
		LineHeight: s.TermLineHeight, Antialiased: !s.TermSmooth}
}

func (a *app) editorFont() ui.Font {
	weight, _ := weightsOf(a.state.EditorWeight, 0)
	return ui.Font{Family: fontStack(a.editorFamily), Size: a.editorSize, Weight: weight, Antialiased: !a.state.EditorSmooth}
}

// editorBoldWeight 是编辑器里 Markdown 标题、加粗用的字重（editor.State.BoldWeight）。
func (a *app) editorBoldWeight() int {
	_, bold := weightsOf(a.state.EditorWeight, a.state.EditorBoldWeight)
	return bold
}

// weightsOf 是 ui.json 里存的正文、粗体字重（0 是默认）实际用的值：没设时粗体比正文重 200、至少 600。
// 终端插件和编辑器默认的 700 显得太重。
func weightsOf(weight, bold int) (int, int) {
	weight = cmp.Or(weight, defaultWeight)
	return weight, cmp.Or(bold, min(max(weight+200, 600), maxWeight))
}

func termLineHeightOf(s uiState) float32 { return cmp.Or(s.TermLineHeight, defaultTermLineHeight) }

// stepWeight 把字重加减 100，限制在 100 到 900。
func stepWeight(w, d int) int { return min(max(w+d*100, minWeight), maxWeight) }

// stepLineHeight 把行高加减 0.1，取一位小数，限制在 [1, hi]。
func stepLineHeight(h float32, d int, hi float32) float32 {
	return min(max(float32(math.Round(float64(h)*10+float64(d))/10), 1), hi)
}

// setTermStyle 改终端的字重或行高（f 改 a.state），马上换到接入的终端上。
func (a *app) setTermStyle(f func(s *uiState)) {
	f(&a.state)
	a.eachPane(func(v *termView) { v.term.SetFont(a.termFont()) })
	a.saveState()
}

func (a *app) copyOnSelect() terminal.CopyOnSelect {
	if a.state.TermCopyOnSelect {
		return terminal.CopyOnSelectClipboard
	}
	return terminal.CopyOnSelectNone
}

func (a *app) setCopyOnSelect(on bool) {
	a.state.TermCopyOnSelect = on
	a.eachPane(func(v *termView) { v.term.SetCopyOnSelect(a.copyOnSelect()) })
	a.saveState()
}

func (a *app) setEditorStyle(f func(s *uiState)) {
	f(&a.state)
	a.saveState()
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
	a.eachPane(func(v *termView) { v.term.SetFont(a.termFont()) })
	a.state.TermFont = family
	a.saveState()
}

func (a *app) setEditorFamily(family string) {
	a.editorFamily = family
	a.state.EditorFont = family
	a.saveState()
}

// mdSize 是 Markdown 预览、历史页对话和任务说明的正文字号，就是编辑器字号。
func (a *app) mdSize() float32 { return a.editorSize }

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
func fontLabel(m locales.Messages, family string, labels map[string]string) string {
	if family == "" {
		return m.FontsDefault()
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
func fontRows(m locales.Messages, all []ui.FontFamily, labels map[string]string, query string) []fontRow {
	q := strings.ToLower(strings.TrimSpace(query))
	var mono, other []fontRow
	for _, f := range all {
		label := fontLabel(m, f.Name, labels)
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
		rows = append(rows, fontRow{label: fontLabel(m, "", nil)})
	}
	if len(mono) > 0 {
		rows = append(append(rows, fontRow{header: m.FontsMono()}), mono...)
	}
	if len(other) > 0 {
		rows = append(append(rows, fontRow{header: m.FontsOther()}), other...)
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
	name := fontLabel(L(c), current, labels)
	b := ui.ButtonBase(c).Height(sz(28)).Width(240).Padding(0, 8, 0, 12).Gap(6).Radius(rControl).Label(label).AlignItems(ui.Center)
	bg := colors(t).fill
	if b.Hovered() {
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() {
		ui.Text(c, name).FontSize(fsBody).Font(fontStack(current)).SingleLine().Grow(1).Shrink(1)
		ui.Icon(c, iconDown).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
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

// fontRowHeight 是字体列表一行的高度，跟着界面字号缩放。
func fontRowHeight() float32 { return sz(28) }

func (a *app) fontList(c *ui.Context, p *fontPick, current string, pick func(family string)) {
	t, m := c.Theme(), L(c)
	all, labels, done := a.loadFonts()
	// 先画搜索框：它在这一帧就改写 p.query，下面按新的搜索词列出来。最后一个字和回车常落在同一帧，
	// 按旧词列的话回车会落在分组标题上
	in := fieldInput(c, &p.query, m.FontsSearch()).AutoFocus()
	rows := fontRows(m, all, labels, p.query)
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
		ui.Text(c, m.FontsLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 12)
	case len(rows) == 0:
		ui.Text(c, m.FontsNoMatch(strings.TrimSpace(p.query))).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 12)
	}
	if len(rows) == 0 {
		return
	}
	// 两百多个字体每个都要加载，只画看得见的几行（ui.List）；列表要有确定的高度才能只画看得见的
	p.list.Key = func(i int) any { return rows[i] }
	ui.List(c, &p.list, len(rows), func(i int) {
		r := rows[i]
		if r.header != "" {
			ui.Row(c).Height(fontRowHeight()).Padding(6, 12, 0, 12).AlignItems(ui.Center).Children(func() {
				ui.Text(c, r.header).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			})
			return
		}
		label := r.label
		row := ui.Row(c).Height(fontRowHeight()).Margin(2, 0, 0, 0).Padding(0, 12, 0, 8).Gap(8).Radius(rRow).AlignItems(ui.Center).
			Role(ui.RoleMenuItem).Label(label)
		bg := ui.Transparent
		if row.Hovered() || i == p.hl {
			bg = colors(t).wash
		}
		row.Background(row.AnimateColor("bg", bg, hover))
		row.Children(func() {
			box := ui.Box(c).Width(14).Shrink(0).Center()
			if r.family == current {
				box.Children(func() { ui.Icon(c, iconCheck).FontSize(fsBody).TextColor(t.Accent) })
			}
			ui.Text(c, label).FontSize(fsBody).Font(fontStack(r.family)).SingleLine().Grow(1).Shrink(1)
		})
		if row.Clicked() {
			choose(r.family)
		}
	}).Height(min(float32(len(rows))*(fontRowHeight()+2), 320)).Shrink(0) // 每行上方留 2
}

// fontSizeStepper 是字号的"− 13 + 默认"一组按钮。
func fontSizeStepper(c *ui.Context, size, def float32, set func(float32)) {
	stepper(c, L(c).SettingsFontSize(), fmt.Sprintf("%g", size), func(d int) { set(size + float32(d)) }, func() { set(def) })
}

// stepper 是"− 值 + 默认"一组按钮：step 收到 -1 或 1，reset 恢复默认；what 是读屏念的名字。
func stepper(c *ui.Context, what, value string, step func(d int), reset func()) {
	m := L(c)
	if pressable(textButton(c, "−")).Label(m.FontsDecrease(what)).Clicked() {
		step(-1)
	}
	ui.Text(c, value).FontSize(fsBody).FontFeatures("tnum").Width(sz(32)).Center()
	if pressable(textButton(c, "+")).Label(m.FontsIncrease(what)).Clicked() {
		step(1)
	}
	if pressable(textButton(c, m.CommonDefault())).Label(m.FontsResetWhat(what)).Clicked() {
		reset()
	}
}

// previewBlock 是设置卡片底部的预览：上面一条细线和"预览"二字，内容直接放在卡片底色上，
// 卡片和终端、编辑器同一个底色，看到的就是实际的样子。
func previewBlock(c *ui.Context, body func()) {
	t := c.Theme()
	ui.Column(c).Gap(8).Padding(16, 0, 0, 0).Margin(4, 0, 0, 0).Children(func() {
		ui.Text(c, L(c).CommonPreview()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
		body()
	})
}

// termPreviewLines 是终端预览的内容：每段是颜色在调色板里的序号（-1 是前景色）、是否粗体和文字。
// 分支图标来自 Nerd Font，看得出选的字体缺不缺图标。
func termPreviewLines(m locales.Messages) [][]termPreviewSeg {
	sample := m.FontsTermSample()
	return [][]termPreviewSeg{
		{{6, true, "~/posse"}, {-1, false, " on "}, {5, false, "\ue0a0 main"}, {1, false, " [!]"}},
		{{2, false, "❯ "}, {-1, false, "ls"}},
		{{4, true, "cmd  docs  internal  "}, {-1, false, "go.mod  README.md"}},
		{{2, false, "❯ "}, {-1, false, `echo "` + sample + `"`}},
		{{-1, false, sample}},
		{{-1, false, "0O 1lI {}[] => != ≥ █"}},
	}
}

type termPreviewSeg struct {
	color int
	bold  bool
	text  string
}

// termPreview 照终端的配色、用终端同样的字体（字重、粗体、行高、平滑）画一段提示符、ls 和中英混排。
// 和终端一样自己排字画出来：普通文字的字体平滑关不掉，用 RichText 看不出平滑的区别。
func (a *app) termPreview(c *ui.Context) {
	t := c.Theme()
	light, dark := terminalThemes()
	th := light
	if t.Dark {
		th = dark
	}
	tf := a.termFont()
	font := ui.Font{Family: tf.Family, Size: tf.Size, Weight: tf.Weight, Antialiased: tf.Antialiased}
	boldFont := font
	boldFont.Weight = tf.BoldWeight
	m := font.Metrics()
	lineH := (m.Ascent + m.Descent + m.LineGap) * termLineHeightOf(a.state)
	base := (lineH-(m.Ascent+m.Descent))/2 + m.Ascent
	lines := termPreviewLines(L(c))
	previewBlock(c, func() {
		ui.Box(c).Height(lineH * float32(len(lines))).Draw(func(p *ui.Painter, r ui.Rect) {
			for i, line := range lines {
				x := r.X
				for _, seg := range line {
					f, col := font, th.Foreground
					if seg.bold {
						f = boldFont
					}
					if seg.color >= 0 {
						col = th.Palette[seg.color]
					}
					g := ui.Shape(seg.text, f)
					p.Glyphs(g, x, r.Y+float32(i)*lineH+base, col)
					w := float32(0)
					for _, gl := range g {
						w = max(w, gl.X+gl.Advance)
					}
					x += w
				}
			}
		})
	})
}

// editorPreviewText 是编辑器预览的示例代码，注释和返回的问候语按界面语言填。
const editorPreviewText = `// %s
func greet(name string) string {
	if name == "" {
		return %q
	}
	return fmt.Sprintf("hi %%s => %%d", name, len(name))
}`

// editorPreview 是一个只读的真编辑器，高亮、行号、行高都和文件标签里一样。
func (a *app) editorPreview(c *ui.Context) {
	m := L(c)
	// 换了界面语言，示例代码跟着换
	text := fmt.Sprintf(editorPreviewText, m.FontsEditorSampleComment(), m.FontsEditorSampleGreeting())
	if a.editorDemo == nil || a.editorDemo.Buffer.String() != text {
		a.editorDemo = &editor.State{Buffer: editor.NewBuffer(text), ReadOnly: true, FitContent: true,
			NoActiveLine: true, NoFolding: true, NoScrollPastEnd: true, Label: m.FontsEditorPreview()}
		a.editorDemo.Buffer.SetLanguage(editor.LanguageFor("preview.go"))
	}
	a.editorDemo.Font, a.editorDemo.LineHeight, a.editorDemo.BoldWeight = a.editorFont(), a.state.EditorLineHeight, a.editorBoldWeight()
	previewBlock(c, func() {
		editor.View(c.Key("editor-preview"), a.editorDemo)
	})
}
