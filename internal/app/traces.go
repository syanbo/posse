package app

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"posse/internal/errtext"
	"posse/internal/gitx"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// 痕迹：agent 在工作区外留下的文件和目录，常驻进程记账（trace.list），这里列出来清理。
// 删工作区时一起列出这个工作区的，项目的右键菜单「清理 agent 痕迹…」列出整个项目的。
// 新建的目录和文件可以勾选移到废纸篓（默认勾上，留下它的终端还开着的除外），自建的 worktree 走 git worktree remove（默认不勾），
// 改过的已有文件和配置只列出来，不给删。

type traceItem struct {
	Path       string    `json:"path"`
	Kind       string    `json:"kind"`
	Repo       string    `json:"repo"`
	TerminalID string    `json:"terminalId"`
	Agent      string    `json:"agent"`
	At         time.Time `json:"at"`
	IsDir      bool      `json:"isDir"`
	Running    bool      `json:"running"`
}

func (t traceItem) removable() bool { return t.Kind == "created" || t.Kind == "worktree" }

type traceRow struct {
	traceItem
	checked bool
	size    int64 // -1 是还没算完
}

type tracesView struct {
	open  bool
	title string
	body  string
	verb  string
	rows  []traceRow
	// do 在确认后调用（后台），先做自己的事（比如删工作区），返回 false 时不再清理痕迹
	do func() bool
}

// fetchTraces 取痕迹；常驻进程是旧版本、没有这个接口时当作没有，不报错。
func (a *app) fetchTraces(params map[string]any) []traceItem {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var items []traceItem
	if err := a.c.Call(ctx, "trace.list", params, &items); err != nil {
		return nil
	}
	return items
}

// openTraces 打开痕迹对话框，并在后台算各项的大小。
func (a *app) openTraces(title, body, verb string, items []traceItem, keepRunning bool, do func() bool) {
	rows := make([]traceRow, len(items))
	for i, it := range items {
		// 删工作区时它的终端都会关掉，正在运行的也默认勾上
		rows[i] = traceRow{traceItem: it, size: -1, checked: it.Kind == "created" && (!it.Running || !keepRunning)}
	}
	a.traces = tracesView{open: true, title: title, body: body, verb: verb, rows: rows, do: do}
	for i, r := range rows {
		go func() {
			n := pathSize(r.Path)
			a.update(func() {
				if i < len(a.traces.rows) && a.traces.rows[i].Path == r.Path {
					a.traces.rows[i].size = n
				}
			})
		}()
	}
}

// pathSize 是文件或整个目录的大小；目录太大时最多数 5 秒。
func pathSize(path string) int64 {
	var n int64
	deadline := time.Now().Add(5 * time.Second)
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return filepath.SkipAll
		}
		if fi, err := d.Info(); err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// askRemoveWorkspaceTraces 删工作区前查一下它的痕迹：有就换成带勾选的确认框，没有照旧。
func (a *app) askRemoveWorkspace(w workspace) {
	body := "工作区里的终端会被关闭。"
	if w.Kind == "worktree" {
		body = "会关闭终端并删除 git worktree 目录（分支保留）。有未提交的改动时会拒绝删除。"
	}
	title := "删除工作区 " + displayName(&w) + "？"
	remove := func() bool {
		// 常驻进程的 force 同时管关终端和丢弃改动：worktree 要保住未提交的改动，不能带 force，
		// 就先查改动、再自己关终端，否则开着终端的 worktree 删不掉
		if w.Kind == "worktree" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			out, err := gitx.Dirty(ctx, w.Path)
			cancel()
			if err == nil && out != "" {
				a.update(func() { a.err = "工作区 " + displayName(&w) + " 有未提交的改动，没有删除" })
				return false
			}
			for _, t := range w.Terminals {
				if t.Running {
					a.call("terminal.close", map[string]any{"terminal": t.ID}, nil, 10*time.Second)
				}
			}
		}
		err := a.call("workspace.rm", map[string]any{"workspace": w.ID, "force": w.Kind != "worktree"}, nil, 30*time.Second)
		a.refresh()
		return err == nil
	}
	go func() {
		items := a.fetchTraces(map[string]any{"workspace": w.ID})
		a.update(func() {
			if len(items) == 0 {
				a.confirm(title, body, "删除", func() { remove() })
				return
			}
			a.openTraces(title, body+"\n\nagent 在工作区外留下了下面这些，勾选的会一起移到废纸篓：", "删除", items, false, remove)
		})
	}()
}

// askCleanTraces 是项目右键菜单的「清理 agent 痕迹…」。
func (a *app) askCleanTraces(w workspace) {
	go func() {
		items := a.fetchTraces(map[string]any{"project": w.ProjectID})
		a.update(func() {
			if len(items) == 0 {
				a.confirm("没有 agent 痕迹", "项目 "+w.Project+" 里的 agent 没有在工作区外留下记录到的文件。", "好", nil)
				return
			}
			a.openTraces("清理 "+w.Project+" 的 agent 痕迹", "agent 在工作区外留下了下面这些。勾选的移到废纸篓，worktree 用 git 删除。", "清理", items, true, nil)
		})
	}()
}

// cleanTraces 清理勾选的痕迹，清掉的从账本里去掉。
func (a *app) cleanTraces(rows []traceRow) {
	var done, failed []string
	for _, r := range rows {
		if !r.checked || !r.removable() {
			continue
		}
		var err error
		if r.Kind == "worktree" {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			repo := r.Repo
			if repo == "" {
				repo = gitx.CommonDir(ctx, r.Path)
			}
			err = gitx.RemoveWorktree(ctx, repo, r.Path, false)
			cancel()
		} else {
			err = mygo.Shell.TrashItem(r.Path)
		}
		if err != nil {
			failed = append(failed, shortPath(r.Path)+"："+firstLine(errtext.Text(err)))
			continue
		}
		done = append(done, r.Path)
	}
	if len(done) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		a.c.Call(ctx, "trace.forget", map[string]any{"paths": done}, nil)
		cancel()
	}
	if len(failed) > 0 {
		a.update(func() { a.err = "有些痕迹没清掉：" + strings.Join(failed, "；") })
	}
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

var traceKindText = map[string]string{
	"created":  "新建",
	"worktree": "自建的 worktree，用 git 删除",
	"modified": "改过的已有文件，只列出",
	"config":   "改过的配置，只列出",
}

func (a *app) tracesDialog(c *ui.Context) {
	t := c.Theme()
	v := &a.traces
	ui.Modal(c, &v.open, func() {
		ui.Column(c).Width(520).Gap(12).Children(func() {
			titleText(c, v.title, fsTitle)
			ui.Text(c, v.body).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.45)
			ui.Scroll(c).MaxHeight(sz(320)).Children(func() {
				ui.Column(c).Gap(12).Children(func() {
					for i := range v.rows {
						a.traceRowView(c, &v.rows[i])
					}
				})
			})
			var picked int
			var total int64
			for _, r := range v.rows {
				if r.checked && r.removable() {
					picked++
					if r.size > 0 {
						total += r.size
					}
				}
			}
			if picked > 0 {
				ui.Text(c, fmt.Sprintf("已选 %d 项，共 %s", picked, sizeText(total))).FontSize(fsSmall).TextColor(t.TextMuted)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, "取消")).Clicked() {
					v.open = false
				}
				if pressable(accentButton(c, v.verb)).Clicked() {
					v.open = false
					rows, do := v.rows, v.do
					go func() {
						if do != nil && !do() {
							return
						}
						a.cleanTraces(rows)
					}()
				}
			})
		})
	})
}

func (a *app) traceRowView(c *ui.Context, r *traceRow) {
	t := c.Theme()
	ui.Column(c.Key("trace:" + r.Path)).Gap(2).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			name := shortPath(r.Path)
			if r.IsDir {
				name += "/"
			}
			if r.removable() {
				ui.Checkbox(c, &r.checked, name)
			} else {
				ui.Text(c, name).FontSize(fsBody).TextColor(t.TextMuted).Padding(0, 0, 0, t.Space(6))
			}
			ui.Spacer(c)
			if r.size >= 0 {
				ui.Text(c, sizeText(r.size)).FontSize(fsSmall).TextColor(t.TextMuted)
			}
			if pressable(iconButtonSized(c, iconFolder, "在访达中显示", compactBox(), smallIcon())).Clicked() {
				mygo.Shell.ShowItemInFolder(r.Path)
			}
		})
		meta := agentTitle(r.Agent)
		if meta == "" {
			meta = "终端"
		}
		meta += " · " + r.At.Local().Format("01-02 15:04") + " · " + traceKindText[r.Kind]
		if r.Running {
			meta += " · 终端还开着"
		}
		ui.Text(c, meta).FontSize(fsSmall).TextColor(colors(t).faint).Padding(0, 0, 0, t.Space(6))
	})
}

// sizeText 把字节数写成 KB、MB、GB。
func sizeText(n int64) string {
	if n >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	}
	return byteSize(int(n))
}
