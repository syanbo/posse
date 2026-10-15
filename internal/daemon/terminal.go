package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"hive/internal/agent"
	"hive/internal/rpc"
	"hive/internal/store"
	"hive/internal/term"
)

type terminalInfo struct {
	ID           string           `json:"id"`
	WorkspaceID  string           `json:"workspaceId"`
	Title        string           `json:"title,omitempty"`
	Command      []string         `json:"command"`
	Dir          string           `json:"dir"`
	Agent        string           `json:"agent,omitempty"`
	AgentState   *term.AgentState `json:"agentState,omitempty"`
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
		Agent: s.agent, Running: s.Running(), Pid: s.Pid(), Cols: cols, Rows: rows,
		CreatedAt: s.CreatedAt, LastOutputAt: s.LastOutput(), IdleMs: time.Since(s.LastOutput()).Milliseconds(),
	}
	if st := s.Agent(); st.Status != "" {
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
		s.SetAgent(term.AgentState{Status: agent.StatusStarting, SessionID: l.SessionID, At: time.Now()})
	}
	return s, nil
}

func (d *Daemon) startTerminal(w store.Workspace, argv []string, agentName string, cols, rows int, extraEnv ...string) (*session, error) {
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
	t, err := term.Start(term.Options{
		ID: id, WorkspaceID: w.ID, Dir: w.Path, Command: argv, Env: env,
		Cols: cols, Rows: rows, Scrollback: d.cfg.Scrollback,
		OnTitle: func(t *term.Terminal, title string) {
			if agentName == "" {
				return
			}
			if st, ok := agent.TitleStatus(agentName, title, t.Agent().Status); ok {
				d.setAgent(t, term.AgentState{Status: st, Event: "title", At: time.Now()})
			}
		},
		OnExit: func(t *term.Terminal, code int) {
			if err := d.store.EndTerminal(t.ID, "exited", &code, time.Now()); err != nil {
				log.Printf("terminal %s: record exit: %v", t.ID, err)
			}
			d.workerGone(t.ID, fmt.Sprintf("worker terminal exited (code %d) before reporting", code))
		},
	})
	if err != nil {
		return nil, err
	}
	s := &session{Terminal: t, agent: agentName}
	d.mu.Lock()
	d.terms[id] = s
	d.mu.Unlock()
	if err := d.store.AddTerminal(store.Terminal{ID: id, WorkspaceID: w.ID, Command: argv, Agent: agentName, State: "running", CreatedAt: t.CreatedAt}); err != nil {
		log.Printf("terminal %s: record: %v", id, err)
	}
	return s, nil
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
			matched = since >= idle || !s.Running()
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
	st, ok := agent.MapEvent(p.Agent, p.Event, p.Payload)
	if !ok {
		return map[string]bool{"ignored": true}, nil
	}
	st.At = time.Now()
	d.setAgent(s.Terminal, st)
	return st, nil
}

// setAgent 记录 agent 状态，并处理与任务相关的后续：空闲时投递未读消息，agent 退出时结束它的任务。
func (d *Daemon) setAgent(t *term.Terminal, st term.AgentState) {
	t.SetAgent(st)
	switch st.Status {
	case agent.StatusIdle:
		go d.deliver(t.ID)
	case agent.StatusExited:
		go d.workerGone(t.ID, "worker agent exited before reporting")
	}
}
