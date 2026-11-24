package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// discTraceSchema 是本机私有的执行过程记录表（绝不进入局域网同步）。
// 记录每一轮执行在 worker 会话文件里的起止字节偏移和结束后的精简过程摘要，
// 让复用 worker 的多轮讨论能按轮次准确切分，并在终端关闭或重启后仍可展开查看。
const discTraceSchema = `
CREATE TABLE IF NOT EXISTS disc_exec_trace (
	exec_id TEXT PRIMARY KEY,
	agent TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	transcript_path TEXT NOT NULL DEFAULT '',
	start_offset INTEGER NOT NULL DEFAULT 0,
	end_offset INTEGER NOT NULL DEFAULT 0,
	summary_json TEXT NOT NULL DEFAULT '',
	event_count INTEGER NOT NULL DEFAULT 0,
	finalized INTEGER NOT NULL DEFAULT 0,
	updated_at INTEGER NOT NULL DEFAULT 0
);
`

// ExecTraceEvent 是一轮讨论执行过程中的一条精简记录（工具、命令、技能、MCP 或网页访问）。
type ExecTraceEvent struct {
	At     time.Time `json:"at,omitzero"`
	Kind   string    `json:"kind"`             // tool | cli | mcp | skill | web | search
	Name   string    `json:"name"`             // 工具/程序/技能/域名
	Detail string    `json:"detail,omitempty"` // 命令或简短参数摘要（截断，不含长输出）
	Count  int       `json:"count,omitempty"`  // 连续同类调用合并后的次数（>1 时展示）
	Failed bool      `json:"failed,omitempty"` // 调用失败或被拒
}

// ExecTraceSummary 是一轮执行的过程汇总与精简事件时间线。
type ExecTraceSummary struct {
	Tools    int              `json:"tools,omitempty"`
	Commands int              `json:"commands,omitempty"`
	Skills   int              `json:"skills,omitempty"`
	MCP      int              `json:"mcp,omitempty"`
	Web      int              `json:"web,omitempty"`
	Events   []ExecTraceEvent `json:"events,omitempty"`
}

// ExecTrace 是本机保存的一轮执行过程锚点与摘要。
type ExecTrace struct {
	ExecID         string           `json:"execId"`
	Agent          string           `json:"agent,omitempty"`
	SessionID      string           `json:"sessionId,omitempty"`
	TranscriptPath string           `json:"-"` // 不直接暴露本机路径给手机端
	StartOffset    int64            `json:"-"`
	EndOffset      int64            `json:"-"`
	Summary        ExecTraceSummary `json:"summary"`
	EventCount     int              `json:"eventCount"`
	Finalized      bool             `json:"finalized"`
	UpdatedAt      time.Time        `json:"updatedAt,omitzero"`
}

// SaveExecTraceAnchor 在轮次开始时记下会话文件与起始偏移量；已有记录时只在路径为空或首次记录时写入。
func (s *Store) SaveExecTraceAnchor(execID, agentName, sessionID, path string, startOffset int64, at time.Time) error {
	if execID == "" {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO disc_exec_trace(exec_id,agent,session_id,transcript_path,start_offset,updated_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(exec_id) DO UPDATE SET
			agent=CASE WHEN disc_exec_trace.agent='' THEN excluded.agent ELSE disc_exec_trace.agent END,
			session_id=CASE WHEN disc_exec_trace.session_id='' THEN excluded.session_id ELSE disc_exec_trace.session_id END,
			transcript_path=CASE WHEN disc_exec_trace.transcript_path='' THEN excluded.transcript_path ELSE disc_exec_trace.transcript_path END,
			updated_at=excluded.updated_at`,
		execID, agentName, sessionID, path, startOffset, ms(at))
	return err
}

// FinalizeExecTrace 保存本轮结束时的偏移量与精简过程摘要。
func (s *Store) FinalizeExecTrace(execID, agentName, sessionID, path string, endOffset int64, sum ExecTraceSummary, count int, at time.Time) error {
	if execID == "" {
		return nil
	}
	b, _ := json.Marshal(sum)
	_, err := s.db.Exec(`INSERT INTO disc_exec_trace(exec_id,agent,session_id,transcript_path,end_offset,summary_json,event_count,finalized,updated_at)
		VALUES(?,?,?,?,?,?,?,1,?)
		ON CONFLICT(exec_id) DO UPDATE SET
			agent=CASE WHEN excluded.agent!='' THEN excluded.agent ELSE disc_exec_trace.agent END,
			session_id=CASE WHEN excluded.session_id!='' THEN excluded.session_id ELSE disc_exec_trace.session_id END,
			transcript_path=CASE WHEN excluded.transcript_path!='' THEN excluded.transcript_path ELSE disc_exec_trace.transcript_path END,
			end_offset=MAX(disc_exec_trace.end_offset, excluded.end_offset),
			summary_json=excluded.summary_json,
			event_count=excluded.event_count,
			finalized=1,
			updated_at=excluded.updated_at`,
		execID, agentName, sessionID, path, endOffset, string(b), count, ms(at))
	return err
}

// ExecTrace 读一轮执行的本机过程记录。
func (s *Store) ExecTrace(execID string) (ExecTrace, error) {
	var t ExecTrace
	var raw string
	var fin int
	var updated int64
	err := s.db.QueryRow(`SELECT exec_id,agent,session_id,transcript_path,start_offset,end_offset,summary_json,event_count,finalized,updated_at FROM disc_exec_trace WHERE exec_id=?`, execID).
		Scan(&t.ExecID, &t.Agent, &t.SessionID, &t.TranscriptPath, &t.StartOffset, &t.EndOffset, &raw, &t.EventCount, &fin, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if raw != "" {
		json.Unmarshal([]byte(raw), &t.Summary)
	}
	t.Finalized = fin != 0
	if updated > 0 {
		t.UpdatedAt = fromMS(updated)
	}
	return t, nil
}

// ExecTraceCounts 批量取一批执行已经固化好的过程事件数（未固化或没有记录的不在 map 里）。
func (s *Store) ExecTraceCounts(execIDs []string) map[string]int {
	out := map[string]int{}
	if len(execIDs) == 0 {
		return out
	}
	q := `SELECT exec_id, event_count FROM disc_exec_trace WHERE finalized=1 AND event_count>0 AND exec_id IN (?`
	args := make([]any, len(execIDs))
	args[0] = execIDs[0]
	for i := 1; i < len(execIDs); i++ {
		q += `,?`
		args[i] = execIDs[i]
	}
	rows, err := s.db.Query(q+`)`, args...)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if rows.Scan(&id, &n) == nil {
			out[id] = n
		}
	}
	return out
}
