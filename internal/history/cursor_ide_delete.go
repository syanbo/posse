package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
)

// ErrCursorIDEBusy 是 Cursor 正占用 state.vscdb，这条对话没删成。
var ErrCursorIDEBusy = errors.New("Cursor is using its database; quit Cursor and try again")

var cursorChatID = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// cursor 删除一条对话时清掉的键。只按「前缀 + 这个会话 ID」匹配，避免碰到别的对话。
var cursorIDEExactKeys = []string{
	"composerData:",
	"composerRootWriterDiagnostic:",
	"localAgentMailboxCursor:",
}

var cursorIDEKeyPrefixes = []string{
	"bubbleId:",
	"checkpointId:",
	"codeBlockDiff:",
	"codeBlockPartialInlineDiffFates:",
	"composerConvHeaders:",
	"messageRequestContext:",
	"ofsContent:",
}

// DeleteCursorIDEChat 从 state.vscdb 删掉这一条 IDE 对话的记录。库文件留下，其它对话不动。
// 不能进废纸篓：正文和几百条对话挤在同一个库里。
func DeleteCursorIDEChat(ctx context.Context, home, id string) error {
	if !cursorChatID.MatchString(id) {
		return errors.New("invalid Cursor chat id")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	db, err := openCursorIDEWrite(cursorIDEDatabase(home))
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		if cursorDBBusy(err) {
			return ErrCursorIDEBusy
		}
		return err
	}
	defer tx.Rollback()
	if err := deleteCursorIDEKeys(ctx, tx, id); err != nil {
		if cursorDBBusy(err) {
			return ErrCursorIDEBusy
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		if cursorDBBusy(err) {
			return ErrCursorIDEBusy
		}
		return err
	}
	return nil
}

func openCursorIDEWrite(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	// mode=rw 不新建文件。缺库时创建会留下一个空的 state.vscdb。
	u.RawQuery = "mode=rw&_pragma=busy_timeout(8000)&_txlock=immediate"
	return sql.Open("sqlite", u.String())
}

func deleteCursorIDEKeys(ctx context.Context, tx *sql.Tx, id string) error {
	n := len(cursorIDEExactKeys) + len(cursorIDEKeyPrefixes)
	parts := make([]string, 0, n)
	args := make([]any, 0, n)
	for _, prefix := range cursorIDEExactKeys {
		parts = append(parts, "key = ?")
		args = append(args, prefix+id)
	}
	for _, prefix := range cursorIDEKeyPrefixes {
		parts = append(parts, "key LIKE ?")
		args = append(args, prefix+id+":%")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM cursorDiskKV WHERE `+strings.Join(parts, " OR "), args...); err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM composerHeaders WHERE composerId = ?`, id); err != nil && !strings.Contains(err.Error(), "no such table") {
		return err
	}
	return stripCursorIDEView(ctx, tx, id)
}

// stripCursorIDEView 从工作区面板状态里拿掉这条对话的视图项。不是这种形状的值不动。
func stripCursorIDEView(ctx context.Context, tx *sql.Tx, id string) error {
	view := "workbench.panel.aichat.view." + id
	rows, err := tx.QueryContext(ctx, `SELECT key, value FROM ItemTable WHERE instr(cast(value as text), ?) > 0`, view)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	defer rows.Close()
	type hit struct {
		key   string
		value []byte
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.key, &h.value); err != nil {
			return err
		}
		hits = append(hits, h)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, h := range hits {
		next, ok := dropCursorView(h.value, view)
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ItemTable SET value = ? WHERE key = ?`, next, h.key); err != nil {
			return err
		}
	}
	return nil
}

func dropCursorView(value []byte, view string) ([]byte, bool) {
	var rows []json.RawMessage
	if json.Unmarshal(value, &rows) != nil {
		return nil, false
	}
	out := make([]json.RawMessage, 0, len(rows))
	dropped := false
	for _, raw := range rows {
		var row struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &row) == nil && row.ID == view {
			dropped = true
			continue
		}
		out = append(out, raw)
	}
	if !dropped {
		return nil, false
	}
	next, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return next, true
}

func cursorDBBusy(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SQLITE_BUSY") || strings.Contains(s, "SQLITE_LOCKED") || strings.Contains(s, "database is locked")
}
