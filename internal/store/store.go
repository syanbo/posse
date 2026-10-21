// Package store 持久化项目、工作区和终端记录。终端的运行态在内存里，
// 这里只留元数据，常驻进程重启后据此标记失联的终端。
package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct{ db *sql.DB }

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Kind        string    `json:"kind"` // folder | git
	SetupScript string    `json:"setupScript,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

type Workspace struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"projectId"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Kind      string    `json:"kind"` // main | folder | worktree
	Branch    string    `json:"branch,omitempty"`
	BaseRef   string    `json:"baseRef,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type Terminal struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	Title       string     `json:"title,omitempty"` // 用户给标签起的名字，空是默认（会话标题或终端标题）
	Command     []string   `json:"command"`
	Agent       string     `json:"agent,omitempty"`
	State       string     `json:"state"` // running | exited | lost
	ExitCode    *int       `json:"exitCode,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	EndedAt     *time.Time `json:"endedAt,omitempty"`
}

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	path TEXT NOT NULL UNIQUE,
	kind TEXT NOT NULL,
	setup_script TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS workspaces (
	id TEXT PRIMARY KEY,
	project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	path TEXT NOT NULL,
	kind TEXT NOT NULL,
	branch TEXT NOT NULL DEFAULT '',
	base_ref TEXT NOT NULL DEFAULT '',
	created_at INTEGER NOT NULL,
	UNIQUE(project_id, name)
);
CREATE TABLE IF NOT EXISTS terminals (
	id TEXT PRIMARY KEY,
	workspace_id TEXT NOT NULL,
	title TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	exit_code INTEGER,
	created_at INTEGER NOT NULL,
	ended_at INTEGER
);
CREATE INDEX IF NOT EXISTS terminals_workspace ON terminals(workspace_id);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=cache_size(-2000)")
	if err != nil {
		return nil, err
	}
	// 单写者足够，也避免 SQLITE_BUSY
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema + taskSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func NewID(prefix string) string {
	b := make([]byte, 4)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v) }

// --- projects

func (s *Store) AddProject(p Project) error {
	_, err := s.db.Exec(`INSERT INTO projects(id,name,path,kind,setup_script,created_at) VALUES(?,?,?,?,?,?)`,
		p.ID, p.Name, p.Path, p.Kind, p.SetupScript, ms(p.CreatedAt))
	return err
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(`UPDATE projects SET name=?, kind=?, setup_script=? WHERE id=?`, p.Name, p.Kind, p.SetupScript, p.ID)
	return err
}

func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var created int64
	err := row.Scan(&p.ID, &p.Name, &p.Path, &p.Kind, &p.SetupScript, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt = fromMS(created)
	return p, err
}

const projectCols = `id,name,path,kind,setup_script,created_at`

func (s *Store) Projects() ([]Project, error) {
	rows, err := s.db.Query(`SELECT ` + projectCols + ` FROM projects ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// FindProject 按 id、名称或路径查找。
func (s *Store) FindProject(sel string) (Project, error) {
	return scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=? OR name=? OR path=? LIMIT 1`, sel, sel, sel))
}

// --- workspaces

func (s *Store) AddWorkspace(w Workspace) error {
	_, err := s.db.Exec(`INSERT INTO workspaces(id,project_id,name,path,kind,branch,base_ref,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		w.ID, w.ProjectID, w.Name, w.Path, w.Kind, w.Branch, w.BaseRef, ms(w.CreatedAt))
	return err
}

func (s *Store) DeleteWorkspace(id string) error {
	_, err := s.db.Exec(`DELETE FROM workspaces WHERE id=?`, id)
	return err
}

const workspaceCols = `id,project_id,name,path,kind,branch,base_ref,created_at`

func scanWorkspace(row interface{ Scan(...any) error }) (Workspace, error) {
	var w Workspace
	var created int64
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Path, &w.Kind, &w.Branch, &w.BaseRef, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	w.CreatedAt = fromMS(created)
	return w, err
}

func (s *Store) Workspaces(projectID string) ([]Workspace, error) {
	q := `SELECT ` + workspaceCols + ` FROM workspaces`
	var args []any
	if projectID != "" {
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.db.Query(q+` ORDER BY created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// WorkspaceCounts 返回每个项目的工作区数量，没有工作区的项目不在其中。
func (s *Store) WorkspaceCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT project_id, COUNT(*) FROM workspaces GROUP BY project_id`)
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

func (s *Store) Workspace(id string) (Workspace, error) {
	return scanWorkspace(s.db.QueryRow(`SELECT `+workspaceCols+` FROM workspaces WHERE id=?`, id))
}

// FindWorkspaces 按 id、名称或 "项目/名称" 查找，名称可能重名所以返回多个。
func (s *Store) FindWorkspaces(sel string) ([]Workspace, error) {
	rows, err := s.db.Query(`SELECT w.id,w.project_id,w.name,w.path,w.kind,w.branch,w.base_ref,w.created_at
		FROM workspaces w JOIN projects p ON p.id=w.project_id
		WHERE w.id=? OR w.name=? OR p.name||'/'||w.name=? ORDER BY w.created_at`, sel, sel, sel)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// --- terminals

func (s *Store) AddTerminal(t Terminal) error {
	cmd, _ := json.Marshal(t.Command)
	_, err := s.db.Exec(`INSERT INTO terminals(id,workspace_id,title,command,agent,state,created_at) VALUES(?,?,?,?,?,?,?)`,
		t.ID, t.WorkspaceID, t.Title, string(cmd), t.Agent, t.State, ms(t.CreatedAt))
	return err
}

func (s *Store) SetTerminalTitle(id, title string) error {
	_, err := s.db.Exec(`UPDATE terminals SET title=? WHERE id=?`, title, id)
	return err
}

func (s *Store) TerminalTitle(id string) (string, error) {
	var title string
	err := s.db.QueryRow(`SELECT title FROM terminals WHERE id=?`, id).Scan(&title)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return title, err
}

func (s *Store) EndTerminal(id, state string, code *int, at time.Time) error {
	_, err := s.db.Exec(`UPDATE terminals SET state=?, exit_code=?, ended_at=? WHERE id=?`, state, code, ms(at), id)
	return err
}

// MarkRunningLost 在常驻进程启动时调用：上一个进程托管的终端都已随之结束，keep 里的是交接过来的、还在运行。
func (s *Store) MarkRunningLost(at time.Time, keep []string) (int64, error) {
	q, args := notIn("id", keep)
	res, err := s.db.Exec(`UPDATE terminals SET state='lost', ended_at=? WHERE state='running'`+q, append([]any{ms(at)}, args...)...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneTerminals 删除所有已结束的终端记录；没有地方读历史记录，不清理表会一直增长。
func (s *Store) PruneTerminals() error {
	_, err := s.db.Exec(`DELETE FROM terminals WHERE state!='running'`)
	return err
}

func (s *Store) DeleteEndedTerminals(workspaceID string) error {
	_, err := s.db.Exec(`DELETE FROM terminals WHERE workspace_id=? AND state!='running'`, workspaceID)
	return err
}

// notIn 生成 " AND col NOT IN (?,...)" 和参数；ids 为空时什么都不加。
func notIn(col string, ids []string) (string, []any) {
	if len(ids) == 0 {
		return "", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return " AND " + col + " NOT IN (?" + strings.Repeat(",?", len(ids)-1) + ")", args
}
