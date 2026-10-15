package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Task 是派给一个 worker agent 的任务。派发方（coordinator）是另一个 agent 的终端，
// 为空表示由用户在界面或 hive 终端之外派发。
type Task struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Spec        string     `json:"spec"`
	Coordinator string     `json:"coordinator,omitempty"`
	WorkspaceID string     `json:"workspaceId"`
	Worker      string     `json:"worker"`
	Agent       string     `json:"agent,omitempty"`
	Group       string     `json:"group,omitempty"` // 同一次派发给多个 agent 的任务共用
	Status      string     `json:"status"`          // running | done | failed | stopped | exited | lost
	Summary     string     `json:"summary,omitempty"`
	Report      string     `json:"report,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	EndedAt     *time.Time `json:"endedAt,omitempty"`
}

// Message 是任务双方之间的一条消息。question、done、exit 发给派发方，answer、note 发给 worker。
type Message struct {
	ID        string     `json:"id"`
	TaskID    string     `json:"taskId"`
	Kind      string     `json:"kind"`
	Body      string     `json:"body"`
	ReplyTo   string     `json:"replyTo,omitempty"`
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

const taskCols = `id,title,spec,coordinator,workspace_id,worker,agent,grp,status,summary,report,created_at,ended_at`

func scanTask(row interface{ Scan(...any) error }) (Task, error) {
	var t Task
	var created int64
	var ended sql.NullInt64
	err := row.Scan(&t.ID, &t.Title, &t.Spec, &t.Coordinator, &t.WorkspaceID, &t.Worker, &t.Agent, &t.Group,
		&t.Status, &t.Summary, &t.Report, &created, &ended)
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

func (s *Store) AddTask(t Task) error {
	_, err := s.db.Exec(`INSERT INTO tasks(`+taskCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,NULL)`,
		t.ID, t.Title, t.Spec, t.Coordinator, t.WorkspaceID, t.Worker, t.Agent, t.Group, t.Status, t.Summary, t.Report, ms(t.CreatedAt))
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

// MarkRunningTasksLost 在常驻进程启动时调用：worker 终端已随上一个进程结束。
func (s *Store) MarkRunningTasksLost(at time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET status='lost', ended_at=? WHERE status='running'`, ms(at))
	return err
}

// DeleteEndedTasks 清掉已结束的任务及其消息。
func (s *Store) DeleteEndedTasks() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tasks WHERE status!='running'`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// --- messages

const messageCols = `id,task_id,kind,body,reply_to,created_at,read_at`

func scanMessage(row interface{ Scan(...any) error }) (Message, error) {
	var m Message
	var created int64
	var read sql.NullInt64
	err := row.Scan(&m.ID, &m.TaskID, &m.Kind, &m.Body, &m.ReplyTo, &created, &read)
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

func (s *Store) AddMessage(m Message) error {
	_, err := s.db.Exec(`INSERT INTO messages(id,task_id,kind,body,reply_to,created_at) VALUES(?,?,?,?,?,?)`,
		m.ID, m.TaskID, m.Kind, m.Body, m.ReplyTo, ms(m.CreatedAt))
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
	q := `SELECT m.id,m.task_id,m.kind,m.body,m.reply_to,m.created_at,m.read_at FROM messages m JOIN tasks t ON t.id=m.task_id WHERE `
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
