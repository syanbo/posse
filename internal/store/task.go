package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Task 是派给一个 worker agent 的任务。派发方（coordinator）是另一个 agent 的终端，
// 为空表示由用户在界面或 posse 终端之外派发。
type Task struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Spec        string `json:"spec"`
	Coordinator string `json:"coordinator,omitempty"`
	WorkspaceID string `json:"workspaceId"`
	Worker      string `json:"worker"`
	Agent       string `json:"agent,omitempty"`
	Model       string `json:"model,omitempty"` // 模型和思考深度，为空是 agent 自己的默认设置
	Effort      string `json:"effort,omitempty"`
	Group       string `json:"group,omitempty"` // 同一次派发给多个 agent 的任务共用
	// DiscussionID 是讨论成员专用 worker 的任务所属的讨论（roomKey），普通任务为空。
	// 不放进 Coordinator：那是派发方终端，讨论任务的派发方是用户
	DiscussionID string     `json:"discussionId,omitempty"`
	Status       string     `json:"status"` // running | done | failed | stopped | exited | lost
	Summary      string     `json:"summary,omitempty"`
	Report       string     `json:"report,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	EndedAt      *time.Time `json:"endedAt,omitempty"`
}

// Message 是任务双方之间的一条消息。question、done、exit 发给派发方，answer、note 发给 worker。
type Message struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"taskId"`
	Kind      string     `json:"kind"`
	Body      string     `json:"body"`
	ReplyTo   string     `json:"replyTo,omitempty"`
	Outcome   string     `json:"outcome,omitempty"` // done 消息汇报时的结果（done、failed）：任务重新打开后它的状态会变
	CreatedAt time.Time  `json:"createdAt"`
	ReadAt    *time.Time `json:"readAt,omitempty"`
}

const (
	TaskRunning = "running"
	TaskDone    = "done"
	TaskFailed  = "failed"
	TaskStopped = "stopped"
	TaskExited  = "exited"
	TaskLost    = "lost"
)

// ToCoordinator 判断这类消息的收件方是不是派发方。
func ToCoordinator(kind string) bool {
	return kind == "question" || kind == "done" || kind == "exit"
}

const taskSchema = `
CREATE TABLE IF NOT EXISTS tasks (
	id TEXT PRIMARY KEY,
	title TEXT NOT NULL,
	spec TEXT NOT NULL,
	coordinator TEXT NOT NULL DEFAULT '',
	workspace_id TEXT NOT NULL,
	worker TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT '',
	model TEXT NOT NULL DEFAULT '',
	effort TEXT NOT NULL DEFAULT '',
	grp TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	summary TEXT NOT NULL DEFAULT '',
	report TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	ended_at INTEGER
);
CREATE INDEX IF NOT EXISTS tasks_coordinator ON tasks(coordinator);
CREATE INDEX IF NOT EXISTS tasks_worker ON tasks(worker);
CREATE TABLE IF NOT EXISTS messages (
	id TEXT PRIMARY KEY,
	task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	body TEXT NOT NULL,
	reply_to TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	read_at INTEGER,
	nudged_at INTEGER
);
CREATE INDEX IF NOT EXISTS messages_task ON messages(task_id);
CREATE INDEX IF NOT EXISTS messages_reply ON messages(reply_to);
`

const taskCols = `id,title,spec,coordinator,workspace_id,worker,agent,model,effort,grp,status,summary,report,created_at,ended_at,discussion_id`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var created int64
	var ended sql.NullInt64
	err := row.Scan(&t.ID, &t.Title, &t.Spec, &t.Coordinator, &t.WorkspaceID, &t.Worker, &t.Agent, &t.Model, &t.Effort, &t.Group,
		&t.Status, &t.Summary, &t.Report, &created, &ended, &t.DiscussionID)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.CreatedAt = fromMS(created)
	if ended.Valid {
		at := fromMS(ended.Int64)
		t.EndedAt = &at
	}
	return t, err
}

func (s *Store) AddTask(t Task) error { return addTask(s.db, t) }

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func addTask(db execer, t Task) error {
	_, err := db.Exec(`INSERT INTO tasks(`+taskCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?)`,
		t.ID, t.Title, t.Spec, t.Coordinator, t.WorkspaceID, t.Worker, t.Agent, t.Model, t.Effort, t.Group, t.Status, t.Summary, t.Report, ms(t.CreatedAt), t.DiscussionID)
	return err
}

func (s *Store) Task(id string) (Task, error) {
	return scanTask(s.db.QueryRow(`SELECT `+taskCols+` FROM tasks WHERE id=?`, id))
}

// TaskFilter 为空的字段不参与过滤。
type TaskFilter struct {
	Coordinator *string
	Worker      string
	Running     bool
}

func (s *Store) Tasks(f TaskFilter) ([]Task, error) {
	var where []string
	var args []any
	if f.Coordinator != nil {
		where = append(where, "coordinator=?")
		args = append(args, *f.Coordinator)
	}
	if f.Worker != "" {
		where = append(where, "worker=?")
		args = append(args, f.Worker)
	}
	if f.Running {
		where = append(where, "status='running'")
	}
	q := `SELECT ` + taskCols + ` FROM tasks`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	rows, err := s.db.Query(q+" ORDER BY created_at", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// FinishTask 只结束仍在运行的任务，返回是否真的改了状态；重复汇报因此不会覆盖第一次的结果。
func (s *Store) FinishTask(id, status, summary, report string, at time.Time) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET status=?, summary=?, report=?, ended_at=? WHERE id=? AND status='running'`,
		status, summary, report, ms(at), id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ReopenTask 让已汇报的任务回到运行中，接着在原来的 worker 上做追加的活；返回是否真的改了状态。
func (s *Store) ReopenTask(id string) (bool, error) {
	res, err := s.db.Exec(`UPDATE tasks SET status='running', ended_at=NULL WHERE id=? AND status IN ('done','failed')`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkRunningTasksLost 在常驻进程启动时调用：worker 终端已随上一个进程结束，交接过来的 worker 除外。
func (s *Store) MarkRunningTasksLost(at time.Time, keepWorkers []string) error {
	q, args := notIn("worker", keepWorkers)
	_, err := s.db.Exec(`UPDATE tasks SET status='lost', ended_at=? WHERE status='running'`+q, append([]any{ms(at)}, args...)...)
	return err
}

// DeleteEndedTasks 清掉已结束的任务及其消息。讨论成员当前绑定的任务两轮之间是 done/failed，
// 还要接着复用，跳过；讨论历史另存在讨论表里，不随任务删掉。
func (s *Store) DeleteEndedTasks() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tasks WHERE status!='running' AND id NOT IN (SELECT task_id FROM disc_members WHERE task_id!='')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- messages

const messageCols = `id,task_id,kind,body,reply_to,outcome,created_at,read_at`

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var created int64
	var read sql.NullInt64
	err := row.Scan(&m.ID, &m.TaskID, &m.Kind, &m.Body, &m.ReplyTo, &m.Outcome, &created, &read)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	m.CreatedAt = fromMS(created)
	if read.Valid {
		at := fromMS(read.Int64)
		m.ReadAt = &at
	}
	return m, err
}

func (s *Store) queryMessages(q string, args ...any) ([]Message, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddMessage(m Message) error { return addMessage(s.db, m) }

func addMessage(db execer, m Message) error {
	_, err := db.Exec(`INSERT INTO messages(id,task_id,kind,body,reply_to,outcome,created_at) VALUES(?,?,?,?,?,?,?)`,
		m.ID, m.TaskID, m.Kind, m.Body, m.ReplyTo, m.Outcome, ms(m.CreatedAt))
	return err
}

func (s *Store) Message(id string) (Message, error) {
	return scanMessage(s.db.QueryRow(`SELECT `+messageCols+` FROM messages WHERE id=?`, id))
}

func (s *Store) Messages(taskID string) ([]Message, error) {
	return s.queryMessages(`SELECT `+messageCols+` FROM messages WHERE task_id=? ORDER BY created_at, rowid`, taskID)
}

// Answer 返回对某个问题的回复。
func (s *Store) Answer(questionID string) (Message, error) {
	return scanMessage(s.db.QueryRow(`SELECT `+messageCols+` FROM messages WHERE reply_to=? AND kind='answer' ORDER BY created_at LIMIT 1`, questionID))
}

// Inbox 选出一个收件方的消息：派发方按 coordinator 匹配，worker 按任务 ID 匹配。
type Inbox struct {
	Coordinator *string
	TaskID      string // worker 的收件箱
	Unread      bool
	Unnudged    bool
}

func (s *Store) Inbox(in Inbox) ([]Message, error) {
	q := `SELECT m.id,m.task_id,m.kind,m.body,m.reply_to,m.outcome,m.created_at,m.read_at FROM messages m JOIN tasks t ON t.id=m.task_id WHERE `
	var args []any
	if in.Coordinator != nil {
		q += `t.coordinator=? AND m.kind IN ('question','done','exit')`
		args = append(args, *in.Coordinator)
	} else {
		q += `t.id=? AND m.kind IN ('answer','note')`
		args = append(args, in.TaskID)
	}
	if in.Unread {
		q += ` AND m.read_at IS NULL`
	}
	if in.Unnudged {
		q += ` AND m.nudged_at IS NULL`
	}
	return s.queryMessages(q+` ORDER BY m.created_at, m.rowid`, args...)
}

func (s *Store) markMessages(col string, ids []string, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{ms(at)}
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.Exec(`UPDATE messages SET `+col+`=? WHERE `+col+` IS NULL AND id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

func (s *Store) MarkRead(ids []string, at time.Time) error { return s.markMessages("read_at", ids, at) }
func (s *Store) MarkNudged(ids []string, at time.Time) error {
	return s.markMessages("nudged_at", ids, at)
}

// UnmarkNudged 把交给 agent 却没被处理的提醒退回去，之后再提醒一次。
func (s *Store) UnmarkNudged(ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	_, err := s.db.Exec(`UPDATE messages SET nudged_at=NULL WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	return err
}

// OpenQuestions 统计每个任务里还没回复的问题数。
func (s *Store) OpenQuestions() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT task_id, COUNT(*) FROM messages q WHERE kind='question'
		AND NOT EXISTS (SELECT 1 FROM messages a WHERE a.reply_to=q.id AND a.kind='answer') GROUP BY task_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
