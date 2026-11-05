package app

// 讨论页（docs/features/discussions.md）：左边是讨论列表，中间是消息和输入区，右边可收起的一栏是详情、成员和关键消息。
// 数据都在常驻进程；窗口只轮询列表的变更序号，打开的讨论按版本取增量，历史分页往前取，不一次读进全部消息。

import (
	"cmp"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/locales"
)

// discBoardID 是侧栏「讨论」一项的 ID；工作区 ID 都以 w_ 开头，不会冲突。
const discBoardID = "discussions"

// 一页消息
const discPage = 50

// --- 数据

type discSummary struct {
	Key, Host, LocalID, Title, Description, ProjectID, ProjectName, ProjectPath, WorkspaceID, Status string
	Rev, MsgSeq, Changed                                                                             int64
	CreatedAt                                                                                        time.Time
	ArchivedAt                                                                                       *time.Time
	Last                                                                                             *discMessage
	Questions, Active, Pending, Members                                                              int
}

type discMember struct {
	ID, Discussion, Name, Role, Agent, Model, Effort, WorkspaceID, WorkspaceLabel, TaskID, Worker, Paused, Path string
	ConfigVersion, WorkerConfig, RecvSeq, Rev                                                                   int64
	RemovedAt                                                                                                   *time.Time
	Terminal                                                                                                    *terminalInfo
}

type discAttachment struct {
	ID, Name, Path, Type string
	Size                 int64
}

type discMessage struct {
	ID, Discussion, Kind, AuthorMember, AuthorName, AuthorAgent, Body, Quote, Target, TargetName, Execution, Question, Report, Outcome string
	Seq, Rev                                                                                                                           int64
	Attachments                                                                                                                        []discAttachment
	KeyAt                                                                                                                              *time.Time
	CreatedAt                                                                                                                          time.Time
}

type discExec struct {
	ID, Discussion, Member, Message, RetryOf, State, Reason, TaskID, Worker, Agent, Model, Effort, Role, WorkspaceID, WorkspaceLabel, Reply string
	ConfigVersion, Rev                                                                                                                      int64
	Confirmed, Terminal                                                                                                                     bool
	ContextLen, Acked                                                                                                                       int
	CreatedAt                                                                                                                               time.Time
	StartedAt, EndedAt                                                                                                                      *time.Time
}

type discList struct {
	Seq   int64
	Items []discSummary
}

type discView struct {
	Discussion discSummary
	Members    []discMember
	Messages   []discMessage
	Executions []discExec
	Key        []discMessage
	More       bool
	Delta      bool
}

// discPoll 是一次轮询拿到的讨论数据：列表（序号没变时 Items 为空）和打开的讨论（全量或增量）。
type discPoll struct {
	list  *discList
	view  *discView
	key   string
	since int64
}

// discDetail 是打开的讨论在窗口里的样子：只有读进来的那几页消息。
type discDetail struct {
	key     string
	info    discSummary
	members []discMember
	msgs    []discMessage // 按序号升序
	execs   map[string]discExec
	keyMsgs []discMessage
	more    bool
	rev     int64
	md      map[string][]mdBlock // 消息 ID → 解析好的 Markdown（正文不会改，解析一次）
}

func (d *discDetail) msg(id string) *discMessage {
	for i := range d.msgs {
		if d.msgs[i].ID == id {
			return &d.msgs[i]
		}
	}
	return nil
}

func (d *discDetail) member(id string) *discMember {
	for i := range d.members {
		if d.members[i].ID == id {
			return &d.members[i]
		}
	}
	return nil
}

// execFor 是这条消息最近的一轮执行（重试过的取最后一轮）。
func (d *discDetail) execFor(msgID string) *discExec {
	var found *discExec
	for _, x := range d.execs {
		if x.Message == msgID && (found == nil || x.CreatedAt.After(found.CreatedAt)) {
			x := x
			found = &x
		}
	}
	return found
}

// merge 把增量并进来：消息按 ID 替换或按序号插入，执行、成员按 ID 替换，关键消息跟着消息的标记增减。
func (d *discDetail) merge(v discView) {
	d.info = v.Discussion
	for _, m := range v.Messages {
		if i := slices.IndexFunc(d.msgs, func(x discMessage) bool { return x.ID == m.ID }); i >= 0 {
			d.msgs[i] = m
		} else if len(d.msgs) == 0 || m.Seq > d.msgs[0].Seq || !d.more {
			d.msgs = append(d.msgs, m)
		}
		k := slices.IndexFunc(d.keyMsgs, func(x discMessage) bool { return x.ID == m.ID })
		switch {
		case m.KeyAt != nil && k < 0:
			d.keyMsgs = append(d.keyMsgs, m)
		case m.KeyAt != nil:
			d.keyMsgs[k] = m
		case k >= 0:
			d.keyMsgs = slices.Delete(d.keyMsgs, k, k+1)
		}
	}
	slices.SortFunc(d.msgs, func(a, b discMessage) int { return cmp.Compare(a.Seq, b.Seq) })
	slices.SortFunc(d.keyMsgs, func(a, b discMessage) int { return cmp.Compare(a.Seq, b.Seq) })
	for _, x := range v.Executions {
		d.execs[x.ID] = x
	}
	for _, m := range v.Members {
		if i := slices.IndexFunc(d.members, func(x discMember) bool { return x.ID == m.ID }); i >= 0 {
			d.members[i] = m
		} else {
			d.members = append(d.members, m)
		}
	}
	d.rev = v.Discussion.Rev
}

func newDiscDetail(v discView) *discDetail {
	d := &discDetail{key: v.Discussion.Key, info: v.Discussion, msgs: v.Messages, members: v.Members, keyMsgs: v.Key, more: v.More,
		rev: v.Discussion.Rev, execs: map[string]discExec{}, md: map[string][]mdBlock{}}
	for _, x := range v.Executions {
		d.execs[x.ID] = x
	}
	return d
}

// discBoard 是讨论页在窗口里的状态。
type discBoard struct {
	list   []discSummary
	sel    string
	d      *discDetail
	scroll ui.ScrollState
	follow bool  // 停在底部：新消息来了跟着滚
	seen   int64 // 用户看到过的最新序号（滚到底时更新），比它新的算新消息提示
	// reveal 是下一帧要滚进视野的消息（滚过就清掉，revealed 记下最后滚到的那条）；
	// flash 是跳过去后高亮两秒的消息（搜索结果、关键消息、引用）
	reveal   string
	revealed string
	flash    string
	flashAt  time.Time
	older  bool // 正在取更早的一页

	// 输入区
	text     string
	target   string // @ 的成员 ID
	quote    string // 引用的消息 ID
	atts     []string
	answerQ  string // 正在回答的问题（任务消息 ID）
	clientID string // 这条草稿的请求 ID：发送失败重试时沿用，常驻进程据此去重
	sending  bool

	panel   bool // 右边一栏开着
	search  string
	hits    []discMessage
	searchQ string

	newOpen  bool
	newTitle string
	newDesc  string
	newProj  string
	newWS    string

	form memberForm
	edit discEditForm
}

type memberForm struct {
	open     bool
	id       string // 编辑的成员，空是新加
	name     string
	role     string
	agent    int
	model    string
	effort   string
	ws       string
	mode     int // 0 用已有工作区，1 新建 worktree
	worktree string
	applyNow bool
}

type discEditForm struct {
	open        bool
	title, desc string
	ws          string
}

// --- 轮询

// wantedDisc 是轮询要取详情的讨论和它已有的版本（0 表示要全量）。
func (a *app) wantedDisc() (string, int64) {
	a.discMu.Lock()
	defer a.discMu.Unlock()
	return a.discWant, a.discWantRev
}

func (a *app) setWantedDisc(key string, rev int64) {
	a.discMu.Lock()
	a.discWant, a.discWantRev = key, rev
	a.discMu.Unlock()
}

// pollDiscussions 在 refresh 里调用：列表只在变更序号变了时才取全；打开的讨论按版本取增量。
func (a *app) pollDiscussions(r *pollResult, call func(method string, params, out any) error) {
	var l discList
	if call("discussion.list", map[string]any{"since": a.discSeq.Load()}, &l) != nil {
		return
	}
	p := discPoll{}
	if l.Items != nil {
		p.list = &l
	}
	if key, since := a.wantedDisc(); key != "" {
		var v discView
		if call("discussion.show", map[string]any{"discussion": key, "since": since, "limit": discPage}, &v) == nil {
			p.view, p.key, p.since = &v, key, since
		}
	}
	if p.list != nil || p.view != nil && (!p.view.Delta || len(p.view.Messages)+len(p.view.Executions)+len(p.view.Members) > 0) {
		r.disc = &p
	}
}

// applyDiscussions 在主线程里把轮询结果交给界面，并为没在看的讨论里的问题和回复发通知。
func (a *app) applyDiscussions(p *discPoll) {
	if p == nil {
		return
	}
	b := &a.disc
	if p.list != nil {
		a.discSeq.Store(p.list.Seq)
		old := map[string]discSummary{}
		for _, s := range b.list {
			old[s.Key] = s
		}
		b.list = sortDiscussions(p.list.Items)
		if a.discLoaded {
			for _, s := range b.list {
				a.discNotice(old[s.Key], s)
			}
		}
		a.discLoaded = true
		if b.sel != "" && a.discussion(b.sel) == nil {
			b.sel, b.d = "", nil
			a.setWantedDisc("", 0)
		}
	}
	if v := p.view; v != nil && p.key == b.sel {
		switch {
		case !v.Delta:
			b.d = newDiscDetail(*v)
			b.follow = true
		case b.d != nil && b.d.key == p.key && b.d.rev == p.since:
			b.d.merge(*v)
		}
		if b.d != nil {
			a.setWantedDisc(b.sel, b.d.rev)
		}
	}
}

// discNotice 发系统通知：成员提了问题、回复了，你没在看这个讨论时。同一次汇报不再经任务发第二次。
func (a *app) discNotice(prev, cur discSummary) {
	if prev.Key == "" || a.state.NotifyOff || cur.Last == nil || prev.MsgSeq == cur.MsgSeq {
		return
	}
	if a.win != nil && a.win.IsFocused() && a.selected == discBoardID && a.disc.sel == cur.Key {
		return
	}
	m := appL()
	var title string
	switch {
	case cur.Last.Kind == "question" && cur.Questions > prev.Questions:
		title = m.DiscNoticeQuestion(cur.Last.AuthorName)
	case cur.Last.Kind == "reply" && cur.Last.Outcome == "failed":
		title = m.DiscNoticeFailed(cur.Last.AuthorName)
	case cur.Last.Kind == "reply":
		title = m.DiscNoticeReply(cur.Last.AuthorName)
	default:
		return
	}
	n := mygo.NewNotification(mygo.NotificationOptions{ID: "posse-disc:" + cur.Key, Title: title, Body: cur.Title, Group: "discussions"})
	go func() {
		if err := n.Show(); err != nil {
			log.Printf("notification: %v", err)
		}
	}()
}

// sortDiscussions：进行中的在前、按最近消息排，归档的放最后。
func sortDiscussions(in []discSummary) []discSummary {
	out := slices.Clone(in)
	last := func(s discSummary) time.Time {
		if s.Last != nil {
			return s.Last.CreatedAt
		}
		return s.CreatedAt
	}
	slices.SortStableFunc(out, func(x, y discSummary) int {
		if ax, ay := x.Status == "archived", y.Status == "archived"; ax != ay {
			if ax {
				return 1
			}
			return -1
		}
		return last(y).Compare(last(x))
	})
	return out
}

func (a *app) discussion(key string) *discSummary {
	for i := range a.disc.list {
		if a.disc.list[i].Key == key {
			return &a.disc.list[i]
		}
	}
	return nil
}

// discUnread 是讨论里你还没看过的消息数（阅读位置记在 ui.json，和 agent 接收无关）。
func (a *app) discUnread(s discSummary) int {
	read, ok := a.state.DiscRead[s.Key]
	if !ok {
		// 没打开过的讨论：自己建的、只有自己发言的不算未读
		if s.Last == nil || s.Last.Kind == "user" || s.Last.Kind == "answer" {
			return 0
		}
	}
	return int(max(0, s.MsgSeq-read))
}

func (a *app) markDiscRead(key string, seq int64) {
	if a.state.DiscRead == nil {
		a.state.DiscRead = map[string]int64{}
	}
	if a.state.DiscRead[key] >= seq {
		return
	}
	a.state.DiscRead[key] = seq
	a.saveState()
}

func (a *app) openDiscussion(key string) {
	a.selected, a.revealSel = discBoardID, true
	a.selectDiscussion(key)
}

func (a *app) selectDiscussion(key string) {
	b := &a.disc
	if b.sel == key && b.d != nil {
		return
	}
	*b = discBoard{list: b.list, panel: b.panel, newOpen: b.newOpen}
	b.sel = key
	a.setWantedDisc(key, 0)
	a.pollForce.Store(true)
	if !a.refreshQueued.Swap(true) {
		go a.refresh()
	}
}

// discCounts 是侧栏磁贴的标记：等你回答的问题数和未读的讨论数。
func (a *app) discCounts() (questions, unread int) {
	for _, s := range a.disc.list {
		if s.Status == "archived" {
			continue
		}
		questions += s.Questions
		if a.discUnread(s) > 0 {
			unread++
		}
	}
	return
}

// --- 侧栏入口

func (a *app) discSidebarItem(c *ui.Context) ui.Element {
	t := c.Theme()
	questions, unread := a.discCounts()
	var badge func()
	if questions > 0 || unread > 0 {
		badge = func() {
			if questions > 0 {
				statusGlyph(c, agent.StatusWaiting, smallIcon())
				return
			}
			ui.Text(c, fmt.Sprint(unread)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.Accent).Tooltip(L(c).DiscUnreadTip(unread))
		}
	}
	item := a.navTile(c, discBoardID, iconDiscussion, L(c).DiscTitle(), badge)
	if a.revealSel && a.selected == discBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
	return item
}

// --- 页面

func (a *app) discBoard(c *ui.Context) {
	b := &a.disc
	m := L(c)
	if b.sel != "" {
		_, rev := a.wantedDisc()
		if b.d != nil {
			rev = b.d.rev
		}
		a.setWantedDisc(b.sel, rev)
	}
	a.band(c, func() {
		titleText(c, m.DiscTitle(), fsTitle)
		ui.Spacer(c)
		if len(a.projects) > 0 && len(b.list) > 0 && pressable(accentButton(c, m.DiscNewEllipsis())).Clicked() {
			a.openNewDiscussion()
		}
		a.closeBoardButton(c)
	})
	if len(b.list) == 0 {
		a.discEmpty(c)
		return
	}
	if b.sel == "" || a.discussion(b.sel) == nil {
		a.selectDiscussion(b.list[0].Key)
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Padding(pageGap).Gap(pageGap).Children(func() {
		card(c).Width(260).Shrink(0).Clip().Label(m.DiscList()).Children(func() {
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Padding(4, 0, 8, 0).Gap(4).Children(func() { a.discListView(c) })
			})
		})
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Children(func() {
			if b.d == nil || b.d.key != b.sel {
				ui.Column(c).Grow(1).Center().Children(func() {
					ui.Text(c, m.CommonLoading()).FontSize(fsBody).TextColor(c.Theme().TextMuted)
				})
				return
			}
			a.discDetailView(c, b.d)
		})
		if b.panel && b.d != nil && b.d.key == b.sel {
			card(c).Width(260).Shrink(0).Clip().Children(func() { a.discPanel(c, b.d) })
		}
	})
}

func (a *app) discEmpty(c *ui.Context) {
	t, m := c.Theme(), L(c)
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).MaxWidth(460).Padding(24).Gap(16).Children(func() {
			tile(c, iconDiscussion, 48, colors(t).tint, t.Accent)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, m.DiscEmptyTitle(), fsPage)
				ui.Text(c, m.DiscEmptyBody()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			ui.Row(c).Gap(8).Children(func() {
				btn := accentButton(c, m.DiscNewEllipsis()).Disabled(len(a.projects) == 0)
				if pressable(btn).Clicked() && len(a.projects) > 0 {
					a.openNewDiscussion()
				}
			})
		})
	})
}

func (a *app) discListView(c *ui.Context) {
	t, m := c.Theme(), L(c)
	c.After(30 * time.Second)
	archived := false
	for _, s := range a.disc.list {
		if s.Status == "archived" && !archived {
			archived = true
			ui.Text(c.Key("section-archived"), m.DiscSectionArchived()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(12, 20, 4, 20)
		}
		a.discRow(c, s)
	}
}

func (a *app) discRow(c *ui.Context, s discSummary) {
	t, m := c.Theme(), L(c)
	row := ui.Row(c.Key(s.Key)).Margin(0, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Start)
	selectedBg(c, row, a.disc.sel == s.Key)
	unread := a.discUnread(s)
	row.Children(func() {
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, s.Title).FontSize(fsBody).FontWeight(fwMedium).MaxLines(1).Shrink(1).MinWidth(0)
				ui.Spacer(c)
				at := s.CreatedAt
				if s.Last != nil {
					at = s.Last.CreatedAt
				}
				ui.Text(c, elapsed(m, at)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Icon(c, iconFolder).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Text(c, s.ProjectName).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
				ui.Spacer(c)
				switch {
				case s.Questions > 0:
					statusGlyph(c, agent.StatusWaiting, fsSmall)
				case s.Active > 0:
					statusGlyph(c, agent.StatusWorking, fsSmall)
				}
				if unread > 0 {
					ui.Text(c, fmt.Sprint(unread)).FontSize(fsSmall).FontFeatures("tnum").FontWeight(fwMedium).TextColor(t.Accent).Tooltip(m.DiscUnreadTip(unread))
				}
			})
			preview, color := m.DiscNoMessages(), t.TextMuted
			if s.Last != nil {
				preview = discPreview(m, *s.Last)
			}
			if s.Questions > 0 {
				preview, color = m.DiscQuestionsWaiting(s.Questions), colors(t).honey
			}
			ui.Text(c, preview).FontSize(fsSmall).TextColor(color).MaxLines(2).LineHeight(1.35)
		})
	})
	if row.Clicked() {
		a.selectDiscussion(s.Key)
	}
}

func discPreview(m locales.Messages, msg discMessage) string {
	body := strings.Join(strings.Fields(msg.Body), " ")
	if body == "" && len(msg.Attachments) > 0 {
		body = msg.Attachments[0].Name
	}
	switch msg.Kind {
	case "reply", "question":
		return msg.AuthorName + m.DiscColon() + body
	}
	return m.DiscYou() + m.DiscColon() + body
}

// --- 详情

func (a *app) discDetailView(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	archived := d.info.Status == "archived"
	ui.Column(c).Grow(1).MinHeight(0).Gap(pageGap).Children(func() {
		// 顶栏：标题、项目、搜索、更多
		card(c).Padding(12, cardPad).Gap(8).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						titleText(c, d.info.Title, fsTitle).MaxLines(1).Shrink(1).Selectable()
						if archived {
							ui.Text(c, m.DiscArchivedBadge()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).
								Padding(2, 6).Radius(rSmall).Background(colors(t).fill).Shrink(0)
						}
					})
					metaRow(c, metaMono(c, iconFolder, d.info.ProjectName, d.info.ProjectPath), meta(c, m.DiscMemberCount(len(activeMembers(d)))))
				})
				if iconButton(c.Key("disc-search"), iconSearch, m.DiscSearch()).Clicked() {
					b.panel = true
					b.searchQ = "search"
				}
				panelTip := m.DiscShowPanel()
				if b.panel {
					panelTip = m.DiscHidePanel()
				}
				if iconButton(c.Key("disc-panel"), iconPanelRight, panelTip).Clicked() {
					b.panel = !b.panel
					b.searchQ = ""
				}
				more := iconButton(c.Key("disc-more"), iconMore, m.DiscMore())
				menuButton(c, more, func(pm *popMenu) {
					if !archived && pm.Item(m.DiscEditEllipsis()).Icon(iconPencil).Chosen() {
						b.edit = discEditForm{open: true, title: d.info.Title, desc: d.info.Description, ws: a.wsLabel(d.info.WorkspaceID)}
					}
					if !archived && pm.Item(m.DiscAddMemberEllipsis()).Icon(iconPlus).Chosen() {
						a.openMemberForm(d, "")
					}
					pm.Separator()
					if archived {
						if pm.Item(m.CommonRestore()).Icon(iconRollback).Chosen() {
							a.discArchive(d.key, false)
						}
						if pm.Item(m.DiscCleanAttachmentsEllipsis()).Icon(iconTrash).Chosen() {
							go a.askCleanAttachments(m, d.key)
						}
					} else if pm.Item(m.DiscArchive()).Icon(iconArchive).Chosen() {
						a.askArchive(m, d)
					}
				})
			})
		})
		// 消息区
		card(c).Grow(1).MinHeight(0).Clip().Children(func() { a.discMessages(c, d) })
		// 输入区
		if archived {
			card(c).Padding(cardPad).Gap(8).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, m.DiscArchivedNote()).FontSize(fsBody).TextColor(t.TextMuted).Grow(1)
					if pressable(textButton(c, m.CommonRestore())).Clicked() {
						a.discArchive(d.key, false)
					}
				})
			})
			return
		}
		a.discPausedBanner(c, d)
		a.discComposer(c, d)
	})
}

func activeMembers(d *discDetail) []discMember {
	var out []discMember
	for _, m := range d.members {
		if m.RemovedAt == nil {
			out = append(out, m)
		}
	}
	return out
}

func (a *app) wsLabel(id string) string {
	if w := a.workspace(id); w != nil {
		return w.Label
	}
	return ""
}

// discMessages 画消息列表：停在底部时跟着新消息走；往上翻着时不动，底部给一个新消息提示。
func (a *app) discMessages(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	s := &b.scroll
	atBottom := s.MaxY <= 0 || s.Y >= s.MaxY-4
	last := int64(0)
	if len(d.msgs) > 0 {
		last = d.msgs[len(d.msgs)-1].Seq
	}
	if b.follow && b.reveal == "" {
		s.Y = math.MaxFloat32
	}
	if atBottom && last > 0 {
		b.seen = max(b.seen, last)
		if a.win != nil && a.win.IsFocused() {
			a.markDiscRead(d.key, d.info.MsgSeq)
		}
	}
	b.follow = atBottom
	box := ui.Box(c).Grow(1).MinHeight(0)
	box.Children(func() {
		ui.Scroll(c).Fill().TrackScroll(s).Children(func() {
			ui.Column(c).Padding(12, cardPad, 16, cardPad).Gap(16).Children(func() {
				if d.more {
					ui.Row(c.Key("older")).Justify(ui.Center).Children(func() {
						label := m.DiscLoadOlder()
						if b.older {
							label = m.CommonLoading()
						}
						if pressable(textButton(c, label)).Clicked() && !b.older {
							a.loadOlder(d.key, 0, "")
						}
					})
				}
				if len(d.msgs) == 0 {
					ui.Text(c, m.DiscNoMessagesHint()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
				}
				for _, msg := range d.msgs {
					a.discMessageRow(c, d, msg)
				}
			})
		})
		if fresh := countAfter(d.msgs, b.seen); fresh > 0 && !atBottom {
			ui.Row(c).Absolute().Bottom(12).Left(0).Right(0).Justify(ui.Center).Children(func() {
				pill := ui.Row(c.Key("new-pill")).Gap(6).Padding(6, 12).Radius(full(28)).Background(t.Accent).AlignItems(ui.Center).Role(ui.RoleButton)
				pill.Children(func() {
					ui.Icon(c, iconArrowDown).FontSize(fsSmall).TextColor(white)
					ui.Text(c, m.DiscNewMessages(fresh)).FontSize(fsSmall).FontWeight(fwMedium).TextColor(white)
				})
				elevFloat(c, pill)
				if pill.Clicked() {
					b.follow = true
					s.ScrollTo(0, math.MaxFloat32, ui.Smooth)
				}
			})
		}
	})
	if b.flash != "" && time.Since(b.flashAt) > 2*time.Second {
		b.flash = ""
	} else if b.flash != "" {
		c.After(time.Second)
	}
}

func countAfter(msgs []discMessage, seq int64) int {
	n := 0
	for _, m := range msgs {
		if m.Seq > seq {
			n++
		}
	}
	return n
}

func (a *app) discMessageRow(c *ui.Context, d *discDetail, msg discMessage) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	archived := d.info.Status == "archived"
	row := ui.Row(c.Key(msg.ID)).Gap(12).AlignItems(ui.Start).Padding(6, 8).Radius(rControl)
	if b.reveal == msg.ID {
		row.ScrollIntoView()
		b.reveal, b.revealed = "", msg.ID
	}
	if b.flash == msg.ID {
		row.Background(colors(t).mark)
	} else if row.Hovered() {
		row.Background(colors(t).wash)
	}
	hovered := row.Hovered()
	row.Children(func() {
		// 头像：成员是它的 agent 图标，用户是一个字
		switch msg.Kind {
		case "reply", "question":
			agentTile(c, msg.AuthorAgent, tileMedium())
		default:
			ui.Box(c).Size(tileMedium(), tileMedium()).Radius(full(tileMedium())).Background(colors(t).tint).Center().Shrink(0).Children(func() {
				ui.Text(c, m.DiscYouInitial()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Accent)
			})
		}
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(6).Children(func() {
			// 行高按悬停按钮定好：按钮出现、消失时不跳
			ui.Row(c).Gap(8).AlignItems(ui.Center).MinHeight(compactBox()).Children(func() {
				name := m.DiscYou()
				if msg.Kind == "reply" || msg.Kind == "question" {
					name = msg.AuthorName
				}
				ui.Text(c, name).FontSize(fsBody).FontWeight(fwMedium).Shrink(0)
				switch {
				case msg.Kind == "question":
					x := d.execs[msg.Execution]
					if x.State == "waiting" && !answered(d, msg) {
						pendingLabel(c)
					} else {
						ui.Text(c, m.DiscQuestionLabel()).FontSize(fsSmall).TextColor(t.TextMuted)
					}
				case msg.Kind == "reply" && msg.Outcome == "failed":
					ui.Text(c, m.DiscReportedFailed()).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Danger)
				case msg.Kind == "answer":
					ui.Text(c, m.DiscAnswerLabel()).FontSize(fsSmall).TextColor(t.TextMuted)
				}
				if msg.TargetName != "" {
					ui.Text(c, "@"+msg.TargetName).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.Accent).
						Padding(2, 6).Radius(rSmall).Background(colors(t).tint).Shrink(0)
				}
				if msg.KeyAt != nil {
					ui.Icon(c, iconStar).FontSize(fsSmall).TextColor(colors(t).honey).Tooltip(m.DiscKeyMessage())
				}
				ui.Text(c, m.AppTime(msg.CreatedAt.Local())).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
				ui.Spacer(c)
				// 悬停时才有引用、标关键两个按钮（不悬停时不生成：看不见又点不动的按钮不留在辅助功能里），
				// 同样的操作在右键菜单里也有
				if !hovered || archived {
					return
				}
				ui.Row(c).Gap(2).Children(func() {
					if iconButtonSized(c.Key("quote-"+msg.ID), iconQuote, m.DiscQuote(), compactBox(), smallIcon()).Clicked() {
						b.quote = msg.ID
					}
					tip := m.DiscMarkKey()
					if msg.KeyAt != nil {
						tip = m.DiscUnmarkKey()
					}
					if iconButtonSized(c.Key("key-"+msg.ID), iconStar, tip, compactBox(), smallIcon()).Clicked() {
						go a.discCall("discussion.key", map[string]any{"message": msg.ID, "on": msg.KeyAt == nil})
					}
				})
			})
			if msg.Quote != "" {
				a.quoteBlock(c, d, msg.Quote)
			}
			if strings.TrimSpace(msg.Body) != "" {
				if msg.Kind == "reply" {
					blocks, ok := d.md[msg.ID]
					if !ok {
						blocks = parseMarkdown(strings.TrimSpace(msg.Body))
						d.md[msg.ID] = blocks
					}
					ui.Column(c).Selectable().Children(func() { mdView{size: a.mdSize()}.page(c, blocks) })
				} else {
					ui.Text(c, msg.Body).FontSize(fsBody).LineHeight(1.55).Selectable()
				}
			}
			if msg.Report != "" {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Icon(c, iconFile).FontSize(fsSmall).TextColor(t.TextMuted)
					link := ui.Text(c.Key("report-"+msg.ID), msg.Report).FontSize(fsSmall).Font(monoFont).TextColor(t.Accent).MaxLines(1).Shrink(1).Role(ui.RoleButton)
					if link.Clicked() {
						a.openDiscFile(msg.Report)
					}
				})
			}
			if len(msg.Attachments) > 0 {
				a.attachmentChips(c, d, msg)
			}
			if msg.Kind == "question" && !archived && d.execs[msg.Execution].State == "waiting" && !answered(d, msg) {
				ui.Row(c).Gap(8).Children(func() {
					if pressable(textButton(c.Key("answer-"+msg.ID), m.DiscAnswerEllipsis())).Clicked() {
						b.answerQ, b.quote, b.target = msg.Question, "", ""
					}
				})
			}
			if msg.Kind == "user" && msg.Execution != "" {
				if x := d.execFor(msg.ID); x != nil {
					a.execCard(c, d, *x)
				}
			}
		})
	})
	contextMenu(c, row, func(pm *popMenu) {
		if !archived && pm.Item(m.DiscQuote()).Icon(iconQuote).Chosen() {
			b.quote = msg.ID
		}
		keyLabel := m.DiscMarkKey()
		if msg.KeyAt != nil {
			keyLabel = m.DiscUnmarkKey()
		}
		if !archived && pm.Item(keyLabel).Icon(iconStar).Chosen() {
			go a.discCall("discussion.key", map[string]any{"message": msg.ID, "on": msg.KeyAt == nil})
		}
		if pm.Item(m.CommonCopy()).Icon(iconCopy).Chosen() {
			mygo.Clipboard.WriteText(msg.Body)
		}
		if pm.Item(m.DiscCopyID()).Chosen() {
			mygo.Clipboard.WriteText(msg.ID)
		}
	})
}

func answered(d *discDetail, q discMessage) bool {
	for _, m := range d.msgs {
		if m.Kind == "answer" && m.Question == q.Question {
			return true
		}
	}
	return false
}

// quoteBlock 是消息里引用的原文：作者和第一行，点了跳过去。
func (a *app) quoteBlock(c *ui.Context, d *discDetail, id string) {
	t, m := c.Theme(), L(c)
	q := d.msg(id)
	box := ui.Row(c.Key("q-"+id)).Gap(8).Padding(6, 12).Radius(rSmall).Background(colors(t).wash).Role(ui.RoleButton)
	box.Children(func() {
		ui.Box(c).Width(2).Radius(full(2)).Background(t.TextMuted).Shrink(0)
		ui.Column(c).Gap(2).Shrink(1).MinWidth(0).Children(func() {
			if q == nil {
				ui.Text(c, m.DiscQuotedEarlier()).FontSize(fsSmall).TextColor(t.TextMuted)
				return
			}
			who := m.DiscYou()
			if q.Kind == "reply" || q.Kind == "question" {
				who = q.AuthorName
			}
			ui.Text(c, who).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted)
			ui.Text(c, firstLineText(q.Body)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2)
		})
	})
	if box.Clicked() {
		a.discJump(d, id, 0)
	}
}

// firstLineText 是正文第一行有字的内容，去掉 Markdown 的标题、列表和引用记号。
func firstLineText(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>-*` "))
		if line != "" {
			return line
		}
	}
	return ""
}

// attachmentChips 画附件：点了用系统默认程序打开；文件不在了写明不可用。
func (a *app) attachmentChips(c *ui.Context, d *discDetail, msg discMessage) {
	t, m := c.Theme(), L(c)
	root := d.info.ProjectPath
	for _, p := range a.projects {
		if p.ID == d.info.ProjectID {
			root = p.Path
		}
	}
	ui.Row(c).Gap(6).Wrap().Children(func() {
		for _, att := range msg.Attachments {
			abs := filepath.Join(root, att.Path)
			_, err := os.Stat(abs)
			missing := err != nil
			chip := ui.Row(c.Key("att-"+att.ID)).Gap(6).Padding(4, 8).Radius(rSmall).Background(colors(t).fill).AlignItems(ui.Center).Role(ui.RoleButton)
			chip.Children(func() {
				fileGlyph(c, att.Name, fsSmall)
				color := t.Text
				if missing {
					color = t.TextMuted
				}
				ui.Text(c, att.Name).FontSize(fsSmall).TextColor(color).MaxLines(1)
				label := humanSize(att.Size)
				if missing {
					label = m.DiscAttachmentMissing()
				}
				ui.Text(c, label).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
			})
			if chip.Clicked() && !missing {
				mygo.Shell.OpenPath(abs)
			}
			contextMenu(c, chip, func(pm *popMenu) {
				if !missing && pm.Item(m.CommonRevealInFinder()).Icon(iconFolder).Chosen() {
					mygo.Shell.ShowItemInFolder(abs)
				}
				if pm.Item(m.CommonCopyPath()).Icon(iconCopy).Chosen() {
					mygo.Clipboard.WriteText(abs)
				}
			})
		}
	})
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (a *app) openDiscFile(path string) {
	if _, err := os.Stat(path); err != nil {
		a.err = errtext.Text(err)
		return
	}
	mygo.Shell.OpenPath(path)
}

// execStatus 是执行卡上的状态文字和形状。
func execStatus(m locales.Messages, x discExec) (string, string) {
	switch x.State {
	case "queued":
		return m.DiscExecQueued(), "ended"
	case "held":
		return m.DiscExecHeld(), agent.StatusWaiting
	case "blocked":
		return m.DiscExecBlocked(), agent.StatusWaiting
	case "starting":
		return m.DiscExecStarting(), agent.StatusStarting
	case "running":
		return m.CommonInProgress(), agent.StatusWorking
	case "waiting":
		return m.DiscExecWaiting(), agent.StatusWaiting
	case "done":
		return m.CommonDone(), agent.StatusIdle
	case "failed":
		return m.CommonFailed(), "failed"
	case "interrupted":
		return m.DiscExecInterrupted(), "failed"
	case "stopped":
		return m.TasksStatusStopped(), "ended"
	case "cancelled":
		return m.DiscExecCancelled(), "ended"
	}
	return x.State, "ended"
}

// execReason 把常驻进程写的原因换成界面语言：同目录并发带着另一位成员的名字。
func execReason(m locales.Messages, x discExec) string {
	if name, ok := strings.CutPrefix(x.Reason, "concurrent:"); ok {
		return m.DiscConcurrent(name)
	}
	return errtext.Text(fmt.Errorf("%s", x.Reason))
}

func (a *app) execCard(c *ui.Context, d *discDetail, x discExec) {
	t, m := c.Theme(), L(c)
	text, glyph := execStatus(m, x)
	member := d.member(x.Member)
	name := x.Member
	if member != nil {
		name = member.Name
	}
	box := ui.Column(c.Key("exec-"+x.ID)).Gap(8).Padding(8, 12).Radius(rControl).Background(colors(t).wash)
	box.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			switch glyph {
			case "ended":
				ui.Icon(c, iconHexOutline).FontSize(fsSmall).TextColor(t.TextMuted)
			case "failed":
				ui.Icon(c, iconHexOutline).FontSize(fsSmall).TextColor(t.Danger)
			default:
				statusGlyph(c, glyph, fsSmall)
			}
			color := t.TextMuted
			switch x.State {
			case "waiting", "held", "blocked":
				color = colors(t).honey
			case "failed", "interrupted":
				color = t.Danger
			}
			ui.Text(c, text).FontSize(fsSmall).FontWeight(fwMedium).TextColor(color).Shrink(0)
			agentTile(c, x.Agent, smallIcon())
			ui.Text(c, name).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			if x.WorkspaceLabel != "" {
				ui.Icon(c, iconBranch).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Text(c, x.WorkspaceLabel).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			}
			if x.RetryOf != "" {
				ui.Text(c, m.DiscRetried()).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			}
		})
		if x.Reason != "" && x.State != "done" {
			ui.Text(c, execReason(m, x)).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45).Selectable()
		}
		if d.info.Status == "archived" {
			return
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if x.Terminal && pressable(textButton(c, m.DiscOpenTerminal())).Clicked() {
				a.openTerminal(x.WorkspaceID, x.Worker)
			}
			if x.TaskID != "" && a.task(x.TaskID) != nil && pressable(textButton(c, m.DiscViewTask())).Clicked() {
				a.openTask(x.TaskID)
			}
			switch x.State {
			case "queued", "blocked":
				if x.State == "blocked" && pressable(accentButton(c, m.DiscRestartContinue())).Clicked() {
					go a.discCall("discussion.member.restart", map[string]any{"member": x.Member})
				}
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					go a.discCall("discussion.cancel", map[string]any{"execution": x.ID})
				}
			case "held":
				if pressable(accentButton(c, m.DiscContinueAnyway())).Clicked() {
					go a.discCall("discussion.confirm", map[string]any{"execution": x.ID})
				}
				if pressable(textButton(c, m.DiscIsolate())).Clicked() {
					go a.discCall("discussion.isolate", map[string]any{"execution": x.ID})
				}
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					go a.discCall("discussion.cancel", map[string]any{"execution": x.ID})
				}
			case "running", "waiting":
				if pressable(textButton(c, m.DiscStop())).Clicked() {
					a.confirm(m.DiscStopConfirm(name), m.DiscStopBody(), m.DiscStop(), func() {
						a.discCall("discussion.stop", map[string]any{"execution": x.ID})
					})
				}
			case "failed", "interrupted", "stopped", "cancelled":
				if member != nil && member.RemovedAt == nil && !retried(d, x) && pressable(textButton(c, m.CommonRetry())).Clicked() {
					go a.discCall("discussion.retry", map[string]any{"execution": x.ID})
				}
			}
		})
	})
}

// retried 是这一轮已经重试过（有更新的一轮）。
func retried(d *discDetail, x discExec) bool {
	for _, o := range d.execs {
		if o.RetryOf == x.ID {
			return true
		}
	}
	return false
}

// discPausedBanner：成员的队列暂停了（worker 中断、启动失败、常驻进程重启）且还有排队的请求时，给「重新启动并继续」。
func (a *app) discPausedBanner(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	for _, mem := range activeMembers(d) {
		if mem.Paused == "" {
			continue
		}
		queued := 0
		for _, x := range d.execs {
			if x.Member == mem.ID && (x.State == "queued" || x.State == "blocked") {
				queued++
			}
		}
		if queued == 0 {
			continue
		}
		ui.Row(c.Key("paused-"+mem.ID)).Gap(8).Padding(8, 12).Radius(rControl).Background(soft(t, colors(t).honey)).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, iconPause).FontSize(fsBody).TextColor(colors(t).honey)
			ui.Text(c, m.DiscPaused(mem.Name, queued)).FontSize(fsSmall).LineHeight(1.45).Grow(1).Shrink(1)
			if pressable(accentButton(c, m.DiscRestartContinue())).Clicked() {
				go a.discCall("discussion.member.restart", map[string]any{"member": mem.ID})
			}
		})
	}
}

// discComposer 是输入区：接收者、引用、附件、正文，⌘↩ 发送。回答问题时换成回答模式。
func (a *app) discComposer(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	members := activeMembers(d)
	if b.target != "" && !slices.ContainsFunc(members, func(x discMember) bool { return x.ID == b.target }) {
		b.target = ""
	}
	var question *discMessage
	if b.answerQ != "" {
		for i := range d.msgs {
			if d.msgs[i].Kind == "question" && d.msgs[i].Question == b.answerQ {
				question = &d.msgs[i]
			}
		}
		if question == nil || answered(d, *question) {
			b.answerQ, question = "", nil
		}
	}
	box := ui.Column(c).Radius(rComposer).Background(colors(t).card).Padding(12, 12, 12, 16).Gap(8)
	var in ui.Element
	sent := false
	box.Children(func() {
		// 接收者一直写明：发给所有人（只是发言）或 @ 一位成员（为它排一轮执行）
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if question != nil {
				ui.Icon(c, iconReply).FontSize(fsSmall).TextColor(colors(t).honey)
				ui.Text(c, m.DiscAnsweringTo(question.AuthorName)).FontSize(fsSmall).FontWeight(fwMedium).TextColor(colors(t).honey).Shrink(1)
				ui.Spacer(c)
				if iconButtonSized(c.Key("cancel-answer"), iconClose, m.CommonCancel(), miniBox(), fsSmall).Clicked() {
					b.answerQ = ""
				}
				return
			}
			ui.Text(c, m.DiscSendTo()).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			label := m.DiscEveryone()
			if mem := d.member(b.target); mem != nil {
				label = "@" + mem.Name
			}
			btn := ui.Row(c.Key("target")).Gap(4).Padding(2, 8).Shrink(0).Radius(rSmall).Background(colors(t).fill).AlignItems(ui.Center).Role(ui.RoleButton)
			btn.Children(func() {
				if mem := d.member(b.target); mem != nil {
					agentTile(c, mem.Agent, fsSmall)
				}
				ui.Text(c, label).FontSize(fsSmall).FontWeight(fwMedium).SingleLine()
				ui.Icon(c, iconChevron).FontSize(fsSmall).TextColor(t.TextMuted)
			})
			menuButton(c, btn, func(pm *popMenu) {
				if pm.Item(m.DiscEveryoneHint()).Checked(b.target == "").Chosen() {
					b.target = ""
				}
				if len(members) > 0 {
					pm.Separator()
				}
				for _, mem := range members {
					it := pm.Item("@" + mem.Name).Checked(b.target == mem.ID).Lead(func() { agentTile(c, mem.Agent, smallIcon()) })
					if it.Chosen() {
						b.target = mem.ID
					}
				}
				pm.Separator()
				if pm.Item(m.DiscAddMemberEllipsis()).Icon(iconPlus).Chosen() {
					a.openMemberForm(d, "")
				}
			})
			if b.target != "" {
				ui.Text(c, m.DiscTargetNote()).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			}
		})
		if b.quote != "" {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Grow(1).Shrink(1).MinWidth(0).Children(func() { a.quoteBlock(c, d, b.quote) })
				if iconButtonSized(c.Key("cancel-quote"), iconClose, m.CommonCancel(), miniBox(), fsSmall).Clicked() {
					b.quote = ""
				}
			})
		}
		if len(b.atts) > 0 {
			ui.Row(c).Gap(6).Wrap().Children(func() {
				for i, p := range b.atts {
					ui.Row(c.Key(fmt.Sprint("draft-att-", i))).Gap(4).Padding(4, 8).Radius(rSmall).Background(colors(t).fill).AlignItems(ui.Center).Children(func() {
						fileGlyph(c, p, fsSmall)
						ui.Text(c, filepath.Base(p)).FontSize(fsSmall).MaxLines(1)
						if iconButtonSized(c.Key(fmt.Sprint("rm-att-", i)), iconClose, m.CommonRemove(), miniBox(), fsSmall).Clicked() {
							b.atts = slices.Delete(slices.Clone(b.atts), i, i+1)
						}
					})
				}
			})
		}
		placeholder := m.DiscPlaceholder()
		if question != nil {
			placeholder = m.DiscAnswerPlaceholder()
		} else if mem := d.member(b.target); mem != nil {
			placeholder = m.DiscPlaceholderTo(mem.Name)
		}
		in = ui.TextAreaBase(c.Key("disc-input"), &b.text).Placeholder(placeholder).Label(placeholder).Height(sz(64)).FontSize(fsBody)
		sent = in.Shortcut(ui.Cmd, ui.KeyEnter) && !in.Composing()
		// 粘贴剪贴板里的图片（没有文字）：存成临时文件当附件
		if question == nil {
			in.OnPaste(func(text string) bool {
				if text != "" {
					return false
				}
				png := mygo.Clipboard.ReadImage()
				if len(png) == 0 {
					return false
				}
				go a.pasteDiscImage(png)
				return true
			})
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if question == nil {
				if iconButtonSized(c.Key("attach"), iconPaperclip, m.DiscAttach(), compactBox(), smallIcon()).Clicked() {
					a.pickAttachments(m)
				}
			}
			ui.Spacer(c)
			verb := m.DiscSend()
			if question != nil {
				verb = m.TasksReplyButton()
			}
			btn := pressable(accentButton(c, verb)).Tooltip(verb + " ⌘↩")
			empty := strings.TrimSpace(b.text) == "" && (len(b.atts) == 0 || question != nil)
			if empty || b.sending {
				btn.Disabled(true).Opacity(0.5)
			} else if btn.Clicked() {
				sent = true
			}
		})
	})
	composerFocus(c, box, in.Focused())
	if !sent || b.sending || strings.TrimSpace(b.text) == "" && (len(b.atts) == 0 || question != nil) {
		return
	}
	if question != nil {
		b.sending = true
		go a.sendDiscAnswer(question.Question, strings.TrimSpace(b.text))
		return
	}
	if b.clientID == "" {
		b.clientID = fmt.Sprintf("c_%x", time.Now().UnixNano())
	}
	b.sending = true
	params := map[string]any{"discussion": d.key, "body": strings.TrimSpace(b.text), "target": b.target, "quote": b.quote, "clientId": b.clientID}
	var atts []map[string]string
	for _, p := range b.atts {
		atts = append(atts, map[string]string{"path": p})
	}
	params["attachments"] = atts
	go a.sendDiscMessage(d.key, params)
}

func (a *app) sendDiscMessage(key string, params map[string]any) {
	var r struct{ Message discMessage }
	err := a.call("discussion.post", params, &r, 30*time.Second)
	a.update(func() {
		b := &a.disc
		b.sending = false
		if err != nil || b.sel != key {
			return
		}
		b.text, b.quote, b.atts, b.clientID = "", "", nil, ""
		b.follow = true
		a.markDiscRead(key, r.Message.Seq)
	})
	a.refresh()
}

func (a *app) sendDiscAnswer(question, text string) {
	err := a.call("discussion.answer", map[string]any{"question": question, "body": text}, nil, 10*time.Second)
	a.update(func() {
		b := &a.disc
		b.sending = false
		if err == nil {
			b.text, b.answerQ = "", ""
			b.follow = true
		}
	})
	a.refresh()
}

// pickAttachments 选本机文件当附件；常驻进程发送时复制一份快照。
func (a *app) pickAttachments(m locales.Messages) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: m.DiscAttachTitle(), ButtonLabel: m.DiscAttach(), Multiple: true})
	if err != nil || len(paths) == 0 {
		return
	}
	b := &a.disc
	for _, p := range paths {
		if !slices.Contains(b.atts, p) {
			b.atts = append(b.atts, p)
		}
	}
	if len(b.atts) > 10 {
		b.atts = b.atts[:10]
		a.err = m.DiscTooManyAttachments(10)
	}
}

func (a *app) pasteDiscImage(png []byte) {
	path := filepath.Join(os.TempDir(), fmt.Sprintf("posse-paste-%d.png", time.Now().UnixNano()))
	if err := os.WriteFile(path, png, 0o600); err != nil {
		a.update(func() { a.err = appL().AppPasteImageError(errtext.Text(err)) })
		return
	}
	a.update(func() { a.disc.atts = append(a.disc.atts, path) })
}

// discCall 调常驻进程，完了刷新；出错显示在错误条上。
func (a *app) discCall(method string, params map[string]any) {
	a.call(method, params, nil, 30*time.Second)
	a.refresh()
}

// loadOlder 往前取一页历史；until 不为 0 时一直取到这个序号（跳到搜索结果、关键消息），取完跳到 jump。
func (a *app) loadOlder(key string, until int64, jump string) {
	b := &a.disc
	if b.d == nil || b.d.key != key || len(b.d.msgs) == 0 {
		return
	}
	b.older = true
	first := b.d.msgs[0].Seq
	limit := discPage
	if until > 0 {
		limit = int(min(500, first-until+10))
	}
	go func() {
		var v discView
		err := a.call("discussion.show", map[string]any{"discussion": key, "before": first, "limit": limit}, &v, 10*time.Second)
		a.update(func() {
			b.older = false
			if err != nil || b.d == nil || b.d.key != key {
				return
			}
			var add []discMessage
			for _, m := range v.Messages {
				if b.d.msg(m.ID) == nil {
					add = append(add, m)
				}
			}
			b.d.msgs = append(add, b.d.msgs...)
			b.d.more = v.More
			b.follow = false
			if jump != "" {
				b.reveal, b.flash, b.flashAt = jump, jump, time.Now()
			} else if len(add) > 0 {
				// 留在原来看的地方：原来最上面那条滚回视野，不高亮
				b.reveal = b.d.msgs[len(add)].ID
			}
		})
	}()
}

// discJump 定位到一条消息：不在读进来的几页里时先往前取到它。
func (a *app) discJump(d *discDetail, id string, seq int64) {
	b := &a.disc
	b.follow = false
	if d.msg(id) != nil {
		b.reveal, b.flash, b.flashAt = id, id, time.Now()
		return
	}
	if seq > 0 {
		a.loadOlder(d.key, seq, id)
	}
}

func (a *app) discArchive(key string, archived bool) {
	go a.discCall("discussion.archive", map[string]any{"discussion": key, "archived": archived})
}

// askArchive 先列出挡着归档的请求；没有就确认后归档。
func (a *app) askArchive(m locales.Messages, d *discDetail) {
	var busy []string
	for _, x := range d.execs {
		switch x.State {
		case "queued", "held", "blocked", "starting", "running", "waiting":
			name := x.Member
			if mem := d.member(x.Member); mem != nil {
				name = mem.Name
			}
			text, _ := execStatus(m, x)
			busy = append(busy, name+" · "+text)
		}
	}
	if len(busy) > 0 {
		slices.Sort(busy)
		a.confirm(m.DiscArchiveBlocked(), m.DiscArchiveBlockedBody(strings.Join(busy, "\n")), m.CommonOk(), func() {})
		return
	}
	a.confirm(m.DiscArchiveConfirm(d.info.Title), m.DiscArchiveBody(), m.DiscArchive(), func() {
		a.discCall("discussion.archive", map[string]any{"discussion": d.key, "archived": true})
	})
}

// askCleanAttachments 列出已归档讨论的附件和占的空间，确认后移到废纸篓；消息和附件信息留着。
func (a *app) askCleanAttachments(m locales.Messages, key string) {
	var r struct {
		Dir   string
		Items []struct {
			discAttachment
			Abs    string
			Exists bool
		}
	}
	if a.call("discussion.attachments", map[string]any{"discussion": key}, &r, 10*time.Second) != nil {
		return
	}
	var files []string
	var total int64
	for _, it := range r.Items {
		if it.Exists {
			files = append(files, it.Abs)
			total += it.Size
		}
	}
	a.update(func() {
		if len(files) == 0 {
			a.confirm(m.DiscCleanNothing(), m.DiscCleanNothingBody(), m.CommonOk(), func() {})
			return
		}
		var names []string
		for _, f := range files[:min(len(files), 8)] {
			names = append(names, filepath.Base(f))
		}
		if len(files) > 8 {
			names = append(names, m.DiscAndMore(len(files)-8))
		}
		a.confirm(m.DiscCleanConfirm(len(files), humanSize(total)), m.DiscCleanBody(strings.Join(names, "\n")), m.CommonMoveToTrash(), func() {
			var failed error
			trash := a.trashItem
			if trash == nil {
				trash = mygo.Shell.TrashItem
			}
			for _, f := range files {
				if err := trash(f); err != nil {
					failed = err
				}
			}
			if failed != nil {
				a.update(func() { a.err = m.TrashMoveError(errtext.Text(failed)) })
			}
			a.refresh()
		})
	})
}

// --- 右边一栏：详情、成员、关键消息，或搜索结果

func (a *app) discPanel(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	if b.searchQ != "" {
		a.discSearchPanel(c, d)
		return
	}
	archived := d.info.Status == "archived"
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Padding(cardPad).Gap(20).Children(func() {
			ui.Column(c).Gap(6).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					cardTitle(c, m.DiscDetails()).Grow(1)
					if !archived && iconButtonSized(c.Key("edit-disc"), iconPencil, m.DiscEditEllipsis(), compactBox(), smallIcon()).Clicked() {
						b.edit = discEditForm{open: true, title: d.info.Title, desc: d.info.Description, ws: a.wsLabel(d.info.WorkspaceID)}
					}
				})
				desc := d.info.Description
				if desc == "" {
					desc = m.DiscNoDescription()
				}
				ui.Text(c, desc).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5).Selectable()
				ui.Text(c, m.DiscCreatedAt(d.info.CreatedAt.Local())).FontSize(fsSmall).TextColor(t.TextMuted)
			})
			ui.Column(c).Gap(6).Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					cardTitle(c, m.DiscMembers()).Grow(1)
					if !archived && iconButtonSized(c.Key("add-member"), iconPlus, m.DiscAddMemberEllipsis(), compactBox(), smallIcon()).Clicked() {
						a.openMemberForm(d, "")
					}
				})
				if len(activeMembers(d)) == 0 {
					ui.Text(c, m.DiscNoMembers()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
				}
				for _, mem := range activeMembers(d) {
					a.memberRow(c, d, mem)
				}
			})
			ui.Column(c).Gap(6).Children(func() {
				cardTitle(c, m.DiscKeyMessages())
				if len(d.keyMsgs) == 0 {
					ui.Text(c, m.DiscNoKeyMessages()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.5)
				}
				for _, k := range d.keyMsgs {
					row := ui.Row(c.Key("key-"+k.ID)).Gap(8).Padding(6, 8).Radius(rRow).AlignItems(ui.Start).Role(ui.RoleButton)
					if row.Hovered() {
						row.Background(colors(t).wash)
					}
					row.Children(func() {
						ui.Icon(c, iconStar).FontSize(fsSmall).TextColor(colors(t).honey)
						ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
							who := m.DiscYou()
							if k.AuthorName != "" {
								who = k.AuthorName
							}
							ui.Text(c, who).FontSize(fsSmall).FontWeight(fwMedium)
							ui.Text(c, firstLineText(k.Body)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2)
						})
						if !archived && iconButtonSized(c.Key("unkey-"+k.ID), iconClose, m.DiscUnmarkKey(), miniBox(), fsSmall).Clicked() {
							go a.discCall("discussion.key", map[string]any{"message": k.ID, "on": false})
						}
					})
					if row.Clicked() {
						a.discJump(d, k.ID, k.Seq)
					}
				}
			})
		})
	})
}

func (a *app) memberRow(c *ui.Context, d *discDetail, mem discMember) {
	t, m := c.Theme(), L(c)
	row := ui.Row(c.Key("mem-"+mem.ID)).Gap(8).Padding(6, 8).Radius(rRow).AlignItems(ui.Start)
	if row.Hovered() {
		row.Background(colors(t).wash)
	}
	row.Children(func() {
		agentTile(c, mem.Agent, iconSize)
		ui.Column(c).Gap(2).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, mem.Name).FontSize(fsBody).FontWeight(fwMedium).MaxLines(1).Shrink(1)
				status := ""
				switch {
				case mem.Paused != "":
					status = m.DiscMemberPaused()
				case mem.Terminal != nil && mem.Terminal.AgentState != nil:
					status = sessionStatusText(m, mem.Terminal.AgentState.Status)
				case mem.TaskID == "":
					status = m.DiscMemberNotStarted()
				}
				if status != "" {
					ui.Text(c, status).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
				}
			})
			if mem.Role != "" {
				ui.Text(c, mem.Role).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(2)
			}
			model := agentTitle(mem.Agent)
			if mem.Model != "" {
				model += " · " + mem.Model
			}
			if mem.Effort != "" {
				model += " · " + mem.Effort
			}
			ui.Text(c, model).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1)
			ui.Text(c, mem.WorkspaceLabel).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1)
			if mem.TaskID != "" && mem.WorkerConfig != 0 && mem.WorkerConfig != mem.ConfigVersion {
				ui.Text(c, m.DiscConfigPending()).FontSize(fsSmall).TextColor(colors(t).honey).LineHeight(1.45)
			}
		})
	})
	if d.info.Status == "archived" {
		return
	}
	contextMenu(c, row, func(pm *popMenu) {
		if pm.Item(m.DiscMention()).Icon(iconAt).Chosen() {
			a.disc.target, a.disc.answerQ = mem.ID, ""
		}
		if mem.Terminal != nil && pm.Item(m.DiscOpenTerminal()).Icon(iconTerminal).Chosen() {
			a.openTerminal(mem.Terminal.WorkspaceID, mem.Worker)
		}
		if pm.Item(m.DiscEditMemberEllipsis()).Icon(iconPencil).Chosen() {
			a.openMemberForm(d, mem.ID)
		}
		if mem.Paused != "" && pm.Item(m.DiscRestartContinue()).Icon(iconRefresh).Chosen() {
			go a.discCall("discussion.member.restart", map[string]any{"member": mem.ID})
		}
		pm.Separator()
		if pm.Item(m.DiscRemoveMemberEllipsis()).Icon(iconTrash).Danger().Chosen() {
			a.confirm(m.DiscRemoveMemberConfirm(mem.Name), m.DiscRemoveMemberBody(), m.CommonRemove(), func() {
				a.discCall("discussion.member.remove", map[string]any{"member": mem.ID})
			})
		}
	})
}

// sessionStatusText 是成员 worker 的 agent 状态写成文字。
func sessionStatusText(m locales.Messages, status string) string {
	switch status {
	case agent.StatusWorking:
		return m.CommonInProgress()
	case agent.StatusWaiting:
		return m.TermPending()
	case agent.StatusStarting:
		return m.DiscExecStarting()
	case agent.StatusIdle:
		return m.DiscMemberIdle()
	}
	return ""
}

func (a *app) discSearchPanel(c *ui.Context, d *discDetail) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	ui.Column(c).Grow(1).MinHeight(0).Children(func() {
		ui.Row(c).Padding(12, 12, 8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			in, cleared := clearableField(c.Key("disc-search-field"), &b.search, m.DiscSearchPlaceholder())
			in.AutoFocus()
			if in.Submitted() || cleared {
				q := strings.TrimSpace(b.search)
				if q == "" {
					b.hits = nil
				} else {
					go func() {
						var hits []discMessage
						if a.call("discussion.search", map[string]any{"discussion": d.key, "query": q}, &hits, 10*time.Second) == nil {
							a.update(func() { b.hits = hits })
						}
					}()
				}
			}
			if iconButtonSized(c.Key("close-search"), iconClose, m.CommonClose(), compactBox(), smallIcon()).Clicked() {
				b.searchQ, b.hits, b.search = "", nil, ""
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			ui.Column(c).Padding(0, 8, 8, 8).Gap(2).Children(func() {
				if b.hits != nil && len(b.hits) == 0 {
					ui.Text(c, m.DiscNoMatches()).FontSize(fsSmall).TextColor(t.TextMuted).Padding(8, 8)
				}
				for _, h := range b.hits {
					row := ui.Column(c.Key("hit-"+h.ID)).Gap(2).Padding(6, 8).Radius(rRow).Role(ui.RoleButton)
					if row.Hovered() {
						row.Background(colors(t).wash)
					}
					row.Children(func() {
						who := m.DiscYou()
						if h.AuthorName != "" {
							who = h.AuthorName
						}
						ui.Row(c).Gap(6).Children(func() {
							ui.Text(c, who).FontSize(fsSmall).FontWeight(fwMedium)
							ui.Text(c, m.AppDateTime(h.CreatedAt.Local())).FontSize(fsSmall).TextColor(t.TextMuted)
						})
						ui.Text(c, snippet(h.Body, b.search)).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(3)
					})
					if row.Clicked() {
						a.discJump(d, h.ID, h.Seq)
					}
				}
			})
		})
	})
}

// snippet 取命中处前后的一段，长消息里也看得到匹配的位置。
func snippet(body, q string) string {
	flat := strings.Join(strings.Fields(body), " ")
	i := strings.Index(strings.ToLower(flat), strings.ToLower(strings.TrimSpace(q)))
	if i < 0 {
		return flat
	}
	start := max(0, i-40)
	for start > 0 && !utf8RuneStart(flat[start]) {
		start--
	}
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	return prefix + flat[start:]
}

func utf8RuneStart(b byte) bool { return b&0xC0 != 0x80 }

// --- 对话框

func (a *app) openNewDiscussion() {
	b := &a.disc
	b.newOpen, b.newTitle, b.newDesc = true, "", ""
	if w := a.workspace(a.lastWS); w != nil {
		if p := a.projectOf(w); p != nil {
			b.newProj, b.newWS = p.Name, w.Label
		}
	}
	if b.newProj == "" && len(a.projects) > 0 {
		b.newProj = a.projects[0].Name
	}
}

func (a *app) projectByName(name string) *project {
	for i := range a.projects {
		if a.projects[i].Name == name {
			return &a.projects[i]
		}
	}
	return nil
}

// projectWorkspaces 是项目的工作区标签（成员执行工作区只能在讨论的项目里选）。
func (a *app) projectWorkspaces(projectID string) []string {
	var out []string
	for _, w := range a.byProject[projectID] {
		out = append(out, w.Label)
	}
	return out
}

func (a *app) newDiscussionDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	ui.Modal(c, &b.newOpen, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			titleText(c, m.DiscNewTitle(), fsTitle)
			fieldInput(c, &b.newTitle, m.DiscTitlePlaceholder()).AutoFocus()
			fieldArea(c, &b.newDesc, m.DiscDescPlaceholder(), 72)
			var names []string
			for _, p := range a.projects {
				names = append(names, p.Name)
			}
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, m.DiscProject()).FontSize(fsSmall).TextColor(t.TextMuted)
				if ui.Select(c, &b.newProj, names).Changed() {
					b.newWS = ""
				}
			})
			p := a.projectByName(b.newProj)
			if p != nil {
				labels := a.projectWorkspaces(p.ID)
				if !slices.Contains(labels, b.newWS) && len(labels) > 0 {
					b.newWS = labels[0]
				}
				ui.Column(c).Gap(6).Children(func() {
					ui.Text(c, m.DiscDefaultWorkspace()).FontSize(fsSmall).TextColor(t.TextMuted)
					ui.Select(c, &b.newWS, labels)
					ui.Text(c, m.DiscDefaultWorkspaceNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				})
			}
			ready := strings.TrimSpace(b.newTitle) != "" && p != nil
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					b.newOpen = false
				}
				if pressable(accentButton(c, m.DiscCreate()).Disabled(!ready)).Clicked() && ready {
					b.newOpen = false
					params := map[string]any{"title": strings.TrimSpace(b.newTitle), "description": strings.TrimSpace(b.newDesc), "project": p.ID}
					if w := a.workspaceByLabel(b.newWS); w != nil {
						params["workspace"] = w.ID
					}
					go func() {
						var d discSummary
						if a.call("discussion.create", params, &d, 10*time.Second) != nil {
							return
						}
						a.refresh()
						a.update(func() { a.openDiscussion(d.Key) })
					}()
				}
			})
		})
	})
}

func (a *app) editDiscussionDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	f := &b.edit
	if !f.open || b.d == nil {
		f.open = false
		return
	}
	d := b.d
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			titleText(c, m.DiscEditTitle(), fsTitle)
			fieldInput(c, &f.title, m.DiscTitlePlaceholder()).AutoFocus()
			fieldArea(c, &f.desc, m.DiscDescPlaceholder(), 72)
			ui.Column(c).Gap(6).Children(func() {
				ui.Text(c, m.DiscDefaultWorkspace()).FontSize(fsSmall).TextColor(t.TextMuted)
				ui.Select(c, &f.ws, a.projectWorkspaces(d.info.ProjectID))
			})
			ready := strings.TrimSpace(f.title) != ""
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					f.open = false
				}
				if pressable(accentButton(c, m.CommonSave()).Disabled(!ready)).Clicked() && ready {
					f.open = false
					params := map[string]any{"discussion": d.key, "title": strings.TrimSpace(f.title), "description": strings.TrimSpace(f.desc)}
					if w := a.workspaceByLabel(f.ws); w != nil {
						params["workspace"] = w.ID
					}
					go a.discCall("discussion.update", params)
				}
			})
		})
	})
}

func (a *app) openMemberForm(d *discDetail, id string) {
	f := memberForm{open: true, id: id, ws: a.wsLabel(cmp.Or(d.info.WorkspaceID, a.projectMain(d.info.ProjectID)))}
	choices := a.agentChoices()
	if mem := d.member(id); mem != nil {
		f.name, f.role, f.model, f.effort, f.ws = mem.Name, mem.Role, mem.Model, mem.Effort, mem.WorkspaceLabel
		f.agent = max(0, slices.Index(choices, mem.Agent))
	} else {
		// 默认名字是 agent 名加序号，同一家可以有多个成员
		f.name = memberDefaultName(d, choices, 0)
	}
	f.worktree = "discuss-" + randomSuffix()
	a.disc.form = f
}

func memberDefaultName(d *discDetail, choices []string, i int) string {
	if i >= len(choices) {
		return ""
	}
	base := agentTitle(choices[i])
	n := 1
	for _, m := range d.members {
		if m.Agent == choices[i] {
			n++
		}
	}
	if n == 1 {
		return base
	}
	return fmt.Sprintf("%s %d", base, n)
}

func (a *app) memberDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	f := &b.form
	if !f.open || b.d == nil {
		f.open = false
		return
	}
	d := b.d
	editing := d.member(f.id)
	ui.Modal(c, &f.open, func() {
		ui.Column(c).Width(480).Gap(12).Children(func() {
			title := m.DiscAddMemberTitle()
			if editing != nil {
				title = m.DiscEditMemberTitle()
			}
			titleText(c, title, fsTitle)
			choices := a.agentChoices()
			if len(choices) == 0 {
				ui.Text(c, m.TasksNoAgents()).FontSize(fsBody).TextColor(t.TextMuted)
			} else {
				var labels []string
				for _, ag := range choices {
					labels = append(labels, agentTitle(ag))
				}
				prev := f.agent
				segmented(c, &f.agent, m.DiscAgent(), labels...)
				if f.agent != prev && editing == nil {
					f.name = memberDefaultName(d, choices, f.agent)
				}
			}
			fieldInput(c, &f.name, m.DiscMemberNamePlaceholder()).AutoFocus()
			fieldArea(c, &f.role, m.DiscRolePlaceholder(), 56)
			ui.Row(c).Gap(8).Children(func() {
				ui.Box(c).Grow(1).Basis(0).Children(func() { fieldInput(c, &f.model, m.DiscModelPlaceholder()) })
				ui.Box(c).Grow(1).Basis(0).Children(func() { fieldInput(c, &f.effort, m.DiscEffortPlaceholder()) })
			})
			isGit := false
			for _, p := range a.projects {
				if p.ID == d.info.ProjectID && p.Kind == "git" {
					isGit = true
				}
			}
			ui.Column(c).Gap(6).Children(func() {
				if isGit {
					segmented(c, &f.mode, m.DiscWorkDir(), m.DiscExistingWorkspace(), m.DiscNewWorktree())
				}
				if isGit && f.mode == 1 {
					fieldInput(c, &f.worktree, m.TasksBranchPlaceholder())
					ui.Text(c, m.DiscWorktreeNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				} else {
					ui.Select(c, &f.ws, a.projectWorkspaces(d.info.ProjectID))
					note := m.DiscSharedDirNote()
					if !isGit {
						note = m.DiscNoGitNote()
					}
					ui.Text(c, note).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				}
			})
			if editing != nil && editing.TaskID != "" {
				ui.Column(c).Gap(6).Children(func() {
					ui.Checkbox(c.Key("apply-now"), &f.applyNow, m.DiscApplyNow())
					ui.Text(c, m.DiscApplyNowNote()).FontSize(fsSmall).TextColor(t.TextMuted).LineHeight(1.45)
				})
			}
			ready := len(choices) > 0 && strings.TrimSpace(f.name) != "" && (f.mode == 0 || strings.TrimSpace(f.worktree) != "")
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					f.open = false
				}
				verb := m.DiscAddMember()
				if editing != nil {
					verb = m.CommonSave()
				}
				if pressable(accentButton(c, verb).Disabled(!ready)).Clicked() && ready {
					f.open = false
					params := map[string]any{"discussion": d.key, "name": strings.TrimSpace(f.name), "role": strings.TrimSpace(f.role),
						"agent": choices[f.agent], "model": strings.TrimSpace(f.model), "effort": strings.TrimSpace(f.effort)}
					if f.mode == 1 && isGit {
						params["worktree"] = strings.TrimSpace(f.worktree)
					} else if w := a.workspaceByLabel(f.ws); w != nil {
						params["workspace"] = w.ID
					}
					method := "discussion.member.add"
					if editing != nil {
						method = "discussion.member.update"
						params["member"] = editing.ID
						params["applyNow"] = f.applyNow
					}
					go a.discCall(method, params)
				}
			})
		})
	})
}
