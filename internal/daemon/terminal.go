package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"hive/internal/agent"
	"hive/internal/host"
	"hive/internal/rpc"
	"hive/internal/store"
)

type terminalInfo struct {
	ID           string           `json:"id"`
	WorkspaceID  string           `json:"workspaceId"`
	Title        string           `json:"title,omitempty"`
	Command      []string         `json:"command"`
	Dir          string           `json:"dir"`
	Agent        string           `json:"agent,omitempty"`
	AgentState   *host.AgentState `json:"agentState,omitempty"`
	Running      bool             `json:"running"`
	ExitCode     *int             `json:"exitCode,omitempty"`
	Pid          int              `json:"pid"`
	Cols         int              `json:"cols"`
	Rows         int              `json:"rows"`
	CreatedAt    time.Time        `json:"createdAt"`
	LastOutputAt time.Time        `json:"lastOutputAt"`
	IdleMs       int64            `json:"idleMs"`
}

func (d *Daemon) terminalInfo(s *session) terminalInfo {
	cols, rows := s.Size()
	info := terminalInfo{
		ID: s.ID, WorkspaceID: s.WorkspaceID, Title: s.Title(), Command: s.Command, Dir: s.Dir,
		Agent: s.agentName(), Running: s.Running(), Pid: s.Pid(), Cols: cols, Rows: rows,
		CreatedAt: s.CreatedAt, LastOutputAt: s.LastOutput(), IdleMs: time.Since(s.LastOutput()).Milliseconds(),
	}
	// agent 退出后终端退回普通 shell，最后的状态和会话信息不再给出去，界面、手机都按 shell 显示
	if st := s.Agent(); st.Status != "" && info.Agent != "" {
		info.AgentState = &st
	}
	if !info.Running {
		code := s.ExitCode()
		info.ExitCode = &code
	}
	return info
}

// startAgent 启动 agent；Claude 的会话 ID 在启动前就确定了。
func (d *Daemon) startAgent(w store.Workspace, name, prompt string, cols, rows int, env ...string) (*session, error) {
	l, err := d.launcher.Launch(name, prompt)
	if err != nil {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "%v", err)
	}
	s, err := d.startTerminal(w, l.Argv, name, cols, rows, env...)
	if err != nil {
		return nil, err
	}
	if l.SessionID != "" {
		s.SetAgent(host.AgentState{Status: agent.StatusStarting, SessionID: l.SessionID, At: time.Now()})
	}
	return s, nil
}

func (d *Daemon) startTerminal(w store.Workspace, argv []string, agentName string, cols, rows int, extraEnv ...string) (*session, error) {
	d.handoff.startMu.RLock()
	defer d.handoff.startMu.RUnlock()
	if d.handoff.active() {
		return nil, rpc.Errorf(rpc.CodeConflict, "daemon is handing off to a new version, try again")
	}
	id := store.NewID("t_")
	env := append([]string{
		"HIVE_HOME=" + d.cfg.Home,
		"HIVE_SOCKET=" + SocketPath(d.cfg.Home),
		"HIVE_TERMINAL_ID=" + id,
		"HIVE_WORKSPACE_ID=" + w.ID,
		"HIVE_WORKSPACE_PATH=" + w.Path,
	}, extraEnv...)
	if agentName != "" {
		env = append(env, "HIVE_AGENT="+agentName)
	}
	if err := d.shellFiles(); err == nil {
		env = append(env, agent.ShellEnv(d.cfg.Home, argv[0], d.userZdotdir)...)
	}
	t, err := d.terminalHost().Start(host.StartOptions{
		ID: id, WorkspaceID: w.ID, Dir: w.Path, Command: argv, Env: env,
		Cols: cols, Rows: rows, Scrollback: d.cfg.Scrollback,
		OnTitle: d.onTitle, OnExit: d.onExit,
	})
	if err != nil {
		return nil, err
	}
	info := t.Info()
	s := &session{Terminal: t, ID: info.ID, WorkspaceID: info.WorkspaceID, Dir: info.Dir, Command: info.Command, CreatedAt: info.CreatedAt,
		agent: agentName, hooked: agentName != ""}
	d.mu.Lock()
	d.terms[id] = s
	d.mu.Unlock()
	if err := d.store.AddTerminal(store.Terminal{ID: id, WorkspaceID: w.ID, Command: argv, Agent: agentName, State: "running", CreatedAt: info.CreatedAt}); err != nil {
		log.Printf("terminal %s: record: %v", id, err)
	}
	return s, nil
}

func (d *Daemon) onTitle(id, title string) {
	d.mu.Lock()
	s := d.terms[id]
	d.mu.Unlock()
	if s == nil {
		return
	}
	d.checkAgentGone(s)
	// agent 启动时会设标题，在这里顺便看前台换没换成别的 agent
	name, hooked, changed := s.detectAgent()
	if changed {
		s.ResetAgent(host.AgentState{Status: agent.StatusIdle, Event: "process", At: time.Now()})
	}
	if name == "" {
		return
	}
	status := agent.TitleStatus
	if !hooked {
		status = agent.TitleOnlyStatus
	}
	if st, ok := status(name, title, s.Agent().Status); ok {
		d.setAgent(s, host.AgentState{Status: st, Event: "title", At: time.Now()})
	}
}

func (d *Daemon) onExit(id string, code int) {
	// 交接期间旧进程不处理退出：成功了由新进程盯着，失败了恢复时补上
	if d.handoff.deferExit(id, code) {
		return
	}
	if err := d.store.EndTerminal(id, "exited", &code, time.Now()); err != nil {
		log.Printf("terminal %s: record exit: %v", id, err)
	}
	d.workerGone(id, fmt.Sprintf("worker terminal exited (code %d) before reporting", code))
	d.refreshAwake()
}

type terminalCreateParams struct {
	Workspace string   `json:"workspace"`
	Command   []string `json:"command"`
	Agent     string   `json:"agent"`
	Prompt    string   `json:"prompt"`
	Cols      int      `json:"cols"`
	Rows      int      `json:"rows"`
}

func (d *Daemon) terminalCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalCreateParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	w, err := d.workspace(p.Workspace)
	if err != nil {
		return nil, err
	}
	var s *session
	switch {
	case p.Agent != "" && len(p.Command) > 0:
		return nil, rpc.Errorf(rpc.CodeBadRequest, "agent and command are exclusive")
	case p.Agent != "":
		s, err = d.startAgent(w, p.Agent, p.Prompt, p.Cols, p.Rows)
	case len(p.Command) > 0:
		s, err = d.startTerminal(w, p.Command, "", p.Cols, p.Rows)
	default:
		s, err = d.startTerminal(w, agent.ShellArgv(""), "", p.Cols, p.Rows)
	}
	if err != nil {
		return nil, err
	}
	return d.terminalInfo(s), nil
}

type terminalListParams struct {
	Workspace string `json:"workspace"`
}

func (d *Daemon) terminalList(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalListParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	wid := ""
	if p.Workspace != "" {
		w, err := d.workspace(p.Workspace)
		if err != nil {
			return nil, err
		}
		wid = w.ID
	}
	out := []terminalInfo{}
	for _, s := range d.sessionsIn(wid) {
		out = append(out, d.terminalInfo(s))
	}
	return out, nil
}

type terminalSel struct {
	Terminal string `json:"terminal"`
}

type terminalShowResult struct {
	terminalInfo
	Workspace string   `json:"workspace"`
	Screen    []string `json:"screen"`
}

func (d *Daemon) terminalShow(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalSel
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	res := terminalShowResult{terminalInfo: d.terminalInfo(s), Screen: s.Screen()}
	if w, err := d.store.Workspace(s.WorkspaceID); err == nil {
		res.Workspace = d.workspaceLabel(w)
	}
	return res, nil
}

type terminalReadParams struct {
	Terminal string `json:"terminal"`
	Lines    int    `json:"lines"`
	Screen   bool   `json:"screen"`
}

type terminalReadResult struct {
	Terminal   string   `json:"terminal"`
	Running    bool     `json:"running"`
	TotalLines int      `json:"totalLines"`
	Lines      []string `json:"lines"`
}

func (d *Daemon) terminalRead(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalReadParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	var lines []string
	if p.Screen {
		lines = s.Screen()
	} else {
		lines = s.Lines()
	}
	total := len(lines)
	if p.Lines <= 0 {
		p.Lines = 60
	}
	if !p.Screen && len(lines) > p.Lines {
		lines = lines[len(lines)-p.Lines:]
	}
	return terminalReadResult{Terminal: s.ID, Running: s.Running(), TotalLines: total, Lines: lines}, nil
}

type terminalSendParams struct {
	Terminal string   `json:"terminal"`
	Text     string   `json:"text"`
	Enter    bool     `json:"enter"`
	Keys     []string `json:"keys"`
}

var keyBytes = map[string]string{
	"enter": "\r", "return": "\r", "tab": "\t", "shift-tab": "\x1b[Z", "esc": "\x1b", "escape": "\x1b",
	"backspace": "\x7f", "space": " ", "up": "\x1b[A", "down": "\x1b[B", "right": "\x1b[C", "left": "\x1b[D",
	"home": "\x1b[H", "end": "\x1b[F", "pageup": "\x1b[5~", "pagedown": "\x1b[6~",
	"ctrl-c": "\x03", "ctrl-d": "\x04", "ctrl-l": "\x0c", "ctrl-u": "\x15", "ctrl-z": "\x1a",
}

func (d *Daemon) terminalSend(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalSendParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	if err := d.mustRunning(s); err != nil {
		return nil, err
	}
	var keys []string
	for _, k := range p.Keys {
		b, ok := keyBytes[strings.ToLower(k)]
		if !ok {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "unknown key %q", k)
		}
		keys = append(keys, b)
	}
	if p.Text != "" {
		if err := s.Send([]byte(p.Text)); err != nil {
			return nil, err
		}
	}
	if p.Enter {
		if p.Text != "" {
			// TUI 会把与文本同批到达的回车当成粘贴内容，分开发送才算提交
			time.Sleep(120 * time.Millisecond)
		}
		keys = append(keys, "\r")
	}
	for _, k := range keys {
		if err := s.Send([]byte(k)); err != nil {
			return nil, err
		}
	}
	return map[string]any{"terminal": s.ID, "sent": true}, nil
}

type terminalWaitParams struct {
	Terminal  string `json:"terminal"`
	For       string `json:"for"` // exit | idle | text | status
	IdleMs    int    `json:"idleMs"`
	Text      string `json:"text"`
	Status    string `json:"status"`
	TimeoutMs int    `json:"timeoutMs"`
}

type terminalWaitResult struct {
	Matched  bool         `json:"matched"`
	For      string       `json:"for"`
	Terminal terminalInfo `json:"terminal"`
}

func (d *Daemon) terminalWait(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalWaitParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	timeout := 10 * time.Minute
	if p.TimeoutMs > 0 {
		timeout = time.Duration(p.TimeoutMs) * time.Millisecond
	}
	idle := time.Duration(p.IdleMs) * time.Millisecond
	if idle <= 0 {
		idle = 3 * time.Second
	}
	statuses := map[string]bool{}
	for _, st := range strings.Split(p.Status, ",") {
		if st = strings.TrimSpace(st); st != "" {
			statuses[st] = true
		}
	}
	switch p.For {
	case "exit", "idle":
	case "text":
		if p.Text == "" {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "text required")
		}
	case "status":
		if len(statuses) == 0 {
			return nil, rpc.Errorf(rpc.CodeBadRequest, "status required (working, waiting, idle)")
		}
	default:
		return nil, rpc.Errorf(rpc.CodeBadRequest, "for must be exit, idle, text or status")
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var lastText time.Time
	for {
		changed := s.Changed()
		var tick <-chan time.Time
		matched := false
		switch p.For {
		case "exit":
			matched = !s.Running()
		case "idle":
			since := time.Since(s.LastOutput())
			// 交接期间不读 PTY，没有输出不代表空闲；等交接结束回 restarting，CLI 到新进程上接着等
			matched = (since >= idle && !d.handoff.active()) || !s.Running()
			if !matched {
				tick = time.After(idle - since)
			}
		case "text":
			// 全文检索有成本，输出很频繁时限制在每 200ms 一次
			if wait := 200*time.Millisecond - time.Since(lastText); wait > 0 {
				tick = time.After(wait)
				changed = nil
			} else {
				lastText = time.Now()
				matched = s.ContainsText(p.Text)
				if !matched && !s.Running() {
					return nil, rpc.Errorf(rpc.CodeConflict, "terminal exited before %q appeared", p.Text)
				}
			}
		case "status":
			matched = statuses[s.Agent().Status]
			if !matched && !s.Running() {
				return nil, rpc.Errorf(rpc.CodeConflict, "terminal exited (code %d) before status %s", s.ExitCode(), p.Status)
			}
		}
		if matched {
			return terminalWaitResult{Matched: true, For: p.For, Terminal: d.terminalInfo(s)}, nil
		}
		select {
		case <-changed:
		case <-tick:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, rpc.Errorf(rpc.CodeTimeout, "timed out after %s waiting for %s", timeout, p.For)
		}
	}
}

type terminalResizeParams struct {
	Terminal string `json:"terminal"`
	Cols     int    `json:"cols"`
	Rows     int    `json:"rows"`
}

func (d *Daemon) terminalResize(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalResizeParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	if p.Cols < 2 || p.Rows < 2 || p.Cols > 1000 || p.Rows > 500 {
		return nil, rpc.Errorf(rpc.CodeBadRequest, "invalid size %dx%d", p.Cols, p.Rows)
	}
	s.Resize(p.Cols, p.Rows)
	return d.terminalInfo(s), nil
}

type terminalClearParams struct {
	Terminal string `json:"terminal"`
	Redraw   bool   `json:"redraw"`
}

// terminalClear 清屏并清空回滚区，见 host.Terminal.Clear。
func (d *Daemon) terminalClear(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalClearParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	if err := s.Clear(p.Redraw); err != nil {
		return nil, err
	}
	return d.terminalInfo(s), nil
}

type terminalCloseParams struct {
	Terminal string `json:"terminal"`
	GraceMs  int    `json:"graceMs"`
}

func (d *Daemon) terminalClose(ctx context.Context, raw json.RawMessage) (any, error) {
	var p terminalCloseParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.Terminal)
	if err != nil {
		return nil, err
	}
	grace := 2 * time.Second
	if p.GraceMs > 0 {
		grace = time.Duration(p.GraceMs) * time.Millisecond
	}
	d.closeSession(s, grace)
	return map[string]any{"closed": s.ID, "exitCode": s.ExitCode()}, nil
}

type hookEventParams struct {
	TerminalID string          `json:"terminalId"`
	Agent      string          `json:"agent"`
	Event      string          `json:"event"`
	Payload    json.RawMessage `json:"payload"`
}

func (d *Daemon) hookEvent(ctx context.Context, raw json.RawMessage) (any, error) {
	var p hookEventParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	s, err := d.session(p.TerminalID)
	if err != nil {
		return nil, err
	}
	if s.agentName() != p.Agent || !s.isHooked() {
		s.markHooked(p.Agent)
	}
	st, ok := agent.MapEvent(p.Agent, p.Event, p.Payload)
	if !ok {
		return map[string]bool{"ignored": true}, nil
	}
	st.At = time.Now()
	d.setAgent(s, st)
	return st, nil
}

// hookPermission 先照常记下等待状态，再只在有 Hive 手机看着这个会话时挂起等决定。
// 没有手机、桌面有人键入、或 30 秒到了，都返回空 behavior，hook 不往 stdout 写，TUI 自己弹框。
func (d *Daemon) hookPermission(ctx context.Context, raw json.RawMessage) (any, error) {
	var p hookEventParams
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	behavior := ""
	s, err := d.session(p.TerminalID)
	if err != nil {
		return map[string]any{"behavior": behavior}, nil
	}
	if st, ok := agent.MapEvent(p.Agent, p.Event, p.Payload); ok {
		st.At = time.Now()
		d.setAgent(s, st)
	}
	if d.mobile != nil && p.Agent == "claude" && p.Event == "PermissionRequest" {
		behavior = d.mobile.HoldPermission(ctx, s.ID, p.Payload, s.LastInput)
		// 常驻进程退出打断的挂起：让 hook 到新进程上再挂一次，手机重连后还能批
		if behavior == "" && ctx.Err() != nil {
			return nil, rpc.Errorf(rpc.CodeRestarting, "daemon is shutting down; retry")
		}
	}
	return map[string]any{"behavior": behavior}, nil
}

// setAgent 记录 agent 状态，并处理与任务相关的后续：空闲时投递未读消息，agent 退出时结束它的任务。
func (d *Daemon) setAgent(s *session, st host.AgentState) {
	s.SetAgent(st)
	d.refreshAwake()
	switch st.Status {
	case agent.StatusIdle:
		go d.deliver(s.ID)
	case agent.StatusExited:
		s.clearAgent(0)
		go d.workerGone(s.ID, "worker agent exited before reporting")
	}
}

// checkAgentGone 发现 agent 进程已经不在时按退出处理。
func (d *Daemon) checkAgentGone(s *session) {
	pg := s.goneAgentPg()
	if pg == 0 || !s.Running() || !s.clearAgent(pg) {
		return
	}
	s.SetAgent(host.AgentState{Status: agent.StatusExited, Event: "process", At: time.Now()})
	d.refreshAwake()
	go d.workerGone(s.ID, "worker agent exited before reporting")
}

// agentLoop 定期检查各终端的 agent 还在不在：退出后 shell 不一定改标题，等不到 onTitle。
func (d *Daemon) agentLoop(ctx context.Context) {
	tick := time.NewTicker(agentTick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		d.mu.Lock()
		ss := make([]*session, 0, len(d.terms))
		for _, s := range d.terms {
			ss = append(ss, s)
		}
		d.mu.Unlock()
		for _, s := range ss {
			d.checkAgentGone(s)
		}
	}
}

const agentTick = 2 * time.Second
