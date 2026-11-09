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
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
	// self 是本机安装身份的指纹（局域网协作分本机和别人的 Agent）；首次建讨论、加入时才生成，所以会在运行中设上
	self atomic.Pointer[string]
}

// SetSelf 记下本机安装身份；没有身份（还没建过讨论）时为空。
func (s *Store) SetSelf(fp string) { s.self.Store(&fp) }

// Self 是 SetSelf 记下的本机身份。
func (s *Store) Self() string {
	if p := s.self.Load(); p != nil {
		return *p
	}
	return ""
}

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Kind        string    `json:"kind"` // folder | git
	SetupScript string    `json:"setupScript,omitempty"`
	SingleRepo  bool      `json:"singleRepo,omitempty"` // 关掉"识别多个 Git 仓库"，只认项目目录所在的仓库；存成关掉，旧常驻进程不带这个字段时照默认的开着
	CreatedAt   time.Time `json:"createdAt"`
}

type Workspace struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Kind      string `json:"kind"` // main | folder | worktree
	Branch    string `json:"branch,omitempty"`
	BaseRef   string `json:"baseRef,omitempty"`
	// Repos 是多仓库目录上的 worktree 工作区里的各个仓库；这时 Branch 是各仓库共用的分支，BaseRef 为空
	Repos     []WorkspaceRepo `json:"repos,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// WorkspaceRepo 是多仓库 worktree 工作区里的一个仓库。
type WorkspaceRepo struct {
	Path   string `json:"path"`   // 在工作区（也是在项目目录）里的相对路径，斜杠分隔
	Source string `json:"source"` // 项目里那个仓库的根目录（解析过软链），git worktree 命令在这里执行
	Base   string `json:"base"`   // 建 worktree 时从哪个分支开出来，合并时合回这里
}

type Terminal struct {
	ID          string     `json:"id"`
	WorkspaceID string     `json:"workspaceId"`
	Title       string     `json:"title,omitempty"` // 用户给标签起的名字，空是默认（会话标题或终端标题）
	Command     []string   `json:"command"`
	Agent       string     `json:"agent,omitempty"`
	SessionID   string     `json:"sessionId,omitempty"` // agent 的会话，重启后按它续接
	Cwd         string     `json:"cwd,omitempty"`       // 前台程序最后所在的目录，重启后在这里恢复
	Label       string     `json:"label,omitempty"`     // 最后显示的会话名或终端标题，重启后还没恢复时照它显示
	State       string     `json:"state"`               // running | exited | lost（上一个常驻进程留下、等着恢复）
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
	if _, err := db.Exec(schema + taskSchema + traceSchema + discussionSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumn(db, "workspaces", "repos", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumn(db, "projects", "single_repo", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	for _, c := range [][2]string{{"tasks", "model"}, {"tasks", "effort"}, {"tasks", "discussion_id"}, {"terminals", "session_id"}, {"terminals", "cwd"}, {"terminals", "label"}, {"messages", "outcome"}} {
		if err := addColumn(db, c[0], c[1], "TEXT NOT NULL DEFAULT ''"); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	for _, c := range [][3]string{
		{"discussions", "mirror", "INTEGER NOT NULL DEFAULT 0"}, {"discussions", "shared", "INTEGER NOT NULL DEFAULT 0"},
		{"disc_members", "owner", "TEXT NOT NULL DEFAULT ''"}, {"disc_members", "generation", "INTEGER NOT NULL DEFAULT 0"},
		{"disc_messages", "author_id", "TEXT NOT NULL DEFAULT ''"}, {"disc_messages", "mention", "TEXT NOT NULL DEFAULT ''"},
		{"disc_messages", "mention_name", "TEXT NOT NULL DEFAULT ''"}, {"disc_messages", "key_by", "TEXT NOT NULL DEFAULT ''"},
		{"disc_executions", "owner", "TEXT NOT NULL DEFAULT ''"}, {"disc_executions", "generation", "INTEGER NOT NULL DEFAULT 0"},
		{"disc_members", "own_config", "TEXT NOT NULL DEFAULT ''"}, {"disc_members", "access", "TEXT NOT NULL DEFAULT ''"}, {"disc_members", "client_id", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := addColumn(db, c[0], c[1], c[2]); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	// 回答的唯一约束改成讨论内唯一（加入者的问题 ID 来自别的电脑），新索引在 collabSchema 里
	if _, err := db.Exec(`DROP INDEX IF EXISTS disc_messages_answer;` + collabSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	for _, c := range [][3]string{{"collab_intents", "config", "TEXT NOT NULL DEFAULT ''"}, {"collab_joined", "att_seq", "INTEGER NOT NULL DEFAULT 0"},
		{"collab_joined", "profile_sent", "INTEGER NOT NULL DEFAULT 0"}, {"collab_people", "avatar", "TEXT NOT NULL DEFAULT ''"},
		{"collab_people", "profile_rev", "INTEGER NOT NULL DEFAULT 0"}} {
		if err := addColumn(db, c[0], c[1], c[2]); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate: %w", err)
		}
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// addColumn 给旧库补上后来加的列；CREATE TABLE IF NOT EXISTS 不会改已有的表。
func addColumn(db *sql.DB, table, column, decl string) error {
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&n); err != nil || n > 0 {
		return err
	}
	_, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + decl)
	return err
}

func NewID(prefix string) string {
	b := make([]byte, 4)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func ms(t time.Time) int64     { return t.UnixMilli() }
func fromMS(v int64) time.Time { return time.UnixMilli(v) }

// --- projects

func (s *Store) AddProject(p Project) error {
	_, err := s.db.Exec(`INSERT INTO projects(id,name,path,kind,setup_script,single_repo,created_at) VALUES(?,?,?,?,?,?,?)`,
		p.ID, p.Name, p.Path, p.Kind, p.SetupScript, p.SingleRepo, ms(p.CreatedAt))
	return err
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(`UPDATE projects SET name=?, kind=?, setup_script=?, single_repo=? WHERE id=?`, p.Name, p.Kind, p.SetupScript, p.SingleRepo, p.ID)
	return err
}

// SetProjectPath 改项目目录（目录在外面挪了地方，重新指定）。
func (s *Store) SetProjectPath(id, path string) error {
	_, err := s.db.Exec(`UPDATE projects SET path=? WHERE id=?`, path, id)
	return err
}

func (s *Store) DeleteProject(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id=?`, id)
	return err
}

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var created int64
	err := row.Scan(&p.ID, &p.Name, &p.Path, &p.Kind, &p.SetupScript, &p.SingleRepo, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	p.CreatedAt = fromMS(created)
	return p, err
}

const projectCols = `id,name,path,kind,setup_script,single_repo,created_at`

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
	var repos []byte
	if len(w.Repos) > 0 {
		repos, _ = json.Marshal(w.Repos)
	}
	_, err := s.db.Exec(`INSERT INTO workspaces(id,project_id,name,path,kind,branch,base_ref,repos,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		w.ID, w.ProjectID, w.Name, w.Path, w.Kind, w.Branch, w.BaseRef, string(repos), ms(w.CreatedAt))
	return err
}

// SetWorkspacePath 改工作区的目录和分支（目录在外面挪了地方，重新指定）。
func (s *Store) SetWorkspacePath(id, path, branch string) error {
	_, err := s.db.Exec(`UPDATE workspaces SET path=?, branch=? WHERE id=?`, path, branch, id)
	return err
}

func (s *Store) DeleteWorkspace(id string) error {
	_, err := s.db.Exec(`DELETE FROM workspaces WHERE id=?`, id)
	return err
}

const workspaceCols = `id,project_id,name,path,kind,branch,base_ref,repos,created_at`

func scanWorkspace(row interface{ Scan(...any) error }) (Workspace, error) {
	var w Workspace
	var created int64
	var repos string
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &w.Path, &w.Kind, &w.Branch, &w.BaseRef, &repos, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return w, ErrNotFound
	}
	if repos != "" {
		json.Unmarshal([]byte(repos), &w.Repos)
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
	rows, err := s.db.Query(`SELECT w.id,w.project_id,w.name,w.path,w.kind,w.branch,w.base_ref,w.repos,w.created_at
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
	_, err := s.db.Exec(`INSERT INTO terminals(id,workspace_id,title,command,agent,session_id,cwd,state,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		t.ID, t.WorkspaceID, t.Title, string(cmd), t.Agent, t.SessionID, t.Cwd, t.State, ms(t.CreatedAt))
	return err
}

// SetTerminalResume 记下重启后恢复要用的 agent、会话和目录，以及恢复前显示的名字。
func (s *Store) SetTerminalResume(id, agent, sessionID, cwd, label string) error {
	_, err := s.db.Exec(`UPDATE terminals SET agent=?, session_id=?, cwd=?, label=? WHERE id=?`, agent, sessionID, cwd, label, id)
	return err
}

// ReviveTerminal 把恢复了的终端改回运行中，名字和创建时间不变。
func (s *Store) ReviveTerminal(id string, command []string, agent, sessionID string) error {
	cmd, _ := json.Marshal(command)
	_, err := s.db.Exec(`UPDATE terminals SET command=?, agent=?, session_id=?, state='running', exit_code=NULL, ended_at=NULL WHERE id=? AND state='lost'`,
		string(cmd), agent, sessionID, id)
	return err
}

// LostTerminals 是等着恢复的终端（工作区还在的），按创建时间排序；workspaceID 为空时是全部。
func (s *Store) LostTerminals(workspaceID string) ([]Terminal, error) {
	if workspaceID == "" {
		return s.lostTerminals("")
	}
	return s.lostTerminals(` AND t.workspace_id=?`, workspaceID)
}

func (s *Store) LostTerminal(id string) (Terminal, error) {
	ts, err := s.lostTerminals(` AND t.id=?`, id)
	if err != nil {
		return Terminal{}, err
	}
	if len(ts) == 0 {
		return Terminal{}, ErrNotFound
	}
	return ts[0], nil
}

func (s *Store) lostTerminals(cond string, args ...any) ([]Terminal, error) {
	rows, err := s.db.Query(`SELECT t.id,t.workspace_id,t.title,t.command,t.agent,t.session_id,t.cwd,t.label,t.created_at FROM terminals t
		JOIN workspaces w ON w.id=t.workspace_id WHERE t.state='lost'`+cond+` ORDER BY t.created_at`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Terminal
	for rows.Next() {
		var t Terminal
		var cmd string
		var at int64
		if err := rows.Scan(&t.ID, &t.WorkspaceID, &t.Title, &cmd, &t.Agent, &t.SessionID, &t.Cwd, &t.Label, &at); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(cmd), &t.Command)
		t.State, t.CreatedAt = "lost", fromMS(at)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteTerminal(id string) error {
	_, err := s.db.Exec(`DELETE FROM terminals WHERE id=?`, id)
	return err
}

func (s *Store) SetTerminalTitle(id, title string) error {
	_, err := s.db.Exec(`UPDATE terminals SET title=? WHERE id=?`, title, id)
	return err
}

// TerminalResume 读终端的名字和恢复信息（交接过来的终端用，两边共用一个数据库）。
func (s *Store) TerminalResume(id string) (Terminal, error) {
	t := Terminal{ID: id}
	err := s.db.QueryRow(`SELECT title,agent,session_id,cwd,label FROM terminals WHERE id=?`, id).Scan(&t.Title, &t.Agent, &t.SessionID, &t.Cwd, &t.Label)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
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

// PruneTerminals 删除已结束的终端记录和工作区已经不在的记录；没有地方读历史记录，不清理表会一直增长。
// 等着恢复的（lost）留着。
func (s *Store) PruneTerminals() error {
	_, err := s.db.Exec(`DELETE FROM terminals WHERE state='exited' OR workspace_id NOT IN (SELECT id FROM workspaces)`)
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
