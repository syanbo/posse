package mobile

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/projicon"
)

// Host 是常驻进程提供给手机的数据和操作。
type Host interface {
	State() State
	Terminal(id string) host.Terminal // 没有时为 nil
	// Restore 拉起上一个常驻进程留下、还没恢复的终端（Terminal.Lost），拉不起来或没有时为 nil
	Restore(id string) host.Terminal
	// View 记下手机开始看这个会话（终端页或会话页），算看过；返回的函数在不看时调用
	View(id string) func()
	// CreateTerminal 开一个终端：agent 非空时启动 agent（可带首条提示），否则运行 command，都为空是 shell
	CreateTerminal(workspaceID, agent, prompt, command string) (string, error)
	CloseTerminal(id string) error
	Agents() []string
	// History 调常驻进程的会话历史接口：history.list、history.search 原样返回；
	// history.resume 返回 ResumeResult。错误可以是 *rpc.Error，带上它的错误码
	History(ctx context.Context, method string, params json.RawMessage) (any, error)
	// Usage 是各 agent 的套餐用量，和 daemon.status 里给窗口的一样
	Usage() any
	// RefreshUsage 让各家马上取一次用量（同窗口的刷新），做完返回新的读数；Codex、Cursor 要联网，最长半分钟
	RefreshUsage(ctx context.Context) (any, error)
	// Desk 开始一次看桌面：采集起来、UDP 听上之后返回。之后一直运行到 ctx 结束或出错，
	// 期间经 notify 推状态，结束时恰好推一次 Type 为 end 的事件（ctx 结束也推）。
	// 错误用 DeskError 带上错误码。
	Desk(ctx context.Context, req DeskRequest, notify func(DeskEvent)) (DeskReady, error)
	// RecordGit 把手机做的 git 操作记进桌面 Git 日志的操作记录
	RecordGit(r GitRecord)
}

// ResumeResult 是在电脑上继续一个历史会话的结果：会话所在的终端和工作区（手机上的 ID）。
type ResumeResult struct {
	Terminal    string `json:"terminal"`
	Worktree    string `json:"worktree"`
	AlreadyOpen bool   `json:"alreadyOpen,omitempty"`
}

type State struct {
	Projects   []Project
	Workspaces []Workspace
}

type Project struct {
	ID, Name, Path string
	SingleRepo     bool // 关掉了"识别多个 Git 仓库"
}

type Workspace struct {
	ID, ProjectID, Name, Path, Kind, Branch string
	// Attached 是从别处加进来的 worktree（记成 folder，目录不是项目目录）；
	// Shown 是它显示的名字：当前分支，游离时为空。和桌面侧栏的 isWorktree、showBranches 一样
	Attached  bool
	Shown     string
	CreatedAt time.Time
	Terminals []Terminal // 按创建时间排序
}

type Terminal struct {
	ID, Title, Agent      string
	Status                string // agent 状态：starting | working | waiting | idle | exited，普通终端为空
	StatusAt              time.Time
	Message               string
	SessionID             string
	TranscriptPath        string
	ToolName              string
	ToolInput             string
	Prompt                string // interactivePrompt
	Running               bool
	CreatedAt, LastOutput time.Time
	// 下面三个照桌面侧栏：等它派出去的任务、本轮完成了还没看、上一个常驻进程留下还没恢复（Running 为假）
	Tasks, Unread, Lost bool
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

// worktreeID 是手机上的工作区 ID。手机按 "<repo>::" 前缀找所属项目，后半段只在缺名称时当标签用。
func worktreeID(w Workspace) string { return w.ProjectID + "::" + w.ID }

// workspaceOf 解析手机发来的工作区选择器（"id:<worktreeId>"）。
func workspaceOf(st State, sel string) (Workspace, bool) {
	sel = strings.TrimPrefix(sel, "id:")
	if _, after, ok := strings.Cut(sel, "::"); ok {
		sel = after
	}
	for _, w := range st.Workspaces {
		if w.ID == sel {
			return w, true
		}
	}
	return Workspace{}, false
}

func projectName(st State, id string) string {
	for _, p := range st.Projects {
		if p.ID == id {
			return p.Name
		}
	}
	return ""
}

// displayName 与 posse 侧栏一致：项目目录本身显示项目名，posse 建的 worktree 和同目录的工作区显示工作区名，
// 加进来的 worktree 显示当前分支（目录名常和仓库同名，看不出是哪个）。
func displayName(st State, w Workspace) string {
	switch {
	case w.Kind == "main":
		return projectName(st, w.ProjectID)
	case w.Shown != "":
		return w.Shown
	}
	return w.Name
}

// agentState 把 posse 的 agent 状态换成 Orca 协议里 state 的取值；没在运行的 agent 不显示。
func agentState(t Terminal) string {
	switch t.Status {
	case "starting", "working":
		return "working"
	case "waiting":
		return "waiting"
	case "idle":
		return "done"
	}
	return ""
}

// posseStatus 是桌面侧栏上的状态，手机照它画；state 只有 Orca 协议的三个取值，不够分，所以单独一个字段。
func posseStatus(t Terminal) string {
	switch {
	case t.Lost:
		return "lost"
	case !t.Running:
		return ""
	case t.Tasks:
		return "tasks"
	case t.Unread:
		return "unread"
	}
	switch t.Status {
	case "starting", "working", "waiting", "idle":
		return t.Status
	}
	return ""
}

// leafID 由终端 ID 派生一个固定的 UUID：手机解析标签 ID 时只认 UUID 形状的 leaf。
func leafID(termID string) string {
	h := sha256.Sum256([]byte("posse-leaf:" + termID))
	h[6] = h[6]&0x0f | 0x40
	h[8] = h[8]&0x3f | 0x80
	s := hex.EncodeToString(h[:16])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func parentTab(termID string) string { return "tab-" + termID }

func surfaceID(termID string) string { return parentTab(termID) + "::" + leafID(termID) }

func tabTitle(t Terminal) string {
	if t.Title != "" {
		return t.Title
	}
	if t.Agent != "" {
		return t.Agent
	}
	return "shell"
}

// agentRow 是 worktree.ps 和标签里的 agent 状态（Orca 的 AgentStatusEntry / RuntimeWorktreeAgentRow）。
func agentRow(t Terminal, now time.Time) map[string]any {
	st := agentState(t)
	// 手机把 30 分钟没更新的工作中、待处理当成空闲；状态还在就一直算刚更新
	updated := ms(t.StatusAt)
	if st == "working" || st == "waiting" {
		updated = now.UnixMilli()
	}
	row := map[string]any{
		"paneKey": parentTab(t.ID) + ":" + leafID(t.ID), "parentPaneKey": nil,
		"state": st, "agentType": t.Agent, "prompt": "", "taskTitle": nil, "displayName": nil,
		"lastAssistantMessage": nilIfEmpty(t.Message), "toolName": nilIfEmpty(t.ToolName), "toolInput": nilIfEmpty(t.ToolInput),
		"interactivePrompt": nilIfEmpty(t.Prompt),
		"interrupted":       false, "stateStartedAt": ms(t.StatusAt), "updatedAt": updated, "stateHistory": []any{},
	}
	if ps := posseStatus(t); ps != "" {
		row["posseStatus"] = ps
	}
	if t.SessionID != "" {
		ps := map[string]any{"key": "session_id", "id": t.SessionID}
		path := t.TranscriptPath
		if path == "" {
			path = history.TranscriptPath(t.Agent, t.SessionID, "")
		}
		if path != "" {
			ps["transcriptPath"] = path
		}
		row["providerSession"] = ps
	}
	return row
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func worktreeRow(st State, w Workspace, now time.Time) map[string]any {
	// liveTerminalCount 只数在跑的（Orca 协议的字段）；posseTerminalCount 连还没恢复的也算，
	// 手机据此决定拉不拉这个工作区的会话列表，全是未恢复的终端时也能列出来
	var agents []map[string]any
	live, waiting, working, done := 0, false, false, false
	var last time.Time
	for _, t := range w.Terminals {
		if t.Running {
			live++
		}
		if t.LastOutput.After(last) {
			last = t.LastOutput
		}
		s := ""
		if t.Running {
			s = agentState(t)
		}
		if s == "" {
			continue
		}
		agents = append(agents, agentRow(t, now))
		waiting = waiting || s == "waiting"
		working = working || s == "working"
		done = done || s == "done"
	}
	status := "inactive"
	switch {
	case waiting:
		status = "permission"
	case working:
		status = "working"
	case done:
		status = "done"
	case live > 0:
		status = "active"
	}
	if agents == nil {
		agents = []map[string]any{}
	}
	if last.IsZero() {
		last = w.CreatedAt
	}
	kind := "folder-workspace"
	if w.Kind != "folder" && w.Branch != "" {
		kind = "git"
	}
	return map[string]any{
		"worktreeId": worktreeID(w), "repoId": w.ProjectID, "repo": projectName(st, w.ProjectID),
		"path": w.Path, "branch": w.Branch, "displayName": displayName(st, w),
		"liveTerminalCount": live, "posseTerminalCount": len(w.Terminals), "hasAttachedPty": live > 0, "unread": false, "isPinned": false,
		"linkedPR": nil, "linkedIssue": nil, "linkedLinearIssue": nil, "linkedGitLabMR": nil, "linkedGitLabIssue": nil,
		"status": status, "agents": agents, "isActive": false, "workspaceKind": kind,
		"isMainWorktree": w.Kind == "main", "isWorktree": w.Kind == "worktree" || w.Attached, "isArchived": false, "hasHostSidebarActivity": live > 0,
		"parentWorktreeId": nil, "childWorktreeIds": []string{}, "workspaceStatus": "in-progress",
		"sortOrder": 0, "lastActivityAt": ms(last), "createdAt": ms(w.CreatedAt), "lastOutputAt": ms(last),
		"comment": "", "preview": "",
	}
}

// tabState 记录一个工作区标签快照的版本：内容变了版本加一，手机按版本丢弃过时的快照。
type tabState struct {
	mu      sync.Mutex
	last    []byte
	version int
	active  string // 手机最后选中的终端
}

func (s *Server) tabState(wid string) *tabState {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := s.tabs[wid]
	if ts == nil {
		ts = &tabState{}
		s.tabs[wid] = ts
	}
	return ts
}

// sessionTabs 生成 session.tabs.list 的结果；每个终端是一个标签。
func (s *Server) sessionTabs(st State, w Workspace) map[string]any {
	ts := s.tabState(w.ID)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	now := time.Now()
	tabs := []map[string]any{}
	var order []string
	active := ""
	for _, t := range w.Terminals {
		if t.ID == ts.active {
			active = t.ID
		}
	}
	if active == "" && len(w.Terminals) > 0 {
		active = w.Terminals[len(w.Terminals)-1].ID
	}
	for _, t := range w.Terminals {
		tab := map[string]any{
			"type": "terminal", "id": surfaceID(t.ID), "title": tabTitle(t),
			"parentTabId": parentTab(t.ID), "leafId": leafID(t.ID), "status": "ready",
			"terminal": t.ID, "isActive": t.ID == active,
		}
		if ps := posseStatus(t); ps != "" {
			tab["posseStatus"] = ps
		}
		if t.Agent != "" {
			tab["launchAgent"] = t.Agent
			if agentState(t) != "" && t.Running {
				tab["agentStatus"] = agentRow(t, now)
			}
		}
		tabs = append(tabs, tab)
		order = append(order, parentTab(t.ID))
	}
	if order == nil {
		order = []string{}
	}
	res := map[string]any{
		"worktree": worktreeID(w), "publicationEpoch": s.epoch,
		"activeGroupId": "group-1", "activeTabType": "terminal",
		"tabGroups": []map[string]any{{"id": "group-1", "activeTabId": nilIfEmpty(parentTabOr(active)), "tabOrder": order, "recentTabIds": order}},
		"tabs":      tabs,
	}
	if active != "" {
		res["activeTabId"] = surfaceID(active)
	} else {
		res["activeTabId"] = nil
	}
	// 版本只看内容，不看 updatedAt 这类随时间变的字段
	key, _ := json.Marshal([]any{active, order, tabsKey(w)})
	if !bytes.Equal(key, ts.last) {
		ts.last, ts.version = key, ts.version+1
	}
	res["snapshotVersion"] = ts.version
	return res
}

func parentTabOr(id string) string {
	if id == "" {
		return ""
	}
	return parentTab(id)
}

func tabsKey(w Workspace) []string {
	var out []string
	for _, t := range w.Terminals {
		state := ""
		if t.Running {
			state = agentState(t)
		}
		out = append(out, t.ID+"\x00"+tabTitle(t)+"\x00"+state+"\x00"+posseStatus(t)+"\x00"+t.SessionID+"\x00"+t.ToolName+"\x00"+t.Prompt)
	}
	return out
}

func (s *Server) setActive(wid, termID string) {
	ts := s.tabState(wid)
	ts.mu.Lock()
	ts.active = termID
	ts.mu.Unlock()
}

func (s *Server) status() map[string]any {
	name, _ := os.Hostname()
	name = strings.TrimSuffix(name, ".local")
	return map[string]any{
		"runtimeId": s.epoch, "protocolVersion": 3, "minCompatibleMobileVersion": 2,
		"runtimeProtocolVersion": 3, "minCompatibleRuntimeClientVersion": 2,
		"appVersion": "posse", "capabilities": []string{}, "hostPlatform": "darwin",
		"machineName": "Posse " + name, "graphStatus": "ready", "floatingWorkspaceEnabled": false,
	}
}

type wtParams struct {
	Worktree string `json:"worktree"`
}

type termParams struct {
	Terminal string `json:"terminal"`
}

// dispatch 处理一个请求。流式方法在自己的 goroutine 里继续发送，这里立刻返回。
func (c *conn) dispatch(req request) error {
	s := c.srv
	p := req.Params
	if len(p) == 0 || string(p) == "null" {
		p = json.RawMessage("{}")
	}
	switch req.Method {
	case "runtime.clientCapabilities.update":
		var body struct {
			Client       string   `json:"client"`
			Version      int      `json:"version"`
			Capabilities []string `json:"capabilities"`
			// Posse 扩展：手机界面的语言（BCP 47，如 zh-Hans、en），报错和说明按它翻译
			Locale string `json:"locale"`
			// Posse 扩展：这台手机的安装 ID、型号和它存着的这台电脑的全部 token，见 pairing.identify
			Phone *struct {
				ID     string   `json:"id"`
				Name   string   `json:"name"`
				Tokens []string `json:"tokens"`
			} `json:"phone"`
		}
		if json.Unmarshal(p, &body) != nil {
			return c.fail(req.ID, "bad_params", "client capabilities")
		}
		// 每次声明整份替换。后面的扩展推送只发给带上对应能力的连接。
		c.setClientCaps(body.Client, body.Version, body.Capabilities, body.Locale)
		if ph := body.Phone; ph != nil && ph.ID != "" {
			name := strings.TrimSpace(ph.Name)
			if len([]rune(name)) > 64 {
				name = string([]rune(name)[:64])
			}
			removed := s.pair.identify(c.device.ID, ph.ID, name, ph.Tokens)
			s.mu.Lock()
			c.device.Phone = ph.ID
			s.mu.Unlock()
			s.dropDevices(removed)
		}
		return c.reply(req.ID, map[string]any{})
	case "worktree.activate", "session.tabs.activate", "terminal.focus",
		"terminal.setDisplayMode":
		if req.Method == "session.tabs.activate" {
			var a struct {
				wtParams
				TabID string `json:"tabId"`
			}
			json.Unmarshal(p, &a)
			if w, ok := workspaceOf(s.host.State(), a.Worktree); ok {
				id, _, _ := strings.Cut(strings.TrimPrefix(a.TabID, "tab-"), "::")
				s.setActive(w.ID, id)
			}
		}
		return c.reply(req.ID, map[string]any{})
	case "status.get":
		return c.reply(req.ID, s.status())
	case "posse.push.register":
		return c.registerPush(req.ID, p)
	case "posse.push.presence":
		var a struct {
			Foreground bool `json:"foreground"`
		}
		json.Unmarshal(p, &a)
		c.background.Store(!a.Foreground)
		return c.reply(req.ID, map[string]any{})
	case "posse.punch":
		var a struct {
			Addr string `json:"addr"`
		}
		json.Unmarshal(p, &a)
		// 探测要等中转回一趟，不占住这条连接的请求队列
		go func() {
			info, err := s.punch(c.device.ID, a.Addr)
			c.replyOrFail(req.ID, info, err)
		}()
		return nil
	case "posse.quic":
		info, err := s.quicInfo(c.device.ID)
		c.replyOrFail(req.ID, info, err)
		return nil
	case "posse.projectIcon":
		// Posse 扩展：项目图标的图片（PNG），文件名每次导入都不同，手机按名字缓存
		var a struct {
			Name string `json:"name"`
		}
		json.Unmarshal(p, &a)
		data, err := projicon.ReadImage(s.home, a.Name)
		if err != nil {
			return c.fail(req.ID, "not_found", "icon not found")
		}
		return c.reply(req.ID, map[string]any{"data": base64.StdEncoding.EncodeToString(data)})
	case "runtime.ping":
		// Posse 扩展：手机按往返时间显示延迟，连着几次不回就当连接死了重连。不做任何事，回得越快越准
		return c.reply(req.ID, map[string]any{})
	case "repo.list":
		icons := s.icons.Load(s.home)
		repos := []map[string]any{}
		for _, pr := range s.host.State().Projects {
			repo := map[string]any{"id": pr.ID, "displayName": pr.Name, "path": pr.Path}
			if icon, ok := icons[pr.ID]; ok {
				repo["icon"] = icon
			}
			repos = append(repos, repo)
		}
		return c.reply(req.ID, map[string]any{"repos": repos})
	case "worktree.ps":
		st := s.host.State()
		now := time.Now()
		// Posse 扩展：桌面侧栏给项目设的图标（repoIcon，没设不给），图片另经 posse.projectIcon 按文件名取
		icons := s.icons.Load(s.home)
		rows := []map[string]any{}
		for _, w := range st.Workspaces {
			row := worktreeRow(st, w, now)
			if icon, ok := icons[w.ProjectID]; ok {
				row["repoIcon"] = icon
			}
			rows = append(rows, row)
		}
		return c.reply(req.ID, map[string]any{"worktrees": rows, "totalCount": len(rows), "truncated": false})
	case "ui.get":
		return c.reply(req.ID, map[string]any{"ui": map[string]any{
			"groupBy": "repo", "sortBy": "recent", "hideSleepingWorkspaces": false, "hideDefaultBranchWorkspace": false,
			"filterRepoIds": []string{}, "collapsedGroups": []string{},
		}})
	case "ui.set":
		return c.reply(req.ID, map[string]any{"ok": true})
	case "settings.get":
		def := "claude"
		if a := s.host.Agents(); len(a) > 0 {
			def = a[0]
		}
		return c.reply(req.ID, map[string]any{"settings": map[string]any{
			"defaultTuiAgent": def, "disabledTuiAgents": []string{}, "agentCmdOverrides": map[string]any{},
		}})
	case "settings.getTerminalQuickCommands":
		return c.reply(req.ID, map[string]any{"terminalQuickCommands": []any{}})
	case "preflight.detectAgents":
		return c.reply(req.ID, s.host.Agents())
	case "session.tabs.list":
		var a wtParams
		json.Unmarshal(p, &a)
		st := s.host.State()
		w, ok := workspaceOf(st, a.Worktree)
		if !ok {
			return errorf("not_found", "workspace %s not found", a.Worktree)
		}
		return c.reply(req.ID, s.sessionTabs(st, w))
	case "session.tabs.subscribe":
		var a wtParams
		json.Unmarshal(p, &a)
		st := s.host.State()
		w, ok := workspaceOf(st, a.Worktree)
		if !ok {
			return errorf("not_found", "workspace %s not found", a.Worktree)
		}
		go c.tabsStream(c.startStream(req.ID), req.ID, w.ID)
		return nil
	case "session.tabs.unsubscribe", "runtime.clientEvents.unsubscribe", "notifications.unsubscribe":
		var a struct {
			SubscriptionID string `json:"subscriptionId"`
		}
		json.Unmarshal(p, &a)
		c.stopStream(a.SubscriptionID)
		return c.reply(req.ID, map[string]any{"unsubscribed": true})
	case "runtime.clientEvents.subscribe":
		go c.eventsStream(c.startStream(req.ID), req.ID)
		return nil
	case "notifications.subscribe":
		// 只为让手机知道连接支持通知流；posse 不经推送网关发通知
		c.startStream(req.ID)
		return c.stream(req.ID, map[string]any{"type": "ready", "subscriptionId": req.ID, "epoch": s.epoch})
	case "notifications.getMissedSince":
		return c.reply(req.ID, map[string]any{"notifications": []any{}, "epoch": s.epoch, "dismissedPushes": []any{}})
	case "terminal.list":
		var a wtParams
		json.Unmarshal(p, &a)
		st := s.host.State()
		out := []map[string]any{}
		for _, w := range st.Workspaces {
			if a.Worktree != "" {
				if sel, ok := workspaceOf(st, a.Worktree); !ok || sel.ID != w.ID {
					continue
				}
			}
			for _, t := range w.Terminals {
				out = append(out, map[string]any{
					"handle": t.ID, "ptyId": t.ID, "worktreeId": worktreeID(w), "worktreePath": w.Path, "branch": w.Branch,
					"tabId": parentTab(t.ID), "leafId": leafID(t.ID), "title": tabTitle(t), "connected": t.Running,
					"writable": t.Running, "lastOutputAt": ms(t.LastOutput), "preview": "", "agentIdentity": nilIfEmpty(t.Agent),
				})
			}
		}
		return c.reply(req.ID, map[string]any{"terminals": out, "totalCount": len(out), "truncated": false})
	case "terminal.subscribe":
		return c.subscribeTerminal(req.ID, p)
	case "desk.subscribe":
		return c.deskSubscribe(req.ID, p)
	case "desk.unsubscribe":
		return c.deskUnsubscribe(req.ID)
	case "desk.punch":
		return c.deskPunchRPC(req.ID, p)
	case "desk.display":
		return c.deskDisplayRPC(req.ID, p)
	case "desk.windows":
		return c.deskWindowsRPC(req.ID)
	case "desk.window":
		return c.deskWindowRPC(req.ID, p)
	case "terminal.unsubscribe":
		var a struct {
			SubscriptionID string `json:"subscriptionId"`
			RequestID      string `json:"requestId"`
		}
		json.Unmarshal(p, &a)
		if a.RequestID != "" {
			c.stopStream(a.RequestID)
		}
		return c.reply(req.ID, map[string]any{"unsubscribed": true})
	case "terminal.send":
		return c.sendInput(req.ID, p)
	case "nativeChat.readSession":
		return c.readNativeChat(req.ID, p)
	case "nativeChat.subscribe":
		return c.subscribeNativeChat(req.ID, p)
	case "nativeChat.unsubscribe":
		return c.unsubscribeNativeChat(req.ID, p)
	case "agent.permission.decide":
		return c.decidePermission(req.ID, p)
	// 下面几个要读盘、调 git 或等索引，放到自己的 goroutine 里：同一连接的请求排队处理，
	// 不能让按键和 ping 等它们。都是各回各的，回复的先后不要紧
	case "discussion.list", "discussion.show", "discussion.search", "discussion.create", "discussion.update", "discussion.archive", "discussion.post", "discussion.answer", "discussion.key", "discussion.cancel", "discussion.stop", "discussion.retry", "discussion.confirm", "discussion.isolate", "discussion.member.add", "discussion.member.update", "discussion.member.remove", "discussion.member.restart", "discussion.member.resume", "discussion.share", "discussion.invite", "discussion.decide", "discussion.person.remove", "discussion.join", "discussion.leave", "discussion.upload.begin", "discussion.upload.chunk", "discussion.upload.commit", "discussion.upload.abort", "discussion.attachment.read", "discussion.attachment.thumb", "discussion.report.read", "discussion.profile.get", "discussion.profile.set", "discussion.profile.avatar":
		return c.async(req.ID, func() error { return c.discussion(req.ID, req.Method, p) })
	case "history.list", "history.search", "history.resume":
		return c.async(req.ID, func() error { return c.history(req.ID, req.Method, p) })
	case "usage.get":
		// Posse 扩展：声明了 posse.usage.v1 的连接才有，手机首页的用量卡片用
		if !c.hasCapability("posse.usage.v1") {
			return errorf("method_not_found", "Unknown method: %s", req.Method)
		}
		return c.reply(req.ID, c.srv.host.Usage())
	case "usage.refresh":
		if !c.hasCapability("posse.usage.v1") {
			return errorf("method_not_found", "Unknown method: %s", req.Method)
		}
		// 要等联网，放到一边做：同一连接的请求排队处理，不能挡住后面的
		go func() {
			ctx, cancel := context.WithTimeout(c.ctx, 45*time.Second)
			defer cancel()
			res, err := c.srv.host.RefreshUsage(ctx)
			c.replyOrFail(req.ID, res, err)
		}()
		return nil
	case "session.context":
		return c.async(req.ID, func() error { return c.sessionContext(req.ID, p) })
	case "git.status":
		return c.async(req.ID, func() error { return c.gitStatus(req.ID, p) })
	case "git.diff":
		return c.async(req.ID, func() error { return c.gitDiff(req.ID, p) })
	case "git.branches":
		return c.async(req.ID, func() error { return c.gitBranches(req.ID, p) })
	case "git.checkout":
		return c.async(req.ID, func() error { return c.gitCheckout(req.ID, p) })
	case "terminal.close", "session.tabs.close":
		var a struct {
			termParams
			TabID string `json:"tabId"`
		}
		json.Unmarshal(p, &a)
		id := a.Terminal
		if id == "" {
			id, _, _ = strings.Cut(strings.TrimPrefix(a.TabID, "tab-"), "::")
		}
		if err := s.host.CloseTerminal(id); err != nil {
			return errorf("not_found", "%v", err)
		}
		return c.reply(req.ID, map[string]any{"closed": true})
	case "session.tabs.createTerminal":
		return c.createTerminal(req.ID, p)
	case "clipboard.startImageUpload", "clipboard.appendImageUploadChunk", "clipboard.commitImageUpload",
		"clipboard.abortImageUpload", "clipboard.saveImageAsTempFile":
		return c.image(req.ID, req.Method, p)
	}
	if c.mediaMethod(req, p) {
		return nil
	}
	if c.toolsMethod(req, p) {
		return nil
	}
	if c.fileMethod(req, p) {
		return nil
	}
	return errorf("method_not_found", "Unknown method: %s", req.Method)
}

// tabsStream 先发快照，之后每秒检查一次，标签有变化就发完整的新快照。
func (c *conn) tabsStream(ctx contextDone, id, wid string) {
	s := c.srv
	send := func(typ string) (any, bool) {
		st := s.host.State()
		w, ok := workspaceOf(st, wid)
		if !ok {
			return nil, false
		}
		res := s.sessionTabs(st, w)
		res["type"] = typ
		return res["snapshotVersion"], c.stream(id, res) == nil
	}
	last, ok := send("snapshot")
	if !ok {
		c.stream(id, map[string]any{"type": "end"})
		c.endStream(id)
		return
	}
	// 手机收到 snapshot 之后再收到一次 updated 才把流标成正常
	if _, ok := send("updated"); !ok {
		return
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		st := s.host.State()
		w, ok := workspaceOf(st, wid)
		if !ok {
			c.stream(id, map[string]any{"type": "end"})
			c.endStream(id)
			return
		}
		// 只在版本变了（标签、标题或 agent 状态变了）时发，updatedAt 之类的时间字段不算
		res := s.sessionTabs(st, w)
		if res["snapshotVersion"] == last {
			continue
		}
		last = res["snapshotVersion"]
		res["type"] = "updated"
		if c.stream(id, res) != nil {
			return
		}
	}
}

// eventsStream 在工作区列表变化时通知手机立刻刷新，不用等它 3 秒一次的轮询。
func (c *conn) eventsStream(ctx contextDone, id string) {
	if c.stream(id, map[string]any{"type": "ready", "subscriptionId": id}) != nil {
		return
	}
	key := func() string {
		st := c.srv.host.State()
		var b strings.Builder
		for _, w := range st.Workspaces {
			b.WriteString(w.ID)
			for _, t := range w.Terminals {
				st := ""
				if t.Running {
					st = agentState(t)
				}
				b.WriteString("|" + t.ID + ":" + st + ":" + tabTitle(t) + ":" + t.SessionID + ":" + t.Prompt)
			}
			b.WriteByte('\n')
		}
		return b.String()
	}
	last := key()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if k := key(); k != last {
			last = k
			if c.stream(id, map[string]any{"type": "worktreesChanged"}) != nil {
				return
			}
		}
	}
}

type contextDone interface{ Done() <-chan struct{} }

func (c *conn) sendInput(id string, p json.RawMessage) error {
	var a struct {
		termParams
		Text string `json:"text"`
		Raw  string `json:"raw"` // 终端按键的原始字节，标准 base64。有它就按字节写
		// Posse 扩展：一串按键，每个是标准 base64，一个一个写、中间隔 [keyGap]。有它就不看 raw。
		// Cursor Agent 把一次读到的几个按键当成一段输入，Ctrl+U、退格原样插进输入框，只能分开送
		RawKeys   []string `json:"rawKeys"`
		Enter     bool     `json:"enter"`
		Interrupt bool     `json:"interrupt"`
		InputKind string   `json:"inputKind"`
	}
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	t := c.srv.host.Terminal(a.Terminal)
	if t == nil {
		return errorf("not_found", "terminal %s not found", a.Terminal)
	}
	// text 是 Unicode，按 UTF-8 写入。终端模拟器的按键是原始字节，若先按 Latin-1
	// 塞进 text，这里再编成 UTF-8，0x80–0x9F 会变成 C1 控制符，中文就被拆碎。
	var payload []byte
	var keys [][]byte
	for _, k := range a.RawKeys {
		b, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			return errorf("invalid_params", "bad rawKeys")
		}
		keys = append(keys, b)
	}
	switch {
	case len(keys) > 0:
	case a.Raw != "":
		b, err := base64.StdEncoding.DecodeString(a.Raw)
		if err != nil {
			return errorf("invalid_params", "bad raw")
		}
		payload = b
	default:
		payload = pasteText(a.Text, t.BracketedPaste())
	}
	n := 0
	accepted, outcome := true, "accepted"
	write := func(p []byte) bool {
		if err := t.Send(p); err != nil {
			accepted, outcome = false, "refused"
			return false
		}
		n += len(p)
		return true
	}
	// 终端查询由常驻进程里的内核应答，手机 xterm 的应答丢掉，免得程序收到两份
	if a.InputKind != "query-reply" {
		for i, k := range keys {
			if i > 0 {
				time.Sleep(keyGap)
			}
			if !write(k) {
				break
			}
		}
		if len(payload) > 0 {
			write(payload)
		}
		if accepted && a.Enter {
			// TUI 会把与文本同批到达的回车当成粘贴内容，分开发送才算提交
			if len(payload) > 0 || len(keys) > 0 {
				time.Sleep(120 * time.Millisecond)
			}
			write([]byte("\r"))
		}
		if accepted && a.Interrupt {
			write([]byte{3})
		}
	}
	return c.reply(id, map[string]any{"send": map[string]any{
		"accepted": accepted, "handle": a.Terminal, "bytesWritten": n,
		"writeSettlement": map[string]any{"outcome": outcome},
	}})
}

// keyGap 是 rawKeys 里相邻两个按键的间隔：Cursor Agent 实测隔 15ms 左右就分得开，紧挨着写会被并成一次读。
const keyGap = 20 * time.Millisecond

// pasteText 是输入框发来的一段文字。带换行时按一次粘贴送进去：直接写的话，Claude Code 这类程序
// 把第一个换行当成回车，前半段先提交了，后半段留在输入框。文字里的 ESC[201~ 去掉，免得提前结束粘贴、
// 后面的内容被当成按键执行。程序没开括号粘贴（不认它）时原样写。
func pasteText(text string, bracketed bool) []byte {
	if !bracketed || !strings.ContainsAny(text, "\r\n") {
		return []byte(text)
	}
	text = strings.ReplaceAll(text, "\x1b[201~", "")
	return []byte("\x1b[200~" + text + "\x1b[201~")
}

func (c *conn) createTerminal(id string, p json.RawMessage) error {
	var a struct {
		wtParams
		Agent       string `json:"agent"`
		LaunchAgent string `json:"launchAgent"`
		AgentPrompt string `json:"agentPrompt"`
		Command     string `json:"command"`
	}
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	s := c.srv
	w, ok := workspaceOf(s.host.State(), a.Worktree)
	if !ok {
		return errorf("not_found", "workspace %s not found", a.Worktree)
	}
	agent := a.Agent
	if agent == "" {
		agent = a.LaunchAgent
	}
	tid, err := s.host.CreateTerminal(w.ID, agent, a.AgentPrompt, a.Command)
	if err != nil {
		return errorf("create_failed", "%v", err)
	}
	s.setActive(w.ID, tid)
	title := agent
	if title == "" {
		title = "shell"
	}
	st := s.host.State()
	w, _ = workspaceOf(st, w.ID)
	res := s.sessionTabs(st, w)
	return c.reply(id, map[string]any{
		"tab": map[string]any{"type": "terminal", "id": surfaceID(tid), "terminal": tid, "title": title,
			"status": "ready", "isActive": true, "parentTabId": parentTab(tid), "leafId": leafID(tid)},
		"publicationEpoch": s.epoch, "snapshotVersion": res["snapshotVersion"],
	})
}

// upload 是手机分块上传中的一张图片。
type upload struct {
	data   []byte
	expect int
	// raw 的上传（posse.bulk.v1）：原始字节由编号为 blob 的二进制帧送来，边收边写进 file（视频动辄几百 MB，
	// 不攒在常驻进程的内存里）；否则 data 是 base64 文本
	raw  bool
	blob uint32
	file *os.File
	got  int
	name string // 手机上的文件名，存的时候用它，agent 看得到原名
	bad  bool   // 送来的超过了说好的大小，或者写盘失败
}

// discard 丢掉没传完的上传留下的临时文件。
func (u *upload) discard() {
	if u.file != nil {
		u.file.Close()
		os.Remove(u.file.Name())
		u.file = nil
	}
}

// uploadChunk 收下手机经二进制帧发来的一段上传。在读循环里调用，和 append、commit 同一个协程。
func (c *conn) uploadChunk(blob uint32, p []byte) {
	c.smu.Lock()
	defer c.smu.Unlock()
	for _, u := range c.uploads {
		if u.raw && u.blob == blob {
			if u.bad || u.got+len(p) > u.expect {
				u.bad = true
				return
			}
			if _, err := u.file.Write(p); err != nil {
				u.bad = true
				return
			}
			u.got += len(p)
			return
		}
	}
}

func (c *conn) dropUploads() {
	c.smu.Lock()
	for _, u := range c.uploads {
		u.discard()
	}
	c.uploads = map[string]*upload{}
	c.smu.Unlock()
}

// saveImage 把图片存成临时文件，返回路径；手机随后把路径输入终端，和在电脑上粘贴图片一样。
func saveImage(b64data string) (string, error) {
	if i := strings.Index(b64data, ";base64,"); i >= 0 && strings.HasPrefix(b64data, "data:") {
		b64data = b64data[i+len(";base64,"):]
	}
	data, err := base64.StdEncoding.DecodeString(b64data)
	if err != nil {
		return "", errorf("invalid_params", "bad image data")
	}
	return saveImageBytes(data)
}

func saveImageBytes(data []byte) (string, error) {
	f, err := os.CreateTemp("", "posse-mobile-*"+sniffExt(data))
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	return filepath.Clean(f.Name()), nil
}

// sniffExt 按开头的字节认出图片和视频的扩展名，认不出当 png（剪贴板里的图片）。
func sniffExt(data []byte) string {
	ext := ".png"
	switch {
	case bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff}):
		ext = ".jpg"
	case bytes.HasPrefix(data, []byte("GIF8")):
		ext = ".gif"
	case len(data) > 12 && string(data[8:12]) == "WEBP":
		ext = ".webp"
	case len(data) > 12 && string(data[4:12]) == "ftypheic":
		ext = ".heic"
	case len(data) > 12 && string(data[4:12]) == "ftypqt  ":
		ext = ".mov"
	case len(data) > 12 && string(data[4:8]) == "ftyp":
		ext = ".mp4"
	}
	return ext
}

// saveUpload 把收完的 raw 上传落成最终的文件：带了文件名的放进一个新的临时目录、用原名，
// 没带的（剪贴板里的图片）按内容认扩展名。
func saveUpload(u *upload) (string, error) {
	tmp := u.file.Name()
	if err := u.file.Close(); err != nil {
		os.Remove(tmp)
		return "", err
	}
	u.file = nil
	var dest string
	if name := uploadName(u.name); name != "" {
		dir, err := os.MkdirTemp("", "posse-mobile-*")
		if err != nil {
			os.Remove(tmp)
			return "", err
		}
		dest = filepath.Join(dir, name)
	} else {
		head := make([]byte, 16)
		if f, err := os.Open(tmp); err == nil {
			n, _ := io.ReadFull(f, head)
			head = head[:n]
			f.Close()
		}
		dest = tmp + sniffExt(head)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return filepath.Clean(dest), nil
}

// uploadName 只留文件名本身：去掉目录、控制字符，. 和 .. 不要。
func uploadName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' || r == '\\' {
			return -1
		}
		return r
	}, filepath.Base(strings.ReplaceAll(name, "\\", "/")))
	name = strings.TrimSpace(name)
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	// 太长的留后面一截（扩展名在后面），按字符截，不切坏 UTF-8
	if r := []rune(name); len(r) > 120 {
		name = string(r[len(r)-120:])
	}
	return name
}

// base64 上传整段攒在内存里，只给图片用；raw 上传边收边写盘，视频、文件也走它
const (
	maxImage  = 32 << 20
	maxUpload = 1 << 30
)

func (c *conn) image(id, method string, p json.RawMessage) error {
	var a struct {
		UploadID          string `json:"uploadId"`
		ExpectedBase64Len int    `json:"expectedBase64Length"`
		ExpectedBytes     int    `json:"expectedBytes"` // 有它就是 raw 上传，分段走二进制帧
		Offset            int    `json:"offset"`
		ContentBase64     string `json:"contentBase64"`
		Name              string `json:"name"` // 手机上的文件名，raw 上传才用
	}
	if err := json.Unmarshal(p, &a); err != nil {
		return errorf("invalid_params", "%v", err)
	}
	switch method {
	case "clipboard.saveImageAsTempFile":
		path, err := saveImage(a.ContentBase64)
		if err != nil {
			return err
		}
		return c.reply(id, path)
	case "clipboard.startImageUpload":
		if a.ExpectedBytes > 0 && c.hasCapability("posse.bulk.v1") {
			if a.ExpectedBytes > maxUpload {
				return errorf("invalid_params", "bad upload size")
			}
			f, err := os.CreateTemp("", "posse-mobile-*.part")
			if err != nil {
				return err
			}
			uid := "u-" + randHex(8)
			bid := c.nextBlob.Add(1)
			c.smu.Lock()
			c.uploads[uid] = &upload{expect: a.ExpectedBytes, raw: true, blob: bid, file: f, name: a.Name}
			c.smu.Unlock()
			return c.reply(id, map[string]any{"uploadId": uid, "blob": bid})
		}
		if a.ExpectedBase64Len <= 0 || a.ExpectedBase64Len > maxImage {
			return errorf("invalid_params", "bad upload size")
		}
		uid := "u-" + randHex(8)
		c.smu.Lock()
		c.uploads[uid] = &upload{expect: a.ExpectedBase64Len}
		c.smu.Unlock()
		return c.reply(id, map[string]any{"uploadId": uid})
	}
	c.smu.Lock()
	u := c.uploads[a.UploadID]
	if method != "clipboard.appendImageUploadChunk" {
		delete(c.uploads, a.UploadID)
	}
	c.smu.Unlock()
	if u == nil {
		return errorf("not_found", "upload %s not found", a.UploadID)
	}
	if method != "clipboard.appendImageUploadChunk" && method != "clipboard.commitImageUpload" {
		u.discard()
	}
	switch method {
	case "clipboard.appendImageUploadChunk":
		if u.raw {
			return errorf("invalid_params", "raw uploads send their chunks as binary frames")
		}
		if a.Offset != len(u.data) || len(u.data)+len(a.ContentBase64) > u.expect {
			return errorf("invalid_params", "chunk offset %d does not match %d", a.Offset, len(u.data))
		}
		u.data = append(u.data, a.ContentBase64...)
		return c.reply(id, map[string]any{"received": len(u.data)})
	case "clipboard.commitImageUpload":
		if u.raw {
			if u.bad || u.got != u.expect {
				u.discard()
				return errorf("invalid_params", "got %d of %d bytes", u.got, u.expect)
			}
			path, err := saveUpload(u)
			if err != nil {
				return err
			}
			return c.reply(id, path)
		}
		path, err := saveImage(string(u.data))
		if err != nil {
			return err
		}
		return c.reply(id, path)
	}
	return c.reply(id, map[string]any{"aborted": true})
}
