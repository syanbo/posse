package daemon

import (
	"cmp"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"

	"posse/internal/agent"
	"posse/internal/desk"
	"posse/internal/gitx"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/mobile"
	"posse/internal/rpc"
	"posse/internal/store"
	"posse/internal/usage"
)

// mobileHost 把常驻进程的项目、工作区和终端交给手机服务。
type mobileHost struct{ d *Daemon }

func (h mobileHost) HostMeta() mobile.HostMeta {
	return buildMobileHostMeta(runtime.GOOS, runtime.GOARCH, Edition, h.d.runtime != "", Version, Commit)
}

func buildMobileHostMeta(goos, goarch, edition string, managed bool, version, commit string) mobile.HostMeta {
	isManaged := edition == "headless" && managed
	features := rpc.RemoteFeatures(goos, edition)
	caps := make([]string, 0, 4)
	if slices.Contains(features, "desk.v1") {
		caps = append(caps, "desk")
	}
	if slices.Contains(features, "memory.v1") {
		caps = append(caps, "memory")
	}
	if slices.Contains(features, "autostart.v1") {
		caps = append(caps, "autostart")
	}
	if isManaged && slices.Contains(features, "autostart.write.v1") {
		caps = append(caps, "autostartWrite")
	}
	return mobile.HostMeta{
		Platform:     goos,
		Arch:         goarch,
		Edition:      edition,
		Managed:      isManaged,
		Version:      version,
		Commit:       commit,
		Capabilities: caps,
	}
}

// startMobile 打开手机服务；已经配对过手机时立刻监听。端口可用 POSSE_MOBILE_PORT 改（端到端测试用，免得和日常的冲突），
// 开发包不设时用 devMobilePort。
// newMobile 读手机配置，还不监听：交接过来时端口还在旧进程手里。
func (d *Daemon) newMobile() {
	port := mobile.DefaultPort
	if runningDevBundle() {
		port = devMobilePort
	}
	if v, err := strconv.Atoi(os.Getenv("POSSE_MOBILE_PORT")); err == nil {
		port = v
	}
	m, err := mobile.New(d.cfg.Home, mobileHost{d}, port)
	if err != nil {
		log.Printf("mobile: %v", err)
		return
	}
	m.SetQUIC(mobile.QUICOptions{Relay: d.deskRelay, ListenOut: desk.ListenPhysical, Tailscale: daemonTSProvider{d}})
	d.mobile = m
}

func (d *Daemon) startMobile() {
	d.startTailscale()
	if d.mobile == nil {
		return
	}
	if err := d.mobile.Start(); err != nil {
		log.Printf("%v", err)
	}
}

func (h mobileHost) State() mobile.State {
	d := h.d
	var st mobile.State
	ps, err := d.store.Projects()
	if err != nil {
		return st
	}
	projectPath := map[string]string{}
	for _, p := range ps {
		st.Projects = append(st.Projects, mobile.Project{ID: p.ID, Name: p.Name, Path: p.Path, SingleRepo: p.SingleRepo})
		projectPath[p.ID] = filepath.Clean(p.Path)
	}
	ws, err := d.store.Workspaces("")
	if err != nil {
		return st
	}
	byWS := d.sessionsByWorkspace()
	lost := d.lostByWorkspace()
	var termIDs []string
	for _, ss := range byWS {
		for _, s := range ss {
			if s.Running() {
				termIDs = append(termIDs, s.ID)
			}
		}
	}
	for _, ts := range lost {
		for _, t := range ts {
			termIDs = append(termIDs, t.ID)
		}
	}
	workerTitles, _ := d.store.WorkerTitles(termIDs)
	// 和桌面侧栏一样：有进行中的任务、又阻塞在 posse task wait 上或停着，是在等派出去的任务
	coordinating := map[string]bool{}
	if ts, err := d.store.Tasks(store.TaskFilter{Running: true}); err == nil {
		for _, t := range ts {
			if t.Coordinator != "" {
				coordinating[t.Coordinator] = true
			}
		}
	}
	for _, w := range ws {
		// 数据库里的分支只在建工作区时记一次，main 工作区切分支后就过时了；和界面一样以 HEAD 为准
		// 多仓库的 worktree 工作区根目录不是仓库（HeadRef 会往上找到别的仓库），分支用记下的那个
		var head string
		var detached bool
		if len(w.Repos) == 0 {
			head, detached = gitx.HeadRef(w.Path)
		}
		if head != "" {
			w.Branch = head
		}
		mw := mobile.Workspace{ID: w.ID, ProjectID: w.ProjectID, Name: w.Name, Path: w.Path, Kind: w.Kind, Branch: w.Branch, CreatedAt: w.CreatedAt}
		if p, ok := projectPath[w.ProjectID]; ok && w.Kind == "folder" && filepath.Clean(w.Path) != p {
			mw.Attached = true
			if !detached {
				mw.Shown = head
			}
		}
		for _, s := range byWS[w.ID] {
			// 手机上只显示还在运行的终端：退出的终端没有输出可看，订阅会立刻结束
			if !s.Running() {
				continue
			}
			v := s.snapshotView()
			a := v.state
			title := mobileTerminalTitle(v, workerTitles[s.ID])
			mw.Terminals = append(mw.Terminals, mobile.Terminal{
				ID: s.ID, Title: title, Agent: v.agent, Status: a.Status, StatusAt: a.At, Message: a.Message,
				SessionID: a.SessionID, TranscriptPath: a.TranscriptPath,
				ToolName: a.ToolName, ToolInput: a.ToolInput, Prompt: a.Prompt,
				Running: true, CreatedAt: s.CreatedAt, LastOutput: s.LastOutput(),
				Tasks:  coordinating[s.ID] && (d.waitingTasks(s.ID) || a.Status == agent.StatusIdle),
				Unread: a.Status == agent.StatusIdle && v.agent != "" && v.unread,
			})
		}
		// 还没恢复的终端也列出来，手机点开时才拉起（mobileHost.Restore）；优先级与运行/退出路径保持一致。
		for _, t := range lost[w.ID] {
			title := cmp.Or(t.Title, t.SessionTitle, workerTitles[t.ID], lostFallback(t), t.Agent, "shell")
			mw.Terminals = append(mw.Terminals, mobile.Terminal{ID: t.ID, Title: title, Agent: t.Agent, CreatedAt: t.CreatedAt, Lost: true})
		}
		if len(lost[w.ID]) > 0 {
			slices.SortStableFunc(mw.Terminals, func(a, b mobile.Terminal) int { return a.CreatedAt.Compare(b.CreatedAt) })
		}
		st.Workspaces = append(st.Workspaces, mw)
	}
	return st
}

// mobileTerminalTitle 算出手机上显示的终端标签名：用户改的名优先，其次会话标题、任务标题，
// agent 退出回 shell 后保留退出前的名称而不突然退回 "shell"。入参 v 已在同一临界区内采集，不出现跨代际撕裂。
func mobileTerminalTitle(v sessionView, workerTitle string) string {
	if v.name != "" {
		return v.name
	}
	if v.agent != "" {
		switch {
		case v.state.Title != "":
			return v.state.Title
		case workerTitle != "":
			return workerTitle
		}
		if v.agent == "grok" {
			if v.confirmedTitle != "" {
				return v.confirmedTitle
			}
			return "grok"
		}
		return v.confirmedTitle
	}
	if v.exitedSessionTitle != "" {
		return v.exitedSessionTitle
	}
	if workerTitle != "" {
		return workerTitle
	}
	if v.exitedTitle != "" {
		return v.exitedTitle
	}
	return v.ptyTitle
}

// Terminal 只给托管着的终端：手机的会话流每 500ms 按会话 ID 找一次终端，经 d.session 会把还没恢复的拉起来。
func (h mobileHost) Terminal(id string) host.Terminal {
	s := h.d.liveSession(id)
	if s == nil {
		return nil
	}
	return s.Terminal
}

func (h mobileHost) TerminalSessionID(id string) string {
	s := h.d.liveSession(id)
	if s == nil || !s.Running() {
		return ""
	}
	switch s.agentName() {
	case "":
		return ""
	case "antigravity":
		// PTY 的输入交给前台进程。在它持有唯一会话锁时才允许对话写入。
		return h.d.antigravityPresenceSessionID(s.Foreground())
	}
	return s.Agent().SessionID
}

// SessionTerminal 和从 State 里按会话 ID 找选到同一个：按工作区的顺序、工作区里按终端开的先后，
// 只看还在运行的。只查一次工作区列表，不读各工作区的 HEAD、不查任务。
func (h mobileHost) SessionTerminal(agentName, sessionID string) host.Terminal {
	ws, err := h.d.store.Workspaces("")
	if err != nil {
		return nil
	}
	byWS := h.d.sessionsByWorkspace()
	for _, w := range ws {
		for _, s := range byWS[w.ID] {
			if s.Running() && s.Agent().SessionID == sessionID && (agentName == "" || s.agentName() == agentName) {
				return s.Terminal
			}
		}
	}
	return nil
}

func (h mobileHost) Restore(id string) host.Terminal {
	s, err := h.d.session(id)
	if err != nil {
		log.Printf("mobile: restore %s: %v", id, err)
		return nil
	}
	return s.Terminal
}

func (h mobileHost) View(id string) func() {
	if s := h.d.liveSession(id); s != nil {
		return s.view()
	}
	return func() {}
}

func (h mobileHost) CreateTerminal(workspaceID, agentName, prompt, command string) (string, error) {
	w, err := h.d.workspace(workspaceID)
	if err != nil {
		return "", err
	}
	var s *session
	if agentName != "" {
		s, err = h.d.startUserAgent(w, agentName, agent.Model{}, prompt, 0, 0)
	} else {
		s, err = h.d.startTerminal(w, agent.ShellArgv(command), "", 0, 0)
	}
	if err != nil {
		return "", err
	}
	return s.ID, nil
}

func (h mobileHost) CloseTerminal(id string) error {
	if !h.d.closeTerminal(id, 2*time.Second) {
		return rpc.Errorf(rpc.CodeNotFound, "terminal %s not found", id)
	}
	return nil
}

func (h mobileHost) Agents() []string {
	if got := h.d.enabledAgents(); got != nil {
		return got
	}
	return agent.FallbackAgents()
}

func (h mobileHost) Usage() any {
	if st := h.d.usageStatus(); st != nil {
		return st
	}
	return usageStatus{Usages: []usage.Usage{}}
}

func (h mobileHost) RecordGit(r mobile.GitRecord) {
	rec := gitTraceRecord{Source: "mobile", Device: r.Device, Dir: r.Dir, Args: r.Args, Start: r.Start,
		TookMs: r.Took.Milliseconds(), Err: r.Err}
	if r.Err != "" {
		rec.Code = 1
	}
	if w, err := h.d.store.Workspace(r.WorkspaceID); err == nil {
		rec.Project = w.ProjectID
	}
	if h.d.gitTrace != nil {
		h.d.gitTrace.add(rec)
	}
}

func (h mobileHost) RefreshUsage(ctx context.Context, params json.RawMessage) (any, error) {
	return h.d.usageRefresh(ctx, params)
}

func (h mobileHost) ZCodeSessionTools(ctx context.Context, id string) (history.SessionUsage, error) {
	return h.d.readZCodeTools(ctx, id)
}

func (h mobileHost) History(ctx context.Context, method string, params json.RawMessage) (any, error) {
	d := h.d
	switch method {
	case "history.list":
		return d.historyList(ctx, params)
	case "history.search":
		return d.historySearch(ctx, params)
	case "history.resume":
		res, err := d.historyResume(ctx, params)
		if err != nil {
			return nil, err
		}
		r := res.(historyResumeResult)
		w, err := d.workspace(r.WorkspaceID)
		if err != nil {
			return nil, err
		}
		return mobile.ResumeResult{Terminal: r.ID, Worktree: w.ProjectID + "::" + w.ID, AlreadyOpen: r.AlreadyOpen}, nil
	}
	return nil, rpc.Errorf(rpc.CodeBadRequest, "unknown method %s", method)
}

type mobilePairParams struct {
	Rotate  bool   `json:"rotate"`
	Address string `json:"address"`
}

func (d *Daemon) mobilePair(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mobilePairParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if d.mobile == nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "mobile service unavailable, see daemon log")
	}
	info, err := d.mobile.Pair(p.Rotate, p.Address)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	return info, nil
}

func (d *Daemon) mobileDevices(ctx context.Context, _ json.RawMessage) (any, error) {
	if d.mobile == nil {
		return []mobile.Device{}, nil
	}
	out := d.mobile.Devices()
	if out == nil {
		out = []mobile.Device{}
	}
	return out, nil
}

type mobileRmParams struct {
	Device string `json:"device"`
}

func (d *Daemon) mobileRm(ctx context.Context, raw json.RawMessage) (any, error) {
	var p mobileRmParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if d.mobile == nil {
		return nil, rpc.Errorf(rpc.CodeNotFound, "device %q not found", p.Device)
	}
	found, err := d.mobile.RemoveDevice(p.Device)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeInternal, "%v", err)
	}
	if !found {
		return nil, rpc.Errorf(rpc.CodeNotFound, "device %q not found", p.Device)
	}
	return map[string]any{"removed": p.Device}, nil
}
