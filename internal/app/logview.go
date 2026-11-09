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

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/locales"
)

// Git 日志标签页，照 WebStorm 的 Log：左边是分支树（logrefs.go），中间是提交图和筛选栏（文字或哈希、分支、作者，文件历史时是路径），
// 右边是选中提交的说明和改的文件。提交的右键菜单有 cherry-pick、reset、revert、改提交信息、
// 和上一个提交合并、删除提交。

const (
	logPage = 300
)

type logDoc struct {
	root      string // 仓库根目录；整个仓库的日志跟着当前仓库换（useRepo），文件历史一直是文件所在的仓库
	filter    gitx.LogFilter
	text      string // 输入框里的文字，停顿后才应用到 filter
	author    string
	commits   []gitx.CommitInfo
	graph     *commitGraph // 提交图；按作者、文字筛选时为 nil，只画点
	jump      string       // 点了提交图的箭头、要跳去的提交还没读出来：往下读到它为止
	more      bool         // 还有更早的提交
	loading   bool
	gen       int
	commit    *commitView
	list      ui.ListState
	reloaded  time.Time
	refsOpen  map[string]bool // 左边分支树展开了哪些组："local"、"remote"、"tags"、"remote:<远端>"
	refQuery  string
	refList   ui.ListState
	refCursor string       // 分支树里键盘停在的行（refItem.key）
	focusList bool         // 在分支树里按了 →：提交列表建出来时拿焦点
	err       string       // 上一次读日志的错误（筛选写错、选中的分支被删了），写在列表里
	notRepo   bool         // 不是 git 仓库：整页报错（d.kind 是 docError）
	hist      *fileHistory // 文件历史（filter.Path 非空）选中的提交和它的 diff
	head      bool         // 下次读日志时先读出当前分支，只看它（打开整个仓库的日志时默认选中当前分支）
	readGen   int          // 最近一次开始读日志时的 gen
}

func logKey(root string, f gitx.LogFilter) string {
	if f.Path != "" {
		return "log:path:" + filepath.Join(root, f.Path)
	}
	return "log:"
}

// openLog 在底部浮层里打开日志；path 非空时是这个文件的历史，branch 非空时只看这个分支，
// 整个仓库的日志不给 branch 时只看当前分支。
func (a *app) openLog(w *workspace, branch, path string) {
	v := a.filesFor(w)
	a.withRepos(v, func() { a.openLogIn(w, v.git, branch, path) })
}

// openLogIn 打开仓库 g 的日志（path 为空）或其中一个文件的历史（path 相对 g 的根目录）。
// 整个仓库的日志只有一个，对着当前仓库：g 不是当前仓库时先换过去。
func (a *app) openLogIn(w *workspace, g *gitState, branch, path string) {
	v := a.filesFor(w)
	if path == "" {
		a.useRepo(v, g)
	}
	f := gitx.LogFilter{Path: path}
	if branch != "" {
		f.Branches = []string{branch}
	}
	head := branch == "" && path == ""
	key := logKey(g.repo, f)
	a.setLogPanel(true)
	v.console = false
	for _, d := range v.logs {
		if d.path == key {
			v.logCur = d
			if head || !slices.Equal(d.log.filter.Branches, f.Branches) {
				d.log.filter.Branches, d.log.head = f.Branches, head
				a.loadLog(v, d, false)
			}
			return
		}
	}
	d := &fileDoc{path: key, log: &logDoc{root: g.repo, filter: f, head: head}, started: true}
	// 整个仓库的日志排在最前面，文件历史按打开的先后排在后面
	if path == "" {
		v.logs = slices.Insert(v.logs, 0, d)
	} else {
		v.logs = append(v.logs, d)
	}
	v.logCur = d
	a.loadLog(v, d, false)
}

// logPanelOpen 报告选中的工作区所在项目开着日志浮层。
func (a *app) logPanelOpen() bool { return a.state.LogPanels[a.panelProject()] }

func (a *app) setLogPanel(open bool) {
	a.setProjectPanel(&a.state.LogPanels, a.panelProject(), open)
}

// toggleLogPanel 是 ⌘9 和标签栏的日志按钮：开着就隐藏，关着就打开（还没有日志时打开整个仓库的）。
func (a *app) toggleLogPanel(w *workspace) {
	if a.logPanelOpen() {
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

// logRowH 是提交列表一行的高度，跟着界面字号缩放。
func logRowH() float32 { return sz(32) }

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
	if a.logPanelOpen() && a.gitBranch(w) != "" {
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
	panel := elevSheet(c, ui.Column(c.Key("log-panel")).Absolute().Left(0).Right(0).Bottom(-(1-shown)*h).Height(h).
		Background(p.card))
	panel.Children(func() {
		ui.Row(c).Height(sz(34)).Shrink(0).Padding(0, 12, 0, 8).Gap(2).AlignItems(ui.Center).Background(p.band).
			BorderWidth(0, 0, hairline, 0).BorderColor(p.line).Children(func() {
			// 点 × 会在这次遍历里关掉文件历史：遍历副本
			for i, d := range slices.Clone(v.logs) {
				a.logTab(c, v, d)
				// 操作记录排在整个仓库的提交日志后面、文件历史前面
				if i == 0 {
					a.consoleTab(c, w, v)
				}
			}
			ui.Spacer(c)
			if iconButton(c.Key("log-hide"), iconMinus, a.withKey(L(c).LogHide(), "gitLog")).Clicked() {
				a.setLogPanel(false)
			}
		})
		d := v.logCur
		switch {
		case v.console:
			a.gitConsole(c, w, v)
		case d == nil:
		case d.kind == docLog:
			a.logView(c, w, v, d)
		case d.kind == docError && d.log != nil && d.log.notRepo:
			docMessage(c, d, L(c).GitNotRepo())
		case d.kind == docError:
			docMessage(c, d, d.err)
		default:
			ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(16, 16)
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

// logTab 是浮层顶上的一个小标签：整个仓库的日志写"提交日志"，文件历史写"历史"加文件名、可以关掉。
func (a *app) logTab(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	active := v.logCur == d && !v.console
	label := L(c).LogCommitsTitle()
	if f := d.log.filter; f.Path != "" {
		label = L(c).LogHistoryTab(path.Base(f.Path))
	}
	row := ui.Row(c.Key(d.path)).Height(sz(26)).Margin(2, 0, 0, 0).Padding(0, 6, 0, 12).Gap(6).Radius(rRow).AlignItems(ui.Center).Shrink(0).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, active)
	row.Children(func() {
		ui.Icon(c, iconGitLog).FontSize(fsBody).TextColor(t.Accent).Shrink(0)
		tabText(c, ui.Text(c, label).FontSize(fsSmall).Shrink(0), active)
		if d.log.filter.Path == "" {
			ui.Box(c).Width(4)
			return
		}
		x := ui.Box(c.Key("close")).Size(miniBox(), miniBox()).Radius(rSmall).Center().Shrink(0).Label(L(c).LogCloseTab(label))
		if x.Hovered() {
			x.Background(t.SurfacePressed)
		}
		x.Children(func() { ui.Icon(c, iconClose).FontSize(fsSmall).TextColor(t.TextMuted) })
		if x.Clicked() {
			a.closeLog(v, d)
		}
	})
	if row.Clicked() {
		v.logCur, v.console = d, false
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
	f, root := ld.filter, ld.root
	head := ld.head
	ld.head, ld.readGen = false, gen
	go func() {
		ctx := context.Background()
		if head && root != "" {
			// 分离的 HEAD 时 git 给 "HEAD"，就选分支树里的 HEAD 一行
			if name, err := gitx.CurrentBranch(ctx, root); err == nil && name != "HEAD" {
				f.Branches = []string{"refs/heads/" + name}
			} else if err == nil {
				f.Branches = []string{"HEAD"}
			}
		}
		var list []gitx.CommitInfo
		var err error
		if root != "" {
			list, err = gitx.Log(ctx, root, f, skip, limit)
		}
		a.update(func() {
			if ld.gen != gen {
				// 只是输入筛选文字的停顿把 gen 加了一、还没开始新的读：默认选中当前分支留给那一次
				if head && ld.readGen == gen {
					ld.head = true
				}
				return
			}
			ld.loading = false
			if head {
				ld.filter.Branches = f.Branches
			}
			// 出错时筛选栏和分支树照样画，错误写在列表里，改了筛选再读；不是 git 仓库才整页报错
			if root == "" {
				d.kind, ld.notRepo = docError, true
				return
			}
			ld.err = ""
			if err != nil {
				ld.err, ld.commits, ld.graph, ld.more = errtext.Text(err), nil, nil, false
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
			// 文件历史另有界面（filehistory.go），不画提交图
			if f.Author == "" && strings.TrimSpace(f.Text) == "" && f.Path == "" {
				ld.graph = buildGraph(ld.commits)
			}
			// 往下读是为了找箭头要跳的提交时接着找；从头重读（换了筛选、刷新）就作罢
			if ld.jump != "" && more {
				a.jumpToCommit(v, d, ld.jump)
			} else {
				ld.jump = ""
			}
			ld.reloaded = time.Now()
			d.kind = docLog
		})
	}()
}

// --- 日志界面

// pane 是能拖分栏线调宽的一栏：默认宽度和上下限。
type pane struct{ def, lo, hi float32 }

var (
	logRefsPane   = pane{220, 160, 480}
	logDetailPane = pane{340, 260, 640}
)

func paneWidth(w float32, p pane) float32 {
	if w == 0 {
		w = p.def
	}
	return min(max(w, p.lo), p.hi)
}

// paneDivider 是两栏之间的分栏线，左右拖调宽 width 那一栏，松手才写 ui.json。
// dir 是那一栏在线的哪边：1 是左边（往右拖变宽），-1 是右边。
func (a *app) paneDivider(c *ui.Context, key string, width *float32, p pane, dir float32) {
	vline(c).Children(func() {
		grip := ui.Box(c.Key(key)).Absolute().Top(0).Bottom(0).Left(-3).Width(7).Cursor(ui.CursorResizeEW)
		if dx, _, held := grip.Dragged(); held {
			*width = min(max(paneWidth(*width, p)+dir*dx, p.lo), p.hi)
			a.resizingSplit = key
		} else if a.resizingSplit == key {
			a.resizingSplit = ""
			a.saveState()
		}
		if grip.Hovered() || a.resizingSplit == key {
			accent := c.Theme().Accent
			grip.Draw(func(pt *ui.Painter, r ui.Rect) {
				pt.Fill(ui.Rect{X: r.X + r.W/2 - 1, Y: r.Y, W: 2, H: r.H}, accent, 0)
			})
		}
	})
}

// logView 是日志标签页：左边分支树，右边筛选栏、提交列表和选中提交的详情。
func (a *app) logView(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	if d.log.filter.Path != "" {
		a.fileHistoryView(c, w, v, d)
		return
	}
	p := colors(c.Theme())
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(paneWidth(a.state.LogRefsWidth, logRefsPane)).Shrink(0).Background(p.band).Children(func() { a.logRefs(c, v, d) })
		a.paneDivider(c, "log-refs-grip", &a.state.LogRefsWidth, logRefsPane, 1)
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			a.logFilterBar(c, v, d)
			hline(c)
			a.logList(c, w, v, d)
		})
	})
}

// logList 是提交列表和右边选中提交的详情。
func (a *app) logList(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	ld := d.log
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			if len(ld.commits) == 0 {
				msg, col := L(c).LogNoMatch(), t.TextMuted
				switch {
				case ld.loading:
					msg = L(c).CommonLoading()
				case ld.err != "":
					msg, col = ld.err, t.Danger
				}
				ui.Column(c).Padding(12, 16).Gap(8).Children(func() {
					ui.Text(c, msg).FontSize(fsSmall).TextColor(col).LineHeight(1.4)
					if !ld.loading && ld.filtered() && pressable(textButton(c, L(c).LogClearFilter())).Clicked() {
						a.clearLogFilter(v, d)
					}
				})
				return
			}
			commits := ld.commits
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
					more := ui.Row(c.Key("more")).Height(logRowH()).Padding(0, 16).AlignItems(ui.Center).Role(ui.RoleButton)
					more.Children(func() { ui.Text(c, L(c).LogLoadEarlier()).FontSize(fsSmall).TextColor(t.Accent) })
					if more.Clicked() && !ld.loading {
						a.loadLog(v, d, true)
					}
					return
				}
				a.logRow(c, w, v, d, commits[i], i)
			}).Grow(1)
			if ld.focusList && ld.list.Focus(c) {
				ld.focusList = false
			}
			// 焦点在列表里（点过一行）时 ↑↓ 换选中的提交，→ 到右边它改的文件，← 回左边的分支树，照 WebStorm 的日志
			a.commitListKeys(c, ld.root, &ld.list, commits, &ld.commit)
			if ld.list.Shortcut(c, 0, ui.KeyLeft) {
				ld.refList.Focus(c)
			}
		})
		if ld.commit != nil {
			a.paneDivider(c, "log-detail-grip", &a.state.LogDetailWidth, logDetailPane, -1)
			ui.Column(c).Width(paneWidth(a.state.LogDetailWidth, logDetailPane)).Shrink(0).Children(func() {
				a.commitDetail(c, w, v, ld.commit, true, func() { ld.list.Focus(c) })
			})
		}
	})
}

// commitListKeys 是提交列表的方向键：↑↓ 换选中的提交，→ 让右边的文件列表拿焦点。
func (a *app) commitListKeys(c *ui.Context, root string, list *ui.ListState, commits []gitx.CommitInfo, holder **commitView) {
	cv := *holder
	step := 0
	switch {
	case list.Shortcut(c, 0, ui.KeyDown):
		step = 1
	case list.Shortcut(c, 0, ui.KeyUp):
		step = -1
	case cv != nil && list.Shortcut(c, 0, ui.KeyRight):
		cv.focus = true
	}
	if step == 0 {
		return
	}
	i := -1
	if cv != nil {
		i = slices.IndexFunc(commits, func(ci gitx.CommitInfo) bool { return ci.Hash == cv.info.Hash })
	}
	if j := i + step; j >= 0 && j < len(commits) {
		a.selectCommit(root, holder, commits[j])
		list.ScrollIntoView(j)
	}
}

// logFilterBar 是日志上方的筛选：文字或哈希、分支、作者；文件历史时显示路径。
func (a *app) logFilterBar(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	ld := d.log
	ui.Row(c).Height(sz(40)).Shrink(0).Padding(0, 12, 0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
		text := fieldInputSized(c, &ld.text, L(c).LogMessageHash(), 220, 120).Label(L(c).LogMessageFilter())
		if text.Changed() || text.Submitted() {
			a.applyLogFilter(v, d)
		}
		author := fieldInputSized(c.Key("author"), &ld.author, L(c).LogAuthor(), 120, 80).Label(L(c).LogAuthorFilter())
		if author.Changed() || author.Submitted() {
			a.applyLogFilter(v, d)
		}
		branch := L(c).LogAllBranches()
		switch n := len(ld.filter.Branches); {
		case n == 1:
			branch = refShort(ld.filter.Branches[0])
		case n > 1:
			branch = L(c).LogBranchCount(n)
		}
		// 分支芯片不缩：窄的时候让两个输入框让出宽度
		filter := ui.Row(c.Key("branch-filter")).Height(sz(26)).Shrink(0).Padding(0, 8).Gap(4).Radius(rRow).AlignItems(ui.Center).Background(t.Surface).
			Role(ui.RoleButton).Label(L(c).LogBranchFilter()).Children(func() {
			ui.Icon(c, iconBranch).FontSize(fsSmall).TextColor(t.TextMuted)
			ui.Text(c, branch).FontSize(fsSmall).Font(monoFont).SingleLine().MaxWidth(160)
			ui.Icon(c, iconDown).FontSize(fsSmall).TextColor(t.TextMuted)
		})
		menuButton(c, filter, func(m *popMenu) {
			if m.Item(L(c).LogAllBranches()).Checked(len(ld.filter.Branches) == 0).Chosen() {
				ld.filter.Branches = nil
				a.loadLog(v, d, false)
			}
			g := v.repoOrGone(ld.root)
			if g.branches.list == nil {
				a.loadBranches(g)
			}
			m.Separator()
			for _, b := range g.branches.list {
				if m.Item(b.Name).Checked(slices.Contains(ld.filter.Branches, branchRef(b))).Chosen() {
					ld.filter.Branches = []string{branchRef(b)}
					a.loadLog(v, d, false)
				}
			}
		})
		if v.multiRepo() {
			g := v.repoOrGone(ld.root)
			ui.Row(c.Key("log-repo")).Gap(4).AlignItems(ui.Center).Shrink(1).Tooltip(v.shownPath(ld.root)).Children(func() {
				ui.Icon(c, iconFolder).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
				ui.Text(c, v.repoName(g)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().MaxWidth(160).Shrink(1)
			})
		}
		if ld.filter.Path != "" {
			ui.Row(c).Height(sz(26)).Padding(0, 8).Gap(4).Radius(rRow).AlignItems(ui.Center).Background(colors(t).tint).Children(func() {
				fileGlyph(c, ld.filter.Path, fsSmall)
				ui.Text(c, ld.filter.Path).FontSize(fsSmall).Font(monoFont).SingleLine().MaxWidth(240)
			})
		}
		ui.Spacer(c)
		if ld.loading {
			ui.Text(c, L(c).LogReading()).FontSize(fsSmall).TextColor(t.TextMuted)
		}
		if pressable(iconButtonSized(c, iconRefresh, L(c).LogRefresh(), compactBox(), smallIcon())).Clicked() {
			a.loadLog(v, d, false)
		}
	})
}

// filtered 报告日志有没有按文字、作者或分支筛选（文件历史的路径不算）。
func (ld *logDoc) filtered() bool {
	return len(ld.filter.Branches) > 0 || ld.filter.Author != "" || strings.TrimSpace(ld.filter.Text) != ""
}

// clearLogFilter 清掉文字、作者和分支的筛选，读全部分支。
func (a *app) clearLogFilter(v *filesView, d *fileDoc) {
	ld := d.log
	ld.text, ld.author = "", ""
	ld.filter.Text, ld.filter.Author, ld.filter.Branches = "", "", nil
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
func (a *app) logRow(c *ui.Context, w *workspace, v *filesView, d *fileDoc, ci gitx.CommitInfo, i int) {
	t := c.Theme()
	ld := d.log
	row := ui.Row(c.Key(ci.Hash)).Height(logRowH()).Padding(0, 16, 0, 16).Gap(8).AlignItems(ui.Center).Role(ui.RoleButton).Label(ci.Subject)
	bg := ui.Transparent
	switch {
	case ld.commit != nil && ld.commit.info.Hash == ci.Hash:
		bg = colors(t).tint
	case row.Hovered():
		bg = colors(t).wash
	}
	// 底色和左边分支列表的行一样内缩、圆角、上下留缝；行本身不缩，提交图的线才连得上
	bg = row.AnimateColor("bg", bg, hover)
	row.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(ui.Rect{X: r.X + 6, Y: r.Y + 1, W: r.W - 12, H: r.H - 2}, bg, rRow)
	})
	row.Children(func() {
		a.graphCell(c, v, d, i)
		// 标题和引用让位，作者、时间固定宽度、各行对齐，不被挤掉
		// 窗口太窄时标题至少留 56（12 小时制的时间列宽出 PM、「下午」，标题先让）；短标题不撑满，后面的分支标签紧挨着它
		sw, _ := c.MeasureText(0, ui.Span{Text: ci.Subject, Size: fsBody})
		title := ui.Text(c, ci.Subject).FontSize(fsBody).SingleLine().MaxWidth(460).Shrink(1).MinWidth(min(56, sw))
		// 合并提交照 WebStorm 用次要色：一眼分出真正改代码的提交
		if len(ci.Parents) > 1 {
			title.TextColor(t.TextMuted)
		}
		refChips(c, ci.Refs)
		ui.Text(c, ci.Author).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Width(authorW).Shrink(0)
		ui.Text(c, commitTime(L(c), ci.Time)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Width(commitTimeW(c)).Shrink(0).TextAlign(ui.End).Tooltip(commitStamp(L(c), ci.Time))
	})
	if row.Clicked() {
		a.selectCommit(ld.root, &ld.commit, ci)
		// 行是按钮、不拿焦点；焦点给列表，↑↓ 才能接着换
		ld.list.Focus(c)
	}
	contextMenu(c, row, func(m *popMenu) { a.commitMenu(c, m, w, v, d, ci) })
}

// authorW 是日志和比较里作者一列的宽度。
const authorW = 64

// refChipsMax 是一行最多列几个分支和标签，其余写成 +N（选中后详情里有全部）。
const refChipsMax = 2

// refChips 画提交上的分支和标签：当前分支是强调色淡底。它占标题后面剩下的地方（代替 Spacer），
// 标题不为它让位；按上一帧分到的宽度只列整个放得下的，其余写成 +N，不切掉半个、也不缩成"…"。
func refChips(c *ui.Context, refs string) {
	t := c.Theme()
	names := refNames(refs)
	box := ui.Row(c.Key("refs")).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(6).AlignItems(ui.Center).Clip()
	if len(names) == 0 {
		return
	}
	shown := refsFit(c, names, box.Bounds().W)
	box.Children(func() {
		for _, ref := range names[:shown] {
			bg, fg := t.Surface, t.TextMuted
			if ref.head {
				bg, fg = colors(t).tint, t.Accent
			}
			ui.Text(c, ref.name).FontSize(fsSmall).Font(monoFont).TextColor(fg).Padding(2, refPadX).Radius(rSmall).Background(bg).SingleLine().Shrink(0).MaxWidth(refChipMaxW)
		}
		if shown < len(names) {
			ui.Text(c, fmt.Sprintf("+%d", len(names)-shown)).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0).Tooltip(refs)
		}
	})
}

const (
	refPadX     = 6
	refChipMaxW = 160
)

// refsFit 是 room 宽里能整个列出几个标签（最多 refChipsMax 个），列不全时还要给 +N 留地方；
// room 为 0 是第一帧还没排过，照最多的列，放不下的被裁掉。
func refsFit(c *ui.Context, names []refName, room float32) int {
	most := min(len(names), refChipsMax)
	if room <= 0 {
		return most
	}
	plus := func(k int) float32 {
		if k == len(names) {
			return 0
		}
		w, _ := c.MeasureText(0, ui.Span{Text: fmt.Sprintf("+%d", len(names)-k), Size: fsSmall})
		return w + 6
	}
	used := float32(0)
	for k := 0; k < most; k++ {
		w, _ := c.MeasureText(0, ui.Span{Text: names[k].name, Font: monoFont, Size: fsSmall})
		used += min(w+2*refPadX, refChipMaxW)
		if k > 0 {
			used += 6
		}
		if used+plus(k+1) > room {
			return k
		}
	}
	return most
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
func (a *app) commitMenu(c *ui.Context, m *popMenu, w *workspace, v *filesView, d *fileDoc, ci gitx.CommitInfo) {
	lm := L(c)
	short := ci.Hash[:7]
	// 做完由 gitDo 里的 refsChanged 重读日志，翻过的页数一起读回来
	do := func(what string, fn func(ctx context.Context, root string) error) {
		a.guardAgent(lm, w, what, func() { a.gitDo(lm, v.repoOrGone(d.log.root), what, fn) })
	}
	if m.Item(lm.LogCopyHash()).Chosen() {
		c.WriteClipboard(ci.Hash)
	}
	m.Separator()
	if m.Item(lm.LogCheckoutCommit()).Chosen() {
		do(lm.BranchCheckout(), func(ctx context.Context, root string) error { return gitx.CheckoutRevision(ctx, root, ci.Hash) })
	}
	if m.Item(lm.LogNewBranch()).Chosen() {
		a.askNewBranch(lm, w, v, ci.Hash)
	}
	m.Separator()
	if m.Item("Cherry-pick").Icon(iconCherryPick).Chosen() {
		do("Cherry-pick", func(ctx context.Context, root string) error { return gitx.CherryPick(ctx, root, ci.Hash) })
	}
	m.Submenu(lm.LogResetHere(), func(sm *popMenu) {
		if sm.Item(lm.LogSoftReset()).Chosen() {
			do(lm.LogReset(), func(ctx context.Context, root string) error { return gitx.Reset(ctx, root, ci.Hash, "soft") })
		}
		if sm.Item(lm.LogMixedReset()).Chosen() {
			do(lm.LogReset(), func(ctx context.Context, root string) error { return gitx.Reset(ctx, root, ci.Hash, "mixed") })
		}
		if sm.Item(lm.LogHardResetEllipsis()).Chosen() {
			a.confirm(lm.LogHardResetConfirm(short), lm.LogHardResetBody(), lm.LogHardReset(), func() {
				a.update(func() {
					do(lm.LogReset(), func(ctx context.Context, root string) error { return gitx.Reset(ctx, root, ci.Hash, "hard") })
				})
			})
		}
	})
	if m.Item(lm.LogRevertCommit()).Chosen() {
		do("Revert", func(ctx context.Context, root string) error { return gitx.RevertCommit(ctx, root, ci.Hash) })
	}
	m.Separator()
	if m.Item(lm.LogRewordEllipsis()).Chosen() {
		a.guardPushed(lm, d.log.root, ci.Hash, func() {
			a.askMessage(lm, d.log.root, lm.LogRewordTitle(short), ci.Hash, "", func(msg string) {
				do(lm.LogReword(), func(ctx context.Context, root string) error { return gitx.Reword(ctx, root, ci.Hash, msg) })
			})
		})
	}
	if len(ci.Parents) == 1 && m.Item(lm.LogSquashEllipsis()).Chosen() {
		a.guardPushed(lm, d.log.root, ci.Parents[0], func() {
			a.askMessage(lm, d.log.root, lm.LogSquashTitle(), ci.Parents[0], ci.Hash, func(msg string) {
				do(lm.LogSquash(), func(ctx context.Context, root string) error { return gitx.SquashIntoParent(ctx, root, ci.Hash, msg) })
			})
		})
	}
	if m.Item(lm.LogDropEllipsis()).Chosen() {
		a.guardPushed(lm, d.log.root, ci.Hash, func() {
			a.confirm(lm.LogDropConfirm(short), lm.LogDropBody(ci.Subject), lm.LogDrop(), func() {
				a.update(func() {
					do(lm.LogDrop(), func(ctx context.Context, root string) error { return gitx.Drop(ctx, root, ci.Hash) })
				})
			})
		})
	}
}

// guardPushed 在改写从 oldest 开始的历史之前查它推送过没有：推送过的先说明之后要强制推送，确认了再继续。
func (a *app) guardPushed(m locales.Messages, root, oldest string, then func()) {
	go func() {
		remotes := gitx.RemotesContaining(context.Background(), root, oldest)
		a.update(func() {
			if len(remotes) == 0 {
				then()
				return
			}
			where := remotes[0]
			if len(remotes) > 1 {
				where = m.LogRemotesMore(remotes[0], len(remotes))
			}
			a.confirm(m.LogPushedTitle(), m.LogPushedBody(where), m.LogRewriteAnyway(), func() {
				a.update(then)
			})
		})
	}()
}

// askMessage 读出提交信息（两个提交时拼在一起）填进输入框，让用户改完再执行。
func (a *app) askMessage(m locales.Messages, root, title, hash, second string, do func(string)) {
	go func() {
		ctx := context.Background()
		msg := gitx.FullMessage(ctx, root, hash)
		if second != "" {
			msg += "\n\n" + gitx.FullMessage(ctx, root, second)
		}
		a.update(func() {
			a.prompt(promptReq{title: title, initial: msg, verb: m.LogMessageConfirm(), multiline: true, do: func(s string, _ bool) { do(s) }})
		})
	}()
}

// --- Annotate

// blameDoc 是文件标签里开着的 Annotate：每行最后改它的提交，同一个提交的连续几行只在第一行写作者和日期。
// 和 WebStorm 一样在同一个标签里开关，不另开标签。
type blameDoc struct {
	root    string // 仓库根目录
	rel     string
	lines   []gitx.BlameLine
	loading bool
	err     string
	list    ui.ListState
	sel     *commitView
	// 查找栏的命中：每个命中所在的行，按行排；hitQ 是算它们用的搜索词
	hits []int
	hitQ string
}

// blameText 是 Annotate 里一行显示的文字，查找也按它算。
func blameText(l gitx.BlameLine) string { return strings.ReplaceAll(l.Text, "\t", "    ") }

// findHits 按查找栏的词重算命中（行是按需搭的，不能边搭边数），要滚动时把当前命中的行滚进视野。
func (bd *blameDoc) findHits(f *docFind) {
	q := ""
	if w := f.words(); w != nil {
		q = w[0]
	}
	if q != bd.hitQ {
		bd.hitQ, bd.hits = q, nil
		if q != "" {
			for i, l := range bd.lines {
				for range markRanges(blameText(l), []string{q}) {
					bd.hits = append(bd.hits, i)
				}
			}
		}
	}
	f.begin()
	f.total = len(bd.hits)
	f.end()
	if f.scroll && f.n > 0 {
		bd.list.ScrollIntoView(bd.hits[f.cur])
		f.scroll = false
	}
}

// openBlame 在这个文件的标签里打开 Annotate（文件没开着就先打开）。
func (a *app) openBlame(m locales.Messages, w *workspace, v *filesView, abs string) {
	g, rel := v.relIn(abs)
	if g == nil {
		a.err = m.LogFileNotRepo()
		return
	}
	a.openFile(w, abs, 0, [2]int{})
	v = a.filesFor(w)
	d := v.cur
	if d == nil || d.path != filepath.Clean(abs) {
		return
	}
	root := g.repo
	bd := &blameDoc{root: root, rel: rel, loading: true}
	d.blame = bd
	go func() {
		lines, err := gitx.Blame(context.Background(), root, rel)
		a.update(func() {
			bd.loading = false
			if err != nil {
				bd.err = errtext.Text(err)
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
		ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(16, 16)
		return
	}
	f := &d.find
	f.shortcuts(c, a)
	bd.findHits(f)
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		bd.list.Key = func(i int) any { return i }
		ui.Box(c).Grow(1).Children(func() {
			ui.List(c, &bd.list, len(lines), func(i int) {
				l := lines[i]
				first := i == 0 || lines[i-1].Hash != l.Hash
				uncommitted := strings.Trim(l.Hash, "0") == ""
				row := ui.Row(c.Key(i)).Height(sz(22)).Gap(8).AlignItems(ui.Center)
				if !uncommitted && bd.sel != nil && bd.sel.info.Hash == l.Hash {
					row.Background(colors(t).tint)
				}
				row.Children(func() {
					who := ui.Row(c.Key("who")).Width(190).Height(sz(22)).Padding(0, 8).Gap(6).AlignItems(ui.Center).Shrink(0).Background(colors(t).wash)
					who.Children(func() {
						if !first {
							return
						}
						if uncommitted {
							ui.Text(c, L(c).LogUncommitted()).FontSize(fsSmall).TextColor(t.Accent)
							return
						}
						ui.Text(c, l.Time.Format("2006-01-02")).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
						ui.Text(c, l.Author).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
					})
					if !uncommitted {
						who.Tooltip(l.Hash[:10] + "  " + l.Author + "\n" + l.Summary)
						if who.Clicked() {
							a.selectCommit(bd.root, &bd.sel, gitx.CommitInfo{Hash: l.Hash, Author: l.Author, Time: l.Time, Subject: l.Summary})
						}
					}
					ui.Text(c, fmt.Sprint(i+1)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Width(sz(36)).Shrink(0).TextAlign(ui.End)
					blameLine(c, bd, f, i).FontSize(a.editorSize).Font(fontStack(a.editorFamily)).SingleLine().Shrink(1)
				})
			}).Fill()
			f.bar(c, a)
		})
		if bd.sel != nil {
			a.paneDivider(c, "log-detail-grip", &a.state.LogDetailWidth, logDetailPane, -1)
			ui.Column(c).Width(paneWidth(a.state.LogDetailWidth, logDetailPane)).Shrink(0).Children(func() { a.commitDetail(c, w, v, bd.sel, false, nil) })
		}
	})
}

// blameLine 是第 i 行的文字，查找的命中铺底色，当前那个深一点。
func blameLine(c *ui.Context, bd *blameDoc, f *docFind, i int) ui.Element {
	text := blameText(bd.lines[i])
	marks := markRanges(text, f.words())
	if len(marks) == 0 {
		return ui.Text(c, text)
	}
	p := colors(c.Theme())
	spans := markSpans(text, marks, ui.Span{}, p.mark)
	// 这一行第一个命中的编号；markSpans 的结果里命中的段在奇数位置
	if k := f.cur - slices.Index(bd.hits, i); k >= 0 && k < len(marks) {
		spans[2*k+1].Background = p.markOn
	}
	return ui.RichText(c, spans...)
}

// gitFileMenu 是文件在 Git 下的操作：文件历史、Annotate。用在文件树和打开的文件上。
func (a *app) gitFileMenu(lm locales.Messages, m *popMenu, w *workspace, v *filesView, abs string) {
	g, rel := v.relIn(abs)
	if g == nil {
		return
	}
	m.Separator()
	if m.Item(lm.LogFileHistory()).Chosen() {
		a.openLogIn(w, g, "", rel)
	}
	if m.Item(lm.LogAnnotate()).Chosen() {
		a.openBlame(lm, w, v, abs)
	}
}
