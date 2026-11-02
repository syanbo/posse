package app

import (
	"fmt"
	"log"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"posse/internal/agent"
	"posse/locales"
)

// --- 状态

// applyTasks 在主线程更新任务列表，并为用户自己派发的任务发通知；agent 派发的由它自己跟进。
func (a *app) applyTasks(ts []taskInfo) {
	a.tasks = ts
	a.indexTasks()
	for id := range a.lastTask {
		if a.task(id) == nil {
			delete(a.lastTask, id)
		}
	}
	for _, t := range ts {
		key := fmt.Sprintf("%s:%d", t.Status, t.Questions)
		prev, seen := a.lastTask[t.ID]
		a.lastTask[t.ID] = key
		if !seen || prev == key || t.Coordinator != "" {
			continue
		}
		var title string
		m := appL()
		switch {
		case t.Questions > 0 && !strings.HasSuffix(prev, fmt.Sprintf(":%d", t.Questions)) && t.Status == "running":
			title = m.TasksNoticeQuestion(agentTitle(t.Agent))
		case t.Status == "done" && !strings.HasPrefix(prev, "done"):
			title = m.TasksNoticeDone()
		case t.Status == "failed" && !strings.HasPrefix(prev, "failed"):
			title = m.TasksNoticeFailed()
		case t.Status == "exited" && !strings.HasPrefix(prev, "exited"):
			title = m.TasksNoticeExited()
		default:
			continue
		}
		if a.state.NotifyOff || a.win.IsFocused() && a.selected == taskBoardID && a.taskSel == t.ID {
			continue
		}
		n := mygo.NewNotification(mygo.NotificationOptions{ID: "posse-task:" + t.ID, Title: title, Body: t.Title, Group: "tasks"})
		go func() {
			if err := n.Show(); err != nil {
				log.Printf("notification: %v", err)
			}
		}()
	}
	if a.taskSel != "" && a.task(a.taskSel) == nil {
		a.taskSel, a.detail = "", nil
	}
}

func (a *app) task(id string) *taskInfo {
	for i := range a.tasks {
		if a.tasks[i].ID == id {
			return &a.tasks[i]
		}
	}
	return nil
}

// taskByWorker 找这个终端上的任务：有进行中的就用它，否则用最后一个。
func (a *app) taskByWorker(terminalID string) *taskInfo {
	var found *taskInfo
	for i := range a.tasks {
		if a.tasks[i].Worker != terminalID {
			continue
		}
		found = &a.tasks[i]
		if a.tasks[i].Status == "running" {
			return found
		}
	}
	return found
}

// indexTasks 排好总览的顺序，记下每个 worker 终端的任务标题（同一终端有多个时取最后一个）。
func (a *app) indexTasks() {
	a.sorted = sortTasks(a.tasks)
	a.workerTitle = make(map[string]string, len(a.tasks))
	a.coordOf = map[string]string{}
	a.runningFor = map[string]int{}
	for _, t := range a.tasks {
		if t.Worker != "" {
			a.workerTitle[t.Worker] = t.Title
			if t.Coordinator != "" {
				a.coordOf[t.Worker] = t.Coordinator
			} else {
				delete(a.coordOf, t.Worker)
			}
		}
		if t.Coordinator != "" && t.Status == "running" {
			a.runningFor[t.Coordinator]++
		}
	}
}

func (a *app) openTask(id string) {
	a.selected, a.revealSel = taskBoardID, true
	a.selectTask(id)
}

// showTaskHome 回到任务总览，不停留在上一个任务的详情里。
func (a *app) showTaskHome() {
	a.taskHome = true
	a.taskSel, a.detail = "", nil
	a.replyText, a.noteText = "", ""
	a.setWantedTask("")
}

func (a *app) selectTask(id string) {
	if a.taskSel == id && !a.taskHome {
		return
	}
	a.taskHome = false
	a.taskSel, a.replyText, a.noteText = id, "", ""
	a.detail = nil
	a.setWantedTask(id)
	// 详情刚清空，即使拉到的和上次一样也要交给界面
	a.pollForce.Store(true)
	// 按住方向键时每次都排一次完整轮询会堆起来：已有一次在等着就不再排，它开始时读到的就是最新选中的任务
	if !a.refreshQueued.Swap(true) {
		go a.refresh()
	}
}

// needsUser 判断任务是否在等用户：用户派发的任务里有没回复的问题。
func needsUser(t taskInfo) bool {
	return t.Coordinator == "" && t.Status == "running" && t.Questions > 0
}

// taskCounts 返回进行中的任务数和等用户回复的任务数。
func (a *app) taskCounts() (running, waiting int) {
	for _, t := range a.tasks {
		if t.Status == "running" {
			running++
		}
		if needsUser(t) {
			waiting++
		}
	}
	return
}

// taskGlyph 把任务映射到蜂巢格状态：进行中跟随 worker 里 agent 的状态，结束后是空心。
func taskGlyph(t taskInfo) string {
	switch {
	case needsUser(t):
		return agent.StatusWaiting
	case t.Status == "running":
		if t.Terminal != nil && t.Terminal.AgentState != nil && t.Terminal.AgentState.Status != "" {
			return t.Terminal.AgentState.Status
		}
		return agent.StatusWorking
	case t.Status == "done":
		return agent.StatusIdle
	}
	return "ended"
}

func taskStatusText(m locales.Messages, t taskInfo) string {
	switch t.Status {
	case "running":
		if needsUser(t) {
			return m.TasksStatusNeedsReply()
		}
		if t.Questions > 0 {
			return m.TasksStatusAwaitDispatcher()
		}
		if t.Terminal != nil && t.Terminal.AgentState != nil && t.Terminal.AgentState.Status == agent.StatusWaiting {
			return m.TasksStatusNeedsApproval()
		}
		return m.CommonInProgress()
	case "done":
		return m.CommonDone()
	case "failed":
		return m.CommonFailed()
	case "stopped":
		return m.TasksStatusStopped()
	case "exited":
		return m.TasksStatusExited()
	case "lost":
		return m.TasksStatusLost()
	}
	return t.Status
}

// elapsed 是 t 到现在过了多久，写成"20 分钟"；不到一分钟是"刚刚"。
func elapsed(m locales.Messages, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return m.AppJustNow()
	case d < time.Hour:
		return m.AppMinutes(int(d.Minutes()))
	case d < 48*time.Hour:
		return m.AppHours(int(d.Hours()))
	}
	return m.AppDays(int(d.Hours() / 24))
}

// --- 动作

func (a *app) openDispatch() {
	a.dispatching = true
	a.dTitle, a.dSpec, a.dMode = "", "", 0
	a.dBranch = "task-" + randomSuffix()
	choices := a.agentChoices()
	if !slices.ContainsFunc(choices, func(ag string) bool { return a.dAgents[ag] }) && len(choices) > 0 {
		a.dAgents = map[string]bool{choices[0]: true}
	}
	if w := a.workspace(a.selected); w != nil {
		a.dWorkspace = w.Label
	} else if t := a.task(a.taskSel); t != nil {
		a.dWorkspace = t.Workspace
	} else if len(a.workspaces) > 0 && a.workspaceByLabel(a.dWorkspace) == nil {
		a.dWorkspace = a.workspaces[0].Label
	}
}

func randomSuffix() string { return fmt.Sprintf("%04x", time.Now().UnixNano()&0xffff) }

func (a *app) workspaceByLabel(label string) *workspace {
	for i := range a.workspaces {
		if a.workspaces[i].Label == label {
			return &a.workspaces[i]
		}
	}
	return nil
}

func (a *app) projectOf(w *workspace) *project {
	for i := range a.projects {
		if a.projects[i].ID == w.ProjectID {
			return &a.projects[i]
		}
	}
	return nil
}

// dispatch 把同一个任务派给选中的每个 agent；各自建 worktree 时分支名加上 agent 后缀。
func (a *app) dispatch(workspaceID, title, spec, branch string, agents []string) {
	group := ""
	if len(agents) > 1 {
		group = "g_" + randomSuffix()
	}
	first := ""
	for _, ag := range agents {
		params := map[string]any{"workspace": workspaceID, "agent": ag, "title": title, "spec": spec, "group": group}
		if branch != "" {
			name := branch
			if len(agents) > 1 {
				name += "-" + ag
			}
			params["worktree"] = name
		}
		var t taskInfo
		if a.call("task.start", params, &t, 2*time.Minute) != nil {
			break
		}
		if first == "" {
			first = t.ID
		}
	}
	a.refresh()
	if first != "" {
		a.win.Update(func() { a.openTask(first) })
	}
}

func (a *app) reply(questionID, text string) {
	if a.call("task.reply", map[string]any{"message": questionID, "body": text}, nil, 10*time.Second) == nil {
		a.win.Update(func() { a.replyText = "" })
	}
	a.refresh()
}

func (a *app) sendNote(taskID, text string) {
	if a.call("task.send", map[string]any{"task": taskID, "body": text}, nil, 10*time.Second) == nil {
		a.win.Update(func() { a.noteText = "" })
	}
	a.refresh()
}

func (a *app) askCloseTask(m locales.Messages, t taskInfo) {
	body := m.TasksCloseBody()
	if t.Status == "running" {
		body = m.TasksCloseRunningBody()
	}
	a.confirm(m.TasksCloseConfirm(agentTitle(t.Agent)), body, m.CommonClose(), func() {
		a.call("task.close", map[string]any{"task": t.ID}, nil, 15*time.Second)
		a.refresh()
	})
}

// viewDiff 在 worktree 里开一个终端显示相对分叉点的改动，看完留在 shell 里。
func (a *app) viewDiff(t taskInfo) {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/zsh"
	}
	mb := "$(git merge-base " + agent.Quote(t.BaseRef) + " HEAD)"
	script := "git --no-pager diff --stat " + mb + " && git status --short && git diff " + mb + "; exec " + agent.Quote(sh) + " -l"
	go func() {
		var term terminalInfo
		if a.call("terminal.create", map[string]any{"workspace": t.WorkspaceID, "command": []string{sh, "-l", "-i", "-c", script}}, &term, 10*time.Second) != nil {
			return
		}
		a.refresh()
		a.win.Update(func() { a.openTerminal(t.WorkspaceID, term.ID) })
	}()
}

// --- 侧栏入口

func (a *app) taskSidebarItem(c *ui.Context) ui.Element {
	t := c.Theme()
	running, waiting := a.taskCounts()
	var badge func()
	if running > 0 || waiting > 0 {
		badge = func() {
			if waiting > 0 {
				statusGlyph(c, agent.StatusWaiting, smallIcon())
				return
			}
			// 进行中只写个数，不跟转动的图标：这是入口，不是哪个会话的状态
			ui.Text(c, fmt.Sprint(running)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(L(c).TasksRunningTip(running))
		}
	}
	item := a.navTile(c, taskBoardID, iconTasks, L(c).TasksTitle(), badge)
	if a.revealSel && a.selected == taskBoardID {
		item.ScrollIntoView()
		a.revealSel = false
	}
	return item
}

// --- 总览

func (a *app) taskBoard(c *ui.Context) {
	if a.taskHome {
		a.setWantedTask("")
	} else {
		a.setWantedTask(a.taskSel)
	}
	ended := 0
	for _, x := range a.tasks {
		if x.Status != "running" {
			ended++
		}
	}
	m := L(c)
	a.band(c, func() {
		titleText(c, m.TasksTitle(), fsTitle)
		ui.Spacer(c)
		if ended > 0 {
			if pressable(textButton(c, m.TasksClearEnded())).Clicked() {
				go func() {
					a.call("task.clear", nil, nil, 10*time.Second)
					a.refresh()
				}()
			}
		}
		// 空总览中间已有同一个按钮
		if len(a.tasks) > 0 && pressable(accentButton(c, m.TasksDispatchEllipsis())).Clicked() {
			a.openDispatch()
		}
		a.closeBoardButton(c)
	})
	if len(a.tasks) == 0 {
		a.emptyBoard(c)
		return
	}
	// 从别的页面进来、还没指定要看总览时，打开列表里的第一个。点侧栏「任务」是要总览，不帮它再选上一个。
	if !a.taskHome && a.task(a.taskSel) == nil {
		a.selectTask(a.sorted[0].ID)
	}
	ui.Row(c).Grow(1).AlignItems(ui.Stretch).Padding(pageGap).Gap(pageGap).Children(func() {
		// 可以聚焦：点过列表后方向键归它，不被侧栏拿走（和历史页一样）
		card(c).Width(360).Shrink(0).Clip().Focusable().FocusRing(false).Label(m.TasksList()).Children(func() {
			ui.Scroll(c).Grow(1).Children(func() {
				// 项与项之间留 4：悬停和选中的底色不粘在一起（和历史列表一样）
				ui.Column(c).Padding(4, 0, 8, 0).Gap(4).Children(func() { a.taskList(c) })
			})
		})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			if sel := a.task(a.taskSel); sel != nil && !a.taskHome {
				ui.Scroll(c).Grow(1).Children(func() { a.taskDetailView(c, *sel) })
				return
			}
			a.taskEmptyCard(c)
		})
	})
	// 上下方向键换选中的任务，和历史页一样；焦点在回复框里时归输入框，开着对话框时不动
	if a.dispatching || a.confirming {
		return
	}
	switch {
	case c.Shortcut(0, ui.KeyDown):
		a.stepTask(1)
	case c.Shortcut(0, ui.KeyUp):
		a.stepTask(-1)
	}
}

// stepTask 选中列表里的下一个（step 为 1）或上一个任务；还没选中时下键选第一个、上键选最后一个。到头了不动。
func (a *app) stepTask(step int) {
	// 换任务会清掉回复和追加的草稿：写了一半时不让方向键顺手丢掉，要换就点别的任务
	if len(a.sorted) == 0 || strings.TrimSpace(a.replyText) != "" || strings.TrimSpace(a.noteText) != "" {
		return
	}
	i := -1
	if !a.taskHome {
		i = slices.IndexFunc(a.sorted, func(t taskInfo) bool { return t.ID == a.taskSel })
	}
	j := i + step
	if i < 0 {
		j = 0
		if step < 0 {
			j = len(a.sorted) - 1
		}
	}
	if j >= 0 && j < len(a.sorted) {
		a.taskScroll = a.sorted[j].ID
		a.selectTask(a.sorted[j].ID)
	}
}

// taskEmptyCard 是没选任务时右边的说明卡片，和历史页没选会话时的一样。
func (a *app) taskEmptyCard(c *ui.Context) {
	t, m := c.Theme(), L(c)
	var counts [3]int
	for _, k := range a.tasks {
		counts[taskSection(k)]++
	}
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).MaxWidth(420).Padding(24).Gap(12).Children(func() {
			tile(c, iconTasks, 48, colors(t).tint, t.Accent)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, m.TasksTitle(), fsPage)
				ui.Text(c, m.TasksHomeNote()).
					FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			if len(a.tasks) > 0 {
				ui.Row(c).Gap(12).Children(func() {
					for i, label := range []func(int) string{m.TasksCountPending, m.TasksCountRunning, m.TasksCountEnded} {
						if counts[i] > 0 {
							ui.Text(c, label(counts[i])).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
						}
					}
				})
			}
		})
	})
}

// taskSection 是任务在总览里的分组：0 等用户回复，1 进行中，2 已结束。
func taskSection(t taskInfo) int {
	switch {
	case needsUser(t):
		return 0
	case t.Status == "running":
		return 1
	}
	return 2
}

// sortTasks 按分组排，同组里新的在前。
func sortTasks(tasks []taskInfo) []taskInfo {
	ts := slices.Clone(tasks)
	slices.SortStableFunc(ts, func(x, y taskInfo) int {
		if sx, sy := taskSection(x), taskSection(y); sx != sy {
			return sx - sy
		}
		return y.CreatedAt.Compare(x.CreatedAt)
	})
	return ts
}

func (a *app) taskList(c *ui.Context) {
	t, m := c.Theme(), L(c)
	// 列表里有"几分钟"这样的相对时间，界面不再每秒重建，自己约下一帧
	c.After(30 * time.Second)
	section := -1
	for _, task := range a.sorted {
		if s := taskSection(task); s != section {
			section = s
			label := []string{m.TasksSectionNeedsReply(), m.CommonInProgress(), m.TasksSectionEnded()}[s]
			ui.Text(c.Key(fmt.Sprint("section-", s)), label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(12, 20, 4, 20)
		}
		a.taskRow(c, task)
	}
}

func (a *app) taskRow(c *ui.Context, task taskInfo) {
	t, m := c.Theme(), L(c)
	row := ui.Row(c.Key(task.ID)).Margin(0, 8).Padding(8, 12).Gap(8).Radius(rControl).AlignItems(ui.Start)
	selectedBg(c, row, a.taskSel == task.ID)
	row.Children(func() {
		ui.Box(c).Padding(2, 0, 0, 0).Children(func() { taskGlyphIcon(c, task, fsBody) })
		// MinWidth(0)：标题长时让它省略，不把右边的时间挤出卡片
		ui.Column(c).Grow(1).Shrink(1).Basis(0).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Text(c, task.Title).FontSize(fsBody).FontWeight(fwMedium).MaxLines(1).Shrink(1).MinWidth(0)
				// 结束但不是完成的任务，状态写成文字跟在标题后面，不只靠颜色
				if task.Status != "running" && task.Status != "done" {
					color := t.TextMuted
					if task.Status == "failed" {
						color = t.Danger
					}
					ui.Text(c, taskStatusText(m, task)).FontSize(fsSmall).FontWeight(fwMedium).TextColor(color).Shrink(0)
				}
				ui.Spacer(c)
				ui.Text(c, elapsed(L(c), task.CreatedAt)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				// 图标代替 agent 名字，悬停提示是名字
				agentTile(c, task.Agent, smallIcon())
				ui.Text(c, task.Workspace).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			})
			preview, color := taskStatusText(m, task), t.TextMuted
			switch {
			case needsUser(task):
				color = colors(t).honey
			case task.Summary != "":
				preview = task.Summary
			}
			ui.Text(c, preview).FontSize(fsSmall).TextColor(color).MaxLines(2).LineHeight(1.35)
		})
	})
	if a.taskScroll == task.ID {
		a.taskScroll = ""
		row.ScrollIntoView()
	}
	if row.Clicked() {
		a.selectTask(task.ID)
	}
}

// taskGlyphIcon 画任务的状态格；失败、停止、退出都是空心灰，靠旁边的文字区分。
func taskGlyphIcon(c *ui.Context, task taskInfo, size float32) {
	g := taskGlyph(task)
	if g == "ended" {
		t := c.Theme()
		color := t.TextMuted
		if task.Status == "failed" {
			color = t.Danger
		}
		ui.Icon(c, iconHexOutline).FontSize(size).TextColor(color).Shrink(0).Tooltip(taskStatusText(L(c), task))
		return
	}
	statusGlyph(c, g, size)
}

func (a *app) emptyBoard(c *ui.Context) {
	t, m := c.Theme(), L(c)
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).MaxWidth(460).Padding(24).Gap(16).Children(func() {
			tile(c, iconTasks, 48, colors(t).tint, t.Accent)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, m.TasksEmptyTitle(), fsPage)
				ui.Text(c, m.TasksEmptyBody()).FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			ui.Row(c).Gap(8).Children(func() {
				if pressable(accentButton(c, m.TasksDispatchEllipsis())).Clicked() {
					a.openDispatch()
				}
			})
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, "posse task guide").FontSize(fsSmall).Font(monoFont).TextColor(t.Text).Padding(2, 6).Radius(rSmall).Background(t.Surface)
				ui.Text(c, m.TasksGuideNote()).FontSize(fsSmall).TextColor(t.TextMuted)
			})
		})
	})
}

func (a *app) taskDetailView(c *ui.Context, task taskInfo) {
	t, m := c.Theme(), L(c)
	var msgs []taskMessage
	if a.detail != nil && a.detail.ID == task.ID {
		msgs = a.detail.Messages
	}
	ui.Column(c).Gap(pageGap).MaxWidth(860).Children(func() {
		card(c).Padding(cardPad).Gap(12).Children(func() {
			ui.Row(c).Gap(12).AlignItems(ui.Start).Children(func() {
				agentTile(c, task.Agent, tileLarge())
				ui.Column(c).Gap(8).Grow(1).Shrink(1).Children(func() {
					titleText(c, task.Title, fsPage).LineHeight(1.3).Selectable()
					status := func() {
						ui.Row(c).Gap(4).Children(func() {
							taskGlyphIcon(c, task, fsSmall)
							color := t.TextMuted
							if needsUser(task) {
								color = colors(t).honey
							}
							ui.Text(c, taskStatusText(m, task)).FontSize(fsSmall).TextColor(color).MaxLines(1)
						})
					}
					metaRow(c, status, meta(c, agentTitle(task.Agent)), metaMono(c, workspaceIcon(task.WorkspaceKind), task.Workspace, ""),
						meta(c, a.coordinatorLabel(m, task)), meta(c, L(c).AppDateTime(task.CreatedAt)))
				})
			})
			a.taskActions(c, task)
			if task.WorkspaceKind == "worktree" {
				a.diffLine(c, task)
			}
		})
		if task.Summary != "" {
			title := m.TasksReport()
			if task.Status == "running" {
				title = m.TasksLastReport() // 追加的活还在做
			}
			a.block(c, title, func() {
				ui.Text(c, task.Summary).FontSize(fsBody).LineHeight(1.55).Selectable()
				if task.Report != "" {
					ui.Text(c, task.Report).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Selectable()
				}
			})
		}
		if open := a.thread(c, task, msgs); open != nil && task.Status == "running" {
			q := *open
			placeholder := m.TasksReplyPlaceholder()
			if task.Coordinator != "" {
				placeholder = m.TasksReplyForDispatcher()
			}
			if composer(c, &a.replyText, placeholder, m.TasksReplyButton(), true) {
				go a.reply(q.ID, strings.TrimSpace(a.replyText))
			}
		} else if task.Status == "running" && task.Terminal != nil {
			if composer(c, &a.noteText, m.TasksNotePlaceholder(), m.TasksSend(), false) {
				go a.sendNote(task.ID, strings.TrimSpace(a.noteText))
			}
		} else if followable(task) {
			if composer(c, &a.noteText, m.TasksFollowUpPlaceholder(), m.TasksSend(), false) {
				go a.sendNote(task.ID, strings.TrimSpace(a.noteText))
			}
		}
		if task.Spec != "" {
			a.block(c, m.TasksSpec(), func() {
				// 派发方写的说明多是 Markdown（标题、列表、代码），和历史页 agent 的回复一样渲染
				ui.Column(c).Gap(8).Selectable().Children(func() {
					mdView{size: a.mdSize()}.blocks(c, a.specMarkdown(task.Spec))
				})
			})
		}
	})
}

// followable 是用户派发、已汇报、worker 里的 agent 还在的任务：追加的活由常驻进程重新打开任务交给它。
// agent 派发的任务不在这里追加，免得派发方收到一次不知来由的汇报；要追加就对派发方说，或者直接在 worker 终端里说。
func followable(task taskInfo) bool {
	return task.Coordinator == "" && (task.Status == "done" || task.Status == "failed") &&
		task.Terminal != nil && task.Terminal.Running && task.Terminal.Agent != "" && (task.Agent == "" || task.Terminal.Agent == task.Agent)
}

func (a *app) coordinatorLabel(m locales.Messages, task taskInfo) string {
	if task.Coordinator == "" {
		return m.TasksByYou()
	}
	if _, term := a.findTerminal(task.Coordinator); term != nil {
		return m.TasksByAgent(agentTitle(term.Agent))
	}
	return m.TasksByAnyAgent()
}

func (a *app) taskActions(c *ui.Context, task taskInfo) {
	if task.Terminal == nil && task.WorkspaceKind != "worktree" {
		return
	}
	m := L(c)
	// 英文的按钮长，详情窄时换行
	ui.Row(c).Gap(8).Wrap().Children(func() {
		if task.Terminal != nil {
			if pressable(textButton(c, m.TasksOpenWorker())).Clicked() {
				a.openTerminal(task.WorkspaceID, task.Worker)
			}
		}
		if task.WorkspaceKind == "worktree" {
			if pressable(textButton(c, m.TasksViewChanges())).Clicked() {
				a.viewDiff(task)
			}
			if task.Status != "running" && pressable(textButton(c, m.TasksMergeInto(task.BaseRef))).Clicked() {
				a.askMergeWorkspace(m, task.WorkspaceID)
			}
		}
		ui.Spacer(c)
		if task.Terminal != nil {
			if pressable(textButton(c, m.TasksCloseWorker())).Clicked() {
				a.askCloseTask(m, task)
			}
		}
	})
}

func (a *app) diffLine(c *ui.Context, task taskInfo) {
	t, m := c.Theme(), L(c)
	d := a.diffs[task.WorkspaceID]
	text := m.TasksCounting()
	if d != nil {
		parts := []string{}
		if d.Commits > 0 {
			parts = append(parts, m.TasksCommits(d.Commits))
		}
		if d.Files > 0 {
			parts = append(parts, m.TasksFilesChanged(d.Files, d.Insertions, d.Deletions))
		}
		if d.Untracked > 0 {
			parts = append(parts, m.TasksUntracked(d.Untracked))
		}
		text = strings.Join(parts, m.TasksDiffSeparator())
		if text == "" {
			text = m.TasksNoChanges()
		}
		if d.Dirty {
			text = m.TasksDiffDirty(text)
		}
	}
	ui.Row(c).Gap(8).Padding(12, 12).Radius(rControl).Background(colors(t).wash).Children(func() {
		ui.Icon(c, iconBranch).FontSize(fsBody).TextColor(t.TextMuted)
		ui.Text(c, m.TasksBranchVsBase(task.Branch, task.BaseRef)).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
		ui.Text(c, text).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(1)
	})
}

// block 是详情里的一张卡片：标题加内容。
// specMarkdown 返回任务说明解析好的 Markdown，只缓存正在看的这一份：说明不变时不必每帧重新解析。
func (a *app) specMarkdown(spec string) []mdBlock {
	if a.specMD.src != spec || a.specMD.blocks == nil {
		a.specMD.src, a.specMD.blocks = spec, parseMarkdown(strings.TrimSpace(spec))
	}
	return a.specMD.blocks
}

func (a *app) block(c *ui.Context, label string, fn func()) {
	card(c).Padding(cardPad).Gap(8).Children(func() {
		cardTitle(c, label)
		fn()
	})
}

// monoFont 用于路径和命令；系统的 monospace 在部分环境下会落到 Courier
const monoFont = "SF Mono, Menlo, monospace"

var messageLabels = map[string]func(locales.Messages) string{
	"question": locales.Messages.TasksMsgQuestion, "answer": locales.Messages.TasksMsgAnswer, "note": locales.Messages.TasksMsgNote,
	"done": locales.Messages.TasksReport, "exit": locales.Messages.TasksMsgExit,
}

// messageLabel 是往来消息里一条消息的种类名，认不出的种类不写。
func messageLabel(m locales.Messages, kind string) string {
	if f := messageLabels[kind]; f != nil {
		return f(m)
	}
	return ""
}

// thread 画往来消息，返回还没回答的提问（没有为 nil）。
func (a *app) thread(c *ui.Context, task taskInfo, msgs []taskMessage) *taskMessage {
	t, l := c.Theme(), L(c)
	answered := map[string]bool{}
	var open *taskMessage
	var shown []taskMessage
	last := -1
	for i, m := range msgs {
		if m.Kind == "done" {
			last = i
		}
	}
	for i, m := range msgs {
		if m.Kind == "answer" {
			answered[m.ReplyTo] = true
		}
		// 最近一次汇报已在上面单独显示，追加过的任务前几轮的汇报留在往来消息里
		if i != last {
			shown = append(shown, m)
		}
	}
	for i := range msgs {
		if msgs[i].Kind == "question" && !answered[msgs[i].ID] {
			open = &msgs[i]
		}
	}
	if len(shown) == 0 {
		return open
	}
	a.block(c, l.TasksMessages(), func() {
		ui.Column(c).Gap(12).Children(func() {
			for _, m := range shown {
				ui.Column(c.Key(m.ID)).Gap(4).Children(func() {
					ui.Row(c).Gap(8).Children(func() {
						from := "worker"
						if m.Kind == "answer" || m.Kind == "note" {
							from = l.TasksDispatcher()
						}
						ui.Text(c, messageLabel(l, m.Kind)).FontSize(fsSmall).FontWeight(fwMedium)
						ui.Text(c, from).FontSize(fsSmall).TextColor(t.TextMuted)
						ui.Text(c, m.CreatedAt.Format("15:04")).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
						if m.Kind == "question" && !answered[m.ID] && task.Status == "running" {
							label, color := l.TasksStatusAwaitDispatcher(), t.TextMuted
							if task.Coordinator == "" {
								label, color = l.TasksStatusNeedsReply(), colors(t).honey
							}
							ui.Text(c, label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(color)
						}
					})
					ui.Text(c, m.Body).FontSize(fsBody).LineHeight(1.55).Selectable()
				})
			}
		})
	})
	return open
}

// --- 派发对话框

func (a *app) dispatchDialog(c *ui.Context) {
	t, m := c.Theme(), L(c)
	ui.Modal(c, &a.dispatching, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			titleText(c, m.TasksDispatchTitle(), fsTitle)
			var labels []string
			for _, w := range a.workspaces {
				labels = append(labels, w.Label)
			}
			w := a.workspaceByLabel(a.dWorkspace)
			isGit := false
			if w != nil {
				if p := a.projectOf(w); p != nil && p.Kind == "git" {
					isGit = true
				}
			}
			ui.Column(c).Gap(6).Children(func() {
				ui.Select(c, &a.dWorkspace, labels)
				hint := m.TasksDirHint()
				if isGit && a.dMode == 1 {
					hint = m.TasksWorktreeHint()
				}
				ui.Text(c, hint).FontSize(fsSmall).TextColor(t.TextMuted)
			})
			fieldInput(c, &a.dTitle, m.TasksTitlePlaceholder()).AutoFocus()
			fieldArea(c, &a.dSpec, m.TasksSpecPlaceholder(), 136)
			ui.Row(c).Gap(16).Children(func() {
				ui.Text(c, m.TasksAssignTo()).FontSize(fsBody).TextColor(t.TextMuted)
				if a.dAgents == nil {
					a.dAgents = map[string]bool{}
				}
				for _, name := range a.agentChoices() {
					on := a.dAgents[name]
					ui.Checkbox(c.Key("agent-"+name), &on, agentTitle(name))
					a.dAgents[name] = on
				}
				if len(a.agentChoices()) == 0 {
					ui.Text(c, m.TasksNoAgents()).FontSize(fsBody).TextColor(t.TextMuted)
				}
			})
			if isGit {
				segmented(c, &a.dMode, m.TasksWorkDir(), m.TasksInWorkspace(), m.TasksNewWorktrees())
				if a.dMode == 1 {
					ui.Column(c).Gap(6).Children(func() {
						fieldInput(c, &a.dBranch, m.TasksBranchPlaceholder())
						ui.Text(c, m.TasksBranchNote()).FontSize(fsSmall).TextColor(t.TextMuted)
					})
				}
			}
			var agents []string
			for _, name := range a.agentChoices() {
				if a.dAgents[name] {
					agents = append(agents, name)
				}
			}
			ready := w != nil && len(agents) > 0 && strings.TrimSpace(a.dSpec) != "" && (!isGit || a.dMode == 0 || strings.TrimSpace(a.dBranch) != "")
			ui.Row(c).Gap(8).Justify(ui.End).Padding(8, 0, 0, 0).Children(func() {
				if pressable(textButton(c, m.CommonCancel())).Clicked() {
					a.dispatching = false
				}
				verb := m.TasksDispatch()
				if len(agents) > 1 {
					verb = m.TasksDispatchTo(len(agents))
				}
				btn := accentButton(c, verb).Disabled(!ready)
				if pressable(btn).Clicked() && ready {
					a.dispatching = false
					branch := ""
					if isGit && a.dMode == 1 {
						branch = strings.TrimSpace(a.dBranch)
					}
					go a.dispatch(w.ID, strings.TrimSpace(a.dTitle), strings.TrimSpace(a.dSpec), branch, agents)
				}
			})
		})
	})
}
