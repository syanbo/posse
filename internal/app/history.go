package app

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/history"
	"posse/internal/rpc"
	"posse/locales"
)

// historyLimit 是列表和搜索最多取的会话数。
const historyLimit = 200

// historyBoardID 是侧栏里"历史"一项的 ID。
const historyBoardID = "history"

// historyAgents 与界面上的筛选分段一一对应，0 是全部。
var historyAgents = append([]string{""}, history.Agents...)

// historyAgentLabels 是筛选分段的文字，和 historyAgents 一一对应。
func historyAgentLabels(m locales.Messages) []string {
	return append([]string{m.CommonAll()}, agentTitles(history.Agents)...)
}

type historyStatus struct {
	Sessions int              `json:"sessions"`
	Messages int              `json:"messages"`
	ByAgent  map[string]int   `json:"byAgent"`
	Indexing bool             `json:"indexing"`
	Progress history.Progress `json:"progress"`
	Error    string           `json:"error"`
}

type historyDetail struct {
	Session  history.Session   `json:"session"`
	Messages []history.Message `json:"messages"`
	Terminal string            `json:"terminal"`

	context   history.Context // 最后一次请求占用的上下文，由窗口进程读会话文件得到
	contextOK bool
	md        map[int][]mdBlock // 消息序号 → 解析好的 Markdown
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
	groups   []string          // 与 sessions 对应的分组：按时间分组时是 dayGroup 的代号，否则是文件夹（空串是不知道目录的会话）
	// folderOf 不为空时按文件夹分组，返回会话目录归到的组名
	folderOf func(cwd string) string
	// groupOpen 是点过的分组展开与否；没点过的只有第一组展开
	groupOpen map[string]bool
	hits      []history.Hit // 有搜索词时
	anyWord   bool          // 没有同时含所有词的结果，列出的是含任一词的
	status    historyStatus
	loaded    bool
	sel       string // 选中的会话（sessionKey）
	hitSeq    int    // 从搜索结果打开时命中的消息序号，-1 表示没有
	placed    bool   // 打开会话后是否已经定位过
	detail    *historyDetail
	tools     bool // 显示工具调用
	list      ui.ListState
	// resQuery、resAgent 是列表上这份结果对应的查询条件：新结果回来之前照旧显示它，
	// 不至于一敲字就闪出"没有找到"；换了条件的结果回来时列表回到顶部
	resQuery string
	resAgent int
	askedAt  time.Time // 最近一次请求发出（防抖结束）的时间
	failed   string    // 最近一次请求的错误
	messages ui.ListState

	mu      sync.Mutex
	req     int // 列表请求的序号，只采用最新一次的结果
	wQuery  string
	wAgent  int
	visible bool
	fetched time.Time
}

// setSessions 换上新的会话列表，顺便分好组：分组要比较日期或目录，不放在每帧里做。
// 按文件夹分组时同一组的排在一起，组按其中最近的会话排先后；组内都是最近的在前。
func (h *historyView) setSessions(ss []history.Session) {
	ss = slices.Clone(ss)
	// 换分组方式时传进来的是上一次排过的列表，先排回按时间
	slices.SortStableFunc(ss, func(x, y history.Session) int { return y.UpdatedAt.Compare(x.UpdatedAt) })
	groups := make([]string, len(ss))
	for i, s := range ss {
		if h.folderOf != nil {
			groups[i] = h.folderOf(s.Cwd)
		} else {
			groups[i] = dayGroup(s.UpdatedAt)
		}
	}
	if h.folderOf != nil {
		rank := map[string]int{}
		for _, g := range groups {
			if _, ok := rank[g]; !ok {
				rank[g] = len(rank)
			}
		}
		idx := make([]int, len(ss))
		for i := range idx {
			idx[i] = i
		}
		slices.SortStableFunc(idx, func(i, j int) int { return rank[groups[i]] - rank[groups[j]] })
		sorted, sortedGroups := make([]history.Session, len(ss)), make([]string, len(ss))
		for k, i := range idx {
			sorted[k], sortedGroups[k] = ss[i], groups[i]
		}
		ss, groups = sorted, sortedGroups
	}
	if len(ss) == 0 {
		ss, groups = nil, nil
	}
	h.sessions, h.groups = ss, groups
}

// setHistoryGrouping 按设置换分组方式（时间或文件夹），列表上已有的会话原地重新分组。
func (a *app) setHistoryGrouping() {
	h := &a.history
	h.folderOf = nil
	if a.state.HistoryByFolder {
		h.folderOf = a.folderGroup
	}
	h.groupOpen = nil
	h.setSessions(h.sessions)
}

// folderGroup 是按文件夹分组时会话归到的组：目录在哪个项目里（含它的 worktree）就是那个项目，
// 都不在就用目录名。
func (a *app) folderGroup(cwd string) string {
	if cwd == "" {
		return ""
	}
	name, best := "", -1
	match := func(root, label string) {
		if root != "" && len(root) > best && (cwd == root || strings.HasPrefix(cwd, strings.TrimSuffix(root, "/")+"/")) {
			name, best = label, len(root)
		}
	}
	for _, p := range a.projects {
		match(p.Path, p.Name)
	}
	for _, w := range a.workspaces {
		match(w.Path, w.Project)
	}
	if name == "" {
		name = filepath.Base(cwd)
	}
	return name
}

// pending 报告搜索框或筛选已经变了、对应的结果还没回来：列表上仍是上一次的结果。
func (h *historyView) pending() bool {
	return h.loaded && (h.resQuery != strings.TrimSpace(h.query) || h.resAgent != h.agent)
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
	h.askedAt = time.Now().Add(delay)
	time.AfterFunc(delay, func() { a.fetchHistory(req) })
}

func (a *app) fetchHistory(req int) {
	h := &a.history
	query, agentIdx, cur := h.want()
	if req != cur || a.c == nil {
		return
	}
	params := map[string]any{"query": strings.TrimSpace(query), "agent": historyAgents[agentIdx], "limit": historyLimit}
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
		a.win.Update(func() { h.failed = errtext.Text(err) })
		return
	}
	a.win.Update(func() {
		h.loaded, h.failed = true, ""
		h.status, h.anyWord = res.Status, res.Any
		// 列表按会话 ID 保持滚动位置，定期刷新时有用；换了搜索词或筛选就该从第一条看起
		if q := params["query"].(string); q != h.resQuery || agentIdx != h.resAgent {
			h.resQuery, h.resAgent = q, agentIdx
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

// historyGroupSwitch 选会话列表按时间还是按文件夹分组，记在 ui.json。
func (a *app) historyGroupSwitch(c *ui.Context) {
	sel := 0
	if a.state.HistoryByFolder {
		sel = 1
	}
	if segmented(c, &sel, L(c).HistoryGroupBy(), L(c).HistoryGroupByTime(), L(c).HistoryGroupByFolder()) {
		a.state.HistoryByFolder = sel == 1
		a.saveState()
		a.setHistoryGrouping()
		a.history.list.ScrollTo(0, ui.Start)
	}
}

// releaseHistory 放掉历史页的列表和打开的会话；还在路上的请求作废。
func (a *app) releaseHistory() {
	h := &a.history
	h.mu.Lock()
	h.req++
	h.mu.Unlock()
	h.sessions, h.groups, h.hits, h.detail = nil, nil, nil, nil
	h.sel, h.loaded, h.placed = "", false, false
	h.list, h.messages = ui.ListState{}, ui.ListState{}
}

func (a *app) setHistoryVisible(v bool) {
	h := &a.history
	h.mu.Lock()
	opened := v && !h.visible
	h.visible = v
	h.mu.Unlock()
	if opened {
		a.setHistoryGrouping()
		a.searchHistory(0)
	}
}

// selectSession 打开一个会话。按会话文件认：同一个 ID 可能是几个不同的会话（见 history.FindPath）。
func (a *app) selectSession(sess history.Session, seq int) {
	h := &a.history
	key := sessionKey(sess)
	h.sel, h.hitSeq, h.placed = key, seq, false
	if h.detail != nil && sessionKey(h.detail.Session) != key {
		h.detail = nil
	}
	go func() {
		var d historyDetail
		if a.call("history.show", map[string]any{"session": sess.ID, "path": sess.Path}, &d, 30*time.Second) != nil {
			return
		}
		s := d.Session
		d.context, d.contextOK = history.ReadContext(s.Agent, s.Path, s.Model)
		a.win.Update(func() {
			if h.sel == key {
				h.detail = &d
			}
		})
	}()
}

// resumeSession 在 posse 里继续会话；会话目录不在任何工作区时，问一句再把目录加成项目。
func (a *app) resumeSession(m locales.Messages, s history.Session) {
	go func() {
		var t struct {
			ID          string `json:"id"`
			WorkspaceID string `json:"workspaceId"`
		}
		err := a.call("history.resume", map[string]any{"session": s.ID, "cols": 120, "rows": 36}, &t, 30*time.Second)
		var re *rpc.Error
		if errors.As(err, &re) && re.Code == rpc.CodeNotFound && s.Cwd != "" && strings.Contains(re.Message, "no workspace contains") {
			a.win.Update(func() {
				a.confirm(m.HistoryAddProjectConfirm(shortPath(s.Cwd)),
					m.HistoryAddProjectBody(),
					m.HistoryAddAndContinue(), func() {
						go func() {
							if a.call("project.add", map[string]any{"path": s.Cwd}, nil, 10*time.Second) != nil {
								return
							}
							a.refresh()
							a.resumeSession(m, s)
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

// ago 是会话最后使用的时间，写成"20 分钟前"；分档和 elapsed 一样。
func ago(m locales.Messages, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return m.AppJustNow()
	case d < time.Hour:
		return m.AppMinutesAgo(int(d.Minutes()))
	case d < 48*time.Hour:
		return m.AppHoursAgo(int(d.Hours()))
	}
	return m.AppDaysAgo(int(d.Hours() / 24))
}

// agoInline 是嵌在句子里的 ago（"Connected just now"）：英文的"刚刚"在句中要小写。
func agoInline(m locales.Messages, t time.Time) string {
	if time.Since(t) < time.Minute {
		return m.AppJustNowInline()
	}
	return ago(m, t)
}

// sessionMeta 是会话卡片上的消息数和最近用的模型；常驻进程是旧版本时没有模型，只显示消息数。
func sessionMeta(c *ui.Context, s history.Session) {
	t := c.Theme()
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, L(c).HistoryMessageCount(s.Messages)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		if s.Model != "" {
			ui.Text(c, shortModel(s.Model)).FontSize(fsSmall).TextColor(t.TextMuted).SingleLine().Shrink(1).Tooltip(s.Model)
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

// resumeCommand 是在 posse 之外继续会话的命令，复制给用户自己用。
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

func (a *app) historySidebarItem(c *ui.Context) ui.Element {
	item := a.navTile(c, historyBoardID, iconHistory, L(c).HistoryTitle(), nil)
	if a.revealSel && a.selected == historyBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
	return item
}

func (a *app) historyBoard(c *ui.Context) {
	h := &a.history
	a.band(c, func() {
		titleText(c, L(c).HistoryTitle(), fsTitle).Margin(0, 6, 0, 0).Shrink(0)
		// 系统搜索框的占位文字固定是英文 Search，这里用普通输入框
		search := fieldInputSized(c, &h.query, L(c).HistorySearchPlaceholder(a.keyText("historySearch")), 340, 160).Label(L(c).HistorySearchLabel())
		if a.pressed(c, "historySearch") {
			search.Focus()
		}
		changed := search.Changed()
		if h.query != "" && search.Shortcut(0, ui.KeyEscape) {
			h.query, changed = "", true
		}
		if changed {
			a.searchHistory(250 * time.Millisecond)
		}
		a.historyGroupSwitch(c)
		ui.Spacer(c)
		if segmented(c, &h.agent, L(c).HistoryFilterByAgent(), historyAgentLabels(L(c))...) {
			a.searchHistory(0)
		}
		a.closeBoardButton(c)
	})
	searching := h.resQuery != ""
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(pageGap).Gap(pageGap).Children(func() {
		// 可以聚焦：点过列表后方向键归它，不被侧栏拿走
		list := card(c).Width(380).Shrink(0).Clip().Focusable().FocusRing(false).Label(L(c).HistorySessionList())
		list.Children(func() {
			a.historyTotals(c)
			a.historyNotice(c, searching)
			if searching {
				a.hitList(c)
			} else {
				a.sessionList(c)
			}
		})
		ui.Column(c).Grow(1).Shrink(1).Gap(pageGap).Children(func() { a.sessionDetail(c) })
	})
	// 上下方向键换选中的会话；焦点在搜索框里、列表上或哪儿都不在时都管用
	switch {
	case c.Shortcut(0, ui.KeyDown):
		a.stepSession(1)
	case c.Shortcut(0, ui.KeyUp):
		a.stepSession(-1)
	}
}

// stepSession 选中列表里看得见的下一个（step 为 1）或上一个会话，折起来的分组跳过；
// 还没选中时下键选第一个、上键选最后一个。到头了不动。
func (a *app) stepSession(step int) {
	h := &a.history
	type item struct {
		s   history.Session
		seq int
		row int // 在列表里是第几行
	}
	var items []item
	if h.resQuery != "" {
		for i, hit := range h.hits {
			seq := -1
			if hit.Role != "title" {
				seq = hit.Seq
			}
			items = append(items, item{hit.Session, seq, i})
		}
	} else {
		for i, r := range h.groupRows() {
			if r.session >= 0 {
				items = append(items, item{h.sessions[r.session], -1, i})
			}
		}
	}
	if len(items) == 0 {
		return
	}
	to := 0
	if step < 0 {
		to = len(items) - 1
	}
	for i, it := range items {
		if sessionKey(it.s) == h.sel {
			to = i + step
			break
		}
	}
	if to < 0 || to >= len(items) {
		return
	}
	a.selectSession(items[to].s, items[to].seq)
	h.list.ScrollIntoView(items[to].row)
}

// historyTotals 是列表顶上的总计，跟着筛选走：平时是会话数、消息数和各 agent 的会话数；
// 搜索时是匹配的会话数、命中的消息数和各 agent 的匹配数。
func (a *app) historyTotals(c *ui.Context) {
	t := c.Theme()
	h := &a.history
	st := h.status
	m := L(c)
	sessions, messages, byAgent := st.Sessions, m.HistoryMessageCount(st.Messages), st.ByAgent
	if h.resQuery != "" {
		byAgent = map[string]int{}
		n := 0
		for _, hit := range h.hits {
			byAgent[hit.Agent]++
			n += hit.Matches
		}
		sessions, messages = len(h.hits), m.HistoryMessageHits(n)
	} else if ag := historyAgents[h.resAgent]; ag != "" {
		sessions = st.ByAgent[ag]
	}
	// 没有结果时下面的空状态已经说明了
	if sessions == 0 {
		return
	}
	count := m.HistorySessionCount(sessions)
	if h.resQuery != "" {
		count = m.HistoryMatchCount(sessions)
		if sessions >= historyLimit {
			// 搜索只取最相关的前 historyLimit 个
			count = m.HistoryTopMatches(historyLimit)
		}
	}
	ui.Column(c).Padding(12, 20, 8, 20).Gap(8).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Text(c, count).FontSize(fsBody).FontWeight(fwMedium).FontFeatures("tnum")
			if h.resAgent == 0 || h.resQuery != "" {
				ui.Text(c, messages).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			}
		})
		if h.resAgent != 0 {
			return
		}
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for _, ag := range history.Agents {
				n := byAgent[ag]
				if n == 0 {
					continue
				}
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					agentTile(c, ag, smallIcon())
					ui.Text(c, thousands(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(agentTitle(ag))
				})
			}
		})
	})
	hline(c).Margin(0, 20)
}

// thousands 把整数写成带千分位的：12345 → 12,345。
func thousands(n int) string {
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// cwdLink 是会话目录，点一下在访达中打开。
func (a *app) cwdLink(c *ui.Context, dir string) func() {
	return func() {
		t := c.Theme()
		b := ui.Row(c.Key("cwd")).Gap(4).Shrink(1).Role(ui.RoleButton).Label(L(c).CommonOpenInFinder()).Tooltip(L(c).HistoryOpenInFinderTip(dir))
		color := t.TextMuted
		if b.Hovered() {
			color = t.Accent
		}
		b.Children(func() {
			ui.Icon(c, iconFolder).FontSize(fsBody).TextColor(color)
			ui.Text(c, shortPath(dir)).FontSize(fsSmall).Font(monoFont).TextColor(color).MaxLines(1).Shrink(1)
		})
		if b.Clicked() {
			if err := mygo.Shell.OpenPath(dir); err != nil {
				a.err = errtext.Text(err)
			}
		}
	}
}

// contextMeta 是会话最后一次请求占用的上下文比例；读不到用量的会话（grok、cursor）不显示。
func contextMeta(c *ui.Context, d *historyDetail) func() {
	if !d.contextOK {
		return nil
	}
	return func() {
		ctx := d.context
		ui.Text(c, L(c).HistoryContext(ctx.Percent())).FontSize(fsSmall).FontFeatures("tnum").TextColor(c.Theme().TextMuted).MaxLines(1).Shrink(0).
			Tooltip(L(c).HistoryContextTip(ctx.Used, ctx.Window))
	}
}

// historyNotice 是列表上方的一行说明：建索引进度、退而求其次的结果。
func (a *app) historyNotice(c *ui.Context, searching bool) {
	t := c.Theme()
	h := &a.history
	var msg string
	// 结果没回来时先照旧显示上一次的；超过 150ms 才提示，快的查询不闪
	waiting := false
	if h.pending() && h.failed == "" {
		if wait := time.Until(h.askedAt.Add(150 * time.Millisecond)); wait > 0 {
			c.After(wait)
		} else {
			waiting = true
		}
	}
	m := L(c)
	switch {
	case h.failed != "":
		msg = m.HistoryQueryFailed(h.failed)
	case waiting && strings.TrimSpace(h.query) != "":
		msg = m.HistorySearching()
	case waiting:
		msg = m.HistoryLoading()
	case h.status.Indexing && h.status.Progress.Total > 0:
		msg = m.HistoryIndexingProgress(fmt.Sprint(h.status.Progress.Done), fmt.Sprint(h.status.Progress.Total))
	case h.status.Indexing:
		msg = m.HistoryIndexing()
	case searching && h.anyWord:
		msg = m.HistoryAnyWord()
	case h.status.Error != "":
		msg = m.HistoryIndexFailed(h.status.Error)
	}
	if msg == "" {
		return
	}
	ui.Text(c, msg).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.4).Padding(12, 20, 2, 20)
}

func (a *app) sessionList(c *ui.Context) {
	t := c.Theme()
	h := &a.history
	if h.loaded && len(h.sessions) == 0 && !h.status.Indexing && !h.pending() {
		a.historyEmpty(c, L(c).HistoryEmptyTitle(), L(c).HistoryEmptyBody())
		return
	}
	// 按时间（dayGroup）或文件夹分组；默认只展开第一组，其余只列组标题和会话数
	rows := h.groupRows()
	// 列表里有"几分钟前"，界面不再每秒重建，自己约下一帧
	c.After(30 * time.Second)
	h.list.Key = func(i int) any {
		if r := rows[i]; r.session >= 0 {
			return sessionKey(h.sessions[r.session])
		}
		return "group:" + rows[i].group
	}
	fadeEdges(ui.List(c, &h.list, len(rows), func(i int) {
		r := rows[i]
		if r.session >= 0 {
			a.sessionRow(c, h.sessions[r.session], nil)
			return
		}
		open := h.isGroupOpen(r.group)
		label := cmp.Or(r.group, L(c).HistoryUnknownFolder())
		if h.folderOf == nil {
			label = dayGroupText(L(c), r.group)
		}
		head := ui.Row(c.Key("group:"+r.group)).Margin(0, 8).Padding(12, 12, 4, 8).Gap(4).Radius(rRow).AlignItems(ui.Center).
			Role(ui.RoleButton).Label(label)
		head.Children(func() {
			turn := float32(0)
			if open {
				turn = 90
			}
			chev := ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(t.TextMuted)
			chev.Rotate(chev.AnimateWith("turn", turn, 150*time.Millisecond, ui.EaseOut))
			ui.Text(c, label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			ui.Text(c, fmt.Sprint(r.count)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
		})
		if head.Clicked() {
			if h.groupOpen == nil {
				h.groupOpen = map[string]bool{}
			}
			h.groupOpen[r.group] = !open
		}
	}).Grow(1).Padding(4, 0, 8, 0).Gap(4), &h.list, colors(t).card)
}

// historyRow 是会话列表的一行：分组标题（session 为 -1）或一个会话。
type historyRow struct {
	group   string
	count   int // 分组里的会话数
	session int
}

func (h *historyView) isGroupOpen(g string) bool {
	if open, ok := h.groupOpen[g]; ok {
		return open
	}
	return len(h.groups) > 0 && g == h.groups[0]
}

// groupRows 列出分组标题和展开的分组里的会话。
func (h *historyView) groupRows() []historyRow {
	var rows []historyRow
	for i := 0; i < len(h.sessions); {
		g, j := h.groups[i], i
		for j < len(h.sessions) && h.groups[j] == g {
			j++
		}
		rows = append(rows, historyRow{group: g, count: j - i, session: -1})
		if h.isGroupOpen(g) {
			for k := i; k < j; k++ {
				rows = append(rows, historyRow{group: g, session: k})
			}
		}
		i = j
	}
	return rows
}

func (a *app) hitList(c *ui.Context) {
	h := &a.history
	if h.loaded && len(h.hits) == 0 && !h.status.Indexing && !h.pending() {
		a.historyEmpty(c, L(c).HistoryNoResults(h.resQuery), L(c).HistoryNoResultsBody())
		return
	}
	c.After(30 * time.Second)
	h.list.Key = func(i int) any { return sessionKey(h.hits[i].Session) }
	fadeEdges(ui.List(c, &h.list, len(h.hits), func(i int) {
		hit := h.hits[i]
		a.sessionRow(c, hit.Session, &hit)
	}).Grow(1).Padding(8, 0).Gap(4), &h.list, colors(c.Theme()).card)
}

func (a *app) historyEmpty(c *ui.Context, title, body string) {
	t := c.Theme()
	ui.Column(c).Padding(24, 20).Gap(6).Children(func() {
		ui.Text(c, title).FontSize(fsBody).FontWeight(fwMedium)
		ui.Text(c, body).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
	})
}

// sessionRow 是列表里的一个会话；搜索时多一行命中的摘要。
func (a *app) sessionRow(c *ui.Context, s history.Session, hit *history.Hit) {
	t := c.Theme()
	h := &a.history
	row := ui.Row(c.Key(sessionKey(s))).Margin(0, 8).Padding(8, 12).Gap(12).Radius(rControl).AlignItems(ui.Center)
	selectedBg(c, row, h.sel == sessionKey(s))
	row.Children(func() {
		agentTile(c, s.Agent, tileMedium())
		ui.Column(c).Gap(2).Grow(1).Shrink(1).Children(func() {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, oneLine(s.Title)).FontSize(fsBody).FontWeight(fwMedium).MaxLines(1).Grow(1).Shrink(1)
				ui.Text(c, ago(L(c), s.UpdatedAt)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			if s.Cwd != "" {
				ui.Text(c, shortPath(s.Cwd)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1)
			}
			sessionMeta(c, s)
			// 只有标题命中时，标题本身就是证据
			if hit != nil && hit.Role != "title" {
				snippetText(c, *hit).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2).LineHeight(1.4).Padding(2, 0, 0, 0)
			}
		})
	})
	if row.Clicked() {
		seq := -1
		if hit != nil && hit.Role != "title" {
			seq = hit.Seq
		}
		a.selectSession(s, seq)
	}
}

// snippetText 显示命中摘要，命中的词用正文色加粗，其余是次要色。
func snippetText(c *ui.Context, hit history.Hit) ui.Element {
	t := c.Theme()
	spans := []ui.Span{{Text: L(c).HistorySnippetRole(roleName(L(c), hit.Role, hit.Agent))}}
	last := 0
	for _, sp := range hit.Spans {
		if sp.Start < last || sp.End > len(hit.Snippet) {
			continue
		}
		spans = append(spans, ui.Span{Text: hit.Snippet[last:sp.Start]}, ui.Span{Text: hit.Snippet[sp.Start:sp.End], Color: t.Text, Weight: fwBold})
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
			emptyCard(c).MaxWidth(420).Padding(24).Gap(12).Children(func() {
				tile(c, iconHistory, 48, colors(t).tint, t.Accent)
				ui.Column(c).Gap(6).Children(func() {
					titleText(c, L(c).HistoryIntroTitle(), fsPage)
					ui.Text(c, L(c).HistoryIntroBody()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				})
				if n := h.status.Sessions; n > 0 {
					ui.Text(c, L(c).HistoryIndexedCount(n)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
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
	card(c).Padding(cardPad).Gap(12).Shrink(0).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
			agentTile(c, s.Agent, tileLarge())
			ui.Column(c).Gap(8).Grow(1).Shrink(1).Children(func() {
				titleText(c, oneLine(s.Title), fsPage).LineHeight(1.3).MaxLines(2).Selectable()
				var cwd, branch func()
				if s.Cwd != "" {
					cwd = a.cwdLink(c, s.Cwd)
				}
				if s.Branch != "" {
					branch = metaMono(c, iconBranch, s.Branch, "")
				}
				metaRow(c, meta(c, agentTitle(s.Agent)), cwd, branch, meta(c, L(c).AppDateTime(s.UpdatedAt.Local())), meta(c, L(c).HistoryMessageCount(s.Messages)), contextMeta(c, d))
			})
		})
		// 放不下时折行（英文），"显示工具调用"靠右
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if d.Terminal != "" {
				if pressable(accentButton(c, L(c).HistorySwitchToTerminal())).Clicked() {
					if w, _ := a.findTerminal(d.Terminal); w != nil {
						a.openTerminal(w.ID, d.Terminal)
					}
				}
			} else if pressable(accentButton(c, L(c).HistoryContinueInPosse())).Tooltip(resumeCommand(s)).Clicked() {
				a.resumeSession(L(c), s)
			}
			if pressable(textButton(c, L(c).HistoryCopyResumeCommand())).Tooltip(resumeCommand(s)).Clicked() {
				c.WriteClipboard(resumeCommand(s))
			}
			if pressable(textButton(c, L(c).CommonMoveToTrash())).Clicked() {
				a.askTrashSession(L(c), d)
			}
			ui.Row(c).Grow(1).Justify(ui.End).Children(func() {
				ui.Checkbox(c, &h.tools, L(c).HistoryShowToolCalls())
			})
		})
	})
	msgs := d.visible(h.tools, h.hitSeq)
	if len(msgs) == 0 {
		card(c).Children(func() {
			a.historyEmpty(c, L(c).HistoryNoMessages(), L(c).HistoryNoMessagesBody())
		})
		return
	}
	ns := queryWords(h.resQuery)
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
	// 对话装进卡片，和上面的会话信息一样一层底色，和画布分得开；铺满卡片宽，两边只留卡片内边距
	card(c).Grow(1).Shrink(1).Clip().Children(func() {
		transcript := ui.List(c, &h.messages, len(msgs), func(i int) {
			it := msgs[i]
			if it.tools != nil {
				a.toolRun(c, d, it.tools)
				return
			}
			a.messageView(c, s, it.msg, ns, it.msg.Seq == h.hitSeq)
		}).Grow(1).Padding(cardPad-8, 0, 32, 0).Gap(6) // 第一条消息框上内边距 8，文字离卡片顶也是 cardPad
		fadeEdges(transcript, &h.messages, colors(t).card)
	})
}

// transcriptInset 是消息框离对话卡片边的距离：框自己有 12 的内边距，两者加起来文字离卡片边 cardPad，和上面会话信息卡片的文字对齐。
const transcriptInset = cardPad - 12

// transcriptWidth 是对话的版心宽度：默认字号下一行四十几个汉字，随 Markdown 字号等比放宽。
func (a *app) transcriptWidth() float32 { return 680 * a.mdSize() / defaultEditorFontSize }

// toolRun 是折起来的一串工具调用：点一下展开或收起。
func (a *app) toolRun(c *ui.Context, d *historyDetail, run []history.Message) {
	t := c.Theme()
	open := d.opened[run[0].Seq]
	row := ui.Row(c.Key("tools")).Height(sz(26)).Margin(0, transcriptInset).Padding(0, 12).Gap(6).Radius(rControl).AlignItems(ui.Center).AlignSelf(ui.Start).Role(ui.RoleButton)
	selectedBg(c, row, false)
	row.Children(func() {
		turn := float32(0)
		if open {
			turn = 90
		}
		ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(t.TextMuted).Rotate(turn)
		ui.Text(c, L(c).HistoryToolCalls(len(run))).FontSize(fsSmall).TextColor(t.TextMuted)
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
	who := roleName(L(c), m.Role, s.Agent)
	whoColor := t.TextMuted
	if m.Role == "user" {
		whoColor = t.Accent
	}
	// agent 的回复不装框，直接铺开；命中搜索的那条铺一层琥珀淡底
	box := ui.Column(c).Margin(0, transcriptInset).Padding(8, 12).Gap(6).Radius(rCard)
	switch {
	case hit:
		box.Background(soft(t, colors(t).honey))
	case m.Role == "user":
		// 自己说的话靠右、强调色淡底，一眼和 agent 的回复分开
		box.Background(colors(t).tint).AlignSelf(ui.End).MaxWidth(a.transcriptWidth() * 0.8)
	}
	box.Children(func() {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, who).FontSize(fsSmall).FontWeight(fwMedium).TextColor(whoColor)
			if !m.At.IsZero() {
				ui.Text(c, L(c).AppDateTime(m.At.Local())).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			}
		})
		text := strings.TrimSpace(m.Text)
		switch m.Role {
		case "tool":
			highlighted(c, text, ns).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(8).LineHeight(1.5).Selectable()
		case "assistant":
			// agent 的回复按 Markdown 显示；用户自己打的字原样显示
			ui.Column(c).Selectable().Children(func() {
				mdView{words: ns, size: a.mdSize()}.page(c, a.markdown(m))
			})
		default:
			highlighted(c, text, ns).FontSize(a.mdSize()).LineHeight(1.55).Selectable()
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
	return ui.RichText(c, markSpans(text, marks, ui.Span{}, colors(c.Theme()).mark)...)
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

// roleName 是对话里说话的一方：你、agent 的名字或工具。
func roleName(m locales.Messages, role, agent string) string {
	switch role {
	case "user":
		return m.HistoryRoleUser()
	case "assistant":
		return agentTitle(agent)
	case "tool":
		return m.HistoryRoleTool()
	}
	return ""
}

// 按时间分组时的组名是代号，显示时由 dayGroupText 按界面语言写：切语言不会让点过的展开状态对不上。
const (
	groupToday     = "today"
	groupYesterday = "yesterday"
	groupWeek      = "week"
	groupMonth     = "month"
	groupOlder     = "older"
)

func dayGroup(t time.Time) string {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch {
	case !t.Before(today):
		return groupToday
	case !t.Before(today.AddDate(0, 0, -1)):
		return groupYesterday
	case !t.Before(today.AddDate(0, 0, -7)):
		return groupWeek
	case !t.Before(today.AddDate(0, 0, -30)):
		return groupMonth
	}
	return groupOlder
}

func dayGroupText(m locales.Messages, g string) string {
	switch g {
	case groupToday:
		return m.AppToday()
	case groupYesterday:
		return m.AppYesterday()
	case groupWeek:
		return m.HistoryGroupLast7Days()
	case groupMonth:
		return m.HistoryGroupLast30Days()
	}
	return m.HistoryGroupOlder()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
