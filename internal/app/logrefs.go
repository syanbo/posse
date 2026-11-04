package app

import (
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"

	"posse/internal/gitx"
)

// Git 日志左边的分支树，照 WebStorm：HEAD（当前分支）、本地、远程（按远端分组）、标签。
// 点一项只看它的提交，再点一次回到全部分支。

func (a *app) logRefs(c *ui.Context, v *filesView, d *fileDoc) {
	ld := d.log
	g := v.repoOrGone(ld.root)
	if g.branches.list == nil && !g.branches.loading {
		a.loadBranches(g)
	}
	if ld.refsOpen == nil {
		ld.refsOpen = map[string]bool{"local": true}
	}
	ui.Box(c).Height(sz(40)).Shrink(0).Padding(0, 8).Center().Children(func() {
		fieldInput(c, &ld.refQuery, L(c).LogFilterRefs())
	})
	query := strings.ToLower(strings.TrimSpace(ld.refQuery))
	match := func(name string) bool { return query == "" || strings.Contains(strings.ToLower(name), query) }
	// 筛选时各组都展开，只列出对得上的
	open := func(key string) bool { return query != "" || ld.refsOpen[key] }
	// 单击只看这一个（再点一次回到全部），⌘ 单击照 WebStorm 加进或移出已选的几个
	pick := func(ref string, add bool) {
		sel := ld.filter.Branches
		switch {
		case add && slices.Contains(sel, ref):
			sel = slices.DeleteFunc(slices.Clone(sel), func(s string) bool { return s == ref })
		case add:
			sel = append(slices.Clone(sel), ref)
		case len(sel) == 1 && sel[0] == ref:
			sel = nil
		default:
			sel = []string{ref}
		}
		ld.filter.Branches = sel
		a.loadLog(v, d, false)
	}
	picked := func(ref string) bool { return slices.Contains(ld.filter.Branches, ref) }
	var local, remote []gitx.Branch
	for _, b := range g.branches.list {
		if !match(b.Name) {
			continue
		}
		if b.Remote {
			remote = append(remote, b)
		} else {
			local = append(local, b)
		}
	}
	var tags []string
	for _, tag := range g.branches.tags {
		if match(tag) {
			tags = append(tags, tag)
		}
	}
	// 先排成一列行，再交给列表：列表能拿焦点，方向键才有地方收
	var items []refItem
	group := func(key, label string, n, indent, parent int) (int, bool) {
		items = append(items, refItem{key: "g:" + key, group: key, label: label, n: n, indent: indent, parent: parent, open: open(key), fixed: query != ""})
		return len(items) - 1, open(key)
	}
	ref := func(key, label, name string, indent, parent int, r string, mark func(*ui.Context)) {
		items = append(items, refItem{key: key, label: label, name: name, indent: indent, parent: parent, ref: r, mark: mark})
	}
	if query == "" {
		label := L(c).LogHead()
		if cur, ok := currentBranch(g.branches.list); ok {
			label = L(c).LogHeadBranch(cur.Name)
		}
		ref("HEAD", label, label, 0, -1, "HEAD", nil)
	}
	if gi, ok := group("local", L(c).BranchLocal(), len(local), 0, -1); ok {
		for _, b := range local {
			ref("l:"+b.Name, b.Name, L(c).LogLocalBranch(b.Name), 1, gi, branchRef(b), refMark(b))
		}
	}
	if gi, ok := group("remote", L(c).BranchRemote(), len(remote), 0, -1); ok {
		// 按远端分组：origin/main 放在 origin 下面
		var remotes []string
		for _, b := range remote {
			r, _, _ := strings.Cut(b.Name, "/")
			if !slices.Contains(remotes, r) {
				remotes = append(remotes, r)
			}
		}
		for _, r := range remotes {
			ri, ok := group("remote:"+r, r, -1, 1, gi)
			if !ok {
				continue
			}
			for _, b := range remote {
				if rest, ok := strings.CutPrefix(b.Name, r+"/"); ok {
					ref("r:"+b.Name, rest, L(c).LogRemoteBranch(b.Name), 2, ri, branchRef(b), nil)
				}
			}
		}
	}
	if gi, ok := group("tags", L(c).LogTags(), len(tags), 0, -1); ok {
		for _, tag := range tags {
			ref("t:"+tag, tag, L(c).LogTag(tag), 1, gi, "refs/tags/"+tag, nil)
		}
	}
	list := &ld.refList
	list.Key = func(i int) any { return items[i].key }
	ui.List(c, list, len(items), func(i int) {
		it := items[i]
		cursor := it.key == ld.refCursor && list.FocusWithin(c)
		if it.group != "" {
			if a.refGroupAt(c, it, cursor) {
				ld.toggleGroup(it)
				ld.refCursor = it.key
				list.Focus(c)
			}
			return
		}
		if ok, add := a.refRow(c, it, picked(it.ref), cursor); ok {
			pick(it.ref, add)
			ld.refCursor = it.key
			// 行是按钮、不拿焦点；焦点给列表，方向键才能接着用
			list.Focus(c)
		}
	}).Grow(1).Padding(0, 6, 12, 6)
	a.refKeys(c, v, d, items)
}

// refItem 是分支树的一行：组（group 非空）或一个分支、标签。
type refItem struct {
	key    string // 列表里的键：组是 g:<组>，分支和标签带种类前缀
	group  string // 组的键（refsOpen 里的），分支和标签是空的
	label  string
	name   string // 读屏念的名字
	n      int    // 组里有几项，-1 不显示
	indent int
	parent int // 上一层组所在的行，-1 是顶层
	open   bool
	fixed  bool   // 筛选时各组都展开、收不起来：点击、←、回车都不改 refsOpen，清掉筛选后还是原来的样子
	ref    string // 日志按它筛选的引用名
	mark   func(c *ui.Context)
}

// toggleGroup 展开或收起一组；筛选时不动（见 refItem.fixed）。点击、←、回车都走这里。
func (ld *logDoc) toggleGroup(it refItem) {
	if !it.fixed {
		ld.refsOpen[it.group] = !it.open
	}
}

// refKeys 是分支树的方向键，照 WebStorm 的树：↑↓ 换行，停在分支、标签上就只看它的提交；
// → 展开组、在展开的组上进到第一项、在分支上到右边的提交列表；← 收起组、回到上一层组；回车在组上展开或收起，在分支上和单击一样：只看它，已经只看它时回到全部分支。
func (a *app) refKeys(c *ui.Context, v *filesView, d *fileDoc, items []refItem) {
	ld := d.log
	list := &ld.refList
	cur := slices.IndexFunc(items, func(it refItem) bool { return it.key == ld.refCursor })
	if cur < 0 && len(ld.filter.Branches) > 0 {
		// 还没用键盘走过（比如从提交列表按 ← 过来）：从正在看的那个分支起步
		cur = slices.IndexFunc(items, func(it refItem) bool { return it.ref == ld.filter.Branches[0] })
	}
	move := func(j int) {
		if j < 0 || j >= len(items) {
			return
		}
		it := items[j]
		ld.refCursor = it.key
		list.ScrollIntoView(j)
		if it.ref != "" && !(len(ld.filter.Branches) == 1 && ld.filter.Branches[0] == it.ref) {
			ld.filter.Branches = []string{it.ref}
			a.loadLog(v, d, false)
		}
	}
	switch {
	case list.Shortcut(c, 0, ui.KeyDown):
		move(cur + 1)
	case list.Shortcut(c, 0, ui.KeyUp):
		move(max(cur-1, 0))
	case cur >= 0 && list.Shortcut(c, 0, ui.KeyRight):
		it := items[cur]
		switch {
		case it.group != "" && !it.open:
			ld.refsOpen[it.group] = true
		case it.group != "":
			if cur+1 < len(items) && items[cur+1].parent == cur {
				move(cur + 1)
			}
		default:
			ld.focusList = true
		}
	case cur >= 0 && list.Shortcut(c, 0, ui.KeyLeft):
		it := items[cur]
		switch {
		// 筛选时各组都展开、收不起来，只回到上一层
		case it.group != "" && it.open && !it.fixed:
			ld.toggleGroup(it)
		case it.parent >= 0:
			ld.refCursor = items[it.parent].key
			list.ScrollIntoView(it.parent)
		}
	// 收不起来的组上回车什么也不做，不接这个键
	case cur >= 0 && !items[cur].fixed && list.Shortcut(c, 0, ui.KeyEnter):
		it := items[cur]
		if it.group != "" {
			ld.toggleGroup(it)
			break
		}
		ld.refCursor = it.key
		if len(ld.filter.Branches) == 1 && ld.filter.Branches[0] == it.ref {
			ld.filter.Branches = nil
		} else {
			ld.filter.Branches = []string{it.ref}
		}
		a.loadLog(v, d, false)
	}
}

// branchRef 是日志按分支筛选用的完整引用名：分支和标签同名时 git 会取标签。
func branchRef(b gitx.Branch) string {
	if b.Remote {
		return "refs/remotes/" + b.Name
	}
	return "refs/heads/" + b.Name
}

// refShort 是筛选栏上显示的名字。
func refShort(ref string) string {
	for _, p := range []string{"refs/heads/", "refs/remotes/", "refs/tags/"} {
		if s, ok := strings.CutPrefix(ref, p); ok {
			return s
		}
	}
	return ref
}

// refMark 是本地分支名后面的小字：当前分支。
func refMark(b gitx.Branch) func(c *ui.Context) {
	if !b.Current {
		return nil
	}
	return func(c *ui.Context) {
		ui.Text(c, L(c).LogCurrent()).FontSize(fsSmall).TextColor(c.Theme().TextMuted).Shrink(0)
	}
}

// refGroupAt 是可以展开的一组，返回这一帧是否点了它；cursor 是键盘停在这一行。
func (a *app) refGroupAt(c *ui.Context, it refItem, cursor bool) bool {
	t := c.Theme()
	row := ui.Row(c.Key(it.key)).Height(sz(26)).Margin(2, 0, 0, 0).Padding(0, 8, 0, 4+float32(it.indent)*treeIndent).Gap(4).Radius(rRow).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(it.label)
	selectedBg(c, row, cursor)
	row.Children(func() {
		chevron := iconChevron
		if it.open {
			chevron = iconDown
		}
		ui.Icon(c, chevron).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, it.label).FontSize(fsBody).SingleLine().Shrink(1)
		if it.n >= 0 {
			ui.Text(c, strconv.Itoa(it.n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		}
	})
	return row.Clicked()
}

// refRow 是一个分支或标签，返回这一帧是否点了它；it.name 是读屏念的名字，带上种类免得和别处同名。
func (a *app) refRow(c *ui.Context, it refItem, selected, cursor bool) (clicked, add bool) {
	row := ui.Row(c.Key(it.key)).Height(sz(26)).Margin(2, 0, 0, 0).Padding(0, 8, 0, 20+float32(it.indent)*treeIndent).Gap(6).Radius(rRow).AlignItems(ui.Center).
		Role(ui.RoleButton).Label(it.name).Tooltip(it.label)
	selectedBg(c, row, selected || cursor)
	row.Children(func() {
		ui.Text(c, it.label).FontSize(fsCode).Font(monoFont).SingleLine().Shrink(1).Grow(1)
		if it.mark != nil {
			it.mark(c)
		}
	})
	if !row.Clicked() {
		return false, false
	}
	return true, row.ClickModifiers()&ui.Cmd != 0
}
