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
		switch {
		case t.Questions > 0 && !strings.HasSuffix(prev, fmt.Sprintf(":%d", t.Questions)) && t.Status == "running":
			title = agentTitle(t.Agent) + " 在等你回复"
		case t.Status == "done" && !strings.HasPrefix(prev, "done"):
			title = "任务完成"
		case t.Status == "failed" && !strings.HasPrefix(prev, "failed"):
			title = "任务失败"
		case t.Status == "exited" && !strings.HasPrefix(prev, "exited"):
			title = "worker 没有汇报就退出了"
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

func taskStatusText(t taskInfo) string {
	switch t.Status {
	case "running":
		if needsUser(t) {
			return "待你回复"
		}
		if t.Questions > 0 {
			return "等派发方回复"
		}
		if t.Terminal != nil && t.Terminal.AgentState != nil && t.Terminal.AgentState.Status == agent.StatusWaiting {
			return "待处理：worker 在等你批准"
		}
		return "进行中"
	case "done":
		return "已完成"
	case "failed":
		return "失败"
	case "stopped":
		return "已停止"
	case "exited":
		return "没有汇报就退出了"
	case "lost":
		return "常驻进程重启时中断"
	}
	return t.Status
}

func elapsed(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	}
	return fmt.Sprintf("%d 天", int(d.Hours()/24))
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

func (a *app) askCloseTask(t taskInfo) {
	body := "worker 终端会被关闭。"
	if t.Status == "running" {
		body = "任务还没汇报，关闭后记为已停止，worker 正在做的事会被打断。"
	}
	a.confirm("关闭 "+agentTitle(t.Agent)+" worker？", body, "关闭", func() {
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
			ui.Text(c, fmt.Sprint(running)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Tooltip(fmt.Sprintf("%d 个任务进行中", running))
		}
	}
	item := a.navTile(c, taskBoardID, iconTasks, "任务", badge)
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
	a.band(c, func() {
		titleText(c, "任务", fsTitle)
		ui.Spacer(c)
		if ended > 0 {
			if pressable(textButton(c, "清除已结束")).Clicked() {
				go func() {
					a.call("task.clear", nil, nil, 10*time.Second)
					a.refresh()
				}()
			}
		}
		// 空总览中间已有同一个按钮
		if len(a.tasks) > 0 && pressable(accentButton(c, "派发任务…")).Clicked() {
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
		card(c).Width(360).Shrink(0).Clip().Focusable().FocusRing(false).Label("任务列表").Children(func() {
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
	t := c.Theme()
	var counts [3]int
	for _, k := range a.tasks {
		counts[taskSection(k)]++
	}
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).MaxWidth(420).Padding(24).Gap(12).Children(func() {
			tile(c, iconTasks, 48, colors(t).tint, t.Accent)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, "任务", fsPage)
				ui.Text(c, "把活儿派给其他 agent，在这里看进度、回答它们的问题、合入结果。从左边选一个任务看详情。").
					FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			if len(a.tasks) > 0 {
				ui.Row(c).Gap(12).Children(func() {
					for i, label := range []string{"待处理", "进行中", "已结束"} {
						if counts[i] > 0 {
							ui.Text(c, fmt.Sprintf("%s %d", label, counts[i])).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
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
	t := c.Theme()
	// 列表里有"几分钟"这样的相对时间，界面不再每秒重建，自己约下一帧
	c.After(30 * time.Second)
	section := -1
	for _, task := range a.sorted {
		if s := taskSection(task); s != section {
			section = s
			label := []string{"需要你回复", "进行中", "已结束"}[s]
			ui.Text(c.Key(fmt.Sprint("section-", s)), label).FontSize(fsSmall).FontWeight(fwMedium).TextColor(t.TextMuted).Padding(12, 20, 4, 20)
		}
		a.taskRow(c, task)
	}
}

func (a *app) taskRow(c *ui.Context, task taskInfo) {
	t := c.Theme()
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
					ui.Text(c, taskStatusText(task)).FontSize(fsSmall).FontWeight(fwMedium).TextColor(color).Shrink(0)
				}
				ui.Spacer(c)
				ui.Text(c, elapsed(task.CreatedAt)).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted).Shrink(0)
			})
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				// 图标代替 agent 名字，悬停提示是名字
				agentTile(c, task.Agent, smallIcon())
				ui.Text(c, task.Workspace).FontSize(fsSmall).TextColor(t.TextMuted).MaxLines(1).Shrink(1)
			})
			preview, color := taskStatusText(task), t.TextMuted
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
		ui.Icon(c, iconHexOutline).FontSize(size).TextColor(color).Shrink(0).Tooltip(taskStatusText(task))
		return
	}
	statusGlyph(c, g, size)
}

func (a *app) emptyBoard(c *ui.Context) {
	t := c.Theme()
	ui.Column(c).Grow(1).Center().Children(func() {
		emptyCard(c).MaxWidth(460).Padding(24).Gap(16).Children(func() {
			tile(c, iconTasks, 48, colors(t).tint, t.Accent)
			ui.Column(c).Gap(6).Children(func() {
				titleText(c, "还没有任务", fsPage)
				ui.Text(c, "把一件事交给 Claude、Codex 或 Grok 去做，它们做完会在这里汇报，有问题也会在这里问你。也可以让一个 agent 当协调者，派活给其他 agent。").FontSize(fsBody).TextColor(t.TextMuted).LineHeight(1.5)
			})
			ui.Row(c).Gap(8).Children(func() {
				if pressable(accentButton(c, "派发任务…")).Clicked() {
					a.openDispatch()
				}
			})
			ui.Row(c).Gap(6).Children(func() {
				ui.Text(c, "posse task guide").FontSize(fsSmall).Font(monoFont).TextColor(t.Text).Padding(2, 6).Radius(rSmall).Background(t.Surface)
				ui.Text(c, "Agent 的用法说明").FontSize(fsSmall).TextColor(t.TextMuted)
			})
		})
	})
}

func (a *app) taskDetailView(c *ui.Context, task taskInfo) {
	t := c.Theme()
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
							ui.Text(c, taskStatusText(task)).FontSize(fsSmall).TextColor(color).MaxLines(1)
						})
					}
					metaRow(c, status, meta(c, agentTitle(task.Agent)), metaMono(c, workspaceIcon(task.WorkspaceKind), task.Workspace, ""),
						meta(c, a.coordinatorLabel(task)), meta(c, task.CreatedAt.Format("1月2日 15:04")))
				})
			})
			a.taskActions(c, task)
			if task.WorkspaceKind == "worktree" {
				a.diffLine(c, task)
			}
		})
		if task.Summary != "" {
			title := "汇报"
			if task.Status == "running" {
				title = "上一轮汇报" // 追加的活还在做
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
			placeholder := "回答 worker 的提问"
			if task.Coordinator != "" {
				placeholder = "替派发方回答（它也可能自己回答）"
			}
			if composer(c, &a.replyText, placeholder, "回复", true) {
				go a.reply(q.ID, strings.TrimSpace(a.replyText))
			}
		} else if task.Status == "running" && task.Terminal != nil {
			if composer(c, &a.noteText, "补充说明：worker 会在下一个检查点读到", "发送", false) {
				go a.sendNote(task.ID, strings.TrimSpace(a.noteText))
			}
		} else if followable(task) {
			if composer(c, &a.noteText, "追加任务：交给同一个 worker 接着做", "发送", false) {
				go a.sendNote(task.ID, strings.TrimSpace(a.noteText))
			}
		}
		if task.Spec != "" {
			a.block(c, "任务说明", func() {
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

func (a *app) coordinatorLabel(task taskInfo) string {
	if task.Coordinator == "" {
		return "你派发的"
	}
	if _, term := a.findTerminal(task.Coordinator); term != nil {
		return agentTitle(term.Agent) + " 派发的"
	}
	return "Agent 派发的"
}

func (a *app) taskActions(c *ui.Context, task taskInfo) {
	if task.Terminal == nil && task.WorkspaceKind != "worktree" {
		return
	}
	ui.Row(c).Gap(8).Children(func() {
		if task.Terminal != nil {
			if pressable(textButton(c, "打开 worker 终端")).Clicked() {
				a.openTerminal(task.WorkspaceID, task.Worker)
			}
		}
		if task.WorkspaceKind == "worktree" {
			if pressable(textButton(c, "查看改动")).Clicked() {
				a.viewDiff(task)
			}
			if task.Status != "running" && pressable(textButton(c, "合并到 "+task.BaseRef)).Clicked() {
				a.askMergeWorkspace(task.WorkspaceID)
			}
		}
		ui.Spacer(c)
		if task.Terminal != nil {
			if pressable(textButton(c, "关闭 worker")).Clicked() {
				a.askCloseTask(task)
			}
		}
	})
}

func (a *app) diffLine(c *ui.Context, task taskInfo) {
	t := c.Theme()
	d := a.diffs[task.WorkspaceID]
	text := "正在统计改动…"
	if d != nil {
		parts := []string{}
		if d.Commits > 0 {
			parts = append(parts, fmt.Sprintf("%d 个提交", d.Commits))
		}
		if d.Files > 0 {
			parts = append(parts, fmt.Sprintf("改了 %d 个文件，+%d −%d", d.Files, d.Insertions, d.Deletions))
		}
		if d.Untracked > 0 {
			parts = append(parts, fmt.Sprintf("%d 个新文件未跟踪", d.Untracked))
		}
		text = strings.Join(parts, "，")
		if text == "" {
			text = "还没有改动"
		}
		if d.Dirty {
			text += "。有未提交的改动，合并前要先提交"
		}
	}
	ui.Row(c).Gap(8).Padding(12, 12).Radius(rControl).Background(colors(t).wash).Children(func() {
		ui.Icon(c, iconBranch).FontSize(fsBody).TextColor(t.TextMuted)
		ui.Text(c, task.Branch+" 相对 "+task.BaseRef).FontSize(fsSmall).Font(monoFont).TextColor(t.TextMuted).Shrink(0)
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

var messageLabels = map[string]string{"question": "提问", "answer": "回复", "note": "补充说明", "done": "汇报", "exit": "退出"}

// thread 画往来消息，返回还没回答的提问（没有为 nil）。
func (a *app) thread(c *ui.Context, task taskInfo, msgs []taskMessage) *taskMessage {
	t := c.Theme()
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
	a.block(c, "往来消息", func() {
		ui.Column(c).Gap(12).Children(func() {
			for _, m := range shown {
				ui.Column(c.Key(m.ID)).Gap(4).Children(func() {
					ui.Row(c).Gap(8).Children(func() {
						from := "worker"
						if m.Kind == "answer" || m.Kind == "note" {
							from = "派发方"
						}
						ui.Text(c, messageLabels[m.Kind]).FontSize(fsSmall).FontWeight(fwMedium)
						ui.Text(c, from).FontSize(fsSmall).TextColor(t.TextMuted)
						ui.Text(c, m.CreatedAt.Format("15:04")).FontSize(fsSmall).FontFeatures("tnum").TextColor(t.TextMuted)
						if m.Kind == "question" && !answered[m.ID] && task.Status == "running" {
							label, color := "等派发方回复", t.TextMuted
							if task.Coordinator == "" {
								label, color = "待你回复", colors(t).honey
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
	t := c.Theme()
	ui.Modal(c, &a.dispatching, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			titleText(c, "派发任务", fsTitle)
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
				hint := "worker 在这个工作区的目录里工作。"
				if isGit && a.dMode == 1 {
					hint = "每个 agent 各开一个 git worktree 和分支，改动互不干扰，做完可以对比后合并。"
				}
				ui.Text(c, hint).FontSize(fsSmall).TextColor(t.TextMuted)
			})
			fieldInput(c, &a.dTitle, "标题，比如 修复登录页闪退").AutoFocus()
			fieldArea(c, &a.dSpec, "要做什么、范围在哪、不能动什么、怎样算完成。worker 看不到你和其他 agent 的对话，这里要写全。", 136)
			ui.Row(c).Gap(16).Children(func() {
				ui.Text(c, "交给").FontSize(fsBody).TextColor(t.TextMuted)
				if a.dAgents == nil {
					a.dAgents = map[string]bool{}
				}
				for _, name := range a.agentChoices() {
					on := a.dAgents[name]
					ui.Checkbox(c.Key("agent-"+name), &on, agentTitle(name))
					a.dAgents[name] = on
				}
				if len(a.agentChoices()) == 0 {
					ui.Text(c, "这台 Mac 上没找到 Claude、Codex、grok 或 Cursor").FontSize(fsBody).TextColor(t.TextMuted)
				}
			})
			if isGit {
				segmented(c, &a.dMode, "工作目录", "在工作区目录里", "各自新建 worktree")
				if a.dMode == 1 {
					ui.Column(c).Gap(6).Children(func() {
						fieldInput(c, &a.dBranch, "分支名")
						ui.Text(c, "选了多个 agent 时，分支名会加上 -claude 这样的后缀。").FontSize(fsSmall).TextColor(t.TextMuted)
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
				if pressable(textButton(c, "取消")).Clicked() {
					a.dispatching = false
				}
				verb := "派发"
				if len(agents) > 1 {
					verb = fmt.Sprintf("派发给 %d 个 agent", len(agents))
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
