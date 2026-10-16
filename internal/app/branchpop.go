package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"hive/internal/agent"
	"hive/internal/gitx"
)

// 分支弹窗：点标签栏的分支名打开，结构照 WebStorm 的 Git 分支弹窗（见 docs/plans/git-ops.html）。

type branchPopup struct {
	ws    string
	query string
}

// branchInfo 是分支弹窗要的数据，打开弹窗、Fetch、操作之后在后台重读。
type branchInfo struct {
	list    []gitx.Branch
	recent  []string
	loading bool
	again   bool // 读的期间又要求重读（期间做了操作），读完再读一次
}

// openBranches 打开工作区的分支弹窗，并在后台读分支。
func (a *app) openBranches(w *workspace) {
	v := a.filesFor(w)
	a.branchPop = &branchPopup{ws: w.ID}
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
		var recent []string
		if root != "" {
			list, _ = gitx.Branches(ctx, root)
			recent = gitx.RecentBranches(ctx, root, 6)
		}
		a.update(func() {
			again := g.branches.again
			g.branches = branchInfo{list: list, recent: recent}
			if !g.checked {
				g.checked, g.repo = true, root
			}
			if again {
				a.loadBranches(v)
			}
		})
	}()
}

// branchButton 是标签栏右边的分支名，点开分支弹窗。
func (a *app) branchButton(c *ui.Context, w *workspace) {
	t := c.Theme()
	b := ui.Row(c.Key("branch")).Height(26).Padding(0, 6, 0, 8).Gap(4).Radius(6).AlignItems(ui.Center).Shrink(1).
		Role(ui.RoleButton).Label("分支 " + w.Branch).Tooltip("Git 分支")
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
		ui.Icon(c, iconBranch).FontSize(13).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, w.Branch).FontSize(11.5).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Icon(c, iconDown).FontSize(10).TextColor(t.TextMuted).Shrink(0)
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
	t := c.Theme()
	p := colors(t)
	v := a.filesFor(w)
	g := &v.git
	panel := floating(c, ui.Column(c).Absolute().Top(6).Right(8).Width(420).MaxHeight(600).Padding(6).Gap(2))
	panel.Children(func() {
		ui.Row(c).Gap(4).Padding(2, 2, 6, 2).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Children(func() { fieldInput(c, &bp.query, "搜索分支").AutoFocus() })
			busy := g.busy == "Fetch"
			fetch := pressable(iconButtonSized(c, iconFetch, "Fetch：从所有远端拉取", 28, 15))
			if busy {
				fetch.Opacity(0.4)
			} else if fetch.Clicked() {
				a.gitDo(v, "Fetch", func(ctx context.Context, root string) error { return gitx.Fetch(ctx, root) })
			}
		})
		if g.busy != "" {
			ui.Text(c, g.busy+"…").FontSize(11).TextColor(t.TextMuted).Padding(0, 10, 4, 10)
		}
		query := strings.ToLower(strings.TrimSpace(bp.query))
		ui.Scroll(c).Grow(1).Shrink(1).Children(func() {
			ui.Column(c).Gap(1).Children(func() {
				if query == "" {
					a.branchActions(c, w, v)
					ui.Box(c).Height(1).Margin(4, 8).Background(p.line)
				}
				a.branchGroups(c, w, v, query)
			})
		})
	})
	if panel.PressedOutside() && !a.overPanelButton || panel.Shortcut(0, ui.KeyEscape) {
		a.branchPop = nil
	}
}

// popupItem 是弹窗里的一行操作，返回这一帧是否点了它。
func popupItem(c *ui.Context, icon *ui.SVG, label, keys string) bool {
	t := c.Theme()
	row := ui.Row(c.Key(label)).Height(28).Padding(0, 10).Gap(10).Radius(6).AlignItems(ui.Center).Role(ui.RoleButton).Label(label)
	bg := ui.Transparent
	if row.Hovered() {
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		if icon != nil {
			ui.Icon(c, icon).FontSize(14).TextColor(t.TextMuted).Shrink(0)
		} else {
			ui.Box(c).Width(14).Shrink(0)
		}
		ui.Text(c, label).FontSize(12.5).Grow(1)
		if keys != "" {
			ui.Text(c, keys).FontSize(11).TextColor(t.TextMuted).Shrink(0)
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
				a.gitDo(v, "更新项目", func(ctx context.Context, root string) error { return gitx.Update(ctx, root, cur) })
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
	ui.Box(c).Height(1).Margin(4, 8).Background(colors(c.Theme()).line)
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
		ui.Text(c, "正在读取分支…").FontSize(12).TextColor(t.TextMuted).Padding(6, 10)
		return
	}
	match := func(b gitx.Branch) bool { return query == "" || strings.Contains(strings.ToLower(b.Name), query) }
	header := func(title string) {
		ui.Text(c, title).FontSize(11).FontWeight(500).TextColor(t.TextMuted).Padding(8, 10, 2, 10)
	}
	var recent []gitx.Branch
	if query == "" {
		for _, name := range g.branches.recent {
			if i := slices.IndexFunc(list, func(b gitx.Branch) bool { return !b.Remote && b.Name == name }); i >= 0 {
				recent = append(recent, list[i])
			}
		}
	}
	if len(recent) > 0 {
		header("最近")
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
	if len(local) > 0 {
		header("本地")
		for _, b := range local {
			a.branchRow(c, w, v, b, "local")
		}
	}
	for _, r := range remoteNames {
		header("远端 " + r)
		for _, b := range remotes[r] {
			a.branchRow(c, w, v, b, "remote")
		}
	}
	if query != "" && len(local) == 0 && len(remoteNames) == 0 {
		ui.Text(c, "没有名字里含“"+query+"”的分支").FontSize(12).TextColor(t.TextMuted).Padding(6, 10)
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
	row := ui.Row(c.Key(group+":"+b.Name)).Height(28).Padding(0, 10).Gap(8).Radius(6).AlignItems(ui.Center).Role(ui.RoleButton).Label(b.Name)
	bg := ui.Transparent
	if row.Hovered() {
		bg = p.wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		color := t.TextMuted
		if b.Current {
			color = t.Accent
		}
		ui.Icon(c, iconBranch).FontSize(13).TextColor(color).Shrink(0)
		name := ui.Text(c, b.Name).FontSize(12.5).Font(monoFont).SingleLine().Shrink(1)
		if b.Current {
			name.FontWeight(500)
		}
		if b.Ahead > 0 {
			ui.Text(c, fmt.Sprintf("↑%d", b.Ahead)).FontSize(11).FontFeatures("tnum").TextColor(p.sage).Tooltip(fmt.Sprintf("%d 个提交待推送", b.Ahead)).Shrink(0)
		}
		if b.Behind > 0 {
			ui.Text(c, fmt.Sprintf("↓%d", b.Behind)).FontSize(11).FontFeatures("tnum").TextColor(t.Accent).Tooltip(fmt.Sprintf("远端有 %d 个提交待拉取", b.Behind)).Shrink(0)
		}
		ui.Spacer(c)
		switch {
		case inOther && otherWs != nil:
			ui.Text(c, "在 "+displayName(otherWs)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		case inOther:
			ui.Text(c, "在另一个 worktree").FontSize(11).TextColor(t.TextMuted).Shrink(0)
		case b.Gone:
			ui.Text(c, "远端已删除").FontSize(11).TextColor(t.TextMuted).Shrink(0)
		case b.Upstream != "":
			ui.Text(c, b.Upstream).FontSize(11).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1)
		}
	})
	row.Menu(func(m *ui.Menu) { a.branchMenu(m, w, v, b, cur, otherWs, inOther) })
}

// branchMenu 是一个分支的操作，顺序照 WebStorm。
func (a *app) branchMenu(m *ui.Menu, w *workspace, v *filesView, b, cur gitx.Branch, otherWs *workspace, inOther bool) {
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
			m.Item("检出在另一个 worktree 里").Disabled(true)
		default:
			if m.Item("检出").Chosen() {
				done()
				a.guardAgent(w, "检出", func() {
					a.gitDo(v, "检出", func(ctx context.Context, root string) error { return gitx.Checkout(ctx, root, b) })
				})
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
		a.openLog(w, b.Name, "")
	}
	if !b.Remote && b.Upstream != "" && m.Item("更新").Chosen() {
		done()
		if b.Current {
			a.guardAgent(w, "更新", func() {
				a.gitDo(v, "更新", func(ctx context.Context, root string) error { return gitx.Update(ctx, root, b) })
			})
		} else {
			a.gitDo(v, "更新 "+b.Name, func(ctx context.Context, root string) error { return gitx.Update(ctx, root, b) })
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
	if !b.Current && !inOther && m.Item("删除…").Chosen() {
		done()
		a.askDeleteBranch(v, b)
	}
}

// askDeleteBranch 删除分支前确认；本地分支没合并时再问一次是否强制删除，远端分支删除的是远端上的。
func (a *app) askDeleteBranch(v *filesView, b gitx.Branch) {
	if b.Remote {
		a.confirm("删除远端分支 "+b.Name+"？", "会推送到远端，把远端上的这个分支删掉，其他人也拉不到了。", "删除", func() {
			a.update(func() {
				a.gitDo(v, "删除远端分支", func(ctx context.Context, root string) error { return gitx.DeleteRemoteBranch(ctx, root, b.Name) })
			})
		})
		return
	}
	a.confirm("删除分支 "+b.Name+"？", "只删除本地分支，远端的不受影响。", "删除", func() {
		a.update(func() {
			a.gitDo(v, "删除分支", func(ctx context.Context, root string) error {
				err := gitx.DeleteBranch(ctx, root, b.Name, false)
				if gitx.NotMerged(err) {
					a.update(func() {
						a.confirm("分支 "+b.Name+" 还没合并", "它上面有别的分支里没有的提交，强制删除后这些提交就找不回来了（除非记得哈希）。", "强制删除", func() {
							a.update(func() {
								a.gitDo(v, "强制删除分支", func(ctx context.Context, root string) error { return gitx.DeleteBranch(ctx, root, b.Name, true) })
							})
						})
					})
					return nil
				}
				return err
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

// askWorktreeFrom 是 WebStorm 的 New Worktree：在 hive 里新建一个 worktree 工作区，分支从 base 分出去。
func (a *app) askWorktreeFrom(w *workspace, base string) {
	a.prompt(promptReq{title: "基于 " + base + " 新建工作区", placeholder: "工作区名，也是新分支名", verb: "新建", do: func(name string, _ bool) {
		go a.createWorkspace(map[string]any{"project": w.Project, "name": name, "mode": "worktree", "base": base})
	}})
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
			titleText(c, p.title, fsCardTitle)
			if p.body != "" {
				ui.Text(c, p.body).FontSize(13).TextColor(t.TextMuted).LineHeight(1.45)
			}
			var submitted bool
			if p.multiline {
				ui.TextArea(c, &p.value).Label(p.title).AutoFocus().Height(120).FontSize(12.5)
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
			if v := strings.TrimSpace(p.value); submitted && v != "" {
				a.promptDlg = nil
				p.do(v, p.checked)
			}
		})
	})
	if !open {
		a.promptDlg = nil
	}
}
