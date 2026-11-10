package app

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/errtext"
	"posse/internal/gitx"
)

// 工作区里的仓库：文件栏开着时找出工作区里的所有仓库（gitx.Discover：子目录、软链过去的、嵌套的），
// 每个仓库一份 gitState。当前仓库（v.git）是改动页、分支按钮、日志浮层对着的那个，跟着当前标签走：
// 打开的文件在哪个仓库里，或 diff、比较是哪个仓库的。diff、比较、日志、Annotate 各自记着自己的仓库，
// 当前仓库换了也照样对着原来的仓库。

const (
	// gitPollOther 是不是当前仓库的多久读一次：只读 status，给文件树标记和行号旁的改动用。
	// 每个仓库每次全量读要起七八个 git 进程，十几个仓库都按 1.5 秒读就是每秒几十个
	gitPollOther = 10 * time.Second
	// repoRescanEvery 是多久重新找一次仓库：新克隆进来、软链删掉。工作区目录本身变了（顶层加了、删了目录）马上找
	repoRescanEvery = time.Minute
)

// repoSet 是工作区里找到的仓库。
type repoSet struct {
	list     []*gitState // 按根目录排好
	none     *gitState   // 工作区里没有仓库时 v.git 指向它：repo 为空
	scanned  bool        // 找过一次了
	scanning bool
	partial  bool      // 撞上扫描上限、可能没找全：不再自动重找，刷新按钮可以
	single   bool      // 找的时候项目关着"识别多个 Git 仓库"
	scanAt   time.Time // 上次找完的时间
	rootMod  time.Time // 上次找的时候工作区目录的修改时间
	statAt   time.Time // 上次看工作区目录修改时间的时间
	waiting  []func()  // 等第一次找完再做的事
	// followDoc、followRepo 是 followRepo 上次看到的当前标签和它的仓库
	followDoc  *fileDoc
	followRepo *gitState
	marks      map[string]byte
	marksOf    []*gitx.Status // 合并 marks 时各仓库的状态
}

// newGitStates 给新建的文件标签准备仓库状态：还没找过，v.git 先指向空的。
func newGitStates(v *filesView) {
	v.repos.none = &gitState{v: v}
	v.git = v.repos.none
}

// repoFor 返回 abs 所在的仓库：嵌套时取最里面的；不在任何仓库里时为 nil。
func (v *filesView) repoFor(abs string) *gitState {
	var best *gitState
	n := -1
	for _, g := range v.repos.list {
		if root, ok := (gitx.Repo{Root: g.repo, Aliases: g.aliases}).Contains(abs); ok && len(root) > n {
			best, n = g, len(root)
		}
	}
	if best == nil {
		// 仓库根目录是真实路径（工作区是指向仓库子目录的软链）：换成真实路径再找
		if r, ok := v.realPath(abs); ok {
			return v.repoFor(r)
		}
	}
	return best
}

// realPath 把工作区里的路径换成真实路径；工作区不经过软链、或 abs 不在工作区里时返回假。
func (v *filesView) realPath(abs string) (string, bool) {
	if v.realRoot == "" || v.realRoot == v.root {
		return "", false
	}
	if abs == v.root || strings.HasPrefix(abs, v.root+string(filepath.Separator)) {
		return v.realRoot + abs[len(v.root):], true
	}
	return "", false
}

// shownPath 是 realPath 反过来：真实路径在工作区里的写法，不在工作区里的原样返回。
func (v *filesView) shownPath(p string) string {
	if v.realRoot == "" || v.realRoot == v.root {
		return p
	}
	if p == v.realRoot || strings.HasPrefix(p, v.realRoot+string(filepath.Separator)) {
		return v.root + p[len(v.realRoot):]
	}
	return p
}

// repoAt 按根目录找仓库；没有了（重找后消失）时为 nil。
func (v *filesView) repoAt(root string) *gitState {
	if root == "" {
		return nil
	}
	for _, g := range v.repos.list {
		if g.repo == root {
			return g
		}
	}
	return nil
}

// projectSingleRepo 报告工作区所在的项目关掉了"识别多个 Git 仓库"。
func (a *app) projectSingleRepo(v *filesView) bool {
	if w := a.workspace(v.wsID); w != nil {
		if p := a.projectOf(w); p != nil {
			return p.SingleRepo
		}
	}
	return false
}

// withRepos 在找过仓库之后调用 fn：已经找过就马上调用，否则开始找、找完再调用。
func (a *app) withRepos(v *filesView, fn func()) {
	if v.repos.scanned {
		fn()
		return
	}
	v.repos.waiting = append(v.repos.waiting, fn)
	a.scanRepos(v)
}

// scanRepos 在后台找工作区里的仓库，找完换上。
func (a *app) scanRepos(v *filesView) {
	if v.repos.scanning {
		return
	}
	v.repos.scanning = true
	root, single := v.root, a.projectSingleRepo(v)
	go func() {
		var mod time.Time
		if fi, err := os.Stat(root); err == nil {
			mod = fi.ModTime()
		}
		d := gitx.Discover(context.Background(), root, !single)
		a.update(func() { a.applyRepos(v, d, single, mod) })
	}()
}

// applyRepos 换上新找到的仓库：还在的沿用原来的状态（勾选、提交信息、排队的操作），没了的丢掉。
func (a *app) applyRepos(v *filesView, d gitx.Discovery, single bool, mod time.Time) {
	rs := &v.repos
	rs.scanning, rs.scanned, rs.partial, rs.single = false, true, d.Partial, single
	rs.scanAt, rs.rootMod = time.Now(), mod
	v.realRoot, _ = filepath.EvalSymlinks(v.root)
	list := make([]*gitState, 0, len(d.Repos))
	for _, r := range d.Repos {
		g := v.repoAt(r.Root)
		if g == nil {
			g = &gitState{v: v, repo: r.Root}
		}
		g.aliases, g.real = r.Aliases, r.Real
		list = append(list, g)
	}
	rs.list = list
	// 终端里的 git 命令按仓库的真实路径归到工作区：仓库变了重新归
	a.runWS = nil
	switch {
	case len(list) == 0:
		a.useRepo(v, rs.none)
	case !slices.Contains(list, v.git):
		a.useRepo(v, a.defaultRepo(v))
	}
	waiting := rs.waiting
	rs.waiting = nil
	for _, fn := range waiting {
		fn()
	}
}

// defaultRepo 是没有别的线索时的当前仓库：当前文件所在的，工作区本身所在的，或者第一个。
func (a *app) defaultRepo(v *filesView) *gitState {
	if d := v.cur; d != nil {
		if g := a.docRepo(v, d); g != nil {
			return g
		}
	}
	if g := v.repoFor(v.root); g != nil {
		return g
	}
	if len(v.repos.list) > 0 {
		return v.repos.list[0]
	}
	return v.repos.none
}

// docRepo 是标签对着的仓库：diff、比较记着的，或文件所在的。日志不在标签栏，不算。
func (a *app) docRepo(v *filesView, d *fileDoc) *gitState {
	switch {
	case d.diff != nil:
		return v.repoAt(d.diff.root)
	case d.comp != nil:
		return v.repoAt(d.comp.root)
	case d.virtual():
		return nil
	}
	return v.repoFor(d.path)
}

// followRepo 让当前仓库跟着当前标签：切到别的仓库里的文件，改动页、分支按钮、日志也换过去。
// 只在标签（或它对着的仓库）换了时跟：之后在弹窗、日志里手动换的仓库留着。
func (a *app) followRepo(v *filesView) {
	rs := &v.repos
	d := v.cur
	var g *gitState
	if d != nil {
		g = a.docRepo(v, d)
	}
	if d == rs.followDoc && g == rs.followRepo {
		return
	}
	rs.followDoc, rs.followRepo = d, g
	if g != nil && g != v.git {
		a.useRepo(v, g)
	}
}

// useRepo 换当前仓库：马上全量读一次它的状态，日志浮层里整个仓库的日志换成它的。
func (a *app) useRepo(v *filesView, g *gitState) {
	if g == v.git {
		return
	}
	v.git = g
	if g.repo != "" && !g.poll.busy {
		g.poll.at = time.Time{}
	}
	for _, d := range v.logs {
		if ld := d.log; ld != nil && ld.filter.Path == "" && ld.root != g.repo {
			ld.root, ld.filter.Branches, ld.head, ld.commit = g.repo, nil, true, nil
			ld.commits, ld.graph = nil, nil
			a.loadLog(v, d, false)
		}
	}
}

// pollRepos 是文件内容看得见时每帧的 Git 部分：没找过仓库先找，到点了重找，各仓库到点了读状态。
func (a *app) pollRepos(c *ui.Context, v *filesView) {
	rs := &v.repos
	if !rs.scanned {
		a.scanRepos(v)
		return
	}
	a.followRepo(v)
	if !rs.scanning && a.rescanDue(c, v) {
		a.scanRepos(v)
	}
	for _, g := range rs.list {
		every := gitPollOther
		if g == v.git {
			every = gitPollEvery
		}
		if g.poll.due(c, every) {
			a.pollRepo(g, g == v.git)
		}
	}
}

// rescanDue 报告该重新找仓库了：项目的开关变了、工作区目录变了、到点了。没找全的不自动重找，免得每分钟都扫一遍大目录。
func (a *app) rescanDue(c *ui.Context, v *filesView) bool {
	rs := &v.repos
	if a.projectSingleRepo(v) != rs.single {
		return true
	}
	if rs.partial {
		return false
	}
	if wait := repoRescanEvery - time.Since(rs.scanAt); wait <= 0 {
		return true
	} else {
		c.After(min(wait, gitPollEvery))
	}
	if time.Since(rs.statAt) >= gitPollEvery {
		rs.statAt = time.Now()
		if fi, err := os.Stat(v.root); err == nil && !fi.ModTime().Equal(rs.rootMod) {
			return true
		}
	}
	return false
}

// pollRepo 在后台读一个仓库的状态，读完交回界面线程。full 为假时只读 status（不是当前仓库）。
func (a *app) pollRepo(g *gitState, full bool) {
	root := g.repo
	go func() {
		ctx := context.Background()
		var st *gitx.Status
		var head map[string]gitx.LineStats
		var stashes []gitx.Stash
		var err error
		if root != "" {
			if st, err = gitx.ReadStatus(ctx, root); err == nil && full {
				head, _ = gitx.NumStatHEAD(ctx, root)
				stashes, _ = gitx.Stashes(ctx, root)
			}
		}
		a.update(func() {
			g.poll.done()
			if g.pollAgain {
				g.pollAgain = false
				g.poll.at = time.Time{}
			}
			if err != nil {
				g.err = errtext.Text(err)
				return
			}
			if st == nil {
				return
			}
			old := g.status
			g.err = ""
			st.Entries = slices.DeleteFunc(st.Entries, func(e gitx.Entry) bool { return g.v.nestedEntry(g, e) })
			g.status = st
			if full {
				g.head, g.stashes = head, stashes
			}
			// 不在改动里的文件不再记它的勾选
			for p := range g.picks {
				if !slices.ContainsFunc(st.Entries, func(e gitx.Entry) bool { return e.Path == p }) {
					delete(g.picks, p)
				}
			}
			g.rows = changeRows(g, st, g.stashes)
			a.gitChanged(g, old)
		})
	}()
}

// refreshGit 在操作之后马上重读当前仓库的状态，不等下一次轮询。
// 还没找过仓库时先找，找完再读。
func (a *app) refreshGit(v *filesView) {
	if !v.repos.scanned {
		if !v.repos.scanning {
			a.withRepos(v, func() { a.refreshRepo(v.git) })
		}
		return
	}
	a.refreshRepo(v.git)
}

// refreshRepo 马上重读一个仓库的状态。
func (a *app) refreshRepo(g *gitState) {
	if g.repo == "" {
		return
	}
	g.headAt = time.Time{}
	// 正在读的那次可能是操作之前开始的：读完马上再读一次，不等下一轮（不是当前仓库的下一轮在 10 秒后）
	if g.poll.busy {
		g.pollAgain = true
		return
	}
	g.poll.busy = true
	a.pollRepo(g, g == g.v.git)
}

// rescanRepos 是改动页的刷新按钮：重新找仓库（没找全的也找），再重读当前仓库。
func (a *app) rescanRepos(v *filesView) {
	a.scanRepos(v)
	a.refreshGit(v)
}

// gitChanged 在一个仓库的状态变了之后更新依赖它的东西：打开的 diff 重读，编辑器的比较基准跟着 HEAD 走。
func (a *app) gitChanged(g *gitState, old *gitx.Status) {
	v := g.v
	// 操作停在冲突上：切到改动，横幅在那里（多个仓库时每个仓库各一条）
	if op := g.status.Operation; op != "" && (old == nil || old.Operation != op) {
		v.side = sideChanges
		// 刚停在冲突上（日志里 cherry-pick、分支弹窗里合并）：文件栏关着也打开，不然看不到继续 / 中止
		if w := a.workspace(v.wsID); w != nil && old != nil {
			a.openFilesPanel(w, sideChanges)
		}
	}
	headMoved := old != nil && old.Commit != g.status.Commit
	for _, d := range v.docs {
		switch {
		case d.diff != nil && d.kind != docLoading:
			if d.diff.root != g.repo {
				continue
			}
			// 终端里提交、reset 之后 HEAD 变了：工作区文件没动，旧的一边却换了
			if headMoved && !d.diff.fixedRevs() {
				a.loadDiff(v, d)
			} else {
				a.reloadDiff(v, d)
			}
		case d.kind == docText && v.repoFor(d.path) == g && (headMoved || d.headFor != g.status.Commit):
			a.loadHead(v, d)
		}
	}
}

// relIn 返回文件所在的仓库和相对它根目录的路径；不在仓库里时 g 为 nil。
func (v *filesView) relIn(abs string) (g *gitState, rel string) {
	if g = v.repoFor(abs); g != nil {
		if rel = g.relPath(abs); rel != "" {
			return g, rel
		}
	}
	return nil, ""
}

// treeMarks 是文件树上各仓库的 git 状态合在一起；哪个仓库的状态换了才重新合并。
func (v *filesView) treeMarks() map[string]byte {
	rs := &v.repos
	same := len(rs.marksOf) == len(rs.list)
	for i, g := range rs.list {
		if !same || rs.marksOf[i] != g.status {
			same = false
			break
		}
	}
	if same {
		return rs.marks
	}
	rs.marksOf = rs.marksOf[:0]
	// 工作区就是这个仓库（或在它里面）：直接用它的
	if len(rs.list) == 1 && !strings.HasPrefix(rs.list[0].repo, v.root+string(filepath.Separator)) {
		rs.marksOf = append(rs.marksOf, rs.list[0].status)
		rs.marks = rs.list[0].treeMarks()
		return rs.marks
	}
	rs.marks = map[string]byte{}
	for _, g := range rs.list {
		rs.marksOf = append(rs.marksOf, g.status)
		gm := g.treeMarks()
		for p, m := range gm {
			// 外层仓库的目录标记不盖住嵌套仓库里文件的标记
			if _, ok := rs.marks[p]; !ok || m != 'M' {
				rs.marks[p] = m
			}
		}
		// 有改动的仓库：它的目录和上面的普通目录（到工作区为止）也标出来，收起时看得见哪里有改动
		if len(gm) > 0 {
			for dir := g.repo; len(dir) > len(v.root) && strings.HasPrefix(dir, v.root+string(filepath.Separator)); dir = filepath.Dir(dir) {
				if _, ok := rs.marks[dir]; !ok {
					rs.marks[dir] = 'M'
				}
			}
		}
	}
	return rs.marks
}

// nestedEntry 报告外层仓库状态里的一项其实是找到的嵌套仓库（git 报成未跟踪的 nested/ 或子模块）：
// 它的改动在它自己那份状态里，外层不再列出，回滚也不碰它。
func (v *filesView) nestedEntry(g *gitState, e gitx.Entry) bool {
	if !e.Untracked || len(v.repos.list) < 2 {
		return false
	}
	abs := g.absPath(strings.TrimSuffix(e.Path, "/"))
	n := v.repoAt(abs)
	return n != nil && n != g
}

// gitBranch 是标签栏分支按钮写的分支：工作区当前仓库检出的分支（游离时是哈希前 8 位），没有仓库时为空。
// 工作区就在仓库里时先用每秒读 HEAD 的 w.Branch，不用等找完仓库；工作区目录本身不是仓库时
// （下面有几个仓库），文件栏没开过也在这里先去找仓库。
func (a *app) gitBranch(w *workspace) string {
	if w.Missing {
		return ""
	}
	v := a.files[w.ID]
	if v == nil || !v.repos.scanned {
		a.scanRepos(a.filesFor(w))
		return w.Branch
	}
	// 文件栏关着时 pollRepos 不跑：项目的开关变了在这里重找
	if !v.repos.scanning && a.projectSingleRepo(v) != v.repos.single {
		a.scanRepos(v)
	}
	if v.git.repo == "" {
		return ""
	}
	if filepath.Clean(v.git.repo) == filepath.Clean(w.Path) && w.Branch != "" {
		return w.Branch
	}
	// 一帧里要问好几次（快捷键、标签栏、分支按钮、日志），读 HEAD 文件最多一秒一次；操作后 refreshRepo 清掉
	g := v.git
	if time.Since(g.headAt) > time.Second {
		g.headRef, g.headAt = gitx.Head(g.repo), time.Now()
	}
	return g.headRef
}

// repoOrGone 按根目录找仓库；重新找仓库后没了时给一个不在列表里的，交给 gitDoKind 报"仓库不在了"。
func (v *filesView) repoOrGone(root string) *gitState {
	if g := v.repoAt(root); g != nil {
		return g
	}
	return &gitState{v: v, repo: root}
}

// setSingleRepo 是项目右键菜单的"识别多个 Git 仓库"：存进常驻进程的项目记录，开着的文件标签下一帧按新的开关重新找仓库。
func (a *app) setSingleRepo(projectID string, single bool) {
	if p := a.projectByID(projectID); p != nil {
		p.SingleRepo = single
	}
	go a.call("project.set", map[string]any{"project": projectID, "singleRepo": single}, nil, 10*time.Second)
}

// changesPage 是改动页自己的状态，多个仓库共用一份：提交信息对所有仓库，提交时各仓库分别提交。
type changesPage struct {
	list     ui.ListState
	tab      int // 0 提交（文件改动），1 储藏（git stash），照 WebStorm 的提交工具窗口
	stashLst ui.ListState
	message  string
	amend    bool
	amendMsg string // 勾"修改上一次提交"时自动填进来的信息
	focusMsg bool   // 下一帧把焦点给提交信息（⌘K）
}

// multiRepo 报告工作区里找到了不止一个仓库：改动页按仓库分组，分支按钮、弹窗、日志可以换仓库。
func (v *filesView) multiRepo() bool { return len(v.repos.list) > 1 }

// repoName 是仓库在界面上的名字：相对工作区的路径；工作区就是它或在它里面时是目录名。
func (v *filesView) repoName(g *gitState) string {
	rel, err := filepath.Rel(v.root, g.repo)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.Base(g.repo)
	}
	return filepath.ToSlash(rel)
}

// changeCount 是文件栏"改动"上的数：所有仓库有改动的文件数。
func (v *filesView) changeCount() int {
	n := 0
	for _, g := range v.repos.list {
		n += g.changeCount()
	}
	return n
}

// rollbackRepo 是改动页顶上"回滚"对着的仓库：当前仓库有改动就是它，否则是第一个有改动的。
func (v *filesView) rollbackRepo() *gitState {
	if v.git.changeCount() > 0 {
		return v.git
	}
	for _, g := range v.repos.list {
		if g.changeCount() > 0 {
			return g
		}
	}
	return nil
}

// repoPicker 是换当前仓库的按钮：仓库名和下拉箭头，点开列出所有仓库。只有一个仓库时不画。
func (a *app) repoPicker(c *ui.Context, v *filesView) {
	if !v.multiRepo() {
		return
	}
	t := c.Theme()
	name := v.repoName(v.git)
	b := ui.Row(c.Key("repo-picker")).Height(sz(26)).Padding(0, 6, 0, 8).Gap(4).Radius(rRow).AlignItems(ui.Center).Shrink(1).
		Role(ui.RoleButton).Label(L(c).GitRepoPickerLabel(name))
	if b.Hovered() {
		b.Background(colors(t).wash)
	}
	b.Children(func() {
		dirGlyph(c, filepath.Base(v.git.repo), fsBody)
		ui.Text(c, name).FontSize(fsSmall).SingleLine().Shrink(1)
		ui.Icon(c, iconDown).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
	})
	menuButton(c, b, func(m *popMenu) {
		for _, g := range v.repos.list {
			if m.Item(v.repoName(g)).Checked(g == v.git).Chosen() {
				a.useRepo(v, g)
			}
		}
	})
}

// sameBranch 报告所有仓库都检出着 branch：分支按钮就只写分支，不写仓库名。没读到状态的仓库不算相同。
func (v *filesView) sameBranch(branch string) bool {
	for _, g := range v.repos.list {
		if g.status == nil || g.status.Head != branch {
			return false
		}
	}
	return true
}
