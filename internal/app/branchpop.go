package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/gitx"
)

// 分支弹窗：点标签栏的分支名打开，结构照 WebStorm 的 Git 分支弹窗（见 docs/features/git.md）。

type branchPopup struct {
	ws    string
	query string
	// 搜索框里 ↑↓ 选中的分支（在 rows 里的位置，-1 是没选），回车检出；搜索时默认选第一个
	hl     int
	rows   []gitx.Branch // 上一帧列出来的分支，按显示顺序
	scroll bool          // 下一帧把选中的分支滚进视野
}

// branchInfo 是分支弹窗要的数据，打开弹窗、Fetch、操作之后在后台重读。
type branchInfo struct {
	list    []gitx.Branch
	recent  []string
	tags    []string // Git 日志左边的"标签"
	loading bool
	again   bool // 读的期间又要求重读（期间做了操作），读完再读一次
}

// openBranches 打开工作区的分支弹窗，并在后台读分支。
func (a *app) openBranches(w *workspace) {
	v := a.filesFor(w)
	a.branchPop = &branchPopup{ws: w.ID, hl: -1}
	a.loadBranches(v)
}

func (a *app) loadBranches(v *filesView) {
	g := &v.git
	if g.branches.loading {
		g.branches.again = true
		return
	}
	g.branches.loading = true
	ws, root := v.root, g.repo
	go func() {
		ctx := context.Background()
		if root == "" {
			root = repoRoot(ws, gitx.TopLevel(ctx, ws))
		}
		var list []gitx.Branch
		var recent, tags []string
		if root != "" {
			list, _ = gitx.Branches(ctx, root)
			recent = gitx.RecentBranches(ctx, root, 6)
			tags, _ = gitx.Tags(ctx, root)
		}
		a.update(func() {
			again := g.branches.again
			g.branches = branchInfo{list: list, recent: recent, tags: tags}
			if !g.checked {
				g.checked, g.repo = true, root
			}
			if again {
				a.loadBranches(v)
			}
		})
	}()
}

// trackInfo 是当前分支相对跟踪的远端分支的传入、传出提交数。
type trackInfo struct {
	ahead, behind int
	base          gitx.Divergence // worktree 相对基准分支，见 worktreebase.go
	baseOK        bool
	poll          panelPoll
}

// trackRefresh 是分支按钮多久重算一次传入、传出；只用本地已有的远端引用，几毫秒。
const trackRefresh = 10 * time.Second

// pollTrack 在分支按钮画出来时调用，到点了在后台重算。
func (a *app) pollTrack(c *ui.Context, w *workspace) *trackInfo {
	if a.track == nil {
		a.track = map[string]*trackInfo{}
	}
	tr := a.track[w.ID]
	if tr == nil {
		tr = &trackInfo{}
		a.track[w.ID] = tr
	}
	a.autoFetch(c, w, tr)
	if tr.poll.due(c, trackRefresh) {
		dir, ws, prev := w.Path, *w, tr.base
		go func() {
			ahead, behind, _ := gitx.AheadBehind(context.Background(), dir)
			base, baseOK := pollBase(context.Background(), &ws, &prev)
			a.update(func() {
				tr.ahead, tr.behind = ahead, behind
				tr.base, tr.baseOK = base, baseOK
				tr.poll.done()
			})
		}()
	}
	return tr
}

// autoFetchEvery 是多久自动 fetch 一次。只拉看得见的工作区所在的项目，同一项目的 worktree 共用远端引用，按项目计时。
const autoFetchEvery = 5 * time.Minute

// autoFetch 到点了在后台 fetch，失败不报错，下次再试。进行中占着改动页的 busy（分支弹窗的按钮转圈），
// 免得和用户手动的 git 操作同时改引用。
func (a *app) autoFetch(c *ui.Context, w *workspace, tr *trackInfo) {
	if a.fetchAt == nil {
		a.fetchAt = map[string]time.Time{}
	}
	last, ok := a.fetchAt[w.ProjectID]
	if wait := autoFetchEvery - time.Since(last); ok && wait > 0 {
		c.After(wait)
		return
	}
	// 同一项目的 worktree 共用远端引用：fetch 期间把它们开着的改动页都占住，那边手动的 git 操作排队，不同时改引用
	var views []*filesView
	for i := range a.workspaces {
		if a.workspaces[i].ProjectID != w.ProjectID {
			continue
		}
		if v := a.files[a.workspaces[i].ID]; v != nil {
			if v.git.busy != "" {
				c.After(time.Second)
				return
			}
			views = append(views, v)
		}
	}
	a.fetchAt[w.ProjectID] = time.Now()
	ctx, cancel := context.WithCancel(gitx.WithOp(context.Background(), "自动 Fetch"))
	for _, v := range views {
		v.git.busy, v.git.cancel = "Fetch", cancel
	}
	dir, project := w.Path, w.ProjectID
	go func() {
		gitx.Fetch(ctx, dir)
		cancel()
		a.update(func() {
			tr.poll = panelPoll{}
			a.refsChanged(project)
			for _, v := range views {
				if v.git.busy == "Fetch" {
					v.git.busy, v.git.cancel = "", nil
					a.gitNext(v)
				}
			}
		})
	}()
}

// trackTip 是分支按钮的悬停提示：分支名和传入、传出的提交数。
func trackTip(branch string, tr *trackInfo) string {
	tip := "Git 分支：" + branch
	if tr.behind > 0 {
		tip += fmt.Sprintf("\n%d 个传入提交", tr.behind)
	}
	if tr.ahead > 0 {
		tip += fmt.Sprintf("\n%d 个传出提交", tr.ahead)
	}
	return tip
}

// incomingColor 是传入提交的蓝色（sky），传出用 sage 绿，和 WebStorm 一样分开。
func incomingColor(t *ui.Theme) ui.Color { return colors(t).sky }

// trackBadge 是传入（↙ 蓝）、传出（↗ 绿）的箭头和数字；showCount 为假时只画箭头（标签栏的分支按钮）。
func trackBadge(c *ui.Context, ahead, behind int, showCount bool) {
	t := c.Theme()
	item := func(icon *ui.SVG, color ui.Color, n int, tip string) {
		ui.Row(c).Gap(2).AlignItems(ui.Center).Shrink(0).Tooltip(tip).Label(tip).Children(func() {
			ui.Icon(c, icon).FontSize(fsSmall).TextColor(color).Shrink(0)
			if showCount {
				ui.Text(c, strconv.Itoa(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(color)
			}
		})
	}
	if behind > 0 {
		item(iconFetch, incomingColor(t), behind, fmt.Sprintf("%d 个传入提交（远端有、还没拉取）", behind))
	}
	if ahead > 0 {
		item(iconPush, colors(t).sage, ahead, fmt.Sprintf("%d 个传出提交（本地有、还没推送）", ahead))
	}
}

// branchButton 是标签栏右边的分支名，点开分支弹窗；有传入、传出提交时名字后面带箭头。
func (a *app) branchButton(c *ui.Context, w *workspace) {
	t := c.Theme()
	tr := a.pollTrack(c, w)
	b := ui.Row(c.Key("branch")).Height(sz(26)).Padding(0, 6, 0, 8).Gap(4).Radius(rRow).AlignItems(ui.Center).Shrink(1).
		Role(ui.RoleButton).Label("分支 " + w.Branch).Tooltip(trackTip(w.Branch, tr) + baseTip(w.BaseRef, tr))
	open := a.branchPop != nil && a.branchPop.ws == w.ID
	bg := ui.Transparent
	switch {
	case open:
		bg = colors(t).tint
	case b.Hovered():
		bg = colors(t).wash
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	if b.Hovered() {
		a.overPanelButton = true
	}
	b.Children(func() {
		ui.Icon(c, iconBranch).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, w.Branch).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		trackBadge(c, tr.ahead, tr.behind, false)
		ui.Icon(c, iconDown).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
	})
	if b.Clicked() {
		if open {
			a.branchPop = nil
		} else {
			// 两个按钮都算"面板按钮"，点它们不会触发对方的点外面收起，只能在这里互相关掉
			if a.state.RightPanel != "" {
				a.state.RightPanel = ""
				a.saveState()
			}
			a.openBranches(w)
		}
	}
}

// branchPopupView 画分支弹窗，浮在主区域右上角，点外面或 Esc 关闭。
func (a *app) branchPopupView(c *ui.Context, w *workspace) {
	bp := a.branchPop
	if bp == nil || bp.ws != w.ID {
		return
	}
	v := a.filesFor(w)
	g := &v.git
	panel := floating(c, ui.Column(c).Absolute().Top(6).Right(8).Width(a.branchPopWidth()).MaxHeight(600).Padding(6).Gap(2))
	panel.Children(func() {
		ui.Row(c).Gap(4).Padding(2, 2, 6, 2).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Children(func() {
				in := fieldInput(c, &bp.query, "搜索分支").AutoFocus()
				switch {
				case in.Changed():
					bp.hl = -1
					if strings.TrimSpace(bp.query) != "" {
						bp.hl = 0
					}
				case in.Shortcut(0, ui.KeyDown):
					bp.hl, bp.scroll = min(bp.hl+1, len(bp.rows)-1), true
				case in.Shortcut(0, ui.KeyUp):
					bp.hl, bp.scroll = max(bp.hl-1, 0), true
				case in.Submitted() && bp.hl >= 0 && bp.hl < len(bp.rows):
					a.checkoutBranch(w, v, bp.rows[bp.hl])
				}
			})
			// Fetch 进行中按钮换成转圈，不另起一行文字
			if g.busy == "Fetch" {
				spin := ui.Row(c.Key("fetching")).Size(buttonBox(), buttonBox()).Shrink(0).Justify(ui.Center).AlignItems(ui.Center).
					Role(ui.RoleButton).Label("取消 Fetch").Tooltip("正在 Fetch，点一下取消")
				spin.Children(func() { ui.Spinner(c).Label("正在 Fetch") })
				if spin.Clicked() && g.cancel != nil {
					g.cancel()
				}
			} else if fetch := pressable(iconButtonSized(c, iconFetch, "Fetch：从所有远端拉取（每 5 分钟自动一次）", buttonBox(), iconSize)); g.busy == "" && fetch.Clicked() {
				a.gitDoThen(v, "Fetch", func(ctx context.Context, root string) error { return gitx.Fetch(ctx, root) },
					func() { a.notify("Fetch 完成", "", nil) })
			} else if g.busy != "" {
				fetch.Opacity(0.4)
			}
		})
		if g.busy != "" && (g.busy != "Fetch" || len(g.queue) > 0) {
			ui.Row(c).Padding(0, 12, 4, 12).Children(func() { busyLine(c, g) })
		}
		query := strings.ToLower(strings.TrimSpace(bp.query))
		bp.rows = bp.rows[:0]
		ui.Scroll(c).Grow(1).Shrink(1).Children(func() {
			ui.Column(c).Gap(2).Children(func() {
				if query == "" {
					a.branchActions(c, w, v)
					hline(c).Margin(4, 6)
				}
				a.branchGroups(c, w, v, query)
			})
		})
		a.branchPopGrip(c)
	})
	if panel.PressedOutside() && !a.overPanelButton || panel.Shortcut(0, ui.KeyEscape) {
		a.branchPop = nil
	}
}

const (
	branchPopDefault = 420
	branchPopMin     = 360
	branchPopMax     = 800
)

func (a *app) branchPopWidth() float32 {
	if w := a.state.BranchPopWidth; w > 0 {
		return min(max(w, branchPopMin), branchPopMax)
	}
	return branchPopDefault
}

// branchPopGrip 是分支弹窗左边缘调宽的把手：弹窗贴着右边，往左拖变宽，长分支名和远端名看得全；松手才写 ui.json。
func (a *app) branchPopGrip(c *ui.Context) {
	grip := ui.Box(c.Key("branch-pop-grip")).Absolute().Top(0).Bottom(0).Left(0).Width(6).Cursor(ui.CursorResizeEW)
	if dx, _, held := grip.Dragged(); held {
		a.state.BranchPopWidth = min(max(a.branchPopWidth()-dx, branchPopMin), branchPopMax)
		a.resizingBranch = true
	} else if a.resizingBranch {
		a.resizingBranch = false
		a.saveState()
	}
	if grip.Hovered() || a.resizingBranch {
		accent := c.Theme().Accent
		grip.Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(ui.Rect{X: r.X + 2, Y: r.Y + 12, W: 2, H: r.H - 24}, accent, 1)
		})
	}
}

// popupItem 是弹窗里的一行操作，返回这一帧是否点了它。
func popupItem(c *ui.Context, icon *ui.SVG, label, keys string) bool {
	t := c.Theme()
	row := ui.Row(c.Key(label)).Height(sz(28)).Padding(0, 12).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(label)
	bg := ui.Transparent
	if row.Hovered() {
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).FontSize(fsBody).TextColor(t.TextMuted).Shrink(0)
		} else {
			ui.Box(c).Width(14).Shrink(0)
		}
		ui.Text(c, label).FontSize(fsBody).Grow(1)
		if keys != "" {
			ui.Text(c, keys).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		}
	})
	return row.Clicked()
}

func (a *app) branchActions(c *ui.Context, w *workspace, v *filesView) {
	g := &v.git
	cur, _ := currentBranch(g.branches.list)
	if popupItem(c, iconFetch, "更新项目", "") {
		a.branchPop = nil
		if cur.Upstream == "" {
			a.err = "当前分支没有跟踪的远端分支，没法更新"
		} else {
			a.guardAgent(w, "更新项目", func() {
				a.updateBranch(v, "更新项目", cur)
			})
		}
	}
	if popupItem(c, iconCommit, "提交…", "⌘K") {
		a.branchPop = nil
		a.openFilesPanel(w, sideChanges).git.focusMsg = true
	}
	if popupItem(c, iconPush, "Push…", "⇧⌘K") {
		a.branchPop = nil
		a.openPush(w)
	}
	hline(c).Margin(4, 6)
	if popupItem(c, iconPlus, "新建分支…", "⌥⌘N") {
		a.branchPop = nil
		a.askNewBranch(w, v, "")
	}
	if popupItem(c, nil, "检出标签或修订…", "") {
		a.branchPop = nil
		a.prompt(promptReq{title: "检出标签或修订", placeholder: "标签名或提交哈希", verb: "检出", do: func(rev string, _ bool) {
			a.guardAgent(w, "检出", func() {
				a.gitDo(v, "检出", func(ctx context.Context, root string) error { return gitx.CheckoutRevision(ctx, root, rev) })
			})
		}})
	}
}

func currentBranch(list []gitx.Branch) (gitx.Branch, bool) {
	i := slices.IndexFunc(list, func(b gitx.Branch) bool { return b.Current })
	if i < 0 {
		return gitx.Branch{}, false
	}
	return list[i], true
}

// branchGroups 画最近、本地、远端三组分支；远端按远端名分组。query 非空时只列名字含它的。
func (a *app) branchGroups(c *ui.Context, w *workspace, v *filesView, query string) {
	t := c.Theme()
	g := &v.git
	list := g.branches.list
	if list == nil {
		ui.Text(c, "正在读取分支…").FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 12)
		return
	}
	match := func(b gitx.Branch) bool { return query == "" || strings.Contains(strings.ToLower(b.Name), query) }
	// header 是一组的标题，点了折叠或展开；最近和本地默认展开，远端默认收起，搜索时全部展开
	header := func(key, title string, n int) bool {
		open, set := a.branchFolds[key]
		if !set {
			open = !strings.HasPrefix(key, "remote:")
		}
		row := ui.Row(c.Key("fold:"+key)).Height(sz(26)).Padding(6, 12, 0, 8).Gap(4).Radius(rRow).AlignItems(ui.Center).
			Role(ui.RoleButton).Label(title)
		if row.Hovered() {
			row.Background(colors(t).wash)
		}
		row.Children(func() {
			chevron := iconChevron
			if open || query != "" {
				chevron = iconDown
			}
			ui.Icon(c, chevron).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, title).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			ui.Text(c, strconv.Itoa(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
		})
		if row.Clicked() && query == "" {
			if a.branchFolds == nil {
				a.branchFolds = map[string]bool{}
			}
			a.branchFolds[key] = !open
			open = !open
		}
		return open || query != ""
	}
	var recent []gitx.Branch
	if query == "" {
		for _, name := range g.branches.recent {
			if i := slices.IndexFunc(list, func(b gitx.Branch) bool { return !b.Remote && b.Name == name }); i >= 0 {
				recent = append(recent, list[i])
			}
		}
	}
	if len(recent) > 0 && header("recent", "最近", len(recent)) {
		for _, b := range recent {
			a.branchRow(c, w, v, b, "recent")
		}
	}
	var local []gitx.Branch
	remotes := map[string][]gitx.Branch{}
	var remoteNames []string
	for _, b := range list {
		if !match(b) {
			continue
		}
		if !b.Remote {
			local = append(local, b)
			continue
		}
		r, _, _ := strings.Cut(b.Name, "/")
		if remotes[r] == nil {
			remoteNames = append(remoteNames, r)
		}
		remotes[r] = append(remotes[r], b)
	}
	if len(local) > 0 && header("local", "本地", len(local)) {
		for _, b := range local {
			a.branchRow(c, w, v, b, "local")
		}
	}
	for _, r := range remoteNames {
		if !header("remote:"+r, "远端 "+r, len(remotes[r])) {
			continue
		}
		for _, b := range remotes[r] {
			a.branchRow(c, w, v, b, "remote")
		}
	}
	if query != "" && len(local) == 0 && len(remoteNames) == 0 {
		ui.Text(c, "没有名字里含“"+query+"”的分支").FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 12)
	}
}

// branchRow 是弹窗里的一个分支：当前分支加粗，后面是待推送 / 待拉取的提交数，右侧是跟踪的远端分支，
// 或者它检出在哪个工作区。点开是操作菜单，顺序照 WebStorm。
func (a *app) branchRow(c *ui.Context, w *workspace, v *filesView, b gitx.Branch, group string) {
	t := c.Theme()
	p := colors(t)
	g := &v.git
	cur, _ := currentBranch(g.branches.list)
	other, inOther := gitx.WorktreeOf(b, g.repo)
	otherWs := a.workspaceAt(other)
	// 左边多缩进一些，分支图标和组标题的文字对齐，一眼看出属于哪一组
	row := ui.Row(c.Key(group+":"+b.Name)).Height(sz(28)).Padding(0, 12, 0, 24).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(b.Name)
	bp := a.branchPop
	keyed := bp != nil && bp.hl == len(bp.rows)
	if bp != nil {
		bp.rows = append(bp.rows, b)
	}
	if keyed && bp.scroll {
		row.ScrollIntoView()
		bp.scroll = false
	}
	bg := ui.Transparent
	if row.Hovered() || keyed {
		bg = p.wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		color := t.TextMuted
		if b.Current {
			color = t.Accent
		}
		ui.Icon(c, iconBranch).FontSize(fsBody).TextColor(color).Shrink(0)
		name := ui.Text(c, b.Name).FontSize(fsCode).Font(monoFont).SingleLine().Shrink(1)
		if b.Current {
			name.FontWeight(fwMedium)
		}
		trackBadge(c, b.Ahead, b.Behind, true)
		ui.Spacer(c)
		switch {
		case inOther && otherWs != nil:
			ui.Text(c, "在 "+displayName(otherWs)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
		case inOther:
			// 不是 posse 建的 worktree（别的工具或命令行建的）：报目录名，悬停看完整路径
			ui.Text(c, "worktree · "+filepath.Base(other)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(other)
		case b.Gone:
			ui.Text(c, "远端已删除").FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		case b.Upstream != "":
			ui.Text(c, b.Upstream).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
	})
	menuButton(c, row, func(m *popMenu) { a.branchMenu(c, m, w, v, b, cur, other, otherWs, inOther) })
}

// checkoutBranch 是分支弹窗里回车或菜单"检出"：在别的工作区检出着的切到那个工作区，当前分支什么也不做。
func (a *app) checkoutBranch(w *workspace, v *filesView, b gitx.Branch) {
	a.branchPop = nil
	if b.Current {
		return
	}
	if other, inOther := gitx.WorktreeOf(b, v.git.repo); inOther {
		if ws := a.workspaceAt(other); ws != nil {
			a.selected, a.revealSel = ws.ID, true
		} else {
			a.attachWorktree(w, other)
		}
		return
	}
	a.guardAgent(w, "检出", func() {
		a.gitDo(v, "检出", func(ctx context.Context, root string) error { return gitx.Checkout(ctx, root, b) })
	})
}

// branchMenu 是一个分支的操作，顺序照 WebStorm。
func (a *app) branchMenu(c *ui.Context, m *popMenu, w *workspace, v *filesView, b, cur gitx.Branch, other string, otherWs *workspace, inOther bool) {
	done := func() { a.branchPop = nil }
	q := "‘" + b.Name + "’"
	curName := cur.Name
	if curName == "" {
		curName = "HEAD"
	}
	cq := "‘" + curName + "’"
	if !b.Current {
		switch {
		case inOther && otherWs != nil:
			if m.Item("切到它所在的工作区 " + displayName(otherWs)).Chosen() {
				done()
				a.selected, a.revealSel = otherWs.ID, true
			}
		case inOther:
			if m.Item("在 Posse 中打开").Chosen() {
				done()
				a.attachWorktree(w, other)
			}
			if m.Item("在访达中显示 worktree").Chosen() {
				done()
				mygo.Shell.ShowItemInFolder(other)
			}
			if m.Item("复制 worktree 路径").Chosen() {
				done()
				c.WriteClipboard(other)
			}
		default:
			if m.Item("检出").Chosen() {
				a.checkoutBranch(w, v, b)
			}
		}
	}
	if m.Item("基于 " + q + " 新建分支…").Chosen() {
		done()
		a.askNewBranch(w, v, b.Name)
	}
	if !b.Current && !b.Remote && !inOther && m.Item("检出并 rebase 到 "+cq).Chosen() {
		done()
		a.guardAgent(w, "检出并 rebase", func() {
			a.gitDo(v, "检出并 rebase", func(ctx context.Context, root string) error {
				return gitx.CheckoutAndRebase(ctx, root, b.Name, curName)
			})
		})
	}
	if !b.Current {
		m.Separator()
		if m.Item("和 " + cq + " 比较").Chosen() {
			done()
			a.openCompare(w, v, b.Name, curName, false)
		}
		if m.Item("和工作区比较").Chosen() {
			done()
			a.openCompare(w, v, b.Name, curName, true)
		}
		m.Separator()
		if m.Item("把 " + cq + " rebase 到 " + q + " 上").Chosen() {
			done()
			a.guardAgent(w, "rebase", func() {
				a.gitDo(v, "rebase", func(ctx context.Context, root string) error { return gitx.Rebase(ctx, root, b.Name) })
			})
		}
		if m.Item("把 " + q + " 合并到 " + cq).Chosen() {
			done()
			a.guardAgent(w, "合并", func() {
				a.gitDo(v, "合并", func(ctx context.Context, root string) error { return gitx.MergeInto(ctx, root, b.Name) })
			})
		}
	}
	m.Separator()
	if m.Item("基于 " + q + " 新建工作区…").Chosen() {
		done()
		a.askWorktreeFrom(w, b.Name)
	}
	m.Separator()
	if m.Item("在日志里显示").Chosen() {
		done()
		a.openLog(w, branchRef(b), "")
	}
	if !b.Remote && b.Upstream != "" && m.Item("更新").Chosen() {
		done()
		if b.Current {
			a.guardAgent(w, "更新", func() {
				a.updateBranch(v, "更新", b)
			})
		} else {
			a.updateBranch(v, "更新 "+b.Name, b)
		}
	}
	if !b.Remote && m.Item("Push…").Chosen() {
		done()
		a.openPushBranch(w, b.Name)
	}
	m.Separator()
	if !b.Remote && m.Item("重命名…").Chosen() {
		done()
		a.prompt(promptReq{title: "重命名分支 " + b.Name, initial: b.Name, verb: "重命名", do: func(name string, _ bool) {
			a.gitDo(v, "重命名", func(ctx context.Context, root string) error { return gitx.RenameBranch(ctx, root, b.Name, name) })
		}})
	}
	// 本地分支直接删、可以恢复，不再弹确认；远端分支要确认
	del := "删除"
	if b.Remote {
		del = "删除…"
	}
	if !b.Current && !inOther && m.Item(del).Chosen() {
		done()
		a.askDeleteBranch(v, b)
	}
}

// updateBranch 用远端更新分支，做完提示拉到了几个提交。
func (a *app) updateBranch(v *filesView, what string, b gitx.Branch) {
	var n int
	a.gitDoThen(v, what, func(ctx context.Context, root string) error {
		before := gitx.Tip(ctx, root, "refs/heads/"+b.Name)
		err := gitx.Update(ctx, root, b)
		if err == nil {
			n = gitx.Incoming(ctx, root, before, "refs/remotes/"+b.Upstream)
		}
		return err
	}, func() {
		if n == 0 {
			a.notify(b.Name+" 已经是最新的", "", nil)
		} else {
			a.notify(fmt.Sprintf("%s 拉到了 %d 个提交", b.Name, n), "", nil)
		}
	})
}

// askDeleteBranch 删除分支，照 WebStorm：已经合并的本地分支直接删，提示里可以恢复；没合并的先问一次是否强制删除。
// 远端分支删的是远端上的，别人也拉不到了，先确认。
func (a *app) askDeleteBranch(v *filesView, b gitx.Branch) {
	if b.Remote {
		a.confirm("删除远端分支 "+b.Name+"？", "会推送到远端，把远端上的这个分支删掉，其他人也拉不到了。", "删除", func() {
			a.update(func() {
				a.gitDo(v, "删除远端分支", func(ctx context.Context, root string) error { return gitx.DeleteRemoteBranch(ctx, root, b.Name) })
			})
		})
		return
	}
	var tip string
	deleted := func() {
		a.notify("已删除分支 "+b.Name, "恢复", func() {
			a.gitDo(v, "恢复分支", func(ctx context.Context, root string) error { return gitx.CreateBranch(ctx, root, b.Name, tip, false) })
		})
	}
	var unmerged bool
	a.gitDoThen(v, "删除分支", func(ctx context.Context, root string) error {
		tip = gitx.Tip(ctx, root, "refs/heads/"+b.Name)
		err := gitx.DeleteBranch(ctx, root, b.Name, false)
		if gitx.NotMerged(err) {
			unmerged = true
			return nil
		}
		return err
	}, func() {
		if !unmerged {
			deleted()
			return
		}
		a.confirm("分支 "+b.Name+" 还没合并", "它上面有别的分支里没有的提交。强制删除后可以在提示里马上恢复，过后就只能靠记得的哈希找回。", "强制删除", func() {
			a.update(func() {
				a.gitDoThen(v, "强制删除分支", func(ctx context.Context, root string) error { return gitx.DeleteBranch(ctx, root, b.Name, true) }, deleted)
			})
		})
	})
}

func (a *app) askNewBranch(w *workspace, v *filesView, from string) {
	title := "新建分支"
	if from != "" {
		title = "基于 " + from + " 新建分支"
	}
	a.prompt(promptReq{title: title, placeholder: "分支名", verb: "新建", check: "切换到新分支", checked: true, do: func(name string, checkout bool) {
		run := func() {
			a.gitDo(v, "新建分支", func(ctx context.Context, root string) error {
				return gitx.CreateBranch(ctx, root, name, from, checkout)
			})
		}
		if checkout {
			a.guardAgent(w, "切换分支", run)
		} else {
			run()
		}
	}})
}

// askWorktreeFrom 是 WebStorm 的 New Worktree：在 posse 里新建一个 worktree 工作区，分支从 base 分出去。
func (a *app) askWorktreeFrom(w *workspace, base string) {
	a.prompt(promptReq{title: "基于 " + base + " 新建工作区", placeholder: "工作区名，也是新分支名", verb: "新建", do: func(name string, _ bool) {
		go a.createWorkspace(map[string]any{"project": w.Project, "name": name, "mode": "worktree", "base": base})
	}})
}

// attachWorktree 把不是 posse 建的 worktree 加成项目的工作区并切过去，名字取目录名。
// 常驻进程把它记成 folder：删工作区只删记录，不碰别人建的目录和分支。
func (a *app) attachWorktree(w *workspace, dir string) {
	name := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|~^`, r) {
			return '-'
		}
		return r
	}, filepath.Base(dir))
	name = strings.TrimLeft(name, "-.")
	if name == "" || name == "main" {
		name = "worktree"
	}
	taken := map[string]bool{}
	for _, x := range a.byProject[w.ProjectID] {
		taken[x.Name] = true
	}
	for i, base := 2, name; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	go a.createWorkspace(map[string]any{"project": w.Project, "name": name, "mode": "attach", "path": dir})
}

// workspaceAt 找目录正好是 dir 的工作区（比较时解析符号链接）。
func (a *app) workspaceAt(dir string) *workspace {
	if dir == "" {
		return nil
	}
	real := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return p
	}
	want := real(dir)
	for i := range a.workspaces {
		if real(a.workspaces[i].Path) == want {
			return &a.workspaces[i]
		}
	}
	return nil
}

// guardAgent 在会改动工作目录的操作之前，看工作区里有没有 agent 正在工作，有就先问。
func (a *app) guardAgent(w *workspace, what string, fn func()) {
	for _, t := range w.Terminals {
		if t.Running && t.AgentState != nil && t.AgentState.Status == agent.StatusWorking {
			a.confirm(agentTitle(t.Agent)+" 正在这个工作区里工作", what+"会改动工作目录里的文件，agent 正在改的内容可能被覆盖或打乱。仍然要"+what+"吗？", "仍然"+what, func() {
				a.update(fn)
			})
			return
		}
	}
	fn()
}

// --- 输入框对话框

type promptReq struct {
	title, body, placeholder, initial, verb string
	check                                   string // 非空时多一个勾选项
	checked                                 bool
	multiline                               bool // 多行输入（提交信息）
	optional                                bool // 可以留空（储藏的说明）
	do                                      func(value string, checked bool)
}

type promptState struct {
	promptReq
	value string
}

func (a *app) prompt(r promptReq) {
	a.promptDlg = &promptState{promptReq: r, value: r.initial}
}

func (a *app) promptDialog(c *ui.Context) {
	t := c.Theme()
	open := a.promptDlg != nil
	ui.Modal(c, &open, func() {
		p := a.promptDlg
		if p == nil {
			return
		}
		ui.Column(c).Width(360).Gap(12).Children(func() {
			titleText(c, p.title, fsTitle)
			if p.body != "" {
				ui.Text(c, p.body).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			}
			var submitted bool
			if p.multiline {
				fieldArea(c, &p.value, p.placeholder, 106).Label(p.title).AutoFocus().FontSize(fsBody)
			} else {
				submitted = fieldInput(c, &p.value, p.placeholder).AutoFocus().Submitted()
			}
			if p.check != "" {
				ui.Checkbox(c, &p.checked, p.check)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					a.promptDlg = nil
				}
				if pressable(accentButton(c, p.verb)).Clicked() {
					submitted = true
				}
			})
			if v := strings.TrimSpace(p.value); submitted && (v != "" || p.optional) {
				a.promptDlg = nil
				p.do(v, p.checked)
			}
		})
	})
	if !open {
		a.promptDlg = nil
	}
}
