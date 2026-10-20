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

	"hive/internal/gitx"
)

// Git 操作记录：照 WebStorm Git 工具窗口的 Console，列出所有项目里界面执行过的 Git 操作（提交、推送、储藏……），
// 每个操作下面是它调用的 git 命令、输出和报错。界面的记录来自 gitx.Trace，命令完成后追加到
// $HIVE_HOME/git-console.jsonl，窗口打开时读回；Hive 终端里改动仓库的 git 命令由常驻进程经 trace2 收集
// （internal/daemon/gittrace.go），轮询时按序号取新的，按时间排进同一个列表，只有命令和退出码、没有输出。

// gitConsoleMax 是最多留多少条命令，超过丢掉最早的。
const gitConsoleMax = 1000

// traceGit 读回上次留下的记录，再让 gitx 把命令报给界面线程。
func (a *app) traceGit() {
	a.loadGitRuns()
	gitx.Trace = func(r gitx.Run) { a.update(func() { a.addGitRun(r) }) }
}

func gitConsolePath(home string) string { return filepath.Join(home, "git-console.jsonl") }

// loadGitRuns 读回界面上次的记录，gitx 的编号接在后面；文件攒到上限的两倍时只留最近的重写。
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
	if len(runs) > gitConsoleMax {
		runs = runs[len(runs)-gitConsoleMax:]
	}
	a.gitRuns = runs
	if n > 2*gitConsoleMax {
		var buf bytes.Buffer
		for _, r := range runs {
			line, _ := json.Marshal(r)
			buf.Write(append(line, '\n'))
		}
		os.WriteFile(gitConsolePath(a.home), buf.Bytes(), 0o600)
	}
}

// saveGitRun 把完成的命令交给写文件的协程；nil 表示清空文件。写不过来就丢，记录不能卡住界面。
func (a *app) saveGitRun(line []byte) {
	if a.home == "" {
		return
	}
	a.consoleOnce.Do(func() {
		a.consoleQ = make(chan []byte, 256)
		go func(path string, q <-chan []byte) {
			for line := range q {
				if line == nil {
					os.WriteFile(path, nil, 0o600)
					continue
				}
				f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
				if err != nil {
					continue
				}
				f.Write(append(line, '\n'))
				f.Close()
			}
		}(gitConsolePath(a.home), a.consoleQ)
	})
	select {
	case a.consoleQ <- line:
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
			a.consoleErr = true
		}
		i := len(a.gitRuns)
		for i > 0 && a.gitRuns[i-1].Start.After(r.Start) {
			i--
		}
		a.gitRuns = slices.Insert(a.gitRuns, i, r)
	}
	if len(a.gitRuns) > gitConsoleMax {
		a.gitRuns = append(a.gitRuns[:0], a.gitRuns[len(a.gitRuns)-gitConsoleMax:]...)
	}
}

// addGitRun 记下一条命令：同一个 ID 的结束记录换掉开始时那条。失败的命令在记录看不见时给标签标个红点。
func (a *app) addGitRun(r gitx.Run) {
	if r.Err != "" {
		a.consoleErr = true
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
	if len(a.gitRuns) > gitConsoleMax {
		a.gitRuns = append(a.gitRuns[:0], a.gitRuns[len(a.gitRuns)-gitConsoleMax:]...)
	}
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

// runWhere 是命令所在的项目和工作区（主工作区只写项目名）；对不上时是目录名。
func (a *app) runWhere(dir string) string {
	for _, w := range a.workspaces {
		if dir == w.Path || strings.HasPrefix(dir, w.Path+string(filepath.Separator)) {
			if w.Kind == "main" || w.Name == "" {
				return w.Project
			}
			return w.Project + "/" + w.Name
		}
	}
	return filepath.Base(dir)
}

// consoleTab 是日志浮层顶上的"操作记录"标签；有没看过的失败命令时带个红点。
func (a *app) consoleTab(c *ui.Context, v *filesView) {
	t := c.Theme()
	label := "操作记录"
	row := ui.Row(c.Key("console-tab")).Height(26).Padding(0, 10).Gap(6).Radius(6).AlignItems(ui.Center).Shrink(0).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, v.console)
	row.Children(func() {
		ui.Icon(c, iconOpLog).FontSize(13).TextColor(t.Accent).Shrink(0)
		tabText(c, ui.Text(c, label).FontSize(12).Shrink(0), v.console)
		if a.consoleErr && !v.console {
			ui.Box(c).Size(6, 6).Radius(3).Background(t.Danger).Shrink(0).Label("有失败的 git 命令").Tooltip("有失败的 git 命令")
		}
	})
	if row.Clicked() {
		v.console = true
	}
}

// gitConsole 是操作记录的内容：最新的在最下面，有新命令时滚到底。
func (a *app) gitConsole(c *ui.Context, v *filesView) {
	t := c.Theme()
	a.consoleErr = false
	ops := groupGitRuns(a.gitRuns)
	ui.Row(c).Height(30).Shrink(0).Padding(0, 8, 0, 16).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "所有项目里 Hive 执行的 Git 操作和它们调用的 git 命令，以及 Hive 终端里改动仓库的 git 命令").FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if len(ops) > 0 && pressable(iconButtonSized(c, iconTrash, "清空", 26, 15)).Clicked() {
			a.gitRuns = nil
			ops = nil
			a.state.GitConsoleCleared = time.Now()
			a.saveState()
			a.saveGitRun(nil)
		}
	})
	if len(ops) == 0 {
		ui.Text(c, "还没有执行过 Git 操作").FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		return
	}
	v.consoleList.Key = func(i int) any { return ops[i].runs[0].ID }
	ui.List(c, &v.consoleList, len(ops), func(i int) { a.gitOpRow(c, ops[i]) }).Grow(1).Padding(0, 0, 8, 0)
	if last := a.gitRuns[len(a.gitRuns)-1]; last.ID != v.consoleLast || last.Done != v.consoleDone {
		v.consoleLast, v.consoleDone = last.ID, last.Done
		v.consoleList.ScrollIntoView(len(ops) - 1)
	}
}

// gitOpRow 是一个操作：时间、操作名、项目，右边是结果；下面靠左列出它的 git 命令。
func (a *app) gitOpRow(c *ui.Context, op gitOpRecord) {
	t := c.Theme()
	first, last := op.runs[0], op.runs[len(op.runs)-1]
	ui.Column(c.Key(first.ID)).Padding(6, 16, 8, 16).Gap(3).Shrink(0).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, first.Start.Format("15:04:05")).FontSize(11).Font(monoFont).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			name := ui.Text(c, op.name).FontSize(12.5).FontWeight(500).Shrink(0)
			if op.failed() {
				name.TextColor(t.Danger)
			}
			ui.Text(c, a.runWhere(first.Dir)).FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1)
			ui.Spacer(c)
			switch {
			case op.running():
				ui.Text(c, "进行中…").FontSize(11).TextColor(t.TextMuted).Shrink(0)
			case op.canceled():
				ui.Text(c, "已取消").FontSize(11).TextColor(t.TextMuted).Shrink(0)
			case op.failed():
				ui.Text(c, "失败").FontSize(11).TextColor(t.Danger).Shrink(0)
			default:
				ui.Text(c, took(last.Start.Add(last.Took).Sub(first.Start))).FontSize(11).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
			}
		})
		for _, r := range op.runs {
			if len(r.Args) > 0 {
				gitRunRow(c, r)
				continue
			}
			// 操作的结果：和某条命令的报错一样时不再写一遍
			if !slices.ContainsFunc(op.runs, func(o gitx.Run) bool { return len(o.Args) > 0 && o.Err != "" && strings.Contains(r.Err, o.Err) }) {
				ui.Text(c, r.Err).FontSize(11.5).LineHeight(1.4).TextColor(t.Danger).Selectable()
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
			line := ui.Text(c, cmd).FontSize(11.5).Font(monoFont).Shrink(1)
			if r.Err != "" {
				line.TextColor(t.Danger)
			}
			ui.Spacer(c)
			if r.Done && r.Took >= 100*time.Millisecond {
				ui.Text(c, took(r.Took)).FontSize(11).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
			}
		})
		out := r.Out
		if r.Err != "" && !strings.Contains(out, r.Err) {
			out = strings.TrimLeft(out+"\n"+r.Err, "\n")
		}
		if out != "" {
			text := ui.Text(c, out).FontSize(11).Font(monoFont).LineHeight(1.4).Padding(0, 0, 0, 12)
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
