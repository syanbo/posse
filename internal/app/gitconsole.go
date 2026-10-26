package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/gitx"
)

// Git 操作记录：照 WebStorm Git 工具窗口的 Console，列出日志浮层所在项目里界面执行过的 Git 操作（提交、推送、储藏……），
// 每个操作下面是它调用的 git 命令、输出和报错。界面的记录来自 gitx.Trace，命令完成后追加到
// $POSSE_HOME/git-console.jsonl，窗口打开时读回；Posse 终端里改动仓库的 git 命令由常驻进程经 trace2 收集
// （internal/daemon/gittrace.go），轮询时按序号取新的，按时间排进同一个列表，只有命令和退出码、没有输出。
// 所有项目的命令存在一起，显示时按命令所在目录归到项目（runWorkspace），每个项目各留 gitConsoleMax 条。

// gitConsoleMax 是每个项目最多留多少条命令，超过丢掉这个项目最早的：一个项目命令多不挤掉别的项目的。
const gitConsoleMax = 500

// traceGit 读回上次留下的记录，再让 gitx 把命令报给界面线程。
func (a *app) traceGit() {
	a.loadGitRuns()
	gitx.Trace = func(r gitx.Run) { a.update(func() { a.addGitRun(r) }) }
}

func gitConsolePath(home string) string { return filepath.Join(home, "git-console.jsonl") }

// loadGitRuns 读回界面上次的记录，gitx 的编号接在后面。这时还不知道各命令属于哪个项目，
// 拿到工作区列表后才按项目限条数（trimGitRuns）。
func (a *app) loadGitRuns() {
	if a.home == "" {
		return
	}
	b, err := os.ReadFile(gitConsolePath(a.home))
	if err != nil {
		return
	}
	var runs []gitx.Run
	var maxID, maxOp int64
	n := 0
	for _, line := range bytes.Split(b, []byte("\n")) {
		var r gitx.Run
		if json.Unmarshal(line, &r) != nil || r.ID <= 0 {
			continue
		}
		n++
		maxID, maxOp = max(maxID, r.ID), max(maxOp, r.OpID)
		if r.Start.Before(a.state.GitConsoleCleared) {
			continue
		}
		runs = append(runs, r)
	}
	gitx.Seed(maxID, maxOp)
	a.gitRuns, a.consoleLines = runs, n
}

// trimGitRuns 每个项目只留最近 gitConsoleMax 条，对不上工作区的按目录算。文件攒到留着的两倍
// （至少 2 倍上限）时按留着的重写。
func (a *app) trimGitRuns() {
	count := map[string]int{}
	keep := make([]bool, len(a.gitRuns))
	n := 0
	for i := len(a.gitRuns) - 1; i >= 0; i-- {
		key := "dir:" + a.gitRuns[i].Dir
		if w := a.runWorkspace(a.gitRuns[i].Dir); w != nil {
			key = w.ProjectID
		}
		if count[key]++; count[key] <= gitConsoleMax {
			keep[i] = true
			n++
		}
	}
	if n < len(a.gitRuns) {
		out := make([]gitx.Run, 0, n)
		for i, r := range a.gitRuns {
			if keep[i] {
				out = append(out, r)
			}
		}
		a.gitRuns = out
	}
	// 文件里只有界面自己的、做完的命令；终端的由常驻进程存
	saved := 0
	for _, r := range a.gitRuns {
		if r.ID > 0 && r.Done {
			saved++
		}
	}
	if a.consoleLines < 2*max(saved, gitConsoleMax) {
		return
	}
	var buf bytes.Buffer
	for _, r := range a.gitRuns {
		if r.ID > 0 && r.Done {
			line, _ := json.Marshal(r)
			buf.Write(append(line, '\n'))
		}
	}
	a.consoleLines = saved
	a.writeConsole(consoleWrite{data: buf.Bytes(), rewrite: true})
}

// consoleWrite 是交给写 git-console.jsonl 的协程的一件事：追加一行，或者整个重写。
type consoleWrite struct {
	data    []byte
	rewrite bool
}

// saveGitRun 把完成的命令追加到文件。
func (a *app) saveGitRun(line []byte) {
	a.consoleLines++
	a.writeConsole(consoleWrite{data: append(line, '\n')})
}

// writeConsole 交给写文件的协程，追加和重写按顺序做。写不过来就丢，记录不能卡住界面。
func (a *app) writeConsole(w consoleWrite) {
	if a.home == "" {
		return
	}
	a.consoleOnce.Do(func() {
		a.consoleQ = make(chan consoleWrite, 256)
		go func(path string, q <-chan consoleWrite) {
			for w := range q {
				if w.rewrite {
					os.WriteFile(path, w.data, 0o600)
					continue
				}
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
				if err != nil {
					continue
				}
				f.Write(w.data)
				f.Close()
			}
		}(gitConsolePath(a.home), a.consoleQ)
	})
	select {
	case a.consoleQ <- w:
	default:
	}
}

// termGitRun 是常驻进程记下的一条终端 git 命令（git.trace）。
type termGitRun struct {
	Seq                  int64
	Terminal, Agent, Dir string
	Args                 []string
	Start                time.Time
	TookMs               int64
	Code                 int
}

// addTermRuns 把终端里的命令按时间排进记录。编号取负的序号，和界面的不撞；
// initial 是窗口打开后第一次取的历史，里面失败的不再标红点。
func (a *app) addTermRuns(recs []termGitRun, initial bool) {
	for _, x := range recs {
		if x.Start.Before(a.state.GitConsoleCleared) {
			continue
		}
		r := gitx.Run{ID: -x.Seq, Op: "终端", Dir: x.Dir, Args: x.Args, Start: x.Start,
			Took: time.Duration(x.TookMs) * time.Millisecond, Done: true}
		if x.Agent != "" {
			r.Op = agentTitle(x.Agent) + " 终端"
		}
		switch {
		case x.Code == -1:
			r.Err = "没有正常结束"
		case x.Code != 0:
			r.Err = fmt.Sprintf("退出码 %d", x.Code)
		}
		if slices.ContainsFunc(a.gitRuns, func(o gitx.Run) bool { return o.ID == r.ID }) {
			continue
		}
		if r.Err != "" && !initial {
			a.flagConsoleErr(r.Dir)
		}
		i := len(a.gitRuns)
		for i > 0 && a.gitRuns[i-1].Start.After(r.Start) {
			i--
		}
		a.gitRuns = slices.Insert(a.gitRuns, i, r)
	}
	a.trimGitRuns()
}

// flagConsoleErr 记下 dir 里有没看过的失败命令。
func (a *app) flagConsoleErr(dir string) {
	if a.consoleErr == nil {
		a.consoleErr = map[string]bool{}
	}
	a.consoleErr[dir] = true
}

// addGitRun 记下一条命令：同一个 ID 的结束记录换掉开始时那条。失败的命令在记录看不见时给标签标个红点。
func (a *app) addGitRun(r gitx.Run) {
	if r.Err != "" {
		a.flagConsoleErr(r.Dir)
	}
	if r.Done {
		line, _ := json.Marshal(r)
		a.saveGitRun(line)
	}
	for i := len(a.gitRuns) - 1; i >= 0 && i >= len(a.gitRuns)-50; i-- {
		if a.gitRuns[i].ID == r.ID {
			a.gitRuns[i] = r
			return
		}
	}
	a.gitRuns = append(a.gitRuns, r)
	a.trimGitRuns()
}

// gitOpRecord 是记录里的一个操作和它的命令；不属于任何操作的命令（后台读取失败等）各自算一个。
type gitOpRecord struct {
	name string
	runs []gitx.Run
}

func (o gitOpRecord) failed() bool {
	for _, r := range o.runs {
		if r.Err != "" {
			return true
		}
	}
	return false
}

func (o gitOpRecord) canceled() bool {
	return slices.ContainsFunc(o.runs, func(r gitx.Run) bool { return len(r.Args) == 0 && r.Err == "已取消" })
}

func (o gitOpRecord) running() bool {
	for _, r := range o.runs {
		if !r.Done {
			return true
		}
	}
	return false
}

func groupGitRuns(runs []gitx.Run) []gitOpRecord {
	var ops []gitOpRecord
	at := map[int64]int{}
	for _, r := range runs {
		if r.OpID != 0 {
			if i, ok := at[r.OpID]; ok {
				ops[i].runs = append(ops[i].runs, r)
				continue
			}
			at[r.OpID] = len(ops)
		}
		name := r.Op
		if name == "" {
			name = "后台"
		}
		ops = append(ops, gitOpRecord{name: name, runs: []gitx.Run{r}})
	}
	return ops
}

// runWorkspace 是命令所在的工作区：目录在它下面的里面最深的那个（项目目录里可能套着别的项目）；对不上时是 nil。
// 终端里的命令目录来自 trace2，是解析过符号链接的，所以也和工作区的真实路径比。按目录记下结果，每帧不用重找。
func (a *app) runWorkspace(dir string) *workspace {
	if id, ok := a.runWS[dir]; ok {
		return a.workspace(id)
	}
	under := func(root string) bool {
		return root != "" && (dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)))
	}
	var best *workspace
	depth := 0
	for i := range a.workspaces {
		w := &a.workspaces[i]
		for _, root := range []string{w.Path, a.realPath(w.Path)} {
			if under(root) && len(root) > depth {
				best, depth = w, len(root)
			}
		}
	}
	if a.runWS == nil {
		a.runWS = map[string]string{}
	}
	a.runWS[dir] = ""
	if best != nil {
		a.runWS[dir] = best.ID
	}
	return best
}

// realPath 是 path 解析过符号链接的样子，解析不了就是它自己。
func (a *app) realPath(path string) string {
	if p, ok := a.realPaths[path]; ok {
		return p
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	if a.realPaths == nil {
		a.realPaths = map[string]string{}
	}
	a.realPaths[path] = real
	return real
}

// projectRuns 是项目里的命令：目录归到这个项目、晚于这个项目上次清空的。
func (a *app) projectRuns(projectID string) []gitx.Run {
	cleared := a.state.GitConsoleClearedAt[projectID]
	var out []gitx.Run
	for _, r := range a.gitRuns {
		if w := a.runWorkspace(r.Dir); w != nil && w.ProjectID == projectID && !r.Start.Before(cleared) {
			out = append(out, r)
		}
	}
	return out
}

// projectConsoleErr 报告项目里有没有没看过的失败命令；clear 时顺带标成看过。
func (a *app) projectConsoleErr(projectID string, clear bool) bool {
	found := false
	for dir := range a.consoleErr {
		if w := a.runWorkspace(dir); w != nil && w.ProjectID == projectID {
			found = true
			if clear {
				delete(a.consoleErr, dir)
			}
		}
	}
	return found
}

// runWhere 是项目里的哪个工作区：主工作区不写。
func (a *app) runWhere(dir string) string {
	if w := a.runWorkspace(dir); w != nil && w.Kind != "main" {
		return w.Name
	}
	return ""
}

// consoleTab 是日志浮层顶上的"操作记录"标签；有没看过的失败命令时带个红点。
func (a *app) consoleTab(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	label := "操作记录"
	row := ui.Row(c.Key("console-tab")).Height(sz(26)).Padding(0, 12).Gap(6).Radius(6).AlignItems(ui.Center).Shrink(0).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, v.console)
	row.Children(func() {
		ui.Icon(c, iconOpLog).FontSize(fsBody).TextColor(t.Accent).Shrink(0)
		tabText(c, ui.Text(c, label).FontSize(fsSmall).Shrink(0), label, v.console)
		if !v.console && a.projectConsoleErr(w.ProjectID, false) {
			ui.Box(c).Size(6, 6).Radius(3).Background(t.Danger).Shrink(0).Label("有失败的 git 命令").Tooltip("有失败的 git 命令")
		}
	})
	if row.Clicked() {
		v.console = true
	}
}

// gitConsole 是工作区所在项目的操作记录：最新的在最下面，有新命令时滚到底。
func (a *app) gitConsole(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	a.projectConsoleErr(w.ProjectID, true)
	runs := a.projectRuns(w.ProjectID)
	ops := groupGitRuns(runs)
	ui.Row(c).Height(sz(30)).Shrink(0).Padding(0, 8, 0, 16).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, w.Project+" 各工作区里 Posse 执行的 Git 操作和它们调用的 git 命令，以及 Posse 终端里改动仓库的 git 命令").FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		// 只清这个项目的：记下时间，文件里的记录留给别的项目
		if len(ops) > 0 && pressable(iconButtonSized(c, iconTrash, "清空", compactBox(), smallIcon())).Clicked() {
			runs, ops = nil, nil
			if a.state.GitConsoleClearedAt == nil {
				a.state.GitConsoleClearedAt = map[string]time.Time{}
			}
			a.state.GitConsoleClearedAt[w.ProjectID] = time.Now()
			a.saveState()
		}
	})
	if len(ops) == 0 {
		ui.Text(c, "还没有执行过 Git 操作").FontSize(fsSmall).TextColor(t.TextMuted).Padding(12, 16)
		return
	}
	v.consoleList.Key = func(i int) any { return ops[i].runs[0].ID }
	ui.List(c, &v.consoleList, len(ops), func(i int) { a.gitOpRow(c, ops[i]) }).Grow(1).Padding(0, 0, 8, 0)
	if last := runs[len(runs)-1]; last.ID != v.consoleLast || last.Done != v.consoleDone {
		v.consoleLast, v.consoleDone = last.ID, last.Done
		v.consoleList.ScrollIntoView(len(ops) - 1)
	}
}

// gitOpRow 是一个操作：时间、操作名、项目，右边是结果；下面靠左列出它的 git 命令。
func (a *app) gitOpRow(c *ui.Context, op gitOpRecord) {
	t := c.Theme()
	first, last := op.runs[0], op.runs[len(op.runs)-1]
	ui.Column(c.Key(first.ID)).Padding(6, 16, 8, 16).Gap(4).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, first.Start.Format("15:04:05")).FontSize(fsSmall).Font(monoFont).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			name := ui.Text(c, op.name).FontSize(fsBody).FontWeight(fwMedium).Shrink(0)
			if op.failed() {
				name.TextColor(t.Danger)
			}
			if where := a.runWhere(first.Dir); where != "" {
				ui.Text(c, where).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1)
			}
			ui.Spacer(c)
			switch {
			case op.running():
				ui.Text(c, "进行中…").FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			case op.canceled():
				ui.Text(c, "已取消").FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			case op.failed():
				ui.Text(c, "失败").FontSize(fsSmall).TextColor(t.Danger).Shrink(0)
			default:
				ui.Text(c, took(last.Start.Add(last.Took).Sub(first.Start))).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
			}
		})
		for _, r := range op.runs {
			if len(r.Args) > 0 {
				gitRunRow(c, r)
				continue
			}
			// 操作的结果：和某条命令的报错一样时不再写一遍
			if !slices.ContainsFunc(op.runs, func(o gitx.Run) bool { return len(o.Args) > 0 && o.Err != "" && strings.Contains(r.Err, o.Err) }) {
				ui.Text(c, r.Err).FontSize(fsSmall).LineHeight(1.4).TextColor(t.Danger).Selectable()
			}
		}
	})
}

// gitRunRow 是一条 git 命令和它的输出，失败时是红的；右键复制命令或输出。
func gitRunRow(c *ui.Context, r gitx.Run) {
	t := c.Theme()
	cmd := "git " + quoteArgs(r.Args)
	// 可选的是整列：文字自己可选时右键弹的是它的编辑菜单，盖掉下面的"复制命令"
	col := ui.Column(c.Key(r.ID)).Gap(2).Shrink(0).Selectable()
	col.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			line := ui.Text(c, cmd).FontSize(fsSmall).Font(monoFont).Shrink(1)
			if r.Err != "" {
				line.TextColor(t.Danger)
			}
			ui.Spacer(c)
			if r.Done && r.Took >= 100*time.Millisecond {
				ui.Text(c, took(r.Took)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
			}
		})
		out := r.Out
		if r.Err != "" && !strings.Contains(out, r.Err) {
			out = strings.TrimLeft(out+"\n"+r.Err, "\n")
		}
		if out != "" {
			text := ui.Text(c, out).FontSize(fsSmall).Font(monoFont).LineHeight(1.4).Padding(0, 0, 0, 12)
			if r.Err != "" {
				text.TextColor(t.Danger)
			} else {
				text.TextColor(t.TextMuted)
			}
		}
	})
	contextMenu(c, col, func(m *popMenu) {
		if m.Item("复制命令").Chosen() {
			c.WriteClipboard(cmd)
		}
		if out := strings.TrimSpace(r.Out + "\n" + r.Err); out != "" && m.Item("复制输出").Chosen() {
			c.WriteClipboard(out)
		}
	})
}

func took(d time.Duration) string { return fmt.Sprintf("%.1fs", d.Seconds()) }

// quoteArgs 把参数拼成能在 shell 里照抄的命令行。
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = shellQuote(s)
	}
	return strings.Join(q, " ")
}

// pollGitTrace 在轮询里调用：常驻进程的序号变了就取新的终端 git 命令。序号变小说明记录文件没了，从头取。
func (a *app) pollGitTrace(ctx context.Context, seq int64) {
	initial := !a.traceLoaded
	a.traceLoaded = true
	if seq == a.traceSeq {
		return
	}
	after := a.traceSeq
	if seq < after {
		after = 0
	}
	var res struct {
		Records []termGitRun
		Seq     int64
	}
	if a.c.Call(ctx, "git.trace", map[string]any{"after": after}, &res) != nil {
		return
	}
	a.traceSeq = res.Seq
	a.update(func() { a.addTermRuns(res.Records, initial) })
}
