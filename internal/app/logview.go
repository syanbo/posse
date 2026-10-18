package app

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// Git 日志标签页，照 WebStorm 的 Log：左边是分支树（logrefs.go），中间是提交图和筛选栏（文字或哈希、分支、作者，文件历史时是路径），
// 右边是选中提交的说明和改的文件。提交的右键菜单有 cherry-pick、reset、revert、改提交信息、
// 和上一个提交合并、删除提交。

const (
	logPage   = 300
	logRowH   = 32
	laneWidth = 14
)

type logDoc struct {
	filter   gitx.LogFilter
	text     string // 输入框里的文字，停顿后才应用到 filter
	author   string
	commits  []gitx.CommitInfo
	graph    []graphRow
	more     bool // 还有更早的提交
	loading  bool
	gen      int
	commit   *commitView
	list     ui.ListState
	reloaded time.Time
	refsOpen map[string]bool // 左边分支树展开了哪些组："local"、"remote"、"tags"、"remote:<远端>"
	refQuery string
	err      string       // 上一次读日志的错误（筛选写错、选中的分支被删了），写在列表里
	hist     *fileHistory // 文件历史（filter.Path 非空）选中的提交和它的 diff
}

func logKey(f gitx.LogFilter) string {
	if f.Path != "" {
		return "log:path:" + f.Path
	}
	return "log:"
}

// openLog 在底部浮层里打开日志；path 非空时是这个文件的历史，branch 非空时只看这个分支。
func (a *app) openLog(w *workspace, branch, path string) {
	v := a.filesFor(w)
	f := gitx.LogFilter{Branch: branch, Path: path}
	key := logKey(f)
	a.setLogPanel(true)
	for _, d := range v.logs {
		if d.path == key {
			v.logCur = d
			if d.log.filter.Branch != branch {
				d.log.filter.Branch = branch
				a.loadLog(v, d, false)
			}
			return
		}
	}
	d := &fileDoc{path: key, log: &logDoc{filter: f}, started: true}
	// 整个仓库的日志排在最前面，文件历史按打开的先后排在后面
	if path == "" {
		v.logs = slices.Insert(v.logs, 0, d)
	} else {
		v.logs = append(v.logs, d)
	}
	v.logCur = d
	a.loadLog(v, d, false)
}

func (a *app) setLogPanel(open bool) {
	if a.state.LogPanel != open {
		a.state.LogPanel = open
		a.saveState()
	}
}

// toggleLogPanel 是 ⌘9 和标签栏的日志按钮：开着就隐藏，关着就打开（还没有日志时打开整个仓库的）。
func (a *app) toggleLogPanel(w *workspace) {
	if a.state.LogPanel {
		a.setLogPanel(false)
		return
	}
	if v := a.filesFor(w); v.logCur == nil {
		a.openLog(w, "", "")
		return
	}
	a.setLogPanel(true)
}

// closeLog 关掉浮层里的一个文件历史；整个仓库的日志不关，只能隐藏浮层。
func (a *app) closeLog(v *filesView, d *fileDoc) {
	i := slices.Index(v.logs, d)
	if i < 0 {
		return
	}
	v.logs = slices.Delete(v.logs, i, i+1)
	if v.logCur == d {
		v.logCur = nil
		if len(v.logs) > 0 {
			v.logCur = v.logs[max(i-1, 0)]
		}
	}
}

// Git 日志浮层的高度，拖上边缘调整；上限还要给终端留出几行。
const (
	logPanelDefault = 340
	logPanelMin     = 180
	logPanelRoom    = 120
)

func (a *app) logPanelHeight(avail float32) float32 {
	h := a.state.LogPanelHeight
	if h == 0 {
		h = logPanelDefault
	}
	if avail > 0 {
		h = min(h, avail-logPanelRoom)
	}
	return max(h, logPanelMin)
}

// logPanel 是从终端那一栏底边滑上来的 Git 日志浮层，照 WebStorm 底部的 Git 工具窗口：盖在终端上面、不改终端尺寸
// （改了里面的程序要按新行数重排），点别处不收，只有隐藏按钮、⌘9 和标签栏的日志按钮开关。
// 顶上一排是日志和文件历史的小标签，拖上边缘调高度。body 是终端那一栏，按它的高度限制浮层高度。
func (a *app) logPanel(c *ui.Context, w *workspace, body ui.Element) {
	open := float32(0)
	if a.state.LogPanel && w.Branch != "" {
		open = 1
	}
	shown := body.AnimateTo("log-panel", open, ui.Animation{Duration: 200 * time.Millisecond, Ease: ui.EaseOut})
	if shown < 0.01 {
		return
	}
	v := a.filesFor(w)
	if v.logCur == nil {
		if open == 0 {
			return
		}
		a.openLog(w, "", "")
	}
	t := c.Theme()
	p := colors(t)
	h := a.logPanelHeight(body.Bounds().H)
	panel := ui.Column(c.Key("log-panel")).Absolute().Left(0).Right(0).Bottom(-(1-shown)*h).Height(h).
		Background(p.card).BorderWidth(1, 0, 0, 0).BorderColor(p.line).Shadow(0, -6, 18, 0, ui.RGBA(0, 0, 0, 0.12))
	panel.Children(func() {
		ui.Row(c).Height(34).Shrink(0).Padding(0, 10, 0, 8).Gap(2).AlignItems(ui.Center).Background(p.band).
			BorderWidth(0, 0, 1, 0).BorderColor(p.line).Children(func() {
			// 点 × 会在这次遍历里关掉文件历史：遍历副本
			for _, d := range slices.Clone(v.logs) {
				a.logTab(c, v, d)
			}
			ui.Spacer(c)
			if iconButton(c.Key("log-hide"), iconMinus, "隐藏 ⌘9").Clicked() {
				a.setLogPanel(false)
			}
		})
		d := v.logCur
		switch {
		case d == nil:
		case d.kind == docLog:
			a.logView(c, w, v, d)
		case d.kind == docError:
			docMessage(c, d, d.err)
		default:
			ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(16, 18)
		}
		// 拖上边缘调高度，松手才写 ui.json
		grip := ui.Box(c.Key("log-panel-grip")).Absolute().Left(0).Right(0).Top(-3).Height(6).Cursor(ui.CursorResizeNS)
		if _, dy, held := grip.Dragged(); held {
			a.state.LogPanelHeight = a.logPanelHeight(body.Bounds().H) - dy
			a.state.LogPanelHeight = a.logPanelHeight(body.Bounds().H)
			a.resizingLog = true
		} else if a.resizingLog {
			a.resizingLog = false
			a.saveState()
		}
		if grip.Hovered() || a.resizingLog {
			accent := t.Accent
			grip.Draw(func(pt *ui.Painter, r ui.Rect) {
				pt.Fill(ui.Rect{X: r.X, Y: r.Y + r.H/2 - 1, W: r.W, H: 2}, accent, 0)
			})
		}
	})
}

// logTab 是浮层顶上的一个小标签：整个仓库的日志写"日志"，文件历史写"历史"加文件名、可以关掉。
func (a *app) logTab(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	active := v.logCur == d
	label := "日志"
	if f := d.log.filter; f.Path != "" {
		label = "历史：" + path.Base(f.Path)
	}
	row := ui.Row(c.Key(d.path)).Height(26).Padding(0, 6, 0, 10).Gap(6).Radius(6).AlignItems(ui.Center).Shrink(0).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, active)
	row.Children(func() {
		ui.Icon(c, iconGitLog).FontSize(13).TextColor(t.Accent).Shrink(0)
		tabText(c, ui.Text(c, label).FontSize(12).Shrink(0), active)
		if d.log.filter.Path == "" {
			ui.Box(c).Width(4)
			return
		}
		x := ui.Box(c.Key("close")).Size(18, 18).Radius(4).Center().Shrink(0).Label("关闭" + label)
		if x.Hovered() {
			x.Background(t.SurfacePressed)
		}
		x.Children(func() { ui.Icon(c, iconClose).FontSize(11).TextColor(t.TextMuted) })
		if x.Clicked() {
			a.closeLog(v, d)
		}
	})
	if row.Clicked() {
		v.logCur = d
	}
}

// loadLog 读日志：more 为真时接着往后读一页。
func (a *app) loadLog(v *filesView, d *fileDoc, more bool) {
	a.readLog(v, d, more, logPage)
}

// reloadLog 引用变了之后从头重读，已经往后翻过的页数一起读回来，列表不缩回第一页。
func (a *app) reloadLog(v *filesView, d *fileDoc) {
	a.readLog(v, d, false, max(logPage, len(d.log.commits)))
}

func (a *app) readLog(v *filesView, d *fileDoc, more bool, limit int) {
	ld := d.log
	ld.gen++
	gen := ld.gen
	ld.loading = true
	skip := 0
	if more {
		skip = len(ld.commits)
	}
	f, root, ws := ld.filter, v.git.repo, v.root
	go func() {
		ctx := context.Background()
		if root == "" {
			root = repoRoot(ws, gitx.TopLevel(ctx, ws))
		}
		list, err := gitx.Log(ctx, root, f, skip, limit)
		if root == "" {
			list, err = nil, fmt.Errorf("这个工作区不是 git 仓库")
		}
		a.update(func() {
			if !v.git.checked {
				v.git.checked, v.git.repo = true, root
			}
			if ld.gen != gen {
				return
			}
			ld.loading = false
			// 出错时筛选栏和分支树照样画，错误写在列表里，改了筛选再读；不是 git 仓库才整页报错
			if root == "" {
				d.kind, d.err = docError, err.Error()
				return
			}
			ld.err = ""
			if err != nil {
				ld.err, ld.commits, ld.graph, ld.more = err.Error(), nil, nil, false
				d.kind = docLog
				return
			}
			if more {
				ld.commits = append(ld.commits, list...)
			} else {
				ld.commits = list
			}
			ld.more = len(list) == limit
			// 按作者、文字筛出来的提交父提交多半不在结果里，画出来每个都是一条新线；这时只画点
			ld.graph = nil
			if f.Author == "" && strings.TrimSpace(f.Text) == "" {
				ld.graph = buildGraph(ld.commits)
			}
			ld.reloaded = time.Now()
			d.kind = docLog
		})
	}()
}

// --- 提交图

// graphRow 是提交图的一行：提交在第几条线上，和进出这一行的线。
// in 是这一行上边的线（每条线等着的提交），out 是下边的线；同一个提交在 in 里的位置连到它在 out 里的位置。
type graphRow struct {
	lane    int
	in, out []string
	parents []int // 父提交在 out 里的位置
}

// buildGraph 按日志顺序给每个提交分线：提交接到等它的线上（没有就开一条新线），
// 线接着等它的第一个父提交，其余父提交接到已经在等它的线上或开新线；结束的线去掉，后面的线左移。
func buildGraph(commits []gitx.CommitInfo) []graphRow {
	rows := make([]graphRow, len(commits))
	var lanes []string
	for i, c := range commits {
		in := slices.Clone(lanes)
		lane := slices.Index(lanes, c.Hash)
		if lane < 0 {
			lanes = append(lanes, c.Hash)
			lane = len(lanes) - 1
			in = slices.Clone(lanes)
			in[lane] = "" // 新开的线：上边没有线进来
		}
		// 别的线也在等这个提交（合并到这里的分支），它们在这里结束
		for j := len(lanes) - 1; j >= 0; j-- {
			if j != lane && lanes[j] == c.Hash {
				lanes = slices.Delete(lanes, j, j+1)
				if j < lane {
					lane--
				}
			}
		}
		var parents []int
		if len(c.Parents) == 0 {
			lanes = slices.Delete(lanes, lane, lane+1)
		} else {
			lanes[lane] = c.Parents[0]
			for _, p := range c.Parents[1:] {
				if !slices.Contains(lanes, p) {
					lanes = append(lanes, p)
				}
			}
			for _, p := range c.Parents {
				parents = append(parents, slices.Index(lanes, p))
			}
		}
		rows[i] = graphRow{lane: lane, in: in, out: slices.Clone(lanes), parents: parents}
	}
	return rows
}

var laneColors = []ui.Color{ui.Hex("#D97757"), ui.Hex("#5B8DEF"), ui.Hex("#3E9E6B"), ui.Hex("#B36AD9"), ui.Hex("#D9A441"), ui.Hex("#4BB3B3")}

func laneColor(i int) ui.Color { return laneColors[i%len(laneColors)] }

// graphCell 画一行的提交图。
func graphCell(c *ui.Context, r graphRow, hash string) ui.Element {
	n := max(len(r.in), len(r.out), r.lane+1)
	t := c.Theme()
	return ui.Box(c).Width(float32(n*laneWidth + 6)).Height(logRowH).Shrink(0).Draw(func(p *ui.Painter, rect ui.Rect) {
		x := func(i int) float32 { return rect.X + 6 + float32(i*laneWidth) }
		top, mid, bot := rect.Y, rect.Y+rect.H/2, rect.Y+rect.H
		line := func(x0, y0, x1, y1 float32, col ui.Color) {
			var path ui.Path
			path.MoveTo(x0, y0).LineTo(x1, y1)
			p.StrokePath(&path, 1.6, col)
		}
		// 上半段：进来的线连到这个提交，或者连到它在下边的位置
		for j, h := range r.in {
			switch {
			case h == "":
			case h == hash:
				line(x(j), top, x(r.lane), mid, laneColor(j))
			default:
				if k := slices.Index(r.out, h); k >= 0 {
					line(x(j), top, x(k), mid, laneColor(k))
				}
			}
		}
		// 下半段：经过的线直下，提交连到父提交的线
		for k, h := range r.out {
			if !slices.Contains(r.in, h) || h == hash {
				continue
			}
			line(x(k), mid, x(k), bot, laneColor(k))
		}
		for _, k := range r.parents {
			if k >= 0 {
				line(x(r.lane), mid, x(k), bot, laneColor(k))
			}
		}
		p.Fill(ui.Rect{X: x(r.lane) - 4, Y: mid - 4, W: 8, H: 8}, laneColor(r.lane), 4)
		p.Fill(ui.Rect{X: x(r.lane) - 2, Y: mid - 2, W: 4, H: 4}, t.Background, 2)
	})
}

// --- 日志界面

// logView 是日志标签页：左边分支树，右边筛选栏、提交列表和选中提交的详情。
func (a *app) logView(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	if d.log.filter.Path != "" {
		a.fileHistoryView(c, w, v, d)
		return
	}
	p := colors(c.Theme())
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(220).Shrink(0).Background(p.band).Children(func() { a.logRefs(c, v, d) })
		ui.Box(c).Width(1).Shrink(0).Background(p.line)
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			a.logFilterBar(c, v, d)
			ui.Box(c).Height(1).Shrink(0).Background(p.line)
			a.logList(c, w, v, d)
		})
	})
}

// logList 是提交列表和右边选中提交的详情。
func (a *app) logList(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	p := colors(t)
	ld := d.log
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			if len(ld.commits) == 0 {
				msg, col := "没有符合条件的提交", t.TextMuted
				switch {
				case ld.loading:
					msg = "正在读取…"
				case ld.err != "":
					msg, col = ld.err, t.Danger
				}
				ui.Column(c).Padding(12, 16).Gap(10).Children(func() {
					ui.Text(c, msg).FontSize(12).TextColor(col).LineHeight(1.4)
					if !ld.loading && ld.filtered() && pressable(textButton(c, "清除筛选")).Clicked() {
						a.clearLogFilter(v, d)
					}
				})
				return
			}
			commits, graph := ld.commits, ld.graph
			n := len(commits)
			if ld.more {
				n++
			}
			ld.list.Key = func(i int) any {
				if i == len(commits) {
					return "more"
				}
				return commits[i].Hash
			}
			ui.List(c, &ld.list, n, func(i int) {
				if i == len(commits) {
					more := ui.Row(c.Key("more")).Height(logRowH).Padding(0, 16).AlignItems(ui.Center).Role(ui.RoleButton)
					more.Children(func() { ui.Text(c, "加载更早的提交").FontSize(12).TextColor(t.Accent) })
					if more.Clicked() && !ld.loading {
						a.loadLog(v, d, true)
					}
					return
				}
				var gr graphRow // 没有提交图时只画点
				if graph != nil {
					gr = graph[i]
				}
				a.logRow(c, w, v, d, commits[i], gr)
			}).Grow(1)
			// 焦点在列表里（点过一行）时 ↑↓ 换选中的提交，照 WebStorm 的日志
			step := 0
			switch {
			case ld.list.Shortcut(c, 0, ui.KeyDown):
				step = 1
			case ld.list.Shortcut(c, 0, ui.KeyUp):
				step = -1
			}
			if step != 0 {
				i := -1
				if ld.commit != nil {
					i = slices.IndexFunc(commits, func(ci gitx.CommitInfo) bool { return ci.Hash == ld.commit.info.Hash })
				}
				if j := i + step; j >= 0 && j < len(commits) {
					a.selectCommit(v, &ld.commit, commits[j])
					ld.list.ScrollIntoView(j)
				}
			}
		})
		if ld.commit != nil {
			ui.Box(c).Width(1).Shrink(0).Background(p.line)
			ui.Column(c).Width(340).Shrink(0).Children(func() { a.commitDetail(c, w, v, ld.commit) })
		}
	})
}

// logFilterBar 是日志上方的筛选：文字或哈希、分支、作者；文件历史时显示路径。
func (a *app) logFilterBar(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	ld := d.log
	ui.Row(c).Height(40).Shrink(0).Padding(0, 10, 0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		text := ui.TextInput(c, &ld.text).Placeholder("提交信息或哈希").Label("按提交信息或哈希筛选").Width(220)
		if text.Changed() || text.Submitted() {
			a.applyLogFilter(v, d)
		}
		author := ui.TextInput(c.Key("author"), &ld.author).Placeholder("作者").Label("按作者筛选").Width(120)
		if author.Changed() || author.Submitted() {
			a.applyLogFilter(v, d)
		}
		branch := refShort(ld.filter.Branch)
		if branch == "" {
			branch = "全部分支"
		}
		ui.Row(c.Key("branch-filter")).Height(26).Padding(0, 8).Gap(4).Radius(6).AlignItems(ui.Center).Background(t.Surface).
			Role(ui.RoleButton).Label("按分支筛选").Children(func() {
			ui.Icon(c, iconBranch).FontSize(12).TextColor(t.TextMuted)
			ui.Text(c, branch).FontSize(11.5).Font(monoFont).SingleLine().MaxWidth(160)
			ui.Icon(c, iconDown).FontSize(10).TextColor(t.TextMuted)
		}).Menu(func(m *ui.Menu) {
			if m.Item("全部分支").Checked(ld.filter.Branch == "").Chosen() {
				ld.filter.Branch = ""
				a.loadLog(v, d, false)
			}
			if v.git.branches.list == nil {
				a.loadBranches(v)
			}
			m.Separator()
			for _, b := range v.git.branches.list {
				if m.Item(b.Name).Checked(ld.filter.Branch == branchRef(b)).Chosen() {
					ld.filter.Branch = branchRef(b)
					a.loadLog(v, d, false)
				}
			}
		})
		if ld.filter.Path != "" {
			ui.Row(c).Height(26).Padding(0, 8).Gap(4).Radius(6).AlignItems(ui.Center).Background(colors(t).tint).Children(func() {
				ui.Icon(c, iconFile).FontSize(12).TextColor(t.Accent)
				ui.Text(c, ld.filter.Path).FontSize(11.5).Font(monoFont).SingleLine().MaxWidth(240)
			})
		}
		ui.Spacer(c)
		if ld.loading {
			ui.Text(c, "读取中…").FontSize(11).TextColor(t.TextMuted)
		}
		if pressable(iconButtonSized(c, iconRefresh, "刷新日志", 24, 14)).Clicked() {
			a.loadLog(v, d, false)
		}
	})
}

// filtered 报告日志有没有按文字、作者或分支筛选（文件历史的路径不算）。
func (ld *logDoc) filtered() bool {
	return ld.filter.Branch != "" || ld.filter.Author != "" || strings.TrimSpace(ld.filter.Text) != ""
}

// clearLogFilter 清掉文字、作者和分支的筛选，读全部分支。
func (a *app) clearLogFilter(v *filesView, d *fileDoc) {
	ld := d.log
	ld.text, ld.author = "", ""
	ld.filter.Text, ld.filter.Author, ld.filter.Branch = "", "", ""
	a.loadLog(v, d, false)
}

// applyLogFilter 在输入停顿 300ms 后按新的文字和作者重读。
func (a *app) applyLogFilter(v *filesView, d *fileDoc) {
	ld := d.log
	ld.gen++
	gen := ld.gen
	time.AfterFunc(300*time.Millisecond, func() {
		a.update(func() {
			if ld.gen != gen {
				return
			}
			ld.filter.Text, ld.filter.Author = ld.text, ld.author
			a.loadLog(v, d, false)
		})
	})
}

// logRow 是日志的一行：提交图、标题、分支和标签、作者、时间；右键是提交的操作。
func (a *app) logRow(c *ui.Context, w *workspace, v *filesView, d *fileDoc, ci gitx.CommitInfo, gr graphRow) {
	t := c.Theme()
	ld := d.log
	row := ui.Row(c.Key(ci.Hash)).Height(logRowH).Padding(0, 14, 0, 14).Gap(10).AlignItems(ui.Center).Role(ui.RoleButton).Label(ci.Subject)
	bg := ui.Transparent
	switch {
	case ld.commit != nil && ld.commit.info.Hash == ci.Hash:
		bg = colors(t).tint
	case row.Hovered():
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		graphCell(c, gr, ci.Hash)
		// 标题和引用让位，作者、时间固定宽度、各行对齐，不被挤掉
		ui.Text(c, ci.Subject).FontSize(12.5).SingleLine().MaxWidth(460).Shrink(1).MinWidth(90)
		refChips(c, ci.Refs)
		ui.Spacer(c)
		ui.Text(c, ci.Author).FontSize(11).TextColor(t.TextMuted).SingleLine().Width(authorW).Shrink(0)
		ui.Text(c, ago(ci.Time)).FontSize(11).TextColor(t.TextMuted).Width(56).Shrink(0).TextAlign(ui.End)
	})
	if row.Clicked() {
		a.selectCommit(v, &ld.commit, ci)
		// 行是按钮、不拿焦点；焦点给列表，↑↓ 才能接着换
		ld.list.Focus(c)
	}
	row.ContextMenu(func(m *ui.Menu) { a.commitMenu(c, m, w, v, d, ci) })
}

// authorW 是日志和比较里作者一列的宽度。
const authorW = 64

// refChipsMax 是一行最多列几个分支和标签，其余写成 +N（选中后详情里有全部）。
const refChipsMax = 2

// refChips 画提交上的分支和标签：当前分支是强调色淡底。整组比标题先让位，放不下时从右边裁掉，
// 不把每个都缩成"…"。
func refChips(c *ui.Context, refs string) {
	t := c.Theme()
	names := refNames(refs)
	if len(names) == 0 {
		return
	}
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(6).MinWidth(0).Clip().Children(func() {
		for i, ref := range names {
			if i == refChipsMax {
				ui.Text(c, fmt.Sprintf("+%d", len(names)-i)).FontSize(10.5).TextColor(t.TextMuted).Shrink(0).Tooltip(refs)
				break
			}
			bg, fg := t.Surface, t.TextMuted
			if ref.head {
				bg, fg = colors(t).tint, t.Accent
			}
			ui.Text(c, ref.name).FontSize(10.5).Font(monoFont).TextColor(fg).Padding(1, 5).Radius(4).Background(bg).SingleLine().Shrink(0).MaxWidth(160)
		}
	})
}

type refName struct {
	name string
	head bool // 当前检出的分支
}

// refNames 拆开 %D："HEAD -> main, origin/main, tag: v1"。
func refNames(d string) []refName {
	var out []refName
	for _, part := range strings.Split(d, ", ") {
		part = strings.TrimSpace(part)
		switch {
		case part == "":
		case strings.HasPrefix(part, "HEAD -> "):
			out = append(out, refName{name: strings.TrimPrefix(part, "HEAD -> "), head: true})
		case part == "HEAD":
		case strings.HasPrefix(part, "tag: "):
			out = append(out, refName{name: strings.TrimPrefix(part, "tag: ")})
		default:
			out = append(out, refName{name: part})
		}
	}
	return out
}

// commitMenu 是日志里一个提交的操作；改写历史的几项只对当前分支上的提交有效（gitx 会检查）。
func (a *app) commitMenu(c *ui.Context, m *ui.Menu, w *workspace, v *filesView, d *fileDoc, ci gitx.CommitInfo) {
	short := ci.Hash[:7]
	// 做完由 gitDo 里的 refsChanged 重读日志，翻过的页数一起读回来
	do := func(what string, fn func(ctx context.Context, root string) error) {
		a.guardAgent(w, what, func() { a.gitDo(v, what, fn) })
	}
	if m.Item("复制提交哈希").Chosen() {
		c.WriteClipboard(ci.Hash)
	}
	m.Separator()
	if m.Item("检出这个提交").Chosen() {
		do("检出", func(ctx context.Context, root string) error { return gitx.CheckoutRevision(ctx, root, ci.Hash) })
	}
	if m.Item("基于这个提交新建分支…").Chosen() {
		a.askNewBranch(w, v, ci.Hash)
	}
	m.Separator()
	if m.Item("Cherry-pick").Chosen() {
		do("Cherry-pick", func(ctx context.Context, root string) error { return gitx.CherryPick(ctx, root, ci.Hash) })
	}
	m.Submenu("把当前分支重置到这里", func(sm *ui.Menu) {
		if sm.Item("Soft：改动留在暂存区").Chosen() {
			do("重置", func(ctx context.Context, root string) error { return gitx.Reset(ctx, root, ci.Hash, "soft") })
		}
		if sm.Item("Mixed：改动留在工作区").Chosen() {
			do("重置", func(ctx context.Context, root string) error { return gitx.Reset(ctx, root, ci.Hash, "mixed") })
		}
		if sm.Item("Hard：丢掉之后的改动…").Chosen() {
			a.confirm("硬重置到 "+short+"？", "当前分支会回到这个提交，之后的提交和所有没提交的改动都会丢掉。", "硬重置", func() {
				a.update(func() {
					do("重置", func(ctx context.Context, root string) error { return gitx.Reset(ctx, root, ci.Hash, "hard") })
				})
			})
		}
	})
	if m.Item("Revert：新建撤销它的提交").Chosen() {
		do("Revert", func(ctx context.Context, root string) error { return gitx.RevertCommit(ctx, root, ci.Hash) })
	}
	m.Separator()
	if m.Item("改提交信息…").Chosen() {
		a.guardPushed(v, ci.Hash, func() {
			a.askMessage(v, "改 "+short+" 的提交信息", ci.Hash, "", func(msg string) {
				do("改提交信息", func(ctx context.Context, root string) error { return gitx.Reword(ctx, root, ci.Hash, msg) })
			})
		})
	}
	if len(ci.Parents) == 1 && m.Item("和上一个提交合并…").Chosen() {
		a.guardPushed(v, ci.Parents[0], func() {
			a.askMessage(v, "和上一个提交合并", ci.Parents[0], ci.Hash, func(msg string) {
				do("合并提交", func(ctx context.Context, root string) error { return gitx.SquashIntoParent(ctx, root, ci.Hash, msg) })
			})
		})
	}
	if m.Item("删除这个提交…").Chosen() {
		a.guardPushed(v, ci.Hash, func() {
			a.confirm("从历史里删除 "+short+"？", "“"+ci.Subject+"”会从当前分支的历史里去掉，它的改动也一起去掉。", "删除提交", func() {
				a.update(func() {
					do("删除提交", func(ctx context.Context, root string) error { return gitx.Drop(ctx, root, ci.Hash) })
				})
			})
		})
	}
}

// guardPushed 在改写从 oldest 开始的历史之前查它推送过没有：推送过的先说明之后要强制推送，确认了再继续。
func (a *app) guardPushed(v *filesView, oldest string, then func()) {
	root := v.git.repo
	go func() {
		remotes := gitx.RemotesContaining(context.Background(), root, oldest)
		a.update(func() {
			if len(remotes) == 0 {
				then()
				return
			}
			where := remotes[0]
			if len(remotes) > 1 {
				where += fmt.Sprintf(" 等 %d 个远端分支", len(remotes))
			}
			a.confirm("这些提交已经推送过", "它们在 "+where+" 上。改写之后要强制推送，别人基于旧提交的工作会对不上。", "仍然改写", func() {
				a.update(then)
			})
		})
	}()
}

// askMessage 读出提交信息（两个提交时拼在一起）填进输入框，让用户改完再执行。
func (a *app) askMessage(v *filesView, title, hash, second string, do func(string)) {
	root := v.git.repo
	go func() {
		ctx := context.Background()
		msg := gitx.FullMessage(ctx, root, hash)
		if second != "" {
			msg += "\n\n" + gitx.FullMessage(ctx, root, second)
		}
		a.update(func() {
			a.prompt(promptReq{title: title, initial: msg, verb: "确定", multiline: true, do: func(s string, _ bool) { do(s) }})
		})
	}()
}

// --- Annotate

// blameDoc 是文件标签里开着的 Annotate：每行最后改它的提交，同一个提交的连续几行只在第一行写作者和日期。
// 和 WebStorm 一样在同一个标签里开关，不另开标签。
type blameDoc struct {
	rel     string
	lines   []gitx.BlameLine
	loading bool
	err     string
	list    ui.ListState
	sel     *commitView
}

// openBlame 在这个文件的标签里打开 Annotate（文件没开着就先打开）。
func (a *app) openBlame(w *workspace, v *filesView, abs string) {
	rel := v.git.relPath(abs)
	if rel == "" {
		a.err = "这个文件不在 git 仓库里"
		return
	}
	a.openFile(w, abs, 0, [2]int{})
	v = a.filesFor(w)
	d := v.cur
	if d == nil || d.path != filepath.Clean(abs) {
		return
	}
	bd := &blameDoc{rel: rel, loading: true}
	d.blame = bd
	root := v.git.repo
	go func() {
		lines, err := gitx.Blame(context.Background(), root, rel)
		a.update(func() {
			bd.loading = false
			if err != nil {
				bd.err = err.Error()
				return
			}
			bd.lines = lines
		})
	}()
}

func (a *app) blameView(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	bd := d.blame
	lines := bd.lines
	switch {
	case bd.err != "":
		docMessage(c, d, bd.err)
		return
	case bd.loading:
		ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(16, 18)
		return
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		bd.list.Key = func(i int) any { return i }
		ui.List(c, &bd.list, len(lines), func(i int) {
			l := lines[i]
			first := i == 0 || lines[i-1].Hash != l.Hash
			uncommitted := strings.Trim(l.Hash, "0") == ""
			row := ui.Row(c.Key(i)).Height(22).Gap(10).AlignItems(ui.Center)
			if !uncommitted && bd.sel != nil && bd.sel.info.Hash == l.Hash {
				row.Background(colors(t).tint)
			}
			row.Children(func() {
				who := ui.Row(c.Key("who")).Width(190).Height(22).Padding(0, 8).Gap(6).AlignItems(ui.Center).Shrink(0).Background(colors(t).wash)
				who.Children(func() {
					if !first {
						return
					}
					if uncommitted {
						ui.Text(c, "没提交").FontSize(11).TextColor(t.Accent)
						return
					}
					ui.Text(c, l.Time.Format("2006-01-02")).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
					ui.Text(c, l.Author).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
				})
				if !uncommitted {
					who.Tooltip(l.Hash[:10] + "  " + l.Author + "\n" + l.Summary)
					if who.Clicked() {
						a.selectCommit(v, &bd.sel, gitx.CommitInfo{Hash: l.Hash, Author: l.Author, Time: l.Time, Subject: l.Summary})
					}
				}
				ui.Text(c, fmt.Sprint(i+1)).FontSize(11).Font(monoFont).TextColor(t.TextMuted).Width(36).Shrink(0).TextAlign(ui.End)
				ui.Text(c, strings.ReplaceAll(l.Text, "\t", "    ")).FontSize(a.fontSize - editorInset).Font(termFontFamily).SingleLine().Shrink(1)
			})
		}).Grow(1)
		if bd.sel != nil {
			ui.Box(c).Width(1).Shrink(0).Background(colors(t).line)
			ui.Column(c).Width(340).Shrink(0).Children(func() { a.commitDetail(c, w, v, bd.sel) })
		}
	})
}

// gitFileMenu 是文件在 Git 下的操作：文件历史、Annotate。用在文件树和打开的文件上。
func (a *app) gitFileMenu(m *ui.Menu, w *workspace, v *filesView, abs string) {
	rel := v.git.relPath(abs)
	if rel == "" {
		return
	}
	m.Separator()
	if m.Item("文件历史").Chosen() {
		a.openLog(w, "", rel)
	}
	if m.Item("Annotate（逐行看是谁改的）").Chosen() {
		a.openBlame(w, v, abs)
	}
}
