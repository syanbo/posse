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
	Type      int    `json:"type"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
	Tool      *struct {
		Name    string          `json:"name"`
		RawArgs json.RawMessage `json:"rawArgs"`
		Params  json.RawMessage `json:"params"`
		Result  json.RawMessage `json:"result"`
	} `json:"toolFormerData"`
}

// cursorIDETurn 是对话里的一个气泡，按 fullConversationHeadersOnly 的顺序。
type cursorIDETurn struct {
	id     string // bubbleId
	role   string // user / assistant
	text   string
	at     int64 // 毫秒，气泡没记时间时为 0
	tool   string
	input  json.RawMessage
	result string
}

// cursorIDETurns 读一个对话的全部气泡。查询出错时返回错误，不交出残缺的结果。
func cursorIDETurns(ctx context.Context, db *sql.DB, id string, data cursorComposer) ([]cursorIDETurn, error) {
	bubbles := map[string]cursorBubble{}
	prefix := "bubbleId:" + id + ":"
	// 按主键区间查：LIKE 用不上 key 的索引，每个会话都要扫一遍整张表。
	rows, err := db.QueryContext(ctx, `SELECT key, value FROM cursorDiskKV WHERE key >= ? AND key < ?`, prefix, "bubbleId:"+id+";")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		var b cursorBubble
		if json.Unmarshal(value, &b) != nil {
			continue
		}
		bubbles[strings.TrimPrefix(key, prefix)] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []cursorIDETurn
	for _, h := range data.Headers {
		b, ok := bubbles[h.BubbleID]
		if !ok {
			continue
		}
		kind := h.Type
		if kind == 0 {
			kind = b.Type
		}
		t := cursorIDETurn{id: h.BubbleID, role: "assistant", text: strings.TrimSpace(b.Text), at: parseTime(b.CreatedAt)}
		if kind == 1 {
			t.role = "user"
			t.text = strings.TrimSpace(systemReminder.ReplaceAllString(t.text, ""))
		} else if kind != 2 {
			continue
		}
		if b.Tool != nil && b.Tool.Name != "" {
			t.tool = b.Tool.Name
			t.input = b.Tool.RawArgs
			if len(t.input) == 0 || string(t.input) == "null" {
				t.input = b.Tool.Params
			}
			t.input = unwrapJSON(t.input)
			t.result = cursorToolResult(b.Tool.Result)
		}
		if t.text == "" && t.tool == "" {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

func cursorIDEMessages(db *sql.DB, ctx context.Context, id string, data cursorComposer) ([]Message, fileState, error) {
	turns, err := cursorIDETurns(ctx, db, id, data)
	if err != nil {
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
	for _, t := range turns {
		at := t.at
		if at == 0 {
			at = st.Updated
		}
		if t.text != "" {
			text := t.text
			if t.role == "user" {
				text = userText(&st, text)
			}
			if text != "" {
				msgs = append(msgs, msg(t.role, text, at))
			}
		}
		if t.tool != "" {
			if c := toolCall(t.tool, t.input); c != "" {
				msgs = append(msgs, msg("tool", c, at))
			}
			if t.result != "" {
				msgs = append(msgs, msg("tool", t.result, at))
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
	raw = unwrapJSON(raw)
	if len(raw) == 0 {
		return ""
	}
	var obj struct {
		Output string `json:"output"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Output != "" {
		return clip(obj.Output, toolClip)
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return clip(s, toolClip)
	}
	return clip(string(raw), toolClip)
}

// IsCursorIDEChat 报告会话路径是不是 state.vscdb 里的 IDE 对话。这种会话没有自己的文件，
// cursor-agent 也恢复不了（--resume 只认 CLI 的会话）。
func IsCursorIDEChat(path string) bool {
	_, _, ok := cursorIDESource(path)
	return ok
}

// cursorIDESource 从索引里的 IDE 对话路径（globalStorage/composer/<ID>）取回库路径和对话 ID。
func cursorIDESource(path string) (db, id string, ok bool) {
	if path == "" {
		return "", "", false
	}
	dir := filepath.Dir(path)
	storage := filepath.Dir(dir)
	id = filepath.Base(path)
	if filepath.Base(dir) != "composer" || !strings.HasSuffix(storage, filepath.Join("Cursor", "User", "globalStorage")) || id == "" || id == "." {
		return "", "", false
	}
	return filepath.Join(storage, "state.vscdb"), id, true
}

// readCursorIDEChat 给手机读一个 IDE 对话。偏移是气泡在对话里的位置（每个气泡占两格：正文和工具调用一条，
// 工具结果一条），翻页前后一致。
func readCursorIDEChat(dbPath, id string, limit int, beforeOffset int64) (ChatPage, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return ChatPage{}, ErrChatNotFound
	}
	db, err := openCursorIDE(dbPath)
	if err != nil {
		return ChatPage{}, err
	}
	defer db.Close()
	ctx := context.Background()
	var value []byte
	if err := db.QueryRowContext(ctx, `SELECT value FROM cursorDiskKV WHERE key=?`, "composerData:"+id).Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ChatPage{}, ErrChatNotFound
		}
		return ChatPage{}, err
	}
	var data cursorComposer
	if json.Unmarshal(value, &data) != nil {
		return ChatPage{}, ErrChatNotFound
	}
	turns, err := cursorIDETurns(ctx, db, id, data)
	if err != nil {
		return ChatPage{}, err
	}
	var msgs []ChatMessage
	for i, t := range turns {
		off := int64(i+1) * 2
		var blocks []ChatBlock
		if t.text != "" {
			blocks = append(blocks, sanitizeBlock(ChatBlock{Type: "text", Text: t.text}))
		}
		if t.tool != "" && t.role == "assistant" {
			blocks = append(blocks, sanitizeBlock(ChatBlock{Type: "tool-call", Name: t.tool, CallID: t.id, Input: grokBlockInput(t.input)}))
		}
		if len(blocks) > 0 {
			msgs = append(msgs, ChatMessage{ID: "bubble-" + t.id, Role: t.role, Blocks: blocks, Timestamp: stamp(t.at), Source: "transcript", Offset: off})
		}
		if t.tool != "" && t.result != "" {
			msgs = append(msgs, ChatMessage{
				ID: "bubble-" + t.id + "-result", Role: "tool", Timestamp: stamp(t.at), Source: "transcript", Offset: off + 1,
				Blocks: []ChatBlock{sanitizeBlock(ChatBlock{Type: "tool-result", Output: t.result, CallID: t.id})},
			})
		}
	}
	if limit <= 0 {
		limit = 40
	}
	return window(msgs, limit, beforeOffset), nil
}
