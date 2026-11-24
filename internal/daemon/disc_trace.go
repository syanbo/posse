package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
	"posse/internal/rpc"
	"posse/internal/store"
)

// discProgress 是活跃执行（仅本机拥有）的实时进度，随 discussion.show 带出，不落盘、不推进讨论版本。
type discProgress struct {
	Phase     string `json:"phase,omitempty"`     // 屏幕状态行，如 "Puzzling… (1m 54s · ↓ 9.5k tokens)"
	Tool      string `json:"tool,omitempty"`      // hooks 上报的当前工具名
	ToolInput string `json:"toolInput,omitempty"` // 工具一行摘要
	Subagents int    `json:"subagents,omitempty"` // 后台子 agent 数量
	Events    int    `json:"events,omitempty"`    // 本轮已记录的过程事件数
}

type discStatusCache struct {
	lastOutput time.Time
	checkedAt  time.Time
	phase      string
}

type discTraceLiveCache struct {
	at    time.Time
	sum   store.ExecTraceSummary
	count int
}

type discTraceState struct {
	mu      sync.Mutex
	status  map[string]discStatusCache    // workerID → 屏幕状态行缓存
	liveRes map[string]discTraceLiveCache // execID → 进行中实时扫描缓存（1 秒）
}

const (
	discStatusThrottle = 500 * time.Millisecond
	discTraceLiveTTL   = time.Second
)

// discProgressFor 收集当前讨论里本机拥有的活跃轮次的实时进度。
func (d *Daemon) discProgressFor(xs []store.Execution) map[string]discProgress {
	var out map[string]discProgress
	for _, x := range xs {
		if !store.ExecActive(x.State) || !d.mine(x.Owner) || x.Worker == "" {
			continue
		}
		s := d.liveSession(x.Worker)
		if s == nil || !s.Running() {
			continue
		}
		a := s.Agent()
		// 首次拉起的 worker 可能在 BindWorker 时还没拿到 TranscriptPath，这里趁轮询顺手补记锚点路径
		if a.TranscriptPath != "" || a.SessionID != "" {
			d.ensureTracePath(x.ID, s.agentName(), a.SessionID, a.TranscriptPath)
		}
		var p discProgress
		p.Tool = a.ToolName
		p.ToolInput = trimOneLine(a.ToolInput, 80)
		p.Subagents = len(a.Subagents)
		p.Phase = d.cachedStatusLine(s)
		if p.Tool == "" && p.Phase == "" && p.Subagents == 0 {
			continue
		}
		if out == nil {
			out = map[string]discProgress{}
		}
		out[x.ID] = p
	}
	return out
}

func (d *Daemon) cachedStatusLine(s *session) string {
	lastOut := s.LastOutput()
	now := time.Now()
	d.disc.trace.mu.Lock()
	if d.disc.trace.status == nil {
		d.disc.trace.status = map[string]discStatusCache{}
	}
	c, ok := d.disc.trace.status[s.ID]
	if ok && (c.lastOutput.Equal(lastOut) || now.Sub(c.checkedAt) < discStatusThrottle) {
		d.disc.trace.mu.Unlock()
		return c.phase
	}
	d.disc.trace.mu.Unlock()

	phase := agent.ParseStatusLine(s.Screen())

	d.disc.trace.mu.Lock()
	d.disc.trace.status[s.ID] = discStatusCache{lastOutput: lastOut, checkedAt: now, phase: phase}
	d.disc.trace.mu.Unlock()
	return phase
}

func trimOneLine(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

func (d *Daemon) ensureTracePath(execID, agentName, sessionID, reported string) {
	if d.store == nil || execID == "" {
		return
	}
	t, err := d.store.ExecTrace(execID)
	if err == nil && t.TranscriptPath != "" {
		return
	}
	path := history.TranscriptPath(agentName, sessionID, reported)
	if path == "" && sessionID == "" {
		return
	}
	d.store.SaveExecTraceAnchor(execID, agentName, sessionID, path, t.StartOffset, time.Now())
}

// recordTraceStart 在一轮开始时记下会话文件与当前字节偏移：复用 worker 时精确定位本轮起点，新 worker 从 0 开始。
func (d *Daemon) recordTraceStart(execID, worker string, reuse bool) {
	if d.store == nil || execID == "" {
		return
	}
	var agentName, sessionID, path string
	var offset int64
	if s := d.liveSession(worker); s != nil {
		a := s.Agent()
		agentName = s.agentName()
		sessionID = a.SessionID
		path = history.TranscriptPath(agentName, sessionID, a.TranscriptPath)
		if reuse && path != "" {
			if st, err := os.Stat(path); err == nil {
				offset = st.Size()
			}
		}
	}
	d.store.SaveExecTraceAnchor(execID, agentName, sessionID, path, offset, time.Now())
}

// finalizeTraceAsync 在一轮结束（完成、失败、停止、中断）后异步扫描本轮偏移区间并固化精简摘要。
func (d *Daemon) finalizeTraceAsync(execID, worker string) {
	if d.store == nil || execID == "" {
		return
	}
	var agentName, sessionID, reported string
	if s := d.liveSession(worker); s != nil {
		a := s.Agent()
		agentName, sessionID, reported = s.agentName(), a.SessionID, a.TranscriptPath
	}
	go func() {
		t, err := d.store.ExecTrace(execID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return
		}
		if agentName == "" {
			agentName = t.Agent
		}
		if sessionID == "" {
			sessionID = t.SessionID
		}
		if reported == "" {
			reported = t.TranscriptPath
		}
		if agentName == "" {
			if x, err := d.store.Execution(execID); err == nil {
				agentName = x.Agent
			}
		}
		path := history.TranscriptPath(agentName, sessionID, reported)
		if path == "" {
			return
		}
		sum, count, endOff, err := history.ScanTrace(agentName, sessionID, path, t.StartOffset, 0)
		if err != nil {
			return
		}
		d.store.FinalizeExecTrace(execID, agentName, sessionID, path, endOff, toStoreTrace(sum), count, time.Now())
	}()
}

func toStoreTrace(in history.TraceSummary) store.ExecTraceSummary {
	out := store.ExecTraceSummary{
		Tools: in.Tools, Commands: in.Commands, Skills: in.Skills, MCP: in.MCP, Web: in.Web,
	}
	if len(in.Events) > 0 {
		out.Events = make([]store.ExecTraceEvent, len(in.Events))
		for i, e := range in.Events {
			out.Events[i] = store.ExecTraceEvent{
				At: e.At, Kind: e.Kind, Name: e.Name, Detail: e.Detail, Count: e.Count, Failed: e.Failed,
			}
		}
	}
	return out
}

// discExecTrace 返回本机拥有的某轮执行的过程摘要（进行中按起始偏移实时增量扫，结束后读已固化的快照）。
func (d *Daemon) discExecTrace(ctx context.Context, raw json.RawMessage) (any, error) {
	var p struct {
		Execution string `json:"execution"`
	}
	if err := rpc.Decode(raw, &p); err != nil {
		return nil, err
	}
	x, err := d.execSel(p.Execution)
	if err != nil {
		return nil, err
	}
	if !d.mine(x.Owner) {
		return nil, rpc.Errorf(rpc.CodeConflict, "execution trace is only available on the agent's computer")
	}
	t, err := d.store.ExecTrace(x.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if t.Finalized {
		return t, nil
	}
	// 进行中或尚未固化：查 1 秒热缓存，未命中则按起始偏移实时扫描
	now := time.Now()
	d.disc.trace.mu.Lock()
	if d.disc.trace.liveRes == nil {
		d.disc.trace.liveRes = map[string]discTraceLiveCache{}
	}
	if c, ok := d.disc.trace.liveRes[x.ID]; ok && now.Sub(c.at) < discTraceLiveTTL {
		d.disc.trace.mu.Unlock()
		return store.ExecTrace{ExecID: x.ID, Agent: cmpOr(t.Agent, x.Agent), SessionID: t.SessionID, Summary: c.sum, EventCount: c.count}, nil
	}
	d.disc.trace.mu.Unlock()

	agentName, sessionID, reported := cmpOr(t.Agent, x.Agent), t.SessionID, t.TranscriptPath
	if s := d.liveSession(x.Worker); s != nil {
		a := s.Agent()
		agentName = cmpOr(s.agentName(), agentName)
		sessionID = cmpOr(a.SessionID, sessionID)
		reported = cmpOr(a.TranscriptPath, reported)
	}
	path := history.TranscriptPath(agentName, sessionID, reported)
	if path == "" {
		return store.ExecTrace{ExecID: x.ID, Agent: agentName, SessionID: sessionID}, nil
	}
	sum, count, endOff, err := history.ScanTrace(agentName, sessionID, path, t.StartOffset, t.EndOffset)
	if err != nil {
		return store.ExecTrace{ExecID: x.ID, Agent: agentName, SessionID: sessionID}, nil
	}
	stSum := toStoreTrace(sum)
	if !store.ExecActive(x.State) && !store.ExecPending(x.State) {
		// 已经结束但还没固化过（如老轮次或异步未落盘）：顺手固化
		d.store.FinalizeExecTrace(x.ID, agentName, sessionID, path, endOff, stSum, count, now)
		return store.ExecTrace{ExecID: x.ID, Agent: agentName, SessionID: sessionID, Summary: stSum, EventCount: count, Finalized: true, UpdatedAt: now}, nil
	}
	d.disc.trace.mu.Lock()
	d.disc.trace.liveRes[x.ID] = discTraceLiveCache{at: now, sum: stSum, count: count}
	d.disc.trace.mu.Unlock()
	return store.ExecTrace{ExecID: x.ID, Agent: agentName, SessionID: sessionID, Summary: stSum, EventCount: count}, nil
}
