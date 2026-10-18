package app

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// 分支比较的标签页，照 WebStorm：
//   - 和当前分支比较：两边各自独有的提交，选中提交看它改的文件；
//   - 和工作区比较：工作区（含没提交的改动）和那个分支不同的文件。
// 点文件打开 diff，F7 在同一组文件之间跳。

type compareDoc struct {
	other, cur string
	files      bool
	onlyOther  []gitx.CommitInfo
	onlyCur    []gitx.CommitInfo
	changes    []gitx.FileChange
	commit     *commitView // 选中的提交
	list       ui.ListState
	folded     map[string]bool // 文件树里收起的目录
	listOther  ui.ListState    // 只在 other 里的提交
	listCur    ui.ListState    // 只在 cur 里的提交
}

// commitView 是选中的一个提交和它改的文件。
type commitView struct {
	info   gitx.CommitInfo
	files  []gitx.FileChange
	err    string
	list   ui.ListState
	folded map[string]bool // 文件树里收起的目录
}

func compareKey(other, cur string, files bool) string {
	if files {
		return "compare:files:" + other
	}
	return "compare:" + other + ".." + cur
}

func (a *app) openCompare(w *workspace, v *filesView, other, cur string, files bool) {
	v = a.showFiles(w)
	key := compareKey(other, cur, files)
	for _, d := range v.docs {
		if d.path == key {
			v.cur = d
			return
		}
	}
	d := &fileDoc{path: key, comp: &compareDoc{other: other, cur: cur, files: files}, started: true}
	i := len(v.docs)
	if j := slices.Index(v.docs, v.cur); j >= 0 {
		i = j + 1
	}
	v.docs = slices.Insert(v.docs, i, d)
	v.cur = d
	a.loadCompare(v, d)
}

// refsChanged 在 fetch、拉取、推送等改了引用的操作之后，重读一个项目里所有依赖引用的界面：
// 分支按钮的传入传出、分支弹窗、分支比较、按分支名比较的 diff、日志和文件历史。
// 同一项目的工作区共用引用，别的工作区里开着的也要重读。
func (a *app) refsChanged(projectID string) {
	for _, w := range a.byProject[projectID] {
		if tr := a.track[w.ID]; tr != nil {
			tr.poll = panelPoll{}
		}
		v := a.files[w.ID]
		if v == nil {
			continue
		}
		if v.git.branches.list != nil {
			a.loadBranches(v)
		}
		for _, d := range v.docs {
			switch {
			case d.comp != nil:
				a.loadCompare(v, d)
			case d.diff != nil && d.kind != docLoading && !d.diff.fixedRevs():
				a.loadDiff(v, d)
			}
		}
		for _, d := range v.logs {
			if d.log != nil && !d.log.loading {
				a.reloadLog(v, d)
			}
		}
	}
}

func (a *app) loadCompare(v *filesView, d *fileDoc) {
	cd, root := d.comp, v.git.repo
	other, cur := cd.other, cd.cur
	go func() {
		ctx := context.Background()
		var onlyOther, onlyCur []gitx.CommitInfo
		var changes []gitx.FileChange
		var err error
		if cd.files {
			changes, err = gitx.DiffWithWorkingTree(ctx, root, other)
		} else if onlyOther, err = gitx.CommitsBetween(ctx, root, cur, other, 1000); err == nil {
			onlyCur, err = gitx.CommitsBetween(ctx, root, other, cur, 1000)
		}
		a.update(func() {
			// 读的时候用户点了交换：结果跟着对调
			if cd.other != other {
				onlyOther, onlyCur = onlyCur, onlyOther
			}
			if err != nil {
				d.kind, d.err = docError, err.Error()
				return
			}
			cd.onlyOther, cd.onlyCur, cd.changes = onlyOther, onlyCur, changes
			d.kind = docCompare
		})
	}()
}

// selectCommit 选中提交，在后台读它改的文件。
func (a *app) selectCommit(v *filesView, holder **commitView, c gitx.CommitInfo) {
	cv := &commitView{info: c}
	*holder = cv
	root := v.git.repo
	go func() {
		files, err := gitx.CommitFiles(context.Background(), root, c.Hash)
		a.update(func() {
			cv.files = files
			if err != nil {
				cv.err = err.Error()
			}
		})
	}()
}

// commitSpecs 是一个提交里各文件的 diff：父提交对比这个提交，根提交的旧一边是空的。
func commitSpecs(c gitx.CommitInfo, files []gitx.FileChange) []diffSpec {
	parent := ""
	if len(c.Parents) > 0 {
		parent = c.Parents[0]
	}
	out := make([]diffSpec, len(files))
	for i, f := range files {
		old := parent
		if f.Status == 'A' {
			old = ""
		}
		out[i] = diffSpec{rel: f.Path, orig: f.Orig, oldRev: old, newRev: c.Hash, label: c.Hash[:7]}
	}
	return out
}

func (a *app) compareView(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	cd := d.comp
	if cd.files {
		specs := make([]diffSpec, len(cd.changes))
		for i, f := range cd.changes {
			specs[i] = diffSpec{rel: f.Path, orig: f.Orig, oldRev: cd.other, label: cd.other + " → 工作区"}
		}
		ui.Row(c).Height(36).Shrink(0).Padding(0, 16).AlignItems(ui.Center).Children(func() {
			ui.Text(c, fmt.Sprintf("工作区和 %s 有 %d 个文件不同", cd.other, len(cd.changes))).FontSize(12.5).FontWeight(500)
		})
		if len(cd.changes) == 0 {
			ui.Text(c, "没有差别").FontSize(12).TextColor(t.TextMuted).Padding(8, 16)
			return
		}
		a.fileChangeList(c, w, v, &cd.list, &cd.folded, cd.changes, specs)
		return
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		// 照 WebStorm：上下两栏，各是一边独有的提交
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			a.compareBar(c, w, v, d)
			ui.Column(c).Grow(1).Shrink(1).Children(func() {
				a.commitPane(c, v, &cd.commit, &cd.listOther, cd.other, cd.cur, cd.onlyOther)
			})
			ui.Box(c).Height(1).Shrink(0).Background(colors(t).line)
			ui.Column(c).Grow(1).Shrink(1).Children(func() {
				a.commitPane(c, v, &cd.commit, &cd.listCur, cd.cur, cd.other, cd.onlyCur)
			})
		})
		if cd.commit != nil {
			ui.Box(c).Width(1).Shrink(0).Background(colors(t).line)
			ui.Column(c).Width(340).Shrink(0).Children(func() { a.commitDetail(c, w, v, cd.commit) })
		}
	})
}

// compareBar 是比较页顶上一行：比较的两个分支，交换两边，刷新。
func (a *app) compareBar(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	cd := d.comp
	ui.Row(c).Height(40).Shrink(0).Padding(0, 10, 0, 16).Gap(6).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "比较").FontSize(12.5).TextColor(t.TextMuted)
		ui.Text(c, cd.other).FontSize(12.5).Font(monoFont).FontWeight(500).SingleLine().Shrink(1)
		ui.Text(c, "和").FontSize(12.5).TextColor(t.TextMuted)
		ui.Text(c, cd.cur).FontSize(12.5).Font(monoFont).FontWeight(500).SingleLine().Shrink(1)
		ui.Spacer(c)
		if pressable(textButton(c, "交换")).Tooltip("对调两边的分支").Clicked() {
			// 对调后的比较已经开着就切过去，不留两个同样的标签
			if i := slices.IndexFunc(v.docs, func(x *fileDoc) bool { return x.path == compareKey(cd.cur, cd.other, false) }); i >= 0 {
				v.cur = v.docs[i]
				return
			}
			cd.other, cd.cur = cd.cur, cd.other
			cd.onlyOther, cd.onlyCur = cd.onlyCur, cd.onlyOther
			cd.listOther, cd.listCur = cd.listCur, cd.listOther
			d.path = compareKey(cd.other, cd.cur, false)
		}
		if pressable(iconButtonSized(c, iconRefresh, "刷新", 24, 14)).Clicked() {
			a.loadCompare(v, d)
		}
	})
	ui.Box(c).Height(1).Shrink(0).Background(colors(t).line)
}

// commitPane 是一边独有的提交：琥珀色横幅说明是哪边，下面是提交列表。
func (a *app) commitPane(c *ui.Context, v *filesView, sel **commitView, list *ui.ListState, in, notIn string, commits []gitx.CommitInfo) {
	t := c.Theme()
	p := colors(t)
	ui.Row(c).Height(34).Shrink(0).Padding(0, 16).Gap(8).AlignItems(ui.Center).Background(p.honey.Alpha(0.12)).Children(func() {
		ui.Icon(c, iconHexAlert).FontSize(13).TextColor(p.honey).Shrink(0)
		ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(1).Children(func() {
			ui.Text(c, "存在于").FontSize(12.5)
			ui.Text(c, in).FontSize(12.5).Font(monoFont).FontWeight(600).SingleLine().Shrink(1)
			ui.Text(c, "中但不存在于").FontSize(12.5)
			ui.Text(c, notIn).FontSize(12.5).Font(monoFont).FontWeight(600).SingleLine().Shrink(1)
			ui.Text(c, "中的提交").FontSize(12.5)
		})
		ui.Spacer(c)
		ui.Text(c, fmt.Sprintf("%d 个", len(commits))).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
	})
	if len(commits) == 0 {
		ui.Text(c, fmt.Sprintf("没有，%s 已经包含 %s 的全部提交", notIn, in)).FontSize(12).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	list.Key = func(i int) any { return commits[i].Hash }
	ui.List(c, list, len(commits), func(i int) {
		ci := commits[i]
		if commitRow(c, ci, *sel != nil && (*sel).info.Hash == ci.Hash, i == 0, i == len(commits)-1) {
			a.selectCommit(v, sel, ci)
		}
	}).Grow(1).Padding(4, 0, 8, 0)
}

// commitRow 是比较页的一个提交，和 Git 日志一个样子：左边一条线串起圆点，标题、分支和标签、作者、时间。
// first、last 决定线从哪画到哪。返回是否点了它。
func commitRow(c *ui.Context, ci gitx.CommitInfo, selected, first, last bool) bool {
	t := c.Theme()
	row := ui.Row(c.Key(ci.Hash)).Height(logRowH).Padding(0, 14, 0, 14).Gap(10).AlignItems(ui.Center).Role(ui.RoleButton).Label(ci.Subject)
	bg := ui.Transparent
	switch {
	case selected:
		bg = colors(t).tint
	case row.Hovered():
		bg = colors(t).wash
	}
	row.Background(row.AnimateColor("bg", bg, hover))
	row.Children(func() {
		lane := laneColor(0)
		ui.Box(c).Width(12).Height(logRowH).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
			cx, cy := r.X+r.W/2, r.Y+r.H/2
			top, bot := r.Y, r.Y+r.H
			if first {
				top = cy
			}
			if last {
				bot = cy
			}
			if bot > top {
				p.Fill(ui.Rect{X: cx - 0.8, Y: top, W: 1.6, H: bot - top}, lane, 0)
			}
			p.Fill(ui.Rect{X: cx - 4, Y: cy - 4, W: 8, H: 8}, lane, 4)
		})
		ui.Text(c, ci.Subject).FontSize(12.5).SingleLine().Grow(1).Shrink(1)
		for _, ref := range refNames(ci.Refs) {
			rbg, fg := t.Surface, t.TextMuted
			if ref.head {
				rbg, fg = colors(t).tint, t.Accent
			}
			ui.Text(c, ref.name).FontSize(10.5).Font(monoFont).TextColor(fg).Padding(1, 5).Radius(4).Background(rbg).SingleLine().Shrink(0).MaxWidth(160)
		}
		ui.Text(c, ci.Author).FontSize(11.5).TextColor(t.TextMuted).SingleLine().MaxWidth(110).Shrink(0)
		ui.Text(c, commitTime(ci.Time)).FontSize(11.5).FontFeatures("tnum").TextColor(t.TextMuted).Width(88).TextAlign(ui.End).Shrink(0)
	})
	return row.Clicked()
}

// commitTime 写成"今天 14:26""昨天 17:04""10月3日"，跨年的带年份，和 WebStorm 一样看得出先后。
func commitTime(at time.Time) string {
	at = at.Local()
	now := time.Now()
	day := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local) }
	switch d := day(now).Sub(day(at)); {
	case d == 0:
		return "今天 " + at.Format("15:04")
	case d == 24*time.Hour:
		return "昨天 " + at.Format("15:04")
	case at.Year() == now.Year():
		return at.Format("1月2日")
	}
	return at.Format("2006-01-02")
}

// commitDetail 是选中提交的说明和它改的文件，点文件看这个提交里的改动。
func (a *app) commitDetail(c *ui.Context, w *workspace, v *filesView, cv *commitView) {
	t := c.Theme()
	ci := cv.info
	ui.Column(c).Padding(12, 14, 8, 14).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, ci.Subject).FontSize(13).FontWeight(500).LineHeight(1.35).Selectable()
		ui.Text(c, ci.Hash[:10]+"  "+ci.Author+"  "+ci.Time.Format("2006-01-02 15:04")).FontSize(11).Font(monoFont).TextColor(t.TextMuted).Selectable()
		if ci.Refs != "" {
			ui.Text(c, ci.Refs).FontSize(11).TextColor(t.TextMuted).SingleLine()
		}
	})
	switch {
	case cv.err != "":
		ui.Text(c, cv.err).FontSize(12).TextColor(t.Danger).Padding(6, 14)
	case cv.files == nil:
		ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(6, 14)
	default:
		a.fileChangeList(c, w, v, &cv.list, &cv.folded, cv.files, commitSpecs(ci, cv.files))
	}
}

// fileChangeList 是一组改了的文件，照 WebStorm 按目录排成树，点文件打开对应的 diff，F7 在这组里跳。
// specs 为空时只列文件、不能点（Push 对话框里 diff 会被对话框挡住）。
func (a *app) fileChangeList(c *ui.Context, w *workspace, v *filesView, list *ui.ListState, folded *map[string]bool, files []gitx.FileChange, specs []diffSpec) {
	t := c.Theme()
	p := colors(t)
	if *folded == nil {
		*folded = map[string]bool{}
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	rows := changeTree(paths, *folded)
	list.Key = func(i int) any { return rows[i].path }
	ui.List(c, list, len(rows), func(i int) {
		r := rows[i]
		row := ui.Row(c.Key(r.path)).Height(26).Margin(0, 6).Padding(0, 8, 0, 4+float32(r.depth)*18).Gap(6).Radius(6).AlignItems(ui.Center)
		if r.dir {
			row.Role(ui.RoleButton).Label(r.path)
			row.Children(func() {
				// 整行可点，箭头只是指示，不另接点击
				turn := float32(90)
				if (*folded)[r.path] {
					turn = 0
				}
				ui.Box(c).Size(20, 20).Center().Shrink(0).Children(func() {
					chev := ui.Icon(c, iconChevron).FontSize(12).TextColor(t.TextMuted)
					chev.Rotate(chev.AnimateWith("turn", turn, 150*time.Millisecond, ui.EaseOut))
				})
				ui.Icon(c, iconFolder).FontSize(15).TextColor(t.Accent).Shrink(0)
				ui.Text(c, r.name).FontSize(12.5).SingleLine().Shrink(1).Tooltip(r.path)
				ui.Text(c, fmt.Sprint(len(r.files))).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			if row.Clicked() {
				(*folded)[r.path] = !(*folded)[r.path]
			}
			return
		}
		f := files[r.entry]
		if specs != nil {
			row.Role(ui.RoleButton).Label(f.Path)
			selectedBg(c, row, v.cur != nil && v.cur.path == diffKey(v.git.absPath(f.Path), specs[r.entry]))
		}
		color := t.Accent
		switch f.Status {
		case 'A':
			color = p.sage
		case 'D':
			color = t.TextMuted
		}
		row.Children(func() {
			// 文件没有展开箭头，留出同样的宽度，和上一层目录名对齐
			ui.Box(c).Width(20).Shrink(0)
			ui.Text(c, string(f.Status)).FontSize(11).Font(monoFont).FontWeight(500).TextColor(color).Width(10).Shrink(0)
			name := ui.Text(c, r.name).FontSize(12.5).SingleLine().Shrink(1).Tooltip(f.Path)
			if f.Status == 'D' {
				name.Strikethrough().TextColor(t.TextMuted)
			}
		})
		if specs != nil && row.Clicked() {
			a.openDiff(w, specs[r.entry], specs, true)
		}
	}).Grow(1).Padding(0, 0, 8, 0)
}

// --- Push 对话框

type pushState struct {
	ws      string
	branch  string
	plan    gitx.PushPlan
	loaded  bool
	err     string
	commit  *commitView
	tags    bool
	pushing bool
}

// openPush 推送当前分支。
func (a *app) openPush(w *workspace) {
	// 直接读 HEAD：文件栏没打开过时没有状态，w.Branch 在游离时是哈希
	branch, detached := gitx.HeadRef(w.Path)
	if branch == "" || detached {
		a.err = "现在不在分支上（分离的 HEAD），没法推送"
		return
	}
	a.openPushBranch(w, branch)
}

func (a *app) openPushBranch(w *workspace, branch string) {
	v := a.filesFor(w)
	ps := &pushState{ws: w.ID, branch: branch}
	a.push = ps
	ws := v.root
	go func() {
		ctx := context.Background()
		root := repoRoot(ws, gitx.TopLevel(ctx, ws))
		plan, err := gitx.PushPreview(ctx, root, branch)
		a.update(func() {
			if !v.git.checked {
				v.git.checked, v.git.repo = true, root
			}
			ps.plan, ps.loaded = plan, true
			if err != nil {
				ps.err = err.Error()
			}
		})
	}()
}

func (a *app) pushDialog(c *ui.Context) {
	t := c.Theme()
	ps := a.push
	open := ps != nil
	ui.Modal(c, &open, func() {
		if ps == nil {
			return
		}
		w := a.workspace(ps.ws)
		if w == nil {
			a.push = nil
			return
		}
		v := a.filesFor(w)
		ui.Column(c).Width(760).Gap(12).Children(func() {
			titleText(c, "推送提交", fsCardTitle)
			ui.Row(c).Height(320).Gap(0).AlignItems(ui.Stretch).Children(func() {
				ui.Column(c).Width(380).Shrink(0).Children(func() {
					plan := ps.plan
					switch {
					case !ps.loaded:
						ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(4)
						return
					case ps.err != "":
						ui.Text(c, ps.err).FontSize(12).TextColor(t.Danger).Padding(4).LineHeight(1.4)
						return
					}
					ui.Row(c).Gap(6).Padding(2, 4, 8, 4).Children(func() {
						ui.Text(c, plan.Branch+" → "+plan.Remote+" : "+plan.RemoteBranch).FontSize(12.5).Font(monoFont).FontWeight(500).SingleLine().Shrink(1)
						if plan.NewBranch {
							ui.Text(c, "新分支").FontSize(11).TextColor(colors(t).sage).Shrink(0)
						}
					})
					if plan.Gone {
						ui.Text(c, "跟踪的远端分支已经被删了，推送会在 "+plan.Remote+" 上重新建出 "+plan.RemoteBranch+"").FontSize(11.5).TextColor(t.TextMuted).Padding(0, 4, 8, 4).LineHeight(1.4)
					}
					if len(plan.Commits) == 0 {
						ui.Text(c, "没有要推送的提交").FontSize(12).TextColor(t.TextMuted).Padding(4)
						return
					}
					ui.Scroll(c).Grow(1).Children(func() {
						ui.Column(c).Gap(1).Children(func() {
							for i, ci := range plan.Commits {
								if commitRow(c, ci, ps.commit != nil && ps.commit.info.Hash == ci.Hash, i == 0, i == len(plan.Commits)-1) {
									a.selectCommit(v, &ps.commit, ci)
								}
							}
						})
					})
				})
				ui.Box(c).Width(1).Shrink(0).Background(colors(t).line)
				ui.Column(c).Grow(1).Children(func() {
					if ps.commit == nil {
						ui.Text(c, "选一个提交看它改的文件").FontSize(12).TextColor(t.TextMuted).Padding(12, 14)
						return
					}
					cv := ps.commit
					ui.Column(c).Padding(4, 14, 6, 14).Gap(4).Shrink(0).Children(func() {
						ui.Text(c, cv.info.Subject).FontSize(13).FontWeight(500)
						ui.Text(c, cv.info.Hash[:10]+"  "+cv.info.Author).FontSize(11).Font(monoFont).TextColor(t.TextMuted)
					})
					switch {
					case cv.err != "":
						ui.Text(c, cv.err).FontSize(12).TextColor(t.Danger).Padding(6, 14)
					case cv.files == nil:
						ui.Text(c, "正在读取…").FontSize(12).TextColor(t.TextMuted).Padding(6, 14)
					default:
						a.fileChangeList(c, w, v, &cv.list, &cv.folded, cv.files, nil)
					}
				})
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Checkbox(c, &ps.tags, "一起推送标签")
				ui.Spacer(c)
				if pressable(textButton(c, "取消")).Clicked() {
					a.push = nil
				}
				ready := ps.loaded && ps.err == "" && (len(ps.plan.Commits) > 0 || ps.plan.NewBranch) && !ps.pushing
				force := pressable(textButton(c, "强制推送…")).Tooltip("--force-with-lease --force-if-includes：远端有你没合进来的提交时会被拒绝，不会覆盖别人的提交")
				push := pressable(accentButton(c, "推送"))
				if !ready {
					force.Disabled(true)
					push.Disabled(true)
				}
				if ready && push.Clicked() {
					a.doPush(v, ps, false)
				}
				if ready && force.Clicked() {
					a.confirm("强制推送 "+ps.plan.Branch+"？", "会用本地的历史替换远端的 "+ps.plan.Remote+"/"+ps.plan.RemoteBranch+"。远端有你还没合进来的提交时会被拒绝，不会覆盖别人的提交。", "强制推送", func() {
						a.update(func() { a.doPush(v, ps, true) })
					})
				}
			})
		})
	})
	if !open && ps != nil && !ps.pushing {
		a.push = nil
	}
}

func (a *app) doPush(v *filesView, ps *pushState, force bool) {
	ps.pushing = true
	plan, tags := ps.plan, ps.tags
	a.gitDo(v, "Push", func(ctx context.Context, root string) error {
		err := gitx.Push(ctx, root, plan, force, tags)
		a.update(func() {
			ps.pushing = false
			if err == nil && a.push == ps {
				a.push = nil
			}
		})
		if gitx.Rejected(err) {
			if force {
				return fmt.Errorf("远端有你还没合进来的提交，强制推送被拒绝；先更新分支看看是谁推的")
			}
			return fmt.Errorf("远端有你还没拉到的提交；先更新分支再推送，或者确认后强制推送")
		}
		return err
	})
}
