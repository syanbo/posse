package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/files"
	"hive/internal/gitx"
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
	unstaged map[string]gitx.LineStats
	staged   map[string]gitx.LineStats
	stashes  []gitx.Stash
	rows     []changeRow
	list     ui.ListState
	tab      int // 改动页的分页：0 提交（文件改动），1 搁置（git stash），照 WebStorm 的提交工具窗口
	stashLst ui.ListState
	poll     panelPoll
	message  string
	amend    bool
	busy     string // 正在执行的操作，期间按钮不可点
	err      string
	focusMsg bool // 下一帧把焦点给提交信息（⌘K）
	branches branchInfo
}

type changeGroup uint8

const (
	groupConflict changeGroup = iota
	groupStaged
	groupChanged
	groupUntracked
	groupStash
)

var groupTitles = [...]string{"冲突", "已暂存", "改动", "未跟踪", "搁置"}

// changeRow 是改动列表的一行：组标题、一个文件，或一次搁置。
type changeRow struct {
	group  changeGroup
	header bool
	count  int
	entry  gitx.Entry
	stash  gitx.Stash
}

// changeRows 按组排好改动：冲突、已暂存（有才显示）、改动、未跟踪、搁置。
// 和 WebStorm 一样不强调暂存区：没有已暂存的文件时，所有已跟踪的改动都在"改动"里。
func changeRows(st *gitx.Status, stashes []gitx.Stash) []changeRow {
	var groups [5][]changeRow
	for _, e := range st.Entries {
		switch {
		case e.Conflict:
			groups[groupConflict] = append(groups[groupConflict], changeRow{group: groupConflict, entry: e})
		case e.Untracked:
			groups[groupUntracked] = append(groups[groupUntracked], changeRow{group: groupUntracked, entry: e})
		default:
			if e.Staged() {
				groups[groupStaged] = append(groups[groupStaged], changeRow{group: groupStaged, entry: e})
			}
			if e.Unstaged() {
				groups[groupChanged] = append(groups[groupChanged], changeRow{group: groupChanged, entry: e})
			}
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

// changeCount 是左栏"改动"上显示的数：有改动的文件数，不含搁置。
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
		var unstaged, staged map[string]gitx.LineStats
		var stashes []gitx.Stash
		var err error
		if root != "" {
			if st, err = gitx.ReadStatus(ctx, root); err == nil {
				unstaged, _ = gitx.NumStat(ctx, root, false)
				staged, _ = gitx.NumStat(ctx, root, true)
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
			g.status, g.unstaged, g.staged, g.stashes = st, unstaged, staged, stashes
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
	}
	headMoved := old != nil && old.Commit != v.git.status.Commit
	for _, d := range v.docs {
		switch {
		case d.diff != nil && d.kind != docLoading:
			a.reloadDiff(v, d)
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

// diffSpec 说明一个 diff 比较哪两个版本的哪个文件。版本的写法：newRev 为空是工作区，":" 是暂存区，
// 其余是提交或分支；oldRev 为空表示旧的一边没有这个文件（新文件、根提交）。路径相对仓库根目录。
type diffSpec struct {
	rel, orig      string // 重命名时 orig 是旧路径
	oldRev, newRev string
	label          string // 标签页上的补充说明，为空时按版本推断
}

func (s diffSpec) worktree() bool { return s.newRev == "" }
func (s diffSpec) staged() bool   { return s.newRev == ":" }

// changeSpec 是改动列表里一个文件的 diff：HEAD 对比工作区，或对比暂存区。
func changeSpec(e gitx.Entry, staged bool) diffSpec {
	s := diffSpec{rel: e.Path, orig: e.Orig, oldRev: "HEAD"}
	if staged {
		s.newRev = ":"
	}
	return s
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
	stamp    files.Stamp // 工作区文件的磁盘戳，变了才重读
	sig      string      // 暂存区 diff 读的时候的 HEAD 和暂存区状态，变了才重读
}

// label 是标签页上的补充说明。
func (df *diffDoc) tabLabel() string {
	switch {
	case df.label != "":
		return df.label
	case df.staged():
		return "已暂存的改动"
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
	case rev == ":":
		return "暂存区"
	case len(rev) == 40:
		return rev[:7]
	}
	return rev
}

func diffKey(abs string, s diffSpec) string {
	return "diff:" + s.oldRev + ".." + s.newRev + ":" + abs
}

// openDiff 在文档区打开一个文件的 diff；已经开着就切过去。
func (a *app) openDiff(w *workspace, s diffSpec, siblings []diffSpec, first bool) {
	v := a.showFiles(w)
	g := &v.git
	key := diffKey(g.absPath(s.rel), s)
	for _, d := range v.docs {
		if d.path == key {
			v.cur = d
			return
		}
	}
	df := &diffDoc{diffSpec: s, siblings: siblings, first: first}
	if s.staged() {
		df.sig = stagedSig(g, s.rel)
	}
	df.state = editor.DiffState{Layout: editor.DiffSplit, IgnoreWhitespace: true}
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

// showRev 读一边的内容：版本里没有这个文件时是空的。
func showRev(ctx context.Context, root, rev, rel string) ([]byte, error) {
	b, err := gitx.Show(ctx, root, rev, rel)
	if errors.Is(err, gitx.ErrNotFound) {
		return nil, nil
	}
	return b, err
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
					nw, err = nil, nil
				}
			case spec.staged():
				nw, err = showRev(ctx, root, "", spec.rel)
			default:
				nw, err = showRev(ctx, root, spec.newRev, spec.rel)
			}
		}
		a.update(func() {
			if err != nil {
				d.kind, d.err = docError, err.Error()
				return
			}
			df.stamp = stamp
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
		})
	}()
}

// reloadDiff 在状态变了之后重读 diff：工作区一边看文件的磁盘戳，暂存区一边看 HEAD 和这个文件的暂存状态；
// 两边都是固定版本的不重读。
func (a *app) reloadDiff(v *filesView, d *fileDoc) {
	df := d.diff
	switch {
	case df.staged():
		sig := stagedSig(&v.git, df.rel)
		if sig == df.sig {
			return
		}
		df.sig = sig
	case df.worktree():
		if files.StatFile(v.git.absPath(df.rel)).Same(df.stamp) {
			return
		}
	default:
		return
	}
	a.loadDiff(v, d)
}

func stagedSig(g *gitState, rel string) string {
	if g.status == nil {
		return ""
	}
	s := g.staged[rel]
	return g.status.Commit + "\x00" + strconv.Itoa(s.Added) + "/" + strconv.Itoa(s.Deleted)
}

// stepDiff 是 F7 / ⇧F7：在当前 diff 里跳到下一处 / 上一处改动，到头了打开同一组里的下一个 / 上一个文件
// （改动列表，或比较、提交里的文件）。
func (a *app) stepDiff(w *workspace, v *filesView, d *fileDoc, dir int) {
	df := d.diff
	hunks := df.state.Hunks()
	if next := df.hunk + dir; d.kind == docDiff && next >= 0 && next < len(hunks) {
		df.hunk = next
		df.state.GoToHunk(next)
		df.state.Current = next + 1
		return
	}
	list := df.siblings
	if list == nil {
		for _, r := range v.git.rows {
			if !r.header && r.group != groupStash && !r.entry.Conflict {
				list = append(list, changeSpec(r.entry, r.group == groupStaged))
			}
		}
	}
	i := slices.IndexFunc(list, func(s diffSpec) bool { return s.rel == df.rel && s.oldRev == df.oldRev && s.newRev == df.newRev })
	if j := i + dir; i >= 0 && j >= 0 && j < len(list) {
		a.openDiff(w, list[j], df.siblings, dir > 0)
	}
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
	df.state.Revert(i)
	snap := df.state.New.Snapshot()
	abs := v.git.absPath(df.rel)
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
	g := &v.git
	if g.busy != "" || g.repo == "" {
		return
	}
	g.busy = what
	root := g.repo
	go func() {
		err := fn(context.Background(), root)
		a.update(func() {
			g.busy = ""
			if err != nil {
				a.err = what + "失败：" + err.Error()
			}
			a.refreshGit(v)
			if g.branches.list != nil {
				a.loadBranches(v)
			}
			// Fetch、更新、推送之后分支按钮上的传入、传出马上重算
			if tr := a.track[v.wsID]; tr != nil {
				tr.poll = panelPoll{}
			}
		})
	}()
}

// askRollback 回滚文件前确认：已跟踪的恢复成 HEAD 的样子，新文件会被删除。
func (a *app) askRollback(v *filesView, entries []gitx.Entry) {
	var tracked, fresh []string
	for _, e := range entries {
		switch {
		case e.Untracked || e.X == 'A':
			fresh = append(fresh, e.Path)
		default:
			tracked = append(tracked, e.Path)
			if e.Orig != "" {
				tracked = append(tracked, e.Orig)
			}
		}
	}
	name := filepath.Base(entries[0].Path)
	title := "回滚 " + name + " 的改动？"
	if len(entries) > 1 {
		title = "回滚这 " + strconv.Itoa(len(entries)) + " 个文件的改动？"
	}
	body := "已跟踪的文件会恢复成上一次提交时的样子，没提交的改动会丢失。"
	if len(fresh) > 0 {
		body = "新文件会被删除，已跟踪的文件会恢复成上一次提交时的样子，没提交的改动会丢失。"
	}
	a.confirm(title, body, "回滚", func() {
		a.update(func() {
			a.gitDo(v, "回滚", func(ctx context.Context, root string) error {
				if len(tracked) > 0 {
					if err := gitx.Rollback(ctx, root, tracked...); err != nil {
						return err
					}
				}
				if len(fresh) > 0 {
					return gitx.RemoveNew(ctx, root, fresh...)
				}
				return nil
			})
		})
	})
}

// commitMode 是提交按钮做什么：有已暂存的文件时只提交它们，否则提交所有已跟踪文件的改动。
func (g *gitState) commitMode() (label string, all bool) {
	for _, r := range g.rows {
		if r.group == groupStaged {
			return "提交已暂存的", false
		}
	}
	return "提交", true
}

func (a *app) commit(v *filesView) {
	g := &v.git
	msg := strings.TrimSpace(g.message)
	if msg == "" {
		a.err = "先写提交信息"
		return
	}
	_, all := g.commitMode()
	amend := g.amend
	a.gitDo(v, "提交", func(ctx context.Context, root string) error {
		if err := gitx.Commit(ctx, root, msg, all, amend); err != nil {
			return err
		}
		a.update(func() { g.message, g.amend = "", false })
		return nil
	})
}

// setAmend 切换"修改上一次提交"：打开时输入框还空着，就填上上一次的提交信息。
func (a *app) setAmend(v *filesView, on bool) {
	g := &v.git
	g.amend = on
	if !on || strings.TrimSpace(g.message) != "" || g.repo == "" {
		return
	}
	root := g.repo
	go func() {
		msg := gitx.LastMessage(context.Background(), root)
		a.update(func() {
			if g.amend && strings.TrimSpace(g.message) == "" {
				g.message = msg
			}
		})
	}()
}
