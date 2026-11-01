package store

import (
	"strings"
	"time"
)

// Trace 是 agent 在工作区外留下的一处文件或目录（痕迹账本），同一路径只记第一次。
type Trace struct {
	Path        string    `json:"path"`
	Kind        string    `json:"kind"`           // created | modified | config | worktree
	Repo        string    `json:"repo,omitempty"` // worktree 所属仓库，删除时走 git worktree remove
	ProjectID   string    `json:"projectId"`
	WorkspaceID string    `json:"workspaceId"`
	TerminalID  string    `json:"terminalId"`
	Agent       string    `json:"agent,omitempty"`
	SessionID   string    `json:"sessionId,omitempty"`
	Source      string    `json:"source"` // tool | shell | git
	At          time.Time `json:"at"`
}

const traceSchema = `
CREATE TABLE IF NOT EXISTS traces (
	path TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	repo TEXT NOT NULL DEFAULT '',
	project_id TEXT NOT NULL,
	workspace_id TEXT NOT NULL,
	terminal_id TEXT NOT NULL,
	agent TEXT NOT NULL DEFAULT '',
	session_id TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL,
	at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS traces_workspace ON traces(workspace_id);
CREATE INDEX IF NOT EXISTS traces_project ON traces(project_id);
`

// AddTrace 记一条痕迹；路径已经记过时不动（保留最早留下它的会话）。
// 已记的新建目录里再写的文件不另记：调用方先按 TraceCovering 查过。
func (s *Store) AddTrace(t Trace) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO traces(path,kind,repo,project_id,workspace_id,terminal_id,agent,session_id,source,at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, t.Path, t.Kind, t.Repo, t.ProjectID, t.WorkspaceID, t.TerminalID, t.Agent, t.SessionID, t.Source, ms(t.At))
	return err
}

// TraceCovering 报告 path 本身或它的某个上级目录已经记成新建或 worktree。
func (s *Store) TraceCovering(path string) bool {
	var n int
	cands := []string{path}
	for p := path; ; {
		parent := parentDir(p)
		if parent == p {
			break
		}
		cands = append(cands, parent)
		p = parent
	}
	q := `SELECT count(*) FROM traces WHERE kind IN ('created','worktree') AND path IN (?` + strings.Repeat(",?", len(cands)-1) + `)`
	args := make([]any, len(cands))
	for i, c := range cands {
		args[i] = c
	}
	s.db.QueryRow(q, args...).Scan(&n)
	return n > 0
}

func parentDir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return "/"
	}
	return p[:i]
}

// Traces 按工作区或项目列出痕迹（都为空时列出全部），新的在前。
func (s *Store) Traces(workspaceID, projectID string) ([]Trace, error) {
	q := `SELECT path,kind,repo,project_id,workspace_id,terminal_id,agent,session_id,source,at FROM traces`
	var args []any
	switch {
	case workspaceID != "":
		q += ` WHERE workspace_id=?`
		args = append(args, workspaceID)
	case projectID != "":
		q += ` WHERE project_id=?`
		args = append(args, projectID)
	}
	rows, err := s.db.Query(q+` ORDER BY at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Trace
	for rows.Next() {
		var t Trace
		var at int64
		if err := rows.Scan(&t.Path, &t.Kind, &t.Repo, &t.ProjectID, &t.WorkspaceID, &t.TerminalID, &t.Agent, &t.SessionID, &t.Source, &at); err != nil {
			return nil, err
		}
		t.At = fromMS(at)
		out = append(out, t)
	}
	return out, rows.Err()
}

// ForgetTraces 删掉这些路径的记录（文件已经清掉，或用户不想再看到）。
func (s *Store) ForgetTraces(paths []string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range paths {
		if _, err := tx.Exec(`DELETE FROM traces WHERE path=?`, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}
