package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// Git 操作记录：照 WebStorm Git 工具窗口的 Console，列出所有项目里界面执行过的 Git 操作（提交、推送、储藏……），
// 每个操作下面是它调用的 git 命令、输出和报错。记录来自 gitx.Trace，只在窗口进程内存里，不写盘。

// gitConsoleMax 是最多留多少条命令，超过丢掉最早的。
const gitConsoleMax = 1000

// traceGit 让 gitx 把命令报给界面线程。
func (a *app) traceGit() {
	gitx.Trace = func(r gitx.Run) { a.update(func() { a.addGitRun(r) }) }
}

// addGitRun 记下一条命令：同一个 ID 的结束记录换掉开始时那条。失败的命令在记录看不见时给标签标个红点。
func (a *app) addGitRun(r gitx.Run) {
	if r.Err != "" {
		a.consoleErr = true
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
		ui.Icon(c, iconTerminal).FontSize(13).TextColor(t.Accent).Shrink(0)
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
		ui.Text(c, "所有项目里 Hive 执行的 Git 操作，和每个操作调用的 git 命令").FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if len(ops) > 0 && pressable(iconButtonSized(c, iconTrash, "清空", 26, 15)).Clicked() {
			a.gitRuns = nil
			ops = nil
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

// gitOpRow 是一个操作：时间、操作名、项目，右边是结果；下面缩进列出它的 git 命令。
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
			case op.failed():
				ui.Text(c, "失败").FontSize(11).TextColor(t.Danger).Shrink(0)
			default:
				ui.Text(c, took(last.Start.Add(last.Took).Sub(first.Start))).FontSize(11).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
			}
		})
		for _, r := range op.runs {
			gitRunRow(c, r)
		}
	})
}

// gitRunRow 是一条 git 命令和它的输出，失败时是红的；右键复制命令或输出。
func gitRunRow(c *ui.Context, r gitx.Run) {
	t := c.Theme()
	cmd := "git " + quoteArgs(r.Args)
	col := ui.Column(c.Key(r.ID)).Padding(0, 0, 0, 64).Gap(2).Shrink(0)
	col.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			line := ui.Text(c, cmd).FontSize(11.5).Font(monoFont).Shrink(1).Selectable()
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
			text := ui.Text(c, out).FontSize(11).Font(monoFont).LineHeight(1.4).Padding(0, 0, 0, 12).Selectable()
			if r.Err != "" {
				text.TextColor(t.Danger)
			} else {
				text.TextColor(t.TextMuted)
			}
		}
	})
	col.ContextMenu(func(m *ui.Menu) {
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
