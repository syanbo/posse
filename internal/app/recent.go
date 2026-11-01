package app

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"posse/internal/history"
)

// recentView 是右侧历史面板的状态：当前项目的最近会话。
type recentView struct {
	key      string // 结果对应的项目，变了就重新查
	sessions []history.Session
	loaded   bool
	poll     panelPoll
	previews map[string]recentPreview // 键是 agent:会话 ID
}

// recentPreview 是会话卡片上的一段聊天内容：最后一条回复，没有回复时是最后一句提问。
type recentPreview struct {
	at   time.Time // 取的时候会话的 UpdatedAt，会话更新了才重取
	user bool
	text string
}

// sessionKey 按会话文件认会话：同一个 ID 可能是几个不同的会话（见 history.FindPath）。
func sessionKey(s history.Session) string {
	if s.Path != "" {
		return s.Path
	}
	return s.Agent + ":" + s.ID
}

// sessionPreview 用 history.show 取会话末尾几条消息（不含工具），挑出要显示的那段。
func (a *app) sessionPreview(s history.Session) recentPreview {
	var d struct {
		Messages []history.Message `json:"messages"`
	}
	p := recentPreview{at: s.UpdatedAt}
	if a.call("history.show", map[string]any{"session": s.ID, "path": s.Path, "tail": 6, "tools": false}, &d, 10*time.Second) != nil {
		return p
	}
	for i := len(d.Messages) - 1; i >= 0; i-- {
		m := d.Messages[i]
		text := oneLine(m.Text)
		if text == "" {
			continue
		}
		if m.Role == "assistant" {
			p.user, p.text = false, text
			break
		}
		if m.Role == "user" && p.text == "" {
			p.user, p.text = true, text
		}
	}
	if r := []rune(p.text); len(r) > 240 {
		p.text = string(r[:240])
	}
	return p
}

// recentRefresh 是面板开着时多久重新查一次；历史索引本身最多 15 秒更新一次。
const recentRefresh = 15 * time.Second

const recentLimit = 50

// pollRecent 在主线程调用：换了项目就重新查，到点了在后台刷新。
func (a *app) pollRecent(c *ui.Context, w *workspace) {
	r := &a.recent
	key := w.ProjectID
	if r.key != key {
		*r = recentView{key: key}
	}
	if !r.poll.due(c, recentRefresh) {
		return
	}
	// 当前项目：项目目录和它的各个 worktree 目录，worktree 不在项目目录下面
	var dirs []string
	for _, x := range a.byProject[w.ProjectID] {
		dirs = append(dirs, x.Path)
	}
	dirs = topDirs(dirs)
	cached := r.previews
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
		all = slices.CompactFunc(all, func(x, y history.Session) bool { return sessionKey(x) == sessionKey(y) })
		if len(all) > recentLimit {
			all = all[:recentLimit]
		}
		// 聊天内容按会话缓存，会话没更新就不重取。先用缓存把列表显示出来，缺的再并发取（每个会话一次 history.show）
		previews := make(map[string]recentPreview, len(all))
		var missing []history.Session
		for _, s := range all {
			k := sessionKey(s)
			if p, ok := cached[k]; ok && p.at.Equal(s.UpdatedAt) {
				previews[k] = p
			} else {
				missing = append(missing, s)
			}
		}
		shown := maps.Clone(previews)
		a.win.Update(func() {
			r := &a.recent
			if r.key != key {
				return
			}
			if len(missing) == 0 {
				r.poll.done()
			}
			// 出错时 a.call 已经报过了
			if firstErr != nil && len(all) == 0 {
				return
			}
			r.sessions, r.loaded, r.previews = all, true, shown
		})
		if len(missing) == 0 {
			return
		}
		var mu sync.Mutex
		var wg sync.WaitGroup
		sem := make(chan struct{}, 4)
		for _, s := range missing {
			wg.Go(func() {
				sem <- struct{}{}
				p := a.sessionPreview(s)
				<-sem
				mu.Lock()
				previews[sessionKey(s)] = p
				mu.Unlock()
			})
		}
		wg.Wait()
		a.win.Update(func() {
			r := &a.recent
			if r.key != key {
				return
			}
			r.poll.done()
			r.previews = previews
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

// recentList 是文件栏的"历史"页：当前项目的会话，点一个跳到历史页打开它。
func (a *app) recentList(c *ui.Context, w *workspace) {
	t := c.Theme()
	a.pollRecent(c, w)
	r := &a.recent
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(8, 8, 16, 8).Gap(2).Children(func() {
			switch {
			case !r.loaded:
				usageNote(c, "正在读取…")
			case len(r.sessions) == 0:
				usageNote(c, "这个项目还没有会话记录。")
			}
			for i, s := range r.sessions {
				// 细线隔开相邻的两个会话，和卡片内容同宽（两头让开卡片的内边距）
				if i > 0 {
					hline(c).Margin(0, 8)
				}
				row := ui.Row(c.Key(sessionKey(s))).Padding(8, 8).Gap(8).Radius(rControl).AlignItems(ui.Start).Role(ui.RoleButton).Label(s.Title)
				selectedBg(c, row, a.selected == historyBoardID && a.history.sel == sessionKey(s))
				row.Children(func() {
					// 图标放在和标题行同高的格子里居中，和标题对齐
					ui.Row(c).Height(sz(20)).AlignItems(ui.Center).Shrink(0).Children(func() {
						agentTile(c, s.Agent, iconSize)
					})
					ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
						// 固定行高，悬停时时间换成按钮也不跳
						ui.Row(c).Height(sz(20)).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Text(c, oneLine(s.Title)).FontSize(fsBody).FontWeight(fwMedium).SingleLine().Grow(1).Shrink(1)
							// 悬停时时间换成"继续"按钮：已在 Posse 里运行的会话切过去，否则开终端接着聊
							if row.Hovered() {
								if pressable(playButton(c)).Clicked() {
									a.resumeSession(s)
								}
							} else {
								ui.Text(c, ago(s.UpdatedAt)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
							}
						})
						// 标题下面是最后一段聊天内容，再下面是目录和消息数
						if p := r.previews[sessionKey(s)]; p.text != "" {
							text := p.text
							if p.user {
								text = "你：" + text
							}
							ui.Text(c, text).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2).LineHeight(1.35).Margin(0, 0, 4, 0)
						}
						// 右下角悬停时有删除（移到废纸篓）；固定行高，按钮出现时卡片不跳
						ui.Row(c).Height(sz(20)).AlignItems(ui.Center).Children(func() {
							ui.Column(c).Grow(1).Shrink(1).Children(func() { sessionMeta(c, s) })
							if row.Hovered() && iconButtonSized(c, iconTrash, "移到废纸篓", compactBox(), smallIcon()).Clicked() {
								a.trashListedSession(s)
							}
						})
					})
				})
				if row.Clicked() {
					a.selected, a.revealSel = historyBoardID, true
					a.selectSession(s, -1)
				}
			}
		})
	})
}

// playButton 是卡片上"在 Posse 里继续"的小按钮：强调色底、白色播放图标，悬停时颜色加深。
func playButton(c *ui.Context) ui.Element {
	t := c.Theme()
	b := ui.Box(c).Size(miniBox()+2, miniBox()).Radius(rRow).Center().Shrink(0).Label("在 Posse 里继续").Tooltip("在 Posse 里继续").Role(ui.RoleButton)
	bg := t.Accent
	if b.Hovered() {
		bg = t.AccentHover
	}
	b.Background(b.AnimateColor("bg", bg, hover))
	b.Children(func() { ui.Icon(c, iconPlay).FontSize(fsSmall).TextColor(t.AccentText) })
	return b
}
