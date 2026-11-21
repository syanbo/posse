package history

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Cursor 的 Chat / Composer 存在 state.vscdb，不写 agent-transcripts。
// 已经有转录文件的会话以转录为准，这里不另收一份。子会话留给后续的嵌套，不单列。

func cursorIDEDatabase(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")
}

func cursorIDEDir(home string) string {
	return filepath.Join(filepath.Dir(cursorIDEDatabase(home)), "composer")
}

func cursorIDEPath(home, id string) string {
	return filepath.Join(cursorIDEDir(home), id)
}

// updateCursorIDE 收录 state.vscdb 里的 IDE 对话。done 为 false 表示库没读全，已索引的 IDE 对话原样留着；
// redo（解析规则升级）时这样就不能写新版本号。
func (x *Index) updateCursorIDE(ctx context.Context, roots Roots, known map[string]knownFile, sources []source, seen map[string]bool, redo bool) (done bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dbPath := cursorIDEDatabase(roots.Home)
	fi, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		markCursorIDE(known, seen, roots.Home, nil)
		return false, nil
	}
	transcripts := map[string]bool{}
	for _, src := range sources {
		if src.agent == "cursor" {
			transcripts[sessionIDFromPath(src.transcript)] = true
		}
	}
	// WAL 里的新写入不改主库长度。把 -wal / -shm 算进戳，否则 Cursor 开着时索引一直停在上次 checkpoint。
	stamp := cursorDBStamp(dbPath)
	var have string
	err = x.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, "cursor-ide").Scan(&have)
	if err != nil && err != sql.ErrNoRows {
		return false, err
	}
	if have == stamp && !redo {
		markCursorIDE(known, seen, roots.Home, transcripts)
		return true, nil
	}
	db, err := openCursorIDE(dbPath)
	if err != nil {
		markCursorIDE(known, seen, roots.Home, nil)
		return false, nil
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM cursorDiskKV WHERE key LIKE 'composerData:%'`)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		// 还没开过 Composer 的库没有这张表。已索引的 IDE 会话留着，下次再对。
		markCursorIDE(known, seen, roots.Home, nil)
		return false, nil
	}
	defer rows.Close()
	type composer struct {
		id   string
		data cursorComposer
		sum  []byte
	}
	var list []composer
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		// 实际库里有值为 NULL 的 composerData，扫进 string 会报错，按字节扫、解不出来跳过。
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			markCursorIDE(known, seen, roots.Home, nil)
			return false, nil
		}
		id := strings.TrimPrefix(key, "composerData:")
		if id == "" || transcripts[id] {
			continue
		}
		var data cursorComposer
		if json.Unmarshal(value, &data) != nil || len(data.Headers) == 0 || cursorSubagent(data.Subagent) {
			continue
		}
		sum := sha256.Sum256(value)
		list = append(list, composer{id: id, data: data, sum: sum[:]})
	}
	if err := rows.Err(); err != nil {
		if errors.Is(err, context.Canceled) {
			return false, err
		}
		markCursorIDE(known, seen, roots.Home, nil)
		return false, nil
	}
	size, mtime := fi.Size(), fi.ModTime().UnixNano()
	for _, c := range list {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		path := cursorIDEPath(roots.Home, c.id)
		// Cursor 开着时库一直在写，戳每次都变。composerData 没变的对话不重读气泡、不重写索引。
		if k := known[path]; k.id != 0 && !redo && bytes.Equal(k.tail, c.sum) {
			seen[path] = true
			continue
		}
		msgs, st, err := cursorIDEMessages(db, ctx, c.id, c.data)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			// 气泡没读完。不把残缺结果写进去，也不推进戳，已索引的对话留到下次再对。
			markCursorIDE(known, seen, roots.Home, transcripts)
			return false, nil
		}
		if len(msgs) == 0 {
			continue
		}
		st.SessionID = c.id
		if err := x.save(ctx, source{agent: "cursor", transcript: path}, known[path], true, st, msgs, size, mtime, size, c.sum, 0); err != nil {
			return false, err
		}
		seen[path] = true
	}
	_, err = x.db.ExecContext(ctx, `INSERT INTO meta(key,value) VALUES('cursor-ide',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, stamp)
	return err == nil, err
}

func cursorDBStamp(path string) string {
	var b strings.Builder
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		fi, err := os.Stat(p)
		if err != nil {
			b.WriteString("0/0;")
			continue
		}
		b.WriteString(fi.ModTime().UTC().Format(time.RFC3339Nano))
		b.WriteByte('/')
		b.WriteString(strconv.FormatInt(fi.Size(), 10))
		b.WriteByte(';')
	}
	return b.String()
}

func markCursorIDE(known map[string]knownFile, seen map[string]bool, home string, skip map[string]bool) {
	dir := cursorIDEDir(home)
	for path := range known {
		if filepath.Dir(path) != dir || skip[filepath.Base(path)] {
			continue
		}
		seen[path] = true
	}
}

func openCursorIDE(path string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(500)"
	return sql.Open("sqlite", u.String())
}

type cursorComposer struct {
	Name          string          `json:"name"`
	CreatedAt     int64           `json:"createdAt"`
	LastUpdatedAt int64           `json:"lastUpdatedAt"`
	Subagent      json.RawMessage `json:"subagentInfo"`
	Workspace     struct {
		URI struct {
			FsPath string `json:"fsPath"`
		} `json:"uri"`
	} `json:"workspaceIdentifier"`
	Repos []struct {
		RepoPath string `json:"repoPath"`
	} `json:"trackedGitRepos"`
	Headers []struct {
		BubbleID string `json:"bubbleId"`
		Type     int    `json:"type"`
	} `json:"fullConversationHeadersOnly"`
}

func cursorSubagent(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	return s != "" && s != "null"
}

type cursorBubble struct {
	Type int    `json:"type"`
	Text string `json:"text"`
	Tool *struct {
		Name    string          `json:"name"`
		RawArgs json.RawMessage `json:"rawArgs"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
	} `json:"toolFormerData"`
}

func cursorIDEMessages(db *sql.DB, ctx context.Context, id string, data cursorComposer) ([]Message, fileState, error) {
	bubbles := map[string]cursorBubble{}
	prefix := "bubbleId:" + id + ":"
	// 按主键区间查：LIKE 用不上 key 的索引，每个会话都要扫一遍整张表。
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM cursorDiskKV WHERE key >= ? AND key < ?`, prefix, "bubbleId:"+id+";")
	if err != nil {
		return nil, fileState{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fileState{}, err
		}
		var b cursorBubble
		if json.Unmarshal(value, &b) != nil {
			continue
		}
		bubbles[strings.TrimPrefix(key, prefix)] = b
	}
	if err := rows.Err(); err != nil {
		return nil, fileState{}, err
	}
	var st fileState
	st.Title = strings.TrimSpace(data.Name)
	st.Cwd = data.Workspace.URI.FsPath
	if st.Cwd == "" && len(data.Repos) > 0 {
		st.Cwd = data.Repos[0].RepoPath
	}
	st.Created = data.CreatedAt
	st.Updated = data.LastUpdatedAt
	if st.Updated == 0 {
		st.Updated = st.Created
	}
	var msgs []Message
	for _, h := range data.Headers {
		b, ok := bubbles[h.BubbleID]
		if !ok {
			continue
		}
		kind := h.Type
		if kind == 0 {
			kind = b.Type
		}
		role := "assistant"
		if kind == 1 {
			role = "user"
		} else if kind != 2 {
			continue
		}
		at := st.Updated
		if text := strings.TrimSpace(b.Text); text != "" {
			if role == "user" {
				text = userText(&st, text)
			}
			if text != "" {
				msgs = append(msgs, msg(role, text, at))
			}
		}
		if b.Tool != nil && b.Tool.Name != "" {
			input := b.Tool.RawArgs
			if len(input) == 0 || string(input) == "null" {
				input = b.Tool.Params
			}
			if t := toolCall(b.Tool.Name, unwrapJSON(input)); t != "" {
				msgs = append(msgs, msg("tool", t, at))
			}
			if t := cursorToolResult(b.Tool.Result); t != "" {
				msgs = append(msgs, msg("tool", t, at))
			}
		}
	}
	for i := range msgs {
		msgs[i].Seq = i
	}
	st.Seq = len(msgs)
	return msgs, st, nil
}

func cursorToolResult(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return clip(s, toolClip)
	}
	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Output != "" {
		return clip(obj.Output, toolClip)
	}
	return clip(string(raw), toolClip)
}
