package app

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/locales"
)

// 随处搜索（照 WebStorm 的双击 Shift）：一个弹框分"所有""文件""符号""操作""文本"几页，⇥ 换页。
// ⌘P 打开在"文件"页（原来的快速打开），⇧⌘A 打开在"操作"页。

const (
	quickAll = iota
	quickFiles
	quickSymbols
	quickActions
	quickText
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
	// 文本页（和"所有"页的文本一组）：全文检索的前 quickTextMax 条，按文件栏检索限定的目录搜
	text quickTextSearch
	// 符号页（和"所有"页的符号一组）：按名字找声明，typesOnly 只要类型（WebStorm 的"类"页）
	sym       quickSymSearch
	typesOnly bool
}

type quickSymSearch struct {
	key     string // syms 对应的查询（查询词、只看类型、限定的目录）
	gen     int
	cancel  context.CancelFunc
	running bool
	syms    []files.Symbol
	err     string
}

// quickTextMax 是随处搜索里全文检索最多列的条数，看全部到文件栏的检索页。
const quickTextMax = 50

type quickTextSearch struct {
	key     string // hits 对应的查询（检索词和限定的目录）
	gen     int
	cancel  context.CancelFunc
	running bool
	hits    []files.Hit
	more    bool // 不止 quickTextMax 条
	err     string
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
	if a.quick.text.cancel != nil {
		a.quick.text.cancel()
	}
	if a.quick.sym.cancel != nil {
		a.quick.sym.cancel()
	}
	a.quick.text, a.quick.sym = quickTextSearch{}, quickSymSearch{}
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

// quickItem 是弹框列表里的一行：文件、操作、文本命中，或"所有"页里一组的标题（不能选，tab 是"更多"换到的页）。
type quickItem struct {
	file   *files.Match
	act    *paletteAction
	hit    *files.Hit
	sym    *files.Symbol
	header string
	tab    int
}

// syncQuickSymbols 让符号搜索跟上查询：变了就取消旧的，停顿 150ms 后在后台现查（files.FindSymbols，不建索引）。
func (a *app) syncQuickSymbols(v *filesView, query string, typesOnly bool) {
	s := &a.quick.sym
	dir := v.search.query.Dir
	key := fmt.Sprint(dir, "\x00", typesOnly, "\x00", query)
	if s.key == key {
		return
	}
	s.key = key
	s.gen++
	gen := s.gen
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.syms, s.err, s.running = nil, "", false
	if strings.TrimSpace(query) == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.running = cancel, true
	q := files.SymbolQuery{Text: query, TypesOnly: typesOnly, Dir: dir}
	time.AfterFunc(150*time.Millisecond, func() {
		a.update(func() {
			if a.quick.sym.gen != gen {
				return
			}
			a.refreshList(v, false, func() {
				if a.quick.sym.gen != gen || v.list == nil {
					return
				}
				l, overlay := v.list, editorOverlay(v)
				go func() {
					var err error
					var syms []files.Symbol
					if q.Dir != "" && l.Ignored(filepath.Join(l.Root, q.Dir), true) {
						l, err = files.ListDir(ctx, l.Root, q.Dir)
					}
					if err == nil {
						syms, _, err = files.FindSymbols(ctx, l, overlay, q)
					}
					a.update(func() {
						s := &a.quick.sym
						if s.gen != gen {
							return
						}
						s.running, s.syms = false, syms
						if err != nil && ctx.Err() == nil {
							s.err = errtext.Text(err)
						}
					})
				}()
			})
		})
	})
}

// syncQuickText 让文本检索跟上查询：变了就取消旧的，停顿 150ms 后在后台重搜，攒够 quickTextMax 条就停。
func (a *app) syncQuickText(v *filesView, query string) {
	t := &a.quick.text
	dir := v.search.query.Dir
	key := dir + "\x00" + query
	if t.key == key {
		return
	}
	t.key = key
	t.gen++
	gen := t.gen
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
	}
	t.hits, t.more, t.err, t.running = nil, false, "", false
	if strings.TrimSpace(query) == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel, t.running = cancel, true
	q := files.Query{Text: query, Dir: dir}
	time.AfterFunc(150*time.Millisecond, func() {
		a.update(func() {
			if a.quick.text.gen != gen {
				return
			}
			a.refreshList(v, false, func() {
				if a.quick.text.gen != gen || v.list == nil {
					return
				}
				l, overlay := v.list, editorOverlay(v)
				go func() {
					var err error
					if q.Dir != "" && l.Ignored(filepath.Join(l.Root, q.Dir), true) {
						l, err = files.ListDir(ctx, l.Root, q.Dir)
					}
					if err == nil {
						_, err = files.Search(ctx, l, q, overlay, func(hs []files.Hit) {
							a.update(func() {
								t := &a.quick.text
								if t.gen != gen {
									return
								}
								t.hits = append(t.hits, hs...)
								if len(t.hits) > quickTextMax {
									t.hits, t.more = t.hits[:quickTextMax], true
									cancel()
								}
							})
						})
					}
					a.update(func() {
						t := &a.quick.text
						if t.gen != gen {
							return
						}
						t.running = false
						if err != nil && ctx.Err() == nil {
							t.err = errtext.Text(err)
						}
					})
				}()
			})
		})
	})
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
				segmentedBadges(c, &q.tab, m.QuickTitle(), nil, m.QuickTabAll(), m.QuickTabFiles(), m.QuickTabSymbols(), m.QuickTabActions(), m.QuickTabText())
				ui.Spacer(c)
				if (q.tab == quickAll || q.tab == quickFiles) && toggleChip(c, m.QuickIncludeIgnored(), m.QuickIncludeIgnoredTip(), q.all) {
					a.toggleQuickAll(v)
				}
				if q.tab == quickSymbols && toggleChip(c, m.QuickTypesOnly(), m.QuickTypesOnlyTip(), q.typesOnly) {
					q.typesOnly, q.sel = !q.typesOnly, 0
				}
			})
			placeholder := m.QuickPlaceholderAll()
			switch q.tab {
			case quickFiles:
				placeholder = m.FilesQuickOpenPlaceholder()
			case quickActions:
				placeholder = m.QuickPlaceholderActions()
			case quickText:
				placeholder = m.FilesSearchPlaceholder()
			case quickSymbols:
				placeholder = m.QuickPlaceholderSymbols()
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
			wantFiles := q.tab == quickAll || q.tab == quickFiles
			if wantFiles && l != nil && (q.list != l || q.key != path) {
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
			if q.tab == quickAll || q.tab == quickActions {
				acts = a.matchActions(m, w, q.query)
			}
			if q.tab == quickAll || q.tab == quickText {
				a.syncQuickText(v, q.query)
			}
			if q.tab == quickAll || q.tab == quickSymbols {
				// "所有"页里的符号一组不分类型，只看类型是符号页自己的开关
				a.syncQuickSymbols(v, path, q.typesOnly && q.tab == quickSymbols)
			}
			syms := q.sym.syms
			addSyms := func(n int) {
				for i := range min(n, len(syms)) {
					items = append(items, quickItem{sym: &syms[i]})
				}
			}
			hits := q.text.hits
			addHits := func(n int) {
				for i := range min(n, len(hits)) {
					items = append(items, quickItem{hit: &hits[i]})
				}
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
			case quickText:
				addHits(quickTextMax)
			case quickSymbols:
				addSyms(len(syms))
			default:
				// 每组只列前几条，标题点了换到那一页看全部
				if len(q.matches) > 0 {
					items = append(items, quickItem{header: m.QuickTabFiles(), tab: quickFiles})
					addFiles(5)
				}
				if len(syms) > 0 {
					items = append(items, quickItem{header: m.QuickTabSymbols(), tab: quickSymbols})
					addSyms(5)
				}
				if len(acts) > 0 {
					items = append(items, quickItem{header: m.QuickTabActions(), tab: quickActions})
					addActs(5)
				}
				if len(hits) > 0 {
					items = append(items, quickItem{header: m.QuickTabText(), tab: quickText})
					addHits(5)
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
			case q.tab == quickText && q.text.err != "":
				ui.Text(c, q.text.err).FontSize(fsSmall).TextColor(t.Danger).Padding(4, 2)
			case q.tab == quickText && strings.TrimSpace(q.query) == "":
				ui.Text(c, m.QuickTextHint()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			case q.tab == quickSymbols && q.sym.err != "":
				ui.Text(c, q.sym.err).FontSize(fsSmall).TextColor(t.Danger).Padding(4, 2)
			case q.tab == quickSymbols && strings.TrimSpace(q.query) == "":
				ui.Text(c, m.QuickSymbolsHint()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			case n == 0 && (q.sym.running && (q.tab == quickSymbols || q.tab == quickAll) || q.text.running && (q.tab == quickText || q.tab == quickAll)):
				ui.Text(c, m.FilesSearching()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			case n == 0:
				ui.Text(c, m.FilesNoMatches()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4, 2)
			default:
				ui.Scroll(c).MaxHeight(420).Children(func() {
					ui.Column(c).Gap(2).Children(func() {
						k := 0
						for i, it := range items {
							if it.header != "" {
								if quickHeader(c, it.header) {
									q.tab, q.sel = it.tab, 0
								}
								continue
							}
							selected := k < n && pickable[k] == i && k == q.sel
							var clicked bool
							switch {
							case it.file != nil:
								clicked = quickRow(c, *it.file, selected, q.moved)
							case it.hit != nil:
								clicked = quickHitRow(c, *it.hit, selected, q.moved)
							case it.sym != nil:
								clicked = quickSymRow(c, *it.sym, strings.TrimSpace(path), selected, q.moved)
							default:
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
			// 底下一行是选中文件的完整路径；文本页右边是"在文件栏中查看全部"，交给文件栏的检索
			showAll := q.tab == quickText && strings.TrimSpace(q.query) != ""
			if n > 0 || showAll {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(0, 4).Children(func() {
					footer := ""
					if n > 0 {
						switch it := items[pickable[q.sel]]; {
						case it.file != nil && l != nil:
							footer = shortPath(filepath.Join(l.Root, it.file.Path))
						case it.hit != nil:
							footer = shortPath(filepath.Join(v.root, it.hit.Path))
						case it.sym != nil:
							footer = shortPath(filepath.Join(v.root, it.sym.Path))
						}
					}
					if dir := v.search.query.Dir; showAll && dir != "" && footer == "" {
						footer = m.FilesSearchScope(filepath.ToSlash(dir))
					}
					ui.Text(c, footer).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
					ui.Spacer(c)
					if showAll {
						label := m.QuickTextShowAll()
						if q.text.more {
							label = m.QuickTextShowAllMore(quickTextMax)
						}
						if ui.Text(c, label).FontSize(fsSmall).TextColor(t.Accent).Role(ui.RoleButton).Shrink(0).Clicked() {
							query := q.query
							a.closeQuick()
							a.searchText(w, query)
						}
					}
				})
			}
			if submitted && n > 0 {
				it := items[pickable[q.sel]]
				root := v.root
				if l != nil {
					root = l.Root
				}
				a.closeQuick()
				switch {
				case it.file != nil:
					a.openFile(w, filepath.Join(root, it.file.Path), line, [2]int{})
				case it.hit != nil:
					var span [2]int
					if len(it.hit.Spans) > 0 {
						span = it.hit.Spans[0]
					}
					a.openFile(w, filepath.Join(v.root, it.hit.Path), it.hit.Line+1, span)
				case it.sym != nil:
					a.openFile(w, filepath.Join(v.root, it.sym.Path), it.sym.Line+1, it.sym.Span)
				default:
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

// searchText 把随处搜索里的检索词交给文件栏的检索框，看全部命中：和弹框里一样按字面、不分大小写，限定的目录照旧。
func (a *app) searchText(w *workspace, query string) {
	v := a.openFilesPanel(w, sideFiles)
	v.search.query.Text, v.search.query.Regex, v.search.query.MatchCase = query, false, false
	v.search.focus = true
	a.startSearch(v, 0)
}

// quickHitRow 是文本页的一条命中：命中的那一行在左（缩进去掉），文件名和行号在右。返回是否被点了。
func quickHitRow(c *ui.Context, h files.Hit, selected, reveal bool) bool {
	t := c.Theme()
	row := ui.Row(c.Key(fmt.Sprint(h.Path, ":", h.Line))).Height(sz(30)).Margin(2, 0, 0, 0).Padding(0, 12).Gap(12).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(h.Text)
	selectedBg(c, row, selected)
	if selected && reveal {
		row.ScrollIntoView()
	}
	text, spans := trimHit(h)
	row.Children(func() {
		ui.RichText(c, markSpans(text, spans, ui.Span{Font: monoFont, Size: fsSmall}, colors(t).mark)...).SingleLine().Shrink(1).Grow(1)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
			fileGlyph(c, h.Path, fsBody)
			ui.Text(c, fmt.Sprintf("%s:%d", filepath.Base(h.Path), h.Line+1)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
		})
	})
	return row.Clicked()
}

// quickSymRow 是符号页的一条声明：名字（查询的那段铺底色）和种类在左，文件名和行号在右。返回是否被点了。
func quickSymRow(c *ui.Context, s files.Symbol, query string, selected, reveal bool) bool {
	t := c.Theme()
	row := ui.Row(c.Key(fmt.Sprint(s.Path, ":", s.Line))).Height(sz(30)).Margin(2, 0, 0, 0).Padding(0, 12).Gap(8).Radius(rRow).AlignItems(ui.Center).Role(ui.RoleButton).Label(s.Name)
	selectedBg(c, row, selected)
	if selected && reveal {
		row.ScrollIntoView()
	}
	var marks [][2]int
	if i := strings.Index(strings.ToLower(s.Name), strings.ToLower(query)); i >= 0 && query != "" {
		marks = [][2]int{{i, i + len(query)}}
	}
	row.Children(func() {
		ui.RichText(c, markSpans(s.Name, marks, ui.Span{Size: fsBody, Font: monoFont}, colors(t).mark)...).SingleLine().Shrink(0)
		ui.Text(c, symbolKindText(L(c), s.Kind)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(0)
		ui.Spacer(c)
		ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).Children(func() {
			fileGlyph(c, s.Path, fsBody)
			ui.Text(c, fmt.Sprintf("%s:%d", filepath.Base(s.Path), s.Line+1)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
		})
	})
	return row.Clicked()
}

// symbolKindText 是声明种类的界面叫法；files 给的是代号。
func symbolKindText(m locales.Messages, kind string) string {
	switch kind {
	case "type":
		return m.QuickSymbolKindType()
	case "func":
		return m.QuickSymbolKindFunc()
	case "method":
		return m.QuickSymbolKindMethod()
	case "macro":
		return m.QuickSymbolKindMacro()
	case "mod":
		return m.QuickSymbolKindModule()
	case "field":
		return m.QuickSymbolKindField()
	}
	return m.QuickSymbolKindVar()
}
