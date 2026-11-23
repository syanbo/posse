package store

import "context"

const antigravitySchema = `CREATE TABLE IF NOT EXISTS antigravity_session_dirs (
	session_id TEXT PRIMARY KEY,
	cwd TEXT NOT NULL
);`

const antigravityTerminalDirs = `SELECT t.session_id, MIN(t.cwd) AS cwd FROM terminals t
	JOIN workspaces w ON w.id=t.workspace_id
	WHERE t.agent='antigravity' AND t.session_id<>'' AND t.cwd GLOB '/*'
		AND NOT EXISTS (SELECT 1 FROM antigravity_session_dirs a WHERE a.session_id=t.session_id)
	GROUP BY t.session_id HAVING COUNT(DISTINCT t.cwd)=1`

// RememberAntigravityDir 保留最早确认的目录，续接到其他目录时不改变旧会话的归属。
func (s *Store) RememberAntigravityDir(id, cwd string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO antigravity_session_dirs(session_id,cwd) VALUES(?,?)`, id, cwd)
	return err
}

func (s *Store) AntigravitySessionDirs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,cwd FROM antigravity_session_dirs UNION ALL `+antigravityTerminalDirs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dirs := make(map[string]string)
	for rows.Next() {
		var id, dir string
		if err := rows.Scan(&id, &dir); err != nil {
			return nil, err
		}
		dirs[id] = dir
	}
	return dirs, rows.Err()
}
