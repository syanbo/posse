package app

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

// discMaxTargets 是一条消息最多同时 @ 的成员数，和 store.MaxTargets 一致。
const discMaxTargets = 8

type discTargetRef struct {
	ID, Name, Execution string
}

type discProgress struct {
	Phase     string `json:"phase,omitempty"`
	Tool      string `json:"tool,omitempty"`
	ToolInput string `json:"toolInput,omitempty"`
	Subagents int    `json:"subagents,omitempty"`
	Events    int    `json:"events,omitempty"`
}

type discExecTraceEvent struct {
	At     time.Time `json:"at,omitzero"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	Detail string    `json:"detail,omitempty"`
	Count  int       `json:"count,omitempty"`
	Failed bool      `json:"failed,omitempty"`
}

type discExecTraceSummary struct {
	Tools    int                  `json:"tools,omitempty"`
	Commands int                  `json:"commands,omitempty"`
	Skills   int                  `json:"skills,omitempty"`
	MCP      int                  `json:"mcp,omitempty"`
	Web      int                  `json:"web,omitempty"`
	Events   []discExecTraceEvent `json:"events,omitempty"`
}

type discExecTrace struct {
	ExecID     string               `json:"execId"`
	Agent      string               `json:"agent,omitempty"`
	SessionID  string               `json:"sessionId,omitempty"`
	Summary    discExecTraceSummary `json:"summary"`
	EventCount int                  `json:"eventCount"`
	Finalized  bool                 `json:"finalized"`
	UpdatedAt  time.Time            `json:"updatedAt,omitzero"`
}

const discTraceRefreshInterval = 2 * time.Second

// execDurationText 把一轮执行的耗时格式化成紧凑等宽文字（如 18s、2m 15s、1h 05m）。
// 进行中的轮次注册 c.After(time.Second) 让耗时随时间推进。
func execDurationText(c *ui.Context, x discExec) string {
	if x.StartedAt == nil {
		return ""
	}
	var dur time.Duration
	if x.EndedAt != nil {
		dur = x.EndedAt.Sub(*x.StartedAt)
	} else if x.State == "starting" || x.State == "running" || x.State == "waiting" {
		dur = time.Since(*x.StartedAt)
		c.After(time.Second)
	} else {
		return ""
	}
	if dur < 0 {
		dur = 0
	}
	sec := int(dur.Seconds())
	switch {
	case sec < 60:
		return fmt.Sprintf("%ds", sec)
	case sec < 3600:
		return fmt.Sprintf("%dm %02ds", sec/60, sec%60)
	default:
		return fmt.Sprintf("%dh %02dm", sec/3600, (sec%3600)/60)
	}
}

// discProgressLine 画进行中执行卡片里的单行实时活动摘要（工具名与参数、屏幕状态行、后台子代理数）。
func (a *app) discProgressLine(c *ui.Context, p discProgress) {
	if p.Tool == "" && p.Phase == "" && p.Subagents == 0 {
		return
	}
	t, m := c.Theme(), L(c)
	ui.Row(c).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
		if p.Tool != "" {
			ui.Icon(c, iconTerminal).FontSize(fsSmall).TextColor(t.TextMuted).Shrink(0)
			ui.Text(c, p.Tool).FontSize(fsSmall).Font(monoFont).FontWeight(fwMedium).TextColor(t.Text).Shrink(0)
			if p.ToolInput != "" {
				ui.Text(c, p.ToolInput).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			}
		} else if p.Phase != "" {
			ui.Text(c, p.Phase).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
		}
		if p.Subagents > 0 {
			ui.Text(c, m.DiscSubagents(p.Subagents)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.Accent).
				Padding(2, 6).Radius(rSmall).Background(colors(t).tint).Shrink(0)
		}
	})
}

// canShowTrace 报告这条执行是否允许展示可折叠过程按钮：仅本机拥有的轮次，且进行中或已有固化记录。
func canShowTrace(x discExec, mine bool, cached *discExecTrace) bool {
	if !mine {
		return false
	}
	if x.State == "running" || x.State == "waiting" {
		return true
	}
	if x.TraceCount > 0 {
		return true
	}
	return cached != nil && cached.EventCount > 0
}

// discTraceToggle 画可折叠的「过程 / 过程 (N)」切换按钮。
func (a *app) discTraceToggle(c *ui.Context, x discExec, open bool) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	count := x.TraceCount
	if tr, ok := b.traceData[x.ID]; ok && tr.EventCount > count {
		count = tr.EventCount
	}
	label := m.DiscTrace()
	if count > 0 {
		label = m.DiscTraceCount(count)
	}
	arrow := iconChevron
	if open {
		arrow = iconDown
	}
	btn := ui.Row(c.Key("trace-btn-" + x.ID)).Gap(2).AlignItems(ui.Center).Shrink(0).Role(ui.RoleButton)
	btn.Children(func() {
		ui.Text(c, label).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.Accent).Shrink(0)
		ui.Icon(c, arrow).FontSize(fsSmall).TextColor(t.Accent).Shrink(0)
	})
	if btn.Clicked() {
		if b.traceOpen == nil {
			b.traceOpen = map[string]bool{}
		}
		if open {
			delete(b.traceOpen, x.ID)
		} else {
			b.traceOpen[x.ID] = true
			a.loadDiscTrace(x.ID, true)
		}
	}
}

// discTracePanel 展开展示这一轮的工具/命令/技能/网页调用时间线（左侧细线缩进，不另画闭合框）。
func (a *app) discTracePanel(c *ui.Context, x discExec) {
	t, m := c.Theme(), L(c)
	b := &a.disc
	tr, has := b.traceData[x.ID]
	active := x.State == "running" || x.State == "waiting"
	// 进行中且已展开时每 2 秒自动刷新一次；已结束但缓存还没固化时补拉一次
	if active || !has || !tr.Finalized {
		if !b.traceBusy[x.ID] && time.Since(b.traceAt[x.ID]) >= discTraceRefreshInterval {
			a.loadDiscTrace(x.ID, false)
		}
		if active {
			c.After(discTraceRefreshInterval)
		}
	}
	ui.Row(c.Key("trace-panel-"+x.ID)).Gap(8).Padding(4, 0).MinWidth(0).Children(func() {
		ui.Box(c).Width(2).Radius(full(2)).Background(colors(t).line).Shrink(0)
		ui.Column(c).Gap(4).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			if !has && b.traceBusy[x.ID] {
				ui.Text(c, m.DiscTraceLoading()).FontSize(fsSmall).TextColor(t.TextMuted)
				return
			}
			if len(tr.Summary.Events) == 0 {
				ui.Text(c, m.DiscTraceEmpty()).FontSize(fsSmall).TextColor(t.TextMuted)
				return
			}
			events := tr.Summary.Events
			// 卡片里最多展最近 20 条，避免超长过程撑爆消息流；完整记录进终端看
			if len(events) > 20 {
				events = events[len(events)-20:]
			}
			for i, ev := range events {
				a.discTraceEventRow(c, x.ID, i, ev)
			}
		})
	})
}

func (a *app) discTraceEventRow(c *ui.Context, execID string, i int, ev discExecTraceEvent) {
	t := c.Theme()
	ui.Row(c.Key(fmt.Sprint("tev-", execID, "-", i))).Gap(6).AlignItems(ui.Center).MinWidth(0).Children(func() {
		ic := iconCode
		switch ev.Kind {
		case "cli":
			ic = iconTerminal
		case "skill":
			ic = iconSkill
		case "web":
			ic = iconGlobe
		case "search":
			ic = iconSearch
		}
		icColor := t.TextMuted
		if ev.Failed {
			icColor = t.Danger
		}
		ui.Icon(c, ic).FontSize(fsSmall).TextColor(icColor).Shrink(0)
		nameColor := t.Text
		if ev.Failed {
			nameColor = t.Danger
		}
		ui.Text(c, ev.Name).FontSize(fsSmall).Font(monoFont).FontWeight(fwMedium).TextColor(nameColor).Shrink(0)
		if ev.Detail != "" {
			ui.Text(c, ev.Detail).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
		}
		if ev.Count > 1 {
			ui.Text(c, fmt.Sprintf("x%d", ev.Count)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
		}
	})
}

// loadDiscTrace 异步向本机常驻进程拉取该轮的过程摘要。
func (a *app) loadDiscTrace(execID string, force bool) {
	a = a.windowOwner()
	b := &a.disc
	if b.traceBusy == nil {
		b.traceBusy = map[string]bool{}
	}
	if b.traceAt == nil {
		b.traceAt = map[string]time.Time{}
	}
	if b.traceData == nil {
		b.traceData = map[string]discExecTrace{}
	}
	if b.traceBusy[execID] {
		return
	}
	if !force {
		if tr, ok := b.traceData[execID]; ok && tr.Finalized {
			return
		}
	}
	b.traceBusy[execID] = true
	b.traceAt[execID] = time.Now()
	q := a.discussionRequest()
	go func() {
		var tr discExecTrace
		err := q.call("discussion.execution.trace", map[string]any{"execution": execID}, &tr, 10*time.Second)
		a.update(func() {
			delete(a.disc.traceBusy, execID)
			if err == nil {
				if a.disc.traceData == nil {
					a.disc.traceData = map[string]discExecTrace{}
				}
				a.disc.traceData[execID] = tr
			}
		})
	}()
}

// quoteDiscMessage 引用某条消息：若被引用的是可用的成员消息且当前输入框还没选接收者，自动预选该成员。
func (a *app) quoteDiscMessage(d *discDetail, msg discMessage) {
	b := &a.disc
	b.quote = msg.ID
	if len(b.targets) == 0 && b.mention == "" && msg.AuthorMember != "" &&
		(msg.Kind == "reply" || msg.Kind == "question" || msg.Kind == "delegate") {
		if mem := d.member(msg.AuthorMember); mem != nil && mem.RemovedAt == nil && d.canUse(*mem) {
			b.targets = []string{mem.ID}
			b.autoTarget = mem.ID
		}
	}
}

// clearDiscQuote 取消引用：若当前接收者仍是引用时自动预选的成员且未手动改过，一并恢复。
func (a *app) clearDiscQuote() {
	b := &a.disc
	b.quote = ""
	if b.autoTarget != "" && len(b.targets) == 1 && b.targets[0] == b.autoTarget {
		b.targets = nil
	}
	b.autoTarget = ""
}
