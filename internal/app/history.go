package app

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"hive/internal/agent"
	"hive/internal/history"
	"hive/internal/rpc"
)

// historyBoardID 是侧栏里"历史"一项的 ID。
const historyBoardID = "history"

// historyAgents 与界面上的筛选分段一一对应，0 是全部。
var (
	historyAgents      = append([]string{""}, history.Agents...)
	historyAgentLabels = append([]string{"全部"}, agentTitles(history.Agents)...)
)

type historyStatus struct {
	Sessions int              `json:"sessions"`
	Indexing bool             `json:"indexing"`
	Progress history.Progress `json:"progress"`
	Error    string           `json:"error"`
}

type historyDetail struct {
	Session  history.Session   `json:"session"`
	Messages []history.Message `json:"messages"`
	Terminal string            `json:"terminal"`

	md map[int][]mdBlock // 消息序号 → 解析好的 Markdown
	// items 是要列出的行，itemsKey 是算它时的条件（是否全显示工具、命中序号、展开了哪些工具组）
	items    []transcriptItem
	itemsKey string
	opened   map[int]bool // 展开了的工具组，键是组里第一条的序号
	openedV  int          // opened 每变一次加一
}

// transcriptItem 是对话里的一行：一条消息，或折成一行的一串连续工具调用。
type transcriptItem struct {
	msg   history.Message
	tools []history.Message
}

// visible 返回要列出的行：全显示工具时逐条列出；否则连续的工具调用折成一行"工具调用 N 次"，
// 点开的组和命中的那条照常列出。结果缓存在会话上。
func (d *historyDetail) visible(tools bool, hitSeq int) []transcriptItem {
	key := fmt.Sprint(tools, hitSeq, d.openedV)
	if d.items != nil && key == d.itemsKey {
		return d.items
	}
	d.items, d.itemsKey = d.items[:0], key
	for i := 0; i < len(d.Messages); i++ {
		m := d.Messages[i]
		if m.Role != "tool" || tools {
			d.items = append(d.items, transcriptItem{msg: m})
			continue
		}
		j := i
		for j < len(d.Messages) && d.Messages[j].Role == "tool" {
			j++
		}
		run := d.Messages[i:j]
		hit := slices.ContainsFunc(run, func(t history.Message) bool { return t.Seq == hitSeq })
		if d.opened[run[0].Seq] || hit {
			d.items = append(d.items, transcriptItem{msg: run[0], tools: run})
			for _, t := range run {
				d.items = append(d.items, transcriptItem{msg: t})
			}
		} else {
			d.items = append(d.items, transcriptItem{msg: run[0], tools: run})
		}
		i = j - 1
	}
	return d.items
}

// historyView 是历史页的状态。界面线程读写；req 和 want 由 mu 保护，供请求协程核对自己是否过期。
type historyView struct {
	query    string
	agent    int
	sessions []history.Session // 没有搜索词时：最近的会话
	groups   []string          // 与 sessions 对应的"今天、昨天……"分组
	hits     []history.Hit     // 有搜索词时
	anyWord  bool              // 没有同时含所有词的结果，列出的是含任一词的
	status   historyStatus
	loaded   bool
	sel      string // 选中的会话 ID
	hitSeq   int    // 从搜索结果打开时命中的消息序号，-1 表示没有
	placed   bool   // 打开会话后是否已经定位过
	detail   *historyDetail
	tools    bool // 显示工具调用
	list     ui.ListState
	shown    string // 列表当前结果对应的查询条件，换了条件才回到顶部
	messages ui.ListState

	mu      sync.Mutex
	req     int // 列表请求的序号，只采用最新一次的结果
	wQuery  string
	wAgent  int
	visible bool
	fetched time.Time
}

// setSessions 换上新的会话列表，顺便分好组：分组要比较日期，不放在每帧里做。
func (h *historyView) setSessions(ss []history.Session) {
	h.sessions = ss
	h.groups = make([]string, len(ss))
	for i, s := range ss {
		h.groups[i] = dayGroup(s.UpdatedAt)
	}
}

func (h *historyView) want() (query string, agent int, req int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.wQuery, h.wAgent, h.req
}

// --- 数据

// searchHistory 在界面线程调用，记下当前的查询条件，停顿 delay 后再请求，免得每敲一个字都查一次。
func (a *app) searchHistory(delay time.Duration) {
	h := &a.history
	h.mu.Lock()
	h.wQuery, h.wAgent = h.query, h.agent
	h.req++
	req := h.req
	h.mu.Unlock()
	time.AfterFunc(delay, func() { a.fetchHistory(req) })
}

func (a *app) fetchHistory(req int) {
	h := &a.history
	query, agentIdx, cur := h.want()
	if req != cur || a.c == nil {
		return
	}
	params := map[string]any{"query": strings.TrimSpace(query), "agent": historyAgents[agentIdx], "limit": 200}
	var res struct {
		Sessions []history.Session `json:"sessions"`
		Hits     []history.Hit     `json:"hits"`
		Any      bool              `json:"any"`
		Status   historyStatus     `json:"status"`
	}
	method := "history.list"
	if params["query"] != "" {
		method = "history.search"
	}
	err := a.call(method, params, &res, 30*time.Second)
	h.mu.Lock()
	h.fetched = time.Now()
	stale := req != h.req
	h.mu.Unlock()
	if stale {
		return
	}
	if err != nil {
		return
	}
	a.win.Update(func() {
		h.loaded = true
		h.status, h.anyWord = res.Status, res.Any
		// 列表按会话 ID 保持滚动位置，定期刷新时有用；换了搜索词或筛选就该从第一条看起
		if shown := fmt.Sprint(agentIdx, "\x00", params["query"]); shown != h.shown {
			h.shown = shown
			h.list.ScrollTo(0, ui.Start)
		}
		if method == "history.list" {
			h.setSessions(res.Sessions)
			h.hits = nil
		} else {
			h.hits = res.Hits
			h.setSessions(nil)
		}
		// 首次建索引时每隔一会儿再查一次，结果随索引进度补全
		if res.Status.Indexing {
			a.searchHistory(2 * time.Second)
		}
	})
}

// historyTick 由轮询协程调用：历史页开着时定期刷新，常驻进程借此做增量索引。
func (a *app) historyTick() {
	h := &a.history
	h.mu.Lock()
	due := h.visible && time.Since(h.fetched) > 15*time.Second
	req := h.req
	if due {
		// 轮询协程不碰界面状态，沿用上次的查询条件
		h.req++
		req = h.req
	}
	h.mu.Unlock()
	if due {
		go a.fetchHistory(req)
	}
}

func (a *app) setHistoryVisible(v bool) {
	h := &a.history
	h.mu.Lock()
	opened := v && !h.visible
	h.visible = v
	h.mu.Unlock()
	if opened {
		a.searchHistory(0)
	}
}

func (a *app) selectSession(id string, seq int) {
	h := &a.history
	h.sel, h.hitSeq, h.placed = id, seq, false
	if h.detail != nil && h.detail.Session.ID != id {
		h.detail = nil
	}
	go func() {
		var d historyDetail
		if a.call("history.show", map[string]any{"session": id}, &d, 30*time.Second) != nil {
			return
		}
		a.win.Update(func() {
			if h.sel == id {
				h.detail = &d
			}
		})
	}()
}

// resumeSession 在 hive 里继续会话；会话目录不在任何工作区时，问一句再把目录加成项目。
func (a *app) resumeSession(s history.Session) {
	go func() {
		var t struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspaceId"`
		}
		err := a.call("history.resume", map[string]any{"session": s.ID, "cols": 120, "rows": 36}, &t, 30*time.Second)
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == rpc.CodeNotFound && s.Cwd != "" && strings.Contains(re.Message, "no workspace contains") {
			a.win.Update(func() {
				a.confirm("把 "+shortPath(s.Cwd)+" 添加为项目？",
					"Hive 里还没有包含这个目录的工作区。添加后会话在这个项目里继续。",
					"添加并继续", func() {
						go func() {
							if a.call("project.add", map[string]any{"path": s.Cwd}, nil, 10*time.Second) != nil {
								return
							}
							a.refresh()
							a.resumeSession(s)
						}()
					})
			})
			return
		}
		a.refresh()
		if err == nil {
			a.win.Update(func() { a.openTerminal(t.WorkspaceID, t.ID) })
		}
	}()
}

// ago 是会话最后使用的时间，写成"20 分钟前"。
func ago(t time.Time) string {
	if s := elapsed(t); s != "刚刚" {
		return s + "前"
	}
	return "刚刚"
}

// sessionMeta 是会话卡片上的消息数和最近用的模型；常驻进程是旧版本时没有模型，只显示消息数。
func sessionMeta(c *ui.Context, s history.Session) {
	t := c.Theme()
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, fmt.Sprintf("%d 条消息", s.Messages)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		if s.Model != "" {
			ui.Text(c, shortModel(s.Model)).FontSize(11).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(s.Model)
		}
	})
}

// shortModel 把模型 ID 写成简短的名字：claude-opus-5-5 → Opus 5.5，gpt-6-astra → GPT-6 Astra，
// grok-4.7-build → Grok 4.7。日期后缀和 build、latest 这类标记去掉。
func shortModel(id string) string {
	var words []string
	for _, w := range strings.Split(strings.ToLower(id), "-") {
		switch {
		case w == "" || w == "build" || w == "latest":
		case len(w) == 8 && strings.Trim(w, "0123456789") == "":
			// 20251001 这样的日期
		default:
			words = append(words, w)
		}
	}
	if len(words) > 1 && words[0] == "claude" {
		// claude-<家族>-<主版本>-<次版本>，旧的命名是 claude-<版本>-<家族>
		var family string
		var version []string
		for _, w := range words[1:] {
			if strings.Trim(w, "0123456789.") == "" {
				version = append(version, w)
			} else {
				family = w
			}
		}
		if family != "" {
			return strings.TrimSpace(capitalize(family) + " " + strings.Join(version, "."))
		}
	}
	out := ""
	for i, w := range words {
		switch {
		case w == "gpt":
			w = "GPT"
		case strings.Trim(w, "0123456789.") == "", len(w) > 1 && w[0] == 'o' && strings.Trim(w[1:], "0123456789") == "":
			// 版本号和 OpenAI 的 o 系列（o3、o4）保持原样
		default:
			w = capitalize(w)
		}
		// GPT 和版本号连写：gpt-6 → GPT-6
		if i > 0 && words[i-1] == "gpt" {
			out += "-" + w
		} else if out == "" {
			out = w
		} else {
			out += " " + w
		}
	}
	return out
}

func capitalize(w string) string {
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

// resumeCommand 是在 hive 之外继续会话的命令，复制给用户自己用。
func resumeCommand(s history.Session) string {
	id := agent.Quote(s.ID)
	var cmd string
	switch s.Agent {
	case "claude":
		cmd = "claude --resume " + id
	case "codex":
		cmd = "codex resume " + id
	case "grok":
		cmd = "grok --resume " + id
	case "cursor":
		cmd = "cursor-agent --resume " + id
	}
	if s.Cwd != "" {
		cmd = "cd " + agent.Quote(s.Cwd) + " && " + cmd
	}
	return cmd
}

// --- 界面

func (a *app) historySidebarItem(c *ui.Context) {
	item := a.navRow(c, historyBoardID, iconHistory, "历史", "", nil)
	if a.revealSel && a.selected == historyBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
}

func (a *app) historyBoard(c *ui.Context) {
	h := &a.history
	band(c, func() {
		titleText(c, "历史", fsCardTitle).Margin(0, 6, 0, 0)
		// 系统搜索框的占位文字固定是英文 Search，这里用普通输入框
		search := ui.TextInput(c, &h.query).Label("搜索会话").Placeholder("搜索会话内容  ⌘F").Width(340)
		if c.Shortcut(ui.Cmd, ui.KeyF) {
			search.Focus()
		}
		changed := search.Changed()
		if h.query != "" && search.Shortcut(0, ui.KeyEscape) {
			h.query, changed = "", true
		}
		if changed {
			a.searchHistory(250 * time.Millisecond)
		}
		ui.Spacer(c)
		if segmented(c, &h.agent, "按 agent 筛选", historyAgentLabels...) {
			a.searchHistory(0)
		}
	})
	searching := strings.TrimSpace(h.query) != ""
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(14).Gap(14).Children(func() {
		card(c).Width(380).Shrink(0).Clip().Children(func() {
			a.historyNotice(c, searching)
			if searching {
				a.hitList(c)
			} else {
				a.sessionList(c)
			}
		})
		ui.Column(c).Grow(1).Shrink(1).Gap(14).Children(func() { a.sessionDetail(c) })
	})
}

// historyNotice 是列表上方的一行说明：建索引进度、退而求其次的结果。
func (a *app) historyNotice(c *ui.Context, searching bool) {
	t := c.Theme()
	h := &a.history
	var msg string
	switch {
	case h.status.Indexing && h.status.Progress.Total > 0:
		msg = fmt.Sprintf("正在建立索引，已检查 %d / %d 个会话文件，结果会陆续补全", h.status.Progress.Done, h.status.Progress.Total)
	case h.status.Indexing:
		msg = "正在建立索引…"
	case searching && h.anyWord:
		msg = "没有同时包含所有词的会话，下面是包含其中任一词的"
	case h.status.Error != "":
		msg = "索引更新失败：" + h.status.Error
	}
	if msg == "" {
		return
	}
	ui.Text(c, msg).FontSize(11).TextColor(t.TextMuted).LineHeight(1.4).Padding(12, 20, 2, 20)
}

func (a *app) sessionList(c *ui.Context) {
	t := c.Theme()
	h := &a.history
	if h.loaded && len(h.sessions) == 0 && !h.status.Indexing {
		a.historyEmpty(c, "还没有会话记录", "Hive 会读取 ~/.claude、~/.codex、~/.cursor、~/.grok 里的会话记录，只读不改。")
		return
	}
	// 按"今天、昨天、本周、更早"分组，组标题跟在第一行前面
	groups := h.groups
	// 列表里有"几分钟前"，界面不再每秒重建，自己约下一帧
	c.After(30 * time.Second)
	h.list.Key = func(i int) any { return h.sessions[i].Agent + h.sessions[i].ID }
	fadeEdges(ui.List(c, &h.list, len(h.sessions), func(i int) {
		s := h.sessions[i]
		ui.Column(c).Children(func() {
			if i == 0 || groups[i] != groups[i-1] {
				ui.Text(c, groups[i]).FontSize(11).FontWeight(500).TextColor(t.TextMuted).Padding(12, 20, 4, 20)
			}
			a.sessionRow(c, s, nil)
		})
	}).Grow(1).Padding(4, 0, 8, 0).Gap(4), &h.list, colors(t).card)
}

func (a *app) hitList(c *ui.Context) {
	h := &a.history
	if h.loaded && len(h.hits) == 0 && !h.status.Indexing {
		a.historyEmpty(c, "没有找到“"+strings.TrimSpace(h.query)+"”", "中文按字匹配，英文按词的开头匹配；加引号可以搜整句。")
		return
	}
	c.After(30 * time.Second)
	h.list.Key = func(i int) any { return h.hits[i].Agent + h.hits[i].ID }
	fadeEdges(ui.List(c, &h.list, len(h.hits), func(i int) {
		hit := h.hits[i]
		a.sessionRow(c, hit.Session, &hit)
	}).Grow(1).Padding(8, 0).Gap(4), &h.list, colors(c.Theme()).card)
}

func (a *app) historyEmpty(c *ui.Context, title, body string) {
	t := c.Theme()
	ui.Column(c).Padding(28, 20).Gap(6).Children(func() {
		ui.Text(c, title).FontSize(13).FontWeight(500)
		ui.Text(c, body).FontSize(12).TextColor(t.TextMuted).LineHeight(1.45)
	})
}

// sessionRow 是列表里的一个会话；搜索时多一行命中的摘要。
func (a *app) sessionRow(c *ui.Context, s history.Session, hit *history.Hit) {
	t := c.Theme()
	h := &a.history
	row := ui.Row(c.Key(s.Agent+s.ID)).Margin(0, 8).Padding(10, 12, 10, 14).Gap(12).Radius(8).AlignItems(ui.Center)
	selectable(c, row, h.sel == s.ID)
	row.Children(func() {
		agentTile(c, s.Agent, 28)
		ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, oneLine(s.Title)).FontSize(13).FontWeight(500).MaxLines(1).Grow(1).Shrink(1)
				ui.Text(c, ago(s.UpdatedAt)).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			if s.Cwd != "" {
				ui.Text(c, shortPath(s.Cwd)).FontSize(11).Font(monoFont).TextColor(t.TextMuted).MaxLines(1)
			}
			sessionMeta(c, s)
			// 只有标题命中时，标题本身就是证据
			if hit != nil && hit.Role != "title" {
				snippetText(c, *hit).FontSize(12).TextColor(t.TextMuted).MaxLines(2).LineHeight(1.4).Padding(2, 0, 0, 0)
			}
		})
	})
	if row.Clicked() {
		seq := -1
		if hit != nil && hit.Role != "title" {
			seq = hit.Seq
		}
		a.selectSession(s.ID, seq)
	}
}

// snippetText 显示命中摘要，命中的词用正文色加粗，其余是次要色。
func snippetText(c *ui.Context, hit history.Hit) ui.Element {
	t := c.Theme()
	label := map[string]string{"user": "你：", "assistant": agentTitle(hit.Agent) + "：", "tool": "工具："}[hit.Role]
	spans := []ui.Span{{Text: label}}
	last := 0
	for _, sp := range hit.Spans {
		if sp.Start < last || sp.End > len(hit.Snippet) {
			continue
		}
		spans = append(spans, ui.Span{Text: hit.Snippet[last:sp.Start]}, ui.Span{Text: hit.Snippet[sp.Start:sp.End], Color: t.Text, Weight: 600})
		last = sp.End
	}
	spans = append(spans, ui.Span{Text: hit.Snippet[last:]})
	return ui.RichText(c, spans...)
}

func (a *app) sessionDetail(c *ui.Context) {
	t := c.Theme()
	h := &a.history
	if h.sel == "" {
		ui.Column(c).Grow(1).Center().Children(func() {
			card(c).Width(420).Padding(24).Gap(14).Children(func() {
				tile(c, iconHistory, 48, colors(t).tint, t.Accent)
				ui.Column(c).Gap(6).Children(func() {
					titleText(c, "会话历史", fsPageTitle)
					ui.Text(c, "在这里找回以前和 agent 的对话，选中后可以在 Hive 里接着聊。").FontSize(13).TextColor(t.TextMuted).LineHeight(1.5)
				})
				if n := h.status.Sessions; n > 0 {
					ui.Text(c, fmt.Sprintf("已索引 %d 个会话", n)).FontSize(12).FontFeatures("tnum").TextColor(t.TextMuted)
				}
			})
		})
		return
	}
	d := h.detail
	if d == nil {
		return
	}
	s := d.Session
	card(c).Padding(18, 20).Gap(14).Shrink(0).Children(func() {
		ui.Row(c).Gap(14).AlignItems(ui.Start).Children(func() {
			agentTile(c, s.Agent, 44)
			ui.Column(c).Gap(7).Grow(1).Shrink(1).Children(func() {
				titleText(c, oneLine(s.Title), fsPageTitle).LineHeight(1.3).MaxLines(2).Selectable()
				var cwd, branch func()
				if s.Cwd != "" {
					cwd = metaMono(c, iconFolder, shortPath(s.Cwd), s.Cwd)
				}
				if s.Branch != "" {
					branch = metaMono(c, iconBranch, s.Branch, "")
				}
				metaRow(c, meta(c, agentTitle(s.Agent)), cwd, branch, meta(c, s.UpdatedAt.Local().Format("1月2日 15:04")), meta(c, fmt.Sprintf("%d 条消息", s.Messages)))
			})
		})
		ui.Row(c).Gap(8).Children(func() {
			if d.Terminal != "" {
				if pressable(accentButton(c, "切到正在运行的终端")).Clicked() {
					if w, _ := a.findTerminal(d.Terminal); w != nil {
						a.openTerminal(w.ID, d.Terminal)
					}
				}
			} else if pressable(accentButton(c, "在 Hive 里继续")).Tooltip(resumeCommand(s)).Clicked() {
				a.resumeSession(s)
			}
			if pressable(textButton(c, "复制恢复命令")).Tooltip(resumeCommand(s)).Clicked() {
				c.WriteClipboard(resumeCommand(s))
			}
			if pressable(textButton(c, "移到废纸篓")).Clicked() {
				a.askTrashSession(d)
			}
			ui.Spacer(c)
			ui.Checkbox(c, &h.tools, "显示工具调用")
		})
	})
	msgs := d.visible(h.tools, h.hitSeq)
	if len(msgs) == 0 {
		card(c).Children(func() {
			a.historyEmpty(c, "这个会话没有可显示的消息", "打开“显示工具调用”可以看到工具的输入和输出。")
		})
		return
	}
	ns := queryWords(h.query)
	h.messages.Key = func(i int) any { return [2]any{msgs[i].msg.Seq, msgs[i].tools != nil} }
	if !h.placed {
		// 从搜索结果打开时定位到命中的消息，否则从第一句读起
		h.placed = true
		h.messages.ScrollTo(0, ui.Start)
		for i, it := range msgs {
			if it.tools == nil && it.msg.Seq == h.hitSeq {
				h.messages.ScrollTo(i, ui.Center)
				break
			}
		}
	}
	// 对话直接铺在画布上，不装卡片；版心限宽居中，一行不至于太长
	transcript := ui.List(c, &h.messages, len(msgs), func(i int) {
		ui.Row(c).Justify(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).MaxWidth(transcriptWidth).Children(func() {
				it := msgs[i]
				if it.tools != nil {
					a.toolRun(c, d, it.tools)
					return
				}
				a.messageView(c, s, it.msg, ns, it.msg.Seq == h.hitSeq)
			})
		})
	}).Grow(1).Padding(8, 0, 32, 0).Gap(6)
	fadeEdges(transcript, &h.messages, colors(t).canvas)
}

// transcriptWidth 是对话的版心宽度：14.5pt 的正文一行四十几个汉字。
const transcriptWidth = 680

// toolRun 是折起来的一串工具调用：点一下展开或收起。
func (a *app) toolRun(c *ui.Context, d *historyDetail, run []history.Message) {
	t := c.Theme()
	open := d.opened[run[0].Seq]
	row := ui.Row(c.Key("tools")).Height(26).Padding(0, 10).Gap(6).Radius(rButton).AlignItems(ui.Center).AlignSelf(ui.Start).Role(ui.RoleButton)
	selectable(c, row, false)
	row.Children(func() {
		turn := float32(0)
		if open {
			turn = 90
		}
		ui.Icon(c, iconChevron).FontSize(11).TextColor(t.TextMuted).Rotate(turn)
		ui.Text(c, fmt.Sprintf("工具调用 %d 次", len(run))).FontSize(fsMeta).TextColor(t.TextMuted)
	})
	if row.Clicked() {
		if d.opened == nil {
			d.opened = map[int]bool{}
		}
		if open {
			delete(d.opened, run[0].Seq)
		} else {
			d.opened[run[0].Seq] = true
		}
		d.openedV++
	}
}

func (a *app) messageView(c *ui.Context, s history.Session, m history.Message, ns []string, hit bool) {
	t := c.Theme()
	who := map[string]string{"user": "你", "assistant": agentTitle(s.Agent), "tool": "工具"}[m.Role]
	whoColor := t.TextMuted
	if m.Role == "user" {
		whoColor = t.Accent
	}
	// agent 的回复不装框，直接铺开；命中搜索的那条铺一层琥珀淡底
	box := ui.Column(c).Margin(0, 16).Padding(10, 16, 12, 16).Gap(6).Radius(10)
	switch {
	case hit:
		box.Background(colors(t).honey.Alpha(0.1))
	case m.Role == "user":
		// 自己说的话靠右，强调色淡底，竖条也在靠外的右边，一眼和 agent 的回复分开
		accent := t.Accent
		box.Background(colors(t).tint).AlignSelf(ui.End).MaxWidth(transcriptWidth * 0.8)
		box.Draw(func(p *ui.Painter, r ui.Rect) {
			p.Fill(ui.Rect{X: r.X + r.W - 3, Y: r.Y + 10, W: 3, H: r.H - 20}, accent, 1.5)
		})
	}
	box.Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, who).FontSize(fsMeta).FontWeight(500).TextColor(whoColor)
			if !m.At.IsZero() {
				ui.Text(c, m.At.Local().Format("1月2日 15:04")).FontSize(11).FontFeatures("tnum").TextColor(t.TextMuted)
			}
		})
		text := strings.TrimSpace(m.Text)
		switch m.Role {
		case "tool":
			highlighted(c, text, ns).FontSize(12).Font(monoFont).TextColor(t.TextMuted).MaxLines(8).LineHeight(1.5).Selectable()
		case "assistant":
			// agent 的回复按 Markdown 显示；用户自己打的字原样显示
			ui.Column(c).Gap(8).Selectable().Children(func() {
				mdView{words: ns}.blocks(c, a.markdown(m))
			})
		default:
			highlighted(c, text, ns).FontSize(fsBody).LineHeight(1.55).Selectable()
		}
	})
}

// markdown 返回消息解析好的 Markdown，按消息序号缓存在当前打开的会话上。
func (a *app) markdown(m history.Message) []mdBlock {
	d := a.history.detail
	if d.md == nil {
		d.md = map[int][]mdBlock{}
	}
	bs, ok := d.md[m.Seq]
	if !ok {
		bs = parseMarkdown(strings.TrimSpace(m.Text))
		d.md[m.Seq] = bs
	}
	return bs
}

// highlighted 把文字里出现的搜索词标上底色，方便在长消息里找到命中处。
func highlighted(c *ui.Context, text string, words []string) ui.Element {
	marks := markRanges(text, words)
	if len(marks) == 0 {
		return ui.Text(c, text)
	}
	return ui.RichText(c, markSpans(text, marks, ui.Span{}, colors(c.Theme()).honey.Alpha(0.28))...)
}

// markRanges 返回 text 里搜索词出现的字节区间，按位置排好、互不重叠。
func markRanges(text string, words []string) [][2]int {
	if len(words) == 0 {
		return nil
	}
	lower := strings.ToLower(text)
	// 转小写改变了字节长度时位置对不上，干脆不标
	if len(lower) != len(text) {
		return nil
	}
	var marks [][2]int
	for _, w := range words {
		for i := 0; ; {
			j := strings.Index(lower[i:], w)
			if j < 0 {
				break
			}
			marks = append(marks, [2]int{i + j, i + j + len(w)})
			i += j + len(w)
		}
	}
	slices.SortFunc(marks, func(a, b [2]int) int { return a[0] - b[0] })
	out := marks[:0]
	last := 0
	for _, m := range marks {
		if m[0] >= last {
			out = append(out, m)
			last = m[1]
		}
	}
	return out
}

// queryWords 是搜索框里的词，小写，去掉引号。
func queryWords(q string) []string {
	return strings.Fields(strings.ToLower(strings.ReplaceAll(q, `"`, " ")))
}

func dayGroup(t time.Time) string {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return "今天"
	case !t.Before(today.AddDate(0, 0, -1)):
		return "昨天"
	case !t.Before(today.AddDate(0, 0, -7)):
		return "最近 7 天"
	case !t.Before(today.AddDate(0, 0, -30)):
		return "最近 30 天"
	}
	return "更早"
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
