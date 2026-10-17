package app

import (
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"hive/internal/files"
)

// 文件标签：每个工作区一个，和终端标签并列，浏览、查看、轻量编辑和检索工作区目录里的文件。
// 全部在窗口进程里，不经过常驻进程。看得见时每秒 stat 一次展开的目录和打开的文件，
// 发现 agent 的修改；不用 fsnotify：macOS 上它基于 kqueue，监听一个目录要为其中每个文件各开一个描述符。

// filesTabID 是 activeTerm 里表示"选中了打开的文件"的值，选中哪个看 filesView.cur；终端 ID 都以 t_ 开头，不会冲突。
const filesTabID = "files"

const (
	filesPollEvery = time.Second
	// listMaxAge 是文件清单的有效期：只监听展开的目录，没展开的目录里新增的文件靠重列清单才看得到
	listMaxAge  = 10 * time.Second
	imageMaxPx  = 2400 // 图片按这个宽度解码，2 倍屏下 1200pt
	editorInset = 1    // 编辑器字号比终端小一号
)

type docKind uint8

const (
	docLoading docKind = iota
	docText
	docImage
	docBinary
	docImageDiff // 图片的 diff：左右两张图（gitpanel.go 的 imageDiffView）
	docTooLarge
	docError
	docDiff    // Git 的 diff 标签页（gitview.go）
	docCompare // 分支比较（compare.go）
	docLog     // Git 日志（logview.go）
)

// fileDoc 是文件标签里打开的一个文件。
type fileDoc struct {
	path string
	kind docKind
	// transient 是预览标签（照 Orca）：文件树里单击打开，再单击别的文件就被换掉；双击、改了内容就常驻
	transient bool
	ed        editor.State
	// base 是编辑器内容所依据的磁盘版本，保存前比对它，防止覆盖 agent 刚写的内容；
	// seen 是轮询最近看到的磁盘版本，变了才重读。两者在冲突时不同。
	base, seen files.Stamp
	readOnly   bool
	conflict   bool // 磁盘上改了，自己也有未保存的改动
	deleted    bool
	err        string
	img        *ui.Bitmap
	preview    bool // Markdown 显示预览
	md         []mdBlock
	mdVer      uint64
	// 未保存判断按缓冲区版本缓存：Modified 在有改动时要整份比较文本
	modVer   uint64
	modStale bool
	modified bool
	// gotoLine、gotoSpan 是加载后要跳到的行（从 1 起）和要选中的字节范围（行内），0 表示不跳
	gotoLine int
	gotoSpan [2]int
	shown    int  // 编辑器建过几帧：第一帧还没有尺寸，跳行要等到第二帧
	focus    bool // 下一帧把焦点给编辑器
	edRef    ui.Ref
	started  bool // 已经开始读文件
	// diff 不为空时这是一个 diff 标签页，path 是 diffKey；comp 不为空时是分支比较，path 是 compareKey
	diff *diffDoc
	comp *compareDoc
	log  *logDoc
	// change 是编辑器行号旁改动标记弹出的卡片（changepop.go），nil 是没开
	change *changePop
	blame  *blameDoc
	// headBuf 是文件在 HEAD 里的内容，编辑器行号旁的改动标记以它为基准；headFor 是读它时的 HEAD
	headBuf *editor.Buffer
	headFor string
}

func (d *fileDoc) name() string {
	if ld := d.log; ld != nil {
		if ld.filter.Path != "" {
			return filepath.Base(ld.filter.Path)
		}
		return "Git 日志"
	}
	if d.diff != nil {
		return "差异：" + path.Base(d.diff.rel)
	}
	if cd := d.comp; cd != nil {
		if cd.files {
			return "工作区 ↔ " + cd.other
		}
		return cd.other + " ↔ " + cd.cur
	}
	return filepath.Base(d.path)
}

// virtual 报告这个标签页不对应磁盘上的文件（diff、比较）：不轮询磁盘，不记进 ui.json。
func (d *fileDoc) virtual() bool {
	return d.diff != nil || d.comp != nil || d.log != nil
}

// isPreview 报告文档还是预览标签。改过内容就转为常驻，保存或撤销回原样之后也不会被下一次单击换掉。
func (d *fileDoc) isPreview() bool {
	if d.transient && d.isModified() {
		d.transient = false
	}
	return d.transient
}

// isModified 报告有没有未保存的改动，只在缓冲区变了之后重新比较。
func (d *fileDoc) isModified() bool {
	if d.kind != docText || d.ed.Buffer == nil {
		return false
	}
	if v := d.ed.Buffer.Version(); d.modStale || v != d.modVer {
		d.modVer, d.modStale = v, false
		d.modified = d.ed.Buffer.Modified()
	}
	return d.modified
}

// fileSearch 是文件标签左栏的全文检索。
type fileSearch struct {
	query      files.Query
	gen        int // 每次检索加一，旧检索交回的结果丢掉
	cancel     context.CancelFunc
	running    bool
	hits       []files.Hit
	rows       []searchRow
	truncated  bool
	err        string
	list       ui.ListState
	focus      bool // 下一帧把焦点给检索框
	showFilter bool
}

// searchRow 是检索结果列表的一行：文件名一行，或其中的一处命中。
type searchRow struct {
	hit    int // hits 的下标
	header bool
	count  int // 文件行：这个文件里的命中数
}

// filesView 是一个工作区的文件标签。
type filesView struct {
	wsID     string
	root     string
	tree     *files.Tree
	treeList ui.ListState
	list     *files.List // 文件清单：快速打开、检索、判断忽略；文件标签不可见时放掉
	listAt   time.Time
	listing  bool
	docs     []*fileDoc
	cur      *fileDoc
	side     int // 文件栏：sideFiles、sideSearch、sideChanges
	search   fileSearch
	poll     panelPoll
	prevTerm string     // 切到文件标签前选中的终端，关掉最后一个文件时切回去
	logs     []*fileDoc // 底部 Git 日志浮层里的日志和文件历史（logview.go），不在标签栏
	logCur   *fileDoc
	git      gitState
}

// filesFor 返回工作区的文件标签，没有就新建，并恢复上次打开的文件和展开的目录。
func (a *app) filesFor(w *workspace) *filesView {
	if v := a.files[w.ID]; v != nil && v.root == filepath.Clean(w.Path) {
		return v
	}
	v := &filesView{wsID: w.ID, root: filepath.Clean(w.Path), tree: files.NewTree(w.Path)}
	if a.files == nil {
		a.files = map[string]*filesView{}
	}
	a.files[w.ID] = v
	saved := a.state.Files[w.ID]
	for _, d := range saved.Expanded {
		if d == v.root || strings.HasPrefix(d, v.root+string(filepath.Separator)) {
			v.tree.SetExpanded(d, true)
		}
	}
	for _, p := range saved.Open {
		d := &fileDoc{path: p}
		v.docs = append(v.docs, d)
		if p == saved.Current {
			v.cur = d
		}
	}
	if v.cur == nil && len(v.docs) > 0 {
		v.cur = v.docs[len(v.docs)-1]
	}
	a.loadDirs(v, v.tree.SetExpanded(v.root, true))
	return v
}

// filesActive 报告工作区选中的是不是文件标签。
func (a *app) filesActive(w *workspace) bool {
	return w != nil && a.activeTerm[w.ID] == filesTabID
}

// showFiles 切到工作区的文件标签。
func (a *app) showFiles(w *workspace) *filesView {
	v := a.filesFor(w)
	if cur := a.activeTerm[w.ID]; cur != filesTabID {
		v.prevTerm = cur
		a.activeTerm[w.ID] = filesTabID
	}
	return v
}

// leaveFiles 从文件标签切回之前的终端；没有终端时留在文件标签。
func (a *app) leaveFiles(w *workspace) {
	if len(w.Terminals) == 0 {
		return
	}
	v := a.files[w.ID]
	if v != nil && slices.ContainsFunc(w.Terminals, func(t terminalInfo) bool { return t.ID == v.prevTerm }) {
		a.activeTerm[w.ID] = v.prevTerm
		return
	}
	a.activeTerm[w.ID] = w.Terminals[0].ID
}

// saveFilesState 把打开的文件和展开的目录记进 ui.json。
func (a *app) saveFilesState(v *filesView) {
	st := filesState{Expanded: v.tree.Expansions()}
	for _, d := range v.docs {
		if !d.virtual() {
			st.Open = append(st.Open, d.path)
		}
	}
	if v.cur != nil && !v.cur.virtual() {
		st.Current = v.cur.path
	}
	if a.state.Files == nil {
		a.state.Files = map[string]filesState{}
	}
	if len(st.Open) == 0 && len(st.Expanded) == 0 {
		delete(a.state.Files, v.wsID)
	} else {
		a.state.Files[v.wsID] = st
	}
	a.saveState()
}

// loadDirs 在后台读目录，读完放进树。
func (a *app) loadDirs(v *filesView, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	go func() {
		type result struct {
			dir     string
			entries []files.Entry
			stamp   files.Stamp
			err     error
		}
		out := make([]result, len(dirs))
		for i, d := range dirs {
			es, st, err := files.ReadDir(d)
			out[i] = result{d, es, st, err}
		}
		a.update(func() {
			for _, r := range out {
				v.tree.Set(r.dir, r.entries, r.stamp, r.err)
			}
		})
	}()
}

// toggleDir 展开或收起树上的目录。
func (a *app) toggleDir(v *filesView, dir string) {
	a.loadDirs(v, v.tree.SetExpanded(dir, !v.tree.Expanded(dir)))
	a.saveFilesState(v)
}

// refreshList 在后台重列文件清单；force 为假时清单还新鲜就不列。done 在界面线程、清单就绪后调用。
func (a *app) refreshList(v *filesView, force bool, done func()) {
	if !force && v.list != nil && time.Since(v.listAt) < listMaxAge {
		if done != nil {
			done()
		}
		return
	}
	if v.listing && done == nil {
		return
	}
	v.listing = true
	root := v.root
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		l, err := files.ListFiles(ctx, root)
		cancel()
		a.update(func() {
			v.listing = false
			if err != nil {
				a.err = "列出文件失败：" + err.Error()
				return
			}
			v.list, v.listAt = l, time.Now()
			if done != nil {
				done()
			}
		})
	}()
}

// openFile 打开文件并切过去，标签常驻；line 从 1 起，0 表示不跳行。
func (a *app) openFile(w *workspace, path string, line int, span [2]int) {
	a.openDoc(w, path, line, span, false)
}

// previewFile 是文件树里的单击：用预览标签打开，换掉上一个还没常驻的预览标签。
func (a *app) previewFile(w *workspace, path string) {
	a.openDoc(w, path, 0, [2]int{}, true)
}

func (a *app) openDoc(w *workspace, path string, line int, span [2]int, preview bool) {
	v := a.showFiles(w)
	path = filepath.Clean(path)
	var d *fileDoc
	for _, x := range v.docs {
		if x.path == path {
			d = x
		}
	}
	switch {
	case d != nil:
		// 已经开着的预览标签被正常打开（双击、跳转）就常驻
		d.transient = d.transient && preview
	case preview && slices.ContainsFunc(v.docs, (*fileDoc).isPreview):
		i := slices.IndexFunc(v.docs, (*fileDoc).isPreview)
		d = &fileDoc{path: path, transient: true}
		v.docs[i] = d
	default:
		d = &fileDoc{path: path, transient: preview}
		// 插在当前文件后面，和编辑器一样
		i := len(v.docs)
		if j := slices.Index(v.docs, v.cur); j >= 0 {
			i = j + 1
		}
		v.docs = slices.Insert(v.docs, i, d)
	}
	v.cur = d
	d.focus = true
	if line > 0 {
		d.gotoLine, d.gotoSpan = line, span
	}
	if strings.HasPrefix(path, v.root+string(filepath.Separator)) {
		a.loadDirs(v, v.tree.Reveal(path))
	}
	a.saveFilesState(v)
}

// loadDoc 在后台读文件，读完按类型放进文档。
func (a *app) loadDoc(d *fileDoc) {
	path := d.path
	go func() {
		if isImagePath(path) {
			st := files.StatFile(path)
			img, err := scaledImage(path, imageMaxPx)
			a.update(func() {
				d.base, d.seen = st, st
				if err != nil {
					d.kind, d.err = docError, err.Error()
					return
				}
				d.kind, d.img = docImage, ui.NewBitmap(img)
			})
			return
		}
		c, err := files.ReadFile(path)
		a.update(func() { a.applyContent(d, c, err) })
	}()
}

// applyContent 把读到的内容放进文档：第一次打开，或磁盘上变了、自己没有未保存的改动时重载。
func (a *app) applyContent(d *fileDoc, c files.Content, err error) {
	d.base, d.seen, d.conflict, d.deleted = c.Stamp, c.Stamp, false, false
	switch {
	case err != nil:
		d.kind, d.err = docError, readError(err)
		return
	case c.Kind == files.Binary:
		d.kind = docBinary
		return
	case c.Kind == files.TooLarge:
		d.kind = docTooLarge
		return
	case c.Kind == files.Directory:
		d.kind, d.err = docError, "这是一个目录"
		return
	}
	d.readOnly = c.ReadOnly
	if d.kind == docText && d.ed.Buffer != nil {
		d.ed.ReadOnly = c.ReadOnly
		// 重载：光标留在原来的行列上
		pos := d.ed.CursorPos()
		d.ed.Buffer.Reset(c.Text)
		snap := d.ed.Buffer.Snapshot()
		pos.Line = min(pos.Line, snap.LineCount()-1)
		pos.Col = min(pos.Col, len(snap.Line(pos.Line)))
		off := snap.Offset(pos)
		d.ed.Select(off, off)
		d.modStale = true
		return
	}
	d.kind = docText
	d.ed = editor.State{Buffer: editor.NewBuffer(c.Text), ReadOnly: c.ReadOnly, Label: d.name()}
	d.ed.Buffer.SetLanguage(editor.LanguageFor(d.path))
	d.modStale = true
	d.shown = 0
}

func readError(err error) string {
	switch {
	case os.IsNotExist(err):
		return "文件不存在"
	case os.IsPermission(err):
		return "没有权限读取这个文件"
	}
	return err.Error()
}

// pollFiles 在后台 stat 看得见的目录和打开的文件，变了的重读，结果交回界面线程。
func (a *app) pollFiles(v *filesView) {
	type dirRes struct {
		dir     string
		entries []files.Entry
		stamp   files.Stamp
		err     error
	}
	type docRes struct {
		d       *fileDoc
		stamp   files.Stamp
		content files.Content
		err     error
	}
	dirs := map[string]files.Stamp{}
	for _, d := range v.tree.Visible() {
		if st, ok := v.tree.Stamp(d); ok {
			dirs[d] = st
		}
	}
	type docReq struct {
		d     *fileDoc
		seen  files.Stamp
		image bool
	}
	var docs []docReq
	for _, d := range v.docs {
		if d.kind != docLoading && !d.virtual() {
			docs = append(docs, docReq{d, d.seen, d.kind == docImage})
		}
	}
	go func() {
		var dr []dirRes
		for dir, old := range dirs {
			if st := files.StatFile(dir); !st.Same(old) {
				es, st, err := files.ReadDir(dir)
				dr = append(dr, dirRes{dir, es, st, err})
			}
		}
		var cr []docRes
		for _, q := range docs {
			st := files.StatFile(q.d.path)
			if st.Same(q.seen) {
				continue
			}
			r := docRes{d: q.d, stamp: st}
			if !st.Missing && !q.image {
				r.content, r.err = files.ReadFile(q.d.path)
			}
			cr = append(cr, r)
		}
		a.update(func() {
			v.poll.done()
			for _, r := range dr {
				v.tree.Set(r.dir, r.entries, r.stamp, r.err)
			}
			for _, r := range cr {
				a.diskChanged(r.d, r.stamp, r.content, r.err)
			}
		})
	}()
}

// diskChanged 处理轮询发现的磁盘变化：没有未保存的改动就重载，有就标出冲突，文件没了标出已删除。
func (a *app) diskChanged(d *fileDoc, st files.Stamp, c files.Content, err error) {
	d.seen = st
	switch {
	case st.Missing:
		d.deleted = true
	case d.kind == docImage:
		d.deleted = false
		a.loadDoc(d)
	case d.isModified():
		d.deleted, d.conflict = false, true
	default:
		a.applyContent(d, c, err)
	}
}

// reloadDoc 放弃未保存的改动，按磁盘上的内容重载。
func (a *app) reloadDoc(d *fileDoc) {
	go func() {
		c, err := files.ReadFile(d.path)
		a.update(func() { a.applyContent(d, c, err) })
	}()
}

// saveDoc 保存文档；磁盘上的文件在打开后被改过时先问，force 为真时直接覆盖。成功返回真。
func (a *app) saveDoc(d *fileDoc, force bool) bool {
	if d.kind != docText || d.readOnly {
		return false
	}
	if !force && !d.base.Missing && !files.StatFile(d.path).Same(d.base) {
		a.confirm("覆盖磁盘上的修改？", d.name()+" 在你打开后被改过（可能是 agent），保存会覆盖那些修改。", "仍然保存", func() {
			a.update(func() { a.saveDoc(d, true) })
		})
		return false
	}
	snap := d.ed.Buffer.Snapshot()
	st, err := files.WriteFile(d.path, func(w io.Writer) error {
		_, err := snap.WriteTo(w)
		return err
	})
	if err != nil {
		a.err = "保存 " + d.name() + " 失败：" + err.Error()
		return false
	}
	// 写入在界面线程同步完成，期间不会有新的输入，缓冲区就是写下去的内容
	d.ed.Buffer.MarkSaved()
	d.base, d.seen, d.conflict, d.deleted, d.modStale = st, st, false, false, true
	return true
}

// closeDoc 关闭文档，有未保存的改动时先问。
func (a *app) closeDoc(v *filesView, d *fileDoc) {
	if d.isModified() {
		a.askUnsaved([]*fileDoc{d}, func() { a.dropDoc(v, d) })
		return
	}
	a.dropDoc(v, d)
}

func (a *app) dropDoc(v *filesView, d *fileDoc) {
	i := slices.Index(v.docs, d)
	if i < 0 {
		return
	}
	v.docs = slices.Delete(v.docs, i, i+1)
	if v.cur == d {
		v.cur = nil
		if len(v.docs) > 0 {
			v.cur = v.docs[min(i, len(v.docs)-1)]
			v.cur.focus = true
		}
	}
	a.saveFilesState(v)
	// 最后一个文件关了，文件标签也就不显示了，切回之前的终端
	if w := a.workspace(v.wsID); len(v.docs) == 0 && a.filesActive(w) {
		a.leaveFiles(w)
	}
}

// dirtyDocs 是所有工作区里有未保存改动的文档。
func (a *app) dirtyDocs() []*fileDoc {
	var out []*fileDoc
	for _, v := range a.files {
		for _, d := range v.docs {
			if d.isModified() {
				out = append(out, d)
			}
		}
	}
	return out
}

// askUnsaved 问要不要保存 docs 里的改动：保存（全部成功才继续）、不保存、取消。then 在继续时调用。
func (a *app) askUnsaved(docs []*fileDoc, then func()) {
	a.unsaved = &unsavedAsk{docs: docs, then: then}
}

type unsavedAsk struct {
	docs []*fileDoc
	then func()
}

// unsavedDialog 画 askUnsaved 的对话框。
func (a *app) unsavedDialog(c *ui.Context) {
	t := c.Theme()
	open := a.unsaved != nil
	ui.Modal(c, &open, func() {
		u := a.unsaved
		if u == nil {
			return
		}
		ui.Column(c).Width(360).Gap(10).Children(func() {
			title := "保存对 " + u.docs[0].name() + " 的修改？"
			if len(u.docs) > 1 {
				title = "有 " + strconv.Itoa(len(u.docs)) + " 个文件的修改还没保存"
			}
			titleText(c, title, fsCardTitle)
			body := "不保存的话，这些修改会丢失。"
			if len(u.docs) > 1 {
				var names []string
				for _, d := range u.docs {
					names = append(names, d.name())
				}
				body = strings.Join(names, "、") + "。不保存的话，这些修改会丢失。"
			}
			ui.Text(c, body).FontSize(13).TextColor(t.TextMuted).LineHeight(1.45)
			ui.Row(c).Gap(8).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "不保存")).Clicked() {
					a.unsaved = nil
					u.then()
				}
				ui.Spacer(c)
				if pressable(textButton(c, "取消")).Clicked() {
					a.unsaved = nil
					a.quitting.Store(false)
				}
				if pressable(accentButton(c, "保存")).Clicked() {
					a.unsaved = nil
					ok := true
					for _, d := range u.docs {
						ok = a.saveDoc(d, false) && ok
					}
					if ok {
						u.then()
					} else {
						a.quitting.Store(false)
					}
				}
			})
		})
	})
	if !open && a.unsaved != nil {
		// 点外面或按 Esc
		a.unsaved = nil
		a.quitting.Store(false)
	}
}

// update 把 fn 交给界面线程执行。测试里没有窗口，先排着，下一帧开头执行（runPending）。
func (a *app) update(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
		return
	}
	a.pendingMu.Lock()
	a.pending = append(a.pending, fn)
	a.pendingMu.Unlock()
}

func (a *app) runPending() {
	a.pendingMu.Lock()
	fns := a.pending
	a.pending = nil
	a.pendingMu.Unlock()
	for _, fn := range fns {
		fn()
	}
}

// watchClose 拦住有未保存改动时的关窗口和退出（退出会先关窗口，同一个钩子就够了）。
func (a *app) watchClose() {
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quitting.Store(true) })
	a.win.OnClose(func(e *mygo.CloseEvent) {
		if a.closeOK.Load() || a.dirtyCount.Load() == 0 {
			return
		}
		e.PreventDefault()
		a.win.Update(func() {
			docs := a.dirtyDocs()
			if len(docs) == 0 {
				a.finishClose()
				return
			}
			a.askUnsaved(docs, a.finishClose)
		})
	})
}

// finishClose 在用户处理完未保存的改动后，接着关窗口或退出。
func (a *app) finishClose() {
	a.closeOK.Store(true)
	if a.quitting.Load() {
		mygo.App.Quit()
		return
	}
	a.win.Close()
}

// workspaceOf 找包含 path 的工作区：优先当前选中的，否则取目录最长的。
func (a *app) workspaceOf(path string) *workspace {
	if w := a.workspace(a.selected); w != nil && inDir(path, w.Path) {
		return w
	}
	var best *workspace
	for i := range a.workspaces {
		w := &a.workspaces[i]
		if inDir(path, w.Path) && (best == nil || len(w.Path) > len(best.Path)) {
			best = w
		}
	}
	return best
}

func inDir(path, dir string) bool {
	dir = filepath.Clean(dir)
	return strings.HasPrefix(filepath.Clean(path), dir+string(filepath.Separator))
}

// startSearch 按 v.search.query 检索，delay 后开始（输入时防抖）。
func (a *app) startSearch(v *filesView, delay time.Duration) {
	s := &v.search
	s.gen++
	gen := s.gen
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	if strings.TrimSpace(s.query.Text) == "" {
		s.hits, s.rows, s.truncated, s.err, s.running = nil, nil, false, "", false
		return
	}
	if _, err := s.query.Compile(); err != nil {
		s.hits, s.rows, s.running, s.err = nil, nil, false, "正则写得不对："+err.Error()
		return
	}
	s.running, s.err = true, ""
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	q := s.query
	time.AfterFunc(delay, func() {
		a.update(func() {
			if s.gen != gen {
				return
			}
			a.refreshList(v, false, func() {
				if s.gen != gen {
					return
				}
				// 有未保存改动的文件搜编辑器里的内容
				overlay := map[string]string{}
				for _, d := range v.docs {
					if d.isModified() {
						if rel, err := filepath.Rel(v.root, d.path); err == nil {
							overlay[rel] = d.ed.Buffer.String()
						}
					}
				}
				s.hits, s.rows, s.truncated = nil, nil, false
				l := v.list
				go func() {
					trunc, err := files.Search(ctx, l, q, overlay, func(hs []files.Hit) {
						a.update(func() {
							if s.gen == gen {
								s.hits = append(s.hits, hs...)
								s.rows = searchRows(s.hits)
							}
						})
					})
					a.update(func() {
						if s.gen != gen {
							return
						}
						s.running, s.truncated = false, trunc
						if err != nil && ctx.Err() == nil {
							s.err = err.Error()
						}
					})
				}()
			})
		})
	})
}

// searchRows 把命中按文件分组成列表的行：每个文件先一行文件名。
func searchRows(hits []files.Hit) []searchRow {
	rows := make([]searchRow, 0, len(hits)+len(hits)/4)
	head := -1
	for i, h := range hits {
		if i == 0 || hits[i-1].Path != h.Path {
			rows = append(rows, searchRow{hit: i, header: true})
			head = len(rows) - 1
		}
		rows[head].count++
		rows = append(rows, searchRow{hit: i})
	}
	return rows
}

// pruneFiles 去掉已经不在的工作区的文件标签；有未保存改动的留着，免得改动丢掉。
func (a *app) pruneFiles() {
	for id, v := range a.files {
		if a.workspace(id) != nil {
			continue
		}
		if !slices.ContainsFunc(v.docs, (*fileDoc).isModified) {
			delete(a.files, id)
		}
	}
}
