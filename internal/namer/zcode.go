package namer

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"posse/internal/agent"
	"posse/internal/history"
)

// WriteZCode 沿用原生自定义标题的字段。只更新标题列，让另一个进程里的回合照常写入。
func WriteZCode(ctx context.Context, path, session, title string, foreground int) error {
	if !agent.IsZCodeSession(session) {
		return errors.New("invalid ZCode session ID")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=rw&_txlock=immediate&_pragma=busy_timeout(500)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	softDelete, err := history.ZCodeHasSoftDelete(ctx, tx)
	if err != nil {
		return err
	}
	query := `UPDATE session SET title = ?, title_source = 'custom', time_title_updated = ? WHERE id = ?`
	if softDelete {
		query += ` AND time_deleted IS NULL`
	}
	args := []any{title, time.Now().UnixMilli(), session}
	if foreground > 0 {
		query += ` AND id = (SELECT CASE WHEN count(DISTINCT s.id) = 1 THEN min(s.id) END
			FROM session_runtime_presence AS p JOIN session AS s ON s.id = p.session_id
			WHERE p.process_id = ? AND s.task_type IN ('interactive', 'fork') AND s.time_deleted IS NULL)`
		args = append(args, foreground)
	}
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("ZCode session was not found")
	}
	return tx.Commit()
}
