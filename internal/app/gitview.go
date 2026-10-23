package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/files"
	"hive/internal/gitx"
	"hive/internal/media"
)

// 文件标签里的 Git：左栏"改动"、diff 标签页、编辑器行号旁的改动标记。全部在窗口进程里调用系统 git。
// 交互照 WebStorm（见 docs/plans/git-ops.html）：改动列表自动刷新，diff 默认左右对照、忽略空白，
// F7 / ⇧F7 跨文件跳到下一处 / 上一处改动。

const (
	gitPollEvery = 1500 * time.Millisecond
	sideFiles    = 0
	sideSearch   = 1
	sideChanges  = 2
)

// gitState 是一个工作区的 Git 状态。
type gitState struct {
	checked  bool   // 查过是不是 git 仓库
	repo     string // 仓库根目录；不是 git 仓库时为空
	status   *gitx.Status
	head     map[string]gitx.LineStats // 相对 HEAD 的增删行数，改动列表显示
	picks    map[string]bool           // 勾选提交的文件里用户改过的；没改过的用默认（见 picked）
	stashes  []gitx.Stash
	rows     []changeRow
	folded   map[changeGroup]*bool // 改动列表里收起的组；没有记录是展开
	list     ui.ListState
	tab      int // 改动页的分页：0 提交（文件改动），1 储藏（git stash），照 WebStorm 的提交工具窗口
	stashLst ui.ListState
	poll     panelPoll
	message  string
	amend    bool
	amendMsg string // 勾"修改上一次提交"时自动填进来的信息
	busy     string // 正在执行的操作，期间按钮不可点
	queue    []gitOp
	cancel   context.CancelFunc // 正在执行的是网络操作时可以取消
	err      string
	focusMsg bool // 下一帧把焦点给提交信息（⌘K）
	branches branchInfo
}

type changeGroup uint8

const (
	groupConflict changeGroup = iota
	groupChanged
	groupUntracked
	groupStash
)

var groupTitles = [...]string{"冲突", "改动", "未跟踪", "储藏"}

// changeRow 是改动列表的一行：组标题、一个文件，或一条储藏。
type changeRow struct {
	group  changeGroup
	header bool
	count  int
	entry  gitx.Entry
	stash  gitx.Stash
}

// changeRows 按组排好改动：冲突、改动、未跟踪、储藏。
// 和 WebStorm 一样不分暂存区：已跟踪的改动不管暂存了没有都在"改动"里，提交时按勾选。
func changeRows(st *gitx.Status, stashes []gitx.Stash) []changeRow {
	var groups [4][]changeRow
	for _, e := range st.Entries {
		switch {
		case e.Conflict:
			groups[groupConflict] = append(groups[groupConflict], changeRow{group: groupConflict, entry: e})
		case e.Untracked:
			groups[groupUntracked] = append(groups[groupUntracked], changeRow{group: groupUntracked, entry: e})
		default:
			groups[groupChanged] = append(groups[groupChanged], changeRow{group: groupChanged, entry: e})
		}
	}
	for _, s := range stashes {
		groups[groupStash] = append(groups[groupStash], changeRow{group: groupStash, stash: s})
	}
	var rows []changeRow
	for g, rs := range groups {
		if len(rs) == 0 {
			continue
		}
		slices.SortStableFunc(rs, func(a, b changeRow) int { return strings.Compare(a.entry.Path, b.entry.Path) })
		rows = append(rows, changeRow{group: changeGroup(g), header: true, count: len(rs)})
		rows = append(rows, rs...)
	}
	return rows
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

// changeCount 是左栏"改动"上显示的数：有改动的文件数，不含储藏。
func (g *gitState) changeCount() int {
	if g.status == nil {
		return 0
	}
	return len(g.status.Entries)
}

// pollGit 在后台读 git 状态，读完交回界面线程；第一次先确认是不是 git 仓库。
func (a *app) pollGit(v *filesView) {
	g := &v.git
	root, checked := g.repo, g.checked
	go func() {
		ctx := context.Background()
		if !checked {
			root = repoRoot(v.root, gitx.TopLevel(ctx, v.root))
		}
		var st *gitx.Status
		var head map[string]gitx.LineStats
		var stashes []gitx.Stash
		var err error
		if root != "" {
			if st, err = gitx.ReadStatus(ctx, root); err == nil {
				head, _ = gitx.NumStatHEAD(ctx, root)
				stashes, _ = gitx.Stashes(ctx, root)
			}
		}
		a.update(func() {
			g.poll.done()
			g.checked, g.repo = true, root
			if err != nil {
				g.err = err.Error()
				return
			}
			if st == nil {
				return
			}
			old := g.status
			g.err = ""
			g.status, g.head, g.stashes = st, head, stashes
			// 不在改动里的文件不再记它的勾选
			for p := range g.picks {
				if !slices.ContainsFunc(st.Entries, func(e gitx.Entry) bool { return e.Path == p }) {
					delete(g.picks, p)
				}
			}
			g.rows = changeRows(st, stashes)
			a.gitChanged(v, old)
		})
	}()
}

// refreshGit 在操作之后马上重读状态，不等下一次轮询。
func (a *app) refreshGit(v *filesView) {
	if v.git.poll.busy {
		return
	}
	v.git.poll.busy = true
	a.pollGit(v)
}

// gitChanged 在状态变了之后更新依赖它的东西：打开的 diff 重读，编辑器的比较基准跟着 HEAD 走。
func (a *app) gitChanged(v *filesView, old *gitx.Status) {
	// 操作停在冲突上：切到改动，横幅在那里
	if op := v.git.status.Operation; op != "" && (old == nil || old.Operation != op) {
		v.side = sideChanges
		// 刚停在冲突上（日志里 cherry-pick、分支弹窗里合并）：文件栏关着也打开，不然看不到继续 / 中止
		if w := a.workspace(v.wsID); w != nil && old != nil {
			a.openFilesPanel(w, sideChanges)
		}
	}
	headMoved := old != nil && old.Commit != v.git.status.Commit
	for _, d := range v.docs {
		switch {
		case d.diff != nil && d.kind != docLoading:
			// 终端里提交、reset 之后 HEAD 变了：工作区文件没动，旧的一边却换了
			if headMoved && !d.diff.fixedRevs() {
				a.loadDiff(v, d)
			} else {
				a.reloadDiff(v, d)
			}
		case d.kind == docText && (headMoved || d.headFor != v.git.status.Commit):
			a.loadHead(v, d)
		}
	}
}

// repoRoot 把 git 给的仓库根目录（已解析符号链接，如 /private/var/…）换成工作区路径的写法，
// 文件标签里的路径都以工作区路径为准，两种写法混用会把同一个文件当成两个。
func repoRoot(ws, top string) string {
	if top == "" {
		return ""
	}
	real, err := filepath.EvalSymlinks(ws)
	if err != nil {
		return top
	}
	up, err := filepath.Rel(real, top)
	if err != nil || (up != "." && up != ".." && !strings.HasPrefix(up, ".."+string(filepath.Separator))) {
		return top
	}
	return filepath.Join(ws, up)
}

// relPath 把绝对路径换成相对仓库根目录的路径；不在仓库里时返回空。
func (g *gitState) relPath(abs string) string {
	if g.repo == "" {
		return ""
	}
	rel, err := filepath.Rel(g.repo, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	return filepath.ToSlash(rel)
}

func (g *gitState) absPath(rel string) string {
	return filepath.Join(g.repo, filepath.FromSlash(rel))
}

// loadHead 读文件在 HEAD 里的内容作为编辑器行号旁改动标记的基准；不在仓库里或是新文件时不标。
func (a *app) loadHead(v *filesView, d *fileDoc) {
	g := &v.git
	rel := g.relPath(d.path)
	if rel == "" || g.status == nil || g.status.Commit == "" {
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
	rel, orig      string // 重命名时 orig 是旧路径
	oldRev, newRev string
	label          string // 标签页上的补充说明，为空时按版本推断
}

func (s diffSpec) worktree() bool { return s.newRev == "" }

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
func changeSpec(e gitx.Entry) diffSpec {
	return diffSpec{rel: e.Path, orig: e.Orig, oldRev: "HEAD"}
}

// diffDoc 是 diff 标签页。
type diffDoc struct {
	diffSpec
	siblings []diffSpec // 同一组的文件（比较、提交里的），F7 在它们之间跳；为空时在改动列表里跳
	state    editor.DiffState
	hunk     int  // F7 走到的改动
	first    bool // 读进来后跳到第一处改动；⇧F7 从后一个文件退回来时为假，跳到最后一处
	jumped   bool // 已经跳过
	binary   bool
	images   [2]diffImage // 图片文件的两边：改之前、改之后
	stamp    files.Stamp  // 工作区文件的磁盘戳，变了才重读
	hint     string       // F7 到头时 diff 栏下面的提示（diffHint）
	hintAt   time.Time
}

// diffImage 是图片 diff 的一边：缩好的图、原始尺寸、文件大小；size 为 -1 表示这一边没有文件（新增或删除）。
type diffImage struct {
	img  *ui.Bitmap
	w, h int
	size int
	err  string
}

func loadDiffImage(b []byte, exists bool) diffImage {
	if !exists {
		return diffImage{size: -1}
	}
	img, dim, err := media.ScaledImageBytes(b, imageMaxPx)
	if err != nil {
		return diffImage{size: len(b), err: "解不开这张图：" + err.Error()}
	}
	return diffImage{img: ui.NewBitmap(img), w: dim.X, h: dim.Y, size: len(b)}
}

// label 是标签页上的补充说明。
func (df *diffDoc) tabLabel() string {
	switch {
	case df.label != "":
		return df.label
	case df.worktree() && df.oldRev == "HEAD":
		return "改动"
	}
	return shortRev(df.oldRev) + " → " + shortRev(df.newRev)
}

// shortRev 是版本在界面上的写法：提交取前 7 位，空的是工作区。
func shortRev(rev string) string {
	switch {
	case rev == "":
		return "工作区"
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
	g := &v.git
	key := diffKey(g.absPath(s.rel), s)
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
	i := len(v.docs)
	if j := slices.Index(v.docs, v.cur); j >= 0 {
		i = j + 1
	}
	v.docs = slices.Insert(v.docs, i, d)
	v.cur = d
	d.started = true
	a.loadDiff(v, d)
}

// diffGroup 是 diff 标签里可以切换的文件：比较、提交里同一组的文件，没有就是改动列表。
func diffGroup(v *filesView, df *diffDoc) []diffSpec {
	if df.siblings != nil {
		return df.siblings
	}
	var list []diffSpec
	for _, r := range v.git.rows {
		if !r.header && r.group != groupStash && !r.entry.Conflict {
			list = append(list, changeSpec(r.entry))
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

// diffMaxSize 以上的文件不比较。
const diffMaxSize = 2 << 20

var tooLargeDiff = fmt.Sprintf("文件超过 %d MB，不在这里比较", diffMaxSize>>20)

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
	g := &v.git
	root, df := g.repo, d.diff
	spec := df.diffSpec
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
		a.update(func() {
			// 读的时候标签已经换成别的文件
			if d.diff != df {
				return
			}
			if err != nil {
				d.kind, d.err = docError, err.Error()
				return
			}
			df.stamp = stamp
			// 比较在界面线程上做，几 MB 的 lock 文件就要卡一两秒
			if !isImage && max(len(old), len(nw)) > diffMaxSize {
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
			d.kind = docDiff
			clampHunk(df)
		})
	}()
}

// reloadDiff 在状态变了之后重读 diff：只有工作区一边会变，看文件的磁盘戳；两边都是固定版本的不重读。
func (a *app) reloadDiff(v *filesView, d *fileDoc) {
	df := d.diff
	if !df.worktree() || files.StatFile(v.git.absPath(df.rel)).Same(df.stamp) {
		return
	}
	a.loadDiff(v, d)
}

// stepDiff 是 F7 / ⇧F7：在当前 diff 里跳到下一处 / 上一处改动，到头了打开同一组里的下一个 / 上一个文件
// （改动列表，或比较、提交里的文件）。
// 已经是第一处或最后一处、前后也没有文件可换时，在 diff 栏下面提示一下。
func (a *app) stepDiff(w *workspace, v *filesView, d *fileDoc, dir int) {
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
	msg := "已经是最后一处改动"
	if dir < 0 {
		msg = "已经是第一处改动"
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
		ui.Text(c, hint).FontSize(12.5).TextColor(t.Background).Background(t.Text).Padding(7, 14).Radius(8).
			Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, 0.25)).Opacity(opacity)
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
func (a *app) revertHunk(v *filesView, d *fileDoc, i int) {
	df := d.diff
	if !df.worktree() {
		return
	}
	abs := v.git.absPath(df.rel)
	// 读进来之后文件又被改过（agent 在写）：拿旧内容回滚会把新写的冲掉，先重读
	if !files.StatFile(abs).Same(df.stamp) {
		a.err = "文件刚被改过，已重新读取，请再看一眼要回滚的改动"
		a.loadDiff(v, d)
		return
	}
	df.state.Revert(i)
	// 少了一处，停在原来后面那一处上
	clampHunk(df)
	snap := df.state.New.Snapshot()
	st, err := files.WriteFile(abs, func(w io.Writer) error { _, err := snap.WriteTo(w); return err })
	if err != nil {
		a.err = "回滚失败：" + err.Error()
		a.loadDiff(v, d)
		return
	}
	df.stamp = st
	a.refreshGit(v)
}

// --- 文件和提交操作：都在后台执行，完成后重读状态

// gitDo 在后台执行一个 git 操作，期间 busy 显示操作名；出错显示在错误条上。
func (a *app) gitDo(v *filesView, what string, fn func(ctx context.Context, root string) error) {
	a.gitDoThen(v, what, fn, nil)
}

// gitDoThen 和 gitDo 一样，成功后在界面线程上调用 done。已经有操作在执行（比如自动 Fetch）时排在后面，
// 不丢掉用户的点击。
func (a *app) gitDoThen(v *filesView, what string, fn func(ctx context.Context, root string) error, done func()) {
	g := &v.git
	if g.repo == "" {
		return
	}
	op := gitOp{what: what, fn: fn, done: done}
	if g.busy != "" {
		g.queue = append(g.queue, op)
		return
	}
	a.gitRun(v, op)
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
	what string
	fn   func(ctx context.Context, root string) error
	done func()
}

// pending 报告 what 正在执行或排着队。
func (g *gitState) pending(what string) bool {
	return g.busy == what || slices.ContainsFunc(g.queue, func(op gitOp) bool { return op.what == what })
}

// busyLine 画忙碌的文字，网络操作后面跟一个"取消"。
func busyLine(c *ui.Context, g *gitState) {
	t := c.Theme()
	ui.Row(c).Gap(6).AlignItems(ui.Center).Shrink(1).Children(func() {
		ui.Text(c, g.busyText()).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1)
		if g.cancel != nil {
			if ui.Text(c.Key("cancel-git"), "取消").FontSize(11).TextColor(t.Accent).Role(ui.RoleButton).Label("取消 " + g.busy).Clicked() {
				g.cancel()
			}
		}
	})
}

// busyText 是忙碌时显示的文字：正在执行的操作，后面排着的跟在后面。
func (g *gitState) busyText() string {
	s := g.busy + "…"
	if n := len(g.queue); n > 0 {
		s += "，接着" + g.queue[0].what
		if n > 1 {
			s += fmt.Sprintf("等 %d 个", n)
		}
	}
	return s
}

// networkOp 报告操作要连远端：可能很慢、等不到回应，忙碌时给"取消"。
func networkOp(what string) bool {
	return what == "Fetch" || what == "Push" || strings.HasPrefix(what, "更新")
}

func (a *app) gitRun(v *filesView, op gitOp) {
	g := &v.git
	g.busy = op.what
	root := g.repo
	ctx, cancel := context.WithCancel(gitx.WithOp(context.Background(), op.what))
	if networkOp(op.what) {
		g.cancel = cancel
	}
	go func() {
		err := op.fn(ctx, root)
		canceled := ctx.Err() != nil
		switch {
		case canceled:
			gitx.OpResult(ctx, root, "已取消")
		case err != nil:
			gitx.OpResult(ctx, root, err.Error())
		}
		cancel()
		a.update(func() {
			g.busy, g.cancel = "", nil
			if canceled {
				a.err = op.what + "已取消"
			} else if err != nil {
				a.err = op.what + "失败：" + err.Error()
			} else if op.done != nil {
				op.done()
			}
			a.refreshGit(v)
			// Fetch、更新、推送之后传入传出马上重算，分支弹窗、比较、diff、日志也重读
			if w := a.workspace(v.wsID); w != nil {
				a.refsChanged(w.ProjectID)
			} else if g.branches.list != nil {
				a.loadBranches(v)
			}
			a.gitNext(v)
		})
	}()
}

// gitNext 在一个操作结束后执行排队的下一个。
func (a *app) gitNext(v *filesView) {
	g := &v.git
	if g.busy != "" || len(g.queue) == 0 {
		return
	}
	op := g.queue[0]
	g.queue = g.queue[1:]
	a.gitRun(v, op)
}

// rollbackDoc 是编辑器顶栏的"回滚这个文件"：打开回滚对话框只勾这个文件，回滚后编辑器按磁盘重载（没保存的改动一起丢掉，和 WebStorm 一样），
// 新文件被删掉后关掉标签。
func (a *app) rollbackDoc(v *filesView, d *fileDoc) {
	e := v.git.entry(d.path)
	if e == nil || e.Conflict {
		return
	}
	rel := e.Path
	a.openRollbackFor(v, []string{rel}, func(restored, removed []string) {
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

// rollbackDiffFile 是 diff 栏的"回滚这个文件"：在回滚对话框里回滚了这个文件后接着看同一组的下一个文件（没有就看上一个），
// 都没有了就关掉 diff 标签。
func (a *app) rollbackDiffFile(w *workspace, v *filesView, d *fileDoc) {
	df := d.diff
	var entry *gitx.Entry
	if st := v.git.status; st != nil {
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
	a.openRollbackFor(v, []string{entry.Path}, func(restored, removed []string) {
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

// wholeCommit 返回进行中、只能整体提交的操作的名字（合并、拣选、还原），没有时为空。
func (g *gitState) wholeCommit() string {
	if g.status == nil {
		return ""
	}
	switch g.status.Operation {
	case "merge":
		return "合并"
	case "cherry-pick":
		return "拣选"
	case "revert":
		return "还原"
	}
	return ""
}

func (a *app) commit(v *filesView) {
	g := &v.git
	msg := strings.TrimSpace(g.message)
	if msg == "" {
		a.err = "先写提交信息"
		return
	}
	paths, add, n := g.pickedPaths()
	amend, whole := g.amend, g.wholeCommit() != ""
	if n == 0 && !amend && !whole {
		a.err = "先勾选要提交的文件"
		return
	}
	a.gitDoThen(v, "提交", func(ctx context.Context, root string) error {
		var err error
		if whole {
			// 合并、拣选、还原进行中 git 不许只提交部分文件（commit --only），勾选的先加入 Git，再整体提交暂存区
			if len(paths) > 0 {
				err = gitx.Stage(ctx, root, paths...)
			}
			if err == nil {
				err = gitx.Commit(ctx, root, msg, false, amend)
			}
		} else {
			err = gitx.CommitPaths(ctx, root, msg, paths, add, amend)
		}
		if err != nil {
			return err
		}
		a.update(func() { g.message, g.amend, g.picks = "", false, nil })
		return nil
	}, func() {
		subject, _, _ := strings.Cut(msg, "\n")
		a.notify("已提交："+subject, "", nil)
	})
}

// setAmend 切换"修改上一次提交"：打开时输入框还空着，就填上上一次的提交信息。
func (a *app) setAmend(v *filesView, on bool) {
	g := &v.git
	g.amend = on
	// 取消时把自动填进来的旧信息清掉，免得被当成新提交的信息；改过的留着
	if !on {
		if g.message == g.amendMsg {
			g.message = ""
		}
		g.amendMsg = ""
		return
	}
	if strings.TrimSpace(g.message) != "" || g.repo == "" {
		return
	}
	root := g.repo
	go func() {
		msg := gitx.LastMessage(context.Background(), root)
		a.update(func() {
			if g.amend && strings.TrimSpace(g.message) == "" {
				g.message, g.amendMsg = msg, msg
			}
		})
	}()
}
