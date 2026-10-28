package daemon

import (
	"errors"
	"log"
	"os"
	"slices"
	"time"

	"posse/internal/agent"
	"posse/internal/host"
	"posse/internal/procmem"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 重启电脑或常驻进程之后恢复终端：上一个进程托管的终端在库里记成 lost，界面、手机照常列出（Lost），
// 第一次接入或操作时按记下的 agent、会话和目录重新拉起，ID、名字、标签顺序不变。设置里打开
// "重启后恢复全部终端"（agent.RestoreAll）时常驻进程一启动就全部拉起。

// resumeRec 是恢复一个终端要用的信息，存在 terminals 的 agent、session_id、cwd。
// label 是这时显示的名字（会话标题，没有时是终端标题），重启后还没恢复时照它显示。
type resumeRec struct{ agent, sid, cwd, label string }

// resumeSettle 是 agent 退出后等多久才把它从记录上摘掉：关机时 agent 和常驻进程差不多同时收到
// SIGTERM，agent 先退出的话不能把要续接的会话清掉，等这一会儿常驻进程已经在退出了（exiting）。
var resumeSettle = 5 * time.Second

// saveResume 记下 s 现在的 agent、会话和前台程序所在的目录，和库里的不同才写。
// settled 为真是 resumeSettle 之后的复查，这时 agent 不在了才摘。
func (d *Daemon) saveResume(s *session, settled bool) {
	if d.exiting.Load() || !s.Running() {
		return
	}
	rec := resumeRec{agent: s.agentName()}
	if rec.agent != "" {
		st := s.Agent()
		rec.sid, rec.label = st.SessionID, st.Title
	}
	if rec.label == "" {
		rec.label = agent.BareTitle(s.Title())
	}
	pid := s.Foreground()
	if pid <= 0 {
		pid = s.Pid()
	}
	rec.cwd = procmem.Cwd(pid)
	s.mu.Lock()
	if rec.cwd == "" {
		rec.cwd = s.saved.cwd
	}
	if rec.label == "" {
		// 刚恢复、agent 还没设标题时先沿用之前的名字
		rec.label = s.saved.label
	}
	if rec.sid == "" && rec.agent == s.saved.agent {
		// 会话 ID 要等第一个 hook 才有，中间被标题、进程检查重置成空时不丢掉已经记下的
		rec.sid = s.saved.sid
	}
	if rec == s.saved {
		s.mu.Unlock()
		return
	}
	if rec.agent == "" && s.saved.agent != "" && !settled {
		if !s.settling {
			s.settling = true
			time.AfterFunc(resumeSettle, func() { d.saveResume(s, true) })
		}
		s.mu.Unlock()
		return
	}
	if settled {
		s.settling = false
	}
	s.saved = rec
	s.mu.Unlock()
	if err := d.store.SetTerminalResume(s.ID, rec.agent, rec.sid, rec.cwd, rec.label); err != nil {
		log.Printf("terminal %s: record resume: %v", s.ID, err)
	}
}

// lostIn 是工作区里还没恢复的终端，workspaceID 为空时是全部。lost 只在启动时产生，
// 查到一次没有之后就不再查库（界面每秒都要列）。
func (d *Daemon) lostIn(workspaceID string) []store.Terminal {
	if d.noLost.Load() {
		return nil
	}
	ts, err := d.store.LostTerminals(workspaceID)
	if err != nil {
		log.Printf("lost terminals: %v", err)
		return nil
	}
	if len(ts) == 0 && workspaceID == "" {
		d.noLost.Store(true)
	}
	return ts
}

// lostByWorkspace 按工作区分组还没恢复的终端。
func (d *Daemon) lostByWorkspace() map[string][]store.Terminal {
	ts := d.lostIn("")
	if len(ts) == 0 {
		return nil
	}
	out := map[string][]store.Terminal{}
	for _, t := range ts {
		out[t.WorkspaceID] = append(out[t.WorkspaceID], t)
	}
	return out
}

// terminalInfos 把托管着的和还没恢复的终端合成一个列表，按创建时间排序。
func (d *Daemon) terminalInfos(sessions []*session, lost []store.Terminal) []terminalInfo {
	out := make([]terminalInfo, 0, len(sessions)+len(lost))
	for _, s := range sessions {
		out = append(out, d.terminalInfo(s))
	}
	if len(lost) == 0 {
		return out
	}
	for _, t := range lost {
		out = append(out, d.lostInfo(t))
	}
	slices.SortStableFunc(out, func(a, b terminalInfo) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return out
}

func (d *Daemon) lostInfo(t store.Terminal) terminalInfo {
	return terminalInfo{ID: t.ID, WorkspaceID: t.WorkspaceID, Title: t.Label, Name: t.Title, Command: t.Command, Dir: t.Cwd,
		Agent: t.Agent, CreatedAt: t.CreatedAt, Lost: true}
}

// closeTerminal 关掉托管着的终端，或删掉还没恢复的记录；两样都没有时返回 false。
func (d *Daemon) closeTerminal(id string, grace time.Duration) bool {
	if s := d.liveSession(id); s != nil {
		d.closeSession(s, grace)
		return true
	}
	d.restoreMu.Lock()
	defer d.restoreMu.Unlock()
	if s := d.liveSession(id); s != nil {
		d.closeSession(s, grace)
		return true
	}
	if _, err := d.store.LostTerminal(id); err != nil {
		return false
	}
	if err := d.store.DeleteTerminal(id); err != nil {
		log.Printf("terminal %s: delete: %v", id, err)
	}
	return true
}

// restoreTerminal 拉起上一个常驻进程留下的终端：agent 有会话的续接（同历史页的恢复），没有会话的
// 新开一个，关掉了的 agent 和普通终端在记下的目录开 shell。cols、rows 为 0 时用默认尺寸，界面接入时再改。
func (d *Daemon) restoreTerminal(id string, cols, rows int) (*session, error) {
	d.restoreMu.Lock()
	defer d.restoreMu.Unlock()
	if s := d.liveSession(id); s != nil {
		return s, nil
	}
	t, err := d.store.LostTerminal(id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, rpc.Errorf(rpc.CodeNotFound, "terminal %s not found", id)
	} else if err != nil {
		return nil, err
	}
	w, err := d.store.Workspace(t.WorkspaceID)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(t.Cwd); t.Cwd == "" || err != nil || !fi.IsDir() {
		t.Cwd = w.Path
	}
	name, argv, st := d.restoreCommand(&t)
	s, err := d.launchTerminal(w, argv, name, cols, rows, t)
	if err != nil {
		return nil, err
	}
	if st != nil {
		s.SetAgent(*st)
	}
	log.Printf("terminal %s: restored (agent %q, session %q)", id, name, t.SessionID)
	return s, nil
}

// restoreBanner 在恢复的终端最前面打一行，说明之前的输出没有了。
const restoreBanner = `printf '\033[2m— Posse 已恢复上次的终端 —\033[0m\n'`

// restoreCommand 决定恢复 t 时运行什么；t.SessionID 改成实际用的会话（新开的 agent 是新会话）。
func (d *Daemon) restoreCommand(t *store.Terminal) (string, []string, *host.AgentState) {
	name := t.Agent
	if name != "" && d.checkEnabled(name) == nil {
		var l agent.Launch
		var err error
		if t.SessionID != "" {
			l, err = d.launcher.Resume(name, t.SessionID, t.Cwd)
		} else {
			l, err = d.launcher.Launch(name, agent.Model{}, "")
		}
		if err == nil && len(l.Argv) > 0 {
			sid := t.SessionID
			if sid == "" {
				sid = l.SessionID
			}
			t.SessionID = sid
			argv := slices.Clone(l.Argv)
			argv[len(argv)-1] = restoreBanner + "; " + argv[len(argv)-1]
			st := &host.AgentState{Status: agent.StatusStarting, SessionID: sid, At: time.Now()}
			// 同 historyResume：codex、cursor 改状态的第一个 hook 要到第一轮对话才来
			if name == "codex" || name == "cursor" {
				st.Status = ""
			}
			return name, argv, st
		}
		log.Printf("terminal %s: cannot restore %s: %v", t.ID, name, err)
	}
	t.SessionID = ""
	return "", agent.ShellArgv(restoreBanner), nil
}

// restoreAll 恢复上一个常驻进程留下的全部终端。
func (d *Daemon) restoreAll() {
	ts, err := d.store.LostTerminals("")
	if err != nil {
		log.Printf("restore terminals: %v", err)
		return
	}
	for _, t := range ts {
		if d.exiting.Load() {
			return
		}
		if _, err := d.restoreTerminal(t.ID, 0, 0); err != nil {
			log.Printf("terminal %s: restore: %v", t.ID, err)
		}
	}
}
