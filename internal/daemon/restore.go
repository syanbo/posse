package daemon

import (
	"errors"
	"log"
	"os"
	"slices"
	"time"

	"posse/internal/agent"
	"posse/internal/errtext"
	"posse/internal/host"
	"posse/internal/procmem"
	"posse/internal/rpc"
	"posse/internal/store"
)

// 重启电脑或常驻进程之后恢复终端：上一个进程托管的终端在库里记成 lost，界面、手机照常列出（Lost），
// 第一次接入或操作时按记下的 agent、会话和目录重新拉起，ID、名字、标签顺序不变。设置里打开
// "重启后恢复全部终端"（agent.RestoreAll）时常驻进程一启动就全部拉起。

// resumeRec 是恢复一个终端要用的信息，存在 terminals 的 agent、session_id、cwd、label、session_title。
// sessionTitle 是显式会话标题（优先级高于任务标题），label 是退出前或终端的兜底标题，重启后还没恢复时照它们显示。
type resumeRec struct{ agent, sid, cwd, label, sessionTitle string }

// resumeSettle 是 agent 退出后等多久才把它从记录上摘掉：关机时 agent 和常驻进程差不多同时收到
// SIGTERM，agent 先退出的话不能把要续接的会话清掉，等这一会儿常驻进程已经在退出了（exiting）。
var resumeSettle = 5 * time.Second

// saveResume 记下 s 现在的 agent、会话和前台程序所在的目录，和库里的不同才写。
// settled 为真是 resumeSettle 之后的复查，这时 agent 不在了才摘。
func (d *Daemon) saveResume(s *session, settled bool) {
	d.saveResumeFor(s, settled, 0)
}

func (d *Daemon) saveResumeSettled(s *session, timerGen uint64) {
	d.saveResumeFor(s, true, timerGen)
}

func (d *Daemon) saveResumeFor(s *session, settled bool, timerGen uint64) {
	s.resumeMu.Lock()
	defer s.resumeMu.Unlock()
	for attempt := 0; attempt < 4; attempt++ {
		if d.exiting.Load() || !s.Running() {
			if timerGen != 0 {
				s.mu.Lock()
				if s.settleGen == timerGen {
					s.settleGen = 0
				}
				s.mu.Unlock()
			}
			return
		}
		// 在同一 session.mu 临界区内采集 agent、代际、会话态与标题（session.mu → Terminal.mu），
		// 避免把旧 agent 的会话 ID 或标题拼到刚启动的新 agent 上。
		s.mu.Lock()
		gen := s.agentGen
		if timerGen != 0 {
			if s.settleGen == timerGen {
				s.settleGen = 0
			}
			if gen != timerGen {
				s.mu.Unlock()
				return
			}
		}
		agentName := s.agent
		confirmed := s.confirmedAgentTitleLocked()
		exitedSession, exitedTitle := s.exitedSessionTitle, s.exitedTitle
		st := s.Agent()
		curTitle := s.Title()
		s.mu.Unlock()

		pid := s.Foreground()
		if pid <= 0 {
			pid = s.Pid()
		}
		cwd := procmem.Cwd(pid)

		s.mu.Lock()
		if s.agentGen != gen {
			// 探测前台目录期间 agent 已切换，重试重新采集同一代际的快照。
			if timerGen != 0 {
				if s.settleGen == timerGen {
					s.settleGen = 0
				}
				s.mu.Unlock()
				return
			}
			s.mu.Unlock()
			continue
		}
		rec := resumeRec{agent: agentName, cwd: cwd}
		if rec.agent != "" {
			rec.sid, rec.sessionTitle = st.SessionID, st.Title
			if confirmed != "" && confirmed != rec.agent {
				rec.label = confirmed
			}
			s.settleGen = 0
		} else {
			rec.sessionTitle = exitedSession
			if exitedTitle != "" {
				rec.label = exitedTitle
			} else {
				rec.label = agent.BareTitle(curTitle)
			}
		}
		if rec.cwd == "" {
			rec.cwd = s.saved.cwd
		}
		sameInstance := s.savedGen == gen && rec.agent == s.saved.agent && (rec.sid == "" || s.saved.sid == "" || rec.sid == s.saved.sid)
		if sameInstance {
			if rec.sessionTitle == "" {
				rec.sessionTitle = s.saved.sessionTitle
			}
			if rec.label == "" {
				// 刚恢复、agent 还没设标题时先沿用同一实例之前的名字
				rec.label = s.saved.label
			}
			if rec.sid == "" {
				// 会话 ID 要等第一个 hook 才有，中间被标题、进程检查重置成空时不丢掉同一实例已经记下的
				rec.sid = s.saved.sid
			}
		}
		if s.savedSet && rec == s.saved && s.savedGen == gen {
			if settled && timerGen == 0 && s.settleGen == gen {
				s.settleGen = 0
			}
			s.mu.Unlock()
			return
		}
		if rec.agent == "" && s.saved.agent != "" && !settled {
			if s.settleGen != gen {
				s.settleGen = gen
				time.AfterFunc(resumeSettle, func() { d.saveResumeSettled(s, gen) })
			}
			s.mu.Unlock()
			return
		}
		if settled && s.settleGen == gen {
			s.settleGen = 0
		}
		s.mu.Unlock()

		if err := d.store.SetTerminalResume(s.ID, rec.agent, rec.sid, rec.cwd, rec.label, rec.sessionTitle); err != nil {
			log.Printf("terminal %s: record resume: %v", s.ID, err)
			return
		}
		s.mu.Lock()
		// s.saved 反映数据库里当前实际落盘的值；写库期间若发生代际切换，继续补写最新一代。
		s.saved, s.savedGen, s.savedSet = rec, gen, true
		changed := s.agentGen != gen
		s.mu.Unlock()
		if !changed || timerGen != 0 {
			return
		}
	}
}

// lostFallback 取出恢复记录里的兜底标题（不包含显式会话标题，旧版记录只有 label 时照旧回退）。
func lostFallback(t store.Terminal) string {
	if t.FallbackTitle != "" {
		return t.FallbackTitle
	}
	if t.SessionTitle == "" {
		return t.Label
	}
	return ""
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
	return terminalInfo{ID: t.ID, WorkspaceID: t.WorkspaceID, Title: lostFallback(t), SessionTitle: t.SessionTitle, Name: t.Title, Command: t.Command, Dir: t.Cwd,
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
	t, err := d.store.LostTerminal(id)
	if err != nil {
		return false
	}
	if err := d.store.DeleteTerminal(id); err != nil {
		log.Printf("terminal %s: delete: %v", id, err)
	}
	d.dropChatFolder(id, t.Cwd)
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
		s.mu.Lock()
		if s.agent == name && s.agentGen == 1 {
			cur := s.Agent()
			if cur.Status == "" && cur.SessionID == "" && cur.Title == "" && cur.Event == "" {
				s.SetAgent(*st)
			} else if st.Title != "" && cur.Title == "" && (cur.SessionID == "" || cur.SessionID == st.SessionID) {
				s.SetTitle(st.Title, st.SessionID, false)
			}
		}
		s.mu.Unlock()
	}
	log.Printf("terminal %s: restored (agent %q, session %q)", id, name, t.SessionID)
	return s, nil
}

// restoreBanner 在恢复的终端最前面打一行，说明之前的输出没有了。常驻进程没有界面语言，按用户选的语言写
// （errtext.SystemLang）；译文里不能有单引号和 %，它们会进 printf 的格式串。
func restoreBanner() string {
	return `printf '\033[2m` + errtext.MessageIn(errtext.SystemLang(), srcRestored) + `\033[0m\n'`
}

const srcRestored = "— Posse restored the previous terminal —"

// restoreCommand 决定恢复 t 时运行什么；t.SessionID 改成实际用的会话（新开的 agent 是新会话）。
func (d *Daemon) restoreCommand(t *store.Terminal) (string, []string, *host.AgentState) {
	name := t.Agent
	if name != "" && d.checkEnabled(name) == nil && (name != "zcode" || d.installedAgents() == nil || d.agentInstalled(name)) {
		var l agent.Launch
		var err error
		if t.SessionID != "" {
			l, err = d.launcher.Resume(name, t.SessionID, t.Cwd)
		} else {
			l, err = d.launcher.Launch(name, agent.Model{}, "")
		}
		if err == nil && len(l.Argv) > 0 {
			resumed := t.SessionID != ""
			sid := t.SessionID
			if sid == "" {
				sid = l.SessionID
			}
			t.SessionID = sid
			argv := slices.Clone(l.Argv)
			argv[len(argv)-1] = restoreBanner() + "; " + argv[len(argv)-1]
			st := &host.AgentState{Status: agent.StatusStarting, SessionID: sid, At: time.Now()}
			// 同 historyResume：codex、cursor 改状态的第一个 hook 要到第一轮对话才来
			if name == "codex" || name == "cursor" || name == "antigravity" || name == "zcode" {
				st.Status = ""
			}
			if resumed {
				// 续接原会话时恢复显式会话标题；新开会话从自己的标题开始。
				st.Title = t.SessionTitle
				if st.Title == "" && name == "zcode" {
					st.Title = t.Label
				}
			} else {
				t.Label, t.FallbackTitle, t.SessionTitle = "", "", ""
			}
			return name, argv, st
		}
		log.Printf("terminal %s: cannot restore %s: %v", t.ID, name, err)
	}
	t.SessionID = ""
	return "", agent.ShellArgv(restoreBanner()), nil
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
