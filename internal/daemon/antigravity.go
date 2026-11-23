package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/host"
	"posse/internal/procmem"
	"posse/internal/rpc"
	"posse/internal/usage"
)

const antigravityReportFresh = 5 * time.Second

type antigravityStatus struct {
	Product string `json:"product"`
	Session string `json:"conversation_id"`
	Title   string `json:"conversation_title"`
	Context *struct {
		Window  int      `json:"context_window_size"`
		Percent *float64 `json:"used_percentage"`
		Current *struct {
			Input       int `json:"input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
		} `json:"current_usage"`
	} `json:"context_window"`
	Quota map[string]usage.AntigravityStatusQuota `json:"quota"`
}

func (d *Daemon) antigravityStatusline(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Terminal string            `json:"terminal"`
		At       time.Time         `json:"at"`
		Payload  antigravityStatus `json:"payload"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	if p.Payload.Product != "antigravity" || time.Since(p.At) > antigravityReportFresh || time.Until(p.At) > time.Second {
		return nil, nil
	}
	s := d.liveSession(p.Terminal)
	if s == nil || !s.Running() || d.foreground(s) != "antigravity" {
		return nil, nil
	}
	s.agyReportMu.Lock()
	defer s.agyReportMu.Unlock()
	if ctx.Err() != nil {
		return nil, nil
	}
	pg := s.Foreground()
	name, _ := agent.OfProcess(pg)
	if name != "antigravity" || pg == s.agyReportPG && !p.At.After(s.agyReportAt) {
		return nil, nil
	}
	id := history.AntigravityOpenSession(d.agentDataHome(), procmem.OpenFiles(pg))
	if id != p.Payload.Session {
		return nil, nil
	}
	if len(p.Payload.Quota) > 0 && d.usageAgentOn("antigravity") {
		d.usage.store.UpdateAntigravityStatusQuota(p.Payload.Quota, p.At)
	}
	if id == "" {
		return nil, nil
	}
	s.agyReportPG, s.agyReportAt = pg, p.At
	// agy 1.5.15 的上报路径指向桌面 App 目录，CLI 日志单独存放。
	path := history.AntigravityTranscriptPath(d.agentDataHome(), id)
	applyAntigravityIdentity(s, id, strings.TrimSpace(p.Payload.Title), path)
	if w := p.Payload.Context; w != nil {
		var value history.Context
		if u := w.Current; u != nil {
			value.Used = u.Input + u.CacheCreate + u.CacheRead
			if w.Window > 0 && w.Percent != nil && *w.Percent >= 0 && *w.Percent <= 100 {
				value.Window, value.ReportedPercent = w.Window, w.Percent
			}
		}
		if err := history.ReportAntigravityContext(d.cfg.Home, id, path, value, p.At); err != nil {
			log.Printf("save Antigravity context: %v", err)
		}
	}
	return nil, nil
}

// antigravityHook 只处理交接与写入，状态继续以 OSC 和前台进程为准。
func (d *Daemon) antigravityHook(ctx context.Context, s *session, p hookEventParams) hookEventResult {
	var payload struct {
		Session    string   `json:"conversationId"`
		Workspaces []string `json:"workspacePaths"`
	}
	if json.Unmarshal(p.Payload, &payload) != nil || payload.Session == "" || !s.Running() {
		return hookEventResult{}
	}
	pg := s.Foreground()
	name, args := agent.OfProcess(pg)
	if name != "antigravity" || history.AntigravityOpenSession(d.agentDataHome(), procmem.OpenFiles(pg)) != payload.Session {
		return hookEventResult{}
	}
	switch p.Event {
	case "PostToolUse":
		d.noteWrites(s, "antigravity", payload.Session, agent.ToolWrites(p.Agent, p.Event, p.Payload))
	case "SessionStart":
		dir := procmem.Cwd(pg)
		if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir {
			return hookEventResult{}
		}
		dir = resolvePath(dir)
		d.rememberAntigravityDir(s, payload.Session, dir)
		// agy 会先挂载 --add-dir 的目录，再等待用户信任主工作区。
		if agent.SessionFromArgs("antigravity", args) == payload.Session ||
			!slices.ContainsFunc(payload.Workspaces, func(path string) bool { return resolvePath(path) == dir }) {
			return hookEventResult{}
		}
		d.refreshHistory(ctx, false, 500*time.Millisecond)
		if brief, err := d.indexedAntigravityBrief(ctx, dir, payload.Session); err == nil {
			return hookEventResult{Brief: brief}
		}
	}
	return hookEventResult{}
}

// syncAntigravity 给 agy 终端补会话 ID 和标题：agy 不能预分配会话 ID，也不写终端标题。
// 会话按 agy 进程持有的 presence 锁认，后续原生改名也一起同步。
func (d *Daemon) syncAntigravity(s *session) {
	if s.agentName() != "antigravity" || !s.Running() {
		return
	}
	s.mu.Lock()
	pg := s.agentPg
	s.mu.Unlock()
	if pg <= 0 {
		pg = s.Foreground()
	}
	if pg <= 0 {
		return
	}
	name, args := agent.OfProcess(pg)
	if name != "antigravity" {
		return
	}
	processDir := procmem.Cwd(pg)
	dir := processDir
	if dir == "" {
		dir = s.Dir
	}
	dirs := slices.DeleteFunc(agent.AntigravityAddedDirs(args, dir), agent.IsAntigravityHookDir)
	sid := history.AntigravityOpenSession(d.agentDataHome(), procmem.OpenFiles(pg))
	s.mu.Lock()
	if s.agent != "antigravity" || s.agentPg != pg && s.agentPg != 0 {
		s.mu.Unlock()
		return
	}
	s.agyDir, s.agyDirs = dir, dirs
	s.mu.Unlock()
	if sid == "" {
		return
	}
	if filepath.IsAbs(processDir) {
		d.rememberAntigravityDir(s, sid, resolvePath(processDir))
	}
	s.agyReportMu.Lock()
	fresh := s.agyReportPG == pg && s.Agent().SessionID == sid && time.Since(s.agyReportAt) < antigravityReportFresh
	s.agyReportMu.Unlock()
	if fresh {
		return
	}
	title := history.AntigravityTitle(d.agentDataHome(), sid)
	s.agyReportMu.Lock()
	defer s.agyReportMu.Unlock()
	if s.agyReportPG == pg && s.Agent().SessionID == sid && time.Since(s.agyReportAt) < antigravityReportFresh || d.antigravityPresenceSessionID(pg) != sid {
		return
	}
	applyAntigravityIdentity(s, sid, title, "")
}

func (d *Daemon) rememberAntigravityDir(s *session, sid, dir string) {
	s.mu.Lock()
	recorded := s.agyRecordedSID == sid
	s.mu.Unlock()
	if recorded {
		return
	}
	if err := d.store.RememberAntigravityDir(sid, dir); err != nil {
		log.Printf("terminal %s: record Antigravity directory: %v", s.ID, err)
		return
	}
	s.mu.Lock()
	s.agyRecordedSID = sid
	s.mu.Unlock()
}

func applyAntigravityIdentity(s *session, sid, title, path string) {
	if s.Agent().SessionID != sid {
		s.SetTitle("", sid, false)
	}
	s.UpdateAgent(func(cur host.AgentState) (host.AgentState, bool) {
		if cur.SessionID != sid {
			return cur, false
		}
		changed := false
		if title != "" && cur.NativeTitle != title {
			// 首次看到原生标题时可能已另起过名，之后原生标题变化才覆盖它。
			if cur.Title == "" || cur.NativeTitle != "" {
				cur.Title = title
			}
			cur.NativeTitle, changed = title, true
		}
		if path != "" && cur.TranscriptPath != path {
			cur.TranscriptPath, changed = path, true
		}
		return cur, changed
	})
}

func (d *Daemon) antigravityPresenceSessionID(pg int) string {
	if pg <= 0 {
		return ""
	}
	if found, _ := agent.OfProcess(pg); found != "antigravity" {
		return ""
	}
	return history.AntigravityOpenSession(d.agentDataHome(), procmem.OpenFiles(pg))
}

// agentDataHome 是各 agent 数据目录所在的家目录，测试里跟着 HistoryRoots。
func (d *Daemon) agentDataHome() string {
	if d.cfg.HistoryRoots != nil {
		return d.cfg.HistoryRoots.Home
	}
	home, _ := os.UserHomeDir()
	return home
}
