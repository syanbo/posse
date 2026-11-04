package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"posse/internal/errtext"
	"posse/internal/files"
	"posse/internal/gitx"
	"posse/internal/media"
	"posse/locales"
)

// 文件标签里的 Git：文件栏"改动"、diff 标签页、编辑器行号旁的改动标记。全部在窗口进程里调用系统 git。
// 交互照 WebStorm（见 docs/features/git.md）：改动列表自动刷新，diff 默认左右对照、忽略空白，
// F7 / ⇧F7 跨文件跳到下一处 / 上一处改动。

const (
	gitPollEvery = 1500 * time.Millisecond
	sideFiles    = 0
	sideSearch   = 1
	sideChanges  = 2
)

// gitState 是工作区里一个仓库的 Git 状态（找仓库见 repos.go）。
type gitState struct {
	v         *filesView
	repo      string   // 仓库根目录，工作区里的写法（经过软链时是软链的路径）；工作区里没有仓库时为空
	aliases   []string // 同一个仓库经别的软链进来的写法
	status    *gitx.Status
	head      map[string]gitx.LineStats // 相对 HEAD 的增删行数，改动列表显示
	headRef   string                    // 检出的分支（游离时是哈希前 8 位），gitBranch 一秒最多读一次
	headAt    time.Time
	picks     map[string]bool // 勾选提交的文件里用户改过的；没改过的用默认（见 picked）
	stashes   []gitx.Stash
	real      string // 解析过软链的根目录：终端里的 git 命令按它归到工作区（runWorkspace）
	rows      []changeRow
	folded    map[changeGroup]*bool // 改动列表里收起的组；没有记录是展开
	closed    bool                  // 多个仓库时改动页里收起了这个仓库
	poll      panelPoll
	pollAgain bool    // 读的期间要求重读（refreshRepo），读完再读一次
	busy      string  // 正在执行的操作，期间按钮不可点
	busyKind  gitKind // 正在执行的操作的代号，逻辑按它判断，busy 只用来显示
	queue     []gitOp
	cancel    context.CancelFunc // 正在执行的是网络操作时可以取消
	err       string
	branches  branchInfo
	// dirFolded 是按目录分组时各组里收起的目录
	dirFolded map[changeGroup]map[string]bool
	// autostash 是这个工作区里的操作（更新、改写提交）留在储藏里的本地改动（储藏的提交），横幅引去处理；
	// 储藏列表是所有 worktree 共用的，只记自己操作留下的。关掉横幅或储藏没了就清空
	autostash          string
	autostashConflicts bool            // 取回了、有冲突；为假是根本没取回
	autostashSeen      bool            // 储藏列表里已经读到过它：之后不在了就是在别处删掉、弹出了
	marks              map[string]byte // 文件树的状态标记，按绝对路径；跟着 marksOf 这份状态算
	marksOf            *gitx.Status
}

type changeGroup uint8

const (
	groupConflict changeGroup = iota
	groupChanged
	groupUntracked
	groupStash
)

var groupTitles = [...]func(locales.Messages) string{
	locales.Messages.GitConflicts, locales.Messages.GitChanges, locales.Messages.GitUnversioned, locales.Messages.GitStash,
}

// changeRow 是改动列表的一行：组标题、一个文件，或一条储藏；按目录分组时还有目录行。
type changeRow struct {
	g      *gitState // 文件、目录和组标题所在的仓库
	repo   bool      // 多个仓库时的仓库标题行
	group  changeGroup
	header bool
	count  int
	entry  gitx.Entry
	stash  gitx.Stash
	tree   bool   // 按目录分组时的行（目录和文件）：按 depth 缩进，文件不再写所在目录
	dir    bool   // 目录行
	path   string // 目录行相对仓库根目录的路径
	name   string // 目录行显示的名字：只有一个子目录的中间层并成一行，如 internal/app
	depth  int
	files  []gitx.Entry // 目录行下面所有的文件
}

// changeRows 按组排好改动：冲突、改动、未跟踪、储藏。
// 和 WebStorm 一样不分暂存区：已跟踪的改动不管暂存了没有都在"改动"里，提交时按勾选。
func changeRows(g *gitState, st *gitx.Status, stashes []gitx.Stash) []changeRow {
	var groups [4][]changeRow
	for _, e := range st.Entries {
		switch {
		case e.Conflict:
			groups[groupConflict] = append(groups[groupConflict], changeRow{g: g, group: groupConflict, entry: e})
		case e.Untracked:
			groups[groupUntracked] = append(groups[groupUntracked], changeRow{g: g, group: groupUntracked, entry: e})
		default:
			groups[groupChanged] = append(groups[groupChanged], changeRow{g: g, group: groupChanged, entry: e})
		}
	}
	for _, s := range stashes {
		groups[groupStash] = append(groups[groupStash], changeRow{g: g, group: groupStash, stash: s})
	}
	var rows []changeRow
	for gr, rs := range groups {
		if len(rs) == 0 {
			continue
		}
		slices.SortStableFunc(rs, func(a, b changeRow) int { return strings.Compare(a.entry.Path, b.entry.Path) })
		rows = append(rows, changeRow{g: g, group: changeGroup(gr), header: true, count: len(rs)})
		rows = append(rows, rs...)
	}
	return rows
}

// byDirRows 把每组的文件照回滚对话框的文件树按目录排（changeTree），收起的目录不列下面的行。
// rows 里已经去掉了收起的组，组标题（含仓库标题）原样留着。
func byDirRows(rows []changeRow) []changeRow {
	var out []changeRow
	for i := 0; i < len(rows); {
		if rows[i].header {
			out = append(out, rows[i])
			i++
			continue
		}
		j := i
		for j < len(rows) && !rows[j].header {
			j++
		}
		grp := rows[i:j]
		paths := make([]string, len(grp))
		for k, r := range grp {
			paths[k] = r.entry.Path
		}
		gr, g := grp[0].group, grp[0].g
		for _, t := range changeTree(paths, g.dirFolds(gr)) {
			r := changeRow{g: g, group: gr, tree: true, dir: t.dir, path: t.path, name: t.name, depth: t.depth}
			if t.dir {
				for _, k := range t.files {
					r.files = append(r.files, grp[k].entry)
				}
			} else {
				r.entry = grp[t.entry].entry
			}
			out = append(out, r)
		}
		i = j
	}
	return out
}

// dirFolds 是一组里收起的目录，按 changeRow.path；不记进 ui.json，和组的展开收起一样。
func (g *gitState) dirFolds(gr changeGroup) map[string]bool {
	if g.dirFolded == nil {
		g.dirFolded = map[changeGroup]map[string]bool{}
	}
	m := g.dirFolded[gr]
	if m == nil {
		m = map[string]bool{}
		g.dirFolded[gr] = m
	}
	return m
}

// groupOpen 返回组展开与否的开关，没有就新建一个展开的。
func (g *gitState) groupOpen(gr changeGroup) *bool {
	if g.folded == nil {
		g.folded = map[changeGroup]*bool{}
	}
	open := g.folded[gr]
	if open == nil {
		open = new(bool)
		*open = true
		g.folded[gr] = open
	}
	return open
}

// changeCount 是文件栏"改动"上显示的数：有改动的文件数，不含储藏。
func (g *gitState) changeCount() int {
	if g.status == nil {
		return 0
	}
	return len(g.status.Entries)
}

// repoRoot 把 git 给的仓库根目录换成工作区路径的写法，见 gitx.ShownRoot。
func repoRoot(ws, top string) string { return gitx.ShownRoot(ws, top) }

// relPath 把绝对路径换成相对仓库根目录的路径；不在仓库里时返回空。
func (g *gitState) relPath(abs string) string {
	if g.repo == "" {
		return ""
	}
	root, ok := gitx.Repo{Root: g.repo, Aliases: g.aliases}.Contains(abs)
	if !ok && g.v != nil {
		if r, isReal := g.v.realPath(abs); isReal {
			abs = r
			root, ok = gitx.Repo{Root: g.repo, Aliases: g.aliases}.Contains(abs)
		}
	}
	if !ok {
		return ""
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (g *gitState) absPath(rel string) string {
	p := filepath.Join(g.repo, filepath.FromSlash(rel))
	if g.v != nil {
		p = g.v.shownPath(p)
	}
	return p
}

// loadHead 读文件在 HEAD 里的内容作为编辑器行号旁改动标记的基准；不在仓库里或是新文件时不标。
func (a *app) loadHead(v *filesView, d *fileDoc) {
	g, rel := v.relIn(d.path)
	if g == nil || g.status == nil || g.status.Commit == "" {
		return
	}
	root, commit := g.repo, g.status.Commit
	d.headFor = commit
	go func() {
		b, err := gitx.Show(context.Background(), root, commit, rel)
		a.update(func() {
			if d.kind != docText || d.ed.Buffer == nil {
				return
			}
			switch {
			case errors.Is(err, gitx.ErrNotFound):
				d.headBuf, d.ed.DiffBase = nil, nil
			case err == nil && !files.IsBinary(b):
				if d.headBuf == nil {
					d.headBuf = editor.NewBuffer(string(b))
				} else {
					d.headBuf.Reset(string(b))
				}
				d.ed.DiffBase = d.headBuf
			}
		})
	}()
}

// --- diff 标签页

// diffSpec 说明一个 diff 比较哪两个版本的哪个文件。版本的写法：newRev 为空是工作区，
// 其余是提交或分支；oldRev 为空表示旧的一边没有这个文件（新文件、根提交）。路径相对仓库根目录。
type diffSpec struct {
	root           string // 仓库根目录；为空时 openDiff 填上当前仓库
	rel, orig      string // 重命名时 orig 是旧路径
	oldRev, newRev string
	label          string // 标签页上的补充说明，为空时按版本推断
}

func (s diffSpec) worktree() bool { return s.newRev == "" }

// abs 是文件的绝对路径。
func (s diffSpec) abs() string { return filepath.Join(s.root, filepath.FromSlash(s.rel)) }

// fixedRevs 说明两边都是提交哈希（可带 ^、~N），引用变了内容也不变；
// HEAD、分支名这类会随 fetch、拉取移动。工作区一边另看磁盘戳。
func (s diffSpec) fixedRevs() bool {
	return isCommitRev(s.oldRev) && (s.worktree() || s.newRev == ":" || isCommitRev(s.newRev))
}

func isCommitRev(rev string) bool {
	if rev == "" {
		return true
	}
	if i := strings.IndexAny(rev, "^~"); i >= 0 {
		rev = rev[:i]
	}
	if len(rev) != 40 && len(rev) != 64 {
		return false
	}
	return strings.Trim(rev, "0123456789abcdef") == ""
}

// changeSpec 是改动列表里一个文件的 diff：HEAD 对比工作区。
func changeSpec(root string, e gitx.Entry) diffSpec {
	return diffSpec{root: root, rel: e.Path, orig: e.Orig, oldRev: "HEAD"}
}

// diffDoc 是 diff 标签页。
type diffDoc struct {
	diffSpec
	siblings  []diffSpec // 同一组的文件（比较、提交里的），F7 在它们之间跳；为空时在改动列表里跳
	state     editor.DiffState
	hunk      int  // F7 走到的改动
	first     bool // 读进来后跳到第一处改动；⇧F7 从后一个文件退回来时为假，跳到最后一处
	jumped    bool // 已经跳过
	binary    bool
	images    [2]diffImage // 图片文件的两边：改之前、改之后
	checkers  [2]checkerCache
	stamp     files.Stamp // 工作区文件的磁盘戳，变了才重读
	hint      string      // F7 到头时 diff 栏下面的提示（diffHint）
	hintAt    time.Time
	force     bool   // 超过 diffMaxSize 也比较：点过"仍要比较"
	comparing bool   // 大文件正在后台比较（读进来之后，或切换了忽略空白）
	sizes     [2]int // 超过 diffMaxSize 时两边的字节数和行数，给提示用
	lines     [2]int
	tracked   bool // 已经记进 trackBigDiff
}

// diffImage 是图片 diff 的一边：缩好的图、原始尺寸、文件大小；size 为 -1 表示这一边没有文件（新增或删除）。
type diffImage struct {
	img  *ui.Bitmap
	anim *animPlayer // 动图：img 是它的位图
	w, h int
	size int
	err  string // 解不开图片时的报错（已经过 errtext）
}

func loadDiffImage(b []byte, exists bool) diffImage {
	if !exists {
		return diffImage{size: -1}
	}
	if anim, a, err := newAnimPlayer(b, imageMaxPx); anim != nil && err == nil {
		return diffImage{img: anim.bm, anim: anim, w: a.Size.X, h: a.Size.Y, size: len(b)}
	}
	img, dim, err := media.ScaledImageBytes(b, imageMaxPx)
	if err != nil {
		return diffImage{size: len(b), err: errtext.Text(err)}
	}
	return diffImage{img: ui.NewBitmap(img), w: dim.X, h: dim.Y, size: len(b)}
}

// label 是标签页上的补充说明。
func (df *diffDoc) tabLabel(m locales.Messages) string {
	switch {
	case df.label != "":
		return df.label
	case df.plainChanges():
		return m.GitChanges()
	}
	return shortRev(m, df.oldRev) + " → " + shortRev(m, df.newRev)
}

// plainChanges 报告是工作区对 HEAD 的改动，标签就叫"改动"、不另标版本。
func (df *diffDoc) plainChanges() bool {
	return df.label == "" && df.worktree() && df.oldRev == "HEAD"
}

// shortRev 是版本在界面上的写法：提交取前 7 位，空的是工作区。
func shortRev(m locales.Messages, rev string) string {
	switch {
	case rev == "":
		return m.GitWorkspace()
	case len(rev) == 40:
		return rev[:7]
	}
	return rev
}

func diffKey(abs string, s diffSpec) string {
	return "diff:" + s.oldRev + ".." + s.newRev + ":" + abs
}

// openDiff 在文档区打开一个文件的 diff。每个工作区只有一个 diff 标签：已经开着就在里面换成这个文件，
// 排版和忽略空白沿用；换文件用 diff 上方的文件菜单或 F7。
func (a *app) openDiff(w *workspace, s diffSpec, siblings []diffSpec, first bool) {
	v := a.showFiles(w)
	if s.root == "" {
		s.root = v.git.repo
	}
	for i := range siblings {
		if siblings[i].root == "" {
			siblings[i].root = s.root
		}
	}
	key := diffKey(s.abs(), s)
	df := &diffDoc{diffSpec: s, siblings: siblings, first: first}
	df.state = editor.DiffState{Layout: editor.DiffSplit, IgnoreWhitespace: true}
	for _, d := range v.docs {
		if d.diff == nil {
			continue
		}
		v.cur = d
		if d.path == key {
			return
		}
		df.state.Layout, df.state.IgnoreWhitespace = d.diff.state.Layout, d.diff.state.IgnoreWhitespace
		d.path, d.diff, d.kind, d.err = key, df, docLoading, ""
		a.loadDiff(v, d)
		return
	}
	d := &fileDoc{path: key, diff: df}
	v.insertDoc(d)
	d.started = true
	a.loadDiff(v, d)
}

// diffGroup 是 diff 标签里可以切换的文件：比较、提交里同一组的文件，没有就是改动列表。
func diffGroup(v *filesView, df *diffDoc) []diffSpec {
	if df.siblings != nil {
		return df.siblings
	}
	g := v.repoAt(df.root)
	if g == nil {
		return nil
	}
	var list []diffSpec
	for _, r := range g.rows {
		if !r.header && r.group != groupStash && !r.entry.Conflict {
			list = append(list, changeSpec(g.repo, r.entry))
		}
	}
	return list
}

// showRev 读一边的内容：版本里没有这个文件时是空的。
func showRev(ctx context.Context, root, rev, rel string) ([]byte, error) {
	b, err := gitx.Show(ctx, root, rev, rel)
	if errors.Is(err, gitx.ErrNotFound) {
		return nil, nil
	}
	return b, err
}

// diffMaxSize 以上的文件默认不比较，点"仍要比较"才比。比较在后台做，这么大也只是要等几秒。
const diffMaxSize = 20 << 20

// diffAheadSize 以上的两边合起来在后台比较时显示"正在比较…"；切换忽略空白也在后台重比。
// 比较本身在界面线程上做的话，几 MB 的 lock 文件就要卡零点几秒到一两秒。
const diffAheadSize = 1 << 20

// diffLoadingText 是 diff 还没出来时的提示。
func diffLoadingText(m locales.Messages, df *diffDoc) string {
	if df.comparing {
		return m.GitComparing()
	}
	return m.CommonLoading()
}

// tooLargeDiffView 是超过 diffMaxSize 的文件：两边多大、多少行，可以仍要比较。
func (a *app) tooLargeDiffView(c *ui.Context, v *filesView, d *fileDoc) {
	t := c.Theme()
	m := L(c)
	df := d.diff
	side := func(i int) string {
		return m.GitSizeLines(byteSize(df.sizes[i]), df.lines[i])
	}
	ui.Column(c).Grow(1).AlignSelf(ui.Stretch).Center().Gap(12).Padding(16).Children(func() {
		ui.Text(c, m.GitTooLarge(diffMaxSize>>20)).FontSize(fsBody).TextColor(t.TextMuted)
		ui.Text(c, m.GitTooLargeSides(side(0), side(1))).FontSize(fsSmall).TextColor(t.TextMuted).FontFeatures("tnum")
		if pressable(textButton(c, m.GitCompareAnyway())).Clicked() {
			df.force = true
			d.kind = docLoading
			a.loadDiff(v, d)
		}
	})
}

// lineCount 是文字的行数，最后一行没有换行符也算。
func lineCount(b []byte) int {
	n := bytes.Count(b, []byte{'\n'})
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// toggleIgnoreSpace 切换忽略空白。大文件在后台重新比较，算完再换上，期间界面照旧显示原来的结果。
func (a *app) toggleIgnoreSpace(d *fileDoc) {
	df := d.diff
	on := !df.state.IgnoreWhitespace
	if df.state.Old == nil || df.state.Old.Len()+df.state.New.Len() <= diffAheadSize {
		df.state.IgnoreWhitespace = on
		return
	}
	if df.comparing {
		return
	}
	df.comparing = true
	old, nw := df.state.Old.String(), df.state.New.String()
	go func() {
		cmp := editor.CompareTexts(old, nw, on)
		a.update(func() {
			df.comparing = false
			if d.diff != df {
				return
			}
			df.state.IgnoreWhitespace = on
			df.state.UseComparison(cmp)
			releaseMemorySoon()
		})
	}()
}

// clampHunk 在改动数变少（重读、切换忽略空白、回滚了一处）之后把当前位置收回到范围内。
func clampHunk(df *diffDoc) {
	n := len(df.state.Hunks())
	if df.hunk < n {
		return
	}
	df.hunk = max(n-1, 0)
	if df.jumped && n > 0 {
		df.state.Current = df.hunk + 1
	}
}

// loadDiff 在后台读两边的内容。
func (a *app) loadDiff(v *filesView, d *fileDoc) {
	df := d.diff
	if df.root == "" {
		df.root = v.git.repo
	}
	root, spec := df.root, df.diffSpec
	ignore, force := df.state.IgnoreWhitespace, df.force
	go func() {
		ctx := context.Background()
		oldPath := spec.rel
		if spec.orig != "" {
			oldPath = spec.orig
		}
		var old, nw []byte
		var err error
		oldExists, newExists := spec.oldRev != "", true
		if spec.oldRev != "" {
			old, err = showRev(ctx, root, spec.oldRev, oldPath)
		}
		var stamp files.Stamp
		if err == nil {
			switch {
			case spec.worktree():
				abs := filepath.Join(root, filepath.FromSlash(spec.rel))
				stamp = files.StatFile(abs)
				nw, err = os.ReadFile(abs)
				if os.IsNotExist(err) {
					nw, err, newExists = nil, nil, false
				}
			default:
				nw, err = showRev(ctx, root, spec.newRev, spec.rel)
				newExists = len(nw) > 0
			}
		}
		// 图片：两边都解码成位图，左右对照看
		var images [2]diffImage
		isImage := err == nil && isImagePath(spec.rel)
		if isImage {
			images = [2]diffImage{loadDiffImage(old, oldExists && len(old) > 0), loadDiffImage(nw, newExists)}
		}
		tooLarge := err == nil && !isImage && !force && max(len(old), len(nw)) > diffMaxSize
		var lines [2]int
		if tooLarge {
			lines = [2]int{lineCount(old), lineCount(nw)}
		}
		// 比较在这里做完，界面线程只装结果
		var cmp *editor.DiffComparison
		if err == nil && !isImage && !tooLarge && !files.IsBinary(old) && !files.IsBinary(nw) {
			if len(old)+len(nw) > diffAheadSize {
				a.update(func() {
					if d.diff == df {
						df.comparing = true
					}
				})
			}
			cmp = editor.CompareTexts(string(old), string(nw), ignore)
		}
		a.update(func() {
			// 读的时候标签已经换成别的文件
			if d.diff != df {
				return
			}
			df.comparing = false
			if err != nil {
				d.kind, d.err = docError, errtext.Text(err)
				return
			}
			df.stamp = stamp
			if tooLarge {
				df.sizes, df.lines = [2]int{len(old), len(nw)}, lines
				d.kind = docTooLarge
				return
			}
			if isImage {
				df.binary, df.images, d.kind = true, images, docImageDiff
				return
			}
			if files.IsBinary(old) || files.IsBinary(nw) {
				df.binary, d.kind = true, docBinary
				return
			}
			lang := editor.LanguageFor(spec.rel)
			if df.state.Old == nil {
				df.state.Old, df.state.New = editor.NewBuffer(string(old)), editor.NewBuffer(string(nw))
				df.state.Old.SetLanguage(lang)
				df.state.New.SetLanguage(lang)
			} else {
				df.state.Old.Reset(string(old))
				df.state.New.Reset(string(nw))
			}
			if cmp != nil {
				df.state.UseComparison(cmp)
			}
			if len(old)+len(nw) > diffAheadSize {
				trackBigDiff(df)
				// 比较时临时要的内存还给系统
				releaseMemorySoon()
			}
			d.kind = docDiff
			clampHunk(df)
		})
	}()
}

// reloadDiff 在状态变了之后重读 diff：只有工作区一边会变，看文件的磁盘戳；两边都是固定版本的不重读。
func (a *app) reloadDiff(v *filesView, d *fileDoc) {
	df := d.diff
	if !df.worktree() || files.StatFile(df.abs()).Same(df.stamp) {
		return
	}
	a.loadDiff(v, d)
}

// stepDiff 是 F7 / ⇧F7：在当前 diff 里跳到下一处 / 上一处改动，到头了打开同一组里的下一个 / 上一个文件
// （改动列表，或比较、提交里的文件）。
// 已经是第一处或最后一处、前后也没有文件可换时，在 diff 栏下面提示一下。
func (a *app) stepDiff(m locales.Messages, w *workspace, v *filesView, d *fileDoc, dir int) {
	df := d.diff
	// 还在读：连按 F7 时不跳过没读完的文件
	if d.kind == docLoading {
		return
	}
	hunks := df.state.Hunks()
	if next := df.hunk + dir; d.kind == docDiff && next >= 0 && next < len(hunks) {
		df.hunk = next
		df.state.GoToHunk(next)
		df.state.Current = next + 1
		return
	}
	list := diffGroup(v, df)
	i := slices.IndexFunc(list, func(s diffSpec) bool { return s.rel == df.rel && s.oldRev == df.oldRev && s.newRev == df.newRev })
	if j := i + dir; i >= 0 && j >= 0 && j < len(list) {
		a.openDiff(w, list[j], df.siblings, dir > 0)
		return
	}
	// 这个文件已经不在列表里（刚提交、被回滚、成了冲突文件）：从列表的头或尾接着走
	if i < 0 && len(list) > 0 {
		j := 0
		if dir < 0 {
			j = len(list) - 1
		}
		a.openDiff(w, list[j], df.siblings, dir > 0)
		return
	}
	msg := m.GitLastChange()
	if dir < 0 {
		msg = m.GitFirstChange()
	}
	df.hint, df.hintAt = msg, time.Now()
}

// diffHintFor 是提示显示多久，头尾各有 diffHintFade 的淡入淡出。
const (
	diffHintFor  = 1500 * time.Millisecond
	diffHintFade = 200 * time.Millisecond
)

// diffHint 把 F7 到头的提示画在 diff 栏正下方、diff 区域居中：上下箭头在左上角，视线还在那里，
// 比 mygo 的 toast（只能在窗口底部）近。连按时重新计时，不叠起来。
func diffHint(c *ui.Context, df *diffDoc) {
	fadeHint(c, "diff-hint", df.hint, df.hintAt, 44)
}

// fadeHint 在所在容器顶部 top 处居中画一句提示，从 at 起显示 diffHintFor、头尾淡入淡出；
// at 为零值时一直显示（正在进行的操作）。配色和 toast 一样反过来。
func fadeHint(c *ui.Context, key, hint string, at time.Time, top float32) {
	if hint == "" {
		return
	}
	opacity := float32(1)
	if !at.IsZero() {
		age := time.Since(at)
		if age >= diffHintFor {
			return
		}
		switch left := diffHintFor - age; {
		case age < diffHintFade:
			opacity = float32(age) / float32(diffHintFade)
			c.AnimationFrame()
		case left < diffHintFade:
			opacity = float32(left) / float32(diffHintFade)
			c.AnimationFrame()
		default:
			c.After(left - diffHintFade)
		}
	}
	t := c.Theme()
	ui.Row(c.Key(key)).Absolute().Top(top).Left(0).Right(0).Justify(ui.Center).Children(func() {
		elevFloat(c, ui.Text(c, hint).FontSize(fsBody).TextColor(t.Background).Background(t.Text).Padding(8, 16).Radius(rControl)).
			Opacity(opacity)
	})
}

// diffJump 在 diff 第一次显示时跳到第一处（或从后一个文件退回来时的最后一处）改动。
func diffJump(df *diffDoc) {
	df.jumped = true
	n := len(df.state.Hunks())
	if n == 0 {
		return
	}
	df.hunk = 0
	if !df.first {
		df.hunk = n - 1
	}
	df.state.GoToHunk(df.hunk)
	// 当前这一处加深显示，上下切换时看得出走到了哪
	df.state.Current = df.hunk + 1
}

// revertHunk 回滚工作区 diff 里的一处改动，立刻写回磁盘（和 WebStorm 一样不用再保存）。
func (a *app) revertHunk(m locales.Messages, v *filesView, d *fileDoc, i int) {
	df := d.diff
	if !df.worktree() {
		return
	}
	abs := df.abs()
	// 读进来之后文件又被改过（agent 在写）：拿旧内容回滚会把新写的冲掉，先重读
	if !files.StatFile(abs).Same(df.stamp) {
		a.err = m.GitFileChanged()
		a.loadDiff(v, d)
		return
	}
	df.state.Revert(i)
	// 少了一处，停在原来后面那一处上
	clampHunk(df)
	snap := df.state.New.Snapshot()
	st, err := files.WriteFile(abs, func(w io.Writer) error { _, err := snap.WriteTo(w); return err })
	if err != nil {
		a.err = m.GitRollbackFailed(errtext.Text(err))
		a.loadDiff(v, d)
		return
	}
	df.stamp = st
	if g := v.repoAt(df.root); g != nil {
		a.refreshRepo(g)
	}
}

// --- 文件和提交操作：都在后台执行，完成后重读状态

// gitDo 在仓库 g 里后台执行一个 git 操作，期间 busy 显示操作名；出错显示在错误条上。
func (a *app) gitDo(m locales.Messages, g *gitState, what string, fn func(ctx context.Context, root string) error) {
	a.gitDoThen(m, g, what, fn, nil)
}

// gitDoThen 和 gitDo 一样，成功后在界面线程上调用 done。已经有操作在执行（比如自动 Fetch）时排在后面，
// 不丢掉用户的点击。
func (a *app) gitDoThen(m locales.Messages, g *gitState, what string, fn func(ctx context.Context, root string) error, done func()) {
	a.gitDoKind(m, g, "", what, fn, done)
}

// gitDoKind 同 gitDoThen，带上操作的代号。
// m 是发起操作的视图的界面文字，操作结束后的报错、提示用它。
func (a *app) gitDoKind(m locales.Messages, g *gitState, kind gitKind, what string, fn func(ctx context.Context, root string) error, done func()) {
	a.gitStart(g, gitOp{m: m, kind: kind, what: what, fn: fn, done: done})
}

// gitStart 执行或排队一个操作。
func (a *app) gitStart(g *gitState, op gitOp) {
	if g.repo == "" {
		return
	}
	// 重新找仓库后这个仓库没了（软链删了）：报出来，不默默丢掉操作
	if !slices.Contains(g.v.repos.list, g) {
		op.fail(a, op.m.GitOperationFailed(op.what, errtext.Text(errRepoGone)))
		return
	}
	if g.busy != "" {
		g.queue = append(g.queue, op)
		return
	}
	a.gitRun(g, op)
}

// errRepoGone 是操作的仓库在重新找仓库后不在了。
var errRepoGone = errors.New("repository no longer in workspace")

// autostashLeft 在操作留下 autostash 后：记到这个工作区，打开改动页；操作自己的成功提示（拉到几个提交）
// 和这条合成一条警告，免得一绿一黄同时说两样。shown 是操作结束前已有的提示数。
func (a *app) autostashLeft(m locales.Messages, g *gitState, left *gitx.AutostashLeft, shown int) {
	v := g.v
	g.autostash, g.autostashConflicts, g.autostashSeen = left.Stash, left.Conflicts, false
	title, desc := m.GitRestoreConflicts(), m.GitRestoreConflictsNote()
	if !left.Conflicts {
		title, desc = m.GitRestoreFailed(), m.GitRestoreFailedNote()
	}
	if len(a.toasts) > shown {
		if prev := a.toasts[shown].Title; left.Conflicts {
			title = m.GitRestoreConflictsAfter(prev)
		} else {
			title = m.GitRestoreFailedAfter(prev)
		}
		a.toasts = a.toasts[:shown]
	}
	a.warn(title, desc)
	a.useRepo(v, g)
	if w := a.workspace(v.wsID); w != nil {
		a.openFilesPanel(w, sideChanges)
	}
	v.side, v.ch.tab = sideChanges, 0
}

// warn 在窗口底部提示一句要留意的事，停留久一点。
func (a *app) warn(title, desc string) {
	a.toasts = append(a.toasts, ui.Toast{Title: title, Description: desc, Type: "warning", Timeout: 8 * time.Second})
}

// notify 在窗口底部提示一句（操作成功之类），带 action 时提示上有一个按钮。
func (a *app) notify(title, action string, onAction func()) {
	t := ui.Toast{Title: title, Type: "success", Action: action, OnAction: onAction}
	// 带"恢复"这类按钮的多留一会儿，来得及点
	if action != "" {
		t.Timeout = 8 * time.Second
	}
	a.toasts = append(a.toasts, t)
}

// gitOp 是一个排队等着执行的 git 操作。
type gitOp struct {
	m    locales.Messages
	kind gitKind
	what string
	fn   func(ctx context.Context, root string) error
	done func()
	// failed 在失败或取消时代替直接写错误条：一次点击发起几个操作（跨仓库提交）时把报错攒在一起
	failed func(msg string)
}

func (op gitOp) fail(a *app, msg string) {
	if op.failed != nil {
		op.failed(msg)
		return
	}
	a.err = msg
}

// gitKind 是要按种类判断的 git 操作的代号（要不要给取消、是不是在提交）；其余操作为空。
type gitKind string

const (
	gitFetch  gitKind = "fetch"
	gitPush   gitKind = "push"
	gitUpdate gitKind = "update"
	gitCommit gitKind = "commit"
)

// pending 报告 kind 这种操作正在执行或排着队。
func (g *gitState) pending(kind gitKind) bool {
	return g.busyKind == kind || slices.ContainsFunc(g.queue, func(op gitOp) bool { return op.kind == kind })
}

// busyLine 画忙碌的文字，网络操作后面跟一个"取消"。
func busyLine(c *ui.Context, g *gitState) {
	t := c.Theme()
	m := L(c)
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).Children(func() {
		ui.Text(c, g.busyText(m)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
		if g.cancel != nil {
			if ui.Text(c.Key("cancel-git"), m.CommonCancel()).FontSize(fsSmall).TextColor(t.Accent).Role(ui.RoleButton).Label(m.GitCancelOperation(g.busy)).Clicked() {
				g.cancel()
			}
		}
	})
}

// busyText 是忙碌时显示的文字：正在执行的操作，后面排着的跟在后面。
func (g *gitState) busyText(m locales.Messages) string {
	switch n := len(g.queue); {
	case n == 1:
		return m.GitBusyNext(g.busy, g.queue[0].what)
	case n > 1:
		return m.GitBusyQueue(g.busy, g.queue[0].what, n)
	}
	return g.busy + "…"
}

// networkOp 报告操作要连远端：可能很慢、等不到回应，忙碌时给"取消"。
func networkOp(kind gitKind) bool {
	return kind == gitFetch || kind == gitPush || kind == gitUpdate
}

func (a *app) gitRun(g *gitState, op gitOp) {
	v := g.v
	g.busy, g.busyKind = op.what, op.kind
	root := g.repo
	ctx, cancel := context.WithCancel(gitx.WithOp(context.Background(), op.what))
	if networkOp(op.kind) {
		g.cancel = cancel
	}
	go func() {
		err := op.fn(ctx, root)
		// 照 WebStorm：操作本身成功了，只是本地改动没干净地取回，不当失败报，引去改动页（autostashBanner）
		var left *gitx.AutostashLeft
		if errors.As(err, &left) {
			err = nil
		}
		canceled := ctx.Err() != nil
		switch {
		case canceled:
			gitx.OpCanceled(ctx, root)
		case err != nil:
			gitx.OpResult(ctx, root, err.Error())
		}
		cancel()
		a.update(func() {
			g.busy, g.busyKind, g.cancel = "", "", nil
			if canceled {
				op.fail(a, op.m.GitOperationCanceled(op.what))
			} else if err != nil {
				op.fail(a, op.m.GitOperationFailed(op.what, errtext.Text(err)))
			} else {
				shown := len(a.toasts)
				if op.done != nil {
					op.done()
				}
				if left != nil {
					a.autostashLeft(op.m, g, left, shown)
				}
			}
			a.refreshRepo(g)
			// Fetch、更新、推送之后传入传出马上重算，分支弹窗、比较、diff、日志也重读
			if w := a.workspace(v.wsID); w != nil {
				a.refsChanged(w.ProjectID)
			} else if g.branches.list != nil {
				a.loadBranches(g)
			}
			a.gitNext(g)
		})
	}()
}

// gitNext 在一个操作结束后执行排队的下一个。
func (a *app) gitNext(g *gitState) {
	if g.busy != "" || len(g.queue) == 0 {
		return
	}
	op := g.queue[0]
	g.queue = g.queue[1:]
	// 排队期间重新找过仓库、这个仓库不在了：照 gitStart 报出来，接着看下一个（也都会报）
	if !slices.Contains(g.v.repos.list, g) {
		op.fail(a, op.m.GitOperationFailed(op.what, errtext.Text(errRepoGone)))
		a.gitNext(g)
		return
	}
	a.gitRun(g, op)
}

// rollbackDoc 是编辑器顶栏的"回滚这个文件"：打开回滚对话框只勾这个文件，回滚后编辑器按磁盘重载（没保存的改动一起丢掉，和 WebStorm 一样），
// 新文件被删掉后关掉标签。
func (a *app) rollbackDoc(v *filesView, d *fileDoc) {
	g := v.repoFor(d.path)
	if g == nil {
		return
	}
	e := g.entry(d.path)
	if e == nil || e.Conflict {
		return
	}
	rel := e.Path
	a.openRollbackFor(g, []string{rel}, func(restored, removed []string) {
		switch {
		case slices.Contains(removed, rel):
			a.closeDoc(v, d)
		case slices.Contains(restored, rel):
			a.reloadDoc(d)
		}
	})
}

// entry 返回绝对路径 abs 在 git 状态里的改动，没有改动时为 nil。
func (g *gitState) entry(abs string) *gitx.Entry {
	rel := g.relPath(abs)
	if g.status == nil || rel == "" {
		return nil
	}
	for i := range g.status.Entries {
		if g.status.Entries[i].Path == rel {
			return &g.status.Entries[i]
		}
	}
	return nil
}

// treeMarks 是文件树上的 git 状态，照 WebStorm：文件记 statusLetter（新增 A、未跟踪 ?、冲突 U，其余记 M），
// 含改动的目录记 M（删掉的文件不在树里，它的目录照样标出来）。状态换了才重算。
func (g *gitState) treeMarks() map[string]byte {
	if g.status == g.marksOf {
		return g.marks
	}
	g.marksOf, g.marks = g.status, nil
	if g.status == nil {
		return nil
	}
	g.marks = map[string]byte{}
	for _, e := range g.status.Entries {
		mark := statusLetter(e)
		switch mark {
		case 'A', '?', 'U':
		default:
			mark = 'M'
		}
		abs := g.absPath(e.Path)
		g.marks[abs] = mark
		for dir := filepath.Dir(abs); len(dir) > len(g.repo); dir = filepath.Dir(dir) {
			if _, ok := g.marks[dir]; ok {
				break
			}
			g.marks[dir] = 'M'
		}
	}
	return g.marks
}

// gitStatusText 是悬停文件名时的状态说明：列表不画状态字母，色弱分不清颜色时靠它。
func gitStatusText(m locales.Messages, letter byte) string {
	switch letter {
	case 'A':
		return m.GitAdded()
	case 'D':
		return m.GitDeleted()
	case 'R':
		return m.GitRenamed()
	case '?':
		return m.GitNotAdded()
	case 'U':
		return m.GitHasConflicts()
	}
	return m.GitModified()
}

// rollbackDiffFile 是 diff 栏的"回滚这个文件"：在回滚对话框里回滚了这个文件后接着看同一组的下一个文件（没有就看上一个），
// 都没有了就关掉 diff 标签。
func (a *app) rollbackDiffFile(w *workspace, v *filesView, d *fileDoc) {
	df := d.diff
	var entry *gitx.Entry
	g := v.repoAt(df.root)
	if g == nil {
		return
	}
	if st := g.status; st != nil {
		for i := range st.Entries {
			if st.Entries[i].Path == df.rel {
				entry = &st.Entries[i]
			}
		}
	}
	if entry == nil || !df.worktree() {
		return
	}
	list := diffGroup(v, df)
	i := slices.IndexFunc(list, func(s diffSpec) bool { return s.rel == df.rel })
	var next *diffSpec
	switch {
	case i >= 0 && i+1 < len(list):
		next = &list[i+1]
	case i > 0:
		next = &list[i-1]
	}
	a.openRollbackFor(g, []string{entry.Path}, func(restored, removed []string) {
		if !slices.Contains(restored, df.rel) && !slices.Contains(removed, df.rel) || v.cur != d || d.diff != df {
			return
		}
		if next != nil {
			a.openDiff(w, *next, df.siblings, true)
		} else {
			a.closeDoc(v, d)
		}
	})
}

// picked 报告文件勾没勾上提交：用户点过的按他的，否则已跟踪的改动默认勾上，新文件和冲突默认不勾。
func (g *gitState) picked(e gitx.Entry) bool {
	if e.Conflict {
		return false
	}
	if on, ok := g.picks[e.Path]; ok {
		return on
	}
	return !e.Untracked
}

// pickState 是一组文件的勾选状态。
func (g *gitState) pickState(es []gitx.Entry) int {
	on := 0
	for _, e := range es {
		if g.picked(e) {
			on++
		}
	}
	return pickStateOf(on, len(es))
}

func (g *gitState) setPickedAll(es []gitx.Entry, on bool) {
	for _, e := range es {
		g.setPicked(e, on)
	}
}

func (g *gitState) setPicked(e gitx.Entry, on bool) {
	if g.picks == nil {
		g.picks = map[string]bool{}
	}
	g.picks[e.Path] = on
}

// pickedPaths 是这次要提交的文件，和其中要先加入 Git 的新文件；重命名的连原路径一起提交。
func (g *gitState) pickedPaths() (paths, add []string, n int) {
	if g.status == nil {
		return nil, nil, 0
	}
	for _, e := range g.status.Entries {
		if !g.picked(e) {
			continue
		}
		n++
		paths = append(paths, e.Path)
		if e.Orig != "" {
			paths = append(paths, e.Orig)
		}
		if e.Untracked {
			add = append(add, e.Path)
		}
	}
	return paths, add, n
}

// wholeCommit 返回进行中、只能整体提交的操作（merge、cherry-pick、revert），没有时为空。
func (g *gitState) wholeCommit() string {
	if g.status == nil {
		return ""
	}
	switch op := g.status.Operation; op {
	case "merge", "cherry-pick", "revert":
		return op
	}
	return ""
}

// commitTarget 是一次提交里的一个仓库：勾选的文件，以及它是不是只能整体提交（合并进行中等）。
type commitTarget struct {
	g          *gitState
	paths, add []string
	n          int
	whole      bool
}

// commitTargets 是提交按钮要提交的仓库：各仓库里有勾选文件的。都没勾时，当前仓库在合并等操作中（整体提交）
// 或勾了"修改上一次提交"（只改信息）也算。n 是勾选的文件总数。
func (v *filesView) commitTargets() (ts []commitTarget, n int) {
	for _, g := range v.repos.list {
		paths, add, k := g.pickedPaths()
		if k == 0 {
			continue
		}
		ts = append(ts, commitTarget{g: g, paths: paths, add: add, n: k, whole: g.wholeCommit() != ""})
		n += k
	}
	if g := v.git; len(ts) == 0 && g.repo != "" && (g.wholeCommit() != "" || v.ch.amend) {
		ts = append(ts, commitTarget{g: g, whole: g.wholeCommit() != ""})
	}
	return ts, n
}

// commit 用同一条提交信息在各仓库分别提交勾选的文件。全部成功才清空提交信息；
// 失败的仓库报错、留着勾选，成功的仓库勾选清掉，改好再点一次只提交剩下的。
func (a *app) commit(m locales.Messages, v *filesView) {
	ch := &v.ch
	msg := strings.TrimSpace(ch.message)
	if msg == "" {
		a.err = m.GitMessageRequired()
		return
	}
	targets, _ := v.commitTargets()
	if len(targets) == 0 {
		a.err = m.GitSelectFiles()
		return
	}
	amend := ch.amend
	if amend && len(targets) > 1 {
		a.err = m.GitAmendOneRepo()
		return
	}
	left, failed := len(targets), 0
	var errs []string
	finish := func() {
		if left--; left > 0 || failed > 0 {
			return
		}
		ch.message, ch.amend, ch.amendMsg = "", false, ""
		for _, t := range targets {
			t.g.picks = nil
		}
		subject, _, _ := strings.Cut(msg, "\n")
		if len(targets) > 1 {
			a.notify(m.GitCommittedRepos(subject, len(targets)), "", nil)
		} else {
			a.notify(m.GitCommitted(subject), "", nil)
		}
	}
	for _, t := range targets {
		what := m.GitCommitButton()
		if len(targets) > 1 {
			what = m.GitCommitInRepo(v.repoName(t.g))
		}
		a.gitStart(t.g, gitOp{m: m, kind: gitCommit, what: what, fn: func(ctx context.Context, root string) error {
			if t.whole {
				// 合并、Cherry-pick、还原进行中 git 不许只提交部分文件（commit --only），勾选的先加入 Git，再整体提交暂存区
				var err error
				if len(t.paths) > 0 {
					err = gitx.Stage(ctx, root, t.paths...)
				}
				if err == nil {
					err = gitx.Commit(ctx, root, msg, false, amend)
				}
				return err
			}
			return gitx.CommitPaths(ctx, root, msg, t.paths, t.add, amend)
		}, done: func() {
			// 别的仓库失败、要再点一次时：只去掉提交了的文件的勾选记录，用户取消勾选的其他文件仍不勾
			for _, p := range t.paths {
				delete(t.g.picks, p)
			}
			finish()
		}, failed: func(msg string) {
			failed++
			errs = append(errs, msg)
			a.err = strings.Join(errs, "\n")
			finish()
		}})
	}
}

// amendRepo 是"修改上一次提交"改的仓库：唯一有勾选文件的仓库，都没勾时是当前仓库；勾了几个仓库时为 nil。
func (v *filesView) amendRepo() *gitState {
	var only *gitState
	for _, g := range v.repos.list {
		if _, _, k := g.pickedPaths(); k > 0 {
			if only != nil {
				return nil
			}
			only = g
		}
	}
	if only == nil && v.git.repo != "" {
		only = v.git
	}
	return only
}

// setAmend 切换"修改上一次提交"：打开时输入框还空着，就填上上一次的提交信息。
func (a *app) setAmend(v *filesView, on bool) {
	ch := &v.ch
	ch.amend = on
	// 取消时把自动填进来的旧信息清掉，免得被当成新提交的信息；改过的留着
	if !on {
		if ch.message == ch.amendMsg {
			ch.message = ""
		}
		ch.amendMsg = ""
		return
	}
	g := v.amendRepo()
	if strings.TrimSpace(ch.message) != "" || g == nil {
		return
	}
	root := g.repo
	go func() {
		msg := gitx.LastMessage(context.Background(), root)
		a.update(func() {
			// 读的这会儿勾选换到了别的仓库：不填，免得改的是另一个仓库的提交
			if r := v.amendRepo(); ch.amend && strings.TrimSpace(ch.message) == "" && r != nil && r.repo == root {
				ch.message, ch.amendMsg = msg, msg
			}
		})
	}()
}
