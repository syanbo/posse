package app

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/history"
)

// 右侧面板：标签栏右边的按钮打开，同一时间只开一个，开着哪个记在 ui.json。
const (
	panelUsage   = "usage"
	panelHistory = "history"
)

// recentView 是右侧历史面板的状态：当前项目或全部的最近会话。
type recentView struct {
	scope    int    // 0 当前项目，1 全部
	key      string // 结果对应的范围（筛选 + 项目），变了就重新查
	sessions []history.Session
	loaded   bool
	busy     bool
	fetched  time.Time
}

// recentRefresh 是面板开着时多久重新查一次；历史索引本身最多 15 秒更新一次。
const recentRefresh = 15 * time.Second

const recentLimit = 50

// pollRecent 在主线程调用：换了项目或筛选就重新查，到点了在后台刷新。
func (a *app) pollRecent(w *workspace) {
	r := &a.recent
	key := fmt.Sprint(r.scope, "\x00", w.ProjectID)
	if r.key != key {
		*r = recentView{scope: r.scope, key: key}
	}
	if r.busy || time.Since(r.fetched) < recentRefresh {
		return
	}
	r.busy = true
	// 当前项目：项目目录和它的各个 worktree 目录，worktree 不在项目目录下面
	var dirs []string
	if r.scope == 0 {
		for _, x := range a.workspacesOf(w.ProjectID) {
			dirs = append(dirs, x.Path)
		}
		dirs = topDirs(dirs)
	} else {
		dirs = []string{""}
	}
	go func() {
		var all []history.Session
		var firstErr error
		for _, dir := range dirs {
			var res struct {
				Sessions []history.Session `json:"sessions"`
			}
			if err := a.call("history.list", map[string]any{"cwd": dir, "limit": recentLimit}, &res, 30*time.Second); err != nil {
				firstErr = err
				continue
			}
			all = append(all, res.Sessions...)
		}
		slices.SortFunc(all, func(x, y history.Session) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
		all = slices.CompactFunc(all, func(x, y history.Session) bool { return x.Agent == y.Agent && x.ID == y.ID })
		if len(all) > recentLimit {
			all = all[:recentLimit]
		}
		a.win.Update(func() {
			r := &a.recent
			if r.key != key {
				return
			}
			r.busy, r.fetched = false, time.Now()
			if firstErr != nil && len(all) == 0 {
				a.err = firstErr.Error()
				return
			}
			r.sessions, r.loaded = all, true
		})
	}()
}

// topDirs 去掉被别的目录包含的目录和重复的目录：查询本来就含子目录。
func topDirs(dirs []string) []string {
	slices.Sort(dirs)
	var out []string
	for _, d := range dirs {
		d = strings.TrimRight(d, "/")
		if d == "" {
			continue
		}
		if n := len(out); n > 0 && (d == out[n-1] || strings.HasPrefix(d, out[n-1]+"/")) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// recentPanel 是右侧的历史面板：点一个会话跳到历史页打开它。
func (a *app) recentPanel(c *ui.Context, w *workspace) ui.Element {
	t := c.Theme()
	a.pollRecent(w)
	r := &a.recent
	return a.rightPanel(c, "历史", func() {
		ui.Box(c).Padding(0, 12, 10, 12).Children(func() {
			if segmented(c, &r.scope, "范围", "当前项目", "全部") {
				r.fetched = time.Time{}
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 8, 16, 8).Gap(2).Children(func() {
				switch {
				case !r.loaded:
					usageNote(c, "正在读取…")
				case len(r.sessions) == 0 && r.scope == 0:
					usageNote(c, "这个项目还没有会话记录。")
				case len(r.sessions) == 0:
					usageNote(c, "还没有会话记录。")
				}
				for _, s := range r.sessions {
					row := ui.Row(c.Key(s.Agent+":"+s.ID)).Padding(7, 8).Gap(10).Radius(8).AlignItems(ui.Center).Role(ui.RoleButton).Label(s.Title)
					selectable(c, row, a.selected == historyBoardID && a.history.sel == s.ID)
					row.Children(func() {
						agentTile(c, s.Agent, 20)
						ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
							// 固定行高，悬停时时间换成按钮也不跳
							ui.Row(c).Height(20).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Text(c, oneLine(s.Title)).FontSize(12.5).FontWeight(500).SingleLine().Grow(1).Shrink(1)
								// 悬停时时间换成"继续"按钮：已在 Hive 里运行的会话切过去，否则开终端接着聊
								if row.Hovered() {
									if pressable(playButton(c)).Clicked() {
										a.resumeSession(s)
									}
								} else {
									ui.Text(c, ago(s.UpdatedAt)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
								}
							})
							// 全部会话时看目录才知道是哪个项目的
							if r.scope == 1 && s.Cwd != "" {
								ui.Text(c, shortPath(s.Cwd)).FontSize(10).Font(monoFont).TextColor(t.TextMuted).SingleLine()
							}
							sessionMeta(c, s)
						})
					})
					if row.Clicked() {
						a.selected, a.revealSel = historyBoardID, true
						a.selectSession(s.ID, -1)
					}
				}
			})
		})
	})
}

// rightPanel 是右侧面板的外框：浮在终端右侧的卡片，有标题和关闭按钮，body 自己决定滚动。
func (a *app) rightPanel(c *ui.Context, title string, body func()) ui.Element {
	t := c.Theme()
	p := colors(t)
	shadow := ui.RGBA(0, 0, 0, 0.18)
	if t.Dark {
		shadow = ui.RGBA(0, 0, 0, 0.45)
	}
	return ui.Column(c).Absolute().Top(8).Right(8).Bottom(8).Width(280).Radius(12).Background(p.band).
		Border(1, p.line).Shadow(0, 8, 24, 0, shadow).Children(func() {
		ui.Row(c).Padding(14, 12, 10, 16).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(600).Grow(1)
			if iconButtonSized(c, iconClose, "关闭", 22, 12).Clicked() {
				a.state.RightPanel = ""
				a.saveState()
			}
		})
		body()
	})
}

// panelButton 是标签栏右边打开某个右侧面板的按钮，开着时垫一层强调色淡底。
func (a *app) panelButton(c *ui.Context, name string, icon *ui.SVG, label string) {
	b := iconButton(c.Key("panel-"+name), icon, label)
	if b.Hovered() {
		a.overPanelButton = true
	}
	if a.state.RightPanel == name {
		b.Background(colors(c.Theme()).tint)
	}
	if b.Clicked() {
		if a.state.RightPanel == name {
			a.state.RightPanel = ""
		} else {
			a.state.RightPanel = name
		}
		a.saveState()
	}
}

// playButton 是卡片上"在 Hive 里继续"的小按钮：强调色底、白色播放图标，悬停时颜色加深。
func playButton(c *ui.Context) ui.Element {
	t := c.Theme()
	b := ui.Box(c).Size(22, 20).Radius(6).Center().Shrink(0).Label("在 Hive 里继续").Tooltip("在 Hive 里继续").Role(ui.RoleButton)
	bg := t.Accent
	if b.Hovered() {
		bg = t.AccentHover
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() { ui.Icon(c, iconPlay).FontSize(12).TextColor(t.AccentText) })
	return b
}
