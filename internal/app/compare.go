package app

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/errtext"
	"posse/internal/gitx"
	"posse/internal/rpc"
	"posse/locales"
)

// 分支比较的标签页，照 WebStorm：
//   - 和当前分支比较：两边各自独有的提交，选中提交看它改的文件；
//   - 和工作区比较：工作区（含没提交的改动）和那个分支不同的文件。
// 点文件打开 diff，F7 在同一组文件之间跳。

type compareDoc struct {
	worktree                       *gitx.WorktreeComparison
	read                           gitRead
	request                        appRequest
	repository                     *gitState
	loading, loaded, resume, drawn bool
	err                            string
	otherOID, curOID               string
	moreOther, moreCur             bool
	epoch                          uint64 // 本地重读的代次，旧结果不能盖住后来的 Fetch
	root                           string // 仓库根目录
	other, cur                     string
	files                          bool
	onlyOther                      []gitx.CommitInfo
	onlyCur                        []gitx.CommitInfo
	changes                        []gitx.FileChange
	commit                         *commitView // 右边看着的提交
	picked                         []string    // ⌘、⇧ 点选的多个提交，都在同一边；空时选中的就是 commit
	anchor                         string      // ⇧ 点选范围的起点
	queryOther                     string      // 两栏各自的搜索词，只筛这一栏显示的提交
	queryCur                       string
	list                           ui.ListState
	folded                         map[string]bool // 文件树里收起的目录
	cursor                         string          // 和工作区比较时键盘停在的文件
	listOther                      ui.ListState    // 只在 other 里的提交
	listCur                        ui.ListState    // 只在 cur 里的提交
}

// commitView 是选中的一个提交和它改的文件。
type commitView struct {
	diffBase *string
	read     gitRead
	loading  bool
	root     string // 仓库根目录
	info     gitx.CommitInfo
	files    []gitx.FileChange
	err      string
	list     ui.ListState
	folded   map[string]bool // 文件树里收起的目录
	focus    bool            // 在提交列表里按了 →：文件列表建出来时拿焦点
	cursor   string          // 键盘停在的文件（diff 会挡住列表时才用）
}

func compareKey(root, other, cur string, files bool) string {
	if files {
		return "compare:files:" + other + "@" + root
	}
	return "compare:" + other + ".." + cur + "@" + root
}

func (a *app) openCompare(w *workspace, other, cur string, files bool) {
	if a.remoteHost() && !a.targetGitReady() {
		a.targetFileUnsupported("git.compare")
		return
	}
	// 比较要整块地方看上下两栏，底部的日志浮层盖着下半截，先收起
	a.setLogPanel(false)
	v := a.showFiles(w)
	key := compareKey(v.git.repo, other, cur, files)
	for _, d := range v.docs {
		if d.path == key {
			v.cur = d
			return
		}
	}
	d := &fileDoc{path: key, comp: &compareDoc{root: v.git.repo, other: other, cur: cur, files: files}, started: true}
	v.insertDoc(d)
	a.loadCompare(v, d)
}

// refsChanged 在 fetch、拉取、推送等改了引用的操作之后，重读一个项目里所有依赖引用的界面：
// 分支按钮的传入传出、分支弹窗、分支比较、按分支名比较的 diff、日志和文件历史。
// 比较里对着某一端顶端提交打开的 diff 不在这里重读（两边都是哈希），等比较结果回来后跟着新顶端走。
// 同一项目的工作区共用引用，别的工作区里开着的也要重读。
func (a *app) refsChanged(projectID string) {
	for _, w := range a.byProject[projectID] {
		v := a.files[w.ID]
		if v == nil {
			continue
		}
		for _, g := range v.repos.list {
			if tr := a.track[trackKey(&w, g.repo)]; tr != nil {
				tr.poll = panelPoll{}
			}
			if g.branches.list != nil {
				a.loadBranches(g)
			}
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
			if d.log == nil {
				continue
			}
			if d.log.loading {
				d.log.again = true
				continue
			}
			a.reloadLog(v, d)
		}
	}
	a.reloadOpenPush(projectID)
}

func (a *app) loadCompare(v *filesView, d *fileDoc) {
	if a.remoteHost() {
		if d.comp.files {
			a.loadTargetWorktreeCompare(v, d)
			return
		}
		a.loadTargetCompare(v, d)
		return
	}
	cd := d.comp
	cd.epoch++
	epoch := cd.epoch
	root := cd.root
	other, cur := cd.other, cd.cur
	go func() {
		ctx := context.Background()
		var onlyOther, onlyCur []gitx.CommitInfo
		var changes []gitx.FileChange
		var err error
		otherOID, curOID := "", ""
		if cd.files {
			changes, err = gitx.DiffWithWorkingTree(ctx, root, other)
		} else if onlyOther, err = gitx.CommitsBetween(ctx, root, cur, other, 1000); err == nil {
			onlyCur, err = gitx.CommitsBetween(ctx, root, other, cur, 1000)
		}
		if err == nil {
			// 顶端提交用来判断开着的 diff 要不要跟着分支走；解析失败就留着上次的
			if oid, e := gitx.ResolveBranchCommit(ctx, root, other); e == nil {
				otherOID = oid
			}
			if oid, e := gitx.ResolveBranchCommit(ctx, root, cur); e == nil {
				curOID = oid
			}
		}
		a.update(func() {
			if cd.epoch != epoch || d.comp != cd || !slices.Contains(v.docs, d) {
				return
			}
			// 读的时候用户点了交换：结果跟着对调
			if cd.other != other {
				onlyOther, onlyCur = onlyCur, onlyOther
				otherOID, curOID = curOID, otherOID
			}
			if err != nil {
				d.kind, d.err = docError, errtext.Text(err)
				return
			}
			prevOther, prevCur := cd.otherOID, cd.curOID
			if otherOID == "" {
				otherOID = prevOther
			}
			if curOID == "" {
				curOID = prevCur
			}
			cd.onlyOther, cd.onlyCur, cd.changes = onlyOther, onlyCur, changes
			cd.otherOID, cd.curOID = otherOID, curOID
			// 刷新后已经不在两边的提交（比如 cherry-pick 过去了）不再算选中
			cd.picked = slices.DeleteFunc(cd.picked, func(h string) bool {
				has := func(x gitx.CommitInfo) bool { return x.Hash == h }
				return !slices.ContainsFunc(onlyOther, has) && !slices.ContainsFunc(onlyCur, has)
			})
			d.kind = docCompare
			a.followCompareDiffs(v, d, prevOther, prevCur)
		})
	}()
}

// followCompareDiffs 在比较重读之后，把开着的 diff 对齐到新的分支顶端。
// 提交哈希的内容不会变，所以 Fetch、更新之后 diff 标签会停在旧顶端；当时看的正是移动前的顶端时，改看新顶端里的同一个文件。
// 更早的提交不动。和工作区比较的 diff 按分支名或新的基线重读。
func (a *app) followCompareDiffs(v *filesView, d *fileDoc, prevOther, prevCur string) {
	cd := d.comp
	if cd == nil {
		return
	}
	if cd.files {
		a.syncCompareFileDiff(v, cd)
		return
	}
	doc, info, prev, ok := compareTipDiff(v, cd, prevOther, prevCur)
	a.followCompareSelection(cd, prevOther, prevCur)
	if !ok {
		return
	}
	if a.remoteHost() {
		a.followTargetCompareDiff(v, d, doc, info, prev)
		return
	}
	w := a.workspace(v.wsID)
	if w == nil {
		return
	}
	rel, root := doc.diff.rel, cd.root
	go func() {
		files, err := gitx.CommitFiles(context.Background(), root, info.Hash)
		a.update(func() {
			if !compareTipCurrent(v, d, cd, doc, prev, rel) {
				return
			}
			if err != nil {
				return
			}
			// 这次读完时顶端又往前了：丢掉这次，按比较里现在的顶端再跟
			if info.Hash != cd.otherOID && info.Hash != cd.curOID {
				a.followCompareDiffs(v, d, prevOther, prevCur)
				return
			}
			specs := commitSpecs(root, info, files)
			for _, s := range specs {
				if s.rel == rel {
					a.reopenDiff(w, v, s, specs, true)
					return
				}
			}
			// 新顶端没改这个文件：改成两边分支上这个文件的差别，之后引用再动还会重读
			spec := diffSpec{root: root, rel: rel, oldRev: cd.cur, newRev: cd.other, label: cd.other}
			if info.Hash == cd.curOID {
				spec.oldRev, spec.newRev, spec.label = cd.other, cd.cur, cd.cur
			}
			a.reopenDiff(w, v, spec, []diffSpec{spec}, true)
		})
	}()
}

// compareTipDiff 找到正显示某一端旧顶端的 diff，以及这一端现在的顶端提交。
func compareTipDiff(v *filesView, cd *compareDoc, prevOther, prevCur string) (*fileDoc, gitx.CommitInfo, string, bool) {
	for _, doc := range v.docs {
		df := doc.diff
		if df == nil || df.root != cd.root || df.newRev == "" {
			continue
		}
		switch {
		case prevOther != "" && df.newRev == prevOther && cd.otherOID != prevOther:
			if info, ok := commitByHash(cd.onlyOther, cd.otherOID); ok {
				return doc, info, prevOther, true
			}
		case prevCur != "" && df.newRev == prevCur && cd.curOID != prevCur:
			if info, ok := commitByHash(cd.onlyCur, cd.curOID); ok {
				return doc, info, prevCur, true
			}
		}
	}
	return nil, gitx.CommitInfo{}, "", false
}

// diffStillOpen 是这个 diff 标签还开着，而且仍显示当时那次提交里的同一个文件。
func diffStillOpen(v *filesView, doc *fileDoc, prev, rel string) bool {
	return v != nil && doc != nil && slices.Contains(v.docs, doc) && doc.diff != nil && doc.diff.newRev == prev && doc.diff.rel == rel
}

// compareTipCurrent 是比较和它要跟着走的 diff 都还在。
func compareTipCurrent(v *filesView, d *fileDoc, cd *compareDoc, doc *fileDoc, prev, rel string) bool {
	return diffStillOpen(v, doc, prev, rel) && slices.Contains(v.docs, d) && d.comp == cd
}

func commitByHash(list []gitx.CommitInfo, hash string) (gitx.CommitInfo, bool) {
	for _, c := range list {
		if hash != "" && c.Hash == hash {
			return c, true
		}
	}
	return gitx.CommitInfo{}, false
}

// followCompareSelection 让比较右边看着的提交跟着顶端走；新顶端不在列表里就收起。
func (a *app) followCompareSelection(cd *compareDoc, prevOther, prevCur string) {
	cv := cd.commit
	if cv == nil {
		return
	}
	var list []gitx.CommitInfo
	var tip string
	switch cv.info.Hash {
	case prevOther:
		if cd.otherOID == prevOther {
			return
		}
		list, tip = cd.onlyOther, cd.otherOID
	case prevCur:
		if cd.curOID == prevCur {
			return
		}
		list, tip = cd.onlyCur, cd.curOID
	default:
		return
	}
	if info, ok := commitByHash(list, tip); ok {
		a.selectCommit(cd.root, &cd.commit, info)
		return
	}
	cd.commit = nil
}

// syncCompareFileDiff 重读「和工作区比较」打开的 diff。分支名会在 refsChanged 里重读；
// 远端把旧提交钉死了，基线变了要按新的比较结果重新打开。
func (a *app) syncCompareFileDiff(v *filesView, cd *compareDoc) {
	w := a.workspace(v.wsID)
	if w == nil {
		return
	}
	for _, doc := range v.docs {
		df := doc.diff
		if df == nil || df.root != cd.root || !df.worktree() {
			continue
		}
		if df.oldRev != cd.other && (df.worktreeBase == nil || df.worktreeBase.revision != cd.other) {
			continue
		}
		specs := compareFileSpecs(cd, df.label)
		df.siblings = specs
		next, ok := specForPath(specs, df.rel)
		if !ok || specSameBase(df.diffSpec, next) {
			continue
		}
		next.label = df.label
		a.reopenDiff(w, v, next, specs, df.first)
	}
}

func compareFileSpecs(cd *compareDoc, label string) []diffSpec {
	if cd.worktree != nil {
		out := make([]diffSpec, len(cd.worktree.Files))
		for i, f := range cd.worktree.Files {
			out[i] = diffSpec{root: cd.root, rel: f.Path, orig: f.Orig, oldRev: cd.worktree.Commit, label: label, worktreeBase: &worktreeBaseline{baseline: cd.worktree.Baseline, revision: cd.other, file: f}}
		}
		return out
	}
	out := make([]diffSpec, len(cd.changes))
	for i, f := range cd.changes {
		out[i] = diffSpec{root: cd.root, rel: f.Path, orig: f.Orig, oldRev: cd.other, label: label}
	}
	return out
}

func specForPath(specs []diffSpec, rel string) (diffSpec, bool) {
	for _, s := range specs {
		if s.rel == rel {
			return s, true
		}
	}
	return diffSpec{}, false
}

func specSameBase(a, b diffSpec) bool {
	if a.oldRev != b.oldRev || a.newRev != b.newRev || a.rel != b.rel || a.orig != b.orig {
		return false
	}
	if (a.worktreeBase == nil) != (b.worktreeBase == nil) {
		return false
	}
	if a.worktreeBase == nil {
		return true
	}
	return a.worktreeBase.baseline == b.worktreeBase.baseline && a.worktreeBase.revision == b.worktreeBase.revision
}

// selectCommit 选中提交，在后台读它改的文件。
func (a *app) selectCommit(root string, holder **commitView, c gitx.CommitInfo) {
	if a.remoteHost() || a.push != nil && a.push.remote && holder == &a.push.commit {
		a.selectTargetCommit(root, holder, c)
		return
	}
	cv := &commitView{info: c, root: root}
	*holder = cv
	go func() {
		files, err := gitx.CommitFiles(context.Background(), root, c.Hash)
		a.update(func() {
			cv.files = files
			if err != nil {
				cv.err = errtext.Text(err)
			}
		})
	}()
}

// commitSpecs 是一个提交里各文件的 diff：父提交对比这个提交，根提交的旧一边是空的。
func commitSpecs(root string, c gitx.CommitInfo, files []gitx.FileChange) []diffSpec {
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
		out[i] = diffSpec{root: root, rel: f.Path, orig: f.Orig, oldRev: old, newRev: c.Hash, label: c.Hash[:7]}
	}
	return out
}

func (a *app) compareView(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	t := c.Theme()
	cd := d.comp
	if v.remote {
		cd.drawn = true
		a.resumeTargetCompare(v, d)
		if cd.err != "" {
			ui.Text(c, cd.err).FontSize(fsSmall).TextColor(t.Danger).Padding(6, 16)
		}
		if cd.loading {
			ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 16)
		}
	}
	if cd.files {
		specs := make([]diffSpec, len(cd.changes))
		for i, f := range cd.changes {
			specs[i] = diffSpec{root: cd.root, rel: f.Path, orig: f.Orig, oldRev: cd.other, label: cd.other + " → " + L(c).GitWorkspace()}
		}
		if v.remote {
			specs = cd.worktreeSpecs(L(c))
		}
		ui.Row(c).Height(sz(36)).Shrink(0).Padding(0, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, L(c).CompareWorkspaceFiles(cd.other, len(cd.changes))).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Shrink(1)
			ui.Spacer(c)
			if v.remote && pressable(iconButtonSized(c, iconRefresh, L(c).CommonRefresh(), compactBox(), smallIcon())).Disabled(cd.loading).Clicked() {
				a.loadCompare(v, d)
			}
		})
		if v.remote && !cd.loaded {
			return
		}
		if len(cd.changes) == 0 {
			ui.Text(c, L(c).CompareNoDifference()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(8, 16)
			return
		}
		selected, open := a.tabDiffs(w, v, specs)
		a.fileChangeList(c, &cd.list, &cd.folded, cd.changes, selected, open, fileListNav{cursor: &cd.cursor})
		return
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Children(func() {
		// 照 WebStorm：上下两栏，各是一边独有的提交
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			// 照 WebStorm 上下两栏默认各一半，拖中间的分隔条调比例，所有比较共用
			split := a.compareSplit()
			panes := ui.Column(c.Key("compare-panes")).Grow(1).Shrink(1)
			panes.Children(func() {
				// 两边各一种颜色（照 WebStorm 的分支颜色）：标题里的分支名和提交的圆点同色，一眼分得出是哪边
				ui.Column(c).Height(0).Grow(split).Shrink(1).Children(func() {
					a.commitPane(c, v, cd, &cd.listOther, &cd.queryOther, cd.other, cd.cur, cd.onlyOther, laneColor(t, 0), func() { a.compareActions(c, w, v, d) })
				})
				a.compareGrip(c, panes.Bounds().H)
				ui.Column(c).Height(0).Grow(1 - split).Shrink(1).Children(func() {
					a.commitPane(c, v, cd, &cd.listCur, &cd.queryCur, cd.cur, cd.other, cd.onlyCur, laneColor(t, 1), nil)
				})
			})
		})
		if cd.commit != nil {
			vline(c)
			ui.Column(c).Width(340).Shrink(0).Children(func() {
				// ← 回到选中提交所在的那一栏
				a.commitDetail(c, w, v, cd.commit, false, func() {
					if slices.ContainsFunc(cd.onlyOther, func(x gitx.CommitInfo) bool { return x.Hash == cd.commit.info.Hash }) {
						cd.listOther.Focus(c)
					} else {
						cd.listCur.Focus(c)
					}
				})
			})
		}
	})
}

// compareSplit 是分支比较上一栏占两栏的比例。
func (a *app) compareSplit() float32 {
	if s := a.state.CompareSplit; s > 0 && s < 1 {
		return s
	}
	return 0.5
}

// compareGrip 是两栏之间的分隔条：看上去是一条细线，上下拖调比例，松手才写 ui.json。total 是两栏连同它的高度。
func (a *app) compareGrip(c *ui.Context, total float32) {
	t := c.Theme()
	// 线只有一个物理像素，拖的地方留宽一些
	const gripH = 5
	grip := ui.Box(c.Key("compare-grip")).Height(gripH).Shrink(0).Cursor(ui.CursorResizeNS)
	if _, dy, held := grip.Dragged(); held && total > gripH {
		room := total - gripH
		// 每栏至少留出标题和一行提交
		least := min((sz(42)+logRowH()+8)/room, 0.5)
		a.state.CompareSplit = min(max(a.compareSplit()+dy/room, least), 1-least)
		a.resizingCompare = true
	} else if a.resizingCompare && !held {
		a.resizingCompare = false
		a.saveState()
	}
	line, h := colors(t).line, float32(hairline)
	if grip.Hovered() || a.resizingCompare {
		line, h = t.Accent, 2
	}
	grip.Draw(func(pt *ui.Painter, r ui.Rect) {
		pt.Fill(ui.Rect{X: r.X, Y: r.Y + (r.H-h)/2, W: r.W, H: h}, line, 0)
	})
}

// compareActions 是上一栏标题行右边的操作：Cherry-pick、交换两边、刷新。
func (a *app) compareActions(c *ui.Context, w *workspace, v *filesView, d *fileDoc) {
	cd := d.comp
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(0).Children(func() {
		a.comparePick(c, w, v, cd)
		if pressable(iconButtonSized(c, iconSwap, L(c).CompareSwap(), compactBox(), smallIcon())).Clicked() {
			// 对调后的比较已经开着就切过去，不留两个同样的标签
			if i := slices.IndexFunc(v.docs, func(x *fileDoc) bool { return x.path == compareKey(cd.root, cd.cur, cd.other, false) }); i >= 0 {
				v.cur = v.docs[i]
				return
			}
			if v.remote {
				a.swapTargetCompare(v, d)
			} else {
				cd.other, cd.cur = cd.cur, cd.other
				cd.otherOID, cd.curOID = cd.curOID, cd.otherOID
				cd.onlyOther, cd.onlyCur = cd.onlyCur, cd.onlyOther
				cd.listOther, cd.listCur = cd.listCur, cd.listOther
				cd.queryOther, cd.queryCur = cd.queryCur, cd.queryOther
				d.path = compareKey(cd.root, cd.other, cd.cur, false)
			}
		}
		if pressable(iconButtonSized(c, iconRefresh, L(c).CommonRefresh(), compactBox(), smallIcon())).Clicked() {
			a.loadCompare(v, d)
		}
	})
}

// comparePick 是 Cherry-pick 按钮：把选中的提交按提交先后应用到当前分支。
// cherry-pick 只能落在检出的分支上，所以选中的提交要都来自另一边、另一边又正好是工作区检出的分支才可点。
func (a *app) comparePick(c *ui.Context, w *workspace, v *filesView, cd *compareDoc) {
	if !a.pathsControls() {
		return
	}
	m := L(c)
	target, tip := "", m.CompareSelectHint()
	var hashes []string
	if sel := cd.selected(); len(sel) > 0 {
		// 列表从新到旧，倒过来才是 cherry-pick 的先后
		inOrder := func(cs []gitx.CommitInfo) []string {
			var out []string
			for _, ci := range slices.Backward(cs) {
				if slices.Contains(sel, ci.Hash) {
					out = append(out, ci.Hash)
				}
			}
			return out
		}
		switch {
		case len(inOrder(cd.onlyOther)) == len(sel):
			target, hashes = cd.cur, inOrder(cd.onlyOther)
		case len(inOrder(cd.onlyCur)) == len(sel):
			target, hashes = cd.other, inOrder(cd.onlyCur)
		}
		// 比较的可能是工作区里的嵌套仓库：看这个仓库自己检出的分支，不是工作区目录的 w.Branch
		if branch := v.repoBranch(cd.root); target != "" && target != branch {
			tip = m.CompareCherryPickCheckedOut(branch)
			target = ""
		}
	}
	switch {
	case target == "":
	case len(hashes) == 1:
		tip = m.CompareCherryPickOne(hashes[0][:7], target)
	default:
		tip = m.CompareCherryPickMany(len(hashes), target)
	}
	b := quietIconButton(c, iconCherryPick, m.GitCherryPick(), compactBox(), smallIcon()).Tooltip(m.CompareCherryPickTip(tip))
	if target == "" {
		b.Disabled(true)
		return
	}
	if b.Clicked() {
		a.integrateCommits(m, w, v, v.repoOrGone(cd.root), rpc.GitIntegrateCherryPick, hashes)
	}
}

// selected 是选中的提交：多选时是点选的那些，否则是右边看着的那个。
func (cd *compareDoc) selected() []string {
	if len(cd.picked) > 0 {
		return cd.picked
	}
	if cd.commit != nil {
		return []string{cd.commit.info.Hash}
	}
	return nil
}

// clickCommit 照 WebStorm 处理点选：单击只选这一个，⌘ 点加上或去掉，⇧ 点选上从起点到这里的一段。
// 多选只在同一边里，点另一边的提交从头选。all 是这一边全部的提交，shown 是筛选后显示的。
func (a *app) clickCommit(v *filesView, cd *compareDoc, all, shown []gitx.CommitInfo, ci gitx.CommitInfo, mods ui.Modifiers) {
	sel := cd.selected()
	// 按全部提交判断：选中的被搜索藏起来时也还是同一边
	samePane := len(sel) > 0 && slices.ContainsFunc(all, func(x gitx.CommitInfo) bool { return x.Hash == sel[0] })
	switch {
	case mods&ui.Cmd != 0 && samePane:
		if i := slices.Index(sel, ci.Hash); i >= 0 {
			cd.picked = slices.Delete(slices.Clone(sel), i, i+1)
			if cd.commit != nil && cd.commit.info.Hash == ci.Hash {
				cd.commit.read.stop()
				cd.commit = nil
			}
			if len(cd.picked) == 0 {
				cd.picked = nil
			} else if cd.commit == nil {
				// 右边换成还选着的一个，不然只剩一个时就当没选
				i := slices.IndexFunc(shown, func(x gitx.CommitInfo) bool { return slices.Contains(cd.picked, x.Hash) })
				if i >= 0 {
					a.selectCommit(cd.root, &cd.commit, shown[i])
				}
			}
			return
		}
		cd.picked = append(slices.Clone(sel), ci.Hash)
		cd.anchor = ci.Hash
	case mods&ui.Shift != 0 && samePane:
		from := slices.IndexFunc(shown, func(x gitx.CommitInfo) bool { return x.Hash == cd.anchor })
		to := slices.IndexFunc(shown, func(x gitx.CommitInfo) bool { return x.Hash == ci.Hash })
		if from < 0 {
			from = to
		}
		cd.picked = nil
		for _, x := range shown[min(from, to) : max(from, to)+1] {
			cd.picked = append(cd.picked, x.Hash)
		}
	default:
		cd.picked, cd.anchor = nil, ci.Hash
	}
	a.selectCommit(cd.root, &cd.commit, ci)
}

// matchCommit 报告提交的信息、作者里有没有 q（不分大小写），或者哈希以 q 开头。q 已经是小写。
func matchCommit(ci gitx.CommitInfo, q string) bool {
	return strings.Contains(strings.ToLower(ci.Subject), q) || strings.HasPrefix(ci.Hash, q) || strings.Contains(strings.ToLower(ci.Author), q)
}

// commitPane 是一边独有的提交：标题说明是哪边，in 这个分支名和下面提交的圆点都是 lane 色。
func (a *app) commitPane(c *ui.Context, v *filesView, cd *compareDoc, list *ui.ListState, query *string, in, notIn string, all []gitx.CommitInfo, lane ui.Color, actions func()) {
	t := c.Theme()
	p := colors(t)
	commits := all
	q := strings.ToLower(strings.TrimSpace(*query))
	if q != "" {
		commits = slices.DeleteFunc(slices.Clone(all), func(ci gitx.CommitInfo) bool { return !matchCommit(ci, q) })
	}
	m := L(c)
	count := m.CompareCount(len(all))
	if q != "" {
		count = m.CompareFilteredCount(len(commits), len(all))
	}
	more := in == cd.other && cd.moreOther || in == cd.cur && cd.moreCur
	if more {
		count = m.LogMoreCommitCount(len(all))
	}
	// 比较方向单独占一行，打开提交详情后也能辨认两侧分支。
	ui.Row(c).Height(sz(28)).Shrink(0).Padding(0, 16).Gap(8).AlignItems(ui.Center).Background(p.band).Children(func() {
		ui.Icon(c, iconBranch).FontSize(fsBody).TextColor(lane).Shrink(0)
		ui.Row(c).Gap(4).AlignItems(ui.Center).Shrink(1).MinWidth(0).Clip().Tooltip(m.CompareOnlyIn(in, notIn)).Children(func() {
			// 两个分支名换了字体和颜色：整句按语言取出来，在占位处切开，分支名单独画
			for _, part := range strings.Split(m.CompareOnlyIn("\x00in\x00", "\x00notIn\x00"), "\x00") {
				switch part {
				case "":
				case "in":
					// 名称可截断，完整比较方向仍可悬停查看。
					ui.Text(c, in).FontSize(fsCode).Font(monoFont).FontWeight(fwMedium).TextColor(lane).SingleLine().Shrink(1).MinWidth(0).MaxWidth(200)
				case "notIn":
					ui.Text(c, notIn).FontSize(fsCode).Font(monoFont).FontWeight(fwMedium).SingleLine().Shrink(1).MinWidth(0).MaxWidth(200)
				default:
					ui.Text(c, strings.TrimSpace(part)).FontSize(fsBody).SingleLine().Shrink(0)
				}
			}
		})
	})
	ui.Row(c).Height(sz(42)).Shrink(0).Padding(0, 16, 0, 8).Gap(8).AlignItems(ui.Center).Background(p.band).BorderWidth(0, 0, hairline, 0).BorderColor(p.line).Children(func() {
		fieldInputSized(c.Key("search-"+in), query, m.CompareSearch(), 220, 100).Label(m.CompareSearchLabel(in))
		ui.Spacer(c)
		ui.Text(c, count).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		if actions != nil {
			actions()
		}
	})
	if more {
		ui.Text(c, m.CompareLoadedLimit(len(all))).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 16)
	}
	if v.remote && !cd.loaded {
		return
	}
	if len(all) == 0 {
		ui.Text(c, m.CompareNoneOnly(notIn, in)).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	if len(commits) == 0 {
		ui.Text(c, L(c).CompareNoMatch()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	sel := cd.selected()
	list.Key = func(i int) any { return commits[i].Hash }
	ui.List(c, list, len(commits), func(i int) {
		ci := commits[i]
		if row := commitRow(c, ci, slices.Contains(sel, ci.Hash), i == 0, i == len(commits)-1, lane); row.Clicked() {
			a.clickCommit(v, cd, all, commits, ci, row.ClickModifiers())
			// 行是按钮、不拿焦点；焦点给列表，方向键才能接着用
			list.Focus(c)
		}
	}).Grow(1).Padding(4, 0, 8, 0)
	// ↑↓ 在这一栏里换选中的提交（只选一个），→ 到右边它改的文件
	cur := -1
	if cd.commit != nil {
		cur = slices.IndexFunc(commits, func(x gitx.CommitInfo) bool { return x.Hash == cd.commit.info.Hash })
	}
	j := -1
	switch {
	case list.Shortcut(c, 0, ui.KeyDown):
		j = cur + 1
	case list.Shortcut(c, 0, ui.KeyUp):
		j = max(cur-1, 0)
	case cur >= 0 && list.Shortcut(c, 0, ui.KeyRight):
		cd.commit.focus = true
	}
	if j >= 0 && j < len(commits) && j != cur {
		a.clickCommit(v, cd, all, commits, commits[j], 0)
		list.ScrollIntoView(j)
	}
}

// commitRow 是比较页的一个提交，和 Git 日志一个样子：左边一条线串起圆点，标题、分支和标签、作者、时间。
// first、last 决定线从哪画到哪，lane 是线和圆点的颜色。返回这一行，调用方看它点没点。
func commitRow(c *ui.Context, ci gitx.CommitInfo, selected, first, last bool, lane ui.Color) ui.Element {
	t := c.Theme()
	row := ui.Row(c.Key(ci.Hash)).Height(logRowH()).Padding(0, 16, 0, 16).Gap(8).AlignItems(ui.Center).Role(ui.RoleButton).Label(ci.Subject)
	bg := ui.Transparent
	switch {
	case selected:
		bg = colors(t).tint
	case row.Hovered():
		bg = colors(t).wash
	}
	// 和日志的行一样内缩、圆角、上下留缝；行本身不缩，左边的线才连得上
	bg = row.AnimateColor("bg", bg, hover)
	row.Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(ui.Rect{X: r.X + 6, Y: r.Y + 1, W: r.W - 12, H: r.H - 2}, bg, rRow)
	})
	row.Children(func() {
		ui.Box(c).Width(12).Height(logRowH()).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
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
		ui.Text(c, ci.Subject).FontSize(fsBody).SingleLine().Shrink(1)
		refChips(c, ci.Refs)
		// 作者按名字本身的宽度紧挨着时间，不占固定的一列，标题多显示一些；时间列靠右对齐，作者也就右对齐
		ui.Text(c, ci.Author).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().MaxWidth(authorW).Shrink(0)
		ui.Text(c, commitTime(L(c), ci.Time)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Width(commitTimeW(c)).TextAlign(ui.End).Shrink(0).Tooltip(commitStamp(L(c), ci.Time))
	})
	return row
}

// commitTime 写成"今天 14:26""昨天 17:04""10月3日 09:12"，跨年的只写日期，和 WebStorm 一样看得出先后。
func commitTime(m locales.Messages, at time.Time) string {
	at = at.Local()
	now := time.Now()
	// 按日历日比，不按差几小时：夏令时切换那天两个零点之间不是 24 小时
	sameDay := func(a, b time.Time) bool { return a.Year() == b.Year() && a.YearDay() == b.YearDay() }
	switch {
	case sameDay(at, now):
		return m.AppTodayAt(at)
	case sameDay(at, now.AddDate(0, 0, -1)):
		return m.AppYesterdayAt(at)
	case at.Year() == now.Year():
		return m.AppDateTime(at)
	}
	return at.Format("2006-01-02")
}

// commitTimeW 放得下 commitTime 最长的写法（"12月30日 23:59"、"Yesterday 23:59"）：按这一帧的语言和时制量，
// 12 小时制多出的 PM、「下午」也放得下，至少 96。
func commitTimeW(c *ui.Context) float32 {
	m := L(c)
	at := time.Date(2026, 12, 30, 22, 58, 0, 0, time.Local) // 两位数的钟点，12 小时制是 10:58 PM
	w := float32(96)
	for _, s := range []string{m.AppTodayAt(at), m.AppYesterdayAt(at), m.AppDateTime(at)} {
		if tw, _ := c.MeasureText(0, ui.Span{Text: s, Size: fsSmall}); tw+2 > w {
			w = float32(math.Ceil(float64(tw + 2)))
		}
	}
	return w
}

// commitStamp 是悬停时看的完整时间：2026-10-14 17:17:05 周三。
func commitStamp(m locales.Messages, at time.Time) string {
	at = at.Local()
	return m.AppTimestamp(at.Format("2006-01-02"), at)
}

// commitDetail 是选中提交的说明和它改的文件，点文件看这个提交里的改动。
// live 说明打开的 diff 不挡住这里（日志浮层），↑↓ 才直接打开；back 是在文件列表里按 ← 时回到哪，nil 时不处理。
func (a *app) commitDetail(c *ui.Context, w *workspace, v *filesView, cv *commitView, live bool, back func()) {
	t := c.Theme()
	ci := cv.info
	ui.Column(c).Padding(12, 16, 8, 16).Gap(4).Shrink(0).Children(func() {
		ui.Text(c, ci.Subject).FontSize(fsBody).FontWeight(fwMedium).LineHeight(1.35).Selectable()
		ui.Text(c, ci.Hash[:10]+"  "+ci.Author+"  "+L(c).AppDigitsDateTime(ci.Time.Local().Format("2006-01-02"), ci.Time.Local())).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Selectable()
		if ci.Refs != "" {
			ui.Text(c, ci.Refs).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine()
		}
	})
	if cv.err != "" {
		ui.Text(c, cv.err).FontSize(fsSmall).TextColor(t.Danger).Padding(6, 16)
		if a.remoteHost() && !cv.loading && pressable(textButton(c, L(c).CommonRefresh())).Clicked() {
			for _, d := range v.docs {
				if d.comp != nil && d.comp.commit == cv {
					a.loadTargetCompareCommit(v, d, cv.info)
					break
				}
				if d.blame != nil && d.blame.sel == cv {
					a.loadTargetBlameCommit(v, d, cv.info)
					break
				}
			}
			for _, d := range v.logs {
				if d.log.commit == cv {
					a.loadTargetCommit(v, d, cv.info)
					break
				}
			}
		}
	}
	if cv.files == nil {
		if cv.err == "" {
			ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 16)
		}
	} else {
		selected, open := a.tabDiffs(w, v, cv.diffSpecs())
		nav := fileListNav{focus: &cv.focus, back: back}
		if !live {
			nav.cursor = &cv.cursor
		}
		a.fileChangeList(c, &cv.list, &cv.folded, cv.files, selected, open, nav)
	}
}

// tabDiffs 让文件列表点开的 diff 进文档区的 diff 标签，F7 在这组里跳。
func (a *app) tabDiffs(w *workspace, v *filesView, specs []diffSpec) (selected func(int) bool, open func(int)) {
	selected = func(i int) bool { return v.cur != nil && v.cur.path == diffKey(specs[i].abs(), specs[i]) }
	open = func(i int) { a.openDiff(w, specs[i], specs, true) }
	return selected, open
}

// fileChangeList 是一组改了的文件，照 WebStorm 按目录排成树，点文件交给 open 打开它的 diff（参数是 files 的下标）。
// 方向键见 fileListNav。
func (a *app) fileChangeList(c *ui.Context, list *ui.ListState, folded *map[string]bool, files []gitx.FileChange, selected func(int) bool, open func(int), nav fileListNav) {
	t := c.Theme()
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
		row := ui.Row(c.Key(r.path)).Height(sz(26)).Margin(2, 6, 0, 6).Padding(0, 8, 0, 4+float32(r.depth)*treeIndent).Gap(6).Radius(rRow).AlignItems(ui.Center)
		if r.dir {
			row.Role(ui.RoleButton).Label(r.path)
			row.Children(func() {
				// 整行可点，箭头只是指示，不另接点击
				turn := float32(90)
				if (*folded)[r.path] {
					turn = 0
				}
				ui.Box(c).Size(miniBox(), miniBox()).Center().Shrink(0).Children(func() {
					chev := ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(t.TextMuted)
					chev.Rotate(chev.AnimateWith("turn", turn, 150*time.Millisecond, ui.EaseOut))
				})
				treeDirLabel(c, r.name, len(r.files), r.path)
			})
			if row.Clicked() {
				(*folded)[r.path] = !(*folded)[r.path]
			}
			return
		}
		f := files[r.entry]
		row.Role(ui.RoleButton).Label(f.Path)
		selectedBg(c, row, selected(r.entry) || nav.cursor != nil && *nav.cursor == f.Path && list.FocusWithin(c))
		color := letterColor(c, f.Status)
		row.Children(func() {
			// 文件没有展开箭头，留出同样的宽度，和上一层目录名对齐
			ui.Box(c).Width(miniBox()).Shrink(0)
			changeGlyph(c, r.name, false, f.Status == 'D')
			name := ui.Text(c, r.name).FontSize(fsBody).SingleLine().Shrink(1).Tooltip(gitStatusTooltip(L(c), f.Status, f.Orig, f.Path, true)).TextColor(color)
			if f.Status == 'D' {
				name.Strikethrough().TextColor(t.TextMuted)
			}
			if origin := gitRenameOrigin(L(c), f.Orig, f.Path); origin != "" {
				ui.Text(c, origin).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().MinWidth(sz(48)).Shrink(2).Tooltip(gitRenamePath(f.Orig, f.Path))
			}
		})
		if row.Clicked() {
			if nav.cursor != nil {
				*nav.cursor = f.Path
			}
			open(r.entry)
			// 行是按钮、不拿焦点；焦点给列表，↑↓ 才能接着换
			list.Focus(c)
		}
	}).Grow(1).Padding(0, 0, 8, 0)
	nav.keys(c, list, rows, files, selected, open)
}

// fileListNav 是文件树的方向键，照 WebStorm：↑↓ 在展开着的文件之间换（跳过目录），← 交给 back。
type fileListNav struct {
	focus *bool // 为 true 时列表拿焦点，没选中的文件就选第一个
	// cursor 非 nil 时 ↑↓ 只移动它、回车才打开：打开的 diff 会换掉列表所在的标签（比较页），
	// 直接打开的话列表一下就看不见了。nil 时 ↑↓ 直接打开 diff（日志浮层、推送对话框）。
	cursor *string
	back   func()
}

func (nav fileListNav) keys(c *ui.Context, list *ui.ListState, rows []treeRow, files []gitx.FileChange, selected func(int) bool, open func(int)) {
	var order []int // 文件所在的行
	cur := -1
	for i, r := range rows {
		if r.dir {
			continue
		}
		if nav.cursor != nil && *nav.cursor == files[r.entry].Path || nav.cursor == nil && selected(r.entry) {
			cur = len(order)
		}
		order = append(order, i)
	}
	pick := func(j int) {
		r := rows[order[j]]
		if nav.cursor != nil {
			*nav.cursor = files[r.entry].Path
		} else {
			open(r.entry)
		}
		list.ScrollIntoView(order[j])
		// 打开 diff 不该把焦点从列表拿走，接着按 ↑↓
		list.Focus(c)
	}
	if nav.focus != nil && *nav.focus && list.Focus(c) {
		*nav.focus = false
		if cur < 0 && len(order) > 0 {
			pick(0)
		}
		return
	}
	switch {
	case list.Shortcut(c, 0, ui.KeyDown):
		if j := cur + 1; j < len(order) {
			pick(j)
		}
	case list.Shortcut(c, 0, ui.KeyUp):
		if cur > 0 {
			pick(cur - 1)
		} else if cur < 0 && len(order) > 0 {
			pick(0)
		}
	case nav.cursor != nil && cur >= 0 && list.Shortcut(c, 0, ui.KeyEnter):
		open(rows[order[cur]].entry)
	case nav.back != nil && list.Shortcut(c, 0, ui.KeyLeft):
		nav.back()
	}
}

// --- Push 对话框

type pushState struct {
	read                                         gitRead
	request                                      appRequest
	repository                                   *gitState
	view                                         *filesView
	preview                                      *gitx.PushPreviewRead
	previewIdentity                              gitx.RepositoryIdentity
	remote, followHead, loading, waiting, resume bool
	ws                                           string
	root                                         string // 推送的仓库
	branch                                       string
	plan                                         gitx.PushPlan
	loaded                                       bool
	err                                          string
	commit                                       *commitView
	detail                                       *fileDoc // 右边看着的文件 diff，不在文档区里（文档区的 diff 会被对话框挡住）
	tags                                         bool
	pushing                                      bool
	reload                                       uint64 // 预览重读进行中又来一次时，只采用最后一次
	followFrom                                   string // 右边还显示着这个旧顶端、换 diff 还没回来
	followGen                                    uint64
}

// openPush 推送当前分支。
func (a *app) openPush(m locales.Messages, w *workspace) {
	if a.typedFetch() {
		a.openTargetPush(w, "")
		return
	}
	v := a.filesFor(w)
	a.withRepos(v, func() {
		// 直接读 HEAD：文件栏没打开过时没有状态，w.Branch 在游离时是哈希
		branch, detached := gitx.HeadRef(v.git.repo)
		if v.git.repo == "" || branch == "" || detached {
			a.err = m.CompareDetachedPush()
			return
		}
		a.openPushBranch(w, branch)
	})
}

func (a *app) openPushBranch(w *workspace, branch string) {
	if a.typedFetch() {
		a.openTargetPush(w, branch)
		return
	}
	v := a.filesFor(w)
	root := v.git.repo
	ps := &pushState{ws: w.ID, root: root, branch: branch}
	a.push = ps
	a.pushBox.reset()
	a.loadLocalPush(ps)
}

// reloadPushIfOpen 在推送结束、对话框还开着时重读预览。进行中丢掉的那次结果要补回来。
func (a *app) reloadPushIfOpen(ps *pushState) {
	if ps == nil || a.push != ps || ps.pushing {
		return
	}
	if ps.remote {
		a.loadTargetPush(ps)
		return
	}
	a.loadLocalPush(ps)
}

// reloadOpenPush 在 Fetch、更新或推送之后重算开着的推送预览。正在推送时基线已经锁住，不重算。
func (a *app) reloadOpenPush(projectID string) {
	ps := a.push
	if ps == nil || ps.pushing {
		return
	}
	w := a.workspace(ps.ws)
	if w == nil || w.ProjectID != projectID {
		return
	}
	if ps.remote {
		a.loadTargetPush(ps)
		return
	}
	a.loadLocalPush(ps)
}

func (a *app) loadLocalPush(ps *pushState) {
	ps.reload++
	n := ps.reload
	prev := ""
	if len(ps.plan.Commits) > 0 {
		prev = ps.plan.Commits[0].Hash
	}
	root, branch := ps.root, ps.branch
	go func() {
		plan, err := gitx.PushPreview(context.Background(), root, branch)
		a.update(func() {
			if a.push != ps || ps.reload != n || ps.pushing {
				return
			}
			ps.loaded = true
			if err != nil {
				ps.err = errtext.Text(err)
				if ps.commit != nil {
					ps.commit.read.stop()
					ps.commit = nil
				}
				ps.clearDetail()
				return
			}
			ps.plan, ps.err = plan, ""
			a.followPushTip(ps, prev)
		})
	}()
}

// followPushTip 把正看着的、将要推送的最新提交对齐到新的最新提交。更早的提交保持不动。
func (a *app) followPushTip(ps *pushState, prev string) {
	if prev == "" {
		return
	}
	if len(ps.plan.Commits) == 0 {
		if ps.commit != nil {
			ps.commit.read.stop()
			ps.commit = nil
		}
		ps.clearDetail()
		return
	}
	next := ps.plan.Commits[0]
	// 上一次换成新顶端的 diff 还没回来时，计划里的「上一个顶端」已经是中间那次。右边实际显示的才是要接着跟的。
	// 右边已经不是那个提交（人点开了别的）就把残留清掉，否则之后再点回这次旧提交会被当成顶端跟走。
	if ps.followFrom != "" && ps.detail != nil && ps.detail.diff != nil && ps.detail.diff.newRev == ps.followFrom {
		prev = ps.followFrom
		if next.Hash == prev {
			ps.followFrom = ""
		}
	} else if ps.followFrom != "" {
		ps.followFrom = ""
	}
	if next.Hash != prev && ps.detail != nil && ps.detail.diff != nil && ps.detail.diff.newRev == prev && ps.detail.diff.rel != "" {
		a.retargetPushDetail(ps, next, ps.detail.diff.rel)
		return
	}
	if next.Hash != prev && ps.commit != nil && ps.commit.info.Hash == prev {
		if ps.remote {
			a.loadTargetPushCommit(ps, next)
		} else {
			a.selectCommit(ps.root, &ps.commit, next)
		}
		return
	}
	if ps.commit != nil && !containsCommit(ps.plan.Commits, ps.commit.info.Hash) {
		ps.commit.read.stop()
		ps.commit = nil
		ps.clearDetail()
	}
}

// retargetPushDetail 把推送对话框右边的 diff 换成新提交里的同一个文件。
func (a *app) retargetPushDetail(ps *pushState, info gitx.CommitInfo, rel string) {
	v := ps.view
	if v == nil {
		v = a.files[ps.ws]
	}
	if v == nil {
		return
	}
	if ps.remote {
		a.retargetTargetPushDetail(ps, v, info, rel)
		return
	}
	prev := ""
	if ps.detail != nil && ps.detail.diff != nil {
		prev = ps.detail.diff.newRev
	}
	ps.followGen++
	gen := ps.followGen
	ps.followFrom = prev
	n := ps.reload
	go func() {
		files, err := gitx.CommitFiles(context.Background(), ps.root, info.Hash)
		a.update(func() {
			if a.push != ps || ps.followGen != gen {
				return
			}
			if ps.pushing || ps.followFrom != prev || ps.detail == nil || ps.detail.diff == nil || ps.detail.diff.rel != rel || ps.detail.diff.newRev != prev || err != nil {
				ps.followFrom = ""
				return
			}
			if ps.reload != n {
				return
			}
			ps.followFrom = ""
			cv := &commitView{info: info, root: ps.root, files: files}
			ps.commit = cv
			for i, f := range files {
				if f.Path == rel {
					a.pushShow(v, ps, i, true)
					return
				}
			}
			ps.clearDetail()
		})
	}()
}

// pushListPane 是推送对话框里提交列表的宽度。
var pushListPane = pane{380, 240, 560}

func (a *app) pushDialog(c *ui.Context) {
	t := c.Theme()
	ps := a.push
	if ps != nil && ps.remote {
		a.resumeTargetPush(ps)
	}
	open := ps != nil
	box := &a.pushBox
	box.min = [2]ui.Rect{{W: 560, H: 300}, {W: 860, H: 420}}
	box.mode = 0
	if ps != nil && ps.detail != nil {
		box.mode = 1
	}
	panel := ui.Modal(c, &open, func() {
		defer box.grips(c)
		if ps == nil {
			return
		}
		w := a.workspace(ps.ws)
		if w == nil {
			a.closePush(ps)
			return
		}
		v := a.filesFor(w)
		// 看 diff 时对话框加宽加高，最右边是 diff
		width, height := float32(760), float32(320)
		if ps.detail != nil {
			width, height = 1080, 560
		}
		dlg := ui.Column(c).Gap(12)
		if box.sized() {
			dlg.Grow(1).Shrink(1)
		} else {
			dlg.Width(width)
		}
		dlg.Children(func() {
			box.titleBar(c, L(c).ComparePushTitle(), func() {
				if pressable(iconButtonSized(c, iconClose, L(c).CommonClose(), compactBox(), smallIcon())).Clicked() {
					a.closePush(ps)
				}
			})
			if ps.remote {
				ui.Text(c, L(c).PushPreviewNotice()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.4)
				if ps.err != "" {
					ui.Text(c, ps.err).FontSize(fsSmall).TextColor(t.Danger).LineHeight(1.4)
				}
			}
			body := ui.Row(c).Gap(0).AlignItems(ui.Stretch)
			if box.sized() {
				body.Grow(1).Shrink(1)
			} else {
				body.Height(height)
			}
			body.Children(func() {
				ui.Column(c).Width(paneWidth(a.state.PushListWidth, pushListPane)).Shrink(0).Children(func() {
					plan := ps.plan
					switch {
					case !ps.loaded:
						if ps.err == "" {
							ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4)
						}
						return
					case ps.err != "" && !ps.remote:
						ui.Text(c, ps.err).FontSize(fsSmall).TextColor(t.Danger).Padding(4).LineHeight(1.4)
						return
					}
					ui.Row(c).Gap(6).Padding(2, 4, 8, 4).Children(func() {
						ui.Text(c, plan.Branch+" → "+plan.Remote+" : "+plan.RemoteBranch).FontSize(fsCode).Font(monoFont).FontWeight(fwMedium).SingleLine().Shrink(1)
						if plan.NewBranch {
							label := L(c).CompareNewBranch()
							if ps.remote {
								label = L(c).PushPreviewMissing()
							}
							ui.Text(c, label).FontSize(fsSmall).TextColor(colors(t).sage).Shrink(0)
						}
					})
					if ps.preview != nil && ps.preview.More {
						ui.Text(c, L(c).LogMoreCommitCount(len(plan.Commits))).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, 4, 4, 4)
						ui.Text(c, L(c).PushPreviewLimit(len(plan.Commits))).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, 4, 8, 4)
					}
					if plan.Gone && !ps.remote {
						ui.Text(c, L(c).ComparePushGone(plan.Remote, plan.RemoteBranch)).FontSize(fsSmall).TextColor(t.TextMuted).Padding(0, 4, 8, 4).LineHeight(1.4)
					}
					if len(plan.Commits) == 0 {
						label := L(c).ComparePushEmpty()
						if ps.remote {
							label = L(c).PushPreviewEmpty()
						}
						ui.Text(c, label).FontSize(fsSmall).TextColor(t.TextMuted).Padding(4)
						return
					}
					ui.Scroll(c).Grow(1).Children(func() {
						// 行之间不留缝，左边的竖线才连得起来
						ui.Column(c).Children(func() {
							for i, ci := range plan.Commits {
								if commitRow(c, ci, ps.commit != nil && ps.commit.info.Hash == ci.Hash, i == 0, i == len(plan.Commits)-1, laneColor(t, 0)).Clicked() {
									a.selectCommit(ps.root, &ps.commit, ci)
									ps.clearDetail()
								}
							}
						})
					})
				})
				a.paneDivider(c, "push-list-grip", &a.state.PushListWidth, pushListPane, 1)
				files := ui.Column(c)
				if ps.detail != nil {
					files.Width(240).Shrink(0)
				} else {
					files.Grow(1)
				}
				files.Children(func() {
					if ps.commit == nil {
						ui.Text(c, L(c).CompareSelectCommit()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
						return
					}
					cv := ps.commit
					ui.Column(c).Padding(4, 16, 6, 16).Gap(4).Shrink(0).Children(func() {
						ui.Text(c, cv.info.Subject).FontSize(fsBody).FontWeight(fwMedium)
						ui.Text(c, cv.info.Hash[:10]+"  "+cv.info.Author).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted)
					})
					if cv.err != "" {
						ui.Text(c, cv.err).FontSize(fsSmall).TextColor(t.Danger).Padding(6, 16)
						if ps.remote && pressable(textButton(c, L(c).CommonRefresh())).Disabled(cv.loading).Clicked() {
							a.loadTargetPushCommit(ps, cv.info)
						}
					}
					if cv.files == nil {
						if cv.err == "" {
							ui.Text(c, L(c).CommonLoading()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(6, 16)
						}
					} else {
						selected := func(i int) bool { return ps.detail != nil && ps.detail.diff.rel == cv.files[i].Path }
						a.fileChangeList(c, &cv.list, &cv.folded, cv.files, selected, func(i int) { a.pushShow(v, ps, i, true) }, fileListNav{})
					}
				})
				if ps.detail != nil {
					a.pushDetail(c, v, ps)
				}
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Checkbox(c, &ps.tags, L(c).ComparePushTags()).Disabled(ps.pushing)
				if ps.remote && pressable(textButton(c, L(c).CommonRefresh())).Disabled(ps.loading || ps.waiting).Clicked() {
					a.loadTargetPush(ps)
				}
				if ps.remote {
					if s := a.pushFor(ps.repository); s != nil {
						a.pushOperationBar(c, s)
					}
				}
				ui.Spacer(c)
				if pressable(textButton(c, L(c).CommonCancel())).Clicked() {
					a.closePush(ps)
				}
				ready := ps.loaded && ps.err == "" && (len(ps.plan.Commits) > 0 || ps.plan.NewBranch) && !ps.pushing
				if ps.remote {
					ready = a.pushWritable(ps)
				}
				force := pressable(textButton(c, L(c).CompareForcePushEllipsis())).Tooltip(L(c).CompareForcePushTip())
				push := pressable(accentButton(c, L(c).ComparePush()))
				if !ready {
					force.Disabled(true)
					push.Disabled(true)
				}
				m := L(c)
				if ready && push.Clicked() {
					a.doPush(m, v, ps, false)
				}
				if ready && force.Clicked() {
					if ps.remote {
						a.startPush(m, ps, true)
					} else {
						a.confirm(m.CompareForcePushConfirm(ps.plan.Branch), m.CompareForcePushBody(ps.plan.Remote+"/"+ps.plan.RemoteBranch), m.CompareForcePush(), func() {
							a.update(func() { a.doPush(m, v, ps, true) })
						})
					}
				}
			})
		})
	})
	box.apply(panel)
	if !open && ps != nil && (ps.remote || !ps.pushing) {
		a.closePush(ps)
	}
}

// pushShow 在对话框右边打开选中提交里第 i 个文件的 diff；排版和忽略空白沿用上一个，地方窄，默认上下合并。
func (a *app) pushShow(v *filesView, ps *pushState, i int, first bool) {
	cv := ps.commit
	df := &diffDoc{diffSpec: cv.diffSpecs()[i], first: first}
	df.state = editor.DiffState{Layout: editor.DiffUnified, IgnoreWhitespace: true}
	if old := ps.detail; old != nil {
		df.state.Layout, df.state.IgnoreWhitespace = old.diff.state.Layout, old.diff.state.IgnoreWhitespace
	}
	ps.clearDetail()
	ps.detail = &fileDoc{path: diffKey(df.abs(), df.diffSpec), diff: df, kind: docLoading, started: true}
	a.loadDiff(v, ps.detail)
}

// pushStep 是 diff 里的上一处 / 下一处：到头了换到文件树里的上一个 / 下一个文件。
func (a *app) pushStep(m locales.Messages, v *filesView, ps *pushState, dir int) {
	d, cv := ps.detail, ps.commit
	if d == nil || cv == nil || cv.loading || d.diff.loading {
		return
	}
	df := d.diff
	if next := df.hunk + dir; d.kind == docDiff && next >= 0 && next < len(df.state.Hunks()) {
		df.hunk = next
		df.state.GoToHunk(next)
		df.state.Current = next + 1
		return
	}
	paths := make([]string, len(cv.files))
	for i, f := range cv.files {
		paths[i] = f.Path
	}
	var order []int
	for _, row := range changeTree(paths, nil) {
		if !row.dir {
			order = append(order, row.entry)
		}
	}
	if j := slices.IndexFunc(order, func(i int) bool { return paths[i] == df.rel }) + dir; j >= 0 && j < len(order) {
		a.pushShow(v, ps, order[j], dir > 0)
		return
	}
	msg := m.GitLastChange()
	if dir < 0 {
		msg = m.GitFirstChange()
	}
	df.hint, df.hintAt = msg, time.Now()
}

// pushDetail 是对话框右边的 diff，只读。
func (a *app) pushDetail(c *ui.Context, v *filesView, ps *pushState) {
	t := c.Theme()
	d := ps.detail
	df := d.diff
	switch {
	case a.pressed(c, "nextChange"):
		a.pushStep(L(c), v, ps, 1)
	case a.pressed(c, "prevChange"):
		a.pushStep(L(c), v, ps, -1)
	}
	ui.Column(c).Grow(1).Shrink(1).Margin(0, 0, 0, 12).Radius(rControl).Background(colors(t).fill).Clip().Children(func() {
		ui.Row(c).Height(sz(36)).Shrink(0).Padding(0, 6, 0, 6).Gap(4).AlignItems(ui.Center).Children(func() {
			if pressable(iconButtonSized(c, iconArrowUp, a.withKey(L(c).GitPreviousChange(), "prevChange"), buttonBox(), iconSize)).Clicked() {
				a.pushStep(L(c), v, ps, -1)
			}
			if pressable(iconButtonSized(c, iconArrowDown, a.withKey(L(c).GitNextChange(), "nextChange"), buttonBox(), iconSize)).Clicked() {
				a.pushStep(L(c), v, ps, 1)
			}
			ui.Text(c, gitDiffPath(df.orig, df.rel)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).SingleLine().Shrink(1).Padding(0, 4).Tooltip(gitRenamePath(df.orig, df.rel))
			if d.kind == docDiff {
				if n := len(df.state.Hunks()); n > 0 {
					added, deleted := df.state.Stats()
					ui.Text(c, L(c).GitChangeStats(min(df.hunk+1, n), n, added, deleted)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).SingleLine().Shrink(1)
				} else if df.orig != "" && df.orig != df.rel {
					ui.Text(c, gitDiffZeroMsg(L(c), df)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
				}
			}
			ui.Spacer(c)
			if d.kind == docDiff {
				split := df.state.Layout == editor.DiffSplit
				if toggleChip(c, L(c).GitSideBySide(), L(c).GitSideBySideTip(), split) {
					if split {
						df.state.Layout = editor.DiffUnified
					} else {
						df.state.Layout = editor.DiffSplit
					}
				}
				if toggleChip(c, L(c).GitIgnoreWhitespace(), L(c).GitIgnoreWhitespaceTip(), df.state.IgnoreWhitespace) {
					a.toggleIgnoreSpace(d)
				}
			}
			if ps.remote && d.err != "" && pressable(iconButtonSized(c, iconRefresh, L(c).CommonRefresh(), compactBox(), smallIcon())).Disabled(df.loading).Clicked() {
				a.loadDiff(v, d)
			}
			if pressable(iconButtonSized(c, iconClose, L(c).CompareHideDiff(), compactBox(), smallIcon())).Clicked() {
				ps.clearDetail()
			}
		})
		if ps.detail == nil {
			return
		}
		hline(c)
		ui.Column(c).Grow(1).Shrink(1).Children(func() {
			muted := func(msg string) {
				ui.Text(c, msg).FontSize(fsBody).TextColor(t.TextMuted).Padding(16)
			}
			switch d.kind {
			case docLoading:
				muted(diffLoadingText(L(c), df))
			case docError:
				ui.Text(c, d.err).FontSize(fsBody).TextColor(t.Danger).Padding(16)
			case docImageDiff:
				imageDiffView(c, d)
			case docBinary:
				muted(L(c).CompareBinary())
			case docTooLarge:
				a.tooLargeDiffView(c, v, d)
			default:
				a.diffView(c, v, d)
			}
			diffHint(c, df)
		})
	})
}

func (a *app) doPush(m locales.Messages, v *filesView, ps *pushState, force bool) {
	if ps.remote || v.remote || a.typedFetch() {
		a.startPush(m, ps, force)
		return
	}
	ps.pushing = true
	plan, tags := ps.plan, ps.tags
	a.gitDoKind(m, v.repoOrGone(ps.root), gitPush, "Push", func(ctx context.Context, root string) error {
		err := gitx.Push(ctx, root, plan, force, tags)
		a.update(func() {
			ps.pushing = false
			if err == nil && a.push == ps {
				a.closePush(ps)
			}
		})
		if gitx.Rejected(err) {
			if force {
				return errors.New(m.CompareForceRejected())
			}
			return errors.New(m.ComparePushRejected())
		}
		return err
	}, func() {
		msg := m.ComparePushed(len(plan.Commits), plan.Remote+"/"+plan.RemoteBranch)
		if len(plan.Commits) == 0 {
			msg = m.ComparePushedTo(plan.Remote + "/" + plan.RemoteBranch)
		}
		a.notify(msg, "", nil)
	})
}
