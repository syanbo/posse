package app

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/internal/files"
	"posse/locales"
)

// 随处搜索（照 WebStorm 的双击 Shift）：一个弹框分"所有""文件""操作"几页，⇥ 换页。
// ⌘P 打开在"文件"页（原来的快速打开），⇧⌘A 打开在"操作"页。

const (
	quickAll = iota
	quickFiles
	quickActions
	quickTabs
)

// quickOpen 是随处搜索弹框的状态。
type quickOpen struct {
	open    bool
	ws      string
	tab     int
	query   string
	sel     int
	matches []files.Match
	key     string      // matches 对应的查询
	list    *files.List // matches 对应的清单
	moved   bool        // 这一帧用方向键换了选中项，要滚到它
	// all 开着「包括非项目条目」：文件页改用 allList（被 git 忽略的也列，后台现列，关弹框就放掉）
	all        bool
	allList    *files.List
	allCancel  context.CancelFunc
	allListing bool
}

// openQuick 打开随处搜索到 tab 页，清单不新鲜就重列；选中文件后才切到文件标签。
func (a *app) openQuick(w *workspace, tab int) {
	v := a.filesFor(w)
	a.closeQuick()
	a.quick = quickOpen{open: true, ws: w.ID, tab: tab}
	a.refreshList(v, false, nil)
}

func (a *app) closeQuick() {
	if a.quick.allCancel != nil {
		a.quick.allCancel()
	}
	a.quick.open, a.quick.allList, a.quick.allCancel = false, nil, nil
}

// paletteAction 是"操作"页的一条：id 是 keymap 里的键（名字和按键都从那里取），run 直接做这件事。
// 只收不依赖焦点、当前视图的窗口级操作：依赖焦点的（终端、编辑器里的键）弹框关掉后焦点已经换了，做了也没效果。
type paletteAction struct {
	id  string
	ok  func(a *app, w *workspace) bool // 为 nil 时总能用
	run func(a *app, m locales.Messages, w *workspace)
}

var paletteActions = []paletteAction{
	{id: "newTerminal", run: func(a *app, _ locales.Messages, w *workspace) { a.startTerminal(w.ID, "") }},
	{id: "newWorkspace", run: func(a *app, _ locales.Messages, _ *workspace) { a.openNewWorkspace() }},
	{id: "newChat", run: func(a *app, m locales.Messages, _ *workspace) { a.openChat(m, true) }},
	{id: "settings", run: func(a *app, _ locales.Messages, _ *workspace) { a.openSettings() }},
	{id: "toggleSidebar", run: func(a *app, _ locales.Messages, _ *workspace) { a.toggleSidebar() }},
	{id: "filesPanel", run: func(a *app, _ locales.Messages, w *workspace) {
		if a.filesPanelOpen() {
			a.closeFilesPanel()
		} else {
			a.openFilesPanel(w, a.filesFor(w).side)
		}
	}},
	{id: "fileTree", run: func(a *app, _ locales.Messages, w *workspace) { a.toggleFilesPanel(w, sideFiles) }},
	{id: "searchFiles", run: func(a *app, _ locales.Messages, w *workspace) { a.searchIn(w, "") }},
	{id: "revealFile", ok: func(a *app, w *workspace) bool { v := a.filesFor(w); return revealable(v, v.cur) },
		run: func(a *app, _ locales.Messages, w *workspace) { a.revealCurrent(w) }},
	{id: "changes", run: func(a *app, _ locales.Messages, w *workspace) { a.toggleFilesPanel(w, sideChanges) }},
	{id: "gitLog", ok: func(a *app, w *workspace) bool { return a.gitBranch(w) != "" },
		run: func(a *app, _ locales.Messages, w *workspace) { a.toggleLogPanel(w) }},
	{id: "push", ok: func(a *app, w *workspace) bool { return a.gitBranch(w) != "" },
		run: func(a *app, m locales.Messages, w *workspace) { a.openPush(m, w) }},
	{id: "newBranch", ok: func(a *app, w *workspace) bool { return a.gitBranch(w) != "" },
		run: func(a *app, m locales.Messages, w *workspace) { a.askNewBranch(m, w, a.filesFor(w), "") }},
}

// actionLabel 是操作在 keymap 里的名字。
func actionLabel(m locales.Messages, id string) string {
	for _, k := range posseKeys {
		if k.id == id {
			return k.label(m)
		}
	}
	return id
}

// matchActions 找名字里含有查询的每个词的操作（不分大小写），按表里的顺序；名字以查询开头的排前面。
func (a *app) matchActions(m locales.Messages, w *workspace, query string) []paletteAction {
	words := strings.Fields(strings.ToLower(query))
	var head, rest []paletteAction
	for _, p := range paletteActions {
		if p.ok != nil && !p.ok(a, w) {
			continue
		}
		label := strings.ToLower(actionLabel(m, p.id))
		if !allOf(words, func(s string) bool { return strings.Contains(label, s) }) {
			continue
		}
		if len(words) > 0 && strings.HasPrefix(label, words[0]) {
			head = append(head, p)
		} else {
			rest = append(rest, p)
		}
	}
	return append(head, rest...)
}

func allOf[T any](xs []T, f func(T) bool) bool {
	for _, x := range xs {
		if !f(x) {
			return false
		}
	}
	return true
}

// quickItem 是弹框列表里的一行：文件、操作，或"所有"页里一组的标题（不能选）。
type quickItem struct {
	file   *files.Match
	act    *paletteAction
	header string
}

// recentMatches 是查询为空时文件页先列的最近打开的文件（还在清单里的），后面接清单顺序的。
func recentMatches(v *filesView, l *files.List, limit int) []files.Match {
	var out []files.Match
	seen := map[string]bool{}
	for _, p := range v.recent {
		rel, ok := relInside(v.root, p)
		if !ok || seen[rel] {
			continue
		}
		if _, found := slices.BinarySearch(l.Files, rel); !found {
			continue
		}
		seen[rel] = true
		out = append(out, files.Match{Path: rel})
		if len(out) == limit {
			return out
		}
	}
	for _, m := range files.Fuzzy(l.Files, "", limit) {
		if len(out) == limit {
			break
		}
		if !seen[m.Path] {
			out = append(out, m)
		}
	}
	return out
}

func (a *app) quickDialog(c *ui.Context) {
	q := &a.quick
	if !q.open {
		return
	}
	w := a.workspace(q.ws)
	if w == nil {
		a.closeQuick()
		return
	}
	v := a.filesFor(w)
	a.reportListErr(c, v)
	t := c.Theme()
	m := L(c)
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(600).Gap(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				segmentedBadges(c, &q.tab, m.QuickTitle(), nil, m.QuickTabAll(), m.QuickTabFiles(), m.QuickTabActions())
				ui.Spacer(c)
				if q.tab != quickActions && toggleChip(c, m.QuickIncludeIgnored(), m.QuickIncludeIgnoredTip(), q.all) {
					a.toggleQuickAll(v)
				}
			})
			placeholder := m.QuickPlaceholderAll()
			switch q.tab {
			case quickFiles:
				placeholder = m.FilesQuickOpenPlaceholder()
			case quickActions:
				placeholder = m.QuickPlaceholderActions()
			}
			in, _ := clearableField(c, &q.query, placeholder)
			in.AutoFocus()
			if in.Shortcut(0, ui.KeyTab) {
				q.tab, q.sel = (q.tab+1)%quickTabs, 0
			} else if in.Shortcut(ui.Shift, ui.KeyTab) {
				q.tab, q.sel = (q.tab+quickTabs-1)%quickTabs, 0
			}

			// 文件：路径:行号 跳行；allList 还没列好时先用项目清单
			path, line := files.SplitLine(q.query)
			l := v.list
			if q.all && q.allList != nil {
				l = q.allList
			}
			if q.tab != quickActions && l != nil && (q.list != l || q.key != path) {
				if path == "" {
					q.matches = recentMatches(v, l, 50)
				} else {
					q.matches = files.Fuzzy(l.Files, path, 50)
				}
				q.key, q.list, q.sel = path, l, 0
			}
			var items []quickItem
			addFiles := func(n int) {
				for i := range min(n, len(q.matches)) {
					items = append(items, quickItem{file: &q.matches[i]})
				}
			}
			acts := []paletteAction(nil)
			if q.tab != quickFiles {
				acts = a.matchActions(m, w, q.query)
			}
			addActs := func(n int) {
				for i := range min(n, len(acts)) {
					items = append(items, quickItem{act: &acts[i]})
				}
			}
			switch q.tab {
			case quickFiles:
				addFiles(50)
			case quickActions:
				addActs(len(acts))
			default:
				// 每组只列前几条，标题点了换到那一页看全部
				if len(q.matches) > 0 {
					items = append(items, quickItem{header: m.QuickTabFiles()})
					addFiles(5)
				}
				if len(acts) > 0 {
					items = append(items, quickItem{header: m.QuickTabActions()})
					addActs(5)
				}
			}
			var pickable []int
			for i, it := range items {
				if it.header == "" {
					pickable = append(pickable, i)
				}
			}
			n := len(pickable)
			q.sel = min(q.sel, max(n-1, 0))
			switch {
			case n > 0 && in.Shortcut(0, ui.KeyDown):
				q.sel, q.moved = (q.sel+1)%n, true
			case n > 0 && in.Shortcut(0, ui.KeyUp):
				q.sel, q.moved = (q.sel+n-1)%n, true
			}
			submitted := in.Submitted()
			switch {
			case q.tab != quickActions && l == nil:
				ui.Text(c, m.FilesListing()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			case n == 0:
				ui.Text(c, m.FilesNoMatches()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			default:
				ui.Scroll(c).MaxHeight(420).Children(func() {
					ui.Column(c).Gap(2).Children(func() {
						k := 0
						for i, it := range items {
							if it.header != "" {
								if quickHeader(c, it.header) {
									q.tab, q.sel = quickFiles, 0
									if it.header == m.QuickTabActions() {
										q.tab = quickActions
									}
								}
								continue
							}
							selected := k < n && pickable[k] == i && k == q.sel
							var clicked bool
							if it.file != nil {
								clicked = quickRow(c, *it.file, selected, q.moved)
							} else {
								clicked = a.actionRow(c, m, *it.act, selected, q.moved)
							}
							if clicked {
								q.sel, submitted = k, true
							}
							k++
						}
					})
				})
			}
			q.moved = false
			// 底下一行是选中文件的完整路径
			if n > 0 {
				if f := items[pickable[q.sel]].file; f != nil {
					ui.Text(c, shortPath(filepath.Join(l.Root, f.Path))).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Padding(0, 4)
				}
			}
			if submitted && n > 0 {
				it := items[pickable[q.sel]]
				root := l.Root
				a.closeQuick()
				if it.file != nil {
					a.openFile(w, filepath.Join(root, it.file.Path), line, [2]int{})
				} else {
					it.act.run(a, m, w)
				}
			}
		})
	})
	if !open {
		a.closeQuick()
	}
}

// toggleQuickAll 开关「包括非项目条目」：打开时在后台遍历整个工作区（被忽略的也列），列好前先用项目清单。
func (a *app) toggleQuickAll(v *filesView) {
	q := &a.quick
	q.all = !q.all
	if !q.all {
		if q.allCancel != nil {
			q.allCancel()
		}
		q.allList, q.allCancel, q.allListing = nil, nil, false
		return
	}
	if q.allList != nil || q.allListing {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	q.allCancel, q.allListing = cancel, true
	root := v.root
	go func() {
		l, err := files.ListAll(ctx, root)
		a.update(func() {
			if ctx.Err() != nil || !a.quick.open {
				return
			}
			a.quick.allListing = false
			if err == nil {
				a.quick.allList = l
			}
		})
	}()
}

// quickHeader 是"所有"页里一组的标题，右边"更多"点了换到那一页。
func quickHeader(c *ui.Context, title string) bool {
	t := c.Theme()
	clicked := false
	ui.Row(c.Key("head-"+title)).Padding(6, 12, 2, 12).AlignItems(ui.Center).Children(func() {
		ui.Text(c, title).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
		ui.Spacer(c)
		more := ui.Text(c, L(c).QuickMore()).FontSize(fsSmall).TextColor(t.Accent).Role(ui.RoleButton)
		clicked = more.Clicked()
	})
	return clicked
}

// actionRow 是"操作"页的一行：名字在左，快捷键在右。返回是否被点了。
func (a *app) actionRow(c *ui.Context, m locales.Messages, p paletteAction, selected, reveal bool) bool {
	t := c.Theme()
	label := actionLabel(m, p.id)
	row := ui.Row(c.Key("act-"+p.id)).Height(sz(30)).Margin(2, 0, 0, 0).Padding(0, 12).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(label)
	selectedBg(c, row, selected)
	if selected && reveal {
		row.ScrollIntoView()
	}
	row.Children(func() {
		ui.Text(c, label).FontSize(fsBody).SingleLine().Shrink(1)
		ui.Spacer(c)
		if k := a.keyText(p.id); k != "" {
			ui.Text(c, k).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
	})
	return row.Clicked()
}
