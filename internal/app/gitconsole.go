package app

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/gitx"
)

// Git 控制台：照 WebStorm Git 工具窗口的 Console，列出界面执行过的 git 命令、输出和报错。
// 记录来自 gitx.Trace（只记会改动仓库的命令和失败的命令），只在窗口进程内存里，按项目筛选，不写盘。

// gitConsoleMax 是最多留多少条，超过丢掉最早的。
const gitConsoleMax = 500

// traceGit 让 gitx 把命令报给界面线程。
func (a *app) traceGit() {
	gitx.Trace = func(r gitx.Run) { a.update(func() { a.addGitRun(r) }) }
}

// addGitRun 记下一条命令：同一个 ID 的结束记录换掉开始时那条。失败的命令在控制台看不见时给标签标个红点。
func (a *app) addGitRun(r gitx.Run) {
	for i := len(a.gitRuns) - 1; i >= 0 && i >= len(a.gitRuns)-50; i-- {
		if a.gitRuns[i].ID == r.ID {
			a.gitRuns[i] = r
			a.noteGitError(r)
			return
		}
	}
	a.gitRuns = append(a.gitRuns, r)
	if len(a.gitRuns) > gitConsoleMax {
		a.gitRuns = append(a.gitRuns[:0], a.gitRuns[len(a.gitRuns)-gitConsoleMax:]...)
	}
	a.noteGitError(r)
}

func (a *app) noteGitError(r gitx.Run) {
	if r.Err == "" {
		return
	}
	if p := a.runProject(r); p != "" {
		if a.consoleErr == nil {
			a.consoleErr = map[string]bool{}
		}
		a.consoleErr[p] = true
	}
}

// runProject 是命令所在目录属于的项目：目录是某个工作区的路径或在它下面。
func (a *app) runProject(r gitx.Run) string {
	for _, w := range a.workspaces {
		if r.Dir == w.Path || strings.HasPrefix(r.Dir, w.Path+string(filepath.Separator)) {
			return w.ProjectID
		}
	}
	return ""
}

// consoleTab 是日志浮层顶上的"控制台"标签；有没看过的失败命令时带个红点。
func (a *app) consoleTab(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	label := "控制台"
	row := ui.Row(c.Key("console-tab")).Height(26).Padding(0, 10).Gap(6).Radius(6).AlignItems(ui.Center).Shrink(0).
		Label(label).Role(ui.RoleButton)
	selectedBg(c, row, v.console)
	row.Children(func() {
		ui.Icon(c, iconTerminal).FontSize(13).TextColor(t.Accent).Shrink(0)
		tabText(c, ui.Text(c, label).FontSize(12).Shrink(0), v.console)
		if a.consoleErr[w.ProjectID] && !v.console {
			ui.Box(c).Size(6, 6).Radius(3).Background(t.Danger).Shrink(0).Label("有失败的 git 命令").Tooltip("有失败的 git 命令")
		}
	})
	if row.Clicked() {
		v.console = true
	}
}

// gitConsole 是控制台的内容：最新的在最下面，有新命令时滚到底。
func (a *app) gitConsole(c *ui.Context, w *workspace, v *filesView) {
	t := c.Theme()
	delete(a.consoleErr, w.ProjectID)
	var runs []gitx.Run
	for _, r := range a.gitRuns {
		if a.runProject(r) == w.ProjectID {
			runs = append(runs, r)
		}
	}
	ui.Row(c).Height(30).Shrink(0).Padding(0, 8, 0, 16).Gap(4).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Hive 执行的 git 命令：改动仓库的和失败的，查询类的不记").FontSize(11.5).TextColor(t.TextMuted).SingleLine().Shrink(1)
		ui.Spacer(c)
		if len(runs) > 0 && pressable(iconButtonSized(c, iconTrash, "清空", 26, 15)).Clicked() {
			keep := a.gitRuns[:0]
			for _, r := range a.gitRuns {
				if a.runProject(r) != w.ProjectID {
					keep = append(keep, r)
				}
			}
			a.gitRuns = keep
			runs = nil
		}
	})
	if len(runs) == 0 {
		ui.Text(c, "还没有执行过 git 命令").FontSize(12).TextColor(t.TextMuted).Padding(12, 18)
		return
	}
	v.consoleList.Key = func(i int) any { return runs[i].ID }
	ui.List(c, &v.consoleList, len(runs), func(i int) { gitRunRow(c, runs[i]) }).Grow(1).Padding(0, 0, 8, 0)
	if last := runs[len(runs)-1]; last.ID != v.consoleLast || last.Done != v.consoleDone {
		v.consoleLast, v.consoleDone = last.ID, last.Done
		v.consoleList.ScrollIntoView(len(runs) - 1)
	}
}

// gitRunRow 是一条命令：时间、命令行、耗时，下面是输出，失败时报错是红的。
func gitRunRow(c *ui.Context, r gitx.Run) {
	t := c.Theme()
	cmd := "git " + quoteArgs(r.Args)
	col := ui.Column(c.Key(r.ID)).Padding(6, 16).Gap(3).Shrink(0)
	col.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, r.Start.Format("15:04:05")).FontSize(11).Font(monoFont).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			line := ui.Text(c, cmd).FontSize(11.5).Font(monoFont).Shrink(1).Selectable()
			if r.Err != "" {
				line.TextColor(t.Danger)
			}
			ui.Spacer(c)
			switch {
			case !r.Done:
				ui.Text(c, "进行中…").FontSize(11).TextColor(t.TextMuted).Shrink(0)
			case r.Took >= 100*time.Millisecond:
				ui.Text(c, fmt.Sprintf("%.1fs", r.Took.Seconds())).FontSize(11).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
			}
		})
		out := r.Out
		if r.Err != "" && !strings.Contains(out, r.Err) {
			out = strings.TrimLeft(out+"\n"+r.Err, "\n")
		}
		if out != "" {
			text := ui.Text(c, out).FontSize(11).Font(monoFont).LineHeight(1.4).Padding(0, 0, 0, 64).Selectable()
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

// quoteArgs 把参数拼成能在 shell 里照抄的命令行。
func quoteArgs(args []string) string {
	q := make([]string, len(args))
	for i, s := range args {
		q[i] = shellQuote(s)
	}
	return strings.Join(q, " ")
}
